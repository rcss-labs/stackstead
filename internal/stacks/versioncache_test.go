package stacks_test

import (
	"context"
	"errors"
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
