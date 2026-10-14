package stacks

import (
	"context"
	"sync"
	"time"
)

// versionCache wraps a Compose so the `docker compose version` probe, which
// runs on every lifecycle call and status poll, is spawned at most once per
// ttl. Failures are cached for the same short ttl, so a recovered plugin is
// noticed quickly.
type versionCache struct {
	Compose
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	at      time.Time
	version string
	err     error
	have    bool
}

// NewVersionCache returns c with Version cached for ttl. A nil now means time.Now.
func NewVersionCache(c Compose, ttl time.Duration, now func() time.Time) Compose {
	if now == nil {
		now = time.Now
	}
	return &versionCache{Compose: c, ttl: ttl, now: now}
}

func (v *versionCache) Version(ctx context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.have && v.now().Sub(v.at) < v.ttl {
		return v.version, v.err
	}
	version, err := v.Compose.Version(ctx)
	if ctx.Err() != nil {
		return version, err // a cancelled probe says nothing about compose
	}
	v.version, v.err, v.at, v.have = version, err, v.now(), true
	return version, err
}
