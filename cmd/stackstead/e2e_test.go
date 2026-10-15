//go:build e2e

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/web"
)

// syncBuffer collects the child's stderr while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// dockerOutput runs one docker CLI command with a deadline so a wedged daemon cannot hang the suite.
func dockerOutput(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).Output()
}

// redact hides the first-run setup token, which the server logs at startup.
var setupTokenRe = regexp.MustCompile(`(setup_token=)\S+`)

func redact(log string) string { return setupTokenRe.ReplaceAllString(log, "${1}[redacted]") }

type e2eServer struct {
	base   string
	stderr *syncBuffer
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// dockerSocket returns the local Docker socket path when `docker compose` is
// usable, or "" when the Docker part of the smoke test should be skipped.
func dockerSocket() string {
	if _, err := dockerOutput("info"); err != nil {
		return ""
	}
	if _, err := dockerOutput("compose", "version"); err != nil {
		return ""
	}
	out, err := dockerOutput("context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	if err != nil {
		return ""
	}
	host := strings.TrimSpace(string(out))
	if !strings.HasPrefix(host, "unix://") {
		return ""
	}
	return strings.TrimPrefix(host, "unix://")
}

func startServer(t *testing.T, socket string) *e2eServer {
	t.Helper()
	bin := t.TempDir() + "/stackstead"
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	port := freePort(t)
	dir := t.TempDir()
	if socket == "" {
		socket = dir + "/nonexistent.sock"
	}
	cmd := exec.Command(bin, "serve")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("STACKSTEAD_LISTEN=127.0.0.1:%d", port),
		"STACKSTEAD_DATA_DIR="+dir+"/data",
		"STACKSTEAD_STACKS_DIR="+dir+"/stacks",
		"STACKSTEAD_DOCKER_SOCKET="+socket,
		"STACKSTEAD_METRICS_INTERVAL=2s",
	)
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := http.Get(base + "/api/setup/status")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not answer: %v\nstderr:\n%s", err, redact(stderr.String()))
		}
		time.Sleep(100 * time.Millisecond)
	}
	return &e2eServer{base: base, stderr: stderr}
}

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func newClient(t *testing.T, base string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: base, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
}

// do sends a JSON request and returns status and body.
func (c *client) do(method, path string, body any, header ...string) (int, []byte) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (c *client) expect(want int, method, path string, body any) []byte {
	c.t.Helper()
	got, out := c.do(method, path, body)
	if got != want {
		c.t.Fatalf("%s %s = %d, want %d (body: %.200s)", method, path, got, want, out)
	}
	return out
}

func randomSecret(t *testing.T) string {
	t.Helper()
	return rand.Text() + rand.Text()
}

func TestE2ESmoke(t *testing.T) {
	if !web.Built() {
		t.Skip("web UI not built (run make ui)")
	}
	socket := dockerSocket()
	srv := startServer(t, socket)
	c := newClient(t, srv.base)
	anon := newClient(t, srv.base)

	t.Run("static", func(t *testing.T) {
		resp, err := http.Get(srv.base + "/")
		if err != nil {
			t.Fatal(err)
		}
		page, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("GET / = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if resp.Header.Get("Content-Security-Policy") == "" {
			t.Error("missing Content-Security-Policy")
		}
		if !strings.Contains(string(page), `<div id="app"`) {
			t.Error("index.html has no #app mount point")
		}
		refs := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`).FindAllStringSubmatch(string(page), -1)
		hasScript := false
		for _, m := range refs {
			r, err := http.Get(srv.base + m[1])
			if err != nil {
				t.Fatal(err)
			}
			r.Body.Close()
			ct := r.Header.Get("Content-Type")
			if r.StatusCode != 200 || !(strings.Contains(ct, "javascript") || strings.Contains(ct, "css") || ct == "font/woff2") {
				t.Errorf("%s = %d %s", m[1], r.StatusCode, ct)
			}
			if !strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
				t.Errorf("%s Cache-Control = %q", m[1], r.Header.Get("Cache-Control"))
			}
			if strings.HasSuffix(m[1], ".js") {
				hasScript = true
			}
		}
		if !hasScript {
			t.Error("index.html references no /assets/*.js")
		}
		if got, _ := anon.do("GET", "/assets/missing.js", nil); got != 404 {
			t.Errorf("/assets/missing.js = %d, want 404", got)
		}
		for _, p := range []string{"/stacks/anything", "/settings"} {
			r, err := http.Get(srv.base + p)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			if r.StatusCode != 200 || !bytes.Equal(b, page) {
				t.Errorf("%s did not serve index.html (%d)", p, r.StatusCode)
			}
		}
	})

	password := randomSecret(t)
	var newPassword, metricsToken string
	t.Run("auth and settings", func(t *testing.T) {
		anon.expect(401, "GET", "/api/me", nil)
		anon.expect(204, "POST", "/api/setup", map[string]string{"username": "admin", "password": password, "setupToken": ""})
		c.expect(204, "POST", "/api/login", map[string]string{"username": "admin", "password": password})
		c.expect(200, "GET", "/api/me", nil)

		var status struct{ Docker string }
		if err := json.Unmarshal(c.expect(200, "GET", "/api/status", nil), &status); err != nil {
			t.Fatal(err)
		}
		wantDocker := "unavailable"
		if socket != "" {
			wantDocker = "ok"
		}
		if status.Docker != wantDocker {
			t.Errorf("status.docker = %q, want %q", status.Docker, wantDocker)
		}

		var ts struct{ Configured bool }
		_ = json.Unmarshal(c.expect(200, "GET", "/api/settings/metrics-token", nil), &ts)
		if ts.Configured {
			t.Error("metrics token configured on a fresh instance")
		}
		anon.expect(404, "GET", "/metrics", nil)
		var created struct{ Token string }
		if err := json.Unmarshal(c.expect(200, "POST", "/api/settings/metrics-token", map[string]string{}), &created); err != nil || created.Token == "" {
			t.Fatalf("create token: %v", err)
		}
		metricsToken = created.Token
		_ = json.Unmarshal(c.expect(200, "GET", "/api/settings/metrics-token", nil), &ts)
		if !ts.Configured {
			t.Error("token status not configured after create")
		}
		if got, _ := anon.do("GET", "/metrics", nil); got != 401 {
			t.Errorf("/metrics without token = %d, want 401", got)
		}
		got, body := anon.do("GET", "/metrics", nil, "Authorization", "Bearer "+created.Token)
		if got != 200 || !strings.Contains(string(body), "stackstead_metrics_degraded") {
			t.Errorf("/metrics with token = %d", got)
		}
	})

	t.Run("password change", func(t *testing.T) {
		other := newClient(t, srv.base)
		other.expect(204, "POST", "/api/login", map[string]string{"username": "admin", "password": password})
		other.expect(200, "GET", "/api/me", nil)

		newPassword = randomSecret(t)
		c.expect(403, "POST", "/api/password", map[string]string{"current": randomSecret(t), "new": newPassword})
		c.expect(204, "POST", "/api/password", map[string]string{"current": password, "new": newPassword})
		c.expect(200, "GET", "/api/me", nil)
		other.expect(401, "GET", "/api/me", nil)
		anon.expect(401, "POST", "/api/login", map[string]string{"username": "admin", "password": password})
		fresh := newClient(t, srv.base)
		fresh.expect(204, "POST", "/api/login", map[string]string{"username": "admin", "password": newPassword})
	})

	if socket != "" {
		t.Run("docker stack", func(t *testing.T) { dockerStack(t, c) })
	} else {
		t.Log("docker unavailable: skipped the stack lifecycle part")
	}

	t.Run("static assets", func(t *testing.T) {
		resp, err := anon.http.Get(srv.base + "/theme.js")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "javascript") ||
			!strings.Contains(resp.Header.Get("Cache-Control"), "no-cache") {
			t.Errorf("/theme.js = %d %q cache %q, want 200, a JS type and no-cache",
				resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control"))
		}

		_, index := anon.do("GET", "/", nil)
		m := regexp.MustCompile(`href="(/assets/[^"]+\.woff2)"`).FindSubmatch(index)
		if m == nil {
			t.Fatalf("no woff2 preload in index.html:\n%.800s", index)
		}
		resp, err = anon.http.Get(srv.base + string(m[1]))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "font/woff2" {
			t.Errorf("%s = %d %q, want 200 font/woff2", m[1], resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	})

	t.Run("chrome render", func(t *testing.T) {
		chrome := findChrome()
		if chrome == "" {
			t.Skip("no Chrome/Chromium found")
		}
		// --bg of each theme (spec 4.1); the pixel at (4,4) is page background on /login.
		for _, tc := range []struct {
			theme string
			want  color.RGBA
			w, h  int
		}{
			{"dark", color.RGBA{0x0f, 0x12, 0x16, 0xff}, 375, 812},
			{"dark", color.RGBA{0x0f, 0x12, 0x16, 0xff}, 1280, 800},
			{"light", color.RGBA{0xf3, 0xf5, 0xf7, 0xff}, 375, 812},
			{"light", color.RGBA{0xf3, 0xf5, 0xf7, 0xff}, 1280, 800},
		} {
			name := fmt.Sprintf("%s-%dx%d", tc.theme, tc.w, tc.h)
			t.Run(name, func(t *testing.T) {
				shot := filepath.Join(t.TempDir(), name+".png")
				size := fmt.Sprintf("--window-size=%d,%d", tc.w, tc.h)
				url := srv.base + "/login?theme=" + tc.theme
				run := func(extra ...string) []byte {
					ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					defer cancel()
					args := append([]string{"--headless=new", "--disable-gpu", "--no-sandbox",
						"--user-data-dir=" + t.TempDir(), size, "--virtual-time-budget=10000"}, extra...)
					out, err := exec.CommandContext(ctx, chrome, append(args, url)...).Output()
					if err != nil {
						t.Fatalf("chrome: %v", err)
					}
					return out
				}
				dom := string(run("--dump-dom"))
				run("--screenshot=" + shot)

				if !strings.Contains(dom, `data-theme="`+tc.theme+`"`) {
					t.Errorf("<html> lacks data-theme=%q; DOM:\n%.400s", tc.theme, dom)
				}
				if !strings.Contains(dom, `autocomplete="username"`) {
					t.Errorf("login form not rendered; DOM:\n%.800s", dom)
				}
				if strings.Contains(dom, "not been built") {
					t.Error("placeholder page served")
				}

				f, err := os.Open(shot)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				img, err := png.Decode(f)
				if err != nil {
					t.Fatalf("decode screenshot: %v", err)
				}
				r, g, b, _ := img.At(4, 4).RGBA()
				got := [3]int{int(r >> 8), int(g >> 8), int(b >> 8)}
				want := [3]int{int(tc.want.R), int(tc.want.G), int(tc.want.B)}
				for i := range got {
					if d := got[i] - want[i]; d < -1 || d > 1 {
						t.Errorf("pixel (4,4) = %v, want %v +-1 per channel", got, want)
						break
					}
				}
			})
		}
	})

	log := srv.stderr.String()
	if !strings.Contains(log, "listening") || strings.Contains(log, "not built") {
		t.Errorf("unexpected server log:\n%s", redact(log))
	}
	for name, secret := range map[string]string{
		"original admin password": password,
		"new admin password":      newPassword,
		"metrics token":           metricsToken,
	} {
		if secret == "" {
			t.Errorf("%s was never captured; the log check would prove nothing", name)
		} else if strings.Contains(log, secret) {
			t.Errorf("server log contains the %s", name)
		}
	}
}

func findChrome() string {
	if p := os.Getenv("STACKSTEAD_E2E_CHROME"); p != "" {
		return p
	}
	const mac = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	if _, err := os.Stat(mac); err == nil {
		return mac
	}
	for _, n := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// projectContainers lists the IDs of all containers (any state) of one compose project.
func projectContainers(project string) []string {
	out, _ := dockerOutput("ps", "-aq", "--filter", "label=com.docker.compose.project="+project)
	return strings.Fields(string(out))
}

// dockerStack creates, starts, observes and removes a throwaway stack. The
// name is unique so a pre-existing container is never touched; cleanup is
// registered before anything is created.
func dockerStack(t *testing.T, c *client) {
	var rnd [3]byte
	_, _ = rand.Read(rnd[:])
	name := fmt.Sprintf("e2e-smoke-%x", rnd)
	t.Cleanup(func() {
		// Best effort: remove through the API, then anything still labelled
		// with this exact compose project.
		c.do("DELETE", "/api/stacks/"+name+"?volumes=true", nil)
		time.Sleep(3 * time.Second)
		for _, id := range projectContainers(name) {
			_, _ = dockerOutput("rm", "-f", id)
		}
		_, _ = dockerOutput("network", "rm", name+"_default")
	})

	c.expect(201, "POST", "/api/stacks", map[string]any{
		"name": name, "service": "web",
		"config": map[string]any{"image": "nginx:alpine", "limits": map[string]string{"memory": "64m"}},
	})
	wait := func(opBody []byte, what string) {
		var op struct{ ID, State string }
		_ = json.Unmarshal(opBody, &op)
		deadline := time.Now().Add(240 * time.Second)
		for time.Now().Before(deadline) {
			var cur struct{ State string }
			_ = json.Unmarshal(c.expect(200, "GET", "/api/operations/"+op.ID, nil), &cur)
			if cur.State != "running" {
				if cur.State != "succeeded" {
					t.Fatalf("%s finished as %q", what, cur.State)
				}
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("%s did not finish", what)
	}
	got, body := c.do("POST", "/api/stacks/"+name+"/actions/up", map[string]any{})
	if got != 202 {
		t.Fatalf("up = %d: %.200s", got, body)
	}
	wait(body, "up")

	// Logs: every log event carries an id of the form <12hex>:<ns>:<ord>.
	idRe := regexp.MustCompile(`^[0-9a-f]{1,12}:\d+:\d+$`)
	deadline := time.Now().Add(30 * time.Second)
	sawID := false
	for !sawID && time.Now().Before(deadline) {
		sawID = readLogIDs(t, c, name, idRe)
		if !sawID {
			time.Sleep(time.Second)
		}
	}
	if !sawID {
		t.Error("no log event with a valid id")
	}

	// Metrics history appears once the sampler has run (interval 2s).
	deadline = time.Now().Add(30 * time.Second)
	for {
		var h struct{ Points []struct{ Service string } }
		_ = json.Unmarshal(c.expect(200, "GET", "/api/stacks/"+name+"/metrics", nil), &h)
		if len(h.Points) > 0 && h.Points[0].Service == "web" {
			break
		}
		if time.Now().After(deadline) {
			t.Error("no metrics history points")
			break
		}
		time.Sleep(2 * time.Second)
	}

	dashboardEndpoints(t, c)
	containerEndpoints(t, c, name)

	got, body = c.do("POST", "/api/stacks/"+name+"/actions/down", map[string]any{})
	if got != 202 {
		t.Fatalf("down = %d: %.200s", got, body)
	}
	wait(body, "down")
	got, body = c.do("DELETE", "/api/stacks/"+name+"?volumes=true", nil)
	if got != 202 {
		t.Fatalf("remove = %d: %.200s", got, body)
	}
	wait(body, "remove")
	if left := projectContainers(name); len(left) > 0 {
		t.Errorf("containers of %s remain after remove: %v", name, left)
	}
}

// readLogIDs reads the logs SSE for a moment and reports whether a log event
// carried a well-formed id.
func readLogIDs(t *testing.T, c *client, name string, idRe *regexp.Regexp) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q := url.Values{"follow": {"false"}, "tail": {"all"}}
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+"/api/stacks/"+name+"/logs?"+q.Encode(), nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	event, id := "", ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "id: "):
			id = strings.TrimPrefix(line, "id: ")
		case line == "":
			if event == "log" {
				if !idRe.MatchString(id) {
					t.Errorf("log event id %q is malformed", id)
					return false
				}
				return true
			}
			event, id = "", ""
		}
	}
	return false
}

// dashboardEndpoints checks the three 8b dashboard routes against the running
// binary: documented top-level keys, the window validation and the session gate.
func dashboardEndpoints(t *testing.T, c *client) {
	t.Helper()
	keys := func(path string) map[string]json.RawMessage {
		t.Helper()
		var m map[string]json.RawMessage
		if err := json.Unmarshal(c.expect(200, "GET", path, nil), &m); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return m
	}
	requireKeys := func(path string, m map[string]json.RawMessage, want ...string) {
		t.Helper()
		for _, k := range want {
			if _, ok := m[k]; !ok {
				t.Errorf("%s lacks key %q", path, k)
			}
		}
	}

	requireKeys("/api/host", keys("/api/host"), "hostname", "os", "source", "sampledAt", "containers",
		"cpu", "memory", "disk", "network", "diskIO", "volumes", "uptime", "restarts24h")
	requireKeys("/api/metrics/summary", keys("/api/metrics/summary"), "window", "step", "from", "to", "stacks", "top")

	var alerts []json.RawMessage
	if err := json.Unmarshal(c.expect(200, "GET", "/api/alerts", nil), &alerts); err != nil || alerts == nil {
		t.Errorf("/api/alerts is not a JSON array: %v", err)
	}

	var bad struct{ Error string }
	if err := json.Unmarshal(c.expect(400, "GET", "/api/metrics/summary?window=30m", nil), &bad); err != nil || bad.Error == "" {
		t.Errorf("window=30m: want a 400 {error} body, got %q (%v)", bad.Error, err)
	}

	anon := newClient(t, c.base)
	for _, p := range []string{"/api/host", "/api/alerts", "/api/metrics/summary"} {
		anon.expect(401, "GET", p, nil)
	}
}

// containerEndpoints checks the Containers routes with a real plain container
// next to the running smoke stack: managed values, start/stop, the 409 guard
// on stack containers, details without env, the logs stream and the session gate.
func containerEndpoints(t *testing.T, c *client, stackName string) {
	t.Helper()
	const plainName = "ss-e2e-plain"
	t.Cleanup(func() { _, _ = dockerOutput("rm", "-f", plainName) })
	_, _ = dockerOutput("rm", "-f", plainName)
	if out, err := exec.Command("docker", "run", "-d", "--label", "io.stackstead.e2e=1",
		"--name", plainName, "busybox", "sleep", "300").CombinedOutput(); err != nil {
		t.Skipf("cannot start the plain container (busybox unavailable?): %v: %.200s", err, out)
	}

	type row struct {
		ID, Name, State, Managed, Stack string
	}
	find := func(match func(row) bool) (row, bool) {
		var rows []row
		_ = json.Unmarshal(c.expect(200, "GET", "/api/containers", nil), &rows)
		for _, r := range rows {
			if match(r) {
				return r, true
			}
		}
		return row{}, false
	}
	var plain, stackRow row
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		p, okPlain := find(func(r row) bool { return r.Name == plainName })
		s, okStack := find(func(r row) bool { return r.Stack == stackName })
		if okPlain && okStack {
			plain, stackRow = p, s
			break
		}
		time.Sleep(time.Second)
	}
	if plain.ID == "" || stackRow.ID == "" {
		t.Fatalf("list lacks the plain container (%q) or a %s container (%q)", plain.ID, stackName, stackRow.ID)
	}
	if plain.Managed != "none" || stackRow.Managed != "stack" {
		t.Errorf("managed = %q (plain), %q (stack); want none, stack", plain.Managed, stackRow.Managed)
	}

	var after row
	_ = json.Unmarshal(c.expect(200, "POST", "/api/containers/"+plain.ID+"/stop", map[string]any{}), &after)
	if after.State != "exited" {
		t.Errorf("state after stop = %q, want exited", after.State)
	}
	_ = json.Unmarshal(c.expect(200, "POST", "/api/containers/"+plain.ID+"/start", map[string]any{}), &after)
	if after.State != "running" {
		t.Errorf("state after start = %q, want running", after.State)
	}
	c.expect(409, "POST", "/api/containers/"+stackRow.ID+"/stop", map[string]any{})

	var details map[string]json.RawMessage
	if err := json.Unmarshal(c.expect(200, "GET", "/api/containers/"+plain.ID, nil), &details); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"env", "cmd", "entrypoint"} {
		if _, ok := details[k]; ok {
			t.Errorf("container details expose %q", k)
		}
	}

	logs := c.expect(200, "GET", "/api/containers/"+plain.ID+"/logs?tail=10&follow=false", nil)
	if !strings.Contains(string(logs), "event: end") {
		t.Errorf("logs stream has no end event: %.200s", logs)
	}

	anon := newClient(t, c.base)
	for _, p := range []string{"/api/containers", "/api/containers/" + plain.ID, "/api/containers/" + plain.ID + "/logs?follow=false"} {
		anon.expect(401, "GET", p, nil)
	}
	for _, a := range []string{"start", "stop", "restart"} {
		anon.expect(401, "POST", "/api/containers/"+plain.ID+"/"+a, map[string]any{})
	}
}
