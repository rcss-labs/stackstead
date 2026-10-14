package cli

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeEngine answers the Docker Engine ping on a unix socket (short path:
// macOS limits socket paths to about 100 bytes).
func fakeEngine(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hlt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.45")
		w.Write([]byte("OK"))
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return sock
}

// fakeDockerOnPath puts a `docker` that answers `compose version` on PATH.
func fakeDockerOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho 2.29.1\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func healthHost(t *testing.T, listen, socket string) *localHost {
	h := newLocalHost(t, true)
	cfg := "listen: " + listen + "\nstacks_dir: " + h.dir + "/stacks\ndata_dir: " + h.dataDir + "\ndocker_socket: " + socket + "\n"
	if err := os.WriteFile(h.cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return h
}

type healthReportJSON struct {
	OK     bool
	Checks []struct {
		Name   string
		OK     bool
		Detail string
	}
}

func parseHealth(t *testing.T, out string) healthReportJSON {
	t.Helper()
	var r healthReportJSON
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	return r
}

func TestHealthAllOK(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer api.Close()
	fakeDockerOnPath(t)
	h := healthHost(t, strings.TrimPrefix(api.URL, "http://"), fakeEngine(t))

	env := newTestEnv(t, nil)
	wantCode(t, runLocal(env, "health", "--config", h.cfgPath), ExitOK)
	for _, name := range []string{"config", "database", "server", "docker", "compose"} {
		mustContain(t, "table", env.Out.String(), name)
	}
	if strings.Contains(env.Out.String(), "fail") {
		t.Errorf("unexpected failure:\n%s", env.Out.String())
	}
}

func TestHealthServerDownExits7(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close() // nothing listens here any more
	fakeDockerOnPath(t)
	h := healthHost(t, addr, fakeEngine(t))

	env := newTestEnv(t, nil)
	wantCode(t, runLocal(env, "health", "--config", h.cfgPath, "-o", "json"), ExitUnavailable)
	r := parseHealth(t, env.Out.String())
	if r.OK {
		t.Error("ok = true with the server down")
	}
	for _, c := range r.Checks {
		if c.Name == "server" && c.OK {
			t.Error("server check passed")
		}
		if c.Name != "server" && !c.OK {
			t.Errorf("check %s failed: %s", c.Name, c.Detail)
		}
	}
}

func TestHealthDatabaseProblemExits1(t *testing.T) {
	h := newLocalHost(t, false) // no database
	fakeDockerOnPath(t)
	env := newTestEnv(t, nil)
	wantCode(t, runLocal(env, "health", "--config", h.cfgPath, "-o", "json"), ExitGeneral)
	r := parseHealth(t, env.Out.String())
	for _, c := range r.Checks {
		if c.Name == "database" && c.OK {
			t.Error("database check passed without a database")
		}
	}
}

func TestHealthBadConfigExits1(t *testing.T) {
	h := newLocalHost(t, false)
	os.WriteFile(h.cfgPath, []byte("data_dir: relative\n"), 0o600)
	env := newTestEnv(t, nil)
	wantCode(t, runLocal(env, "health", "--config", h.cfgPath, "-o", "json"), ExitGeneral)
	r := parseHealth(t, env.Out.String())
	if len(r.Checks) != 5 || r.Checks[0].Name != "config" || r.Checks[0].OK {
		t.Errorf("unexpected checks: %+v", r.Checks)
	}
}

func TestHealthAsRootOverServiceDBSkipsDatabase(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) }))
	defer api.Close()
	fakeDockerOnPath(t)
	h := healthHost(t, strings.TrimPrefix(api.URL, "http://"), fakeEngine(t))
	geteuid = func() int { return 0 }
	fileOwner = func(string) (int, error) { return 1001, nil }
	t.Cleanup(func() { fileOwner = realFileOwner })

	env := newTestEnv(t, nil)
	wantCode(t, runLocal(env, "health", "--config", h.cfgPath, "-o", "json"), ExitOK)
	r := parseHealth(t, env.Out.String())
	for _, c := range r.Checks {
		if c.Name == "database" && (!c.OK || !strings.Contains(c.Detail, "skipped")) {
			t.Errorf("database check = %+v, want ok with skipped detail", c)
		}
	}
}

func TestHealthJSONShape(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) }))
	defer api.Close()
	fakeDockerOnPath(t)
	h := healthHost(t, strings.TrimPrefix(api.URL, "http://"), fakeEngine(t))
	env := newTestEnv(t, nil)
	wantCode(t, runLocal(env, "health", "--config", h.cfgPath, "-o", "json"), ExitOK)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(env.Out.String()), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 || raw["ok"] == nil || raw["checks"] == nil {
		t.Errorf("keys = %v, want ok and checks", raw)
	}
	r := parseHealth(t, env.Out.String())
	var names []string
	for _, c := range r.Checks {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "config,database,server,docker,compose" || !r.OK {
		t.Errorf("report = %+v", r)
	}
}
