package updatecontrol

import (
	"context"
	"net"
	"net/http"

	"github.com/rcss-labs/stackstead/internal/update"
	"github.com/rcss-labs/stackstead/internal/updateipc"
)

func Serve(ctx context.Context, l net.Listener, s *State) error {
	return updateipc.ServeAuthenticated(ctx, l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := updateipc.PeerUID(r.Context())
		if !ok || uid != 0 {
			updateipc.Reply(w, updateipc.Response{}, update.ErrUnauthorized)
			return
		}
		var q updateipc.Request
		if e := updateipc.Decode(w, r, &q); e != nil {
			updateipc.Reply(w, updateipc.Response{}, e)
			return
		}
		if q.InstanceID != s.Instance.ID {
			updateipc.Reply(w, updateipc.Response{}, update.ErrUnauthorized)
			return
		}
		if q.InstallationID != "" || q.Begin != nil || q.Operation != "prepare" && q.Probation != nil || q.Operation != "release" && q.JobID != "" {
			updateipc.Reply(w, updateipc.Response{}, update.ErrIncompatible)
			return
		}
		var v updateipc.Response
		var e error
		switch q.Operation {
		case "freeze":
			if s.Gate == nil {
				e = update.ErrUnavailable
			} else {
				e = s.Gate.Freeze()
				if e != nil {
					e = update.ErrBusy
				}
			}
		case "prepare":
			if q.Probation == nil {
				e = update.ErrIncompatible
			} else {
				e = s.Apply(*q.Probation)
			}
		case "ready":
			x := s.Ready()
			v.Readiness = &x
		case "release":
			e = s.Release(q.JobID)
		default:
			e = update.ErrIncompatible
		}
		updateipc.Reply(w, v, e)
	}))
}
