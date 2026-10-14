package api_test

import (
	"errors"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/metrics"
)

// newTokenCacheEnv returns an environment whose server clock reads
// time.Unix(0,0) + *offset, and a helper that writes a fresh token hash
// straight into the fake store (as `stackstead admin metrics-token` does,
// behind the server's back) and returns the plaintext.
func newTokenCacheEnv(t *testing.T, fake *fakeObserve) (env *testEnv, offset *time.Duration, writeBehind func() string) {
	t.Helper()
	offset = new(time.Duration)
	st := newFakeStacks()
	env = newEnvWith(t, "", func(o *api.Options) {
		o.Stacks, o.Operations, o.FSRoots, o.Status = st, fakeOps{st}, st.roots, st.status
		o.Observe = fake
		o.Now = func() time.Time { return time.Unix(0, 0).Add(*offset) }
	})
	writeBehind = func() string {
		plain, hash, err := metrics.NewToken()
		if err != nil {
			t.Fatal(err)
		}
		fake.mu.Lock()
		fake.tokenHash = hash
		fake.mu.Unlock()
		return plain
	}
	return env, offset, writeBehind
}

// A hash written to the store behind the server's back is picked up on a
// failed /metrics request, at most once per 5 s.
func TestMetricsTokenCacheRevalidates(t *testing.T) {
	fake := newFakeObserve()
	env, offset, writeBehind := newTokenCacheEnv(t, fake)

	if rec := scrape(t, env, ""); rec.Code != 404 {
		t.Fatalf("no token configured = %d, want 404", rec.Code)
	}

	first := writeBehind()
	*offset = 6 * time.Second
	if rec := scrape(t, env, "Bearer "+first); rec.Code != 200 {
		t.Fatalf("token written behind the server's back = %d, want 200 after revalidation", rec.Code)
	}

	second := writeBehind()
	*offset = 8 * time.Second // 2 s after the last load: the re-read is bounded
	if rec := scrape(t, env, "Bearer "+second); rec.Code != 401 {
		t.Fatalf("rotated within 5 s = %d, want 401", rec.Code)
	}
	*offset = 12 * time.Second
	if rec := scrape(t, env, "Bearer "+second); rec.Code != 200 {
		t.Fatalf("rotated, 5 s later = %d, want 200", rec.Code)
	}
}

// At one instant the cache serves everything: at most one store read.
func TestMetricsCacheReadsOncePerInterval(t *testing.T) {
	fake := newFakeObserve()
	env, _, writeBehind := newTokenCacheEnv(t, fake)
	token := writeBehind()
	for i := range 100 {
		if rec := scrape(t, env, "Bearer "+token); rec.Code != 200 {
			t.Fatalf("request %d = %d", i, rec.Code)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.tokenReads > 1 {
		t.Errorf("token reads = %d across 100 requests at one instant, want <= 1", fake.tokenReads)
	}
}

// A rotation done offline must stop the old token within the TTL even though
// every request keeps matching.
func TestMetricsOldTokenRejectedAfterOfflineRotate(t *testing.T) {
	fake := newFakeObserve()
	env, offset, writeBehind := newTokenCacheEnv(t, fake)
	old := writeBehind()
	if rec := scrape(t, env, "Bearer "+old); rec.Code != 200 {
		t.Fatalf("initial = %d", rec.Code)
	}
	fresh := writeBehind()
	*offset = 4 * time.Second
	if rec := scrape(t, env, "Bearer "+old); rec.Code != 200 {
		t.Fatalf("within TTL = %d, want 200 (bounded staleness)", rec.Code)
	}
	*offset = 5 * time.Second
	if rec := scrape(t, env, "Bearer "+old); rec.Code != 401 {
		t.Fatalf("old token after TTL = %d, want 401", rec.Code)
	}
	if rec := scrape(t, env, "Bearer "+fresh); rec.Code != 200 {
		t.Fatalf("new token after TTL = %d, want 200", rec.Code)
	}
}

func TestMetricsEndpointDisappearsAfterOfflineClear(t *testing.T) {
	fake := newFakeObserve()
	env, offset, writeBehind := newTokenCacheEnv(t, fake)
	token := writeBehind()
	if rec := scrape(t, env, "Bearer "+token); rec.Code != 200 {
		t.Fatalf("initial = %d", rec.Code)
	}
	fake.mu.Lock()
	fake.tokenHash = ""
	fake.mu.Unlock()
	*offset = 5 * time.Second
	if rec := scrape(t, env, "Bearer "+token); rec.Code != 404 {
		t.Fatalf("after offline clear = %d, want 404", rec.Code)
	}
}

func TestMetricsKeepsCachedHashWhenRevalidationFails(t *testing.T) {
	fake := newFakeObserve()
	env, offset, writeBehind := newTokenCacheEnv(t, fake)
	token := writeBehind()
	if rec := scrape(t, env, "Bearer "+token); rec.Code != 200 {
		t.Fatalf("initial = %d", rec.Code)
	}
	fake.mu.Lock()
	fake.tokenErr = errors.New("db busy")
	fake.mu.Unlock()
	*offset = 6 * time.Second
	if rec := scrape(t, env, "Bearer "+token); rec.Code != 200 {
		t.Fatalf("with failing store = %d, want 200 from cache", rec.Code)
	}
}
