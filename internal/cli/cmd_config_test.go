package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigInitWritesTemplateAndRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "etc", "stackstead", "config.yaml")
	env := newTestEnv(t, nil)
	err := runLocal(env, "config", "init", "--path", path, "--listen", "0.0.0.0:9000",
		"--stacks-dir", dir+"/s", "--data-dir", dir+"/d")
	wantCode(t, err, ExitOK)
	mustContain(t, "stdout", env.Out.String(), path)
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	mustContain(t, "config", string(data), "0.0.0.0:9000", dir+"/s", dir+"/d", "#")
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
	if _, err := os.Stat(dir + "/d"); err == nil {
		t.Error("config init created data_dir")
	}

	err = runLocal(newTestEnv(t, nil), "config", "init", "--path", path)
	wantCode(t, err, ExitConflict)
	if after, _ := os.ReadFile(path); string(after) != string(data) {
		t.Error("existing file was modified without --force")
	}
	err = runLocal(newTestEnv(t, nil), "config", "init", "--path", path, "--force", "--listen", "127.0.0.1:1")
	wantCode(t, err, ExitOK)
	if after, _ := os.ReadFile(path); string(after) == string(data) {
		t.Error("--force did not rewrite the file")
	}
}

func TestConfigInitRejectsRelativeDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	err := runLocal(newTestEnv(t, nil), "config", "init", "--path", path, "--data-dir", "relative/dir")
	wantCode(t, err, ExitValidation)
	if _, serr := os.Stat(path); serr == nil {
		t.Error("an invalid config was written")
	}
}

func TestConfigValidateReportsLoaderError(t *testing.T) {
	h := newLocalHost(t, false)
	env := newTestEnv(t, nil)
	wantCode(t, runLocal(env, "config", "validate", "--config", h.cfgPath), ExitOK)
	mustContain(t, "stdout", env.Out.String(), "is valid")

	if err := os.WriteFile(h.cfgPath, []byte("metrics_interval: 10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runLocal(newTestEnv(t, nil), "config", "validate", "--config", h.cfgPath)
	wantCode(t, err, ExitValidation)
	mustContain(t, "error", errMessage(err), "metrics_interval")
}

func TestConfigValidateWithoutFileUsesDefaults(t *testing.T) {
	newLocalHost(t, false)
	env := newTestEnv(t, nil)
	wantCode(t, runLocal(env, "config", "validate"), ExitOK)
	mustContain(t, "stdout", env.Out.String(), "no config file")
}

func TestConfigShowSources(t *testing.T) {
	h := newLocalHost(t, false)
	env := newTestEnv(t, map[string]string{"STACKSTEAD_SHUTDOWN_GRACE": "45s"})
	wantCode(t, runLocal(env, "config", "show", "--config", h.cfgPath, "-o", "json"), ExitOK)
	var rows []struct{ Key, Value, Source string }
	if err := json.Unmarshal([]byte(env.Out.String()), &rows); err != nil {
		t.Fatalf("json: %v\n%s", err, env.Out.String())
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.Key] = r.Source + "=" + r.Value
	}
	if got["listen"] != "file=127.0.0.1:1" || got["shutdown_grace"] != "env=45s" || got["metrics_interval"] != "default=10s" {
		t.Errorf("unexpected rows: %v", got)
	}

	table := newTestEnv(t, nil)
	wantCode(t, runLocal(table, "config", "show", "--config", h.cfgPath), ExitOK)
	mustContain(t, "table", table.Out.String(), "KEY", "VALUE", "SOURCE", "listen")
}

func TestConfigInitDoesNotReplaceDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), path); err != nil {
		t.Fatal(err)
	}
	err := runLocal(newTestEnv(t, nil), "config", "init", "--path", path)
	wantCode(t, err, ExitConflict)
	if target, err := os.Readlink(path); err != nil || target != filepath.Join(dir, "elsewhere") {
		t.Errorf("the symlink was replaced (%q, %v)", target, err)
	}
}

func TestLoadConfigMapsIOErrorsAwayFromValidation(t *testing.T) {
	dir := t.TempDir()
	err := runLocal(newTestEnv(t, nil), "config", "validate", "--config", dir) // a directory
	wantCode(t, err, ExitGeneral)

	if os.Geteuid() != 0 {
		unreadable := filepath.Join(dir, "secret.yaml")
		os.WriteFile(unreadable, []byte("listen: 127.0.0.1:1\n"), 0o000)
		wantCode(t, runLocal(newTestEnv(t, nil), "config", "validate", "--config", unreadable), ExitAuth)
	}
	// Parse and value problems stay validation errors.
	bad := filepath.Join(dir, "bad.yaml")
	os.WriteFile(bad, []byte("listen: [\n"), 0o600)
	wantCode(t, runLocal(newTestEnv(t, nil), "config", "validate", "--config", bad), ExitValidation)
}
