package stacks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rcss-labs/stackstead/internal/maintenance"
)

// OpState is the lifecycle state of an operation.
type OpState string

const (
	OpRunning   OpState = "running"
	OpSucceeded OpState = "succeeded"
	OpFailed    OpState = "failed"
	OpCancelled OpState = "cancelled"
	OpTimedOut  OpState = "timed_out"
)

const (
	maxRetainedLines  = 20000
	maxRecentPerStack = 20
	maxLineBytes      = 8 << 10
	// subscriberBuffer is how many live lines a subscriber may lag behind
	// before it is dropped.
	subscriberBuffer = 256
)

var (
	ErrBusy              = errors.New("another operation is running for this stack")
	ErrOperationNotFound = errors.New("operation not found")
	ErrShutdown          = errors.New("operations manager is shut down")
)

// Operation is a snapshot of one long-running operation.
type Operation struct {
	ID, Stack, Action  string
	State              OpState
	StartedAt, EndedAt time.Time
	ExitCode           int    // -1 when not applicable
	Error              string // human summary when not succeeded
}

// OpFunc does the work. It must return promptly once ctx is done and
// reports output lines through out.
type OpFunc func(ctx context.Context, out func(line string)) error

// op is the internal record of an operation. Every field except finished
// is guarded by Ops.mu; finished is closed (once) when the op ends.
type op struct {
	info    Operation
	lines   []string // newest maxRetainedLines lines
	dropped int      // lines discarded from the front of lines
	subs    map[*subscriber]struct{}
	cancel  context.CancelFunc
	// finished is closed after info reaches its final state.
	finished chan struct{}
	release  func()
}

// subscriber is registered in op.subs until it is dropped, cancels or the
// op finishes; whoever removes it from op.subs closes both channels.
type subscriber struct {
	live chan string
	done chan Operation // buffered (1); receives the final Operation only on finish
}

// Ops runs at most one operation per stack, retains its output and
// streams it to subscribers.
//
// Locking: a single mutex guards the maps and all op state. Nothing under
// the lock blocks: subscriber sends are non-blocking (a full subscriber is
// dropped) and OpFuncs always run outside the lock.
type Ops struct {
	maintenance *maintenance.Gate
	timeout     time.Duration
	now         func() time.Time

	mu       sync.Mutex
	byID     map[string]*op
	byStack  map[string][]*op // oldest first, at most maxRecentPerStack
	shutdown bool
}

// NewOps creates a manager. A timeout <= 0 disables the per-operation timeout.
func NewOps(timeout time.Duration, now func() time.Time) *Ops {
	if now == nil {
		now = time.Now
	}
	return &Ops{timeout: timeout, now: now, byID: map[string]*op{}, byStack: map[string][]*op{}}
}

// SetMaintenance configures admission before the manager is used.
func (o *Ops) SetMaintenance(g *maintenance.Gate) { o.maintenance = g }

func newOpID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Start runs fn in the background. It returns ErrBusy if the stack already
// has a running operation.
func (o *Ops) Start(stack, action string, fn OpFunc) (Operation, error) {
	release, err := o.maintenance.Enter()
	if err != nil {
		return Operation{}, err
	}
	admitted := false
	defer func() {
		if !admitted {
			release()
		}
	}()
	id, err := newOpID()
	if err != nil {
		return Operation{}, fmt.Errorf("generate operation id: %w", err)
	}

	o.mu.Lock()
	if o.shutdown {
		o.mu.Unlock()
		return Operation{}, ErrShutdown
	}
	if hist := o.byStack[stack]; len(hist) > 0 && hist[len(hist)-1].info.State == OpRunning {
		o.mu.Unlock()
		return Operation{}, ErrBusy
	}
	var ctx context.Context
	var cancel context.CancelFunc
	if o.timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), o.timeout)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	rec := &op{
		info: Operation{ID: id, Stack: stack, Action: action, State: OpRunning,
			StartedAt: o.now(), ExitCode: -1},
		subs:     map[*subscriber]struct{}{},
		cancel:   cancel,
		finished: make(chan struct{}),
		release:  release,
	}
	o.byID[id] = rec
	hist := append(o.byStack[stack], rec)
	if len(hist) > maxRecentPerStack {
		// Only the newest op can be running, so the evicted one is finished.
		delete(o.byID, hist[0].info.ID)
		hist[0] = nil // let the evicted op be garbage collected
		hist = hist[1:]
	}
	o.byStack[stack] = hist
	snapshot := rec.info
	o.mu.Unlock()

	admitted = true
	go o.run(ctx, rec, fn)
	return snapshot, nil
}

func (o *Ops) run(ctx context.Context, rec *op, fn OpFunc) {
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("operation panicked: %v", r)
				o.emit(rec, fmt.Sprintf("panic: %v", r))
				for _, l := range strings.Split(strings.TrimRight(string(debug.Stack()), "\n"), "\n") {
					o.emit(rec, l)
				}
			}
		}()
		return fn(ctx, func(line string) { o.emit(rec, line) })
	}()
	ctxErr := ctx.Err()
	rec.cancel()
	o.finish(rec, err, ctxErr)
}

// finish records the final state and releases every subscriber.
func (o *Ops) finish(rec *op, err, ctxErr error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	info := &rec.info
	info.EndedAt = o.now()
	// The op context decides cancelled/timed_out; errors inside err (for
	// example an internal deadline) never do.
	switch {
	case ctxErr != nil && errors.Is(ctxErr, context.DeadlineExceeded):
		info.State = OpTimedOut
		info.Error = "operation timed out"
	case ctxErr != nil:
		info.State = OpCancelled
		info.Error = "operation cancelled"
	case err == nil:
		info.State = OpSucceeded
	default:
		info.State = OpFailed
		info.Error = err.Error()
		var ec interface{ ExitCode() int }
		if errors.As(err, &ec) {
			info.ExitCode = ec.ExitCode()
		}
	}
	for s := range rec.subs {
		close(s.live)
		s.done <- *info // buffered, capacity 1, written once
		close(s.done)
	}
	rec.subs = nil
	rec.release()
	close(rec.finished)
}

func (o *Ops) emit(rec *op, line string) {
	line = truncateLine(line) // live subscribers and replay must see the same text
	o.mu.Lock()
	defer o.mu.Unlock()
	if rec.info.State != OpRunning {
		return
	}
	rec.lines = append(rec.lines, line)
	if len(rec.lines) > maxRetainedLines {
		rec.lines = rec.lines[1:]
		rec.dropped++
	}
	for s := range rec.subs {
		select {
		case s.live <- line:
		default: // too slow: drop it rather than block the operation
			delete(rec.subs, s)
			close(s.live)
			close(s.done)
		}
	}
}

// retained returns the output as stored. When lines were dropped, the first
// line is a note and replaces the oldest kept line so the total stays within
// maxRetainedLines. Caller holds o.mu.
func (rec *op) retained() []string {
	if rec.dropped == 0 {
		return append([]string(nil), rec.lines...)
	}
	out := make([]string, len(rec.lines))
	copy(out, rec.lines)
	out[0] = fmt.Sprintf("... %d earlier lines dropped", rec.dropped+1)
	return out
}

func (o *Ops) lookup(id string) (*op, error) {
	rec, ok := o.byID[id]
	if !ok {
		return nil, ErrOperationNotFound
	}
	return rec, nil
}

// Get returns a snapshot of the operation.
func (o *Ops) Get(id string) (Operation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	rec, err := o.lookup(id)
	if err != nil {
		return Operation{}, err
	}
	return rec.info, nil
}

// Lines returns the retained output.
func (o *Ops) Lines(id string) ([]string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	rec, err := o.lookup(id)
	if err != nil {
		return nil, err
	}
	return rec.retained(), nil
}

// Subscribe returns the output so far plus a channel of subsequent lines.
// Replay and registration happen atomically, so no line is missed or
// repeated.
//
// first is the absolute index (counted from the operation's first line) of
// replay[0]; live lines continue at first+len(replay). It is 0 until the
// retention cap evicts lines. Once first > 0, replay[0] is the "earlier lines
// dropped" note standing in for absolute line first, and carries no line of
// its own: real lines start at replay[1] (absolute index first+1).
//
// Contract: live is closed when the subscription ends. After live closes,
// receive from done: a value means the operation finished and is its final
// state; a closed channel with no value means only this subscription ended
// (dropped for being too slow, or ended by cancel), so call Get or Subscribe
// again to continue. cancel unsubscribes and is safe to call repeatedly.
func (o *Ops) Subscribe(id string) (replay []string, first int, live <-chan string, done <-chan Operation, cancel func(), err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	rec, err := o.lookup(id)
	if err != nil {
		return nil, 0, nil, nil, nil, err
	}
	replay, first = rec.retained(), rec.dropped
	s := &subscriber{live: make(chan string, subscriberBuffer), done: make(chan Operation, 1)}
	if rec.info.State != OpRunning {
		close(s.live)
		s.done <- rec.info
		close(s.done)
		return replay, first, s.live, s.done, func() {}, nil
	}
	rec.subs[s] = struct{}{}
	cancel = func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		if _, ok := rec.subs[s]; ok {
			delete(rec.subs, s)
			close(s.live)
			close(s.done)
		}
	}
	return replay, first, s.live, s.done, cancel, nil
}

// Cancel asks a running operation to stop. It is a no-op once finished.
func (o *Ops) Cancel(id string) error {
	o.mu.Lock()
	rec, err := o.lookup(id)
	o.mu.Unlock()
	if err != nil {
		return err
	}
	rec.cancel() // context.CancelFunc is safe for concurrent and repeated use
	return nil
}

// Recent returns up to the last 20 operations of the stack, newest first.
func (o *Ops) Recent(stack string) []Operation {
	o.mu.Lock()
	defer o.mu.Unlock()
	hist := o.byStack[stack]
	out := make([]Operation, 0, len(hist))
	for i := len(hist) - 1; i >= 0; i-- {
		out = append(out, hist[i].info)
	}
	return out
}

// All returns the retained operations of every stack, newest first.
func (o *Ops) All() []Operation {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []Operation
	for _, hist := range o.byStack {
		for _, rec := range hist {
			out = append(out, rec.info)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// Shutdown rejects new operations, cancels running ones and waits until
// they have finished or ctx expires.
func (o *Ops) Shutdown(ctx context.Context) {
	o.mu.Lock()
	o.shutdown = true
	var pending []*op
	for _, rec := range o.byID {
		if rec.info.State == OpRunning {
			pending = append(pending, rec)
		}
	}
	o.mu.Unlock()
	for _, rec := range pending {
		rec.cancel()
	}
	for _, rec := range pending {
		select {
		case <-rec.finished:
		case <-ctx.Done():
			return
		}
	}
}

const truncatedSuffix = "…(truncated)"

// truncateLine caps a line at maxLineBytes, cutting on a rune boundary.
func truncateLine(line string) string {
	if len(line) <= maxLineBytes {
		return line
	}
	cut := maxLineBytes
	// Back off at most UTFMax-1 bytes to a rune start; invalid UTF-8 is cut as is.
	for i := 0; i < utf8.UTFMax-1 && cut > 0 && !utf8.RuneStart(line[cut]); i++ {
		cut--
	}
	if !utf8.RuneStart(line[cut]) {
		cut = maxLineBytes
	}
	return line[:cut] + truncatedSuffix
}
