package api_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/imageref"
	"github.com/rcss-labs/stackstead/internal/images"
	"github.com/rcss-labs/stackstead/internal/peers"
	"github.com/rcss-labs/stackstead/internal/registry"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
)

const imgInstanceID = "0123456789abcdef0123"

var imgOwner = imageref.OwnerID(imgInstanceID)

// imgEngine is a minimal images.Engine: builds succeed (optionally after a
// gate) and every built tag is present.
type imgEngine struct {
	mu      sync.Mutex
	present map[string]bool
	gate    chan struct{}
	started chan struct{}
}

func (f *imgEngine) Build(ctx context.Context, in io.Reader, opts engine.BuildOptions, out func(string)) (engine.BuildResult, error) {
	tr := tar.NewReader(in)
	for {
		if _, err := tr.Next(); err != nil {
			break
		}
	}
	f.mu.Lock()
	gate, started := f.gate, f.started
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return engine.BuildResult{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range opts.Tags {
		f.present[t] = true
	}
	out("built")
	return engine.BuildResult{ImageID: "sha256:abc"}, nil
}
func (f *imgEngine) Push(context.Context, string, engine.RegistryAuth, func(string)) (string, error) {
	return "", errors.New("unused")
}
func (f *imgEngine) TagImage(context.Context, string, string, string) error { return nil }
func (f *imgEngine) SaveImage(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("unused")
}
func (f *imgEngine) LoadImage(context.Context, io.Reader) error { return nil }
func (f *imgEngine) InspectImage(_ context.Context, ref string) (engine.ImageInspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.present[ref] {
		return engine.ImageInspect{}, engine.ErrImageNotFound
	}
	return engine.ImageInspect{Image: engine.Image{ID: "sha256:abc", Size: 10}, Architecture: "amd64"}, nil
}
func (f *imgEngine) RemoveImage(_ context.Context, ref string) (engine.ImageDeleteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.present, ref)
	return engine.ImageDeleteResult{}, nil
}
func (f *imgEngine) Arch(context.Context) (string, error) { return "amd64", nil }

type imgLog struct {
	mu      sync.Mutex
	entries []actionlog.Entry
}

func (l *imgLog) Append(_ context.Context, e actionlog.Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, e)
	return nil
}

func (l *imgLog) last(kind string) (actionlog.Entry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.entries) - 1; i >= 0; i-- {
		if l.entries[i].Kind == kind {
			return l.entries[i], true
		}
	}
	return actionlog.Entry{}, false
}

type imgUsage struct{ usages []imageref.Usage }

func (u *imgUsage) LibraryUsage(context.Context) ([]imageref.Usage, error) { return u.usages, nil }

type imagesEnv struct {
	*testEnv
	cookie *http.Cookie
	svc    *images.Service
	eng    *imgEngine
	log    *imgLog
	usage  *imgUsage
	ops    *stacks.Ops
	dir    string
	regs   *registry.Service
	prov   *fakeRegistryProvider
	gh     *fakeGitHub
}

func newImagesEnv(t *testing.T, tweaks ...func(*api.Options)) *imagesEnv {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "images.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ie := &imagesEnv{eng: &imgEngine{present: map[string]bool{}}, log: &imgLog{}, usage: &imgUsage{}, dir: t.TempDir()}
	ie.ops = stacks.NewOps(time.Minute, time.Now)
	t.Cleanup(func() { ie.ops.Shutdown(context.Background()) })
	ie.prov = &fakeRegistryProvider{}
	ie.gh = &fakeGitHub{login: "octocat", head: strings.Repeat("a", 40)}
	ie.regs = registry.NewService(registry.NewStore(db, time.Now), map[registry.Kind]registry.Provider{
		registry.KindDockerHub: ie.prov, registry.KindECR: ie.prov,
	}, ie.log, nil)
	ie.svc = images.NewService(images.Deps{
		Registries: ie.regs,
		Store:      images.NewStore(db, time.Now), Ops: ie.ops, Engine: ie.eng, Dir: ie.dir,
		Limits: images.Limits{MaxFileBytes: 1 << 20, MaxContextBytes: 2 << 20}, OpTimeout: time.Minute,
		Actions: ie.log, Usage: ie.usage, Now: time.Now, GitHub: ie.gh, Settings: images.NewSettingsStore(db),
		Self: func(context.Context) (images.Self, error) {
			return images.Self{InstanceID: imgInstanceID, Name: "Host A", Slug: "host-a"}, nil
		},
	})
	ie.testEnv = newEnvWith(t, "", func(o *api.Options) {
		o.Images = ie.svc
		o.Registries = ie.regs
		o.Arch = func(context.Context) (string, error) { return "amd64", nil }
		for _, tw := range tweaks {
			tw(o)
		}
	})
	ie.createAdmin(t)
	ie.cookie = ie.login(t)
	return ie
}

func (e *imagesEnv) req(t *testing.T, method, path, body string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	return e.do(t, method, path, body, append([]func(*http.Request){withCookie(e.cookie)}, opts...)...)
}

func (e *imagesEnv) createImage(t *testing.T, name string) apitypes.LibraryImage {
	t.Helper()
	rec := e.req(t, "POST", "/api/images", `{"name":"`+name+`","source":"upload","dockerfilePath":"","buildArgs":{}}`)
	if rec.Code != 201 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var img apitypes.LibraryImage
	decodeBody(t, rec, &img)
	return img
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
}

func (e *imagesEnv) build(t *testing.T, slug, tag string) *httptest.ResponseRecorder {
	t.Helper()
	return e.req(t, "POST", "/api/images/"+slug+"/builds", `{"tag":"`+tag+`","buildArgs":{},"saveArgs":false,"ref":""}`)
}

// buildDone builds a version and waits for its operation to finish.
func (e *imagesEnv) buildDone(t *testing.T, slug, tag string) apitypes.BuildStarted {
	t.Helper()
	rec := e.build(t, slug, tag)
	if rec.Code != 202 {
		t.Fatalf("build = %d %s", rec.Code, rec.Body)
	}
	var started apitypes.BuildStarted
	decodeBody(t, rec, &started)
	waitOp(t, e.ops, started.Operation.ID)
	return started
}

func waitOp(t *testing.T, ops *stacks.Ops, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		op, err := ops.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if op.State != stacks.OpRunning {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("operation did not finish")
}

type multipartPart struct {
	name string
	body string
}

func multipartBody(t *testing.T, parts []multipartPart) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, p := range parts {
		pw, err := w.CreateFormFile(p.name, "blob")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = pw.Write([]byte(p.body))
	}
	_ = w.Close()
	return buf.String(), w.FormDataContentType()
}

func (e *imagesEnv) upload(t *testing.T, slug string, parts []multipartPart) *httptest.ResponseRecorder {
	t.Helper()
	body, ctype := multipartBody(t, parts)
	return e.req(t, "POST", "/api/images/"+slug+"/files", "", func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Set("Content-Type", ctype)
	})
}

func TestImagesRoutesNeedSession(t *testing.T) {
	env := newImagesEnv(t)
	for _, rt := range []struct{ method, path string }{
		{"GET", "/api/images"}, {"POST", "/api/images"}, {"GET", "/api/images/web"},
		{"POST", "/api/images/web/builds"}, {"GET", "/api/images/web/versions/v1"},
		{"DELETE", "/api/images/web/files/a/b"},
	} {
		if rec := env.do(t, rt.method, rt.path, ""); rec.Code != 401 {
			t.Errorf("%s %s = %d, want 401", rt.method, rt.path, rec.Code)
		}
	}
}

func TestImagesRoutesAbsentWithoutService(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	cookie := env.login(t)
	if rec := env.do(t, "GET", "/api/images", "", withCookie(cookie)); rec.Code != 404 {
		t.Errorf("GET /api/images = %d, want 404", rec.Code)
	}
}

func TestCreateImageAndList(t *testing.T) {
	env := newImagesEnv(t)
	img := env.createImage(t, "My Web")
	if img.Slug != "my-web" || img.Owner.ID != imgInstanceID || !img.Owner.Local {
		t.Fatalf("created = %+v", img)
	}
	rec := env.req(t, "GET", "/api/images", "")
	if rec.Code != 200 {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	var lib apitypes.Library
	decodeBody(t, rec, &lib)
	if len(lib.Images) != 1 || lib.Images[0].Slug != "my-web" || len(lib.Servers) != 1 || lib.Servers[0].ID != imgInstanceID {
		t.Fatalf("library = %+v", lib)
	}
	if rec := env.req(t, "POST", "/api/images", `{"name":"My Web","source":"upload","dockerfilePath":"","buildArgs":{}}`); rec.Code != 409 {
		t.Errorf("duplicate = %d, want 409", rec.Code)
	}
	if rec := env.req(t, "POST", "/api/images", `{"name":"","source":"upload","dockerfilePath":"","buildArgs":{}}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), `"fields"`) {
		t.Errorf("blank name = %d %s, want 400 with fields", rec.Code, rec.Body)
	}
	if rec := env.req(t, "GET", "/api/images/nope", ""); rec.Code != 404 {
		t.Errorf("missing = %d, want 404", rec.Code)
	}
}

func TestImageDetailSuggestedTag(t *testing.T) {
	env := newImagesEnv(t)
	env.createImage(t, "web")
	env.buildDone(t, "web", "v1.4")
	rec := env.req(t, "GET", "/api/images/web", "")
	if rec.Code != 200 {
		t.Fatalf("detail = %d %s", rec.Code, rec.Body)
	}
	var d apitypes.LibraryImageDetail
	decodeBody(t, rec, &d)
	if d.SuggestedTag != "v1.5" || d.Arch != "amd64" || d.Revision < 1 || len(d.Versions) != 1 {
		t.Fatalf("detail = %+v", d)
	}
	if d.Versions[0].Ref != imageref.Format(imgOwner, "web", "v1.4") || d.LatestVersion == nil || d.LatestVersion.Tag != "v1.4" {
		t.Fatalf("version = %+v latest = %+v", d.Versions[0], d.LatestVersion)
	}
}

func TestImageJSONHasNoNulls(t *testing.T) {
	env := newImagesEnv(t)
	env.createImage(t, "web")
	for _, path := range []string{"/api/images", "/api/images/web", "/api/images/web/revisions", "/api/images/web/files"} {
		rec := env.req(t, "GET", path, "")
		if rec.Code != 200 {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "null") {
			t.Errorf("%s contains null: %s", path, rec.Body)
		}
	}
}

func TestUpdateAndDeleteImage(t *testing.T) {
	env := newImagesEnv(t)
	env.createImage(t, "web")
	rec := env.req(t, "PUT", "/api/images/web", `{"name":"Web Two","dockerfilePath":"Dockerfile","buildArgs":{"A":"1"},"retention":{"keepLast":3,"keepDays":0}}`)
	if rec.Code != 200 {
		t.Fatalf("update = %d %s", rec.Code, rec.Body)
	}
	var img apitypes.LibraryImage
	decodeBody(t, rec, &img)
	if img.Name != "Web Two" || img.Slug != "web" || img.Retention.KeepLast != 3 || img.BuildArgs["A"] != "1" {
		t.Fatalf("updated = %+v", img)
	}
	if rec := env.req(t, "DELETE", "/api/images/web", ""); rec.Code != 204 {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if rec := env.req(t, "GET", "/api/images/web", ""); rec.Code != 404 {
		t.Errorf("after delete = %d, want 404", rec.Code)
	}
}

func TestUploadFilesAndRevisions(t *testing.T) {
	env := newImagesEnv(t)
	env.createImage(t, "web")
	rec := env.upload(t, "web", []multipartPart{{"f/app/main.txt", "hello"}, {"f/Dockerfile", "FROM alpine\nCOPY app /app\n"}})
	if rec.Code != 201 {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	var rev apitypes.ImageRevision
	decodeBody(t, rec, &rev)
	if rev.Number < 2 || !rev.DockerfileChanged {
		t.Fatalf("revision = %+v", rev)
	}
	rec = env.req(t, "GET", "/api/images/web/files", "")
	var files apitypes.ImageFiles
	decodeBody(t, rec, &files)
	if files.Revision != rev.Number || len(files.Files) != 2 {
		t.Fatalf("files = %+v", files)
	}
	rec = env.req(t, "GET", "/api/images/web/files/app/main.txt", "")
	var fc apitypes.ImageFileContent
	decodeBody(t, rec, &fc)
	if rec.Code != 200 || fc.Content != "hello" || fc.Path != "app/main.txt" {
		t.Fatalf("file = %d %+v", rec.Code, fc)
	}
	if rec := env.req(t, "POST", "/api/images/web/files/rename", `{"from":"app/main.txt","to":"app/renamed.txt"}`); rec.Code != 200 {
		t.Fatalf("rename = %d %s", rec.Code, rec.Body)
	}
	if rec := env.req(t, "DELETE", "/api/images/web/files/app/renamed.txt", ""); rec.Code != 200 {
		t.Fatalf("delete file = %d %s", rec.Code, rec.Body)
	}
	if rec := env.req(t, "GET", "/api/images/web/files/app/renamed.txt", ""); rec.Code != 404 {
		t.Fatalf("deleted file = %d, want 404", rec.Code)
	}
	rec = env.req(t, "GET", "/api/images/web/revisions", "")
	var revs []apitypes.ImageRevision
	decodeBody(t, rec, &revs)
	if len(revs) < 4 || revs[0].Number < revs[len(revs)-1].Number {
		t.Fatalf("revisions = %+v", revs)
	}
	rec = env.req(t, "GET", "/api/images/web/revisions/"+itoa(rev.Number), "")
	var detail apitypes.ImageRevisionDetail
	decodeBody(t, rec, &detail)
	if rec.Code != 200 || !strings.Contains(detail.Dockerfile, "COPY app /app") || len(detail.Files) != 2 || detail.Previous == "" {
		t.Fatalf("revision detail = %d %+v", rec.Code, detail)
	}
	if rec := env.req(t, "POST", "/api/images/web/revisions/"+itoa(rev.Number)+"/restore", ""); rec.Code != 200 {
		t.Fatalf("restore = %d %s", rec.Code, rec.Body)
	}
	if rec := env.req(t, "GET", "/api/images/web/revisions/99", ""); rec.Code != 404 {
		t.Fatalf("missing revision = %d", rec.Code)
	}
	if rec := env.req(t, "GET", "/api/images/web/revisions/abc", ""); rec.Code != 400 {
		t.Fatalf("bad revision = %d", rec.Code)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// Review Focus 1: hostile multipart paths answer 400 naming the path, add no
// revision and write nothing outside the image directory.
func TestUploadRouteTraversal(t *testing.T) {
	env := newImagesEnv(t)
	env.createImage(t, "web")
	before := revisionCount(t, env)
	outside := filepath.Join(filepath.Dir(env.dir), "etc")
	for _, name := range []string{"f/../etc/x", "f//abs", "f/a\\b", "f/a\x00b"} {
		rec := env.upload(t, "web", []multipartPart{{"f/ok.txt", "fine"}, {name, "evil"}})
		if rec.Code != 400 {
			t.Errorf("%q: status = %d %s, want 400", name, rec.Code, rec.Body)
			continue
		}
		path := strings.TrimPrefix(name, "f/")
		var body apitypes.ErrorBody
		decodeBody(t, rec, &body)
		// %q escapes NUL and backslash; the parser's own rejection of a NUL header quotes the name the same way.
		if !strings.Contains(body.Error, strings.Trim(fmt.Sprintf("%q", path), `"`)) {
			t.Errorf("%q: error %q does not name the path", name, body.Error)
		}
	}
	if got := revisionCount(t, env); got != before {
		t.Errorf("revisions = %d, want %d", got, before)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Errorf("something was written outside the image dir: %v", err)
	}
	// Everything the service wrote lives under the image dir; no stray "x" file anywhere.
	_ = filepath.Walk(env.dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Base(p) == "x" {
			t.Errorf("hostile file written: %s", p)
		}
		return nil
	})
}

func revisionCount(t *testing.T, env *imagesEnv) int {
	t.Helper()
	rec := env.req(t, "GET", "/api/images/web/revisions", "")
	var revs []apitypes.ImageRevision
	decodeBody(t, rec, &revs)
	return len(revs)
}

func TestUploadTooLarge413(t *testing.T) {
	env := newImagesEnv(t)
	env.createImage(t, "web")
	before := revisionCount(t, env)
	big := strings.Repeat("a", (1<<20)+1)
	rec := env.upload(t, "web", []multipartPart{{"f/big.bin", big}})
	if rec.Code != 413 {
		t.Fatalf("per-file cap = %d %s, want 413", rec.Code, rec.Body)
	}
	var body apitypes.ErrorBody
	decodeBody(t, rec, &body)
	if body.Code != "image_too_large" {
		t.Errorf("code = %q", body.Code)
	}
	assertNoSpool(t, env)
	// Three files under the per-file cap whose sum passes the context cap; the
	// body (about 2.1 MiB) is below the body cap, so only the context cap can trip.
	chunk := strings.Repeat("b", 700<<10)
	rec = env.upload(t, "web", []multipartPart{{"f/1", chunk}, {"f/2", chunk}, {"f/3", chunk}})
	eb := apitypes.ErrorBody{}
	decodeBody(t, rec, &eb)
	if rec.Code != 413 || eb.Code != "image_too_large" || !strings.Contains(eb.Error, "build context") {
		t.Fatalf("context cap = %d %s, want 413 naming the build context", rec.Code, rec.Body)
	}
	assertNoSpool(t, env)
	if got := revisionCount(t, env); got != before {
		t.Errorf("revisions = %d, want %d", got, before)
	}
}

// assertNoSpool checks that no upload scratch directory is left in the image dir.
func assertNoSpool(t *testing.T, env *imagesEnv) {
	t.Helper()
	left, _ := filepath.Glob(filepath.Join(env.dir, "*", "tmp-upload-*"))
	if len(left) > 0 {
		t.Errorf("upload spool left behind: %v", left)
	}
}

func TestUploadSpoolRemovedAndUnknownImage(t *testing.T) {
	env := newImagesEnv(t)
	env.createImage(t, "web")
	if rec := env.upload(t, "web", []multipartPart{{"f/a.txt", "x"}}); rec.Code != 201 {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	assertNoSpool(t, env)
	if rec := env.upload(t, "ghost", []multipartPart{{"f/a.txt", "x"}}); rec.Code != 404 {
		t.Fatalf("unknown image = %d, want 404", rec.Code)
	}
	assertNoSpool(t, env)
}

func TestDockerfileConflict409(t *testing.T) {
	env := newImagesEnv(t)
	env.createImage(t, "web")
	rec := env.req(t, "GET", "/api/images/web/dockerfile", "")
	var df apitypes.ImageDockerfile
	decodeBody(t, rec, &df)
	if rec.Code != 200 || df.Content == "" || df.Revision < 1 {
		t.Fatalf("dockerfile = %d %+v", rec.Code, df)
	}
	body := `{"content":"FROM alpine\n","revision":` + itoa(df.Revision) + `}`
	if rec := env.req(t, "PUT", "/api/images/web/dockerfile", body); rec.Code != 200 {
		t.Fatalf("save = %d %s", rec.Code, rec.Body)
	}
	rec = env.req(t, "PUT", "/api/images/web/dockerfile", body) // stale revision
	var eb apitypes.ErrorBody
	decodeBody(t, rec, &eb)
	if rec.Code != 409 || eb.Code != "revision_conflict" {
		t.Fatalf("stale save = %d %+v", rec.Code, eb)
	}
	if rec := env.req(t, "PUT", "/api/images/web/dockerfile", `{"content":"","revision":9}`); rec.Code != 400 && rec.Code != 409 {
		t.Errorf("empty dockerfile = %d", rec.Code)
	}
}

func TestBuildConflicts(t *testing.T) {
	env := newImagesEnv(t)
	env.createImage(t, "web")
	env.buildDone(t, "web", "1")
	var eb apitypes.ErrorBody
	rec := env.build(t, "web", "1")
	decodeBody(t, rec, &eb)
	if rec.Code != 409 || eb.Code != "tag_taken" {
		t.Fatalf("taken tag = %d %+v", rec.Code, eb)
	}
	rec = env.build(t, "web", "bad tag!")
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"fields"`) {
		t.Fatalf("bad tag = %d %s", rec.Code, rec.Body)
	}

	env.eng.mu.Lock()
	env.eng.gate = make(chan struct{})
	env.eng.started = make(chan struct{}, 1)
	gate := env.eng.gate
	env.eng.mu.Unlock()
	rec = env.build(t, "web", "2")
	if rec.Code != 202 {
		t.Fatalf("first build = %d %s", rec.Code, rec.Body)
	}
	var started apitypes.BuildStarted
	decodeBody(t, rec, &started)
	<-env.eng.started
	rec = env.build(t, "web", "3")
	eb = apitypes.ErrorBody{}
	decodeBody(t, rec, &eb)
	if rec.Code != 409 || eb.Code != "build_running" {
		t.Fatalf("second build = %d %+v", rec.Code, eb)
	}
	close(gate)
	waitOp(t, env.ops, started.Operation.ID)
}

func TestDeleteVersionInUse409Body(t *testing.T) {
	env := newImagesEnv(t)
	env.createImage(t, "web")
	started := env.buildDone(t, "web", "1")
	env.usage.usages = []imageref.Usage{{Stack: "shop", Service: "app", Ref: imageref.Ref{Owner: imgOwner, Slug: "web", Tag: "1"}}}

	rec := env.req(t, "GET", "/api/images/web/versions/"+started.Version.ID, "")
	var vd apitypes.LibraryVersionDetail
	decodeBody(t, rec, &vd)
	if rec.Code != 200 || vd.Tag != "1" || vd.Log == "" || len(vd.UsedBy) != 1 || vd.UsedBy[0].Server != imgInstanceID {
		t.Fatalf("version detail = %d %+v", rec.Code, vd)
	}

	rec = env.req(t, "DELETE", "/api/images/web/versions/"+started.Version.ID, "")
	var body struct {
		Error  string                `json:"error"`
		Code   string                `json:"code"`
		UsedBy []apitypes.LibraryUse `json:"usedBy"`
	}
	decodeBody(t, rec, &body)
	if rec.Code != 409 || body.Code != "image_in_use" || len(body.UsedBy) != 1 || body.UsedBy[0].Stack != "shop" || body.UsedBy[0].Server != imgInstanceID {
		t.Fatalf("in use = %d %s", rec.Code, rec.Body)
	}
	if rec := env.req(t, "DELETE", "/api/images/web", ""); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"image_in_use"`) {
		t.Errorf("delete image in use = %d %s", rec.Code, rec.Body)
	}

	env.usage.usages = nil
	if rec := env.req(t, "DELETE", "/api/images/web/versions/"+started.Version.ID, ""); rec.Code != 204 {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if rec := env.req(t, "GET", "/api/images/web/versions/"+started.Version.ID, ""); rec.Code != 404 {
		t.Fatalf("deleted version = %d, want 404", rec.Code)
	}
}

// libStacks wraps fakeStacks with library references.
type libStacks struct {
	*fakeStacks
	images []string // image of service i
}

func (l *libStacks) List(ctx context.Context) ([]stacks.Summary, error) {
	s := l.sum()
	s.LibraryRefs = nil
	for _, img := range l.images {
		if imageref.IsLibrary(img) {
			s.LibraryRefs = append(s.LibraryRefs, img)
		}
	}
	return []stacks.Summary{s}, nil
}

func (l *libStacks) Get(context.Context, string) (stacks.Detail, error) {
	d := stacks.Detail{Summary: l.sum(), Hash: "h"}
	for i, img := range l.images {
		d.Services = append(d.Services, stacks.ServiceDetail{Name: "svc" + itoa(i), Config: composefile.ServiceConfig{Image: img}})
	}
	return d, nil
}

func TestStacksLibraryBadges(t *testing.T) {
	ref1 := imageref.Format(imgOwner, "web", "1")
	unknown := imageref.Format(imgOwner, "ghost", "1")
	fake := &libStacks{fakeStacks: newFakeStacks(), images: []string{ref1, "nginx:1", unknown}}
	env := newImagesEnv(t, func(o *api.Options) {
		o.Stacks, o.Operations, o.FSRoots, o.Status = fake, fakeOps{fake.fakeStacks}, fake.roots, fake.status
	})
	env.createImage(t, "web")
	env.buildDone(t, "web", "1")
	env.buildDone(t, "web", "2")

	rec := env.req(t, "GET", "/api/stacks", "")
	var list []apitypes.StackSummary
	decodeBody(t, rec, &list)
	if rec.Code != 200 || len(list) != 1 || list[0].LibraryUpdates != 1 {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}

	rec = env.req(t, "GET", "/api/stacks/web", "")
	var d apitypes.StackDetail
	decodeBody(t, rec, &d)
	if rec.Code != 200 || len(d.Services) != 3 {
		t.Fatalf("detail = %d %s", rec.Code, rec.Body)
	}
	if lib := d.Services[0].Library; lib == nil || !lib.Known || !lib.Newer || lib.LatestTag != "2" || lib.Tag != "1" || lib.Ref != ref1 || lib.OwnerName != "Host A" {
		t.Errorf("services[0].library = %+v", lib)
	}
	if d.Services[1].Library != nil {
		t.Errorf("plain image has a badge: %+v", d.Services[1].Library)
	}
	if lib := d.Services[2].Library; lib == nil || lib.Known || lib.Newer {
		t.Errorf("unknown library = %+v", lib)
	}
}

func TestStacksListWhenLibraryBroken(t *testing.T) {
	fake := &libStacks{fakeStacks: newFakeStacks(), images: []string{imageref.Format(imgOwner, "web", "1")}}
	env := newImagesEnv(t, func(o *api.Options) {
		o.Stacks, o.Operations, o.FSRoots, o.Status = fake, fakeOps{fake.fakeStacks}, fake.roots, fake.status
		o.Images = brokenImages{}
	})
	if rec := env.req(t, "GET", "/api/stacks", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"libraryUpdates":0`) {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if rec := env.req(t, "GET", "/api/stacks/web", ""); rec.Code != 200 || strings.Contains(rec.Body.String(), `"library"`) {
		t.Fatalf("detail = %d %s", rec.Code, rec.Body)
	}
}

// brokenImages is an ImagesAPI whose Resolve always fails.
type brokenImages struct{ api.ImagesAPI }

func (brokenImages) Resolve(context.Context, []string) (map[string]images.ResolvedRef, error) {
	return nil, errors.New("library is broken")
}

func TestStatusArch(t *testing.T) {
	env := newImagesEnv(t, func(o *api.Options) {
		fake := newFakeStacks()
		o.Stacks, o.Operations, o.FSRoots, o.Status = fake, fakeOps{fake}, fake.roots, fake.status
	})
	rec := env.req(t, "GET", "/api/status", "")
	var st apitypes.Status
	decodeBody(t, rec, &st)
	if rec.Code != 200 || st.Arch != "amd64" {
		t.Fatalf("status = %d %+v", rec.Code, st)
	}
	failing := newImagesEnv(t, func(o *api.Options) {
		fake := newFakeStacks()
		o.Stacks, o.Operations, o.FSRoots, o.Status = fake, fakeOps{fake}, fake.roots, fake.status
		o.Arch = func(context.Context) (string, error) { return "", errors.New("docker down") }
	})
	rec = failing.req(t, "GET", "/api/status", "")
	if strings.Contains(rec.Body.String(), `"arch"`) {
		t.Errorf("arch on error: %s", rec.Body)
	}
}

func TestPeerCredentialCanManageImages(t *testing.T) {
	clock := &peerTestClock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	st, psvc := newPeerStore(t, clock)
	err := st.InsertPeer(context.Background(), peers.Peer{InstanceID: "inst-a", Name: "mini-01", URL: "http://127.0.0.1:1", State: peers.StateActive}, peers.HashSecret("ssp_good"))
	if err != nil {
		t.Fatal(err)
	}
	env := newImagesEnv(t, func(o *api.Options) { o.Peers = psvc })
	env.createImage(t, "web")

	rec := env.do(t, "POST", "/api/images/web/builds", `{"tag":"1","buildArgs":{},"saveArgs":false,"ref":""}`, peerReq("ssp_good", "pavel")...)
	if rec.Code != 202 {
		t.Fatalf("peer build = %d %s", rec.Code, rec.Body)
	}
	var started apitypes.BuildStarted
	decodeBody(t, rec, &started)
	waitOp(t, env.ops, started.Operation.ID)
	entry, ok := env.log.last("image.build")
	if !ok || entry.Username != "peer:mini-01 (pavel)" {
		t.Fatalf("action entry = %+v ok=%v", entry, ok)
	}
	if rec := env.do(t, "GET", "/api/images", "", peerReq("ssp_good", "")...); rec.Code != 200 {
		t.Errorf("peer list = %d %s", rec.Code, rec.Body)
	}
	if rec := env.do(t, "GET", "/api/images", "", peerReq("ssp_wrong", "")...); rec.Code != 401 {
		t.Errorf("wrong credential = %d, want 401", rec.Code)
	}
}
