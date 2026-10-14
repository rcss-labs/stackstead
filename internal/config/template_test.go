package config_test

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/config"
)

func TestTemplateWithDefaultsEqualsDeployExample(t *testing.T) {
	want, err := os.ReadFile("../../deploy/config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	got, err := config.Template(config.Defaults())
	if err != nil {
		t.Fatalf("Template: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("Template(Defaults()) differs from deploy/config.example.yaml:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestTemplateSubstitutesValues(t *testing.T) {
	cfg := config.Defaults()
	cfg.Listen = "0.0.0.0:9000"
	cfg.StacksDir = "/srv/stacks"
	cfg.DataDir = "/srv/data"
	cfg.MetricsRetention = 90 * time.Minute
	cfg.OperationTimeout = 30 * time.Minute
	cfg.DockerSocket = "/run/user/1000/docker.sock"
	out, err := config.Template(cfg)
	if err != nil {
		t.Fatalf("Template: %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"listen: 0.0.0.0:9000\n",
		"stacks_dir: /srv/stacks\n",
		"data_dir: /srv/data\n",
		"metrics_retention: 1h30m\n",
		"operation_timeout: 30m\n",
		"\ndocker_socket: /run/user/1000/docker.sock\n",
		"# Address the web UI listens on.",
		"# Directories the file browser may open",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
}

func TestTemplateOutputLoads(t *testing.T) {
	cfg := config.Defaults()
	cfg.Listen = "127.0.0.1:9999"
	cfg.StacksDir = "/srv/my stacks" // needs quoting-safe rendering
	cfg.DataDir = "/srv/data"
	cfg.MetricsRetention = 36 * time.Hour
	cfg.DockerSocket = "/run/docker.sock"
	cfg.ShutdownGrace = 45 * time.Second
	out, err := config.Template(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := writeConfig(t, string(out))
	loaded, err := config.Load(path, env(nil))
	if err != nil {
		t.Fatalf("Load of rendered template: %v\n%s", err, out)
	}
	cfg.AllowedRoots = []string{cfg.StacksDir}
	if !reflect.DeepEqual(loaded, cfg) {
		t.Errorf("round trip = %+v, want %+v", loaded, cfg)
	}
}

func TestEffectiveReportsSources(t *testing.T) {
	path := writeConfig(t, "listen: 127.0.0.1:7000\nmetrics_interval: 20s\n")
	getenv := env(map[string]string{"STACKSTEAD_DATA_DIR": "/srv/data"})
	cfg, err := config.Load(path, getenv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := config.Effective(cfg, path, getenv)
	if err != nil {
		t.Fatalf("Effective: %v", err)
	}
	want := []config.Setting{
		{Key: "listen", Value: "127.0.0.1:7000", Source: "file"},
		{Key: "stacks_dir", Value: "/opt/stacks", Source: "default"},
		{Key: "data_dir", Value: "/srv/data", Source: "env"},
		{Key: "allowed_roots", Value: "/opt/stacks", Source: "default"},
		{Key: "metrics_retention", Value: "72h", Source: "default"},
		{Key: "metrics_interval", Value: "20s", Source: "file"},
		{Key: "operation_timeout", Value: "15m", Source: "default"},
		{Key: "docker_socket", Value: "/var/run/docker.sock", Source: "default"},
		{Key: "shutdown_grace", Value: "30s", Source: "default"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Effective =\n%+v\nwant\n%+v", got, want)
	}
}

func TestEffectiveJoinsAllowedRoots(t *testing.T) {
	getenv := env(map[string]string{"STACKSTEAD_ALLOWED_ROOTS": "/a, /b"})
	cfg, err := config.Load("", getenv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := config.Effective(cfg, "", getenv)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got {
		if s.Key == "allowed_roots" && (s.Value != "/a, /b" || s.Source != "env") {
			t.Errorf("allowed_roots = %+v", s)
		}
	}
}
