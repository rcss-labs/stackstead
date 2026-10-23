package github

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name string
	typ  byte
	body string
	mode int64
	link string
}

func buildTarGz(t *testing.T, entries []entry) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: mode, Linkname: e.link}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return bytes.NewReader(buf.Bytes())
}

func TestExtractSubdirAndTraversal(t *testing.T) {
	dst := t.TempDir()
	good := buildTarGz(t, []entry{
		{name: "repo-abc/", typ: tar.TypeDir},
		{name: "repo-abc/README.md", body: "outside"},
		{name: "repo-abc/svc/", typ: tar.TypeDir},
		{name: "repo-abc/svc/Dockerfile", body: "FROM alpine\n"},
		{name: "repo-abc/svc/run.sh", body: "#!/bin/sh\n", mode: 0o755},
		{name: "repo-abc/svc/sub/a.txt", body: "a", mode: 0o600},
		{name: "repo-abc/svc/link", typ: tar.TypeSymlink, link: "/etc/passwd"},
		{name: "repo-abc/svc/hard", typ: tar.TypeLink, link: "repo-abc/svc/Dockerfile"},
		{name: "repo-abc/svc/dev", typ: tar.TypeChar},
	})
	n, err := ExtractContext(good, "svc", dst, 1024, 4096)
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	for p, mode := range map[string]os.FileMode{"Dockerfile": 0o644, "run.sh": 0o755, "sub/a.txt": 0o644} {
		fi, err := os.Lstat(filepath.Join(dst, p))
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if fi.Mode().Perm() != mode {
			t.Errorf("%s mode %v, want %v", p, fi.Mode().Perm(), mode)
		}
	}
	for _, p := range []string{"README.md", "link", "hard", "dev", "svc"} {
		if _, err := os.Lstat(filepath.Join(dst, p)); err == nil {
			t.Errorf("%s must not be extracted", p)
		}
	}

	evil := buildTarGz(t, []entry{
		{name: "repo-abc/svc/Dockerfile", body: "x"},
		{name: "repo-abc/svc/../../etc/x", body: "x"},
	})
	if _, err := ExtractContext(evil, "svc", t.TempDir(), 1024, 4096); !errors.Is(err, ErrBadPath) {
		t.Fatalf("want ErrBadPath, got %v", err)
	}
}

func TestExtractWholeRepoAndOversizedFile(t *testing.T) {
	dst := t.TempDir()
	n, err := ExtractContext(buildTarGz(t, []entry{
		{name: "repo-abc/a", body: "1"}, {name: "repo-abc/d/b", body: "2"},
	}), "", dst, 10, 100)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := ExtractContext(buildTarGz(t, []entry{{name: "repo-abc/a", body: "123"}}), "", t.TempDir(), 2, 100); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("want ErrFileTooLarge, got %v", err)
	}
}

func TestExtractHostilePaths(t *testing.T) {
	long := strings.Repeat("a", 256)
	var deep []string
	for len(strings.Join(deep, "/")) <= 4096 {
		deep = append(deep, "dddddddddddddddddddd")
	}
	for name, p := range map[string]string{
		"dotdot":    "repo-abc/../x",
		"absolute":  "/etc/x",
		"backslash": `repo-abc/a\b`,
		"segment":   "repo-abc/" + long,
		"total":     "repo-abc/" + strings.Join(deep, "/"),
	} {
		_, err := ExtractContext(buildTarGz(t, []entry{{name: p, body: "x"}}), "", t.TempDir(), 1024, 1<<20)
		if !errors.Is(err, ErrBadPath) {
			t.Errorf("%s: want ErrBadPath, got %v", name, err)
		}
	}
	for _, sub := range []string{"..", "/abs", `a\b`, "a/../b"} {
		if _, err := ExtractContext(buildTarGz(t, nil), sub, t.TempDir(), 1, 1); !errors.Is(err, ErrBadPath) {
			t.Errorf("subdir %q: want ErrBadPath, got %v", sub, err)
		}
	}
}

func TestValidatePathRejectsNUL(t *testing.T) {
	// archive/tar cannot write a NUL name, so check the validator directly.
	if _, err := validatePath("repo-abc/a\x00b"); !errors.Is(err, ErrBadPath) {
		t.Fatalf("want ErrBadPath, got %v", err)
	}
}

func TestExtractCaps(t *testing.T) {
	_, err := ExtractContext(buildTarGz(t, []entry{
		{name: "r/a", body: "1234"}, {name: "r/b", body: "1234"}, {name: "r/c", body: "1234"},
	}), "", t.TempDir(), 10, 10)
	if !errors.Is(err, ErrContextTooLarge) {
		t.Fatalf("want ErrContextTooLarge, got %v", err)
	}
}

func TestExtractGzipBombIsBounded(t *testing.T) {
	// A large entry outside the subdir is never written, so only the
	// decompression bound can stop it.
	big := strings.Repeat("\x00", 8<<20)
	_, err := ExtractContext(buildTarGz(t, []entry{{name: "r/skip/big", body: big}, {name: "r/svc/a", body: "x"}}), "svc", t.TempDir(), 1<<20, 1<<20)
	if !errors.Is(err, ErrContextTooLarge) {
		t.Fatalf("want ErrContextTooLarge, got %v", err)
	}
}

func TestExtractRequiresEmptyDst(t *testing.T) {
	dst := t.TempDir()
	os.WriteFile(filepath.Join(dst, "x"), nil, 0o600)
	if _, err := ExtractContext(buildTarGz(t, nil), "", dst, 1, 1); err == nil {
		t.Fatal("want error for non-empty dst")
	}
	if _, err := ExtractContext(buildTarGz(t, nil), "", filepath.Join(dst, "missing"), 1, 1); err == nil {
		t.Fatal("want error for missing dst")
	}
}

func TestExtractNotGzip(t *testing.T) {
	if _, err := ExtractContext(strings.NewReader("plain"), "", t.TempDir(), 1, 1); err == nil {
		t.Fatal("want error")
	}
}
