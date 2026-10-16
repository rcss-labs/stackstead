package api

import (
	"net/netip"
	"reflect"
	"testing"

	"github.com/rcss-labs/stackstead/internal/engine"
)

func TestIsBuiltinNetwork(t *testing.T) {
	for _, tc := range []struct {
		name, driver string
		want         bool
	}{
		{"bridge", "bridge", true}, {"host", "host", true}, {"none", "null", true},
		{"bridge2", "bridge", false}, {"host", "macvlan", false}, {"bridge", "host", false}, {"none", "bridge", false},
	} {
		if got := isBuiltinNetwork(engine.Network{Name: tc.name, Driver: tc.driver}); got != tc.want {
			t.Errorf("%s/%s = %v", tc.name, tc.driver, got)
		}
	}
}

func TestNetworkRowShapes(t *testing.T) {
	empty := networkRow(engine.Network{}, nil)
	if empty.Subnets == nil || empty.Gateways == nil || empty.Containers == nil {
		t.Fatalf("nil arrays: %+v", empty)
	}
	row := networkRow(engine.Network{ID: "abcdefghijklmnop", Name: "lan", Driver: "ipvlan", Scope: "local", Internal: true,
		Labels: map[string]string{"com.docker.compose.project": "dns-project"}, Options: map[string]string{"parent": "eth0", "ipvlan_mode": "l3"},
		Subnets:    []engine.Subnet{{Prefix: netip.MustParsePrefix("10.0.0.0/24"), IPRange: netip.MustParsePrefix("10.0.0.128/25")}, {Prefix: netip.MustParsePrefix("10.1.0.0/24"), Gateway: netip.MustParseAddr("10.1.0.1")}},
		Containers: []engine.NetworkEndpoint{{Name: "z", IPv4: "10.0.0.3/24"}, {Name: "a", IPv4: "10.0.0.2/24"}}}, map[string]string{"dns-project": "dns"})
	if row.ID != "abcdefghijkl" || row.Stack != "dns" || row.Project != "dns-project" || row.Parent != "eth0" || row.IPvlanMode != "l3" || row.IPRange != "10.0.0.128/25" || !row.Internal || row.Scope != "local" || !reflect.DeepEqual(row.Gateways, []string{"", "10.1.0.1"}) || row.Containers[0].Name != "a" {
		t.Fatalf("row = %+v", row)
	}
}
