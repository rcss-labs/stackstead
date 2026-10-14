package fsguard

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// tree builds R (root), a sibling R2 and an unrelated outside dir, all with
// symlinks in the temp dir resolved (macOS /var -> /private/var).
type tree struct{ base, root, sibling, outside string }

func newTree(t *testing.T) tree {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr := tree{base: base, root: filepath.Join(base, "R"), sibling: filepath.Join(base, "R2"), outside: filepath.Join(base, "outside")}
	for _, d := range []string{filepath.Join(tr.root, "a", "b"), tr.sibling, tr.outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return tr
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestResolveAccepts(t *testing.T) {
	tr := newTree(t)
	for _, p := range []string{tr.root, filepath.Join(tr.root, "a", "b")} {
		got, err := Resolve([]string{tr.root}, p)
		if err != nil || got != p {
			t.Errorf("Resolve(%q) = %q, %v", p, got, err)
		}
	}
}

func TestResolveRejects(t *testing.T) {
	tr := newTree(t)
	roots := []string{tr.root}
	symlink(t, tr.outside, filepath.Join(tr.root, "escape"))
	cases := map[string]string{
		"dotdot":         filepath.Join(tr.root, "..", "etc"),
		"dotdot exists":  filepath.Join(tr.root, "..", "outside"),
		"prefix sibling": tr.sibling,
		"symlink out":    filepath.Join(tr.root, "escape"),
		"through link":   filepath.Join(tr.root, "escape", "x"),
	}
	for name, p := range cases {
		if _, err := Resolve(roots, p); !errors.Is(err, ErrOutsideRoots) {
			t.Errorf("%s: err = %v, want ErrOutsideRoots", name, err)
		}
	}
}

func TestResolveRelativeAndMissing(t *testing.T) {
	tr := newTree(t)
	if _, err := Resolve([]string{tr.root}, "a/b"); err == nil || errors.Is(err, ErrOutsideRoots) {
		t.Errorf("relative: err = %v, want a non-confinement error", err)
	}
	if _, err := Resolve([]string{tr.root}, filepath.Join(tr.root, "nope")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: err = %v, want ErrNotExist", err)
	}
}

func TestResolveSymlinkInsideRoot(t *testing.T) {
	tr := newTree(t)
	symlink(t, filepath.Join(tr.root, "a"), filepath.Join(tr.root, "link"))
	got, err := Resolve([]string{tr.root}, filepath.Join(tr.root, "link"))
	if want := filepath.Join(tr.root, "a"); err != nil || got != want {
		t.Errorf("got %q, %v; want %q", got, err, want)
	}
}

func TestResolveSymlinkedRoot(t *testing.T) {
	tr := newTree(t)
	alias := filepath.Join(tr.base, "alias")
	symlink(t, tr.root, alias)
	for _, p := range []string{alias, filepath.Join(alias, "a"), filepath.Join(tr.root, "a")} {
		if _, err := Resolve([]string{alias}, p); err != nil {
			t.Errorf("Resolve(%q) = %v", p, err)
		}
	}
}

func TestResolveMultipleRoots(t *testing.T) {
	tr := newTree(t)
	if _, err := Resolve([]string{tr.root, tr.sibling}, tr.sibling); err != nil {
		t.Errorf("second root: %v", err)
	}
	if _, err := Resolve(nil, tr.root); !errors.Is(err, ErrOutsideRoots) {
		t.Errorf("no roots: err = %v", err)
	}
}

func TestList(t *testing.T) {
	tr := newTree(t)
	for _, f := range []string{"z.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(tr.root, f), []byte("hello"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(tr.root, "m"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, tr.outside, filepath.Join(tr.root, "escape"))
	symlink(t, filepath.Join(tr.root, "m"), filepath.Join(tr.root, "inlink"))
	symlink(t, filepath.Join(tr.root, "gone"), filepath.Join(tr.root, "broken"))

	got, err := List([]string{tr.root}, tr.root)
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{{"a", true, 0}, {"inlink", true, 0}, {"m", true, 0}, {"b.txt", false, 5}, {"z.txt", false, 5}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].IsDir != want[i].IsDir || (!want[i].IsDir && got[i].Size != want[i].Size) {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestListErrors(t *testing.T) {
	tr := newTree(t)
	f := filepath.Join(tr.root, "f.txt")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := List([]string{tr.root}, f); err == nil {
		t.Error("listing a file: want error")
	}
	if _, err := List([]string{tr.root}, tr.sibling); !errors.Is(err, ErrOutsideRoots) {
		t.Errorf("outside: err = %v", err)
	}
}
