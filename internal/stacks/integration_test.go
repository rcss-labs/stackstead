//go:build integration

package stacks_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/composecli"
	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// These tests talk to a real Docker engine:
//   go test -tags integration -race ./internal/stacks/
// Each skips when no engine answers. Everything they create carries a random
// "itest<hex>" prefix and is removed in cleanup, even on failure.

type itEnv struct {
	t        *testing.T
	sock     string
	prefix   string
	root     string // resolved temp dir: StacksDir lives in it and it is the allowed root
	svc      *stacks.Service
	ops      *stacks.Ops
	reg      *stacks.Registry
	eng      *engine.Client
	teardown []func()
}

// itSocket returns the first Docker socket that answers a ping.
func itSocket() string {
	var candidates []string
	if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		candidates = append(candidates, strings.TrimPrefix(h, "unix://"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".docker", "run", "docker.sock"))
	}
	candidates = append(candidates, "/var/run/docker.sock")
	for _, c := range candidates {
		conn, err := net.DialTimeout("unix", c, time.Second)
		if err != nil {
			continue
		}
		conn.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err = engine.New(c, 3*time.Second).Ping(ctx)
		cancel()
		if err == nil {
			return c
		}
	}
	return ""
}

func newITEnv(t *testing.T) *itEnv {
	t.Helper()
	sock := itSocket()
	if sock == "" {
		t.Skip("no reachable Docker socket")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not installed")
	}
	if out, err := exec.Command("docker", "compose", "version").CombinedOutput(); err != nil {
		t.Skipf("docker compose plugin unavailable: %v: %s", err, out)
	}
	raw := make([]byte, 3)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &itEnv{t: t, sock: sock, prefix: "itest" + hex.EncodeToString(raw), root: root}
	e.eng = engine.New(sock, 5*time.Second)
	e.ops = stacks.NewOps(5*time.Minute, nil)
	e.reg = newRegistry(t)
	e.svc = stacks.NewService(stacks.Deps{
		Registry: e.reg, Ops: e.ops,
		Compose:   &composecli.Runner{Env: []string{"DOCKER_HOST=unix://" + sock}, Grace: 5 * time.Second},
		Engine:    e.eng,
		StacksDir: filepath.Join(root, "stacks"), AllowedRoots: []string{root},
		Now: time.Now,
	})
	t.Cleanup(func() {
		// Stop our own operations first so nothing recreates containers
		// while they are being removed.
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		e.ops.Shutdown(sctx)
		for i := len(e.teardown) - 1; i >= 0; i-- {
			e.teardown[i]()
		}
	})
	return e
}

func (e *itEnv) docker(args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	cmd.Env = append(os.Environ(), "DOCKER_HOST=unix://"+e.sock)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

// downOnCleanup removes a compose project (containers, volumes, orphans) and
// optionally the image Stackstead names for a built stack.
func (e *itEnv) downOnCleanup(composeFile, image string) {
	e.teardown = append(e.teardown, func() {
		e.docker("compose", "-f", composeFile, "down", "-v", "--remove-orphans", "-t", "1")
		if image != "" {
			e.docker("rmi", "-f", image)
		}
	})
}

func (e *itEnv) writeContext(dockerfile string) string {
	dir := filepath.Join(e.root, "ctx-"+e.prefix)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return dir
}

func (e *itEnv) createBuilt(name, dockerfile string) stacks.Summary {
	e.t.Helper()
	ctxDir := e.writeContext(dockerfile)
	sum, err := e.svc.Create(context.Background(), stacks.CreateRequest{Name: name, Services: []composefile.NamedService{{Name: "", Config: composefile.ServiceConfig{Build: &composefile.Build{Context: ctxDir}}}}, IndexedFields: false})
	if err != nil {
		e.t.Fatalf("Create %s: %v", name, err)
	}
	e.downOnCleanup(sum.Path, "stackstead/"+name+":latest")
	return sum
}

func (e *itEnv) waitOp(id string, limit time.Duration) stacks.Operation {
	e.t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		op, err := e.ops.Get(id)
		if err != nil {
			e.t.Fatal(err)
		}
		if op.State != stacks.OpRunning {
			return op
		}
		time.Sleep(100 * time.Millisecond)
	}
	lines, _ := e.ops.Lines(id)
	e.t.Fatalf("operation %s still running after %s; output:\n%s", id, limit, strings.Join(lines, "\n"))
	return stacks.Operation{}
}

// act runs a lifecycle action and requires it to succeed.
func (e *itEnv) act(stack, action string) {
	e.t.Helper()
	op, err := e.svc.Act(context.Background(), stack, action)
	if err != nil {
		e.t.Fatalf("Act %s %s: %v", stack, action, err)
	}
	e.requireSucceeded(e.waitOp(op.ID, 3*time.Minute))
}

func (e *itEnv) requireSucceeded(op stacks.Operation) {
	e.t.Helper()
	if op.State != stacks.OpSucceeded {
		lines, _ := e.ops.Lines(op.ID)
		e.t.Fatalf("operation %s %s = %s (%s); output:\n%s", op.Stack, op.Action, op.State, op.Error, strings.Join(lines, "\n"))
	}
}

func (e *itEnv) projectContainers(project string) []engine.Container {
	e.t.Helper()
	all, err := e.eng.Containers(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	var out []engine.Container
	for _, c := range all {
		if c.Project() == project {
			out = append(out, c)
		}
	}
	return out
}

func TestCreateBuildUpEditDown(t *testing.T) {
	e := newITEnv(t)
	name := e.prefix + "-web"
	sum := e.createBuilt(name, "FROM busybox\nCMD sleep 3600\n")

	e.act(name, "up")
	cs := e.projectContainers(sum.Project)
	if len(cs) != 1 || cs[0].State != "running" {
		t.Fatalf("after up: containers = %+v", cs)
	}
	firstID := cs[0].ID

	detail, err := e.svc.Get(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != stacks.StatusRunning {
		t.Errorf("status after up = %s, want running", detail.Status)
	}
	cfg := detail.Services[0].Config
	cfg.Limits.Memory = "64M"
	if _, err := e.svc.UpdateService(context.Background(), name, detail.Services[0].Name, cfg, detail.Hash); err != nil {
		t.Fatalf("UpdateService: %v", err)
	}
	e.act(name, "up")
	cs = e.projectContainers(sum.Project)
	if len(cs) != 1 {
		t.Fatalf("after second up: containers = %+v", cs)
	}
	if cs[0].ID == firstID {
		t.Errorf("container was not recreated after the limit changed")
	}
	mem, err := e.docker("inspect", "-f", "{{.HostConfig.Memory}}", cs[0].ID)
	if err != nil || mem != "67108864" {
		t.Errorf("memory limit = %q (err %v), want 67108864", mem, err)
	}

	op, err := e.svc.Remove(context.Background(), name, true)
	if err != nil {
		t.Fatal(err)
	}
	e.requireSucceeded(e.waitOp(op.ID, time.Minute))
	if cs := e.projectContainers(sum.Project); len(cs) != 0 {
		t.Errorf("containers remain after Remove: %+v", cs)
	}
	if _, err := os.Stat(sum.Path); err != nil {
		t.Errorf("compose file must survive Remove: %v", err)
	}
	if _, err := e.reg.Get(context.Background(), name); !errors.Is(err, stacks.ErrStackNotFound) {
		t.Errorf("stack still registered after Remove: %v", err)
	}
}

func TestImportAdoptsRunningProject(t *testing.T) {
	e := newITEnv(t)
	dir := filepath.Join(e.root, e.prefix+"-imp") // compose derives the project name from this folder
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "compose.yaml")
	content := []byte("# hand written\nservices:\n  app:\n    image: busybox\n    command: [\"sleep\", \"3600\"]\n")
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatal(err)
	}
	e.downOnCleanup(file, "")
	if out, err := e.docker("compose", "-f", file, "up", "-d"); err != nil {
		t.Fatalf("docker compose up: %v\n%s", err, out)
	}
	project := e.prefix + "-imp"
	before := e.projectContainers(project)
	if len(before) != 1 {
		t.Fatalf("containers before import = %+v", before)
	}

	sum, err := e.svc.Import(context.Background(), file)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if sum.Project != project || sum.Origin != stacks.OriginExternal {
		t.Errorf("summary = %+v, want project %s external", sum, project)
	}
	detail, err := e.svc.Get(context.Background(), sum.Name)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != stacks.StatusRunning || detail.Running != 1 {
		t.Errorf("status = %s running = %d, want running 1", detail.Status, detail.Running)
	}
	if got := detail.Services[0].Containers; len(got) != 1 || got[0].ID != before[0].ID {
		t.Errorf("adopted containers = %+v, want the pre-existing %s", got, before[0].ID)
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(after, content) {
		t.Errorf("imported file changed (err %v):\n%s", err, after)
	}
	// An up through Stackstead must reuse the same project, not duplicate it.
	e.act(sum.Name, "up")
	if cs := e.projectContainers(project); len(cs) != 1 || cs[0].ID != before[0].ID {
		t.Errorf("up after import changed containers: %+v", cs)
	}
}

// leftoverProcesses lists docker/compose/buildx processes whose command line
// mentions needle.
func leftoverProcesses(needle string) []string {
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil
	}
	var found []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, needle) && (strings.Contains(line, "docker") || strings.Contains(line, "buildx")) {
			found = append(found, strings.TrimSpace(line))
		}
	}
	return found
}

// dockerProcs snapshots the docker compose and buildx processes on the host
// (pid -> command line).
func dockerProcs() map[string]string {
	procs := map[string]string{}
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return procs
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "docker-buildx") || strings.Contains(line, "docker-compose") || strings.Contains(line, "docker compose") {
			pid, cmd, _ := strings.Cut(line, " ")
			procs[pid] = strings.TrimSpace(cmd)
		}
	}
	return procs
}

func TestCancelBuild(t *testing.T) {
	e := newITEnv(t)
	name := e.prefix + "-slow"
	e.createBuilt(name, "FROM busybox\nRUN sleep 120\n")

	preexisting := dockerProcs()
	op, err := e.svc.Act(context.Background(), name, "build")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if len(leftoverProcesses(e.root)) == 0 {
		t.Fatal("no compose process found while building; the process check cannot prove anything")
	}
	if err := e.ops.Cancel(op.ID); err != nil {
		t.Fatal(err)
	}
	final := e.waitOp(op.ID, 30*time.Second)
	if final.State != stacks.OpCancelled {
		lines, _ := e.ops.Lines(op.ID)
		t.Fatalf("state = %s (%s), want cancelled; output:\n%s", final.State, final.Error, strings.Join(lines, "\n"))
	}
	// Everything of ours lives under e.root, so its path identifies the processes.
	// Also catch children that do not mention our path: any compose/buildx
	// process that did not exist before the build must be gone too.
	leftovers := func() []string {
		left := leftoverProcesses(e.root)
		for pid, cmd := range dockerProcs() {
			if _, old := preexisting[pid]; !old {
				left = append(left, pid+" "+cmd)
			}
		}
		return left
	}
	var left []string
	deadline := time.Now().Add(15 * time.Second)
	for {
		if left = leftovers(); len(left) == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(left) > 0 {
		t.Errorf("processes left after cancel:\n%s", strings.Join(left, "\n"))
	}
}

func TestStaticIPConflict(t *testing.T) {
	e := newITEnv(t)
	netName := e.prefix + "-net"
	e.teardown = append(e.teardown, func() { e.docker("network", "rm", netName) })
	var subnet, lastOut string
	created := false
	for attempt := 0; attempt < 8 && !created; attempt++ {
		b := make([]byte, 2)
		if _, err := rand.Read(b); err != nil {
			t.Fatal(err)
		}
		prefix := fmt.Sprintf("10.%d.%d", 100+int(b[0])%100, 1+int(b[1])%250)
		out, err := e.docker("network", "create", "--subnet", prefix+".0/24", netName)
		if err == nil {
			subnet, created = prefix, true
		}
		lastOut = out
	}
	if !created {
		t.Fatalf("could not create a test network after 8 attempts: %s", lastOut)
	}
	holder := e.prefix + "-holder"
	e.teardown = append(e.teardown, func() { e.docker("rm", "-f", holder) }) // registered before run so a half-started container is still removed
	taken := subnet + ".10"
	if out, err := e.docker("run", "-d", "--name", holder, "--network", netName, "--ip", taken, "busybox", "sleep", "3600"); err != nil {
		t.Fatalf("start holder: %v\n%s", err, out)
	}

	name := e.prefix + "-ip"
	_, err := e.svc.Create(context.Background(), stacks.CreateRequest{Name: name, Services: []composefile.NamedService{{Name: "", Config: composefile.ServiceConfig{
		Image:    "busybox",
		Networks: []composefile.NetworkAttachment{{Name: netName, IPv4Address: taken}},
	}}}, IndexedFields: false})
	var ve *stacks.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Create err = %v, want ValidationError", err)
	}
	want := "networks." + netName + ".ipv4_address"
	if len(ve.Fields) != 1 || ve.Fields[0].Field != want {
		t.Errorf("fields = %+v, want one on %s", ve.Fields, want)
	}
	if _, err := os.Stat(filepath.Join(e.root, "stacks", name)); !os.IsNotExist(err) {
		t.Errorf("rejected create left a folder behind (stat err %v)", err)
	}
}
