package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/metrics"
)

type fakeRestarts map[string]int

func (f fakeRestarts) RestartCounts() map[string]int { return f }

const (
	idWeb  = "aaaaaaaaaaaa1111111111111111111111111111111111111111111111111111"
	idDB   = "bbbbbbbbbbbb2222222222222222222222222222222222222222222222222222"
	idCron = "cccccccccccc3333333333333333333333333333333333333333333333333333"
)

func containerObserve() *fakeObserve {
	obs := newFakeObserve()
	created := time.Date(2026, 10, 1, 8, 0, 0, 0, time.FixedZone("EST", -5*3600))
	obs.containers = []engine.Container{
		{ID: idWeb, Name: "web-web-1", Image: "web:1", State: "running", Status: "Up 2 hours (unhealthy)", Created: created,
			Labels: map[string]string{"com.docker.compose.project": "web", "com.docker.compose.service": "web"},
			Ports: []engine.Port{{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
				{IP: "::", PrivatePort: 80, PublicPort: 8080, Type: "tcp"}},
			Networks: map[string]string{"web_default": "172.18.0.2"}},
		{ID: idDB, Name: "adhoc-db", Image: "postgres:16", State: "exited", Status: "Exited (137) 1 hour ago", Created: created},
		{ID: idCron, Name: "other-cron-1", Image: "cron:1", State: "running", Status: "Up 1 minute",
			Labels: map[string]string{"com.docker.compose.project": "other", "com.docker.compose.service": "cron"}},
	}
	obs.latest = []metrics.ExpositionSample{
		{Project: "web", Service: "web", Container: "web-web-1", CPU: 12.5, Mem: 1000, MemLimit: 4000},
		{Container: "other-cron-1", CPU: 0.5, Mem: 10, MemLimit: 20},
	}
	return obs
}

func containersEnv(t *testing.T, obs *fakeObserve, tweak func(*api.Options)) (*testEnv, func(string) *httpResult) {
	t.Helper()
	return dashboardEnv(t, func(o *api.Options) {
		fs := newFakeStacks()
		o.Stacks, o.Operations, o.Observe = fs, fakeOps{fs}, obs
		o.Restarts = fakeRestarts{idWeb: 3}
		o.Now = func() time.Time { return summaryNow }
		if tweak != nil {
			tweak(o)
		}
	})
}

func TestContainerListJoinsSources(t *testing.T) {
	_, get := containersEnv(t, containerObserve(), nil)
	res := get("/api/containers")
	if res.Code != 200 {
		t.Fatalf("status = %d, body %s", res.Code, res.Body)
	}
	want := `[
	 {"id":"bbbbbbbbbbbb","name":"adhoc-db","image":"postgres:16","state":"exited","status":"Exited (137) 1 hour ago","health":"none",
	  "exitCode":137,"createdAt":"2026-10-01T13:00:00Z","project":"","service":"","stack":"","managed":"none",
	  "ports":[],"networks":{},"cpu":null,"mem":null,"memLimit":null,"restarts24h":0},
	 {"id":"cccccccccccc","name":"other-cron-1","image":"cron:1","state":"running","status":"Up 1 minute","health":"none",
	  "exitCode":null,"createdAt":"0001-01-01T00:00:00Z","project":"other","service":"cron","stack":"","managed":"compose",
	  "ports":[],"networks":{},"cpu":0.5,"mem":10,"memLimit":20,"restarts24h":0},
	 {"id":"aaaaaaaaaaaa","name":"web-web-1","image":"web:1","state":"running","status":"Up 2 hours (unhealthy)","health":"unhealthy",
	  "exitCode":null,"createdAt":"2026-10-01T13:00:00Z","project":"web","service":"web","stack":"web","managed":"stack",
	  "ports":["0.0.0.0:8080->80/tcp"],"networks":{"web_default":"172.18.0.2"},"cpu":12.5,"mem":1000,"memLimit":4000,"restarts24h":3}
	]`
	var got, exp any
	if err := json.Unmarshal(res.Body, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &exp); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, exp) {
		t.Errorf("body = %s\nwant %s", res.Body, want)
	}
	var rows []apitypes.ContainerRow
	if err := json.Unmarshal(res.Body, &rows); err != nil || len(rows) != 3 || rows[0].Name != "adhoc-db" {
		t.Errorf("rows must decode and be sorted by name: %v %+v", err, rows)
	}
}

func TestContainerListNilRestartsSourceIsZeros(t *testing.T) {
	_, get := containersEnv(t, containerObserve(), func(o *api.Options) { o.Restarts = nil })
	var rows []apitypes.ContainerRow
	res := get("/api/containers")
	if err := json.Unmarshal(res.Body, &rows); err != nil || res.Code != 200 {
		t.Fatalf("%d %s %v", res.Code, res.Body, err)
	}
	for _, r := range rows {
		if r.Restarts24h != 0 {
			t.Errorf("%s restarts = %d, want 0", r.Name, r.Restarts24h)
		}
	}
}

// countingObserve counts state-cache reads and can block them.
type countingObserve struct {
	*fakeObserve
	mu      sync.Mutex
	calls   int
	gate    chan struct{}
	entered chan struct{}
	panicOn bool
}

func (c *countingObserve) Containers(ctx context.Context) ([]engine.Container, error) {
	c.mu.Lock()
	c.calls++
	gate, entered, boom := c.gate, c.entered, c.panicOn
	c.panicOn = false
	c.mu.Unlock()
	if boom {
		panic("cache exploded")
	}
	if gate != nil {
		entered <- struct{}{}
		<-gate
	}
	return c.fakeObserve.Containers(ctx)
}

func (c *countingObserve) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestContainerListCachedFor2s(t *testing.T) {
	obs := &countingObserve{fakeObserve: containerObserve()}
	now := summaryNow
	env, get := containersEnv(t, nil, func(o *api.Options) { o.Observe = obs; o.Now = func() time.Time { return now } })
	_ = env
	get("/api/containers")
	now = now.Add(1900 * time.Millisecond)
	get("/api/containers")
	if n := obs.callCount(); n != 1 {
		t.Fatalf("calls within 2 s = %d, want 1", n)
	}
	now = now.Add(200 * time.Millisecond)
	get("/api/containers")
	if n := obs.callCount(); n != 2 {
		t.Errorf("calls after 2 s = %d, want 2", n)
	}
}

func TestContainerListSingleFlight50(t *testing.T) {
	obs := &countingObserve{fakeObserve: containerObserve(), gate: make(chan struct{}), entered: make(chan struct{}, 64)}
	env, _ := containersEnv(t, nil, func(o *api.Options) { o.Observe = obs })
	cookie := env.login(t)
	codes := make(chan int, 50)
	for i := 0; i < 50; i++ {
		go func() { codes <- env.do(t, "GET", "/api/containers", "", withCookie(cookie)).Code }()
	}
	<-obs.entered
	time.Sleep(100 * time.Millisecond)
	close(obs.gate)
	for i := 0; i < 50; i++ {
		if c := <-codes; c != 200 {
			t.Errorf("status = %d, want 200", c)
		}
	}
	if n := obs.callCount(); n != 1 {
		t.Errorf("state reads = %d, want 1", n)
	}
}

func TestContainerListPanicIsAnErrorAndNotCached(t *testing.T) {
	obs := &countingObserve{fakeObserve: containerObserve(), panicOn: true}
	_, get := containersEnv(t, nil, func(o *api.Options) { o.Observe = obs })
	if res := get("/api/containers"); res.Code != 500 {
		t.Errorf("panicking read = %d, want 500", res.Code)
	}
	if res := get("/api/containers"); res.Code != 200 {
		t.Errorf("retry = %d, want 200 (errors are not cached)", res.Code)
	}
}

func TestContainerList503WhenDisconnected(t *testing.T) {
	obs := containerObserve()
	obs.containersErr = fmt.Errorf("list: %w", engine.ErrUnavailable)
	_, get := containersEnv(t, obs, nil)
	res := get("/api/containers")
	var body map[string]string
	if res.Code != 503 || json.Unmarshal(res.Body, &body) != nil || body["error"] == "" {
		t.Errorf("= %d %s, want 503 {error}", res.Code, res.Body)
	}
}

func TestContainerListStacksErrorIs500(t *testing.T) {
	st := newFakeStacks()
	st.err = errors.New("boom")
	_, get := containersEnv(t, containerObserve(), func(o *api.Options) { o.Stacks = st })
	if res := get("/api/containers"); res.Code != 500 {
		t.Errorf("= %d %s, want 500", res.Code, res.Body)
	}
}

func TestContainerListRequiresSession(t *testing.T) {
	env := newEnvWith(t, "", func(o *api.Options) {
		fs := newFakeStacks()
		o.Stacks, o.Operations, o.Observe = fs, fakeOps{fs}, containerObserve()
	})
	env.createAdmin(t)
	if rec := env.do(t, "GET", "/api/containers", ""); rec.Code != 401 {
		t.Errorf("without cookie = %d, want 401", rec.Code)
	}
}

func TestContainerListAbsentWithoutSources(t *testing.T) {
	_, get := dashboardEnv(t, nil)
	if res := get("/api/containers"); res.Code != 404 {
		t.Errorf("= %d, want 404", res.Code)
	}
}
