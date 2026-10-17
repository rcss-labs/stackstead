package updatecontrol

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/rcss-labs/stackstead/internal/maintenance"
	"github.com/rcss-labs/stackstead/internal/update"
	"github.com/rcss-labs/stackstead/internal/updateipc"
)

func TestControlRejectsOldBootNonce(t *testing.T) {
	g := maintenance.New()
	g.Freeze()
	p := update.Probation{JobID: "new", Nonce: "new-boot", Expected: update.Identity{Version: "2"}}
	s := NewState(update.Instance{ID: "app"}, g, &p)
	s.MarkReady(update.Readiness{InstanceID: "app", Identity: p.Expected, Ready: true, PID: 123})
	if s.Ready().Ready {
		t.Error("PID and health alone satisfied readiness without ticket binding")
	}
	if e := s.Apply(update.Probation{JobID: "old", Nonce: "old-boot", Expected: p.Expected}); !errors.Is(e, update.ErrUnauthorized) {
		t.Errorf("old ticket accepted: %v", e)
	}
	if e := s.Release("old"); !errors.Is(e, update.ErrUnauthorized) {
		t.Errorf("old job released current boot: %v", e)
	}
	if !g.Frozen() {
		t.Error("old job thawed gate")
	}
}

func TestControlAuthenticatesRootPeer(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("non-root peer rejection requires non-root runner")
	}
	dir, e := os.MkdirTemp("/tmp", "ss-ctl-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "c.sock")
	l, e := net.Listen("unix", p)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := maintenance.New()
	s := NewState(update.Instance{ID: "app"}, g, nil)
	go Serve(ctx, l, s)
	_, e = updateipc.Call(ctx, p, uint32(os.Getuid()), updateipc.Request{Operation: "freeze", InstanceID: "app"})
	if !errors.Is(e, update.ErrUnauthorized) {
		t.Fatalf("non-root helper accepted: %v", e)
	}
	if g.Frozen() {
		t.Fatal("unauthorized freeze changed gate")
	}
}
func TestControlReadyBindsCurrentBoot(t *testing.T) {
	g := maintenance.New()
	g.Freeze()
	p := update.Probation{JobID: "job", Nonce: "nonce", Expected: update.Identity{Version: "2"}}
	s := NewState(update.Instance{ID: "app"}, g, &p)
	s.MarkReady(update.Readiness{Identity: p.Expected, Ready: true, PID: 1, ConfigSHA256: "hash", Schema: 3})
	r := s.Ready()
	if !r.Ready || r.JobID != p.JobID || r.Nonce != p.Nonce || r.InstanceID != "app" {
		t.Fatalf("not exact boot: %+v", r)
	}
	if e := s.Release("job"); e != nil || g.Frozen() {
		t.Fatalf("release: %v", e)
	}
	if s.Ready().Ready {
		t.Fatal("committed ticket remains ready")
	}
}

func TestControlListenerRejectsUnsafeParent(t *testing.T) {
	dir, e := os.MkdirTemp("/tmp", "ss-bind-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	dir, _ = filepath.EvalSymlinks(dir)
	in := update.Instance{ID: "app", UID: uint32(os.Getuid()), ControlSocket: filepath.Join(dir, "app", "control.sock")}
	if e = os.Mkdir(filepath.Join(dir, "app"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(dir, 0777); e != nil {
		t.Fatal(e)
	}
	if l, e := listenControl(in, dir, uint32(os.Getuid())); e == nil {
		l.Close()
		t.Fatal("writable runtime parent accepted")
	}
	if e = os.Chmod(dir, 0755); e != nil {
		t.Fatal(e)
	}
	l, e := listenControl(in, dir, uint32(os.Getuid()))
	if e != nil {
		t.Fatalf("safe app-owned leaf rejected: %v", e)
	}
	l.Close()
	in.ControlSocket = filepath.Join(dir, "other.sock")
	if l, e = listenControl(in, dir, uint32(os.Getuid())); e == nil {
		l.Close()
		t.Fatal("outside instance leaf accepted")
	}
}

func TestControlListenerRestartsAfterStaleSocket(t *testing.T) {
	dir, e := os.MkdirTemp("/tmp", "ss-stale-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	dir, _ = filepath.EvalSymlinks(dir)
	os.Chmod(dir, 0755)
	os.Mkdir(filepath.Join(dir, "app"), 0700)
	in := update.Instance{ID: "app", UID: uint32(os.Getuid()), ControlSocket: filepath.Join(dir, "app", "control.sock")}
	l, e := net.ListenUnix("unix", &net.UnixAddr{Name: in.ControlSocket, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	l.SetUnlinkOnClose(false)
	l.Close()
	next, e := listenControl(in, dir, uint32(os.Getuid()))
	if e != nil {
		t.Fatalf("stale socket blocked new boot: %v", e)
	}
	next.Close()
}

func TestControlPrepareUsesActualIdentity(t *testing.T) {
	g := maintenance.New()
	g.Freeze()
	s := NewState(update.Instance{ID: "app"}, g, nil)
	actual := update.Identity{Version: "1"}
	s.MarkReady(update.Readiness{Identity: actual, Ready: true, PID: 1, ConfigSHA256: "hash", Schema: 3})
	p := update.Probation{JobID: "job", Nonce: "boot", Expected: update.Identity{Version: "2"}}
	if e := s.Apply(p); e != nil {
		t.Fatalf("old app cannot apply target ticket: %v", e)
	}
	r := s.Ready()
	if r.Ready || r.Identity != actual {
		t.Fatalf("expected identity masqueraded as actual: %+v", r)
	}
}
