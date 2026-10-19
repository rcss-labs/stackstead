package stacks

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type exitErr struct{ code int }

func (e exitErr) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e exitErr) ExitCode() int { return e.code }

var fixedNow = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }

// waitDone blocks until the operation leaves the running state. It drains
// live output and resubscribes if it was dropped for being slow.
func waitDone(t *testing.T, o *Ops, id string) Operation {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		_, _, live, done, cancel, err := o.Subscribe(id)
		if err != nil {
			t.Fatal(err)
		}
		finished := make(chan Operation, 1)
		go func() {
			for range live {
			}
			op, ok := <-done
			if !ok {
				op, _ = o.Get(id)
			}
			finished <- op
		}()
		select {
		case op := <-finished:
			cancel()
			if op.State != OpRunning {
				return op
			}
		case <-deadline:
			t.Fatal("operation did not finish")
		}
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
		// Cancelled/timed_out are driven by the op context: see TestCancel,
		// TestTimeout and TestStateFollowsOpContextNotErrorChain.
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
	replay, _, live, done, cancel, err := o.Subscribe(op.ID)
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
	replay, _, live, done, cancel, err := o.Subscribe(op.ID)
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
	if _, _, _, _, _, err := o.Subscribe("nope"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestSlowSubscriberDroppedWithoutBlockingOp(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	subscribed := make(chan struct{})
	spammed := make(chan struct{})
	release := make(chan struct{})
	op, _ := o.Start("s", "up", func(ctx context.Context, out func(string)) error {
		<-subscribed
		for i := 0; i < subscriberBuffer*4; i++ {
			out("spam") // nobody reads the subscriber channel
		}
		close(spammed)
		<-release
		return nil
	})
	_, _, live, done, cancel, err := o.Subscribe(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	close(subscribed)
	<-spammed
	n := 0
	for range live { // must terminate: channel was closed
		n++
	}
	if n > subscriberBuffer {
		t.Fatalf("slow subscriber received %d lines", n)
	}
	select {
	case v, ok := <-done:
		if ok {
			t.Fatalf("done must close without a value on drop, got %+v", v)
		}
	case <-time.After(time.Second):
		t.Fatal("done not closed after drop")
	}
	if got, _ := o.Get(op.ID); got.State != OpRunning {
		t.Fatalf("op must continue, got %s", got.State)
	}
	close(release)
	if got := waitDone(t, o, op.ID); got.State != OpSucceeded {
		t.Fatalf("op must complete, got %s", got.State)
	}
	if lines, _ := o.Lines(op.ID); len(lines) != subscriberBuffer*4 {
		t.Fatalf("output must still be fully retained, got %d", len(lines))
	}
}

func TestStateFollowsOpContextNotErrorChain(t *testing.T) {
	t.Run("cancel with wrapped internal deadline is cancelled", func(t *testing.T) {
		o := NewOps(time.Minute, fixedNow)
		started := make(chan struct{})
		op, _ := o.Start("s", "up", func(ctx context.Context, out func(string)) error {
			close(started)
			<-ctx.Done()
			return fmt.Errorf("grace: %w", context.DeadlineExceeded)
		})
		<-started
		_ = o.Cancel(op.ID)
		if got := waitDone(t, o, op.ID); got.State != OpCancelled {
			t.Fatalf("state %s", got.State)
		}
	})
	t.Run("internal deadline with live ctx is failed", func(t *testing.T) {
		o := NewOps(time.Minute, fixedNow)
		op, _ := o.Start("s", "up", func(context.Context, func(string)) error {
			return fmt.Errorf("inspect: %w", context.DeadlineExceeded)
		})
		got := waitDone(t, o, op.ID)
		if got.State != OpFailed || got.Error != "inspect: context deadline exceeded" {
			t.Fatalf("got %s %q", got.State, got.Error)
		}
	})
	t.Run("op timeout with wrapped Canceled is timed_out", func(t *testing.T) {
		o := NewOps(20*time.Millisecond, fixedNow)
		op, _ := o.Start("s", "up", func(ctx context.Context, out func(string)) error {
			<-ctx.Done()
			return fmt.Errorf("x: %w", context.Canceled)
		})
		if got := waitDone(t, o, op.ID); got.State != OpTimedOut {
			t.Fatalf("state %s", got.State)
		}
	})
}

func TestLongLinesTruncatedOnRuneBoundary(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	// "é" is 2 bytes; an odd cap offset forces a cut inside a rune.
	long := strings.Repeat("é", maxLineBytes)
	op, _ := o.Start("s", "up", func(ctx context.Context, out func(string)) error {
		out(long)
		out("short")
		return nil
	})
	waitDone(t, o, op.ID)
	lines, _ := o.Lines(op.ID)
	if len(lines) != 2 || lines[1] != "short" {
		t.Fatalf("lines %d", len(lines))
	}
	if !strings.HasSuffix(lines[0], "…(truncated)") || !utf8.ValidString(lines[0]) {
		t.Fatal("not truncated on a rune boundary")
	}
	if len(lines[0]) > maxLineBytes+len("…(truncated)") {
		t.Fatalf("line too long: %d", len(lines[0]))
	}
}

func TestPanicRetainsStackInOutput(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	op, _ := o.Start("s", "up", func(context.Context, func(string)) error { panic("kaboom") })
	got := waitDone(t, o, op.ID)
	if got.State != OpFailed || strings.Contains(got.Error, "\n") {
		t.Fatalf("error should be short: %q", got.Error)
	}
	lines, _ := o.Lines(op.ID)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "kaboom") || !strings.Contains(joined, "goroutine") {
		t.Fatalf("stack not retained: %q", joined)
	}
}

func TestSubscriberCancelIsIdempotent(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	started := make(chan struct{})
	op, _ := o.Start("s", "up", blockingFn(started))
	<-started
	_, _, live, _, cancel, _ := o.Subscribe(op.ID)
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

func TestLiveAndRetainedLongLinesAgree(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	subscribed := make(chan struct{})
	op, _ := o.Start("s", "up", func(ctx context.Context, out func(string)) error {
		<-subscribed
		out(strings.Repeat("a", 3*maxLineBytes))
		return nil
	})
	_, _, live, _, cancel, _ := o.Subscribe(op.ID)
	defer cancel()
	close(subscribed)
	got := <-live
	waitDone(t, o, op.ID)
	lines, _ := o.Lines(op.ID)
	if got != lines[0] || len(got) != maxLineBytes+len(truncatedSuffix) {
		t.Fatalf("live %d bytes, retained %d bytes", len(got), len(lines[0]))
	}
}

func TestTruncateLine(t *testing.T) {
	suffix := truncatedSuffix
	tests := []struct {
		name, in, want string
	}{
		{"exact cap unchanged", strings.Repeat("a", maxLineBytes), strings.Repeat("a", maxLineBytes)},
		{"one over", strings.Repeat("a", maxLineBytes+1), strings.Repeat("a", maxLineBytes) + suffix},
		{"invalid utf8 keeps content", strings.Repeat("\x80", maxLineBytes+100), strings.Repeat("\x80", maxLineBytes) + suffix},
		{"emoji straddling cap cut before it",
			strings.Repeat("a", maxLineBytes-2) + "😀" + "tail",
			strings.Repeat("a", maxLineBytes-2) + suffix},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateLine(tc.in); got != tc.want {
				t.Fatalf("got len %d, want len %d", len(got), len(tc.want))
			}
		})
	}
}

func TestSubscribeReportsAbsoluteIndexOfFirstRetainedLine(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	const total = maxRetainedLines + 500
	op, _ := o.Start("s", "up", func(ctx context.Context, out func(string)) error {
		for i := 0; i < total; i++ {
			out(fmt.Sprintf("line %d", i))
		}
		return nil
	})
	waitDone(t, o, op.ID)
	replay, first, _, _, cancel, err := o.Subscribe(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if first != total-maxRetainedLines {
		t.Fatalf("first = %d, want %d", first, total-maxRetainedLines)
	}
	// replay[0] is the note; replay[i] for i>=1 is absolute line first+i.
	if want := fmt.Sprintf("line %d", first+1); replay[1] != want {
		t.Fatalf("replay[1] = %q, want %q", replay[1], want)
	}
}

func TestAllListsOperationsAcrossStacks(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	release := make(chan struct{})
	defer close(release)
	running, err := o.Start("a", "up", func(ctx context.Context, _ func(string)) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	finished, _ := o.Start("b", "up", func(context.Context, func(string)) error { return nil })
	waitDone(t, o, finished.ID)

	states := map[string]OpState{}
	for _, op := range o.All() {
		states[op.ID] = op.State
	}
	if len(states) != 2 || states[running.ID] != OpRunning || states[finished.ID] != OpSucceeded {
		t.Fatalf("All = %v", states)
	}
}

func TestHostOperationIndependentOfStack(t *testing.T) {
	o := NewOps(time.Minute, fixedNow)
	host, err := o.Start("", "storage-cleanup", blockingFn(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = o.Start("", "storage-cleanup", blockingFn(nil)); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	stack, err := o.Start("media", "up", blockingFn(nil))
	if err != nil {
		t.Fatal(err)
	}
	if host.Stack != "" || host.Action != "storage-cleanup" || len(o.All()) != 2 {
		t.Fatal(host, o.All())
	}
	o.Cancel(host.ID)
	o.Cancel(stack.ID)
	if waitDone(t, o, host.ID).State != OpCancelled || waitDone(t, o, stack.ID).State != OpCancelled {
		t.Fatal("cancellation failed")
	}
}
