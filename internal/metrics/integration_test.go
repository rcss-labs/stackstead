//go:build integration

package metrics_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/metrics"
	"github.com/rcss-labs/stackstead/internal/state"
	"github.com/rcss-labs/stackstead/internal/store"
)

// itSocket returns the first Docker socket that answers a ping, or "".
func itSocket() string {
	var candidates []string
	if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		candidates = append(candidates, strings.TrimPrefix(h, "unix://"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".docker", "run", "docker.sock"))
	}
	candidates = append(candidates, "/var/run/docker.sock")
	for _, c := range candidates {
		conn, err := net.DialTimeout("unix", c, time.Second)
		if err != nil {
			continue
		}
		conn.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err = engine.New(c, 3*time.Second).Ping(ctx)
		cancel()
		if err == nil {
			return c
		}
	}
	return ""
}

// itDocker skips the test without Docker and returns a runner for docker CLI
// commands against the same engine plus the engine client.
func itDocker(t *testing.T) (run func(args ...string) (string, error), eng *engine.Client) {
	t.Helper()
	sock := itSocket()
	if sock == "" {
		t.Skip("no reachable Docker socket")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not installed")
	}
	run = func(args ...string) (string, error) {
		cmd := exec.Command("docker", args...)
		cmd.Env = append(os.Environ(), "DOCKER_HOST=unix://"+sock)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := run("image", "inspect", "busybox"); err != nil {
		if out, err := run("pull", "busybox"); err != nil {
			t.Skipf("busybox unavailable: %v: %s", err, out)
		}
	}
	return run, engine.New(sock, 5*time.Second)
}

// itName returns a unique resource name and registers its removal. Call it
// before creating anything so cleanup runs even when creation fails.
func itName(t *testing.T, run func(args ...string) (string, error)) string {
	t.Helper()
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	name := "stackstead-it-" + hex.EncodeToString(raw)
	t.Cleanup(func() { run("rm", "-f", name) })
	return name
}

func TestSamplerWritesSamples(t *testing.T) {
	run, eng := itDocker(t)
	name := itName(t, run)
	project := name + "-project"
	if out, err := run("run", "-d", "--name", name, "--label", "com.docker.compose.project="+project,
		"--label", "com.docker.compose.service=web", "busybox", "sleep", "60"); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}

	db, err := store.Open(filepath.Join(t.TempDir(), "it.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := metrics.NewStore(db, time.Now)
	cache := state.New(eng, nil, state.Backoff{})

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); cache.Run(ctx) }()
	sampler := metrics.NewSampler(st, cache, eng, time.Second, time.Hour, nil, time.Now)
	go func() { defer wg.Done(); sampler.Run(ctx) }()

	time.Sleep(6 * time.Second) // each stats read takes ~1s, so ticks run back to back
	cancel()
	wg.Wait()

	var n int
	var maxMem int64
	row := db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(mem), 0) FROM metric_samples WHERE container = ?`, name)
	if err := row.Scan(&n, &maxMem); err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Errorf("rows for %s = %d, want >= 2", name, n)
	}
	if maxMem <= 0 {
		t.Errorf("max mem = %d, want > 0", maxMem)
	}
}
