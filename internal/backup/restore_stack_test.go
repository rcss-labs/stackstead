package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// restoreCtl is a fake RestoreControl that records into the engine's call
// log, so stack and Docker steps can be ordered against each other.
type restoreCtl struct {
	eng      *restoreEngine
	reg      *stacks.Registry
	onStop   func()
	startErr error
}

func (c *restoreCtl) StopForRestore(_ context.Context, stack string, out func(string)) error {
	if c.onStop != nil {
		c.onStop()
	}
	c.eng.record("stack-stop:" + stack)
	return nil
}

func (c *restoreCtl) StartRestored(_ context.Context, stack string, out func(string)) error {
	c.eng.record("stack-start:" + stack)
	return c.startErr
}

func (c *restoreCtl) RegisterRestored(ctx context.Context, e stacks.Entry) error {
	c.eng.record("register:" + e.Name)
	return c.reg.Add(ctx, e)
}

type restoreEnv struct {
	*runnerEnv
	eng       *restoreEngine
	ctl       *restoreCtl
	root      string // the only allowed root
	stacksDir string
	outside   string
}

func newRestoreEnv(t *testing.T) *restoreEnv {
	t.Helper()
	env := newRunnerEnv(t, localOnly(t))
	setInstalledVersion(t, "v1.4.0")
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &restoreEnv{runnerEnv: env, eng: &restoreEngine{}, root: filepath.Join(base, "srv"), outside: filepath.Join(base, "elsewhere")}
	e.stacksDir = filepath.Join(e.root, "stacks")
	if err := os.MkdirAll(e.stacksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.ctl = &restoreCtl{eng: e.eng, reg: env.reg}
	env.deps.Engine = e.eng
	env.deps.Restore = e.ctl
	env.deps.AllowedRoots = []string{e.root}
	env.deps.StacksDir = e.stacksDir
	env.deps.Version = "1.4.0"
	env.newRunner()
	return e
}

// addArchive stores the fixture's archive and sidecar in the local destination.
func (e *restoreEnv) addArchive(t *testing.T, f hostFixture) ArchiveRef {
	t.Helper()
	path := f.build(t, t.TempDir(), false)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sidecar, _ := json.Marshal(f.manifest())
	name := filepath.Base(path)
	e.local.archives[name] = raw
	e.local.sidecars[name] = sidecar
	return ArchiveRef{Dest: destLocal, Name: name}
}

// existingStack registers a stack here with its compose file and marks it running.
func (e *restoreEnv) existingStack(t *testing.T, name, file, content string, running bool) string {
	t.Helper()
	dir := filepath.Join(e.stacksDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.reg.Add(context.Background(), stacks.Entry{Name: name, ComposePath: path, Origin: stacks.OriginManaged, Project: name}); err != nil {
		t.Fatal(err)
	}
	status := stacks.StatusStopped
	if running {
		status = stacks.StatusRunning
	}
	e.stacks.summaries = append(e.stacks.summaries, stacks.Summary{Name: name, Status: status})
	return path
}

// run restores and waits for the operation to end.
func (e *restoreEnv) run(t *testing.T, req StackRestoreRequest) stacks.Operation {
	t.Helper()
	op, err := e.runner.RestoreStack(context.Background(), req, "alice")
	if err != nil {
		t.Fatalf("RestoreStack: %v", err)
	}
	e.deps.Ops.Wait(op.ID)
	final, err := e.deps.Ops.Get(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	return final
}

func (e *restoreEnv) restoreActions(t *testing.T) []actionlog.Entry {
	t.Helper()
	entries, err := e.deps.Actions.Recent(context.Background(), actionlog.Filter{KindPrefix: "backup.restore"})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// webFixture is an archive of stack web with a volume and an image.
func webFixture(composePath string, running bool) hostFixture {
	f := hostFixture{
		version: "1.3.0",
		stacks:  []stacks.Entry{{Name: "web", ComposePath: composePath, Origin: stacks.OriginManaged, Project: "web"}},
		files: map[string]string{
			"base/stacks/web/" + filepath.Base(composePath): "services: {app: {image: new}}\n",
			"base/stacks/web/conf/app.ini":                  "new=1\n",
		},
		volumes: map[string][]ManifestVolume{"web": {{Key: "v-web_data", Kind: "volume", Name: "web_data", Target: "/data"}}},
		volData: map[string]map[string]string{"web/v-web_data": {"file.txt": "restored"}},
		images:  map[string]map[string]map[string]string{"web": {"nginx:1.27": {"manifest.json": "[]"}}},
	}
	if running {
		f.running = []string{"web"}
	}
	return f
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(raw)
}

func indexOf(calls []string, prefix string) int {
	return slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func TestRestoreStackOrder(t *testing.T) {
	e := newRestoreEnv(t)
	compose := e.existingStack(t, "web", "compose.yaml", "services: {app: {image: old}}\n", true)
	ref := e.addArchive(t, webFixture(compose, true))
	preDir := filepath.Join(e.dataDir, "backups", "pre-restore", "web-20261026T030000Z")

	var atStop, atVolume []string
	e.ctl.onStop = func() {
		atStop = []string{readFile(t, compose), readFile(t, filepath.Join(preDir, "compose.yaml"))}
	}
	e.eng.onCreate = func() {
		if atVolume == nil {
			atVolume = []string{readFile(t, compose), readFile(t, filepath.Join(preDir, "compose.yaml"))}
		}
	}

	op := e.run(t, StackRestoreRequest{Archive: ref, Stack: "web", Files: true, Volumes: true, Images: true})
	if op.State != stacks.OpSucceeded || op.Stack != "web" || op.Action != "restore" {
		t.Fatalf("operation = %+v", op)
	}
	if atStop[0] != "services: {app: {image: old}}\n" || !strings.HasPrefix(atStop[1], "<") {
		t.Errorf("at stop: compose %q, pre-restore copy %q; want old files and no copy yet", atStop[0], atStop[1])
	}
	if atVolume[0] != "services: {app: {image: new}}\n" || atVolume[1] != "services: {app: {image: old}}\n" {
		t.Errorf("at the volume: compose %q, copy %q; want new files and the old ones copied", atVolume[0], atVolume[1])
	}
	calls := e.eng.changes()
	order := []int{indexOf(calls, "stack-stop:web"), indexOf(calls, "create:"), indexOf(calls, "put:"), indexOf(calls, "load"), indexOf(calls, "stack-start:web")}
	if slices.Contains(order, -1) || !slices.IsSorted(order) {
		t.Errorf("calls = %v, want stop, volume, image, start in that order", calls)
	}
	if got := e.eng.puts; len(got) != 1 || got[0]["file.txt"] != "restored" {
		t.Errorf("volume contents = %v", got)
	}
	acts := e.restoreActions(t)
	if len(acts) != 1 || acts[0].Scope != "web" || acts[0].Result != "success" || acts[0].Username != "alice" || acts[0].OperationID != op.ID || acts[0].Target != ref.Name {
		t.Errorf("action log = %+v", acts)
	}
}

func TestRestoreStackKeepsPreRestoreCopy(t *testing.T) {
	e := newRestoreEnv(t)
	compose := e.existingStack(t, "web", "compose.yaml", "services: {app: {image: old}}\n", false)
	if err := os.MkdirAll(filepath.Join(filepath.Dir(compose), "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(compose), "conf", "app.ini"), []byte("old=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := e.addArchive(t, webFixture(compose, false))

	op := e.run(t, StackRestoreRequest{Archive: ref, Stack: "web", Files: true})
	if op.State != stacks.OpSucceeded {
		t.Fatalf("operation = %+v", op)
	}
	preDir := filepath.Join(e.dataDir, "backups", "pre-restore", "web-20261026T030000Z")
	if got := readFile(t, filepath.Join(preDir, "compose.yaml")); got != "services: {app: {image: old}}\n" {
		t.Errorf("pre-restore compose = %q", got)
	}
	if got := readFile(t, filepath.Join(preDir, "conf", "app.ini")); got != "old=1\n" {
		t.Errorf("pre-restore conf = %q", got)
	}
	if got := readFile(t, filepath.Join(filepath.Dir(compose), "conf", "app.ini")); got != "new=1\n" {
		t.Errorf("restored conf = %q", got)
	}
	// A stack that was not running stays stopped.
	if calls := e.eng.changes(); indexOf(calls, "stack-stop") >= 0 || indexOf(calls, "stack-start") >= 0 {
		t.Errorf("calls = %v, want no stop or start", calls)
	}
}

func TestRestoreStackStartsEvenIfVolumeFails(t *testing.T) {
	e := newRestoreEnv(t)
	compose := e.existingStack(t, "web", "compose.yaml", "services: {}\n", true)
	ref := e.addArchive(t, webFixture(compose, true))
	e.eng.waitCode = 1 // emptying the volume fails

	op := e.run(t, StackRestoreRequest{Archive: ref, Stack: "web", Files: true, Volumes: true})
	if op.State != stacks.OpFailed {
		t.Fatalf("state = %s, want failed", op.State)
	}
	if indexOf(e.eng.changes(), "stack-start:web") < 0 {
		t.Errorf("calls = %v, want the stack started again", e.eng.changes())
	}
	if acts := e.restoreActions(t); len(acts) != 1 || acts[0].Result != "failed" || !strings.Contains(acts[0].Detail, "web_data") {
		t.Errorf("action log = %+v", acts)
	}
}

func TestRestoreStackStartsAgainWhenArchiveIsDamaged(t *testing.T) {
	e := newRestoreEnv(t)
	compose := e.existingStack(t, "web", "compose.yaml", "services: {}\n", true)
	f := webFixture(compose, true)
	path := truncatedAfterDB(t, t.TempDir(), f, true) // no manifest.json at the end
	raw, _ := os.ReadFile(path)
	name := ArchiveName("old", f.manifest().CreatedAt)
	sidecar, _ := json.Marshal(f.manifest())
	e.local.archives[name], e.local.sidecars[name] = raw, sidecar

	op := e.run(t, StackRestoreRequest{Archive: ArchiveRef{Dest: destLocal, Name: name}, Stack: "web", Files: true})
	if op.State != stacks.OpFailed || !strings.Contains(op.Error, "damaged") {
		t.Fatalf("operation = %+v, want failed as damaged", op)
	}
	if indexOf(e.eng.changes(), "stack-start:web") < 0 {
		t.Errorf("calls = %v, want the stopped stack started again", e.eng.changes())
	}
}

func TestRestoreVolumeEmptiesThenPuts(t *testing.T) {
	eng := &restoreEngine{}
	err := RestoreVolume(context.Background(), eng, "web", stacks.Mount{Kind: "volume", Source: "web_data", Target: "/data"},
		map[string]string{"com.docker.compose.project": "web"}, innerTar(t, map[string]string{"a.txt": "A"}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"volume:web_data", "create:find /data -mindepth 1 -delete", "start:helper1", "wait:helper1", "put:helper1:/", "remove:helper1"}
	if got := eng.changes(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	if m := eng.specs[0].Mounts[0]; m.Source != "web_data" || m.Target != "/data" || m.ReadOnly {
		t.Errorf("helper mount = %+v, want web_data read-write at /data", m)
	}
	if eng.volLabels["web_data"]["com.docker.compose.project"] != "web" {
		t.Errorf("volume labels = %v", eng.volLabels)
	}
	if eng.puts[0]["a.txt"] != "A" {
		t.Errorf("put = %v", eng.puts)
	}
}

func TestRestoreVolumeRemovesHelperOnError(t *testing.T) {
	for _, tc := range []struct {
		name string
		eng  *restoreEngine
	}{
		{"empty fails", &restoreEngine{waitCode: 2}},
		{"put fails", &restoreEngine{putErr: errors.New("disk full")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := RestoreVolume(context.Background(), tc.eng, "web", stacks.Mount{Kind: "bind", Source: "/srv/data", Target: "/data"}, nil, strings.NewReader(""))
			if err == nil {
				t.Fatal("no error")
			}
			if calls := tc.eng.changes(); calls[len(calls)-1] != "remove:helper1" {
				t.Errorf("calls = %v, want the helper removed last", calls)
			}
		})
	}
}

func TestRestoreRegistersNewStack(t *testing.T) {
	e := newRestoreEnv(t)
	// From another host, at a path outside this host's roots, under a new name.
	archived := filepath.Join(e.outside, "app", "compose.yaml")
	f := hostFixture{
		version: "1.3.0",
		stacks:  []stacks.Entry{{Name: "app", ComposePath: archived, Origin: stacks.OriginExternal, Project: "app"}},
		running: []string{"app"},
		files:   map[string]string{"base/stacks/app/compose.yaml": "services: {a: {image: x}}\n"},
		volumes: map[string][]ManifestVolume{"app": {{Key: "v-app_data", Kind: "volume", Name: "app_data", Target: "/data"}}},
		volData: map[string]map[string]string{"app/v-app_data": {"x": "1"}},
	}
	ref := e.addArchive(t, f)

	op := e.run(t, StackRestoreRequest{Archive: ref, Stack: "app", NewName: "app2", Files: true, Volumes: true})
	if op.State != stacks.OpSucceeded || op.Stack != "app2" {
		t.Fatalf("operation = %+v", op)
	}
	want := filepath.Join(e.stacksDir, "app2", "compose.yaml")
	got, err := e.reg.Get(context.Background(), "app2")
	if err != nil || got.ComposePath != want || got.Origin != stacks.OriginManaged || got.Project != "app2" {
		t.Fatalf("registered = %+v (%v), want app2 at %s, managed, project app2", got, err, want)
	}
	if readFile(t, want) != "services: {a: {image: x}}\n" {
		t.Errorf("compose = %q", readFile(t, want))
	}
	if labels := e.eng.volLabels["app2_data"]; labels["com.docker.compose.project"] != "app2" || labels["com.docker.compose.volume"] != "data" {
		t.Errorf("volume labels = %v, want app2_data owned by project app2", e.eng.volLabels)
	}
	calls := e.eng.changes()
	if indexOf(calls, "stack-stop") >= 0 || indexOf(calls, "register:app2") < 0 || indexOf(calls, "stack-start:app2") < indexOf(calls, "register:app2") {
		t.Errorf("calls = %v, want register then start (it was running at backup), no stop", calls)
	}
}

// R17: the archived compose content goes into the existing file's name.
func TestRestoreStackWritesIntoExistingComposeName(t *testing.T) {
	e := newRestoreEnv(t)
	existing := e.existingStack(t, "web", "docker-compose.yml", "services: {old: {}}\n", false)
	ref := e.addArchive(t, webFixture(filepath.Join(filepath.Dir(existing), "compose.yaml"), false))

	if op := e.run(t, StackRestoreRequest{Archive: ref, Stack: "web", Files: true}); op.State != stacks.OpSucceeded {
		t.Fatalf("operation = %+v", op)
	}
	if got := readFile(t, existing); got != "services: {app: {image: new}}\n" {
		t.Errorf("existing compose file = %q", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(existing), "compose.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a second compose file was written: %v", err)
	}
}

// R18: a volume whose backup failed is not restored.
func TestRestoreStackSkipsVolumeThatFailedAtBackup(t *testing.T) {
	e := newRestoreEnv(t)
	compose := e.existingStack(t, "web", "compose.yaml", "services: {}\n", false)
	f := webFixture(compose, false)
	f.items = []ItemResult{{Kind: "volume", Stack: "web", Name: "web_data", Status: "failed", Note: "copy broke"}}
	ref := e.addArchive(t, f)

	op := e.run(t, StackRestoreRequest{Archive: ref, Stack: "web", Volumes: true})
	if op.State != stacks.OpSucceeded {
		t.Fatalf("operation = %+v", op)
	}
	if calls := e.eng.changes(); indexOf(calls, "put:") >= 0 || indexOf(calls, "create:") >= 0 {
		t.Errorf("calls = %v, want the failed volume left alone", calls)
	}
}

func TestRestoreStackRefusesBeforeStarting(t *testing.T) {
	e := newRestoreEnv(t)
	compose := e.existingStack(t, "web", "compose.yaml", "services: {}\n", true)
	f := webFixture(compose, true)
	f.version = "9.0.0"
	ref := e.addArchive(t, f)
	if _, err := e.runner.RestoreStack(context.Background(), StackRestoreRequest{Archive: ref, Stack: "web", Files: true}, "alice"); !errors.Is(err, ErrArchiveNewer) {
		t.Errorf("newer archive err = %v, want ErrArchiveNewer", err)
	}

	f.version = "1.3.0"
	ref = e.addArchive(t, f)
	gate := make(chan struct{})
	busy, err := e.deps.Ops.Start("web", "up", func(context.Context, func(string)) error { <-gate; return nil })
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.runner.RestoreStack(context.Background(), StackRestoreRequest{Archive: ref, Stack: "web", Files: true}, "alice")
	close(gate)
	e.deps.Ops.Wait(busy.ID)
	if !errors.Is(err, stacks.ErrBusy) {
		t.Errorf("busy err = %v, want ErrBusy", err)
	}
	if calls := e.eng.changes(); len(calls) != 0 {
		t.Errorf("the host was changed: %v", calls)
	}
}

func TestPlanRestoreReadsArchivedCompose(t *testing.T) {
	e := newRestoreEnv(t)
	compose := e.existingStack(t, "web", "compose.yaml", "services: {app: {image: old}}\n", false)
	ref := e.addArchive(t, webFixture(compose, false))

	plan, err := e.runner.PlanRestore(context.Background(), StackRestoreRequest{Archive: ref, Stack: "web", Files: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []FileDiff{{Path: compose, Old: "services: {app: {image: old}}\n", New: "services: {app: {image: new}}\n"}}
	if !reflect.DeepEqual(plan.FileDiffs, want) || !plan.Overwrites {
		t.Errorf("plan = %+v", plan)
	}
	if len(e.eng.changes()) != 0 {
		t.Errorf("planning changed the host: %v", e.eng.changes())
	}
}

func TestArchiveManifestValidatesName(t *testing.T) {
	e := newRestoreEnv(t)
	ref := e.addArchive(t, webFixture(filepath.Join(e.stacksDir, "web", "compose.yaml"), false))
	m, err := e.runner.ArchiveManifest(context.Background(), destLocal, ref.Name)
	if err != nil || len(m.Stacks) != 1 || m.Stacks[0].Name != "web" {
		t.Errorf("manifest = %+v (%v)", m, err)
	}
	for _, bad := range [][2]string{{"ftp", ref.Name}, {destLocal, "../" + ref.Name}, {destLocal, "notes.txt"}} {
		if _, err := e.runner.ArchiveManifest(context.Background(), bad[0], bad[1]); !errors.Is(err, ErrBadArchiveRef) {
			t.Errorf("ArchiveManifest(%q, %q) err = %v, want ErrBadArchiveRef", bad[0], bad[1], err)
		}
	}
}
