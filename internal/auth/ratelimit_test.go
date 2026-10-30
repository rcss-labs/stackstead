package auth

import (
	"fmt"
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

func TestLimiterSharesOneBucketPerIPv6Prefix(t *testing.T) {
	l, _ := newTestLimiter()
	for range 3 {
		l.Attempt("2001:db8:1:2::1")
	}
	if l.Attempt("2001:db8:1:2:ffff:ffff:ffff:ffff") {
		t.Error("another address in the same /64 allowed, want blocked")
	}
	if !l.Attempt("2001:db8:1:3::1") {
		t.Error("a different /64 blocked by the first one's attempts")
	}
}

func TestLimiterResetClearsTheWholeIPv6Prefix(t *testing.T) {
	l, _ := newTestLimiter()
	for range 3 {
		l.Attempt("2001:db8:1:2::1")
	}
	l.Reset("2001:db8:1:2::99")
	if !l.Attempt("2001:db8:1:2::1") {
		t.Error("still blocked after a reset from the same /64")
	}
}

func TestLimiterTreatsIPv4MappedIPv6AsIPv4(t *testing.T) {
	l, _ := newTestLimiter()
	for range 3 {
		l.Attempt("::ffff:10.0.0.1")
	}
	if l.Attempt("10.0.0.1") {
		t.Error("plain IPv4 allowed after its mapped form used up the budget")
	}
	if !l.Attempt("10.0.0.2") {
		t.Error("neighbouring IPv4 address blocked; IPv4 must stay per address")
	}
}

func TestLimiterStaysBoundedUnderAddressRotation(t *testing.T) {
	l, _ := newTestLimiter()
	const rotated = 20000
	for i := range rotated {
		l.Attempt(fmt.Sprintf("2001:db8:%x:%x::1", i>>16, i&0xffff)) // a new /64 each time
		l.Attempt(fmt.Sprintf("10.%d.%d.%d", i>>16&0xff, i>>8&0xff, i&0xff))
	}
	if got := len(l.attempts); got > 10000 {
		t.Errorf("limiter holds %d entries, want at most 10000", got)
	}
}

func TestLimiterEvictsExpiredEntriesBeforeLiveOnes(t *testing.T) {
	l, clock := newTestLimiter()
	l.Attempt("10.0.0.1")
	clock.Advance(16 * time.Minute)
	for range 3 {
		l.Attempt("10.0.0.2")
	}
	l.Attempt("10.0.0.3") // any later attempt expires 10.0.0.1's stale window
	if _, ok := l.attempts["10.0.0.1"]; ok {
		t.Error("expired entry still held")
	}
	if l.Attempt("10.0.0.2") {
		t.Error("live entry was dropped; 10.0.0.2 should still be blocked")
	}
}
