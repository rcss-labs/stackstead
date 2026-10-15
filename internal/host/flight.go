package host

import (
	"context"
	"sync"
	"time"
)

// cached holds one value with single-flight loading. At most one load runs at
// a time, whether it was started by Get (synchronous) or GetStale (background).
type cached[T any] struct {
	now func() time.Time

	mu        sync.Mutex
	val       T
	has       bool      // val holds a successfully loaded value
	loadedAt  time.Time // when the last load finished successfully (Get)
	checked   time.Time // when the last background refresh finished, success or not
	checkedOK bool      // checked is meaningful
	inflight  *flight[T]
}

type flight[T any] struct {
	done chan struct{}
	val  T
	err  error
}

func newCached[T any](now func() time.Time) *cached[T] {
	return &cached[T]{now: now}
}

// Get returns the cached value when it is younger than maxAge. Otherwise the
// first caller runs load while concurrent callers wait for its result; a
// waiting caller gives up with ctx.Err() when its own context ends.
func (c *cached[T]) Get(ctx context.Context, maxAge time.Duration, load func(context.Context) (T, error)) (T, error) {
	c.mu.Lock()
	if c.has && c.now().Sub(c.loadedAt) < maxAge {
		v := c.val
		c.mu.Unlock()
		return v, nil
	}
	if f := c.inflight; f != nil {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.val, f.err
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
	}
	f := &flight[T]{done: make(chan struct{})}
	c.inflight = f
	c.mu.Unlock()

	// Finish the flight even if load panics, so waiters are never stranded.
	finished := false
	defer func() {
		if !finished {
			c.finish(f, f.val, context.Canceled, false)
		}
	}()
	v, err := load(ctx)
	finished = true
	c.finish(f, v, err, false)
	return v, err
}

// GetStale returns the last loaded value immediately. When the last refresh
// attempt is older than maxAge (or there was none) and no load is running, it
// starts one background load. ok is false until the first load succeeds. A
// failed refresh keeps the old value and is not retried before maxAge passes.
func (c *cached[T]) GetStale(maxAge time.Duration, load func() (T, error)) (v T, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok = c.val, c.has
	if c.inflight == nil && (!c.checkedOK || c.now().Sub(c.checked) >= maxAge) {
		f := &flight[T]{done: make(chan struct{})}
		c.inflight = f
		go func() {
			finished := false
			defer func() {
				if !finished {
					c.finish(f, f.val, context.Canceled, true)
				}
			}()
			nv, err := load()
			finished = true
			c.finish(f, nv, err, true)
		}()
	}
	return v, ok
}

func (c *cached[T]) finish(f *flight[T], v T, err error, background bool) {
	c.mu.Lock()
	f.val, f.err = v, err
	if err == nil {
		c.val, c.has = v, true
		c.loadedAt = c.now()
	}
	if background {
		c.checked, c.checkedOK = c.now(), true
	}
	c.inflight = nil
	c.mu.Unlock()
	close(f.done)
}
