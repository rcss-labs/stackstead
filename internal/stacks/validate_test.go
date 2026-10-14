package stacks

import (
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/engine"
)

func TestValidateName(t *testing.T) {
	good := []string{"a", "web", "my-stack_1", "0abc", strings.Repeat("a", 63)}
	bad := []string{"", "-web", "_web", "Web", "my stack", "a.b", "a/b", strings.Repeat("a", 64)}
	for _, n := range good {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
	for _, n := range bad {
		err := ValidateName(n)
		var ve *ValidationError
		if !errors.As(err, &ve) || len(ve.Fields) != 1 || ve.Fields[0].Field != "name" {
			t.Errorf("ValidateName(%q) = %v, want ValidationError on name", n, err)
		}
	}
}

func TestValidationErrorMessage(t *testing.T) {
	e := &ValidationError{Fields: []FieldError{{"a", "bad a"}, {"b", "bad b"}}}
	if got := e.Error(); got != "bad a; bad b" {
		t.Errorf("Error() = %q", got)
	}
}

func hp(ip string, port uint16, proto string) HostPort {
	var a netip.Addr
	if ip != "" {
		a = netip.MustParseAddr(ip)
	}
	return HostPort{IP: a, Port: port, Proto: proto}
}

func TestParsePorts(t *testing.T) {
	tests := []struct {
		spec    string
		want    []HostPort
		wantErr bool
	}{
		{"8080:80", []HostPort{hp("", 8080, "tcp")}, false},
		{"127.0.0.1:8080:80", []HostPort{hp("127.0.0.1", 8080, "tcp")}, false},
		{"8000-8002:80-82", []HostPort{hp("", 8000, "tcp"), hp("", 8001, "tcp"), hp("", 8002, "tcp")}, false},
		{"53:53/udp", []HostPort{hp("", 53, "udp")}, false},
		{"[::1]:80:80", []HostPort{hp("::1", 80, "tcp")}, false},
		{"80", nil, false},
		{"80/udp", nil, false},
		{"127.0.0.1::80", nil, false},
		{"${PORT:-80}:80", nil, false},
		{"127.0.0.1:${PORT}:80", nil, false},
		{"99999:80", nil, true},
		{"0:80", nil, true},
		{"a:b", nil, true},
		{"", nil, true},
		{"8080:80/sctpx", nil, true},
		{"1.2.3:80:80", nil, true},
		{"8000-8002:80-81", nil, true},
		{"8002-8000:80-82", nil, true},
		{"1:2:3:4", nil, true},
	}
	for _, tc := range tests {
		got, err := ParsePorts(tc.spec)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParsePorts(%q) err = %v, wantErr %v", tc.spec, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParsePorts(%q) = %v, want %v", tc.spec, got, tc.want)
		}
	}
}

func TestPortConflicts(t *testing.T) {
	use := func(ip string, port uint16, proto, owner string) PortUse {
		return PortUse{HostPort: hp(ip, port, proto), Owner: owner}
	}
	tests := []struct {
		name   string
		wanted []string
		inUse  []PortUse
		fields []string // expected Field values
		msgHas string
	}{
		{"free", []string{"8080:80"}, []PortUse{use("", 9090, "tcp", "container a")}, nil, ""},
		{"same port conflicts", []string{"8080:80"}, []PortUse{use("", 8080, "tcp", "container web")}, []string{"ports[0]"}, "container web"},
		{"different proto ok", []string{"53:53/udp"}, []PortUse{use("", 53, "tcp", "container a")}, nil, ""},
		{"same proto udp conflicts", []string{"53:53/udp"}, []PortUse{use("", 53, "udp", "stack dns")}, []string{"ports[0]"}, "stack dns"},
		{"zero ip overlaps specific", []string{"8080:80"}, []PortUse{use("127.0.0.1", 8080, "tcp", "c")}, []string{"ports[0]"}, ""},
		{"specific overlaps zero", []string{"127.0.0.1:8080:80"}, []PortUse{use("", 8080, "tcp", "c")}, []string{"ports[0]"}, ""},
		{"different ips ok", []string{"127.0.0.1:8080:80"}, []PortUse{use("10.0.0.1", 8080, "tcp", "c")}, nil, ""},
		{"same ip conflicts", []string{"127.0.0.1:8080:80"}, []PortUse{use("127.0.0.1", 8080, "tcp", "c")}, []string{"ports[0]"}, ""},
		{"range overlaps single", []string{"8000-8002:80-82"}, []PortUse{use("", 8001, "tcp", "c")}, []string{"ports[0]"}, "8001"},
		{"range misses single", []string{"8000-8002:80-82"}, []PortUse{use("", 8003, "tcp", "c")}, nil, ""},
		{"random port never conflicts", []string{"80"}, []PortUse{use("", 80, "tcp", "c")}, nil, ""},
		{"variable skipped", []string{"${PORT:-80}:80"}, []PortUse{use("", 80, "tcp", "c")}, nil, ""},
		{"invalid reported with index", []string{"8080:80", "99999:80"}, nil, []string{"ports[1]"}, ""},
		{"index of conflicting entry", []string{"1:1", "8080:80"}, []PortUse{use("", 8080, "tcp", "c")}, []string{"ports[1]"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PortConflicts("ports", tc.wanted, tc.inUse)
			var fields []string
			for _, f := range got {
				fields = append(fields, f.Field)
			}
			if !reflect.DeepEqual(fields, tc.fields) {
				t.Fatalf("fields = %v, want %v (%v)", fields, tc.fields, got)
			}
			if tc.msgHas != "" && !strings.Contains(got[0].Message, tc.msgHas) {
				t.Errorf("message %q lacks %q", got[0].Message, tc.msgHas)
			}
		})
	}
}

func TestCheckStaticIP(t *testing.T) {
	nets := []engine.Network{
		{Name: "lan", Subnets: []engine.Subnet{{
			Prefix:  netip.MustParsePrefix("192.168.50.0/24"),
			Gateway: netip.MustParseAddr("192.168.50.1"),
		}}},
		{Name: "v6only"},
	}
	used := map[string][]string{"lan": {"192.168.50.10 container web", "192.168.50.11"}}
	tests := []struct {
		name, network, ip string
		wantMsg           string // "" = no error
	}{
		{"ok", "lan", "192.168.50.20", ""},
		{"missing network", "nope", "192.168.50.20", "network nope not found"},
		{"no ipv4 subnet", "v6only", "192.168.50.20", "no IPv4 subnet"},
		{"unparsable", "lan", "abc", "not a valid IPv4"},
		{"ipv6 rejected", "lan", "::1", "not a valid IPv4"},
		{"outside subnet", "lan", "10.0.0.5", "not in a subnet"},
		{"network address", "lan", "192.168.50.0", "network address"},
		{"broadcast", "lan", "192.168.50.255", "broadcast"},
		{"gateway", "lan", "192.168.50.1", "gateway"},
		{"used by named", "lan", "192.168.50.10", "192.168.50.10 is already used by container web"},
		{"used anonymous", "lan", "192.168.50.11", "192.168.50.11 is already used"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckStaticIP("networks.lan.ipv4_address", tc.network, tc.ip, nets, used)
			if tc.wantMsg == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected errors: %v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Field != "networks.lan.ipv4_address" || !strings.Contains(got[0].Message, tc.wantMsg) {
				t.Fatalf("got %v, want message containing %q", got, tc.wantMsg)
			}
		})
	}
}

func TestCheckStaticIPPointToPointSubnet(t *testing.T) {
	nets := []engine.Network{{Name: "p", Subnets: []engine.Subnet{{Prefix: netip.MustParsePrefix("10.1.0.0/31")}}}}
	// /31 and /32 have no distinct network/broadcast addresses.
	if got := CheckStaticIP("f", "p", "10.1.0.1", nets, nil); len(got) != 0 {
		t.Errorf("unexpected errors: %v", got)
	}
}
