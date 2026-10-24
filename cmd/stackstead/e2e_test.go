//go:build e2e

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
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
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		// Output() keeps stderr only inside the error; callers print out, so fold it in.
		out = append(out, exit.Stderr...)
	}
	return out, err
}

// redact hides the first-run setup token, which the server logs at startup.
var setupTokenRe = regexp.MustCompile(`(setup_token=)\S+`)

func redact(log string) string { return setupTokenRe.ReplaceAllString(log, "${1}[redacted]") }

type e2eServer struct {
	base   string
	bin    string
	stderr *syncBuffer
	// stacksDir is the server's stacks directory on the host.
	stacksDir string
	// stop interrupts the process and waits for it to exit; safe to call twice.
	stop func()
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
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			_ = cmd.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		})
	}
	t.Cleanup(stop)
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
	return &e2eServer{base: base, bin: bin, stderr: stderr, stacksDir: dir + "/stacks", stop: stop}
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
	var releaseDF func()
	if socket != "" {
		socket, releaseDF = pendingDFSocket(t, socket)
	}
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

	if releaseDF != nil {
		t.Run("storage overview pending", func(t *testing.T) {
			started := time.Now()
			var overview apitypes.StorageOverview
			if err := json.Unmarshal(c.expect(200, "GET", "/api/storage", nil), &overview); err != nil {
				t.Fatal(err)
			}
			if time.Since(started) > 2*time.Second {
				t.Error("overview waited for blocked disk usage")
			}
			for _, totals := range []apitypes.StorageKindTotals{overview.Images, overview.Containers, overview.Volumes, overview.BuildCache} {
				if totals.SizesAt != nil {
					t.Error("disk usage published while daemon response blocked")
				}
			}
		})
		releaseDF()
	}
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
		t.Run("storage cleanup", func(t *testing.T) { storageCleanup(t, c, srv.bin) })
		t.Run("docker stack", func(t *testing.T) { dockerStack(t, c) })
		t.Run("dockerfile stack", func(t *testing.T) { dockerfileStack(t, c, srv) })
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
		_, _ = dockerOutput("volume", "rm", name+"_data")
	})

	c.expect(201, "POST", "/api/stacks", map[string]any{
		"name": name, "services": []any{
			map[string]any{"service": "web", "config": map[string]any{"image": "nginx:alpine", "limits": map[string]string{"memory": "64m"}}},
			map[string]any{"service": "web2", "config": map[string]any{"image": "nginx:alpine"}},
		},
	})
	base := "/api/stacks/" + name
	var detail apitypes.StackDetail
	readDetail := func(body []byte, count int) {
		t.Helper()
		if err := json.Unmarshal(body, &detail); err != nil {
			t.Fatal(err)
		}
		if detail.ServiceCount != count || len(detail.Services) != count || detail.Hash == "" {
			t.Fatalf("detail: count=%d services=%d hash present=%t, want %d", detail.ServiceCount, len(detail.Services), detail.Hash != "", count)
		}
	}
	readDetail(c.expect(200, "GET", base, nil), 2)
	var initialCompose apitypes.RawCompose
	if err := json.Unmarshal(c.expect(200, "GET", base+"/compose", nil), &initialCompose); err != nil {
		t.Fatal(err)
	}
	withVolume := strings.Replace(initialCompose.Content, "  web:\n", "  web:\n    volumes: [data:/data]\n", 1) + "\nvolumes:\n  data: {}\n"
	c.expect(200, "PUT", base+"/compose", map[string]string{"content": withVolume, "hash": initialCompose.Hash})
	readDetail(c.expect(200, "GET", base, nil), 2)
	anon := newClient(t, c.base)
	anon.expect(401, "POST", base+"/services", map[string]any{})
	anon.expect(401, "POST", base+"/services/preview", map[string]any{})
	anon.expect(401, "DELETE", base+"/services/web2?hash="+detail.Hash, nil)
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
	readDetail(c.expect(200, "GET", base, nil), 2)
	for _, service := range detail.Services {
		if len(service.Containers) != 1 || service.Containers[0].State != "running" {
			t.Fatalf("%s has no running container after up: %+v", service.Name, service.Containers)
		}
	}

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
		if slices.ContainsFunc(h.Points, func(point struct{ Service string }) bool { return point.Service == "web" }) {
			break
		}
		if time.Now().After(deadline) {
			t.Error("no metrics history points")
			break
		}
		time.Sleep(2 * time.Second)
	}

	dashboardEndpoints(t, c)
	t.Run("containers", func(t *testing.T) { containerEndpoints(t, c, name) })
	t.Run("networks", func(t *testing.T) { networkEndpoints(t, c, name) })

	// File mutations leave running containers alone until a compose action.
	readDetail(c.expect(200, "GET", base, nil), 2)
	stale := detail.Hash
	readDetail(c.expect(200, "DELETE", base+"/services/web2?hash="+detail.Hash, nil), 1)
	var preview apitypes.PreviewResponse
	add := map[string]any{"service": "web2", "config": map[string]any{"image": "nginx:alpine"}, "hash": detail.Hash}
	if err := json.Unmarshal(c.expect(200, "POST", base+"/services/preview", add), &preview); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.Content, "web2:") || preview.Hash != detail.Hash {
		t.Fatalf("add preview missing web2 or changed base hash: %+v", preview)
	}
	add["hash"] = stale
	c.expect(409, "POST", base+"/services", add)
	readDetail(c.expect(200, "GET", base, nil), 1)
	if detail.Hash != preview.Hash {
		t.Fatal("stale add or preview changed the compose file")
	}
	add["hash"] = detail.Hash
	readDetail(c.expect(200, "POST", base+"/services", add), 2)
	var compose apitypes.RawCompose
	if err := json.Unmarshal(c.expect(200, "GET", base+"/compose", nil), &compose); err != nil {
		t.Fatal(err)
	}
	dependent := strings.Replace(compose.Content, "  web2:\n", "  web2:\n    depends_on: [web]\n", 1)
	if dependent == compose.Content {
		t.Fatal("web2 service not found for dependency check")
	}
	c.expect(200, "PUT", base+"/compose", map[string]string{"content": dependent, "hash": compose.Hash})
	readDetail(c.expect(200, "GET", base, nil), 2)
	if refusal := c.expect(409, "DELETE", base+"/services/web?hash="+detail.Hash, nil); !strings.Contains(string(refusal), "web2") {
		t.Fatalf("dependency refusal does not name web2: %s", refusal)
	}
	readDetail(c.expect(200, "DELETE", base+"/services/web2?hash="+detail.Hash, nil), 1)
	c.expect(409, "DELETE", base+"/services/web?hash="+detail.Hash, nil)
	wait(c.expect(202, "POST", base+"/actions/up", map[string]any{}), "apply service removal")
	if left := projectContainers(name); len(left) != 1 {
		t.Fatalf("next Up left %d project containers, want web only", len(left))
	}
	readDetail(c.expect(200, "GET", base, nil), 1)
	if detail.Services[0].Name != "web" || len(detail.Services[0].Containers) != 1 || detail.Services[0].Containers[0].State != "running" {
		t.Fatalf("next Up did not retain running web: %+v", detail.Services)
	}

	legacy := name + "-legacy"
	t.Cleanup(func() { c.do("DELETE", "/api/stacks/"+legacy, nil) })
	c.expect(201, "POST", "/api/stacks", map[string]any{
		"name": legacy, "service": "web", "config": map[string]string{"image": "nginx:alpine"},
	})
	wait(c.expect(202, "DELETE", "/api/stacks/"+legacy, nil), "legacy remove")

	got, body = c.do("POST", "/api/stacks/"+name+"/actions/down", map[string]any{})
	if got != 202 {
		t.Fatalf("down = %d: %.200s", got, body)
	}
	wait(body, "down")
	time.Sleep(2100 * time.Millisecond)
	storageProtectedVolume(t, c, name, name+"_data")
	got, body = c.do("DELETE", "/api/stacks/"+name+"?volumes=true", nil)
	if got != 202 {
		t.Fatalf("remove = %d: %.200s", got, body)
	}
	wait(body, "remove")
	if left := projectContainers(name); len(left) > 0 {
		t.Errorf("containers of %s remain after remove: %v", name, left)
	}
}

// dockerfileStack creates a stack whose service is built from a Dockerfile
// written through the API, edits that Dockerfile and rebuilds only that service.
func dockerfileStack(t *testing.T, c *client, srv *e2eServer) {
	var rnd [3]byte
	_, _ = rand.Read(rnd[:])
	name := fmt.Sprintf("e2e-df-%x", rnd)
	t.Cleanup(func() {
		c.do("DELETE", "/api/stacks/"+name+"?volumes=true", nil)
		time.Sleep(3 * time.Second)
		for _, id := range projectContainers(name) {
			_, _ = dockerOutput("rm", "-f", id)
		}
		_, _ = dockerOutput("network", "rm", name+"_default")
		_, _ = dockerOutput("rmi", "-f", "stackstead/"+name+":latest")
	})
	wait := func(opBody []byte, what string) {
		t.Helper()
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
	// inspect returns one field of the project's container for a service.
	inspect := func(service, format string) string {
		t.Helper()
		out, err := dockerOutput("ps", "-aq", "--filter", "label=com.docker.compose.project="+name,
			"--filter", "label=com.docker.compose.service="+service)
		ids := strings.Fields(string(out))
		if err != nil || len(ids) != 1 {
			t.Fatalf("%s containers = %v (err %v), want exactly one", service, ids, err)
		}
		got, err := dockerOutput("inspect", "--format", format, ids[0])
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(got))
	}

	original := "FROM busybox:1.36\nCMD [\"sleep\", \"3600\"]\n"
	c.expect(201, "POST", "/api/stacks", map[string]any{
		"name": name, "services": []any{
			map[string]any{
				"service":    "app",
				"config":     map[string]any{"build": map[string]any{"context": "./app"}, "limits": map[string]any{}},
				"dockerfile": map[string]any{"content": original},
				"extra":      map[string]any{"labels": map[string]string{"e2e": "1"}},
			},
			map[string]any{"service": "other", "config": map[string]any{"image": "busybox:1.36"}, "extra": map[string]any{"command": []string{"sleep", "3600"}}},
		},
	})
	onDisk, err := os.ReadFile(filepath.Join(srv.stacksDir, name, "app", "Dockerfile"))
	if err != nil || string(onDisk) != original {
		t.Fatalf("Dockerfile on disk = %q (err %v), want the created content", onDisk, err)
	}
	dfPath := "/api/stacks/" + name + "/services/app/dockerfile"
	var view apitypes.DockerfileView
	if err := json.Unmarshal(c.expect(200, "GET", dfPath, nil), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Editable || view.Path != "app/Dockerfile" || view.Content != original || view.Hash == "" {
		t.Fatalf("dockerfile view = %+v", view)
	}

	got, body := c.do("POST", "/api/stacks/"+name+"/actions/up", map[string]any{})
	if got != 202 {
		t.Fatalf("up = %d: %.200s", got, body)
	}
	wait(body, "up")
	if state := inspect("app", "{{.State.Status}}"); state != "running" {
		t.Fatalf("app state = %q, want running", state)
	}
	if label := inspect("app", `{{index .Config.Labels "e2e"}}`); label != "1" {
		t.Fatalf("app label e2e = %q, want 1", label)
	}
	otherID := inspect("other", "{{.Id}}")
	otherStarted := inspect("other", "{{.State.StartedAt}}")

	edited := strings.Replace(original, "3600", "3601", 1)
	var saved apitypes.DockerfileContent
	if err := json.Unmarshal(c.expect(200, "PUT", dfPath, map[string]string{"content": edited, "hash": view.Hash}), &saved); err != nil {
		t.Fatal(err)
	}
	c.expect(409, "PUT", dfPath, map[string]string{"content": original, "hash": view.Hash})
	if err := json.Unmarshal(c.expect(200, "GET", dfPath, nil), &view); err != nil {
		t.Fatal(err)
	}
	if view.Content != edited || view.Hash == "" {
		t.Fatalf("dockerfile after save = %+v", view)
	}
	if saved.Hash != "" && saved.Hash != view.Hash {
		t.Fatalf("PUT hash %q differs from GET hash %q", saved.Hash, view.Hash)
	}

	got, body = c.do("POST", "/api/stacks/"+name+"/services/app/actions/up", map[string]any{})
	if got != 202 || !strings.Contains(string(body), `"service":"app"`) {
		t.Fatalf("scoped up = %d: %.200s", got, body)
	}
	wait(body, "scoped up")
	if cmd := inspect("app", "{{json .Config.Cmd}}"); !strings.Contains(cmd, "3601") {
		t.Fatalf("app Cmd after rebuild = %s, want 3601", cmd)
	}
	if id, started := inspect("other", "{{.Id}}"), inspect("other", "{{.State.StartedAt}}"); id != otherID || started != otherStarted {
		t.Fatalf("scoped rebuild touched other: id %s->%s started %s->%s", otherID, id, otherStarted, started)
	}
	if log := srv.stderr.String(); strings.Contains(log, srv.stacksDir) {
		t.Errorf("server log leaks the host stacks path:\n%s", redact(log))
	}

	wait(c.expect(202, "DELETE", "/api/stacks/"+name+"?volumes=true", nil), "remove")
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
	plainName := "ss-e2e-plain-" + strings.ToLower(rand.Text()[:8])
	t.Cleanup(func() { _, _ = dockerOutput("rm", "-f", plainName) })
	if out, err := dockerOutput("run", "-d", "--label", "io.stackstead.e2e=1", "--name", plainName,
		"busybox", "sh", "-c", "echo ss-e2e-ready; sleep 300"); err != nil {
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
	if refusal := c.expect(409, "POST", "/api/containers/"+stackRow.ID+"/stop", map[string]any{}); !strings.Contains(string(refusal), "belongs to stack") {
		t.Errorf("409 body = %.200s, want it to say the container belongs to a stack", refusal)
	}

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
	if !strings.Contains(string(logs), "event: log") || !strings.Contains(string(logs), "ss-e2e-ready") {
		t.Errorf("logs stream has no log event with ss-e2e-ready: %.300s", logs)
	}
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

// networkEndpoints exercises server-side network guards and a real endpoint's lifecycle.
func networkEndpoints(t *testing.T, c *client, stackName string) {
	t.Helper()
	var interfaces []struct{ Default bool }
	if err := json.Unmarshal(c.expect(200, "GET", "/api/host/interfaces", nil), &interfaces); err != nil || interfaces == nil {
		t.Fatalf("interfaces must be a JSON array: %v", err)
	}
	if runtime.GOOS == "linux" {
		defaults := 0
		for _, iface := range interfaces {
			if iface.Default {
				defaults++
			}
		}
		if defaults > 1 {
			t.Errorf("interfaces have %d default routes", defaults)
		}
	}
	list := func() []apitypes.Network {
		t.Helper()
		var rows []apitypes.Network
		if err := json.Unmarshal(c.expect(200, "GET", "/api/networks", nil), &rows); err != nil || rows == nil {
			t.Fatalf("networks must be a JSON array: %v", err)
		}
		return rows
	}
	find := func(name string) (apitypes.Network, bool) {
		for _, row := range list() {
			if row.Name == name {
				return row, true
			}
		}
		return apitypes.Network{}, false
	}
	if row, ok := find("bridge"); !ok || !row.Builtin {
		t.Errorf("bridge missing or not built-in: %+v", row)
	}
	if row, ok := find(stackName + "_default"); !ok || row.Stack != stackName || len(row.Containers) != 2 {
		t.Errorf("stack network ownership/endpoints: %+v", row)
	}
	for name, reason := range map[string]string{"bridge": "built-in", stackName + "_default": stackName} {
		if body := c.expect(409, "DELETE", "/api/networks/"+name, nil); !strings.Contains(string(body), reason) {
			t.Errorf("remove %s lacks refusal reason %q: %s", name, reason, body)
		}
	}
	name := "e2e-net-" + strings.ToLower(rand.Text()[:10])
	container := "ss-e2e-net-" + strings.ToLower(rand.Text()[:10])
	t.Cleanup(func() {
		_, _ = dockerOutput("rm", "-f", container)
		_, _ = dockerOutput("network", "rm", name)
	})
	var subnet, gateway string
	var request apitypes.NetworkCreateRequest
	created := false
	var seed [1]byte
	if _, err := rand.Read(seed[:]); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		// Keep retries distinct even when the first candidate overlaps.
		r := (int(seed[0]) + attempt) % 256
		subnet, gateway = fmt.Sprintf("10.213.%d.0/24", r), fmt.Sprintf("10.213.%d.1", r)
		request = apitypes.NetworkCreateRequest{Name: name, Driver: "bridge", Subnet: subnet, Gateway: gateway}
		status, body := c.do("POST", "/api/networks", request)
		if status == 400 && strings.Contains(strings.ToLower(string(body)), "overlap") {
			continue
		}
		if status != 201 {
			t.Fatalf("create network = %d: %s", status, body)
		}
		var row apitypes.Network
		if err := json.Unmarshal(body, &row); err != nil {
			t.Fatal(err)
		}
		if row.Builtin || row.Stack != "" || row.Containers == nil || len(row.Containers) != 0 || len(row.Subnets) != 1 || row.Subnets[0] != subnet || len(row.Gateways) != 1 || row.Gateways[0] != gateway {
			t.Fatalf("created network metadata: %+v", row)
		}
		created = true
		break
	}
	if !created {
		t.Fatal("three candidate subnets overlapped existing networks")
	}
	c.expect(409, "POST", "/api/networks", request)
	if out, err := dockerOutput("run", "-d", "--rm", "--network", name, "--name", container, "busybox", "sleep", "300"); err != nil {
		t.Fatalf("start network endpoint: %v: %.200s", err, out)
	}
	prefix := netip.MustParsePrefix(subnet)
	deadline := time.Now().Add(10 * time.Second)
	for {
		row, ok := find(name)
		if ok && len(row.Containers) == 1 {
			endpoint := row.Containers[0]
			ip, err := netip.ParseAddr(strings.Split(endpoint.IPv4, "/")[0])
			if endpoint.Name != container || err != nil || !prefix.Contains(ip) {
				t.Fatalf("unexpected endpoint: %+v", endpoint)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("network endpoint not listed: %+v", row)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if body := c.expect(409, "DELETE", "/api/networks/"+name, nil); !strings.Contains(string(body), container) {
		t.Errorf("in-use refusal does not name container: %s", body)
	}
	if _, err := dockerOutput("rm", "-f", container); err != nil {
		t.Fatal(err)
	}
	c.expect(204, "DELETE", "/api/networks/"+name, nil)
	if _, ok := find(name); ok {
		t.Error("deleted network still listed")
	}
	anon := newClient(t, c.base)
	for _, route := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/networks", nil}, {"POST", "/api/networks", request}, {"DELETE", "/api/networks/x", nil}, {"GET", "/api/host/interfaces", nil},
	} {
		anon.expect(401, route.method, route.path, route.body)
	}
}

// pendingDFSocket forwards the real daemon while holding disk-usage responses.
// This proves the running server can answer before its first df completes.
func pendingDFSocket(t *testing.T, upstream string) (string, func()) {
	t.Helper()
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", upstream)
	}}
	proxy := &httputil.ReverseProxy{Director: func(r *http.Request) { r.URL.Scheme = "http"; r.URL.Host = "docker" }, Transport: transport}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/system/df") {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		proxy.ServeHTTP(w, r)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { release(); _ = server.Close(); transport.CloseIdleConnections() })
	return socket, release
}

func storageCleanup(t *testing.T, c *client, bin string) {
	t.Helper()
	name := "stackstead-e2e-" + strings.ToLower(rand.Text()[:10])
	tag := name + ":tmp"
	t.Cleanup(func() { _, _ = dockerOutput("volume", "rm", name); _, _ = dockerOutput("image", "rm", tag) })
	if out, err := dockerOutput("volume", "create", "--label", "io.stackstead.e2e=1", name); err != nil {
		t.Fatalf("create volume: %v: %s", err, out)
	}
	var volumes apitypes.StorageVolumes
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := json.Unmarshal(c.expect(200, "GET", "/api/storage/volumes", nil), &volumes); err != nil {
			t.Fatal(err)
		}
		if slices.ContainsFunc(volumes.Volumes, func(v apitypes.StorageVolume) bool {
			return v.Name == name && v.Status == "unused" && v.Protection == nil
		}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("throwaway volume not unused and unprotected")
		}
		time.Sleep(100 * time.Millisecond)
	}
	plan := func(request apitypes.StoragePlanRequest) apitypes.StoragePlan {
		t.Helper()
		var p apitypes.StoragePlan
		if err := json.Unmarshal(c.expect(201, "POST", "/api/storage/plans", request), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	confirm := func(p apitypes.StoragePlan) apitypes.Operation {
		t.Helper()
		var op apitypes.Operation
		if err := json.Unmarshal(c.expect(202, "POST", "/api/storage/plans/"+p.ID+"/confirm", map[string]string{"confirm": p.ConfirmPhrase}), &op); err != nil {
			t.Fatal(err)
		}
		if op.Stack != "" || op.Action != "storage-cleanup" {
			t.Fatalf("not host storage operation: %+v", op)
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			var current apitypes.Operation
			if err := json.Unmarshal(c.expect(200, "GET", "/api/operations/"+op.ID, nil), &current); err != nil {
				t.Fatal(err)
			}
			if current.State != "running" {
				if current.State != "succeeded" {
					t.Fatalf("cleanup state: %s", current.State)
				}
				return op
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("cleanup timed out")
		return op
	}
	p := plan(apitypes.StoragePlanRequest{Kind: "volume", Name: name})
	if len(p.Items) != 1 || p.Items[0].Ref != name {
		t.Fatalf("wrong volume preview: %+v", p.Items)
	}
	c.expect(400, "POST", "/api/storage/plans/"+p.ID+"/confirm", map[string]string{"confirm": p.ConfirmPhrase + " "})
	op := confirm(p)
	if _, err := dockerOutput("volume", "inspect", name); err == nil {
		t.Fatal("deleted volume remains")
	}
	assertAction := func(op apitypes.Operation, target, result string) {
		t.Helper()
		var actions []apitypes.ActionEntry
		if err := json.Unmarshal(c.expect(200, "GET", "/api/actions?kind=storage.delete&scope=host&limit=500", nil), &actions); err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, a := range actions {
			if a.OperationID == op.ID {
				count++
				if a.Target != target || a.Result != result || a.Username != "admin" {
					t.Fatalf("unexpected action: %+v", a)
				}
			}
		}
		if count != 1 {
			t.Fatalf("action rows=%d, want one", count)
		}
	}
	assertAction(op, name, "deleted")
	// docker tag does not pull; a fresh host has no busybox, so fetch the tag the other subtests pin.
	if out, err := dockerOutput("pull", "busybox:1.36"); err != nil {
		t.Fatalf("pull busybox: %v: %s", err, out)
	}
	if out, err := dockerOutput("tag", "busybox:1.36", tag); err != nil {
		t.Fatalf("tag busybox: %v: %s", err, out)
	}
	op = confirm(plan(apitypes.StoragePlanRequest{Kind: "image", Ref: tag}))
	if _, err := dockerOutput("image", "inspect", tag); err == nil {
		t.Fatal("throwaway tag remains")
	}
	if _, err := dockerOutput("image", "inspect", "busybox:1.36"); err != nil {
		t.Fatal("busybox removed with throwaway tag")
	}
	assertAction(op, tag, "untagged")
	config := t.TempDir()
	u, _ := url.Parse(c.base)
	cookies := c.http.Jar.Cookies(u)
	if len(cookies) == 0 {
		t.Fatal("missing authenticated session")
	}
	contextDir := filepath.Join(config, "stackstead")
	if err := os.MkdirAll(contextDir, 0700); err != nil {
		t.Fatal(err)
	}
	saved, _ := json.Marshal(map[string]any{"version": 1, "current": "e2e", "contexts": map[string]any{"e2e": map[string]any{"url": c.base, "username": "admin", "session": cookies[0].Value, "expiresAt": time.Now().Add(time.Hour)}}})
	if err := os.WriteFile(filepath.Join(contextDir, "contexts.json"), saved, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "storage", "prune", "volumes", "--preview")
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+config, "STACKSTEAD_CONTEXT=e2e")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("CLI preview: %v: %s", err, out)
	}
}

func storageProtectedVolume(t *testing.T, c *client, stack, name string) {
	t.Helper()
	var volumes apitypes.StorageVolumes
	if err := json.Unmarshal(c.expect(200, "GET", "/api/storage/volumes", nil), &volumes); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(volumes.Volumes, func(v apitypes.StorageVolume) bool {
		return v.Name == name && v.Status == "unused" && v.Protection != nil && v.Protection.Stack == stack
	}) {
		t.Fatal("stopped stack volume not unused and protected")
	}
	refusal := c.expect(409, "POST", "/api/storage/plans", apitypes.StoragePlanRequest{Kind: "volume", Name: name})
	if !strings.Contains(string(refusal), stack) {
		t.Fatal("refusal does not name owning stack")
	}
	var p apitypes.StoragePlan
	if err := json.Unmarshal(c.expect(201, "POST", "/api/storage/plans", apitypes.StoragePlanRequest{Kind: "volumes", Selection: "unused"}), &p); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(p.Items, func(i apitypes.StoragePlanItem) bool { return i.Ref == name }) {
		t.Fatal("protected volume included in deletions")
	}
	if !slices.ContainsFunc(p.Protected, func(i apitypes.StorageProtectedItem) bool { return i.Ref == name && i.Stack == stack }) {
		t.Fatal("bulk preview omits protected volume")
	}
}

// TestE2EConnectedServers pairs two real server processes over loopback and
// checks forwarding, removal and that neither log leaks a peer secret.
func TestE2EConnectedServers(t *testing.T) {
	if !web.Built() {
		t.Skip("web UI not built (run make ui)")
	}
	srvA := startServer(t, "")
	srvB := startServer(t, "")
	password := randomSecret(t)
	signIn := func(srv *e2eServer) *client {
		c := newClient(t, srv.base)
		c.expect(204, "POST", "/api/setup", map[string]string{"username": "admin", "password": password, "setupToken": ""})
		c.expect(204, "POST", "/api/login", map[string]string{"username": "admin", "password": password})
		return c
	}
	a, b := signIn(srvA), signIn(srvB)

	var token apitypes.ConnectionToken
	if err := json.Unmarshal(b.expect(201, "POST", "/api/peers/tokens", nil), &token); err != nil {
		t.Fatal(err)
	}
	a.expect(200, "PUT", "/api/peers/self", apitypes.ServerSelfUpdate{Name: "e2e-a", AdvertisedURL: srvA.base})
	plaintext := apitypes.PeerTrust{Plaintext: true}
	a.expect(201, "POST", "/api/peers", apitypes.AddPeerRequest{
		URL: srvB.base, Token: token.Token, AdvertisedURL: srvA.base, Trust: plaintext, SelfTrust: plaintext,
	})

	var bViews, aViews []apitypes.PeerView
	if err := json.Unmarshal(b.expect(200, "GET", "/api/peers", nil), &bViews); err != nil {
		t.Fatal(err)
	}
	if len(bViews) != 1 || bViews[0].Name != "e2e-a" {
		t.Fatalf("B lists %+v, want A", bViews)
	}
	if err := json.Unmarshal(a.expect(200, "GET", "/api/peers", nil), &aViews); err != nil {
		t.Fatal(err)
	}
	if len(aViews) != 1 || aViews[0].URL != srvB.base {
		t.Fatalf("A lists %+v, want B", aViews)
	}
	bID := aViews[0].ID

	a.expect(200, "GET", "/api/servers/"+bID+"/api/stacks", nil)

	a.expect(204, "DELETE", "/api/peers/"+bID, nil)
	if err := json.Unmarshal(b.expect(200, "GET", "/api/peers", nil), &bViews); err != nil {
		t.Fatal(err)
	}
	if len(bViews) != 0 {
		t.Errorf("B still lists %+v after A removed it", bViews)
	}

	// Stop both first so shutdown logs are scanned too.
	srvA.stop()
	srvB.stop()
	for name, srv := range map[string]*e2eServer{"A": srvA, "B": srvB} {
		log := redact(srv.stderr.String())
		for _, prefix := range []string{"ssc_", "ssp_"} {
			if strings.Contains(log, prefix) {
				t.Errorf("server %s log contains a %s secret", name, prefix)
			}
		}
	}
}
