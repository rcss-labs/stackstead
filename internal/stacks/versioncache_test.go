package stacks_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/stacks"
)

type countingCompose struct {
	fakeCompose
	calls int
	err   error
}

func (c *countingCompose) Version(context.Context) (string, error) {
	c.calls++
	return "2.29.0", c.err
}

func TestVersionCacheSpawnsProbeOncePerTTLAndCachesFailuresBriefly(t *testing.T) {
	now := time.Unix(1000, 0)
	inner := &countingCompose{}
	c := stacks.NewVersionCache(inner, 5*time.Second, func() time.Time { return now })
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if v, err := c.Version(ctx); err != nil || v != "2.29.0" {
			t.Fatalf("version = %q, %v", v, err)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("calls = %d, want 1", inner.calls)
	}
	now = now.Add(6 * time.Second)
	inner.err = errors.New("plugin gone")
	if _, err := c.Version(ctx); err == nil {
		t.Fatal("want error")
	}
	if _, err := c.Version(ctx); err == nil || inner.calls != 2 {
		t.Fatalf("failure not cached: calls = %d err = %v", inner.calls, err)
	}
	now = now.Add(6 * time.Second)
	inner.err = nil
	if _, err := c.Version(ctx); err != nil || inner.calls != 3 {
		t.Fatalf("recovery not seen after ttl: calls = %d err = %v", inner.calls, err)
	}
}

// hungCompose's version probe blocks until its context ends.
type hungCompose struct {
	fakeCompose
	calls atomic.Int32
}

func (h *hungCompose) Version(ctx context.Context) (string, error) {
	h.calls.Add(1)
	<-ctx.Done()
	return "", ctx.Err()
}

func TestVersionCacheHungProbeDoesNotBlockSecondCallerBeyondItsContext(t *testing.T) {
	inner := &hungCompose{}
	c := stacks.NewVersionCache(inner, 5*time.Second, nil)
	stacks.SetProbeTimeout(c, 400*time.Millisecond)

	first := make(chan error, 1)
	go func() { _, err := c.Version(context.Background()); first <- err }()
	for inner.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Version(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's own deadline", err)
	}
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Errorf("second caller waited %v for the hung probe", d)
	}
	if n := inner.calls.Load(); n != 1 {
		t.Errorf("probes = %d, want 1 shared by both callers", n)
	}

	// The first caller is released by the probe's own timeout, not its context.
	select {
	case err := <-first:
		if err == nil {
			t.Error("hung probe reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probe timeout did not end the hung probe")
	}
}

func TestVersionCacheCachesProbeTimeoutBriefly(t *testing.T) {
	inner := &hungCompose{}
	c := stacks.NewVersionCache(inner, 5*time.Second, nil)
	stacks.SetProbeTimeout(c, 50*time.Millisecond)
	for i := 0; i < 2; i++ {
		if _, err := c.Version(context.Background()); err == nil {
			t.Fatal("want error")
		}
	}
	if n := inner.calls.Load(); n != 1 {
		t.Errorf("probes = %d, want the failure cached", n)
	}
}
