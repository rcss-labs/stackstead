package updatehost

import (
	"errors"
	"github.com/rcss-labs/stackstead/internal/update"
	"os"
	"path/filepath"
	"testing"
)

func TestCustomParentOwnershipAndSymlinkTrust(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "parent")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatal(err)
	}
	d, err := trustedDirectory(child, uint32(os.Getuid()), root)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if _, err = trustedDirectory(child, uint32(os.Getuid())+1, root); !errors.Is(err, update.ErrSetupRequired) {
		t.Fatalf("wrong owner: %v", err)
	}
	if err = os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err = trustedDirectory(child, uint32(os.Getuid()), root); !errors.Is(err, update.ErrSetupRequired) {
		t.Fatalf("writable parent: %v", err)
	}
	if err = os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(parent, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err = trustedDirectory(filepath.Join(root, "link/child"), uint32(os.Getuid()), root); !errors.Is(err, update.ErrSetupRequired) {
		t.Fatalf("symlink parent: %v", err)
	}
	if err = trustedExecutable(filepath.Join(root, "adapter")); !errors.Is(err, update.ErrSetupRequired) {
		t.Fatalf("nonroot registration: %v", err)
	}
}
