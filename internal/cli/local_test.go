package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/store"
)

// localHost is a throwaway data dir plus a config file pointing at it.
type localHost struct {
	dir, cfgPath, dataDir, dbPath string
}

func newLocalHost(t *testing.T, withDB bool) *localHost {
	t.Helper()
	dir := t.TempDir()
	h := &localHost{
		dir:     dir,
		cfgPath: filepath.Join(dir, "config.yaml"),
		dataDir: filepath.Join(dir, "data"),
	}
	h.dbPath = filepath.Join(h.dataDir, "stackstead.db")
	cfg := fmt.Sprintf("listen: 127.0.0.1:1\nstacks_dir: %s/stacks\ndata_dir: %s\ndocker_socket: %s/none.sock\n", dir, h.dataDir, dir)
	if err := os.WriteFile(h.cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if withDB {
		db, err := store.Open(h.dbPath)
		if err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	// Never read the host's real config and never act as root by accident.
	origPath, origEuid := defaultConfigPath, geteuid
	defaultConfigPath = filepath.Join(dir, "no-default.yaml")
	geteuid = func() int { return 1000 }
	t.Cleanup(func() { defaultConfigPath, geteuid = origPath, origEuid })
	return h
}

// runLocal runs a host command through a root holding only the local groups.
func runLocal(env *testEnv, args ...string) error {
	root := &Command{Name: "stackstead", Sub: []*Command{configCommand(), dbCommand(), adminCommand(), healthCommand()}}
	return Run(context.Background(), root, args, env.Env)
}

func mustContain(t *testing.T, what, got string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(got, p) {
			t.Errorf("%s %q does not contain %q", what, got, p)
		}
	}
}

func errMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func wantCode(t *testing.T, err error, code int) {
	t.Helper()
	if got := codeFor(err); got != code {
		t.Fatalf("exit code = %d (%v), want %d", got, err, code)
	}
}
