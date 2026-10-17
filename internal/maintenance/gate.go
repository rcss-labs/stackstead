// Package maintenance controls write admission during managed updates.
package maintenance

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrFrozen = errors.New("maintenance in progress")
	ErrBusy   = errors.New("writes are active")
)

// Gate atomically coordinates mutation admission with update quiescence.
// A nil gate admits writes, preserving ordinary startup without an updater.
type Gate struct {
	mu     sync.Mutex
	active int
	frozen bool
	thawed chan struct{}
}

func New() *Gate { return &Gate{} }

// Enter holds admission until the returned idempotent release is called.
func (g *Gate) Enter() (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.frozen {
		return nil, ErrFrozen
	}
	g.active++
	var once sync.Once
	return func() { once.Do(func() { g.mu.Lock(); g.active--; g.mu.Unlock() }) }, nil
}

// Freeze refuses active writes without changing admission; callers retry after
// draining operations. Success guarantees that no admitted writer remains.
func (g *Gate) Freeze() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active != 0 {
		return ErrBusy
	}
	if !g.frozen {
		g.frozen = true
		g.thawed = make(chan struct{})
	}
	return nil
}

// Thaw is reserved for trusted update control after durable success.
func (g *Gate) Thaw() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.frozen {
		g.frozen = false
		close(g.thawed)
	}
}
func (g *Gate) Frozen() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.frozen
}

// Wait delays startup work until admission reopens or shutdown cancels it.
// Callers still Enter before writing: a new Freeze may race the wakeup.
func (g *Gate) Wait(ctx context.Context) error {
	if g == nil {
		return ctx.Err()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		g.mu.Lock()
		frozen, ch := g.frozen, g.thawed
		g.mu.Unlock()
		if !frozen {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
