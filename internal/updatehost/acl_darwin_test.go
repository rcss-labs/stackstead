package updatehost

import (
	"context"
	"encoding/binary"
	"errors"
	"github.com/rcss-labs/stackstead/internal/update"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDarwinPrefixACLWritableAncestorCannotSelectRootWriter(t *testing.T) {
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	bin := filepath.Join(root, "bin")
	if e := os.Mkdir(bin, 0755); e != nil {
		t.Fatal(e)
	}
	var ancestor unix.Stat_t
	if e := unix.Stat(root, &ancestor); e != nil {
		t.Fatal(e)
	}
	s := NewDarwinScript(&darwinRunner{}).(*darwinScriptAdapter)
	s.core.owner = uint32(os.Getuid())
	s.core.anchor = root
	inspected := false
	s.aclProof = func(f *os.File) error {
		var st unix.Stat_t
		if unix.Fstat(int(f.Fd()), &st) != nil {
			t.Fatal("proof did not receive live descriptor")
		}
		if st.Dev == ancestor.Dev && st.Ino == ancestor.Ino {
			inspected = true
			return update.ErrSetupRequired
		}
		return nil
	}
	in := darwinInstance()
	in.UID = uint32(os.Getuid())
	in.GID = uint32(os.Getgid())
	p := update.Plan{Registration: update.Registration{Prefix: root, Binary: filepath.Join(bin, "stackstead"), Instances: []update.Instance{in}}}
	dest, writer, e := s.destination(p)
	if dest != nil {
		defer dest.Close()
	}
	if e == nil && writer == nil {
		t.Fatal("ACL-writable ancestor selected root writer")
	}
	if !inspected {
		t.Fatal("ancestor ACL was not checked on its descriptor")
	}
	// The root install path must not be entered when the fallback child is absent.
	p.Registration.Installer = update.InstallerScript
	p.Registration.Identity.OS = "darwin"
	if e = s.Install(context.Background(), p, update.VerifiedArtifact{}); !errors.Is(e, update.ErrUnavailable) {
		t.Fatalf("unsafe chain reached root installer: %v", e)
	}
}
func TestDarwinACLProofNativeEmptyDescriptor(t *testing.T) {
	f, e := os.CreateTemp(t.TempDir(), "acl-")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if e = darwinACLProof(f); e != nil {
		t.Fatalf("descriptor ACL proof for new file: %v", e)
	}
}

func TestDarwinACLProofRejectsAmbiguousEvidence(t *testing.T) {
	empty := make([]byte, 12)
	binary.LittleEndian.PutUint32(empty[:4], 12)
	binary.LittleEndian.PutUint32(empty[4:8], 8)
	if e := validateDarwinACL(empty); e != nil {
		t.Fatal(e)
	}
	grant := make([]byte, 80)
	binary.LittleEndian.PutUint32(grant[:4], 80)
	binary.LittleEndian.PutUint32(grant[4:8], 8)
	binary.LittleEndian.PutUint32(grant[8:12], 68)
	binary.LittleEndian.PutUint32(grant[12:16], 0x012cc16d)
	binary.LittleEndian.PutUint32(grant[48:52], 1)
	// One ACE is refused regardless of principal or its individual permission bits.
	for _, data := range [][]byte{nil, empty[:11], grant, grant[:79], {0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0, 0, 0, 0, 0}} {
		if e := validateDarwinACL(data); !errors.Is(e, update.ErrSetupRequired) {
			t.Fatalf("unproven ACL accepted: %x %v", data, e)
		}
	}
	if e := validateDarwinVolumeACL(make([]byte, 56)); !errors.Is(e, update.ErrSetupRequired) {
		t.Fatal("unsupported volume accepted")
	}
	if e := platformDescriptorTrustFD(-1); !errors.Is(e, update.ErrSetupRequired) {
		t.Fatal("invalid descriptor accepted")
	}
}

func TestDarwinACLProofNativeGrantIsRefused(t *testing.T) {
	f, e := os.CreateTemp(t.TempDir(), "acl-grant-")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	// Exact test-owned file, current nonroot user only; no host service/root path.
	if output, e := exec.Command("/bin/chmod", "+a", "everyone allow write", f.Name()).CombinedOutput(); e != nil {
		t.Fatalf("test-owned ACL fixture: %v %s", e, output)
	}
	if e := darwinACLProof(f); !errors.Is(e, update.ErrSetupRequired) {
		t.Fatalf("actual ACL grant accepted by descriptor proof: %v", e)
	}
}
