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

type schedulerEnv struct {
	*runnerEnv
	sched *Scheduler
	timer *fakeTimer
	logs  *lockedBuffer
	done  chan struct{}
}

func startScheduler(t *testing.T, cfg Config) *schedulerEnv {
	t.Helper()
	env := &schedulerEnv{runnerEnv: newRunnerEnv(t, cfg), timer: newFakeTimer(), logs: &lockedBuffer{}, done: make(chan struct{})}
	env.sched = &Scheduler{
		Runner: env.runner,
		Config: env.deps.Config,
		Now:    func() time.Time { return runnerNow },
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

func (env *schedulerEnv) waitForRuns(t *testing.T, n int) []Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := env.deps.Runs.Recent(context.Background(), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) >= n {
			return runs
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %d runs", n)
	return nil
}

func TestSchedulerFiresAtNextRun(t *testing.T) {
	// runnerNow is 03:00 UTC, so the next 04:30 slot is 90 minutes away.
	env := startScheduler(t, scheduledConfig(t, "04:30"))
	w := env.timer.next(t)
	if w.d != 90*time.Minute {
		t.Fatalf("waits %v, want 1h30m", w.d)
	}
	w.ch <- runnerNow
	runs := env.waitForRuns(t, 1)
	if runs[0].Trigger != "schedule" {
		t.Errorf("trigger = %q, want schedule", runs[0].Trigger)
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
	w.ch <- runnerNow
	env.timer.next(t) // the scheduler is waiting for the following slot

	if !strings.Contains(env.logs.String(), "backup skipped: previous run still in progress") {
		t.Errorf("log lacks skip message: %s", env.logs.String())
	}
	runs, err := env.deps.Runs.Recent(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Errorf("a busy slot queued %d runs", len(runs))
	}
}

func TestSchedulerReloadsOnConfigChange(t *testing.T) {
	env := startScheduler(t, scheduledConfig(t, "04:30"))
	first := env.timer.next(t)
	if first.d != 90*time.Minute {
		t.Fatalf("first wait %v", first.d)
	}

	cfg := scheduledConfig(t, "05:00")
	cfg.Local = env.mustLoad(t).Local
	if err := env.deps.Config.Save(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	env.sched.Reload()
	second := env.timer.next(t)
	if second.d != 2*time.Hour {
		t.Fatalf("after reload waits %v, want 2h", second.d)
	}

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
