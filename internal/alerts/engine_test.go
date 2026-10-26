package alerts_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/alerts"
	"github.com/rcss-labs/stackstead/internal/backup"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

type fakeDetails struct {
	mu       sync.Mutex
	details  []stacks.Detail
	err      error
	calls    atomic.Int32
	gate     chan struct{} // when set, Details blocks until closed
	entered  chan struct{} // receives once per call when set
	honorCtx bool          // fail with ctx.Err() after the gate when ctx is done
	panicNow atomic.Bool   // the next call panics, once
}

func (f *fakeDetails) Details(ctx context.Context) ([]stacks.Detail, error) {
	f.calls.Add(1)
	if f.panicNow.CompareAndSwap(true, false) {
		panic("details exploded")
	}
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.gate != nil {
		<-f.gate
	}
	if f.honorCtx {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.details, f.err
}

type fakeContainers struct {
	mu         sync.Mutex
	containers []engine.Container
	err        error
}

func (f *fakeContainers) Containers(context.Context) ([]engine.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.containers, f.err
}

func (f *fakeContainers) set(cs ...engine.Container) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers = cs
}

type fakeRestarts map[string]int

func (f fakeRestarts) RestartCounts() map[string]int { return f }

type connFlag struct{ v atomic.Bool }

func (f *connFlag) Connected() bool { return f.v.Load() }

type degradedFlag struct{ v atomic.Bool }

func (f *degradedFlag) Degraded() bool { return f.v.Load() }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type rig struct {
	det       *fakeDetails
	cont      *fakeContainers
	restarts  fakeRestarts
	connected *connFlag
	degraded  *degradedFlag
	clock     *clock
	engine    *alerts.Engine
}

func newRig() *rig {
	r := &rig{
		det:       &fakeDetails{},
		cont:      &fakeContainers{},
		restarts:  fakeRestarts{},
		connected: &connFlag{},
		degraded:  &degradedFlag{},
		clock:     &clock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)},
	}
	r.connected.v.Store(true)
	r.det.details = []stacks.Detail{stack("s", "s", svc("web", ""))}
	r.engine = alerts.NewEngine(r.det, r.cont, r.restarts, r.connected, r.degraded, r.clock.now, nil)
	return r
}

func (r *rig) get(t *testing.T) []alerts.Alert {
	t.Helper()
	got, err := r.engine.Alerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestSeverityOrder(t *testing.T) {
	r := newRig()
	r.degraded.v.Store(true) // metrics_degraded: warning
	r.det.details = []stacks.Detail{stack("s", "s", svc("web", ""))}
	r.cont.set(ctr("c1", "s-web-1", "s", "web", "running", "Up 1 minute (unhealthy)")) // danger
	first := r.get(t)
	if len(first) != 2 || first[0].Severity != "danger" || first[1].Severity != "warning" {
		t.Fatalf("danger must precede warning: %+v", first)
	}

	// Same severity: newer since first. Bring a second danger alert later.
	r.clock.advance(10 * time.Second)
	r.connected.v.Store(false) // docker_disconnected: danger, newer
	got := r.get(t)
	if len(got) != 3 || got[0].Kind != "docker_disconnected" || got[1].Kind != "unhealthy" || got[2].Kind != "metrics_degraded" {
		t.Fatalf("order = %+v", got)
	}
}

func TestSinceStableAcrossComputations(t *testing.T) {
	r := newRig()
	r.cont.set(ctr("c1", "s-web-1", "s", "web", "running", "Up 1 minute (unhealthy)"))
	first := r.get(t)
	r.clock.advance(time.Minute)
	second := r.get(t)
	if len(first) != 1 || len(second) != 1 || !first[0].Since.Equal(r.clock.t.Add(-time.Minute)) || !second[0].Since.Equal(first[0].Since) {
		t.Errorf("since changed: %+v then %+v", first, second)
	}
}

func TestClearedAlertForgotten(t *testing.T) {
	r := newRig()
	bad := ctr("c1", "s-web-1", "s", "web", "running", "Up 1 minute (unhealthy)")
	r.cont.set(bad)
	first := r.get(t)
	r.clock.advance(time.Minute)
	r.cont.set(ctr("c1", "s-web-1", "s", "web", "running", "Up 2 minutes (healthy)"))
	if got := r.get(t); len(got) != 0 {
		t.Fatalf("cleared alert still reported: %+v", got)
	}
	r.clock.advance(time.Minute)
	r.cont.set(bad)
	back := r.get(t)
	if len(back) != 1 || !back[0].Since.After(first[0].Since) {
		t.Errorf("returning alert must get a new since: first %v, back %+v", first[0].Since, back)
	}
}

func TestRecreatedContainerKeepsKey(t *testing.T) {
	r := newRig()
	r.cont.set(ctr("old-id", "s-web-1", "s", "web", "running", "Up 1 minute (unhealthy)"))
	first := r.get(t)
	r.clock.advance(time.Minute)
	r.cont.set(ctr("new-id", "s-web-1", "s", "web", "running", "Up 1 second (unhealthy)"))
	second := r.get(t)
	if len(second) != 1 || !second[0].Since.Equal(first[0].Since) {
		t.Errorf("recreated container changed since: %+v then %+v", first, second)
	}
}

func TestRestartCountsMapIDToName(t *testing.T) {
	r := newRig()
	r.restarts["c1"] = 4
	r.cont.set(ctr("c1", "s-web-1", "s", "web", "running", "Up 5 seconds"))
	got := r.get(t)
	if len(got) != 1 || got[0].Kind != "restart_loop" || got[0].Container != "s-web-1" {
		t.Errorf("got %+v", got)
	}
}

func TestAlertsCachedFor5s(t *testing.T) {
	r := newRig()
	r.get(t)
	r.clock.advance(4 * time.Second)
	r.get(t)
	if n := r.det.calls.Load(); n != 1 {
		t.Fatalf("Details calls within 5s = %d, want 1", n)
	}
	r.clock.advance(time.Second)
	r.get(t)
	if n := r.det.calls.Load(); n != 2 {
		t.Errorf("Details calls after 5s = %d, want 2", n)
	}
}

func TestAlertsSingleFlight(t *testing.T) {
	r := newRig()
	r.det.gate = make(chan struct{})
	r.det.entered = make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.engine.Alerts(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	<-r.det.entered // one computation is in flight; the others join or hit the cache
	close(r.det.gate)
	wg.Wait()
	if n := r.det.calls.Load(); n != 1 {
		t.Errorf("Details calls = %d, want 1", n)
	}
}

func TestDetailsErrorNotCached(t *testing.T) {
	r := newRig()
	r.det.err = errors.New("registry down")
	got, err := r.engine.Alerts(context.Background())
	if err == nil || got != nil {
		t.Fatalf("got %v, %v; want nil, error", got, err)
	}
	r.det.mu.Lock()
	r.det.err = nil
	r.det.mu.Unlock()
	if _, err := r.engine.Alerts(context.Background()); err != nil {
		t.Fatalf("error was cached: %v", err)
	}
	if n := r.det.calls.Load(); n != 2 {
		t.Errorf("Details calls = %d, want 2", n)
	}
}

func TestDisconnectedStillReportsFileAlerts(t *testing.T) {
	r := newRig()
	r.connected.v.Store(false)
	r.cont.err = engine.ErrUnavailable
	d := stack("s", "s", svc("web", ""))
	d.ParseError = "broken"
	r.det.details = []stacks.Detail{d}
	got := r.get(t)
	kinds := map[string]bool{}
	for _, a := range got {
		kinds[a.Kind] = true
	}
	if len(got) != 2 || !kinds["docker_disconnected"] || !kinds["parse_error"] {
		t.Errorf("got %+v", got)
	}
}

func TestOverrideAndClashAlertsKeepTheirOwnSince(t *testing.T) {
	r := newRig()
	d := stack("s", "s", svc("web", "", "8080:80"))
	d.Overrides = []string{"compose.override.yaml"}
	r.det.details = []stacks.Detail{d}
	r.cont.set(ctr("x1", "legacy-a", "", "", "running", "Up", published(8080)))
	t1 := r.clock.now()
	first := r.get(t)
	if len(first) != 2 {
		t.Fatalf("first = %+v", first)
	}

	r.clock.advance(time.Minute)
	t2 := r.clock.now()
	d.Overrides = []string{"compose.override.yaml", "docker-compose.override.yml"}
	r.det.mu.Lock()
	r.det.details = []stacks.Detail{d}
	r.det.mu.Unlock()
	r.cont.set(ctr("x1", "legacy-a", "", "", "running", "Up", published(8080)),
		ctr("x2", "legacy-b", "", "", "running", "Up", published(8080)))
	got := r.get(t)
	if len(got) != 4 {
		t.Fatalf("got %+v", got)
	}
	for _, a := range got {
		wantSince := t2
		if a.Message == "compose.override.yaml found; override files are not supported yet" ||
			a.Message == "web wants 8080 but a non-compose container (legacy-a) holds it" {
			wantSince = t1
		}
		if !a.Since.Equal(wantSince) {
			t.Errorf("%q since = %v, want %v", a.Message, a.Since, wantSince)
		}
	}
}

func TestContainerAlertSinceSurvivesDisconnect(t *testing.T) {
	r := newRig()
	r.cont.set(ctr("c1", "s-web-1", "s", "web", "running", "Up 1 minute (unhealthy)"))
	t1 := r.clock.now()
	r.get(t)

	r.clock.advance(time.Minute)
	r.connected.v.Store(false)
	r.cont.mu.Lock()
	r.cont.err = engine.ErrUnavailable
	r.cont.mu.Unlock()
	got := r.get(t)
	if len(got) != 1 || got[0].Kind != "docker_disconnected" {
		t.Fatalf("while disconnected: %+v", got)
	}

	r.clock.advance(time.Minute)
	r.connected.v.Store(true)
	r.cont.mu.Lock()
	r.cont.err = nil
	r.cont.mu.Unlock()
	got = r.get(t)
	if len(got) != 1 || got[0].Kind != "unhealthy" || !got[0].Since.Equal(t1) {
		t.Errorf("after reconnect: %+v, want since %v", got, t1)
	}
}

func TestCancelledLeaderDoesNotFailJoiners(t *testing.T) {
	r := newRig()
	r.det.honorCtx = true
	r.det.gate = make(chan struct{})
	r.det.entered = make(chan struct{}, 4)
	leaderCtx, cancel := context.WithCancel(context.Background())
	type result struct {
		a   []alerts.Alert
		err error
	}
	leader, joiner := make(chan result, 1), make(chan result, 1)
	go func() {
		a, err := r.engine.Alerts(leaderCtx)
		leader <- result{a, err}
	}()
	<-r.det.entered
	go func() {
		a, err := r.engine.Alerts(context.Background())
		joiner <- result{a, err}
	}()
	cancel()
	close(r.det.gate)
	if res := <-joiner; res.err != nil {
		t.Errorf("joiner failed: %v", res.err)
	}
	if res := <-leader; res.err != nil {
		t.Errorf("leader computation must outlive its request: %v", res.err)
	}
}

func TestPanickingComputeBecomesErrorAndEngineRecovers(t *testing.T) {
	r := newRig()
	r.det.panicNow.Store(true)
	var got []alerts.Alert
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("panic escaped Alerts: %v", p)
			}
		}()
		got, err = r.engine.Alerts(context.Background())
	}()
	if err == nil || got != nil {
		t.Fatalf("got %v, %v; want nil, error", got, err)
	}
	done := make(chan error, 1)
	go func() { _, err := r.engine.Alerts(context.Background()); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("engine did not recover after the panic: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("later Alerts call hung: the failed flight was never cleared")
	}
}

type fakeBackupHealth struct {
	run backup.Run
	ok  bool
}

func (f fakeBackupHealth) LastRun(context.Context) (backup.Run, bool, error) { return f.run, f.ok, nil }

func backupAlerts(t *testing.T, src alerts.BackupHealth) []alerts.Alert {
	t.Helper()
	r := newRig()
	r.engine = alerts.NewEngine(r.det, r.cont, r.restarts, r.connected, r.degraded, r.clock.now, src)
	return r.get(t)
}

func TestAlertBackupFailed(t *testing.T) {
	ended := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	got := backupAlerts(t, fakeBackupHealth{ok: true, run: backup.Run{Status: "failed", Error: "disk full", EndedAt: ended}})
	if len(got) != 1 || got[0].Kind != alerts.KindBackupFailed || got[0].Severity != alerts.SeverityDanger ||
		got[0].Message != "Last backup failed: disk full" || !got[0].Since.Equal(ended) {
		t.Fatalf("got %+v", got)
	}
}

func TestAlertBackupPartial(t *testing.T) {
	ended := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	run := backup.Run{Status: "partial", EndedAt: ended, Items: []backup.ItemResult{
		{Status: "ok"}, {Status: "failed"}, {Status: "failed"}, {Status: "skipped"}}}
	got := backupAlerts(t, fakeBackupHealth{ok: true, run: run})
	if len(got) != 1 || got[0].Kind != alerts.KindBackupPartial || got[0].Severity != alerts.SeverityWarning ||
		got[0].Message != "Last backup was partial: 2 item(s) failed" || !got[0].Since.Equal(ended) {
		t.Fatalf("got %+v", got)
	}
}

func TestNoBackupAlertWhenSuccessOrNoRuns(t *testing.T) {
	for name, src := range map[string]alerts.BackupHealth{
		"success": fakeBackupHealth{ok: true, run: backup.Run{Status: "success"}},
		"no runs": fakeBackupHealth{},
		"nil":     nil,
	} {
		if got := backupAlerts(t, src); len(got) != 0 {
			t.Errorf("%s: unexpected alerts %+v", name, got)
		}
	}
}
