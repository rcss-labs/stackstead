package updatehost

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/rcss-labs/stackstead/internal/update"
)

type commandCall struct {
	uid, gid uint32
	path     string
	args     []string
	stdin    []byte
}
type recordingRunner struct {
	calls  []commandCall
	output []byte
	err    error
}

func (r *recordingRunner) Run(_ context.Context, uid, gid uint32, path string, args []string, stdin []byte) ([]byte, []byte, error) {
	r.calls = append(r.calls, commandCall{uid, gid, path, args, stdin})
	return r.output, nil, r.err
}
func TestSystemdPreservesMaskedDisabledState(t *testing.T) {
	in := update.Instance{ID: "one", Supervisor: update.SupervisorSpec{Kind: "systemd", Name: "stackstead.service"}}
	for _, tc := range []struct {
		name, unit              string
		running, masked, loaded bool
		starts                  int
	}{
		{"disabled running", "disabled", true, false, true, 1}, {"masked", "masked", true, true, true, 0}, {"stopped", "disabled", false, false, true, 0}, {"unloaded", "disabled", false, false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &recordingRunner{}
			s := NewSystemd(r)
			state := update.ServiceState{InstanceID: in.ID, Running: tc.running, Enabled: false, Masked: tc.masked, Loaded: tc.loaded}
			if err := s.Start(context.Background(), in, state); err != nil {
				t.Fatal(err)
			}
			if len(r.calls) != tc.starts {
				t.Fatalf("start count = %d", len(r.calls))
			}
			if tc.starts > 0 && (!reflect.DeepEqual(r.calls[0].args, []string{"--system", "--no-ask-password", "start", "--", "stackstead.service"}) || r.calls[0].uid != 0) {
				t.Fatalf("unsafe mutation: %+v", r.calls)
			}
		})
	}
	r := &recordingRunner{output: []byte("LoadState=loaded\nActiveState=active\nUnitFileState=disabled\nMainPID=123\n")}
	state, err := NewSystemd(r).Probe(context.Background(), in)
	if err != nil || !state.Running || state.Enabled || !state.Loaded || state.PID != 123 {
		t.Fatalf("probe: %+v %v", state, err)
	}
	in.Supervisor.Name = "--user"
	r.calls = nil
	if err := NewSystemd(r).Stop(context.Background(), in); err == nil || len(r.calls) != 0 {
		t.Fatal("untrusted unit reached runner")
	}
}
func TestCustomSupervisorRequiresTrustedRegistration(t *testing.T) {
	r := &recordingRunner{output: []byte(`{"instanceId":"one","running":true,"enabled":false,"masked":false,"loaded":true,"pid":9}`)}
	in := update.Instance{ID: "one", Supervisor: update.SupervisorSpec{Kind: "custom"}}
	if _, err := NewCustom(r).Probe(context.Background(), in); !errors.Is(err, update.ErrSetupRequired) {
		t.Fatalf("missing adapter: %v", err)
	}
	in.Supervisor.AdapterPath = "/tmp/untrusted-adapter"
	if err := NewCustom(r).Stop(context.Background(), in); !errors.Is(err, update.ErrSetupRequired) || len(r.calls) != 0 {
		t.Fatal("untrusted custom adapter executed")
	}
	s := &customSupervisor{runner: r, trust: func(string) error { return nil }}
	in.Supervisor.AdapterPath = "/root-registered/adapter"
	state, err := s.Probe(context.Background(), in)
	if err != nil || state.PID != 9 {
		t.Fatalf("probe: %+v %v", state, err)
	}
	if !reflect.DeepEqual(r.calls[0].args, []string{"probe", "one"}) {
		t.Fatalf("caller argv: %+v", r.calls)
	}
	if err = s.Start(context.Background(), in, update.ServiceState{InstanceID: "one", Running: false}); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 {
		t.Fatal("started stopped instance")
	}
	r.output = []byte(`{"instanceId":"other","running":true,"pid":9}`)
	if _, err = s.Probe(context.Background(), in); err == nil {
		t.Fatal("wrong peer identity accepted")
	}
}
func TestCommandRunnerRejectsRootAppAndUnboundedOutput(t *testing.T) {
	if _, _, err := NewCommandRunner().Run(context.Background(), 0, 0, "/tmp/stackstead", []string{"version"}, nil); !errors.Is(err, update.ErrUnauthorized) {
		t.Fatalf("root app: %v", err)
	}
	b := &boundedOutput{limit: 4}
	_, _ = b.Write([]byte("hello"))
	if string(b.data) != "hell" || !b.exceeded {
		t.Fatalf("unbounded buffer: %+v", b)
	}
	if got := sanitizeOutput([]byte("ok\x00\x1b\n")); string(got) != "ok\n" {
		t.Fatalf("unsanitized: %q", got)
	}
}

func TestCustomRejectsIncompleteAndAmbiguousProbe(t *testing.T) {
	in := update.Instance{ID: "one", Supervisor: update.SupervisorSpec{Kind: "custom", AdapterPath: "/registered/adapter"}}
	for _, out := range []string{`{"instanceId":"one"}`, `{"instanceId":"one","running":false,"enabled":false,"masked":false,"loaded":true,"pid":0} {}`, `{"instanceId":"one","running":true,"enabled":false,"masked":false,"loaded":false,"pid":9}`} {
		r := &recordingRunner{output: []byte(out)}
		s := &customSupervisor{runner: r, trust: func(string) error { return nil }}
		if _, err := s.Probe(context.Background(), in); err == nil {
			t.Fatalf("ambiguous state accepted: %s", out)
		}
	}
}

func TestCommandRunnerRootAllowlist(t *testing.T) {
	r := commandRunner{custom: map[string]bool{"/registered/adapter": true}}
	for _, tc := range []struct {
		path string
		args []string
		ok   bool
	}{
		{"/usr/bin/systemctl", []string{"--system", "--no-ask-password", "start", "--", "stackstead.service"}, true},
		{"/usr/bin/systemctl", []string{"enable", "stackstead.service"}, false},
		{"/usr/bin/systemctl", []string{"--system", "--no-ask-password", "start", "--", "--user"}, false},
		{"/registered/adapter", []string{"probe", "main"}, true},
		{"/registered/adapter", []string{"probe", "main", "extra"}, false},
		{"/registered/adapter", []string{"exec", "main"}, false},
		{"/usr/bin/docker", []string{"run", "--privileged"}, false},
		{"/bin/sh", []string{"-c", "id"}, false},
	} {
		if got := r.allowedRoot(tc.path, tc.args); got != tc.ok {
			t.Fatalf("allowlist %s %v = %v", tc.path, tc.args, got)
		}
	}
}
