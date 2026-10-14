package auth

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestLimiter() (*LoginLimiter, *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return NewLoginLimiter(3, 15*time.Minute, clock.Now), clock
}

func TestLimiterBlocksAfterMaxAttempts(t *testing.T) {
	l, _ := newTestLimiter()
	for i := range 3 {
		if !l.Attempt("10.0.0.1") {
			t.Fatalf("attempt %d blocked, want allowed until 3", i+1)
		}
	}
	if l.Attempt("10.0.0.1") {
		t.Error("4th attempt allowed, want blocked")
	}
}

func TestLimiterUnblocksAfterWindow(t *testing.T) {
	l, clock := newTestLimiter()
	for range 3 {
		l.Attempt("10.0.0.1")
	}
	clock.Advance(15 * time.Minute)
	if !l.Attempt("10.0.0.1") {
		t.Error("still blocked after the window elapsed")
	}
}

func TestLimiterResetClearsAttempts(t *testing.T) {
	l, _ := newTestLimiter()
	for range 2 {
		l.Attempt("10.0.0.1")
	}
	l.Reset("10.0.0.1")
	for i := range 3 {
		if !l.Attempt("10.0.0.1") {
			t.Fatalf("attempt %d blocked after reset, want allowed", i+1)
		}
	}
}

func TestLimiterTracksIPsIndependently(t *testing.T) {
	l, _ := newTestLimiter()
	for range 3 {
		l.Attempt("10.0.0.1")
	}
	if !l.Attempt("10.0.0.2") {
		t.Error("other IP blocked by 10.0.0.1's attempts")
	}
}

func TestLimiterConcurrentAttemptsNeverExceedMax(t *testing.T) {
	l, _ := newTestLimiter()
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Attempt("10.0.0.1") {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 3 {
		t.Errorf("%d concurrent attempts allowed, want exactly 3", got)
	}
}
