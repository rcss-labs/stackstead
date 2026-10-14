package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"go.yaml.in/yaml/v3"
)

//go:embed template.yaml
var templateText string

var configTemplate = template.Must(template.New("config").Parse(templateText))

// Template renders the commented config file with cfg's values. Settings that
// are optional in the file (docker_socket, shutdown_grace) stay commented out
// while they hold the default and are written out once they differ, so
// rendering never silently drops a customised value.
func Template(cfg Config) ([]byte, error) {
	def := Defaults()
	optional := func(key, value, defaultValue string) string {
		if value == defaultValue {
			return "# " + key + ": " + value
		}
		return key + ": " + value
	}
	allowedRoots := `# allowed_roots:
#   - /opt/stacks
#   - /home/me/projects`
	if len(cfg.AllowedRoots) > 0 && !(len(cfg.AllowedRoots) == 1 && cfg.AllowedRoots[0] == cfg.StacksDir) {
		lines := []string{"allowed_roots:"}
		for _, root := range cfg.AllowedRoots {
			lines = append(lines, "  - "+yamlScalar(root))
		}
		allowedRoots = strings.Join(lines, "\n")
	}
	view := map[string]string{
		"AllowedRootsBlock": allowedRoots,
		"Listen":            yamlScalar(cfg.Listen),
		"StacksDir":         yamlScalar(cfg.StacksDir),
		"DataDir":           yamlScalar(cfg.DataDir),
		"MetricsRetention":  formatDuration(cfg.MetricsRetention),
		"MetricsInterval":   formatDuration(cfg.MetricsInterval),
		"OperationTimeout":  formatDuration(cfg.OperationTimeout),
		"DockerSocketLine":  optional("docker_socket", yamlScalar(cfg.DockerSocket), yamlScalar(def.DockerSocket)),
		"ShutdownGraceLine": optional("shutdown_grace", formatDuration(cfg.ShutdownGrace), formatDuration(def.ShutdownGrace)),
	}
	var buf bytes.Buffer
	if err := configTemplate.Execute(&buf, view); err != nil {
		return nil, fmt.Errorf("render config template: %w", err)
	}
	return buf.Bytes(), nil
}

var plainScalar = regexp.MustCompile(`^[A-Za-z0-9_./:+][A-Za-z0-9_./:@+-]*$`)

// yamlScalar leaves ordinary paths and addresses bare and double-quotes
// anything else (a double-quoted Go string is a valid YAML scalar).
func yamlScalar(s string) string {
	if plainScalar.MatchString(s) {
		return s
	}
	return strconv.Quote(s)
}

// formatDuration prints 72h0m0s as 72h, 15m0s as 15m and 1h30m0s as 1h30m.
func formatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// Setting is one effective configuration value and where it came from.
type Setting struct {
	Key, Value string
	Source     string // "default", "file" or "env"
}

// Effective reports every setting of cfg (as returned by Load for the same
// path and getenv) in the order of the template, with its source: an env var
// beats a key present in the file, which beats the built-in default.
func Effective(cfg Config, path string, getenv func(string) string) ([]Setting, error) {
	inFile := map[string]bool{}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		var raw map[string]yaml.Node
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
		for key, node := range raw {
			// `key:` with no value (null) leaves the default in place.
			inFile[key] = !(node.Kind == yaml.ScalarNode && node.Tag == "!!null")
		}
	}
	source := func(key string) string {
		if getenv("STACKSTEAD_"+strings.ToUpper(key)) != "" {
			return "env"
		}
		if inFile[key] {
			return "file"
		}
		return "default"
	}
	values := []struct{ key, value string }{
		{"listen", cfg.Listen},
		{"stacks_dir", cfg.StacksDir},
		{"data_dir", cfg.DataDir},
		{"allowed_roots", strings.Join(cfg.AllowedRoots, ", ")},
		{"metrics_retention", formatDuration(cfg.MetricsRetention)},
		{"metrics_interval", formatDuration(cfg.MetricsInterval)},
		{"operation_timeout", formatDuration(cfg.OperationTimeout)},
		{"docker_socket", cfg.DockerSocket},
		{"shutdown_grace", formatDuration(cfg.ShutdownGrace)},
	}
	settings := make([]Setting, len(values))
	for i, v := range values {
		settings[i] = Setting{Key: v.key, Value: v.value, Source: source(v.key)}
	}
	return settings, nil
}
