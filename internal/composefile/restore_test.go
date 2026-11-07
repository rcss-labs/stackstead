package composefile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreBackupKeepsBrokenCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	writeFile(t, path, "broken: [", 0o640)
	writeFile(t, path+".bak", "services: {}\n", 0o640)
	if err := RestoreBackup(path, Hash([]byte("broken: ["))); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if got := readFile(t, path); got != "services: {}\n" {
		t.Errorf("restored content = %q", got)
	}
	if got := readFile(t, path+".broken"); got != "broken: [" {
		t.Errorf(".broken = %q, want the file as it was", got)
	}
	if got := readFile(t, path+".bak"); got != "services: {}\n" {
		t.Errorf(".bak changed to %q", got)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, %v; want 0640 kept", info.Mode().Perm(), err)
	}
}

func TestRestoreBackupStaleHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	writeFile(t, path, "current", 0o600)
	writeFile(t, path+".bak", "old", 0o600)
	if err := RestoreBackup(path, Hash([]byte("something else"))); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if got := readFile(t, path); got != "current" {
		t.Errorf("file changed to %q", got)
	}
	if _, err := os.Stat(path + ".broken"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf(".broken written on a stale hash: %v", err)
	}
}

func TestRestoreBackupMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	writeFile(t, path, "current", 0o600)
	if err := RestoreBackup(path, Hash([]byte("current"))); !errors.Is(err, ErrNoBackup) {
		t.Fatalf("err = %v, want ErrNoBackup", err)
	}
	if HasBackup(path) {
		t.Error("HasBackup = true with no .bak")
	}
}

func TestRestoreBackupRefusesSymlinkedBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	outside := filepath.Join(t.TempDir(), "secret")
	writeFile(t, outside, "TOP SECRET", 0o600)
	writeFile(t, path, "current", 0o600)
	if err := os.Symlink(outside, path+".bak"); err != nil {
		t.Skip("symlinks unavailable")
	}
	if HasBackup(path) {
		t.Error("HasBackup followed a symlink")
	}
	if err := RestoreBackup(path, Hash([]byte("current"))); !errors.Is(err, ErrNoBackup) {
		t.Fatalf("err = %v, want ErrNoBackup", err)
	}
	if got := readFile(t, path); got != "current" {
		t.Errorf("file changed to %q", got)
	}
}
