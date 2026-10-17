// Package updateipc carries bounded, peer-authenticated local updater requests.
package updateipc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/rcss-labs/stackstead/internal/update"
)

const MaxRequest = 1 << 20
const Deadline = 10 * time.Second

type Backend interface {
	Startup(context.Context, string, string) (*update.Probation, error)
	Status(context.Context, string) (update.UpdateStatus, error)
	Check(context.Context, string) (update.UpdateStatus, error)
	Begin(context.Context, string, update.BeginRequest) (update.JobView, error)
	Job(context.Context, string, string) (update.JobView, error)
	Cancel(context.Context, string, string) (update.JobView, error)
}
type Request struct {
	Operation      string               `json:"operation"`
	InstallationID string               `json:"installationID,omitempty"`
	InstanceID     string               `json:"instanceID,omitempty"`
	JobID          string               `json:"jobID,omitempty"`
	Begin          *update.BeginRequest `json:"begin,omitempty"`
	Probation      *update.Probation    `json:"probation,omitempty"`
}
type Response struct {
	Error     string               `json:"error,omitempty"`
	Probation *update.Probation    `json:"probation,omitempty"`
	Status    *update.UpdateStatus `json:"status,omitempty"`
	Job       *update.JobView      `json:"job,omitempty"`
	Readiness *update.Readiness    `json:"readiness,omitempty"`
}
type peerKey struct{}

func PeerUID(ctx context.Context) (uint32, bool) {
	v, ok := ctx.Value(peerKey{}).(uint32)
	return v, ok
}

// ServeAuthenticated obtains credentials from each connected Unix socket. Filesystem
// permissions alone never establish the identity of either peer.
func ServeAuthenticated(ctx context.Context, l net.Listener, h http.Handler) error {
	bounded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), Deadline)
		defer cancel()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
	s := &http.Server{Handler: bounded, ReadHeaderTimeout: Deadline, ReadTimeout: Deadline, WriteTimeout: Deadline, IdleTimeout: Deadline, MaxHeaderBytes: 8192,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			uid, err := SocketUID(c)
			if err != nil {
				c.Close()
				return ctx
			}
			return context.WithValue(ctx, peerKey{}, uid)
		}}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-done:
		}
	}()
	err := s.Serve(l)
	if errors.Is(err, http.ErrServerClosed) || ctx.Err() != nil {
		return nil
	}
	return err
}
func Decode(w http.ResponseWriter, r *http.Request, q *Request) error {
	if r.Method != http.MethodPost || r.URL.Path != "/rpc" {
		return update.ErrIncompatible
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequest)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(q); err != nil {
		return update.ErrIncompatible
	}
	if err := d.Decode(new(struct{})); err != io.EOF {
		return update.ErrIncompatible
	}
	return nil
}
func Reply(w http.ResponseWriter, v Response, err error) {
	if err != nil {
		v = Response{Error: ErrorCode(err)}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func ErrorCode(err error) string {
	for _, e := range []error{update.ErrUnauthorized, update.ErrDisabled, update.ErrBusy, update.ErrIncompatible, update.ErrCandidateExpired, update.ErrSetupRequired, update.ErrRecoveryRequired} {
		if errors.Is(err, e) {
			return e.Error()
		}
	}
	return update.ErrUnavailable.Error()
}
func DecodeError(code string) error {
	if code == "" {
		return nil
	}
	for _, e := range []error{update.ErrUnauthorized, update.ErrDisabled, update.ErrBusy, update.ErrIncompatible, update.ErrCandidateExpired, update.ErrSetupRequired, update.ErrRecoveryRequired} {
		if code == e.Error() {
			return e
		}
	}
	return update.ErrUnavailable
}

func Serve(ctx context.Context, l net.Listener, registrations []update.Registration, b Backend) error {
	return ServeAuthenticated(ctx, l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q Request
		if err := Decode(w, r, &q); err != nil {
			Reply(w, Response{}, err)
			return
		}
		uid, ok := PeerUID(r.Context())
		var reg *update.Registration
		for i := range registrations {
			if registrations[i].ID == q.InstallationID {
				reg = &registrations[i]
				break
			}
		}
		authorized := false
		if ok && reg != nil {
			for _, in := range reg.Instances {
				if in.UID == uid && (q.Operation != "startup" || in.ID == q.InstanceID) {
					authorized = true
				}
			}
		}
		if !authorized {
			Reply(w, Response{}, update.ErrUnauthorized)
			return
		}
		if !reg.UpdateEnabled {
			Reply(w, Response{}, update.ErrDisabled)
			return
		}
		if q.Probation != nil || q.Operation != "startup" && q.InstanceID != "" || q.Operation != "begin" && q.Begin != nil || q.Operation != "job" && q.Operation != "cancel" && q.JobID != "" {
			Reply(w, Response{}, update.ErrIncompatible)
			return
		}
		var v Response
		var err error
		switch q.Operation {
		case "startup":
			v.Probation, err = b.Startup(r.Context(), q.InstallationID, q.InstanceID)
		case "status":
			var x update.UpdateStatus
			x, err = b.Status(r.Context(), q.InstallationID)
			v.Status = &x
		case "check":
			var x update.UpdateStatus
			x, err = b.Check(r.Context(), q.InstallationID)
			v.Status = &x
		case "begin":
			if q.Begin == nil {
				err = update.ErrIncompatible
				break
			}
			var x update.JobView
			x, err = b.Begin(r.Context(), q.InstallationID, *q.Begin)
			v.Job = &x
		case "job", "cancel":
			var x update.JobView
			if q.Operation == "job" {
				x, err = b.Job(r.Context(), q.InstallationID, q.JobID)
			} else {
				x, err = b.Cancel(r.Context(), q.InstallationID, q.JobID)
			}
			v.Job = &x
		default:
			err = update.ErrIncompatible
		}
		Reply(w, v, err)
	}))
}
