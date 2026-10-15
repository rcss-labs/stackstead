package metrics_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/metrics"
)

type fakeLister struct {
	list   []engine.Container
	err    error
	onCall func()
}

func (f *fakeLister) Containers(context.Context) ([]engine.Container, error) {
	if f.onCall != nil {
		f.onCall()
	}
	return f.list, f.err
}

type fakeStats struct {
	mu       sync.Mutex
	inflight int
	max      int
	delay    time.Duration
	fail     map[string]bool
	zero     map[string]bool
	calls    int32
	deadline time.Duration
}

func (f *fakeStats) Stats(ctx context.Context, id string) (engine.Stats, error) {
	atomic.AddInt32(&f.calls, 1)
	f.mu.Lock()
	f.inflight++
	if f.inflight > f.max {
		f.max = f.inflight
	}
	if dl, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(dl)
	}
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.inflight--; f.mu.Unlock() }()
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return engine.Stats{}, ctx.Err()
		}
	}
	if f.fail[id] {
		return engine.Stats{}, errors.New("boom")
	}
	if f.zero[id] {
		return engine.Stats{}, nil
	}
	return engine.Stats{Read: base, CPUPercent: 1.5, MemUsage: 10, MemLimit: 100, NetRx: 1, NetTx: 2, BlkRead: 3, BlkWrite: 4}, nil
}

func ctr(id, project, service, state string) engine.Container {
	l := map[string]string{}
	if project != "" {
		l["com.docker.compose.project"] = project
		l["com.docker.compose.service"] = service
	}
	return engine.Container{ID: id, Name: id, State: state, Labels: l}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type harness struct {
	s     *metrics.Sampler
	db    *sql.DB
	ticks chan time.Time
	clk   *clock
	stats *fakeStats
	lst   *fakeLister
	done  chan struct{}
	stop  context.CancelFunc
	fin   chan struct{}
}

func newHarness(t *testing.T, cs ...engine.Container) *harness {
	t.Helper()
	st, db := newStore(t)
	h := &harness{db: db, ticks: make(chan time.Time), clk: &clock{t: base}, stats: &fakeStats{}, lst: &fakeLister{list: cs}, done: make(chan struct{})}
	h.s = metrics.NewSampler(st, h.lst, h.stats, 10*time.Second, 72*time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)), h.clk.now)
	h.s.UseTicks(h.ticks)
	h.fin = make(chan struct{}, 1000)
	h.s.OnTickDone(func() { h.fin <- struct{}{} })
	return h
}

func (h *harness) start(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h.stop = cancel
	go func() { h.s.Run(ctx); close(h.done) }()
	t.Cleanup(func() { cancel(); <-h.done })
}

// tick delivers one tick and waits until its body has finished.
func (h *harness) tick() {
	h.ticks <- h.clk.now()
	<-h.fin
}

func (h *harness) rows() int {
	var n int
	h.db.QueryRow("SELECT COUNT(*) FROM metric_samples").Scan(&n)
	return n
}

func TestTickStoresRunningComposeContainersOnly(t *testing.T) {
	h := newHarness(t,
		ctr("a", "p", "web", "running"),
		ctr("b", "p", "db", "running"),
		ctr("c", "", "", "running"),
		ctr("d", "p", "old", "exited"),
		ctr("e", "p", "gone", "running"), // zero Read => stopped
	)
	h.stats.zero = map[string]bool{"e": true}
	h.start(t)
	h.tick()
	if n := h.rows(); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
	if n := atomic.LoadInt32(&h.stats.calls); n != 4 {
		t.Errorf("stats calls = %d, want 4 (non-running not queried)", n)
	}
	got := h.s.Latest()
	if len(got) != 3 || got[0].Project != "p" || got[0].Mem != 10 {
		t.Errorf("Latest = %+v", got)
	}
	got[0].Project = "mutated"
	if h.s.Latest()[0].Project != "p" {
		t.Error("Latest returned shared slice")
	}
	if h.s.Degraded() {
		t.Error("degraded")
	}
}

func TestFailingContainerSkipped(t *testing.T) {
	h := newHarness(t, ctr("a", "p", "web", "running"), ctr("b", "p", "db", "running"))
	h.stats.fail = map[string]bool{"a": true}
	h.start(t)
	h.tick()
	if n := h.rows(); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestConcurrencyCappedAtFour(t *testing.T) {
	var cs []engine.Container
	for i := 0; i < 12; i++ {
		cs = append(cs, ctr(fmt.Sprint("c", i), "p", fmt.Sprint("s", i), "running"))
	}
	h := newHarness(t, cs...)
	h.stats.delay = 20 * time.Millisecond
	h.start(t)
	h.tick()
	h.stats.mu.Lock()
	defer h.stats.mu.Unlock()
	if h.stats.max > 4 || h.stats.max < 2 {
		t.Errorf("max concurrency = %d, want 2..4", h.stats.max)
	}
	if h.rows() != 12 {
		t.Errorf("rows = %d", h.rows())
	}
}

func TestPerCallTimeoutIsMinOfIntervalAndFiveSeconds(t *testing.T) {
	h := newHarness(t, ctr("a", "p", "web", "running"))
	h.start(t)
	h.tick()
	h.stats.mu.Lock()
	d := h.stats.deadline
	h.stats.mu.Unlock()
	if d <= 0 || d > 10*time.Second || d < 4*time.Second {
		t.Errorf("deadline = %v, want about 5s", d)
	}
}

func TestInsertFailureDegradesThenRecovers(t *testing.T) {
	h := newHarness(t, ctr("a", "p", "web", "running"))
	h.start(t)
	h.db.Exec("ALTER TABLE metric_samples RENAME TO metric_samples_x")
	h.tick()
	if !h.s.Degraded() {
		t.Fatal("want degraded after failed insert")
	}
	if len(h.s.Latest()) != 1 {
		t.Error("Latest should still be populated")
	}
	h.db.Exec("ALTER TABLE metric_samples_x RENAME TO metric_samples")
	h.tick()
	if h.s.Degraded() {
		t.Error("want recovered")
	}
	if h.rows() != 1 {
		t.Errorf("rows = %d, want 1 (failed batch dropped)", h.rows())
	}
}

func TestPruneAtStartAndHourly(t *testing.T) {
	h := newHarness(t, ctr("a", "p", "web", "running"))
	old := base.Add(-73 * time.Hour)
	fresh := base.Add(-time.Hour)
	for _, ts := range []time.Time{old, fresh} {
		h.db.Exec(`INSERT INTO metric_samples (ts, project, service, container, cpu, mem, mem_limit, net_rx, net_tx, blk_read, blk_write)
			VALUES (?, 'p', 'web', 'c', 0,0,0,0,0,0,0)`, ts.Unix())
	}
	h.start(t)
	h.tick()
	// start prune removed old; the tick added one row
	if n := h.rows(); n != 2 {
		t.Fatalf("rows after start = %d, want 2", n)
	}
	// age the fresh row past retention without an hour passing: no prune yet
	h.clk.add(72 * time.Hour)
	h.tick() // 72h later: hour elapsed -> prune (cutoff = base, removes 'fresh' and nothing newer)
	if n := h.rows(); n != 2 {
		// remaining: tick-1 row (ts=base) is not < cutoff(base); tick-2 row ts=base+72h
		t.Fatalf("rows after hourly prune = %d, want 2", n)
	}
	h.clk.add(10 * time.Minute)
	h.db.Exec(`INSERT INTO metric_samples (ts, project, service, container, cpu, mem, mem_limit, net_rx, net_tx, blk_read, blk_write)
		VALUES (?, 'p', 'web', 'c', 0,0,0,0,0,0,0)`, base.Add(-100*time.Hour).Unix())
	h.tick() // < 1h since last prune: not pruned
	if n := h.rows(); n != 4 {
		t.Errorf("rows = %d, want 4 (no prune within the hour)", n)
	}
}

func TestTicksDoNotOverlapAndMissedAreSkipped(t *testing.T) {
	h := newHarness(t, ctr("a", "p", "web", "running"))
	h.stats.delay = 100 * time.Millisecond
	h.start(t)
	h.ticks <- h.clk.now()
	// Run is busy; a second tick offered meanwhile must wait, never run concurrently.
	h.ticks <- h.clk.now()
	h.ticks <- h.clk.now()
	h.stats.mu.Lock()
	defer h.stats.mu.Unlock()
	if h.stats.max != 1 {
		t.Errorf("max in flight = %d, want 1", h.stats.max)
	}
}

func TestSlowSubscriberDropped(t *testing.T) {
	h := newHarness(t, ctr("a", "p", "web", "running"), ctr("b", "q", "web", "running"))
	h.start(t)
	fast, cancelFast := h.s.Subscribe("p")
	defer cancelFast()
	slow, cancelSlow := h.s.Subscribe("p")
	other, cancelOther := h.s.Subscribe("q")
	defer cancelOther()

	var got int
	for i := 0; i < 300; i++ {
		h.tick()
		select {
		case s := <-fast:
			if s.Project != "p" {
				t.Fatalf("fast got project %q", s.Project)
			}
			got++
		default:
			t.Fatal("fast subscriber missed a sample")
		}
		select {
		case <-other:
		default:
			t.Fatal("other-project subscriber missed a sample")
		}
	}
	// slow never read: must have been closed after its buffer filled
	closed := false
	for i := 0; i < 1000; i++ {
		select {
		case _, ok := <-slow:
			if !ok {
				closed = true
			}
		default:
		}
		if closed {
			break
		}
	}
	if !closed {
		t.Error("slow subscriber channel not closed")
	}
	cancelSlow()
	cancelSlow() // idempotent
	if got != 300 {
		t.Errorf("fast got %d", got)
	}
}

func TestCancelClosesChannelOnce(t *testing.T) {
	h := newHarness(t)
	ch, cancel := h.s.Subscribe("p")
	cancel()
	cancel()
	if _, ok := <-ch; ok {
		t.Error("channel not closed")
	}
}

func TestRunReturnsOnCancelWithoutLeaks(t *testing.T) {
	h := newHarness(t, ctr("a", "p", "web", "running"))
	before := runtime.NumGoroutine()
	h.stats.delay = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.s.Run(ctx); close(done) }()
	h.ticks <- h.clk.now()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Errorf("goroutines %d -> %d", before, n)
	}
}

func TestKeepingUpSubscriberNotDroppedOnLargeStack(t *testing.T) {
	var cs []engine.Container
	for i := 0; i < 12; i++ {
		cs = append(cs, ctr(fmt.Sprint("c", i), "p", fmt.Sprint("s", i), "running"))
	}
	h := newHarness(t, cs...)
	h.start(t)
	reader, cancelReader := h.s.Subscribe("p")
	defer cancelReader()
	idle, cancelIdle := h.s.Subscribe("p")
	defer cancelIdle()

	var received int32
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for range reader {
			atomic.AddInt32(&received, 1)
		}
	}()
	for i := 0; i < 40; i++ {
		h.tick()
		for deadline := time.Now().Add(2 * time.Second); atomic.LoadInt32(&received) < int32(12*(i+1)) && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
	}
	cancelReader()
	<-readerDone
	if n := atomic.LoadInt32(&received); n != 12*40 {
		t.Errorf("reader received %d, want %d (dropped while keeping up)", n, 12*40)
	}
	closed := false
	for i := 0; i < 1000 && !closed; i++ {
		select {
		case _, ok := <-idle:
			closed = !ok
		default:
			i = 1000
		}
	}
	if !closed {
		t.Error("never-reading subscriber was not dropped")
	}
}

func TestEmptyTickDoesNotClearDegraded(t *testing.T) {
	h := newHarness(t, ctr("a", "p", "web", "running"))
	h.start(t)
	h.db.Exec("ALTER TABLE metric_samples RENAME TO metric_samples_x")
	h.tick()
	if !h.s.Degraded() {
		t.Fatal("want degraded")
	}
	h.lst.list = nil
	h.tick()
	if !h.s.Degraded() {
		t.Error("empty tick cleared degraded while the store is still broken")
	}
	h.db.Exec("ALTER TABLE metric_samples_x RENAME TO metric_samples")
	h.lst.list = []engine.Container{ctr("a", "p", "web", "running")}
	h.tick()
	if h.s.Degraded() {
		t.Error("want recovered after a successful write")
	}
}

func TestListerErrorClearsLatest(t *testing.T) {
	h := newHarness(t, ctr("a", "p", "web", "running"))
	h.start(t)
	h.tick()
	if len(h.s.Latest()) != 1 {
		t.Fatal("want latest populated")
	}
	h.lst.err = errors.New("cache down")
	h.tick()
	if n := len(h.s.Latest()); n != 0 {
		t.Errorf("Latest has %d stale entries after lister error", n)
	}
}

func TestListContainersFailureLogIsRateLimitedToOncePerMinute(t *testing.T) {
	st, _ := newStore(t)
	var buf syncBuf
	clk := &clock{t: base}
	lst := &fakeLister{err: errors.New("docker down")}
	ticks := make(chan time.Time)
	s := metrics.NewSampler(st, lst, &fakeStats{}, 10*time.Second, time.Hour, slog.New(slog.NewTextHandler(&buf, nil)), clk.now)
	s.UseTicks(ticks)
	fin := make(chan struct{}, 100)
	s.OnTickDone(func() { fin <- struct{}{} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	tick := func() { ticks <- clk.now(); <-fin }
	count := func() int { return strings.Count(buf.String(), "list containers") }

	tick()
	clk.add(10 * time.Second)
	tick()
	clk.add(10 * time.Second)
	tick()
	if n := count(); n != 1 {
		t.Fatalf("logs within one minute = %d, want 1", n)
	}
	clk.add(time.Minute)
	tick()
	if n := count(); n != 2 {
		t.Fatalf("logs after a minute = %d, want 2", n)
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  []byte
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b = append(s.b, p...)
	return len(p), nil
}

func (s *syncBuf) String() string { s.mu.Lock(); defer s.mu.Unlock(); return string(s.b) }

func TestShutdownIsNotLoggedOrReportedAsFailure(t *testing.T) {
	st, _ := newStore(t)
	var buf syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	lst := &fakeLister{list: []engine.Container{ctr("a", "p", "web", "running")}}
	lst.onCall = func() { cancel(); lst.err = context.Canceled }
	ticks := make(chan time.Time)
	s := metrics.NewSampler(st, lst, &fakeStats{}, 10*time.Second, time.Hour, slog.New(slog.NewTextHandler(&buf, nil)), time.Now)
	s.UseTicks(ticks)
	fin := make(chan struct{}, 1)
	s.OnTickDone(func() { fin <- struct{}{} })
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	ticks <- time.Now()
	<-done
	if s.Degraded() {
		t.Error("shutdown reported as degraded")
	}
	if out := buf.String(); out != "" {
		t.Errorf("unexpected log on shutdown: %s", out)
	}
}

// fakeSampleStore is a controllable sampler store.
type fakeSampleStore struct {
	insert func(ctx context.Context, n int) error
}

func (f *fakeSampleStore) InsertBatch(ctx context.Context, b []metrics.Sample) error {
	return f.insert(ctx, len(b))
}
func (f *fakeSampleStore) Prune(context.Context, time.Time) (int64, error) { return 0, nil }

func newFakeStoreSampler(t *testing.T, interval time.Duration, st *fakeSampleStore, stats metrics.StatsSource, buf io.Writer, clk *clock, ctx context.Context) (*metrics.Sampler, chan time.Time, chan struct{}) {
	t.Helper()
	real, _ := newStore(t)
	lst := &fakeLister{list: []engine.Container{ctr("a", "p", "web", "running")}}
	s := metrics.NewSampler(real, lst, stats, interval, time.Hour, slog.New(slog.NewTextHandler(buf, nil)), clk.now)
	s.UseStore(st)
	ticks := make(chan time.Time)
	s.UseTicks(ticks)
	fin := make(chan struct{}, 100)
	s.OnTickDone(func() { fin <- struct{}{} })
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	t.Cleanup(func() { <-done })
	return s, ticks, fin
}

func TestWriteCancelledMidWriteIsNotLoggedOrDegraded(t *testing.T) {
	var buf syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := &fakeSampleStore{insert: func(ctx context.Context, n int) error {
		cancel() // shutdown arrives while the batch is being written
		return ctx.Err()
	}}
	s, ticks, _ := newFakeStoreSampler(t, 10*time.Second, st, &fakeStats{}, &buf, &clock{t: base}, ctx)
	ticks <- base
	<-ctx.Done()
	time.Sleep(50 * time.Millisecond) // let the interrupted tick body finish
	if s.Degraded() {
		t.Error("write cut short by shutdown reported as degraded")
	}
	if out := buf.String(); out != "" {
		t.Errorf("unexpected log on shutdown: %s", out)
	}
}

func TestPerCallTimeoutEqualsIntervalWithinBounds(t *testing.T) {
	stats := &fakeStats{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := &fakeSampleStore{insert: func(context.Context, int) error { return nil }}
	_, ticks, fin := newFakeStoreSampler(t, 4*time.Second, st, stats, io.Discard, &clock{t: base}, ctx)
	ticks <- base
	<-fin
	stats.mu.Lock()
	d := stats.deadline
	stats.mu.Unlock()
	if d <= 3*time.Second || d > 4*time.Second {
		t.Errorf("deadline = %v, want about 4s (the interval)", d)
	}
	cancel()
}

func TestWriteErrorLogIsRateLimitedToOncePerMinute(t *testing.T) {
	var buf syncBuf
	clk := &clock{t: base}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := &fakeSampleStore{insert: func(context.Context, int) error { return errors.New("disk full") }}
	_, ticks, fin := newFakeStoreSampler(t, 10*time.Second, st, &fakeStats{}, &buf, clk, ctx)
	tick := func() { ticks <- clk.now(); <-fin }
	count := func() int { return strings.Count(buf.String(), "write failed") }

	tick()
	clk.add(10 * time.Second)
	tick()
	clk.add(10 * time.Second)
	tick()
	if n := count(); n != 1 {
		t.Fatalf("logs within one minute = %d, want 1", n)
	}
	clk.add(time.Minute)
	tick()
	if n := count(); n != 2 {
		t.Fatalf("logs after a minute = %d, want 2", n)
	}
	cancel()
}

// Docker takes about a second to compute the CPU delta, so a short interval
// must not shrink the per-call timeout to the point where every read fails.
func TestStatsTimeoutNeverBelowDockerCPUWindow(t *testing.T) {
	for interval, want := range map[time.Duration]time.Duration{
		time.Second:      3 * time.Second,
		10 * time.Second: 5 * time.Second,
		4 * time.Second:  4 * time.Second,
	} {
		if got := metrics.StatsTimeout(interval); got != want {
			t.Errorf("StatsTimeout(%s) = %s, want %s", interval, got, want)
		}
	}
}

func TestSamplerSamplesNonComposeContainersLive(t *testing.T) {
	h := newHarness(t, ctr("a", "p", "web", "running"), ctr("plain", "", "", "running"))
	h.start(t)
	h.tick()
	got := h.s.Latest()
	if len(got) != 2 {
		t.Fatalf("Latest = %+v, want both containers", got)
	}
	var sawPlain bool
	for _, e := range got {
		if e.Container == "plain" && e.Project == "" {
			sawPlain = true
		}
	}
	if !sawPlain {
		t.Errorf("non-compose container missing from Latest: %+v", got)
	}
}

func TestSamplerStoresOnlyComposeSamples(t *testing.T) {
	var mu sync.Mutex
	var stored []metrics.Sample
	st := &recordingStore{insert: func(b []metrics.Sample) {
		mu.Lock()
		defer mu.Unlock()
		stored = append(stored, b...)
	}}
	real, _ := newStore(t)
	lst := &fakeLister{list: []engine.Container{ctr("a", "p", "web", "running"), ctr("plain", "", "", "running")}}
	s := metrics.NewSampler(real, lst, &fakeStats{}, 10*time.Second, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)), (&clock{t: base}).now)
	s.UseStore(st)
	ticks := make(chan time.Time)
	s.UseTicks(ticks)
	fin := make(chan struct{}, 10)
	s.OnTickDone(func() { fin <- struct{}{} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	ticks <- base
	<-fin

	mu.Lock()
	defer mu.Unlock()
	if len(stored) != 1 || stored[0].Container != "a" || stored[0].Project != "p" {
		t.Errorf("stored = %+v, want only the compose sample", stored)
	}
	if n := len(s.Latest()); n != 2 {
		t.Errorf("Latest len = %d, want 2", n)
	}
}

func TestSamplerSamplesAllRunningContainersWithinConcurrency(t *testing.T) {
	var cs []engine.Container
	for i := 0; i < 200; i++ {
		cs = append(cs, ctr(fmt.Sprint("c", i), "", "", "running"))
	}
	h := newHarness(t, cs...)
	h.stats.delay = time.Millisecond
	h.start(t)
	h.tick()
	h.stats.mu.Lock()
	maxInFlight := h.stats.max
	h.stats.mu.Unlock()
	if maxInFlight > metrics.MaxStatsConcurrency {
		t.Errorf("max in-flight = %d, want <= %d", maxInFlight, metrics.MaxStatsConcurrency)
	}
	if n := len(h.s.Latest()); n != 200 {
		t.Errorf("Latest len = %d, want 200", n)
	}
	if h.rows() != 0 {
		t.Errorf("non-compose samples were stored: %d rows", h.rows())
	}
}

// recordingStore captures the batches handed to the sampler's store.
type recordingStore struct {
	insert func(b []metrics.Sample)
}

func (r *recordingStore) InsertBatch(_ context.Context, b []metrics.Sample) error {
	r.insert(b)
	return nil
}
func (r *recordingStore) Prune(context.Context, time.Time) (int64, error) { return 0, nil }

func TestSubscribeEmptyProjectReceivesNothing(t *testing.T) {
	h := newHarness(t, ctr("plain", "", "", "running"))
	ch, cancel := h.s.Subscribe("")
	defer cancel()
	h.start(t)
	h.tick()
	select {
	case m, ok := <-ch:
		t.Fatalf("Subscribe(\"\") delivered %+v (ok=%v)", m, ok)
	case <-time.After(100 * time.Millisecond):
	}
}
