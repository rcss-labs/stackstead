package api

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// flightCache shares a detached computation while each caller waits on its own context.
type flightCache[T any] struct {
	mu       sync.Mutex
	at       time.Time
	body     T
	ok       bool
	inflight *cacheFlight[T]
	gen      uint64
}

type cacheFlight[T any] struct {
	done chan struct{}
	gen  uint64
	body T
	err  error
}

func (c *flightCache[T]) invalidate() {
	c.mu.Lock()
	c.ok = false
	c.gen++
	c.inflight = nil
	c.mu.Unlock()
}

func (c *flightCache[T]) get(ctx context.Context, now time.Time, ttl, timeout time.Duration, compute func(context.Context) (T, error)) (T, error) {
	c.mu.Lock()
	if c.ok && now.Sub(c.at) < ttl {
		body := c.body
		c.mu.Unlock()
		return body, nil
	}
	f := c.inflight
	if f == nil {
		f = &cacheFlight[T]{done: make(chan struct{}), gen: c.gen}
		c.inflight = f
		go c.run(ctx, f, now, timeout, compute)
	}
	c.mu.Unlock()
	select {
	case <-f.done:
		return f.body, f.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

func (c *flightCache[T]) run(ctx context.Context, f *cacheFlight[T], now time.Time, timeout time.Duration, compute func(context.Context) (T, error)) {
	qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			var zero T
			f.body, f.err = zero, fmt.Errorf("list computation panicked: %v", r)
		}
		c.mu.Lock()
		if c.inflight == f {
			c.inflight = nil
		}
		if f.err == nil && f.gen == c.gen {
			c.body, c.at, c.ok = f.body, now, true
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.body, f.err = compute(qctx)
}
