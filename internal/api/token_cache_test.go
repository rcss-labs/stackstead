package api_test

import (
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

func TestMetricsCacheNotReReadOnSuccess(t *testing.T) {
	fake := newFakeObserve()
	env, offset, writeBehind := newTokenCacheEnv(t, fake)
	token := writeBehind()

	scrape(t, env, "Bearer "+token) // loads the cache
	fake.mu.Lock()
	reads := fake.tokenReads
	fake.mu.Unlock()
	for i := range 100 {
		*offset = time.Duration(i+1) * 10 * time.Second
		if rec := scrape(t, env, "Bearer "+token); rec.Code != 200 {
			t.Fatalf("request %d = %d", i, rec.Code)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.tokenReads != reads {
		t.Errorf("token reads grew from %d to %d across successful requests", reads, fake.tokenReads)
	}
}
