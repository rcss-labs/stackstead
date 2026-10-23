// Package revocation lets a credential's owner cancel the contexts of the
// long-lived requests that were authenticated with it.
package revocation

import (
	"context"
	"sync"
)

// Registry tracks watched contexts by a key describing the credential they
// were authenticated with (a session, a peer).
type Registry[K any] struct {
	mu      sync.Mutex
	next    uint64
	watches map[uint64]watch[K]
}

type watch[K any] struct {
	key    K
	cancel context.CancelFunc
}

// Watch returns a context derived from parent that Revoke can cancel. The
// returned stop function must be called when the request ends.
func (r *Registry[K]) Watch(parent context.Context, key K) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	r.mu.Lock()
	if r.watches == nil {
		r.watches = map[uint64]watch[K]{}
	}
	id := r.next
	r.next++
	r.watches[id] = watch[K]{key: key, cancel: cancel}
	r.mu.Unlock()
	return ctx, func() {
		cancel()
		r.mu.Lock()
		delete(r.watches, id)
		r.mu.Unlock()
	}
}

// Revoke cancels every watched context whose key matches.
func (r *Registry[K]) Revoke(matches func(K) bool) {
	r.mu.Lock()
	var cancels []context.CancelFunc
	for _, w := range r.watches {
		if matches(w.key) {
			cancels = append(cancels, w.cancel)
		}
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}
