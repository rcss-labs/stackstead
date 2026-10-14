// Package config loads Stackstead's configuration: built-in defaults, then an
// optional YAML file, then STACKSTEAD_* environment variable overrides.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Listen           string        `yaml:"listen"`
	StacksDir        string        `yaml:"stacks_dir"`
	DataDir          string        `yaml:"data_dir"`
	AllowedRoots     []string      `yaml:"allowed_roots"`
	MetricsRetention time.Duration `yaml:"metrics_retention"`
	MetricsInterval  time.Duration `yaml:"metrics_interval"`
	OperationTimeout time.Duration `yaml:"operation_timeout"`
}

func Defaults() Config {
	return Config{
		Listen:           "127.0.0.1:8420",
		StacksDir:        "/opt/stacks",
		DataDir:          "/var/lib/stackstead",
		MetricsRetention: 72 * time.Hour,
		MetricsInterval:  10 * time.Second,
		OperationTimeout: 15 * time.Minute,
	}
}

// Load builds the configuration. An empty path skips the file; a non-empty
// path that does not exist is an error. AllowedRoots defaults to StacksDir.
func Load(path string, getenv func(string) string) (Config, error) {
	cfg := Defaults()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if err := applyEnv(&cfg, getenv); err != nil {
		return Config{}, err
	}
	if len(cfg.AllowedRoots) == 0 {
		cfg.AllowedRoots = []string{cfg.StacksDir}
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	cfg.StacksDir = filepath.Clean(cfg.StacksDir)
	cfg.DataDir = filepath.Clean(cfg.DataDir)
	for i, root := range cfg.AllowedRoots {
		cfg.AllowedRoots[i] = filepath.Clean(root)
	}
	return cfg, nil
}

func applyEnv(cfg *Config, getenv func(string) string) error {
	for key, dst := range map[string]*string{
		"STACKSTEAD_LISTEN":     &cfg.Listen,
		"STACKSTEAD_STACKS_DIR": &cfg.StacksDir,
		"STACKSTEAD_DATA_DIR":   &cfg.DataDir,
	} {
		if v := getenv(key); v != "" {
			*dst = v
		}
	}
	for key, dst := range map[string]*time.Duration{
		"STACKSTEAD_METRICS_RETENTION": &cfg.MetricsRetention,
		"STACKSTEAD_METRICS_INTERVAL":  &cfg.MetricsInterval,
		"STACKSTEAD_OPERATION_TIMEOUT": &cfg.OperationTimeout,
	} {
		if v := getenv(key); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			*dst = d
		}
	}
	if v := getenv("STACKSTEAD_ALLOWED_ROOTS"); v != "" {
		cfg.AllowedRoots = nil
		for _, root := range strings.Split(v, ",") {
			if root = strings.TrimSpace(root); root != "" {
				cfg.AllowedRoots = append(cfg.AllowedRoots, root)
			}
		}
	}
	return nil
}

func (c Config) validate() error {
	if c.Listen == "" {
		return errors.New("listen must not be empty")
	}
	for name, p := range map[string]string{"stacks_dir": c.StacksDir, "data_dir": c.DataDir} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("%s must be an absolute path, got %q", name, p)
		}
	}
	for _, root := range c.AllowedRoots {
		if !filepath.IsAbs(root) {
			return fmt.Errorf("allowed_roots entries must be absolute paths, got %q", root)
		}
	}
	for name, d := range map[string]time.Duration{
		"metrics_retention": c.MetricsRetention,
		"metrics_interval":  c.MetricsInterval,
		"operation_timeout": c.OperationTimeout,
	} {
		if d <= 0 {
			return fmt.Errorf("%s must be positive, got %s", name, d)
		}
	}
	return nil
}
