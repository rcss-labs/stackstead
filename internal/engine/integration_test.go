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
