package auth

import (
	"testing"
	"time"
)

func newTestLimiter() (*LoginLimiter, *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return NewLoginLimiter(3, 15*time.Minute, clock.Now), clock
}

func TestLimiterBlocksAfterMaxFailures(t *testing.T) {
	l, _ := newTestLimiter()
	for i := range 3 {
		if !l.Allowed("10.0.0.1") {
			t.Fatalf("blocked after %d failures, want allowed until 3", i)
		}
		l.RecordFailure("10.0.0.1")
	}
	if l.Allowed("10.0.0.1") {
		t.Error("allowed after 3 failures, want blocked")
	}
}

func TestLimiterUnblocksAfterWindow(t *testing.T) {
	l, clock := newTestLimiter()
	for range 3 {
		l.RecordFailure("10.0.0.1")
	}
	clock.Advance(15 * time.Minute)
	if !l.Allowed("10.0.0.1") {
		t.Error("still blocked after the window elapsed")
	}
}

func TestLimiterResetClearsFailures(t *testing.T) {
	l, _ := newTestLimiter()
	for range 2 {
		l.RecordFailure("10.0.0.1")
	}
	l.Reset("10.0.0.1")
	l.RecordFailure("10.0.0.1")
	l.RecordFailure("10.0.0.1")
	if !l.Allowed("10.0.0.1") {
		t.Error("blocked after reset + 2 failures, want allowed")
	}
}

func TestLimiterTracksIPsIndependently(t *testing.T) {
	l, _ := newTestLimiter()
	for range 3 {
		l.RecordFailure("10.0.0.1")
	}
	if !l.Allowed("10.0.0.2") {
		t.Error("other IP blocked by 10.0.0.1's failures")
	}
}
