package state

import (
	"context"
	"errors"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
)

type fakeStream struct {
	ctx    context.Context
	events chan engine.Event
	errs   chan error
}

func (s *fakeStream) send(t *testing.T, ev engine.Event) {
	t.Helper()
	select {
	case s.events <- ev:
	case <-s.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("event not consumed")
	}
}

// fail ends the stream with an error the way engine.Client does. The stream
// stays "open" for concurrency accounting until the cache cancels its context.
func (s *fakeStream) fail(err error) {
	s.errs <- err
	close(s.errs)
	close(s.events)
}

type fakeSource struct {
	mu         sync.Mutex
	containers []engine.Container
	err        error
	fetches    int
	streams    []*fakeStream
	active     int
	maxActive  int
}

func (f *fakeSource) Containers(context.Context) ([]engine.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches++
	if f.err != nil {
		return nil, f.err
	}
	return append([]engine.Container(nil), f.containers...), nil
}

func (f *fakeSource) Events(ctx context.Context, _ time.Time) (<-chan engine.Event, <-chan error) {
	s := &fakeStream{ctx: ctx, events: make(chan engine.Event), errs: make(chan error, 1)}
	f.mu.Lock()
	f.streams = append(f.streams, s)
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	go func() {
		<-ctx.Done()
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	return s.events, s.errs
}

func (f *fakeSource) set(cs []engine.Container, err error) {
	f.mu.Lock()
	f.containers, f.err = cs, err
	f.mu.Unlock()
}

func (f *fakeSource) fetchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetches
}

func (f *fakeSource) stream(t *testing.T, n int) *fakeStream {
	t.Helper()
	var s *fakeStream
	waitFor(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.streams) >= n {
			s = f.streams[n-1]
		}
		return s != nil
	})
	return s
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func ctr(project, id, state string) engine.Container {
	return engine.Container{ID: id, Name: id, State: state,
		Labels: map[string]string{"com.docker.compose.project": project}}
}

type harness struct {
	c      *Cache
	src    *fakeSource
	cancel context.CancelFunc
	done   chan struct{}
}

func start(t *testing.T, src *fakeSource) *harness {
	t.Helper()
	c := New(src, nil, Backoff{Min: 10 * time.Millisecond, Max: 40 * time.Millisecond})
	c.debounce = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{c: c, src: src, cancel: cancel, done: make(chan struct{})}
	go func() { defer close(h.done); c.Run(ctx) }()
	t.Cleanup(h.stop)
	return h
}

func (h *harness) stop() {
	h.cancel()
	<-h.done
}

func next(t *testing.T, ch <-chan Change) Change {
	t.Helper()
	select {
	case c, ok := <-ch:
		if !ok {
			t.Fatal("subscription closed")
		}
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("no change published")
	}
	return Change{}
}

func noChange(t *testing.T, ch <-chan Change, d time.Duration) {
	t.Helper()
	select {
	case c, ok := <-ch:
		if ok {
			t.Fatalf("unexpected change %+v", c)
		}
	case <-time.After(d):
	}
}

func projectsOf(t *testing.T, ch <-chan Change, n int) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	for range n {
		got[next(t, ch).Project] = true
	}
	return got
}

func TestInitialResyncPublishesSnapshot(t *testing.T) {
	src := &fakeSource{containers: []engine.Container{ctr("a", "a1", "running"), ctr("b", "b1", "running")}}
	c := New(src, nil, Backoff{})
	c.debounce = 20 * time.Millisecond
	changes, cancel := c.Subscribe()
	defer cancel()
	if _, err := c.Containers(context.Background()); !errors.Is(err, engine.ErrUnavailable) {
		t.Fatalf("before sync err = %v, want ErrUnavailable", err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	defer func() { stop(); <-done }()

	got := map[string]bool{}
	var last uint64
	for len(got) < 3 { // a, b, and the connection-state change ("")
		ch := next(t, changes)
		if ch.Seq <= last {
			t.Fatalf("seq not increasing: %d after %d", ch.Seq, last)
		}
		last = ch.Seq
		got[ch.Project] = true
	}
	if !got["a"] || !got["b"] || !got[""] {
		t.Errorf("changes = %v", got)
	}
	cs, err := c.Containers(context.Background())
	if err != nil || len(cs) != 2 || !c.Connected() {
		t.Fatalf("Containers = %v, %v connected=%v", cs, err, c.Connected())
	}
}

func TestEventTriggersRefetchAndChangeForProject(t *testing.T) {
	src := &fakeSource{containers: []engine.Container{ctr("a", "a1", "running"), ctr("b", "b1", "running")}}
	h := start(t, src)
	waitFor(t, h.c.Connected)
	changes, cancel := h.c.Subscribe()
	defer cancel()

	src.set([]engine.Container{ctr("a", "a1", "exited"), ctr("b", "b1", "running")}, nil)
	src.stream(t, 1).send(t, engine.Event{Type: "container", Action: "die", ContainerID: "a1"})
	if ch := next(t, changes); ch.Project != "a" {
		t.Fatalf("change = %+v, want project a", ch)
	}
	noChange(t, changes, 60*time.Millisecond) // b did not change
	cs, _ := h.c.Containers(context.Background())
	for _, c := range cs {
		if c.ID == "a1" && c.State != "exited" {
			t.Errorf("a1 state = %s", c.State)
		}
	}
}

func TestEventBurstIsDebounced(t *testing.T) {
	src := &fakeSource{containers: []engine.Container{ctr("a", "a1", "running")}}
	h := start(t, src)
	waitFor(t, h.c.Connected)
	base := src.fetchCount()
	s := src.stream(t, 1)
	for range 10 {
		s.send(t, engine.Event{Type: "container", Action: "start", ContainerID: "a1"})
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	if n := src.fetchCount() - base; n < 1 || n > 2 {
		t.Errorf("refetches = %d, want 1..2", n)
	}
}

func TestStreamErrorDisconnectsThenResyncs(t *testing.T) {
	src := &fakeSource{containers: []engine.Container{ctr("a", "a1", "running"), ctr("b", "b1", "running")}}
	h := start(t, src)
	waitFor(t, h.c.Connected)
	changes, cancel := h.c.Subscribe()
	defer cancel()

	// While the stream is down the engine is unreachable and a1 dies.
	src.set(nil, errors.New("down"))
	src.stream(t, 1).fail(errors.Join(engine.ErrUnavailable, io.EOF))
	waitFor(t, func() bool { return !h.c.Connected() })
	if _, err := h.c.Containers(context.Background()); !errors.Is(err, engine.ErrUnavailable) {
		t.Fatalf("disconnected Containers err = %v, want ErrUnavailable", err)
	}
	if ch := next(t, changes); ch.Project != "" {
		t.Fatalf("first change = %+v, want connection-state change", ch)
	}

	src.set([]engine.Container{ctr("a", "a1", "exited"), ctr("b", "b1", "running")}, nil)
	src.stream(t, 2) // resubscribed
	waitFor(t, h.c.Connected)
	got := projectsOf(t, changes, 2)
	if !got["a"] || !got[""] {
		t.Errorf("changes after reconnect = %v, want a and connection-state", got)
	}
	cs, err := h.c.Containers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.ID == "a1" && c.State == "running" {
			t.Error("container that died while disconnected still running")
		}
	}
}

func TestResyncFailureBacksOffAndRetries(t *testing.T) {
	src := &fakeSource{err: errors.New("boom")}
	h := start(t, src)
	waitFor(t, func() bool { return src.fetchCount() >= 3 })
	if h.c.Connected() {
		t.Error("connected despite failing resync")
	}
	src.set([]engine.Container{ctr("a", "a1", "running")}, nil)
	waitFor(t, h.c.Connected)
}

func TestVanishedProjectGetsChange(t *testing.T) {
	src := &fakeSource{containers: []engine.Container{ctr("a", "a1", "running"), ctr("b", "b1", "running")}}
	h := start(t, src)
	waitFor(t, h.c.Connected)
	changes, cancel := h.c.Subscribe()
	defer cancel()
	src.set([]engine.Container{ctr("a", "a1", "running")}, nil)
	src.stream(t, 1).send(t, engine.Event{Type: "container", Action: "destroy", ContainerID: "b1"})
	if ch := next(t, changes); ch.Project != "b" {
		t.Fatalf("change = %+v, want vanished project b", ch)
	}
}

func TestSlowSubscriberDroppedWithoutBlocking(t *testing.T) {
	src := &fakeSource{containers: []engine.Container{ctr("a", "a1", "running")}}
	h := start(t, src)
	waitFor(t, h.c.Connected)
	slow, cancelSlow := h.c.Subscribe() // never read
	fast, cancelFast := h.c.Subscribe()
	defer cancelFast()
	s := src.stream(t, 1)
	states := []string{"exited", "running"}
	for i := range 200 {
		src.set([]engine.Container{ctr("a", "a1", states[i%2])}, nil)
		s.send(t, engine.Event{Type: "container", Action: "x", ContainerID: "a1"})
		next(t, fast)
	}
	closed := false
	for range 1000 {
		if _, ok := <-slow; !ok {
			closed = true
			break
		}
	}
	if !closed {
		t.Error("slow subscriber was not dropped")
	}
	cancelSlow()
	cancelSlow() // idempotent
}

func TestNeverTwoConcurrentEventStreams(t *testing.T) {
	src := &fakeSource{containers: []engine.Container{ctr("a", "a1", "running")}}
	h := start(t, src)
	for n := 1; n <= 3; n++ {
		s := src.stream(t, n)
		waitFor(t, h.c.Connected)
		s.fail(engine.ErrUnavailable)
		waitFor(t, func() bool { return !h.c.Connected() })
	}
	src.stream(t, 4)
	src.mu.Lock()
	defer src.mu.Unlock()
	if src.maxActive > 1 {
		t.Errorf("max concurrent event streams = %d, want 1", src.maxActive)
	}
}

func TestContainersReturnsCopy(t *testing.T) {
	src := &fakeSource{containers: []engine.Container{ctr("a", "a1", "running")}}
	h := start(t, src)
	waitFor(t, h.c.Connected)
	cs, _ := h.c.Containers(context.Background())
	cs[0].State = "mutated"
	cs[0].Labels["x"] = "y"
	again, _ := h.c.Containers(context.Background())
	if again[0].State != "running" || again[0].Labels["x"] != "" {
		t.Errorf("cache was mutated through the returned slice: %+v", again[0])
	}
}

func TestRunReturnsOnCancelWithoutLeaks(t *testing.T) {
	before := runtime.NumGoroutine()
	src := &fakeSource{containers: []engine.Container{ctr("a", "a1", "running")}}
	h := start(t, src)
	waitFor(t, h.c.Connected)
	_, cancel := h.c.Subscribe()
	defer cancel()
	src.stream(t, 1).fail(engine.ErrUnavailable) // leave it in backoff too
	h.stop()
	waitFor(t, func() bool { return runtime.NumGoroutine() <= before })
}
