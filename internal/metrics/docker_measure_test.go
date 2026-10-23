package metrics_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/metrics"
)

type statsAPI interface {
	Stats(ctx context.Context, id string) (engine.Stats, error)
}

// measuringStats wraps a StatsSource and records each call's duration and how
// long after the surrounding tick started the daemon took its reading.
type measuringStats struct {
	src         statsAPI
	mu          sync.Mutex
	dur         []time.Duration
	tickStart   time.Time
	startToRead []time.Duration // Stats.Read minus tickStart
}

func (m *measuringStats) Stats(ctx context.Context, id string) (engine.Stats, error) {
	start := time.Now()
	st, err := m.src.Stats(ctx, id)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dur = append(m.dur, time.Since(start))
	if err == nil && !m.tickStart.IsZero() {
		m.startToRead = append(m.startToRead, st.Read.Sub(m.tickStart))
	}
	return st, err
}

type labelLister struct {
	c     *engine.Client
	label string
}

func (l labelLister) Containers(ctx context.Context) ([]engine.Container, error) {
	all, err := l.c.Containers(ctx)
	if err != nil {
		return nil, err
	}
	var out []engine.Container
	for _, c := range all {
		if c.Labels["stackstead.measure"] == l.label {
			out = append(out, c)
		}
	}
	return out, nil
}

func quantile(d []time.Duration, q float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(q*float64(len(s)-1))]
}

// TestMeasureSamplerAgainstDocker times the sampler's stats fan-out against a
// real Docker daemon. It is opt-in: set STACKSTEAD_DOCKER_MEASURE to the number
// of containers to start (for example 40), optionally STACKSTEAD_DOCKER_IMAGE
// (default busybox:1.37, which must already be present: nothing is pulled) and
// DOCKER_SOCKET. Resource budget: N idle `sleep` containers, each limited to
// 16 MiB and 0.1 CPU, labelled stackstead.measure=<unique id> and always
// removed on exit.
func TestMeasureSamplerAgainstDocker(t *testing.T) {
	raw := os.Getenv("STACKSTEAD_DOCKER_MEASURE")
	if testing.Short() || raw == "" {
		t.Skip("set STACKSTEAD_DOCKER_MEASURE=<container count> to run against a local Docker daemon")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		t.Fatalf("STACKSTEAD_DOCKER_MEASURE=%q is not a positive count", raw)
	}
	image := os.Getenv("STACKSTEAD_DOCKER_IMAGE")
	if image == "" {
		image = "busybox:1.37"
	}
	socket := os.Getenv("DOCKER_SOCKET")
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	project := "stackstead-measure-" + id

	cleanup := func() {
		out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=stackstead.measure="+id).Output()
		if ids := strings.Fields(string(out)); len(ids) > 0 {
			exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).Run()
		}
	}
	t.Cleanup(cleanup)
	for i := 0; i < n; i++ {
		out, err := exec.Command("docker", "run", "-d", "--pull=never", "--memory", "16m", "--cpus", "0.1",
			"--name", fmt.Sprintf("%s-%d", project, i),
			"--label", "stackstead.measure="+id,
			"--label", "com.docker.compose.project="+project, "--label", "com.docker.compose.service=sleep",
			image, "sleep", "600").CombinedOutput()
		if err != nil {
			t.Fatalf("start container %d: %v: %s", i, err, out)
		}
	}

	client := engine.New(socket, 10*time.Second)
	ctx := context.Background()
	lister := labelLister{client, id}
	cs, err := lister.Containers(ctx)
	if err != nil || len(cs) != n {
		t.Fatalf("listed %d containers, err %v; want %d", len(cs), err, n)
	}
	t.Logf("%d x %s containers, docker socket %s", n, image, socket)

	// Fan-out cost by concurrency, calling Stats directly.
	for _, conc := range []int{1, 4, 8, 16, 32} {
		ms := &measuringStats{src: client}
		start := time.Now()
		ms.tickStart = start
		sem := make(chan struct{}, conc)
		var wg sync.WaitGroup
		for _, c := range cs {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				ms.Stats(cctx, c.ID)
			}()
		}
		wg.Wait()
		total := time.Since(start)
		t.Logf("concurrency %2d: pass of %d containers %s; per-call p50 %s p95 %s; Stats.Read minus pass start: p50 %s max %s",
			conc, n, total.Round(time.Millisecond), quantile(ms.dur, .5).Round(time.Millisecond), quantile(ms.dur, .95).Round(time.Millisecond),
			quantile(ms.startToRead, .5).Round(time.Millisecond), quantile(ms.startToRead, 1).Round(time.Millisecond))
	}

	// The real sampler tick at the shipped concurrency.
	ms := &measuringStats{src: client}
	s := metrics.NewSampler(nil, lister, ms, 10*time.Second, time.Hour, nil, nil)
	s.UseStore(discardStore{})
	ticks := make(chan time.Time)
	done := make(chan struct{}, 1)
	s.UseTicks(ticks)
	s.OnTickDone(func() { done <- struct{}{} })
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.Run(runCtx)
	for i := 0; i < 3; i++ {
		start := time.Now()
		ms.mu.Lock()
		ms.tickStart = start
		ms.mu.Unlock()
		ticks <- start
		<-done
		t.Logf("sampler tick %d (concurrency %d): %s for %d containers (interval is 10s)", i+1, metrics.MaxStatsConcurrency, time.Since(start).Round(time.Millisecond), n)
	}
	cancel()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	t.Logf("tick stamp vs Stats.Read over 3 sampler ticks: Read is %s (p50) and %s (max) after the tick start that stamps the sample",
		quantile(ms.startToRead, .5).Round(time.Millisecond), quantile(ms.startToRead, 1).Round(time.Millisecond))
}

type discardStore struct{}

func (discardStore) InsertBatch(context.Context, []metrics.Sample) error { return nil }
func (discardStore) Prune(context.Context, time.Time) (int64, error)     { return 0, nil }
