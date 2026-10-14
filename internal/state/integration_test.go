//go:build integration

package state_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/state"
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

func TestCacheSeesStartStop(t *testing.T) {
	run, eng := itDocker(t)
	name := itName(t, run)

	ctx, cancel := context.WithCancel(context.Background())
	cache := state.New(eng, nil, state.Backoff{})
	done := make(chan struct{})
	go func() { cache.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	waitFor(t, 10*time.Second, "cache connected", cache.Connected)

	project := name + "-project"
	if out, err := run("run", "-d", "--name", name, "--label", "com.docker.compose.project="+project,
		"--label", "com.docker.compose.service=web", "busybox", "sleep", "60"); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	waitFor(t, 2*time.Second, "container running in cache", func() bool { return stateOf(cache, project) == "running" })

	if out, err := run("stop", "-t", "1", name); err != nil {
		t.Fatalf("docker stop: %v: %s", err, out)
	}
	waitFor(t, 2*time.Second, "container no longer running in cache", func() bool { return stateOf(cache, project) != "running" })
}

func stateOf(c *state.Cache, project string) string {
	cs, err := c.Containers(context.Background())
	if err != nil {
		return "error"
	}
	for _, ct := range cs {
		if ct.Project() == project {
			return ct.State
		}
	}
	return "absent"
}

func waitFor(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for: %s", limit, what)
}
