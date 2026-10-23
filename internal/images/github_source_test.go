package images

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/images/github"
	"github.com/rcss-labs/stackstead/internal/secret"
)

const (
	testToken = "ghp_supersecrettoken"
	headSHA   = "0123456789abcdef0123456789abcdef01234567"
	tagSHA    = "fedcba9876543210fedcba9876543210fedcba98"
)

type fakeGitHub struct {
	login      string
	heads      map[string]string // branch or tag -> sha
	files      map[string]string // path -> content
	tarball    []byte
	tarballErr error
	userErr    error

	gotTokens  []secret.Secret
	headCalls  []string
	tarballRef string
	onHead     func()
	onUser     func() // runs inside User, before it answers
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{login: "octocat", heads: map[string]string{"main": headSHA, "v1": tagSHA}, files: map[string]string{}}
}

func (f *fakeGitHub) User(_ context.Context, t secret.Secret) (string, error) {
	f.gotTokens = append(f.gotTokens, t)
	if f.onUser != nil {
		f.onUser()
	}
	return f.login, f.userErr
}

func (f *fakeGitHub) Head(_ context.Context, t secret.Secret, _, branch string) (string, error) {
	f.gotTokens = append(f.gotTokens, t)
	f.headCalls = append(f.headCalls, branch)
	if f.onHead != nil {
		f.onHead()
	}
	sha, ok := f.heads[branch]
	if !ok {
		return "", github.ErrNotFound
	}
	return sha, nil
}

func (f *fakeGitHub) File(_ context.Context, _ secret.Secret, _, _, p string) ([]byte, error) {
	c, ok := f.files[p]
	if !ok {
		return nil, github.ErrNotFound
	}
	return []byte(c), nil
}

func (f *fakeGitHub) Tarball(_ context.Context, _ secret.Secret, _, ref string) (io.ReadCloser, error) {
	f.tarballRef = ref
	if f.tarballErr != nil {
		return nil, f.tarballErr
	}
	return io.NopCloser(bytes.NewReader(f.tarball)), nil
}

func repoTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, c := range files {
		if err := tw.WriteHeader(&tar.Header{Name: "octo-repo-" + headSHA[:7] + "/" + name, Mode: 0o644, Size: int64(len(c)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(c))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func (h *harness) setToken(t *testing.T) {
	t.Helper()
	if err := h.svc.SetGitHubToken(context.Background(), "alice", secret.Secret(testToken)); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) createGitHub(t *testing.T, name string) Image {
	t.Helper()
	img, err := h.svc.Create(context.Background(), "alice", CreateRequest{Name: name, Source: SourceGitHub,
		GitHub: GitHubSource{Repo: "octo/repo", Branch: "main", Path: "app"}})
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func (h *harness) buildRef(t *testing.T, slug, tag, ref string) Version {
	t.Helper()
	v, op, err := h.svc.Build(context.Background(), "alice", slug, BuildRequest{Tag: tag, Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	h.waitOp(t, op.ID)
	got, err := h.svc.store.VersionByID(context.Background(), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestGitHubBuildRecordsCommit(t *testing.T) {
	h := newHarness(t)
	h.setToken(t)
	h.gh.tarball = repoTarball(t, map[string]string{"app/Dockerfile": "FROM alpine\n", "app/run.sh": "x", "other/secret.txt": "no"})
	h.createGitHub(t, "gh app")
	v := h.buildRef(t, "gh-app", "1", "")
	if v.Status != VersionSucceeded || v.Commit != headSHA || v.Revision != 0 {
		t.Fatalf("version = %+v", v)
	}
	if h.gh.tarballRef != headSHA {
		t.Fatalf("tarball ref = %q", h.gh.tarballRef)
	}
	if h.eng.files["Dockerfile"] != "FROM alpine\n" || h.eng.files["run.sh"] != "x" {
		t.Fatalf("context = %v", h.eng.files)
	}
	if _, ok := h.eng.files["other/secret.txt"]; ok {
		t.Fatal("context leaked files outside the path")
	}
	if m, _ := filepath.Glob(filepath.Join(h.dir, "gh-app", "tmp-*")); len(m) != 0 {
		t.Fatalf("temp dirs left: %v", m)
	}
}

func TestGitHubBuildAtCommit(t *testing.T) {
	h := newHarness(t)
	h.setToken(t)
	h.gh.tarball = repoTarball(t, map[string]string{"app/Dockerfile": "FROM alpine\n"})
	h.createGitHub(t, "gh app")
	pinned := strings.Repeat("ab", 20)
	v := h.buildRef(t, "gh-app", "1", pinned)
	if v.Commit != pinned || h.gh.tarballRef != pinned || len(h.gh.headCalls) != 0 {
		t.Fatalf("commit=%q ref=%q heads=%v", v.Commit, h.gh.tarballRef, h.gh.headCalls)
	}
	// A tag or other branch name is resolved to a commit first.
	v = h.buildRef(t, "gh-app", "2", "v1")
	if v.Commit != tagSHA || h.gh.tarballRef != tagSHA {
		t.Fatalf("commit=%q ref=%q", v.Commit, h.gh.tarballRef)
	}
}

func TestGitHubBuildFailureMessages(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{github.ErrArchiveTooLarge, "repository archive is too large"},
		{github.ErrStalled, "GitHub download stalled"},
		{github.ErrRateLimited, "GitHub rate limit reached, try again later"},
		{github.ErrUnauthorized, "GitHub rejected the token"},
	}
	for _, c := range cases {
		h := newHarness(t)
		h.setToken(t)
		h.createGitHub(t, "gh app")
		h.gh.tarballErr = c.err
		v := h.buildRef(t, "gh-app", "1", "")
		if v.Status != VersionFailed || v.Error != c.want {
			t.Fatalf("%v: version = %+v", c.err, v)
		}
		log, _ := h.svc.readLog("gh-app", v.ID)
		if !strings.Contains(log, c.want) || strings.Contains(log, testToken) {
			t.Fatalf("log = %q", log)
		}
	}
}

func TestGitHubBuildBadContentRemovesTemp(t *testing.T) {
	h := newHarness(t)
	h.setToken(t)
	h.createGitHub(t, "gh app")
	h.gh.tarball = repoTarball(t, map[string]string{"app/big.bin": strings.Repeat("x", 2<<20), "app/Dockerfile": "FROM a\n"})
	v := h.buildRef(t, "gh-app", "1", "")
	if v.Status != VersionFailed || !strings.Contains(v.Error, "per-file limit") {
		t.Fatalf("version = %+v", v)
	}
	if m, _ := filepath.Glob(filepath.Join(h.dir, "gh-app", "tmp-*")); len(m) != 0 {
		t.Fatalf("temp dirs left: %v", m)
	}
}

func TestGitHubHeadView(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.createGitHubNoToken(t)
	if _, err := h.svc.GitHubHead(ctx, "gh-app"); !errors.Is(err, ErrGitHubUnconfigured) {
		t.Fatalf("err = %v", err)
	}
	h.setToken(t)
	if _, err := h.svc.GitHubHead(ctx, "gh-app"); !isFieldError(err, "github.path") {
		t.Fatalf("missing file err = %v", err)
	}
	h.gh.files["app/Dockerfile"] = "FROM alpine\n"
	got, err := h.svc.GitHubHead(ctx, "gh-app")
	if err != nil {
		t.Fatal(err)
	}
	if got.Commit != headSHA || got.Dockerfile != "FROM alpine\n" || got.Path != "app/Dockerfile" {
		t.Fatalf("head = %+v", got)
	}
}

// createGitHubNoToken inserts a GitHub image straight into the store, as an
// image created before the token was cleared would exist.
func (h *harness) createGitHubNoToken(t *testing.T) {
	t.Helper()
	if _, err := h.svc.store.CreateImage(context.Background(), Image{Slug: "gh-app", Name: "gh app", Source: SourceGitHub,
		DockerfilePath: "Dockerfile", GitHub: GitHubSource{Repo: "octo/repo", Branch: "main", Path: "app"}}); err != nil {
		t.Fatal(err)
	}
}

func isFieldError(err error, field string) bool {
	var ve *ValidationError
	if !errors.As(err, &ve) {
		return false
	}
	for _, f := range ve.Fields {
		if f.Field == field {
			return true
		}
	}
	return false
}

func TestGitHubImageNeedsToken(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	req := CreateRequest{Name: "gh app", Source: SourceGitHub, GitHub: GitHubSource{Repo: "octo/repo", Branch: "main"}}
	if _, err := h.svc.Create(ctx, "alice", req); !errors.Is(err, ErrGitHubUnconfigured) {
		t.Fatalf("create err = %v", err)
	}
	h.setToken(t)
	img, err := h.svc.Create(ctx, "alice", req)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.SetGitHubToken(ctx, "alice", ""); err != nil {
		t.Fatal(err)
	}
	changed := img.GitHub
	changed.Branch = "dev"
	_, err = h.svc.Update(ctx, "alice", img.Slug, UpdateRequest{Name: "gh app", GitHub: changed})
	if !errors.Is(err, ErrGitHubUnconfigured) {
		t.Fatalf("update err = %v", err)
	}
}

func TestUpdateGitHubImageWithoutToken(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.setToken(t)
	img, err := h.svc.Create(ctx, "alice", CreateRequest{Name: "gh app", Source: SourceGitHub,
		GitHub: GitHubSource{Repo: "octo/repo", Branch: "main", Path: "app"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.SetGitHubToken(ctx, "alice", ""); err != nil {
		t.Fatal(err)
	}
	renamed := UpdateRequest{Name: "renamed", GitHub: img.GitHub, Retention: img.Retention}
	if _, err := h.svc.Update(ctx, "alice", img.Slug, renamed); err != nil {
		t.Fatalf("update without GitHub change: %v", err)
	}
	got, err := h.svc.Get(ctx, img.Slug)
	if err != nil || got.Name != "renamed" {
		t.Fatalf("image %+v err %v", got, err)
	}
	for field, mutate := range map[string]func(*GitHubSource){
		"repo":   func(g *GitHubSource) { g.Repo = "octo/other" },
		"branch": func(g *GitHubSource) { g.Branch = "dev" },
		"path":   func(g *GitHubSource) { g.Path = "other" },
	} {
		gs := img.GitHub
		mutate(&gs)
		if _, err := h.svc.Update(ctx, "alice", img.Slug, UpdateRequest{Name: "renamed", GitHub: gs}); !errors.Is(err, ErrGitHubUnconfigured) {
			t.Errorf("changing %s: err = %v", field, err)
		}
	}
}

func TestGitHubImageValidatesSource(t *testing.T) {
	h := newHarness(t)
	h.setToken(t)
	for field, gs := range map[string]GitHubSource{
		"github.repo":   {Repo: "not a repo", Branch: "main"},
		"github.branch": {Repo: "octo/repo", Branch: "bad branch"},
		"github.path":   {Repo: "octo/repo", Branch: "main", Path: "../etc"},
	} {
		_, err := h.svc.Create(context.Background(), "alice", CreateRequest{Name: "gh app", Source: SourceGitHub, GitHub: gs})
		if !isFieldError(err, field) {
			t.Fatalf("%s: err = %v", field, err)
		}
	}
}

func TestGitHubImageHasNoRevisions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.setToken(t)
	h.createGitHub(t, "gh app")
	checks := map[string]func() error{
		"files": func() error { _, _, err := h.svc.Files(ctx, "gh-app"); return err },
		"save":  func() error { _, err := h.svc.SaveDockerfile(ctx, "a", "gh-app", []byte("FROM a"), 0); return err },
		"upload": func() error {
			_, err := h.svc.Upload(ctx, "a", "gh-app", []UploadFile{upload("x", "y")})
			return err
		},
		"rename":        func() error { _, err := h.svc.Rename(ctx, "a", "gh-app", "a", "b"); return err },
		"delete":        func() error { _, err := h.svc.DeleteFile(ctx, "a", "gh-app", "a"); return err },
		"restore":       func() error { _, err := h.svc.Restore(ctx, "a", "gh-app", 1); return err },
		"readfile":      func() error { _, err := h.svc.ReadFile(ctx, "gh-app", "Dockerfile"); return err },
		"dockerfile":    func() error { _, _, err := h.svc.Dockerfile(ctx, "gh-app"); return err },
		"revdockerfile": func() error { _, err := h.svc.RevisionDockerfile(ctx, "gh-app", 1); return err },
		"revisions":     func() error { _, err := h.svc.Revisions(ctx, "gh-app"); return err },
	}
	for name, f := range checks {
		err := f()
		var ve *ValidationError
		if !errors.As(err, &ve) || !strings.Contains(err.Error(), "GitHub images are read-only") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestSetGitHubTokenLogsWithoutValue(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if st, _ := h.svc.GitHubStatus(ctx); st.Configured {
		t.Fatal("configured before a token is set")
	}
	h.setToken(t)
	e, ok := h.log.last("github.update")
	if !ok || e.Result != "set" || e.Scope != "host" {
		t.Fatalf("entry = %+v", e)
	}
	login, err := h.svc.TestGitHub(ctx)
	if err != nil || login != "octocat" {
		t.Fatalf("login = %q err = %v", login, err)
	}
	if st, _ := h.svc.GitHubStatus(ctx); !st.Configured || st.Login != "octocat" {
		t.Fatalf("status = %+v", st)
	}
	if len(h.gh.gotTokens) == 0 || h.gh.gotTokens[0].Reveal() != testToken {
		t.Fatal("token was not passed to GitHub")
	}
	if err := h.svc.SetGitHubToken(ctx, "alice", ""); err != nil {
		t.Fatal(err)
	}
	e, _ = h.log.last("github.update")
	if e.Result != "cleared" {
		t.Fatalf("entry = %+v", e)
	}
	if st, _ := h.svc.GitHubStatus(ctx); st.Configured || st.Login != "" {
		t.Fatalf("status after clear = %+v", st)
	}
	for _, e := range h.log.entries {
		if strings.Contains(e.Detail+e.Target+e.Result+e.Scope, testToken) {
			t.Fatalf("token leaked into log: %+v", e)
		}
	}
	if _, err := h.svc.TestGitHub(ctx); !errors.Is(err, ErrGitHubUnconfigured) {
		t.Fatalf("test without token err = %v", err)
	}
}

func TestTestGitHubDoesNotStoreLoginOfReplacedToken(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.setToken(t)
	inCall, release := make(chan struct{}), make(chan struct{})
	h.gh.onUser = func() {
		close(inCall)
		<-release
	}
	errc := make(chan error, 1)
	go func() {
		_, err := h.svc.TestGitHub(ctx)
		errc <- err
	}()
	<-inCall
	if err := h.svc.SetGitHubToken(ctx, "alice", "ghp_replacement"); err != nil {
		t.Fatal(err)
	}
	close(release)
	var ve *ValidationError
	if err := <-errc; !errors.As(err, &ve) {
		t.Fatalf("test of a replaced token err = %v; want a validation error asking to test again", err)
	}
	if st, _ := h.svc.GitHubStatus(ctx); !st.Configured || st.Login != "" {
		t.Fatalf("status = %+v; want the new token configured with no login", st)
	}
}

func TestTestGitHubFailureKeepsNoLogin(t *testing.T) {
	h := newHarness(t)
	h.setToken(t)
	h.gh.userErr = github.ErrUnauthorized
	_, err := h.svc.TestGitHub(context.Background())
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("err = %v", err)
	}
	if st, _ := h.svc.GitHubStatus(context.Background()); st.Login != "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestGitHubBuildRepoChangedUnderLock(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.setToken(t)
	img := h.createGitHub(t, "gh app")
	h.svc.beforeBuildLock = func() {
		img.GitHub.Branch = "dev"
		if err := h.svc.store.UpdateImage(ctx, img); err != nil {
			t.Error(err)
		}
	}
	_, _, err := h.svc.Build(ctx, "alice", "gh-app", BuildRequest{Tag: "1"})
	if !isFieldError(err, "github") {
		t.Fatalf("err = %v", err)
	}
	if vs, _ := h.svc.store.Versions(ctx, img.ID); len(vs) != 0 {
		t.Fatalf("versions = %d", len(vs))
	}
}

func TestGitHubBuildUnknownRef(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.setToken(t)
	img := h.createGitHub(t, "gh app")
	if _, _, err := h.svc.Build(ctx, "alice", "gh-app", BuildRequest{Tag: "1", Ref: "nope"}); !isFieldError(err, "ref") {
		t.Fatalf("err = %v", err)
	}
	delete(h.gh.heads, "main")
	if _, _, err := h.svc.Build(ctx, "alice", "gh-app", BuildRequest{Tag: "1"}); !isFieldError(err, "github.branch") ||
		!strings.Contains(err.Error(), "repository or branch not found") {
		t.Fatalf("err = %v", err)
	}
	if vs, _ := h.svc.store.Versions(ctx, img.ID); len(vs) != 0 {
		t.Fatalf("versions = %d", len(vs))
	}
}

func TestBuildRefRefusedForUploadImage(t *testing.T) {
	h := newHarness(t)
	h.create(t, "app")
	_, _, err := h.svc.Build(context.Background(), "alice", "app", BuildRequest{Tag: "1", Ref: "main"})
	if !isFieldError(err, "ref") {
		t.Fatalf("err = %v", err)
	}
}

func TestGitHubTokenClearedMidBuild(t *testing.T) {
	h := newHarness(t)
	h.setToken(t)
	h.createGitHub(t, "gh app")
	h.gh.onHead = func() {
		if err := h.svc.SetGitHubToken(context.Background(), "alice", ""); err != nil {
			t.Error(err)
		}
	}
	v := h.buildRef(t, "gh-app", "1", "")
	if v.Status != VersionFailed || !strings.Contains(v.Error, "no GitHub token") {
		t.Fatalf("version = %+v", v)
	}
	if m, _ := filepath.Glob(filepath.Join(h.dir, "gh-app", "tmp-*")); len(m) != 0 {
		t.Fatalf("temp dirs left: %v", m)
	}
}
