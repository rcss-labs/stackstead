package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/metrics"
)

type fakeRestarts map[string]int

func (f fakeRestarts) RestartCounts() map[string]int { return f }

const (
	idWeb  = "aaaaaaaaaaaa1111111111111111111111111111111111111111111111111111"
	idDB   = "bbbbbbbbbbbb2222222222222222222222222222222222222222222222222222"
	idCron = "cccccccccccc3333333333333333333333333333333333333333333333333333"
)

func containerObserve() *fakeObserve {
	obs := newFakeObserve()
	created := time.Date(2026, 10, 1, 8, 0, 0, 0, time.FixedZone("EST", -5*3600))
	obs.containers = []engine.Container{
		{ID: idWeb, Name: "web-web-1", Image: "web:1", State: "running", Status: "Up 2 hours (unhealthy)", Created: created,
			Labels: map[string]string{"com.docker.compose.project": "web", "com.docker.compose.service": "web"},
			Ports: []engine.Port{{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
				{IP: "::", PrivatePort: 80, PublicPort: 8080, Type: "tcp"}},
			Networks: map[string]string{"web_default": "172.18.0.2"}},
		{ID: idDB, Name: "adhoc-db", Image: "postgres:16", State: "exited", Status: "Exited (137) 1 hour ago", Created: created},
		{ID: idCron, Name: "other-cron-1", Image: "cron:1", State: "running", Status: "Up 1 minute",
			Labels: map[string]string{"com.docker.compose.project": "other", "com.docker.compose.service": "cron"}},
	}
	obs.latest = []metrics.ExpositionSample{
		{Project: "web", Service: "web", Container: "web-web-1", CPU: 12.5, Mem: 1000, MemLimit: 4000},
		{Container: "other-cron-1", CPU: 0.5, Mem: 10, MemLimit: 20},
	}
	return obs
}

func containersEnv(t *testing.T, obs *fakeObserve, tweak func(*api.Options)) (*testEnv, func(string) *httpResult) {
	t.Helper()
	return dashboardEnv(t, func(o *api.Options) {
		fs := newFakeStacks()
		o.Stacks, o.Operations, o.Observe = fs, fakeOps{fs}, obs
		o.Restarts = fakeRestarts{idWeb: 3}
		o.Now = func() time.Time { return summaryNow }
		if tweak != nil {
			tweak(o)
		}
	})
}

func TestContainerListJoinsSources(t *testing.T) {
	_, get := containersEnv(t, containerObserve(), nil)
	res := get("/api/containers")
	if res.Code != 200 {
		t.Fatalf("status = %d, body %s", res.Code, res.Body)
	}
	want := `[
	 {"id":"bbbbbbbbbbbb","name":"adhoc-db","image":"postgres:16","state":"exited","status":"Exited (137) 1 hour ago","health":"none",
	  "exitCode":137,"createdAt":"2026-10-01T13:00:00Z","project":"","service":"","stack":"","managed":"none",
	  "ports":[],"networks":{},"cpu":null,"mem":null,"memLimit":null,"restarts24h":0},
	 {"id":"cccccccccccc","name":"other-cron-1","image":"cron:1","state":"running","status":"Up 1 minute","health":"none",
	  "exitCode":null,"createdAt":"0001-01-01T00:00:00Z","project":"other","service":"cron","stack":"","managed":"compose",
	  "ports":[],"networks":{},"cpu":0.5,"mem":10,"memLimit":20,"restarts24h":0},
	 {"id":"aaaaaaaaaaaa","name":"web-web-1","image":"web:1","state":"running","status":"Up 2 hours (unhealthy)","health":"unhealthy",
	  "exitCode":null,"createdAt":"2026-10-01T13:00:00Z","project":"web","service":"web","stack":"web","managed":"stack",
	  "ports":["0.0.0.0:8080->80/tcp"],"networks":{"web_default":"172.18.0.2"},"cpu":12.5,"mem":1000,"memLimit":4000,"restarts24h":3}
	]`
	var got, exp any
	if err := json.Unmarshal(res.Body, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &exp); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, exp) {
		t.Errorf("body = %s\nwant %s", res.Body, want)
	}
	var rows []apitypes.ContainerRow
	if err := json.Unmarshal(res.Body, &rows); err != nil || len(rows) != 3 || rows[0].Name != "adhoc-db" {
		t.Errorf("rows must decode and be sorted by name: %v %+v", err, rows)
	}
}

func TestContainerListNilRestartsSourceIsZeros(t *testing.T) {
	_, get := containersEnv(t, containerObserve(), func(o *api.Options) { o.Restarts = nil })
	var rows []apitypes.ContainerRow
	res := get("/api/containers")
	if err := json.Unmarshal(res.Body, &rows); err != nil || res.Code != 200 {
		t.Fatalf("%d %s %v", res.Code, res.Body, err)
	}
	for _, r := range rows {
		if r.Restarts24h != 0 {
			t.Errorf("%s restarts = %d, want 0", r.Name, r.Restarts24h)
		}
	}
}

// countingObserve counts state-cache reads and can block them.
type countingObserve struct {
	*fakeObserve
	mu      sync.Mutex
	calls   int
	gate    chan struct{}
	entered chan struct{}
	panicOn bool
}

func (c *countingObserve) Containers(ctx context.Context) ([]engine.Container, error) {
	c.mu.Lock()
	c.calls++
	gate, entered, boom := c.gate, c.entered, c.panicOn
	c.panicOn = false
	c.mu.Unlock()
	if boom {
		panic("cache exploded")
	}
	if gate != nil {
		entered <- struct{}{}
		<-gate
	}
	return c.fakeObserve.Containers(ctx)
}

func (c *countingObserve) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestContainerListCachedFor2s(t *testing.T) {
	obs := &countingObserve{fakeObserve: containerObserve()}
	now := summaryNow
	env, get := containersEnv(t, nil, func(o *api.Options) { o.Observe = obs; o.Now = func() time.Time { return now } })
	_ = env
	get("/api/containers")
	now = now.Add(1900 * time.Millisecond)
	get("/api/containers")
	if n := obs.callCount(); n != 1 {
		t.Fatalf("calls within 2 s = %d, want 1", n)
	}
	now = now.Add(200 * time.Millisecond)
	get("/api/containers")
	if n := obs.callCount(); n != 2 {
		t.Errorf("calls after 2 s = %d, want 2", n)
	}
}

func TestContainerListSingleFlight50(t *testing.T) {
	obs := &countingObserve{fakeObserve: containerObserve(), gate: make(chan struct{}), entered: make(chan struct{}, 64)}
	env, _ := containersEnv(t, nil, func(o *api.Options) { o.Observe = obs })
	cookie := env.login(t)
	codes := make(chan int, 50)
	for i := 0; i < 50; i++ {
		go func() { codes <- env.do(t, "GET", "/api/containers", "", withCookie(cookie)).Code }()
	}
	<-obs.entered
	time.Sleep(100 * time.Millisecond)
	close(obs.gate)
	for i := 0; i < 50; i++ {
		if c := <-codes; c != 200 {
			t.Errorf("status = %d, want 200", c)
		}
	}
	if n := obs.callCount(); n != 1 {
		t.Errorf("state reads = %d, want 1", n)
	}
}

func TestContainerListPanicIsAnErrorAndNotCached(t *testing.T) {
	obs := &countingObserve{fakeObserve: containerObserve(), panicOn: true}
	_, get := containersEnv(t, nil, func(o *api.Options) { o.Observe = obs })
	if res := get("/api/containers"); res.Code != 500 {
		t.Errorf("panicking read = %d, want 500", res.Code)
	}
	if res := get("/api/containers"); res.Code != 200 {
		t.Errorf("retry = %d, want 200 (errors are not cached)", res.Code)
	}
}

func TestContainerList503WhenDisconnected(t *testing.T) {
	obs := containerObserve()
	obs.containersErr = fmt.Errorf("list: %w", engine.ErrUnavailable)
	_, get := containersEnv(t, obs, nil)
	res := get("/api/containers")
	var body map[string]string
	if res.Code != 503 || json.Unmarshal(res.Body, &body) != nil || body["error"] == "" {
		t.Errorf("= %d %s, want 503 {error}", res.Code, res.Body)
	}
}

func TestContainerListStacksErrorIs500(t *testing.T) {
	st := newFakeStacks()
	st.err = errors.New("boom")
	_, get := containersEnv(t, containerObserve(), func(o *api.Options) { o.Stacks = st })
	if res := get("/api/containers"); res.Code != 500 {
		t.Errorf("= %d %s, want 500", res.Code, res.Body)
	}
}

func TestContainerListRequiresSession(t *testing.T) {
	env := newEnvWith(t, "", func(o *api.Options) {
		fs := newFakeStacks()
		o.Stacks, o.Operations, o.Observe = fs, fakeOps{fs}, containerObserve()
	})
	env.createAdmin(t)
	if rec := env.do(t, "GET", "/api/containers", ""); rec.Code != 401 {
		t.Errorf("without cookie = %d, want 401", rec.Code)
	}
}

func TestContainerListAbsentWithoutSources(t *testing.T) {
	_, get := dashboardEnv(t, nil)
	if res := get("/api/containers"); res.Code != 404 {
		t.Errorf("= %d, want 404", res.Code)
	}
}

func TestContainerListStoppedContainerHasNoSample(t *testing.T) {
	obs := containerObserve()
	// A just-stopped container still has a sample in Latest() until the
	// sampler's next tick.
	obs.latest = append(obs.latest, metrics.ExpositionSample{Container: "adhoc-db", CPU: 9, Mem: 9, MemLimit: 9})
	_, get := containersEnv(t, obs, nil)
	var rows []apitypes.ContainerRow
	if err := json.Unmarshal(get("/api/containers").Body, &rows); err != nil {
		t.Fatal(err)
	}
	if rows[0].Name != "adhoc-db" || rows[0].CPU != nil || rows[0].Mem != nil || rows[0].MemLimit != nil {
		t.Errorf("exited container must show null cpu/mem: %+v", rows[0])
	}
}

func TestContainerListCancelledRequestLogsNoError(t *testing.T) {
	obs := &countingObserve{fakeObserve: containerObserve(), gate: make(chan struct{}), entered: make(chan struct{}, 4)}
	var logs bytes.Buffer
	env, _ := containersEnv(t, nil, func(o *api.Options) {
		o.Observe = obs
		o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})
	cookie := env.login(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.do(t, "GET", "/api/containers", "", withCookie(cookie), func(r *http.Request) { *r = *r.WithContext(ctx) })
	}()
	<-obs.entered
	cancel()
	<-done
	close(obs.gate)
	if strings.Contains(logs.String(), "request failed") {
		t.Errorf("a cancelled request must not log an error: %s", logs.String())
	}
}

// fakeContainerEngine records actions and serves canned inspect results.
type fakeContainerEngine struct {
	mu         sync.Mutex
	inspect    map[string]engine.ContainerInspect // by full id
	inspectErr error
	actionErr  error
	actions    []string // "id action timeout"
	actionCtx  context.Context
	onAction   func(id, action string)
	aliases    map[string]string // a name Docker would resolve -> full id
}

func (f *fakeContainerEngine) InspectContainer(_ context.Context, id string) (engine.ContainerInspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inspectErr != nil {
		return engine.ContainerInspect{}, f.inspectErr
	}
	if full, ok := f.aliases[id]; ok {
		return f.inspect[full], nil
	}
	for full, in := range f.inspect {
		if strings.HasPrefix(full, id) {
			return in, nil
		}
	}
	return engine.ContainerInspect{}, fmt.Errorf("inspect %s: %w", id, engine.ErrNotFound)
}

func (f *fakeContainerEngine) ContainerAction(ctx context.Context, id, action string, timeout int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, fmt.Sprintf("%s %s %d", id, action, timeout))
	f.actionCtx = ctx
	if f.onAction != nil {
		f.onAction(id, action)
	}
	return f.actionErr
}

func (f *fakeContainerEngine) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.actions...)
}

func inspectFixtures() map[string]engine.ContainerInspect {
	started := time.Date(2026, 10, 15, 6, 0, 0, 0, time.UTC)
	return map[string]engine.ContainerInspect{
		idWeb: {ID: idWeb, Name: "web-web-1", Image: "web:1", State: "running", Status: "running", Health: "unhealthy",
			StartedAt: started, Running: true, RestartPolicy: "unless-stopped",
			Labels: map[string]string{"com.docker.compose.project": "web", "com.docker.compose.service": "web"},
			Ports: []engine.Port{{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
				{IP: "::", PrivatePort: 80, PublicPort: 8080, Type: "tcp"}},
			Networks: map[string]string{"web_default": "172.18.0.2"},
			Mounts:   []engine.Mount{{Type: "bind", Source: "/srv/web", Destination: "/data", ReadOnly: true}}},
		idDB: {ID: idDB, Name: "adhoc-db", Image: "postgres:16", State: "exited", Status: "exited", Health: "none",
			StartedAt: started, FinishedAt: started.Add(time.Hour), ExitCode: 137, RestartPolicy: "no"},
		idCron: {ID: idCron, Name: "other-cron-1", Image: "cron:1", State: "running", Status: "running", Health: "none",
			StartedAt: started, Running: true,
			Labels: map[string]string{"com.docker.compose.project": "other", "com.docker.compose.service": "cron"}},
	}
}

func actionsEnv(t *testing.T, eng *fakeContainerEngine, tweak func(*api.Options)) (*testEnv, *http.Cookie) {
	t.Helper()
	env, _ := containersEnv(t, containerObserve(), func(o *api.Options) {
		o.ContainerEngine = eng
		if tweak != nil {
			tweak(o)
		}
	})
	return env, env.login(t)
}

func newFakeEngine() *fakeContainerEngine {
	return &fakeContainerEngine{inspect: inspectFixtures()}
}

func (e *testEnv) post(t *testing.T, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	return e.do(t, "POST", path, "{}", withCookie(cookie))
}

func errorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var b map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("body %s is not {error}: %v", rec.Body, err)
	}
	return b["error"]
}

func TestDetailsMapsInspect(t *testing.T) {
	env, cookie := actionsEnv(t, newFakeEngine(), nil)
	rec := env.do(t, "GET", "/api/containers/"+idWeb, "", withCookie(cookie))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	want := `{"id":"` + idWeb + `","name":"web-web-1","image":"web:1","state":"running","status":"Up 2 hours (unhealthy)","health":"unhealthy",
	 "startedAt":"2026-10-15T06:00:00Z","finishedAt":null,"exitCode":null,"restartPolicy":"unless-stopped","restarts24h":3,
	 "project":"web","service":"web","stack":"web","managed":"stack",
	 "ports":["0.0.0.0:8080->80/tcp"],"networks":{"web_default":"172.18.0.2"},
	 "mounts":[{"type":"bind","source":"/srv/web","destination":"/data","readOnly":true}],
	 "labels":{"com.docker.compose.project":"web","com.docker.compose.service":"web"}}`
	var got, exp any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &exp); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, exp) {
		t.Errorf("body = %s\nwant %s", rec.Body, want)
	}

	rec = env.do(t, "GET", "/api/containers/"+idDB[:12], "", withCookie(cookie))
	var db map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &db); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, err)
	}
	if db["exitCode"] != float64(137) || db["finishedAt"] != "2026-10-15T07:00:00Z" || db["managed"] != "none" {
		t.Errorf("exited container = %v", db)
	}
	for _, k := range []string{"ports", "mounts", "labels", "networks"} {
		if db[k] == nil {
			t.Errorf("%s must never be null", k)
		}
	}
}

func TestDetailsBodyHasNoEnvOrCmd(t *testing.T) {
	env, cookie := actionsEnv(t, newFakeEngine(), nil)
	rec := env.do(t, "GET", "/api/containers/"+idWeb, "", withCookie(cookie))
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"exitCode", "finishedAt", "health", "id", "image", "labels", "managed", "mounts", "name", "networks",
		"ports", "project", "restartPolicy", "restarts24h", "service", "stack", "startedAt", "state", "status"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v\nwant %v", keys, want)
	}
	for _, banned := range []string{"env", "cmd", "entrypoint", "user", "hostConfig"} {
		if _, ok := m[banned]; ok {
			t.Errorf("details must not include %q", banned)
		}
	}
}

func TestDetailsBadIdIs404(t *testing.T) {
	eng := newFakeEngine()
	env, cookie := actionsEnv(t, eng, nil)
	for _, id := range []string{"web-web-1", "abc", "AAAAAAAAAAAA", strings.Repeat("a", 65), "..%2f..%2fetc"} {
		if rec := env.do(t, "GET", "/api/containers/"+id, "", withCookie(cookie)); rec.Code != 404 {
			t.Errorf("GET %q = %d, want 404", id, rec.Code)
		}
		if rec := env.post(t, cookie, "/api/containers/"+id+"/stop"); rec.Code != 404 {
			t.Errorf("POST %q = %d, want 404", id, rec.Code)
		}
	}
	if len(eng.recorded()) != 0 {
		t.Errorf("Docker was called for a bad id: %v", eng.recorded())
	}
}

func TestDetailsUnknownIsNotFound(t *testing.T) {
	env, cookie := actionsEnv(t, newFakeEngine(), nil)
	if rec := env.do(t, "GET", "/api/containers/"+strings.Repeat("d", 12), "", withCookie(cookie)); rec.Code != 404 {
		t.Errorf("= %d, want 404", rec.Code)
	}
}

func TestDetailsUnavailableIs503(t *testing.T) {
	eng := newFakeEngine()
	eng.inspectErr = fmt.Errorf("inspect: %w", engine.ErrUnavailable)
	env, cookie := actionsEnv(t, eng, nil)
	rec := env.do(t, "GET", "/api/containers/"+idWeb, "", withCookie(cookie))
	if rec.Code != 503 || errorBody(t, rec) == "" {
		t.Errorf("= %d %s, want 503 {error}", rec.Code, rec.Body)
	}
}

func TestActionRefusesRegisteredStackContainer(t *testing.T) {
	eng := newFakeEngine()
	env, cookie := actionsEnv(t, eng, nil)
	for _, id := range []string{idWeb[:12], idWeb} {
		for _, action := range []string{"start", "stop", "restart"} {
			rec := env.post(t, cookie, "/api/containers/"+id+"/"+action)
			if rec.Code != 409 || errorBody(t, rec) != "web-web-1 belongs to stack web; use the stack's actions" {
				t.Errorf("%s %s = %d %s, want 409 with stack message", id, action, rec.Code, rec.Body)
			}
		}
	}
	if got := eng.recorded(); len(got) != 0 {
		t.Errorf("engine actions = %v, want none", got)
	}
}

func TestActionGuardReadsRegistryEveryRequest(t *testing.T) {
	eng := newFakeEngine()
	fs := newFakeStacks()
	fs.summary.Project = "not-yet"
	env, cookie := actionsEnv(t, eng, func(o *api.Options) { o.Stacks, o.Operations = fs, fakeOps{fs} })
	if rec := env.post(t, cookie, "/api/containers/"+idWeb+"/stop"); rec.Code != 200 {
		t.Fatalf("before registration = %d %s, want 200", rec.Code, rec.Body)
	}
	fs.summary.Project = "web" // the stack is registered between requests
	if rec := env.post(t, cookie, "/api/containers/"+idWeb+"/stop"); rec.Code != 409 {
		t.Errorf("after registration = %d %s, want 409", rec.Code, rec.Body)
	}
	if n := len(eng.recorded()); n != 1 {
		t.Errorf("engine actions = %d, want 1", n)
	}
}

func TestActionAllowsUnregisteredComposeProject(t *testing.T) {
	eng := newFakeEngine()
	env, cookie := actionsEnv(t, eng, nil)
	rec := env.post(t, cookie, "/api/containers/"+idCron+"/restart")
	if rec.Code != 200 {
		t.Fatalf("= %d %s, want 200", rec.Code, rec.Body)
	}
	if got := eng.recorded(); !reflect.DeepEqual(got, []string{idCron + " restart 10"}) {
		t.Errorf("engine actions = %v", got)
	}
	if dl, ok := eng.actionCtx.Deadline(); !ok || time.Until(dl) < 20*time.Second || time.Until(dl) > 31*time.Second {
		t.Errorf("action context deadline = %v (%v), want about 30 s", dl, ok)
	}
	var row apitypes.ContainerRow
	if err := json.Unmarshal(rec.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if row.ID != idCron[:12] || row.Name != "other-cron-1" || row.Managed != "compose" || row.Status != "Up 1 minute" ||
		row.State != "running" || row.CPU == nil || *row.CPU != 0.5 {
		t.Errorf("row = %+v", row)
	}
}

func TestActionReturnsFreshStateRow(t *testing.T) {
	eng := newFakeEngine()
	eng.onAction = func(id, action string) { // the engine now reports the container stopped
		in := eng.inspect[idCron]
		in.State, in.Status, in.Running, in.ExitCode = "exited", "exited", false, 0
		eng.inspect[idCron] = in
	}
	env, cookie := actionsEnv(t, eng, nil)
	rec := env.post(t, cookie, "/api/containers/"+idCron+"/stop")
	var row apitypes.ContainerRow
	if err := json.Unmarshal(rec.Body.Bytes(), &row); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, err)
	}
	if row.Status != "exited" {
		t.Errorf("a stale snapshot status must not be reused: %q", row.Status)
	}
	if row.State != "exited" || row.CPU != nil || row.ExitCode == nil || *row.ExitCode != 0 {
		t.Errorf("row after stop = %+v, want exited with null cpu and exit code 0", row)
	}
}

func TestActionDockerRefusalIs502(t *testing.T) {
	eng := newFakeEngine()
	eng.actionErr = fmt.Errorf("start: %w", &engine.ActionError{Status: 500, Message: "port is already allocated"})
	env, cookie := actionsEnv(t, eng, nil)
	rec := env.post(t, cookie, "/api/containers/"+idDB+"/start")
	if rec.Code != 502 || errorBody(t, rec) != "port is already allocated" {
		t.Errorf("= %d %s, want 502 with Docker's message", rec.Code, rec.Body)
	}
}

func TestActionEngineUnavailableOrSlowIs503(t *testing.T) {
	for name, err := range map[string]error{
		"unavailable": fmt.Errorf("start: %w", engine.ErrUnavailable),
		"deadline":    fmt.Errorf("docker engine request: %w", context.DeadlineExceeded),
	} {
		t.Run(name, func(t *testing.T) {
			eng := newFakeEngine()
			eng.actionErr = err
			env, cookie := actionsEnv(t, eng, nil)
			if rec := env.post(t, cookie, "/api/containers/"+idDB+"/start"); rec.Code != 503 || errorBody(t, rec) == "" {
				t.Errorf("= %d %s, want 503", rec.Code, rec.Body)
			}
		})
	}
}

func TestActionAlreadyStoppedIsSuccess(t *testing.T) {
	eng := newFakeEngine() // ContainerAction returns nil for Docker's 304
	env, cookie := actionsEnv(t, eng, nil)
	rec := env.post(t, cookie, "/api/containers/"+idDB+"/stop")
	var row apitypes.ContainerRow
	if err := json.Unmarshal(rec.Body.Bytes(), &row); err != nil || rec.Code != 200 || row.Name != "adhoc-db" {
		t.Errorf("= %d %s %v", rec.Code, rec.Body, err)
	}
}

func TestActionUnknownContainerIs404(t *testing.T) {
	eng := newFakeEngine()
	env, cookie := actionsEnv(t, eng, nil)
	if rec := env.post(t, cookie, "/api/containers/"+strings.Repeat("d", 12)+"/stop"); rec.Code != 404 {
		t.Errorf("= %d, want 404", rec.Code)
	}
	if len(eng.recorded()) != 0 {
		t.Errorf("engine actions = %v, want none", eng.recorded())
	}
}

func TestActionUnknownActionIs404(t *testing.T) {
	eng := newFakeEngine()
	env, cookie := actionsEnv(t, eng, nil)
	for _, a := range []string{"kill", "remove", "pause"} {
		if rec := env.post(t, cookie, "/api/containers/"+idDB+"/"+a); rec.Code != 404 {
			t.Errorf("%s = %d, want 404", a, rec.Code)
		}
	}
	if len(eng.recorded()) != 0 {
		t.Errorf("engine actions = %v, want none", eng.recorded())
	}
}

func TestActionRequiresJSON(t *testing.T) {
	eng := newFakeEngine()
	env, cookie := actionsEnv(t, eng, nil)
	rec := env.do(t, "POST", "/api/containers/"+idDB+"/start", "a=b", withCookie(cookie),
		func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") })
	if rec.Code != 415 {
		t.Errorf("form post = %d, want 415", rec.Code)
	}
	if len(eng.recorded()) != 0 {
		t.Errorf("engine actions = %v, want none", eng.recorded())
	}
}

func TestActionRoutesRequireSession(t *testing.T) {
	env, _ := actionsEnv(t, newFakeEngine(), nil)
	if rec := env.do(t, "POST", "/api/containers/"+idDB+"/start", "{}"); rec.Code != 401 {
		t.Errorf("POST without cookie = %d, want 401", rec.Code)
	}
	if rec := env.do(t, "GET", "/api/containers/"+idDB, ""); rec.Code != 401 {
		t.Errorf("GET without cookie = %d, want 401", rec.Code)
	}
}

func TestContainerActionRoutesAbsentWithoutEngine(t *testing.T) {
	env, get := containersEnv(t, containerObserve(), nil)
	if res := get("/api/containers/" + idWeb); res.Code != 404 {
		t.Errorf("details = %d, want 404", res.Code)
	}
	if rec := env.post(t, env.login(t), "/api/containers/"+idWeb+"/stop"); rec.Code != 404 {
		t.Errorf("action = %d, want 404", rec.Code)
	}
}

func TestContainerRoutesNeverResolveByName(t *testing.T) {
	eng := newFakeEngine()
	// Docker would resolve a container literally named like a short id.
	eng.aliases = map[string]string{"deadbeefcafe": idCron}
	env, cookie := actionsEnv(t, eng, nil)
	if rec := env.do(t, "GET", "/api/containers/deadbeefcafe", "", withCookie(cookie)); rec.Code != 404 {
		t.Errorf("details by name = %d %s, want 404", rec.Code, rec.Body)
	}
	if rec := env.post(t, cookie, "/api/containers/deadbeefcafe/stop"); rec.Code != 404 {
		t.Errorf("action by name = %d %s, want 404", rec.Code, rec.Body)
	}
	if got := eng.recorded(); len(got) != 0 {
		t.Errorf("engine actions = %v, want none", got)
	}
}

func TestContainerAmbiguousPrefixIs404(t *testing.T) {
	eng := newFakeEngine()
	eng.inspectErr = fmt.Errorf("inspect: %w", &engine.ActionError{Status: 400, Message: "multiple IDs found with provided prefix"})
	env, cookie := actionsEnv(t, eng, nil)
	if rec := env.do(t, "GET", "/api/containers/"+idWeb[:12], "", withCookie(cookie)); rec.Code != 404 {
		t.Errorf("details = %d %s, want 404", rec.Code, rec.Body)
	}
	if rec := env.post(t, cookie, "/api/containers/"+idWeb[:12]+"/stop"); rec.Code != 404 {
		t.Errorf("action = %d %s, want 404", rec.Code, rec.Body)
	}
}

func TestActionOutcomesAreLogged(t *testing.T) {
	var logs bytes.Buffer
	eng := newFakeEngine()
	eng.actionErr = fmt.Errorf("start: %w", &engine.ActionError{Status: 500, Message: "port is already allocated"})
	env, cookie := actionsEnv(t, eng, func(o *api.Options) { o.Logger = slog.New(slog.NewTextHandler(&logs, nil)) })
	env.post(t, cookie, "/api/containers/"+idWeb+"/stop") // refused: stack container
	env.post(t, cookie, "/api/containers/"+idDB+"/start") // failed: Docker refusal
	out := logs.String()
	for _, want := range []string{"container=web-web-1", "action=stop", "outcome=refused", "container=adhoc-db", "action=start", "outcome=failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "com.docker.compose") {
		t.Errorf("log must not include labels: %s", out)
	}
}

func containerLogsObserve() *fakeObserve {
	obs := containerObserve()
	obs.logs[idWeb] = fakeLogSpec{lines: []engine.LogLine{
		{Stream: "stdout", Time: t0, Text: "web one"}, {Stream: "stdout", Time: t0.Add(time.Second), Text: "web two"}}}
	obs.logs[idDB] = fakeLogSpec{lines: []engine.LogLine{{Stream: "stdout", Time: t0, Text: "db one"}}}
	return obs
}

func TestContainerLogsStreamsOneContainer(t *testing.T) {
	env, _ := containersEnv(t, containerLogsObserve(), nil)
	_, ch := openSSE(t, env, env.login(t), context.Background(), "/api/containers/"+idWeb[:12]+"/logs")
	evs := drain(t, ch)
	if names := eventNames(evs); len(names) == 0 || names[len(names)-1] != "end" {
		t.Fatalf("events = %v, want a trailing end", names)
	}
	var texts []string
	for _, le := range logsOf(t, evs) {
		if le.Container != "web-web-1" {
			t.Errorf("line from %q, want only web-web-1", le.Container)
		}
		texts = append(texts, le.Text)
	}
	if !reflect.DeepEqual(texts, []string{"web one", "web two"}) {
		t.Errorf("texts = %v", texts)
	}
	for _, ev := range evs {
		if ev.name == "log" && !strings.HasPrefix(ev.id, idWeb[:12]+":") {
			t.Errorf("log id = %q, want a cursor for the container", ev.id)
		}
	}
}

func TestContainerLogsResumeFromCursor(t *testing.T) {
	env, _ := containersEnv(t, containerLogsObserve(), nil)
	cursor := fmt.Sprintf("%s:%d:1", idWeb[:12], t0.UnixNano())
	_, ch := openSSEResume(t, env, env.login(t), context.Background(), "/api/containers/"+idWeb+"/logs", cursor)
	var texts []string
	for _, le := range logsOf(t, drain(t, ch)) {
		texts = append(texts, le.Text)
	}
	if !reflect.DeepEqual(texts, []string{"web two"}) {
		t.Errorf("texts = %v, want only the line after the cursor", texts)
	}
}

func TestContainerLogsUnknownIs404(t *testing.T) {
	obs := containerLogsObserve()
	env, _ := containersEnv(t, obs, nil)
	cookie := env.login(t)
	for _, id := range []string{strings.Repeat("d", 12), "web-web-1", "abc"} {
		if rec := env.do(t, "GET", "/api/containers/"+id+"/logs", "", withCookie(cookie)); rec.Code != 404 {
			t.Errorf("GET %q = %d, want 404", id, rec.Code)
		}
	}
	if n := obs.logCalls(); n != 0 {
		t.Errorf("Docker logs were opened %d times for unknown ids", n)
	}
}

func TestContainerLogsAmbiguousPrefixIs404(t *testing.T) {
	obs := containerLogsObserve()
	obs.containers = append(obs.containers, engine.Container{ID: idWeb[:12] + strings.Repeat("9", 52), Name: "twin", State: "running"})
	env, _ := containersEnv(t, obs, nil)
	if rec := env.do(t, "GET", "/api/containers/"+idWeb[:12]+"/logs", "", withCookie(env.login(t))); rec.Code != 404 {
		t.Errorf("ambiguous prefix = %d, want 404", rec.Code)
	}
}

func TestContainerLogsCacheErrorIs503(t *testing.T) {
	obs := containerLogsObserve()
	obs.containersErr = fmt.Errorf("state: %w", engine.ErrUnavailable)
	env, _ := containersEnv(t, obs, nil)
	if rec := env.do(t, "GET", "/api/containers/"+idWeb+"/logs", "", withCookie(env.login(t))); rec.Code != 503 {
		t.Errorf("= %d, want 503", rec.Code)
	}
}

func TestContainerLogsRequiresSession(t *testing.T) {
	env, _ := containersEnv(t, containerLogsObserve(), nil)
	if rec := env.do(t, "GET", "/api/containers/"+idWeb+"/logs", ""); rec.Code != 401 {
		t.Errorf("without cookie = %d, want 401", rec.Code)
	}
}
