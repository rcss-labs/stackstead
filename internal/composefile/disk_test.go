package composefile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSaveCreatesNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := Save(path, []byte("services: {}\n"), ""); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := readFile(t, path); got != "services: {}\n" {
		t.Errorf("content = %q", got)
	}
	if _, err := os.Stat(path + ".bak"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("new file got a backup: %v", err)
	}
}

func TestSaveRefusesToCreateOverExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	os.WriteFile(path, []byte("original\n"), 0o644)
	if err := Save(path, []byte("new\n"), ""); !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
	if got := readFile(t, path); got != "original\n" {
		t.Errorf("file was modified: %q", got)
	}
}

func TestSaveReplacesMatchingVersionAndKeepsBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	os.WriteFile(path, []byte("v1\n"), 0o600)
	_, hash, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(path, []byte("v2\n"), hash); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := readFile(t, path); got != "v2\n" {
		t.Errorf("content = %q, want v2", got)
	}
	if got := readFile(t, path+".bak"); got != "v1\n" {
		t.Errorf("backup = %q, want v1", got)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 preserved", info.Mode().Perm())
	}
}

func TestSaveDetectsConcurrentChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	os.WriteFile(path, []byte("v1\n"), 0o644)
	_, hash, _ := Load(path)
	os.WriteFile(path, []byte("edited by hand\n"), 0o644)
	if err := Save(path, []byte("v2\n"), hash); !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
	if got := readFile(t, path); got != "edited by hand\n" {
		t.Errorf("hand edit was overwritten: %q", got)
	}
}

func TestSaveDetectsDeletedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := Save(path, []byte("v2\n"), Hash([]byte("v1\n"))); !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	if err := Save(path, []byte("v1\n"), ""); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, []byte("v2\n"), Hash([]byte("v1\n"))); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 || names[0] != "compose.yaml" || names[1] != "compose.yaml.bak" {
		t.Errorf("dir contains %v, want [compose.yaml compose.yaml.bak]", names)
	}
}

func TestSaveIntoMissingDirectoryFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "compose.yaml")
	if err := Save(path, []byte("x\n"), ""); err == nil {
		t.Error("want error for missing directory")
	}
}
