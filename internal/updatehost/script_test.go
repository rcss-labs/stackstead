package updatehost

import (
	"context"
	"errors"
	"github.com/rcss-labs/stackstead/internal/update"
	"os"
	"path/filepath"
	"testing"
)

func TestScriptInstallPreservesConfigAndStacks(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(bin, "stackstead")
	if err := os.WriteFile(binary, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.yaml", "stacks", "docker-state"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("preserved"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a := update.VerifiedArtifact{Path: "root-private-artifact"}
	p := update.Plan{Registration: update.Registration{Installer: update.InstallerScript, Prefix: root, Binary: binary, Identity: update.Identity{OS: "linux"}, Instances: []update.Instance{{ID: "one", UID: 123, GID: 456}}}, TargetArtifact: a}
	r := &recordingRunner{output: []byte(`{"version":"1.2.3","commit":"abc","os":"linux","arch":"amd64","date":"today"}`)}
	s := &scriptAdapter{runner: r, owner: uint32(os.Getuid()), anchor: root, stagingRoot: root, ownership: func(context.Context, string) error { return nil }, extract: func(_ update.VerifiedArtifact, dest string) error {
		return os.WriteFile(filepath.Join(dest, "stackstead"), []byte("new"), 0600)
	}}
	if err := s.Install(context.Background(), p, a); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(binary); err != nil || string(got) != "new" {
		t.Fatalf("binary: %q %v", got, err)
	}
	for _, name := range []string{"config.yaml", "stacks", "docker-state"} {
		if got, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(got) != "preserved" {
			t.Fatalf("modified %s", name)
		}
	}
	id, err := s.Inspect(context.Background(), p)
	if err != nil || id.Version != "1.2.3" {
		t.Fatalf("identity: %+v %v", id, err)
	}
	if len(r.calls) != 1 || r.calls[0].uid != 123 || r.calls[0].gid != 456 {
		t.Fatalf("app executed as wrong identity: %+v", r.calls)
	}
	s.ownership = func(context.Context, string) error { return update.ErrIncompatible }
	if err := s.Install(context.Background(), p, a); !errors.Is(err, update.ErrIncompatible) {
		t.Fatalf("package owned binary accepted: %v", err)
	}
	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "config.yaml"), binary); err != nil {
		t.Fatal(err)
	}
	s.ownership = func(context.Context, string) error { return nil }
	if err := s.Install(context.Background(), p, a); err == nil {
		t.Fatal("followed binary symlink")
	}
}

func TestScriptValidateRejectsEmptyPayloadBeforeMutation(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin/stackstead"), []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	a := update.VerifiedArtifact{Path: "private"}
	p := update.Plan{Registration: update.Registration{Installer: update.InstallerScript, Prefix: root, Binary: filepath.Join(root, "bin/stackstead"), Identity: update.Identity{OS: "linux"}, Instances: []update.Instance{{ID: "main", UID: 123}}}, TargetArtifact: a}
	s := &scriptAdapter{runner: &recordingRunner{}, owner: uint32(os.Getuid()), anchor: root, stagingRoot: root, ownership: func(context.Context, string) error { return nil }, extract: func(_ update.VerifiedArtifact, dir string) error {
		return os.WriteFile(filepath.Join(dir, "stackstead"), nil, 0600)
	}}
	if err := s.Validate(context.Background(), p, a); !errors.Is(err, update.ErrIncompatible) {
		t.Fatalf("empty payload accepted: %v", err)
	}
}

func TestScriptAPKOwnershipRequiresExplicitNoOwner(t *testing.T) {
	path := "/usr/local/bin/stackstead"
	if !apkNoOwner(path, nil, []byte("ERROR: "+path+": Could not find owner package\n")) {
		t.Fatal("documented APK no-owner answer refused")
	}
	if apkNoOwner(path, nil, nil) {
		t.Fatal("silent APK failure treated as no owner")
	}
	if apkNoOwner(path, nil, []byte("ERROR: Failed to open apk database")) {
		t.Fatal("broken package database treated as no owner")
	}
}
