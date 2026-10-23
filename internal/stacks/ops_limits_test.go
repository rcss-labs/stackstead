package stacks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// emitBytes emits lines of lineLen bytes until about total bytes were written.
// The same string is emitted every time, so the test itself holds one copy.
func emitBytes(out func(string), total, lineLen int) {
	line := strings.Repeat("x", lineLen)
	for written := 0; written < total; written += lineLen {
		out(line)
	}
}

func retainedBytes(t *testing.T, o *Ops, id string) int {
	t.Helper()
	lines, err := o.Lines(id)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range lines {
		n += len(l)
	}
	return n
}

func TestRetainedOutputIsBoundedByBytes(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	op, _ := o.Start("s", "build", func(ctx context.Context, out func(string)) error {
		emitBytes(out, 3*maxRetainedBytes, maxLineBytes-1) // far fewer lines than maxRetainedLines
		out("last line")
		return nil
	})
	waitDone(t, o, op.ID)
	lines, _ := o.Lines(op.ID)
	if got := retainedBytes(t, o, op.ID); got > maxRetainedBytes {
		t.Errorf("retained %d bytes, want at most %d", got, maxRetainedBytes)
	}
	if !strings.HasPrefix(lines[0], "... ") || !strings.HasSuffix(lines[0], " earlier lines dropped") {
		t.Errorf("first line %q, want the dropped note", lines[0])
	}
	if lines[len(lines)-1] != "last line" {
		t.Errorf("last line %q, want the newest kept", lines[len(lines)-1])
	}
	_, first, _, _, cancel, err := o.Subscribe(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if first == 0 {
		t.Error("replay must report the dropped lines in first")
	}
}

// stepClock is a clock the test advances by hand.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestCompletedOperationsExpireButRunningOnesStay(t *testing.T) {
	clock := &stepClock{t: fixedNow()}
	o := NewOps(time.Hour, clock.now)
	old, _ := o.Start("gone", "up", func(context.Context, func(string)) error { return nil })
	waitDone(t, o, old.ID)
	started := make(chan struct{})
	running, _ := o.Start("busy", "up", blockingFn(started))
	<-started
	clock.advance(completedTTL / 2)
	if _, err := o.Get(old.ID); err != nil {
		t.Fatalf("completed op must survive within the lifetime: %v", err)
	}
	clock.advance(completedTTL)
	if _, err := o.Get(old.ID); !errors.Is(err, ErrOperationNotFound) {
		t.Errorf("expired op: err = %v, want ErrOperationNotFound", err)
	}
	if got := o.Recent("gone"); len(got) != 0 {
		t.Errorf("Recent of an expired stack = %v, want none", got)
	}
	if _, err := o.Get(running.ID); err != nil {
		t.Errorf("a running op must never expire: %v", err)
	}
	o.Shutdown(context.Background())
}

func TestCompletedOperationsAreCappedAcrossStacks(t *testing.T) {
	clock := &stepClock{t: fixedNow()}
	o := NewOps(time.Minute, clock.now)
	const total = maxCompletedOps + 10
	ids := make([]string, total)
	for i := range ids {
		clock.advance(time.Second)
		op, err := o.Start(fmt.Sprintf("stack-%d", i), "up", func(context.Context, func(string)) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		waitDone(t, o, op.ID)
		ids[i] = op.ID
	}
	if got := len(o.All()); got > maxCompletedOps {
		t.Errorf("%d completed ops retained, want at most %d", got, maxCompletedOps)
	}
	if _, err := o.Get(ids[0]); !errors.Is(err, ErrOperationNotFound) {
		t.Errorf("oldest op: err = %v, want evicted", err)
	}
	if _, err := o.Get(ids[total-1]); err != nil {
		t.Errorf("newest op must be kept: %v", err)
	}
}

func TestCompletedOutputIsBoundedGlobally(t *testing.T) {
	clock := &stepClock{t: fixedNow()}
	o := NewOps(time.Minute, clock.now)
	n := maxCompletedBytes/maxRetainedBytes + 4
	ids := make([]string, n)
	for i := range ids {
		clock.advance(time.Second)
		op, _ := o.Start(fmt.Sprintf("stack-%d", i), "build", func(ctx context.Context, out func(string)) error {
			emitBytes(out, maxRetainedBytes, maxLineBytes-1)
			return nil
		})
		waitDone(t, o, op.ID)
		ids[i] = op.ID
	}
	total := 0
	for _, id := range ids {
		if _, err := o.Get(id); err == nil {
			total += retainedBytes(t, o, id)
		}
	}
	if total > maxCompletedBytes {
		t.Errorf("completed output retains %d bytes, want at most %d", total, maxCompletedBytes)
	}
	if _, err := o.Get(ids[n-1]); err != nil {
		t.Errorf("newest op must be kept: %v", err)
	}
}
