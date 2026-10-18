package storage

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

type serviceEngine struct {
	containers []engine.Container
	images     atomic.Int32
	list       func(context.Context) error
	df         func(context.Context, engine.DFKind) (engine.DFSnapshot, error)
}

func (e *serviceEngine) Images(ctx context.Context) ([]engine.Image, error) {
	e.images.Add(1)
	if e.list != nil {
		if err := e.list(ctx); err != nil {
			return nil, err
		}
	}
	return []engine.Image{{ID: "image"}}, nil
}
func (e *serviceEngine) Volumes(context.Context) ([]engine.Volume, error) { return nil, nil }
func (e *serviceEngine) Containers(context.Context) ([]engine.Container, error) {
	return e.containers, nil
}
func (e *serviceEngine) DiskUsage(ctx context.Context, k engine.DFKind) (engine.DFSnapshot, error) {
	return e.df(ctx, k)
}

type serviceRegistry []stacks.Entry

func (r serviceRegistry) List(context.Context) ([]stacks.Entry, error) { return r, nil }

type serviceDeclarations func(context.Context, stacks.Entry) StackDeclarations

func (f serviceDeclarations) Get(c context.Context, e stacks.Entry) StackDeclarations { return f(c, e) }
func newTestService(e *serviceEngine, now func() time.Time) *Service {
	return NewService(ServiceConfig{Engine: e, Registry: serviceRegistry{}, Now: now})
}
func waitService(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}
func typedDF(_ context.Context, k engine.DFKind) (engine.DFSnapshot, error) {
	return engine.DFSnapshot{Kinds: []engine.DFKind{k}}, nil
}

func TestServiceInventoryNeverWaitsForDFAndSharesLists(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	e := &serviceEngine{df: func(ctx context.Context, k engine.DFKind) (engine.DFSnapshot, error) { <-gate; return typedDF(ctx, k) }}
	var ticks atomic.Int64
	base := time.Now()
	s := newTestService(e, func() time.Time { return base.Add(time.Duration(ticks.Load())) })
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			v, err := s.Inventory(ctx)
			if err != nil || len(v.Images) != 1 {
				t.Errorf("inventory: %+v %v", v, err)
			}
			if !v.Totals[engine.DFImages].Refreshing {
				t.Error("missing refreshing metadata")
			}
		})
	}
	wg.Wait()
	if n := e.images.Load(); n != 1 {
		t.Fatalf("image list calls=%d", n)
	}
	ticks.Store(int64(2 * time.Second))
	if _, err := s.Inventory(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := e.images.Load(); n != 2 {
		t.Fatalf("expired list calls=%d", n)
	}
}
func TestServiceListsDetachCallerAndDoNotCacheErrors(t *testing.T) {
	in := make(chan context.Context, 1)
	gate := make(chan struct{})
	e := &serviceEngine{df: typedDF, list: func(ctx context.Context) error { in <- ctx; <-gate; return errors.New("unavailable") }}
	s := newTestService(e, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Inventory(ctx); done <- err }()
	loadCtx := <-in
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("caller cancellation blocked")
	}
	if loadCtx.Err() != nil {
		t.Fatal("shared load inherited caller cancellation")
	}
	dl, ok := loadCtx.Deadline()
	if !ok || time.Until(dl) > 10*time.Second {
		t.Fatal("list load lacks ten second bound")
	}
	close(gate)
	waitService(t, func() bool { return s.lists.Metadata().LastError != nil })
	if _, err := s.Inventory(context.Background()); err == nil {
		t.Fatal("expected Docker error")
	}
	if e.images.Load() != 2 {
		t.Fatal("error was cached")
	}
}
func TestServiceFullFallbackPopulatesAllKindsOnce(t *testing.T) {
	var calls atomic.Int32
	e := &serviceEngine{df: func(context.Context, engine.DFKind) (engine.DFSnapshot, error) {
		calls.Add(1)
		return engine.DFSnapshot{Kinds: dfKinds, Volumes: []engine.DFVolume{{Name: "v", UsageData: &engine.DFVolumeUsage{Size: 42}}}}, nil
	}}
	s := newTestService(e, time.Now)
	if _, err := s.Inventory(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitService(t, func() bool { v, ok := s.VolumeSummary(); return ok && v.Size == 42 })
	v, err := s.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range dfKinds {
		if v.Totals[k].SizesAt == nil || v.Totals[k].Refreshing {
			t.Fatalf("kind %s: %+v", k, v.Totals[k])
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("full walks=%d", calls.Load())
	}
}
func TestServiceFreshDFSharesFlightAndStoresResult(t *testing.T) {
	in := make(chan struct{}, 8)
	gate := make(chan struct{})
	var calls atomic.Int32
	e := &serviceEngine{df: func(ctx context.Context, k engine.DFKind) (engine.DFSnapshot, error) {
		if k == engine.DFImages {
			calls.Add(1)
		}
		in <- struct{}{}
		<-gate
		return typedDF(ctx, k)
	}}
	s := newTestService(e, time.Now)
	s.Inventory(context.Background())
	<-in
	done := make(chan error, 1)
	entered := make(chan struct{})
	waiter := &observedContext{Context: context.Background(), entered: entered}
	go func() { _, err := s.FreshDF(waiter, engine.DFImages); done <- err }()
	<-entered
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("shared image df calls=%d", n)
	}
	// Other kinds may run after typed discovery; only the image must share its load.
	r := s.DFReadings()[engine.DFImages]
	if r.LoadedAt.IsZero() || r.Refreshing {
		t.Fatalf("fresh reading not published: %+v", r)
	}
}
func TestServiceManualRefreshRateAndStaleAge(t *testing.T) {
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	var calls atomic.Int32
	e := &serviceEngine{df: func(ctx context.Context, k engine.DFKind) (engine.DFSnapshot, error) {
		calls.Add(1)
		return engine.DFSnapshot{Kinds: dfKinds}, nil
	}}
	s := newTestService(e, clock)
	s.Inventory(context.Background())
	waitService(t, func() bool { return !s.DFReadings()[engine.DFImages].LoadedAt.IsZero() })
	advance(4 * time.Minute)
	s.Inventory(context.Background())
	if calls.Load() != 1 {
		t.Fatal("refreshed before five minutes")
	}
	if wait := s.Refresh(); wait != 0 {
		t.Fatal(wait)
	}
	advance(30 * time.Second)
	if wait := s.Refresh(); wait != 30*time.Second {
		t.Fatalf("retry=%s", wait)
	}
	s.Inventory(context.Background())
	waitService(t, func() bool { return calls.Load() == 2 })
	advance(30 * time.Second)
	if wait := s.Refresh(); wait != 0 {
		t.Fatal(wait)
	}
}
func TestServiceDeclarationsUseLiveStateAndKeepRegisteredProtection(t *testing.T) {
	e := &serviceEngine{df: typedDF}
	s := NewService(ServiceConfig{Engine: e, Registry: serviceRegistry{{Name: "app", Project: "registered"}}, Declarations: serviceDeclarations(func(_ context.Context, e stacks.Entry) StackDeclarations {
		return StackDeclarations{Name: e.Name, Project: e.Project}
	})})
	in, err := s.Inputs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if in.RegisteredProjects["registered"] != "app" || in.Stacks[0].State != "not-deployed" || in.Stacks[0].Known {
		t.Fatalf("inputs=%+v", in)
	}
}

// Observes entry into the wait select so the test releases an actual in-flight
// reader rather than racing the scheduler against a completed reading.
type observedContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *observedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestServiceFullFallbackRemainsSharedAfterExpiry(t *testing.T) {
	var ticks atomic.Int64
	base := time.Now()
	var calls atomic.Int32
	e := &serviceEngine{df: func(context.Context, engine.DFKind) (engine.DFSnapshot, error) {
		calls.Add(1)
		return engine.DFSnapshot{Kinds: dfKinds}, nil
	}}
	s := newTestService(e, func() time.Time { return base.Add(time.Duration(ticks.Load())) })
	s.Inventory(context.Background())
	waitService(t, func() bool { return !s.DFReadings()[engine.DFVolumes].LoadedAt.IsZero() })
	ticks.Store(int64(5 * time.Minute))
	s.Inventory(context.Background())
	waitService(t, func() bool {
		for _, r := range s.DFReadings() {
			if r.Refreshing {
				return false
			}
		}
		return true
	})
	if n := calls.Load(); n != 2 {
		t.Fatalf("full walks across two refreshes=%d, want 2", n)
	}
}
func TestServiceTypedVolumeDoesNotBlockImagesAndCanceledDiscoveryWaiter(t *testing.T) {
	imageIn := make(chan struct{}, 2)
	imageGate := make(chan struct{})
	volumeGate := make(chan struct{})
	defer close(volumeGate)
	e := &serviceEngine{df: func(ctx context.Context, k engine.DFKind) (engine.DFSnapshot, error) {
		if k == engine.DFImages {
			imageIn <- struct{}{}
			<-imageGate
		}
		if k == engine.DFVolumes {
			<-volumeGate
		}
		return typedDF(ctx, k)
	}}
	s := newTestService(e, time.Now)
	s.Inventory(context.Background())
	<-imageIn
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.FreshDF(ctx, engine.DFVolumes); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(imageGate)
	waitService(t, func() bool { return !s.DFReadings()[engine.DFImages].LoadedAt.IsZero() })
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.FreshDF(ctx, engine.DFImages); err != nil {
		t.Fatalf("image blocked behind volume: %v", err)
	}
	if !s.DFReadings()[engine.DFVolumes].Refreshing {
		t.Fatal("volume load unexpectedly finished")
	}
}
func TestServiceListBudgetDoesNotWaitForComposeCleanup(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	s := NewService(ServiceConfig{Engine: &serviceEngine{df: typedDF}, Registry: serviceRegistry{{Name: "app", Project: "app"}}, ListTimeout: 20 * time.Millisecond, Declarations: serviceDeclarations(func(ctx context.Context, e stacks.Entry) StackDeclarations {
		<-ctx.Done()
		<-gate
		return StackDeclarations{Name: e.Name}
	})})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.Inputs(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("list budget: %v", err)
	}
}

func TestServiceFullFailurePreservesSizesAndReportsEveryKind(t *testing.T) {
	var ticks atomic.Int64
	base := time.Now()
	var calls atomic.Int32
	e := &serviceEngine{df: func(context.Context, engine.DFKind) (engine.DFSnapshot, error) {
		if calls.Add(1) > 1 {
			return engine.DFSnapshot{}, errors.New("df unavailable")
		}
		return engine.DFSnapshot{Kinds: dfKinds, Volumes: []engine.DFVolume{{Name: "v", UsageData: &engine.DFVolumeUsage{Size: 42}}}}, nil
	}}
	s := newTestService(e, func() time.Time { return base.Add(time.Duration(ticks.Load())) })
	s.VolumeSummary()
	waitService(t, func() bool { _, ok := s.VolumeSummary(); return ok })
	ticks.Store(int64(5 * time.Minute))
	s.DFReadings()
	waitService(t, func() bool { return s.DFReadings()[engine.DFImages].Error != "" })
	for k, r := range s.DFReadings() {
		if r.Error != "df unavailable" || r.LoadedAt != base {
			t.Fatalf("kind %s lost error/freshness: %+v", k, r)
		}
	}
	if v, ok := s.VolumeSummary(); !ok || v.Size != 42 {
		t.Fatalf("stale volume summary: %+v %v", v, ok)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("failure hammered full df: %d", n)
	}
}
func TestServiceDFDeadlineAndMetadata(t *testing.T) {
	var budget time.Duration
	e := &serviceEngine{df: func(ctx context.Context, _ engine.DFKind) (engine.DFSnapshot, error) {
		dl, _ := ctx.Deadline()
		budget = time.Until(dl)
		<-ctx.Done()
		return engine.DFSnapshot{}, ctx.Err()
	}}
	s := NewService(ServiceConfig{Engine: e, DFTimeout: 20 * time.Millisecond})
	if _, err := s.FreshDF(context.Background(), engine.DFImages); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if budget <= 0 || budget > 20*time.Millisecond {
		t.Fatalf("df budget=%s", budget)
	}
	r := s.DFReadings()[engine.DFImages]
	if r.Refreshing || r.Error == "" || !r.LoadedAt.IsZero() {
		t.Fatalf("failed first load metadata=%+v", r)
	}
}

func TestServiceRefreshBeforeInitialFullReadNeedsOnlyOneWalk(t *testing.T) {
	var calls atomic.Int32
	e := &serviceEngine{df: func(context.Context, engine.DFKind) (engine.DFSnapshot, error) {
		calls.Add(1)
		return engine.DFSnapshot{Kinds: dfKinds}, nil
	}}
	s := newTestService(e, time.Now)
	s.Refresh()
	s.Inventory(context.Background())
	waitService(t, func() bool {
		for _, r := range s.DFReadings() {
			if r.Refreshing || r.LoadedAt.IsZero() {
				return false
			}
		}
		return true
	})
	if n := calls.Load(); n != 1 {
		t.Fatalf("initial refresh full walks=%d", n)
	}
}

func TestServiceRecoversAllKindsAfterDiscoveryFailure(t *testing.T) {
	var calls atomic.Int32
	e := &serviceEngine{df: func(ctx context.Context, k engine.DFKind) (engine.DFSnapshot, error) {
		if calls.Add(1) == 1 {
			return engine.DFSnapshot{}, errors.New("offline")
		}
		return typedDF(ctx, k)
	}}
	s := newTestService(e, time.Now)
	if _, err := s.FreshDF(context.Background(), engine.DFImages); err == nil {
		t.Fatal("expected failed discovery")
	}
	s.Refresh()
	s.DFReadings()
	waitService(t, func() bool { r := s.DFReadings()[engine.DFImages]; return !r.LoadedAt.IsZero() && !r.Refreshing })
	waitService(t, func() bool {
		for _, r := range s.DFReadings() {
			if r.LoadedAt.IsZero() || r.Refreshing || r.Error != "" {
				return false
			}
		}
		return true
	})
}

func TestServiceListPanicReturnsErrorAndReleasesFlight(t *testing.T) {
	var calls atomic.Int32
	e := &serviceEngine{df: typedDF, list: func(context.Context) error {
		if calls.Add(1) == 1 {
			panic("list exploded")
		}
		return nil
	}}
	s := newTestService(e, time.Now)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.Inputs(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("panic must return loader error: %v", err)
	}
	if in, err := s.Inputs(ctx); err != nil || len(in.Images) != 1 {
		t.Fatalf("retry after panic: %+v %v", in, err)
	}
	if e.images.Load() != 2 {
		t.Fatalf("list flight not retried: %d", e.images.Load())
	}
}
func TestServiceDFPanicPreservesReadingReleasesFlightAndRecovers(t *testing.T) {
	for _, warm := range []bool{false, true} {
		t.Run(map[bool]string{false: "discovery", true: "cached"}[warm], func(t *testing.T) {
			var calls atomic.Int32
			panicCall := int32(1)
			if warm {
				panicCall = 2
			}
			e := &serviceEngine{df: func(context.Context, engine.DFKind) (engine.DFSnapshot, error) {
				if calls.Add(1) == panicCall {
					panic("df exploded")
				}
				return engine.DFSnapshot{Kinds: dfKinds, Volumes: []engine.DFVolume{{Name: "v", UsageData: &engine.DFVolumeUsage{Size: 42}}}}, nil
			}}
			s := newTestService(e, time.Now)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var loaded time.Time
			if warm {
				if _, err := s.FreshDF(ctx, engine.DFImages); err != nil {
					t.Fatal(err)
				}
				loaded = s.DFReadings()[engine.DFImages].LoadedAt
			}
			if _, err := s.FreshDF(ctx, engine.DFImages); err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("panic must release waiter with loader error: %v", err)
			}
			for k, r := range s.DFReadings() {
				if r.Refreshing || r.Error == "" || r.LoadedAt != loaded {
					t.Fatalf("kind %s after panic: %+v", k, r)
				}
			}
			if warm {
				if v, ok := s.VolumeSummary(); !ok || v.Size != 42 {
					t.Fatalf("panic lost prior sizes: %+v %v", v, ok)
				}
			}
			if n := calls.Load(); n != panicCall {
				t.Fatalf("panic refresh hammered df: %d", n)
			}
			s.Refresh()
			s.DFReadings()
			waitService(t, func() bool {
				for _, r := range s.DFReadings() {
					if r.Refreshing || r.Error != "" || r.LoadedAt.IsZero() {
						return false
					}
				}
				return true
			})
			if n := calls.Load(); n != panicCall+1 {
				t.Fatalf("recovery calls=%d", n)
			}
		})
	}
}
func TestServiceStackStateUsesDeclaredServicesAndCurrentContainers(t *testing.T) {
	ct := func(project, service, state string) engine.Container {
		return engine.Container{State: state, Labels: map[string]string{"com.docker.compose.project": project, "com.docker.compose.service": service}}
	}
	cases := []struct {
		name       string
		containers []engine.Container
		want       string
	}{
		{"undeployed", nil, "not-deployed"},
		{"other project", []engine.Container{ct("other", "web", "running")}, "not-deployed"},
		{"existing stopped", []engine.Container{ct("app", "web", "exited"), ct("app", "db", "created")}, "stopped"},
		{"mixed", []engine.Container{ct("app", "web", "running"), ct("app", "db", "exited")}, "partial"},
		{"missing service", []engine.Container{ct("app", "web", "running")}, "partial"},
		{"duplicate cannot stand in for missing", []engine.Container{ct("app", "web", "running"), ct("app", "web", "running")}, "partial"},
		{"running", []engine.Container{ct("app", "web", "running"), ct("app", "db", "running")}, "running"},
		{"recreated old exited", []engine.Container{ct("app", "web", "exited"), ct("app", "web", "running"), ct("app", "db", "running")}, "running"},
		{"orphan does not replace missing", []engine.Container{ct("app", "orphan", "running")}, "partial"},
		{"stopped orphan does not break running", []engine.Container{ct("app", "web", "running"), ct("app", "db", "running"), ct("app", "orphan", "exited")}, "running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &serviceEngine{df: typedDF, containers: tc.containers}
			s := NewService(ServiceConfig{Engine: e, Registry: serviceRegistry{{Name: "app", Project: "app"}}, Declarations: serviceDeclarations(func(context.Context, stacks.Entry) StackDeclarations {
				return StackDeclarations{Name: "app", Project: "app", Known: true, Images: []DeclaredImage{{Service: "web"}, {Service: "db"}}}
			})})
			in, err := s.Inputs(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got := in.Stacks[0].State; got != tc.want {
				t.Fatalf("state=%q want %q", got, tc.want)
			}
		})
	}
}
