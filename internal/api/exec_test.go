package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/peers"
	"github.com/rcss-labs/stackstead/internal/store"
)

const autoShellScript = "if [ -x /bin/bash ]; then exec /bin/bash; else exec /bin/sh; fi"

// fakeExecState adds the exec half of engine.Client to fakeContainerEngine.
type fakeExecState struct {
	mu       sync.Mutex
	specs    []engine.ExecSpec
	resizes  [][2]uint16
	startErr error
	exitCode int
}

func (f *fakeContainerEngine) CreateExec(_ context.Context, _ string, spec engine.ExecSpec) (string, error) {
	f.exec.mu.Lock()
	defer f.exec.mu.Unlock()
	f.exec.specs = append(f.exec.specs, spec)
	return "exec1", nil
}

func (f *fakeContainerEngine) StartExec(_ context.Context, _ string, _ bool) (engine.ExecStream, error) {
	f.exec.mu.Lock()
	defer f.exec.mu.Unlock()
	if f.exec.startErr != nil {
		return nil, f.exec.startErr
	}
	return &fakeExecStream{out: make(chan []byte, 16), closed: make(chan struct{})}, nil
}

func (f *fakeContainerEngine) ResizeExec(_ context.Context, _ string, cols, rows uint16) error {
	f.exec.mu.Lock()
	defer f.exec.mu.Unlock()
	f.exec.resizes = append(f.exec.resizes, [2]uint16{cols, rows})
	return nil
}

func (f *fakeContainerEngine) InspectExec(context.Context, string) (engine.ExecInspect, error) {
	f.exec.mu.Lock()
	defer f.exec.mu.Unlock()
	return engine.ExecInspect{Running: false, ExitCode: f.exec.exitCode}, nil
}

func (f *fakeContainerEngine) execSpecs() []engine.ExecSpec {
	f.exec.mu.Lock()
	defer f.exec.mu.Unlock()
	return slices.Clone(f.exec.specs)
}

func (f *fakeContainerEngine) execResizes() [][2]uint16 {
	f.exec.mu.Lock()
	defer f.exec.mu.Unlock()
	return slices.Clone(f.exec.resizes)
}

// fakeExecStream echoes stdin to stdout; writing "bye\n" ends the output.
type fakeExecStream struct {
	out       chan []byte
	closed    chan struct{}
	closeOnce sync.Once
	endOnce   sync.Once
}

func (s *fakeExecStream) ReadFrame() (byte, []byte, error) {
	select {
	case p, ok := <-s.out:
		if !ok {
			return 0, nil, io.EOF
		}
		return 1, p, nil
	case <-s.closed:
		return 0, nil, io.EOF
	}
}

func (s *fakeExecStream) Write(p []byte) (int, error) {
	if string(p) == "bye\n" {
		s.endOnce.Do(func() { close(s.out) })
		return len(p), nil
	}
	select {
	case s.out <- append([]byte(nil), p...):
	case <-s.closed:
	}
	return len(p), nil
}

func (s *fakeExecStream) CloseWrite() error { return nil }
func (s *fakeExecStream) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

// execFixture is a running API server with exec enabled.
type execFixture struct {
	srv     *httptest.Server
	eng     *fakeContainerEngine
	cookie  *http.Cookie
	actions *actionlog.Store
}

func newExecFixture(t *testing.T, exec api.ExecOptions, tweak func(*api.Options), srvTweak func(*httptest.Server)) *execFixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "exec-actions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	actions := actionlog.NewStore(db, time.Now)
	eng := newFakeEngine()
	env, cookie := actionsEnv(t, eng, func(o *api.Options) {
		o.Actions = actions
		o.Exec = exec
		if tweak != nil {
			tweak(o)
		}
	})
	srv := httptest.NewUnstartedServer(env.handler)
	if srvTweak != nil {
		srvTweak(srv)
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return &execFixture{srv: srv, eng: eng, cookie: cookie, actions: actions}
}

func enabledExec() api.ExecOptions {
	return api.ExecOptions{Enabled: true, MaxSessions: 8}
}

func (f *execFixture) host() string { return strings.TrimPrefix(f.srv.URL, "http://") }

// dial opens a WebSocket with the session cookie and an Origin of the server itself.
func (f *execFixture) dial(t *testing.T, path string) (*websocket.Conn, error) {
	t.Helper()
	cfg, err := websocket.NewConfig("ws://"+f.host()+path, "http://"+f.host())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Header = http.Header{"Cookie": {f.cookie.String()}}
	return websocket.DialConfig(cfg)
}

func (f *execFixture) mustDial(t *testing.T, path string) *websocket.Conn {
	t.Helper()
	ws, err := f.dial(t, path)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	t.Cleanup(func() { ws.Close() })
	return ws
}

// get sends a plain (non-upgrade) GET, as the CLI does to read a refusal.
func (f *execFixture) get(t *testing.T, path string, hdr map[string]string, withCookie bool) (int, map[string]string) {
	t.Helper()
	req, _ := http.NewRequest("GET", f.srv.URL+path, nil)
	if withCookie {
		req.AddCookie(f.cookie)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := map[string]string{}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

// rawUpgrade performs a hand-built handshake and returns the HTTP status.
func (f *execFixture) rawUpgrade(t *testing.T, path string, hdr map[string]string) int {
	t.Helper()
	conn, err := net.Dial("tcp", f.host())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	lines := []string{
		"GET " + path + " HTTP/1.1", "Host: " + f.host(),
		"Upgrade: websocket", "Connection: Upgrade",
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==", "Sec-WebSocket-Version: 13",
	}
	for k, v := range hdr {
		lines = append(lines, k+": "+v)
	}
	_, _ = io.WriteString(conn, strings.Join(lines, "\r\n")+"\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

type wsFrame struct {
	text bool
	data []byte
}

var recvCodec = websocket.Codec{
	Marshal: func(v any) ([]byte, byte, error) { return nil, 0, errors.New("send unsupported") },
	Unmarshal: func(data []byte, payloadType byte, v any) error {
		*(v.(*wsFrame)) = wsFrame{text: payloadType == websocket.TextFrame, data: data}
		return nil
	},
}

func recv(ws *websocket.Conn) (wsFrame, error) {
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var fr wsFrame
	err := recvCodec.Receive(ws, &fr)
	return fr, err
}

func sendStdin(t *testing.T, ws *websocket.Conn, s string) {
	t.Helper()
	if err := websocket.Message.Send(ws, append([]byte{0}, s...)); err != nil {
		t.Fatal(err)
	}
}

// recvStdout reads frames until a binary stdout frame arrives.
func recvStdout(t *testing.T, ws *websocket.Conn) string {
	t.Helper()
	for {
		fr, err := recv(ws)
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if !fr.text && len(fr.data) > 0 && fr.data[0] == 1 {
			return string(fr.data[1:])
		}
	}
}

func recvControl(t *testing.T, ws *websocket.Conn, typ string) map[string]any {
	t.Helper()
	for {
		fr, err := recv(ws)
		if err != nil {
			t.Fatalf("waiting for %s: %v", typ, err)
		}
		if !fr.text {
			continue
		}
		var m map[string]any
		if json.Unmarshal(fr.data, &m) != nil {
			continue
		}
		if m["type"] == typ {
			return m
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func execPath(id string, query string) string {
	p := "/api/containers/" + id[:12] + "/exec"
	if query != "" {
		p += "?" + query
	}
	return p
}

func TestExecDisabled(t *testing.T) {
	f := newExecFixture(t, api.ExecOptions{Enabled: false, MaxSessions: 8}, nil, nil)
	status, body := f.get(t, execPath(idWeb, ""), nil, true)
	if status != 403 || body["code"] != "exec_disabled" {
		t.Errorf("got %d %v, want 403 exec_disabled", status, body)
	}
}

func TestExecRequiresSession(t *testing.T) {
	f := newExecFixture(t, enabledExec(), nil, nil)
	if status, _ := f.get(t, execPath(idWeb, ""), nil, false); status != 401 {
		t.Errorf("status = %d, want 401", status)
	}
}

func TestExecBadID(t *testing.T) {
	f := newExecFixture(t, enabledExec(), nil, nil)
	status, body := f.get(t, "/api/containers/web/exec", nil, true)
	if status != 404 || body["code"] != "container_not_found" {
		t.Errorf("got %d %v, want 404 container_not_found", status, body)
	}
}

func TestExecNotRunning(t *testing.T) {
	f := newExecFixture(t, enabledExec(), nil, nil)
	status, body := f.get(t, execPath(idDB, ""), nil, true)
	if status != 409 || body["code"] != "not_running" {
		t.Errorf("got %d %v, want 409 not_running", status, body)
	}
}

func TestExecForeignOrigin(t *testing.T) {
	f := newExecFixture(t, enabledExec(), nil, nil)
	status, body := f.get(t, execPath(idWeb, ""), map[string]string{"Origin": "https://evil.example"}, true)
	if status != 403 || body["code"] != "bad_origin" {
		t.Errorf("got %d %v, want 403 bad_origin", status, body)
	}
}

func TestExecPlainGETAfterChecksIsBadRequest(t *testing.T) {
	f := newExecFixture(t, enabledExec(), nil, nil)
	status, body := f.get(t, execPath(idWeb, ""), map[string]string{"Origin": "http://" + f.host()}, true)
	if status != 400 || body["error"] != "websocket upgrade required" {
		t.Errorf("got %d %v, want 400 websocket upgrade required", status, body)
	}
	// Every check still runs for a plain GET.
	if status, body := f.get(t, execPath(idDB, ""), nil, true); status != 409 || body["code"] != "not_running" {
		t.Errorf("plain GET on stopped container = %d %v", status, body)
	}
}

func TestExecSameHostDifferentScheme(t *testing.T) {
	f := newExecFixture(t, enabledExec(), nil, nil)
	cfg, _ := websocket.NewConfig("ws://"+f.host()+execPath(idWeb, ""), "https://"+strings.ToUpper(f.host()))
	cfg.Header = http.Header{"Cookie": {f.cookie.String()}}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		t.Fatalf("https origin for the same host must be accepted: %v", err)
	}
	ws.Close()
}

func TestExecNoOrigin(t *testing.T) {
	f := newExecFixture(t, enabledExec(), nil, nil)
	if got := f.rawUpgrade(t, execPath(idWeb, ""), map[string]string{"Cookie": f.cookie.String()}); got != 101 {
		t.Errorf("status = %d, want 101", got)
	}
}

func TestExecValidation(t *testing.T) {
	f := newExecFixture(t, enabledExec(), nil, nil)
	many := strings.Repeat("cmd=a&", 64) + "cmd=a"
	for name, q := range map[string]string{
		"65 args":        many,
		"NUL in user":    "user=ro%00ot",
		"NUL in cmd":     "cmd=a%00b",
		"too many bytes": "cmd=" + strings.Repeat("x", 4097),
		"65 char user":   "user=" + strings.Repeat("u", 65),
		"space in user":  "user=a%20b",
		"newline user":   "user=a%0Ab",
		"bad tty":        "tty=maybe",
		"bad cols":       "cols=0&rows=10",
	} {
		if status, _ := f.get(t, execPath(idWeb, q), nil, true); status != 400 {
			t.Errorf("%s: status = %d, want 400", name, status)
		}
	}
}

func TestExecAutoShellArgv(t *testing.T) {
	f := newExecFixture(t, enabledExec(), nil, nil)
	ws := f.mustDial(t, execPath(idWeb, "cols=100&rows=30"))
	sendStdin(t, ws, "ping")
	if got := recvStdout(t, ws); got != "ping" {
		t.Fatalf("echo = %q", got)
	}
	specs := f.eng.execSpecs()
	if len(specs) != 1 {
		t.Fatalf("specs = %v", specs)
	}
	want := []string{"/bin/sh", "-c", autoShellScript}
	if !slices.Equal(specs[0].Cmd, want) || !specs[0].Tty || !slices.Equal(specs[0].Env, []string{"TERM=xterm-256color"}) {
		t.Errorf("spec = %+v", specs[0])
	}
	if got := f.eng.execResizes(); len(got) == 0 || got[0] != [2]uint16{100, 30} {
		t.Errorf("resizes = %v, want first 100x30", got)
	}
}

func TestExecCustomCommandAndUser(t *testing.T) {
	f := newExecFixture(t, enabledExec(), nil, nil)
	ws := f.mustDial(t, execPath(idWeb, "cmd=ls&cmd=-la&user=app:app&tty=0"))
	sendStdin(t, ws, "x")
	recvStdout(t, ws)
	spec := f.eng.execSpecs()[0]
	if !slices.Equal(spec.Cmd, []string{"ls", "-la"}) || spec.User != "app:app" || spec.Tty || len(spec.Env) != 0 {
		t.Errorf("spec = %+v", spec)
	}
}

func TestExecNoShell(t *testing.T) {
	f := newExecFixture(t, enabledExec(), nil, nil)
	f.eng.exec.startErr = errors.New(`OCI runtime exec failed: exec: "/bin/sh": stat /bin/sh: no such file or directory`)
	ws := f.mustDial(t, execPath(idWeb, ""))
	m := recvControl(t, ws, "error")
	if m["message"] != "no shell found in this container; pass a command" {
		t.Errorf("message = %v", m["message"])
	}
	if _, err := recv(ws); err == nil {
		t.Error("connection should close after the error frame")
	}
}

func TestExecLimit(t *testing.T) {
	f := newExecFixture(t, api.ExecOptions{Enabled: true, MaxSessions: 2}, nil, nil)
	f.mustDial(t, execPath(idWeb, ""))
	f.mustDial(t, execPath(idWeb, ""))
	status, body := f.get(t, execPath(idWeb, ""), nil, true)
	if status != 429 || body["code"] != "exec_limit" {
		t.Errorf("got %d %v, want 429 exec_limit", status, body)
	}
	if _, err := f.dial(t, execPath(idWeb, "")); err == nil {
		t.Error("third session should be refused")
	}
}

func TestExecSlotReleasedOnClose(t *testing.T) {
	f := newExecFixture(t, api.ExecOptions{Enabled: true, MaxSessions: 2}, nil, nil)
	first := f.mustDial(t, execPath(idWeb, ""))
	f.mustDial(t, execPath(idWeb, ""))
	first.Close()
	deadline := time.Now().Add(time.Second)
	for {
		ws, err := f.dial(t, execPath(idWeb, ""))
		if err == nil {
			ws.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot not released within 1s: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func execEntries(t *testing.T, f *execFixture) []actionlog.Entry {
	t.Helper()
	got, err := f.actions.Recent(context.Background(), actionlog.Filter{KindPrefix: "container.exec", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(got) // oldest first
	return got
}

func runToExit(t *testing.T, f *execFixture, id string) {
	t.Helper()
	ws := f.mustDial(t, execPath(id, ""))
	sendStdin(t, ws, "bye\n")
	if m := recvControl(t, ws, "exit"); m["code"] != float64(0) {
		t.Fatalf("exit = %v", m)
	}
}

func TestExecAuditStack(t *testing.T) {
	f := newExecFixture(t, enabledExec(), nil, nil)
	runToExit(t, f, idWeb)
	waitFor(t, "end entry", func() bool { return len(execEntries(t, f)) == 2 })
	es := execEntries(t, f)
	if es[0].Result != "started" || es[1].Result != "exit 0" {
		t.Errorf("results = %q, %q", es[0].Result, es[1].Result)
	}
	for _, e := range es {
		if e.Kind != "container.exec" || e.Scope != "web" || e.Target != "web-web-1" || e.Username != "admin" {
			t.Errorf("entry = %+v", e)
		}
	}
	if !strings.Contains(es[0].Detail, "auto shell") {
		t.Errorf("detail = %q", es[0].Detail)
	}
}

func TestExecAuditHost(t *testing.T) {
	f := newExecFixture(t, enabledExec(), nil, nil)
	runToExit(t, f, idCron)
	waitFor(t, "end entry", func() bool { return len(execEntries(t, f)) == 2 })
	for _, e := range execEntries(t, f) {
		if e.Scope != "host" {
			t.Errorf("scope = %q, want host", e.Scope)
		}
	}
}

func TestExecPeerCredentialAllowed(t *testing.T) {
	clock := &peerTestClock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	st, svc := newPeerStore(t, clock)
	err := st.InsertPeer(context.Background(), peers.Peer{
		InstanceID: "inst-a", Name: "mini-01", URL: "http://127.0.0.1:1", State: peers.StateActive,
		TLS: peers.TLSOptions{Plaintext: true},
	}, peers.HashSecret("ssp_good"))
	if err != nil {
		t.Fatal(err)
	}
	f := newExecFixture(t, enabledExec(), func(o *api.Options) { o.Peers = svc }, nil)
	hdr := map[string]string{peerHeader: "ssp_good", "X-Stackstead-Peer-User": "pavel"}
	if got := f.rawUpgrade(t, execPath(idWeb, ""), hdr); got != 101 {
		t.Fatalf("status = %d, want 101", got)
	}
	waitFor(t, "start entry", func() bool { return len(execEntries(t, f)) >= 1 })
	if u := execEntries(t, f)[0].Username; u != "peer:mini-01 (pavel)" {
		t.Errorf("username = %q", u)
	}
	if got := f.rawUpgrade(t, execPath(idWeb, ""), map[string]string{peerHeader: "ssp_bad"}); got != 401 {
		t.Errorf("bad credential = %d, want 401", got)
	}
}

func TestExecOutlivesReadTimeout(t *testing.T) {
	f := newExecFixture(t, enabledExec(), nil, func(s *httptest.Server) {
		s.Config.ReadTimeout = 200 * time.Millisecond
		s.Config.WriteTimeout = 200 * time.Millisecond
	})
	ws := f.mustDial(t, execPath(idWeb, ""))
	time.Sleep(time.Second)
	sendStdin(t, ws, "still here")
	if got := recvStdout(t, ws); got != "still here" {
		t.Errorf("echo = %q", got)
	}
}

func TestInspectContainer404HasCode(t *testing.T) {
	f := newExecFixture(t, enabledExec(), nil, nil)
	status, body := f.get(t, "/api/containers/"+strings.Repeat("e", 12), nil, true)
	if status != 404 || body["code"] != "container_not_found" || body["error"] != "container not found" {
		t.Errorf("got %d %v", status, body)
	}
}

func TestOriginAllowed(t *testing.T) {
	f := newExecFixture(t, enabledExec(), nil, nil)
	cases := map[string]struct {
		host, origin string
		want         int
	}{
		"port mismatch":    {"box:8443", "https://box", 403},
		"port match":       {"box:8443", "https://box:8443", 400},
		"null origin":      {"box:8443", "null", 403},
		"origin with path": {"box:8443", "https://box:8443/some/path", 400},
	}
	for name, c := range cases {
		req, _ := http.NewRequest("GET", f.srv.URL+execPath(idWeb, ""), nil)
		req.Host = c.host
		req.Header.Set("Origin", c.origin)
		req.AddCookie(f.cookie)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: status = %d, want %d", name, resp.StatusCode, c.want)
		}
	}
}

func TestExecAuditEscapesControlCharacters(t *testing.T) {
	f := newExecFixture(t, enabledExec(), nil, nil)
	ws := f.mustDial(t, execPath(idWeb, "cmd=echo&cmd=a%0Afake"))
	sendStdin(t, ws, "x")
	recvStdout(t, ws)
	waitFor(t, "start entry", func() bool { return len(execEntries(t, f)) >= 1 })
	if d := execEntries(t, f)[0].Detail; strings.ContainsRune(d, '\n') || !strings.Contains(d, `a\nfake`) {
		t.Errorf("detail = %q", d)
	}
}
