package host

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaultRouteParsing(t *testing.T) {
	for _, tt := range []struct{ fixture, iface, gateway string }{
		{"a", "eth0", "192.168.0.1"},
		{"b", "enp5s0", "10.0.0.1"},
	} {
		t.Run(tt.fixture, func(t *testing.T) {
			iface, gateway, ok, err := readDefaultRoute(filepath.Join("testdata", "proc", tt.fixture))
			if err != nil || !ok || iface != tt.iface || gateway.String() != tt.gateway {
				t.Fatalf("got %q %v %v %v", iface, gateway, ok, err)
			}
		})
	}
	t.Run("requires up and gateway flags", func(t *testing.T) {
		root := routeRoot(t, "eth0 00000000 0100A8C0 0001 0 0 1 00000000 0 0 0\neth1 00000000 0100000A 0002 0 0 0 00000000 0 0 0\n")
		_, _, ok, err := readDefaultRoute(root)
		if err != nil || ok {
			t.Fatalf("got ok=%v err=%v", ok, err)
		}
	})
}

func testInterfaceSource() ([]RawInterface, error) {
	return []RawInterface{
		{Name: "lo", Up: true, Loopback: true, Addrs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/8")}},
		{Name: "enp5s0", Up: true, Addrs: []netip.Prefix{netip.MustParsePrefix("10.0.0.5/16")}},
		{Name: "eth0", Up: true, Addrs: []netip.Prefix{netip.MustParsePrefix("2001:db8::1/64"), netip.MustParsePrefix("192.168.0.10/24"), netip.MustParsePrefix("172.16.0.1/16")}},
		{Name: "wlan0", Addrs: []netip.Prefix{netip.MustParsePrefix("192.168.1.2/24")}},
		{Name: "eth1", Up: true, Addrs: []netip.Prefix{netip.MustParsePrefix("2001:db8::2/64")}},
		{Name: "empty", Up: true},
	}, nil
}

func TestInterfacesFiltersAndSorts(t *testing.T) {
	source := func() ([]RawInterface, error) {
		ifaces, _ := testInterfaceSource()
		for _, name := range []string{"docker0", "br-1a2b", "veth9f1", "virbr0", "cni0", "flannel0", "cali123", "tailscale0", "wg0"} {
			ifaces = append(ifaces, RawInterface{Name: name, Up: true, Addrs: []netip.Prefix{netip.MustParsePrefix("172.17.0.1/16")}})
		}
		return ifaces, nil
	}
	got, err := (InterfaceLister{GOOS: "linux", ProcRoot: "testdata/proc/a", Source: source}).Interfaces()
	want := []Interface{
		{Name: "eth0", IPv4: "192.168.0.10/24", Subnet: "192.168.0.0/24", Gateway: "192.168.0.1", Default: true},
		{Name: "enp5s0", IPv4: "10.0.0.5/16", Subnet: "10.0.0.0/16"},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, %v; want %#v", got, err, want)
	}
}

func TestInterfacesWithoutRouteFile(t *testing.T) {
	assertInterfacesWithoutRoute(t, t.TempDir())
}

func TestInterfacesMalformedRouteFile(t *testing.T) {
	for _, row := range []string{
		"eth0 00000000 NOTHEX 0003 0 0 0 00000000 0 0 0\n",
		"eth0 00000000 0100A8C0 0003 0 0 bad 00000000 0 0 0\n",
		"eth0 too-short\n",
	} {
		t.Run(row, func(t *testing.T) { assertInterfacesWithoutRoute(t, routeRoot(t, row)) })
	}
}

func assertInterfacesWithoutRoute(t *testing.T, root string) {
	t.Helper()
	got, err := (InterfaceLister{GOOS: "linux", ProcRoot: root, Source: testInterfaceSource}).Interfaces()
	want := []Interface{
		{Name: "enp5s0", IPv4: "10.0.0.5/16", Subnet: "10.0.0.0/16"},
		{Name: "eth0", IPv4: "192.168.0.10/24", Subnet: "192.168.0.0/24"},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, %v; want %#v", got, err, want)
	}
}

func TestInterfacesNonLinuxIsEmpty(t *testing.T) {
	got, err := (InterfaceLister{GOOS: "darwin", ProcRoot: "/dev/null", Source: func() ([]RawInterface, error) {
		t.Fatal("non-Linux called source")
		return nil, nil
	}}).Interfaces()
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestInterfacesSourceError(t *testing.T) {
	want := errors.New("source failed")
	got, err := (InterfaceLister{GOOS: "linux", Source: func() ([]RawInterface, error) { return nil, want }}).Interfaces()
	if !errors.Is(err, want) || got == nil {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestInterfacesEmptySource(t *testing.T) {
	got, err := (InterfaceLister{GOOS: "linux", ProcRoot: t.TempDir(), Source: func() ([]RawInterface, error) { return nil, nil }}).Interfaces()
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestOSInterfacesRuns(t *testing.T) {
	if _, err := OSInterfaces(); err != nil {
		t.Fatal(err)
	}
}

func routeRoot(t *testing.T, rows string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "net"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "net", "route"), []byte("Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT\n"+rows), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}
