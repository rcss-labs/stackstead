package stacks

import (
	"reflect"
	"testing"

	"github.com/rcss-labs/stackstead/internal/composefile"
	"github.com/rcss-labs/stackstead/internal/engine"
)

func TestPublishedPorts(t *testing.T) {
	tests := []struct {
		name     string
		services map[string][]string
		want     []string
	}{
		{"sorted numerically", map[string][]string{"web": {"8096:8096", "80:80", "443:443/tcp"}}, []string{"80", "443", "8096"}},
		{"range", map[string][]string{"web": {"8000-8010:80"}}, []string{"8000-8010"}},
		{"ip and protocol dropped", map[string][]string{"web": {"127.0.0.1:8080:80/udp"}}, []string{"8080"}},
		{"same port in two services once", map[string][]string{"a": {"80:80"}, "b": {"80:8080"}}, []string{"80"}},
		{"container only and interpolation skipped", map[string][]string{"web": {"80", "${WEB_PORT}:80"}}, []string{}},
		{"invalid spec skipped", map[string][]string{"web": {"abc:80", "9000:90"}}, []string{"9000"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := "services:\n"
			for svc, ports := range tc.services {
				src += "  " + svc + ":\n    image: x\n    ports:\n"
				for _, p := range ports {
					src += "      - \"" + p + "\"\n"
				}
			}
			f, err := composefile.Parse([]byte(src))
			if err != nil {
				t.Fatal(err)
			}
			got := publishedPorts(f)
			if got == nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("publishedPorts = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestCountHealth(t *testing.T) {
	label := map[string]string{"com.docker.compose.project": "p"}
	c := func(state, status string) engine.Container {
		return engine.Container{State: state, Status: status, Labels: label}
	}
	cs := []engine.Container{
		c("running", "Up 1 hour (healthy)"),
		c("running", "Up 1 hour (unhealthy)"),
		c("running", "Up 1 second (health: starting)"),
		c("running", "Up 1 hour"),
		c("dead", ""),
		c("restarting", "Restarting (1) 2 seconds ago"),
		c("exited", "Exited (1) 1 hour ago"),
		c("exited", "Exited (0) 1 hour ago"),
		c("created", "Created"),
		c("paused", "Up 1 hour (Paused)"),
		{State: "running", Status: "Up 1 hour (healthy)", Labels: map[string]string{"com.docker.compose.project": "other"}},
	}
	want := HealthCounts{Healthy: 1, Unhealthy: 1, Starting: 1, None: 1, Failed: 3}
	if got := countHealth("p", cs); got != want {
		t.Errorf("countHealth = %+v, want %+v", got, want)
	}
	if got := countHealth("", cs); got != (HealthCounts{}) {
		t.Errorf("empty project = %+v, want zero", got)
	}
}
