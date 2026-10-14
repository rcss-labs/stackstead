package stacks

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"
)

type exitErr struct{ code int }

func (e exitErr) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e exitErr) ExitCode() int { return e.code }

var fixedNow = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }

// waitDone blocks until the operation leaves the running state.
func waitDone(t *testing.T, o *Ops, id string) Operation {
	t.Helper()
	_, _, done, cancel, err := o.Subscribe(id)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	select {
	case op := <-done:
		return op
	case <-time.After(5 * time.Second):
		t.Fatal("operation did not finish")
		return Operation{}
	}
}

func blockingFn(started chan<- struct{}) OpFunc {
	return func(ctx context.Context, out func(string)) error {
		if started != nil {
			close(started)
		}
		<-ctx.Done()
		return ctx.Err()
	}
}

func TestStartBusyOnSameStackOnly(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	release := make(chan struct{})
	started := make(chan struct{})
	op, err := o.Start("a", "up", func(ctx context.Context, out func(string)) error {
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if len(op.ID) != 16 || op.State != OpRunning || op.ExitCode != -1 {
		t.Fatalf("unexpected op %+v", op)
	}
	if _, err := o.Start("a", "down", func(context.Context, func(string)) error { return nil }); !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
	opB, err := o.Start("b", "up", func(context.Context, func(string)) error { return nil })
	if err != nil {
		t.Fatalf("different stack must be allowed: %v", err)
	}
	waitDone(t, o, opB.ID)
	close(release)
	waitDone(t, o, op.ID)
	if _, err := o.Start("a", "down", func(context.Context, func(string)) error { return nil }); err != nil {
		t.Fatalf("stack free after finish: %v", err)
	}
}

func TestStateMapping(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		want     OpState
		wantExit int
	}{
		{"success", nil, OpSucceeded, -1},
		{"canceled", context.Canceled, OpCancelled, -1},
		{"deadline", context.DeadlineExceeded, OpTimedOut, -1},
		{"exit code", fmt.Errorf("wrapped: %w", exitErr{3}), OpFailed, 3},
		{"plain failure", errors.New("boom"), OpFailed, -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := NewOps(time.Minute, fixedNow)
			op, err := o.Start("s", "up", func(context.Context, func(string)) error { return tc.err })
			if err != nil {
				t.Fatal(err)
			}
			got := waitDone(t, o, op.ID)
			if got.State != tc.want || got.ExitCode != tc.wantExit {
				t.Fatalf("got state=%s exit=%d, want %s/%d", got.State, got.ExitCode, tc.want, tc.wantExit)
			}
			if tc.err != nil && got.Error == "" {
				t.Fatal("expected error summary")
			}
			if tc.err == nil && got.Error != "" {
				t.Fatalf("unexpected error %q", got.Error)
			}
			if got.EndedAt.IsZero() || got.StartedAt.IsZero() {
				t.Fatal("timestamps not set")
			}
			stored, _ := o.Get(op.ID)
			if stored.State != tc.want {
				t.Fatalf("Get state %s", stored.State)
			}
		})
	}
}

func TestPanicBecomesFailed(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	op, _ := o.Start("s", "up", func(context.Context, func(string)) error { panic("kaboom") })
	if got := waitDone(t, o, op.ID); got.State != OpFailed {
		t.Fatalf("state %s", got.State)
	}
}

func TestCancel(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	started := make(chan struct{})
	op, _ := o.Start("s", "up", blockingFn(started))
	<-started
	if err := o.Cancel(op.ID); err != nil {
		t.Fatal(err)
	}
	if got := waitDone(t, o, op.ID); got.State != OpCancelled {
		t.Fatalf("state %s", got.State)
	}
	if err := o.Cancel(op.ID); err != nil {
		t.Fatalf("cancel of finished op must be a no-op, got %v", err)
	}
	if err := o.Cancel("nope"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("want ErrOperationNotFound, got %v", err)
	}
}

func TestTimeout(t *testing.T) {
	o := NewOps(20*time.Millisecond, fixedNow)
	op, _ := o.Start("s", "up", blockingFn(nil))
	if got := waitDone(t, o, op.ID); got.State != OpTimedOut {
		t.Fatalf("state %s", got.State)
	}
}

func TestFailureAfterCancelStillCancelled(t *testing.T) {
	// A killed process reports "signal: killed", not context.Canceled.
	o := NewOps(time.Minute, fixedNow)
	started := make(chan struct{})
	op, _ := o.Start("s", "up", func(ctx context.Context, out func(string)) error {
		close(started)
		<-ctx.Done()
		return errors.New("signal: killed")
	})
	<-started
	_ = o.Cancel(op.ID)
	if got := waitDone(t, o, op.ID); got.State != OpCancelled {
		t.Fatalf("state %s", got.State)
	}
}

func TestLineCapKeepsNewestWithDroppedNote(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	const total = maxRetainedLines + 500
	op, _ := o.Start("s", "up", func(ctx context.Context, out func(string)) error {
		for i := 0; i < total; i++ {
			out(fmt.Sprintf("line %d", i))
		}
		return nil
	})
	waitDone(t, o, op.ID)
	lines, err := o.Lines(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != maxRetainedLines {
		t.Fatalf("retained %d lines, want %d", len(lines), maxRetainedLines)
	}
	wantNote := fmt.Sprintf("... %d earlier lines dropped", total-(maxRetainedLines-1))
	if lines[0] != wantNote {
		t.Fatalf("first line %q, want %q", lines[0], wantNote)
	}
	if last := lines[len(lines)-1]; last != fmt.Sprintf("line %d", total-1) {
		t.Fatalf("last line %q", last)
	}
	if lines[1] != fmt.Sprintf("line %d", total-(maxRetainedLines-1)) {
		t.Fatalf("second line %q", lines[1])
	}
}

func TestSubscribeMidwayGetsEveryLineOnce(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	half := make(chan struct{})
	release := make(chan struct{})
	op, _ := o.Start("s", "up", func(ctx context.Context, out func(string)) error {
		for i := 0; i < 50; i++ {
			out(fmt.Sprintf("l%d", i))
		}
		close(half)
		<-release
		for i := 50; i < 100; i++ {
			out(fmt.Sprintf("l%d", i))
		}
		return nil
	})
	<-half
	replay, live, done, cancel, err := o.Subscribe(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	close(release)
	got := append([]string{}, replay...)
	for l := range live {
		got = append(got, l)
	}
	if len(got) != 100 {
		t.Fatalf("got %d lines", len(got))
	}
	for i, l := range got {
		if l != fmt.Sprintf("l%d", i) {
			t.Fatalf("line %d = %q", i, l)
		}
	}
	if final := <-done; final.State != OpSucceeded {
		t.Fatalf("final %+v", final)
	}
}

func TestSubscribeToFinishedOp(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	op, _ := o.Start("s", "up", func(ctx context.Context, out func(string)) error { out("x"); return nil })
	waitDone(t, o, op.ID)
	replay, live, done, cancel, err := o.Subscribe(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if len(replay) != 1 {
		t.Fatalf("replay %v", replay)
	}
	if _, ok := <-live; ok {
		t.Fatal("live must be closed")
	}
	if final := <-done; final.State != OpSucceeded {
		t.Fatalf("final %+v", final)
	}
	if _, _, _, _, err := o.Subscribe("nope"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestSlowSubscriberDroppedWithoutBlockingOp(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	subscribed := make(chan struct{})
	op, _ := o.Start("s", "up", func(ctx context.Context, out func(string)) error {
		<-subscribed
		for i := 0; i < subscriberBuffer*4; i++ {
			out("spam") // nobody reads the subscriber channel
		}
		return nil
	})
	_, live, _, cancel, err := o.Subscribe(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	close(subscribed)
	if got := waitDone(t, o, op.ID); got.State != OpSucceeded {
		t.Fatalf("op must complete, got %s", got.State)
	}
	n := 0
	for range live { // must terminate: channel was closed
		n++
	}
	if n > subscriberBuffer {
		t.Fatalf("slow subscriber received %d lines", n)
	}
	if lines, _ := o.Lines(op.ID); len(lines) != subscriberBuffer*4 {
		t.Fatalf("output must still be fully retained, got %d", len(lines))
	}
}

func TestSubscriberCancelIsIdempotent(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	started := make(chan struct{})
	op, _ := o.Start("s", "up", blockingFn(started))
	<-started
	_, live, _, cancel, _ := o.Subscribe(op.ID)
	cancel()
	cancel()
	if _, ok := <-live; ok {
		t.Fatal("live must be closed after cancel")
	}
	_ = o.Cancel(op.ID)
	waitDone(t, o, op.ID)
}

func TestShutdownCancelsAndWaitsWithoutLeakingGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	o := NewOps(time.Minute, fixedNow)
	var ids []string
	for _, s := range []string{"a", "b", "c"} {
		started := make(chan struct{})
		op, _ := o.Start(s, "up", blockingFn(started))
		<-started
		ids = append(ids, op.ID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	o.Shutdown(ctx)
	for _, id := range ids {
		if got, _ := o.Get(id); got.State != OpCancelled {
			t.Fatalf("op %s state %s", id, got.State)
		}
	}
	if _, err := o.Start("d", "up", blockingFn(nil)); !errors.Is(err, ErrShutdown) {
		t.Fatalf("Start after shutdown: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+1 && time.Now().Before(deadline) {
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Fatalf("goroutines before=%d after=%d", before, after)
	}
}

func TestShutdownReturnsWhenContextExpires(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	release := make(chan struct{})
	started := make(chan struct{})
	op, _ := o.Start("s", "up", func(ctx context.Context, out func(string)) error {
		close(started)
		<-release // ignores cancellation
		return nil
	})
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	o.Shutdown(ctx) // must return despite the stuck op
	close(release)
	waitDone(t, o, op.ID)
}

func TestRecentKeepsTwentyPerStackNewestFirst(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	var ids []string
	for i := 0; i < 25; i++ {
		op, err := o.Start("s", fmt.Sprintf("a%d", i), func(context.Context, func(string)) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		waitDone(t, o, op.ID)
		ids = append(ids, op.ID)
	}
	other, _ := o.Start("other", "up", func(context.Context, func(string)) error { return nil })
	waitDone(t, o, other.ID)

	recent := o.Recent("s")
	if len(recent) != maxRecentPerStack {
		t.Fatalf("got %d", len(recent))
	}
	if recent[0].ID != ids[24] || recent[19].ID != ids[5] {
		t.Fatal("wrong order or window")
	}
	if _, err := o.Get(ids[0]); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("evicted op should be gone, got %v", err)
	}
	if len(o.Recent("other")) != 1 || len(o.Recent("none")) != 0 {
		t.Fatal("per-stack isolation broken")
	}
}
