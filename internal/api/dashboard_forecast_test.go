package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/alerts"
	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/metrics"
)

type fakeForecast struct {
	f   metrics.DiskForecast
	err error
}

func (f fakeForecast) DiskForecast(context.Context) (metrics.DiskForecast, error) { return f.f, f.err }

func TestDiskForecastRoute(t *testing.T) {
	days := 12.5
	_, get := dashboardEnv(t, func(o *api.Options) {
		o.DiskForecast = fakeForecast{f: metrics.DiskForecast{DaysLeft: &days, SampledDays: 3}}
	})
	res := get("/api/host/disk-forecast")
	if res.Code != 200 || strings.TrimSpace(string(res.Body)) != `{"daysLeft":12.5,"sampledDays":3}` {
		t.Errorf("forecast = %d %s", res.Code, res.Body)
	}
}

func TestDiskForecastNullAndFailureNeverError(t *testing.T) {
	for name, src := range map[string]fakeForecast{
		"no history": {f: metrics.DiskForecast{SampledDays: 0.1}},
		"read fails": {err: errors.New("database is locked")},
	} {
		_, get := dashboardEnv(t, func(o *api.Options) { o.DiskForecast = src })
		res := get("/api/host/disk-forecast")
		var body map[string]any
		if res.Code != 200 || json.Unmarshal(res.Body, &body) != nil || body["daysLeft"] != nil {
			t.Errorf("%s: %d %s", name, res.Code, res.Body)
		}
		if _, ok := body["daysLeft"]; !ok {
			t.Errorf("%s: daysLeft must be present as null: %s", name, res.Body)
		}
	}
}

func TestDiskForecastRequiresSession(t *testing.T) {
	env := newEnvWith(t, "", func(o *api.Options) { o.DiskForecast = fakeForecast{} })
	env.createAdmin(t)
	if rec := env.do(t, "GET", "/api/host/disk-forecast", ""); rec.Code != 401 {
		t.Errorf("without cookie = %d, want 401", rec.Code)
	}
}

func TestAlertsBodyCarriesExitCodeAndLimit(t *testing.T) {
	zero, oom := 0, 137
	_, get := dashboardEnv(t, func(o *api.Options) {
		o.Alerts = fakeAlerts{list: []alerts.Alert{
			{Kind: "exited", Severity: "danger", Container: "a", Message: "m", Since: time.Unix(0, 0), ExitCode: &oom, MemLimit: 268435456},
			{Kind: "restart_loop", Severity: "warning", Container: "b", Message: "m", Since: time.Unix(0, 0), ExitCode: &zero},
		}}
	})
	var body []map[string]any
	res := get("/api/alerts")
	if err := json.Unmarshal(res.Body, &body); err != nil || len(body) != 2 {
		t.Fatalf("body %s err %v", res.Body, err)
	}
	if body[0]["exitCode"] != float64(137) || body[0]["memLimit"] != float64(268435456) {
		t.Errorf("exited = %v", body[0])
	}
	if v, ok := body[1]["exitCode"]; !ok || v != float64(0) {
		t.Errorf("exit code 0 must be reported: %v", body[1])
	}
	if _, ok := body[1]["memLimit"]; ok {
		t.Errorf("memLimit must be omitted without a limit: %v", body[1])
	}
}
