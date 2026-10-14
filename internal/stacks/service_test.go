package stacks_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/composecli"
	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

type runCall struct {
	File string
	Args []string
}

type fakeCompose struct {
	mu             sync.Mutex
	configErr      error
	configFiles    []string
	configContents []string
	projectFn      func(file string) string
	projectErr     error
	versionErr     error
	runErr         error
	runs           []runCall
	runGate        chan struct{} // when set, Run blocks until it is closed
}

func (f *fakeCompose) Version(context.Context) (string, error) { return "2.29.0", f.versionErr }

func (f *fakeCompose) Config(_ context.Context, file string) error {
	data, _ := os.ReadFile(file)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configFiles = append(f.configFiles, file)
	f.configContents = append(f.configContents, string(data))
	return f.configErr
}

func (f *fakeCompose) ProjectName(_ context.Context, file string) (string, error) {
	f.mu.Lock()
	perr, fn := f.projectErr, f.projectFn
	f.mu.Unlock()
	if perr != nil {
		return "", perr
	}
	if fn != nil {
		return fn(file), nil
	}
	return filepath.Base(filepath.Dir(file)), nil
}

func (f *fakeCompose) Run(ctx context.Context, file string, args []string, out func(string)) error {
	f.mu.Lock()
	f.runs = append(f.runs, runCall{file, args})
	gate, err := f.runGate, f.runErr
	f.mu.Unlock()
	out("running " + strings.Join(args, " "))
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (f *fakeCompose) lastRun() runCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs[len(f.runs)-1]
}

type fakeEngine struct {
	onContainers func() // test hook, runs on every Containers call
	err          error
	containers   []engine.Container
	networks     []engine.Network
}

func (e *fakeEngine) Ping(context.Context) error { return e.err }
func (e *fakeEngine) Containers(context.Context) ([]engine.Container, error) {
	if e.onContainers != nil {
		e.onContainers()
	}
	return e.containers, e.err
}
func (e *fakeEngine) Networks(context.Context) ([]engine.Network, error) { return e.networks, e.err }

type harness struct {
	svc     *stacks.Service
	reg     *stacks.Registry
	ops     *stacks.Ops
	compose *fakeCompose
	eng     *fakeEngine
	dir     string // resolved StacksDir
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		reg:     newRegistry(t),
		ops:     stacks.NewOps(time.Minute, nil),
		compose: &fakeCompose{},
		eng:     &fakeEngine{},
		dir:     filepath.Join(dir, "stacks"),
	}
	t.Cleanup(func() { h.ops.Shutdown(context.Background()) })
	h.svc = stacks.NewService(stacks.Deps{
		Registry: h.reg, Ops: h.ops, Compose: h.compose, Engine: h.eng,
		StacksDir: h.dir, AllowedRoots: []string{dir},
		Now: func() time.Time { return fixedNow },
	})
	return h
}

func (h *harness) create(t *testing.T, name string, cfg composefile.ServiceConfig) stacks.Summary {
	t.Helper()
	s, err := h.svc.Create(context.Background(), stacks.CreateRequest{Name: name, Config: cfg})
	if err != nil {
		t.Fatalf("Create %s: %v", name, err)
	}
	return s
}

func (h *harness) path(name string) string { return filepath.Join(h.dir, name, "compose.yaml") }

func wait(t *testing.T, ops *stacks.Ops, id string) stacks.Operation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		op, err := ops.Get(id)
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

func fieldsOf(t *testing.T, err error) []stacks.FieldError {
	t.Helper()
	var ve *stacks.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %v", err)
	}
	return ve.Fields
}

func webCfg(ports ...string) composefile.ServiceConfig {
	return composefile.ServiceConfig{Image: "nginx:1", Ports: ports}
}

func publishing(project string, port uint16) engine.Container {
	return engine.Container{
		ID: "c-" + project, Name: project + "-web-1", State: "running",
		Labels: map[string]string{"com.docker.compose.project": project, "com.docker.compose.service": "web"},
		Ports:  []engine.Port{{IP: "0.0.0.0", PrivatePort: 80, PublicPort: port, Type: "tcp"}},
	}
}

// ---- Create ----

func TestCreateWritesFileAndRegisters(t *testing.T) {
	h := newHarness(t)
	sum, err := h.svc.Create(context.Background(), stacks.CreateRequest{
		Name:   "web",
		Config: composefile.ServiceConfig{Build: &composefile.Build{Dockerfile: "Dockerfile"}, Ports: []string{"8080:80"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(h.path("web"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "stackstead/web:latest") {
		t.Errorf("image tag not defaulted:\n%s", data)
	}
	e, err := h.reg.Get(context.Background(), "web")
	if err != nil {
		t.Fatalf("not registered: %v", err)
	}
	if e.ComposePath != h.path("web") || e.Origin != stacks.OriginManaged || e.Project != "web" {
		t.Errorf("entry = %+v", e)
	}
	if sum.Name != "web" || sum.Path != h.path("web") || sum.Project != "web" || sum.ChecksPartial {
		t.Errorf("summary = %+v", sum)
	}
	if len(h.compose.configFiles) != 1 || h.compose.configFiles[0] != h.path("web") {
		t.Errorf("Config called on %v", h.compose.configFiles)
	}
}

func TestCreateConfigFailureLeavesNoTrace(t *testing.T) {
	h := newHarness(t)
	h.compose.configErr = &composecli.ExitError{Code: 1, Output: "services.web.image invalid"}
	_, err := h.svc.Create(context.Background(), stacks.CreateRequest{Name: "web", Config: webCfg()})
	var ee *composecli.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("want ExitError, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "web")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("folder left behind: %v", err)
	}
	if _, err := h.reg.Get(context.Background(), "web"); !errors.Is(err, stacks.ErrStackNotFound) {
		t.Errorf("row left behind: %v", err)
	}
}

func TestCreateProjectNameFailureRollsBack(t *testing.T) {
	h := newHarness(t)
	h.compose.projectErr = errors.New("boom")
	if _, err := h.svc.Create(context.Background(), stacks.CreateRequest{Name: "web", Config: webCfg()}); err == nil {
		t.Fatal("want error")
	}
	if _, err := os.Stat(filepath.Join(h.dir, "web")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("folder left behind: %v", err)
	}
}

func TestCreatePortConflictWithRunningContainer(t *testing.T) {
	h := newHarness(t)
	h.eng.containers = []engine.Container{publishing("other", 8080)}
	_, err := h.svc.Create(context.Background(), stacks.CreateRequest{Name: "web", Config: webCfg("8080:80")})
	fs := fieldsOf(t, err)
	if len(fs) != 1 || fs[0].Field != "ports[0]" {
		t.Errorf("fields = %+v", fs)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "web")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("folder written despite conflict: %v", err)
	}
}

func TestCreatePortConflictWithOtherStackDeclaredPort(t *testing.T) {
	h := newHarness(t)
	h.create(t, "a", webCfg("8080:80"))
	_, err := h.svc.Create(context.Background(), stacks.CreateRequest{Name: "b", Config: webCfg("8080:80")})
	if fs := fieldsOf(t, err); len(fs) != 1 || fs[0].Field != "ports[0]" {
		t.Errorf("fields = %+v", fs)
	}
}

func TestCreateEngineDownStillChecksDeclaredPortsAndReportsPartial(t *testing.T) {
	h := newHarness(t)
	h.create(t, "a", webCfg("8080:80"))
	h.eng.err = engine.ErrUnavailable

	_, err := h.svc.Create(context.Background(), stacks.CreateRequest{Name: "b", Config: webCfg("8080:80")})
	fieldsOf(t, err)

	sum, err := h.svc.Create(context.Background(), stacks.CreateRequest{Name: "c", Config: webCfg("9090:80")})
	if err != nil {
		t.Fatal(err)
	}
	if !sum.ChecksPartial {
		t.Error("ChecksPartial = false with engine down")
	}
}

func TestCreateExistingFolderIsErrStackExistsAndUntouched(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(h.dir, "web")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "mine.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.Create(context.Background(), stacks.CreateRequest{Name: "web", Config: webCfg()})
	if !errors.Is(err, stacks.ErrStackExists) {
		t.Fatalf("err = %v", err)
	}
	if b, err := os.ReadFile(keep); err != nil || string(b) != "x" {
		t.Errorf("pre-existing folder was modified: %v", err)
	}
}

func TestCreateRejectsInvalidNameAndEscapingBuildContext(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.Create(context.Background(), stacks.CreateRequest{Name: "../x", Config: webCfg()})
	if fs := fieldsOf(t, err); fs[0].Field != "name" {
		t.Errorf("fields = %+v", fs)
	}
	_, err = h.svc.Create(context.Background(), stacks.CreateRequest{
		Name: "web", Config: composefile.ServiceConfig{Build: &composefile.Build{Context: "/etc"}},
	})
	if fs := fieldsOf(t, err); fs[0].Field != "build.context" {
		t.Errorf("fields = %+v", fs)
	}
}

func TestCreateRejectsStaticIPOutsideSubnet(t *testing.T) {
	h := newHarness(t)
	h.eng.networks = []engine.Network{{Name: "lan", Subnets: []engine.Subnet{{Prefix: mustPrefix("10.0.0.0/24")}}}}
	_, err := h.svc.Create(context.Background(), stacks.CreateRequest{
		Name: "web", Config: composefile.ServiceConfig{
			Image:    "nginx",
			Networks: []composefile.NetworkAttachment{{Name: "lan", IPv4Address: "192.168.1.5"}},
		},
	})
	if fs := fieldsOf(t, err); len(fs) != 1 || fs[0].Field != "networks.lan.ipv4_address" {
		t.Errorf("fields = %+v", fs)
	}
}

// ---- Import ----

const externalYAML = "# mine\nservices:\n  app:\n    image: busybox\n"

func TestImportRegistersWithComposeProjectAndNeverWrites(t *testing.T) {
	h := newHarness(t)
	ext := filepath.Join(t.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(ext, []byte(externalYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	h.compose.projectFn = func(string) string { return "legacy" }
	sum, err := h.svc.Import(context.Background(), ext)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Name != "legacy" || sum.Project != "legacy" || sum.Origin != stacks.OriginExternal {
		t.Errorf("summary = %+v", sum)
	}
	resolved, _ := filepath.EvalSymlinks(ext)
	if sum.Path != resolved {
		t.Errorf("path = %q want %q", sum.Path, resolved)
	}
	if b, _ := os.ReadFile(ext); string(b) != externalYAML {
		t.Errorf("file rewritten: %q", b)
	}
	if _, err := os.Stat(ext + ".bak"); err == nil {
		t.Error("import created a .bak")
	}
	if _, err := h.svc.Import(context.Background(), ext); !errors.Is(err, stacks.ErrStackExists) {
		t.Errorf("duplicate import err = %v", err)
	}
}

func TestImportRejectsRelativePathAndConfigFailure(t *testing.T) {
	h := newHarness(t)
	fieldsOf(t, func() error { _, err := h.svc.Import(context.Background(), "rel/compose.yaml"); return err }())

	ext := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(ext, []byte(externalYAML), 0o644)
	h.compose.configErr = &composecli.ExitError{Code: 1}
	if _, err := h.svc.Import(context.Background(), ext); err == nil {
		t.Fatal("want config error")
	}
	if rows, _ := h.reg.List(context.Background()); len(rows) != 0 {
		t.Errorf("rows = %v", rows)
	}
}

// ---- UpdateService / SaveRaw ----

func setupWeb(t *testing.T, h *harness) string {
	t.Helper()
	h.create(t, "web", webCfg("8080:80"))
	_, hash, err := h.svc.Raw(context.Background(), "web")
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func checkTempFiles(t *testing.T, h *harness) {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(h.dir, "web", ".stackstead-check-*"))
	if len(m) != 0 {
		t.Errorf("temp files left: %v", m)
	}
}

func TestUpdateServiceStaleHashIsErrConflict(t *testing.T) {
	h := newHarness(t)
	setupWeb(t, h)
	_, err := h.svc.UpdateService(context.Background(), "web", "web", webCfg("8080:80"), "stale")
	if !errors.Is(err, composefile.ErrConflict) {
		t.Errorf("err = %v", err)
	}
}

func TestUpdateServiceConfigFailureLeavesFileUnchanged(t *testing.T) {
	h := newHarness(t)
	hash := setupWeb(t, h)
	before, _ := os.ReadFile(h.path("web"))
	h.compose.configErr = &composecli.ExitError{Code: 1, Output: "nope"}
	_, err := h.svc.UpdateService(context.Background(), "web", "web", composefile.ServiceConfig{Image: "nginx:2", Ports: []string{"8080:80"}}, hash)
	var ee *composecli.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v", err)
	}
	after, _ := os.ReadFile(h.path("web"))
	if string(before) != string(after) {
		t.Error("file changed")
	}
	checkTempFiles(t, h)
}

func TestUpdateServiceSuccessReturnsNewHashAndKeepsBackup(t *testing.T) {
	h := newHarness(t)
	hash := setupWeb(t, h)
	before, _ := os.ReadFile(h.path("web"))
	h.compose.configFiles, h.compose.configContents = nil, nil

	res, err := h.svc.UpdateService(context.Background(), "web", "web", composefile.ServiceConfig{Image: "nginx:2", Ports: []string{"8080:80"}}, hash)
	if err != nil {
		t.Fatal(err)
	}
	newHash := res.Hash
	data, got, _ := composefile.Load(h.path("web"))
	if got != newHash || !strings.Contains(string(data), "nginx:2") {
		t.Errorf("hash %q vs %q; content %s", newHash, got, data)
	}
	if bak, err := os.ReadFile(h.path("web") + ".bak"); err != nil || string(bak) != string(before) {
		t.Errorf("backup: %v", err)
	}
	if len(h.compose.configFiles) != 1 || filepath.Dir(h.compose.configFiles[0]) != filepath.Dir(h.path("web")) ||
		!strings.HasPrefix(filepath.Base(h.compose.configFiles[0]), ".stackstead-check-") {
		t.Errorf("config checked %v", h.compose.configFiles)
	}
	if !strings.Contains(h.compose.configContents[0], "nginx:2") {
		t.Error("candidate bytes were not what got validated")
	}
	checkTempFiles(t, h)
}

func TestUpdateServicePortCheckIgnoresOwnContainersButNotOthers(t *testing.T) {
	h := newHarness(t)
	hash := setupWeb(t, h)
	cfg := webCfg("8080:80", "9090:90")

	h.eng.containers = []engine.Container{publishing("web", 9090)}
	res, err := h.svc.UpdateService(context.Background(), "web", "web", cfg, hash)
	newHash := res.Hash
	if err != nil {
		t.Fatalf("own container blocked the edit: %v", err)
	}

	h.eng.containers = []engine.Container{publishing("other", 7070)}
	_, err = h.svc.UpdateService(context.Background(), "web", "web", webCfg("8080:80", "9090:90", "7070:70"), newHash)
	if fs := fieldsOf(t, err); len(fs) != 1 || fs[0].Field != "ports[2]" {
		t.Errorf("fields = %+v", fs)
	}
}

func TestUpdateServiceDoesNotRecheckUnchangedPorts(t *testing.T) {
	h := newHarness(t)
	hash := setupWeb(t, h)
	h.eng.containers = []engine.Container{publishing("other", 8080)} // someone grabbed it since
	if _, err := h.svc.UpdateService(context.Background(), "web", "web", composefile.ServiceConfig{Image: "nginx:2", Ports: []string{"8080:80"}}, hash); err != nil {
		t.Fatalf("unchanged port was re-validated: %v", err)
	}
}

func TestUpdateServiceRefreshesProject(t *testing.T) {
	h := newHarness(t)
	hash := setupWeb(t, h)
	h.compose.projectFn = func(string) string { return "renamed" }
	if _, err := h.svc.UpdateService(context.Background(), "web", "web", composefile.ServiceConfig{Image: "nginx:2", Ports: []string{"8080:80"}}, hash); err != nil {
		t.Fatal(err)
	}
	if e, _ := h.reg.Get(context.Background(), "web"); e.Project != "renamed" {
		t.Errorf("project = %q", e.Project)
	}
}

func TestSaveRaw(t *testing.T) {
	h := newHarness(t)
	hash := setupWeb(t, h)
	ctx := context.Background()

	if _, err := h.svc.SaveRaw(ctx, "web", []byte("services: ["), hash); !errors.Is(err, composefile.ErrInvalidConfig) {
		t.Errorf("bad yaml err = %v", err)
	}
	if _, err := h.svc.SaveRaw(ctx, "web", []byte("services:\n  web:\n    image: a\n"), "stale"); !errors.Is(err, composefile.ErrConflict) {
		t.Errorf("stale err = %v", err)
	}
	h.compose.configErr = &composecli.ExitError{Code: 1}
	if _, err := h.svc.SaveRaw(ctx, "web", []byte("services:\n  web:\n    image: a\n"), hash); err == nil {
		t.Error("want config error")
	}
	h.compose.configErr = nil
	h.eng.containers = []engine.Container{publishing("other", 7070)}
	_, err := h.svc.SaveRaw(ctx, "web", []byte("services:\n  web:\n    image: a\n    ports:\n      - \"7070:70\"\n"), hash)
	if fs := fieldsOf(t, err); fs[0].Field != "services.web.ports[0]" {
		t.Errorf("fields = %+v", fs)
	}
	checkTempFiles(t, h)

	h.eng.containers = nil
	want := "services:\n  web:\n    image: a\n"
	res, err := h.svc.SaveRaw(ctx, "web", []byte(want), hash)
	nh := res.Hash
	if err != nil {
		t.Fatal(err)
	}
	got, gh, _ := h.svc.Raw(ctx, "web")
	if string(got) != want || gh != nh {
		t.Errorf("raw = %q hash %q/%q", got, gh, nh)
	}
}

// ---- Act ----

func TestActPassesComposeArgs(t *testing.T) {
	want := map[string][]string{
		"up":      {"up", "-d", "--build", "--remove-orphans"},
		"down":    {"down"},
		"restart": {"restart"},
		"pull":    {"pull"},
		"build":   {"build"},
	}
	for action, args := range want {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t)
			setupWeb(t, h)
			op, err := h.svc.Act(context.Background(), "web", action)
			if err != nil {
				t.Fatal(err)
			}
			if fin := wait(t, h.ops, op.ID); fin.State != stacks.OpSucceeded {
				t.Fatalf("state = %v (%s)", fin.State, fin.Error)
			}
			got := h.compose.lastRun()
			if got.File != h.path("web") || strings.Join(got.Args, " ") != strings.Join(args, " ") {
				t.Errorf("run = %+v", got)
			}
		})
	}
}

func TestActBusyUnknownActionAndUnavailable(t *testing.T) {
	h := newHarness(t)
	setupWeb(t, h)
	ctx := context.Background()
	h.compose.runGate = make(chan struct{})
	op, err := h.svc.Act(ctx, "web", "up")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Act(ctx, "web", "restart"); !errors.Is(err, stacks.ErrBusy) {
		t.Errorf("err = %v", err)
	}
	close(h.compose.runGate)
	wait(t, h.ops, op.ID)

	fieldsOf(t, func() error { _, err := h.svc.Act(ctx, "web", "explode"); return err }())

	os.Remove(h.path("web"))
	if _, err := h.svc.Act(ctx, "web", "up"); !errors.Is(err, stacks.ErrStackUnavailable) {
		t.Errorf("err = %v", err)
	}
	if _, err := h.svc.Act(ctx, "nope", "up"); !errors.Is(err, stacks.ErrStackNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestActEngineDownFailsFast(t *testing.T) {
	h := newHarness(t)
	setupWeb(t, h)
	h.eng.err = engine.ErrUnavailable
	if _, err := h.svc.Act(context.Background(), "web", "up"); !errors.Is(err, engine.ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
}

// ---- Remove ----

func TestRemoveSuccessUnregistersAndKeepsFiles(t *testing.T) {
	h := newHarness(t)
	setupWeb(t, h)
	op, err := h.svc.Remove(context.Background(), "web", true)
	if err != nil {
		t.Fatal(err)
	}
	if fin := wait(t, h.ops, op.ID); fin.State != stacks.OpSucceeded {
		t.Fatalf("state = %v (%s)", fin.State, fin.Error)
	}
	if got := strings.Join(h.compose.lastRun().Args, " "); got != "down --volumes" {
		t.Errorf("args = %q", got)
	}
	if _, err := h.reg.Get(context.Background(), "web"); !errors.Is(err, stacks.ErrStackNotFound) {
		t.Errorf("row still there: %v", err)
	}
	if _, err := os.Stat(h.path("web")); err != nil {
		t.Errorf("file gone: %v", err)
	}
}

func TestRemoveFailureKeepsRow(t *testing.T) {
	h := newHarness(t)
	setupWeb(t, h)
	h.compose.runErr = &composecli.ExitError{Code: 1}
	op, _ := h.svc.Remove(context.Background(), "web", false)
	if fin := wait(t, h.ops, op.ID); fin.State != stacks.OpFailed {
		t.Fatalf("state = %v", fin.State)
	}
	if _, err := h.reg.Get(context.Background(), "web"); err != nil {
		t.Errorf("row lost: %v", err)
	}
}

func TestRemoveStackWhoseFileVanishedStillUnregisters(t *testing.T) {
	h := newHarness(t)
	setupWeb(t, h)
	os.Remove(h.path("web"))
	op, err := h.svc.Remove(context.Background(), "web", false)
	if err != nil {
		t.Fatal(err)
	}
	if fin := wait(t, h.ops, op.ID); fin.State != stacks.OpSucceeded {
		t.Fatalf("state = %v (%s)", fin.State, fin.Error)
	}
	if _, err := h.reg.Get(context.Background(), "web"); !errors.Is(err, stacks.ErrStackNotFound) {
		t.Errorf("row still there: %v", err)
	}
}

// ---- List / Get ----

func summaryByName(t *testing.T, list []stacks.Summary, name string) stacks.Summary {
	t.Helper()
	for _, s := range list {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("%s not listed", name)
	return stacks.Summary{}
}

func TestListIsolatesBrokenStack(t *testing.T) {
	h := newHarness(t)
	h.create(t, "good", webCfg("8080:80"))
	h.create(t, "gone", webCfg("8081:80"))
	h.create(t, "idle", webCfg("8082:80"))
	os.Remove(h.path("gone"))
	good := publishing("good", 8080)
	good.Labels["com.docker.compose.service"] = "good" // Create names the service after the stack
	h.eng.containers = []engine.Container{good}

	list, err := h.svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s := summaryByName(t, list, "good"); s.Status != stacks.StatusRunning || s.Services != 1 || s.Running != 1 {
		t.Errorf("good = %+v", s)
	}
	if s := summaryByName(t, list, "gone"); s.Status != stacks.StatusUnavailable {
		t.Errorf("gone = %+v", s)
	}
	if s := summaryByName(t, list, "idle"); s.Status != stacks.StatusStopped {
		t.Errorf("idle = %+v", s)
	}
}

func TestListPartialStatus(t *testing.T) {
	h := newHarness(t)
	h.create(t, "multi", webCfg())
	raw := "services:\n  web:\n    image: a\n  db:\n    image: b\n"
	_, hash, _ := h.svc.Raw(context.Background(), "multi")
	if _, err := h.svc.SaveRaw(context.Background(), "multi", []byte(raw), hash); err != nil {
		t.Fatal(err)
	}
	h.eng.containers = []engine.Container{publishing("multi", 8080)}
	list, _ := h.svc.List(context.Background())
	if s := summaryByName(t, list, "multi"); s.Status != stacks.StatusPartial || s.Running != 1 || s.Services != 2 {
		t.Errorf("multi = %+v", s)
	}
}

func TestListEngineDownIsUnknownForAll(t *testing.T) {
	h := newHarness(t)
	h.create(t, "a", webCfg())
	h.create(t, "b", webCfg())
	h.eng.err = engine.ErrUnavailable
	list, err := h.svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.Status != stacks.StatusUnknown {
			t.Errorf("%s = %v", s.Name, s.Status)
		}
	}
}

func TestGetDetailAndBrokenFile(t *testing.T) {
	h := newHarness(t)
	h.create(t, "web", webCfg("8080:80"))
	h.eng.containers = []engine.Container{publishing("web", 8080)}
	d, err := h.svc.Get(context.Background(), "web")
	if err != nil {
		t.Fatal(err)
	}
	if d.Hash == "" || len(d.Services) != 1 || d.Services[0].Config.Image != "nginx:1" ||
		len(d.Services[0].Containers) != 1 || d.Services[0].Containers[0].Service != "web" {
		t.Errorf("detail = %+v", d)
	}
	os.WriteFile(h.path("web"), []byte("services: ["), 0o644)
	d, err = h.svc.Get(context.Background(), "web")
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != stacks.StatusUnavailable || d.ParseError == "" {
		t.Errorf("broken detail = %+v", d)
	}
	if _, err := h.svc.Get(context.Background(), "nope"); !errors.Is(err, stacks.ErrStackNotFound) {
		t.Errorf("err = %v", err)
	}
}

// ---- Sync ----

func TestSyncAdoptsFoldersWithComposeYaml(t *testing.T) {
	h := newHarness(t)
	for _, n := range []string{"alpha", "beta"} {
		os.MkdirAll(filepath.Join(h.dir, n), 0o755)
	}
	os.WriteFile(h.path("alpha"), []byte(externalYAML), 0o644)
	os.WriteFile(filepath.Join(h.dir, "stray.txt"), []byte("x"), 0o644)

	for i := 0; i < 2; i++ { // second run must be a no-op
		if err := h.svc.Sync(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := h.reg.List(context.Background())
	if len(rows) != 1 || rows[0].Name != "alpha" || rows[0].Origin != stacks.OriginManaged || rows[0].ComposePath != h.path("alpha") {
		t.Errorf("rows = %+v", rows)
	}
}

func TestSyncWithMissingStacksDirIsNoop(t *testing.T) {
	h := newHarness(t)
	if err := h.svc.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func mustPrefix(s string) netip.Prefix { return netip.MustParsePrefix(s) }

// ---- Fix round 1 ----

var nameLine = regexp.MustCompile(`(?m)^name: (\S+)`)

// projectFromFile mimics compose: a top-level name: wins, else the folder name.
func projectFromFile(file string) string {
	b, _ := os.ReadFile(file)
	if m := nameLine.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return filepath.Base(filepath.Dir(file))
}

func rawWithName(name, service string) []byte {
	return []byte("name: " + name + "\nservices:\n  " + service + ":\n    image: a\n")
}

func TestSaveRawRefusesProjectOwnedByAnotherStack(t *testing.T) {
	h := newHarness(t)
	h.compose.projectFn = projectFromFile
	h.create(t, "alpha", webCfg())
	h.create(t, "beta", webCfg())
	_, hash, _ := h.svc.Raw(context.Background(), "beta")
	before, _ := os.ReadFile(h.path("beta"))

	_, err := h.svc.SaveRaw(context.Background(), "beta", rawWithName("alpha", "beta"), hash)
	if fs := fieldsOf(t, err); len(fs) != 1 || fs[0].Field != "name" || !strings.Contains(fs[0].Message, `"alpha"`) {
		t.Errorf("fields = %+v", fs)
	}
	if after, _ := os.ReadFile(h.path("beta")); string(after) != string(before) {
		t.Error("file was saved despite project collision")
	}
	if e, _ := h.reg.Get(context.Background(), "beta"); e.Project != "beta" {
		t.Errorf("project = %q", e.Project)
	}
	checkTempFilesIn(t, h, "beta")
}

func checkTempFilesIn(t *testing.T, h *harness, name string) {
	t.Helper()
	if m, _ := filepath.Glob(filepath.Join(h.dir, name, ".stackstead-check-*")); len(m) != 0 {
		t.Errorf("temp files left: %v", m)
	}
}

func TestSaveRawKeepingOwnProjectAndRenamingToFreeNameSucceed(t *testing.T) {
	h := newHarness(t)
	h.compose.projectFn = projectFromFile
	h.create(t, "alpha", webCfg())
	_, hash, _ := h.svc.Raw(context.Background(), "alpha")
	res, err := h.svc.SaveRaw(context.Background(), "alpha", rawWithName("alpha", "alpha"), hash)
	if err != nil {
		t.Fatalf("keeping own project: %v", err)
	}
	if _, err := h.svc.SaveRaw(context.Background(), "alpha", rawWithName("gamma", "alpha"), res.Hash); err != nil {
		t.Fatalf("free name: %v", err)
	}
	if e, _ := h.reg.Get(context.Background(), "alpha"); e.Project != "gamma" {
		t.Errorf("project = %q", e.Project)
	}
}

func TestSyncSkipsFolderWhoseProjectIsAlreadyRegistered(t *testing.T) {
	h := newHarness(t)
	h.compose.projectFn = projectFromFile
	h.create(t, "alpha", webCfg())
	os.MkdirAll(filepath.Join(h.dir, "other"), 0o755)
	os.WriteFile(h.path("other"), rawWithName("alpha", "x"), 0o644)

	err := h.svc.Sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "alpha") {
		t.Errorf("want a warning naming the project, got %v", err)
	}
	if rows, _ := h.reg.List(context.Background()); len(rows) != 1 {
		t.Errorf("rows = %+v", rows)
	}
}

func TestSyncComparesResolvedPaths(t *testing.T) {
	h := newHarness(t)
	real := filepath.Join(filepath.Dir(h.dir), "real")
	os.MkdirAll(real, 0o755)
	os.WriteFile(filepath.Join(real, "compose.yaml"), []byte(externalYAML), 0o644)
	os.MkdirAll(h.dir, 0o755)
	if err := os.Symlink(real, filepath.Join(h.dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Import(context.Background(), filepath.Join(real, "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if rows, _ := h.reg.List(context.Background()); len(rows) != 1 {
		t.Errorf("symlinked duplicate adopted: %+v", rows)
	}
}

func TestSyncStoresResolvedPath(t *testing.T) {
	h := newHarness(t)
	real := filepath.Join(filepath.Dir(h.dir), "elsewhere")
	os.MkdirAll(real, 0o755)
	os.WriteFile(filepath.Join(real, "compose.yaml"), []byte(externalYAML), 0o644)
	os.MkdirAll(h.dir, 0o755)
	os.Symlink(real, filepath.Join(h.dir, "linked"))
	if err := h.svc.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, _ := h.reg.List(context.Background())
	if len(rows) != 1 || rows[0].ComposePath != filepath.Join(real, "compose.yaml") {
		t.Errorf("rows = %+v", rows)
	}
}

func TestCreateRefusesProjectAlreadyRegisteredOrRunningUnregistered(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.reg.Add(ctx, stacks.Entry{Name: "x", ComposePath: "/x/compose.yaml", Origin: stacks.OriginExternal, Project: "web"})
	_, err := h.svc.Create(ctx, stacks.CreateRequest{Name: "web", Config: webCfg()})
	if fs := fieldsOf(t, err); fs[0].Field != "name" || !strings.Contains(fs[0].Message, "stack x") {
		t.Errorf("fields = %+v", fs)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "web")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("folder left: %v", err)
	}

	h.reg.Remove(ctx, "x")
	h.eng.containers = []engine.Container{publishing("web", 8080)}
	_, err = h.svc.Create(ctx, stacks.CreateRequest{Name: "web", Config: webCfg()})
	if fs := fieldsOf(t, err); !strings.Contains(fs[0].Message, "import it instead") {
		t.Errorf("fields = %+v", fs)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "web")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("folder left: %v", err)
	}
}

func TestImportAllowedForProjectWithUnregisteredContainers(t *testing.T) {
	h := newHarness(t)
	ext := filepath.Join(t.TempDir(), "compose.yaml")
	os.WriteFile(ext, []byte(externalYAML), 0o644)
	h.compose.projectFn = func(string) string { return "legacy" }
	h.eng.containers = []engine.Container{publishing("legacy", 8080)}
	if _, err := h.svc.Import(context.Background(), ext); err != nil {
		t.Fatal(err)
	}
}

func TestCreateTreatsRowAdoptedByConcurrentSyncAsSuccess(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.compose.projectFn = func(file string) string {
		// Sync adopts our folder between ProjectName and Create's own Add.
		h.reg.Add(ctx, stacks.Entry{Name: "web", ComposePath: file, Origin: stacks.OriginManaged, Project: "web"})
		return "web"
	}
	sum, err := h.svc.Create(ctx, stacks.CreateRequest{Name: "web", Config: webCfg()})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Name != "web" {
		t.Errorf("summary = %+v", sum)
	}
	if _, err := os.Stat(h.path("web")); err != nil {
		t.Errorf("folder was rolled back: %v", err)
	}
}

func TestRemoveMissingFileRefusedWhileProjectContainersExistOrEngineDown(t *testing.T) {
	h := newHarness(t)
	setupWeb(t, h)
	os.Remove(h.path("web"))
	ctx := context.Background()

	h.eng.containers = []engine.Container{publishing("web", 8080)}
	_, err := h.svc.Remove(ctx, "web", false)
	if !errors.Is(err, stacks.ErrStackUnavailable) || !strings.Contains(err.Error(), "1 containers of project web still exist") {
		t.Errorf("containers present: err = %v", err)
	}
	h.eng.containers, h.eng.err = nil, engine.ErrUnavailable
	if _, err := h.svc.Remove(ctx, "web", false); !errors.Is(err, stacks.ErrStackUnavailable) {
		t.Errorf("engine down: err = %v", err)
	}
	if _, err := h.reg.Get(ctx, "web"); err != nil {
		t.Errorf("row dropped: %v", err)
	}
}

func TestMissingComposePluginIsErrComposeUnavailable(t *testing.T) {
	h := newHarness(t)
	hash := setupWeb(t, h)
	ext := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(ext, []byte(externalYAML), 0o644)
	ctx := context.Background()
	h.compose.versionErr = composecli.ErrComposeUnavailable
	h.compose.configErr = &composecli.ExitError{Code: 125} // what a missing plugin looks like to Config

	calls := map[string]error{}
	_, calls["act"] = h.svc.Act(ctx, "web", "up")
	_, calls["remove"] = h.svc.Remove(ctx, "web", false)
	_, calls["create"] = h.svc.Create(ctx, stacks.CreateRequest{Name: "new", Config: webCfg()})
	_, calls["import"] = h.svc.Import(ctx, ext)
	_, calls["update"] = h.svc.UpdateService(ctx, "web", "web", composefile.ServiceConfig{Image: "x"}, hash)
	_, calls["saveraw"] = h.svc.SaveRaw(ctx, "web", []byte("services:\n  web:\n    image: a\n"), hash)
	for name, err := range calls {
		if !errors.Is(err, composecli.ErrComposeUnavailable) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(h.dir, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("create left a folder: %v", err)
	}
}

func TestEditsReportPartialChecksWhenEngineDown(t *testing.T) {
	h := newHarness(t)
	hash := setupWeb(t, h)
	h.eng.err = engine.ErrUnavailable
	res, err := h.svc.UpdateService(context.Background(), "web", "web", webCfg("8080:80", "9090:90"), hash)
	if err != nil || !res.ChecksPartial {
		t.Fatalf("update: %+v %v", res, err)
	}
	res, err = h.svc.SaveRaw(context.Background(), "web", []byte("services:\n  web:\n    image: a\n    ports:\n      - \"7070:70\"\n"), res.Hash)
	if err != nil || !res.ChecksPartial {
		t.Fatalf("saveraw: %+v %v", res, err)
	}
}

func TestStaticIPAlsoCheckedAgainstOtherStacksDeclaredIPs(t *testing.T) {
	h := newHarness(t)
	h.eng.networks = []engine.Network{{Name: "lan", Subnets: []engine.Subnet{{Prefix: mustPrefix("10.0.0.0/24")}}}}
	cfg := func() composefile.ServiceConfig {
		return composefile.ServiceConfig{Image: "nginx", Networks: []composefile.NetworkAttachment{{Name: "lan", IPv4Address: "10.0.0.5"}}}
	}
	h.create(t, "a", cfg())
	_, err := h.svc.Create(context.Background(), stacks.CreateRequest{Name: "b", Config: cfg()})
	fs := fieldsOf(t, err)
	if len(fs) != 1 || fs[0].Field != "networks.lan.ipv4_address" || !strings.Contains(fs[0].Message, "stack a service a") {
		t.Errorf("fields = %+v", fs)
	}
}

func TestMissingFileGivesErrStackUnavailableForRawAndEdits(t *testing.T) {
	h := newHarness(t)
	hash := setupWeb(t, h)
	os.Remove(h.path("web"))
	ctx := context.Background()
	if _, _, err := h.svc.Raw(ctx, "web"); !errors.Is(err, stacks.ErrStackUnavailable) {
		t.Errorf("Raw: %v", err)
	}
	if _, err := h.svc.UpdateService(ctx, "web", "web", webCfg(), hash); !errors.Is(err, stacks.ErrStackUnavailable) {
		t.Errorf("UpdateService: %v", err)
	}
	if _, err := h.svc.SaveRaw(ctx, "web", []byte("services: {}\n"), hash); !errors.Is(err, stacks.ErrStackUnavailable) {
		t.Errorf("SaveRaw: %v", err)
	}
}

func TestConcurrentUpdatesWithSameHashExactlyOneWins(t *testing.T) {
	h := newHarness(t)
	hash := setupWeb(t, h)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = h.svc.UpdateService(context.Background(), "web", "web",
				composefile.ServiceConfig{Image: "nginx:" + string(rune('2'+i)), Ports: []string{"8080:80"}}, hash)
		}()
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, composefile.ErrConflict):
			conflict++
		default:
			t.Errorf("unexpected err %v", err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Errorf("ok=%d conflict=%d", ok, conflict)
	}
}

// ---- Fix round 2 ----

func TestImportRefusesProjectOwnedByRenamedStack(t *testing.T) {
	h := newHarness(t)
	h.compose.projectFn = projectFromFile
	h.create(t, "alpha", webCfg())
	_, hash, _ := h.svc.Raw(context.Background(), "alpha")
	if _, err := h.svc.SaveRaw(context.Background(), "alpha", rawWithName("gamma", "alpha"), hash); err != nil {
		t.Fatal(err)
	}
	ext := filepath.Join(t.TempDir(), "compose.yaml")
	os.WriteFile(ext, rawWithName("gamma", "app"), 0o644)
	_, err := h.svc.Import(context.Background(), ext)
	if fs := fieldsOf(t, err); fs[0].Field != "name" || !strings.Contains(fs[0].Message, "stack alpha") {
		t.Errorf("fields = %+v", fs)
	}
	if rows, _ := h.reg.List(context.Background()); len(rows) != 1 {
		t.Errorf("rows = %+v", rows)
	}
}

func TestConcurrentSaveRawToSameProjectLetsExactlyOneWin(t *testing.T) {
	h := newHarness(t)
	h.compose.projectFn = projectFromFile
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		x, y := fmt.Sprintf("x%d", i), fmt.Sprintf("y%d", i)
		h.create(t, x, webCfg())
		h.create(t, y, webCfg())
		target := fmt.Sprintf("p%d", i)
		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, 2)
		for n, name := range []string{x, y} {
			_, hash, _ := h.svc.Raw(ctx, name)
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[n] = h.svc.SaveRaw(ctx, name, rawWithName(target, name), hash)
			}()
		}
		close(start)
		wg.Wait()
		ok := 0
		for _, err := range errs {
			if err == nil {
				ok++
			} else if fs := fieldsOf(t, err); fs[0].Field != "name" {
				t.Fatalf("iteration %d: %+v", i, fs)
			}
		}
		if ok != 1 {
			t.Fatalf("iteration %d: %d saves succeeded, want 1", i, ok)
		}
	}
}

func TestCreateVersusEditClaimingSameProjectGivesOneWinner(t *testing.T) {
	h := newHarness(t)
	h.compose.projectFn = projectFromFile
	ctx := context.Background()
	h.create(t, "x", webCfg())
	_, hash, _ := h.svc.Raw(ctx, "x")
	var editErr error
	h.compose.projectFn = func(file string) string {
		if filepath.Base(filepath.Dir(file)) == "web" { // Create's ProjectName: an edit lands first
			_, editErr = h.svc.SaveRaw(ctx, "x", rawWithName("web", "x"), hash)
		}
		return projectFromFile(file)
	}
	_, createErr := h.svc.Create(ctx, stacks.CreateRequest{Name: "web", Config: webCfg()})
	if (editErr == nil) == (createErr == nil) {
		t.Fatalf("edit err %v, create err %v: want exactly one success", editErr, createErr)
	}
	owners := 0
	rows, _ := h.reg.List(ctx)
	for _, r := range rows {
		if r.Project == "web" {
			owners++
		}
	}
	if owners != 1 {
		t.Errorf("%d stacks own project web", owners)
	}
}

func TestRegistryProjectConflictIsMappedToValidationError(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// A writer outside the service sneaks in after the ownership check.
	h.eng.onContainers = func() {
		h.eng.onContainers = nil
		h.reg.Add(ctx, stacks.Entry{Name: "sneaky", ComposePath: "/s/compose.yaml", Origin: stacks.OriginExternal, Project: "web"})
	}
	_, err := h.svc.Create(ctx, stacks.CreateRequest{Name: "web", Config: webCfg()})
	if fs := fieldsOf(t, err); fs[0].Field != "name" {
		t.Errorf("fields = %+v", fs)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "web")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("folder left: %v", err)
	}
}

func TestCreateWithEngineDownIsPartialEvenWithoutPorts(t *testing.T) {
	h := newHarness(t)
	h.eng.err = engine.ErrUnavailable
	sum, err := h.svc.Create(context.Background(), stacks.CreateRequest{Name: "web", Config: composefile.ServiceConfig{Image: "nginx"}})
	if err != nil || !sum.ChecksPartial {
		t.Errorf("sum = %+v err = %v", sum, err)
	}
}

func TestCreateDoesNotTouchStacksDirWhenComposeMissing(t *testing.T) {
	h := newHarness(t)
	h.compose.versionErr = composecli.ErrComposeUnavailable
	h.svc.Create(context.Background(), stacks.CreateRequest{Name: "web", Config: webCfg()})
	if _, err := os.Stat(h.dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stacks dir created: %v", err)
	}
}

func TestEditRenamingToUnregisteredHostProjectIsRefusedOrPartial(t *testing.T) {
	h := newHarness(t)
	h.compose.projectFn = projectFromFile
	h.create(t, "alpha", webCfg())
	ctx := context.Background()
	_, hash, _ := h.svc.Raw(ctx, "alpha")

	h.eng.containers = []engine.Container{publishing("legacy", 8080)}
	_, err := h.svc.SaveRaw(ctx, "alpha", rawWithName("legacy", "alpha"), hash)
	if fs := fieldsOf(t, err); fs[0].Field != "name" || !strings.Contains(fs[0].Message, "import it instead") {
		t.Errorf("fields = %+v", fs)
	}
	h.eng.containers, h.eng.err = nil, engine.ErrUnavailable
	res, err := h.svc.SaveRaw(ctx, "alpha", rawWithName("legacy", "alpha"), hash)
	if err != nil || !res.ChecksPartial {
		t.Errorf("engine down: %+v %v", res, err)
	}
}

func TestRemoveMissingFileWithEmptyProjectIsRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.reg.Add(ctx, stacks.Entry{Name: "p", ComposePath: filepath.Join(h.dir, "p", "compose.yaml"), Origin: stacks.OriginExternal})
	if _, err := h.svc.Remove(ctx, "p", false); !errors.Is(err, stacks.ErrStackUnavailable) {
		t.Errorf("err = %v", err)
	}
	if _, err := h.reg.Get(ctx, "p"); err != nil {
		t.Errorf("row dropped: %v", err)
	}
}

// ---- Final fix wave ----

func writeOverride(t *testing.T, composePath string) string {
	t.Helper()
	name := "compose.override.yaml"
	if err := os.WriteFile(filepath.Join(filepath.Dir(composePath), name), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestImportRefusesFileWithOverrideSibling(t *testing.T) {
	h := newHarness(t)
	ext := filepath.Join(t.TempDir(), "docker-compose.yml")
	os.WriteFile(ext, []byte(externalYAML), 0o644)
	name := writeOverride(t, ext)
	_, err := h.svc.Import(context.Background(), ext)
	fs := fieldsOf(t, err)
	if len(fs) != 1 || fs[0].Field != "path" || !strings.Contains(fs[0].Message, name) || !strings.Contains(fs[0].Message, "override") {
		t.Errorf("fields = %+v", fs)
	}
	if rows, _ := h.reg.List(context.Background()); len(rows) != 0 {
		t.Errorf("rows = %v", rows)
	}
}

func TestOverrideAppearingLaterRefusesLifecycleAndEdits(t *testing.T) {
	h := newHarness(t)
	hash := setupWeb(t, h)
	ctx := context.Background()
	name := writeOverride(t, h.path("web"))
	before, _ := os.ReadFile(h.path("web"))

	check := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, stacks.ErrStackUnavailable) || !strings.Contains(err.Error(), name) {
			t.Errorf("%s err = %v", what, err)
		}
	}
	_, err := h.svc.Act(ctx, "web", "up")
	check("act", err)
	_, err = h.svc.Remove(ctx, "web", false)
	check("remove", err)
	_, err = h.svc.SaveRaw(ctx, "web", []byte("services:\n  web:\n    image: a\n"), hash)
	check("saveraw", err)
	_, err = h.svc.UpdateService(ctx, "web", "web", composefile.ServiceConfig{Image: "nginx:2"}, hash)
	check("update", err)
	if len(h.compose.runs) != 0 {
		t.Errorf("compose ran: %v", h.compose.runs)
	}
	if after, _ := os.ReadFile(h.path("web")); string(after) != string(before) {
		t.Error("file modified")
	}
}

func TestSyncSkipsFolderWithOverrideAndWarns(t *testing.T) {
	h := newHarness(t)
	os.MkdirAll(filepath.Join(h.dir, "alpha"), 0o755)
	os.WriteFile(h.path("alpha"), []byte(externalYAML), 0o644)
	name := writeOverride(t, h.path("alpha"))
	err := h.svc.Sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), name) {
		t.Errorf("warning = %v", err)
	}
	if rows, _ := h.reg.List(context.Background()); len(rows) != 0 {
		t.Errorf("rows = %+v", rows)
	}
}

const localNetworkYAML = `services:
  web:
    image: nginx
    networks:
      lan:
        ipv4_address: 10.9.0.5
networks:
  lan:
    ipam:
      config:
        - subnet: 10.9.0.0/24
`

func TestStaticIPOnProjectLocalNetworkIsAcceptedBeforeFirstUp(t *testing.T) {
	ctx := context.Background()
	t.Run("SaveRaw", func(t *testing.T) {
		h := newHarness(t)
		hash := setupWeb(t, h)
		if _, err := h.svc.SaveRaw(ctx, "web", []byte(localNetworkYAML), hash); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("UpdateService", func(t *testing.T) {
		h := newHarness(t)
		hash := setupWeb(t, h)
		res, err := h.svc.SaveRaw(ctx, "web", []byte(strings.Replace(localNetworkYAML, "ipv4_address: 10.9.0.5", "ipv4_address: 10.9.0.4", 1)), hash)
		if err != nil {
			t.Fatal(err)
		}
		cfg := composefile.ServiceConfig{Image: "nginx", Networks: []composefile.NetworkAttachment{{Name: "lan", IPv4Address: "10.9.0.5"}}}
		if _, err := h.svc.UpdateService(ctx, "web", "web", cfg, res.Hash); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("external stays strict", func(t *testing.T) {
		h := newHarness(t)
		hash := setupWeb(t, h)
		ext := strings.Replace(localNetworkYAML, "    ipam:\n      config:\n        - subnet: 10.9.0.0/24\n", "    external: true\n", 1)
		_, err := h.svc.SaveRaw(ctx, "web", []byte(ext), hash)
		if fs := fieldsOf(t, err); len(fs) != 1 || !strings.Contains(fs[0].Message, "not found") {
			t.Errorf("fields = %+v", fs)
		}
	})
}

func TestFailedSaveRevertsProjectEvenWhenRequestContextIsCancelled(t *testing.T) {
	h := newHarness(t)
	setupWeb(t, h)
	bg := context.Background()
	old, _ := h.reg.Get(bg, "web")
	if err := h.reg.SetProject(bg, "web", "claimed"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(bg)
	cancel()
	saveErr := errors.New("save failed")
	if err := h.svc.RevertProject(ctx, old, saveErr); !errors.Is(err, saveErr) {
		t.Fatalf("err = %v", err)
	}
	if e, _ := h.reg.Get(bg, "web"); e.Project != old.Project {
		t.Errorf("project = %q, want %q", e.Project, old.Project)
	}
}

func TestFailedRevertIsJoinedWithSaveError(t *testing.T) {
	h := newHarness(t)
	saveErr := errors.New("save failed")
	err := h.svc.RevertProject(context.Background(), stacks.Entry{Name: "ghost", Project: "p"}, saveErr)
	if !errors.Is(err, saveErr) || !errors.Is(err, stacks.ErrStackNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestActRederivesProjectAfterOutOfBandRename(t *testing.T) {
	h := newHarness(t)
	h.compose.projectFn = projectFromFile
	h.create(t, "web", webCfg())
	h.create(t, "alpha", webCfg())
	ctx := context.Background()

	if err := os.WriteFile(h.path("web"), rawWithName("renamed", "web"), 0o644); err != nil {
		t.Fatal(err)
	}
	op, err := h.svc.Act(ctx, "web", "up")
	if err != nil {
		t.Fatal(err)
	}
	wait(t, h.ops, op.ID)
	if e, _ := h.reg.Get(ctx, "web"); e.Project != "renamed" {
		t.Errorf("project = %q, want renamed", e.Project)
	}

	if err := os.WriteFile(h.path("web"), rawWithName("alpha", "web"), 0o644); err != nil {
		t.Fatal(err)
	}
	runs := len(h.compose.runs)
	_, err = h.svc.Act(ctx, "web", "up")
	if fs := fieldsOf(t, err); fs[0].Field != "name" {
		t.Errorf("fields = %+v", fs)
	}
	_, err = h.svc.Remove(ctx, "web", false)
	if fs := fieldsOf(t, err); fs[0].Field != "name" {
		t.Errorf("remove fields = %+v", fs)
	}
	if len(h.compose.runs) != runs {
		t.Error("compose ran despite a project collision")
	}
	if e, _ := h.reg.Get(ctx, "web"); e.Project != "renamed" {
		t.Errorf("project changed to %q", e.Project)
	}
}
