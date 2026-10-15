package api_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
)

func TestStatusReportsVersion(t *testing.T) {
	st := newFakeStacks()
	env := newEnvWith(t, "", func(o *api.Options) {
		o.Stacks, o.Operations, o.FSRoots, o.Status = st, fakeOps{st}, st.roots, st.status
		o.Version = "v1.2.3"
	})
	env.createAdmin(t)
	cookie := env.login(t)
	rec := env.do(t, "GET", "/api/status", "", withCookie(cookie))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["version"] != "v1.2.3" {
		t.Errorf("version = %v, want v1.2.3 (body %s)", body["version"], rec.Body)
	}
}

func TestStatusReportsDockerVersionAndMetricsSettings(t *testing.T) {
	st := newFakeStacks()
	env := newEnvWith(t, "", func(o *api.Options) {
		o.Stacks, o.Operations, o.FSRoots, o.Status = st, fakeOps{st}, st.roots, st.status
		o.MetricsInterval, o.MetricsRetention = 10*time.Second, 72*time.Hour
	})
	env.createAdmin(t)
	cookie := env.login(t)
	rec := env.do(t, "GET", "/api/status", "", withCookie(cookie))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["dockerVersion"] != "27.1.1" || body["metricsIntervalSeconds"] != float64(10) || body["metricsRetentionSeconds"] != float64(259200) {
		t.Errorf("body = %s", rec.Body)
	}
}
