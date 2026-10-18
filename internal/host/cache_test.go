package host

import (
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/stale"
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

func waitForRefresh[T any](t *testing.T, c *stale.Value[T]) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.Metadata().Refreshing {
		if time.Now().After(deadline) {
			t.Fatal("cache refresh did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}
