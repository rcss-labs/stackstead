package host

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
func (c *cached[T]) wait() {
	c.mu.Lock()
	f := c.inflight
	c.mu.Unlock()
	if f != nil {
		<-f.done
	}
}

func TestGetSingleFlight(t *testing.T) {
	clock := newFakeClock()
	c := newCached[int](clock.Now)
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
	c := newCached[int](clock.Now)
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
	c := newCached[int](clock.Now)
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
	c := newCached[int](clock.Now)
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
	c := newCached[int](clock.Now)
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
