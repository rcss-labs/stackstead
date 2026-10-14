package main

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/config"
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
