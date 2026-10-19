package history

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/state"
)

func TestDeriveState(t *testing.T) {
	c137 := 137
	cases := []struct {
		name, state, status, want string
		code                      *int
	}{
		{"healthy", "running", "Up 5s (healthy)", StateHealthy, nil},
		{"unhealthy", "running", "Up 5s (unhealthy)", StateUnhealthy, nil},
		{"running", "running", "Up", StateRunning, nil},
		{"starting", "running", "Up 1s (health: starting)", StateStarting, nil},
		{"restarting", "restarting", "Restarting (1) 2s ago", StateRestarting, nil},
		{"exited code", "exited", "Exited (137) 2s ago", StateExited, &c137},
		{"exited no code", "exited", "Exited", StateExited, nil},
		{"created", "created", "Created", StateStarting, nil},
		{"paused", "paused", "Up 1m (Paused)", StateRunning, nil},
		{"dead", "dead", "Dead", StateExited, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, code := DeriveState(engine.Container{State: tc.state, Status: tc.status})
			if got != tc.want {
				t.Fatalf("state = %q, want %q", got, tc.want)
			}
			if (code == nil) != (tc.code == nil) || (code != nil && *code != *tc.code) {
				t.Fatalf("code = %v, want %v", code, tc.code)
			}
		})
	}
}

// fakeSource is a Source driven by the test through channels.
type fakeSource struct {
	mu            sync.Mutex
	containers    []engine.Container
	err           error
	connected     bool
	changes       chan state.Change
	events        chan engine.Event
	subscribes    chan struct{} // signalled per Subscribe
	evSubscribes  chan struct{}
	cancelChanges chan struct{}
	cancelEvents  chan struct{}
}

func newFakeSource() *fakeSource {
	return &fakeSource{connected: true,
		changes: make(chan state.Change, 16), events: make(chan engine.Event, 16),
		subscribes: make(chan struct{}, 16), evSubscribes: make(chan struct{}, 16),
		cancelChanges: make(chan struct{}, 16), cancelEvents: make(chan struct{}, 16)}
}

func (f *fakeSource) set(cs ...engine.Container) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers = cs
}

func (f *fakeSource) Containers(context.Context) ([]engine.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]engine.Container(nil), f.containers...), f.err
}

func (f *fakeSource) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

func (f *fakeSource) setConnected(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = v
}

func (f *fakeSource) Subscribe() (<-chan state.Change, func()) {
	f.mu.Lock()
	ch := f.changes
	f.mu.Unlock()
	f.subscribes <- struct{}{}
	return ch, func() { f.cancelChanges <- struct{}{} }
}

func (f *fakeSource) SubscribeEvents() (<-chan engine.Event, func()) {
	f.mu.Lock()
	ch := f.events
	f.mu.Unlock()
	f.evSubscribes <- struct{}{}
	return ch, func() { f.cancelEvents <- struct{}{} }
}

// fakeWriter records every Insert call; each call is also sent on inserted.
type fakeWriter struct {
	mu       sync.Mutex
	calls    [][]Row
	fail     bool
	attempts chan []Row // every Insert call, successful or not
}

func newFakeWriter() *fakeWriter { return &fakeWriter{attempts: make(chan []Row, 64)} }

func (w *fakeWriter) Insert(_ context.Context, rows []Row) error {
	w.mu.Lock()
	fail := w.fail
	if !fail {
		w.calls = append(w.calls, rows)
	}
	w.mu.Unlock()
	w.attempts <- rows
	if fail {
		return errors.New("disk full")
	}
	return nil
}

func (w *fakeWriter) setFail(v bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.fail = v
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func ctr(id, project, service, st, status string) engine.Container {
	labels := map[string]string{}
	if project != "" {
		labels[labelComposeProject] = project
		labels[labelComposeService] = service
	}
	return engine.Container{ID: id, Name: project + "-" + service + "-1", State: st, Status: status, Labels: labels}
}

type harness struct {
	src    *fakeSource
	w      *fakeWriter
	clock  *fakeClock
	logs   *syncBuffer
	cancel context.CancelFunc
	done   chan struct{}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// start runs a recorder against the initial containers and waits for its
// startup baseline (an insert if any compose container exists).
func start(t *testing.T, cs ...engine.Container) *harness {
	t.Helper()
	h := &harness{src: newFakeSource(), w: newFakeWriter(), logs: &syncBuffer{},
		clock: &fakeClock{t: time.Unix(1_700_000_000, 0)}, done: make(chan struct{})}
	h.src.set(cs...)
	rec := NewRecorder(h.src, h.w, slog.New(slog.NewTextHandler(h.logs, nil)), h.clock.now)
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { rec.Run(ctx); close(h.done) }()
	t.Cleanup(func() { cancel(); <-h.done })
	<-h.src.subscribes
	<-h.src.evSubscribes
	for _, c := range cs {
		if c.Project() != "" {
			h.next(t) // startup baseline
			break
		}
	}
	return h
}

// next waits for the next Insert attempt.
func (h *harness) next(t *testing.T) []Row {
	t.Helper()
	select {
	case rows := <-h.w.attempts:
		return rows
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an insert")
		return nil
	}
}

// sync_ proves the recorder has processed everything sent before it by
// sending a change for an unknown project and waiting for it to be consumed.
func (h *harness) drain(t *testing.T) {
	t.Helper()
	h.src.changes <- state.Change{Project: "sync-marker"}
	for len(h.src.changes) > 0 {
		time.Sleep(time.Millisecond)
	}
	// the marker is consumed; the recorder then handles it before reading the next select
	h.src.changes <- state.Change{Project: "sync-marker"}
	for len(h.src.changes) > 0 {
		time.Sleep(time.Millisecond)
	}
}

func (h *harness) noInsert(t *testing.T) {
	t.Helper()
	h.drain(t)
	select {
	case rows := <-h.w.attempts:
		t.Fatalf("unexpected insert: %+v", rows)
	default:
	}
}

func TestRecorder_BaselineOnConnect(t *testing.T) {
	h := &harness{src: newFakeSource(), w: newFakeWriter(), logs: &syncBuffer{},
		clock: &fakeClock{t: time.Unix(1_700_000_000, 0)}, done: make(chan struct{})}
	h.src.connected = false
	h.src.set(ctr("a", "app", "web", "running", "Up"), ctr("b", "app", "db", "running", "Up (healthy)"),
		ctr("c", "", "loose", "running", "Up"))
	rec := NewRecorder(h.src, h.w, nil, h.clock.now)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { rec.Run(ctx); close(h.done) }()
	t.Cleanup(func() { cancel(); <-h.done })
	<-h.src.subscribes
	<-h.src.evSubscribes

	h.src.setConnected(true)
	h.src.changes <- state.Change{}
	rows := h.next(t)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2", rows)
	}
	for _, r := range rows {
		if !r.TS.Equal(h.clock.now()) || r.Project != "app" {
			t.Fatalf("row = %+v", r)
		}
	}
	if rows[0].State != StateRunning || rows[1].State != StateHealthy {
		t.Fatalf("states = %q %q", rows[0].State, rows[1].State)
	}
}

func TestRecorder_DedupesUnchangedState(t *testing.T) {
	h := start(t, ctr("a", "app", "web", "running", "Up 5s (healthy)"))
	h.src.changes <- state.Change{Project: "app"}
	h.noInsert(t)
	h.src.set(ctr("a", "app", "web", "running", "Up 6m (healthy)"))
	h.src.changes <- state.Change{Project: "app"}
	h.noInsert(t)
}

func TestRecorder_WritesTransitionAndRemoved(t *testing.T) {
	h := start(t, ctr("a", "app", "web", "running", "Up 5s (healthy)"))

	h.src.set(ctr("a", "app", "web", "running", "Up 5s (unhealthy)"))
	h.src.changes <- state.Change{Project: "app"}
	if rows := h.next(t); len(rows) != 1 || rows[0].State != StateUnhealthy || rows[0].Service != "web" {
		t.Fatalf("rows = %+v", rows)
	}

	h.src.set()
	h.src.changes <- state.Change{Project: "app"}
	if rows := h.next(t); len(rows) != 1 || rows[0].State != StateRemoved || rows[0].Container != "app-web-1" {
		t.Fatalf("rows = %+v", rows)
	}

	h.src.set(ctr("z", "app", "web", "running", "Up"))
	h.src.changes <- state.Change{Project: "app"}
	if rows := h.next(t); len(rows) != 1 || rows[0].State != StateRunning {
		t.Fatalf("rows = %+v", rows)
	}
	h.src.changes <- state.Change{Project: "app"}
	h.noInsert(t) // the removed id was forgotten, not rewritten
}

func TestRecorder_ReconnectRewritesBaseline(t *testing.T) {
	h := start(t, ctr("a", "app", "web", "running", "Up"), ctr("b", "app", "db", "running", "Up"))
	h.src.changes <- state.Change{}
	if rows := h.next(t); len(rows) != 2 {
		t.Fatalf("rows = %+v, want both containers again", rows)
	}
	h.src.setConnected(false)
	h.src.changes <- state.Change{}
	h.noInsert(t)
}

func TestRecorder_ReconnectWritesRemovedForVanished(t *testing.T) {
	h := start(t, ctr("a", "app", "web", "running", "Up"), ctr("b", "app", "db", "running", "Up"))
	h.src.set(ctr("a", "app", "web", "running", "Up"))
	h.src.changes <- state.Change{}
	rows := h.next(t)
	if len(rows) != 2 || rows[0].State != StateRunning || rows[1].State != StateRemoved || rows[1].Service != "db" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRecorder_ResubscribesAfterDrop(t *testing.T) {
	h := start(t, ctr("a", "app", "web", "running", "Up"))

	old := h.src.changes
	h.src.mu.Lock()
	h.src.changes = make(chan state.Change, 16)
	h.src.mu.Unlock()
	close(old)
	<-h.src.subscribes
	if rows := h.next(t); len(rows) != 1 {
		t.Fatalf("baseline after drop = %+v", rows)
	}

	oldEv := h.src.events
	h.src.mu.Lock()
	h.src.events = make(chan engine.Event, 16)
	h.src.mu.Unlock()
	close(oldEv)
	<-h.src.evSubscribes
	h.noInsert(t)
}

func dieStart(id string, project string) []engine.Event {
	labels := map[string]string{}
	if project != "" {
		labels = map[string]string{labelComposeProject: project, labelComposeService: "web"}
	}
	ev := func(a string) engine.Event {
		return engine.Event{Type: "container", Action: a, ContainerID: id, Name: "app-web-1", Labels: labels}
	}
	return []engine.Event{ev("die"), ev("start")}
}

func TestRecorder_CountsDieStartPairsOnce(t *testing.T) {
	ev := func(a string) engine.Event {
		e := dieStart("a", "app")[0]
		e.Action = a
		return e
	}
	cases := []struct {
		name    string
		actions []string
		want    int
	}{
		{"die start", []string{"die", "start"}, 1},
		{"kill die start restart", []string{"kill", "die", "start", "restart"}, 1},
		{"create start", []string{"create", "start"}, 0},
		{"start alone", []string{"start"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := start(t, ctr("a", "app", "web", "running", "Up"))
			for _, a := range tc.actions {
				h.src.events <- ev(a)
			}
			if tc.want == 0 {
				h.noInsert(t)
				return
			}
			rows := h.next(t)
			if len(rows) != 1 || rows[0].State != StateRestart || rows[0].Project != "app" ||
				rows[0].Service != "web" || rows[0].Container != "app-web-1" {
				t.Fatalf("rows = %+v", rows)
			}
			h.noInsert(t)
		})
	}

	t.Run("two pairs a second apart", func(t *testing.T) {
		h := start(t, ctr("a", "app", "web", "running", "Up"))
		for _, e := range dieStart("a", "app") {
			h.src.events <- e
		}
		h.next(t)
		h.clock.advance(time.Second)
		for _, e := range dieStart("a", "app") {
			h.src.events <- e
		}
		h.next(t)
	})
	t.Run("two pairs in the same second", func(t *testing.T) {
		h := start(t, ctr("a", "app", "web", "running", "Up"))
		for _, e := range append(dieStart("a", "app"), dieStart("a", "app")...) {
			h.src.events <- e
		}
		h.next(t)
		h.noInsert(t)
	})
	t.Run("die without project label", func(t *testing.T) {
		h := start(t, ctr("a", "app", "web", "running", "Up"))
		for _, e := range dieStart("a", "") {
			h.src.events <- e
		}
		h.noInsert(t)
	})
}

func TestRecorder_InsertFailureIsRetriedAndRateLimited(t *testing.T) {
	h := start(t, ctr("a", "app", "web", "running", "Up (healthy)"))
	h.w.setFail(true)
	h.src.set(ctr("a", "app", "web", "running", "Up (unhealthy)"))

	h.src.changes <- state.Change{Project: "app"}
	first := h.next(t)
	h.src.changes <- state.Change{Project: "app"}
	second := h.next(t) // retried: the map was not updated
	if first[0].State != StateUnhealthy || second[0].State != StateUnhealthy {
		t.Fatalf("attempts = %+v %+v", first, second)
	}
	h.drain(t)
	if n := strings.Count(h.logs.String(), "level=WARN"); n != 1 {
		t.Fatalf("warnings = %d, want 1\n%s", n, h.logs.String())
	}

	h.clock.advance(warnEvery)
	h.src.changes <- state.Change{Project: "app"}
	h.next(t)
	h.drain(t)
	if n := strings.Count(h.logs.String(), "level=WARN"); n != 2 {
		t.Fatalf("warnings after window = %d, want 2", n)
	}

	h.w.setFail(false)
	h.src.changes <- state.Change{Project: "app"}
	h.next(t)
	h.src.changes <- state.Change{Project: "app"}
	h.noInsert(t) // success updated the map
}

func TestRecorder_StopsOnCancel(t *testing.T) {
	h := start(t)
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return")
	}
	for i, ch := range []chan struct{}{h.src.cancelChanges, h.src.cancelEvents} {
		select {
		case <-ch:
		default:
			t.Fatalf("subscription %d was not cancelled", i)
		}
	}
}
