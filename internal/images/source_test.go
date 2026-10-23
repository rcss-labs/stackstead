package images

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestValidatePath(t *testing.T) {
	bad := []string{
		"", ".", "./", "../x", "a/../b", "a/..", "/etc/x", `a\b`, "a\x00b",
		strings.Repeat("a", 256), "ok/" + strings.Repeat("b", 300),
		strings.Repeat("a/", 2100) + "a",
	}
	for _, p := range bad {
		if _, err := ValidatePath(p); !errors.Is(err, ErrBadPath) {
			t.Errorf("%q: want ErrBadPath, got %v", p[:min(len(p), 20)], err)
		}
	}
	good := map[string]string{
		"a/b": "a/b", "./a/b": "a/b", "a//b": "a/b", "a/./b": "a/b", "Dockerfile": "Dockerfile",
		strings.Repeat("a", 255): strings.Repeat("a", 255),
	}
	for in, want := range good {
		got, err := ValidatePath(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q %v want %q", in, got, err, want)
		}
	}
}

func newTestBlobs(t *testing.T) (*Blobs, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "blobs")
	b, err := OpenBlobs(dir)
	if err != nil {
		t.Fatal(err)
	}
	return b, dir
}

func up(path, content string) UploadFile {
	return UploadFile{Path: path, Content: strings.NewReader(content)}
}

func TestUploadRejectsDuplicatePaths(t *testing.T) {
	for _, dup := range []string{"a/b", "./a/b", "a//b"} {
		b, _ := newTestBlobs(t)
		got, _, err := ApplyUpload(Manifest{}, b, []UploadFile{up("a/b", "one"), up(dup, "two")}, Limits{1 << 20, 1 << 20})
		if !errors.Is(err, ErrBadPath) || got != nil {
			t.Errorf("duplicate %q: got %v, %v; want ErrBadPath and no manifest", dup, got, err)
		}
	}
}

func TestUploadRejectsPaths(t *testing.T) {
	b, blobDir := newTestBlobs(t)
	base, _, err := ApplyUpload(Manifest{}, b, []UploadFile{up("keep.txt", "k")}, Limits{1 << 20, 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	before := base.JSON()
	root := filepath.Dir(blobDir)
	hostile := []string{"../x", "/etc/x", `a\b`, "a\x00b", strings.Repeat("s", 300) + "/f", "a/../../x"}
	for _, p := range hostile {
		got, _, err := ApplyUpload(base, b, []UploadFile{up("fine.txt", "f"), up(p, "evil")}, Limits{1 << 20, 1 << 20})
		if !errors.Is(err, ErrBadPath) {
			t.Errorf("%q: want ErrBadPath, got %v", p, err)
			continue
		}
		if q := strconv.Quote(p); !strings.Contains(err.Error(), q[:min(len(q), 20)]) {
			t.Errorf("error does not name the path: %v", err)
		}
		if got != nil && got.JSON() != before {
			t.Errorf("%q: manifest changed", p)
		}
	}
	if base.JSON() != before {
		t.Fatal("input manifest mutated")
	}
	var found []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			found = append(found, rel)
		}
		return nil
	})
	for _, f := range found {
		if !strings.HasPrefix(f, "blobs"+string(filepath.Separator)) {
			t.Errorf("file written outside blob dir: %s", f)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "x")); err == nil {
		t.Error("x escaped")
	}
}

func TestUploadEnforcesCaps(t *testing.T) {
	b, _ := newTestBlobs(t)
	base, _, err := ApplyUpload(Manifest{}, b, []UploadFile{up("a", "12345")}, Limits{10, 12})
	if err != nil {
		t.Fatal(err)
	}
	before := base.JSON()

	got, _, err := ApplyUpload(base, b, []UploadFile{up("big.bin", "12345678901")}, Limits{10, 100})
	if !errors.Is(err, ErrFileTooLarge) || !strings.Contains(err.Error(), "big.bin") {
		t.Fatalf("want ErrFileTooLarge naming path, got %v", err)
	}
	if got != nil && got.JSON() != before {
		t.Fatal("manifest changed on file cap")
	}

	got, _, err = ApplyUpload(base, b, []UploadFile{up("b", "1234"), up("c", "1234")}, Limits{10, 12})
	if !errors.Is(err, ErrContextTooLarge) {
		t.Fatalf("want ErrContextTooLarge, got %v", err)
	}
	if got != nil && got.JSON() != before {
		t.Fatal("manifest changed on context cap")
	}
	if base.JSON() != before {
		t.Fatal("input manifest mutated")
	}
	// Replacing a file frees its old size.
	if _, _, err = ApplyUpload(base, b, []UploadFile{up("a", "1234567890")}, Limits{10, 12}); err != nil {
		t.Fatalf("replacement within cap: %v", err)
	}
}

func TestUploadSummaryAndFileDirConflict(t *testing.T) {
	b, _ := newTestBlobs(t)
	lim := Limits{1 << 20, 1 << 20}
	m, sum, err := ApplyUpload(Manifest{}, b, []UploadFile{up("a/b", "1"), up("./c", "2"), up("d", "3")}, lim)
	if err != nil || sum != "uploaded 3 files" {
		t.Fatalf("%v %q", err, sum)
	}
	if _, ok := m["c"]; !ok || len(m.Files()) != 3 || m.Files()[0].Path != "a/b" {
		t.Fatalf("manifest: %v", m.JSON())
	}
	if _, _, err := ApplyUpload(m, b, []UploadFile{up("a", "x")}, lim); !errors.Is(err, ErrBadPath) {
		t.Fatalf("file over dir: %v", err)
	}
	if _, _, err := ApplyUpload(m, b, []UploadFile{up("c/x", "x")}, lim); !errors.Is(err, ErrBadPath) {
		t.Fatalf("dir under file: %v", err)
	}
}

func TestRenameDirectoryPrefix(t *testing.T) {
	b, _ := newTestBlobs(t)
	lim := Limits{1 << 20, 1 << 20}
	m, _, _ := ApplyUpload(Manifest{}, b, []UploadFile{up("src/a.py", "1"), up("src/sub/b.py", "2"), up("srcx", "3"), up("top", "4")}, lim)
	got, sum, err := ApplyRename(m, "src/", "lib")
	if err != nil || sum != "renamed src/ → lib" && sum != "renamed src → lib" {
		t.Fatalf("%v %q", err, sum)
	}
	for _, p := range []string{"lib/a.py", "lib/sub/b.py", "srcx", "top"} {
		if _, ok := got[p]; !ok {
			t.Errorf("missing %s in %s", p, got.JSON())
		}
	}
	if len(got) != 4 {
		t.Errorf("unexpected entries: %s", got.JSON())
	}
	if _, ok := m["src/a.py"]; !ok {
		t.Error("input mutated")
	}
	if _, _, err := ApplyRename(m, "src", "top"); !errors.Is(err, fs.ErrExist) {
		t.Errorf("onto existing: %v", err)
	}
	if _, _, err := ApplyRename(m, "src", "srcx"); !errors.Is(err, fs.ErrExist) {
		t.Errorf("dir onto file: %v", err)
	}
	if _, _, err := ApplyRename(m, "nope", "x"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	if _, _, err := ApplyRename(m, "top", "../x"); !errors.Is(err, ErrBadPath) {
		t.Errorf("bad target: %v", err)
	}
	if _, _, err := ApplyRename(m, "src", "src/inner"); !errors.Is(err, ErrBadPath) {
		t.Errorf("into itself: %v", err)
	}
	got, sum, err = ApplyRename(m, "top", "t2")
	if err != nil || sum != "renamed top → t2" || got["t2"] != m["top"] {
		t.Errorf("file rename: %v %q", err, sum)
	}
}

func TestDeleteAndDockerfile(t *testing.T) {
	b, _ := newTestBlobs(t)
	lim := Limits{1 << 20, 1 << 20}
	m, _, _ := ApplyUpload(Manifest{}, b, []UploadFile{up("src/a", "1"), up("src/b", "2"), up("x", "3")}, lim)
	got, sum, err := ApplyDelete(m, "src")
	if err != nil || sum != "deleted src" || len(got) != 1 {
		t.Fatalf("%v %q %v", err, sum, got)
	}
	if _, _, err := ApplyDelete(m, "zzz"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%v", err)
	}
	got, sum, err = ApplyDockerfile(m, b, "Dockerfile", []byte("FROM alpine:3.20\n"), lim)
	if err != nil || sum != "edited Dockerfile" || got["Dockerfile"].Mode != 0o644 {
		t.Fatalf("%v %q", err, sum)
	}
	for _, bad := range [][]byte{nil, []byte("  \n"), []byte("FROM a\x00"), []byte(strings.Repeat("a", 1<<20+1))} {
		if _, _, err := ApplyDockerfile(m, b, "Dockerfile", bad, lim); err == nil {
			t.Errorf("accepted bad Dockerfile of %d bytes", len(bad))
		}
	}
	if _, _, err := ApplyDockerfile(m, b, "../Dockerfile", []byte("FROM a"), lim); !errors.Is(err, ErrBadPath) {
		t.Fatalf("%v", err)
	}
}

func TestManifestJSONRoundTrip(t *testing.T) {
	h := strings.Repeat("a", 64)
	m := Manifest{"b": {h, 1, 0o644}, "a/x": {h, 2, 0o755}}
	s := m.JSON()
	if strings.Index(s, `"a/x"`) > strings.Index(s, `"b"`) {
		t.Fatalf("keys not sorted: %s", s)
	}
	back, err := ParseManifest(s)
	if err != nil || len(back) != 2 || back["a/x"] != m["a/x"] {
		t.Fatalf("%v %v", err, back)
	}
	if e, err := ParseManifest(""); err != nil || len(e) != 0 {
		t.Fatalf("empty: %v", err)
	}
	for _, bad := range []string{
		`{"../x":{"hash":"` + h + `","size":1,"mode":420}}`,
		`{"a":{"hash":"zz","size":1,"mode":420}}`,
		`{"a":{"hash":"` + h + `","size":1,"mode":511}}`,
		`{"a":{"hash":"` + h + `","size":-1,"mode":420}}`,
		`not json`,
	} {
		if _, err := ParseManifest(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestRenameRejectsOverlongTargets(t *testing.T) {
	b, _ := newTestBlobs(t)
	lim := Limits{1 << 20, 1 << 20}
	deep := strings.Repeat("d/", 1990) + "f"
	m, _, err := ApplyUpload(Manifest{}, b, []UploadFile{up("s/"+deep, "1"), up("x", "2")}, lim)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("n", 255)
	// Child path becomes longer than 4096 bytes once the folder name grows.
	if _, _, err := ApplyRename(m, "s", strings.Repeat(long+"/", 1)+"z"); !errors.Is(err, ErrBadPath) {
		t.Errorf("child over 4096 bytes: %v", err)
	}
	if _, _, err := ApplyRename(m, "x", strings.Repeat("a", 300)); !errors.Is(err, ErrBadPath) {
		t.Errorf("long segment: %v", err)
	}
	if _, _, err := ApplyRename(m, "x", strings.Repeat("a/", 2100)+"a"); !errors.Is(err, ErrBadPath) {
		t.Errorf("long dst: %v", err)
	}
}

func TestApplyDockerfileRespectsCaps(t *testing.T) {
	b, _ := newTestBlobs(t)
	m, _, _ := ApplyUpload(Manifest{}, b, []UploadFile{up("a", "12345678")}, Limits{100, 100})
	before := m.JSON()
	got, _, err := ApplyDockerfile(m, b, "Dockerfile", []byte("FROM alpine:3.20\n"), Limits{100, 20})
	if !errors.Is(err, ErrContextTooLarge) {
		t.Fatalf("want ErrContextTooLarge, got %v", err)
	}
	if got != nil || m.JSON() != before {
		t.Fatal("manifest changed")
	}
	if _, _, err := ApplyDockerfile(m, b, "Dockerfile", []byte("FROM alpine:3.20\n"), Limits{5, 100}); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("want ErrFileTooLarge, got %v", err)
	}
	if _, _, err := ApplyDockerfile(m, b, "Dockerfile", []byte("FROM alpine:3.20\n"), Limits{100, 100}); err != nil {
		t.Fatal(err)
	}
}
