package stale

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Value holds one value with single-flight loading. At most one load runs at
// a time, whether it was started by Get (synchronous) or GetStale (background).
type Value[T any] struct {
	now func() time.Time

	mu               sync.Mutex
	val              T
	has              bool      // val holds a successfully loaded value
	loadedAt         time.Time // when the last load finished successfully
	checked          time.Time // when the last refresh finished
	checkedOK        bool      // checked is meaningful
	inflight         *flight[T]
	lastError        error
	invalidated      bool
	generation       uint64 // increases on invalidation; flights capture their starting generation
	loadedGeneration uint64 // generation of the last successful value
}

type flight[T any] struct {
	done       chan struct{}
	val        T
	err        error
	generation uint64
}

// New creates an empty value using now for freshness and load timestamps.
func New[T any](now func() time.Time) *Value[T] {
	return &Value[T]{now: now}
}

// Get returns the cached value when it is younger than maxAge. Otherwise the
// first caller runs load while concurrent callers wait for its result; a
// waiting caller gives up with ctx.Err() when its own context ends.
func (c *Value[T]) Get(ctx context.Context, maxAge time.Duration, load func(context.Context) (T, error)) (T, error) {
	for {
		c.mu.Lock()
		if c.has && !c.invalidated && c.loadedGeneration == c.generation && c.now().Sub(c.loadedAt) < maxAge {
			v := c.val
			c.mu.Unlock()
			return v, nil
		}
		if f := c.inflight; f != nil {
			generation := c.generation
			c.mu.Unlock()
			select {
			case <-f.done:
				// A caller arriving after invalidation cannot use a load that began
				// before it. Wait for that load to finish, then retry serially.
				if f.generation != generation {
					if err := ctx.Err(); err != nil {
						var zero T
						return zero, err
					}
					continue
				}
				return f.val, f.err
			case <-ctx.Done():
				var zero T
				return zero, ctx.Err()
			}
		}
		f := &flight[T]{done: make(chan struct{}), generation: c.generation}
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
}

// GetStale returns the last loaded value immediately. When the last refresh
// attempt is older than maxAge (or there was none) and no load is running, it
// starts one background load. ok is false until the first load succeeds. A
// failed refresh (including the first) keeps the old value and is not retried
// before maxAge passes, so a slow or failing Docker call is never hammered.
func (c *Value[T]) GetStale(maxAge time.Duration, load func() (T, error)) (v T, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok = c.val, c.has
	if c.inflight == nil && (c.invalidated || !c.checkedOK || c.now().Sub(c.checked) >= maxAge) {
		f := &flight[T]{done: make(chan struct{}), generation: c.generation}
		c.inflight = f
		c.invalidated = false
		go func() {
			var nv T
			var err error
			func() {
				// A panicking loader is a failed refresh, never a crash.
				defer func() {
					if r := recover(); r != nil {
						err = fmt.Errorf("refresh panicked: %v", r)
					}
				}()
				nv, err = load()
			}()
			c.finish(f, nv, err, true)
		}()
	}
	return v, ok
}

func (c *Value[T]) finish(f *flight[T], v T, err error, background bool) {
	c.mu.Lock()
	f.val, f.err = v, err
	if err == nil {
		c.val, c.has = v, true
		c.loadedAt = c.now()
		c.loadedGeneration = f.generation
		if f.generation == c.generation {
			c.invalidated = false
		}
	} else if !background {
		// A failed forced read cannot make its previous cached value fresh again.
		c.invalidated = true
	}
	c.lastError = err
	if background || err == nil {
		c.checked, c.checkedOK = c.now(), true
	}
	c.inflight = nil
	c.mu.Unlock()
	close(f.done)
}

// Metadata describes the last successful load and the current load attempt.
type Metadata struct {
	LoadedAt   time.Time
	Refreshing bool
	LastError  error
}

// Metadata returns a consistent snapshot. LoadedAt is zero until a load succeeds.
func (c *Value[T]) Metadata() Metadata {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Metadata{LoadedAt: c.loadedAt, Refreshing: c.inflight != nil, LastError: c.lastError}
}

// Invalidate makes the next read reload, preserving the previous value for
// stale readers. Invalidation during a load remains pending until the next read.
func (c *Value[T]) Invalidate() {
	c.mu.Lock()
	c.invalidated = true
	c.generation++
	c.mu.Unlock()
}
