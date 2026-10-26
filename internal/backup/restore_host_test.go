package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
)

// restoreEngine is a fake Engine that records every call that would change the host.
type restoreEngine struct {
	mu         sync.Mutex
	calls      []string
	specs      []engine.HelperSpec
	puts       []map[string]string // entry name -> content, per PutArchive
	loads      [][]string
	networks   []engine.Network
	netErr     map[string]error
	volumes    map[string]bool
	containers []engine.Container
	waitCode   int
}

func (f *restoreEngine) record(c string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
}

func (f *restoreEngine) changes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *restoreEngine) CreateContainer(_ context.Context, spec engine.HelperSpec) (string, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	f.record("create:" + strings.Join(spec.Cmd, " "))
	return fmt.Sprintf("helper%d", len(f.specs)), nil
}
func (f *restoreEngine) StartContainer(_ context.Context, id string) error {
	f.record("start:" + id)
	return nil
}
func (f *restoreEngine) WaitContainer(_ context.Context, id string) (int, error) {
	f.record("wait:" + id)
	return f.waitCode, nil
}
func (f *restoreEngine) RemoveContainer(_ context.Context, id string, _ bool) error {
	f.record("remove:" + id)
	return nil
}
func (f *restoreEngine) GetArchive(context.Context, string, string) (io.ReadCloser, error) {
	return nil, errors.New("not used")
}
func (f *restoreEngine) PutArchive(_ context.Context, id, path string, r io.Reader) error {
	f.record("put:" + id + ":" + path)
	got := map[string]string{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(tr)
		got[hdr.Name] = string(body)
	}
	f.mu.Lock()
	f.puts = append(f.puts, got)
	f.mu.Unlock()
	return nil
}
func (f *restoreEngine) PullImage(context.Context, string, func(string)) error { return nil }
func (f *restoreEngine) InspectImage(context.Context, string) (engine.ImageInspect, error) {
	return engine.ImageInspect{}, nil
}
func (f *restoreEngine) SaveImage(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (f *restoreEngine) LoadImage(_ context.Context, r io.Reader) error {
	f.record("load")
	var names []string
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		names = append(names, hdr.Name)
	}
	f.mu.Lock()
	f.loads = append(f.loads, names)
	f.mu.Unlock()
	return nil
}
func (f *restoreEngine) Networks(context.Context) ([]engine.Network, error) { return f.networks, nil }
func (f *restoreEngine) CreateNetwork(_ context.Context, spec engine.NetworkSpec) (string, error) {
	f.record("network:" + spec.Name)
	if err := f.netErr[spec.Name]; err != nil {
		return "", err
	}
	return "net-" + spec.Name, nil
}
func (f *restoreEngine) InspectVolume(_ context.Context, name string) (engine.VolumeInspect, error) {
	if f.volumes[name] {
		return engine.VolumeInspect{Name: name}, nil
	}
	return engine.VolumeInspect{}, engine.ErrVolumeNotFound
}
func (f *restoreEngine) CreateVolume(_ context.Context, name string, _ map[string]string) error {
	f.record("volume:" + name)
	return nil
}
func (f *restoreEngine) InspectContainer(context.Context, string) (engine.ContainerInspect, error) {
	return engine.ContainerInspect{}, nil
}
func (f *restoreEngine) Containers(context.Context) ([]engine.Container, error) {
	return f.containers, nil
}

// hostFixture describes a whole-host archive to build.
type hostFixture struct {
	version  string
	stacks   []stacks.Entry
	running  []string
	schedule bool
	files    map[string]string // base/... entries other than the db, config and networks
	networks []engine.NetworkSpec
	volumes  map[string][]ManifestVolume
	volData  map[string]map[string]string // "<stack>/<key>" -> inner tar files
	images   map[string]map[string]string // stack -> inner tar files
}

// archivedDB builds the database a backup would hold and returns its bytes.
func archivedDB(t *testing.T, entries []stacks.Entry, scheduleEnabled bool) []byte {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reg := stacks.NewRegistry(db, time.Now)
	for _, e := range entries {
		if err := reg.Add(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	cfg := defaultConfig()
	cfg.Schedule.Enabled = scheduleEnabled
	cfg.Local = LocalDest{Enabled: true, Path: "/backups", KeepLast: 7}
	if err := NewConfigStore(db).Save(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(dir, "snap.db")
	if err := store.Backup(context.Background(), db, snap); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func innerTar(t *testing.T, files map[string]string) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if strings.HasSuffix(n, "/") {
			tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeDir, Mode: 0o755})
			continue
		}
		tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(files[n]))})
		io.WriteString(tw, files[n])
	}
	tw.Close()
	return &buf
}

func addBytes(t *testing.T, w *Writer, name string, data []byte) {
	t.Helper()
	if err := w.AddFile(name, &tar.Header{Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(data))}, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
}

func (f hostFixture) manifest() Manifest {
	m := Manifest{Format: ManifestFormat, StacksteadVersion: f.version, Host: "old", HostSlug: "old", CreatedAt: time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC), Status: "complete", Networks: len(f.networks)}
	for _, e := range f.stacks {
		m.Stacks = append(m.Stacks, ManifestStack{
			Name: e.Name, ComposePath: e.ComposePath, Origin: string(e.Origin), Project: e.Project,
			WasRunning: slices.Contains(f.running, e.Name), Volumes: f.volumes[e.Name],
		})
	}
	return m
}

// build writes the archive to dir and returns its path; withSidecar also writes the sidecar.
func (f hostFixture) build(t *testing.T, dir string, withSidecar bool) string {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf)
	addBytes(t, w, "base/stackstead.db", archivedDB(t, f.stacks, f.schedule))
	addBytes(t, w, "base/config.yaml", []byte("listen: :8080\n"))
	nets, _ := json.Marshal(append([]engine.NetworkSpec{}, f.networks...))
	addBytes(t, w, "base/networks.json", nets)
	names := make([]string, 0, len(f.files))
	for n := range f.files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		addBytes(t, w, n, []byte(f.files[n]))
	}
	keys := make([]string, 0, len(f.volData))
	for k := range f.volData {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if _, err := w.AddTarStream("volumes/"+k+"/", innerTar(t, f.volData[k])); err != nil {
			t.Fatal(err)
		}
	}
	for stack, files := range f.images {
		if _, err := w.AddTarStream("images/"+stack+"/", innerTar(t, files)); err != nil {
			t.Fatal(err)
		}
	}
	m := f.manifest()
	if err := w.Close(m); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ArchiveName("old", m.CreatedAt))
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if withSidecar {
		writeSidecar(t, path, m)
	}
	return path
}

func writeSidecar(t *testing.T, archive string, m Manifest) {
	t.Helper()
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(archive+sidecarSuffix, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

type hostSetup struct {
	rootA, rootB, outside string
	env                   HostEnv
	eng                   *restoreEngine
	fixture               hostFixture
}

func newHostSetup(t *testing.T) *hostSetup {
	t.Helper()
	base := t.TempDir()
	s := &hostSetup{
		rootA:   filepath.Join(base, "stacks"),
		rootB:   filepath.Join(base, "srv"),
		outside: filepath.Join(base, "elsewhere"),
		eng:     &restoreEngine{},
	}
	s.env = HostEnv{Engine: s.eng, DataDir: filepath.Join(base, "data"), AllowedRoots: []string{s.rootA, s.rootB}, Version: "1.4.0"}
	if err := os.MkdirAll(s.env.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	web := stacks.Entry{Name: "web", ComposePath: filepath.Join(s.rootA, "web", "compose.yaml"), Origin: stacks.OriginManaged, Project: "web"}
	api := stacks.Entry{Name: "api", ComposePath: filepath.Join(s.rootB, "deep", "api", "docker-compose.yml"), Origin: stacks.OriginExternal, Project: "api"}
	s.fixture = hostFixture{
		version:  "1.3.0",
		stacks:   []stacks.Entry{api, web},
		running:  []string{"web"},
		schedule: true,
		files: map[string]string{
			"base/stacks/web/compose.yaml":         "services: {app: {image: nginx}}\n",
			"base/stacks/web/conf/app.ini":         "a=1\n",
			"base/stacks/api/docker-compose.yml":   "services: {api: {image: api}}\n",
			"base/images-library/tools/Dockerfile": "FROM busybox\n",
		},
		networks: []engine.NetworkSpec{{Name: "lan", Driver: "macvlan", Parent: "eth9"}, {Name: "backend", Driver: "bridge"}},
		volumes: map[string][]ManifestVolume{
			"web": {{Key: "v-web_data", Kind: "volume", Name: "web_data", Target: "/var/lib/data"}},
		},
		volData: map[string]map[string]string{
			"web/v-web_data": {"data/": "", "data/file.txt": "volume contents"},
		},
		images: map[string]map[string]string{
			"web": {"manifest.json": "[]", "blobs/sha256/abc": "layer"},
		},
	}
	return s
}

func pathSource(p string) HostSource { return HostSource{Kind: "path", Path: p} }

// assertHostUntouched checks the anti-goal: no Docker changes and no stack files.
func (s *hostSetup) assertHostUntouched(t *testing.T) {
	t.Helper()
	for _, c := range s.eng.changes() {
		if strings.HasPrefix(c, "create") || strings.HasPrefix(c, "put") || strings.HasPrefix(c, "network") || strings.HasPrefix(c, "volume") || c == "load" {
			t.Errorf("the host was changed: %v", s.eng.changes())
			break
		}
	}
	for _, root := range []string{s.rootA, s.rootB, s.outside, filepath.Join(s.env.DataDir, "images")} {
		if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was created", root)
		}
	}
	if _, err := os.Stat(filepath.Join(s.env.DataDir, "restore", pendingFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a pending restore was recorded")
	}
}

// truncatedAfterDB returns an archive that holds only base/stackstead.db.
// cleanEnd keeps the gzip stream valid (the tar just stops); otherwise the
// gzip stream is cut off too.
func truncatedAfterDB(t *testing.T, dir string, f hostFixture, cleanEnd bool) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	db := archivedDB(t, f.stacks, f.schedule)
	tw.WriteHeader(&tar.Header{Name: "base/stackstead.db", Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(db))})
	tw.Write(db)
	tw.Flush()
	if cleanEnd {
		gz.Close()
	} else {
		gz.Flush()
	}
	path := filepath.Join(dir, "cut.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHostRestoreRefusesTruncatedBeforeChanges(t *testing.T) {
	for _, tc := range []struct {
		name              string
		cleanEnd, sidecar bool
	}{
		{"cut gzip, no sidecar", false, false},
		{"cut gzip, sidecar", false, true},
		{"tar ends early, sidecar", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newHostSetup(t)
			path := truncatedAfterDB(t, t.TempDir(), s.fixture, tc.cleanEnd)
			if tc.sidecar {
				writeSidecar(t, path, s.fixture.manifest())
			}
			_, err := RestoreHost(context.Background(), pathSource(path), s.env, true)
			if err == nil {
				t.Fatal("restore of a truncated archive succeeded")
			}
			s.assertHostUntouched(t)
		})
	}
}

func TestHostRestoreRefusesNewerArchive(t *testing.T) {
	s := newHostSetup(t)
	s.fixture.version = "1.5.0"
	path := s.fixture.build(t, t.TempDir(), false)
	_, err := RestoreHost(context.Background(), pathSource(path), s.env, true)
	if !errors.Is(err, ErrArchiveNewer) {
		t.Fatalf("err = %v, want ErrArchiveNewer", err)
	}
	if _, err := InspectHost(context.Background(), pathSource(path), s.env); !errors.Is(err, ErrArchiveNewer) {
		t.Fatalf("inspect err = %v, want ErrArchiveNewer", err)
	}
	s.assertHostUntouched(t)
}

func TestHostRestoreRefusesFirstEntryNotDatabase(t *testing.T) {
	s := newHostSetup(t)
	var buf bytes.Buffer
	w := NewWriter(&buf)
	addBytes(t, w, "base/stacks/web/compose.yaml", []byte("x"))
	addBytes(t, w, "base/stackstead.db", archivedDB(t, s.fixture.stacks, false))
	w.Close(s.fixture.manifest())
	path := filepath.Join(t.TempDir(), "odd.tar.gz")
	os.WriteFile(path, buf.Bytes(), 0o600)
	if _, err := RestoreHost(context.Background(), pathSource(path), s.env, true); err == nil {
		t.Fatal("restore succeeded although the database was not first")
	}
	s.assertHostUntouched(t)
}

func TestHostRestoreWritesOriginalPaths(t *testing.T) {
	s := newHostSetup(t)
	path := s.fixture.build(t, t.TempDir(), false)
	livePath := filepath.Join(s.env.DataDir, "stackstead.db")
	os.WriteFile(livePath, []byte("live"), 0o600)

	report, err := RestoreHost(context.Background(), pathSource(path), s.env, true)
	if err != nil {
		t.Fatalf("restore: %v (items %+v)", err, report.Items)
	}
	for p, want := range map[string]string{
		filepath.Join(s.rootA, "web", "compose.yaml"):                 "services: {app: {image: nginx}}\n",
		filepath.Join(s.rootA, "web", "conf", "app.ini"):              "a=1\n",
		filepath.Join(s.rootB, "deep", "api", "docker-compose.yml"):   "services: {api: {image: api}}\n",
		filepath.Join(s.env.DataDir, "images", "tools", "Dockerfile"): "FROM busybox\n",
	} {
		got, err := os.ReadFile(p)
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", p, got, err, want)
		}
	}
	if got, _ := os.ReadFile(livePath); string(got) != "live" {
		t.Error("the live database was touched")
	}

	// The volume is emptied by a started helper, then refilled through it.
	calls := s.eng.changes()
	wantOrder := []string{"volume:web_data", "create:find /data -mindepth 1 -delete", "start:helper1", "wait:helper1", "put:helper1:/", "remove:helper1"}
	idx := 0
	for _, c := range calls {
		if idx < len(wantOrder) && c == wantOrder[idx] {
			idx++
		}
	}
	if idx != len(wantOrder) {
		t.Errorf("calls = %v, want in order %v", calls, wantOrder)
	}
	spec := s.eng.specs[0]
	if spec.Image != HelperImage || len(spec.Mounts) != 1 || spec.Mounts[0] != (engine.HelperMount{Type: "volume", Source: "web_data", Target: "/data"}) {
		t.Errorf("helper spec = %+v", spec)
	}
	if len(s.eng.puts) != 1 || s.eng.puts[0]["data/file.txt"] != "volume contents" {
		t.Errorf("put = %+v", s.eng.puts)
	}
	if len(s.eng.loads) != 1 || !slices.Contains(s.eng.loads[0], "manifest.json") {
		t.Errorf("loads = %v", s.eng.loads)
	}

	pending := readPending(t, s.env.DataDir)
	if !slices.Equal(pending.StartStacks, []string{"web"}) || !pending.PauseSchedule {
		t.Errorf("pending = %+v", pending)
	}
	if _, err := os.Stat(pending.StagedDB); err != nil {
		t.Errorf("staged database missing: %v", err)
	}
}

func readPending(t *testing.T, dataDir string) Pending {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dataDir, "restore", pendingFile))
	if err != nil {
		t.Fatal(err)
	}
	var p Pending
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHostRestoreSkipsPathOutsideRoots(t *testing.T) {
	s := newHostSetup(t)
	// "web" now lives outside the allowed roots, in a sibling whose name only
	// shares a prefix with one of them.
	s.fixture.stacks[1].ComposePath = filepath.Join(s.rootA+"-evil", "web", "compose.yaml")
	path := s.fixture.build(t, t.TempDir(), true)

	report, err := RestoreHost(context.Background(), pathSource(path), s.env, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.rootA + "-evil"); !errors.Is(err, os.ErrNotExist) {
		t.Error("files were written outside the allowed roots")
	}
	if !slices.ContainsFunc(report.Items, func(it ItemResult) bool {
		return it.Stack == "web" && it.Kind == "stack-files" && it.Status == "skipped"
	}) {
		t.Errorf("no skipped item for web: %+v", report.Items)
	}
	for _, c := range s.eng.changes() {
		if strings.HasPrefix(c, "create") || c == "load" || strings.HasPrefix(c, "volume") {
			t.Errorf("data of the skipped stack was restored: %v", s.eng.changes())
		}
	}
	if _, err := os.ReadFile(filepath.Join(s.rootB, "deep", "api", "docker-compose.yml")); err != nil {
		t.Errorf("the other stack was not restored: %v", err)
	}
	if p := readPending(t, s.env.DataDir); len(p.StartStacks) != 0 {
		t.Errorf("a skipped stack would be started: %v", p.StartStacks)
	}
}

func TestHostRestoreNetworkFailureContinues(t *testing.T) {
	s := newHostSetup(t)
	s.eng.netErr = map[string]error{"lan": errors.New("parent interface eth9 not found")}
	s.fixture.networks = append(s.fixture.networks, engine.NetworkSpec{Name: "exists", Driver: "bridge"})
	s.eng.networks = []engine.Network{{Name: "exists", Driver: "bridge"}}
	path := s.fixture.build(t, t.TempDir(), false)

	report, err := RestoreHost(context.Background(), pathSource(path), s.env, true)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, it := range report.Items {
		if it.Kind == "network" {
			status[it.Name] = it.Status
		}
	}
	if status["lan"] != "failed" || status["backend"] != "ok" || status["exists"] != "skipped" {
		t.Errorf("network items = %v", status)
	}
	if slices.Contains(s.eng.changes(), "network:exists") {
		t.Error("an existing network was created again")
	}
	if _, err := os.Stat(filepath.Join(s.rootA, "web", "compose.yaml")); err != nil {
		t.Errorf("restore stopped after the network failure: %v", err)
	}
	if len(s.eng.puts) != 1 {
		t.Errorf("volumes were not restored after the network failure")
	}
}

func TestInspectHostListsConflicts(t *testing.T) {
	s := newHostSetup(t)
	s.eng.volumes = map[string]bool{"web_data": true}
	s.eng.networks = []engine.Network{{Name: "backend", Driver: "bridge"}}
	s.eng.containers = []engine.Container{
		{Name: "web-app-1", Labels: map[string]string{"com.docker.compose.project": "web"}},
		{Name: "other-1", Labels: map[string]string{"com.docker.compose.project": "other"}},
	}
	if err := os.MkdirAll(filepath.Join(s.rootA, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := s.fixture.build(t, t.TempDir(), true)

	sum, err := InspectHost(context.Background(), pathSource(path), s.env)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Manifest.Host != "old" || len(sum.Manifest.Stacks) != 2 {
		t.Errorf("manifest = %+v", sum.Manifest)
	}
	want := []Conflict{
		{Kind: "volume", Name: "web_data", Action: "overwrite"},
		{Kind: "network", Name: "backend", Action: "recreate"},
		{Kind: "container", Name: "web-app-1", Action: "recreate"},
		{Kind: "stack-dir", Name: filepath.Join(s.rootA, "web"), Action: "overwrite"},
	}
	for _, c := range want {
		if !slices.Contains(sum.Conflicts, c) {
			t.Errorf("missing conflict %+v in %+v", c, sum.Conflicts)
		}
	}
	if len(sum.Conflicts) != len(want) {
		t.Errorf("conflicts = %+v", sum.Conflicts)
	}
	if c := s.eng.changes(); len(c) != 0 {
		t.Errorf("inspect changed the host: %v", c)
	}
}

func TestInspectHostWithoutSidecarReadsManifest(t *testing.T) {
	s := newHostSetup(t)
	path := s.fixture.build(t, t.TempDir(), false)
	sum, err := InspectHost(context.Background(), pathSource(path), s.env)
	if err != nil || sum.Manifest.Host != "old" {
		t.Fatalf("sum=%+v err=%v", sum, err)
	}
}

func stageForApply(t *testing.T, dataDir string, f hostFixture, start []string) {
	t.Helper()
	dir := filepath.Join(dataDir, "restore")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "stackstead.db")
	if err := os.WriteFile(staged, archivedDB(t, f.stacks, true), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(Pending{StagedDB: staged, StartStacks: start, PauseSchedule: true})
	if err := os.WriteFile(filepath.Join(dir, pendingFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestApplyPendingSwapsAndPausesSchedule(t *testing.T) {
	s := newHostSetup(t)
	live := filepath.Join(s.env.DataDir, "stackstead.db")
	db, err := store.Open(live)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	stageForApply(t, s.env.DataDir, s.fixture, []string{"web"})
	now := time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)

	p, err := ApplyPending(s.env.DataDir, now)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || !slices.Equal(p.StartStacks, []string{"web"}) {
		t.Fatalf("pending = %+v", p)
	}
	db, err = store.OpenExisting(live, store.OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	entries, err := stacks.NewRegistry(db, time.Now).List(context.Background())
	if err != nil || len(entries) != 2 {
		t.Fatalf("restored stacks = %v, %v", entries, err)
	}
	cfg, err := NewConfigStore(db).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Schedule.Enabled {
		t.Error("the schedule is still enabled after a host restore")
	}
	if !cfg.Local.Enabled || cfg.Local.Path != "/backups" {
		t.Errorf("the rest of the backup config was lost: %+v", cfg.Local)
	}
	var paused string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'backup_paused_after_restore'`).Scan(&paused); err != nil || paused != "true" {
		t.Errorf("paused flag = %q, %v", paused, err)
	}
	if _, err := os.Stat(live + ".bak-20261026T093000Z"); err != nil {
		t.Errorf("old database not kept: %v", err)
	}
	for _, f := range []string{"stackstead.db", pendingFile} {
		if _, err := os.Stat(filepath.Join(s.env.DataDir, "restore", f)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("staged %s left behind", f)
		}
	}
	if again, err := ApplyPending(s.env.DataDir, now); again != nil || err != nil {
		t.Errorf("second apply = %+v, %v", again, err)
	}
}

func TestApplyPendingNoMarker(t *testing.T) {
	p, err := ApplyPending(t.TempDir(), time.Now())
	if p != nil || err != nil {
		t.Fatalf("p=%+v err=%v", p, err)
	}
}
