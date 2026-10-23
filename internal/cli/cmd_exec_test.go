package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/client/execws"
)

func execRow(id, name, stack, service string, number int, state string) apitypes.ContainerRow {
	return apitypes.ContainerRow{ID: id, Name: name, Stack: stack, Service: service, Number: number, State: state}
}

func TestResolveExecTarget(t *testing.T) {
	web1 := execRow("aaaaaaaaaaaa", "web-app-1", "web", "app", 1, "running")
	web2 := execRow("bbbbbbbbbbbb", "web-app-2", "web", "app", 2, "running")
	stopped := execRow("cccccccccccc", "web-db-1", "web", "db", 1, "exited")
	old1 := execRow("dddddddddddd", "x-app-1", "x", "app", 0, "running")
	old2 := execRow("eeeeeeeeeeee", "x-app-2", "x", "app", 0, "running")

	tests := []struct {
		name                      string
		rows                      []apitypes.ContainerRow
		stack, service, container string
		index                     int
		wantID                    string
		wantCode                  int
		wantMsg                   string
	}{
		{name: "single replica", rows: []apitypes.ContainerRow{web1, stopped}, stack: "web", service: "app", wantID: web1.ID},
		{name: "two replicas need index", rows: []apitypes.ContainerRow{web1, web2}, stack: "web", service: "app", wantCode: ExitUsage, wantMsg: "web-app-1, web-app-2"},
		{name: "index by number", rows: []apitypes.ContainerRow{web1, web2}, stack: "web", service: "app", index: 2, wantID: web2.ID},
		{name: "index without number sorts by name", rows: []apitypes.ContainerRow{old2, old1}, stack: "x", service: "app", index: 2, wantID: old2.ID},
		{name: "index out of range", rows: []apitypes.ContainerRow{web1}, stack: "web", service: "app", index: 3, wantCode: ExitNotFound},
		{name: "container by name", rows: []apitypes.ContainerRow{web1, web2}, container: "web-app-2", wantID: web2.ID},
		{name: "container by id prefix", rows: []apitypes.ContainerRow{web1, web2}, container: "bbbbbbbb", wantID: web2.ID},
		{name: "no match", rows: []apitypes.ContainerRow{web1}, stack: "web", service: "nope", wantCode: ExitNotFound},
		{name: "container no match", rows: []apitypes.ContainerRow{web1}, container: "zzz", wantCode: ExitNotFound},
		{name: "not running", rows: []apitypes.ContainerRow{stopped}, stack: "web", service: "db", wantCode: ExitConflict, wantMsg: "container is not running"},
		{name: "container not running", rows: []apitypes.ContainerRow{stopped}, container: "web-db-1", wantCode: ExitConflict, wantMsg: "container is not running"},
		{name: "both selectors", rows: []apitypes.ContainerRow{web1}, stack: "web", service: "app", container: "web-app-1", wantCode: ExitUsage},
		{name: "no selector", rows: []apitypes.ContainerRow{web1}, wantCode: ExitUsage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveExecTarget(tc.rows, tc.stack, tc.service, tc.container, tc.index)
			if tc.wantCode != 0 {
				var ee *ExitError
				if !errors.As(err, &ee) || ee.Code != tc.wantCode {
					t.Fatalf("err = %v, want exit code %d", err, tc.wantCode)
				}
				if !strings.Contains(err.Error(), tc.wantMsg) {
					t.Errorf("err = %q, want it to contain %q", err, tc.wantMsg)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != tc.wantID {
				t.Errorf("ID = %s, want %s", got.ID, tc.wantID)
			}
		})
	}
}

// execServer serves /api/containers and the exec websocket. handler drives
// the websocket side; the last dial's query is kept.
type execServer struct {
	*httptest.Server
	mu    sync.Mutex
	query string
}

func newExecServer(t *testing.T, rows []apitypes.ContainerRow, handler func(*websocket.Conn)) *execServer {
	t.Helper()
	s := &execServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/containers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rows)
	})
	mux.Handle("GET /api/containers/{id}/exec", websocket.Server{Handler: func(c *websocket.Conn) {
		s.mu.Lock()
		s.query = c.Request().URL.RawQuery
		s.mu.Unlock()
		handler(c)
	}})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func (s *execServer) Query() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.query
}

func wsCtl(c *websocket.Conn, v any) {
	b, _ := json.Marshal(v)
	_ = websocket.Message.Send(c, string(b))
}

func wsOut(c *websocket.Conn, stream byte, s string) {
	_ = websocket.Message.Send(c, append([]byte{stream}, s...))
}

func execExit(c *websocket.Conn, code int) { wsCtl(c, map[string]any{"type": "exit", "code": code}) }

var execRunning = []apitypes.ContainerRow{execRow("aaaaaaaaaaaa", "web-app-1", "web", "app", 1, "running")}

func execRun(t *testing.T, te *testEnv, args ...string) error {
	t.Helper()
	return execRunCtx(context.Background(), te, args...)
}

func execRunCtx(ctx context.Context, te *testEnv, args ...string) error {
	root := &Command{Name: "stackstead", Sub: []*Command{execCommand()}}
	return Run(ctx, root, append([]string{"exec"}, args...), te.Env)
}

func TestExecExitCodePassthrough(t *testing.T) {
	for _, code := range []int{0, 7, 130} {
		srv := newExecServer(t, execRunning, func(c *websocket.Conn) { execExit(c, code) })
		te := newTestEnv(t, nil)
		t9Seed(t, te, srv.URL)
		err := execRun(t, te, "-T", "web", "app", "--", "true")
		if got := codeFor(err); got != code {
			t.Errorf("remote %d: exit code = %d (err %v)", code, got, err)
		}
		if code == 0 && err != nil {
			t.Errorf("remote 0 returned %v", err)
		}
		if err != nil && err.Error() != "" {
			t.Errorf("remote %d printed message %q", code, err)
		}
	}
}

func TestExecNonTTYStreams(t *testing.T) {
	var gotStdin atomic.Value
	var gotEOF atomic.Bool
	srv := newExecServer(t, execRunning, func(c *websocket.Conn) {
		wsOut(c, 1, "to-out")
		wsOut(c, 2, "to-err")
		var in []byte
		for {
			var m []byte
			if err := websocket.Message.Receive(c, &m); err != nil {
				break
			}
			if len(m) > 0 && m[0] == 0 {
				in = append(in, m[1:]...)
			} else if strings.Contains(string(m), `"eof"`) {
				gotEOF.Store(true)
				break
			}
		}
		gotStdin.Store(string(in))
		execExit(c, 0)
	})
	te := newTestEnv(t, nil)
	te.Stdin = strings.NewReader("in")
	t9Seed(t, te, srv.URL)
	if err := execRun(t, te, "-T", "--user", "root", "web", "app", "--", "cat", "-n"); err != nil {
		t.Fatal(err)
	}
	if gotStdin.Load() != "in" || !gotEOF.Load() {
		t.Errorf("server stdin = %v eof = %v", gotStdin.Load(), gotEOF.Load())
	}
	if te.Out.String() != "to-out" || te.Err.String() != "to-err" {
		t.Errorf("stdout = %q stderr = %q", te.Out.String(), te.Err.String())
	}
	q := srv.Query()
	for _, want := range []string{"tty=0", "user=root", "cmd=cat", "cmd=-n"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q lacks %q", q, want)
		}
	}
}

// ttyFake wires a fake terminal into the env and counts restores.
type ttyFake struct {
	restores atomic.Int32
	resizes  chan struct{}
	stdin    *io.PipeWriter
}

func withTTY(te *testEnv) *ttyFake {
	f := &ttyFake{resizes: make(chan struct{}, 1)}
	pr, pw := io.Pipe()
	f.stdin = pw
	te.Stdin = pr
	te.IsTerminal = func(uintptr) bool { return true }
	te.MakeRaw = func(uintptr) (func() error, error) {
		return func() error { f.restores.Add(1); return nil }, nil
	}
	te.TermSize = func(uintptr) (int, int, error) { return 100, 40, nil }
	te.Resizes = func(context.Context) <-chan struct{} { return f.resizes }
	return f
}

func TestExecRestoresTerminal(t *testing.T) {
	cases := []struct {
		name    string
		handler func(*websocket.Conn)
		cancel  bool
	}{
		{"normal exit", func(c *websocket.Conn) { execExit(c, 0) }, false},
		{"server closes mid-session", func(c *websocket.Conn) { wsOut(c, 1, "x"); c.Close() }, false},
		{"ctx cancel", func(c *websocket.Conn) { time.Sleep(3 * time.Second) }, true},
		{"run error", func(c *websocket.Conn) { c.Close() }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newExecServer(t, execRunning, tc.handler)
			te := newTestEnv(t, nil)
			f := withTTY(te)
			defer f.stdin.Close()
			t9Seed(t, te, srv.URL)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				time.AfterFunc(200*time.Millisecond, cancel)
			}
			done := make(chan error, 1)
			go func() { done <- execRunCtx(ctx, te, "web", "app") }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("exec did not return")
			}
			if n := f.restores.Load(); n != 1 {
				t.Errorf("restore called %d times, want 1", n)
			}
		})
	}
}

func TestExecTTYSendsSizeAndForwardsResize(t *testing.T) {
	gotResize := make(chan string, 4)
	srv := newExecServer(t, execRunning, func(c *websocket.Conn) {
		for {
			var m []byte
			if err := websocket.Message.Receive(c, &m); err != nil {
				return
			}
			if strings.Contains(string(m), `"resize"`) {
				gotResize <- string(m)
				execExit(c, 0)
				return
			}
		}
	})
	te := newTestEnv(t, nil)
	f := withTTY(te)
	defer f.stdin.Close()
	t9Seed(t, te, srv.URL)
	go func() { time.Sleep(100 * time.Millisecond); f.resizes <- struct{}{} }()
	if err := execRun(t, te, "web", "app"); err != nil {
		t.Fatal(err)
	}
	if m := <-gotResize; !strings.Contains(m, `"cols":100`) || !strings.Contains(m, `"rows":40`) {
		t.Errorf("resize frame = %s", m)
	}
	q := srv.Query()
	if !strings.Contains(q, "tty=1") || !strings.Contains(q, "cols=100") || !strings.Contains(q, "rows=40") {
		t.Errorf("query = %q", q)
	}
}

func TestExecIdleNotice(t *testing.T) {
	srv := newExecServer(t, execRunning, func(c *websocket.Conn) {
		wsCtl(c, map[string]any{"type": "idle_warning", "seconds": 60})
		wsCtl(c, map[string]any{"type": "closed", "reason": "idle"})
	})
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	err := execRun(t, te, "-T", "web", "app")
	if codeFor(err) != ExitUnavailable || !strings.Contains(err.Error(), "session closed after being idle") {
		t.Errorf("err = %v", err)
	}
	if want := "\r\n[stackstead: idle session closes in 60s]\r\n"; te.Err.String() != want {
		t.Errorf("stderr = %q, want %q", te.Err.String(), want)
	}
}

func TestExecEndReasons(t *testing.T) {
	cases := []struct {
		frame map[string]any
		code  int
		msg   string
	}{
		{map[string]any{"type": "closed", "reason": "session_expired"}, ExitAuth, "your session expired; log in again"},
		{map[string]any{"type": "closed", "reason": "shutdown"}, ExitUnavailable, "the server is shutting down"},
		{map[string]any{"type": "error", "message": "no such shell"}, ExitGeneral, "no such shell"},
	}
	for _, tc := range cases {
		srv := newExecServer(t, execRunning, func(c *websocket.Conn) { wsCtl(c, tc.frame) })
		te := newTestEnv(t, nil)
		t9Seed(t, te, srv.URL)
		err := execRun(t, te, "-T", "web", "app")
		if codeFor(err) != tc.code || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%v: err = %v (code %d)", tc.frame, err, codeFor(err))
		}
	}
}

func TestExecOldServer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/containers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(execRunning)
	})
	srv := httptest.NewServer(mux) // no exec route: plain 404
	t.Cleanup(srv.Close)
	te := newTestEnv(t, nil)
	t9Seed(t, te, srv.URL)
	err := execRun(t, te, "-T", "web", "app")
	if codeFor(err) != ExitUnavailable || !strings.Contains(err.Error(), execws.ErrOldServer.Error()) {
		t.Errorf("err = %v (code %d)", err, codeFor(err))
	}
}

func TestExecTTYAuto(t *testing.T) {
	cases := []struct {
		name       string
		stdin, out bool
		args       []string
		wantQuery  string
	}{
		{"both terminals", true, true, nil, "tty=1"},
		{"stdin piped", false, true, nil, "tty=0"},
		{"stdout piped", true, false, nil, "tty=0"},
		{"-T", true, true, []string{"-T"}, "tty=0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newExecServer(t, execRunning, func(c *websocket.Conn) { execExit(c, 0) })
			te := newTestEnv(t, nil)
			f := withTTY(te)
			defer f.stdin.Close()
			te.IsTerminal = func(fd uintptr) bool {
				if fd == 0 {
					return tc.stdin
				}
				return tc.out
			}
			t9Seed(t, te, srv.URL)
			if err := execRun(t, te, append(tc.args, "web", "app")...); err != nil {
				t.Fatal(err)
			}
			if q := srv.Query(); !strings.Contains(q, tc.wantQuery) {
				t.Errorf("query = %q, want %q", q, tc.wantQuery)
			}
		})
	}
}
