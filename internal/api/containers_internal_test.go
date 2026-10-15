package api

import (
	"reflect"
	"testing"

	"github.com/rcss-labs/stackstead/internal/engine"
)

func TestContainerManaged(t *testing.T) {
	const projectLabel = "com.docker.compose.project"
	projects := map[string]string{"demo-project": "demo"}
	cases := []struct {
		name                string
		labels              map[string]string
		wantManaged, wantSt string
	}{
		{"registered project", map[string]string{projectLabel: "demo-project"}, "stack", "demo"},
		{"unregistered compose project", map[string]string{projectLabel: "other"}, "compose", ""},
		{"no project label", map[string]string{"x": "y"}, "none", ""},
		{"nil labels", nil, "none", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			managed, stack := containerManaged(engine.Container{Labels: tc.labels}, projects)
			if managed != tc.wantManaged || stack != tc.wantSt {
				t.Errorf("= (%q, %q), want (%q, %q)", managed, stack, tc.wantManaged, tc.wantSt)
			}
		})
	}
}

func TestFormatPorts(t *testing.T) {
	cases := []struct {
		name string
		in   []engine.Port
		want []string
	}{
		{"nil input is an empty slice", nil, []string{}},
		{"unpublished dropped", []engine.Port{{PrivatePort: 9187, Type: "tcp"}}, []string{}},
		{"v4 and v6 duplicate collapsed to 0.0.0.0", []engine.Port{
			{IP: "::", PrivatePort: 5432, PublicPort: 5432, Type: "tcp"},
			{IP: "0.0.0.0", PrivatePort: 5432, PublicPort: 5432, Type: "tcp"},
		}, []string{"0.0.0.0:5432->5432/tcp"}},
		{"lone v6 is bracketed", []engine.Port{{IP: "::", PrivatePort: 80, PublicPort: 80, Type: "tcp"}}, []string{"[::]:80->80/tcp"}},
		{"v6 loopback is bracketed", []engine.Port{{IP: "::1", PrivatePort: 80, PublicPort: 8080, Type: "tcp"}}, []string{"[::1]:8080->80/tcp"}},
		{"sorted by public port, udp kept", []engine.Port{
			{IP: "0.0.0.0", PrivatePort: 53, PublicPort: 5353, Type: "udp"},
			{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
			{IP: "127.0.0.1", PrivatePort: 22, PublicPort: 2222, Type: "tcp"},
			{PrivatePort: 9187, Type: "tcp"},
		}, []string{"127.0.0.1:2222->22/tcp", "0.0.0.0:5353->53/udp", "0.0.0.0:8080->80/tcp"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatPorts(tc.in)
			if got == nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("= %#v, want %#v", got, tc.want)
			}
		})
	}
}
