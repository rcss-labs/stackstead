package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
)

// bodyRecorder keeps what a handler wrote so the test can scan it for secrets.
type bodyRecorder struct {
	http.ResponseWriter
	mu     *sync.Mutex
	into   *bytes.Buffer
	status int
	// allowed is true for the two responses that carry a secret by design.
	allowed func(status int) bool
}

func (r *bodyRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *bodyRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if !r.allowed(r.status) {
		r.mu.Lock()
		r.into.Write(p)
		r.mu.Unlock()
	}
	return r.ResponseWriter.Write(p)
}
func (r *bodyRecorder) Flush()                      { r.ResponseWriter.(http.Flusher).Flush() }
func (r *bodyRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// integrationNode is a peerNode with stacks, a captured log and recorded
// browser-facing response bodies, whose peer checker is running.
type integrationNode struct {
	*peerNode
	logs   *syncBuffer
	mu     sync.Mutex
	bodies bytes.Buffer
}

func newIntegrationNode(t *testing.T, name string, stackNames ...string) *integrationNode {
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
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st := peers.NewStore(db, time.Now)
	svc := peers.NewService(peers.ServiceConfig{
		Store: st, Version: "9.9.9", Logger: logger, Timeout: 3 * time.Second,
		Hostname: func() (string, error) { return name, nil },
	})
	var list []stacks.Summary
	for _, n := range stackNames {
		list = append(list, stacks.Summary{Name: n, Path: "/stacks/" + n, Project: n, Origin: "managed", Status: "running", Services: 1, Running: 1})
	}
	handler := api.New(api.Options{
		Auth: authSvc, Limiter: auth.NewLoginLimiter(5, time.Minute, time.Now), Peers: svc,
		Stacks: fleetStacks{list: list}, Operations: fleetOperations{}, Observe: newFakeObserve(),
		Static: fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}, Logger: logger,
		SSEWriteTimeout: time.Second,
	})
	n := &integrationNode{logs: logs}
	recorded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only a created connection token (201) and a successful pairing
		// response (200) carry a secret by design; everything else, error
		// bodies included, must be clean.
		allowed := func(status int) bool {
			return r.Method == "POST" && (r.URL.Path == "/api/peers/tokens" && status == http.StatusCreated ||
				r.URL.Path == "/api/peer/pair" && status == http.StatusOK)
		}
		handler.ServeHTTP(&bodyRecorder{ResponseWriter: w, mu: &n.mu, into: &n.bodies, allowed: allowed}, r)
	})
	n.peerNode = &peerNode{t: t, name: name, svc: svc, store: st}
	n.srv = httptest.NewServer(recorded)
	t.Cleanup(n.srv.Close)
	n.login()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return n
}

func (n *integrationNode) recordedBodies() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.bodies.String()
}

func TestConnectedServersInProcess(t *testing.T) {
	a := newIntegrationNode(t, "mini-a", "web")
	b := newIntegrationNode(t, "mini-b", "db")
	token := b.token()
	status, body := a.do("POST", "/api/peers", addBody(b.url(), token, a.url(), true))
	if status != 201 {
		t.Fatalf("pair = %d %s", status, body)
	}

	if got := a.list(); len(got) != 1 || got[0].Name != "mini-b" {
		t.Fatalf("A lists %+v", got)
	}
	if got := b.list(); len(got) != 1 || got[0].Name != "mini-a" {
		t.Fatalf("B lists %+v", got)
	}
	bID := a.list()[0].ID

	status, body = a.do("GET", "/api/servers/"+bID+"/api/stacks", "")
	if status != 200 || !strings.Contains(body, `"db"`) {
		t.Fatalf("forwarded stacks = %d %s", status, body)
	}

	req, err := http.NewRequest("GET", a.url()+"/api/servers/"+bID+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(a.cookie)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("forwarded events = %d", resp.StatusCode)
	}
	sawStatus := false
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if sc.Text() == "event: status" {
			sawStatus = true
			break
		}
	}
	resp.Body.Close()
	if !sawStatus {
		t.Fatal("forwarded event stream never delivered a status event")
	}

	status, body = a.do("GET", "/api/fleet/stacks", "")
	if status != 200 {
		t.Fatalf("fleet = %d %s", status, body)
	}
	var fleet apitypes.FleetStacks
	if err := json.Unmarshal([]byte(body), &fleet); err != nil {
		t.Fatal(err)
	}
	if got := stackNamesOf(fleet.Stacks); len(got) != 2 || got["web"] == got["db"] {
		t.Errorf("fleet stacks = %v", got)
	}

	// B forgets A without telling it (as when A was offline during the removal).
	if err := b.store.DeletePeer(context.Background(), b.list()[0].ID); err != nil {
		t.Fatal(err)
	}
	view, err := a.svc.CheckNow(context.Background(), bID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "removed" {
		t.Errorf("A sees B as %q after B removed A, want removed", view.Status)
	}

	for _, n := range []*integrationNode{a, b} {
		for label, text := range map[string]string{"log": n.logs.String(), "response bodies": n.recordedBodies()} {
			for _, prefix := range []string{"ssc_", "ssp_"} {
				if strings.Contains(text, prefix) {
					t.Errorf("%s %s contains a %s secret", n.name, label, prefix)
				}
			}
		}
	}
}
