package api_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/metrics"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/state"
)

// fakeLogSpec scripts one container's log stream.
type fakeLogSpec struct {
	lines []engine.LogLine
	err   error // delivered after the lines, as a daemon crash mid-stream
}

type fakeObserve struct {
	mu sync.Mutex

	containers    []engine.Container
	containersErr error
	connected     bool
	degraded      bool

	changes          chan state.Change
	changesCancelled chan struct{}
	changesOnce      sync.Once

	logs    map[string]fakeLogSpec // by container ID
	logCtxs map[string]context.Context
	logOpts map[string]engine.LogOptions

	points     []metrics.Point
	queryErr   error
	gotProject string
	gotService string
	gotFrom    time.Time
	gotTo      time.Time
	gotStep    time.Duration

	samples          chan metrics.Sample
	gotSampleProject string
	samplesCancelled chan struct{}
	samplesOnce      sync.Once

	latest     []metrics.ExpositionSample
	tokenHash  string
	tokenReads int
}

func newFakeObserve() *fakeObserve {
	return &fakeObserve{
		connected:        true,
		changes:          make(chan state.Change, 16),
		changesCancelled: make(chan struct{}),
		samples:          make(chan metrics.Sample, 16),
		samplesCancelled: make(chan struct{}),
		logs:             map[string]fakeLogSpec{},
		logCtxs:          map[string]context.Context{},
		logOpts:          map[string]engine.LogOptions{},
	}
}

func (f *fakeObserve) Containers(context.Context) ([]engine.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.containers, f.containersErr
}
func (f *fakeObserve) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}
func (f *fakeObserve) setConnected(v bool) { f.mu.Lock(); f.connected = v; f.mu.Unlock() }
func (f *fakeObserve) setDegraded(v bool)  { f.mu.Lock(); f.degraded = v; f.mu.Unlock() }
func (f *fakeObserve) Degraded() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.degraded
}
func (f *fakeObserve) SubscribeChanges() (<-chan state.Change, func()) {
	return f.changes, func() { f.changesOnce.Do(func() { close(f.changesCancelled) }) }
}
func (f *fakeObserve) SubscribeSamples(project string) (<-chan metrics.Sample, func()) {
	f.mu.Lock()
	f.gotSampleProject = project
	f.mu.Unlock()
	return f.samples, func() { f.samplesOnce.Do(func() { close(f.samplesCancelled) }) }
}
func (f *fakeObserve) Latest() []metrics.ExpositionSample { return f.latest }
func (f *fakeObserve) Query(_ context.Context, project, service string, from, to time.Time, step time.Duration) ([]metrics.Point, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotProject, f.gotService, f.gotFrom, f.gotTo, f.gotStep = project, service, from, to, step
	return f.points, f.queryErr
}
func (f *fakeObserve) TokenHash(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenReads++
	return f.tokenHash, nil
}
func (f *fakeObserve) SetTokenHash(_ context.Context, h string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenHash = h
	return nil
}

// Logs mimics engine.Client.Logs: lines, then (when following) a wait for
// ctx, then both channels close; a scripted error is delivered before close.
func (f *fakeObserve) Logs(ctx context.Context, c engine.Container, opts engine.LogOptions) (<-chan engine.LogLine, <-chan error) {
	f.mu.Lock()
	spec := f.logs[c.ID]
	f.logCtxs[c.ID] = ctx
	f.logOpts[c.ID] = opts
	f.mu.Unlock()
	out := make(chan engine.LogLine)
	errs := make(chan error, 1)
	go func() {
		defer close(errs)
		defer close(out)
		for _, l := range spec.lines {
			select {
			case out <- l:
			case <-ctx.Done():
				return
			}
		}
		if spec.err != nil {
			errs <- spec.err
			return
		}
		if opts.Follow {
			<-ctx.Done()
		}
	}()
	return out, errs
}

func (f *fakeObserve) logCtx(id string) context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logCtxs[id]
}

func (f *fakeObserve) logCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.logCtxs)
}

func container(id, name, project, service string) engine.Container {
	return engine.Container{ID: id, Name: name, State: "running", Labels: map[string]string{
		"com.docker.compose.project": project, "com.docker.compose.service": service}}
}

func newObserveEnv(t *testing.T, fake *fakeObserve) (*testEnv, *http.Cookie) {
	t.Helper()
	return newObserveEnvWith(t, fake, newFakeStacks())
}

func newObserveEnvWith(t *testing.T, fake *fakeObserve, st *fakeStacks) (*testEnv, *http.Cookie) {
	t.Helper()
	env := newEnvWith(t, "", func(o *api.Options) {
		o.Stacks, o.Operations, o.FSRoots, o.Status = st, fakeOps{st}, st.roots, st.status
		o.Observe = fake
		o.SSEHeartbeat = 20 * time.Millisecond
	})
	env.createAdmin(t)
	return env, env.login(t)
}

// openSSE opens path against a real server so flushing and disconnects are
// exercised. The returned channel closes when the stream ends.
func openSSE(t *testing.T, env *testEnv, cookie *http.Cookie, ctx context.Context, path string) (*http.Response, <-chan sseEvent) {
	t.Helper()
	srv := httptest.NewServer(env.handler)
	t.Cleanup(srv.Close)
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+path, nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	events := make(chan sseEvent, 256)
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

// drain collects non-ping events until the stream ends.
func drain(t *testing.T, ch <-chan sseEvent) []sseEvent {
	t.Helper()
	var out []sseEvent
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			if ev.name != "ping" {
				out = append(out, ev)
			}
		case <-timeout:
			t.Fatalf("stream did not end; got %v", out)
		}
	}
}

type logEvent struct {
	Service, Container, Stream, Text string
	Time                             time.Time
}

func logsOf(t *testing.T, evs []sseEvent) []logEvent {
	t.Helper()
	var out []logEvent
	for _, ev := range evs {
		if ev.name != "log" {
			continue
		}
		var le logEvent
		if err := json.Unmarshal([]byte(ev.data), &le); err != nil {
			t.Fatal(err)
		}
		out = append(out, le)
	}
	return out
}

func eventNames(evs []sseEvent) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.name)
	}
	return out
}

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func twoContainerFake() *fakeObserve {
	f := newFakeObserve()
	f.containers = []engine.Container{
		container("c-app", "web-app-1", "web", "app"),
		container("c-db", "web-db-1", "web", "db"),
		container("c-other", "other-app-1", "other", "app"),
	}
	f.logs["c-app"] = fakeLogSpec{lines: []engine.LogLine{{Stream: "stdout", Time: t0, Text: "app one"}, {Stream: "stderr", Time: t0, Text: "app two"}}}
	f.logs["c-db"] = fakeLogSpec{lines: []engine.LogLine{{Stream: "stdout", Time: t0, Text: "db one"}}}
	f.logs["c-other"] = fakeLogSpec{lines: []engine.LogLine{{Stream: "stdout", Time: t0, Text: "must not appear"}}}
	return f
}

func TestObserveRoutesRequireSession(t *testing.T) {
	env, _ := newObserveEnv(t, newFakeObserve())
	routes := []struct{ method, path string }{
		{"GET", "/api/stacks/web/logs"}, {"GET", "/api/stacks/web/metrics"}, {"GET", "/api/stacks/web/metrics/live"},
		{"GET", "/api/events"}, {"GET", "/api/operations"},
		{"POST", "/api/settings/metrics-token"}, {"DELETE", "/api/settings/metrics-token"},
	}
	for _, rt := range routes {
		if rec := env.do(t, rt.method, rt.path, "{}"); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without session = %d, want 401", rt.method, rt.path, rec.Code)
		}
	}
}

func TestObserveRoutesAbsentWithoutObserve(t *testing.T) {
	fake := newFakeStacks()
	env, cookie := newStacksEnv(t, fake)
	if rec := env.do(t, "GET", "/metrics", ""); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "spa") {
		t.Errorf("/metrics with Observe nil = %d %q, want 404 (not the SPA)", rec.Code, rec.Body)
	}
	for _, path := range []string{"/api/events", "/api/stacks/web/logs", "/api/stacks/web/metrics"} {
		if rec := env.do(t, "GET", path, "", withCookie(cookie)); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s with Observe nil = %d, want 404", path, rec.Code)
		}
	}
}

func TestLogsMergesStackContainers(t *testing.T) {
	fake := twoContainerFake()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs")
	evs := drain(t, ch)
	if names := eventNames(evs); len(names) == 0 || names[len(names)-1] != "end" {
		t.Fatalf("events = %v, want a trailing end", names)
	}
	got := map[string]logEvent{}
	for _, le := range logsOf(t, evs) {
		got[le.Text] = le
	}
	if len(got) != 3 {
		t.Fatalf("got %d distinct log lines, want 3 (other project excluded): %v", len(got), got)
	}
	if le := got["app two"]; le.Service != "app" || le.Container != "web-app-1" || le.Stream != "stderr" || !le.Time.Equal(t0) {
		t.Errorf("app two = %+v", le)
	}
	if le := got["db one"]; le.Service != "db" || le.Container != "web-db-1" || le.Stream != "stdout" {
		t.Errorf("db one = %+v", le)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if opts := fake.logOpts["c-app"]; opts.Follow || opts.Tail != 200 || !opts.Since.IsZero() {
		t.Errorf("default opts = %+v, want tail 200, no follow, no since", opts)
	}
}

func TestLogsServiceFilter(t *testing.T) {
	fake := twoContainerFake()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?service=db")
	logs := logsOf(t, drain(t, ch))
	if len(logs) != 1 || logs[0].Service != "db" {
		t.Fatalf("logs = %+v, want only the db line", logs)
	}
	if fake.logCalls() != 1 {
		t.Errorf("opened %d log streams, want 1", fake.logCalls())
	}
}

func TestLogsQueryOptionsReachTheEngine(t *testing.T) {
	fake := twoContainerFake()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?service=db&tail=5&since=2026-01-01T11:00:00Z")
	drain(t, ch)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	opts := fake.logOpts["c-db"]
	if opts.Tail != 5 || !opts.Since.Equal(time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("opts = %+v", opts)
	}
}

func TestLogsBadParams(t *testing.T) {
	env, cookie := newObserveEnv(t, twoContainerFake())
	for _, q := range []string{"tail=abc", "tail=-5", "since=yesterday", "follow=maybe"} {
		if rec := env.do(t, "GET", "/api/stacks/web/logs?"+q, "", withCookie(cookie)); rec.Code != http.StatusBadRequest {
			t.Errorf("?%s = %d, want 400", q, rec.Code)
		}
	}
}

func TestLogsNoContainersEndsImmediately(t *testing.T) {
	fake := newFakeObserve()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?follow=1")
	evs := drain(t, ch)
	if names := eventNames(evs); len(names) != 1 || names[0] != "end" {
		t.Fatalf("events = %v, want just end", names)
	}
}

func TestLogsEmptyProjectEndsWithoutStreams(t *testing.T) {
	fake := twoContainerFake()
	fake.containers = append(fake.containers, container("c-plain", "plain-1", "", ""))
	st := newFakeStacks()
	st.summary.Project = ""
	env, cookie := newObserveEnvWith(t, fake, st)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?follow=1")
	evs := drain(t, ch)
	if names := eventNames(evs); len(names) != 1 || names[0] != "end" {
		t.Fatalf("events = %v, want just end", names)
	}
	if n := fake.logCalls(); n != 0 {
		t.Errorf("opened %d log streams for a stack without a project, want 0", n)
	}
}

// A client that stays connected but stops reading must not hold the handler
// (and the Docker streams) forever: the per-write deadline cuts it.
func TestLogsStalledClientIsCutByWriteDeadline(t *testing.T) {
	fake := newFakeObserve()
	fake.containers = []engine.Container{container("c-app", "web-app-1", "web", "app")}
	big := strings.Repeat("x", 4096)
	lines := make([]engine.LogLine, 30000) // ~120MB, far beyond any socket buffer
	for i := range lines {
		lines[i] = engine.LogLine{Stream: "stdout", Time: t0, Text: big}
	}
	fake.logs["c-app"] = fakeLogSpec{lines: lines}
	st := newFakeStacks()
	env := newEnvWith(t, "", func(o *api.Options) {
		o.Stacks, o.Operations, o.FSRoots, o.Status = st, fakeOps{st}, st.roots, st.status
		o.Observe = fake
		o.SSEHeartbeat = time.Hour
		o.SSEWriteTimeout = 200 * time.Millisecond
	})
	env.createAdmin(t)
	cookie := env.login(t)
	srv := httptest.NewServer(env.handler)
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4096)
	}
	fmt.Fprintf(conn, "GET /api/stacks/web/logs?follow=1 HTTP/1.1\r\nHost: x\r\nCookie: %s=%s\r\n\r\n", cookie.Name, cookie.Value)
	// Never read: the connection stays open but stalled.

	deadline := time.Now().Add(5 * time.Second)
	for fake.logCtx("c-app") == nil {
		if time.Now().After(deadline) {
			t.Fatal("log stream was never opened")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-fake.logCtx("c-app").Done():
	case <-time.After(3 * time.Second):
		t.Fatal("stalled client still holds the Docker stream; write deadline did not cut it")
	}
}

func TestLogsCapsContainersAtFifty(t *testing.T) {
	fake := newFakeObserve()
	for i := 0; i < 60; i++ {
		id := fmt.Sprintf("c%02d", i)
		fake.containers = append(fake.containers, container(id, "web-app-"+id, "web", "app"))
	}
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs")
	drain(t, ch)
	if n := fake.logCalls(); n != 50 {
		t.Errorf("opened %d log streams, want 50", n)
	}
}

func TestLogsFollowDisconnectCancelsStreams(t *testing.T) {
	fake := twoContainerFake()
	env, cookie := newObserveEnv(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, ch := openSSE(t, env, cookie, ctx, "/api/stacks/web/logs?follow=1")
	// Three lines arrive, then the streams idle: the request is still open.
	seen := 0
	for seen < 3 {
		if ev := nextEvent(t, ch, true); ev.name == "log" {
			seen++
		}
	}
	for _, id := range []string{"c-app", "c-db"} {
		if c := fake.logCtx(id); c == nil || c.Err() != nil {
			t.Fatalf("stream %s should be open while the client is connected", id)
		}
	}
	cancel()
	for _, id := range []string{"c-app", "c-db"} {
		c := fake.logCtx(id)
		select {
		case <-c.Done():
		case <-time.After(3 * time.Second):
			t.Fatalf("stream %s was not cancelled after the client disconnected", id)
		}
	}
}

func TestLogsStreamErrorIsReportedThenEnds(t *testing.T) {
	fake := newFakeObserve()
	fake.containers = []engine.Container{container("c-app", "web-app-1", "web", "app")}
	fake.logs["c-app"] = fakeLogSpec{
		lines: []engine.LogLine{{Stream: "stdout", Time: t0, Text: "before crash"}},
		err:   fmt.Errorf("%w: connection reset", engine.ErrUnavailable),
	}
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?follow=1")
	names := eventNames(drain(t, ch))
	want := []string{"log", "error", "end"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", names, want)
	}
}

func TestLogsErrorMapping(t *testing.T) {
	t.Run("unknown stack is 404", func(t *testing.T) {
		st := newFakeStacks()
		st.err = stacks.ErrStackNotFound
		env, cookie := newObserveEnvWith(t, twoContainerFake(), st)
		for _, path := range []string{"/api/stacks/nope/logs", "/api/stacks/nope/metrics", "/api/stacks/nope/metrics/live"} {
			if rec := env.do(t, "GET", path, "", withCookie(cookie)); rec.Code != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404", path, rec.Code)
			}
		}
	})
	t.Run("engine down is 503", func(t *testing.T) {
		fake := twoContainerFake()
		fake.containersErr = fmt.Errorf("%w: not synced", engine.ErrUnavailable)
		env, cookie := newObserveEnv(t, fake)
		rec := env.do(t, "GET", "/api/stacks/web/logs", "", withCookie(cookie))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("body = %s, want a JSON error", rec.Body)
		}
	})
}

type metricPoint struct {
	Time                string   `json:"time"`
	Service             string   `json:"service"`
	CPU                 float64  `json:"cpu"`
	Mem                 uint64   `json:"mem"`
	MemLimit            uint64   `json:"memLimit"`
	NetRxBytesPerSec    *float64 `json:"netRxBytesPerSec"`
	NetTxBytesPerSec    *float64 `json:"netTxBytesPerSec"`
	BlkReadBytesPerSec  *float64 `json:"blkReadBytesPerSec"`
	BlkWriteBytesPerSec *float64 `json:"blkWriteBytesPerSec"`
}

func getMetrics(t *testing.T, env *testEnv, cookie *http.Cookie, query string) []metricPoint {
	t.Helper()
	rec := env.do(t, "GET", "/api/stacks/web/metrics"+query, "", withCookie(cookie))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var body struct {
		Points []metricPoint `json:"points"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Points == nil {
		t.Fatalf("points must be an array, body %s", rec.Body)
	}
	return body.Points
}

func TestMetricsDefaultWindowAndStep(t *testing.T) {
	fake := newFakeObserve()
	env, cookie := newObserveEnv(t, fake)
	before := time.Now()
	getMetrics(t, env, cookie, "?service=app")
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.gotProject != "web" || fake.gotService != "app" {
		t.Errorf("queried %q/%q, want web/app", fake.gotProject, fake.gotService)
	}
	if fake.gotTo.Before(before) || fake.gotTo.After(time.Now()) {
		t.Errorf("to = %v, want now", fake.gotTo)
	}
	if got := fake.gotTo.Sub(fake.gotFrom); got != time.Hour {
		t.Errorf("window = %v, want 1h", got)
	}
	if fake.gotStep <= 0 || fake.gotStep%time.Second != 0 {
		t.Fatalf("step = %v, want a positive whole number of seconds", fake.gotStep)
	}
	if points := int(time.Hour/fake.gotStep) + 1; points > 720 {
		t.Errorf("default step %v yields %d points, want <= 720", fake.gotStep, points)
	}
}

func TestMetricsStepIsClamped(t *testing.T) {
	fake := newFakeObserve()
	env, cookie := newObserveEnv(t, fake)
	from, to := "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z" // 24h
	cases := []struct {
		step string
		min  time.Duration
	}{
		{"", time.Second},
		{"&step=0", time.Second},
		{"&step=500ms", time.Second},
		{"&step=1s", time.Hour * 24 / 5000}, // would be 86400 points: raised to the cap
		{"&step=30", 30 * time.Second},      // bare number = seconds
	}
	for _, c := range cases {
		getMetrics(t, env, cookie, "?from="+from+"&to="+to+c.step)
		fake.mu.Lock()
		step := fake.gotStep
		fake.mu.Unlock()
		if step < c.min {
			t.Errorf("step %q -> %v, want >= %v", c.step, step, c.min)
		}
		if n := int(24*time.Hour/step) + 1; n > 5000 {
			t.Errorf("step %q -> %v gives %d points, want <= 5000", c.step, step, n)
		}
	}
	// An explicit sane step is honoured exactly.
	getMetrics(t, env, cookie, "?from="+from+"&to="+to+"&step=5m")
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.gotStep != 5*time.Minute {
		t.Errorf("step = %v, want 5m", fake.gotStep)
	}
}

func TestMetricsRatesFromCounters(t *testing.T) {
	fake := newFakeObserve()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake.points = []metrics.Point{
		{TS: base, Service: "app", CPU: 1.5, Mem: 10, MemLimit: 100, NetRx: 100, NetTx: 1000, BlkRead: 0, BlkWrite: 500},
		{TS: base.Add(10 * time.Second), Service: "app", CPU: 2.5, Mem: 20, MemLimit: 100, NetRx: 160, NetTx: 1000, BlkRead: 50, BlkWrite: 100}, // blkWrite fell: restart
		{TS: base, Service: "db", NetRx: 7},
	}
	env, cookie := newObserveEnv(t, fake)
	points := getMetrics(t, env, cookie, "?from=2026-01-01T00:00:00Z&to=2026-01-01T00:01:00Z&step=10s")
	if len(points) != 3 {
		t.Fatalf("points = %+v", points)
	}
	first, second, dbFirst := points[0], points[1], points[2]
	if first.NetRxBytesPerSec != nil || first.NetTxBytesPerSec != nil || first.BlkReadBytesPerSec != nil || first.BlkWriteBytesPerSec != nil {
		t.Errorf("first point of a service must carry no rates: %+v", first)
	}
	if dbFirst.NetRxBytesPerSec != nil {
		t.Errorf("first point of the next service must carry no rates (no cross-service diff): %+v", dbFirst)
	}
	if first.Service != "app" || first.CPU != 1.5 || first.Mem != 10 || first.MemLimit != 100 || first.Time != "2026-01-01T00:00:00Z" {
		t.Errorf("first = %+v", first)
	}
	if second.NetRxBytesPerSec == nil || *second.NetRxBytesPerSec != 6 {
		t.Errorf("net rx rate = %v, want 6/s (100 -> 160 over 10s)", second.NetRxBytesPerSec)
	}
	if second.NetTxBytesPerSec == nil || *second.NetTxBytesPerSec != 0 {
		t.Errorf("net tx rate = %v, want 0", second.NetTxBytesPerSec)
	}
	if second.BlkReadBytesPerSec == nil || *second.BlkReadBytesPerSec != 5 {
		t.Errorf("blk read rate = %v, want 5", second.BlkReadBytesPerSec)
	}
	if second.BlkWriteBytesPerSec == nil || *second.BlkWriteBytesPerSec != 0 {
		t.Errorf("blk write rate after a counter reset = %v, want 0 (clamped)", second.BlkWriteBytesPerSec)
	}
}

func TestMetricsBadParams(t *testing.T) {
	env, cookie := newObserveEnv(t, newFakeObserve())
	for _, q := range []string{"from=junk", "to=junk", "step=junk", "step=NaN", "step=Inf", "step=-Inf", "step=1e30", "step=99999999999", "from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z"} {
		if rec := env.do(t, "GET", "/api/stacks/web/metrics?"+q, "", withCookie(cookie)); rec.Code != http.StatusBadRequest {
			t.Errorf("?%s = %d, want 400", q, rec.Code)
		}
	}
}

func TestMetricsStoreErrorIs500(t *testing.T) {
	fake := newFakeObserve()
	fake.queryErr = errors.New("disk I/O error")
	env, cookie := newObserveEnv(t, fake)
	rec := env.do(t, "GET", "/api/stacks/web/metrics", "", withCookie(cookie))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "disk") {
		t.Errorf("status = %d body %s, want a generic 500", rec.Code, rec.Body)
	}
}

func TestLiveMetricsStreamsSamples(t *testing.T) {
	fake := newFakeObserve()
	env, cookie := newObserveEnv(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, ch := openSSE(t, env, cookie, ctx, "/api/stacks/web/metrics/live")
	fake.samples <- metrics.Sample{TS: t0, Project: "web", Service: "app", Container: "web-app-1", CPU: 12.5, Mem: 1024, MemLimit: 4096, NetRx: 5}
	ev := nextEvent(t, ch, true)
	if ev.name != "sample" {
		t.Fatalf("event = %+v, want sample", ev)
	}
	var got struct {
		Time               string  `json:"time"`
		Service, Container string  `json:"-"`
		CPU                float64 `json:"cpu"`
		Mem                uint64  `json:"mem"`
		NetRx              uint64  `json:"netRx"`
		ServiceName        string  `json:"service"`
		ContainerName      string  `json:"container"`
	}
	if err := json.Unmarshal([]byte(ev.data), &got); err != nil {
		t.Fatal(err)
	}
	if got.ServiceName != "app" || got.ContainerName != "web-app-1" || got.CPU != 12.5 || got.Mem != 1024 || got.NetRx != 5 || got.Time != "2026-01-01T12:00:00Z" {
		t.Errorf("sample = %s", ev.data)
	}
	if fake.gotSampleProject != "web" {
		t.Errorf("subscribed to %q, want web", fake.gotSampleProject)
	}
	cancel()
	select {
	case <-fake.samplesCancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("sample subscription was not cancelled on disconnect")
	}
}

// History and live samples use the same time format: UTC RFC3339.
func TestLiveMetricsTimeIsUTCRFC3339(t *testing.T) {
	fake := newFakeObserve()
	env, cookie := newObserveEnv(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, ch := openSSE(t, env, cookie, ctx, "/api/stacks/web/metrics/live")
	zone := time.FixedZone("x", 3*3600)
	fake.samples <- metrics.Sample{TS: time.Date(2026, 1, 1, 15, 0, 0, 123456789, zone), Project: "web", Service: "app"}
	ev := nextEvent(t, ch, true)
	var got struct {
		Time string `json:"time"`
	}
	if err := json.Unmarshal([]byte(ev.data), &got); err != nil {
		t.Fatal(err)
	}
	if got.Time != "2026-01-01T12:00:00Z" {
		t.Errorf("time = %q, want 2026-01-01T12:00:00Z", got.Time)
	}
}

func TestLiveMetricsDroppedSubscriberEndsStream(t *testing.T) {
	fake := newFakeObserve()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/metrics/live")
	close(fake.samples)
	evs := drain(t, ch)
	if names := eventNames(evs); len(names) == 0 || names[len(names)-1] != "dropped" {
		t.Fatalf("events = %v, want a final dropped", names)
	}
}

func TestEventsStatusChangeAndFlagFlips(t *testing.T) {
	fake := newFakeObserve()
	env, cookie := newObserveEnv(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, ch := openSSE(t, env, cookie, ctx, "/api/events")

	status := func(ev sseEvent) (string, bool) {
		t.Helper()
		if ev.name != "status" {
			t.Fatalf("event = %+v, want status", ev)
		}
		var s struct {
			Events          string `json:"events"`
			MetricsDegraded bool   `json:"metricsDegraded"`
		}
		if err := json.Unmarshal([]byte(ev.data), &s); err != nil {
			t.Fatal(err)
		}
		return s.Events, s.MetricsDegraded
	}
	if e, d := status(nextEvent(t, ch, true)); e != "connected" || d {
		t.Errorf("initial status = %s/%v", e, d)
	}

	fake.changes <- state.Change{Project: "web", Seq: 7}
	ev := nextEvent(t, ch, true)
	if ev.name != "change" || ev.data != `{"project":"web","seq":7}` {
		t.Fatalf("event = %+v, want change web/7", ev)
	}

	fake.setConnected(false)
	fake.changes <- state.Change{Project: "", Seq: 8}
	if e, _ := status(nextEvent(t, ch, true)); e != "disconnected" {
		t.Errorf("status after disconnect = %s", e)
	}

	fake.setDegraded(true) // no Change accompanies this: found by polling
	if _, d := status(nextEvent(t, ch, true)); !d {
		t.Error("metricsDegraded flip not reported")
	}

	cancel()
	select {
	case <-fake.changesCancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("change subscription was not cancelled on disconnect")
	}
}

func TestEventsDroppedSubscriberEndsStream(t *testing.T) {
	fake := newFakeObserve()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/events")
	nextEvent(t, ch, true) // initial status
	close(fake.changes)
	evs := drain(t, ch)
	if names := eventNames(evs); len(names) == 0 || names[len(names)-1] != "dropped" {
		t.Fatalf("events = %v, want a final dropped", names)
	}
}

type opsFake struct {
	fakeOps
	all []stacks.Operation
}

func (o opsFake) All() []stacks.Operation { return o.all }

func TestListOperationsFiltersByState(t *testing.T) {
	st := newFakeStacks()
	ops := opsFake{fakeOps{st}, []stacks.Operation{
		{ID: "a", Stack: "web", Action: "up", State: stacks.OpRunning, StartedAt: t0, ExitCode: -1},
		{ID: "b", Stack: "db", Action: "down", State: stacks.OpSucceeded, StartedAt: t0, ExitCode: 0},
		{ID: "c", Stack: "cache", Action: "up", State: stacks.OpRunning, StartedAt: t0, ExitCode: -1},
	}}
	env := newEnvWith(t, "", func(o *api.Options) {
		o.Stacks, o.Operations, o.Status = st, ops, st.status
	})
	env.createAdmin(t)
	cookie := env.login(t)

	ids := func(query string) string {
		rec := env.do(t, "GET", "/api/operations"+query, "", withCookie(cookie))
		if rec.Code != 200 {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
		}
		var out []struct{ ID, State string }
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, o := range out {
			got = append(got, o.ID)
		}
		return strings.Join(got, ",")
	}
	if got := ids("?state=running"); got != "a,c" {
		t.Errorf("running = %q, want a,c", got)
	}
	if got := ids(""); got != "a,b,c" {
		t.Errorf("all = %q, want a,b,c", got)
	}
	if rec := env.do(t, "GET", "/api/operations?state=bogus", "", withCookie(cookie)); rec.Code != 400 {
		t.Errorf("bogus state = %d, want 400", rec.Code)
	}
}

func sha(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func scrape(t *testing.T, env *testEnv, auth string) *httptest.ResponseRecorder {
	t.Helper()
	if auth == "" {
		return env.do(t, "GET", "/metrics", "")
	}
	return env.do(t, "GET", "/metrics", "", withHeader("Authorization", auth))
}

func createToken(t *testing.T, env *testEnv, cookie *http.Cookie) string {
	t.Helper()
	rec := env.do(t, "POST", "/api/settings/metrics-token", "{}", withCookie(cookie))
	if rec.Code != 200 {
		t.Fatalf("create token = %d, body %s", rec.Code, rec.Body)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Token == "" {
		t.Fatalf("body %s, err %v", rec.Body, err)
	}
	return body.Token
}

func TestMetricsEndpointWithoutTokenIs404(t *testing.T) {
	env, cookie := newObserveEnv(t, newFakeObserve())
	for _, auth := range []string{"", "Bearer anything"} {
		if rec := scrape(t, env, auth); rec.Code != http.StatusNotFound {
			t.Errorf("auth %q: /metrics = %d, want 404 when no token is configured", auth, rec.Code)
		}
	}
	// A session cookie is not a substitute for the bearer token.
	if rec := env.do(t, "GET", "/metrics", "", withCookie(cookie)); rec.Code != http.StatusNotFound {
		t.Errorf("/metrics with only a session = %d, want 404", rec.Code)
	}
}

func TestMetricsEndpointAuthAndBody(t *testing.T) {
	fake := newFakeObserve()
	fake.latest = []metrics.ExpositionSample{{Project: "web", Service: "app", Container: "web-app-1", CPU: 3, Mem: 100}}
	fake.degraded = true
	env, cookie := newObserveEnv(t, fake)
	token := createToken(t, env, cookie)

	for name, auth := range map[string]string{"missing": "", "wrong": "Bearer " + token + "x", "empty": "Bearer ", "other scheme": "Basic " + token, "bare": token} {
		rec := scrape(t, env, auth)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: /metrics = %d, want 401", name, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "stackstead_") {
			t.Errorf("%s: body leaked metrics", name)
		}
	}
	rec := scrape(t, env, "Bearer "+token)
	if rec.Code != 200 {
		t.Fatalf("/metrics = %d, body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("content type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "stackstead_metrics_degraded 1") || !strings.Contains(body, `service="app"`) {
		t.Errorf("body = %s", body)
	}
}

func TestMetricsTokenLifecycle(t *testing.T) {
	fake := newFakeObserve()
	env, cookie := newObserveEnv(t, fake)

	first := createToken(t, env, cookie)
	if len(first) < 43 || strings.ContainsAny(first, "+/= \n") { // 32 bytes, URL-safe base64
		t.Errorf("token %q is too short or not URL-safe", first)
	}
	if fake.tokenHash != sha(first) {
		t.Errorf("stored %q, want the SHA-256 hex of the token and nothing else", fake.tokenHash)
	}
	if rec := scrape(t, env, "Bearer "+first); rec.Code != 200 {
		t.Fatalf("first token = %d", rec.Code)
	}

	second := createToken(t, env, cookie)
	if second == first {
		t.Fatal("rotation returned the same token")
	}
	if rec := scrape(t, env, "Bearer "+first); rec.Code != http.StatusUnauthorized {
		t.Errorf("rotated-out token = %d, want 401", rec.Code)
	}
	if rec := scrape(t, env, "Bearer "+second); rec.Code != 200 {
		t.Errorf("new token = %d, want 200", rec.Code)
	}

	if rec := env.do(t, "DELETE", "/api/settings/metrics-token", "", withCookie(cookie)); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", rec.Code)
	}
	if fake.tokenHash != "" {
		t.Errorf("hash %q still stored after delete", fake.tokenHash)
	}
	if rec := scrape(t, env, "Bearer "+second); rec.Code != http.StatusNotFound {
		t.Errorf("/metrics after delete = %d, want 404", rec.Code)
	}
}

func TestMetricsTokenCreateRequiresJSON(t *testing.T) {
	env, cookie := newObserveEnv(t, newFakeObserve())
	req := httptest.NewRequest("POST", "/api/settings/metrics-token", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "text/plain")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

func TestStatusReportsEventsAndMetricsDegraded(t *testing.T) {
	fake := newFakeObserve()
	env, cookie := newObserveEnv(t, fake)
	get := func() map[string]any {
		rec := env.do(t, "GET", "/api/status", "", withCookie(cookie))
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := get(); m["events"] != "connected" || m["metricsDegraded"] != false || m["docker"] != "ok" {
		t.Errorf("status = %v", m)
	}
	fake.setConnected(false)
	fake.setDegraded(true)
	if m := get(); m["events"] != "disconnected" || m["metricsDegraded"] != true {
		t.Errorf("status = %v", m)
	}
}

func TestMetricsEndpointDoesNotReadStorePerRequest(t *testing.T) {
	fake := newFakeObserve()
	fake.tokenHash = sha("preconfigured") // as after a restart: set in the store, not through this server
	env, cookie := newObserveEnv(t, fake)

	for i := 0; i < 20; i++ {
		if rec := scrape(t, env, fmt.Sprintf("Bearer wrong-%d", i)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong token = %d", rec.Code)
		}
	}
	if rec := scrape(t, env, "Bearer preconfigured"); rec.Code != 200 {
		t.Fatalf("right token = %d", rec.Code)
	}
	fake.mu.Lock()
	reads := fake.tokenReads
	fake.mu.Unlock()
	if reads > 1 {
		t.Errorf("token hash read from the store %d times for 21 scrapes, want at most 1", reads)
	}

	// Rotation and deletion still take effect immediately.
	token := createToken(t, env, cookie)
	if rec := scrape(t, env, "Bearer preconfigured"); rec.Code != http.StatusUnauthorized {
		t.Errorf("old token after rotation = %d, want 401", rec.Code)
	}
	if rec := scrape(t, env, "Bearer "+token); rec.Code != 200 {
		t.Errorf("new token = %d, want 200", rec.Code)
	}
	env.do(t, "DELETE", "/api/settings/metrics-token", "", withCookie(cookie))
	if rec := scrape(t, env, "Bearer "+token); rec.Code != http.StatusNotFound {
		t.Errorf("after delete = %d, want 404", rec.Code)
	}
}
