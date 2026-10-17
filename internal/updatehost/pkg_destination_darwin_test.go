package updatehost

import (
	"context"
	"github.com/rcss-labs/stackstead/internal/update"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPKGDestinationRejectsSymlinkWritableACLAndAliases(t *testing.T) {
	for _, bad := range []string{"", "writable", "symlink", "acl", "alias", "wrong-type", "hardlink"} {
		t.Run(bad, func(t *testing.T) {
			base := t.TempDir()
			if e := os.Chmod(base, 0755); e != nil {
				t.Fatal(e)
			}
			parent := filepath.Join(base, "usr/local/bin")
			if e := os.MkdirAll(parent, 0755); e != nil {
				t.Fatal(e)
			}
			leaf := filepath.Join(parent, "stackstead")
			if e := os.WriteFile(leaf, []byte("fixture"), 0755); e != nil {
				t.Fatal(e)
			}
			proof := pkgPayloadProof{PayloadPaths: []string{"/usr/local/bin/stackstead"}}
			switch bad {
			case "hardlink":
				if e := os.Link(leaf, filepath.Join(base, "other-root-file")); e != nil {
					t.Fatal(e)
				}
			case "writable":
				os.Chmod(filepath.Dir(parent), 0777)
			case "symlink":
				os.Remove(leaf)
				os.Symlink("/tmp/other", leaf)
			case "acl":
				c := exec.Command("/bin/chmod", "+a", "everyone allow write", parent)
				if b, e := c.CombinedOutput(); e != nil {
					t.Fatalf("ACL fixture: %v %s", e, b)
				}
			case "alias":
				proof.PayloadPaths = []string{"/System/Volumes/Data/usr/local/bin/stackstead"}
			case "wrong-type":
				os.Remove(leaf)
				os.Mkdir(leaf, 0755)
			}
			root, e := os.Open(base)
			if e != nil {
				t.Fatal(e)
			}
			defer root.Close()
			e = provePKGDestinationAt(context.Background(), root, proof, uint32(os.Getuid()), platformDescriptorTrustFD)
			if bad == "" && e != nil {
				t.Fatalf("stable chain rejected: %v", e)
			}
			if bad != "" && e == nil {
				t.Fatalf("unsafe %s destination accepted", bad)
			}
		})
	}
}
func TestPKGDestinationRechecksImmediatelyBeforeInstaller(t *testing.T) {
	d, r, p := pkgFixture()
	base := t.TempDir()
	os.Chmod(base, 0755)
	os.MkdirAll(filepath.Join(base, "usr/local/bin"), 0755)
	os.MkdirAll(filepath.Join(base, "Library/PrivilegedHelperTools"), 0755)
	root, e := os.Open(base)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	d.destinations = func(ctx context.Context, proof pkgPayloadProof) error {
		return provePKGDestinationAt(ctx, root, proof, uint32(os.Getuid()), platformDescriptorTrustFD)
	}
	n := 0
	d.managed = func(context.Context, update.Plan, update.VerifiedArtifact) error {
		n++
		if n == 2 {
			return os.Chmod(filepath.Join(base, "usr/local"), 0777)
		}
		return nil
	}
	if e = d.Install(context.Background(), p, p.TargetArtifact); e == nil || pkgInstalls(r) != 0 {
		t.Fatalf("changed destination reached installer: %v", e)
	}
}
func TestPayloadVerifiesAllThreeExecutableIdentities(t *testing.T) {
	for _, bad := range []string{"helper-commit", "helper-slice", "bootstrap-commit", "missing-helper", "missing-bootstrap"} {
		t.Run(bad, func(t *testing.T) {
			d, _, p := pkgFixture()
			original := d.payload
			d.payload = func(ctx context.Context, p update.Plan, a update.VerifiedArtifact) (pkgPayloadProof, error) {
				v, e := original(ctx, p, a)
				switch bad {
				case "helper-commit":
					v.Helper.Identity.Commit = "wrong"
				case "helper-slice":
					v.Helper.Slices[0].Commit = "wrong"
				case "bootstrap-commit":
					v.Bootstrap.Identity.Commit = "wrong"
				case "missing-helper":
					v.Helper = pkgExecutableProof{}
				case "missing-bootstrap":
					v.Bootstrap = pkgExecutableProof{}
				}
				return v, e
			}
			if e := d.Validate(context.Background(), p, p.TargetArtifact); e == nil {
				t.Fatal("incomplete binary evidence accepted")
			}
		})
	}
}
