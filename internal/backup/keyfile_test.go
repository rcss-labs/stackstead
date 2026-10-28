package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testPassphrase = "correct horse battery staple"

func TestWriteKeyFileIsPrivateAndStable(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	info, err := WriteKeyFile(dir, testPassphrase, now)
	if err != nil {
		t.Fatalf("WriteKeyFile: %v", err)
	}
	st, err := os.Stat(KeyFilePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("directory holds %d entries after writing, want only %s", len(entries), keyFileName)
	}
	read, ok, err := ReadKeyInfo(dir)
	if err != nil || !ok || read.KeyID != info.KeyID || !read.CreatedAt.Equal(now) {
		t.Fatalf("ReadKeyInfo = %+v, %v, %v; want %+v", read, ok, err, info)
	}
	key, err := LoadKey(dir)
	if err != nil {
		t.Fatalf("LoadKey: %v", err)
	}
	if key.ID() != info.KeyID {
		t.Fatalf("LoadKey id = %q, want the stored %q", key.ID(), info.KeyID)
	}
	// Replacing the passphrase gives a new key id.
	next, err := WriteKeyFile(dir, "another passphrase entirely", now)
	if err != nil || next.KeyID == info.KeyID {
		t.Fatalf("second WriteKeyFile = %+v, %v; want a different key id", next, err)
	}
}

func TestReadKeyInfoMissing(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := ReadKeyInfo(dir); ok || err != nil {
		t.Fatalf("ReadKeyInfo = _, %v, %v; want false, nil", ok, err)
	}
	if _, err := LoadKey(dir); !errors.Is(err, ErrNoKeyFile) {
		t.Fatalf("LoadKey err = %v, want ErrNoKeyFile", err)
	}
	if err := RemoveKeyFile(dir); err != nil {
		t.Fatalf("RemoveKeyFile on a missing file: %v", err)
	}
}

func TestLoadKeyRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, keyFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKey(dir); err == nil || errors.Is(err, ErrNoKeyFile) {
		t.Fatalf("LoadKey err = %v, want a corruption error", err)
	}
	if _, _, err := ReadKeyInfo(dir); err == nil {
		t.Fatal("ReadKeyInfo accepted a corrupt file")
	}
}

func TestRemoveKeyFileDeletes(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteKeyFile(dir, testPassphrase, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKeyFile(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := ReadKeyInfo(dir); ok {
		t.Fatal("key file still present")
	}
}

func TestWriteKeyFileRejectsShortPassphrase(t *testing.T) {
	_, err := WriteKeyFile(t.TempDir(), strings.Repeat("a", minPassphraseLen-1), time.Now())
	if !errors.Is(err, ErrPassphraseTooShort) {
		t.Fatalf("err = %v, want ErrPassphraseTooShort", err)
	}
}
