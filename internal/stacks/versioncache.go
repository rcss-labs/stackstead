package stacks

import (
	"context"
	"sync"
	"time"
)

// defaultProbeTimeout bounds one `docker compose version` subprocess.
const defaultProbeTimeout = 3 * time.Second

// versionCache wraps a Compose so the `docker compose version` probe, which
// runs on every lifecycle call and status poll, is spawned at most once per
// ttl. Failures are cached for the same short ttl, so a recovered plugin is
// noticed quickly.
//
// The probe is single-flight and runs detached from any caller's context with
// its own timeout, so the mutex is never held across the subprocess and a hung
// plugin can delay a caller only until that caller's own context ends.
type versionCache struct {
	Compose
	ttl          time.Duration
	now          func() time.Time
	probeTimeout time.Duration

	mu      sync.Mutex
	at      time.Time
	version string
	err     error
	have    bool
	flight  *versionFlight
}

type versionFlight struct {
	done    chan struct{}
	version string
	err     error
}

// NewVersionCache returns c with Version cached for ttl. A nil now means time.Now.
func NewVersionCache(c Compose, ttl time.Duration, now func() time.Time) Compose {
	if now == nil {
		now = time.Now
	}
	return &versionCache{Compose: c, ttl: ttl, now: now, probeTimeout: defaultProbeTimeout}
}

func (v *versionCache) Version(ctx context.Context) (string, error) {
	v.mu.Lock()
	if v.have && v.now().Sub(v.at) < v.ttl {
		version, err := v.version, v.err
		v.mu.Unlock()
		return version, err
	}
	f := v.flight
	if f == nil {
		f = &versionFlight{done: make(chan struct{})}
		v.flight = f
		go v.probe(f)
	}
	v.mu.Unlock()

	select {
	case <-f.done:
		return f.version, f.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// probe runs the subprocess once for every caller waiting on f and caches the
// outcome. Its timeout, not any caller's context, ends a hung probe.
func (v *versionCache) probe(f *versionFlight) {
	ctx, cancel := context.WithTimeout(context.Background(), v.probeTimeout)
	defer cancel()
	version, err := v.Compose.Version(ctx)

	v.mu.Lock()
	v.version, v.err, v.at, v.have = version, err, v.now(), true
	v.flight = nil
	v.mu.Unlock()
	f.version, f.err = version, err
	close(f.done)
}
