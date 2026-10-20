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
	"slices"
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
	// endsAtOnce makes a following stream close right after its lines, as a
	// stream of a container that is already gone would.
	endsAtOnce bool
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

	events          chan engine.Event
	eventsCancelled chan struct{}
	eventsOnce      sync.Once

	logs    map[string]fakeLogSpec // by container ID
	logCtxs map[string]context.Context
	logOpts map[string]engine.LogOptions

	logCallOpts    map[string][]engine.LogOptions // every Logs call's options, by container ID
	logStops       map[string]chan struct{}       // closed by endLogs to end the newest follow stream
	openLogStreams int                            // Logs streams whose goroutine has not returned
	changeSubs     int
	changeCancels  int
	containerCalls int
	onSubscribe    func() // runs inside SubscribeChanges, before it returns

	points     []metrics.ContainerBucket
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
	tokenErr   error
}

func newFakeObserve() *fakeObserve {
	return &fakeObserve{
		connected:        true,
		changes:          make(chan state.Change, 16),
		changesCancelled: make(chan struct{}),
		events:           make(chan engine.Event, 16),
		eventsCancelled:  make(chan struct{}),
		samples:          make(chan metrics.Sample, 16),
		samplesCancelled: make(chan struct{}),
		logs:             map[string]fakeLogSpec{},
		logCtxs:          map[string]context.Context{},
		logOpts:          map[string]engine.LogOptions{},
		logCallOpts:      map[string][]engine.LogOptions{},
		logStops:         map[string]chan struct{}{},
	}
}

func (f *fakeObserve) Containers(context.Context) ([]engine.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containerCalls++
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
	f.mu.Lock()
	f.changeSubs++
	ch := f.changes
	hook := f.onSubscribe
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return ch, func() {
		f.mu.Lock()
		f.changeCancels++
		f.mu.Unlock()
		f.changesOnce.Do(func() { close(f.changesCancelled) })
	}
}
func (f *fakeObserve) SubscribeEvents() (<-chan engine.Event, func()) {
	return f.events, func() { f.eventsOnce.Do(func() { close(f.eventsCancelled) }) }
}
func (f *fakeObserve) SubscribeSamples(project string) (<-chan metrics.Sample, func()) {
	f.mu.Lock()
	f.gotSampleProject = project
	f.mu.Unlock()
	return f.samples, func() { f.samplesOnce.Do(func() { close(f.samplesCancelled) }) }
}
func (f *fakeObserve) Latest() []metrics.ExpositionSample { return f.latest }
func (f *fakeObserve) QueryContainers(_ context.Context, project, service string, from, to time.Time, step time.Duration) ([]metrics.ContainerBucket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotProject, f.gotService, f.gotFrom, f.gotTo, f.gotStep = project, service, from, to, step
	return f.points, f.queryErr
}
func (f *fakeObserve) TokenHash(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenReads++
	if f.tokenErr != nil {
		return "", f.tokenErr
	}
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
	f.logCallOpts[c.ID] = append(f.logCallOpts[c.ID], opts)
	stop := make(chan struct{})
	f.logStops[c.ID] = stop
	f.openLogStreams++
	f.mu.Unlock()
	out := make(chan engine.LogLine)
	errs := make(chan error, 1)
	lines := spec.lines
	if opts.Tail >= 0 && opts.Tail < len(lines) { // like the daemon, keep only the last Tail lines
		lines = lines[len(lines)-opts.Tail:]
	}
	go func() {
		defer close(errs)
		defer close(out)
		defer func() { f.mu.Lock(); f.openLogStreams--; f.mu.Unlock() }()
		for _, l := range lines {
			// Like the daemon, `since` is inclusive: only lines strictly before it are dropped.
			if !opts.Since.IsZero() && l.Time.Before(opts.Since) {
				continue
			}
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
		if opts.Follow && !spec.endsAtOnce {
			select {
			case <-ctx.Done():
			case <-stop:
			}
		}
	}()
	return out, errs
}

// endLogs ends the newest following stream of container id on its own, as a
// restart or a stop does.
func (f *fakeObserve) endLogs(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if stop := f.logStops[id]; stop != nil {
		close(stop)
		delete(f.logStops, id)
	}
}

func (f *fakeObserve) logCallsFor(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.logCallOpts[id])
}

// logCall returns the options of the n-th (0-based) Logs call for id.
func (f *fakeObserve) logCall(id string, n int) engine.LogOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logCallOpts[id][n]
}

func (f *fakeObserve) setLogs(id string, spec fakeLogSpec) {
	f.mu.Lock()
	f.logs[id] = spec
	f.mu.Unlock()
}

func (f *fakeObserve) setContainers(cs []engine.Container) {
	f.mu.Lock()
	f.containers = cs
	f.mu.Unlock()
}

func (f *fakeObserve) pushChange(ch state.Change) {
	f.mu.Lock()
	c := f.changes
	f.mu.Unlock()
	c <- ch
}

// dropChanges simulates the cache dropping a slow subscriber: the current
// changes channel is closed and the next SubscribeChanges gets a fresh one.
func (f *fakeObserve) dropChanges() {
	f.mu.Lock()
	old := f.changes
	f.changes = make(chan state.Change, 16)
	f.mu.Unlock()
	close(old)
}

func (f *fakeObserve) changeSubscriptions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.changeSubs
}

func (f *fakeObserve) containersCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.containerCalls
}

func (f *fakeObserve) openStreams() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.openLogStreams
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

func newObserveEnvWith(t *testing.T, fake *fakeObserve, st *fakeStacks, tweaks ...func(*api.Options)) (*testEnv, *http.Cookie) {
	t.Helper()
	env := newEnvWith(t, "", func(o *api.Options) {
		o.Stacks, o.Operations, o.FSRoots, o.Status = st, fakeOps{st}, st.roots, st.status
		o.Observe = fake
		o.NetworkEngine = lanNetworkEngine()
		o.SSEHeartbeat = 20 * time.Millisecond
		for _, tweak := range tweaks {
			tweak(o)
		}
	})
	env.createAdmin(t)
	return env, env.login(t)
}

// openSSE opens path against a real server so flushing and disconnects are
// exercised. The returned channel closes when the stream ends.
func openSSE(t *testing.T, env *testEnv, cookie *http.Cookie, ctx context.Context, path string) (*http.Response, <-chan sseEvent) {
	t.Helper()
	return openSSEResume(t, env, cookie, ctx, path, "")
}

// openSSEResume is openSSE sending a Last-Event-ID header when lastID is set.
func openSSEResume(t *testing.T, env *testEnv, cookie *http.Cookie, ctx context.Context, path, lastID string) (*http.Response, <-chan sseEvent) {
	t.Helper()
	srv := httptest.NewServer(env.handler)
	t.Cleanup(srv.Close)
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+path, nil)
	req.AddCookie(cookie)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
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
			case strings.HasPrefix(line, "id: "):
				cur.id = strings.TrimPrefix(line, "id: ")
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
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs")
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
	bucket := func(service, container string, sec int, cpu float64, mem, rx, tx, br, bw uint64) metrics.ContainerBucket {
		return metrics.ContainerBucket{TS: base.Add(time.Duration(sec) * time.Second), Service: service, Container: container, Samples: 1,
			CPUSum: cpu, MemSum: mem, MemLimitSum: 100, NetRx: rx, NetTx: tx, BlkRead: br, BlkWrite: bw}
	}
	fake.points = []metrics.ContainerBucket{
		bucket("app", "app-1", 0, 1.5, 10, 100, 1000, 0, 500),
		bucket("app", "app-1", 10, 2.5, 20, 160, 1000, 50, 100), // blkWrite fell: restart
		bucket("db", "db-1", 0, 0, 0, 7, 0, 0, 0),
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

func TestMetricsRatesAreTotalsAcrossReplicas(t *testing.T) {
	fake := newFakeObserve()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rep := func(container string, sec int, cpu float64, rx uint64) metrics.ContainerBucket {
		return metrics.ContainerBucket{TS: base.Add(time.Duration(sec) * time.Second), Service: "app", Container: container,
			Samples: 1, CPUSum: cpu, MemSum: 10, MemLimitSum: 100, NetRx: rx}
	}
	fake.points = []metrics.ContainerBucket{
		rep("app-1", 0, 10, 0), rep("app-1", 10, 10, 1_000_0),
		rep("app-2", 0, 30, 5), rep("app-2", 10, 30, 5+1_000_0),
	}
	env, cookie := newObserveEnv(t, fake)
	points := getMetrics(t, env, cookie, "?from=2026-01-01T00:00:00Z&to=2026-01-01T00:01:00Z&step=10s&service=app")
	if len(points) != 2 {
		t.Fatalf("points = %+v", points)
	}
	if points[0].NetRxBytesPerSec != nil {
		t.Errorf("first bucket must carry no rate: %+v", points[0])
	}
	if got := points[1].NetRxBytesPerSec; got == nil || *got != 2000 {
		t.Errorf("net rx = %v, want 2000/s (two replicas at 1000/s)", got)
	}
	if points[1].CPU != 20 {
		t.Errorf("cpu = %v, want the 20 average", points[1].CPU)
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

func TestEventsStreamCarriesEventMessages(t *testing.T) {
	fake := newFakeObserve()
	env, cookie := newObserveEnv(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, ch := openSSE(t, env, cookie, ctx, "/api/events")
	if ev := nextEvent(t, ch, true); ev.name != "status" {
		t.Fatalf("first event = %+v, want status", ev)
	}

	exit := 1
	at := time.Date(2026, 10, 15, 15, 5, 41, 0, time.FixedZone("x", 3*3600))
	// Sources are independent channels, so interleaving is only ordered per
	// source; read each message before sending the next to keep this deterministic.
	steps := []struct {
		send func()
		want sseEvent
	}{
		{func() {
			fake.events <- engine.Event{Action: "die", Name: "monitoring-loki-1", Image: "grafana/loki:3.1.1", Time: at, ExitCode: &exit,
				Labels: map[string]string{"com.docker.compose.project": "monitoring", "com.docker.compose.service": "loki"}}
		}, sseEvent{name: "event", data: `{"action":"die","container":"monitoring-loki-1","project":"monitoring","service":"loki","image":"grafana/loki:3.1.1","time":"2026-10-15T12:05:41Z","exitCode":1}`}},
		{func() { fake.changes <- state.Change{Project: "monitoring", Seq: 3} },
			sseEvent{name: "change", data: `{"project":"monitoring","seq":3}`}},
		{func() {
			fake.events <- engine.Event{Action: "health_status", Name: "standalone", Image: "redis:7", Time: at, Health: "unhealthy"}
		}, sseEvent{name: "event", data: `{"action":"health_status","container":"standalone","project":"","service":"","image":"redis:7","time":"2026-10-15T12:05:41Z","health":"unhealthy"}`}},
	}
	for i, st := range steps {
		st.send()
		got := nextEvent(t, ch, true)
		if got.name != st.want.name || got.data != st.want.data || got.id != "" {
			t.Fatalf("event %d = %+v, want %+v", i, got, st.want)
		}
	}
	cancel()
	select {
	case <-fake.eventsCancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("event subscription was not cancelled on disconnect")
	}
}

func TestEventMessageOmitsExitCodeExceptDie(t *testing.T) {
	fake := newFakeObserve()
	env, cookie := newObserveEnv(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, ch := openSSE(t, env, cookie, ctx, "/api/events")
	nextEvent(t, ch, true) // initial status

	zero := 0
	fake.events <- engine.Event{Action: "die", Name: "a", ExitCode: &zero}
	fake.events <- engine.Event{Action: "stop", Name: "a", ExitCode: &zero} // defensive: only die carries it
	fake.events <- engine.Event{Action: "start", Name: "a", Health: "healthy"}
	for i, wantExit := range []bool{true, false, false} {
		ev := nextEvent(t, ch, true)
		var m map[string]any
		if err := json.Unmarshal([]byte(ev.data), &m); err != nil {
			t.Fatal(err)
		}
		if _, has := m["exitCode"]; has != wantExit {
			t.Errorf("event %d exitCode present = %v, want %v: %s", i, has, wantExit, ev.data)
		}
		if _, has := m["health"]; has {
			t.Errorf("event %d has health but is not health_status: %s", i, ev.data)
		}
	}
}

func TestEventsDroppedOnEventChannelClose(t *testing.T) {
	fake := newFakeObserve()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/events")
	nextEvent(t, ch, true) // initial status
	close(fake.events)
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

// ---- log cursors and token status ----

const (
	cursorA = "aaaaaaaaaaaa" // 12-hex prefix of cursorAFull
	cursorB = "bbbbbbbbbbbb"
)

var (
	cursorAFull = cursorA + strings.Repeat("0", 52)
	cursorBFull = cursorB + strings.Repeat("0", 52)
	ts1         = t0
	ts2         = t0.Add(time.Second + 7) // nanosecond part must survive
)

func cursorID(prefix string, ts time.Time, ordinal int) string {
	return fmt.Sprintf("%s:%d:%d", prefix, ts.UnixNano(), ordinal)
}

// cursorFake has container A (app) with three lines, two sharing ts2, and
// container B (db) with one line.
func cursorFake() *fakeObserve {
	f := newFakeObserve()
	f.containers = []engine.Container{
		container(cursorAFull, "web-app-1", "web", "app"),
		container(cursorBFull, "web-db-1", "web", "db"),
	}
	f.logs[cursorAFull] = fakeLogSpec{lines: []engine.LogLine{
		{Stream: "stdout", Time: ts1, Text: "one"},
		{Stream: "stdout", Time: ts2, Text: "two"},
		{Stream: "stderr", Time: ts2, Text: "three"},
	}}
	f.logs[cursorBFull] = fakeLogSpec{lines: []engine.LogLine{{Stream: "stdout", Time: ts1, Text: "db"}}}
	return f
}

func TestLogEventsCarryCursorIds(t *testing.T) {
	fake := cursorFake()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?service=app")
	evs := drain(t, ch)
	var ids []string
	for _, ev := range evs {
		switch ev.name {
		case "log":
			ids = append(ids, ev.id)
		case "end":
			if ev.id != "" {
				t.Errorf("end event carries id %q", ev.id)
			}
		}
	}
	want := []string{cursorID(cursorA, ts1, 1), cursorID(cursorA, ts2, 1), cursorID(cursorA, ts2, 2)}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

func TestResumeSkipsOrdinalAtSameTimestamp(t *testing.T) {
	fake := cursorFake()
	// What the daemon returns for since=ts2 (inclusive): nothing older.
	fake.logs[cursorAFull] = fakeLogSpec{lines: []engine.LogLine{
		{Stream: "stdout", Time: ts2, Text: "two"},
		{Stream: "stderr", Time: ts2, Text: "three"},
		{Stream: "stdout", Time: ts2.Add(time.Second), Text: "four"},
	}}
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSEResume(t, env, cookie, context.Background(), "/api/stacks/web/logs?tail=7", cursorID(cursorA, ts2, 1))
	got := logsOf(t, drain(t, ch))
	var app []string
	for _, l := range got {
		if l.Service == "app" {
			app = append(app, l.Text)
		}
	}
	if !slices.Equal(app, []string{"three", "four"}) {
		t.Errorf("app lines = %v, want the first ts2 line skipped", app)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	a, b := fake.logOpts[cursorAFull], fake.logOpts[cursorBFull]
	if !a.Since.Equal(ts2) || a.Tail != -1 {
		t.Errorf("resumed container opts = %+v, want Since=ts2 Tail=-1", a)
	}
	if b.Tail != 7 || !b.Since.IsZero() {
		t.Errorf("container without cursor opts = %+v, want the request's tail", b)
	}
}

func TestResumeIgnoresMalformedCursors(t *testing.T) {
	fake := cursorFake()
	env, cookie := newObserveEnv(t, fake)
	hdr := "garbage,,:1:1,zz:5:1," + cursorA + ":x:1," + cursorA + ":5:0," + cursorID(cursorA, ts2, 1) + ", " + cursorB + ":9"
	_, ch := openSSEResume(t, env, cookie, context.Background(), "/api/stacks/web/logs", hdr)
	evs := drain(t, ch)
	if names := eventNames(evs); names[len(names)-1] != "end" {
		t.Fatalf("stream did not end cleanly: %v", names)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if o := fake.logOpts[cursorAFull]; !o.Since.Equal(ts2) || o.Tail != -1 {
		t.Errorf("valid entry did not resume: %+v", o)
	}
	if o := fake.logOpts[cursorBFull]; o.Tail != defaultTailForTest || !o.Since.IsZero() {
		t.Errorf("malformed entry changed container B: %+v", o)
	}
}

const defaultTailForTest = 200

func TestResumeIgnoresHugeOrdinals(t *testing.T) {
	fake := cursorFake()
	env, cookie := newObserveEnv(t, fake)
	hdr := cursorA + ":5:1000000001," + cursorB + ":5:9223372036854775807"
	_, ch := openSSEResume(t, env, cookie, context.Background(), "/api/stacks/web/logs", hdr)
	drain(t, ch)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, full := range []string{cursorAFull, cursorBFull} {
		if o := fake.logOpts[full]; o.Tail != defaultTailForTest || !o.Since.IsZero() {
			t.Errorf("oversized ordinal was honoured for %s: %+v", full, o)
		}
	}
}

func TestResumeAcceptsOrdinalAtCap(t *testing.T) {
	fake := cursorFake()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSEResume(t, env, cookie, context.Background(), "/api/stacks/web/logs", cursorA+":5:1000000000")
	drain(t, ch)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if o := fake.logOpts[cursorAFull]; o.Tail != -1 {
		t.Errorf("ordinal at the cap was ignored: %+v", o)
	}
}

func TestResumeOrdinalContinues(t *testing.T) {
	fake := cursorFake()
	fake.logs[cursorAFull] = fakeLogSpec{lines: []engine.LogLine{
		{Stream: "stdout", Time: ts2, Text: "two"},
		{Stream: "stderr", Time: ts2, Text: "three"},
		{Stream: "stderr", Time: ts2, Text: "four"},
	}}
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSEResume(t, env, cookie, context.Background(), "/api/stacks/web/logs?service=app", cursorID(cursorA, ts2, 1))
	var ids []string
	for _, ev := range drain(t, ch) {
		if ev.name == "log" {
			ids = append(ids, ev.id)
		}
	}
	want := []string{cursorID(cursorA, ts2, 2), cursorID(cursorA, ts2, 3)}
	if !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}

func TestResumeNeverTruncatesWithTail(t *testing.T) {
	fake := cursorFake()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSEResume(t, env, cookie, context.Background(), "/api/stacks/web/logs?service=app&tail=1&since=10m", cursorID(cursorA, ts1, 1))
	drain(t, ch)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if o := fake.logOpts[cursorAFull]; o.Tail != -1 || !o.Since.Equal(ts1) {
		t.Errorf("opts = %+v, want Tail=-1 and the cursor's Since", o)
	}
}

func TestResumeStopsWithRequestContext(t *testing.T) {
	fake := cursorFake()
	env, cookie := newObserveEnv(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	_, ch := openSSEResume(t, env, cookie, ctx, "/api/stacks/web/logs?follow=true", cursorID(cursorA, ts2, 1))
	for ev := range ch { // wait for the first log so the streams are open
		if ev.name == "log" {
			break
		}
	}
	cancel()
	select {
	case <-fake.logCtx(cursorAFull).Done():
	case <-time.After(5 * time.Second):
		t.Fatal("resumed docker stream outlived the request")
	}
}

func TestMetricsTokenStatus(t *testing.T) {
	fake := newFakeObserve()
	env, cookie := newObserveEnv(t, fake)
	if rec := env.do(t, "GET", "/api/settings/metrics-token", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session = %d, want 401", rec.Code)
	}
	configured := func() bool {
		t.Helper()
		rec := env.do(t, "GET", "/api/settings/metrics-token", "", withCookie(cookie))
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status = %d, cache-control %q", rec.Code, rec.Header().Get("Cache-Control"))
		}
		var body map[string]bool
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body) != 1 {
			t.Fatalf("body %s, err %v", rec.Body, err)
		}
		return body["configured"]
	}
	if configured() {
		t.Error("configured with no hash")
	}
	createToken(t, env, cookie)
	if !configured() {
		t.Error("not configured after create")
	}
	if rec := env.do(t, "DELETE", "/api/settings/metrics-token", "", withCookie(cookie)); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	if configured() {
		t.Error("configured after delete")
	}
}

// A stack without a compose project must not stream other containers' samples:
// non-compose samples carry project "".
func TestLiveMetricsEmptyProjectStreamsNoSamples(t *testing.T) {
	fake := newFakeObserve()
	st := newFakeStacks()
	st.summary.Project = ""
	env, cookie := newObserveEnvWith(t, fake, st)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, ch := openSSE(t, env, cookie, ctx, "/api/stacks/web/metrics/live")
	fake.samples <- metrics.Sample{TS: t0, Project: "", Container: "plain", CPU: 1}
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("stream ended; want it kept open with heartbeats")
			}
			if ev.name == "sample" {
				t.Fatalf("empty-project stack streamed a sample: %s", ev.data)
			}
		case <-deadline:
			return
		}
	}
}

// ---- follow mode: re-attach after restart or recreation ----

// eventually polls cond for up to 3 seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// nextLogEvent returns the next "log" event, failing on "end" or "error".
func nextLogEvent(t *testing.T, ch <-chan sseEvent) sseEvent {
	t.Helper()
	ev := nextEvent(t, ch, true)
	if ev.name != "log" {
		t.Fatalf("next event = %s %s, want a log event", ev.name, ev.data)
	}
	return ev
}

func readLines(t *testing.T, ch <-chan sseEvent, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		nextLogEvent(t, ch)
	}
}

// expectEnd waits for "end" and then for the response body to close.
func expectEnd(t *testing.T, ch <-chan sseEvent) {
	t.Helper()
	if ev := nextEvent(t, ch, true); ev.name != "end" {
		t.Fatalf("next event = %s %s, want end", ev.name, ev.data)
	}
	if rest := drain(t, ch); len(rest) != 0 {
		t.Fatalf("events after end: %v", rest)
	}
}

// expectQuiet fails when a non-ping event arrives within d.
func expectQuiet(t *testing.T, ch <-chan sseEvent, d time.Duration) {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("stream ended unexpectedly")
			}
			if ev.name != "ping" {
				t.Fatalf("unexpected event %s %s", ev.name, ev.data)
			}
		case <-timeout:
			return
		}
	}
}

func TestLogsFollowReattachesAfterRestart(t *testing.T) {
	fake := twoContainerFake()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?follow=1&tail=200")
	readLines(t, ch, 3)

	fake.setLogs("c-app", fakeLogSpec{lines: []engine.LogLine{
		{Stream: "stdout", Time: t0, Text: "app one"}, {Stream: "stderr", Time: t0, Text: "app two"},
		{Stream: "stdout", Time: t0.Add(time.Second), Text: "app three"}}})
	fake.endLogs("c-app")
	fake.pushChange(state.Change{Project: "web"})

	ev := nextLogEvent(t, ch)
	var le logEvent
	if err := json.Unmarshal([]byte(ev.data), &le); err != nil {
		t.Fatal(err)
	}
	if le.Text != "app three" {
		t.Fatalf("next log = %q, want app three (nothing repeated)", le.Text)
	}
	if want := cursorID("c-app", t0.Add(time.Second), 1); ev.id != want {
		t.Errorf("id = %q, want %q", ev.id, want)
	}
	eventually(t, "two c-app log calls", func() bool { return fake.logCallsFor("c-app") == 2 })
	if n := fake.logCallsFor("c-app"); n != 2 {
		t.Errorf("c-app log calls = %d, want 2", n)
	}
	if o := fake.logCall("c-app", 1); o.Tail != -1 || !o.Since.Equal(t0) || !o.Follow {
		t.Errorf("second call opts = %+v, want tail -1, since t0, follow", o)
	}
	expectQuiet(t, ch, 100*time.Millisecond) // no end, nothing repeated
}

func TestLogsFollowAttachesARecreatedContainer(t *testing.T) {
	fake := twoContainerFake()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?follow=1&tail=200")
	readLines(t, ch, 3)

	fake.setContainers([]engine.Container{
		container("c-app2", "web-app-1", "web", "app"),
		container("c-db", "web-db-1", "web", "db"),
		container("c-other", "other-app-1", "other", "app"),
	})
	fake.setLogs("c-app2", fakeLogSpec{lines: []engine.LogLine{{Stream: "stdout", Time: t0.Add(2 * time.Second), Text: "app fresh"}}})
	fake.endLogs("c-app")
	fake.pushChange(state.Change{Project: "web"})

	ev := nextLogEvent(t, ch)
	var le logEvent
	if err := json.Unmarshal([]byte(ev.data), &le); err != nil {
		t.Fatal(err)
	}
	if le.Text != "app fresh" || le.Container != "web-app-1" {
		t.Errorf("log = %+v, want app fresh from web-app-1", le)
	}
	if want := cursorID("c-app2", t0.Add(2*time.Second), 1); ev.id != want {
		t.Errorf("id = %q, want %q", ev.id, want)
	}
	if o := fake.logCall("c-app2", 0); o.Tail != 200 || !o.Follow {
		t.Errorf("new container opts = %+v, want the request's tail 200 and follow", o)
	}
	if n := fake.logCallsFor("c-app"); n != 1 {
		t.Errorf("the removed container was read %d times, want 1", n)
	}
	expectQuiet(t, ch, 100*time.Millisecond)
}

func TestLogsFollowServiceFilterAppliesToReattach(t *testing.T) {
	fake := twoContainerFake()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?follow=1&service=app")
	readLines(t, ch, 2)

	fake.setContainers(append(append([]engine.Container{}, fake.containers...), container("c-db2", "web-db-2", "web", "db")))
	fake.setLogs("c-db2", fakeLogSpec{lines: []engine.LogLine{{Stream: "stdout", Time: t0, Text: "db replica"}}})
	fake.pushChange(state.Change{Project: "web"})
	// A second change after the first reconcile proves the first was handled.
	fake.pushChange(state.Change{Project: "web"})
	eventually(t, "both changes reconciled", func() bool { return fake.containersCalls() >= 4 })
	expectQuiet(t, ch, 100*time.Millisecond)
	if n := fake.logCallsFor("c-db2"); n != 0 {
		t.Errorf("a db replica was read %d times, want 0", n)
	}
	if n := fake.logCallsFor("c-db"); n != 0 {
		t.Errorf("db was read %d times, want 0", n)
	}
}

func TestLogsFollowEndsWhenTheStackHasNoContainersLeft(t *testing.T) {
	fake := twoContainerFake()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?follow=1")
	readLines(t, ch, 3)

	// Both containers are removed: the server closes their streams itself.
	fake.setContainers([]engine.Container{container("c-other", "other-app-1", "other", "app")})
	fake.pushChange(state.Change{Project: "web"})
	expectEnd(t, ch)
	eventually(t, "every stream closed", func() bool { return fake.openStreams() == 0 })
}

func TestLogsFollowStaysOpenWhileAContainerRemains(t *testing.T) {
	fake := twoContainerFake()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?follow=1")
	readLines(t, ch, 3)

	// c-db is removed: its stream is closed, c-app is still being followed.
	fake.setContainers([]engine.Container{container("c-app", "web-app-1", "web", "app")})
	fake.pushChange(state.Change{Project: "web"})
	eventually(t, "the removed container's stream closed", func() bool { return fake.openStreams() == 1 })
	expectQuiet(t, ch, 150*time.Millisecond)
}

func TestLogsFollowPumpEndWithoutAChangeIsNotRetriedForever(t *testing.T) {
	fake := twoContainerFake()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?follow=1&service=app")
	readLines(t, ch, 2)

	// The listing still says running but the stream is gone: the retry ends at once.
	fake.setLogs("c-app", fakeLogSpec{endsAtOnce: true})
	fake.endLogs("c-app")
	time.Sleep(300 * time.Millisecond)
	if n := fake.logCallsFor("c-app"); n != 2 {
		t.Fatalf("c-app log calls = %d, want exactly 2 (one retry)", n)
	}
	expectQuiet(t, ch, 10*time.Millisecond)

	fake.setLogs("c-app", fakeLogSpec{})
	fake.pushChange(state.Change{Project: "web"})
	eventually(t, "a third c-app log call after the change", func() bool { return fake.logCallsFor("c-app") == 3 })
}

func TestLogsFollowDroppedChangeSubscriptionResubscribes(t *testing.T) {
	fake := twoContainerFake()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?follow=1")
	readLines(t, ch, 3)
	eventually(t, "the first subscription", func() bool { return fake.changeSubscriptions() == 1 })
	before := fake.containersCalls()

	fake.dropChanges()
	eventually(t, "a second subscription", func() bool { return fake.changeSubscriptions() == 2 })
	eventually(t, "a reconcile after the drop", func() bool { return fake.containersCalls() > before })

	// The fresh subscription works.
	fake.setContainers([]engine.Container{container("c-other", "other-app-1", "other", "app")})
	fake.pushChange(state.Change{Project: "web"})
	expectEnd(t, ch)
}

func TestLogsFollowIgnoresOtherProjectsChanges(t *testing.T) {
	fake := twoContainerFake()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?follow=1")
	readLines(t, ch, 3)
	eventually(t, "the subscription", func() bool { return fake.changeSubscriptions() == 1 })
	before := fake.containersCalls()
	fake.pushChange(state.Change{Project: "other"})
	fake.pushChange(state.Change{Project: ""}) // a connection change reconciles
	eventually(t, "the empty-project reconcile", func() bool { return fake.containersCalls() == before+1 })
	time.Sleep(50 * time.Millisecond)
	if n := fake.containersCalls(); n != before+1 {
		t.Errorf("Containers calls = %d, want %d (the other project's change is ignored)", n, before+1)
	}
}

func TestLogsFollowReleasesEveryStream(t *testing.T) {
	fake := twoContainerFake()
	env, cookie := newObserveEnv(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, ch := openSSE(t, env, cookie, ctx, "/api/stacks/web/logs?follow=1")
	readLines(t, ch, 3)

	// Several restarts and a recreation, then a disconnect: nothing stays open.
	for i := 0; i < 3; i++ {
		fake.endLogs("c-app")
		fake.pushChange(state.Change{Project: "web"})
		want := i + 2
		eventually(t, "a re-attach", func() bool { return fake.logCallsFor("c-app") == want })
	}
	if n := fake.openStreams(); n != 2 {
		t.Fatalf("open streams = %d, want 2 (one per container, none leaked by re-attach)", n)
	}
	cancel()
	eventually(t, "every stream closed", func() bool { return fake.openStreams() == 0 })
	eventually(t, "the change subscription cancelled", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.changeCancels >= 1
	})
}

func TestLogsFollowStreamErrorIsReportedAndTheStreamStaysAttached(t *testing.T) {
	fake := newFakeObserve()
	fake.containers = []engine.Container{container("c-app", "web-app-1", "web", "app")}
	fake.logs["c-app"] = fakeLogSpec{
		lines: []engine.LogLine{{Stream: "stdout", Time: t0, Text: "before crash"}},
		err:   fmt.Errorf("%w: connection reset", engine.ErrUnavailable),
	}
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?follow=1")
	var names []string
	for len(names) < 3 { // log, error, then the one retry's error
		names = append(names, nextEvent(t, ch, true).name)
	}
	if got, want := strings.Join(names, ","), "log,error,error"; got != want {
		t.Fatalf("events = %s, want %s", got, want)
	}
	// The container is still listed, so the stream stays open until it is removed.
	fake.setContainers(nil)
	fake.pushChange(state.Change{Project: "web"})
	expectEnd(t, ch)
}

func TestContainerLogsFollowReattachesSameID(t *testing.T) {
	obs := containerLogsObserve()
	env, _ := containersEnv(t, obs, nil)
	_, ch := openSSE(t, env, env.login(t), context.Background(), "/api/containers/"+idWeb[:12]+"/logs?follow=1")
	readLines(t, ch, 2)

	obs.setLogs(idWeb, fakeLogSpec{lines: []engine.LogLine{
		{Stream: "stdout", Time: t0, Text: "web one"}, {Stream: "stdout", Time: t0.Add(time.Second), Text: "web two"},
		{Stream: "stdout", Time: t0.Add(2 * time.Second), Text: "web three"}}})
	obs.endLogs(idWeb)
	obs.pushChange(state.Change{Project: "web"})
	ev := nextLogEvent(t, ch)
	var le logEvent
	if err := json.Unmarshal([]byte(ev.data), &le); err != nil {
		t.Fatal(err)
	}
	if le.Text != "web three" {
		t.Errorf("next log = %q, want web three", le.Text)
	}
	if o := obs.logCall(idWeb, 1); o.Tail != -1 || !o.Since.Equal(t0.Add(time.Second)) || !o.Follow {
		t.Errorf("second call opts = %+v, want tail -1, since t0+1s, follow", o)
	}
}

func TestContainerLogsFollowEndsWhenRemoved(t *testing.T) {
	obs := containerLogsObserve()
	env, _ := containersEnv(t, obs, nil)
	_, ch := openSSE(t, env, env.login(t), context.Background(), "/api/containers/"+idWeb[:12]+"/logs?follow=1")
	readLines(t, ch, 2)

	var rest []engine.Container
	for _, c := range obs.containers {
		if c.ID != idWeb {
			rest = append(rest, c)
		}
	}
	obs.setContainers(rest)
	obs.pushChange(state.Change{Project: "web"})
	expectEnd(t, ch)
	eventually(t, "the stream closed", func() bool { return obs.openStreams() == 0 })
}

// ---- follow mode: fix round 1 ----

func oneContainerFake(lines ...engine.LogLine) *fakeObserve {
	f := newFakeObserve()
	f.containers = []engine.Container{container("c-app", "web-app-1", "web", "app")}
	f.logs["c-app"] = fakeLogSpec{lines: lines}
	return f
}

func logTexts(t *testing.T, evs ...sseEvent) []string {
	t.Helper()
	var out []string
	for _, le := range logsOf(t, evs) {
		out = append(out, le.Text)
	}
	return out
}

func TestLogsFollowReattachWithoutALineYetStartsFromTheFirstAttach(t *testing.T) {
	fake := oneContainerFake() // tail=0 and an empty log: nothing delivered yet, so no cursor
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?follow=1&tail=0")
	eventually(t, "the first attach", func() bool { return fake.logCallsFor("c-app") == 1 })

	// Written right after the restart, before the re-attach.
	fresh := time.Now().Add(time.Second)
	fake.setLogs("c-app", fakeLogSpec{lines: []engine.LogLine{{Stream: "stdout", Time: fresh, Text: "after restart"}}})
	fake.endLogs("c-app")
	fake.pushChange(state.Change{Project: "web"})

	ev := nextLogEvent(t, ch)
	if got := logTexts(t, ev); len(got) != 1 || got[0] != "after restart" {
		t.Fatalf("log = %v, want after restart", got)
	}
	if o := fake.logCall("c-app", 1); o.Tail != -1 || o.Since.IsZero() || o.Since.After(fresh) {
		t.Errorf("re-attach opts = %+v, want tail -1 and since the first attach", o)
	}
	expectQuiet(t, ch, 100*time.Millisecond) // exactly once
}

func TestLogsFollowKeepsTheCursorOfAContainerThatLeavesTheListingForAWhile(t *testing.T) {
	fake := twoContainerFake()
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?follow=1&tail=200")
	readLines(t, ch, 3)

	all := append([]engine.Container{}, fake.containers...)
	fake.setContainers([]engine.Container{all[1], all[2]}) // c-app drops out
	fake.pushChange(state.Change{Project: "web"})
	eventually(t, "c-app's stream closed", func() bool { return fake.openStreams() == 1 })
	// Another change while it is absent: the cursor must survive it.
	time.Sleep(50 * time.Millisecond) // let the first change settle
	before := fake.containersCalls()
	fake.pushChange(state.Change{Project: "web"})
	eventually(t, "that change reconciled", func() bool { return fake.containersCalls() > before })

	fake.setLogs("c-app", fakeLogSpec{lines: []engine.LogLine{
		{Stream: "stdout", Time: t0, Text: "app one"}, {Stream: "stderr", Time: t0, Text: "app two"},
		{Stream: "stdout", Time: t0.Add(time.Second), Text: "app later"}}})
	fake.setContainers(all)
	fake.pushChange(state.Change{Project: "web"})
	ev := nextLogEvent(t, ch)
	if got := logTexts(t, ev); len(got) != 1 || got[0] != "app later" {
		t.Fatalf("log = %v, want only app later (no replay from tail)", got)
	}
	if o := fake.logCall("c-app", 1); o.Tail != -1 || !o.Since.Equal(t0) {
		t.Errorf("re-attach opts = %+v, want tail -1, since t0", o)
	}
	expectQuiet(t, ch, 100*time.Millisecond)
}

func TestLogsFollowAttachesAContainerCreatedBeforeTheSubscription(t *testing.T) {
	fake := oneContainerFake()
	fake.setLogs("c-new", fakeLogSpec{lines: []engine.LogLine{{Stream: "stdout", Time: t0, Text: "new replica"}}})
	// The replica appears after the handler's snapshot, before the subscription is live.
	fake.onSubscribe = func() {
		fake.setContainers([]engine.Container{
			container("c-app", "web-app-1", "web", "app"), container("c-new", "web-app-2", "web", "app")})
	}
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/stacks/web/logs?follow=1")
	ev := nextLogEvent(t, ch)
	if got := logTexts(t, ev); len(got) != 1 || got[0] != "new replica" {
		t.Fatalf("log = %v, want new replica without any change event", got)
	}
}

func TestContainerLogsFollowReattachesAContainerWithoutAProject(t *testing.T) {
	// The cache publishes no change for a container outside any compose project.
	fake := newFakeObserve()
	fake.containers = []engine.Container{{ID: "dddddddddddd", Name: "solo-1", State: "running"}}
	fake.logs["dddddddddddd"] = fakeLogSpec{lines: []engine.LogLine{{Stream: "stdout", Time: t0, Text: "solo one"}}}
	env, cookie := newObserveEnv(t, fake)
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/containers/dddddddddddd/logs?follow=1")
	readLines(t, ch, 1)

	fake.setLogs("dddddddddddd", fakeLogSpec{lines: []engine.LogLine{
		{Stream: "stdout", Time: t0, Text: "solo one"}, {Stream: "stdout", Time: t0.Add(time.Second), Text: "solo two"}}})
	fake.endLogs("dddddddddddd")
	ev := nextLogEvent(t, ch) // no pushChange
	if got := logTexts(t, ev); len(got) != 1 || got[0] != "solo two" {
		t.Fatalf("log = %v, want solo two", got)
	}
	// Removal ends the stream, again without a change event.
	fake.setContainers(nil)
	expectEnd(t, ch)
	eventually(t, "the stream closed", func() bool { return fake.openStreams() == 0 })
}

// collectEvents returns the non-ping events that arrive within d.
func collectEvents(t *testing.T, ch <-chan sseEvent, d time.Duration) []sseEvent {
	t.Helper()
	var out []sseEvent
	timeout := time.After(d)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("stream ended unexpectedly")
			}
			if ev.name != "ping" {
				out = append(out, ev)
			}
		case <-timeout:
			return out
		}
	}
}

// ---- follow mode: fix round 2 ----

func TestContainerLogsFollowProjectlessFailingStreamIsNotRetriedByThePoll(t *testing.T) {
	fake := newFakeObserve()
	fake.containers = []engine.Container{{ID: "dddddddddddd", Name: "solo-1", State: "running"}}
	fake.logs["dddddddddddd"] = fakeLogSpec{err: fmt.Errorf("%w: no log driver", engine.ErrUnavailable)}
	env, cookie := newObserveEnv(t, fake) // the 20 ms heartbeat is also the poll interval
	_, ch := openSSE(t, env, cookie, context.Background(), "/api/containers/dddddddddddd/logs?follow=1")

	settled := collectEvents(t, ch, 200*time.Millisecond)
	calls := fake.logCallsFor("dddddddddddd")
	if calls > 2 || len(settled) > 2 {
		t.Fatalf("after 200 ms: %d Logs calls and %d events, want at most 2 each", calls, len(settled))
	}
	more := collectEvents(t, ch, 300*time.Millisecond) // many poll ticks
	if len(more) != 0 || fake.logCallsFor("dddddddddddd") != calls {
		t.Fatalf("poll kept retrying: %d more events, Logs calls %d -> %d", len(more), calls, fake.logCallsFor("dddddddddddd"))
	}

	// A restart the listing shows (exited, then running again) is attached anew.
	fake.setLogs("dddddddddddd", fakeLogSpec{lines: []engine.LogLine{{Stream: "stdout", Time: time.Now().Add(time.Second), Text: "back"}}})
	fake.setContainers([]engine.Container{{ID: "dddddddddddd", Name: "solo-1", State: "exited"}})
	before := fake.containersCalls()
	eventually(t, "the poll to see exited", func() bool { return fake.containersCalls() > before+1 })
	fake.setContainers([]engine.Container{{ID: "dddddddddddd", Name: "solo-1", State: "running"}})
	ev := nextLogEvent(t, ch)
	if got := logTexts(t, ev); len(got) != 1 || got[0] != "back" {
		t.Fatalf("log = %v, want back", got)
	}
	if n := fake.logCallsFor("dddddddddddd"); n != calls+1 {
		t.Errorf("Logs calls = %d, want %d (one attempt for the restart)", n, calls+1)
	}
}

// ---- follow mode: final review F1 ----

// A quick `docker restart` of a container outside any compose project: the
// re-attach that follows the stream's end can land while the container is
// stopped, so it gets an empty, error-free stream, and the listing (running
// before and after) shows nothing. The poll must still re-attach once the
// container is back, and every line must arrive exactly once.
func TestContainerLogsFollowProjectlessResumesAfterQuickRestarts(t *testing.T) {
	const id = "dddddddddddd"
	fake := newFakeObserve()
	fake.containers = []engine.Container{{ID: id, Name: "solo-1", State: "running"}}
	history := []engine.LogLine{{Stream: "stdout", Time: t0, Text: "boot 0"}}
	fake.logs[id] = fakeLogSpec{lines: history}
	env, cookie := newObserveEnv(t, fake) // the 20 ms heartbeat is also the poll interval
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, ch := openSSE(t, env, cookie, ctx, "/api/containers/"+id+"/logs?follow=1")
	readLines(t, ch, 1)

	for restart := 1; restart <= 5; restart++ {
		// Stopped: a stream attached now closes at once with nothing new.
		fake.setLogs(id, fakeLogSpec{lines: history, endsAtOnce: true})
		calls := fake.logCallsFor(id)
		fake.endLogs(id)
		eventually(t, "the re-attach while stopped", func() bool { return fake.logCallsFor(id) > calls })

		// Running again, with one new line.
		line := engine.LogLine{Stream: "stdout", Time: t0.Add(time.Duration(restart) * time.Second), Text: fmt.Sprintf("boot %d", restart)}
		history = append(append([]engine.LogLine(nil), history...), line)
		fake.setLogs(id, fakeLogSpec{lines: history})
		// nextEvent's own timeout restarts on every ping, so bound the wait here.
		evs := collectEvents(t, ch, time.Second)
		if got := logTexts(t, evs...); len(got) != 1 || got[0] != line.Text {
			t.Fatalf("restart %d: logs within 1 s = %v, want exactly [%s]", restart, got, line.Text)
		}
	}
}

// The poll retries an empty, error-free end only a few times: a container the
// listing calls running whose stream keeps closing at once is not attached on
// every tick for ever.
func TestContainerLogsFollowProjectlessEmptyStreamRetriesAreCapped(t *testing.T) {
	const id = "dddddddddddd"
	fake := newFakeObserve()
	fake.containers = []engine.Container{{ID: id, Name: "solo-1", State: "running"}}
	fake.logs[id] = fakeLogSpec{endsAtOnce: true}
	env, cookie := newObserveEnv(t, fake) // the 20 ms heartbeat is also the poll interval
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, ch := openSSE(t, env, cookie, ctx, "/api/containers/"+id+"/logs?follow=1")

	if evs := collectEvents(t, ch, 400*time.Millisecond); len(evs) != 0 {
		t.Fatalf("events = %d, want none", len(evs))
	}
	calls := fake.logCallsFor(id)
	if calls < 3 || calls > 7 { // the first attach, its retry, then at most 5 poll retries
		t.Fatalf("Logs calls = %d, want 3..7", calls)
	}
	expectQuiet(t, ch, 200*time.Millisecond)
	if n := fake.logCallsFor(id); n != calls {
		t.Fatalf("Logs calls %d -> %d, want the poll to stop retrying", calls, n)
	}
}
