package stacks_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

func parseCompose(t *testing.T, src string) *composefile.File {
	t.Helper()
	f, err := composefile.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// dockerfileStack writes a stack's compose file to disk, registers it and
// returns the stack folder.
func dockerfileStack(t *testing.T, h *harness, name, compose string) string {
	t.Helper()
	dir := filepath.Join(h.dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.reg.Add(context.Background(), stacks.Entry{Name: name, ComposePath: file, Origin: stacks.OriginManaged, Project: name}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDockerfileTargetLayouts(t *testing.T) {
	stack := t.TempDir()
	stack, _ = filepath.EvalSymlinks(stack)
	tests := []struct {
		name, compose, want string
		inside              bool
		err                 error
	}{
		{"short build", "services:\n  web:\n    build: ./web\n", "web/Dockerfile", true, nil},
		{"context and dockerfile", "services:\n  web:\n    build:\n      context: .\n      dockerfile: Dockerfile.dev\n", "Dockerfile.dev", true, nil},
		{"absolute context inside", "services:\n  web:\n    build: " + stack + "/w\n", "w/Dockerfile", true, nil},
		{"absolute context outside", "services:\n  web:\n    build: /srv/elsewhere\n", "/srv/elsewhere/Dockerfile", false, nil},
		{"dockerfile relative to context", "services:\n  web:\n    build:\n      context: ./a\n      dockerfile: ../b/Dockerfile\n", "b/Dockerfile", true, nil},
		{"no build", "services:\n  web:\n    image: x\n", "", false, stacks.ErrNoBuild},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, inside, err := stacks.DockerfileTarget(stack, parseCompose(t, tt.compose), "web")
			if !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
			if tt.err != nil {
				return
			}
			want := tt.want
			if !filepath.IsAbs(want) {
				want = filepath.Join(stack, want)
			}
			if path != want || inside != tt.inside {
				t.Errorf("got (%s, %v), want (%s, %v)", path, inside, want, tt.inside)
			}
		})
	}
}

func TestDockerfileTargetDotDot(t *testing.T) {
	stack, _ := filepath.EvalSymlinks(t.TempDir())
	path, inside, err := stacks.DockerfileTarget(stack, parseCompose(t, "services:\n  web:\n    build: ../other\n"), "web")
	if err != nil || inside {
		t.Fatalf("got (%s, %v, %v), want outside", path, inside, err)
	}
	if path != filepath.Join(filepath.Dir(stack), "other", "Dockerfile") {
		t.Errorf("path = %s", path)
	}
}

func TestDockerfileTargetNotEditable(t *testing.T) {
	tests := []struct{ name, compose, reason string }{
		{"variables in context", "services:\n  web:\n    build: ./${APP}\n", "uses variables"},
		{"variables in dockerfile", "services:\n  web:\n    build:\n      context: .\n      dockerfile: $NAME\n", "uses variables"},
		{"https", "services:\n  web:\n    build: https://github.com/a/b.git\n", "remote build context"},
		{"git ssh", "services:\n  web:\n    build: git@github.com:a/b.git\n", "remote build context"},
		{"github prefix", "services:\n  web:\n    build: github.com/a/b\n", "remote build context"},
		{"git fragment", "services:\n  web:\n    build: ./x.git#main\n", "remote build context"},
		{"inline", "services:\n  web:\n    build:\n      context: .\n      dockerfile_inline: FROM x\n", "inline Dockerfile"},
		{"extends", "services:\n  base:\n    build: .\n  web:\n    extends: base\n", "uses extends"},
		{"include", "include:\n  - other.yaml\nservices:\n  web:\n    build: .\n", "uses include"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := stacks.DockerfileTarget(t.TempDir(), parseCompose(t, tt.compose), "web")
			var ne *stacks.NotEditableError
			if !errors.As(err, &ne) || ne.Reason != tt.reason {
				t.Fatalf("err = %v, want reason %q", err, tt.reason)
			}
			if want := "Dockerfile can't be edited here: " + tt.reason; err.Error() != want {
				t.Errorf("message = %q", err.Error())
			}
		})
	}
}

func TestDockerfileViewInside(t *testing.T) {
	h := newHarness(t)
	dir := dockerfileStack(t, h, "app", "services:\n  web:\n    build: ./web\n  db:\n    build: ./db\n")
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("FROM alpine\n")
	if err := os.WriteFile(filepath.Join(dir, "web", "Dockerfile"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := h.svc.Dockerfile(context.Background(), "app", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(v.Content, content) || v.Hash != composefile.Hash(content) || v.Path != "web/Dockerfile" || !v.Exists || !v.Editable {
		t.Errorf("view = %+v", v)
	}
	v, err = h.svc.Dockerfile(context.Background(), "app", "db")
	if err != nil {
		t.Fatal(err)
	}
	if v.Exists || v.Hash != "" || !v.Editable || v.Path != "db/Dockerfile" {
		t.Errorf("missing-file view = %+v", v)
	}
}

func TestDockerfileViewOutside(t *testing.T) {
	h := newHarness(t)
	root := filepath.Dir(h.dir) // an allowed root
	shared := filepath.Join(root, "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "Dockerfile"), []byte("FROM busybox\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dockerfileStack(t, h, "app", "services:\n  web:\n    build: "+shared+"\n")
	v, err := h.svc.Dockerfile(context.Background(), "app", "web")
	if err != nil {
		t.Fatal(err)
	}
	if string(v.Content) != "FROM busybox\n" || v.Editable || v.Reason != "outside the stack folder" || v.Path != filepath.Join(shared, "Dockerfile") {
		t.Errorf("view = %+v", v)
	}

	other, _ := filepath.EvalSymlinks(t.TempDir()) // not under any root
	if err := os.WriteFile(filepath.Join(other, "Dockerfile"), []byte("SECRET\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dockerfileStack(t, h, "app2", "services:\n  web:\n    build: "+other+"\n")
	v, err = h.svc.Dockerfile(context.Background(), "app2", "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Content) != 0 || v.Editable || v.Reason != "outside the allowed directories" || v.Hash != "" {
		t.Errorf("view = %+v", v)
	}
}

func TestDockerfileViewNotEditable(t *testing.T) {
	h := newHarness(t)
	dockerfileStack(t, h, "app", "services:\n  web:\n    build: ./${X}\n  img:\n    image: x\n")
	v, err := h.svc.Dockerfile(context.Background(), "app", "web")
	if err != nil || v.Editable || v.Reason != "uses variables" || len(v.Content) != 0 {
		t.Errorf("view = %+v, err = %v", v, err)
	}
	if _, err := h.svc.Dockerfile(context.Background(), "app", "img"); !errors.Is(err, stacks.ErrNoBuild) {
		t.Errorf("err = %v, want ErrNoBuild", err)
	}
}

func TestSaveDockerfileHashConflict(t *testing.T) {
	h := newHarness(t)
	dir := dockerfileStack(t, h, "app", "services:\n  web:\n    build: ./web\n  api:\n    build: ./api\n")
	ctx := context.Background()
	file := filepath.Join(dir, "web", "Dockerfile")

	// "" on a missing file creates it with the managed modes.
	hash, err := h.svc.SaveDockerfile(ctx, "app", "web", []byte("FROM a\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if hash != composefile.Hash([]byte("FROM a\n")) {
		t.Errorf("hash = %s", hash)
	}
	fi, _ := os.Stat(file)
	di, _ := os.Stat(filepath.Dir(file))
	if fi.Mode().Perm() != 0o644 || di.Mode().Perm() != 0o755 {
		t.Errorf("modes file %v dir %v", fi.Mode().Perm(), di.Mode().Perm())
	}

	for _, stale := range []string{"", "wrong"} {
		if _, err := h.svc.SaveDockerfile(ctx, "app", "web", []byte("FROM b\n"), stale); !errors.Is(err, stacks.ErrDockerfileConflict) {
			t.Errorf("hash %q: err = %v, want conflict", stale, err)
		}
	}
	if got, _ := os.ReadFile(file); string(got) != "FROM a\n" {
		t.Errorf("file changed to %q", got)
	}
	if _, err := h.svc.SaveDockerfile(ctx, "app", "web", []byte("FROM b\n"), hash); err != nil {
		t.Errorf("matching hash: %v", err)
	}
	// A hash for a file that does not exist is stale too.
	if _, err := h.svc.SaveDockerfile(ctx, "app", "api", []byte("x"), "abc"); !errors.Is(err, stacks.ErrDockerfileConflict) {
		t.Errorf("hash for missing file: err = %v, want conflict", err)
	}
}

func TestSaveDockerfileRefusesSymlinkEscape(t *testing.T) {
	h := newHarness(t)
	dir := dockerfileStack(t, h, "app", "services:\n  web:\n    build: ./web\n")
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.Symlink(outside, filepath.Join(dir, "web")); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.SaveDockerfile(context.Background(), "app", "web", []byte("FROM x\n"), "")
	if !errors.Is(err, stacks.ErrOutsideStack) {
		t.Fatalf("err = %v, want ErrOutsideStack", err)
	}
	if strings.Contains(err.Error(), outside) {
		t.Errorf("error echoes the target: %v", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("outside dir has %d entries", len(entries))
	}
}

func TestSaveDockerfileRefusesSymlinkFile(t *testing.T) {
	h := newHarness(t)
	dir := dockerfileStack(t, h, "app", "services:\n  web:\n    build: ./web\n  api:\n    build: ./api\n")
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "web", "Dockerfile")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SaveDockerfile(context.Background(), "app", "web", []byte("FROM x\n"), composefile.Hash([]byte("keep\n"))); err == nil {
		t.Fatal("expected refusal")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "real")); string(got) != "keep\n" {
		t.Errorf("link target changed: %q", got)
	}

	// A link to a file outside the stack is refused and nothing is written there.
	target := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(target, []byte("root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "api", "Dockerfile")); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.SaveDockerfile(context.Background(), "app", "api", []byte("FROM x\n"), composefile.Hash([]byte("root\n")))
	if !errors.Is(err, stacks.ErrOutsideStack) || strings.Contains(err.Error(), target) {
		t.Errorf("err = %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "root\n" {
		t.Errorf("outside file changed: %q", got)
	}
	v, err := h.svc.Dockerfile(context.Background(), "app", "api")
	if err != nil || len(v.Content) != 0 || v.Editable {
		t.Errorf("view through outside link = %+v, %v", v, err)
	}
}

func TestSaveDockerfileOutsideStack(t *testing.T) {
	h := newHarness(t)
	dockerfileStack(t, h, "app", "services:\n  web:\n    build: ../other\n")
	_, err := h.svc.SaveDockerfile(context.Background(), "app", "web", []byte("FROM x\n"), "")
	if !errors.Is(err, stacks.ErrOutsideStack) {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(h.dir), "stacks", "other")); statErr == nil {
		t.Error("created a folder outside the stack")
	}
}

func TestSaveDockerfileLimits(t *testing.T) {
	h := newHarness(t)
	dockerfileStack(t, h, "app", "services:\n  web:\n    build: ./web\n")
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		content []byte
		msg     string
	}{
		{"too large", bytes.Repeat([]byte("a"), 1<<20+1), "Dockerfile is larger than 1 MiB"},
		{"NUL", []byte("FROM x\x00"), "Dockerfile must not contain NUL bytes"},
		{"empty", nil, "Dockerfile is required"},
		{"whitespace only", []byte(" \n\t\r\n"), "Dockerfile is required"},
	} {
		_, err := h.svc.SaveDockerfile(ctx, "app", "web", tc.content, "")
		var ve *stacks.ValidationError
		if !errors.As(err, &ve) || len(ve.Fields) != 1 || ve.Fields[0].Field != "content" || ve.Fields[0].Message != tc.msg {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
	if _, err := h.svc.SaveDockerfile(ctx, "app", "web", bytes.Repeat([]byte("a"), 1<<20), ""); err != nil {
		t.Errorf("exactly 1 MiB: %v", err)
	}
}

func TestWriteDockerfileRollback(t *testing.T) {
	stack, _ := filepath.EvalSymlinks(t.TempDir())
	nested := filepath.Join(stack, "a", "b", "Dockerfile")
	_, rollback, err := stacks.WriteDockerfile(stack, nested, []byte("FROM x\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	rollback()
	if _, err := os.Stat(filepath.Join(stack, "a")); !os.IsNotExist(err) {
		t.Errorf("created folders survived rollback: %v", err)
	}

	existing := filepath.Join(stack, "Dockerfile")
	if err := os.WriteFile(existing, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hash := composefile.Hash([]byte("old\n"))
	_, rollback, err = stacks.WriteDockerfile(stack, existing, []byte("new\n"), &hash)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(existing); fi.Mode().Perm() != 0o600 {
		t.Errorf("existing mode not kept: %v", fi.Mode().Perm())
	}
	rollback()
	got, _ := os.ReadFile(existing)
	fi, _ := os.Stat(existing)
	if string(got) != "old\n" || fi.Mode().Perm() != 0o600 {
		t.Errorf("after rollback: %q %v", got, fi.Mode().Perm())
	}
}

func TestDockerfileViewOutsideUnreadable(t *testing.T) {
	h := newHarness(t)
	root := filepath.Dir(h.dir)
	big := filepath.Join(root, "big")
	dir := filepath.Join(root, "adir")
	for _, d := range []string{big, filepath.Join(dir, "Dockerfile")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(big, "Dockerfile"), bytes.Repeat([]byte("a"), 1<<20+1), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, ctxDir := range map[string]string{"big": big, "dir": dir} {
		dockerfileStack(t, h, name, "services:\n  web:\n    build: "+ctxDir+"\n")
		v, err := h.svc.Dockerfile(context.Background(), name, "web")
		if err != nil {
			t.Fatal(err)
		}
		if len(v.Content) != 0 || v.Editable || v.Reason != "outside the allowed directories" {
			t.Errorf("%s: view = %+v", name, v)
		}
	}
}

func TestDockerfileInsideOddFiles(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dir := dockerfileStack(t, h, "app", "services:\n  dir:\n    build: ./dir\n  big:\n    build: ./big\n  link:\n    build: ./link\n  file:\n    build: ./file\n")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "dir", "Dockerfile"), 0o755))
	must(os.MkdirAll(filepath.Join(dir, "big"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "big", "Dockerfile"), bytes.Repeat([]byte("a"), 1<<20+1), 0o644))
	must(os.MkdirAll(filepath.Join(dir, "link"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "real"), []byte("x\n"), 0o644))
	must(os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "link", "Dockerfile")))
	must(os.WriteFile(filepath.Join(dir, "file"), []byte("not a folder\n"), 0o644))

	for svc, reason := range map[string]string{
		"dir": "not a regular file", "link": "not a regular file",
		"file": "not a regular file", "big": "larger than 1 MiB",
	} {
		v, err := h.svc.Dockerfile(ctx, "app", svc)
		if err != nil || v.Editable || v.Reason != reason || len(v.Content) != 0 {
			t.Errorf("%s view = %+v, %v; want reason %q", svc, v, err, reason)
		}
		_, err = h.svc.SaveDockerfile(ctx, "app", svc, []byte("FROM x\n"), composefile.Hash([]byte("x\n")))
		var ne *stacks.NotEditableError
		switch svc {
		case "link":
			if !errors.Is(err, stacks.ErrOutsideStack) {
				t.Errorf("link save err = %v, want 403 error", err)
			}
		default:
			if !errors.As(err, &ne) || ne.Reason != reason || strings.Contains(err.Error(), dir) {
				t.Errorf("%s save err = %v, want reason %q", svc, err, reason)
			}
		}
	}
}

func TestDockerfileViewPathThroughSymlinkedStack(t *testing.T) {
	h := newHarness(t)
	real := filepath.Join(filepath.Dir(h.dir), "real-stack")
	if err := os.MkdirAll(filepath.Join(real, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(h.dir, "app")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	compose := filepath.Join(link, "compose.yaml")
	if err := os.WriteFile(compose, []byte("services:\n  web:\n    build: "+real+"/web\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.reg.Add(context.Background(), stacks.Entry{Name: "app", ComposePath: compose, Origin: stacks.OriginManaged, Project: "app"}); err != nil {
		t.Fatal(err)
	}
	v, err := h.svc.Dockerfile(context.Background(), "app", "web")
	if err != nil || !v.Editable || v.Path != "web/Dockerfile" {
		t.Errorf("view = %+v, %v", v, err)
	}
}

func TestWriteDockerfileInRootRefusesEscape(t *testing.T) {
	stack, _ := filepath.EvalSymlinks(t.TempDir())
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.Symlink(outside, filepath.Join(stack, "web")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(stack)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	_, _, err = stacks.WriteDockerfileInRoot(root, "web/sub/Dockerfile", []byte("FROM x\n"), nil)
	if !errors.Is(err, stacks.ErrOutsideStack) || strings.Contains(err.Error(), outside) {
		t.Fatalf("err = %v, want ErrOutsideStack", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("outside dir has %d entries", len(entries))
	}
}
