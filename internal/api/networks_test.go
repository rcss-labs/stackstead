package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"reflect"
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
	createErr, removeErr                error
	created                             []engine.NetworkSpec
	removed                             []string
	deadline                            time.Duration
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
func (f *fakeNetworkEngine) CreateNetwork(ctx context.Context, spec engine.NetworkSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, spec)
	if d, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(d)
	}
	if f.createErr != nil {
		return "", f.createErr
	}
	n := engine.Network{ID: strings.Repeat("a", 64), Name: spec.Name, Driver: spec.Driver, Scope: "local", Internal: spec.Internal, Options: map[string]string{"parent": spec.Parent, "ipvlan_mode": spec.IPvlanMode}}
	if spec.Subnet != "" {
		sub := engine.Subnet{Prefix: netip.MustParsePrefix(spec.Subnet)}
		if spec.Gateway != "" {
			sub.Gateway = netip.MustParseAddr(spec.Gateway)
		}
		if spec.IPRange != "" {
			sub.IPRange = netip.MustParsePrefix(spec.IPRange)
		}
		n.Subnets = []engine.Subnet{sub}
	}
	f.list = append(f.list, n)
	if f.inspect == nil {
		f.inspect = map[string]engine.Network{}
	}
	f.inspect[n.ID] = n
	return n.ID, nil
}
func (f *fakeNetworkEngine) RemoveNetwork(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	if f.removeErr != nil {
		return f.removeErr
	}
	for i, n := range f.list {
		if n.ID == id {
			f.list = append(f.list[:i], f.list[i+1:]...)
			delete(f.inspect, id)
			return nil
		}
	}
	return engine.ErrNetworkNotFound
}
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

const createLAN = `{"name":"new-lan","driver":"macvlan","subnet":"192.168.1.0/24","gateway":"192.168.1.1","ipRange":"192.168.1.128/25","parent":"eth0","internal":true}`

func TestNetworkCreateHappyPath(t *testing.T) {
	f := lanNetworkEngine()
	env, _ := networksEnv(t, f, nil)
	r := env.do(t, "POST", "/api/networks", createLAN, withCookie(env.login(t)))
	if r.Code != 201 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	var got apitypes.Network
	if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := apitypes.Network{ID: strings.Repeat("a", 12), Name: "new-lan", Driver: "macvlan", Scope: "local", Subnets: []string{"192.168.1.0/24"}, Gateways: []string{"192.168.1.1"}, IPRange: "192.168.1.128/25", Parent: "eth0", Internal: true, Containers: []apitypes.NetworkContainer{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v want %+v", got, want)
	}
	spec := engine.NetworkSpec{Name: "new-lan", Driver: "macvlan", Subnet: "192.168.1.0/24", Gateway: "192.168.1.1", IPRange: "192.168.1.128/25", Parent: "eth0", Internal: true}
	if !reflect.DeepEqual(f.created, []engine.NetworkSpec{spec}) || f.deadline <= 0 || f.deadline > 30*time.Second {
		t.Fatalf("created=%+v deadline=%v", f.created, f.deadline)
	}
}
func TestNetworkCreateFieldErrorsCallNoDocker(t *testing.T) {
	for _, body := range []string{`{"name":"-x","driver":"bridge","subnet":"10.0.0.0/24","parent":"eth0"}`, `{"name":"bad","driver":"macvlan","subnet":"192.168.1.0/24","gateway":"192.168.2.1","ipRange":"192.168.0.0/16","ipvlanMode":"l9"}`} {
		f := lanNetworkEngine()
		env, _ := networksEnv(t, f, nil)
		r := env.do(t, "POST", "/api/networks", body, withCookie(env.login(t)))
		var got apitypes.ErrorBody
		_ = json.Unmarshal(r.Body.Bytes(), &got)
		if r.Code != 400 || len(got.Fields) < 3 || got.Error == "" || len(f.created) != 0 {
			t.Fatalf("%d %s creates=%v", r.Code, r.Body, f.created)
		}
	}
}
func TestNetworkCreateExistingNameIs409(t *testing.T) {
	f := lanNetworkEngine()
	env, _ := networksEnv(t, f, nil)
	c := env.login(t)
	for _, tc := range []struct {
		body    string
		code    int
		message string
	}{
		{`{"name":"lan","driver":"bridge","subnet":"10.0.0.0/24"}`, 409, "network lan already exists"},
		{createLAN, 201, ""}, {createLAN, 409, "network new-lan already exists"},
		{`{"name":"lan","driver":"bridge","parent":"eth0"}`, 400, ""},
	} {
		r := env.do(t, "POST", "/api/networks", tc.body, withCookie(c))
		if r.Code != tc.code {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
		if tc.message != "" {
			var got apitypes.ErrorBody
			if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Error != tc.message {
				t.Fatalf("message %q want %q", got.Error, tc.message)
			}
		}
	}

	if len(f.created) != 1 {
		t.Fatal(f.created)
	}
}
func TestNetworkCreateDockerErrorsPassThrough(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
		msg  string
	}{{&engine.ActionError{Status: 409, Message: "exact already exists"}, 409, "exact already exists"}, {&engine.ActionError{Status: 400, Message: "failed to create: no such device"}, 400, "failed to create: no such device"}, {engine.ErrUnavailable, 503, "docker engine is unavailable"}} {
		t.Run(tc.msg, func(t *testing.T) {
			f := lanNetworkEngine()
			f.createErr = tc.err
			env, _ := networksEnv(t, f, nil)
			r := env.do(t, "POST", "/api/networks", createLAN, withCookie(env.login(t)))
			var b apitypes.ErrorBody
			_ = json.Unmarshal(r.Body.Bytes(), &b)
			if r.Code != tc.code || b.Error != tc.msg {
				t.Fatalf("%d %s", r.Code, r.Body)
			}
		})
	}
}
func TestNetworkCreateRequiresJSON(t *testing.T) {
	f := lanNetworkEngine()
	env, _ := networksEnv(t, f, nil)
	r := env.do(t, "POST", "/api/networks", "name=x", withCookie(env.login(t)), withHeader("Content-Type", "application/x-www-form-urlencoded"))
	if r.Code != 415 || len(f.created) != 0 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
}
func TestNetworkCreateRejectsUnknownFields(t *testing.T) {
	f := lanNetworkEngine()
	env, _ := networksEnv(t, f, nil)
	r := env.do(t, "POST", "/api/networks", `{"name":"x","driver":"bridge","unknown":true}`, withCookie(env.login(t)))
	if r.Code != 400 || f.calls != 0 || len(f.created) != 0 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
}
func TestNetworkListFreshAfterCreateAndDelete(t *testing.T) {
	f := lanNetworkEngine()
	env, get := networksEnv(t, f, nil)
	c := env.login(t)
	if len(networkRows(t, get("/api/networks"))) != 1 {
		t.Fatal("initial")
	}
	if r := env.do(t, "POST", "/api/networks", createLAN, withCookie(c)); r.Code != 201 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if len(networkRows(t, get("/api/networks"))) != 2 {
		t.Fatal("stale after create")
	}
	if r := env.do(t, "DELETE", "/api/networks/new-lan", "", withCookie(c)); r.Code != 204 || r.Body.Len() != 0 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if len(networkRows(t, get("/api/networks"))) != 1 {
		t.Fatal("stale after delete")
	}
}
func TestNetworkDeleteRefusals(t *testing.T) {
	for _, tc := range []struct {
		n   engine.Network
		msg string
	}{{engine.Network{Name: "bridge", Driver: "bridge"}, "bridge is a built-in network"}, {engine.Network{Name: "dns_default", Labels: map[string]string{"com.docker.compose.project": "dns-project"}}, "dns_default belongs to stack dns; remove it with the stack"}, {engine.Network{Name: "lan", Containers: []engine.NetworkEndpoint{{Name: "z"}, {Name: "c"}, {Name: "b"}, {Name: "a"}}}, "lan is in use by a, b, c (+1 more)"}} {
		t.Run(tc.n.Name, func(t *testing.T) {
			n := tc.n
			n.ID = strings.Repeat("b", 64)
			f := &fakeNetworkEngine{list: []engine.Network{{ID: n.ID, Name: n.Name}}, inspect: map[string]engine.Network{n.ID: n}}
			env, _ := networksEnv(t, f, func(o *api.Options) {
				st := newFakeStacks()
				st.summary.Name = "dns"
				st.summary.Project = "dns-project"
				o.Stacks = st
			})
			r := env.do(t, "DELETE", "/api/networks/"+n.Name, "", withCookie(env.login(t)))
			var b apitypes.ErrorBody
			_ = json.Unmarshal(r.Body.Bytes(), &b)
			if r.Code != 409 || b.Error != tc.msg || len(f.removed) > 0 {
				t.Fatalf("%d %s removes=%v", r.Code, r.Body, f.removed)
			}
		})
	}
}
func TestNetworkDeleteReadsOwnershipFresh(t *testing.T) {
	n := engine.Network{ID: strings.Repeat("b", 64), Name: "dns_default", Labels: map[string]string{"com.docker.compose.project": "new-project"}}
	f := &fakeNetworkEngine{list: []engine.Network{n}, inspect: map[string]engine.Network{n.ID: n}}
	st := newFakeStacks()
	env, _ := networksEnv(t, f, func(o *api.Options) { o.Stacks = st })
	c := env.login(t)
	if r := env.do(t, "DELETE", "/api/networks/dns_default", "", withCookie(c)); r.Code != 204 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	f.list = []engine.Network{n}
	f.inspect[n.ID] = n
	st.summary.Project = "new-project"
	st.summary.Name = "dns"
	if r := env.do(t, "DELETE", "/api/networks/dns_default", "", withCookie(c)); r.Code != 409 || !strings.Contains(r.Body.String(), "stack dns") {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if len(f.removed) != 1 {
		t.Fatal(f.removed)
	}
}
func TestNetworkDeleteByIdNameAndPrefix(t *testing.T) {
	id := strings.Repeat("b", 64)
	for _, ref := range []string{id, id[:12], "lan"} {
		t.Run(ref, func(t *testing.T) {
			n := engine.Network{ID: id, Name: "lan"}
			f := &fakeNetworkEngine{list: []engine.Network{n}, inspect: map[string]engine.Network{id: n}}
			env, _ := networksEnv(t, f, nil)
			r := env.do(t, "DELETE", "/api/networks/"+ref, "", withCookie(env.login(t)))
			if r.Code != 204 || !reflect.DeepEqual(f.removed, []string{id}) {
				t.Fatalf("%d %s removes=%v", r.Code, r.Body, f.removed)
			}
		})
	}
	f := &fakeNetworkEngine{list: []engine.Network{{ID: id, Name: "a"}, {ID: id[:63] + "c", Name: "b"}}}
	env, _ := networksEnv(t, f, nil)
	r := env.do(t, "DELETE", "/api/networks/"+id[:12], "", withCookie(env.login(t)))
	if r.Code != 404 || len(f.removed) > 0 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
}
func TestNetworkDeleteUnknownIs404(t *testing.T) {
	for _, ref := range []string{"unknown", "bbbbbbbbbbb", strings.Repeat("b", 65)} {
		f := lanNetworkEngine()
		env, _ := networksEnv(t, f, nil)
		r := env.do(t, "DELETE", "/api/networks/"+ref, "", withCookie(env.login(t)))
		if r.Code != 404 || len(f.removed) > 0 {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
	}
}
func TestNetworkDeleteDockerConflictIs409(t *testing.T) {
	for _, err := range []error{&engine.ActionError{Status: 409, Message: "has active endpoints"}, &engine.ActionError{Status: 403, Message: "has active endpoints"}} {
		f := lanNetworkEngine()
		f.inspect = map[string]engine.Network{"n1": f.list[0]}
		f.removeErr = err
		env, _ := networksEnv(t, f, nil)
		r := env.do(t, "DELETE", "/api/networks/lan", "", withCookie(env.login(t)))
		if r.Code != 409 || strings.TrimSpace(r.Body.String()) != `{"error":"has active endpoints"}` {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
	}
}
func TestNetworkMutationsRequireSession(t *testing.T) {
	f := lanNetworkEngine()
	env, _ := networksEnv(t, f, nil)
	for _, method := range []string{"POST", "DELETE"} {
		path := "/api/networks"
		body := createLAN
		if method == "DELETE" {
			path += "/lan"
			body = ""
		}
		r := env.do(t, method, path, body)
		if r.Code != 401 {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
	}
	if f.calls != 0 || len(f.created) > 0 || len(f.removed) > 0 {
		t.Fatal("unauthenticated engine call")
	}
}

func TestNetworkDeleteSuccessIs204AndInvalidates(t *testing.T) {
	f := lanNetworkEngine()
	f.inspect = map[string]engine.Network{"n1": f.list[0]}
	obs := containerObserve()
	env, get := containersEnv(t, obs, func(o *api.Options) { o.NetworkEngine = f })
	cookie := env.login(t)
	// Prime both caches while the fake clock stays fixed within their two second TTL.
	get("/api/networks")
	get("/api/containers")
	obs.mu.Lock()
	obs.containers = []engine.Container{{ID: idWeb, Name: "after-delete"}}
	obs.mu.Unlock()
	r := env.do(t, "DELETE", "/api/networks/lan", "", withCookie(cookie))
	if r.Code != 204 || r.Body.Len() != 0 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if rows := networkRows(t, get("/api/networks")); len(rows) != 0 {
		t.Fatalf("stale networks: %+v", rows)
	}
	r2 := get("/api/containers")
	if r2.Code != 200 || !strings.Contains(string(r2.Body), "after-delete") {
		t.Fatalf("stale containers: %d %s", r2.Code, r2.Body)
	}
}
func TestNetworkCreateInvalidatesContainerList(t *testing.T) {
	f := lanNetworkEngine()
	obs := containerObserve()
	env, get := containersEnv(t, obs, func(o *api.Options) { o.NetworkEngine = f })
	cookie := env.login(t)
	get("/api/containers")
	obs.mu.Lock()
	obs.containers = []engine.Container{{ID: idWeb, Name: "after-create"}}
	obs.mu.Unlock()
	if r := env.do(t, "POST", "/api/networks", createLAN, withCookie(cookie)); r.Code != 201 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	r := get("/api/containers")
	if r.Code != 200 || !strings.Contains(string(r.Body), "after-create") {
		t.Fatalf("stale containers: %d %s", r.Code, r.Body)
	}
}
func TestNetworkMutationFailures(t *testing.T) {
	for _, tc := range []struct {
		name, method, stage string
		err                 error
		code                int
	}{
		{"create list unavailable", "POST", "list", engine.ErrUnavailable, 503},
		{"create inspect missing", "POST", "inspect-created", engine.ErrNetworkNotFound, 404},
		{"create registry failure", "POST", "registry", errors.New("registry failure"), 500},
		{"delete list unavailable", "DELETE", "list", engine.ErrUnavailable, 503},
		{"delete inspect unavailable", "DELETE", "inspect", engine.ErrUnavailable, 503},
		{"delete inspect disappeared", "DELETE", "inspect", engine.ErrNetworkNotFound, 404},
		{"delete remove disappeared", "DELETE", "remove", fmt.Errorf("%w: %w", engine.ErrNetworkNotFound, &engine.ActionError{Status: 404, Message: "network lan not found"}), 404},
		{"delete remove unavailable", "DELETE", "remove", engine.ErrUnavailable, 503},
		{"delete registry failure", "DELETE", "registry", errors.New("registry failure"), 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := lanNetworkEngine()
			f.inspect = map[string]engine.Network{"n1": f.list[0]}
			st := newFakeStacks()
			switch tc.stage {
			case "list":
				f.err = tc.err
			case "inspect":
				f.inspectErr = map[string]error{"n1": tc.err}
			case "inspect-created":
				f.inspectErr = map[string]error{strings.Repeat("a", 64): tc.err}
			case "remove":
				f.removeErr = tc.err
			case "registry":
				st.err = tc.err
			}
			env, _ := networksEnv(t, f, func(o *api.Options) { o.Stacks = st })
			path := "/api/networks"
			body := createLAN
			if tc.method == "DELETE" {
				path += "/lan"
				body = ""
			}
			r := env.do(t, tc.method, path, body, withCookie(env.login(t)))
			if r.Code != tc.code {
				t.Fatalf("%d %s", r.Code, r.Body)
			}
			if tc.method == "DELETE" && tc.stage != "remove" && len(f.removed) > 0 {
				t.Fatalf("unexpected remove: %v", f.removed)
			}
			if tc.method == "POST" && tc.stage == "list" && len(f.created) > 0 {
				t.Fatal("create while unavailable")
			}
		})
	}
}
func TestNetworkDeleteRefusalOrder(t *testing.T) {
	n := engine.Network{ID: strings.Repeat("d", 64), Name: "bridge", Driver: "bridge", Labels: map[string]string{"com.docker.compose.project": "web"}, Containers: []engine.NetworkEndpoint{{Name: "web-1"}}}
	f := &fakeNetworkEngine{list: []engine.Network{n}, inspect: map[string]engine.Network{n.ID: n}}
	env, _ := networksEnv(t, f, nil)
	cookie := env.login(t)
	for _, want := range []string{"built-in network", "belongs to stack web"} {
		r := env.do(t, "DELETE", "/api/networks/bridge", "", withCookie(cookie))
		if r.Code != 409 || !strings.Contains(r.Body.String(), want) || len(f.removed) > 0 {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
		n.Driver = "macvlan"
		f.inspect[n.ID] = n
	}
}
