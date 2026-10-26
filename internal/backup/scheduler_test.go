package backup

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTimer hands out channels that tests fire by hand.
type fakeTimer struct {
	mu       sync.Mutex
	requests []fakeWait
	asked    chan struct{}
}

type fakeWait struct {
	d  time.Duration
	ch chan time.Time
}

func newFakeTimer() *fakeTimer { return &fakeTimer{asked: make(chan struct{}, 16)} }

func (f *fakeTimer) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	f.mu.Lock()
	f.requests = append(f.requests, fakeWait{d, ch})
	f.mu.Unlock()
	f.asked <- struct{}{}
	return ch
}

// next waits for the scheduler to ask for another wait and returns it.
func (f *fakeTimer) next(t *testing.T) fakeWait {
	t.Helper()
	select {
	case <-f.asked:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not wait for a timer")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func scheduledConfig(t *testing.T, clock string) Config {
	cfg := localOnly(t)
	cfg.Schedule = Schedule{Enabled: true, Frequency: frequencyDaily, Time: clock}
	return cfg
}

// testClock is a wall clock the tests move by hand.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

type schedulerEnv struct {
	*runnerEnv
	sched *Scheduler
	timer *fakeTimer
	clock *testClock
	logs  *lockedBuffer
	done  chan struct{}
}

func startScheduler(t *testing.T, cfg Config) *schedulerEnv {
	t.Helper()
	env := &schedulerEnv{runnerEnv: newRunnerEnv(t, cfg), timer: newFakeTimer(), clock: &testClock{now: runnerNow}, logs: &lockedBuffer{}, done: make(chan struct{})}
	env.sched = &Scheduler{
		Runner: env.runner,
		Config: env.deps.Config,
		Now:    env.clock.Now,
		After:  env.timer.After,
		Loc:    time.UTC,
		Logger: slog.New(slog.NewTextHandler(env.logs, nil)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(env.done)
		env.sched.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-env.done
	})
	return env
}

func (env *schedulerEnv) recentRuns(t *testing.T) []Run {
	t.Helper()
	runs, err := env.deps.Runs.Recent(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

func (env *schedulerEnv) waitForRuns(t *testing.T, n int) []Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runs := env.recentRuns(t); len(runs) >= n {
			return runs
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %d runs", n)
	return nil
}

// slot is today's HH:MM in UTC on the runner's test date.
func slot(h, m int) time.Time {
	return time.Date(runnerNow.Year(), runnerNow.Month(), runnerNow.Day(), h, m, 0, 0, time.UTC)
}

func TestSchedulerFiresAtNextRun(t *testing.T) {
	env := startScheduler(t, scheduledConfig(t, "04:30"))
	w := env.timer.next(t)
	if w.d != time.Minute {
		t.Fatalf("a wait of %v is not capped at one minute", w.d)
	}
	// The wall clock reaches the slot; the timer wakes the scheduler.
	env.clock.set(slot(4, 30))
	w.ch <- slot(4, 30)
	runs := env.waitForRuns(t, 1)
	if runs[0].Trigger != "schedule" {
		t.Errorf("trigger = %q, want schedule", runs[0].Trigger)
	}
}

func TestSchedulerDoesNotFireTwiceForOneSlot(t *testing.T) {
	env := startScheduler(t, scheduledConfig(t, "04:30"))
	w := env.timer.next(t)
	env.clock.set(slot(4, 30))
	w.ch <- slot(4, 30)
	env.waitForRuns(t, 1)

	// With the clock still on the slot, the next wake must not run again.
	w = env.timer.next(t)
	w.ch <- slot(4, 30)
	env.timer.next(t)
	time.Sleep(100 * time.Millisecond)
	if n := len(env.recentRuns(t)); n != 1 {
		t.Fatalf("one slot produced %d runs", n)
	}
}

func TestSchedulerSkipsMissedSlot(t *testing.T) {
	env := startScheduler(t, scheduledConfig(t, "04:30"))
	w := env.timer.next(t)
	// A suspend or clock jump: the wall clock wakes well after the slot.
	env.clock.set(slot(4, 30).Add(3 * time.Minute))
	w.ch <- env.clock.Now()
	env.timer.next(t) // the scheduler is waiting for the next day's slot
	if n := len(env.recentRuns(t)); n != 0 {
		t.Fatalf("a missed slot ran %d backups", n)
	}
	if !strings.Contains(env.logs.String(), "backup slot missed") {
		t.Errorf("log lacks missed-slot message: %s", env.logs.String())
	}
}

func TestSchedulerSkipsWhenBusy(t *testing.T) {
	env := startScheduler(t, scheduledConfig(t, "04:30"))
	release := make(chan struct{})
	if _, err := env.deps.Ops.StartTimeout(runOpKey, runOpAction, time.Minute, func(ctx context.Context, _ func(string)) error {
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	defer close(release)

	w := env.timer.next(t)
	env.clock.set(slot(4, 30))
	w.ch <- slot(4, 30)
	env.timer.next(t) // the scheduler is waiting for the following slot

	if !strings.Contains(env.logs.String(), "backup skipped: previous run still in progress") {
		t.Errorf("log lacks skip message: %s", env.logs.String())
	}
	if n := len(env.recentRuns(t)); n != 0 {
		t.Errorf("a busy slot queued %d runs", n)
	}
}

func TestSchedulerReloadsOnConfigChange(t *testing.T) {
	env := startScheduler(t, scheduledConfig(t, "04:30"))
	env.timer.next(t)

	cfg := scheduledConfig(t, "05:00")
	cfg.Local = env.mustLoad(t).Local
	if err := env.deps.Config.Save(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	env.sched.Reload()
	w := env.timer.next(t)

	// 04:30 is no longer a slot; 05:00 is.
	env.clock.set(slot(4, 30))
	w.ch <- slot(4, 30)
	w = env.timer.next(t)
	if n := len(env.recentRuns(t)); n != 0 {
		t.Fatalf("the old 04:30 slot ran after the change (%d runs)", n)
	}
	env.clock.set(slot(5, 0))
	w.ch <- slot(5, 0)
	env.waitForRuns(t, 1)
	env.timer.next(t) // waiting for the next day's slot

	// Turning the schedule off waits for the next reload without a timer.
	cfg.Schedule.Enabled = false
	if err := env.deps.Config.Save(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	env.sched.Reload()
	select {
	case <-env.timer.asked:
		t.Fatal("a disabled schedule asked for a timer")
	case <-time.After(100 * time.Millisecond):
	}
}

func (env *schedulerEnv) mustLoad(t *testing.T) Config {
	t.Helper()
	cfg, err := env.deps.Config.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
