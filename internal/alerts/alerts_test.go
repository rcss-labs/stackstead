package alerts_test

import (
	"reflect"
	"testing"

	"github.com/rcss-labs/stackstead/internal/alerts"
	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

func stack(name, project string, services ...stacks.ServiceDetail) stacks.Detail {
	return stacks.Detail{Summary: stacks.Summary{Name: name, Project: project}, Services: services}
}

func svc(name, restart string, ports ...string) stacks.ServiceDetail {
	return stacks.ServiceDetail{Name: name, Config: composefile.ServiceConfig{Restart: restart, Ports: ports}}
}

func ctr(id, name, project, service, state, status string, ports ...engine.Port) engine.Container {
	labels := map[string]string{}
	if project != "" {
		labels["com.docker.compose.project"] = project
		labels["com.docker.compose.service"] = service
	}
	return engine.Container{ID: id, Name: name, State: state, Status: status, Labels: labels, Ports: ports}
}

func published(port uint16) engine.Port {
	return engine.Port{PrivatePort: 80, PublicPort: port, Type: "tcp"}
}

func TestEvaluate(t *testing.T) {
	loki := stack("monitoring", "monitoring", svc("loki", "unless-stopped"))
	tests := []struct {
		name       string
		details    []stacks.Detail
		containers []engine.Container
		restarts   map[string]int
		connected  bool
		degraded   bool
		want       []alerts.Alert
	}{
		{
			name:       "exited with policy and restarts",
			details:    []stacks.Detail{loki},
			containers: []engine.Container{ctr("c1", "monitoring-loki-1", "monitoring", "loki", "exited", "Exited (1) 2 minutes ago")},
			restarts:   map[string]int{"c1": 2},
			connected:  true,
			want: []alerts.Alert{{Kind: "exited", Severity: "danger", Stack: "monitoring", Service: "loki", Container: "monitoring-loki-1",
				Message: "loki exited (1) and restart policy is unless-stopped; 2 restarts in 24h"}},
		},
		{
			name:       "exited with always policy and no restarts",
			details:    []stacks.Detail{stack("s", "s", svc("web", "always"))},
			containers: []engine.Container{ctr("c1", "s-web-1", "s", "web", "exited", "Exited (137) 1 minute ago")},
			connected:  true,
			want: []alerts.Alert{{Kind: "exited", Severity: "danger", Stack: "s", Service: "web", Container: "s-web-1",
				Message: "web exited (137) and restart policy is always"}},
		},
		{
			name:       "exited with other policy has no policy clause",
			details:    []stacks.Detail{stack("s", "s", svc("web", "no"))},
			containers: []engine.Container{ctr("c1", "s-web-1", "s", "web", "exited", "Exited (2) 1 minute ago")},
			connected:  true,
			want: []alerts.Alert{{Kind: "exited", Severity: "danger", Stack: "s", Service: "web", Container: "s-web-1",
				Message: "web exited (2)"}},
		},
		{
			name:       "exit code 0 is a deliberate stop",
			details:    []stacks.Detail{loki},
			containers: []engine.Container{ctr("c1", "monitoring-loki-1", "monitoring", "loki", "exited", "Exited (0) 2 minutes ago")},
			connected:  true,
		},
		{
			name:       "exited container of an unregistered project is ignored",
			details:    []stacks.Detail{loki},
			containers: []engine.Container{ctr("c1", "other-x-1", "other", "x", "exited", "Exited (1) 2 minutes ago")},
			connected:  true,
		},
		{
			name:       "unhealthy",
			details:    []stacks.Detail{stack("s", "s", svc("web", ""))},
			containers: []engine.Container{ctr("c1", "s-web-1", "s", "web", "running", "Up 1 minute (unhealthy)")},
			connected:  true,
			want: []alerts.Alert{{Kind: "unhealthy", Severity: "danger", Stack: "s", Service: "web", Container: "s-web-1",
				Message: "web is unhealthy"}},
		},
		{
			name:       "restart loop at three restarts",
			details:    []stacks.Detail{stack("s", "s", svc("web", ""))},
			containers: []engine.Container{ctr("c1", "s-web-1", "s", "web", "running", "Up 5 seconds")},
			restarts:   map[string]int{"c1": 3},
			connected:  true,
			want: []alerts.Alert{{Kind: "restart_loop", Severity: "warning", Stack: "s", Service: "web", Container: "s-web-1",
				Message: "web restarted 3 times in 24h"}},
		},
		{
			name:       "two restarts is not a loop",
			details:    []stacks.Detail{stack("s", "s", svc("web", ""))},
			containers: []engine.Container{ctr("c1", "s-web-1", "s", "web", "running", "Up 5 seconds")},
			restarts:   map[string]int{"c1": 2},
			connected:  true,
		},
		{
			name: "port clash between two stacks, each names the other",
			details: []stacks.Detail{
				stack("a", "a", svc("web", "", "8080:80")),
				stack("b", "b", svc("proxy", "", "127.0.0.1:8080:80")),
			},
			connected: true,
			want: []alerts.Alert{
				{Kind: "port_clash", Severity: "warning", Stack: "a", Service: "web", Message: "web wants 8080 but b holds it"},
				{Kind: "port_clash", Severity: "warning", Stack: "b", Service: "proxy", Message: "proxy wants 8080 but a holds it"},
			},
		},
		{
			name:       "port clash with a non-compose container",
			details:    []stacks.Detail{stack("a", "a", svc("web", "", "8080:80"))},
			containers: []engine.Container{ctr("x1", "legacy-nginx", "", "", "running", "Up 3 days", published(8080))},
			connected:  true,
			want: []alerts.Alert{{Kind: "port_clash", Severity: "warning", Stack: "a", Service: "web",
				Message: "web wants 8080 but a non-compose container (legacy-nginx) holds it"}},
		},
		{
			name:       "own container publishing the port is no clash",
			details:    []stacks.Detail{stack("a", "a", svc("web", "", "8080:80"))},
			containers: []engine.Container{ctr("c1", "a-web-1", "a", "web", "running", "Up 3 days", published(8080))},
			connected:  true,
		},
		{
			name:       "stopped foreign container holds nothing",
			details:    []stacks.Detail{stack("a", "a", svc("web", "", "8080:80"))},
			containers: []engine.Container{ctr("x1", "legacy-nginx", "", "", "exited", "Exited (0) 3 days ago", published(8080))},
			connected:  true,
		},
		{
			name: "override file",
			details: []stacks.Detail{func() stacks.Detail {
				d := stack("a", "a")
				d.Overrides = []string{"compose.override.yaml"}
				return d
			}()},
			connected: true,
			want: []alerts.Alert{{Kind: "override", Severity: "warning", Stack: "a",
				Message: "compose.override.yaml found; override files are not supported yet"}},
		},
		{
			name: "parse error keeps only the first line",
			details: []stacks.Detail{func() stacks.Detail {
				d := stack("a", "a")
				d.ParseError = "yaml: line 3: bad indent\nsecond line"
				return d
			}()},
			connected: true,
			want: []alerts.Alert{{Kind: "parse_error", Severity: "warning", Stack: "a",
				Message: "yaml: line 3: bad indent"}},
		},
		{
			name:      "docker disconnected",
			connected: false,
			want: []alerts.Alert{{Kind: "docker_disconnected", Severity: "danger",
				Message: "Docker is unreachable; status may be stale"}},
		},
		{
			name:      "metrics degraded",
			connected: true,
			degraded:  true,
			want: []alerts.Alert{{Kind: "metrics_degraded", Severity: "warning",
				Message: "Metrics are not being stored (database write failures)"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := alerts.Evaluate(tc.details, tc.containers, tc.restarts, tc.connected, tc.degraded)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}
