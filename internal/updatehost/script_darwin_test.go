package updatehost

import (
	"context"
	"errors"
	"github.com/rcss-labs/stackstead/internal/update"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDarwinPrefixNeverExecutesRootCode(t *testing.T) {
	in := darwinInstance()
	r := &darwinRunner{run: func(c commandCall) ([]byte, []byte, error) {
		return []byte(`{"version":"v1.2.3","commit":"abcdef","os":"darwin","arch":"arm64"}`), nil, nil
	}}
	p := update.Plan{Registration: update.Registration{Installer: update.InstallerScript, Prefix: "/usr/local", Binary: "/usr/local/bin/stackstead", Identity: update.Identity{OS: "darwin", Arch: "arm64"}, Instances: []update.Instance{in}}}
	id, e := NewDarwinScript(r).Inspect(context.Background(), p)
	if e != nil || id.Version != "1.2.3" || id.OS != "darwin" {
		t.Fatalf("Darwin identity: %+v %v", id, e)
	}
	if len(r.calls) != 1 || r.calls[0].uid != in.UID || r.calls[0].gid != in.GID {
		t.Fatalf("user prefix execution credentials: %+v", r.calls)
	}
}

func TestDarwinPrefixUsesCredentialDroppedWriter(t *testing.T) {
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	bin := filepath.Join(root, "bin")
	if e := os.Mkdir(bin, 0777); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(bin, 0777); e != nil {
		t.Fatal(e)
	}
	binary := filepath.Join(bin, "stackstead")
	if e := os.WriteFile(binary, []byte("old"), 0755); e != nil {
		t.Fatal(e)
	}
	in := darwinInstance()
	in.UID = uint32(os.Getuid())
	in.GID = uint32(os.Getgid())
	a := update.VerifiedArtifact{Path: "verified-private"}
	p := update.Plan{Registration: update.Registration{Installer: update.InstallerScript, Prefix: root, Binary: binary, Identity: update.Identity{OS: "darwin", Arch: "arm64"}, Instances: []update.Instance{in}}, TargetArtifact: a}
	s := NewDarwinScript(&darwinRunner{}).(*darwinScriptAdapter)
	s.core.owner = uint32(os.Getuid())
	s.core.anchor = root
	s.core.stagingRoot = root
	s.core.ownership = func(context.Context, string) error { return nil }
	s.core.extract = func(_ update.VerifiedArtifact, d string) error {
		return os.WriteFile(filepath.Join(d, "stackstead"), []byte("new"), 0600)
	}
	if e := s.Install(context.Background(), p, a); !errors.Is(e, update.ErrUnavailable) {
		t.Fatalf("unconfigured writer accepted: %v", e)
	}
	called := false
	s.writer = func(_ context.Context, uid, gid uint32, payload, dest *os.File, size int64) error {
		called = true
		if uid != in.UID || gid != in.GID || size != 3 {
			t.Fatalf("wrong writer credentials/size: %d %d %d", uid, gid, size)
		}
		content, e := io.ReadAll(payload)
		if e != nil || string(content) != "new" {
			t.Fatalf("invalid inherited payload: %q %v", content, e)
		}
		var st unix.Stat_t
		if unix.Fstat(int(dest.Fd()), &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
			t.Fatal("missing anchored directory")
		}
		return nil
	}
	if e := s.Install(context.Background(), p, a); e != nil || !called {
		t.Fatalf("writer boundary: called=%v err=%v", called, e)
	}
	old, e := os.ReadFile(binary)
	if e != nil || string(old) != "old" {
		t.Fatal("root mutated writable prefix before child")
	}
}
