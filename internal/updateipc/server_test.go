package updateipc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/update"
)

type fakeBackend struct {
	ticket update.Probation
	calls  int
}

func (b *fakeBackend) Startup(context.Context, string, string) (*update.Probation, error) {
	b.calls++
	p := b.ticket
	return &p, nil
}
func (b *fakeBackend) Status(context.Context, string) (update.UpdateStatus, error) {
	b.calls++
	return update.UpdateStatus{}, nil
}
func (b *fakeBackend) Check(context.Context, string) (update.UpdateStatus, error) {
	return update.UpdateStatus{}, nil
}
func (b *fakeBackend) Begin(context.Context, string, update.BeginRequest) (update.JobView, error) {
	return update.JobView{}, nil
}
func (b *fakeBackend) Job(context.Context, string, string) (update.JobView, error) {
	return update.JobView{}, nil
}
func (b *fakeBackend) Cancel(context.Context, string, string) (update.JobView, error) {
	return update.JobView{}, nil
}
func helper(t *testing.T, b *fakeBackend) (string, uint32) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ss-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	p := filepath.Join(dir, "h.sock")
	l, e := net.Listen("unix", p)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	uid := uint32(os.Getuid())
	go Serve(ctx, l, []update.Registration{{ID: "one", UpdateEnabled: true, Instances: []update.Instance{{ID: "app", UID: uid}}}, {ID: "other", UpdateEnabled: true, Instances: []update.Instance{{ID: "other", UID: uid + 1}}}}, b)
	return p, uid
}
func TestPeerCannotOperateAnotherInstance(t *testing.T) {
	b := &fakeBackend{}
	p, uid := helper(t, b)
	for _, q := range []Request{{Operation: "status", InstallationID: "other"}, {Operation: "startup", InstallationID: "one", InstanceID: "other"}} {
		_, e := Call(context.Background(), p, uid, q)
		if !errors.Is(e, update.ErrUnauthorized) {
			t.Fatalf("unauthorized peer accepted: %v", e)
		}
	}
	if b.calls != 0 {
		t.Fatal("unauthorized backend call")
	}
	_, e := Call(context.Background(), p, uid+1, Request{Operation: "status", InstallationID: "one"})
	if !errors.Is(e, update.ErrUnauthorized) {
		t.Fatalf("server UID impersonation: %v", e)
	}
}
func TestStartupRestoresProbationTicket(t *testing.T) {
	b := &fakeBackend{ticket: update.Probation{JobID: "persisted-job", Nonce: "persisted-boot", Expected: update.Identity{Version: "2", Commit: strings.Repeat("a", 40)}}}
	p, uid := helper(t, b)
	for range 2 {
		v, e := Call(context.Background(), p, uid, Request{Operation: "startup", InstallationID: "one", InstanceID: "app"})
		if e != nil || v.Probation == nil || *v.Probation != b.ticket {
			t.Fatalf("ticket changed: %+v %v", v, e)
		}
	}
}
func TestIPCRejectsUnknownFieldsAndOversizedRequest(t *testing.T) {
	b := &fakeBackend{}
	p, _ := helper(t, b)
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p)
	}}
	defer tr.CloseIdleConnections()
	for _, body := range []string{`{"operation":"status","installationID":"one","path":"/etc/passwd"}`, `{"operation":"status","installationID":"one","command":"sh"}`, `{"operation":"status","installationID":"one"} {}`, `{"operation":"status","installationID":"one","jobID":"` + strings.Repeat("a", MaxRequest) + `"}`} {
		r, e := (&http.Client{Transport: tr}).Post("http://local/rpc", "application/json", bytes.NewBufferString(body))
		if e != nil {
			t.Fatal(e)
		}
		got, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if !strings.Contains(string(got), "incompatible") {
			t.Errorf("unsafe request accepted: %.160s", got)
		}
	}
	if b.calls != 0 {
		t.Fatal("invalid input reached backend")
	}
}
