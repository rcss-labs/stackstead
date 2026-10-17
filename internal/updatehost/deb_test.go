package updatehost

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/update"
)

type debRunner struct {
	calls                  []commandCall
	metadata, state, owner string
	dryError               error
	executable             string
}

func (r *debRunner) Run(_ context.Context, uid, gid uint32, path string, args []string, stdin []byte) ([]byte, []byte, error) {
	r.calls = append(r.calls, commandCall{uid, gid, path, append([]string(nil), args...), stdin})
	switch path {
	case "/usr/bin/stackstead":
		return []byte(r.executable), nil, nil
	case "/usr/bin/dpkg-deb":
		return []byte(r.metadata), nil, nil
	case "/usr/bin/dpkg-query":
		if args[0] == "-S" {
			return []byte(r.owner), nil, nil
		}
		return []byte(r.state), nil, nil
	case "/usr/bin/dpkg":
		if args[0] == "--dry-run" {
			return nil, nil, r.dryError
		}
		r.state = "stackstead\tamd64\t" + strings.Fields(r.metadata)[2] + "\tinstall ok installed\n"
		return nil, nil, nil
	}
	return nil, nil, errors.New("unexpected command")
}
func debFixture() (*debAdapter, *debRunner, update.Plan) {
	a := update.VerifiedArtifact{Name: "stackstead.deb", Path: "/var/lib/stackstead-updater/staging/job/target.deb", SHA256: strings.Repeat("a", 64), Size: 12}
	previous := a
	previous.Path = "/var/lib/stackstead-updater/staging/job/previous.deb"
	current := update.Identity{Version: "1.2.3", Commit: "old", OS: "linux", Arch: "amd64"}
	target := current
	target.Version = "1.2.4"
	target.Commit = "new"
	p := update.Plan{Registration: update.Registration{Installer: update.InstallerDeb, Prefix: "/usr", Binary: "/usr/bin/stackstead", Identity: current, Instances: []update.Instance{{ID: "one", UID: 123, GID: 456}}}, Current: update.Candidate{Identity: current}, Target: update.Candidate{Identity: target}, TargetArtifact: a, PreviousArtifact: previous}
	r := &debRunner{metadata: "stackstead\tamd64\t1.2.4-1\n", state: "stackstead\tamd64\t1.2.3-1\tinstall ok installed\n", owner: "stackstead: /usr/bin/stackstead\n"}
	d := NewDeb(r).(*debAdapter)
	d.verify = func(update.VerifiedArtifact) error { return nil }
	d.locks = func() error { return nil }
	d.managed = func(context.Context, update.Plan, update.VerifiedArtifact) error { return nil }
	return d, r, p
}
func TestDebUsesExactVerifiedPackage(t *testing.T) {
	for _, metadata := range []string{"stackstead\tamd64\t1.2.4-1\n", "other\tamd64\t1.2.4-1\n", "stackstead\tarm64\t1.2.4-1\n", "stackstead\tamd64\t1.2.4-2\n"} {
		t.Run(strings.ReplaceAll(metadata, "\n", "_"), func(t *testing.T) {
			d, r, p := debFixture()
			r.metadata = metadata
			err := d.Install(context.Background(), p, p.TargetArtifact)
			good := metadata == "stackstead\tamd64\t1.2.4-1\n"
			if good && err != nil {
				t.Fatalf("valid verified package refused: %v", err)
			}
			if !good && !errors.Is(err, update.ErrIncompatible) {
				t.Fatalf("mismatched metadata: %v", err)
			}
			mutations := 0
			for _, call := range r.calls {
				if call.path == "/usr/bin/dpkg" {
					mutations++
					if !good {
						t.Fatal("mismatch reached dpkg")
					}
				}
			}
			if good && mutations != 2 {
				t.Fatalf("expected dry run then install: %+v", r.calls)
			}
		})
	}
	d, r, p := debFixture()
	a := p.TargetArtifact
	a.Path = "/tmp/other.deb"
	if err := d.Install(context.Background(), p, a); !errors.Is(err, update.ErrUnauthorized) || len(r.calls) != 0 {
		t.Fatalf("unrecorded package: %v %+v", err, r.calls)
	}
	d, r, p = debFixture()
	d.verify = func(update.VerifiedArtifact) error { return update.ErrIncompatible }
	if err := d.Install(context.Background(), p, p.TargetArtifact); !errors.Is(err, update.ErrIncompatible) || len(r.calls) != 0 {
		t.Fatal("artifact failed trust check but commands ran")
	}
}
func TestDebBusyDoesNotStopService(t *testing.T) {
	d, r, p := debFixture()
	d.locks = func() error { return update.ErrBusy }
	if err := d.Validate(context.Background(), p, p.TargetArtifact); !errors.Is(err, update.ErrBusy) || len(r.calls) != 0 {
		t.Fatalf("busy preflight: %v %+v", err, r.calls)
	}
	d, r, p = debFixture()
	r.dryError = errors.New("native lock conflict")
	if err := d.Install(context.Background(), p, p.TargetArtifact); err == nil {
		t.Fatal("dry-run failure ignored")
	}
	for _, call := range r.calls {
		if call.path == "/usr/bin/systemctl" || call.path == "/usr/bin/dpkg" && call.args[0] != "--dry-run" {
			t.Fatalf("mutation after refusal: %+v", call)
		}
	}
}
func TestDebRollbackRestoresPackageBookkeeping(t *testing.T) {
	d, r, p := debFixture()
	r.metadata = "stackstead\tamd64\t1.2.3-1\n"
	r.state = "stackstead\tamd64\t1.2.4-1\tinstall ok installed\n"
	if err := d.Install(context.Background(), p, p.PreviousArtifact); err != nil {
		t.Fatal(err)
	}
	var commands [][]string
	for _, call := range r.calls {
		if call.path == "/usr/bin/dpkg" {
			commands = append(commands, call.args)
		}
		if call.uid != 0 || call.gid != 0 {
			t.Fatal("wrong package tool credentials")
		}
	}
	want := [][]string{{"--dry-run", "--force-confdef", "--force-confold", "--install", p.PreviousArtifact.Path}, {"--force-confdef", "--force-confold", "--install", p.PreviousArtifact.Path}}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("rollback did not reinstall recorded package: %v", commands)
	}
	if !strings.Contains(r.state, "1.2.3-1") {
		t.Fatal("package bookkeeping not restored")
	}
}
func TestDebPartialStateRequiresBoundedRepair(t *testing.T) {
	for _, state := range []string{"install ok unpacked", "install reinstreq half-installed", "hold ok installed"} {
		d, r, p := debFixture()
		r.state = "stackstead\tamd64\t1.2.3-1\t" + state + "\n"
		if err := d.Validate(context.Background(), p, p.TargetArtifact); !errors.Is(err, update.ErrRecoveryRequired) {
			t.Fatalf("partial state %s: %v", state, err)
		}
		for _, call := range r.calls {
			if call.path == "/usr/bin/dpkg" {
				t.Fatal("unbounded native repair attempted")
			}
		}
	}
}
func TestDebManagedContextRequiredOnlyForMutation(t *testing.T) {
	d, r, p := debFixture()
	d.managed = nil
	if err := d.Validate(context.Background(), p, p.TargetArtifact); err != nil {
		t.Fatalf("read-only validation needs active job: %v", err)
	}
	r.calls = nil
	if err := d.Install(context.Background(), p, p.TargetArtifact); !errors.Is(err, update.ErrUnavailable) || len(r.calls) != 0 {
		t.Fatalf("missing managed context: %v %+v", err, r.calls)
	}
	d, r, p = debFixture()
	d.managed = func(context.Context, update.Plan, update.VerifiedArtifact) error { return update.ErrUnauthorized }
	if err := d.Install(context.Background(), p, p.TargetArtifact); !errors.Is(err, update.ErrUnauthorized) || len(r.calls) != 0 {
		t.Fatal("bad context reached native tools")
	}
}
func TestDebOwnershipAndCurrentVersionMustMatch(t *testing.T) {
	for _, change := range []func(*debRunner){func(r *debRunner) { r.owner = "other: /usr/bin/stackstead\n" }, func(r *debRunner) { r.state = "stackstead\tarm64\t1.2.3-1\tinstall ok installed\n" }, func(r *debRunner) { r.state = "stackstead\tamd64\t9.9.9-1\tinstall ok installed\n" }} {
		d, r, p := debFixture()
		change(r)
		if err := d.Validate(context.Background(), p, p.TargetArtifact); err == nil {
			t.Fatal("unrelated native package accepted")
		}
	}
}
func TestDebCommandRunnerAllowlist(t *testing.T) {
	r := commandRunner{}
	p := "/var/lib/stackstead-updater/staging/job/target.deb"
	for _, tc := range []struct {
		path string
		args []string
		ok   bool
	}{
		{"/usr/bin/dpkg-deb", []string{"--show", "--showformat=" + debArtifactFormat, p}, true},
		{"/usr/bin/dpkg-query", []string{"--show", "--showformat=" + debQueryFormat, "--", "stackstead"}, true},
		{"/usr/bin/dpkg", []string{"--dry-run", "--force-confdef", "--force-confold", "--install", p}, true},
		{"/usr/bin/dpkg", []string{"--force-confdef", "--force-confold", "--install", p}, true},
		{"/usr/bin/dpkg", []string{"--configure", "-a"}, false},
		{"/usr/bin/dpkg", []string{"--force-overwrite", "--install", p}, false},
		{"/usr/bin/dpkg", []string{"--force-confdef", "--force-confold", "--install", "/tmp/untrusted.deb"}, false},
	} {
		if got := r.allowedRoot(tc.path, tc.args); got != tc.ok {
			t.Errorf("allowlist %s %v = %v", tc.path, tc.args, got)
		}
	}
}

func TestDebRollbackRepairsOnlyKnownPartialTarget(t *testing.T) {
	for _, state := range []string{"install ok unpacked", "install ok half-configured", "install reinstreq half-installed"} {
		d, r, p := debFixture()
		r.state = "stackstead\tamd64\t1.2.4-1\t" + state + "\n"
		r.metadata = "stackstead\tamd64\t1.2.3-1\n"
		if err := d.Validate(context.Background(), p, p.PreviousArtifact); err != nil {
			t.Fatalf("bounded rollback preflight: %v", err)
		}
		if err := d.Install(context.Background(), p, p.PreviousArtifact); err != nil {
			t.Fatalf("bounded rollback %s: %v", state, err)
		}
	}
	d, r, p := debFixture()
	r.state = "stackstead\tamd64\t9.9.9-1\tinstall ok unpacked\n"
	r.metadata = "stackstead\tamd64\t1.2.3-1\n"
	if err := d.Install(context.Background(), p, p.PreviousArtifact); err == nil {
		t.Fatal("unknown partial package repaired")
	}
	for _, call := range r.calls {
		if call.path == "/usr/bin/dpkg" {
			t.Fatal("unknown partial reached package tool")
		}
	}
}
func TestScriptInspectNormalizesVersion(t *testing.T) {
	r := &recordingRunner{output: []byte(`{"version":"v1.2.3","commit":"abc","os":"linux","arch":"amd64"}`)}
	p := update.Plan{Registration: update.Registration{Installer: update.InstallerScript, Prefix: "/usr/local", Binary: "/usr/local/bin/stackstead", Identity: update.Identity{OS: "linux"}, Instances: []update.Instance{{ID: "one", UID: 123, GID: 456}}}}
	id, err := NewScript(r).Inspect(context.Background(), p)
	if err != nil || id.Version != "1.2.3" {
		t.Fatalf("raw executable version: %+v %v", id, err)
	}
}

func TestDebInspectCorrelatesNativeAndExecutableIdentity(t *testing.T) {
	d, r, p := debFixture()
	r.executable = `{"version":"v1.2.3","commit":"old","os":"linux","arch":"amd64"}`
	id, err := d.Inspect(context.Background(), p)
	if err != nil || id != p.Current.Identity {
		t.Fatalf("full native executable identity: %+v %v", id, err)
	}
	call := r.calls[len(r.calls)-1]
	if call.uid != 123 || call.gid != 456 || call.path != "/usr/bin/stackstead" {
		t.Fatalf("wrong executable credentials: %+v", call)
	}
	r.executable = `{"version":"1.2.3","commit":"wrong","os":"linux","arch":"amd64"}`
	if _, err := d.Inspect(context.Background(), p); !errors.Is(err, update.ErrIncompatible) {
		t.Fatalf("mismatched executable accepted: %v", err)
	}
}

func TestDebRechecksArtifactAfterDryRun(t *testing.T) {
	d, r, p := debFixture()
	checks := 0
	d.verify = func(update.VerifiedArtifact) error {
		checks++
		if checks == 2 {
			return update.ErrIncompatible
		}
		return nil
	}
	if err := d.Install(context.Background(), p, p.TargetArtifact); !errors.Is(err, update.ErrIncompatible) {
		t.Fatalf("changed artifact after dry run: %v", err)
	}
	dryRuns := 0
	for _, call := range r.calls {
		if call.path == "/usr/bin/dpkg" {
			if call.args[0] != "--dry-run" {
				t.Fatal("changed artifact installed")
			}
			dryRuns++
		}
	}
	if dryRuns != 1 {
		t.Fatalf("expected one successful dry run, got %d", dryRuns)
	}
}
