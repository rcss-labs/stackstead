package update_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/update"
)

var _ api.UpdateAPI = (*update.Checker)(nil)

var t0 = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.t }
func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

// releaseServer answers SHA256SUMS with the current version in `version`,
// or the status in `status` when non-200 or the body in `body` when set.
type releaseServer struct {
	*httptest.Server
	hits    atomic.Int32
	mu      sync.Mutex
	version string
	status  int
	body    string
}

func (r *releaseServer) set(version string, status int, body string) {
	r.mu.Lock()
	r.version, r.status, r.body = version, status, body
	r.mu.Unlock()
}

func newReleaseServer(t *testing.T, version string) *releaseServer {
	t.Helper()
	r := &releaseServer{version: version, status: 200}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		r.hits.Add(1)
		r.mu.Lock()
		version, status, body := r.version, r.status, r.body
		r.mu.Unlock()
		if status != 200 {
			http.Error(w, "boom", status)
			return
		}
		if body == "" {
			z := strings.Repeat("a", 64)
			body = z + "  stackstead_" + version + "_linux_amd64.tar.gz\n" + z + "  install.sh\n"
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(r.Close)
	return r
}

type harness struct {
	c      *update.Checker
	clock  *fakeClock
	srv    *releaseServer
	afters chan time.Duration // durations passed to After
	tick   chan time.Time     // feed to release the pending After
	logs   *bytes.Buffer
}

func newHarness(t *testing.T, current string, enabled bool, latest string) *harness {
	t.Helper()
	srv := newReleaseServer(t, latest)
	src, err := update.NewSource(srv.URL, "stackstead/test")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{clock: &fakeClock{t: t0}, srv: srv, afters: make(chan time.Duration, 16), tick: make(chan time.Time), logs: &bytes.Buffer{}}
	h.c = update.NewChecker(update.CheckerOptions{
		Source: src, Enabled: enabled, Current: current, Kind: update.KindDeb, GOOS: "linux", Exe: "/usr/bin/stackstead",
		Now:    h.clock.Now,
		After:  func(d time.Duration) <-chan time.Time { h.afters <- d; return h.tick },
		Logger: slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	return h
}

func (h *harness) nextAfter(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-h.afters:
		return d
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not wait on After")
		return 0
	}
}

func (h *harness) release(t *testing.T) {
	t.Helper()
	select {
	case h.tick <- h.clock.Now():
	case <-time.After(2 * time.Second):
		t.Fatal("Run was not waiting")
	}
}

func TestCheckerDisabledNeverFetches(t *testing.T) {
	h := newHarness(t, "0.2.0", false, "0.3.0")
	done := make(chan struct{})
	go func() { h.c.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return for a disabled checker")
	}
	st := h.c.CheckNow(context.Background())
	if h.srv.hits.Load() != 0 {
		t.Errorf("requests = %d, want 0", h.srv.hits.Load())
	}
	if st.State != "disabled" || st.Command != "sudo /usr/bin/stackstead upgrade" || st.Installer != "deb" || st.Current != "0.2.0" {
		t.Errorf("status = %+v", st)
	}
}

func TestCheckerDevBuildNeverFetches(t *testing.T) {
	h := newHarness(t, "dev-dirty", true, "0.3.0")
	done := make(chan struct{})
	go func() { h.c.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return for a dev build")
	}
	st := h.c.CheckNow(context.Background())
	if h.srv.hits.Load() != 0 {
		t.Errorf("requests = %d, want 0", h.srv.hits.Load())
	}
	if st.State != "unknown" || st.Current != "dev-dirty" {
		t.Errorf("status = %+v", st)
	}
}

func TestCheckerCurrentDropsLeadingV(t *testing.T) {
	h := newHarness(t, "v0.2.0", true, "0.3.0")
	if got := h.c.Status().Current; got != "0.2.0" {
		t.Errorf("Current = %q", got)
	}
}

func TestCheckerCadence(t *testing.T) {
	h := newHarness(t, "0.2.0", true, "0.3.0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.c.Run(ctx); close(done) }()

	if d := h.nextAfter(t); d != 10*time.Second {
		t.Fatalf("initial delay = %v", d)
	}
	if h.srv.hits.Load() != 0 {
		t.Fatal("fetched before the initial delay")
	}
	h.release(t)
	if d := h.nextAfter(t); d != 24*time.Hour {
		t.Fatalf("after success = %v", d)
	}
	if h.srv.hits.Load() != 1 {
		t.Fatalf("requests = %d", h.srv.hits.Load())
	}
	h.srv.set("", 500, "")
	h.release(t)
	if d := h.nextAfter(t); d != time.Hour {
		t.Fatalf("after failure = %v", d)
	}
	if h.srv.hits.Load() != 2 {
		t.Fatalf("requests = %d", h.srv.hits.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}

func TestCheckerAvailableOnlyAfterSuccess(t *testing.T) {
	h := newHarness(t, "0.2.0", true, "0.3.0")
	st := h.c.Status()
	if st.State != "unknown" || st.Error != "" || !st.CheckedAt.IsZero() || st.Latest != "" {
		t.Fatalf("before any attempt: %+v", st)
	}
	h.srv.set("", 500, "")
	if st = h.c.CheckNow(context.Background()); st.State != "failed" || st.Error != "http" {
		t.Fatalf("after failure: %+v", st)
	}
	h.clock.Advance(2 * time.Minute)
	h.srv.set("0.3.0", 200, "")
	if st = h.c.CheckNow(context.Background()); st.State != "available" || st.Error != "" {
		t.Fatalf("after success: %+v", st)
	}
}

func TestCheckerUpToDate(t *testing.T) {
	h := newHarness(t, "0.3.0", true, "0.3.0")
	st := h.c.CheckNow(context.Background())
	if st.State != "up-to-date" || st.Latest != "0.3.0" || !st.CheckedAt.Equal(t0) || st.Error != "" {
		t.Errorf("status = %+v", st)
	}
}

func TestCheckerAvailable(t *testing.T) {
	h := newHarness(t, "0.2.0", true, "0.3.0")
	st := h.c.CheckNow(context.Background())
	if st.State != "available" || st.Latest != "0.3.0" || st.NotesURL != "" || !st.CheckedAt.Equal(t0) {
		t.Errorf("status = %+v", st)
	}
}

func TestCheckerKeepsLastSuccessAcrossFailure(t *testing.T) {
	h := newHarness(t, "0.2.0", true, "0.3.0")
	h.c.CheckNow(context.Background())
	h.clock.Advance(time.Hour)
	h.srv.set("", 500, "")
	st := h.c.CheckNow(context.Background())
	if st.State != "available" || st.Error != "http" || st.Latest != "0.3.0" || !st.CheckedAt.Equal(t0) {
		t.Errorf("status = %+v", st)
	}
}

func TestCheckerFailureCodes(t *testing.T) {
	t.Run("http", func(t *testing.T) {
		h := newHarness(t, "0.2.0", true, "0.3.0")
		h.srv.set("", 503, "")
		if got := h.c.CheckNow(context.Background()).Error; got != "http" {
			t.Errorf("error = %q", got)
		}
	})
	t.Run("malformed", func(t *testing.T) {
		h := newHarness(t, "0.2.0", true, "0.3.0")
		h.srv.set("", 200, "not a checksum file\n")
		if got := h.c.CheckNow(context.Background()).Error; got != "malformed" {
			t.Errorf("error = %q", got)
		}
	})
	t.Run("network", func(t *testing.T) {
		h := newHarness(t, "0.2.0", true, "0.3.0")
		h.srv.Close()
		if got := h.c.CheckNow(context.Background()).Error; got != "network" {
			t.Errorf("error = %q", got)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		h := newHarness(t, "0.2.0", true, "0.3.0")
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		if got := h.c.CheckNow(ctx).Error; got != "timeout" {
			t.Errorf("error = %q", got)
		}
	})
}

func TestCheckNowRateLimited(t *testing.T) {
	h := newHarness(t, "0.2.0", true, "0.3.0")
	h.c.CheckNow(context.Background())
	h.clock.Advance(10 * time.Second)
	h.c.CheckNow(context.Background())
	if n := h.srv.hits.Load(); n != 1 {
		t.Fatalf("requests after 10 s = %d, want 1", n)
	}
	h.clock.Advance(51 * time.Second)
	h.c.CheckNow(context.Background())
	if n := h.srv.hits.Load(); n != 2 {
		t.Fatalf("requests after 61 s = %d, want 2", n)
	}
}

func TestCheckNowRateLimitCountsFailures(t *testing.T) {
	h := newHarness(t, "0.2.0", true, "0.3.0")
	h.srv.set("", 500, "")
	h.c.CheckNow(context.Background())
	h.clock.Advance(5 * time.Second)
	h.c.CheckNow(context.Background())
	if n := h.srv.hits.Load(); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
}

func TestCheckNowSerialised(t *testing.T) {
	h := newHarness(t, "0.2.0", true, "0.3.0")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); h.c.CheckNow(context.Background()) }()
	}
	wg.Wait()
	if n := h.srv.hits.Load(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

func TestCheckerLogsAvailableOnce(t *testing.T) {
	h := newHarness(t, "0.2.0", true, "0.3.0")
	h.c.CheckNow(context.Background())
	h.clock.Advance(time.Hour)
	h.c.CheckNow(context.Background())
	if n := strings.Count(h.logs.String(), "update available"); n != 1 {
		t.Errorf("info lines = %d in %q", n, h.logs.String())
	}
	if !strings.Contains(h.logs.String(), "level=INFO") || !strings.Contains(h.logs.String(), "version=0.3.0") {
		t.Errorf("log = %q", h.logs.String())
	}
}

func TestCheckerFailureLoggedOnceAtWarn(t *testing.T) {
	h := newHarness(t, "0.2.0", true, "0.3.0")
	h.srv.set("", 500, "")
	for i := 0; i < 3; i++ {
		h.c.CheckNow(context.Background())
		h.clock.Advance(time.Hour)
	}
	if n := strings.Count(h.logs.String(), "level=WARN"); n != 1 {
		t.Errorf("warn lines = %d in %q", n, h.logs.String())
	}
	if n := strings.Count(h.logs.String(), "level=DEBUG"); n != 2 {
		t.Errorf("debug lines = %d in %q", n, h.logs.String())
	}
	// a recovery then a new failure warns again
	h.srv.set("0.3.0", 200, "")
	h.c.CheckNow(context.Background())
	h.clock.Advance(time.Hour)
	h.srv.set("", 500, "")
	h.c.CheckNow(context.Background())
	if n := strings.Count(h.logs.String(), "level=WARN"); n != 2 {
		t.Errorf("warn lines after new streak = %d", n)
	}
}
