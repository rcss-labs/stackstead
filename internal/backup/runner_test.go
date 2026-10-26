package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/peers"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
)

// callLog records stack actions and volume copies in the order they happen.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, s)
}

func (l *callLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

type fakeStackControl struct {
	log       *callLog
	summaries []stacks.Summary
	mounts    map[string][]stacks.Mount
	refs      map[string][]string
	actErr    map[string]error // "<action> <stack>" -> error
	listGate  chan struct{}    // when set, List blocks until it is closed
	ops       *stacks.Ops      // when set, ActWait goes through it like stacks.Service does
	panicOn   string           // ImageRefs panics for this stack
}

func (f *fakeStackControl) List(ctx context.Context) ([]stacks.Summary, error) {
	if f.listGate != nil {
		<-f.listGate
	}
	return f.summaries, nil
}

func (f *fakeStackControl) Mounts(_ context.Context, stack string) ([]stacks.Mount, error) {
	return f.mounts[stack], nil
}

func (f *fakeStackControl) ImageRefs(_ context.Context, stack string) ([]string, error) {
	if stack == f.panicOn {
		panic("image refs exploded")
	}
	return f.refs[stack], nil
}

func (f *fakeStackControl) ActWait(_ context.Context, stack, action string, out func(string)) error {
	if f.ops != nil {
		op, err := f.ops.Start(stack, action, func(context.Context, func(string)) error { return nil })
		if err != nil {
			return err
		}
		f.ops.Wait(op.ID)
	}
	f.log.add(action + " " + stack)
	out(action + " " + stack)
	return f.actErr[action+" "+stack]
}

func (f *fakeStackControl) StartAfterBackup(_ context.Context, stack string, out func(string)) error {
	f.log.add("start " + stack)
	out("start " + stack)
	return f.actErr["start "+stack]
}

// runnerEngine serves volume and image contents keyed by mount source and ref.
type runnerEngine struct {
	log        *callLog
	mu         sync.Mutex
	sources    map[string]string // helper id -> mount source
	volumes    map[string][]byte // mount source -> inner tar
	volumeErr  map[string]error
	onCopy     func(source string)
	streams    map[string]func() io.Reader // mount source -> custom stream
	images     map[string][]byte           // ref -> docker save tar
	networks   []engine.Network
	containers []engine.Container
}

func (e *runnerEngine) CreateContainer(_ context.Context, spec engine.HelperSpec) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	id := fmt.Sprintf("helper%d", len(e.sources))
	e.sources[id] = spec.Mounts[0].Source
	return id, nil
}
func (e *runnerEngine) StartContainer(context.Context, string) error        { return nil }
func (e *runnerEngine) WaitContainer(context.Context, string) (int, error)  { return 0, nil }
func (e *runnerEngine) RemoveContainer(context.Context, string, bool) error { return nil }
func (e *runnerEngine) GetArchive(_ context.Context, id, _ string) (io.ReadCloser, error) {
	e.mu.Lock()
	src := e.sources[id]
	e.mu.Unlock()
	e.log.add("copy " + src)
	if e.onCopy != nil {
		e.onCopy(src)
	}
	if err := e.volumeErr[src]; err != nil {
		return nil, err
	}
	if stream := e.streams[src]; stream != nil {
		return io.NopCloser(stream()), nil
	}
	return io.NopCloser(bytes.NewReader(e.volumes[src])), nil
}
func (e *runnerEngine) PutArchive(context.Context, string, string, io.Reader) error { return nil }
func (e *runnerEngine) PullImage(context.Context, string, func(string)) error       { return nil }
func (e *runnerEngine) InspectImage(context.Context, string) (engine.ImageInspect, error) {
	return engine.ImageInspect{}, nil
}
func (e *runnerEngine) SaveImage(_ context.Context, ref string) (io.ReadCloser, error) {
	data, ok := e.images[ref]
	if !ok {
		return nil, errors.New("no such image")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (e *runnerEngine) LoadImage(context.Context, io.Reader) error         { return nil }
func (e *runnerEngine) Networks(context.Context) ([]engine.Network, error) { return e.networks, nil }
func (e *runnerEngine) CreateNetwork(context.Context, engine.NetworkSpec) (string, error) {
	return "", nil
}
func (e *runnerEngine) InspectVolume(context.Context, string) (engine.VolumeInspect, error) {
	return engine.VolumeInspect{}, nil
}
func (e *runnerEngine) CreateVolume(context.Context, string, map[string]string) error { return nil }
func (e *runnerEngine) InspectContainer(context.Context, string) (engine.ContainerInspect, error) {
	return engine.ContainerInspect{}, nil
}
func (e *runnerEngine) Containers(context.Context) ([]engine.Container, error) {
	return e.containers, nil
}

// memDest is an in-memory Destination. Archives appear only after Close.
type memDest struct {
	name      string
	createErr error
	onClose   func()
	mu        sync.Mutex
	archives  map[string][]byte
	sidecars  map[string][]byte
	aborted   []string
}

func newMemDest(name string) *memDest {
	return &memDest{name: name, archives: map[string][]byte{}, sidecars: map[string][]byte{}}
}

type memWriter struct {
	d       *memDest
	archive string
	buf     bytes.Buffer
}

func (w *memWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }
func (w *memWriter) Close() error {
	if w.d.onClose != nil {
		w.d.onClose()
	}
	w.d.mu.Lock()
	defer w.d.mu.Unlock()
	for _, a := range w.d.aborted {
		if a == w.archive {
			return errors.New("closed after abort")
		}
	}
	w.d.archives[w.archive] = w.buf.Bytes()
	return nil
}

func (d *memDest) Name() string { return d.name }
func (d *memDest) Create(_ context.Context, archive string) (io.WriteCloser, error) {
	if d.createErr != nil {
		return nil, d.createErr
	}
	return &memWriter{d: d, archive: archive}, nil
}
func (d *memDest) PutSidecar(_ context.Context, archive string, manifest []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sidecars[archive] = manifest
	return nil
}
func (d *memDest) List(context.Context) ([]ArchiveRef, error) { return nil, nil }
func (d *memDest) Open(_ context.Context, archive string) (io.ReadCloser, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return io.NopCloser(bytes.NewReader(d.archives[archive])), nil
}
func (d *memDest) ReadSidecar(context.Context, string) (Manifest, error) { return Manifest{}, nil }
func (d *memDest) Abort(_ context.Context, archive string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.aborted = append(d.aborted, archive)
	return nil
}

type runnerEnv struct {
	runner  *Runner
	deps    Deps
	db      *sql.DB
	reg     *stacks.Registry
	stacks  *fakeStackControl
	eng     *runnerEngine
	local   *memDest
	s3      *memDest
	log     *callLog
	dataDir string
}

var runnerNow = time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)

func newRunnerEnv(t *testing.T, cfg Config) *runnerEnv {
	t.Helper()
	dataDir := t.TempDir()
	db, err := store.Open(filepath.Join(dataDir, "stackstead.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := func() time.Time { return runnerNow }
	log := &callLog{}
	env := &runnerEnv{
		db:      db,
		reg:     stacks.NewRegistry(db, now),
		stacks:  &fakeStackControl{log: log, mounts: map[string][]stacks.Mount{}, refs: map[string][]string{}, actErr: map[string]error{}},
		eng:     &runnerEngine{log: log, sources: map[string]string{}, volumes: map[string][]byte{}, volumeErr: map[string]error{}, images: map[string][]byte{}},
		local:   newMemDest(destLocal),
		s3:      newMemDest(destS3),
		log:     log,
		dataDir: dataDir,
	}
	configs := NewConfigStore(db)
	if err := configs.Save(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	ops := stacks.NewOps(0, nil)
	t.Cleanup(func() { ops.Shutdown(context.Background()) })
	env.deps = Deps{
		DB:       db,
		Config:   configs,
		Runs:     NewRunStore(db, now),
		Stacks:   env.stacks,
		Registry: env.reg,
		Engine:   env.eng,
		Ops:      ops,
		Actions:  actionlog.NewStore(db, now),
		Identity: func(context.Context) (peers.Identity, error) {
			return peers.Identity{InstanceID: "inst-1", Name: "Test Host"}, nil
		},
		DataDir:   dataDir,
		ImagesDir: filepath.Join(dataDir, "images"),
		Version:   "1.2.3",
		Now:       now,
	}
	env.newRunner()
	return env
}

// newRunner rebuilds the runner from env.deps, using the in-memory destinations.
func (env *runnerEnv) newRunner() {
	env.runner = NewRunner(env.deps)
	env.runner.newLocal = func(LocalDest) Destination { return env.local }
	env.runner.newS3 = func(S3Dest, *http.Client) Destination { return env.s3 }
}

func localOnly(t *testing.T) Config {
	cfg := defaultConfig()
	cfg.Local = LocalDest{Enabled: true, Path: t.TempDir(), KeepLast: 7}
	return cfg
}

func (env *runnerEnv) addStack(t *testing.T, name string, status stacks.Status) stacks.Entry {
	t.Helper()
	dir := filepath.Join(env.dataDir, "stacks", name)
	writeTree(t, dir, map[string]string{"compose.yaml": "services: {}\n"})
	e := stacks.Entry{Name: name, ComposePath: filepath.Join(dir, "compose.yaml"), Origin: stacks.OriginManaged, Project: name}
	if err := env.reg.Add(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if status != "" {
		env.stacks.summaries = append(env.stacks.summaries, stacks.Summary{Name: name, Path: e.ComposePath, Project: name, Origin: e.Origin, Status: status})
	}
	return e
}

// addVolume gives the stack a named volume holding one file.
func (env *runnerEnv) addVolume(t *testing.T, stack, volume string) {
	t.Helper()
	env.stacks.mounts[stack] = append(env.stacks.mounts[stack], stacks.Mount{Service: "app", Kind: "volume", Source: volume, Target: "/data"})
	env.eng.volumes[volume] = tarOf(t, map[string][]byte{"file.txt": []byte("data of " + volume)})
}

func (env *runnerEnv) selectStack(t *testing.T, sel StackSelection) {
	t.Helper()
	if err := env.deps.Config.SetSelection(context.Background(), sel); err != nil {
		t.Fatal(err)
	}
}

// run starts a backup, waits for it and returns the recorded run.
func (env *runnerEnv) run(t *testing.T) Run {
	t.Helper()
	op, err := env.runner.Start(context.Background(), "manual", "admin")
	if err != nil {
		t.Fatal(err)
	}
	env.deps.Ops.Wait(op.ID)
	run, ok, err := env.runner.LastRun(context.Background())
	if err != nil || !ok {
		t.Fatalf("LastRun: ok=%v err=%v", ok, err)
	}
	if run.Trigger != "manual" {
		t.Errorf("trigger = %q", run.Trigger)
	}
	return run
}

func tarOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// readArchive returns the entry names in archive order and the manifest.
func readArchive(t *testing.T, data []byte) ([]string, Manifest) {
	t.Helper()
	r, err := NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	var m Manifest
	for {
		h, body, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Name == ManifestEntryName {
			if err := decodeManifest(body, &m); err != nil {
				t.Fatal(err)
			}
		}
	}
	return names, m
}

func (d *memDest) only(t *testing.T) (string, []byte) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.archives) != 1 {
		t.Fatalf("%s holds %d archives, want 1", d.name, len(d.archives))
	}
	for name, data := range d.archives {
		return name, data
	}
	return "", nil
}

func findItem(items []ItemResult, kind, stack string) (ItemResult, bool) {
	for _, it := range items {
		if it.Kind == kind && it.Stack == stack {
			return it, true
		}
	}
	return ItemResult{}, false
}

func TestRunnerBaseOnlyArchive(t *testing.T) {
	env := newRunnerEnv(t, localOnly(t))
	env.deps.ConfigPath = filepath.Join(env.dataDir, "config.yaml")
	writeTree(t, env.dataDir, map[string]string{"config.yaml": "listen: :8080\n", "images/lib/Dockerfile": "FROM scratch\n"})
	env.eng.networks = []engine.Network{{Name: "lan", Driver: "bridge", Labels: map[string]string{createdLabel: "true"}}}
	env.addStack(t, "web", stacks.StatusRunning)
	env.newRunner()

	run := env.run(t)
	if run.Status != "success" || run.Error != "" {
		t.Fatalf("run = %+v", run)
	}
	name, data := env.local.only(t)
	if run.Archive != name || name != ArchiveName("test-host", runnerNow) || run.Size != int64(len(data)) {
		t.Errorf("archive = %q size %d, run = %+v", name, len(data), run)
	}
	names, m := readArchive(t, data)
	if names[0] != "base/stackstead.db" || names[len(names)-1] != ManifestEntryName {
		t.Errorf("order = %v", names)
	}
	for _, want := range []string{"base/config.yaml", "base/networks.json", "base/stacks/web/compose.yaml", "base/images-library/lib/Dockerfile"} {
		if !strings.Contains(strings.Join(names, "\n"), want) {
			t.Errorf("archive lacks %s: %v", want, names)
		}
	}
	if m.Status != "success" || m.Networks != 1 || m.HostSlug != "test-host" || m.StacksteadVersion != "1.2.3" || len(m.Stacks) != 1 || !m.Stacks[0].WasRunning {
		t.Errorf("manifest = %+v", m)
	}
	if _, ok := env.local.sidecars[name]; !ok {
		t.Error("no sidecar written")
	}
	if !reflect.DeepEqual(run.Destinations, []string{destLocal}) {
		t.Errorf("destinations = %v", run.Destinations)
	}
	entries, err := actionlog.NewStore(env.db, time.Now).Recent(context.Background(), actionlog.Filter{})
	if err != nil || len(entries) != 1 || entries[0].Kind != "backup.run" || entries[0].Result != "success" || entries[0].Target != name || entries[0].OperationID == "" {
		t.Errorf("action log = %+v err=%v", entries, err)
	}
}

func TestRunnerStopsAndRestartsRunningStack(t *testing.T) {
	env := newRunnerEnv(t, localOnly(t))
	env.addStack(t, "web", stacks.StatusRunning)
	env.addVolume(t, "web", "web_data")
	env.stacks.refs["web"] = []string{"nginx:1"}
	env.eng.images["nginx:1"] = tarOf(t, map[string][]byte{"manifest.json": []byte("[]")})
	env.selectStack(t, StackSelection{Stack: "web", Volumes: true, Images: true, StopDuring: true})

	run := env.run(t)
	if run.Status != "success" {
		t.Fatalf("run = %+v", run)
	}
	if got, want := env.log.list(), []string{"stop web", "copy web_data", "start web"}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	_, data := env.local.only(t)
	names, m := readArchive(t, data)
	joined := strings.Join(names, "\n")
	if !strings.Contains(joined, "volumes/web/v-web_data/file.txt") || !strings.Contains(joined, "images/web/"+ImageKey("nginx:1")+"/manifest.json") {
		t.Errorf("names = %v", names)
	}
	st := m.Stacks[0]
	if !st.WasRunning || st.LiveCopy || len(st.Volumes) != 1 || !reflect.DeepEqual(st.Images, []string{"nginx:1"}) {
		t.Errorf("stack = %+v", st)
	}
}

func TestRunnerCopiesLiveWhenStopIsOff(t *testing.T) {
	env := newRunnerEnv(t, localOnly(t))
	env.addStack(t, "web", stacks.StatusRunning)
	env.addVolume(t, "web", "web_data")
	env.selectStack(t, StackSelection{Stack: "web", Volumes: true})

	env.run(t)
	if got := env.log.list(); !reflect.DeepEqual(got, []string{"copy web_data"}) {
		t.Errorf("calls = %v", got)
	}
	_, data := env.local.only(t)
	if _, m := readArchive(t, data); !m.Stacks[0].LiveCopy {
		t.Errorf("manifest does not record the live copy: %+v", m.Stacks[0])
	}
}

func TestRunnerDoesNotStartStoppedStack(t *testing.T) {
	env := newRunnerEnv(t, localOnly(t))
	env.addStack(t, "web", stacks.StatusStopped)
	env.addVolume(t, "web", "web_data")
	env.selectStack(t, StackSelection{Stack: "web", Volumes: true, StopDuring: true})

	run := env.run(t)
	if got := env.log.list(); !reflect.DeepEqual(got, []string{"copy web_data"}) {
		t.Errorf("calls = %v, want only the copy", got)
	}
	if run.Status != "success" {
		t.Errorf("status = %s", run.Status)
	}
}

func TestRunnerRestartsStackAfterCopyError(t *testing.T) {
	env := newRunnerEnv(t, localOnly(t))
	env.addStack(t, "web", stacks.StatusRunning)
	env.addVolume(t, "web", "web_data")
	env.eng.volumeErr["web_data"] = errors.New("disk on fire")
	env.selectStack(t, StackSelection{Stack: "web", Volumes: true, StopDuring: true})

	run := env.run(t)
	if got, want := env.log.list(), []string{"stop web", "copy web_data", "start web"}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	if run.Status != "partial" {
		t.Errorf("status = %s", run.Status)
	}
	it, ok := findItem(run.Items, "volume", "web")
	if !ok || it.Status != "failed" || !strings.Contains(it.Note, "disk on fire") {
		t.Errorf("volume item = %+v (items %+v)", it, run.Items)
	}
	env.local.only(t) // the archive is still written
}

func TestRunnerReportsRestartFailure(t *testing.T) {
	env := newRunnerEnv(t, localOnly(t))
	env.addStack(t, "alpha", stacks.StatusRunning)
	env.addStack(t, "beta", stacks.StatusRunning)
	env.addVolume(t, "alpha", "alpha_data")
	env.addVolume(t, "beta", "beta_data")
	env.stacks.actErr["start alpha"] = errors.New("port taken")
	env.selectStack(t, StackSelection{Stack: "alpha", Volumes: true, StopDuring: true})
	env.selectStack(t, StackSelection{Stack: "beta", Volumes: true, StopDuring: true})

	run := env.run(t)
	want := []string{"stop alpha", "copy alpha_data", "start alpha", "stop beta", "copy beta_data", "start beta"}
	if got := env.log.list(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	if run.Status != "partial" {
		t.Errorf("status = %s", run.Status)
	}
	var leftStopped bool
	for _, it := range run.Items {
		if it.Stack == "alpha" && it.Status == "failed" && strings.Contains(it.Note, "left stopped") && strings.Contains(it.Note, "port taken") {
			leftStopped = true
		}
	}
	if !leftStopped {
		t.Errorf("no item says alpha was left stopped: %+v", run.Items)
	}
	_, data := env.local.only(t)
	if names, _ := readArchive(t, data); !strings.Contains(strings.Join(names, "\n"), "volumes/beta/v-beta_data/file.txt") {
		t.Errorf("beta not archived: %v", names)
	}
}

func TestRunnerSkipsMissingStack(t *testing.T) {
	env := newRunnerEnv(t, localOnly(t))
	env.addStack(t, "ghost", "") // registered, but gone from the stack list
	env.selectStack(t, StackSelection{Stack: "ghost", Volumes: true, StopDuring: true})

	run := env.run(t)
	if run.Status != "success" {
		t.Errorf("status = %s (%s)", run.Status, run.Error)
	}
	it, ok := findItem(run.Items, "volume", "ghost")
	if !ok || it.Status != "skipped" {
		t.Errorf("item = %+v (items %+v)", it, run.Items)
	}
	if got := env.log.list(); len(got) != 0 {
		t.Errorf("calls = %v, want none", got)
	}
}

func TestRunnerBaseFailureFailsRun(t *testing.T) {
	cfg := localOnly(t)
	env := newRunnerEnv(t, cfg)
	broken, err := store.Open(filepath.Join(t.TempDir(), "broken.db"))
	if err != nil {
		t.Fatal(err)
	}
	broken.Close()
	env.deps.DB = broken
	env.newRunner()
	env.runner.newLocal = NewLocal // the real folder, to check nothing is left in it

	run := env.run(t)
	if run.Status != "failed" || run.Error == "" || run.Archive != "" {
		t.Errorf("run = %+v", run)
	}
	entries, err := os.ReadDir(cfg.Local.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("backup folder holds %v, want nothing", entries)
	}
}

func TestRunnerS3OnlyDoesNotStageArchive(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	cfg := defaultConfig()
	cfg.S3 = S3Dest{Enabled: true, Bucket: "bkt", Region: "us-east-1"}
	env := newRunnerEnv(t, cfg)
	env.runner.newLocal = func(LocalDest) Destination { t.Error("local destination used"); return env.local }
	env.addStack(t, "web", stacks.StatusStopped)
	env.stacks.mounts["web"] = []stacks.Mount{{Service: "app", Kind: "volume", Source: "big", Target: "/data"}}
	payload := make([]byte, 4<<20) // random, so gzip cannot shrink it
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	env.eng.volumes["big"] = tarOf(t, map[string][]byte{"blob": payload})
	env.selectStack(t, StackSelection{Stack: "web", Volumes: true})

	const limit = 2 << 20 // well above the DB snapshot, well below the archive
	checkNoLargeFiles := func() {
		for _, dir := range []string{env.dataDir, tmp} {
			_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				if info, err := d.Info(); err == nil && info.Size() > limit {
					t.Errorf("staged file %s (%d bytes)", p, info.Size())
				}
				return nil
			})
		}
	}
	env.s3.onClose = checkNoLargeFiles

	run := env.run(t)
	if run.Status != "success" || !reflect.DeepEqual(run.Destinations, []string{destS3}) {
		t.Fatalf("run = %+v", run)
	}
	if _, data := env.s3.only(t); len(data) < 4<<20 {
		t.Fatalf("archive is %d bytes, too small for the test to mean anything", len(data))
	}
	checkNoLargeFiles()
}

func TestRunnerUploadFailureKeepsLocal(t *testing.T) {
	cfg := localOnly(t)
	cfg.S3 = S3Dest{Enabled: true, Bucket: "bkt", Region: "us-east-1"}
	env := newRunnerEnv(t, cfg)
	env.runner.newLocal = NewLocal
	env.s3.createErr = errors.New("bucket unreachable")

	run := env.run(t)
	if run.Status != "partial" || !reflect.DeepEqual(run.Destinations, []string{destLocal}) {
		t.Errorf("run = %+v", run)
	}
	it, ok := findItem(run.Items, "upload", "")
	if !ok || it.Status != "failed" || !strings.Contains(it.Note, "bucket unreachable") {
		t.Errorf("upload item = %+v", it)
	}
	if _, err := os.Stat(filepath.Join(cfg.Local.Path, run.Archive)); err != nil {
		t.Errorf("local archive missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Local.Path, SidecarName(run.Archive))); err != nil {
		t.Errorf("local sidecar missing: %v", err)
	}
}

func TestRunnerUploadsLocalArchiveToS3(t *testing.T) {
	cfg := localOnly(t)
	cfg.S3 = S3Dest{Enabled: true, Bucket: "bkt", Region: "us-east-1"}
	env := newRunnerEnv(t, cfg)
	env.runner.newLocal = NewLocal

	run := env.run(t)
	if run.Status != "success" || !reflect.DeepEqual(run.Destinations, []string{destLocal, destS3}) {
		t.Fatalf("run = %+v", run)
	}
	local, err := os.ReadFile(filepath.Join(cfg.Local.Path, run.Archive))
	if err != nil {
		t.Fatal(err)
	}
	if name, data := env.s3.only(t); name != run.Archive || !bytes.Equal(data, local) {
		t.Errorf("uploaded %q (%d bytes), local %d bytes", name, len(data), len(local))
	}
	if _, ok := env.s3.sidecars[run.Archive]; !ok {
		t.Error("no S3 sidecar")
	}
}

func TestRunnerBusy(t *testing.T) {
	env := newRunnerEnv(t, localOnly(t))
	env.stacks.listGate = make(chan struct{})
	op, err := env.runner.Start(context.Background(), "manual", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.runner.Start(context.Background(), "schedule", "admin"); !errors.Is(err, stacks.ErrBusy) {
		t.Errorf("second start err = %v, want ErrBusy", err)
	}
	close(env.stacks.listGate)
	env.deps.Ops.Wait(op.ID)
}

// brokenStream is a tar stream that fails partway through a regular file.
func brokenStream(t *testing.T) io.Reader {
	t.Helper()
	whole := tarOf(t, map[string][]byte{"big.bin": bytes.Repeat([]byte("x"), 64<<10)})
	return io.MultiReader(bytes.NewReader(whole[:512+1000]), iotest.ErrReader(errors.New("helper stream broke")))
}

func TestRunnerVolumeStreamBreakingMidEntryIsPartial(t *testing.T) {
	env := newRunnerEnv(t, localOnly(t))
	env.addStack(t, "alpha", stacks.StatusStopped)
	env.addStack(t, "beta", stacks.StatusStopped)
	env.addVolume(t, "alpha", "alpha_data")
	env.addVolume(t, "beta", "beta_data")
	env.eng.streams = map[string]func() io.Reader{"alpha_data": func() io.Reader { return brokenStream(t) }}
	env.selectStack(t, StackSelection{Stack: "alpha", Volumes: true})
	env.selectStack(t, StackSelection{Stack: "beta", Volumes: true})

	run := env.run(t)
	if run.Status != "partial" {
		t.Fatalf("run = %+v", run)
	}
	it, ok := findItem(run.Items, "volume", "alpha")
	if !ok || it.Status != "failed" || !strings.Contains(it.Note, "helper stream broke") {
		t.Errorf("alpha item = %+v", it)
	}
	_, data := env.local.only(t)
	names, m := readArchive(t, data)
	if names[len(names)-1] != ManifestEntryName {
		t.Errorf("archive does not end with the manifest: %v", names)
	}
	if !strings.Contains(strings.Join(names, "\n"), "volumes/beta/v-beta_data/file.txt") {
		t.Errorf("beta not archived: %v", names)
	}
	if it, ok := findItem(m.Items, "volume", "alpha"); !ok || it.Status != "failed" || m.Status != "partial" {
		t.Errorf("manifest status %q, alpha item %+v", m.Status, it)
	}
}

func TestRunnerRestartsStackAfterOpsShutdown(t *testing.T) {
	env := newRunnerEnv(t, localOnly(t))
	env.stacks.ops = env.deps.Ops
	env.addStack(t, "web", stacks.StatusRunning)
	env.addVolume(t, "web", "web_data")
	env.selectStack(t, StackSelection{Stack: "web", Volumes: true, StopDuring: true})
	env.eng.onCopy = func(string) {
		go env.deps.Ops.Shutdown(context.Background())
		for {
			if _, err := env.deps.Ops.Start("probe", "probe", func(context.Context, func(string)) error { return nil }); errors.Is(err, stacks.ErrShutdown) {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}

	env.run(t)
	if got, want := env.log.list(), []string{"stop web", "copy web_data", "start web"}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

func TestRunnerPanicIsRecordedAndAborted(t *testing.T) {
	env := newRunnerEnv(t, localOnly(t))
	env.addStack(t, "web", stacks.StatusRunning)
	env.stacks.panicOn = "web"
	env.selectStack(t, StackSelection{Stack: "web", Images: true})

	op, err := env.runner.Start(context.Background(), "manual", "admin")
	if err != nil {
		t.Fatal(err)
	}
	env.deps.Ops.Wait(op.ID)
	if fin, _ := env.deps.Ops.Get(op.ID); fin.State != stacks.OpFailed || !strings.Contains(fin.Error, "panicked") {
		t.Errorf("operation = %+v", fin)
	}
	run, ok, err := env.runner.LastRun(context.Background())
	if err != nil || !ok || run.Status != "failed" || !strings.Contains(run.Error, "image refs exploded") {
		t.Errorf("run = %+v ok=%v err=%v", run, ok, err)
	}
	if len(env.local.archives) != 0 || len(env.local.aborted) != 1 {
		t.Errorf("archives %d, aborted %v", len(env.local.archives), env.local.aborted)
	}
	entries, err := actionlog.NewStore(env.db, time.Now).Recent(context.Background(), actionlog.Filter{})
	if err != nil || len(entries) != 1 || entries[0].Result != "failed" {
		t.Errorf("action log = %+v err=%v", entries, err)
	}
}
