package api

import (
	"github.com/rcss-labs/stackstead/internal/apitypes"
	"net/netip"
	"reflect"
	"strings"
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
	empty := networkRow(engine.Network{}, nil, nil)
	if empty.Subnets == nil || empty.Gateways == nil || empty.Containers == nil {
		t.Fatalf("nil arrays: %+v", empty)
	}
	row := networkRow(engine.Network{ID: "abcdefghijklmnop", Name: "lan", Driver: "ipvlan", Scope: "local", Internal: true,
		Labels: map[string]string{"com.docker.compose.project": "dns-project"}, Options: map[string]string{"parent": "eth0", "ipvlan_mode": "l3"},
		Subnets:    []engine.Subnet{{Prefix: netip.MustParsePrefix("10.0.0.0/24"), IPRange: netip.MustParsePrefix("10.0.0.128/25")}, {Prefix: netip.MustParsePrefix("10.1.0.0/24"), Gateway: netip.MustParseAddr("10.1.0.1")}},
		Containers: []engine.NetworkEndpoint{{Name: "z", IPv4: "10.0.0.3/24"}, {Name: "a", IPv4: "10.0.0.2/24"}}}, map[string]string{"dns-project": "dns"}, nil)
	if row.ID != "abcdefghijkl" || row.Stack != "dns" || row.Project != "dns-project" || row.Parent != "eth0" || row.IPvlanMode != "l3" || row.IPRange != "10.0.0.128/25" || !row.Internal || row.Scope != "local" || !reflect.DeepEqual(row.Gateways, []string{"", "10.1.0.1"}) || row.Containers[0].Name != "a" {
		t.Fatalf("row = %+v", row)
	}
}

func TestNetworkCreateValidationTable(t *testing.T) {
	base := apitypes.NetworkCreateRequest{Name: "lan", Driver: "macvlan", Subnet: "192.168.1.0/24", Gateway: "192.168.1.1", Parent: "eth0"}
	cases := []struct {
		name, field, fragment string
		change                func(*apitypes.NetworkCreateRequest)
	}{
		{"bridge", "", "", func(r *apitypes.NetworkCreateRequest) {
			*r = apitypes.NetworkCreateRequest{Name: "test", Driver: "bridge"}
		}},
		{"macvlan", "", "", func(r *apitypes.NetworkCreateRequest) {}},
		{"bad name", "name", "name", func(r *apitypes.NetworkCreateRequest) { r.Name = "-x" }},
		{"long name", "name", "name", func(r *apitypes.NetworkCreateRequest) { r.Name = strings.Repeat("x", 64) }},
		{"driver", "driver", "driver", func(r *apitypes.NetworkCreateRequest) { r.Driver = "overlay" }},
		{"required subnet", "subnet", "required", func(r *apitypes.NetworkCreateRequest) { r.Subnet = "" }},
		{"unmasked", "subnet", "masked", func(r *apitypes.NetworkCreateRequest) { r.Subnet = "192.168.1.5/24" }},
		{"ipv6", "subnet", "IPv4", func(r *apitypes.NetworkCreateRequest) { r.Subnet = "fd00::/64" }},
		{"overlap", "subnet", "bridge", func(r *apitypes.NetworkCreateRequest) { r.Subnet = "172.17.1.0/24" }},
		{"gateway requires subnet", "gateway", "subnet", func(r *apitypes.NetworkCreateRequest) { r.Subnet = "" }},
		{"outside gateway", "gateway", "not in", func(r *apitypes.NetworkCreateRequest) { r.Gateway = "192.168.2.1" }},
		{"network gateway", "gateway", "network address", func(r *apitypes.NetworkCreateRequest) { r.Gateway = "192.168.1.0" }},
		{"broadcast gateway", "gateway", "broadcast", func(r *apitypes.NetworkCreateRequest) { r.Gateway = "192.168.1.255" }},
		{"invalid gateway", "gateway", "IPv4", func(r *apitypes.NetworkCreateRequest) { r.Gateway = "bad" }},
		{"outside range", "ipRange", "inside", func(r *apitypes.NetworkCreateRequest) { r.IPRange = "192.168.2.0/25" }},
		{"larger range", "ipRange", "inside", func(r *apitypes.NetworkCreateRequest) { r.IPRange = "192.168.0.0/16" }},
		{"unmasked inside range", "", "", func(r *apitypes.NetworkCreateRequest) { r.IPRange = "192.168.1.7/25" }},
		{"range requires subnet", "ipRange", "subnet", func(r *apitypes.NetworkCreateRequest) { r.Subnet = ""; r.IPRange = "192.168.1.0/25" }},
		{"invalid range", "ipRange", "IPv4", func(r *apitypes.NetworkCreateRequest) { r.IPRange = "bad" }},
		{"v6 range", "ipRange", "IPv4", func(r *apitypes.NetworkCreateRequest) { r.IPRange = "fd00::/64" }},
		{"bridge parent", "parent", "bridge", func(r *apitypes.NetworkCreateRequest) { r.Driver = "bridge" }},
		{"ipvlan parent", "parent", "required", func(r *apitypes.NetworkCreateRequest) { r.Driver = "ipvlan"; r.Parent = "" }},
		{"bad parent", "parent", "interface", func(r *apitypes.NetworkCreateRequest) { r.Parent = "eth0!" }},
		{"macvlan mode", "ipvlanMode", "ipvlan", func(r *apitypes.NetworkCreateRequest) { r.IPvlanMode = "l2" }},
		{"bad mode", "ipvlanMode", "l2", func(r *apitypes.NetworkCreateRequest) { r.Driver = "ipvlan"; r.IPvlanMode = "l9" }},
		{"point to point", "", "", func(r *apitypes.NetworkCreateRequest) { r.Subnet = "192.168.1.0/31"; r.Gateway = "192.168.1.0" }},
		{"single host", "", "", func(r *apitypes.NetworkCreateRequest) { r.Subnet = "192.168.1.1/32"; r.Gateway = "192.168.1.1" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.change(&r)
			got := validateNetworkCreate(r, []engine.Network{{Name: "bridge", Subnets: []engine.Subnet{{Prefix: netip.MustParsePrefix("172.17.0.0/16")}}}})
			if tc.field == "" {
				if len(got) > 0 {
					t.Fatalf("%+v", got)
				}
				return
			}
			seen := map[string]bool{}
			found := false
			for _, f := range got {
				if seen[f.Field] {
					t.Fatalf("duplicate field: %+v", got)
				}
				seen[f.Field] = true
				if f.Field == tc.field && strings.Contains(f.Message, tc.fragment) {
					found = true
				}
			}
			if !found {
				t.Fatalf("want %s/%s: %+v", tc.field, tc.fragment, got)
			}
		})
	}
}
func TestToNetworkSpecDefaults(t *testing.T) {
	got := toNetworkSpec(apitypes.NetworkCreateRequest{Name: "lan", Driver: "ipvlan", Parent: "eth0", Subnet: "10.1.0.0/24", Gateway: "10.1.0.1", IPRange: "10.1.0.128/25", Internal: true})
	want := engine.NetworkSpec{Name: "lan", Driver: "ipvlan", Parent: "eth0", IPvlanMode: "l2", Subnet: "10.1.0.0/24", Gateway: "10.1.0.1", IPRange: "10.1.0.128/25", Internal: true}
	if got != want {
		t.Fatalf("%+v want %+v", got, want)
	}
	if got := toNetworkSpec(apitypes.NetworkCreateRequest{Driver: "bridge", Parent: "eth0"}); got.Parent != "" {
		t.Fatalf("bridge parent: %+v", got)
	}
}
