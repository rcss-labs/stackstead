package stacks_test

import (
	"context"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/imageref"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

// slowFetcher takes transferTime to fetch and gives up when its context ends.
type slowFetcher struct{ transferTime time.Duration }

func (f slowFetcher) EnsureLocal(ctx context.Context, _ imageref.Ref, _ func(string)) error {
	select {
	case <-time.After(f.transferTime): // the simulated transfer
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// shortOpsHarness has a stack operation timeout far below the image timeout.
func shortOpsHarness(t *testing.T, fetcher stacks.LibraryFetcher) *harness {
	t.Helper()
	h := newHarness(t)
	h.ops = stacks.NewOps(50*time.Millisecond, nil)
	t.Cleanup(func() { h.ops.Shutdown(context.Background()) })
	h.svc = stacks.NewService(stacks.Deps{
		Registry: h.reg, Ops: h.ops, Compose: h.compose, Engine: h.eng,
		StacksDir: h.dir, AllowedRoots: []string{h.dir},
		Now:     func() time.Time { return fixedNow },
		Library: fetcher, LibraryTimeout: time.Minute,
	})
	return h
}

func TestUpFetchIsBoundedByTheImageTimeoutNotTheOperationTimeout(t *testing.T) {
	h := shortOpsHarness(t, slowFetcher{transferTime: 300 * time.Millisecond})
	h.createMulti(t, "one", map[string]string{"web": libWeb, "db": "nginx"})
	op, err := h.svc.Act(context.Background(), "one", "up")
	if err != nil {
		t.Fatal(err)
	}
	if op = wait(t, h.ops, op.ID); op.State != stacks.OpSucceeded {
		t.Fatalf("state = %s (%s); a slow fetch must not be cut off by operation_timeout", op.State, op.Error)
	}
}

func TestServiceUpFetchIsBoundedByTheImageTimeoutNotTheOperationTimeout(t *testing.T) {
	h := shortOpsHarness(t, slowFetcher{transferTime: 300 * time.Millisecond})
	h.createMulti(t, "one", map[string]string{"web": libWeb, "db": "nginx"})
	op, err := h.svc.ActService(context.Background(), "one", "web", "up")
	if err != nil {
		t.Fatal(err)
	}
	if op = wait(t, h.ops, op.ID); op.State != stacks.OpSucceeded {
		t.Fatalf("state = %s (%s); a slow fetch must not be cut off by operation_timeout", op.State, op.Error)
	}
}

func TestStackOperationWithoutLibraryRefsKeepsOperationTimeout(t *testing.T) {
	h := shortOpsHarness(t, slowFetcher{})
	h.createMulti(t, "plain", map[string]string{"web": "nginx", "db": "postgres"})
	h.compose.runGate = make(chan struct{}) // compose never finishes by itself
	op, err := h.svc.Act(context.Background(), "plain", "up")
	if err != nil {
		t.Fatal(err)
	}
	if op = wait(t, h.ops, op.ID); op.State != stacks.OpTimedOut {
		t.Fatalf("state = %s; want timed_out at operation_timeout", op.State)
	}
}
