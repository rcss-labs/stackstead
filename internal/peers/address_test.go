package peers

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func fakeResolver(m map[string][]string) func(context.Context, string) ([]netip.Addr, error) {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		ips, ok := m[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		var out []netip.Addr
		for _, ip := range ips {
			out = append(out, netip.MustParseAddr(ip))
		}
		return out, nil
	}
}

func TestValidateAddress(t *testing.T) {
	resolve := fakeResolver(map[string][]string{
		"example.com": {"93.184.216.34"},
		"mini.lan":    {"10.0.0.2", "8.8.8.8"},
		"nas.lan":     {"192.168.1.9"},
	})
	cases := []struct {
		name      string
		raw       string
		plaintext bool
		want      string
		errSub    string
	}{
		{"https prefix kept", "https://mini-02.lan/stackstead/", false, "https://mini-02.lan/stackstead", ""},
		{"https bare slash", "https://mini-02.lan/", false, "https://mini-02.lan", ""},
		{"http without flag", "http://192.168.1.5:8420", false, "", "plain http needs"},
		{"http with flag", "http://192.168.1.5:8420", true, "http://192.168.1.5:8420", ""},
		{"public host", "http://example.com", true, "", "private"},
		{"mixed resolution", "http://mini.lan", true, "", "private"},
		{"private hostname", "http://nas.lan", true, "http://nas.lan", ""},
		{"ula", "http://[fd00::1]", true, "http://[fd00::1]", ""},
		{"link local", "http://169.254.1.1", true, "http://169.254.1.1", ""},
		{"loopback", "http://127.0.0.1:9", true, "http://127.0.0.1:9", ""},
		{"ftp", "ftp://x", false, "", "scheme"},
		{"userinfo", "https://u:p@x", false, "", "credentials"},
		{"query", "https://x/?a=1", false, "", "query"},
		{"fragment", "https://x/#f", false, "", "fragment"},
		{"no host", "https:///x", false, "", "host"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, err := ValidateAddress(c.raw, c.plaintext, resolve)
			if c.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), c.errSub) {
					t.Fatalf("err = %v, want containing %q", err, c.errSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if u.String() != c.want {
				t.Errorf("got %q, want %q", u.String(), c.want)
			}
		})
	}
}

func TestIsPrivate(t *testing.T) {
	for ip, want := range map[string]bool{
		"10.1.2.3": true, "172.16.0.1": true, "192.168.0.1": true, "127.0.0.1": true, "::1": true,
		"fc00::1": true, "fd12::1": true, "fe80::1": true, "169.254.0.5": true, "::ffff:10.0.0.1": true,
		"8.8.8.8": false, "172.32.0.1": false, "2606:4700::1": false, "100.64.0.1": false,
	} {
		if got := IsPrivate(netip.MustParseAddr(ip)); got != want {
			t.Errorf("IsPrivate(%s) = %v, want %v", ip, got, want)
		}
	}
}
