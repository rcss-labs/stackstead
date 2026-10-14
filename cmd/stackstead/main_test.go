package main

import (
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
