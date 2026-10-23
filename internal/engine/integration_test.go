//go:build integration

package engine_test

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

func TestLogsStream(t *testing.T) {
	script := "echo one; echo two >&2; echo three; echo four; sleep 30"
	for _, tc := range []struct {
		name  string
		tty   bool
		ttyOp bool // LogOptions.TTY passed to engine.Logs
	}{
		{"non-tty", false, false},
		{"tty-declared", true, true},
		{"tty-sniffed", true, false}, // TTY container read with TTY:false exercises the raw-stream sniffing
	} {
		t.Run(tc.name, func(t *testing.T) {
			run, eng := itDocker(t)
			name := itName(t, run)
			args := []string{"run", "-d", "--name", name}
			if tc.tty {
				args = append(args, "-t")
			}
			args = append(args, "busybox", "sh", "-c", script)
			if out, err := run(args...); err != nil {
				t.Fatalf("docker run: %v: %s", err, out)
			}
			time.Sleep(time.Second) // let the script print

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			lines, errs := eng.Logs(ctx, name, engine.LogOptions{Tail: -1, TTY: tc.ttyOp})
			got := map[string]string{}
			for len(got) < 4 {
				select {
				case l, ok := <-lines:
					if !ok {
						t.Fatalf("log stream ended with %v", got)
					}
					got[strings.TrimSpace(l.Text)] = l.Stream
				case err := <-errs:
					t.Fatalf("log stream error: %v (got %v)", err, got)
				case <-ctx.Done():
					t.Fatalf("timed out with %v", got)
				}
			}
			want := map[string]string{"one": "stdout", "two": "stderr", "three": "stdout", "four": "stdout"}
			if tc.tty {
				// A TTY merges stderr into stdout; the stream cannot be told apart.
				want["two"] = "stdout"
			}
			for text, stream := range want {
				if got[text] != stream {
					t.Errorf("line %q stream = %q, want %q (all: %v)", text, got[text], stream, got)
				}
			}
		})
	}
}

// TestExecIntegration runs real execs against a busybox container and checks
// the demuxed output and the exit codes Docker reports.
func TestExecIntegration(t *testing.T) {
	run, eng := itDocker(t)
	name := itName(t, run)
	if out, err := run("run", "-d", "--name", name, "busybox", "sleep", "60"); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	exec := func(cmd ...string) (stdout string, inspect engine.ExecInspect) {
		t.Helper()
		id, err := eng.CreateExec(ctx, name, engine.ExecSpec{Cmd: cmd})
		if err != nil {
			t.Fatalf("CreateExec: %v", err)
		}
		s, err := eng.StartExec(ctx, id, false)
		if err != nil {
			t.Fatalf("StartExec: %v", err)
		}
		defer s.Close()
		for {
			stream, p, err := s.ReadFrame()
			if err != nil {
				break
			}
			if stream == 1 {
				stdout += string(p)
			}
		}
		for i := 0; i < 50; i++ { // the exit code lands just after the stream closes
			if inspect, err = eng.InspectExec(ctx, id); err != nil {
				t.Fatalf("InspectExec: %v", err)
			}
			if !inspect.Running {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		return stdout, inspect
	}

	out, st := exec("echo", "hi")
	if out != "hi\n" || st.ExitCode != 0 {
		t.Errorf("echo hi: stdout %q, exit %d", out, st.ExitCode)
	}
	if _, st := exec("sh", "-c", "exit 3"); st.ExitCode != 3 {
		t.Errorf("exit 3: exit code %d", st.ExitCode)
	}
}

// TestBuildCachePruneByID may discover that an engine ignores the ID filter.
// Explicitly opt in only with a disposable daemon: it can prune all unused cache.
func TestBuildCachePruneByID(t *testing.T) {
	if os.Getenv("STACKSTEAD_ISOLATED_BUILD_PRUNE") != "1" {
		t.Skip("requires STACKSTEAD_ISOLATED_BUILD_PRUNE=1 and a disposable DOCKER_HOST")
	}
	host := os.Getenv("DOCKER_HOST")
	if !strings.HasPrefix(host, "unix://") || strings.TrimPrefix(host, "unix://") == "" {
		t.Fatal("explicit unix DOCKER_HOST required; never use socket discovery for prune")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI unavailable")
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command("docker", args...)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if out, err := run("buildx", "inspect", "--bootstrap"); err != nil {
		t.Skipf("no builder: %v: %s", err, out)
	}
	eng := engine.New(strings.TrimPrefix(host, "unix://"), 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	version, err := eng.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := eng.BuildPruneByID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Docker %s; advertised by-ID capability=%v", version, capability)
	records := func() map[string]bool {
		t.Helper()
		df, err := eng.DiskUsage(ctx, engine.DFBuildCache)
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, record := range df.BuildCache {
			ids[record.ID] = true
		}
		return ids
	}
	build := func() string {
		t.Helper()
		before := records()
		dir := t.TempDir()
		suffix := filepath.Base(dir)
		tag := "stackstead-cache-it-" + strings.ToLower(suffix)
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nCOPY payload /payload\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "payload"), []byte(dir), 0600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { run("image", "rm", tag) })
		if out, err := run("build", "--no-cache", "--progress=plain", "-t", tag, dir); err != nil {
			t.Fatalf("build: %v: %s", err, out)
		}
		if out, err := run("image", "rm", tag); err != nil {
			t.Fatalf("remove test image: %v: %s", err, out)
		}
		for id := range records() {
			if !before[id] {
				return id
			}
		}
		t.Fatal("builder produced no engine-visible build-cache record")
		return ""
	}
	first := build()
	second := build()
	if first == second {
		t.Fatal("builds did not produce distinct records")
	}
	reclaimed, pruneErr := eng.PruneBuildCache(ctx, []string{first})
	remaining := records()
	t.Logf("filtered prune target=%s other=%s reclaimed=%d targetRemaining=%v otherRemaining=%v error=%v", first, second, reclaimed, remaining[first], remaining[second], pruneErr)
	if pruneErr != nil || remaining[first] || !remaining[second] {
		if capability {
			t.Fatalf("advertised by-ID prune failed preservation: %v", pruneErr)
		}
		t.Skipf("Docker %s does not demonstrate exact ID filtering; capability correctly remains disabled", version)
	}
	t.Logf("Docker %s honored ID filter in this isolated test; enabling capability requires a separate verified commit", version)
}
