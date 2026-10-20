package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/peers"
	"github.com/rcss-labs/stackstead/internal/store"
)

const forwardCredential = "ssp_forward-test-credential"

// syncBuffer is a log sink safe for concurrent handlers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// forwardRig is server A with one stored peer whose URL points at a fake
// upstream, so the forwarding path is tested against exact upstream answers.
type forwardRig struct {
	t       *testing.T
	a       *httptest.Server
	svc     *peers.Service
	store   *peers.Store
	cookie  *http.Cookie
	closing chan struct{}
	logs    *syncBuffer
	peerID  string
}

func newForwardRig(t *testing.T, upstream *httptest.Server, tls peers.TLSOptions) *forwardRig {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	authSvc, err := auth.NewService(db, time.Now, auth.WithFastHashingForTests())
	if err != nil {
		t.Fatal(err)
	}
	if err := authSvc.CreateAdmin(context.Background(), "admin", adminPassword); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st := peers.NewStore(db, time.Now)
	svc := peers.NewService(peers.ServiceConfig{
		Store: st, Version: "9.9.9", Logger: logger, Timeout: 3 * time.Second,
		Hostname: func() (string, error) { return "mini-a", nil },
	})
	rig := &forwardRig{t: t, svc: svc, store: st, closing: make(chan struct{}), logs: logs, peerID: "peer-b"}
	peer := peers.Peer{
		InstanceID: rig.peerID, Name: "mini-b", URL: upstream.URL, TLS: tls,
		OutCredential: peers.Secret(forwardCredential), State: peers.StateActive,
	}
	if err := st.InsertPeer(context.Background(), peer, "hash-b"); err != nil {
		t.Fatal(err)
	}
	handler := api.New(api.Options{
		Auth: authSvc, Limiter: auth.NewLoginLimiter(5, time.Minute, time.Now), Peers: svc,
		Static: fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}, Logger: logger,
		Closing: rig.closing, SSEWriteTimeout: time.Second,
	})
	rig.a = httptest.NewServer(handler)
	t.Cleanup(rig.a.Close)

	resp, err := http.Post(rig.a.URL+"/api/login", "application/json",
		strings.NewReader(fmt.Sprintf(`{"username":"admin","password":%q}`, adminPassword)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == "stackstead_session" {
			rig.cookie = c
		}
	}
	if rig.cookie == nil {
		t.Fatal("login gave no session")
	}
	return rig
}

func (r *forwardRig) request(ctx context.Context, method, path, body string, opts ...func(*http.Request)) *http.Response {
	r.t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, r.a.URL+"/api/servers/"+r.peerID+"/api"+path, strings.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(r.cookie)
	for _, opt := range opts {
		opt(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (r *forwardRig) do(method, path, body string, opts ...func(*http.Request)) (*http.Response, string) {
	r.t.Helper()
	resp := r.request(context.Background(), method, path, body, opts...)
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

func TestForwardUnary(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/stacks":
			if r.URL.RawQuery != "q=a%2Fb&x=1" {
				t.Errorf("query = %q", r.URL.RawQuery)
			}
			w.Header().Set("X-Stackstead-Checks", "partial")
			fmt.Fprint(w, `[{"name":"web"}]`)
		case r.Method == "PUT" && r.URL.Path == "/api/stacks/web/compose":
			if r.Header.Get("If-Match") != `"stale"` {
				t.Errorf("If-Match = %q", r.Header.Get("If-Match"))
			}
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":"changed on disk","code":"hash_mismatch"}`)
		case r.Method == "DELETE":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	rig := newForwardRig(t, upstream, peers.TLSOptions{Plaintext: true})

	resp, body := rig.do("GET", "/stacks?q=a%2Fb&x=1", "")
	if resp.StatusCode != 200 || body != `[{"name":"web"}]` {
		t.Fatalf("list = %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Stackstead-Checks") != "partial" {
		t.Errorf("X-Stackstead-Checks = %q", resp.Header.Get("X-Stackstead-Checks"))
	}

	resp, body = rig.do("PUT", "/stacks/web/compose", `{"content":"x"}`, func(r *http.Request) {
		r.Header.Set("If-Match", `"stale"`)
	})
	if resp.StatusCode != 409 || body != `{"error":"changed on disk","code":"hash_mismatch"}` {
		t.Fatalf("conflict = %d %s", resp.StatusCode, body)
	}

	resp, _ = rig.do("DELETE", "/stacks/web", "")
	if resp.StatusCode != 204 {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
}

func TestForwardStripsBrowserCredentials(t *testing.T) {
	seen := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		http.SetCookie(w, &http.Cookie{Name: "upstream", Value: "1"})
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer upstream.Close()
	rig := newForwardRig(t, upstream, peers.TLSOptions{Plaintext: true})

	resp, _ := rig.do("GET", "/stacks", "", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer browser-secret")
		r.Header.Set("X-Stackstead-Peer", "spoofed")
		r.Header.Set("X-Stackstead-Peer-User", "mallory")
		r.Header.Set("X-Forwarded-For", "6.6.6.6")
		r.Header.Set("Forwarded", "for=6.6.6.6")
		r.Header.Set("Last-Event-ID", "42")
	})
	var h http.Header
	select {
	case h = <-seen:
	case <-time.After(2 * time.Second):
		t.Fatalf("the upstream was never called (status %d)", resp.StatusCode)
	}
	if h.Get("Cookie") != "" || h.Get("Authorization") != "" {
		t.Errorf("browser credentials reached upstream: cookie=%q auth=%q", h.Get("Cookie"), h.Get("Authorization"))
	}
	if h.Get("X-Stackstead-Peer") != forwardCredential {
		t.Errorf("peer header = %q", h.Get("X-Stackstead-Peer"))
	}
	if h.Get("X-Stackstead-Peer-User") != "admin" {
		t.Errorf("peer user = %q", h.Get("X-Stackstead-Peer-User"))
	}
	if h.Get("X-Forwarded-For") != "" || h.Get("Forwarded") != "" {
		t.Errorf("forwarding headers reached upstream: %v", h)
	}
	if h.Get("Last-Event-ID") != "42" {
		t.Errorf("Last-Event-ID = %q", h.Get("Last-Event-ID"))
	}
	if resp.Header.Get("Set-Cookie") != "" {
		t.Errorf("Set-Cookie leaked to the browser: %q", resp.Header.Get("Set-Cookie"))
	}
}

func TestForwardMapsPeer401(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"authentication required"}`)
	}))
	defer upstream.Close()
	rig := newForwardRig(t, upstream, peers.TLSOptions{Plaintext: true})

	resp, body := rig.do("GET", "/stacks", "")
	if resp.StatusCode != http.StatusBadGateway || decodeFields(t, body).Code != "peer_unpaired" {
		t.Fatalf("upstream 401 = %d %s", resp.StatusCode, body)
	}

	// A lost local session stays a 401 so the web app signs out.
	req, _ := http.NewRequest("GET", rig.a.URL+"/api/servers/"+rig.peerID+"/api/stacks", nil)
	req.AddCookie(&http.Cookie{Name: "stackstead_session", Value: "expired"})
	local, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	local.Body.Close()
	if local.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired local session = %d, want 401", local.StatusCode)
	}
}

func TestForwardMapsPeerRedirect(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://login.example/", http.StatusFound)
	}))
	defer upstream.Close()
	rig := newForwardRig(t, upstream, peers.TLSOptions{Plaintext: true})
	resp, body := rig.do("GET", "/stacks", "")
	if resp.StatusCode != http.StatusBadGateway || decodeFields(t, body).Code != "peer_proxy" {
		t.Fatalf("redirect = %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Location") != "" {
		t.Errorf("Location leaked: %q", resp.Header.Get("Location"))
	}
}

func TestForwardErrorsNeverLeakCredential(t *testing.T) {
	t.Run("unreachable", func(t *testing.T) {
		upstream := httptest.NewServer(http.NotFoundHandler())
		rig := newForwardRig(t, upstream, peers.TLSOptions{Plaintext: true})
		upstream.Close()
		resp, body := rig.do("GET", "/stacks", "")
		e := decodeFields(t, body)
		if resp.StatusCode != http.StatusBadGateway || e.Code != "peer_unreachable" {
			t.Fatalf("stopped peer = %d %s", resp.StatusCode, body)
		}
		if !strings.Contains(e.Error, "mini-b") {
			t.Errorf("message %q does not name the server", e.Error)
		}
		assertNoCredential(t, rig, resp, body)
	})
	t.Run("pin mismatch", func(t *testing.T) {
		upstream := httptest.NewTLSServer(http.NotFoundHandler())
		defer upstream.Close()
		rig := newForwardRig(t, upstream, peers.TLSOptions{PinSHA256: strings.Repeat("ab", 32)})
		resp, body := rig.do("GET", "/stacks", "")
		if resp.StatusCode != http.StatusBadGateway || decodeFields(t, body).Code != "peer_tls" {
			t.Fatalf("pin mismatch = %d %s", resp.StatusCode, body)
		}
		assertNoCredential(t, rig, resp, body)
	})
}

func assertNoCredential(t *testing.T, rig *forwardRig, resp *http.Response, body string) {
	t.Helper()
	if strings.Contains(body, "ssp_") || strings.Contains(rig.logs.String(), "ssp_") {
		t.Errorf("credential leaked.\nbody: %s\nlogs: %s", body, rig.logs.String())
	}
	for name, values := range resp.Header {
		for _, v := range values {
			if strings.Contains(v, "ssp_") {
				t.Errorf("credential in response header %s", name)
			}
		}
	}
}

func TestForwardSSE(t *testing.T) {
	release := make(chan struct{})
	lastID := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastID <- r.Header.Get("Last-Event-ID")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "id: 1\nevent: one\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "id: 2\nevent: two\ndata: {}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer upstream.Close()
	rig := newForwardRig(t, upstream, peers.TLSOptions{Plaintext: true})

	resp := rig.request(context.Background(), "GET", "/events", "", func(r *http.Request) {
		r.Header.Set("Last-Event-ID", "7")
	})
	select {
	case got := <-lastID:
		if got != "7" {
			t.Errorf("Last-Event-ID = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("the upstream was never called (status %d)", resp.StatusCode)
	}
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	waitFor := func(want string) {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream ended before %q", want)
				}
				if l == want {
					return
				}
			case <-deadline:
				t.Fatalf("did not receive %q in time", want)
			}
		}
	}
	waitFor("event: one") // arrives while upstream is still blocked: flush per write
	close(release)
	waitFor("event: two")
}

func TestForwardStreamEndsOnClosing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": open\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	rig := newForwardRig(t, upstream, peers.TLSOptions{Plaintext: true})

	resp := rig.request(context.Background(), "GET", "/events", "")
	if resp.StatusCode != 200 {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	br := bufio.NewReader(resp.Body)
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	close(rig.closing)
	done := make(chan struct{})
	go func() {
		io.Copy(io.Discard, br)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("forwarded stream did not end after Closing")
	}
}

func TestForwardIncompatibleOrWrongInstance(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hello apitypes.PeerHello
		code  string
	}{
		{"wrong instance", apitypes.PeerHello{Protocol: peers.Protocol, InstanceID: "someone-else", Name: "mini-b", Version: "9.9.9"}, "peer_wrong_instance"},
		{"incompatible", apitypes.PeerHello{Protocol: peers.Protocol + 1, InstanceID: "peer-b", Name: "mini-b", Version: "9.9.9"}, "peer_version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var forwarded int
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/peer/hello" {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(tc.hello)
					return
				}
				mu.Lock()
				forwarded++
				mu.Unlock()
			}))
			defer upstream.Close()
			rig := newForwardRig(t, upstream, peers.TLSOptions{Plaintext: true})
			if _, err := rig.svc.CheckNow(context.Background(), rig.peerID); err != nil {
				t.Fatal(err)
			}
			resp, body := rig.do("GET", "/stacks", "")
			if resp.StatusCode != http.StatusBadGateway || decodeFields(t, body).Code != tc.code {
				t.Fatalf("= %d %s, want 502 %s", resp.StatusCode, body, tc.code)
			}
			mu.Lock()
			defer mu.Unlock()
			if forwarded != 0 {
				t.Errorf("%d request(s) reached the upstream", forwarded)
			}
		})
	}
}

func TestForwardUnknownServer(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	rig := newForwardRig(t, upstream, peers.TLSOptions{Plaintext: true})
	rig.peerID = "nope"
	resp, body := rig.do("GET", "/stacks", "")
	if resp.StatusCode != 404 || decodeFields(t, body).Code != "peer_unknown" {
		t.Fatalf("unknown = %d %s", resp.StatusCode, body)
	}
}

func TestForwardRejectsUnsafePaths(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.EscapedPath())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer upstream.Close()
	rig := newForwardRig(t, upstream, peers.TLSOptions{Plaintext: true})

	for _, tail := range []string{"/%2E%2E/%2E%2E/admin", "/foo%2F..%2F..%2Fadmin", "/%2Fadmin", "/a%5Cb", "/a/%2e/b", "/", ""} {
		resp, body := rig.do("GET", tail, "")
		if resp.StatusCode != 400 || decodeFields(t, body).Code != "peer_bad_path" {
			t.Errorf("tail %q = %d %s, want 400 peer_bad_path", tail, resp.StatusCode, body)
		}
	}
	mu.Lock()
	if len(hits) != 0 {
		t.Errorf("unsafe paths reached the upstream: %v", hits)
	}
	hits = nil
	mu.Unlock()

	if resp, _ := rig.do("GET", "/stacks/a%20b%C3%A9", ""); resp.StatusCode != 200 {
		t.Fatalf("encoded name = %d", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 1 || hits[0] != "/api/stacks/a%20b%C3%A9" {
		t.Errorf("upstream path = %v", hits)
	}
}

func TestForwardKeepsPeerBasePath(t *testing.T) {
	got := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer upstream.Close()
	rig := newForwardRig(t, upstream, peers.TLSOptions{Plaintext: true})
	err := rig.store.UpdatePeer(context.Background(), rig.peerID, func(p *peers.Peer) error {
		p.URL = upstream.URL + "/stackstead/"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rig.do("GET", "/stacks", "")
	select {
	case p := <-got:
		if p != "/stackstead/api/stacks" {
			t.Errorf("path = %q", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream not called")
	}
}

func TestForwardStripsHostileResponseHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("Clear-Site-Data", `"cookies"`)
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("X-Stackstead-Peer", "x")
		h.Set("X-Stackstead-Checks", "partial")
		fmt.Fprint(w, `{}`)
	}))
	defer upstream.Close()
	rig := newForwardRig(t, upstream, peers.TLSOptions{Plaintext: true})
	resp, _ := rig.do("GET", "/stacks", "")
	for _, name := range []string{"Clear-Site-Data", "Strict-Transport-Security", "Access-Control-Allow-Origin", "X-Stackstead-Peer"} {
		if v := resp.Header.Get(name); v != "" {
			t.Errorf("%s reached the browser: %q", name, v)
		}
	}
	if resp.Header.Get("X-Stackstead-Checks") != "partial" {
		t.Error("X-Stackstead-Checks was dropped")
	}
	if resp.Header.Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
		t.Errorf("CSP = %q", resp.Header.Get("Content-Security-Policy"))
	}
}
