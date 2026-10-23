package images

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/imageref"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
)

const (
	testInstanceID = "0123456789abcdef0123"
	testOwner      = "0123456789ab"
)

type fakeEngine struct {
	mu       sync.Mutex
	present  map[string]bool
	removed  []string
	built    []engine.BuildOptions
	files    map[string]string // last build context, path -> content
	lines    []string
	buildErr error
	block    chan struct{} // when set, Build waits for it to close (or ctx)
	started  chan struct{} // closed when Build begins, if set

	calls      []string // tag/push/remove calls, in order
	pushErr    func(auth engine.RegistryAuth) error
	pushDigest string
	pushHook   func() // runs inside Push, before it returns

	saveData string   // bytes SaveImage streams; empty makes it fail
	saved    []string // refs SaveImage was asked for

	arch      string                                       // Arch answer; default arm64
	inspectID map[string]string                            // ref -> ID InspectImage reports
	loadRef   string                                       // ref LoadImage makes present
	loadFn    func(ctx context.Context, r io.Reader) error // replaces the default read-all load
	loaded    []byte                                       // bytes the default load received
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{present: map[string]bool{}, lines: []string{"Step 1/1 : FROM alpine", "built it"}}
}

func (f *fakeEngine) Build(ctx context.Context, tarIn io.Reader, opts engine.BuildOptions, out func(string)) (engine.BuildResult, error) {
	f.mu.Lock()
	started, block := f.started, f.block
	f.mu.Unlock()
	if started != nil {
		close(started)
	}
	files := map[string]string{}
	tr := tar.NewReader(tarIn)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return engine.BuildResult{}, err
		}
		b, _ := io.ReadAll(tr)
		files[h.Name] = string(b)
	}
	for _, l := range f.lines {
		out(l)
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return engine.BuildResult{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.built = append(f.built, opts)
	f.files = files
	if f.buildErr != nil {
		return engine.BuildResult{}, f.buildErr
	}
	for _, t := range opts.Tags {
		f.present[t] = true
	}
	return engine.BuildResult{ImageID: "sha256:abc"}, nil
}

func (f *fakeEngine) Push(_ context.Context, ref string, auth engine.RegistryAuth, out func(string)) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "push "+ref)
	hook, perr, digest := f.pushHook, f.pushErr, f.pushDigest
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	out("layer: Pushing as " + auth.Password)
	if perr != nil {
		return "", perr(auth)
	}
	return digest, nil
}
func (f *fakeEngine) TagImage(_ context.Context, source, repo, tag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "tag "+source+" "+repo+" "+tag)
	return nil
}
func (f *fakeEngine) SaveImage(_ context.Context, ref string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveData == "" {
		return nil, errors.New("not used")
	}
	f.saved = append(f.saved, ref)
	return io.NopCloser(strings.NewReader(f.saveData)), nil
}
func (f *fakeEngine) LoadImage(ctx context.Context, r io.Reader) error {
	f.mu.Lock()
	fn, ref := f.loadFn, f.loadRef
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, r)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loaded = data
	if ref != "" {
		f.present[ref] = true
	}
	return nil
}
func (f *fakeEngine) InspectImage(_ context.Context, ref string) (engine.ImageInspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.present[ref] {
		return engine.ImageInspect{}, engine.ErrImageNotFound
	}
	id := "sha256:abc"
	if v, ok := f.inspectID[ref]; ok {
		id = v
	}
	return engine.ImageInspect{Image: engine.Image{ID: id, Size: 4321}, Architecture: "arm64"}, nil
}
func (f *fakeEngine) RemoveImage(_ context.Context, ref string) (engine.ImageDeleteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, ref)
	f.calls = append(f.calls, "remove "+ref)
	if !f.present[ref] {
		return engine.ImageDeleteResult{}, engine.ErrImageNotFound
	}
	delete(f.present, ref)
	return engine.ImageDeleteResult{}, nil
}
func (f *fakeEngine) Arch(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.arch != "" {
		return f.arch, nil
	}
	return "arm64", nil
}

type recordingLog struct {
	mu      sync.Mutex
	entries []actionlog.Entry
}

func (r *recordingLog) Append(_ context.Context, e actionlog.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
	return nil
}

func (r *recordingLog) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.entries {
		out = append(out, e.Kind)
	}
	return out
}

func (r *recordingLog) last(kind string) (actionlog.Entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.entries) - 1; i >= 0; i-- {
		if r.entries[i].Kind == kind {
			return r.entries[i], true
		}
	}
	return actionlog.Entry{}, false
}

type fakeUsage struct{ usages []imageref.Usage }

func (u *fakeUsage) LibraryUsage(context.Context) ([]imageref.Usage, error) { return u.usages, nil }

type harness struct {
	svc   *Service
	eng   *fakeEngine
	log   *recordingLog
	usage *fakeUsage
	ops   *stacks.Ops
	dir   string
	gh    *fakeGitHub
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	h := &harness{eng: newFakeEngine(), log: &recordingLog{}, usage: &fakeUsage{}, dir: t.TempDir(), gh: newFakeGitHub()}
	h.ops = stacks.NewOps(time.Minute, clock.now)
	t.Cleanup(func() { h.ops.Shutdown(context.Background()) })
	h.svc = NewService(Deps{
		Store: NewStore(db, clock.now), Ops: h.ops, Engine: h.eng, Dir: h.dir,
		Limits: Limits{MaxFileBytes: 1 << 20, MaxContextBytes: 4 << 20}, OpTimeout: time.Minute,
		Actions: h.log, Usage: h.usage, Now: clock.now,
		GitHub: h.gh, Settings: NewSettingsStore(db),
		Self: func(context.Context) (Self, error) {
			return Self{InstanceID: testInstanceID, Name: "Host A", Slug: "host-a"}, nil
		},
	})
	return h
}

func (h *harness) create(t *testing.T, name string) Image {
	t.Helper()
	img, err := h.svc.Create(context.Background(), "alice", CreateRequest{Name: name, Source: SourceUpload, BuildArgs: map[string]string{"A": "1", "B": "2"}})
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func (h *harness) waitOp(t *testing.T, id string) stacks.Operation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		op, err := h.ops.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if op.State != stacks.OpRunning {
			return op
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("operation did not finish")
	return stacks.Operation{}
}

func (h *harness) build(t *testing.T, slug, tag string, args map[string]string) Version {
	t.Helper()
	v, op, err := h.svc.Build(context.Background(), "alice", slug, BuildRequest{Tag: tag, BuildArgs: args})
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

func upload(path, content string) UploadFile {
	return UploadFile{Path: path, Content: strings.NewReader(content)}
}

func TestCreateUploadImageHasTemplateRevision(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	img := h.create(t, "My App")
	if img.Slug != "my-app" || img.DockerfilePath != "Dockerfile" {
		t.Fatalf("image = %+v", img)
	}
	m, rev, err := h.svc.Files(ctx, "my-app")
	if err != nil || rev != 1 || len(m) != 1 {
		t.Fatalf("files %v rev %d err %v", m, rev, err)
	}
	df, _, err := h.svc.Dockerfile(ctx, "my-app")
	if err != nil || string(df) != DefaultDockerfile {
		t.Fatalf("dockerfile %q err %v", df, err)
	}
	if e, ok := h.log.last("image.create"); !ok || e.Scope != "my-app" || e.Username != "alice" || e.Result != "succeeded" {
		t.Fatalf("log = %+v ok=%v", e, ok)
	}
	if _, err := h.svc.Create(ctx, "alice", CreateRequest{Name: "my app", Source: SourceUpload}); !errors.Is(err, ErrImageExists) {
		t.Fatalf("duplicate err = %v", err)
	}
	var ve *ValidationError
	_, err = h.svc.Create(ctx, "alice", CreateRequest{Name: "!!!", Source: SourceUpload})
	if !errors.As(err, &ve) || ve.Fields[0].Field != "name" {
		t.Fatalf("unsluggable name err = %v", err)
	}
	if _, err := h.svc.Get(ctx, "nope"); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("get err = %v", err)
	}
}

func TestUpdateKeepsSlugAndLogs(t *testing.T) {
	h := newHarness(t)
	h.create(t, "My App")
	got, err := h.svc.Update(context.Background(), "alice", "my-app", UpdateRequest{Name: "Renamed", DockerfilePath: "Dockerfile", BuildArgs: map[string]string{"C": "3"}, Retention: Retention{KeepLast: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Slug != "my-app" || got.Name != "Renamed" || got.BuildArgs["C"] != "3" || got.Retention.KeepLast != 3 {
		t.Fatalf("updated = %+v", got)
	}
	if _, ok := h.log.last("image.update"); !ok {
		t.Fatal("image.update not logged")
	}
}

func TestLibraryLocalOnly(t *testing.T) {
	h := newHarness(t)
	h.create(t, "My App")
	lib, err := h.svc.Library(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Images) != 1 || !lib.Images[0].Owner.Local || lib.Images[0].Owner.Slug != "host-a" {
		t.Fatalf("library = %+v", lib)
	}
	e := lib.Images[0]
	if e.Versions == nil || e.PushTargets == nil || e.Pushes == nil {
		t.Fatal("nil list or map in library entry")
	}
	if lib.Servers[0].ID != testInstanceID || e.Owner.ID != testInstanceID {
		t.Fatalf("server ids = %q / %q, want the full instance id", lib.Servers[0].ID, e.Owner.ID)
	}
	if len(lib.Servers) != 1 || lib.Servers[0].Name != "Host A" || lib.Servers[0].Status != "reachable" {
		t.Fatalf("servers = %+v", lib.Servers)
	}
}

func TestSaveDockerfileCreatesRevisionAndConflicts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	r, err := h.svc.SaveDockerfile(ctx, "alice", "app", []byte("FROM busybox\n"), 1)
	if err != nil || r.Number != 2 {
		t.Fatalf("rev %+v err %v", r, err)
	}
	if _, err := h.svc.SaveDockerfile(ctx, "alice", "app", []byte("FROM scratch\n"), 1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("err = %v", err)
	}
	got, n, _ := h.svc.Dockerfile(ctx, "app")
	if n != 2 || string(got) != "FROM busybox\n" {
		t.Fatalf("dockerfile %q rev %d", got, n)
	}
	old, err := h.svc.RevisionDockerfile(ctx, "app", 1)
	if err != nil || string(old) != DefaultDockerfile {
		t.Fatalf("rev 1 dockerfile %q err %v", old, err)
	}
}

func TestUploadRenameDeleteRestore(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	if _, err := h.svc.Upload(ctx, "alice", "app", []UploadFile{upload("a.txt", "hello")}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Rename(ctx, "alice", "app", "a.txt", "b.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.DeleteFile(ctx, "alice", "app", "b.txt"); err != nil {
		t.Fatal(err)
	}
	revs, _ := h.svc.Revisions(ctx, "app")
	var summaries []string
	for i := len(revs) - 1; i >= 0; i-- {
		summaries = append(summaries, revs[i].Summary)
	}
	want := []string{"created from template", "uploaded 1 file", "renamed a.txt → b.txt", "deleted b.txt"}
	if strings.Join(summaries, "|") != strings.Join(want, "|") {
		t.Fatalf("summaries = %q", summaries)
	}
	r, err := h.svc.Restore(ctx, "alice", "app", 2)
	if err != nil || r.Number != 5 {
		t.Fatalf("restore %+v err %v", r, err)
	}
	r2 := revs[len(revs)-2]
	if r.ManifestJSON != r2.ManifestJSON {
		t.Fatalf("restored manifest %s != revision 2 %s", r.ManifestJSON, r2.ManifestJSON)
	}
	content, err := h.svc.ReadFile(ctx, "app", "a.txt")
	if err != nil || string(content) != "hello" {
		t.Fatalf("content %q err %v", content, err)
	}
}

func TestUploadRejectsBadPathWithoutRevision(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	_, err := h.svc.Upload(ctx, "alice", "app", []UploadFile{upload("ok.txt", "fine"), upload("../evil", "x")})
	if !errors.Is(err, ErrBadPath) {
		t.Fatalf("err = %v", err)
	}
	_, err = h.svc.Upload(ctx, "alice", "app", []UploadFile{upload("ok.txt", "orphan-me")})
	if err != nil {
		t.Fatal(err)
	}
	revs, _ := h.svc.Revisions(ctx, "app")
	if len(revs) != 2 {
		t.Fatalf("bad upload created a revision: %d", len(revs))
	}
}

func TestBuildSucceeds(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	if _, err := h.svc.Upload(ctx, "alice", "app", []UploadFile{upload("run.sh", "echo hi")}); err != nil {
		t.Fatal(err)
	}
	v := h.build(t, "app", "1", map[string]string{"B": "override", "C": "3"})
	if v.Status != VersionSucceeded || v.Arch != "arm64" || v.ImageDigest != "sha256:abc" || v.Size != 4321 || v.Revision != 2 || v.OperationID == "" {
		t.Fatalf("version = %+v", v)
	}
	ref := "stackstead.invalid/" + testOwner + "/app:1"
	b := h.eng.built[0]
	if len(b.Tags) != 1 || b.Tags[0] != ref || b.Dockerfile != "Dockerfile" {
		t.Fatalf("opts = %+v", b)
	}
	if b.BuildArgs["A"] != "1" || b.BuildArgs["B"] != "override" || b.BuildArgs["C"] != "3" {
		t.Fatalf("args = %v", b.BuildArgs)
	}
	if b.Labels["stackstead.image"] != "app" || b.Labels["stackstead.version"] != v.ID {
		t.Fatalf("labels = %v", b.Labels)
	}
	if h.eng.files["Dockerfile"] != DefaultDockerfile || h.eng.files["run.sh"] != "echo hi" {
		t.Fatalf("context files = %v", h.eng.files)
	}
	vd, err := h.svc.VersionDetail(ctx, "app", v.ID)
	if err != nil || !strings.Contains(vd.Log, "built it") {
		t.Fatalf("log %q err %v", vd.Log, err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "app", "logs", v.ID+".log")); err != nil {
		t.Fatal(err)
	}
	if e, ok := h.log.last("image.build"); !ok || e.Result != "succeeded" || e.OperationID != v.OperationID {
		t.Fatalf("log entry = %+v", e)
	}
	if m, _ := filepath.Glob(filepath.Join(h.dir, "app", "tmp-*")); len(m) != 0 {
		t.Fatalf("temp dirs left: %v", m)
	}
	d, err := h.svc.Detail(ctx, "app")
	if err != nil || d.SuggestedTag != "2" || d.Arch != "arm64" || d.Revision != 2 || len(d.Versions) != 1 {
		t.Fatalf("detail = %+v err %v", d, err)
	}
}

func TestDeleteVersionRemovesTagAndLog(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	v := h.build(t, "app", "1", nil)
	if err := h.svc.DeleteVersion(ctx, "alice", "app", v.ID); err != nil {
		t.Fatal(err)
	}
	if h.eng.present["stackstead.invalid/"+testOwner+"/app:1"] {
		t.Fatal("docker tag still present")
	}
	if _, err := os.Stat(filepath.Join(h.dir, "app", "logs", v.ID+".log")); !os.IsNotExist(err) {
		t.Fatalf("log still exists: %v", err)
	}
	if _, err := h.svc.store.VersionByID(ctx, v.ID); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := h.log.last("image.version"); !ok {
		t.Fatal("image.version not logged")
	}
	h.build(t, "app", "1", nil) // tag is free again
}

func TestDeleteVersionInUse(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	v := h.build(t, "app", "1", nil)
	h.usage.usages = []imageref.Usage{{Stack: "web", Service: "api", Ref: imageref.Ref{Owner: testOwner, Slug: "app", Tag: "1"}}}
	err := h.svc.DeleteVersion(ctx, "alice", "app", v.ID)
	var in *InUseError
	if !errors.As(err, &in) || len(in.UsedBy) != 1 || in.UsedBy[0].Stack != "web" || in.UsedBy[0].Service != "api" {
		t.Fatalf("err = %v", err)
	}
	if in.UsedBy[0].Server != testInstanceID || !strings.Contains(err.Error(), "web/api") {
		t.Fatalf("message = %q", err)
	}
	if !h.eng.present["stackstead.invalid/"+testOwner+"/app:1"] {
		t.Fatal("tag removed despite use")
	}
	if _, err := h.svc.store.VersionByID(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteImageRefusedWhenInUse(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	h.usage.usages = []imageref.Usage{{Stack: "web", Service: "api", Ref: imageref.Ref{Owner: testOwner, Slug: "app", Tag: "1"}}}
	var in *InUseError
	if err := h.svc.Delete(ctx, "alice", "app"); !errors.As(err, &in) {
		t.Fatalf("err = %v", err)
	}
	if _, err := h.svc.Get(ctx, "app"); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteImageRemovesEverything(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	if err := h.svc.Delete(ctx, "alice", "app"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "app")); !os.IsNotExist(err) {
		t.Fatalf("image dir still exists: %v", err)
	}
	if len(h.eng.removed) != 1 || h.eng.present["stackstead.invalid/"+testOwner+"/app:1"] {
		t.Fatalf("removed = %v", h.eng.removed)
	}
	if _, err := h.svc.Get(ctx, "app"); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := h.log.last("image.delete"); !ok {
		t.Fatal("image.delete not logged")
	}
}

func TestEnsureLocalLocalOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	var lines []string
	out := func(l string) { lines = append(lines, l) }
	ref := imageref.Ref{Owner: testOwner, Slug: "app", Tag: "1"}
	if err := h.svc.EnsureLocal(ctx, ref, out); err != nil || len(lines) != 0 {
		t.Fatalf("err %v lines %v", err, lines)
	}
	h.eng.mu.Lock()
	delete(h.eng.present, ref.String())
	h.eng.mu.Unlock()
	err := h.svc.EnsureLocal(ctx, ref, out)
	if err == nil || !strings.Contains(err.Error(), "not built on this server") || !errors.Is(err, ErrImageNotBuilt) {
		t.Fatalf("missing err = %v", err)
	}
	foreign := imageref.Ref{Owner: "ffffffffffff", Slug: "app", Tag: "1"}
	if err := h.svc.EnsureLocal(ctx, foreign, out); !errors.Is(err, ErrImageNotBuilt) {
		t.Fatalf("foreign err = %v", err)
	}
}

func TestResolveLocal(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	r1 := "stackstead.invalid/" + testOwner + "/app:1"
	got, err := h.svc.Resolve(ctx, []string{r1})
	if err != nil || got[r1].Newer || !got[r1].Known || got[r1].LatestTag != "1" {
		t.Fatalf("before: %+v err %v", got, err)
	}
	h.build(t, "app", "2", nil)
	foreign := "stackstead.invalid/ffffffffffff/app:1"
	got, _ = h.svc.Resolve(ctx, []string{r1, foreign, "nginx:1"})
	if !got[r1].Newer || got[r1].LatestTag != "2" || got[r1].Arch != "arm64" || got[r1].OwnerName != "Host A" || got[r1].Image != "app" {
		t.Fatalf("after: %+v", got[r1])
	}
	if got[foreign].Known {
		t.Fatal("foreign ref reported known")
	}
	if got == nil {
		t.Fatal("nil map")
	}
}

func TestRecoverInterruptedBuilds(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	img := h.create(t, "app")
	done := h.build(t, "app", "1", nil)
	stuck, err := h.svc.store.StartVersion(ctx, Version{ImageID: img.ID, Tag: "2", Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := h.svc.store.VersionByID(ctx, stuck.ID)
	if got.Status != VersionFailed || got.Error != "interrupted by server restart" || got.EndedAt == nil {
		t.Fatalf("stuck = %+v", got)
	}
	still, _ := h.svc.store.VersionByID(ctx, done.ID)
	if still.Status != VersionSucceeded || still.Error != "" {
		t.Fatalf("finished version touched: %+v", still)
	}
	h.build(t, "app", "2", nil) // tag reusable after recovery
}
