package execws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/rcss-labs/stackstead/internal/client"
)

const testContainer = "0123456789ab"

type stub struct {
	srv *httptest.Server
	cfg client.Config
}

// newStub serves the exec route under prefix with a websocket.Server whose
// handler is fn.
func newStub(t *testing.T, prefix string, fn func(*websocket.Conn)) *stub {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("GET "+prefix+"/api/containers/{id}/exec", websocket.Server{Handler: fn})
	return serve(t, prefix, mux)
}

// rejectAll answers every request, upgrade or not, with a fixed status and body.
func rejectAll(t *testing.T, status int, body string) *stub {
	t.Helper()
	return serve(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func serve(t *testing.T, prefix string, h http.Handler) *stub {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL + prefix)
	if err != nil {
		t.Fatal(err)
	}
	return &stub{srv: srv, cfg: client.Config{BaseURL: u, Session: "sess123"}}
}

func sendJSON(c *websocket.Conn, v any) {
	b, _ := json.Marshal(v)
	_ = websocket.Message.Send(c, string(b))
}

func dial(t *testing.T, s *stub, o Options) *Session {
	t.Helper()
	sess, err := Dial(context.Background(), s.cfg, testContainer, o)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func runToEnd(t *testing.T, sess *Session) End {
	t.Helper()
	end, err := sess.Run(&bytes.Buffer{}, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return end
}

func TestDialSendsCookieAndSameOrigin(t *testing.T) {
	var mu sync.Mutex
	var req *http.Request
	s := newStub(t, "/prefix", func(c *websocket.Conn) {
		mu.Lock()
		req = c.Request()
		mu.Unlock()
		sendJSON(c, map[string]any{"type": "exit", "code": 0})
	})
	runToEnd(t, dial(t, s, Options{}))
	mu.Lock()
	defer mu.Unlock()
	if req.URL.Path != "/prefix/api/containers/"+testContainer+"/exec" {
		t.Errorf("path = %q", req.URL.Path)
	}
	if got := req.Header.Get("Cookie"); got != "stackstead_session=sess123" {
		t.Errorf("Cookie = %q", got)
	}
	if got, want := req.Header.Get("Origin"), "http://"+s.cfg.BaseURL.Host; got != want {
		t.Errorf("Origin = %q, want %q", got, want)
	}
}

func TestWebsocketSchemeFollowsBase(t *testing.T) {
	for in, want := range map[string]string{
		"http://h:1/p": "ws://h:1/p/api/containers/x/exec",
		"https://h/":   "wss://h/api/containers/x/exec",
	} {
		u, _ := url.Parse(in)
		got, origin := execURL(u, "x", Options{})
		if len(got) < len(want) || got[:len(want)] != want {
			t.Errorf("execURL(%s) = %q, want prefix %q", in, got, want)
		}
		if origin != u.Scheme+"://"+u.Host {
			t.Errorf("origin = %q", origin)
		}
	}
}

func TestDialQuery(t *testing.T) {
	var mu sync.Mutex
	var q url.Values
	s := newStub(t, "", func(c *websocket.Conn) {
		mu.Lock()
		q = c.Request().URL.Query()
		mu.Unlock()
		sendJSON(c, map[string]any{"type": "exit", "code": 0})
	})
	runToEnd(t, dial(t, s, Options{Cmd: []string{"ls", "-la", "/tmp"}, User: "root:wheel", TTY: false, Cols: 120, Rows: 40}))
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(q["cmd"], []string{"ls", "-la", "/tmp"}) {
		t.Errorf("cmd = %v", q["cmd"])
	}
	for k, want := range map[string]string{"user": "root:wheel", "tty": "0", "cols": "120", "rows": "40"} {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestDialHTTPErrorMapped(t *testing.T) {
	t.Run("conflict with code", func(t *testing.T) {
		s := rejectAll(t, http.StatusConflict, `{"error":"container is not running","code":"not_running"}`)
		_, err := Dial(context.Background(), s.cfg, testContainer, Options{})
		var ce *client.Error
		if !errors.As(err, &ce) || ce.Status != 409 || ce.Message != "container is not running" {
			t.Fatalf("err = %#v", err)
		}
		if !errors.Is(err, client.ErrConflict) {
			t.Error("want errors.Is ErrConflict")
		}
	})
	t.Run("bare 404 is old server", func(t *testing.T) {
		s := rejectAll(t, http.StatusNotFound, "404 page not found")
		_, err := Dial(context.Background(), s.cfg, testContainer, Options{})
		if !errors.Is(err, ErrOldServer) {
			t.Fatalf("err = %v, want ErrOldServer", err)
		}
	})
	t.Run("404 with code stays an API error", func(t *testing.T) {
		s := rejectAll(t, http.StatusNotFound, `{"error":"no such container","code":"container_not_found"}`)
		_, err := Dial(context.Background(), s.cfg, testContainer, Options{})
		var ce *client.Error
		if errors.Is(err, ErrOldServer) || !errors.As(err, &ce) || ce.Status != 404 {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDialNoRedirect(t *testing.T) {
	followed := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed = true }))
	defer other.Close()
	s := serve(t, "", http.RedirectHandler(other.URL, http.StatusFound))
	if _, err := Dial(context.Background(), s.cfg, testContainer, Options{}); err == nil {
		t.Fatal("want error for a redirect")
	}
	if followed {
		t.Error("redirect was followed")
	}
}

func TestRunExitCode(t *testing.T) {
	s := newStub(t, "", func(c *websocket.Conn) {
		_ = websocket.Message.Send(c, []byte{1, 'a'})
		_ = websocket.Message.Send(c, []byte{2, 'b'})
		sendJSON(c, map[string]any{"type": "exit", "code": 7})
	})
	sess := dial(t, s, Options{})
	var out, errb bytes.Buffer
	end, err := sess.Run(&out, &errb, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "a" || errb.String() != "b" {
		t.Errorf("stdout=%q stderr=%q", out.String(), errb.String())
	}
	if end.ExitCode == nil || *end.ExitCode != 7 || end.Reason != "" {
		t.Errorf("end = %+v", end)
	}
}

func TestRunAnswersPing(t *testing.T) {
	got := make(chan string, 1)
	s := newStub(t, "", func(c *websocket.Conn) {
		sendJSON(c, map[string]any{"type": "ping"})
		var msg string
		if err := websocket.Message.Receive(c, &msg); err == nil {
			got <- msg
		}
		sendJSON(c, map[string]any{"type": "exit", "code": 0})
	})
	runToEnd(t, dial(t, s, Options{}))
	select {
	case m := <-got:
		if m != `{"type":"pong"}` {
			t.Errorf("reply = %s", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no pong")
	}
}

func TestRunIdleWarningCallback(t *testing.T) {
	s := newStub(t, "", func(c *websocket.Conn) {
		sendJSON(c, map[string]any{"type": "idle_warning", "seconds": 30})
		sendJSON(c, map[string]any{"type": "exit", "code": 0})
	})
	sess := dial(t, s, Options{})
	var secs []int
	if _, err := sess.Run(&bytes.Buffer{}, &bytes.Buffer{}, func(n int) { secs = append(secs, n) }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(secs, []int{30}) {
		t.Errorf("warnings = %v", secs)
	}
}

func TestRunClosedReason(t *testing.T) {
	s := newStub(t, "", func(c *websocket.Conn) {
		sendJSON(c, map[string]any{"type": "closed", "reason": "idle"})
	})
	end := runToEnd(t, dial(t, s, Options{}))
	if end.Reason != "idle" || end.ExitCode != nil {
		t.Errorf("end = %+v", end)
	}
}

func TestRunErrorFrame(t *testing.T) {
	s := newStub(t, "", func(c *websocket.Conn) {
		sendJSON(c, map[string]any{"type": "error", "message": "no shell found"})
	})
	end := runToEnd(t, dial(t, s, Options{}))
	if end.Reason != "error: no shell found" {
		t.Errorf("end = %+v", end)
	}
}

func TestRunDisconnectWithoutExitIsError(t *testing.T) {
	s := newStub(t, "", func(c *websocket.Conn) {})
	sess := dial(t, s, Options{})
	if _, err := sess.Run(&bytes.Buffer{}, &bytes.Buffer{}, nil); err == nil {
		t.Fatal("want error when the stream ends without an exit frame")
	}
}

// rawFrame keeps the websocket payload type, which websocket.Message hides.
type rawFrame struct {
	text bool
	data []byte
}

var rawCodec = websocket.Codec{
	Marshal: func(v any) ([]byte, byte, error) { return nil, 0, errors.New("unused") },
	Unmarshal: func(data []byte, payloadType byte, v any) error {
		*(v.(*rawFrame)) = rawFrame{text: payloadType == websocket.TextFrame, data: append([]byte(nil), data...)}
		return nil
	},
}

func TestClientFrames(t *testing.T) {
	done := make(chan []rawFrame, 1)
	s := newStub(t, "", func(c *websocket.Conn) {
		var got []rawFrame
		for i := 0; i < 4; i++ {
			var f rawFrame
			if err := rawCodec.Receive(c, &f); err != nil {
				break
			}
			got = append(got, f)
		}
		done <- got
		sendJSON(c, map[string]any{"type": "exit", "code": 0})
	})
	sess := dial(t, s, Options{TTY: true})
	for _, err := range []error{sess.Stdin([]byte("hi")), sess.Resize(100, 30), sess.CloseStdin(), sess.Stdin([]byte("!"))} {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []rawFrame{
		{false, []byte{0, 'h', 'i'}},
		{true, []byte(`{"type":"resize","cols":100,"rows":30}`)},
		{true, []byte(`{"type":"eof"}`)},
		{false, []byte{0, '!'}},
	}
	if got := <-done; !reflect.DeepEqual(got, want) {
		t.Errorf("frames = %+v, want %+v", got, want)
	}
}
