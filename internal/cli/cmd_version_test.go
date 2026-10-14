package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/version"
)

func runVersion(t *testing.T, args ...string) (*testEnv, error) {
	t.Helper()
	te := newTestEnv(t, nil)
	root := &Command{Name: "stackstead", Sub: []*Command{versionCommand()}}
	return te, Run(context.Background(), root, append([]string{"version"}, args...), te.Env)
}

func TestVersionHumanOutputHasLicenseLine(t *testing.T) {
	te, err := runVersion(t)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(te.Out.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "stackstead ") || lines[1] != "License: AGPL-3.0" {
		t.Errorf("output = %q", te.Out.String())
	}
}

func TestVersionJSONHasSevenKeys(t *testing.T) {
	te, err := runVersion(t, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(te.Out.String()), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"version", "commit", "date", "go", "os", "arch", "license"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q in %v", k, m)
		}
	}
	if len(m) != 7 || m["license"] != "AGPL-3.0" {
		t.Errorf("keys = %v", m)
	}
}

func TestVersionShortPrintsOnlyTheVersion(t *testing.T) {
	te, err := runVersion(t, "--short")
	if err != nil {
		t.Fatal(err)
	}
	if want := version.Get().Version + "\n"; te.Out.String() != want {
		t.Errorf("output = %q, want %q", te.Out.String(), want)
	}
}

func TestVersionShortWithJSONIsUsageError(t *testing.T) {
	_, err := runVersion(t, "--short", "-o", "json")
	if codeFor(err) != ExitUsage {
		t.Errorf("code = %d (err %v), want %d", codeFor(err), err, ExitUsage)
	}
}
