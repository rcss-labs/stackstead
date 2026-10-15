package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/alerts"
	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/host"
	"github.com/rcss-labs/stackstead/internal/metrics"
)

type fakeHost struct{ snap host.Snapshot }

func (f fakeHost) Snapshot(context.Context) host.Snapshot { return f.snap }

type fakeAlerts struct {
	list []alerts.Alert
	err  error
}

func (f fakeAlerts) Alerts(context.Context) ([]alerts.Alert, error) { return f.list, f.err }

type fakeSummary struct {
	mu         sync.Mutex
	rows       []metrics.ProjectBucket
	err        error
	latest     []metrics.ExpositionSample
	calls      int
	from, to   time.Time
	step       time.Duration
	lastWindow time.Duration
	// gate, when set, makes QueryAll announce itself on entered and then block
	// until gate is closed.
	gate    chan struct{}
	entered chan struct{}
}

func (f *fakeSummary) QueryAll(_ context.Context, from, to time.Time, step time.Duration) ([]metrics.ProjectBucket, error) {
	f.mu.Lock()
	f.calls++
	f.from, f.to, f.step = from, to, step
	gate, entered := f.gate, f.entered
	f.mu.Unlock()
	if gate != nil {
		entered <- struct{}{}
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rows, f.err
}
func (f *fakeSummary) Latest() []metrics.ExpositionSample { return f.latest }

func fptr(v float64) *float64 { return &v }

// dashboardEnv builds a logged-in env with the given dashboard sources.
func dashboardEnv(t *testing.T, tweak func(*api.Options)) (*testEnv, func(string) *httpResult) {
	t.Helper()
	env := newEnvWith(t, "", tweak)
	env.createAdmin(t)
	cookie := env.login(t)
	get := func(path string) *httpResult {
		rec := env.do(t, "GET", path, "", withCookie(cookie))
		return &httpResult{Code: rec.Code, Body: rec.Body.Bytes()}
	}
	return env, get
}

type httpResult struct {
	Code int
	Body []byte
}

func fullSnapshot() host.Snapshot {
	return host.Snapshot{
		Hostname: "docker-host-01", OS: "linux", Source: "proc",
		SampledAt:  time.Date(2026, 10, 15, 14, 2, 30, 0, time.UTC),
		Containers: &host.ContainerCounts{Total: 14, Running: 10, Exited: 4, Bad: 1},
		CPU:        &host.CPU{Percent: fptr(18.2), Cores: 8},
		Memory:     &host.Memory{Used: 6656000000, Total: 17179869184, SwapTotal: 2147483648},
		Disk:       &host.Disk{Path: "/opt/stacks", Free: 442381631488, Total: 1073741824000},
		Network:    &host.Network{RxBytesPerSec: fptr(16e6), TxBytesPerSec: fptr(4e6), Interface: "eth0"},
		DiskIO:     &host.DiskIO{ReadBytesPerSec: fptr(1.2e6), WriteBytesPerSec: fptr(4e5)},
		Volumes:    &host.Volumes{Count: 14, Size: 40802189312, Dangling: 2},
		Uptime:     &host.Uptime{Seconds: 1830240, Load1: 1.42, Load5: 1.10, Load15: 0.95},
		Restarts24h: &host.Restarts{Total: 3, Top: []host.RestartTop{
			{Container: "monitoring-loki-1", Service: "loki", Project: "monitoring", Count: 2}}},
	}
}

func keys(m map[string]any) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestDashboardRoutesRequireSession(t *testing.T) {
	env := newEnvWith(t, "", func(o *api.Options) {
		o.Host, o.Alerts, o.Summary = fakeHost{}, fakeAlerts{}, &fakeSummary{}
	})
	env.createAdmin(t)
	for _, path := range []string{"/api/host", "/api/alerts", "/api/metrics/summary"} {
		if rec := env.do(t, "GET", path, ""); rec.Code != 401 {
			t.Errorf("%s without cookie = %d, want 401", path, rec.Code)
		}
	}
}

func TestHostBodyKeysAreExactlyDocumented(t *testing.T) {
	_, get := dashboardEnv(t, func(o *api.Options) { o.Host = fakeHost{fullSnapshot()} })
	res := get("/api/host")
	if res.Code != 200 {
		t.Fatalf("status = %d, body %s", res.Code, res.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body, &body); err != nil {
		t.Fatal(err)
	}
	if got, want := keys(body), "containers,cpu,disk,diskIO,hostname,memory,network,os,restarts24h,sampledAt,source,uptime,volumes"; got != want {
		t.Errorf("top-level keys = %s, want %s", got, want)
	}
	sections := map[string]string{
		"containers":  "bad,exited,running,total",
		"cpu":         "cores,percent",
		"memory":      "swapTotal,swapUsed,total,used",
		"disk":        "free,path,total",
		"network":     "interface,rxBytesPerSec,txBytesPerSec",
		"diskIO":      "readBytesPerSec,writeBytesPerSec",
		"volumes":     "count,dangling,size",
		"uptime":      "load1,load15,load5,seconds",
		"restarts24h": "top,total",
	}
	for name, want := range sections {
		sec, ok := body[name].(map[string]any)
		if !ok {
			t.Fatalf("section %s = %v, want object", name, body[name])
		}
		if got := keys(sec); got != want {
			t.Errorf("%s keys = %s, want %s", name, got, want)
		}
	}
	top := body["restarts24h"].(map[string]any)["top"].([]any)[0].(map[string]any)
	if got := keys(top); got != "container,count,project,service" {
		t.Errorf("restarts top keys = %s", got)
	}
	if body["hostname"] != "docker-host-01" || body["source"] != "proc" {
		t.Errorf("body = %s", res.Body)
	}
}

func TestHostNilSectionsAreExplicitNull(t *testing.T) {
	snap := host.Snapshot{Hostname: "h", OS: "darwin", Source: "docker"}
	_, get := dashboardEnv(t, func(o *api.Options) { o.Host = fakeHost{snap} })
	res := get("/api/host")
	if res.Code != 200 {
		t.Fatalf("status = %d", res.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 13 {
		t.Errorf("keys = %s", keys(body))
	}
	for _, k := range []string{"containers", "cpu", "memory", "disk", "network", "diskIO", "volumes", "uptime", "restarts24h"} {
		v, present := body[k]
		if !present || v != nil {
			t.Errorf("%s = %v (present %v), want explicit null", k, v, present)
		}
	}
}

func TestHostNullRatesAndEmptyTop(t *testing.T) {
	snap := host.Snapshot{Hostname: "h", OS: "linux", Source: "proc",
		CPU:         &host.CPU{Cores: 2},
		Restarts24h: &host.Restarts{}}
	_, get := dashboardEnv(t, func(o *api.Options) { o.Host = fakeHost{snap} })
	var body map[string]any
	if err := json.Unmarshal(get("/api/host").Body, &body); err != nil {
		t.Fatal(err)
	}
	if v, ok := body["cpu"].(map[string]any)["percent"]; !ok || v != nil {
		t.Errorf("cpu.percent = %v (present %v), want null", v, ok)
	}
	if top, ok := body["restarts24h"].(map[string]any)["top"].([]any); !ok || len(top) != 0 {
		t.Errorf("restarts24h.top = %v, want []", body["restarts24h"])
	}
}

func TestAlertsEmptyIsArray(t *testing.T) {
	_, get := dashboardEnv(t, func(o *api.Options) { o.Alerts = fakeAlerts{} })
	res := get("/api/alerts")
	if res.Code != 200 || strings.TrimSpace(string(res.Body)) != "[]" {
		t.Errorf("status %d body %q, want 200 []", res.Code, res.Body)
	}
}

func TestAlertsBodyOmitsEmptyFields(t *testing.T) {
	since := time.Date(2026, 10, 15, 12, 5, 41, 0, time.UTC)
	_, get := dashboardEnv(t, func(o *api.Options) {
		o.Alerts = fakeAlerts{list: []alerts.Alert{
			{Kind: "exited", Severity: "danger", Stack: "monitoring", Service: "loki", Container: "monitoring-loki-1", Message: "m", Since: since},
			{Kind: "docker_disconnected", Severity: "danger", Message: "Docker is unreachable; status may be stale", Since: since},
		}}
	})
	res := get("/api/alerts")
	var body []map[string]any
	if err := json.Unmarshal(res.Body, &body); err != nil || len(body) != 2 {
		t.Fatalf("body %s err %v", res.Body, err)
	}
	if got := keys(body[0]); got != "container,kind,message,service,severity,since,stack" {
		t.Errorf("full alert keys = %s", got)
	}
	if got := keys(body[1]); got != "kind,message,severity,since" {
		t.Errorf("global alert keys = %s", got)
	}
	if body[0]["since"] != "2026-10-15T12:05:41Z" {
		t.Errorf("since = %v", body[0]["since"])
	}
}

func TestAlertsErrorIs500(t *testing.T) {
	_, get := dashboardEnv(t, func(o *api.Options) { o.Alerts = fakeAlerts{err: errors.New("registry boom")} })
	res := get("/api/alerts")
	var body map[string]string
	if res.Code != 500 || json.Unmarshal(res.Body, &body) != nil || body["error"] == "" {
		t.Errorf("status %d body %s, want 500 {error}", res.Code, res.Body)
	}
	if strings.Contains(string(res.Body), "boom") {
		t.Errorf("internal error text leaked: %s", res.Body)
	}
}

func TestSummaryWindowValidation(t *testing.T) {
	_, get := dashboardEnv(t, func(o *api.Options) { o.Summary = &fakeSummary{} })
	res := get("/api/metrics/summary?window=30m")
	var body map[string]string
	if res.Code != 400 || json.Unmarshal(res.Body, &body) != nil || body["error"] == "" {
		t.Errorf("status %d body %s, want 400 {error}", res.Code, res.Body)
	}
}

// now is deliberately not step-aligned (step for 15m is 30 s).
var summaryNow = time.Date(2026, 10, 15, 14, 2, 37, 0, time.UTC)

func TestSummaryShape(t *testing.T) {
	// 14:02:30 is the bucket containing summaryNow, so it is the last slot.
	newest := time.Date(2026, 10, 15, 14, 2, 30, 0, time.UTC)
	older := newest.Add(-30 * time.Second)
	fake := &fakeSummary{
		rows: []metrics.ProjectBucket{
			{TS: older, Project: "media", Container: "media-jellyfin-1", Samples: 2, CPUSum: 8, MemSum: 2000, NetRx: 1000, NetTx: 100},
			{TS: newest, Project: "media", Container: "media-jellyfin-1", Samples: 2, CPUSum: 10, MemSum: 4000, NetRx: 7000, NetTx: 700},
		},
		latest: []metrics.ExpositionSample{
			{Project: "a", Service: "s1", Container: "c1", CPU: 1, Mem: 10},
			{Project: "a", Service: "s2", Container: "c2", CPU: 7, Mem: 20},
			{Project: "b", Service: "s3", Container: "c3", CPU: 3, Mem: 70},
			{Project: "b", Service: "s4", Container: "c4", CPU: 9, Mem: 30},
			{Project: "c", Service: "s5", Container: "c5", CPU: 5, Mem: 60},
			{Project: "c", Service: "s6", Container: "c6", CPU: 2, Mem: 40},
			{Project: "d", Service: "s7", Container: "c7", CPU: 4, Mem: 50},
		},
	}
	_, get := dashboardEnv(t, func(o *api.Options) {
		o.Summary = fake
		o.Now = func() time.Time { return summaryNow }
	})
	res := get("/api/metrics/summary")
	if res.Code != 200 {
		t.Fatalf("status = %d, body %s", res.Code, res.Body)
	}
	var body apitypes.MetricsSummary
	if err := json.Unmarshal(res.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Window != "15m" || body.Step != 30 {
		t.Errorf("window/step = %s/%d", body.Window, body.Step)
	}
	if want := time.Date(2026, 10, 15, 13, 48, 0, 0, time.UTC); !body.From.Equal(want) || fake.from.Unix()%30 != 0 {
		t.Errorf("from = %v (store %v), want aligned %v", body.From, fake.from, want)
	}
	if want := time.Date(2026, 10, 15, 14, 3, 0, 0, time.UTC); !body.To.Equal(want) || body.To.Sub(body.From) != 15*time.Minute {
		t.Errorf("to = %v, want %v", body.To, want)
	}
	media, ok := body.Stacks["media"]
	if !ok || len(body.Stacks) != 1 {
		t.Fatalf("stacks = %v", body.Stacks)
	}
	if len(media.CPU) != 30 || len(media.Mem) != 30 {
		t.Fatalf("len cpu/mem = %d/%d, want 30", len(media.CPU), len(media.Mem))
	}
	if media.CPU[29] == nil || *media.CPU[29] != 5 || media.CPU[28] == nil || *media.CPU[28] != 4 || media.CPU[27] != nil {
		t.Errorf("cpu tail = %v %v %v", media.CPU[27], media.CPU[28], media.CPU[29])
	}
	if media.CPUNow == nil || *media.CPUNow != 5 || media.MemNow == nil || *media.MemNow != 2000 {
		t.Errorf("now = %v %v", media.CPUNow, media.MemNow)
	}
	if media.NetRxBytesPerSec == nil || *media.NetRxBytesPerSec != 200 {
		t.Errorf("rx = %v, want 200", media.NetRxBytesPerSec)
	}
	if !strings.Contains(string(res.Body), `"cpu":[null,`) {
		t.Errorf("empty buckets are not JSON null: %.200s", res.Body)
	}
	var cpuTop []float64
	for _, e := range body.Top.CPU {
		cpuTop = append(cpuTop, e.Value)
	}
	if len(cpuTop) != 5 || cpuTop[0] != 9 || cpuTop[1] != 7 || cpuTop[2] != 5 || cpuTop[3] != 4 || cpuTop[4] != 3 {
		t.Errorf("top cpu = %v, want 9 7 5 4 3", cpuTop)
	}
	if got := body.Top.Mem; len(got) != 5 || got[0].Value != 70 || got[0].Container != "c3" || got[0].Service != "s3" || got[0].Project != "b" || got[4].Value != 30 {
		t.Errorf("top mem = %+v", got)
	}
}

func TestSummaryEmptyStoreAndLatestAreEmptyNotNull(t *testing.T) {
	_, get := dashboardEnv(t, func(o *api.Options) { o.Summary = &fakeSummary{} })
	res := get("/api/metrics/summary?window=5m")
	var body map[string]any
	if err := json.Unmarshal(res.Body, &body); err != nil || res.Code != 200 {
		t.Fatalf("status %d body %s", res.Code, res.Body)
	}
	if body["window"] != "5m" || body["step"] != float64(10) {
		t.Errorf("window/step = %v/%v", body["window"], body["step"])
	}
	if s, ok := body["stacks"].(map[string]any); !ok || len(s) != 0 {
		t.Errorf("stacks = %v, want {}", body["stacks"])
	}
	top := body["top"].(map[string]any)
	for _, k := range []string{"cpu", "mem"} {
		if a, ok := top[k].([]any); !ok || len(a) != 0 {
			t.Errorf("top.%s = %v, want []", k, top[k])
		}
	}
}

func TestSummaryCachedFor10s(t *testing.T) {
	fake := &fakeSummary{}
	now := summaryNow
	_, get := dashboardEnv(t, func(o *api.Options) {
		o.Summary = fake
		o.Now = func() time.Time { return now }
	})
	get("/api/metrics/summary")
	get("/api/metrics/summary")
	if fake.calls != 1 {
		t.Fatalf("calls after two requests = %d, want 1", fake.calls)
	}
	get("/api/metrics/summary?window=1h")
	if fake.calls != 2 {
		t.Errorf("a different window shares the cache: calls = %d, want 2", fake.calls)
	}
	now = now.Add(11 * time.Second)
	get("/api/metrics/summary")
	if fake.calls != 3 {
		t.Errorf("calls after expiry = %d, want 3", fake.calls)
	}
}

func TestSummaryStoreErrorIs500AndIsNotCached(t *testing.T) {
	fake := &fakeSummary{err: errors.New("db locked")}
	_, get := dashboardEnv(t, func(o *api.Options) {
		o.Summary = fake
		o.Now = func() time.Time { return summaryNow }
	})
	res := get("/api/metrics/summary")
	var body map[string]string
	if res.Code != 500 || json.Unmarshal(res.Body, &body) != nil || body["error"] == "" {
		t.Fatalf("status %d body %s, want 500 {error}", res.Code, res.Body)
	}
	fake.mu.Lock()
	fake.err = nil
	fake.mu.Unlock()
	if res := get("/api/metrics/summary"); res.Code != 200 {
		t.Errorf("after recovery status = %d, want 200", res.Code)
	}
}

func TestUnknownAPIPathStillJSON404WithDashboardRoutes(t *testing.T) {
	_, get := dashboardEnv(t, func(o *api.Options) {
		o.Host, o.Alerts, o.Summary = fakeHost{}, fakeAlerts{}, &fakeSummary{}
	})
	res := get("/api/nope")
	var body map[string]string
	if res.Code != 404 || json.Unmarshal(res.Body, &body) != nil || body["error"] != "not found" {
		t.Errorf("status %d body %s", res.Code, res.Body)
	}
}

func TestDashboardRoutesAbsentWithoutSources(t *testing.T) {
	_, get := dashboardEnv(t, nil)
	for _, p := range []string{"/api/host", "/api/alerts", "/api/metrics/summary"} {
		if res := get(p); res.Code != 404 || !strings.Contains(string(res.Body), `"error"`) {
			t.Errorf("%s = %d %s, want JSON 404", p, res.Code, res.Body)
		}
	}
}

// The CLI/web contract: the shared apitypes decode every body strictly.
func TestDashboardBodiesDecodeIntoAPITypes(t *testing.T) {
	_, get := dashboardEnv(t, func(o *api.Options) {
		o.Host = fakeHost{fullSnapshot()}
		o.Alerts = fakeAlerts{list: []alerts.Alert{{Kind: "unhealthy", Severity: "danger", Stack: "s", Message: "m", Since: summaryNow}}}
		o.Summary = &fakeSummary{}
		o.Now = func() time.Time { return summaryNow }
	})
	strict := func(path string, dst any) {
		t.Helper()
		res := get(path)
		dec := json.NewDecoder(strings.NewReader(string(res.Body)))
		dec.DisallowUnknownFields()
		if res.Code != 200 || dec.Decode(dst) != nil {
			t.Errorf("%s: status %d, body %s", path, res.Code, res.Body)
		}
	}
	var h apitypes.HostSnapshot
	var a []apitypes.Alert
	var s apitypes.MetricsSummary
	strict("/api/host", &h)
	strict("/api/alerts", &a)
	strict("/api/metrics/summary", &s)
	if h.Hostname != "docker-host-01" || h.Network == nil || *h.Network.RxBytesPerSec != 16e6 || len(a) != 1 || s.Window != "15m" {
		t.Errorf("decoded = %+v %+v %+v", h, a, s)
	}
}

func (f *fakeSummary) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestSummaryConcurrentRequestsShareOneQuery(t *testing.T) {
	fake := &fakeSummary{gate: make(chan struct{}), entered: make(chan struct{}, 8)}
	env, _ := dashboardEnv(t, func(o *api.Options) {
		o.Summary = fake
		o.Now = func() time.Time { return summaryNow }
	})
	cookie := env.login(t)
	codes := make(chan int, 4)
	for i := 0; i < 4; i++ {
		go func() { codes <- env.do(t, "GET", "/api/metrics/summary", "", withCookie(cookie)).Code }()
	}
	<-fake.entered
	time.Sleep(50 * time.Millisecond) // let the others queue behind the leader
	close(fake.gate)
	for i := 0; i < 4; i++ {
		if c := <-codes; c != 200 {
			t.Errorf("status = %d, want 200", c)
		}
	}
	if n := fake.callCount(); n != 1 {
		t.Errorf("QueryAll calls = %d, want 1", n)
	}
}

func TestSummaryCancelledLeaderDoesNotFailWaiter(t *testing.T) {
	fake := &fakeSummary{gate: make(chan struct{}), entered: make(chan struct{}, 8)}
	env, _ := dashboardEnv(t, func(o *api.Options) {
		o.Summary = fake
		o.Now = func() time.Time { return summaryNow }
	})
	cookie := env.login(t)
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	withCtx := func(req *http.Request) { *req = *req.WithContext(leaderCtx) }
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		env.do(t, "GET", "/api/metrics/summary", "", withCookie(cookie), withCtx)
	}()
	<-fake.entered
	waiter := make(chan int, 1)
	go func() { waiter <- env.do(t, "GET", "/api/metrics/summary", "", withCookie(cookie)).Code }()
	time.Sleep(50 * time.Millisecond)
	cancelLeader()
	<-leaderDone
	close(fake.gate)
	if c := <-waiter; c != 200 {
		t.Errorf("waiter status = %d, want 200", c)
	}
	if n := fake.callCount(); n != 1 {
		t.Errorf("QueryAll calls = %d, want 1 (waiter must not re-query)", n)
	}
}

func TestDashboardTimesAreUTCOnTheWire(t *testing.T) {
	zone := time.FixedZone("EST", -5*3600)
	local := time.Date(2026, 10, 15, 9, 2, 37, 0, zone)
	snap := fullSnapshot()
	snap.SampledAt = local
	_, get := dashboardEnv(t, func(o *api.Options) {
		o.Host = fakeHost{snap}
		o.Alerts = fakeAlerts{list: []alerts.Alert{{Kind: "unhealthy", Severity: "danger", Message: "m", Since: local}}}
		o.Summary = &fakeSummary{}
		o.Now = func() time.Time { return local }
	})
	for path, key := range map[string]string{"/api/host": "sampledAt", "/api/alerts": "since",
		"/api/metrics/summary": "from"} {
		res := get(path)
		var obj any
		if err := json.Unmarshal(res.Body, &obj); err != nil {
			t.Fatal(err)
		}
		if arr, ok := obj.([]any); ok {
			obj = arr[0]
		}
		raw, _ := obj.(map[string]any)[key].(string)
		if !strings.HasSuffix(raw, "Z") {
			t.Errorf("%s %s = %q, want UTC (Z)", path, key, raw)
		}
		if path == "/api/metrics/summary" {
			if to, _ := obj.(map[string]any)["to"].(string); !strings.HasSuffix(to, "Z") {
				t.Errorf("summary to = %q, want UTC", to)
			}
		}
	}
}
