package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/engine"
)

type fakeNetworkEngine struct {
	mu                                  sync.Mutex
	list                                []engine.Network
	inspect                             map[string]engine.Network
	err                                 error
	inspectErr                          map[string]error
	calls, active, maxActive, inspected int
	gate                                <-chan struct{}
	entered                             chan struct{}
}

func (f *fakeNetworkEngine) Networks(context.Context) ([]engine.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.list, f.err
}
func (f *fakeNetworkEngine) InspectNetwork(ctx context.Context, id string) (engine.Network, error) {
	f.mu.Lock()
	f.active++
	f.inspected++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.active--; f.mu.Unlock() }()
	if f.entered != nil {
		select {
		case f.entered <- struct{}{}:
		case <-ctx.Done():
			return engine.Network{}, ctx.Err()
		}
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return engine.Network{}, ctx.Err()
		}
	}
	return f.inspect[id], f.inspectErr[id]
}
func (*fakeNetworkEngine) CreateNetwork(context.Context, engine.NetworkSpec) (string, error) {
	panic("unexpected create")
}
func (*fakeNetworkEngine) RemoveNetwork(context.Context, string) error { panic("unexpected remove") }
func lanNetworkEngine() *fakeNetworkEngine {
	return &fakeNetworkEngine{list: []engine.Network{{ID: "n1", Name: "lan", Driver: "bridge", Subnets: []engine.Subnet{{Prefix: netip.MustParsePrefix("10.0.0.0/24")}}}}}
}
func networksEnv(t *testing.T, f *fakeNetworkEngine, tweak func(*api.Options)) (*testEnv, func(string) *httpResult) {
	t.Helper()
	return dashboardEnv(t, func(o *api.Options) {
		o.Stacks = newFakeStacks()
		o.NetworkEngine = f
		if tweak != nil {
			tweak(o)
		}
	})
}
func networkRows(t *testing.T, r *httpResult) []apitypes.Network {
	t.Helper()
	var rows []apitypes.Network
	if r.Code != 200 {
		t.Fatalf("status %d: %s", r.Code, r.Body)
	}
	if err := json.Unmarshal(r.Body, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestNetworkListJoinsSources(t *testing.T) {
	f := &fakeNetworkEngine{list: []engine.Network{
		{ID: "ccccccccccccmore", Name: "lan", Driver: "macvlan", Scope: "local", Options: map[string]string{"parent": "eth0"}},
		{ID: "aaaaaaaaaaaamore", Name: "bridge", Driver: "bridge", Scope: "local"},
		{ID: "bbbbbbbbbbbbmore", Name: "dns_default", Driver: "bridge", Scope: "local", Labels: map[string]string{"com.docker.compose.project": "dns-project"}, Subnets: []engine.Subnet{{Prefix: netip.MustParsePrefix("10.0.0.0/24"), Gateway: netip.MustParseAddr("10.0.0.1")}}},
	}, inspect: map[string]engine.Network{
		"bbbbbbbbbbbbmore": {Containers: []engine.NetworkEndpoint{{Name: "z", IPv4: "10.0.0.3/24"}, {Name: "a", IPv4: "10.0.0.2/24"}}},
		"ccccccccccccmore": {Containers: []engine.NetworkEndpoint{{Name: "router", IPv4: "192.168.1.2/24"}}},
	}}
	_, get := networksEnv(t, f, func(o *api.Options) {
		st := newFakeStacks()
		st.summary.Name = "dns"
		st.summary.Project = "dns-project"
		o.Stacks = st
	})
	r := get("/api/networks")
	want := `[{"id":"aaaaaaaaaaaa","name":"bridge","driver":"bridge","scope":"local","subnets":[],"gateways":[],"ipRange":"","parent":"","ipvlanMode":"","internal":false,"builtin":true,"project":"","stack":"","containers":[]},{"id":"bbbbbbbbbbbb","name":"dns_default","driver":"bridge","scope":"local","subnets":["10.0.0.0/24"],"gateways":["10.0.0.1"],"ipRange":"","parent":"","ipvlanMode":"","internal":false,"builtin":false,"project":"dns-project","stack":"dns","containers":[{"name":"a","ipv4":"10.0.0.2/24"},{"name":"z","ipv4":"10.0.0.3/24"}]},{"id":"cccccccccccc","name":"lan","driver":"macvlan","scope":"local","subnets":[],"gateways":[],"ipRange":"","parent":"eth0","ipvlanMode":"","internal":false,"builtin":false,"project":"","stack":"","containers":[{"name":"router","ipv4":"192.168.1.2/24"}]}]`
	if r.Code != 200 || strings.TrimSpace(string(r.Body)) != want {
		t.Fatalf("%d %s\nwant %s", r.Code, r.Body, want)
	}
}
func TestNetworkListExistingFieldsUnchanged(t *testing.T) {
	_, get := networksEnv(t, lanNetworkEngine(), nil)
	r := get("/api/networks")
	var old []struct {
		Name, Driver string
		Subnets      []string
	}
	if err := json.Unmarshal(r.Body, &old); err != nil || len(old) != 1 || old[0].Name != "lan" || old[0].Driver != "bridge" || len(old[0].Subnets) != 1 || old[0].Subnets[0] != "10.0.0.0/24" {
		t.Fatalf("old fields: %s %v", r.Body, err)
	}
}
func TestNetworkListSingleFlight50(t *testing.T) {
	gate := make(chan struct{})
	f := lanNetworkEngine()
	f.gate = gate
	f.entered = make(chan struct{}, 50)
	env, _ := networksEnv(t, f, nil)
	cookie := env.login(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 50 {
		wg.Go(func() {
			<-start
			r := env.do(t, "GET", "/api/networks", "", withCookie(cookie))
			if r.Code != 200 {
				t.Errorf("%d %s", r.Code, r.Body)
			}
		})
	}
	close(start)
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		close(gate)
		t.Fatal("inspect never started")
	}
	close(gate)
	wg.Wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls != 1 || f.inspected != 1 {
		t.Fatalf("calls=%d inspect=%d", f.calls, f.inspected)
	}
}
func TestNetworkListCachedFor2s(t *testing.T) {
	now := summaryNow
	f := lanNetworkEngine()
	_, get := networksEnv(t, f, func(o *api.Options) { o.Now = func() time.Time { return now } })
	get("/api/networks")
	now = now.Add(2*time.Second - time.Nanosecond)
	get("/api/networks")
	if f.calls != 1 {
		t.Fatal(f.calls)
	}
	now = now.Add(time.Nanosecond)
	get("/api/networks")
	if f.calls != 2 {
		t.Fatal(f.calls)
	}
}
func TestNetworkListInspectConcurrencyBounded(t *testing.T) {
	gate := make(chan struct{})
	f := &fakeNetworkEngine{gate: gate, entered: make(chan struct{}, 12)}
	for i := range 12 {
		f.list = append(f.list, engine.Network{ID: fmt.Sprint(i), Name: fmt.Sprint(i)})
	}
	_, get := networksEnv(t, f, nil)
	done := make(chan *httpResult, 1)
	go func() { done <- get("/api/networks") }()
	for range 4 {
		select {
		case <-f.entered:
		case <-time.After(time.Second):
			t.Fatal("inspects never reached four")
		}
	}
	f.mu.Lock()
	max := f.maxActive
	f.mu.Unlock()
	close(gate)
	rows := networkRows(t, <-done)
	if max > 4 || f.maxActive > 4 || len(rows) != 12 || f.inspected != 12 {
		t.Fatalf("max=%d rows=%d inspected=%d", f.maxActive, len(rows), f.inspected)
	}
}
func TestNetworkListDropsNetworkRemovedDuringInspect(t *testing.T) {
	f := lanNetworkEngine()
	f.inspectErr = map[string]error{"n1": engine.ErrNetworkNotFound}
	_, get := networksEnv(t, f, nil)
	r := get("/api/networks")
	if r.Code != 200 || strings.TrimSpace(string(r.Body)) != "[]" {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
}
func TestNetworkListUnavailableIs503(t *testing.T) {
	for _, inspect := range []bool{false, true} {
		t.Run(fmt.Sprint(inspect), func(t *testing.T) {
			f := lanNetworkEngine()
			if inspect {
				f.inspectErr = map[string]error{"n1": engine.ErrUnavailable}
			} else {
				f.err = engine.ErrUnavailable
			}
			_, get := networksEnv(t, f, nil)
			r := get("/api/networks")
			if r.Code != 503 || !strings.Contains(string(r.Body), "docker engine is unavailable") {
				t.Fatalf("%d %s", r.Code, r.Body)
			}
		})
	}
}
func TestNetworkListErrorsNotCached(t *testing.T) {
	f := lanNetworkEngine()
	f.inspectErr = map[string]error{"n1": errors.New("inspect failed")}
	_, get := networksEnv(t, f, nil)
	if r := get("/api/networks"); r.Code != 500 {
		t.Fatal(r.Code)
	}
	f.inspectErr = nil
	networkRows(t, get("/api/networks"))
	if f.calls != 2 {
		t.Fatal(f.calls)
	}
}
func TestNetworkListDeadlineIs503(t *testing.T) {
	f := lanNetworkEngine()
	f.err = context.DeadlineExceeded
	_, get := networksEnv(t, f, nil)
	r := get("/api/networks")
	if r.Code != 503 || !strings.Contains(string(r.Body), "Docker did not answer in time") {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
}
func TestNetworkListStackFailureIs500(t *testing.T) {
	for _, err := range []error{errors.New("store failed"), engine.ErrUnavailable, context.DeadlineExceeded} {
		t.Run(err.Error(), func(t *testing.T) {
			_, get := networksEnv(t, lanNetworkEngine(), func(o *api.Options) { st := newFakeStacks(); st.err = err; o.Stacks = st })
			if r := get("/api/networks"); r.Code != 500 {
				t.Fatalf("%d %s", r.Code, r.Body)
			}
		})
	}
}

func TestNetworkListRequiresSession(t *testing.T) {
	env, _ := networksEnv(t, lanNetworkEngine(), nil)
	r := env.do(t, "GET", "/api/networks", "")
	if r.Code != http.StatusUnauthorized {
		t.Fatal(r.Code)
	}
}
func TestNetworkRoutesAbsentWithoutEngine(t *testing.T) {
	_, get := networksEnv(t, lanNetworkEngine(), func(o *api.Options) { o.NetworkEngine = nil })
	r := get("/api/networks")
	if r.Code != 404 || strings.TrimSpace(string(r.Body)) != `{"error":"not found"}` {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
}
func TestNetworkRoutesAbsentWithoutStacks(t *testing.T) {
	_, get := networksEnv(t, lanNetworkEngine(), func(o *api.Options) { o.Stacks = nil })
	if r := get("/api/networks"); r.Code != 404 {
		t.Fatal(r.Code)
	}
}
