package updatehost

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/update"
)

type rpmRunner struct {
	calls                                     []commandCall
	metadata, state, owner, paths, executable string
	testError, installError                   error
}

func (r *rpmRunner) Run(_ context.Context, uid, gid uint32, path string, args []string, stdin []byte) ([]byte, []byte, error) {
	r.calls = append(r.calls, commandCall{uid, gid, path, append([]string(nil), args...), stdin})
	if path == "/usr/bin/stackstead" {
		return []byte(r.executable), nil, nil
	}
	if path != "/usr/bin/rpm" {
		return nil, nil, errors.New("unexpected command")
	}
	switch args[0] {
	case "--eval":
		return []byte(r.paths), nil, nil
	case "-qp":
		return []byte(r.metadata), nil, nil
	case "-qf":
		return []byte(r.owner), nil, nil
	case "-q":
		return []byte(r.state), nil, nil
	case "--test":
		return nil, nil, r.testError
	case "--upgrade":
		if r.installError != nil {
			return nil, nil, r.installError
		}
		r.state = strings.TrimSuffix(r.metadata, "\n") + "\t000\n"
		r.owner = r.metadata
		return nil, nil, nil
	}
	return nil, nil, errors.New("unexpected rpm arguments")
}
func rpmFixture() (*rpmAdapter, *rpmRunner, update.Plan) {
	_, _, p := debFixture()
	p.Registration.Installer = update.InstallerRPM
	p.TargetArtifact.Name = "stackstead.rpm"
	p.TargetArtifact.Path = "/var/lib/stackstead-updater/staging/job/target.rpm"
	p.PreviousArtifact.Name = "stackstead.rpm"
	p.PreviousArtifact.Path = "/var/lib/stackstead-updater/staging/job/previous.rpm"
	r := &rpmRunner{metadata: "stackstead\tx86_64\t1.2.4\t1\t0\n", state: "stackstead\tx86_64\t1.2.3\t1\t0\t000\n", owner: "stackstead\tx86_64\t1.2.3\t1\t0\n", paths: "/var/lib/rpm\t/var/lib/rpm/.rpm.lock\n"}
	d := NewRPM(r).(*rpmAdapter)
	d.verify = func(update.VerifiedArtifact) error { return nil }
	d.locks = func(string, string) error { return nil }
	d.managed = func(context.Context, update.Plan, update.VerifiedArtifact) error { return nil }
	return d, r, p
}
func rpmTransactions(r *rpmRunner) [][]string {
	var calls [][]string
	for _, c := range r.calls {
		if c.path == "/usr/bin/rpm" && (c.args[0] == "--test" || c.args[0] == "--upgrade") {
			calls = append(calls, c.args)
		}
	}
	return calls
}
func TestRPMVerifiesNameArchVersion(t *testing.T) {
	for _, metadata := range []string{"stackstead\tx86_64\t1.2.4\t1\t0\n", "other\tx86_64\t1.2.4\t1\t0\n", "stackstead\taarch64\t1.2.4\t1\t0\n", "stackstead\tx86_64\t1.2.4\t2\t0\n", "stackstead\tx86_64\t1.2.4\t1\t1\n", "stackstead\tx86_64\t1.2.4\t1\t0\nextra\n"} {
		d, r, p := rpmFixture()
		r.metadata = metadata
		err := d.Install(context.Background(), p, p.TargetArtifact)
		good := metadata == "stackstead\tx86_64\t1.2.4\t1\t0\n"
		if good && err != nil || !good && !errors.Is(err, update.ErrIncompatible) {
			t.Fatalf("metadata %q: %v", metadata, err)
		}
		if !good && len(rpmTransactions(r)) != 0 {
			t.Fatal("invalid metadata reached transaction")
		}
	}
	d, r, p := rpmFixture()
	p.Registration.Identity.Arch = "arm64"
	p.Current.Identity.Arch = "arm64"
	p.Target.Identity.Arch = "arm64"
	r.metadata = "stackstead\taarch64\t1.2.4\t1\t0\n"
	r.owner = "stackstead\taarch64\t1.2.3\t1\t0\n"
	r.state = "stackstead\taarch64\t1.2.3\t1\t0\t000\n"
	if err := d.Install(context.Background(), p, p.TargetArtifact); err != nil {
		t.Fatal(err)
	}
}
func TestRPMTestsBeforeUpgrade(t *testing.T) {
	d, r, p := rpmFixture()
	d.managed = nil
	if err := d.Validate(context.Background(), p, p.TargetArtifact); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"--test", "--upgrade", p.TargetArtifact.Path}}
	if !reflect.DeepEqual(rpmTransactions(r), want) {
		t.Fatalf("preflight transactions: %v", rpmTransactions(r))
	}
	r.calls = nil
	if err := d.Install(context.Background(), p, p.TargetArtifact); !errors.Is(err, update.ErrUnavailable) || len(r.calls) != 0 {
		t.Fatalf("default proof: %v %v", err, r.calls)
	}
	d, r, p = rpmFixture()
	if err := d.Install(context.Background(), p, p.TargetArtifact); err != nil {
		t.Fatal(err)
	}
	want = append(want, []string{"--upgrade", p.TargetArtifact.Path})
	if !reflect.DeepEqual(rpmTransactions(r), want) {
		t.Fatalf("transactions: %v", rpmTransactions(r))
	}
	d, r, p = rpmFixture()
	r.testError = errors.New("dependency refusal")
	if err := d.Validate(context.Background(), p, p.TargetArtifact); !errors.Is(err, r.testError) {
		t.Fatalf("preflight refusal: %v", err)
	}
	if err := d.Install(context.Background(), p, p.TargetArtifact); err == nil {
		t.Fatal("native test refusal ignored")
	}
	for _, args := range rpmTransactions(r) {
		if args[0] != "--test" {
			t.Fatal("mutation after refusal")
		}
	}
}
func TestRPMOldpackageOnlyForRecordedRollback(t *testing.T) {
	for _, version := range []string{"1.2.3", "1.2.4"} {
		d, r, p := rpmFixture()
		r.metadata = "stackstead\tx86_64\t1.2.3\t1\t0\n"
		r.owner = "stackstead\tx86_64\t" + version + "\t1\t0\n"
		r.state = strings.TrimSuffix(r.owner, "\n") + "\t000\n"
		if err := d.Install(context.Background(), p, p.PreviousArtifact); err != nil {
			t.Fatal(err)
		}
		want := [][]string{{"--test", "--upgrade", "--oldpackage", "--replacepkgs", p.PreviousArtifact.Path}, {"--upgrade", "--oldpackage", "--replacepkgs", p.PreviousArtifact.Path}}
		if !reflect.DeepEqual(rpmTransactions(r), want) {
			t.Fatalf("bounded rollback: %v", rpmTransactions(r))
		}
	}
	d, r, p := rpmFixture()
	a := p.PreviousArtifact
	a.Path = "/tmp/arbitrary.rpm"
	if err := d.Install(context.Background(), p, a); !errors.Is(err, update.ErrUnauthorized) || len(r.calls) != 0 {
		t.Fatalf("unrecorded rollback: %v", err)
	}
	d, r, p = rpmFixture()
	p.Target.Identity.Version = "1.2.2"
	if err := d.Validate(context.Background(), p, p.TargetArtifact); !errors.Is(err, update.ErrIncompatible) || len(rpmTransactions(r)) != 0 {
		t.Fatalf("downgrade: %v", err)
	}
}
func TestRPMBusyDoesNotStopService(t *testing.T) {
	d, r, p := rpmFixture()
	d.locks = func(string, string) error { return update.ErrBusy }
	if err := d.Validate(context.Background(), p, p.TargetArtifact); !errors.Is(err, update.ErrBusy) || len(rpmTransactions(r)) != 0 {
		t.Fatalf("busy: %v", err)
	}
	for _, c := range r.calls {
		if c.path != "/usr/bin/rpm" {
			t.Fatal("busy preflight changed services")
		}
	}
}
func TestRPMUnknownPartialAndOwnershipRequireRecovery(t *testing.T) {
	for _, change := range []func(*rpmRunner){func(r *rpmRunner) { r.state = "stackstead\tx86_64\t9.9.9\t1\t0\t000\n" }, func(r *rpmRunner) { r.state = "stackstead\tx86_64\t1.2.3\t1\t0\t020\n" }, func(r *rpmRunner) { r.state = "" }, func(r *rpmRunner) { r.owner = "other\tx86_64\t1.2.3\t1\t0\n" }} {
		d, r, p := rpmFixture()
		change(r)
		r.metadata = "stackstead\tx86_64\t1.2.3\t1\t0\n"
		if err := d.Validate(context.Background(), p, p.PreviousArtifact); !errors.Is(err, update.ErrRecoveryRequired) {
			t.Fatalf("unprovable state: %v", err)
		}
		if len(rpmTransactions(r)) != 0 {
			t.Fatal("unproven state reached transaction")
		}
	}
}
func TestRPMInspectUsesAppIdentity(t *testing.T) {
	d, r, p := rpmFixture()
	r.executable = `{"version":"v1.2.3","commit":"old","os":"linux","arch":"amd64"}`
	id, err := d.Inspect(context.Background(), p)
	if err != nil || id != p.Current.Identity {
		t.Fatalf("inspect: %+v %v", id, err)
	}
	call := r.calls[len(r.calls)-1]
	if call.uid != 123 || call.gid != 456 {
		t.Fatalf("wrong app credentials: %+v", call)
	}
	r.executable = `{"version":"1.2.3","commit":"wrong","os":"linux","arch":"amd64"}`
	if _, err := d.Inspect(context.Background(), p); !errors.Is(err, update.ErrRecoveryRequired) {
		t.Fatalf("wrong commit: %v", err)
	}
}
func TestRPMRechecksBytesLocksAndProof(t *testing.T) {
	for _, boundary := range []string{"bytes", "locks", "proof"} {
		d, r, p := rpmFixture()
		n := 0
		refusal := update.ErrUnauthorized
		check := func() error {
			n++
			if n == 2 {
				return refusal
			}
			return nil
		}
		switch boundary {
		case "bytes":
			d.verify = func(update.VerifiedArtifact) error { return check() }
		case "locks":
			d.locks = func(string, string) error { return check() }
		case "proof":
			d.managed = func(context.Context, update.Plan, update.VerifiedArtifact) error { return check() }
		}
		if err := d.Install(context.Background(), p, p.TargetArtifact); !errors.Is(err, refusal) {
			t.Fatalf("%s: %v", boundary, err)
		}
		for _, a := range rpmTransactions(r) {
			if a[0] != "--test" {
				t.Fatalf("%s changed at final boundary", boundary)
			}
		}
	}
}
func TestRPMRejectsUntrustedNativePaths(t *testing.T) {
	for _, paths := range []string{"/tmp/rpm\t/tmp/rpm/.rpm.lock\n", "/var/lib/rpm\t/tmp/lock\n", "/var/lib/rpm\t/var/lib/rpm/../lock\n"} {
		d, r, p := rpmFixture()
		r.paths = paths
		if err := d.Validate(context.Background(), p, p.TargetArtifact); !errors.Is(err, update.ErrSetupRequired) || len(rpmTransactions(r)) != 0 {
			t.Fatalf("untrusted paths: %v", err)
		}
	}
}
func TestRPMCommandRunnerAllowlist(t *testing.T) {
	r := commandRunner{}
	p := "/var/lib/stackstead-updater/staging/job/target.rpm"
	for _, tc := range []struct {
		args []string
		ok   bool
	}{
		{[]string{"-qp", "--queryformat=" + rpmMetadataFormat, "--", p}, true},
		{[]string{"-q", "--queryformat=" + rpmStateFormat, "--", "stackstead"}, true},
		{[]string{"-qf", "--queryformat=" + rpmMetadataFormat, "--", "/usr/bin/stackstead"}, true},
		{[]string{"--eval", rpmPathsFormat}, true},
		{[]string{"--test", "--upgrade", p}, true}, {[]string{"--upgrade", p}, true},
		{[]string{"--test", "--upgrade", "--oldpackage", "--replacepkgs", p}, true},
		{[]string{"--upgrade", "--oldpackage", "--replacepkgs", p}, true},
		{[]string{"--upgrade", "--force", p}, false}, {[]string{"--upgrade", "--nodeps", p}, false},
		{[]string{"--rebuilddb"}, false}, {[]string{"--upgrade", "/tmp/unsafe.rpm"}, false},
		{[]string{"--eval", "%{lua:print('unsafe')}"}, false},
	} {
		if got := r.allowedRoot("/usr/bin/rpm", tc.args); got != tc.ok {
			t.Errorf("%v = %v", tc.args, got)
		}
	}
}

func TestRPMNativeDefaultAndRelocatedPaths(t *testing.T) {
	for _, paths := range []string{"/usr/lib/sysimage/rpm\t/var/lib/rpm/.rpm.lock\n", "/usr/lib/sysimage/rpm\t/usr/lib/sysimage/rpm/.rpm.lock\n"} {
		d, r, p := rpmFixture()
		r.paths = paths
		var db, lock string
		d.locks = func(d, l string) error { db, lock = d, l; return nil }
		if err := d.Validate(context.Background(), p, p.TargetArtifact); err != nil {
			t.Fatalf("native paths %q: %v", paths, err)
		}
		if db == "" || lock == "" {
			t.Fatal("native lock was not inspected")
		}
	}
}
func TestRPMConstructorFailsClosedWithoutLifecycleProof(t *testing.T) {
	_, r, p := rpmFixture()
	adapter := NewRPM(r)
	if err := adapter.Install(context.Background(), p, p.TargetArtifact); !errors.Is(err, update.ErrUnavailable) || len(r.calls) != 0 {
		t.Fatalf("default mutation enabled: %v", err)
	}
	// Use the real artifact verifier: absent private staging bytes must never reach RPM.
	d := adapter.(*rpmAdapter)
	d.locks = func(string, string) error { return nil }
	if err := d.Validate(context.Background(), p, p.TargetArtifact); err == nil || len(r.calls) != 0 {
		t.Fatalf("default bytes verifier missing: %v", err)
	}
}

func TestRPMAbsentLockMacroRequiresSetup(t *testing.T) {
	d, r, p := rpmFixture()
	r.paths = "/var/lib/rpm\t\n"
	if err := d.Validate(context.Background(), p, p.TargetArtifact); !errors.Is(err, update.ErrSetupRequired) || len(rpmTransactions(r)) != 0 {
		t.Fatalf("unproven compiled lock: %v", err)
	}
}
