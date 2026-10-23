package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/peers"
)

// execRemote is a real exec server (fake engine) that accepts the forward
// rig's peer credential and records the headers of every request it sees.
type execRemote struct {
	mu   sync.Mutex
	seen []http.Header
}

func (e *execRemote) headers() []http.Header {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]http.Header(nil), e.seen...)
}

func newExecRemote(t *testing.T) (*execFixture, *execRemote) {
	t.Helper()
	clock := &peerTestClock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	st, svc := newPeerStore(t, clock)
	err := st.InsertPeer(context.Background(), peers.Peer{
		InstanceID: "inst-a", Name: "mini-a", URL: "http://127.0.0.1:1", State: peers.StateActive,
		TLS: peers.TLSOptions{Plaintext: true},
	}, peers.HashSecret(forwardCredential))
	if err != nil {
		t.Fatal(err)
	}
	rec := &execRemote{}
	f := newExecFixture(t, enabledExec(), func(o *api.Options) { o.Peers = svc }, func(s *httptest.Server) {
		inner := s.Config.Handler
		s.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.mu.Lock()
			rec.seen = append(rec.seen, r.Header.Clone())
			rec.mu.Unlock()
			inner.ServeHTTP(w, r)
		})
	})
	return f, rec
}

func dialThroughForward(t *testing.T, rig *forwardRig, origin string) (*websocket.Conn, error) {
	t.Helper()
	cfg, err := websocket.NewConfig("ws://"+strings.TrimPrefix(rig.a.URL, "http://")+"/api/servers/"+rig.peerID+execPath(idWeb, ""), origin)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Header = http.Header{"Cookie": {rig.cookie.String()}}
	return websocket.DialConfig(cfg)
}

func TestForwardExecStripsOrigin(t *testing.T) {
	remote, rec := newExecRemote(t)
	rig := newForwardRig(t, remote.srv, peers.TLSOptions{Plaintext: true})
	ws, err := dialThroughForward(t, rig, rig.a.URL)
	if err != nil {
		t.Fatalf("dial through forward: %v", err)
	}
	defer ws.Close()
	sendStdin(t, ws, "ping")
	if got := recvStdout(t, ws); got != "ping" {
		t.Errorf("echo = %q", got)
	}
	seen := rec.headers()
	if len(seen) != 1 {
		t.Fatalf("remote saw %d requests, want 1", len(seen))
	}
	if o := seen[0].Get("Origin"); o != "" {
		t.Errorf("remote saw Origin %q, want none", o)
	}
	if seen[0].Get(peerHeader) != forwardCredential {
		t.Errorf("remote did not get the peer credential")
	}
}

func TestForwardExecForeignOrigin(t *testing.T) {
	remote, rec := newExecRemote(t)
	rig := newForwardRig(t, remote.srv, peers.TLSOptions{Plaintext: true})
	resp, body := rig.do("GET", strings.TrimPrefix(execPath(idWeb, ""), "/api"), "", func(r *http.Request) {
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", "websocket")
		r.Header.Set("Sec-WebSocket-Version", "13")
		r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		r.Header.Set("Origin", "http://evil.example")
	})
	if resp.StatusCode != 403 || !strings.Contains(body, `"code":"bad_origin"`) {
		t.Fatalf("got %d %s, want 403 bad_origin", resp.StatusCode, body)
	}
	if n := len(rec.headers()); n != 0 {
		t.Errorf("remote was called %d times, want 0", n)
	}
}

func TestForwardExecSurvivesQuiet(t *testing.T) {
	remote, _ := newExecRemote(t)
	rig := newForwardRig(t, remote.srv, peers.TLSOptions{Plaintext: true}, func(o *api.Options) {
		o.SSEWriteTimeout = 200 * time.Millisecond
	})
	ws, err := dialThroughForward(t, rig, rig.a.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	time.Sleep(time.Second)
	sendStdin(t, ws, "still here")
	if got := recvStdout(t, ws); got != "still here" {
		t.Errorf("echo = %q", got)
	}
}

func TestForwardExecOldPeer(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer upstream.Close()
	rig := newForwardRig(t, upstream, peers.TLSOptions{Plaintext: true})
	resp, body := rig.do("GET", strings.TrimPrefix(execPath(idWeb, ""), "/api"), "", func(r *http.Request) {
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", "websocket")
		r.Header.Set("Origin", rig.a.URL)
	})
	if resp.StatusCode != 404 || body != `{"error":"not found"}` {
		t.Errorf("got %d %s, want the 404 relayed unchanged", resp.StatusCode, body)
	}
}
