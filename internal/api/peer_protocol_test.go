package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/peers"
	"github.com/rcss-labs/stackstead/internal/storage"
	"github.com/rcss-labs/stackstead/internal/store"
)

const peerHeader = "X-Stackstead-Peer"

type peerTestClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *peerTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *peerTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// peerEnv is an API environment whose peer store is reachable by the test.
type peerEnv struct {
	*testEnv
	store *peers.Store
	svc   *peers.Service
	clock *peerTestClock
}

func newPeerStore(t *testing.T, clock *peerTestClock) (*peers.Store, *peers.Service) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "peers.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := peers.NewStore(db, clock.Now)
	svc := peers.NewService(peers.ServiceConfig{
		Store:    st,
		Version:  "9.9.9",
		Hostname: func() (string, error) { return "b-host", nil },
		Now:      clock.Now,
		Logger:   slog.New(slog.DiscardHandler),
		Timeout:  2 * time.Second,
	})
	return st, svc
}

func newPeerEnv(t *testing.T) *peerEnv {
	t.Helper()
	clock := &peerTestClock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	st, svc := newPeerStore(t, clock)
	fake := newFakeStacks()
	env := newEnvWith(t, "", func(o *api.Options) {
		o.Peers = svc
		o.Stacks, o.Operations, o.FSRoots, o.Status = fake, fakeOps{fake}, fake.roots, fake.status
	})
	return &peerEnv{testEnv: env, store: st, svc: svc, clock: clock}
}

func peerReq(cred, user string) []func(*http.Request) {
	opts := []func(*http.Request){withHeader(peerHeader, cred)}
	if user != "" {
		opts = append(opts, withHeader("X-Stackstead-Peer-User", user))
	}
	return opts
}

// addPeer stores a peer that authenticates with credential.
func (e *peerEnv) addPeer(t *testing.T, id, name, credential string, state peers.PeerState) {
	t.Helper()
	err := e.store.InsertPeer(context.Background(), peers.Peer{
		InstanceID: id, Name: name, URL: "http://127.0.0.1:1", State: state,
		TLS: peers.TLSOptions{Plaintext: true},
	}, peers.HashSecret(credential))
	if err != nil {
		t.Fatal(err)
	}
}

// fakeServerA plays the connecting server: it answers hello for its credential.
func fakeServerA(t *testing.T, credential, instanceID string, tls bool) *httptest.Server {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/peer/hello" || r.Header.Get(peerHeader) != credential {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"not paired","code":"peer_not_paired"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apitypes.PeerHello{Protocol: 1, InstanceID: instanceID, Name: "mini-01", Version: "1.2.3", URL: "http://a.example"})
	})
	var srv *httptest.Server
	if tls {
		srv = httptest.NewTLSServer(h)
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	return srv
}

func pairBody(t *testing.T, instanceID, url, credential string, trust apitypes.PeerTrust) string {
	t.Helper()
	raw, err := json.Marshal(apitypes.PeerPairRequest{
		Protocol: 1, InstanceID: instanceID, Name: "mini-01", URL: url, Version: "1.2.3",
		Credential: credential, Trust: trust,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func decodeErrorBody(t *testing.T, rec *httptest.ResponseRecorder) apitypes.ErrorBody {
	t.Helper()
	var b apitypes.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("body %q is not an error body: %v", rec.Body, err)
	}
	return b
}

func TestHealthAdvertisesPeeringProtocol(t *testing.T) {
	for _, withPeers := range []bool{false, true} {
		var env *testEnv
		if withPeers {
			env = newPeerEnv(t).testEnv
		} else {
			env = newTestEnv(t, "")
		}
		rec := env.do(t, "GET", "/api/health", "")
		if got := strings.TrimSpace(rec.Body.String()); got != `{"ok":true,"peering":1}` {
			t.Errorf("withPeers=%v body = %q", withPeers, got)
		}
	}
}

func TestPairHappyPath(t *testing.T) {
	env := newPeerEnv(t)
	ctx := context.Background()
	a := fakeServerA(t, "ssp_from-a", "inst-a", false)
	token, _, err := env.store.CreateToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	body := pairBody(t, "inst-a", a.URL, "ssp_from-a", apitypes.PeerTrust{Plaintext: true})
	rec := env.do(t, "POST", "/api/peer/pair", body, withHeader(peerHeader, token))
	if rec.Code != 200 {
		t.Fatalf("pair = %d, body %s", rec.Code, rec.Body)
	}
	var resp apitypes.PeerPairResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	ident, _ := env.store.Identity(ctx, "b-host")
	if !strings.HasPrefix(resp.Credential, "ssp_") || resp.InstanceID != ident.InstanceID || resp.Protocol != 1 || resp.Version != "9.9.9" {
		t.Errorf("response = %+v", resp)
	}
	list, err := env.store.Peers(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("peers = %v, %v", list, err)
	}
	if p := list[0]; p.InstanceID != "inst-a" || p.State != peers.StateActive || p.OutCredential.Reveal() != "ssp_from-a" {
		t.Errorf("stored peer = %+v", p)
	}
	if _, err := env.store.PeerByInboundHash(ctx, peers.HashSecret(resp.Credential)); err != nil {
		t.Errorf("issued credential not stored by hash: %v", err)
	}

	again := env.do(t, "POST", "/api/peer/pair", body, withHeader(peerHeader, token))
	if again.Code != 401 || decodeErrorBody(t, again).Code != "peer_not_paired" {
		t.Errorf("second pair = %d %s", again.Code, again.Body)
	}
}

func TestPairFailures(t *testing.T) {
	tlsA := fakeServerA(t, "ssp_from-a", "inst-a", true)
	plainA := fakeServerA(t, "ssp_from-a", "inst-a", false)
	otherA := fakeServerA(t, "ssp_from-a", "inst-other", false)
	plain := apitypes.PeerTrust{Plaintext: true}

	cases := []struct {
		name        string
		prepare     func(t *testing.T, e *peerEnv, token string)
		instanceID  func(own string) string
		url         string
		trust       apitypes.PeerTrust
		wantStatus  int
		wantCode    string
		wantSub     string
		wantPeers   int
		tokenUsable bool
	}{
		{name: "expired token", prepare: func(t *testing.T, e *peerEnv, _ string) { e.clock.Advance(16 * time.Minute) },
			url: plainA.URL, trust: plain, wantStatus: 401, wantCode: "peer_not_paired"},
		{name: "used token", prepare: func(t *testing.T, e *peerEnv, token string) {
			if err := e.store.ConsumeToken(context.Background(), token, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
		}, url: plainA.URL, trust: plain, wantStatus: 401, wantCode: "peer_not_paired"},
		{name: "own instance id", instanceID: func(own string) string { return own },
			url: plainA.URL, trust: plain, wantStatus: 409, wantSub: "cannot pair with itself", tokenUsable: true},
		{name: "already connected", prepare: func(t *testing.T, e *peerEnv, _ string) {
			e.addPeer(t, "inst-a", "mini-01", "ssp_whatever", peers.StateActive)
		}, url: plainA.URL, trust: plain, wantStatus: 409, wantSub: "already", wantPeers: 1, tokenUsable: true},
		{name: "unreachable", url: "http://127.0.0.1:1", trust: plain, wantStatus: 502, wantCode: "peer_unreachable",
			wantSub: "could not reach http://127.0.0.1:1", tokenUsable: true},
		{name: "wrong instance", url: otherA.URL, trust: plain, wantStatus: 502, wantCode: "peer_wrong_instance", tokenUsable: true},
		{name: "tls pin mismatch", url: tlsA.URL, trust: apitypes.PeerTrust{PinSHA256: strings.Repeat("ab", 32)},
			wantStatus: 502, wantCode: "peer_tls", tokenUsable: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := newPeerEnv(t)
			ctx := context.Background()
			token, _, err := env.store.CreateToken(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if c.prepare != nil {
				c.prepare(t, env, token)
			}
			ident, _ := env.store.Identity(ctx, "b-host")
			id := "inst-a"
			if c.instanceID != nil {
				id = c.instanceID(ident.InstanceID)
			}
			rec := env.do(t, "POST", "/api/peer/pair", pairBody(t, id, c.url, "ssp_from-a", c.trust), withHeader(peerHeader, token))
			if rec.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d, body %s", rec.Code, c.wantStatus, rec.Body)
			}
			body := decodeErrorBody(t, rec)
			if c.wantCode != "" && body.Code != c.wantCode {
				t.Errorf("code = %q, want %q", body.Code, c.wantCode)
			}
			if !strings.Contains(body.Error, c.wantSub) {
				t.Errorf("error %q lacks %q", body.Error, c.wantSub)
			}
			if strings.Contains(rec.Body.String(), "ssp_") || strings.Contains(rec.Body.String(), "ssc_") {
				t.Errorf("body leaks a secret: %s", rec.Body)
			}
			if n, _ := env.store.CountPeers(ctx); n != c.wantPeers {
				t.Errorf("peers stored = %d, want %d", n, c.wantPeers)
			}
			if c.tokenUsable {
				if err := env.store.ConsumeToken(ctx, token, func() error { return nil }); err != nil {
					t.Errorf("token no longer usable: %v", err)
				}
			}
		})
	}
}

func TestPeerCredentialAuthenticatesNormalRoutes(t *testing.T) {
	env := newPeerEnv(t)
	env.addPeer(t, "inst-a", "mini-01", "ssp_good", peers.StateActive)

	if rec := env.do(t, "GET", "/api/stacks", "", peerReq("ssp_good", "pavel")...); rec.Code != 200 {
		t.Fatalf("stacks = %d, body %s", rec.Code, rec.Body)
	}
	rec := env.do(t, "GET", "/api/me", "", peerReq("ssp_good", "pavel")...)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"peer:mini-01 (pavel)"`) {
		t.Errorf("me = %d %s", rec.Code, rec.Body)
	}
	rec = env.do(t, "GET", "/api/me", "", peerReq("ssp_good", "")...)
	if !strings.Contains(rec.Body.String(), `"peer:mini-01 (unknown)"`) {
		t.Errorf("missing user: %s", rec.Body)
	}
	rec = env.do(t, "GET", "/api/me", "", peerReq("ssp_good", " pa\x01véel"+strings.Repeat("x", 100)+" ")...)
	var me map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if want := "peer:mini-01 (pavel" + strings.Repeat("x", 64-5) + ")"; me["username"] != want {
		t.Errorf("sanitised user = %q, want %q", me["username"], want)
	}
}

func TestPeerCredentialDenylist(t *testing.T) {
	env := newPeerEnv(t)
	env.addPeer(t, "inst-a", "mini-01", "ssp_good", peers.StateActive)
	denied := []struct{ method, path string }{
		{"GET", "/api/peers"}, {"POST", "/api/peers/tokens"}, {"GET", "/api/settings/update"},
		{"POST", "/api/settings/metrics-token"}, {"POST", "/api/password"}, {"POST", "/api/logout"},
		{"POST", "/api/setup"}, {"GET", "/api/setup/status"}, {"GET", "/api/servers/x/api/stacks"},
		{"GET", "/api/fleet/stacks"}, {"GET", "/api//settings/update"}, {"GET", "/api/peers/"},
		{"GET", "/api/./peers"}, {"GET", "/api/stacks/../settings/update"}, {"GET", "/api/servers"},
	}
	for _, c := range denied {
		body := ""
		if c.method == "POST" {
			body = `{}`
		}
		rec := env.do(t, c.method, c.path, body, peerReq("ssp_good", "pavel")...)
		if rec.Code != 403 || decodeErrorBody(t, rec).Code != "peer_forbidden" {
			t.Errorf("%s %s = %d %s, want 403 peer_forbidden", c.method, c.path, rec.Code, rec.Body)
		}
	}
	// A denied path is refused even with an unknown credential, without revealing whether it exists.
	if rec := env.do(t, "GET", "/api/peers", "", peerReq("ssp_bogus", "")...); rec.Code != 403 {
		t.Errorf("bogus credential on denied route = %d", rec.Code)
	}
	rec := env.do(t, "GET", "/api/stacks", "", peerReq("ssp_bogus", "")...)
	if rec.Code != 401 || decodeErrorBody(t, rec).Code != "peer_not_paired" {
		t.Errorf("bogus credential = %d %s", rec.Code, rec.Body)
	}
	if rec := env.do(t, "GET", "/api/stacks", "", peerReq("ssp_good", "")...); rec.Code != 200 {
		t.Errorf("normal route = %d", rec.Code)
	}
}

func TestPeerHeaderIgnoredWithoutPeerService(t *testing.T) {
	env := newTestEnv(t, "")
	rec := env.do(t, "GET", "/api/me", "", peerReq("ssp_good", "pavel")...)
	if rec.Code != 401 || decodeErrorBody(t, rec).Code != "" {
		t.Errorf("me = %d %s, want the plain 401", rec.Code, rec.Body)
	}
}

func TestPendingCredentialOnlyAnswersHello(t *testing.T) {
	env := newPeerEnv(t)
	env.addPeer(t, "inst-a", "mini-01", "ssp_pending", peers.StatePending)
	rec := env.do(t, "GET", "/api/peer/hello", "", peerReq("ssp_pending", "")...)
	if rec.Code != 200 {
		t.Fatalf("hello = %d %s", rec.Code, rec.Body)
	}
	var hello apitypes.PeerHello
	_ = json.Unmarshal(rec.Body.Bytes(), &hello)
	ident, _ := env.store.Identity(context.Background(), "b-host")
	if hello.Protocol != 1 || hello.InstanceID != ident.InstanceID || hello.Version != "9.9.9" || hello.StackCount != 1 {
		t.Errorf("hello = %+v", hello)
	}
	if rec := env.do(t, "GET", "/api/stacks", "", peerReq("ssp_pending", "")...); rec.Code != 401 {
		t.Errorf("stacks with pending credential = %d, want 401", rec.Code)
	}
	if rec := env.do(t, "GET", "/api/peer/hello", "", peerReq("ssp_bogus", "")...); rec.Code != 401 || decodeErrorBody(t, rec).Code != "peer_not_paired" {
		t.Errorf("hello with bogus credential = %d %s", rec.Code, rec.Body)
	}
	if rec := env.do(t, "GET", "/api/peer/hello", ""); rec.Code != 401 {
		t.Errorf("hello without credential = %d", rec.Code)
	}
}

func TestPeerRoutesAbsentWithoutPeerService(t *testing.T) {
	env := newTestEnv(t, "")
	if rec := env.do(t, "GET", "/api/peer/hello", ""); rec.Code != 401 {
		t.Errorf("hello without service = %d, want the catch-all 401", rec.Code)
	}
}

func TestPeerConfirmIsLoggedWithPeerActor(t *testing.T) {
	clock := &peerTestClock{t: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)}
	st, svc := newPeerStore(t, clock)
	err := st.InsertPeer(context.Background(), peers.Peer{InstanceID: "inst-a", Name: "mini-01", URL: "http://127.0.0.1:1", State: peers.StateActive}, peers.HashSecret("ssp_good"))
	if err != nil {
		t.Fatal(err)
	}
	env, cookie, _ := newStorageEnv(t, storageFixture(), func(o *api.Options, _ *storage.Service) { o.Peers = svc })

	plan := storageRequest(t, env, cookie, "POST", "/api/storage/plans", `{"kind":"volume","name":"old_data"}`)
	var p apitypes.StoragePlan
	_ = json.Unmarshal(plan.Body.Bytes(), &p)
	rec := env.do(t, "POST", "/api/storage/plans/"+p.ID+"/confirm", `{"confirm":"old_data"}`, peerReq("ssp_good", "pavel")...)
	if rec.Code != 202 {
		t.Fatalf("peer confirm = %d %s", rec.Code, rec.Body)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows := storageRequest(t, env, cookie, "GET", "/api/actions?kind=storage.", "")
		var entries []apitypes.ActionEntry
		_ = json.Unmarshal(rows.Body.Bytes(), &entries)
		if len(entries) > 0 {
			if entries[0].Username != "peer:mini-01 (pavel)" {
				t.Fatalf("actor = %q", entries[0].Username)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no action row: %s", rows.Body)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestUnpairDeletesRecord(t *testing.T) {
	env := newPeerEnv(t)
	env.addPeer(t, "inst-a", "mini-01", "ssp_good", peers.StateActive)
	rec := env.do(t, "POST", "/api/peer/unpair", "", peerReq("ssp_good", "")...)
	if rec.Code != 204 {
		t.Fatalf("unpair = %d %s", rec.Code, rec.Body)
	}
	if n, _ := env.store.CountPeers(context.Background()); n != 0 {
		t.Errorf("peers left = %d", n)
	}
	if rec := env.do(t, "POST", "/api/peer/unpair", "", peerReq("ssp_good", "")...); rec.Code != 401 {
		t.Errorf("second unpair = %d, want 401", rec.Code)
	}
}

func TestUpdateRenamesPeer(t *testing.T) {
	env := newPeerEnv(t)
	ctx := context.Background()
	env.addPeer(t, "inst-a", "mini-01", "ssp_good", peers.StateActive)
	env.addPeer(t, "inst-c", "Mini Two", "ssp_other", peers.StateActive)

	rec := env.do(t, "POST", "/api/peer/update", `{"name":"Mini Two","url":"http://127.0.0.1:2"}`, peerReq("ssp_good", "")...)
	if rec.Code != 204 {
		t.Fatalf("update = %d %s", rec.Code, rec.Body)
	}
	p, err := env.store.Peer(ctx, "inst-a")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Mini Two" || p.Slug != "mini-two-2" || p.URL != "http://127.0.0.1:2" {
		t.Errorf("peer after update = %+v", p)
	}
	if rec := env.do(t, "POST", "/api/peer/update", `{"name":"  ","url":""}`, peerReq("ssp_good", "")...); rec.Code != 400 {
		t.Errorf("blank name = %d, want 400", rec.Code)
	}
}
