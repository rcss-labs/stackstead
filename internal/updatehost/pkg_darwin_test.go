package updatehost

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/update"
)

type pkgRunner struct {
	calls               []commandCall
	version, executable string
	receipt, owner      string
	stale               bool
	installError        error
}

func (r *pkgRunner) Run(_ context.Context, uid, gid uint32, path string, args []string, stdin []byte) ([]byte, []byte, error) {
	r.calls = append(r.calls, commandCall{uid, gid, path, append([]string(nil), args...), stdin})
	switch path {
	case "/usr/sbin/pkgutil":
		if args[0] == "--pkg-info" {
			if r.receipt != "" {
				return []byte(r.receipt), nil, nil
			}
			return []byte("package-id: io.github.rcss-labs.stackstead\nversion: " + r.version + "\nvolume: /\nlocation: /\ninstall-time: 123\n"), nil, nil
		}
		if r.owner != "" {
			return []byte(r.owner), nil, nil
		}
		return []byte("volume: /\npath: /usr/local/bin/stackstead\npkgid: io.github.rcss-labs.stackstead\npkg-version: " + r.version + "\n"), nil, nil
	case "/usr/sbin/installer":
		if r.installError != nil {
			return nil, nil, r.installError
		}
		if !r.stale {
			if strings.Contains(args[1], "previous") {
				r.version = "1.2.3"
			} else {
				r.version = "1.2.4"
			}
		}
		if !r.stale {
			commit := strings.Repeat("b", 40)
			if r.version == "1.2.3" {
				commit = strings.Repeat("a", 40)
			}
			r.executable = `{"version":"` + r.version + `","commit":"` + commit + `","os":"darwin","arch":"arm64"}`
		}
		return nil, nil, nil
	case "/usr/local/bin/stackstead":
		return []byte(r.executable), nil, nil
	}
	return nil, nil, errors.New("unexpected command")
}
func pkgFixture() (*pkgAdapter, *pkgRunner, update.Plan) {
	current := update.Identity{Version: "1.2.3", Commit: strings.Repeat("a", 40), OS: "darwin", Arch: "arm64"}
	target := current
	target.Version = "1.2.4"
	target.Commit = strings.Repeat("b", 40)
	a := update.VerifiedArtifact{Name: "stackstead_1.2.4_darwin.pkg", Path: "/Library/Application Support/StacksteadUpdater/staging/job/target.pkg", SHA256: strings.Repeat("c", 64), Size: 12}
	prev := a
	prev.Name = "stackstead_1.2.3_darwin.pkg"
	prev.Path = strings.Replace(a.Path, "target", "previous", 1)
	candidate := func(id update.Identity, a update.VerifiedArtifact) update.Candidate {
		return update.Candidate{Identity: id, Asset: update.AssetSpec{OS: "darwin", Arch: "universal", Installer: update.InstallerPkg, Name: a.Name}}
	}
	p := update.Plan{Registration: update.Registration{Installer: update.InstallerPkg, Prefix: "/usr/local", Binary: "/usr/local/bin/stackstead", Identity: current, UpdateEnabled: true, Instances: []update.Instance{{ID: "initiator", UID: 502, GID: 20}}}, Current: candidate(current, prev), Target: candidate(target, a), PreviousArtifact: prev, TargetArtifact: a}
	for _, c := range []*update.Candidate{&p.Current, &p.Target} {
		c.Manifest = update.Manifest{SchemaVersion: 1, Identity: update.ReleaseIdentity{Version: c.Identity.Version, Commit: c.Identity.Commit}, HelperMin: 1, HelperMax: 1, BootstrapProtocol: 1, JournalProtocol: 1, SourceSchemaMin: 1, TargetSchema: 1, Assets: []update.AssetSpec{c.Asset}}
	}
	r := &pkgRunner{version: "1.2.3"}
	r.executable = `{"version":"1.2.3","commit":"` + current.Commit + `","os":"darwin","arch":"arm64"}`
	d := NewPKG(r).(*pkgAdapter)
	d.verify = func(update.VerifiedArtifact) error { return nil }
	d.payload = func(_ context.Context, got update.Plan, a update.VerifiedArtifact) (pkgPayloadProof, error) {
		id, e := nativeArtifactIdentity(got, a)
		return pkgPayloadProof{Identifier: pkgIdentifier, Version: id.Version, InstallLocation: "/", Artifact: a, App: pkgExecutableProof{Identity: id, Architectures: [2]string{"amd64", "arm64"}}, PayloadPaths: []string{"/usr/local/bin/stackstead"}}, e
	}
	d.managed = func(context.Context, update.Plan, update.VerifiedArtifact) error { return nil }
	return d, r, p
}
func pkgInstalls(r *pkgRunner) int {
	n := 0
	for _, c := range r.calls {
		if c.path == "/usr/sbin/installer" {
			n++
		}
	}
	return n
}
func TestPKGUsesUniversalVerifiedPayload(t *testing.T) {
	for _, bad := range []string{"", "arch", "commit", "receipt", "protected"} {
		t.Run(bad, func(t *testing.T) {
			d, r, p := pkgFixture()
			proof := d.payload
			d.payload = func(ctx context.Context, p update.Plan, a update.VerifiedArtifact) (pkgPayloadProof, error) {
				v, e := proof(ctx, p, a)
				switch bad {
				case "arch":
					v.App.Architectures = [2]string{"arm64", "arm64"}
				case "commit":
					v.App.Identity.Commit = "wrong"
				case "receipt":
					v.Identifier = "other"
				case "protected":
					v.PayloadPaths = append(v.PayloadPaths, "/Library/Application Support/StacksteadUpdater/workers/active")
				}
				return v, e
			}
			e := d.Validate(context.Background(), p, p.TargetArtifact)
			if bad == "" && e != nil {
				t.Fatal(e)
			}
			if bad != "" && e == nil {
				t.Fatal("invalid payload accepted")
			}
			if pkgInstalls(r) != 0 {
				t.Fatal("validation mutated native state")
			}
		})
	}
	d, r, p := pkgFixture()
	a := p.TargetArtifact
	a.SHA256 = strings.Repeat("e", 64)
	if e := d.Install(context.Background(), p, a); !errors.Is(e, update.ErrUnauthorized) || len(r.calls) != 0 {
		t.Fatalf("unrecorded artifact: %v", e)
	}
	d, r, p = pkgFixture()
	d.verify = func(update.VerifiedArtifact) error { return update.ErrIncompatible }
	if e := d.Validate(context.Background(), p, p.TargetArtifact); !errors.Is(e, update.ErrIncompatible) || len(r.calls) != 0 {
		t.Fatal("unverified bytes inspected")
	}
}
func TestPKGChecksReceiptAndIdentity(t *testing.T) {
	d, r, p := pkgFixture()
	r.executable = `{"version":"1.2.3","commit":"` + p.Current.Identity.Commit + `","os":"darwin","arch":"arm64"}`
	id, e := d.Inspect(context.Background(), p)
	if e != nil || id != p.Current.Identity {
		t.Fatalf("inspect: %+v %v", id, e)
	}
	r.executable = strings.Replace(r.executable, p.Current.Identity.Commit, "wrong", 1)
	if _, e = d.Inspect(context.Background(), p); e == nil {
		t.Fatal("receipt-only success")
	}
	d, r, p = pkgFixture()
	r.version = "9.9.9"
	if e = d.Validate(context.Background(), p, p.TargetArtifact); e == nil {
		t.Fatal("unrecorded receipt accepted")
	}
	d, r, p = pkgFixture()
	r.stale = true
	if e = d.Install(context.Background(), p, p.TargetArtifact); !errors.Is(e, update.ErrRecoveryRequired) {
		t.Fatalf("stale receipt success: %v", e)
	}
}
func TestPKGRollbackRetainsRecoveryWorker(t *testing.T) {
	d, r, p := pkgFixture()
	r.version = "1.2.4"
	if e := d.Install(context.Background(), p, p.PreviousArtifact); e != nil {
		t.Fatal(e)
	}
	want := []string{"-pkg", p.PreviousArtifact.Path, "-target", "/"}
	if pkgInstalls(r) != 1 {
		t.Fatal("not exactly one native installer")
	}
	for _, c := range r.calls {
		if c.path == "/usr/sbin/installer" && !reflect.DeepEqual(c.args, want) {
			t.Fatalf("rollback args: %v", c.args)
		}
	}
	d, r, p = pkgFixture()
	r.installError = errors.New("downgrade refused")
	if e := d.Install(context.Background(), p, p.PreviousArtifact); !errors.Is(e, update.ErrRecoveryRequired) || !strings.Contains(e.Error(), "/usr/sbin/installer -pkg") {
		t.Fatalf("missing bounded repair instructions: %v", e)
	}
	if pkgInstalls(r) != 1 {
		t.Fatal("fallback attempted")
	}
	d, r, p = pkgFixture()
	d.managed = nil
	if e := d.Install(context.Background(), p, p.TargetArtifact); !errors.Is(e, update.ErrUnavailable) || len(r.calls) != 0 {
		t.Fatal("unproven helper retention")
	}
}
func TestPKGNonConsoleInitiatorRetained(t *testing.T) {
	d, r, p := pkgFixture()
	r.executable = `{"version":"1.2.3","commit":"` + p.Current.Identity.Commit + `","os":"darwin","arch":"arm64"}`
	if _, e := d.Inspect(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	c := r.calls[len(r.calls)-1]
	if c.uid != 502 || c.gid != 20 || c.path != p.Registration.Binary {
		t.Fatalf("initiator changed: %+v", c)
	}
}
func TestPKGProofsFailClosed(t *testing.T) {
	d, r, p := pkgFixture()
	d.payload = nil
	if e := d.Validate(context.Background(), p, p.TargetArtifact); !errors.Is(e, update.ErrUnavailable) || pkgInstalls(r) != 0 {
		t.Fatalf("missing payload proof: %v", e)
	}
	d, r, p = pkgFixture()
	p.Current.Asset.Arch = "arm64"
	if e := d.Validate(context.Background(), p, p.TargetArtifact); e == nil {
		t.Fatal("non-universal previous release accepted")
	}
}
func TestPKGCommandRunnerAllowsOnlyBoundedNativeOperations(t *testing.T) {
	r := commandRunner{}
	path := "/Library/Application Support/StacksteadUpdater/staging/job/target.pkg"
	for _, tc := range []struct {
		path string
		args []string
		ok   bool
	}{
		{"/usr/sbin/pkgutil", []string{"--pkg-info", "io.github.rcss-labs.stackstead"}, true},
		{"/usr/sbin/pkgutil", []string{"--pkg-info", "other"}, false},
		{"/usr/sbin/pkgutil", []string{"--forget", "io.github.rcss-labs.stackstead"}, false},
		{"/usr/bin/pkgutil", []string{"--pkg-info", "io.github.rcss-labs.stackstead"}, false},
		{"/usr/sbin/installer", []string{"-pkg", path, "-target", "/"}, true},
		{"/usr/sbin/installer", []string{"-pkg", path, "-target", "/Volumes/Other"}, false},
		{"/usr/sbin/installer", []string{"-pkg", "/tmp/untrusted.pkg", "-target", "/"}, false},
		{"/usr/sbin/installer", []string{"-allowUntrusted", "-pkg", path, "-target", "/"}, false},
	} {
		if got := r.allowedRoot(tc.path, tc.args); got != tc.ok {
			t.Errorf("allowlist %s %v = %v", tc.path, tc.args, got)
		}
	}
}
func TestPKGRejectsUnboundOrIncompleteProof(t *testing.T) {
	for _, bad := range []string{"artifact", "version", "location", "path", "empty", "previous"} {
		t.Run(bad, func(t *testing.T) {
			d, r, p := pkgFixture()
			proof := d.payload
			d.payload = func(ctx context.Context, p update.Plan, a update.VerifiedArtifact) (pkgPayloadProof, error) {
				v, e := proof(ctx, p, a)
				switch bad {
				case "artifact":
					v.Artifact.SHA256 = "wrong"
				case "version":
					v.Version = "9.9.9"
				case "location":
					v.InstallLocation = "/Volumes/Other"
				case "path":
					v.PayloadPaths = []string{"/usr/local/../bin/stackstead"}
				case "empty":
					v.PayloadPaths = nil
				case "previous":
					if a == p.PreviousArtifact {
						return v, update.ErrIncompatible
					}
				}
				return v, e
			}
			if e := d.Validate(context.Background(), p, p.TargetArtifact); e == nil || pkgInstalls(r) != 0 {
				t.Fatal("unproven target/previous payload accepted")
			}
		})
	}
}
func TestPKGRechecksProofAndManagedBindingBeforeInstall(t *testing.T) {
	d, r, p := pkgFixture()
	proof := d.payload
	targetChecks := 0
	d.payload = func(ctx context.Context, p update.Plan, a update.VerifiedArtifact) (pkgPayloadProof, error) {
		if a == p.TargetArtifact {
			targetChecks++
			if targetChecks == 2 {
				return pkgPayloadProof{}, update.ErrIncompatible
			}
		}
		return proof(ctx, p, a)
	}
	if e := d.Install(context.Background(), p, p.TargetArtifact); !errors.Is(e, update.ErrIncompatible) || pkgInstalls(r) != 0 {
		t.Fatal("changed payload installed")
	}
	d, r, p = pkgFixture()
	checks := 0
	d.managed = func(_ context.Context, got update.Plan, a update.VerifiedArtifact) error {
		if !reflect.DeepEqual(got, p) || a != p.PreviousArtifact {
			t.Fatal("managed context not bound to rollback")
		}
		checks++
		if checks == 2 {
			return update.ErrUnauthorized
		}
		return nil
	}
	if e := d.Install(context.Background(), p, p.PreviousArtifact); !errors.Is(e, update.ErrUnauthorized) || pkgInstalls(r) != 0 {
		t.Fatal("revoked lifecycle proof ignored")
	}
}
func TestPKGRejectsPrivateRootCaseAliases(t *testing.T) {
	d, _, p := pkgFixture()
	proof := d.payload
	d.payload = func(ctx context.Context, p update.Plan, a update.VerifiedArtifact) (pkgPayloadProof, error) {
		v, e := proof(ctx, p, a)
		v.PayloadPaths = append(v.PayloadPaths, "/library/application support/stacksteadupdater/workers/active")
		return v, e
	}
	if e := d.Validate(context.Background(), p, p.TargetArtifact); !errors.Is(e, update.ErrIncompatible) {
		t.Fatalf("private recovery path case alias accepted: %v", e)
	}
}
func TestPKGRefusesAmbiguousNativeReceiptOwnership(t *testing.T) {
	for _, bad := range []string{"identifier", "volume", "version", "duplicate", "truncated", "owner"} {
		t.Run(bad, func(t *testing.T) {
			d, r, p := pkgFixture()
			r.receipt = "package-id: io.github.rcss-labs.stackstead\nversion: 1.2.3\nvolume: /\nlocation: /\ninstall-time: 123\n"
			switch bad {
			case "identifier":
				r.receipt = strings.Replace(r.receipt, pkgIdentifier, "other", 1)
			case "volume":
				r.receipt = strings.Replace(r.receipt, "volume: /", "volume: /Volumes/Other", 1)
			case "version":
				r.receipt = strings.Replace(r.receipt, "1.2.3", "1.2.30", 1)
			case "duplicate":
				r.receipt += "package-id: other\n"
			case "truncated":
				r.receipt = strings.Repeat("x", commandOutputLimit+1)
			case "owner":
				r.owner = "volume: /\npath: /usr/local/bin/stackstead\npkgid: other\npkg-version: 1.2.3\n"
			}
			if e := d.Validate(context.Background(), p, p.TargetArtifact); e == nil || pkgInstalls(r) != 0 {
				t.Fatal("ambiguous receipt/owner reached installer")
			}
		})
	}
	d, r, p := pkgFixture()
	d.managed = nil
	if e := d.Validate(context.Background(), p, p.TargetArtifact); e != nil || pkgInstalls(r) != 0 {
		t.Fatalf("read-only validation needs active context: %v", e)
	}
}
