package images

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlobsPutDedupAndCap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "blobs")
	b, err := OpenBlobs(dir)
	if err != nil {
		t.Fatal(err)
	}
	h1, n1, err := b.Put(strings.NewReader("hello"), 10)
	if err != nil || n1 != 5 {
		t.Fatalf("put: %v %d", err, n1)
	}
	h2, _, err := b.Put(strings.NewReader("hello"), 10)
	if err != nil || h1 != h2 {
		t.Fatalf("second put: %v %s %s", err, h1, h2)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("want one file, got %d", len(entries))
	}
	fi, err := os.Stat(filepath.Join(dir, h1))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("blob mode: %v %v", err, fi)
	}
	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", di.Mode().Perm())
	}

	_, _, err = b.Put(strings.NewReader("12345678901"), 10)
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("want ErrFileTooLarge, got %v", err)
	}
	entries, _ = os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("over-cap upload left files: %d", len(entries))
	}
	if _, _, err = b.Put(strings.NewReader("1234567890"), 10); err != nil {
		t.Fatalf("exactly at cap must pass: %v", err)
	}
}

func TestBlobsOpenRemoveGC(t *testing.T) {
	b, err := OpenBlobs(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	ha, _, _ := b.Put(strings.NewReader("a"), 10)
	hb, _, _ := b.Put(strings.NewReader("b"), 10)
	hc, _, _ := b.Put(strings.NewReader("c"), 10)
	f, err := b.Open(ha)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := b.Open("../../etc/passwd"); err == nil {
		t.Fatal("non-hash name must be refused")
	}
	if err := b.Remove(hb); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Open(hb); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed blob still opens: %v", err)
	}
	removed, err := b.GC(map[string]bool{ha: true})
	if err != nil || removed != 1 {
		t.Fatalf("gc: %d %v", removed, err)
	}
	if _, err := b.Open(hc); err == nil {
		t.Fatal("gc kept an unreferenced blob")
	}
	if _, err := b.Open(ha); err != nil {
		t.Fatal("gc removed a kept blob")
	}
}
