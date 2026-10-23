package images

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/registry"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

const testPassword = "hunter2-secret"

type fakeRegistries struct {
	regs         map[string]registry.Registry
	unconfigured map[string]bool
	credHost     map[string]string // host Credentials returns instead of the stored one
	getErr       map[string]error
	credErr      map[string]error
}

func (f *fakeRegistries) Get(_ context.Context, id string) (registry.Registry, error) {
	if err := f.getErr[id]; err != nil {
		return registry.Registry{}, err
	}
	r, ok := f.regs[id]
	if !ok {
		return registry.Registry{}, registry.ErrNotFound
	}
	return r, nil
}

func (f *fakeRegistries) Credentials(_ context.Context, id string) (engine.RegistryAuth, string, error) {
	r, ok := f.regs[id]
	if !ok {
		return engine.RegistryAuth{}, "", registry.ErrNotFound
	}
	if err := f.credErr[id]; err != nil {
		return engine.RegistryAuth{}, "", err
	}
	if f.unconfigured[id] {
		return engine.RegistryAuth{}, "", registry.ErrUnconfigured
	}
	if h, ok := f.credHost[id]; ok {
		return engine.RegistryAuth{Username: "u", Password: testPassword, ServerAddress: h}, h, nil
	}
	return engine.RegistryAuth{Username: "u", Password: testPassword, ServerAddress: "docker.io"}, r.Host, nil
}

// pushHarness is a harness with registries "hub" and "bare" (unconfigured).
func newPushHarness(t *testing.T) (*harness, *fakeRegistries) {
	t.Helper()
	h := newHarness(t)
	fr := &fakeRegistries{
		regs: map[string]registry.Registry{
			"hub":  {ID: "hub", Name: "Hub", Kind: registry.KindDockerHub, Host: "docker.io", Hub: registry.Hub{Username: "u"}},
			"ecr":  {ID: "ecr", Name: "Ecr", Kind: registry.KindECR},
			"bare": {ID: "bare", Name: "Bare", Kind: registry.KindDockerHub, Host: "docker.io"},
		},
		unconfigured: map[string]bool{"bare": true},
		credHost:     map[string]string{"ecr": "123.dkr.ecr.eu-west-1.amazonaws.com"},
	}
	h.svc.registries = fr
	for id, r := range fr.regs {
		if _, err := h.svc.store.db.Exec(`INSERT INTO registries (id, name, kind, created_at, updated_at) VALUES (?,?,?,0,0)`, id, r.Name, string(r.Kind)); err != nil {
			t.Fatal(err)
		}
	}
	return h, fr
}

func (h *harness) addTarget(t *testing.T, slug, regID, repo string, auto bool) PushTarget {
	t.Helper()
	tg, err := h.svc.AddPushTarget(context.Background(), "alice", slug, PushTargetRequest{RegistryID: regID, Repository: repo, AutoPush: auto})
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

func (h *harness) pushOf(t *testing.T, v Version, targetID string) (Push, bool) {
	t.Helper()
	m, err := h.svc.store.Pushes(context.Background(), []string{v.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range m[v.ID] {
		if p.TargetID == targetID {
			return p, true
		}
	}
	return Push{}, false
}

func (h *harness) engineCalls() []string {
	h.eng.mu.Lock()
	defer h.eng.mu.Unlock()
	return append([]string(nil), h.eng.calls...)
}

func TestPushTagsPushesUntags(t *testing.T) {
	h, _ := newPushHarness(t)
	h.create(t, "app")
	v := h.build(t, "app", "1", nil)
	tg := h.addTarget(t, "app", "hub", "u/app", false)
	h.eng.pushDigest = "sha256:pushed"
	h.eng.mu.Lock()
	h.eng.calls = nil
	h.eng.mu.Unlock()

	op, err := h.svc.Push(context.Background(), "alice", "app", "1", tg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.waitOp(t, op.ID); got.State != stacks.OpSucceeded {
		t.Fatalf("op = %+v", got)
	}
	ref := "stackstead.invalid/" + testOwner + "/app:1"
	want := []string{
		"tag " + ref + " docker.io/u/app 1",
		"push docker.io/u/app:1",
		"remove docker.io/u/app:1",
	}
	if got := h.engineCalls(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	p, ok := h.pushOf(t, v, tg.ID)
	if !ok || p.Status != PushPushed || p.Digest != "sha256:pushed" {
		t.Fatalf("push = %+v", p)
	}
	e, ok := h.log.last("image.push")
	if !ok || e.Result != "succeeded" || e.Detail != "Hub u/app:1" || e.Scope != "app" {
		t.Fatalf("log = %+v", e)
	}
}

func TestPushOutputRedacted(t *testing.T) {
	h, _ := newPushHarness(t)
	h.create(t, "app")
	v := h.build(t, "app", "1", nil)
	tg := h.addTarget(t, "app", "hub", "u/app", false)
	h.eng.pushErr = func(a engine.RegistryAuth) error {
		return fmt.Errorf("push failed: unauthorized for password %s", a.Password)
	}
	op, err := h.svc.Push(context.Background(), "alice", "app", "1", tg.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := h.waitOp(t, op.ID)
	if got.State != stacks.OpFailed {
		t.Fatalf("op = %+v", got)
	}
	lines, _ := h.ops.Lines(op.ID)
	all := got.Error + "\n" + strings.Join(lines, "\n")
	p, _ := h.pushOf(t, v, tg.ID)
	e, _ := h.log.last("image.push")
	all += "\n" + p.Error + "\n" + e.Detail
	if strings.Contains(all, testPassword) {
		t.Fatalf("password leaked: %s", all)
	}
	if p.Status != PushFailed || !strings.Contains(p.Error, "unauthorized") || e.Result != "failed" {
		t.Fatalf("push = %+v log = %+v", p, e)
	}
	// untagged even though the push failed
	if c := h.engineCalls(); c[len(c)-1] != "remove docker.io/u/app:1" {
		t.Fatalf("calls = %q", c)
	}
}

func TestPushUnconfiguredRegistry409(t *testing.T) {
	h, _ := newPushHarness(t)
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	tg := h.addTarget(t, "app", "bare", "u/app", false)
	if _, err := h.svc.Push(context.Background(), "alice", "app", "1", tg.ID); !errors.Is(err, ErrRegistryUnconfigured) {
		t.Fatalf("err = %v", err)
	}
	h.svc.registries = nil
	if _, err := h.svc.Push(context.Background(), "alice", "app", "1", tg.ID); !errors.Is(err, ErrRegistryUnconfigured) {
		t.Fatalf("nil registries err = %v", err)
	}
}

func TestPushFailedVersionRefused(t *testing.T) {
	h, _ := newPushHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	h.eng.buildErr = &engine.BuildError{Message: "boom"}
	h.build(t, "app", "1", nil)
	tg := h.addTarget(t, "app", "hub", "u/app", false)
	if _, err := h.svc.Push(ctx, "alice", "app", "1", tg.ID); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("failed version err = %v", err)
	}
	h.eng.buildErr = nil
	h.eng.block = make(chan struct{})
	h.eng.started = make(chan struct{})
	_, op, err := h.svc.Build(ctx, "alice", "app", BuildRequest{Tag: "2"})
	if err != nil {
		t.Fatal(err)
	}
	<-h.eng.started
	var ve *ValidationError
	if _, err := h.svc.Push(ctx, "alice", "app", "2", tg.ID); !errors.As(err, &ve) {
		t.Fatalf("building version err = %v", err)
	}
	close(h.eng.block)
	h.waitOp(t, op.ID)
	if _, err := h.svc.Push(ctx, "alice", "app", "2", "nope"); !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("unknown target err = %v", err)
	}
}

func TestPushBusyWhileBuilding(t *testing.T) {
	h, _ := newPushHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	tg := h.addTarget(t, "app", "hub", "u/app", false)
	h.eng.block = make(chan struct{})
	h.eng.started = make(chan struct{})
	_, op, err := h.svc.Build(ctx, "alice", "app", BuildRequest{Tag: "2"})
	if err != nil {
		t.Fatal(err)
	}
	<-h.eng.started
	if _, err := h.svc.Push(ctx, "alice", "app", "1", tg.ID); !errors.Is(err, ErrImageBusy) {
		t.Fatalf("err = %v", err)
	}
	close(h.eng.block)
	h.waitOp(t, op.ID)
}

func TestBuildBusyWhilePushing(t *testing.T) {
	h, _ := newPushHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	h.build(t, "app", "1", nil)
	tg := h.addTarget(t, "app", "hub", "u/app", false)
	release := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	defer free()
	started := make(chan struct{})
	h.eng.pushHook = func() {
		close(started)
		<-release
	}
	op, err := h.svc.Push(ctx, "alice", "app", "1", tg.ID)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, _, err := h.svc.Build(ctx, "alice", "app", BuildRequest{Tag: "2"}); !errors.Is(err, ErrImageBusy) {
		t.Fatalf("err = %v, want ErrImageBusy", err)
	}
	if strings.Contains(ErrImageBusy.Error(), "build is already") {
		t.Errorf("message names a build: %q", ErrImageBusy)
	}
	free()
	h.waitOp(t, op.ID)
}

func TestAutoPushUnconfiguredRegistryRecordedAsFailed(t *testing.T) {
	h, _ := newPushHarness(t)
	h.create(t, "app")
	tg := h.addTarget(t, "app", "bare", "u/app", true)
	v, bop, err := h.svc.Build(context.Background(), "alice", "app", BuildRequest{Tag: "1"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitOp(t, bop.ID)
	lines, err := h.ops.Lines(bop.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := h.pushOf(t, v, tg.ID)
	if !ok || p.Status != PushFailed || p.Error != "registry not configured" {
		t.Fatalf("push = %+v, want failed 'registry not configured'", p)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "skipping push to Bare: not configured") {
		t.Errorf("build output lacks skip line: %q", lines)
	}
}

func TestAutoPushAfterBuild(t *testing.T) {
	h, _ := newPushHarness(t)
	h.create(t, "app")
	auto := h.addTarget(t, "app", "hub", "u/auto", true)
	manual := h.addTarget(t, "app", "hub", "u/manual", false)
	skipped := h.addTarget(t, "app", "bare", "u/bare", true)
	h.eng.pushDigest = "sha256:auto"
	v := h.build(t, "app", "1", nil)
	if v.Status != VersionSucceeded {
		t.Fatalf("version = %+v", v)
	}
	if p, ok := h.pushOf(t, v, auto.ID); !ok || p.Status != PushPushed || p.Digest != "sha256:auto" {
		t.Fatalf("auto push = %+v", p)
	}
	if _, ok := h.pushOf(t, v, manual.ID); ok {
		t.Error("manual target was pushed")
	}
	if p, ok := h.pushOf(t, v, skipped.ID); !ok || p.Status != PushFailed {
		t.Errorf("unconfigured registry push = %+v, want failed", p)
	}
	pushes := 0
	for _, c := range h.engineCalls() {
		if strings.HasPrefix(c, "push ") {
			pushes++
			if c != "push docker.io/u/auto:1" {
				t.Errorf("unexpected push %q", c)
			}
		}
	}
	if pushes != 1 {
		t.Fatalf("pushes = %d", pushes)
	}
	if e, ok := h.log.last("image.push"); !ok || e.OperationID == "" || e.OperationID != v.OperationID {
		t.Fatalf("push log = %+v, build op %q", e, v.OperationID)
	}
	log, _ := os.ReadFile(filepath.Join(h.dir, "app", "logs", v.ID+".log"))
	if !strings.Contains(string(log), "pushing to Hub") || strings.Contains(string(log), testPassword) {
		t.Fatalf("build log = %s", log)
	}
}

func TestAutoPushFailureKeepsBuildSucceeded(t *testing.T) {
	h, _ := newPushHarness(t)
	h.create(t, "app")
	tg := h.addTarget(t, "app", "hub", "u/auto", true)
	h.eng.pushErr = func(a engine.RegistryAuth) error { return errors.New("denied " + a.Password) }
	v := h.build(t, "app", "1", nil)
	if v.Status != VersionSucceeded {
		t.Fatalf("version = %+v", v)
	}
	p, ok := h.pushOf(t, v, tg.ID)
	if !ok || p.Status != PushFailed || strings.Contains(p.Error, testPassword) {
		t.Fatalf("push = %+v", p)
	}
	if e, _ := h.log.last("image.build"); e.Result != "succeeded" {
		t.Fatalf("build log = %+v", e)
	}
}

func TestGitHubImageNeverAutoPushes(t *testing.T) {
	h, _ := newPushHarness(t)
	img := h.create(t, "app")
	h.addTarget(t, "app", "hub", "u/auto", true)
	img.Source = SourceGitHub
	h.eng.mu.Lock()
	h.eng.calls = nil
	h.eng.mu.Unlock()
	v := Version{ID: "v", ImageID: img.ID, Tag: "1"}
	h.svc.autoPush(context.Background(), "alice", img, v, "", func(string) {})
	if c := h.engineCalls(); len(c) != 0 {
		t.Fatalf("engine calls = %q", c)
	}
}

func TestPushTargetDeletedMidPush(t *testing.T) {
	h, _ := newPushHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	v := h.build(t, "app", "1", nil)
	tg := h.addTarget(t, "app", "hub", "u/app", false)
	h.eng.pushHook = func() {
		if err := h.svc.store.DeletePushTarget(ctx, tg.ID); err != nil {
			t.Error(err)
		}
	}
	op, err := h.svc.Push(ctx, "alice", "app", "1", tg.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.waitOp(t, op.ID)
	if _, ok := h.pushOf(t, v, tg.ID); ok {
		t.Error("push row left for a deleted target")
	}
}

func TestPushTargetValidation(t *testing.T) {
	h, _ := newPushHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	var ve *ValidationError
	for _, repo := range []string{"", "Upper/case", "a//b", "-a", "a-", strings.Repeat("a", 256)} {
		if _, err := h.svc.AddPushTarget(ctx, "alice", "app", PushTargetRequest{RegistryID: "hub", Repository: repo}); !errors.As(err, &ve) {
			t.Errorf("repo %q err = %v", repo, err)
		}
	}
	for _, repo := range []string{"app", "u/app", "a.b_c-d/e", strings.Repeat("a", 255)} {
		if _, err := h.svc.AddPushTarget(ctx, "alice", "app", PushTargetRequest{RegistryID: "hub", Repository: repo}); err != nil {
			t.Errorf("repo %q err = %v", repo, err)
		}
	}
	if _, err := h.svc.AddPushTarget(ctx, "alice", "app", PushTargetRequest{RegistryID: "nope", Repository: "x"}); !errors.As(err, &ve) {
		t.Errorf("unknown registry err = %v", err)
	}
	if _, err := h.svc.AddPushTarget(ctx, "alice", "app", PushTargetRequest{RegistryID: "hub", Repository: "app"}); !errors.As(err, &ve) {
		t.Errorf("duplicate err = %v", err)
	}
	tg := h.addTarget(t, "app", "hub", "u/other", false)
	up, err := h.svc.UpdatePushTarget(ctx, "alice", "app", tg.ID, PushTargetRequest{RegistryID: "hub", Repository: "u/renamed", AutoPush: true})
	if err != nil || up.Repository != "u/renamed" || !up.AutoPush {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if err := h.svc.DeletePushTarget(ctx, "alice", "app", tg.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.DeletePushTarget(ctx, "alice", "app", tg.ID); !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

func TestStartMarksPushingFailed(t *testing.T) {
	h, _ := newPushHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	v := h.build(t, "app", "1", nil)
	tg := h.addTarget(t, "app", "hub", "u/app", false)
	if err := h.svc.store.SetPush(ctx, Push{VersionID: v.ID, TargetID: tg.ID, Status: PushPushing}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	p, ok := h.pushOf(t, v, tg.ID)
	if !ok || p.Status != PushFailed || p.Error != "interrupted by server restart" {
		t.Fatalf("push = %+v", p)
	}
}

func TestAutoPushStopsWhenVersionDeleted(t *testing.T) {
	h, _ := newPushHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	h.addTarget(t, "app", "hub", "u/one", true)
	h.addTarget(t, "app", "hub", "u/two", true)
	h.eng.pushHook = func() {
		vs, _ := h.svc.store.Versions(ctx, mustID(t, h, "app"))
		for _, v := range vs {
			h.svc.store.DeleteVersion(ctx, v.ID)
		}
	}
	_, op, err := h.svc.Build(ctx, "alice", "app", BuildRequest{Tag: "1"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitOp(t, op.ID)
	pushes := 0
	for _, c := range h.engineCalls() {
		if strings.HasPrefix(c, "push ") {
			pushes++
		}
	}
	if pushes != 1 {
		t.Fatalf("pushes = %d, want 1", pushes)
	}
	h.log.mu.Lock()
	defer h.log.mu.Unlock()
	for _, e := range h.log.entries {
		if e.Kind == "image.push" && e.Result == "failed" {
			t.Errorf("failure entry written: %+v", e)
		}
	}
}

func TestAutoPushCredentialErrorRecorded(t *testing.T) {
	h, fr := newPushHarness(t)
	h.create(t, "app")
	tg := h.addTarget(t, "app", "hub", "u/app", true)
	fr.credErr = map[string]error{"hub": errors.New("token service down")}
	v := h.build(t, "app", "1", nil)
	p, ok := h.pushOf(t, v, tg.ID)
	if !ok || p.Status != PushFailed || !strings.Contains(p.Error, "token service down") {
		t.Fatalf("push = %+v", p)
	}
	if e, ok := h.log.last("image.push"); !ok || e.Result != "failed" || e.Detail != "Hub u/app:1: "+p.Error {
		t.Fatalf("log = %+v", e)
	}
}

func TestAutoPushLogsRegistryGetError(t *testing.T) {
	h, fr := newPushHarness(t)
	var buf strings.Builder
	h.svc.log = slog.New(slog.NewTextHandler(&buf, nil))
	h.create(t, "app")
	tg := h.addTarget(t, "app", "hub", "u/app", true)
	fr.getErr = map[string]error{"hub": errors.New("db locked")}
	h.build(t, "app", "1", nil)
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, tg.ID) || !strings.Contains(out, "app") {
		t.Fatalf("log = %q", out)
	}
	// not-found stays silent
	buf.Reset()
	fr.getErr = nil
	delete(fr.regs, "hub")
	h.build(t, "app", "2", nil)
	if strings.Contains(buf.String(), "WARN") {
		t.Fatalf("not-found logged: %q", buf.String())
	}
}

func TestUpdatePushTargetClearsPushes(t *testing.T) {
	h, _ := newPushHarness(t)
	ctx := context.Background()
	h.create(t, "app")
	v := h.build(t, "app", "1", nil)
	tg := h.addTarget(t, "app", "hub", "u/app", false)
	if err := h.svc.store.SetPush(ctx, Push{VersionID: v.ID, TargetID: tg.ID, Status: PushPushed}); err != nil {
		t.Fatal(err)
	}
	// flag-only change keeps the history
	if _, err := h.svc.UpdatePushTarget(ctx, "alice", "app", tg.ID, PushTargetRequest{RegistryID: "hub", Repository: "u/app", AutoPush: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.pushOf(t, v, tg.ID); !ok {
		t.Fatal("push cleared by flag-only change")
	}
	if _, err := h.svc.UpdatePushTarget(ctx, "alice", "app", tg.ID, PushTargetRequest{RegistryID: "hub", Repository: "u/new"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.pushOf(t, v, tg.ID); ok {
		t.Fatal("push rows kept after repository change")
	}
}

func TestPushUsesHostFromCredentials(t *testing.T) {
	h, _ := newPushHarness(t)
	h.create(t, "app")
	v := h.build(t, "app", "1", nil)
	tg := h.addTarget(t, "app", "ecr", "team/app", false)
	op, err := h.svc.Push(context.Background(), "alice", "app", "1", tg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.waitOp(t, op.ID); got.State != stacks.OpSucceeded {
		t.Fatalf("op = %+v", got)
	}
	want := "push 123.dkr.ecr.eu-west-1.amazonaws.com/team/app:1"
	found := false
	for _, c := range h.engineCalls() {
		found = found || c == want
	}
	if !found {
		t.Fatalf("calls = %q, want %q", h.engineCalls(), want)
	}
	if p, _ := h.pushOf(t, v, tg.ID); p.Status != PushPushed {
		t.Fatalf("push = %+v", p)
	}
}

func TestSplitRefRejectsEmpty(t *testing.T) {
	for _, ref := range []string{"", "/repo:1", "host/repo", "host/repo:"} {
		if _, _, err := splitRef(ref); err == nil {
			t.Errorf("splitRef(%q) = nil error", ref)
		}
	}
	if repo, tag, err := splitRef("h:5000/o/img:2"); err != nil || repo != "h:5000/o/img" || tag != "2" {
		t.Errorf("splitRef ok = %q %q %v", repo, tag, err)
	}
}
