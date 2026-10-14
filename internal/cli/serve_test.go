package cli

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/config"
	"github.com/rcss-labs/stackstead/internal/version"
)

func TestBuildRunnerAlwaysPinsDockerHostToConfiguredSocket(t *testing.T) {
	for _, sock := range []string{config.Defaults().DockerSocket, "/run/user/1000/docker.sock"} {
		cfg := config.Defaults()
		cfg.DockerSocket = sock
		r := buildRunner(cfg)
		want := "DOCKER_HOST=unix://" + sock
		found := false
		for _, e := range r.Env {
			if e == want {
				found = true
			}
		}
		if !found {
			t.Errorf("socket %s: Env = %v, want %s", sock, r.Env, want)
		}
	}
}

func TestBuildRunnerGraceFitsInsideShutdownGrace(t *testing.T) {
	cases := []struct{ shutdown, want time.Duration }{
		{30 * time.Second, 10 * time.Second},
		{20 * time.Second, 10 * time.Second},
		{10 * time.Second, 5 * time.Second},
		{1 * time.Second, 500 * time.Millisecond},
		{100 * time.Millisecond, 500 * time.Millisecond},
		{0, 500 * time.Millisecond},
	}
	for _, c := range cases {
		cfg := config.Defaults()
		cfg.ShutdownGrace = c.shutdown
		if got := buildRunner(cfg).Grace; got != c.want {
			t.Errorf("ShutdownGrace %s: Grace = %s, want %s", c.shutdown, got, c.want)
		}
	}
}

func TestBackgroundStopCancelsAndWaitsForRunners(t *testing.T) {
	var finished atomic.Int32
	runner := func(ctx context.Context) {
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		finished.Add(1)
	}
	bg := startBackground(context.Background(), runner, runner)
	if !bg.stop(time.Second) {
		t.Fatal("stop reported a timeout")
	}
	if got := finished.Load(); got != 2 {
		t.Errorf("finished runners = %d, want 2 (stop must wait for them)", got)
	}
}

func TestBackgroundStopIsBoundedByGrace(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	bg := startBackground(context.Background(), func(context.Context) { <-release })
	start := time.Now()
	if bg.stop(50 * time.Millisecond) {
		t.Fatal("stop reported success although a runner is stuck")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("stop took %s, want about the 50ms grace", d)
	}
}

func TestShutdownOrder(t *testing.T) {
	var order []string
	bg := startBackground(context.Background(), func(ctx context.Context) {
		<-ctx.Done()
		order = append(order, "background")
	})
	shutdownServices(time.Second,
		func() error { order = append(order, "http"); return nil },
		func(context.Context) { order = append(order, "ops") },
		bg)
	want := []string{"http", "ops", "background"}
	if !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestServeLogsVersion(t *testing.T) {
	dir := t.TempDir()
	te := newTestEnv(t, map[string]string{
		"STACKSTEAD_LISTEN":        "127.0.0.1:0",
		"STACKSTEAD_DATA_DIR":      dir + "/data",
		"STACKSTEAD_STACKS_DIR":    dir + "/stacks",
		"STACKSTEAD_DOCKER_SOCKET": dir + "/nonexistent.sock",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	serveCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- serve(serveCtx, te.Env, nil) }()

	deadline := time.After(15 * time.Second)
	for !strings.Contains(te.Err.String(), "stackstead listening") {
		select {
		case err := <-done:
			t.Fatalf("serve exited early: %v\n%s", err, te.Err.String())
		case <-deadline:
			t.Fatalf("no listening line:\n%s", te.Err.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	stop()
	if err := <-done; err != nil {
		t.Errorf("serve returned %v after cancel", err)
	}
	if want := "version=" + version.Get().Version; !strings.Contains(te.Err.String(), want) {
		t.Errorf("startup log lacks %s:\n%s", want, te.Err.String())
	}
}
