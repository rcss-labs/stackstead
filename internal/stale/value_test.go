package stale

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

// wait blocks until the running load, if any, has published its result.
func (c *Value[T]) wait() {
	c.mu.Lock()
	f := c.inflight
	c.mu.Unlock()
	if f != nil {
		<-f.done
	}
}

func TestGetSingleFlight(t *testing.T) {
	clock := newFakeClock()
	c := New[int](clock.Now)
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	load := func(context.Context) (int, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return 7, nil
	}

	const n = 50
	var wg sync.WaitGroup
	results := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.Get(context.Background(), 5*time.Second, load)
			if err != nil {
				t.Error(err)
			}
			results[i] = v
		}()
	}
	<-entered
	close(release)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("load called %d times, want 1", got)
	}
	for i, v := range results {
		if v != 7 {
			t.Fatalf("result %d = %d, want 7", i, v)
		}
	}
}

func TestGetStaleOneRefresh(t *testing.T) {
	clock := newFakeClock()
	c := New[int](clock.Now)
	var calls atomic.Int32
	started := make(chan struct{}, 4)
	gate := make(chan struct{})
	load := func() (int, error) {
		n := int(calls.Add(1))
		if n == 1 {
			return 1, nil
		}
		started <- struct{}{}
		<-gate
		return n, nil
	}

	if _, ok := c.GetStale(time.Minute, load); ok {
		t.Fatal("value reported before first load")
	}
	c.wait()
	if v, ok := c.GetStale(time.Minute, load); !ok || v != 1 {
		t.Fatalf("got (%d,%v), want (1,true)", v, ok)
	}
	clock.Advance(2 * time.Minute)
	for i := 0; i < 20; i++ {
		if v, ok := c.GetStale(time.Minute, load); !ok || v != 1 {
			t.Fatalf("stale read %d = (%d,%v), want (1,true)", i, v, ok)
		}
	}
	<-started
	close(gate)
	c.wait()
	if got := calls.Load(); got != 2 {
		t.Fatalf("load called %d times, want 2", got)
	}
	if v, _ := c.GetStale(time.Minute, load); v != 2 {
		t.Fatalf("after refresh got %d, want 2", v)
	}
}

func TestGetStaleRefreshFailureKeepsOld(t *testing.T) {
	clock := newFakeClock()
	c := New[int](clock.Now)
	fail := false
	var calls atomic.Int32
	load := func() (int, error) {
		calls.Add(1)
		if fail {
			return 0, errors.New("boom")
		}
		return 1, nil
	}
	c.GetStale(time.Minute, load)
	c.wait()
	fail = true
	clock.Advance(2 * time.Minute)
	c.GetStale(time.Minute, load)
	c.wait()
	if v, ok := c.GetStale(time.Minute, load); !ok || v != 1 {
		t.Fatalf("got (%d,%v), want old value (1,true)", v, ok)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("failed refresh retried immediately: %d calls, want 2", got)
	}
}

func TestGetStaleFailedFirstRefreshWaitsForMaxAge(t *testing.T) {
	clock := newFakeClock()
	c := New[int](clock.Now)
	var calls atomic.Int32
	load := func() (int, error) {
		if calls.Add(1) == 1 {
			return 0, errors.New("down")
		}
		return 9, nil
	}
	c.GetStale(time.Minute, load)
	c.wait()
	clock.Advance(55 * time.Second)
	c.GetStale(time.Minute, load)
	c.wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("retried within maxAge after a failure: %d calls", got)
	}
	clock.Advance(6 * time.Second)
	c.GetStale(time.Minute, load)
	c.wait()
	if v, ok := c.GetStale(time.Minute, load); !ok || v != 9 {
		t.Fatalf("got (%d,%v), want (9,true)", v, ok)
	}
}

func TestGetStalePanicKeepsOld(t *testing.T) {
	clock := newFakeClock()
	c := New[int](clock.Now)
	boom := false
	load := func() (int, error) {
		if boom {
			panic("loader exploded")
		}
		return 1, nil
	}
	c.GetStale(time.Minute, load)
	c.wait()
	boom = true
	clock.Advance(2 * time.Minute)
	c.GetStale(time.Minute, load)
	c.wait()
	if v, ok := c.GetStale(time.Minute, load); !ok || v != 1 {
		t.Fatalf("got (%d,%v), want old value (1,true)", v, ok)
	}
}

func TestMetadataTracksRefreshAndSuccessfulLoadTime(t *testing.T) {
	clock := newFakeClock()
	c := New[int](clock.Now)
	if m := c.Metadata(); !m.LoadedAt.IsZero() || m.Refreshing || m.LastError != nil {
		t.Fatalf("initial metadata = %+v", m)
	}
	gate := make(chan struct{})
	c.GetStale(time.Minute, func() (int, error) { <-gate; return 7, nil })
	if m := c.Metadata(); !m.Refreshing || !m.LoadedAt.IsZero() {
		t.Fatalf("loading metadata = %+v", m)
	}
	clock.Advance(time.Second)
	close(gate)
	c.wait()
	if m := c.Metadata(); m.Refreshing || !m.LoadedAt.Equal(clock.Now()) || m.LastError != nil {
		t.Fatalf("loaded metadata = %+v", m)
	}
}

func TestMetadataErrorClearedBySuccess(t *testing.T) {
	clock := newFakeClock()
	c := New[int](clock.Now)
	failure := errors.New("engine down")
	c.GetStale(time.Minute, func() (int, error) { return 0, failure })
	c.wait()
	if m := c.Metadata(); !errors.Is(m.LastError, failure) || !m.LoadedAt.IsZero() {
		t.Fatalf("failed metadata = %+v", m)
	}
	clock.Advance(time.Minute)
	c.GetStale(time.Minute, func() (int, error) { return 8, nil })
	c.wait()
	if m := c.Metadata(); m.LastError != nil || !m.LoadedAt.Equal(clock.Now()) {
		t.Fatalf("recovered metadata = %+v", m)
	}
}

func TestInvalidateForcesBackgroundLoad(t *testing.T) {
	clock := newFakeClock()
	c := New[int](clock.Now)
	load := func() (int, error) { return 1, nil }
	c.GetStale(time.Minute, load)
	c.wait()
	c.Invalidate()
	gate := make(chan struct{})
	if v, ok := c.GetStale(time.Minute, func() (int, error) { <-gate; return 2, nil }); !ok || v != 1 {
		t.Fatalf("invalidated read = (%d,%v), want stale (1,true)", v, ok)
	}
	if !c.Metadata().Refreshing {
		t.Fatal("invalidation did not start refresh")
	}
	close(gate)
	c.wait()
	if v, ok := c.GetStale(time.Minute, load); !ok || v != 2 {
		t.Fatalf("refreshed read = (%d,%v), want (2,true)", v, ok)
	}
}

type observedContext struct {
	context.Context
	waiting chan struct{}
}

func (c observedContext) Done() <-chan struct{} {
	close(c.waiting)
	return c.Context.Done()
}

func TestFreshGetSharesBackgroundFlight(t *testing.T) {
	c := New[int](time.Now)
	gate := make(chan struct{})
	c.GetStale(time.Minute, func() (int, error) { <-gate; return 9, nil })
	result := make(chan int, 1)
	waiting := make(chan struct{})
	go func() {
		v, err := c.Get(observedContext{Context: context.Background(), waiting: waiting}, 0, func(context.Context) (int, error) {
			t.Error("fresh read started a second load")
			return 0, nil
		})
		if err != nil {
			t.Error(err)
		}
		result <- v
	}()
	<-waiting
	// A second waiter can cancel without canceling the shared load.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Get(ctx, 0, func(context.Context) (int, error) {
		t.Error("canceled waiter started a second load")
		return 0, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting error = %v, want canceled", err)
	}
	close(gate)
	if got := <-result; got != 9 {
		t.Fatalf("fresh read = %d, want 9", got)
	}
}

func TestSuccessfulFreshGetRefreshesStaleAge(t *testing.T) {
	clock := newFakeClock()
	c := New[int](clock.Now)
	if _, err := c.Get(context.Background(), 0, func(context.Context) (int, error) { return 7, nil }); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	if v, ok := c.GetStale(time.Minute, func() (int, error) { calls.Add(1); return 8, nil }); !ok || v != 7 {
		t.Fatalf("stale read = (%d,%v), want (7,true)", v, ok)
	}
	c.wait()
	if calls.Load() != 0 {
		t.Fatal("stale read reloaded a freshly fetched value")
	}
}
