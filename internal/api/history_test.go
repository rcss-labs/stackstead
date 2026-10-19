package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/history"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

var healthNow = time.Date(2026, 10, 19, 12, 0, 0, 500_000_000, time.UTC)

type fakeHistory struct {
	mu       sync.Mutex
	timeline history.Timeline
	err      error

	calls      int
	gotProject string
	gotFrom    time.Time
	gotTo      time.Time
}

func (f *fakeHistory) Timeline(_ context.Context, project string, from, to time.Time) (history.Timeline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.gotProject, f.gotFrom, f.gotTo = project, from, to
	return f.timeline, f.err
}

func (f *fakeHistory) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func healthEnv(t *testing.T, fake *fakeHistory, st *fakeStacks) func(string) *httpResult {
	t.Helper()
	_, get := dashboardEnv(t, func(o *api.Options) {
		o.Stacks, o.Operations, o.FSRoots, o.Status = st, fakeOps{st}, st.roots, st.status
		o.History = fake
		o.Now = func() time.Time { return healthNow }
	})
	return get
}

func TestHealthHistoryRequiresSession(t *testing.T) {
	st := newFakeStacks()
	env := newEnvWith(t, "", func(o *api.Options) {
		o.Stacks, o.Operations, o.History = st, fakeOps{st}, &fakeHistory{}
	})
	env.createAdmin(t)
	if rec := env.do(t, "GET", "/api/stacks/web/health", ""); rec.Code != 401 {
		t.Fatalf("anonymous status = %d, want 401", rec.Code)
	}
}

func TestHealthHistoryShape(t *testing.T) {
	to := healthNow.Truncate(time.Second)
	from := to.Add(-time.Hour)
	fake := &fakeHistory{timeline: history.Timeline{From: from, To: to, Services: []history.ServiceTimeline{{
		Service: "home-assistant", Restarts: 3, Runs: []history.Run{
			{State: "nodata", From: from, To: from.Add(420 * time.Second)},
			{State: "healthy", From: from.Add(420 * time.Second), To: from.Add(2900 * time.Second)},
			{State: "unhealthy", From: from.Add(2900 * time.Second), To: to},
		}}}}}
	get := healthEnv(t, fake, newFakeStacks())
	res := get("/api/stacks/web/health")
	if res.Code != 200 {
		t.Fatalf("status %d body %s", res.Code, res.Body)
	}
	want := map[string]any{
		"from": float64(from.Unix()), "to": float64(to.Unix()),
		"services": []any{map[string]any{"service": "home-assistant", "restarts": float64(3), "runs": []any{
			map[string]any{"state": "nodata", "from": float64(from.Unix()), "to": float64(from.Unix() + 420)},
			map[string]any{"state": "healthy", "from": float64(from.Unix() + 420), "to": float64(from.Unix() + 2900)},
			map[string]any{"state": "unhealthy", "from": float64(from.Unix() + 2900), "to": float64(to.Unix())},
		}}},
	}
	var got map[string]any
	if err := json.Unmarshal(res.Body, &got); err != nil {
		t.Fatal(err)
	}
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if string(gb) != string(wb) {
		t.Errorf("body = %s\nwant   %s", gb, wb)
	}
	if fake.gotProject != "web" || !fake.gotTo.Equal(to) || !fake.gotFrom.Equal(from) {
		t.Errorf("Timeline(%q, %v, %v), want (web, %v, %v)", fake.gotProject, fake.gotFrom, fake.gotTo, from, to)
	}
}

func TestHealthHistoryEmptyServicesIsArrayNotNull(t *testing.T) {
	get := healthEnv(t, &fakeHistory{}, newFakeStacks())
	res := get("/api/stacks/web/health")
	if res.Code != 200 {
		t.Fatalf("status %d body %s", res.Code, res.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(res.Body, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["services"]) != "[]" {
		t.Errorf("services = %s, want []", raw["services"])
	}
}

func TestHealthHistoryRunsNeverNull(t *testing.T) {
	fake := &fakeHistory{timeline: history.Timeline{Services: []history.ServiceTimeline{{Service: "a"}}}}
	res := healthEnv(t, fake, newFakeStacks())("/api/stacks/web/health")
	var raw struct {
		Services []map[string]json.RawMessage `json:"services"`
	}
	if err := json.Unmarshal(res.Body, &raw); err != nil || len(raw.Services) != 1 {
		t.Fatalf("body %s err %v", res.Body, err)
	}
	if string(raw.Services[0]["runs"]) != "[]" {
		t.Errorf("runs = %s, want []", raw.Services[0]["runs"])
	}
}

func TestHealthHistorySpan(t *testing.T) {
	for _, tc := range []struct {
		query string
		code  int
		span  time.Duration
	}{
		{"", 200, time.Hour},
		{"?span=1m", 200, time.Minute},
		{"?span=24h", 200, 24 * time.Hour},
		{"?span=90m", 200, 90 * time.Minute},
		{"?span=0s", 400, 0},
		{"?span=59s", 400, 0},
		{"?span=25h", 400, 0},
		{"?span=abc", 400, 0},
		{"?span=-5m", 400, 0},
	} {
		t.Run("span"+tc.query, func(t *testing.T) {
			fake := &fakeHistory{}
			res := healthEnv(t, fake, newFakeStacks())("/api/stacks/web/health" + tc.query)
			if res.Code != tc.code {
				t.Fatalf("status %d body %s, want %d", res.Code, res.Body, tc.code)
			}
			if tc.code == 400 {
				var body map[string]string
				if json.Unmarshal(res.Body, &body) != nil || body["error"] != "span must be a duration between 1m and 24h" {
					t.Errorf("body %s", res.Body)
				}
				if fake.callCount() != 0 {
					t.Error("store queried for a rejected span")
				}
				return
			}
			if got := fake.gotTo.Sub(fake.gotFrom); got != tc.span {
				t.Errorf("to-from = %v, want %v", got, tc.span)
			}
		})
	}
}

func TestHealthHistoryUnknownStack(t *testing.T) {
	st := newFakeStacks()
	st.err = stacks.ErrStackNotFound
	fake := &fakeHistory{}
	res := healthEnv(t, fake, st)("/api/stacks/nope/health")
	var body map[string]string
	if res.Code != 404 || json.Unmarshal(res.Body, &body) != nil || body["error"] == "" {
		t.Fatalf("status %d body %s, want 404 {error}", res.Code, res.Body)
	}
	if fake.callCount() != 0 {
		t.Error("store queried for an unknown stack")
	}
}

func TestHealthHistoryEmptyProjectSkipsStore(t *testing.T) {
	st := newFakeStacks()
	st.summary.Project = ""
	fake := &fakeHistory{}
	res := healthEnv(t, fake, st)("/api/stacks/web/health")
	if res.Code != 200 {
		t.Fatalf("status %d body %s", res.Code, res.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(res.Body, &raw); err != nil || string(raw["services"]) != "[]" || raw["from"] == nil || raw["to"] == nil {
		t.Errorf("body %s", res.Body)
	}
	if fake.callCount() != 0 {
		t.Error("store queried for a stack without a project")
	}
}

func TestHealthHistoryStoreError(t *testing.T) {
	fake := &fakeHistory{err: errors.New("disk I/O error at /var/lib/stackstead.db")}
	res := healthEnv(t, fake, newFakeStacks())("/api/stacks/web/health")
	var body map[string]string
	if res.Code != 500 || json.Unmarshal(res.Body, &body) != nil || body["error"] != "internal error" {
		t.Fatalf("status %d body %s, want 500 generic body", res.Code, res.Body)
	}
}

func TestHealthHistoryNotRegisteredWithoutHistory(t *testing.T) {
	_, get := dashboardEnv(t, func(o *api.Options) {
		st := newFakeStacks()
		o.Stacks, o.Operations, o.FSRoots, o.Status = st, fakeOps{st}, st.roots, st.status
	})
	res := get("/api/stacks/web/health")
	var body map[string]string
	if res.Code != 404 || json.Unmarshal(res.Body, &body) != nil || body["error"] != "not found" {
		t.Fatalf("status %d body %s, want 404 fallback", res.Code, res.Body)
	}
}
