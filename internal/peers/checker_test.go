package peers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

type checkRig struct {
	svc       *Service
	st        *Store
	clock     *fakeClock
	peerURL   string
	mu        sync.Mutex
	durations []time.Duration
}

func (r *checkRig) waits() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.durations...)
}

// newCheckRig builds a service whose single peer "mini-02" (instance b-id)
// is served by handler. The After seam records requested waits and never fires.
func newCheckRig(t *testing.T, handler http.Handler, addPeer bool) *checkRig {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	st := newTestStore(t, clock.now)
	r := &checkRig{st: st, clock: clock, peerURL: srv.URL}
	r.svc = NewService(ServiceConfig{
		Store: st, Version: "v1.5.0", Now: clock.now, Logger: slog.New(slog.DiscardHandler),
		Hostname: func() (string, error) { return "a-host", nil }, Timeout: 2 * time.Second,
		Resolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
		},
		After: func(d time.Duration) <-chan time.Time {
			r.mu.Lock()
			r.durations = append(r.durations, d)
			r.mu.Unlock()
			return make(chan time.Time)
		},
	})
	if addPeer {
		err := st.InsertPeer(context.Background(), Peer{
			InstanceID: "b-id", Name: "mini-02", URL: srv.URL, TLS: TLSOptions{Plaintext: true},
			OutCredential: "ssp_out", State: StateActive, Version: "v1.5.0",
		}, HashSecret("ssp_in"))
		if err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func helloHandler(hello apitypes.PeerHello) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/peer/hello" || req.Header.Get(PeerHeader) != "ssp_out" {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(hello)
	})
}

func goodHello() apitypes.PeerHello {
	return apitypes.PeerHello{Protocol: Protocol, InstanceID: "b-id", Name: "mini-02", Version: "v1.5.2", StackCount: 1}
}

func TestCheckerIdleWithoutPeers(t *testing.T) {
	r := newCheckRig(t, helloHandler(goodHello()), false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.svc.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	r.svc.Kick()
	r.svc.NoteSessionActivity()
	time.Sleep(50 * time.Millisecond)
	if w := r.waits(); len(w) != 0 {
		t.Errorf("checker requested timers %v with no peers", w)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestCheckerCadence(t *testing.T) {
	cases := []struct {
		name     string
		activity bool
		age      time.Duration
		want     time.Duration
	}{
		{"session active just now", true, 0, 30 * time.Second},
		{"session active 61 s ago", true, 61 * time.Second, 5 * time.Minute},
		{"no session ever", false, 0, 5 * time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newCheckRig(t, helloHandler(goodHello()), true)
			if c.activity {
				r.svc.NoteSessionActivity()
				r.clock.advance(c.age)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go r.svc.Run(ctx)
			deadline := time.Now().Add(3 * time.Second)
			for len(r.waits()) == 0 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			w := r.waits()
			if len(w) == 0 || w[0] != c.want {
				t.Errorf("waits = %v, want first %v", w, c.want)
			}
		})
	}
}

func TestHelloUpdatesRecord(t *testing.T) {
	r := newCheckRig(t, helloHandler(apitypes.PeerHello{
		Protocol: Protocol, InstanceID: "b-id", Name: "Mini Zero Two", Version: "v1.5.4",
		URL: "https://b-new.example", StackCount: 7,
	}), true)
	view, err := r.svc.CheckNow(context.Background(), "b-id")
	if err != nil {
		t.Fatal(err)
	}
	views, _ := r.svc.Views(context.Background())
	if len(views) != 1 {
		t.Fatalf("views = %+v", views)
	}
	v := views[0]
	if v.Name != "Mini Zero Two" || v.Version != "v1.5.4" || v.URL != "https://b-new.example" || v.StackCount != 7 ||
		v.Status != "reachable" || v.LastError != "" || !v.Compatible {
		t.Errorf("view = %+v", v)
	}
	if v.Slug != "mini-zero-two" {
		t.Errorf("slug = %q, want it recomputed from the new name", v.Slug)
	}
	if v.LastSeenAt == nil || !v.LastSeenAt.Equal(time.Unix(r.clock.now().Unix(), 0)) {
		t.Errorf("lastSeenAt = %v, want %v", v.LastSeenAt, r.clock.now())
	}
	if view.Status != "reachable" {
		t.Errorf("CheckNow view = %+v", view)
	}
}

func TestWrongInstanceStopsForwarding(t *testing.T) {
	h := goodHello()
	h.InstanceID = "someone-else"
	r := newCheckRig(t, helloHandler(h), true)
	view, err := r.svc.CheckNow(context.Background(), "b-id")
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "wrong_instance" || view.LastError == "" {
		t.Errorf("view = %+v", view)
	}
	_, err = r.svc.Usable("b-id")
	assertPeerCode(t, err, CodeWrongInstance)
}

func TestNotPairedRemovesConnection(t *testing.T) {
	r := newCheckRig(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"not paired","code":"peer_not_paired"}`))
	}), true)
	view, err := r.svc.CheckNow(context.Background(), "b-id")
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "removed" || view.LastError != "mini-02 removed this connection" {
		t.Errorf("view = %+v", view)
	}
	p, err := r.st.Peer(context.Background(), "b-id")
	if err != nil {
		t.Fatal(err)
	}
	if p.State != StateRemoved || p.OutCredential != "" {
		t.Errorf("peer = state %s, credential set %v", p.State, p.OutCredential != "")
	}
	if _, err := r.st.PeerByInboundHash(context.Background(), HashSecret("ssp_in")); err == nil {
		t.Error("inbound hash still resolves after the connection was removed")
	}
	_, err = r.svc.Usable("b-id")
	assertPeerCode(t, err, CodeUnpaired)
}

func TestProxyAnswerNeverRemoves(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"302 to login": func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, "/login", http.StatusFound)
		},
		"200 html": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>sign in</html>"))
		},
		"401 html": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("<html>sign in</html>"))
		},
		"403 json": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"forbidden by gateway"}`))
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			r := newCheckRig(t, handler, true)
			view, err := r.svc.CheckNow(context.Background(), "b-id")
			if err != nil {
				t.Fatal(err)
			}
			if view.Status != "unreachable" || !strings.Contains(view.LastError, "proxy answered instead of Stackstead") {
				t.Errorf("view = %+v", view)
			}
			p, _ := r.st.Peer(context.Background(), "b-id")
			if p.State != StateActive || p.OutCredential == "" {
				t.Errorf("peer = state %s, credential set %v; a proxy answer must not end the connection", p.State, p.OutCredential != "")
			}
			if _, err := r.svc.Usable("b-id"); err != nil {
				t.Errorf("an unreachable peer is still attempted, got %v", err)
			}
		})
	}
}

func TestUnreachablePeerStaysActive(t *testing.T) {
	r := newCheckRig(t, helloHandler(goodHello()), true)
	if err := r.st.UpdatePeer(context.Background(), "b-id", func(p *Peer) error {
		p.URL = "http://127.0.0.1:1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	view, err := r.svc.CheckNow(context.Background(), "b-id")
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "unreachable" || view.LastError == "" {
		t.Errorf("view = %+v", view)
	}
	if p, _ := r.st.Peer(context.Background(), "b-id"); p.State != StateActive {
		t.Errorf("state = %s", p.State)
	}
}

func TestIncompatibleVersion(t *testing.T) {
	h := goodHello()
	h.Version = "v9.0.0"
	r := newCheckRig(t, helloHandler(h), true)
	view, err := r.svc.CheckNow(context.Background(), "b-id")
	if err != nil {
		t.Fatal(err)
	}
	const want = "mini-02 runs v9.0, this server v1.5. Update to match"
	if view.Status != "incompatible" || view.Compatible || view.LastError != want {
		t.Errorf("view = %+v", view)
	}
	_, err = r.svc.Usable("b-id")
	assertPeerCode(t, err, CodeVersion)
	if pe, ok := err.(*PeerError); ok && pe.Msg != want {
		t.Errorf("Usable message = %q, want %q", pe.Msg, want)
	}
}

func TestUsableUnknownAndPending(t *testing.T) {
	r := newCheckRig(t, helloHandler(goodHello()), true)
	_, err := r.svc.Usable("nope")
	assertPeerCode(t, err, CodeUnknown)
	if _, err := r.svc.Usable("b-id"); err != nil {
		t.Errorf("fresh active peer: %v", err)
	}
}

func TestCheckNowUnknownPeer(t *testing.T) {
	r := newCheckRig(t, helloHandler(goodHello()), false)
	if _, err := r.svc.CheckNow(context.Background(), "nope"); err != ErrPeerNotFound {
		t.Errorf("err = %v, want ErrPeerNotFound", err)
	}
}

func assertPeerCode(t *testing.T, err error, code string) {
	t.Helper()
	pe, ok := err.(*PeerError)
	if !ok || pe.Code != code {
		t.Fatalf("err = %v, want PeerError %s", err, code)
	}
}
