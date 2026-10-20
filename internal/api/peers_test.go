package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/peers"
	"github.com/rcss-labs/stackstead/internal/store"
)

// peerNode is a real Stackstead API on a loopback port with its own database,
// admin account and peer service.
type peerNode struct {
	t      *testing.T
	name   string
	srv    *httptest.Server
	svc    *peers.Service
	store  *peers.Store
	cookie *http.Cookie
}

func newPeerNode(t *testing.T, name string) *peerNode {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), name+".db"))
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
	st := peers.NewStore(db, time.Now)
	svc := peers.NewService(peers.ServiceConfig{
		Store: st, Version: "9.9.9", Logger: slog.New(slog.DiscardHandler), Timeout: 3 * time.Second,
		Hostname: func() (string, error) { return name, nil },
	})
	handler := api.New(api.Options{
		Auth: authSvc, Limiter: auth.NewLoginLimiter(5, time.Minute, time.Now), Peers: svc,
		Static: fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}, Logger: slog.New(slog.DiscardHandler),
	})
	n := &peerNode{t: t, name: name, svc: svc, store: st}
	n.srv = httptest.NewServer(handler)
	t.Cleanup(n.srv.Close)
	return n
}

func (n *peerNode) url() string { return n.srv.URL }

func (n *peerNode) login() {
	n.t.Helper()
	body := fmt.Sprintf(`{"username":"admin","password":%q}`, adminPassword)
	resp, err := http.Post(n.url()+"/api/login", "application/json", strings.NewReader(body))
	if err != nil {
		n.t.Fatal(err)
	}
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == "stackstead_session" {
			n.cookie = c
		}
	}
	if n.cookie == nil {
		n.t.Fatalf("login on %s gave no session (status %d)", n.name, resp.StatusCode)
	}
}

// do sends a signed-in request and returns the status and body.
func (n *peerNode) do(method, path, body string, opts ...func(*http.Request)) (int, string) {
	n.t.Helper()
	req, err := http.NewRequest(method, n.url()+path, strings.NewReader(body))
	if err != nil {
		n.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(n.cookie)
	for _, opt := range opts {
		opt(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		n.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (n *peerNode) token() string {
	n.t.Helper()
	status, body := n.do("POST", "/api/peers/tokens", "")
	if status != 201 {
		n.t.Fatalf("create token = %d %s", status, body)
	}
	var tok apitypes.ConnectionToken
	if err := json.Unmarshal([]byte(body), &tok); err != nil {
		n.t.Fatal(err)
	}
	return tok.Token
}

func (n *peerNode) list() []apitypes.PeerView {
	n.t.Helper()
	status, body := n.do("GET", "/api/peers", "")
	if status != 200 {
		n.t.Fatalf("list peers = %d %s", status, body)
	}
	var views []apitypes.PeerView
	if err := json.Unmarshal([]byte(body), &views); err != nil {
		n.t.Fatal(err)
	}
	return views
}

func addBody(url, token, advertised string, plaintext bool) string {
	raw, _ := json.Marshal(apitypes.AddPeerRequest{
		URL: url, Token: token, AdvertisedURL: advertised,
		Trust:     apitypes.PeerTrust{Plaintext: plaintext},
		SelfTrust: apitypes.PeerTrust{Plaintext: plaintext},
	})
	return string(raw)
}

// pair connects a to b over plain loopback http.
func pair(t *testing.T, a, b *peerNode) {
	t.Helper()
	status, body := a.do("POST", "/api/peers", addBody(b.url(), b.token(), a.url(), true))
	if status != 201 {
		t.Fatalf("pair = %d %s", status, body)
	}
}

func closedPortURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return "http://" + addr
}

func decodeFields(t *testing.T, body string) apitypes.ErrorBody {
	t.Helper()
	var e apitypes.ErrorBody
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	return e
}

func TestAddServerPairsBothSides(t *testing.T) {
	a, b := newPeerNode(t, "mini-a"), newPeerNode(t, "mini-b")
	a.login()
	b.login()
	pair(t, a, b)

	for _, c := range []struct {
		node  *peerNode
		other *peerNode
	}{{a, b}, {b, a}} {
		list := c.node.list()
		if len(list) != 1 {
			t.Fatalf("%s lists %d peers: %+v", c.node.name, len(list), list)
		}
		v := list[0]
		if v.Name != c.other.name || v.Status != "reachable" || v.URL != c.other.url() || !v.Plaintext || !v.Compatible {
			t.Errorf("%s sees %+v", c.node.name, v)
		}
		if p, err := c.node.store.Peer(context.Background(), v.ID); err != nil || p.State != peers.StateActive {
			t.Errorf("%s peer row = %+v, %v", c.node.name, p, err)
		}
	}
	ident, _ := b.store.Identity(context.Background(), "x")
	if a.list()[0].ID != ident.InstanceID {
		t.Errorf("A keyed B as %q, want B's real instance ID %q", a.list()[0].ID, ident.InstanceID)
	}
}

func TestAddServerFailuresStoreNothing(t *testing.T) {
	older := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(older.Close)

	cases := []struct {
		name       string
		body       func(a, b *peerNode) string
		wantStatus int
		wantField  string
		wantMsg    string
		wantCode   string
	}{
		{"wrong token", func(a, b *peerNode) string { return addBody(b.url(), "ssc_wrong", a.url(), true) }, 400, "token", "", ""},
		{"b cannot reach a", func(a, b *peerNode) string { return addBody(b.url(), b.token(), closedPortURL(t), true) }, 502, "", "could not reach", "peer_unreachable"},
		{"older stackstead", func(a, b *peerNode) string { return addBody(older.URL, "ssc_x", a.url(), true) }, 502, "", "runs an older Stackstead without connected servers", "peer_old"},
		{"plaintext unticked", func(a, b *peerNode) string { return addBody(b.url(), b.token(), a.url(), false) }, 400, "trust.plaintext", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, b := newPeerNode(t, "mini-a"), newPeerNode(t, "mini-b")
			a.login()
			b.login()
			token := b.token()
			body := c.body(a, b)
			status, resp := a.do("POST", "/api/peers", body)
			if status == 401 {
				t.Fatalf("POST /api/peers answered 401, which logs the user out")
			}
			if status != c.wantStatus {
				t.Fatalf("status = %d, body %s", status, resp)
			}
			e := decodeFields(t, resp)
			if c.wantField != "" && (len(e.Fields) == 0 || e.Fields[0].Field != c.wantField) {
				t.Errorf("fields = %+v, want %q", e.Fields, c.wantField)
			}
			if c.wantMsg != "" && !strings.Contains(e.Error, c.wantMsg) {
				t.Errorf("error = %q, want it to contain %q", e.Error, c.wantMsg)
			}
			if e.Code != c.wantCode {
				t.Errorf("code = %q, want %q", e.Code, c.wantCode)
			}
			if got := a.list(); len(got) != 0 {
				t.Errorf("A kept %+v after a failed pairing", got)
			}
			if got := b.list(); len(got) != 0 {
				t.Errorf("B kept %+v after a failed pairing", got)
			}
			if c.name == "b cannot reach a" {
				// The failed attempt must not have spent B's token.
				if s, r := a.do("POST", "/api/peers", addBody(b.url(), token, a.url(), true)); s != 201 {
					t.Errorf("retry with the same token = %d %s", s, r)
				}
			}
		})
	}
}

func TestCreateTokenResponseIsOnlyPlaintextCopy(t *testing.T) {
	a := newPeerNode(t, "mini-a")
	a.login()
	status, body := a.do("POST", "/api/peers/tokens", "")
	if status != 201 {
		t.Fatalf("status = %d %s", status, body)
	}
	var tok apitypes.ConnectionToken
	if err := json.Unmarshal([]byte(body), &tok); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok.Token, "ssc_") || tok.ExpiresAt.Before(time.Now().Add(10*time.Minute)) {
		t.Errorf("token response = %+v", tok)
	}
	for _, path := range []string{"/api/peers", "/api/peers/self"} {
		if s, got := a.do("GET", path, ""); s != 200 || strings.Contains(got, tok.Token) {
			t.Errorf("GET %s = %d, leaks the token: %v", path, s, strings.Contains(got, tok.Token))
		}
	}
}

func TestRemoveOnlineUnpairsBothSides(t *testing.T) {
	a, b := newPeerNode(t, "mini-a"), newPeerNode(t, "mini-b")
	a.login()
	b.login()
	pair(t, a, b)
	id := a.list()[0].ID
	if status, body := a.do("DELETE", "/api/peers/"+id, ""); status != 204 {
		t.Fatalf("delete = %d %s", status, body)
	}
	if got := a.list(); len(got) != 0 {
		t.Errorf("A still lists %+v", got)
	}
	if got := b.list(); len(got) != 0 {
		t.Errorf("B still lists %+v", got)
	}
	if status, _ := a.do("DELETE", "/api/peers/"+id, ""); status != 404 {
		t.Errorf("second delete = %d, want 404", status)
	}
}

func TestRemoveOfflineDeletesLocally(t *testing.T) {
	a, b := newPeerNode(t, "mini-a"), newPeerNode(t, "mini-b")
	a.login()
	b.login()
	pair(t, a, b)
	id := a.list()[0].ID
	b.srv.Close()
	if status, body := a.do("DELETE", "/api/peers/"+id, ""); status != 204 {
		t.Fatalf("delete = %d %s", status, body)
	}
	if got := a.list(); len(got) != 0 {
		t.Errorf("A still lists %+v", got)
	}
}

func TestSetSelfPushesUpdate(t *testing.T) {
	a, b := newPeerNode(t, "mini-a"), newPeerNode(t, "mini-b")
	a.login()
	b.login()
	pair(t, a, b)
	body := fmt.Sprintf(`{"name":"Mini Alpha","advertisedUrl":%q}`, a.url())
	status, resp := a.do("PUT", "/api/peers/self", body)
	if status != 200 {
		t.Fatalf("PUT self = %d %s", status, resp)
	}
	if v := b.list()[0]; v.Name != "Mini Alpha" || v.Slug != "mini-alpha" {
		t.Errorf("B sees %+v", v)
	}
	var self apitypes.ServerSelf
	_, got := a.do("GET", "/api/peers/self", "")
	if err := json.Unmarshal([]byte(got), &self); err != nil || self.Name != "Mini Alpha" || self.Slug != "mini-alpha" || self.InstanceID == "" {
		t.Errorf("self = %+v, %v", self, err)
	}
	if status, resp := a.do("PUT", "/api/peers/self", `{"name":"x","advertisedUrl":"nope"}`); status != 400 || !strings.Contains(resp, "advertisedUrl") {
		t.Errorf("bad advertised url = %d %s", status, resp)
	}
}

func TestSuggestedURLHonoursForwardedProto(t *testing.T) {
	a := newPeerNode(t, "mini-a")
	a.login()
	status, body := a.do("GET", "/api/peers/self", "", func(r *http.Request) {
		r.Host = "mini.lan"
		r.Header.Set("X-Forwarded-Proto", "https")
	})
	var self apitypes.ServerSelf
	if err := json.Unmarshal([]byte(body), &self); status != 200 || err != nil {
		t.Fatalf("status %d, %v, body %s", status, err, body)
	}
	if self.SuggestedURL != "https://mini.lan" {
		t.Errorf("suggestedUrl = %q", self.SuggestedURL)
	}
}
