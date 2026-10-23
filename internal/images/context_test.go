package images

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMaterializeAndWriteContext(t *testing.T) {
	b, _ := newTestBlobs(t)
	lim := Limits{1 << 20, 1 << 20}
	m, _, err := ApplyUpload(Manifest{}, b, []UploadFile{
		up("Dockerfile", "FROM alpine:3.20\n"), up("src/app.py", "print(1)\n"),
		up(".dockerignore", "*.md\n"), up("README.md", "# hi\n"),
	}, lim)
	if err != nil {
		t.Fatal(err)
	}
	e := m["src/app.py"]
	e.Mode = 0o755
	m["src/app.py"] = e

	dir := filepath.Join(t.TempDir(), "ctx")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Materialize(dir, m, b); err != nil {
		t.Fatal(err)
	}
	if err := Materialize(dir, m, b); err == nil {
		t.Fatal("Materialize into a non-empty dir must fail")
	}
	// A symlink in the directory must never reach the tar.
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := WriteContext(&buf, dir); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&buf)
	var names []string
	modes := map[string]int64{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag != tar.TypeReg {
			t.Errorf("%s: non-regular entry %c", h.Name, h.Typeflag)
		}
		if h.ModTime.Unix() != 0 {
			t.Errorf("%s: mtime %v", h.Name, h.ModTime)
		}
		names = append(names, h.Name)
		modes[h.Name] = h.Mode
	}
	want := []string{".dockerignore", "Dockerfile", "src/app.py"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("entries %v, want %v", names, want)
	}
	if modes["src/app.py"] != 0o755 || modes["Dockerfile"] != 0o644 {
		t.Fatalf("modes %v", modes)
	}
	content, _ := os.ReadFile(filepath.Join(dir, "src/app.py"))
	if string(content) != "print(1)\n" {
		t.Fatalf("content %q", content)
	}
	// The blob must keep its own mode: exec bits are set on a copy only.
	h := m["src/app.py"].Hash
	fi, _ := os.Stat(filepath.Join(filepath.Dir(dir), "blobs", h))
	if fi != nil && fi.Mode().Perm() != 0o600 {
		t.Errorf("blob mode changed to %v", fi.Mode().Perm())
	}
}

func TestMaterializeRefusesBadManifest(t *testing.T) {
	b, _ := newTestBlobs(t)
	h, _, _ := b.Put(strings.NewReader("x"), 10)
	dir := t.TempDir()
	m := Manifest{"../escape": {Hash: h, Size: 1, Mode: 0o644}}
	if err := Materialize(dir, m, b); err == nil {
		t.Fatal("escaping manifest key accepted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape")); err == nil {
		t.Fatal("file escaped")
	}
}

func TestWriteContextNegatedIgnoreAndSizeStable(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM a\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte("*.log\n!keep.log\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.log"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "keep.log"), []byte("x"), 0o644)
	var one, two bytes.Buffer
	if err := WriteContext(&one, dir); err != nil {
		t.Fatal(err)
	}
	WriteContext(&two, dir)
	if !bytes.Equal(one.Bytes(), two.Bytes()) {
		t.Fatal("tar is not reproducible")
	}
	tr := tar.NewReader(&one)
	var names []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	if !reflect.DeepEqual(names, []string{".dockerignore", "Dockerfile", "keep.log"}) {
		t.Fatalf("%v", names)
	}
}
