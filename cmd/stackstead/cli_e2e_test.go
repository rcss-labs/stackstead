//go:build e2e

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// cliRig drives the real binary the way a user and the Plan 7 installers do:
// separate processes, a private HOME/XDG directory, and every byte it prints
// kept for the final secret scan.
type cliRig struct {
	t       *testing.T
	bin     string
	dir     string
	cfg     string
	base    string
	xdg     string
	socket  string
	outputs []string // every stdout and stderr seen, scanned for secrets at the end
}

// buildCLI compiles the binary with a known version stamp.
func buildCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "stackstead")
	build := exec.Command("go", "build",
		"-ldflags", "-X github.com/rcss-labs/stackstead/internal/version.Version=e2e-test",
		"-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func newCLIRig(t *testing.T, socket string) *cliRig {
	t.Helper()
	dir := t.TempDir()
	r := &cliRig{t: t, bin: buildCLI(t), dir: dir, xdg: filepath.Join(dir, "xdg"), cfg: filepath.Join(dir, "config.yaml")}
	if socket == "" {
		socket = filepath.Join(dir, "none.sock")
	}
	r.socket = socket
	port := freePort(t)
	r.base = fmt.Sprintf("http://127.0.0.1:%d", port)
	return r
}

func (r *cliRig) env() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		// HOME stays real: the docker CLI finds its compose plugin there.
		"HOME=" + os.Getenv("HOME"),
		"XDG_CONFIG_HOME=" + r.xdg,
		"NO_COLOR=1",
		"STACKSTEAD_DOCKER_SOCKET=" + r.socket,
		"STACKSTEAD_METRICS_INTERVAL=2s",
	}
}

// run executes the binary and returns its exit code and output.
func (r *cliRig) run(stdin string, args ...string) (code int, stdout, stderr string) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Env = r.env()
	cmd.Stdin = strings.NewReader(stdin)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		code = ee.ExitCode()
	default:
		r.t.Fatalf("run %v: %v", args, err)
	}
	r.outputs = append(r.outputs, so.String(), se.String())
	return code, so.String(), se.String()
}

// want runs the command, requires the exit code, and returns stdout.
func (r *cliRig) want(code int, stdin string, args ...string) string {
	r.t.Helper()
	got, out, errOut := r.run(stdin, args...)
	if got != code {
		r.t.Fatalf("stackstead %s: exit %d, want %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), got, code, out, redact(errOut))
	}
	return out
}

func (r *cliRig) jsonOf(code int, stdin string, v any, args ...string) {
	r.t.Helper()
	out := r.want(code, stdin, args...)
	if err := json.Unmarshal([]byte(out), v); err != nil {
		r.t.Fatalf("stackstead %s: output is not JSON: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func (r *cliRig) contextsFile() string { return filepath.Join(r.xdg, "stackstead", "contexts.json") }

// session reads the stored session cookie value of a context.
func (r *cliRig) session(name string) string {
	r.t.Helper()
	raw, err := os.ReadFile(r.contextsFile())
	if err != nil {
		r.t.Fatal(err)
	}
	var f struct {
		Contexts map[string]struct{ Session string }
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		r.t.Fatal(err)
	}
	return f.Contexts[name].Session
}

type startedServer struct {
	cmd    *exec.Cmd
	done   chan struct{}
	stderr *syncBuffer
}

func (r *cliRig) startServer() *startedServer {
	r.t.Helper()
	cmd := exec.Command(r.bin, "serve", "--config", r.cfg)
	cmd.Env = r.env()
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	s := &startedServer{cmd: cmd, done: make(chan struct{}), stderr: stderr}
	go func() { _ = cmd.Wait(); close(s.done) }()
	r.t.Cleanup(func() {
		select {
		case <-s.done:
			return
		default:
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-s.done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-s.done
		}
	})
	deadline := time.Now().Add(20 * time.Second)
	for {
		if resp, err := http.Get(r.base + "/api/health"); err == nil {
			var body struct{ OK bool }
			_ = json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if resp.StatusCode == 200 && body.OK {
				return s
			}
		}
		select {
		case <-s.done:
			r.t.Fatalf("serve exited early:\n%s", redact(stderr.String()))
		default:
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("serve did not become healthy:\n%s", redact(stderr.String()))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// stop sends SIGTERM and waits for a clean exit.
func (s *startedServer) stop(t *testing.T) {
	t.Helper()
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(45 * time.Second):
		t.Fatal("serve did not stop within 45s of SIGTERM")
	}
	if code := s.cmd.ProcessState.ExitCode(); code != 0 {
		t.Errorf("serve exit code after SIGTERM = %d, want 0\n%s", code, redact(s.stderr.String()))
	}
}

func (r *cliRig) metricsStatus(token string) int {
	r.t.Helper()
	req, _ := http.NewRequest("GET", r.base+"/metrics", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// waitMetrics polls /metrics until it answers want (the server revalidates
// the token every 5 s).
func (r *cliRig) waitMetrics(token string, want int) {
	r.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		got := r.metricsStatus(token)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("/metrics answered %d, want %d within 8s", got, want)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// initHost runs the host-side steps that precede the first serve.
func (r *cliRig) initHost() {
	r.t.Helper()
	r.want(0, "", "config", "init", "--path", r.cfg,
		"--listen", strings.TrimPrefix(r.base, "http://"),
		"--stacks-dir", filepath.Join(r.dir, "stacks"), "--data-dir", filepath.Join(r.dir, "data"))
	r.want(0, "", "db", "init", "--config", r.cfg)
}

// sub runs a subtest whose failures (including those of r's helpers) belong to it.
func (r *cliRig) sub(t *testing.T, name string, fn func(t *testing.T)) {
	t.Run(name, func(t *testing.T) {
		parent := r.t
		r.t = t
		defer func() { r.t = parent }()
		fn(t)
	})
}

func TestE2ECLI(t *testing.T) {
	r := newCLIRig(t, "")
	password := rand.Text() + rand.Text()
	newPassword := rand.Text() + rand.Text()
	var sessions []string
	var tokens []string

	r.sub(t, "version", func(t *testing.T) {
		out := r.want(0, "", "version")
		if !strings.Contains(out, "e2e-test") || !strings.Contains(out, "License: AGPL-3.0") {
			t.Errorf("version output:\n%s", out)
		}
		if got := r.want(0, "", "version", "--short"); got != "e2e-test\n" {
			t.Errorf("version --short = %q", got)
		}
		var v map[string]any
		r.jsonOf(0, "", &v, "version", "-o", "json")
		for _, k := range []string{"version", "commit", "date", "go", "os", "arch", "license"} {
			if _, ok := v[k]; !ok {
				t.Errorf("version -o json lacks %q", k)
			}
		}
		if v["version"] != "e2e-test" {
			t.Errorf("version = %v", v["version"])
		}
	})

	r.sub(t, "config", func(t *testing.T) {
		r.want(0, "", "config", "init", "--path", r.cfg,
			"--listen", strings.TrimPrefix(r.base, "http://"),
			"--stacks-dir", filepath.Join(r.dir, "stacks"), "--data-dir", filepath.Join(r.dir, "data"))
		r.want(5, "", "config", "init", "--path", r.cfg)
		r.want(0, "", "config", "validate", "--config", r.cfg)
		out := r.want(0, "", "config", "show", "--config", r.cfg, "-o", "json")
		var raw any
		if err := json.Unmarshal([]byte(out), &raw); err != nil {
			t.Fatalf("config show -o json: %v\n%s", err, out)
		}
		if !strings.Contains(out, `"file"`) || !strings.Contains(out, "listen") {
			t.Errorf("config show -o json does not report the listen key from the file:\n%s", out)
		}
		bad := filepath.Join(r.dir, "bad.yaml")
		if err := os.WriteFile(bad, []byte("stacks_dir: relative/path\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r.want(6, "", "config", "validate", "--config", bad)
	})

	r.sub(t, "host down and no database", func(t *testing.T) {
		var h struct {
			OK     bool
			Checks []struct {
				Name string
				OK   bool
			}
		}
		// No database yet is a database problem (exit 1, per the Plan 7 contract),
		// which takes precedence over the unreachable server (exit 7).
		r.jsonOf(1, "", &h, "health", "--config", r.cfg, "-o", "json")
		if h.OK {
			t.Error("health ok with no server")
		}
		for _, c := range h.Checks {
			if c.Name == "server" && c.OK {
				t.Error("server check passed with no server")
			}
		}
		r.want(4, "", "db", "check", "--config", r.cfg)
	})

	r.sub(t, "db init and offline token", func(t *testing.T) {
		r.want(0, "", "db", "init", "--config", r.cfg)
		var tok struct{ Token string }
		r.jsonOf(0, "", &tok, "admin", "metrics-token", "rotate", "--config", r.cfg, "-o", "json")
		if tok.Token == "" {
			t.Fatal("no token")
		}
		tokens = append(tokens, tok.Token)
	})
	t1 := tokens[0]

	srv := r.startServer()
	r.sub(t, "second serve exits 5", func(t *testing.T) {
		start := time.Now()
		r.want(5, "", "serve", "--config", r.cfg)
		if time.Since(start) > 5*time.Second {
			t.Error("the second serve took over 5s to give up")
		}
	})

	r.sub(t, "online session", func(t *testing.T) {
		out := r.want(0, password+"\n", "admin", "set-password", "--config", r.cfg, "--username", "admin", "--password-stdin")
		if !strings.Contains(out, "created") {
			t.Errorf("set-password output %q", out)
		}
		r.want(0, password+"\n", "login", r.base, "--context", "e2e", "--password-stdin")
		sessions = append(sessions, r.session("e2e"))

		if got := strings.TrimSpace(r.want(0, "", "whoami")); !strings.Contains(got, "admin") {
			t.Errorf("whoami = %q", got)
		}
		var st struct{ Docker, Version string }
		r.jsonOf(0, "", &st, "status", "-o", "json")
		if st.Docker != "unavailable" || st.Version != "e2e-test" {
			t.Errorf("status = %+v, want docker unavailable and version e2e-test", st)
		}
		var list []any
		r.jsonOf(0, "", &list, "stacks", "ls", "-o", "json")
		if len(list) != 0 {
			t.Errorf("stacks ls = %v", list)
		}
		var ops []any
		r.jsonOf(0, "", &ops, "ops", "ls", "-o", "json")
		if len(ops) != 0 {
			t.Errorf("ops ls = %v", ops)
		}
	})

	r.sub(t, "metrics token from every side", func(t *testing.T) {
		var tok struct{ Token string }
		r.jsonOf(0, "", &tok, "settings", "metrics-token", "rotate", "-o", "json")
		t2 := tok.Token
		tokens = append(tokens, t2)
		if t2 == "" || t2 == t1 {
			t.Fatalf("settings rotate returned %q", t2)
		}
		if got := r.metricsStatus(t2); got != 200 {
			t.Errorf("/metrics with the new token = %d", got)
		}
		if got := r.metricsStatus(t1); got != 401 {
			t.Errorf("/metrics with the old token = %d, want 401", got)
		}
		r.jsonOf(0, "", &tok, "admin", "metrics-token", "rotate", "--config", r.cfg, "-o", "json")
		t3 := tok.Token
		tokens = append(tokens, t3)
		r.waitMetrics(t3, 200)
		r.want(0, "", "admin", "metrics-token", "clear", "--config", r.cfg)
		r.waitMetrics("", 404)
	})

	r.sub(t, "password change and relogin", func(t *testing.T) {
		r.want(0, password+"\n"+newPassword+"\n", "settings", "password", "--password-stdin")
		r.want(0, "", "whoami") // this session survives; only the others are signed out
		r.want(3, password+"\n", "login", r.base, "--context", "e2e", "--password-stdin")
		r.want(0, newPassword+"\n", "login", r.base, "--context", "e2e", "--password-stdin")
		sessions = append(sessions, r.session("e2e"))
	})

	backup := filepath.Join(r.dir, "b.db")
	r.sub(t, "backup and refused restore", func(t *testing.T) {
		r.want(0, "", "db", "backup", backup, "--config", r.cfg)
		r.want(0, "", "db", "check", "--config", r.cfg)
		// Check the backup file itself: a data dir of its own holding it as stackstead.db.
		bk := filepath.Join(r.dir, "bk")
		if err := os.MkdirAll(bk, 0o700); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(backup)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bk, "stackstead.db"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		bkCfg := filepath.Join(r.dir, "bk.yaml")
		r.want(0, "", "config", "init", "--path", bkCfg, "--data-dir", bk)
		r.want(0, "", "db", "check", "--config", bkCfg)
		r.want(5, "", "db", "restore", backup, "--yes", "--config", r.cfg)
	})

	r.sub(t, "errors map to exit codes", func(t *testing.T) {
		r.want(6, "short\n", "admin", "set-password", "--config", r.cfg, "--password-stdin")
		r.want(4, "", "logs", "nope")
		r.want(4, "", "stacks", "up", "nope")
		var h struct {
			OK     bool
			Checks []struct {
				Name string
				OK   bool
			}
		}
		r.jsonOf(7, "", &h, "health", "--config", r.cfg, "-o", "json")
		ok := map[string]bool{}
		for _, c := range h.Checks {
			ok[c.Name] = c.OK
		}
		if !ok["server"] || !ok["database"] || ok["docker"] {
			t.Errorf("health checks = %v, want server and database ok, docker failing", ok)
		}
	})

	r.sub(t, "logout", func(t *testing.T) {
		r.want(0, "", "logout")
		r.want(3, "", "whoami")
	})

	r.sub(t, "restore after the server stopped", func(t *testing.T) {
		srv.stop(t)
		r.want(0, "", "db", "restore", backup, "--yes", "--config", r.cfg)
		data := filepath.Join(r.dir, "data")
		if baks, _ := filepath.Glob(filepath.Join(data, "stackstead.db.bak-*")); len(baks) != 1 {
			t.Errorf("want one stackstead.db.bak-* file, got %v", baks)
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if _, err := os.Stat(filepath.Join(data, "stackstead.db"+suffix)); err == nil {
				t.Errorf("stale stackstead.db%s after restore", suffix)
			}
		}
		var h struct {
			Checks []struct {
				Name string
				OK   bool
			}
		}
		r.jsonOf(7, "", &h, "health", "--config", r.cfg, "-o", "json")
		ok := map[string]bool{}
		for _, c := range h.Checks {
			ok[c.Name] = c.OK
		}
		if ok["server"] || !ok["database"] {
			t.Errorf("health checks = %v, want server failing and database ok", ok)
		}
	})

	r.sub(t, "no secret leaks", func(t *testing.T) {
		all := strings.Join(r.outputs, "\n") + "\n" + srv.stderr.String()
		secrets := map[string]string{"password": password, "new password": newPassword}
		for i, s := range sessions {
			secrets[fmt.Sprintf("session %d", i+1)] = s
		}
		for i, tok := range tokens {
			// Each rotate prints its token once on stdout; only the server log must stay clean.
			if tok == "" {
				t.Errorf("token %d was never captured", i+1)
			}
		}
		for name, secret := range secrets {
			if secret == "" {
				t.Errorf("%s was never captured; the scan would prove nothing", name)
			} else if strings.Contains(all, secret) {
				t.Errorf("the %s appears in command or server output", name)
			}
		}
		for i, tok := range tokens {
			if strings.Contains(srv.stderr.String(), tok) {
				t.Errorf("server log contains metrics token %d", i+1)
			}
		}
		// Everything the CLI left in its config directory: no password or token
		// anywhere, and no session outside contexts.json (it was logged out of).
		err := filepath.WalkDir(r.xdg, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			for name, secret := range secrets {
				if strings.HasPrefix(name, "session") && path == r.contextsFile() {
					continue
				}
				if strings.Contains(string(raw), secret) {
					t.Errorf("%s contains the %s", path, name)
				}
			}
			for i, tok := range tokens {
				if strings.Contains(string(raw), tok) {
					t.Errorf("%s contains metrics token %d", path, i+1)
				}
			}
			return nil
		})
		if err != nil {
			t.Error(err)
		}
		if fi, err := os.Stat(r.contextsFile()); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("contexts file: %v, mode %v, want 0600", err, fi)
		}
	})
}

// TestE2ECLIDocker drives a throwaway stack through the CLI. It skips without
// Docker; the stack name is unique so nothing the user runs is touched, and
// cleanup is registered before anything is created.
func TestE2ECLIDocker(t *testing.T) {
	socket := dockerSocket()
	if socket == "" {
		t.Skip("docker or docker compose unavailable")
	}
	r := newCLIRig(t, socket)
	r.initHost()
	r.startServer()
	password := rand.Text() + rand.Text()
	r.want(0, password+"\n", "admin", "set-password", "--config", r.cfg, "--password-stdin")
	r.want(0, password+"\n", "login", r.base, "--context", "e2e", "--password-stdin")

	var rnd [3]byte
	_, _ = rand.Read(rnd[:])
	name := fmt.Sprintf("e2e-cli-%x", rnd)
	api := newClient(t, r.base)
	t.Cleanup(func() {
		api.do("DELETE", "/api/stacks/"+name+"?volumes=true", nil)
		time.Sleep(3 * time.Second)
		for _, id := range projectContainers(name) {
			_, _ = dockerOutput("rm", "-f", id)
		}
		_, _ = dockerOutput("network", "rm", name+"_default")
	})
	api.expect(204, "POST", "/api/login", map[string]string{"username": "admin", "password": password})
	api.expect(201, "POST", "/api/stacks", map[string]any{
		"name": name, "service": "web",
		"config": map[string]any{"image": "nginx:alpine", "limits": map[string]string{"memory": "64m"}},
	})

	var list []struct{ Name string }
	r.jsonOf(0, "", &list, "stacks", "ls", "-o", "json")
	if len(list) != 1 || list[0].Name != name {
		t.Fatalf("stacks ls = %+v, want only %s", list, name)
	}
	r.want(0, "", "stacks", "up", name) // waits for the operation
	if got := r.want(0, "", "stacks", "show", name); !strings.Contains(got, name) {
		t.Errorf("stacks show lacks the name:\n%s", got)
	}
	r.want(0, "", "logs", name, "--tail", "5")
	r.want(0, "", "metrics", name)
	r.want(0, "", "ops", "ls")
	r.want(0, "", "stacks", "down", name)
	r.want(0, "", "stacks", "rm", name, "--volumes", "--yes")
	if left := projectContainers(name); len(left) > 0 {
		t.Errorf("containers of %s remain after rm: %v", name, left)
	}
}
