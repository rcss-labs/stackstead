package config_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/config"
)

func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadWithoutFileUsesDefaults(t *testing.T) {
	cfg, err := config.Load("", env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := config.Config{
		Listen:           "127.0.0.1:8420",
		StacksDir:        "/opt/stacks",
		DataDir:          "/var/lib/stackstead",
		AllowedRoots:     []string{"/opt/stacks"},
		MetricsRetention: 72 * time.Hour,
		MetricsInterval:  10 * time.Second,
		OperationTimeout: 15 * time.Minute,
		DockerSocket:     "/var/run/docker.sock",
		ShutdownGrace:    30 * time.Second,
		UpdateCheck:      true,
		ExecEnabled:      true,
		ExecMaxSessions:  8,
		ExecIdleTimeout:  30 * time.Minute,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got  %+v\nwant %+v", cfg, want)
	}
}

func TestLoadFileOverridesOnlyKeysItSets(t *testing.T) {
	path := writeConfig(t, "listen: 0.0.0.0:9000\nmetrics_retention: 24h\n")
	cfg, err := config.Load(path, env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != "0.0.0.0:9000" {
		t.Errorf("Listen = %q, want 0.0.0.0:9000", cfg.Listen)
	}
	if cfg.MetricsRetention != 24*time.Hour {
		t.Errorf("MetricsRetention = %s, want 24h", cfg.MetricsRetention)
	}
	if cfg.StacksDir != "/opt/stacks" {
		t.Errorf("StacksDir = %q, want default /opt/stacks", cfg.StacksDir)
	}
}

func TestLoadEmptyFileUsesDefaults(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, ""), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != "127.0.0.1:8420" {
		t.Errorf("Listen = %q, want default", cfg.Listen)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	path := writeConfig(t, "listen: 0.0.0.0:9000\n")
	cfg, err := config.Load(path, env(map[string]string{
		"STACKSTEAD_LISTEN":            "127.0.0.1:7000",
		"STACKSTEAD_OPERATION_TIMEOUT": "30m",
		"STACKSTEAD_ALLOWED_ROOTS":     "/srv/a, /srv/b",
		"STACKSTEAD_DOCKER_SOCKET":     "/run/user/1000/docker.sock",
		"STACKSTEAD_SHUTDOWN_GRACE":    "5s",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != "127.0.0.1:7000" {
		t.Errorf("Listen = %q, want env value", cfg.Listen)
	}
	if cfg.OperationTimeout != 30*time.Minute {
		t.Errorf("OperationTimeout = %s, want 30m", cfg.OperationTimeout)
	}
	if want := []string{"/srv/a", "/srv/b"}; !reflect.DeepEqual(cfg.AllowedRoots, want) {
		t.Errorf("AllowedRoots = %v, want %v", cfg.AllowedRoots, want)
	}
	if cfg.DockerSocket != "/run/user/1000/docker.sock" {
		t.Errorf("DockerSocket = %q, want env value", cfg.DockerSocket)
	}
	if cfg.ShutdownGrace != 5*time.Second {
		t.Errorf("ShutdownGrace = %s, want 5s", cfg.ShutdownGrace)
	}
}

func TestAllowedRootsDefaultFollowsStacksDir(t *testing.T) {
	cfg, err := config.Load("", env(map[string]string{"STACKSTEAD_STACKS_DIR": "/srv/stacks"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := []string{"/srv/stacks"}; !reflect.DeepEqual(cfg.AllowedRoots, want) {
		t.Errorf("AllowedRoots = %v, want %v", cfg.AllowedRoots, want)
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		env     map[string]string
		wantErr string
	}{
		{name: "unknown key", file: "lisen: x\n", wantErr: "lisen"},
		{name: "empty listen", file: "listen: \"\"\n", wantErr: "listen"},
		{name: "relative stacks dir", env: map[string]string{"STACKSTEAD_STACKS_DIR": "stacks"}, wantErr: "stacks_dir"},
		{name: "relative allowed root", file: "allowed_roots: [rel]\n", wantErr: "allowed_roots"},
		{name: "zero interval", env: map[string]string{"STACKSTEAD_METRICS_INTERVAL": "0s"}, wantErr: "metrics_interval"},
		{name: "relative docker socket", file: "docker_socket: docker.sock\n", wantErr: "docker_socket"},
		{name: "zero shutdown grace", file: "shutdown_grace: 0s\n", wantErr: "shutdown_grace"},
		{name: "bare integer duration", file: "operation_timeout: 900\n", wantErr: "operation_timeout"},
		{name: "sub-second duration", env: map[string]string{"STACKSTEAD_OPERATION_TIMEOUT": "900ns"}, wantErr: "operation_timeout must be at least 1s"},
		{name: "unparseable duration", env: map[string]string{"STACKSTEAD_METRICS_RETENTION": "three days"}, wantErr: "STACKSTEAD_METRICS_RETENTION"},
		{name: "negative exec idle timeout", env: map[string]string{"STACKSTEAD_EXEC_IDLE_TIMEOUT": "-1s"}, wantErr: "exec_idle_timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := ""
			if tt.file != "" {
				path = writeConfig(t, tt.file)
			}
			_, err := config.Load(path, env(tt.env))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want error mentioning %q", err, tt.wantErr)
			}
		})
	}
}

func TestExecDefaults(t *testing.T) {
	defaults := config.Defaults()
	if !defaults.ExecEnabled {
		t.Errorf("ExecEnabled = false, want true")
	}
	if defaults.ExecMaxSessions != 8 {
		t.Errorf("ExecMaxSessions = %d, want 8", defaults.ExecMaxSessions)
	}
	if defaults.ExecIdleTimeout != 30*time.Minute {
		t.Errorf("ExecIdleTimeout = %s, want 30m", defaults.ExecIdleTimeout)
	}
}

func TestExecFromFileAndEnv(t *testing.T) {
	path := writeConfig(t, "exec_enabled: false\nexec_idle_timeout: 0s\n")
	cfg, err := config.Load(path, env(map[string]string{"STACKSTEAD_EXEC_MAX_SESSIONS": "3"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ExecEnabled {
		t.Errorf("ExecEnabled = true, want false from file")
	}
	if cfg.ExecIdleTimeout != 0 {
		t.Errorf("ExecIdleTimeout = %s, want 0s from file", cfg.ExecIdleTimeout)
	}
	if cfg.ExecMaxSessions != 3 {
		t.Errorf("ExecMaxSessions = %d, want 3 from env", cfg.ExecMaxSessions)
	}
}

func TestExecMaxSessionsInvalid(t *testing.T) {
	_, err := config.Load(writeConfig(t, "exec_max_sessions: 0\n"), env(nil))
	if err == nil || !strings.Contains(err.Error(), "exec_max_sessions") {
		t.Errorf("err = %v, want error mentioning exec_max_sessions", err)
	}
}

func TestExecIdleTimeoutNeedsUnit(t *testing.T) {
	_, err := config.Load(writeConfig(t, "exec_idle_timeout: 30\n"), env(nil))
	if err == nil || !strings.Contains(err.Error(), "exec_idle_timeout") {
		t.Errorf("err = %v, want error mentioning exec_idle_timeout", err)
	}
}

func TestLoadMissingFileFails(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "missing.yaml"), env(nil))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestUpdateCheckDefaultsOn(t *testing.T) {
	cfg, err := config.Load("", env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.UpdateCheck {
		t.Errorf("UpdateCheck = false, want true by default")
	}
}

func TestUpdateCheckFromYAML(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, "update_check: false\n"), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UpdateCheck {
		t.Errorf("UpdateCheck = true, want false from file")
	}
}

func TestUpdateCheckFromEnv(t *testing.T) {
	for _, value := range []string{"0", "false", "TRUE"} {
		t.Run(value, func(t *testing.T) {
			cfg, err := config.Load("", env(map[string]string{"STACKSTEAD_UPDATE_CHECK": value}))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			want := value == "TRUE"
			if cfg.UpdateCheck != want {
				t.Errorf("UpdateCheck = %v, want %v", cfg.UpdateCheck, want)
			}
		})
	}
}

func TestUpdateCheckEnvRejectsOtherValues(t *testing.T) {
	_, err := config.Load("", env(map[string]string{"STACKSTEAD_UPDATE_CHECK": "maybe"}))
	if err == nil || !strings.Contains(err.Error(), "STACKSTEAD_UPDATE_CHECK") {
		t.Errorf("err = %v, want error naming STACKSTEAD_UPDATE_CHECK", err)
	}
}
