package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/peers"
	"github.com/rcss-labs/stackstead/internal/stacks"
	"github.com/rcss-labs/stackstead/internal/store"
)

// fleetStacks serves a fixed stack list; every other StacksAPI method would
// panic, which no fleet test reaches.
type fleetStacks struct {
	api.StacksAPI
	list []stacks.Summary
}

func (f fleetStacks) List(context.Context) ([]stacks.Summary, error) { return f.list, nil }

// fleetOperations only has to exist so the stack routes are registered.
type fleetOperations struct{ api.OperationsAPI }

// fleetObserve adds recent events to the standard fake.
type fleetObserve struct {
	*fakeObserve
	recent []engine.Event
}

func (f fleetObserve) RecentEvents() []engine.Event { return f.recent }

// fleetNode is a peerNode that also serves stacks and recent events.
type fleetNode struct {
	*peerNode
	requests   atomic.Int32 // requests that carried a peer credential
	gotPeer    atomic.Value // last X-Stackstead-Peer value
	gotUser    atomic.Value // last X-Stackstead-Peer-User value
	stall      atomic.Bool  // makes /api/stacks hang until the client leaves
	instanceID string
}

func newFleetNode(t *testing.T, name, version string, stackNames []string, eventNames []string, eventTimes []time.Time) *fleetNode {
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
		Store: st, Version: version, Logger: slog.New(slog.DiscardHandler), Timeout: 3 * time.Second,
		Hostname: func() (string, error) { return name, nil },
	})
	var list []stacks.Summary
	for _, n := range stackNames {
		list = append(list, stacks.Summary{Name: n, Path: "/stacks/" + n, Project: n, Origin: "managed", Status: "running", Services: 1, Running: 1})
	}
	var recent []engine.Event
	for i, n := range eventNames {
		recent = append(recent, engine.Event{Type: "container", Action: "start", Name: n, Time: eventTimes[i],
			Labels: map[string]string{"com.docker.compose.project": n}})
	}
	handler := api.New(api.Options{
		Auth: authSvc, Limiter: auth.NewLoginLimiter(5, time.Minute, time.Now), Peers: svc,
		Stacks: fleetStacks{list: list}, Operations: fleetOperations{}, Observe: fleetObserve{newFakeObserve(), recent},
		Static: fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}, Logger: slog.New(slog.DiscardHandler),
	})
	f := &fleetNode{}
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c := r.Header.Get(peers.PeerHeader); c != "" && r.URL.Path != "/api/peer/hello" {
			f.requests.Add(1)
			f.gotPeer.Store(c)
			f.gotUser.Store(r.Header.Get("X-Stackstead-Peer-User"))
			if f.stall.Load() && r.URL.Path == "/api/stacks" {
				select {
				case <-r.Context().Done():
				case <-time.After(10 * time.Second):
				}
				return
			}
		}
		handler.ServeHTTP(w, r)
	})
	n := &peerNode{t: t, name: name, svc: svc, store: st}
	n.srv = httptest.NewServer(wrapped)
	t.Cleanup(n.srv.Close)
	f.peerNode = n
	id, err := st.Identity(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	f.instanceID = id.InstanceID
	n.login()
	return f
}

func (f *fleetNode) fleet() (int, apitypes.FleetStacks, string) {
	f.t.Helper()
	status, body := f.do("GET", "/api/fleet/stacks", "")
	var out apitypes.FleetStacks
	if status == 200 {
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			f.t.Fatalf("body %q: %v", body, err)
		}
	}
	return status, out, body
}

var epoch = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func stackNamesOf(fs []apitypes.FleetStack) map[string]string {
	m := map[string]string{}
	for _, s := range fs {
		m[s.Name] = s.Server
	}
	return m
}

func TestFleetStacksMergesServers(t *testing.T) {
	a := newFleetNode(t, "mini-a", "9.9.9", []string{"web"}, []string{"web", "web2"}, []time.Time{epoch.Add(time.Second), epoch.Add(3 * time.Second)})
	b := newFleetNode(t, "mini-b", "9.9.9", []string{"pihole"}, []string{"pihole"}, []time.Time{epoch.Add(2 * time.Second)})
	pair(t, a.peerNode, b.peerNode)

	status, got, body := a.fleet()
	if status != 200 {
		t.Fatalf("fleet = %d %s", status, body)
	}
	if len(got.Servers) != 2 {
		t.Fatalf("servers = %+v", got.Servers)
	}
	local, remote := got.Servers[0], got.Servers[1]
	if !local.Local || local.ID != a.instanceID || local.Status != "reachable" || local.Version != "9.9.9" || local.Name != "mini-a" {
		t.Errorf("local = %+v", local)
	}
	if remote.Local || remote.ID != b.instanceID || remote.Status != "reachable" || remote.Error != "" || remote.Version != "9.9.9" {
		t.Errorf("remote = %+v", remote)
	}
	want := map[string]string{"web": a.instanceID, "pihole": b.instanceID}
	if names := stackNamesOf(got.Stacks); len(names) != 2 || names["web"] != want["web"] || names["pihole"] != want["pihole"] {
		t.Errorf("stacks = %+v", got.Stacks)
	}
	var order []string
	for _, e := range got.Events {
		order = append(order, e.Container+"@"+e.Server)
	}
	wantOrder := []string{"web2@" + a.instanceID, "pihole@" + b.instanceID, "web@" + a.instanceID}
	if len(order) != 3 || order[0] != wantOrder[0] || order[1] != wantOrder[1] || order[2] != wantOrder[2] {
		t.Errorf("events (newest first) = %v, want %v", order, wantOrder)
	}
	if got.Events[0].Project != "web2" || got.Events[0].Action != "start" {
		t.Errorf("event DTO fields not flattened: %+v", got.Events[0])
	}
	if cred, _ := b.gotPeer.Load().(string); cred == "" {
		t.Error("peer call carried no X-Stackstead-Peer credential")
	}
	if u, _ := b.gotUser.Load().(string); u != "admin" {
		t.Errorf("X-Stackstead-Peer-User = %q, want admin", u)
	}
	if cred, _ := b.gotPeer.Load().(string); cred != "" && strings.Contains(body, cred) {
		t.Error("response body leaks the peer credential")
	}
}

func TestFleetStacksWithPeerDown(t *testing.T) {
	a := newFleetNode(t, "mini-a", "9.9.9", []string{"web"}, nil, nil)
	b := newFleetNode(t, "mini-b", "9.9.9", []string{"pihole"}, nil, nil)
	pair(t, a.peerNode, b.peerNode)
	b.srv.Close()

	begun := time.Now()
	status, got, body := a.fleet()
	if status != 200 {
		t.Fatalf("fleet = %d %s", status, body)
	}
	if took := time.Since(begun); took > 3500*time.Millisecond {
		t.Errorf("took %v", took)
	}
	if len(got.Servers) != 2 || got.Servers[1].ID != b.instanceID || got.Servers[1].Status != "unreachable" || got.Servers[1].Error == "" {
		t.Errorf("servers = %+v", got.Servers)
	}
	if names := stackNamesOf(got.Stacks); len(names) != 1 || names["web"] != a.instanceID {
		t.Errorf("stacks = %+v", got.Stacks)
	}
}

func TestFleetStacksWithSlowPeer(t *testing.T) {
	a := newFleetNode(t, "mini-a", "9.9.9", []string{"web"}, nil, nil)
	b := newFleetNode(t, "mini-b", "9.9.9", []string{"pihole"}, nil, nil)
	pair(t, a.peerNode, b.peerNode)
	b.stall.Store(true)

	begun := time.Now()
	status, got, body := a.fleet()
	if status != 200 {
		t.Fatalf("fleet = %d %s", status, body)
	}
	if took := time.Since(begun); took > 3500*time.Millisecond {
		t.Errorf("took %v, want about the 3 s per-peer timeout", took)
	}
	if got.Servers[1].Status != "unreachable" || got.Servers[1].Error == "" || len(got.Stacks) != 1 {
		t.Errorf("got %+v", got)
	}
}

func TestFleetStacksSkipsIncompatiblePeer(t *testing.T) {
	a := newFleetNode(t, "mini-a", "9.9.9", []string{"web"}, nil, nil)
	b := newFleetNode(t, "mini-b", "1.0.0", []string{"pihole"}, nil, nil)
	pair(t, a.peerNode, b.peerNode)
	if _, err := a.svc.CheckNow(context.Background(), b.instanceID); err != nil {
		t.Fatal(err)
	}
	before := b.requests.Load()

	status, got, body := a.fleet()
	if status != 200 {
		t.Fatalf("fleet = %d %s", status, body)
	}
	if got.Servers[1].Status != "incompatible" || got.Servers[1].Error == "" || got.Servers[1].Version != "1.0.0" {
		t.Errorf("server = %+v", got.Servers[1])
	}
	if len(got.Stacks) != 1 {
		t.Errorf("stacks = %+v", got.Stacks)
	}
	if after := b.requests.Load(); after != before {
		t.Errorf("%d requests were sent to the incompatible peer", after-before)
	}
}

func TestFleetWithoutPeers(t *testing.T) {
	a := newFleetNode(t, "mini-a", "9.9.9", []string{"web"}, []string{"web"}, []time.Time{epoch})
	status, got, body := a.fleet()
	if status != 200 {
		t.Fatalf("fleet = %d %s", status, body)
	}
	if len(got.Servers) != 1 || !got.Servers[0].Local || len(got.Stacks) != 1 || len(got.Events) != 1 || got.Events[0].Server != a.instanceID {
		t.Errorf("got %s", body)
	}
}

func TestFleetRequiresSession(t *testing.T) {
	a := newFleetNode(t, "mini-a", "9.9.9", nil, nil, nil)
	resp, err := http.Get(a.url() + "/api/fleet/stacks")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestRecentEventsRoute(t *testing.T) {
	a := newFleetNode(t, "mini-a", "9.9.9", nil, []string{"web", "db"}, []time.Time{epoch.Add(2 * time.Second), epoch})
	status, body := a.do("GET", "/api/events/recent", "")
	var got []apitypes.EventMessage
	if status != 200 || json.Unmarshal([]byte(body), &got) != nil || len(got) != 2 || got[0].Container != "web" || got[0].Project != "web" {
		t.Errorf("recent = %d %s", status, body)
	}
}
