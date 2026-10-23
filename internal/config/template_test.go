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

func TestTemplateExecLinesRoundTrip(t *testing.T) {
	cfg := config.Defaults()
	cfg.ExecEnabled = false
	cfg.ExecMaxSessions = 3
	cfg.ExecIdleTimeout = 0
	out, err := config.Template(cfg)
	if err != nil {
		t.Fatalf("Template: %v", err)
	}
	text := string(out)
	for _, want := range []string{"exec_enabled: false\n", "exec_max_sessions: 3\n", "exec_idle_timeout: 0s\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
	cfg.AllowedRoots = []string{cfg.StacksDir}
	loaded, err := config.Load(writeConfig(t, text), env(nil))
	if err != nil {
		t.Fatalf("Load of rendered template: %v\n%s", err, text)
	}
	if !reflect.DeepEqual(loaded, cfg) {
		t.Errorf("round trip = %+v, want %+v", loaded, cfg)
	}
}

func TestTemplateRendersAndLoadsImageLimits(t *testing.T) {
	cfg := config.Defaults()
	cfg.ImageMaxFileBytes = 268435456
	cfg.ImageMaxContextBytes = 2097152
	cfg.ImageOperationTimeout = 90 * time.Minute
	out, err := config.Template(cfg)
	if err != nil {
		t.Fatalf("Template: %v", err)
	}
	text := string(out)
	for _, want := range []string{
		"image_max_file_bytes: 268435456\n",
		"image_max_context_bytes: 2097152\n",
		"image_operation_timeout: 1h30m\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
	loaded, err := config.Load(writeConfig(t, text), env(nil))
	if err != nil {
		t.Fatalf("Load of rendered template: %v\n%s", err, text)
	}
	if loaded.ImageMaxFileBytes != cfg.ImageMaxFileBytes || loaded.ImageMaxContextBytes != cfg.ImageMaxContextBytes || loaded.ImageOperationTimeout != cfg.ImageOperationTimeout {
		t.Errorf("round trip image limits = %d/%d/%s, want %d/%d/%s",
			loaded.ImageMaxFileBytes, loaded.ImageMaxContextBytes, loaded.ImageOperationTimeout,
			cfg.ImageMaxFileBytes, cfg.ImageMaxContextBytes, cfg.ImageOperationTimeout)
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
		{Key: "update_check", Value: "true", Source: "default"},
		{Key: "exec_enabled", Value: "true", Source: "default"},
		{Key: "exec_max_sessions", Value: "8", Source: "default"},
		{Key: "exec_idle_timeout", Value: "30m", Source: "default"},
		{Key: "image_operation_timeout", Value: "1h", Source: "default"},
		{Key: "image_max_file_bytes", Value: "134217728", Source: "default"},
		{Key: "image_max_context_bytes", Value: "1073741824", Source: "default"},
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

func TestTemplateQuotesUnsafeScalars(t *testing.T) {
	cfg := config.Defaults()
	cfg.StacksDir = "/srv/a #b"
	cfg.DataDir = "/srv/data"
	cfg.AllowedRoots = []string{"/srv/a #b", "/srv/other"}
	out, err := config.Template(cfg)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, "stacks_dir: \"/srv/a #b\"\n") {
		t.Errorf("stacks_dir not quoted:\n%s", text)
	}
	if !strings.Contains(text, "\nallowed_roots:\n  - \"/srv/a #b\"\n  - /srv/other\n") {
		t.Errorf("allowed_roots not rendered:\n%s", text)
	}
	loaded, err := config.Load(writeConfig(t, text), env(nil))
	if err != nil {
		t.Fatalf("Load: %v\n%s", err, text)
	}
	if loaded.StacksDir != "/srv/a #b" || !reflect.DeepEqual(loaded.AllowedRoots, cfg.AllowedRoots) {
		t.Errorf("round trip = %q %v", loaded.StacksDir, loaded.AllowedRoots)
	}
}

func TestTemplateQuotesLeadingAt(t *testing.T) {
	cfg := config.Defaults()
	cfg.Listen = "@host:80" // a bare leading @ is reserved in YAML
	out, err := config.Template(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "listen: \"@host:80\"\n") {
		t.Errorf("leading @ not quoted:\n%s", out)
	}
}

func TestEffectiveEnvBeatsFile(t *testing.T) {
	path := writeConfig(t, "listen: 127.0.0.1:7000\n")
	getenv := env(map[string]string{"STACKSTEAD_LISTEN": "127.0.0.1:7100"})
	cfg, err := config.Load(path, getenv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := config.Effective(cfg, path, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != (config.Setting{Key: "listen", Value: "127.0.0.1:7100", Source: "env"}) {
		t.Errorf("listen = %+v, want env value and source", got[0])
	}
}

func TestEffectiveNullValueIsDefault(t *testing.T) {
	path := writeConfig(t, "listen:\nstacks_dir: /srv/s\n")
	cfg, err := config.Load(path, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	got, err := config.Effective(cfg, path, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Source != "default" || got[1].Source != "file" {
		t.Errorf("listen/stacks_dir sources = %q/%q, want default/file", got[0].Source, got[1].Source)
	}
}

func TestTemplateUpdateCheckLine(t *testing.T) {
	const commentBlock = "# Once a day the server fetches the latest release's SHA256SUMS from\n" +
		"# https://github.com/rcss-labs/stackstead to show \"update available\" in Settings.\n" +
		"# Set to false on air-gapped hosts. Nothing is ever installed automatically.\n"

	defaults, err := config.Template(config.Defaults())
	if err != nil {
		t.Fatalf("Template: %v", err)
	}
	if !strings.Contains(string(defaults), commentBlock+"# update_check: true\n") {
		t.Errorf("default output missing commented update_check block:\n%s", defaults)
	}

	cfg := config.Defaults()
	cfg.UpdateCheck = false
	off, err := config.Template(cfg)
	if err != nil {
		t.Fatalf("Template: %v", err)
	}
	if !strings.Contains(string(off), commentBlock+"update_check: false\n") {
		t.Errorf("output missing active update_check: false line:\n%s", off)
	}
	loaded, err := config.Load(writeConfig(t, string(off)), env(nil))
	if err != nil {
		t.Fatalf("Load of rendered template: %v\n%s", err, off)
	}
	if loaded.UpdateCheck {
		t.Errorf("rendered file loaded UpdateCheck = true, want false")
	}
}
