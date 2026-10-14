package api_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/composecli"
	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/fsguard"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// fakeStacks is a canned api.StacksAPI. A non-nil err is returned by every
// method that can fail, so one table can drive the error mapping.
type fakeStacks struct {
	mu  sync.Mutex
	err error

	roots   []string
	summary stacks.Summary
	partial bool
	op      stacks.Operation

	gotCreate  stacks.CreateRequest
	gotAction  string
	gotVolumes bool
	gotHash    string
	gotContent string
	gotService string
	gotCfg     composefile.ServiceConfig
	gotCancel  string

	replay       []string
	live         chan string
	done         chan stacks.Operation
	cancelOnce   sync.Once
	unsubscribed chan struct{}
}

func newFakeStacks() *fakeStacks {
	return &fakeStacks{
		summary: stacks.Summary{Name: "web", Path: "/srv/web/compose.yaml", Project: "web",
			Origin: stacks.OriginManaged, Status: stacks.StatusRunning, Services: 2, Running: 1},
		op: stacks.Operation{ID: "op1", Stack: "web", Action: "up", State: stacks.OpRunning,
			StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ExitCode: -1},
		unsubscribed: make(chan struct{}),
	}
}

func (f *fakeStacks) sum() stacks.Summary {
	s := f.summary
	s.ChecksPartial = f.partial
	return s
}

func (f *fakeStacks) List(context.Context) ([]stacks.Summary, error) {
	return []stacks.Summary{f.sum()}, f.err
}
func (f *fakeStacks) Create(_ context.Context, req stacks.CreateRequest) (stacks.Summary, error) {
	f.gotCreate = req
	return f.sum(), f.err
}
func (f *fakeStacks) Import(_ context.Context, path string) (stacks.Summary, error) {
	f.gotContent = path
	return f.sum(), f.err
}
func (f *fakeStacks) Get(context.Context, string) (stacks.Detail, error) {
	return stacks.Detail{Summary: f.sum(), Hash: "h1", Services: []stacks.ServiceDetail{{
		Name:       "app",
		Config:     composefile.ServiceConfig{Image: "nginx:1", Networks: []composefile.NetworkAttachment{{Name: "lan", IPv4Address: "10.0.0.5"}}},
		ReadOnly:   []composefile.Field{composefile.FieldVolumes},
		Containers: []stacks.ContainerInfo{{ID: "c1", Name: "web-app-1", Service: "app", State: "running", Status: "Up"}},
	}}}, f.err
}
func (f *fakeStacks) Raw(context.Context, string) ([]byte, string, error) {
	return []byte("services: {}\n"), "h1", f.err
}
func (f *fakeStacks) SaveRaw(_ context.Context, _ string, content []byte, hash string) (stacks.EditResult, error) {
	f.gotContent, f.gotHash = string(content), hash
	return stacks.EditResult{Hash: "h2", ChecksPartial: f.partial}, f.err
}
func (f *fakeStacks) UpdateService(_ context.Context, _, service string, cfg composefile.ServiceConfig, hash string) (stacks.EditResult, error) {
	f.gotService, f.gotCfg, f.gotHash = service, cfg, hash
	return stacks.EditResult{Hash: "h2", ChecksPartial: f.partial}, f.err
}
func (f *fakeStacks) Act(_ context.Context, _, action string) (stacks.Operation, error) {
	f.gotAction = action
	return f.op, f.err
}
func (f *fakeStacks) Remove(_ context.Context, _ string, withVolumes bool) (stacks.Operation, error) {
	f.gotVolumes = withVolumes
	return f.op, f.err
}
func (f *fakeStacks) Networks(context.Context) ([]engine.Network, error) {
	return []engine.Network{{ID: "n1", Name: "lan", Driver: "bridge",
		Subnets: []engine.Subnet{{Prefix: mustPrefix("10.0.0.0/24")}}}}, f.err
}

// fakeOps is the OperationsAPI view of fakeStacks (its Get would clash with
// the stack Get).
type fakeOps struct{ f *fakeStacks }

func (o fakeOps) Recent(string) []stacks.Operation     { return []stacks.Operation{o.f.op} }
func (o fakeOps) Get(string) (stacks.Operation, error) { return o.f.op, o.f.err }
func (o fakeOps) Lines(string) ([]string, error)       { return []string{"a", "b"}, o.f.err }
func (o fakeOps) Cancel(id string) error               { o.f.gotCancel = id; return o.f.err }
func (o fakeOps) Subscribe(string) ([]string, <-chan string, <-chan stacks.Operation, func(), error) {
	if o.f.err != nil {
		return nil, nil, nil, nil, o.f.err
	}
	return o.f.replay, o.f.live, o.f.done, func() { o.f.cancelOnce.Do(func() { close(o.f.unsubscribed) }) }, nil
}
func (f *fakeStacks) status(context.Context) (string, string) {
	return "ok", "v2.30.0"
}

func newStacksEnv(t *testing.T, fake *fakeStacks) (*testEnv, *http.Cookie) {
	t.Helper()
	env := newEnvWith(t, "", func(o *api.Options) {
		o.Stacks, o.Operations, o.FSRoots, o.Status = fake, fakeOps{fake}, fake.roots, fake.status
		o.SSEHeartbeat = 20 * time.Millisecond
	})
	env.createAdmin(t)
	return env, env.login(t)
}

func TestStacksRoutesRequireSession(t *testing.T) {
	env, _ := newStacksEnv(t, newFakeStacks())
	routes := []struct{ method, path string }{
		{"GET", "/api/stacks"}, {"POST", "/api/stacks"}, {"POST", "/api/stacks/import"},
		{"GET", "/api/stacks/web"}, {"GET", "/api/stacks/web/compose"}, {"PUT", "/api/stacks/web/compose"},
		{"PUT", "/api/stacks/web/services/app"}, {"POST", "/api/stacks/web/actions/up"},
		{"DELETE", "/api/stacks/web"}, {"GET", "/api/stacks/web/operations"},
		{"GET", "/api/operations/op1"}, {"GET", "/api/operations/op1/stream"},
		{"POST", "/api/operations/op1/cancel"}, {"GET", "/api/fs"}, {"GET", "/api/networks"}, {"GET", "/api/status"},
	}
	for _, rt := range routes {
		if rec := env.do(t, rt.method, rt.path, "{}"); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without session = %d, want 401", rt.method, rt.path, rec.Code)
		}
	}
}

func TestStacksErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		substr string
	}{
		{"not found", stacks.ErrStackNotFound, 404, "stack not found"},
		{"wrapped not found", fmt.Errorf("x: %w", stacks.ErrStackNotFound), 404, ""},
		{"exists", stacks.ErrStackExists, 409, ""},
		{"busy", stacks.ErrBusy, 409, ""},
		{"unavailable", fmt.Errorf("%w: compose file is missing", stacks.ErrStackUnavailable), 409, "stack is unavailable: compose file is missing"},
		{"op not found", stacks.ErrOperationNotFound, 404, ""},
		{"shutdown", stacks.ErrShutdown, 503, ""},
		{"compose unavailable", composecli.ErrComposeUnavailable, 503, ""},
		{"engine unavailable", fmt.Errorf("%w: dial", engine.ErrUnavailable), 503, ""},
		{"compose config failure", fmt.Errorf("check: %w", &composecli.ExitError{Code: 1, Output: "services.web.image must be a string"}), 422, "services.web.image must be a string"},
		{"outside roots", fsguard.ErrOutsideRoots, 403, ""},
		{"fs missing", fmt.Errorf("p: %w", os.ErrNotExist), 404, ""},
		{"service not found", composefile.ErrServiceNotFound, 404, ""},
		{"file conflict", composefile.ErrConflict, 409, ""},
		{"invalid config", composefile.ErrInvalidConfig, 422, ""},
		{"read only", &composefile.ReadOnlyFieldError{Field: composefile.FieldVolumes}, 422, "volumes"},
		{"not mapping", composefile.ErrNotMapping, 422, ""},
		{"multi doc", composefile.ErrMultipleDocuments, 422, ""},
		{"edit refused", fmt.Errorf("w: %w", composefile.ErrEditRefused), 422, "this change needs the raw YAML editor"},
		{"unknown", errors.New("boom secret"), 500, "internal error"},
	}
	fake := newFakeStacks()
	env, cookie := newStacksEnv(t, fake) // one login: password hashing is slow
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake.err = tc.err
			reqs := []struct{ method, path, body string }{
				{"GET", "/api/stacks/web", ""},
				{"POST", "/api/stacks/web/actions/up", "{}"},
				{"PUT", "/api/stacks/web/compose", `{"content":"x","hash":"h"}`},
			}
			for _, rq := range reqs {
				rec := env.do(t, rq.method, rq.path, rq.body, withCookie(cookie))
				if rec.Code != tc.status {
					t.Fatalf("%s %s = %d, want %d (%s)", rq.method, rq.path, rec.Code, tc.status, rec.Body)
				}
				if !strings.Contains(rec.Body.String(), tc.substr) {
					t.Errorf("body %s lacks %q", rec.Body, tc.substr)
				}
				if tc.status == 500 && strings.Contains(rec.Body.String(), "secret") {
					t.Errorf("500 leaked internal error: %s", rec.Body)
				}
			}
		})
	}
}

func TestValidationErrorIncludesFields(t *testing.T) {
	fake := newFakeStacks()
	fake.err = &stacks.ValidationError{Fields: []stacks.FieldError{{Field: "name", Message: "bad name"}}}
	env, cookie := newStacksEnv(t, fake)
	rec := env.do(t, "POST", "/api/stacks", `{"name":"Bad","config":{"image":"x"}}`, withCookie(cookie))
	if rec.Code != 400 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"fields":[{"field":"name","message":"bad name"}]`) || !strings.Contains(body, `"error":"bad name"`) {
		t.Errorf("body = %s", body)
	}
}

func TestCreateStack(t *testing.T) {
	fake := newFakeStacks()
	fake.partial = true
	env, cookie := newStacksEnv(t, fake)
	rec := env.do(t, "POST", "/api/stacks",
		`{"name":"web","service":"app","config":{"image":"nginx:1","ports":["8080:80"],"environment":[{"name":"A","value":"b"}],"networks":[{"name":"lan","ipv4Address":"10.0.0.5"}],"limits":{"cpus":"0.5","memory":"64M"}}}`,
		withCookie(cookie))
	if rec.Code != 201 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("X-Stackstead-Checks") != "partial" {
		t.Errorf("missing partial header")
	}
	if !strings.Contains(rec.Body.String(), `"name":"web"`) || !strings.Contains(rec.Body.String(), `"origin":"managed"`) {
		t.Errorf("body = %s", rec.Body)
	}
	got := fake.gotCreate
	if got.Name != "web" || got.Service != "app" || got.Config.Image != "nginx:1" ||
		got.Config.Ports[0] != "8080:80" || got.Config.Environment[0].Name != "A" ||
		got.Config.Networks[0].IPv4Address != "10.0.0.5" || got.Config.Limits.Memory != "64M" {
		t.Errorf("decoded request = %+v", got)
	}
}

func TestImportStack(t *testing.T) {
	fake := newFakeStacks()
	env, cookie := newStacksEnv(t, fake)
	rec := env.do(t, "POST", "/api/stacks/import", `{"path":"/srv/x/compose.yaml"}`, withCookie(cookie))
	if rec.Code != 201 || fake.gotContent != "/srv/x/compose.yaml" {
		t.Fatalf("status = %d, path %q", rec.Code, fake.gotContent)
	}
	if rec.Header().Get("X-Stackstead-Checks") != "" {
		t.Errorf("unexpected partial header")
	}
}

func TestListAndGetStack(t *testing.T) {
	env, cookie := newStacksEnv(t, newFakeStacks())
	rec := env.do(t, "GET", "/api/stacks", "", withCookie(cookie))
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), `[{"name":"web"`) || !strings.Contains(rec.Body.String(), `"serviceCount":2`) || strings.Contains(rec.Body.String(), `"services"`) {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	rec = env.do(t, "GET", "/api/stacks/web", "", withCookie(cookie))
	body := rec.Body.String()
	for _, want := range []string{`"hash":"h1"`, `"serviceCount":1`, `"services":[{`, `"image":"nginx:1"`, `"ipv4Address":"10.0.0.5"`, `"readOnly":["volumes"]`, `"containers":[{"id":"c1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("detail lacks %s: %s", want, body)
		}
	}
}

func TestRecentOperations(t *testing.T) {
	env, cookie := newStacksEnv(t, newFakeStacks())
	rec := env.do(t, "GET", "/api/stacks/web/operations", "", withCookie(cookie))
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), `[{"id":"op1"`) {
		t.Fatalf("operations = %d %s", rec.Code, rec.Body)
	}
}

func TestRawComposeRoundTrip(t *testing.T) {
	fake := newFakeStacks()
	env, cookie := newStacksEnv(t, fake)
	rec := env.do(t, "GET", "/api/stacks/web/compose", "", withCookie(cookie))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"hash":"h1"`) || !strings.Contains(rec.Body.String(), `"content":"services: {}\n"`) {
		t.Fatalf("raw = %d %s", rec.Code, rec.Body)
	}
	fake.partial = true
	rec = env.do(t, "PUT", "/api/stacks/web/compose", `{"content":"a: 1\n","hash":"h1"}`, withCookie(cookie))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"hash":"h2"}` {
		t.Fatalf("save = %d %s", rec.Code, rec.Body)
	}
	if fake.gotContent != "a: 1\n" || fake.gotHash != "h1" {
		t.Errorf("got content %q hash %q", fake.gotContent, fake.gotHash)
	}
	if rec.Header().Get("X-Stackstead-Checks") != "partial" {
		t.Errorf("missing partial header")
	}
}

func TestUpdateServiceDecodesConfig(t *testing.T) {
	fake := newFakeStacks()
	env, cookie := newStacksEnv(t, fake)
	rec := env.do(t, "PUT", "/api/stacks/web/services/app",
		`{"config":{"image":"nginx:2","restart":"always","networkMode":"host","healthcheck":{"test":["CMD","true"],"startPeriod":"5s","retries":3},"build":{"context":".","dockerfile":"Dockerfile"}},"hash":"h1"}`,
		withCookie(cookie))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"hash":"h2"}` {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	c := fake.gotCfg
	if fake.gotService != "app" || fake.gotHash != "h1" || c.Image != "nginx:2" || c.Restart != "always" ||
		c.NetworkMode != "host" || c.Healthcheck == nil || c.Healthcheck.StartPeriod != "5s" || c.Healthcheck.Retries != 3 ||
		c.Build == nil || c.Build.Dockerfile != "Dockerfile" {
		t.Errorf("decoded config = %+v", c)
	}
}

func TestLifecycleAndOperations(t *testing.T) {
	fake := newFakeStacks()
	env, cookie := newStacksEnv(t, fake)
	rec := env.do(t, "POST", "/api/stacks/web/actions/restart", "{}", withCookie(cookie))
	if rec.Code != 202 || fake.gotAction != "restart" || !strings.Contains(rec.Body.String(), `"state":"running"`) {
		t.Fatalf("act = %d %s", rec.Code, rec.Body)
	}
	rec = env.do(t, "DELETE", "/api/stacks/web?volumes=true", "", withCookie(cookie))
	if rec.Code != 202 || !fake.gotVolumes {
		t.Fatalf("remove = %d volumes=%v", rec.Code, fake.gotVolumes)
	}
	fake.gotVolumes = true
	if rec = env.do(t, "DELETE", "/api/stacks/web", "", withCookie(cookie)); rec.Code != 202 || fake.gotVolumes {
		t.Fatalf("remove without flag = %d volumes=%v", rec.Code, fake.gotVolumes)
	}
	if rec = env.do(t, "DELETE", "/api/stacks/web?volumes=maybe", "", withCookie(cookie)); rec.Code != 400 {
		t.Errorf("bad volumes flag = %d", rec.Code)
	}
	rec = env.do(t, "GET", "/api/operations/op1", "", withCookie(cookie))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"lines":["a","b"]`) || !strings.Contains(rec.Body.String(), `"exitCode":-1`) {
		t.Fatalf("operation = %d %s", rec.Code, rec.Body)
	}
	rec = env.do(t, "POST", "/api/operations/op1/cancel", "{}", withCookie(cookie))
	if rec.Code != 202 || fake.gotCancel != "op1" {
		t.Fatalf("cancel = %d %q", rec.Code, fake.gotCancel)
	}
}

func TestNetworksAndStatus(t *testing.T) {
	env, cookie := newStacksEnv(t, newFakeStacks())
	rec := env.do(t, "GET", "/api/networks", "", withCookie(cookie))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `[{"name":"lan","driver":"bridge","subnets":["10.0.0.0/24"]}]`) {
		t.Fatalf("networks = %d %s", rec.Code, rec.Body)
	}
	rec = env.do(t, "GET", "/api/status", "", withCookie(cookie))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"docker":"ok","compose":"v2.30.0"}` {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
}

func TestFSBrowse(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := newFakeStacks()
	fake.roots = []string{root}
	env, cookie := newStacksEnv(t, fake)

	rec := env.do(t, "GET", "/api/fs", "", withCookie(cookie))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), root) {
		t.Fatalf("roots = %d %s", rec.Code, rec.Body)
	}
	rec = env.do(t, "GET", "/api/fs?path="+root, "", withCookie(cookie))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `[{"name":"sub","isDir":true,"size":0},{"name":"a.txt","isDir":false,"size":2}]`) {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if rec = env.do(t, "GET", "/api/fs?path="+t.TempDir(), "", withCookie(cookie)); rec.Code != 403 {
		t.Errorf("outside root = %d, want 403", rec.Code)
	}
	if rec = env.do(t, "GET", "/api/fs?path="+filepath.Join(root, "nope"), "", withCookie(cookie)); rec.Code != 404 {
		t.Errorf("missing = %d, want 404", rec.Code)
	}
	if rec = env.do(t, "GET", "/api/fs?path=relative", "", withCookie(cookie)); rec.Code != 400 {
		t.Errorf("relative = %d, want 400", rec.Code)
	}
}

func TestOversizedBodyIsRejected(t *testing.T) {
	env, cookie := newStacksEnv(t, newFakeStacks())
	body := `{"content":"` + strings.Repeat("a", 1<<20) + `","hash":"h"}`
	rec := env.do(t, "PUT", "/api/stacks/web/compose", body, withCookie(cookie))
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// sseClient opens the stream against a real server so flushing is exercised.
type sseEvent struct{ name, data string }

func openStream(t *testing.T, env *testEnv, cookie *http.Cookie, ctx context.Context) (*http.Response, <-chan sseEvent) {
	t.Helper()
	srv := httptest.NewServer(env.handler)
	t.Cleanup(srv.Close)
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/operations/op1/stream", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	events := make(chan sseEvent, 64)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(resp.Body)
		var cur sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				cur.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimPrefix(line, "data: ")
			case line == ": ping":
				events <- sseEvent{name: "ping"}
			case line == "":
				if cur.name != "" {
					events <- cur
				}
				cur = sseEvent{}
			}
		}
	}()
	return resp, events
}

func nextEvent(t *testing.T, ch <-chan sseEvent, skipPings bool) sseEvent {
	t.Helper()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("stream ended early")
			}
			if skipPings && ev.name == "ping" {
				continue
			}
			return ev
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for event")
		}
	}
}

func TestStreamReplaysThenLiveThenDone(t *testing.T) {
	fake := newFakeStacks()
	fake.replay = []string{"old1", "old2"}
	fake.live = make(chan string, 4)
	fake.done = make(chan stacks.Operation, 1)
	env, cookie := newStacksEnv(t, fake)
	resp, events := openStream(t, env, cookie, context.Background())

	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("headers: %d %v", resp.StatusCode, resp.Header)
	}
	for _, want := range []string{`"old1"`, `"old2"`} {
		if ev := nextEvent(t, events, true); ev.name != "line" || ev.data != want {
			t.Fatalf("replay event = %+v, want %s", ev, want)
		}
	}
	fake.live <- "live1"
	if ev := nextEvent(t, events, true); ev.name != "line" || ev.data != `"live1"` {
		t.Fatalf("live event = %+v", ev)
	}
	// Idle: a heartbeat must arrive.
	if ev := nextEvent(t, events, false); ev.name != "ping" {
		t.Fatalf("expected ping, got %+v", ev)
	}
	final := fake.op
	final.State = stacks.OpSucceeded
	final.ExitCode = 0
	close(fake.live)
	fake.done <- final
	close(fake.done)
	ev := nextEvent(t, events, true)
	if ev.name != "done" || !strings.Contains(ev.data, `"state":"succeeded"`) {
		t.Fatalf("done event = %+v", ev)
	}
	if _, ok := <-events; ok {
		t.Error("stream should end after done")
	}
}

func TestStreamDroppedSubscriptionEndsWithDropped(t *testing.T) {
	fake := newFakeStacks()
	fake.live = make(chan string)
	fake.done = make(chan stacks.Operation, 1)
	env, cookie := newStacksEnv(t, fake)
	_, events := openStream(t, env, cookie, context.Background())
	close(fake.live)
	close(fake.done) // closed without a value: dropped
	if ev := nextEvent(t, events, true); ev.name != "dropped" {
		t.Fatalf("event = %+v, want dropped", ev)
	}
	if _, ok := <-events; ok {
		t.Error("stream should end after dropped")
	}
}

func TestStreamClientDisconnectUnsubscribes(t *testing.T) {
	fake := newFakeStacks()
	fake.live = make(chan string)
	fake.done = make(chan stacks.Operation, 1)
	env, cookie := newStacksEnv(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	_, events := openStream(t, env, cookie, ctx)
	nextEvent(t, events, false) // a ping proves the handler is in its loop
	cancel()
	select {
	case <-fake.unsubscribed:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not unsubscribe after client disconnect")
	}
}

func TestStreamUnknownOperation(t *testing.T) {
	fake := newFakeStacks()
	fake.err = stacks.ErrOperationNotFound
	env, cookie := newStacksEnv(t, fake)
	if rec := env.do(t, "GET", "/api/operations/nope/stream", "", withCookie(cookie)); rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func mustPrefix(s string) netip.Prefix { return netip.MustParsePrefix(s) }
