package updatehost

import (
	"context"
	"errors"
	"github.com/rcss-labs/stackstead/internal/update"
	"os/exec"
	"reflect"
	"testing"
)

type darwinRunner struct {
	calls []commandCall
	run   func(commandCall) ([]byte, []byte, error)
}

func (r *darwinRunner) Run(_ context.Context, u, g uint32, p string, a []string, b []byte) ([]byte, []byte, error) {
	c := commandCall{u, g, p, a, b}
	r.calls = append(r.calls, c)
	if r.run != nil {
		return r.run(c)
	}
	return nil, nil, nil
}
func darwinInstance() update.Instance {
	return update.Instance{ID: "one", UID: 501, GID: 20, Supervisor: update.SupervisorSpec{Kind: "launchd", Name: "io.github.rcss-labs.stackstead"}}
}
func commandFailure() error { return exec.Command("/usr/bin/false").Run() }
func TestLaunchdPreservesUnloadedAgents(t *testing.T) {
	in := darwinInstance()
	for _, state := range []update.ServiceState{{InstanceID: in.ID}, {InstanceID: in.ID, Loaded: true}, {InstanceID: in.ID, Loaded: true, Masked: true, Running: true}} {
		r := &darwinRunner{}
		if e := NewLaunchd(r).Start(context.Background(), in, state); e != nil {
			t.Fatal(e)
		}
		if len(r.calls) != 0 {
			t.Fatalf("inactive agent changed: %+v", r.calls)
		}
	}
	r := &darwinRunner{run: func(c commandCall) ([]byte, []byte, error) {
		if c.args[0] == "print-disabled" {
			return []byte("disabled services = {\n \"io.github.rcss-labs.stackstead\" => false\n}\n"), nil, nil
		}
		return []byte("gui/501/io.github.rcss-labs.stackstead = {\n state = running\n pid = 123\n}\n"), nil, nil
	}}
	st, e := NewLaunchd(r).Probe(context.Background(), in)
	if e != nil || !st.Running || !st.Loaded || !st.Enabled || st.PID != 123 {
		t.Fatalf("running probe: %+v %v", st, e)
	}
	r.calls = nil
	if e = NewLaunchd(r).Stop(context.Background(), in); e != nil {
		t.Fatal(e)
	}
	c := r.calls[0]
	if c.uid != 501 || c.gid != 20 || !reflect.DeepEqual(c.args, []string{"bootout", "gui/501/io.github.rcss-labs.stackstead"}) {
		t.Fatalf("wrong stop: %+v", c)
	}
}
func TestNoGuiSessionDoesNotSelectConsoleUser(t *testing.T) {
	r := &darwinRunner{run: func(c commandCall) ([]byte, []byte, error) {
		return nil, []byte("Could not find domain for"), commandFailure()
	}}
	st, e := NewLaunchd(r).Probe(context.Background(), darwinInstance())
	if e != nil || st.Loaded || st.Running {
		t.Fatalf("no GUI: %+v %v", st, e)
	}
	for _, c := range r.calls {
		if c.uid != 501 || c.gid != 20 || c.path != "/bin/launchctl" {
			t.Fatalf("wrong registered identity: %+v", c)
		}
	}
}
func TestLaunchdRejectsAmbiguousProbe(t *testing.T) {
	r := &darwinRunner{run: func(c commandCall) ([]byte, []byte, error) { return []byte("unrecognized state"), nil, nil }}
	if _, e := NewLaunchd(r).Probe(context.Background(), darwinInstance()); !errors.Is(e, update.ErrSetupRequired) {
		t.Fatalf("ambiguous probe accepted: %v", e)
	}
	in := darwinInstance()
	in.Supervisor.Name = "../../other"
	r.calls = nil
	if e := NewLaunchd(r).Stop(context.Background(), in); e == nil || len(r.calls) != 0 {
		t.Fatal("unsafe label accepted")
	}
}

func TestLaunchdDisabledRunningBlocksBeforeStop(t *testing.T) {
	r := &darwinRunner{run: func(c commandCall) ([]byte, []byte, error) {
		if c.args[0] == "print-disabled" {
			return []byte("disabled services = {\n \"io.github.rcss-labs.stackstead\" => true\n}\n"), nil, nil
		}
		return []byte("service = {\n state = running\n pid = 123\n}\n"), nil, nil
	}}
	if _, e := NewLaunchd(r).Probe(context.Background(), darwinInstance()); !errors.Is(e, update.ErrSetupRequired) {
		t.Fatalf("unrestorable disabled agent accepted: %v", e)
	}
	for _, c := range r.calls {
		if c.args[0] != "print" && c.args[0] != "print-disabled" {
			t.Fatal("mutated before failed preflight")
		}
	}
}

func TestLaunchdResumesPartialBootstrapWithoutReload(t *testing.T) {
	in := darwinInstance()
	r := &darwinRunner{run: func(c commandCall) ([]byte, []byte, error) {
		if c.args[0] == "print" {
			return []byte("service = {\n state = not running\n}\n"), nil, nil
		}
		if c.args[0] == "print-disabled" {
			return []byte("disabled services = {\n}\n"), nil, nil
		}
		return nil, nil, nil
	}}
	s := NewLaunchd(r).(*launchdSupervisor)
	s.home = func(uint32) (string, error) { return "/Users/registered", nil }
	e := s.Start(context.Background(), in, update.ServiceState{InstanceID: in.ID, Running: true, Loaded: true, Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range r.calls {
		if c.args[0] == "bootstrap" {
			t.Fatal("reloaded an already loaded job after interrupted start")
		}
		if c.uid != in.UID || c.gid != in.GID {
			t.Fatal("agent operation used wrong credentials")
		}
	}
	if len(r.calls) == 0 || r.calls[len(r.calls)-1].args[0] != "kickstart" {
		t.Fatal("missing resumed start")
	}
}

func TestLaunchdRestartUsesRegisteredHomeAndCredentials(t *testing.T) {
	in := darwinInstance()
	r := &darwinRunner{run: func(c commandCall) ([]byte, []byte, error) {
		if c.args[0] == "print" {
			return nil, []byte("Could not find service registered"), commandFailure()
		}
		return nil, nil, nil
	}}
	s := NewLaunchd(r).(*launchdSupervisor)
	s.home = func(uid uint32) (string, error) {
		if uid != in.UID {
			t.Fatal("looked up another user")
		}
		return "/Users/registered", nil
	}
	if e := s.Start(context.Background(), in, update.ServiceState{InstanceID: in.ID, Running: true, Loaded: true, Enabled: true}); e != nil {
		t.Fatal(e)
	}
	if len(r.calls) != 3 || !reflect.DeepEqual(r.calls[1].args, []string{"bootstrap", "gui/501", "/Users/registered/Library/LaunchAgents/io.github.rcss-labs.stackstead.plist"}) {
		t.Fatalf("wrong restart commands: %+v", r.calls)
	}
	for _, c := range r.calls {
		if c.uid != in.UID || c.gid != in.GID || c.path != "/bin/launchctl" {
			t.Fatalf("unsafe user-home access: %+v", c)
		}
	}
}
