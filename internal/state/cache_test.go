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
	overlaps   int           // Events calls made while an earlier stream was still open
	gate       chan struct{} // when set, Containers blocks until it is closed
	eventsErr  error         // when set, every stream fails immediately
}

func (f *fakeSource) Containers(context.Context) ([]engine.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches++
	if g := f.gate; g != nil {
		f.mu.Unlock()
		<-g
		f.mu.Lock()
	}
	if f.err != nil {
		return nil, f.err
	}
	return append([]engine.Container(nil), f.containers...), nil
}

func (f *fakeSource) Events(ctx context.Context, _ time.Time) (<-chan engine.Event, <-chan error) {
	s := &fakeStream{ctx: ctx, events: make(chan engine.Event), errs: make(chan error, 1)}
	f.mu.Lock()
	for _, old := range f.streams {
		if old.ctx.Err() == nil {
			f.overlaps++
		}
	}
	f.streams = append(f.streams, s)
	if f.eventsErr != nil {
		s.fail(f.eventsErr)
	}
	f.mu.Unlock()
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
	for i := range subscriberBuffer + 6 {
		src.set([]engine.Container{ctr("a", "a1", states[i%2])}, nil)
		s.send(t, engine.Event{Type: "container", Action: "x", ContainerID: "a1"})
		next(t, fast)
	}
	closed := false
	for range 2 * subscriberBuffer {
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
	if src.overlaps > 0 {
		t.Errorf("%d event streams were opened while the previous one was still open", src.overlaps)
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

func TestExecEventsDoNotRefetch(t *testing.T) {
	src := &fakeSource{containers: []engine.Container{ctr("a", "a1", "running")}}
	h := start(t, src)
	waitFor(t, h.c.Connected)
	base := src.fetchCount()
	s := src.stream(t, 1)
	for _, a := range []string{"exec_create: sh", "exec_start: sh", "exec_die"} {
		s.send(t, engine.Event{Type: "container", Action: a, ContainerID: "a1"})
	}
	time.Sleep(100 * time.Millisecond)
	if n := src.fetchCount() - base; n != 0 {
		t.Errorf("exec events caused %d refetches", n)
	}
	s.send(t, engine.Event{Type: "container", Action: "health_status: healthy", ContainerID: "a1"})
	waitFor(t, func() bool { return src.fetchCount() > base })
}

func withStatus(c engine.Container, status string) engine.Container {
	c.Status = status
	return c
}

func TestChangeDependsOnStatusProjectionNotStatusText(t *testing.T) {
	up := withStatus(ctr("a", "a1", "running"), "Up 5 minutes")
	src := &fakeSource{containers: []engine.Container{up}}
	h := start(t, src)
	waitFor(t, h.c.Connected)
	changes, cancel := h.c.Subscribe()
	defer cancel()
	s := src.stream(t, 1)
	ev := engine.Event{Type: "container", Action: "start", ContainerID: "a1"}

	src.set([]engine.Container{withStatus(up, "Up 6 minutes")}, nil)
	s.send(t, ev)
	noChange(t, changes, 120*time.Millisecond)

	for _, status := range []string{"Up 6 minutes (healthy)", "Up 6 minutes (unhealthy)", "Up 6 minutes (health: starting)"} {
		src.set([]engine.Container{withStatus(up, status)}, nil)
		s.send(t, ev)
		if ch := next(t, changes); ch.Project != "a" {
			t.Fatalf("%s: change = %+v", status, ch)
		}
	}

	dead := withStatus(up, "Exited (1) 2 seconds ago")
	dead.State = "exited"
	src.set([]engine.Container{dead}, nil)
	s.send(t, ev)
	next(t, changes)
	src.set([]engine.Container{withStatus(dead, "Exited (137) 2 seconds ago")}, nil)
	s.send(t, ev)
	if ch := next(t, changes); ch.Project != "a" {
		t.Fatalf("exit code change = %+v", ch)
	}
	src.set([]engine.Container{withStatus(dead, "Exited (137) 3 minutes ago")}, nil)
	s.send(t, ev)
	noChange(t, changes, 120*time.Millisecond)
}

func TestFailingEventStreamKeepsBackingOff(t *testing.T) {
	src := &fakeSource{containers: []engine.Container{ctr("a", "a1", "running")}, eventsErr: engine.ErrUnavailable}
	c := New(src, nil, Backoff{Min: 20 * time.Millisecond, Max: time.Second})
	var mu sync.Mutex
	var waits []time.Duration
	c.onWait = func(d time.Duration) { mu.Lock(); waits = append(waits, d); mu.Unlock() }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	defer func() { cancel(); <-done }()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(waits) >= 3 })
	mu.Lock()
	defer mu.Unlock()
	// Attempt 2 waits at least half of 4*Min; a reset after each successful
	// resync would keep every wait at or below Min.
	if waits[2] <= 20*time.Millisecond {
		t.Errorf("waits = %v: backoff did not grow", waits)
	}
}

func TestEventDuringRefetchTriggersAnotherRefetch(t *testing.T) {
	src := &fakeSource{containers: []engine.Container{ctr("a", "a1", "running")}}
	h := start(t, src)
	waitFor(t, h.c.Connected)
	base := src.fetchCount()
	gate := make(chan struct{})
	src.mu.Lock()
	src.gate = gate
	src.mu.Unlock()
	s := src.stream(t, 1)
	ev := engine.Event{Type: "container", Action: "die", ContainerID: "a1"}
	s.send(t, ev)
	waitFor(t, func() bool { return src.fetchCount() == base+1 }) // refetch in flight
	go func() {
		select {
		case s.events <- ev:
		case <-s.ctx.Done():
		}
	}()
	time.Sleep(50 * time.Millisecond)
	close(gate)
	waitFor(t, func() bool { return src.fetchCount() >= base+2 })
}
