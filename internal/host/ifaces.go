package host

import (
	"bufio"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

type Interface struct {
	Name    string `json:"name"`
	IPv4    string `json:"ipv4"`
	Subnet  string `json:"subnet"`
	Gateway string `json:"gateway"`
	Default bool   `json:"default"`
}

type RawInterface struct {
	Name         string
	Up, Loopback bool
	Addrs        []netip.Prefix
}

type InterfaceLister struct {
	ProcRoot, GOOS string
	Source         func() ([]RawInterface, error)
}

var hiddenInterfacePrefixes = []string{"docker", "br-", "veth", "virbr", "cni", "flannel", "cali", "tailscale", "wg"}

func (l InterfaceLister) Interfaces() ([]Interface, error) {
	result := []Interface{}
	goos := l.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "linux" {
		return result, nil
	}
	source := l.Source
	if source == nil {
		source = OSInterfaces
	}
	raw, err := source()
	if err != nil {
		return result, err
	}
	procRoot := l.ProcRoot
	if procRoot == "" {
		procRoot = "/proc"
	}
	defaultIface, gateway, hasRoute, err := readDefaultRoute(procRoot)
	if err != nil {
		hasRoute = false
	}
	for _, iface := range raw {
		if !iface.Up || iface.Loopback {
			continue
		}
		hidden := false
		for _, prefix := range hiddenInterfacePrefixes {
			if strings.HasPrefix(iface.Name, prefix) {
				hidden = true
				break
			}
		}
		if hidden {
			continue
		}
		for _, addr := range iface.Addrs {
			if !addr.IsValid() || !addr.Addr().Is4() {
				continue
			}
			entry := Interface{Name: iface.Name, IPv4: addr.String(), Subnet: addr.Masked().String()}
			if hasRoute && iface.Name == defaultIface {
				entry.Default = true
				entry.Gateway = gateway.String()
			}
			result = append(result, entry)
			break
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Default != result[j].Default {
			return result[i].Default
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// OSInterfaces reads interface addresses without inspecting host configuration files.
func OSInterfaces() ([]RawInterface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	result := make([]RawInterface, 0, len(ifaces))
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		raw := RawInterface{Name: iface.Name, Up: iface.Flags&net.FlagUp != 0, Loopback: iface.Flags&net.FlagLoopback != 0}
		for _, addr := range addrs {
			prefix, err := netip.ParsePrefix(addr.String())
			if err == nil && prefix.Addr().Is4() {
				raw.Addrs = append(raw.Addrs, prefix)
			}
		}
		result = append(result, raw)
	}
	return result, nil
}

func readDefaultRoute(procRoot string) (iface string, gateway netip.Addr, ok bool, err error) {
	f, err := os.Open(filepath.Join(procRoot, "net", "route"))
	if err != nil {
		return "", netip.Addr{}, false, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", netip.Addr{}, false, err
		}
		return "", netip.Addr{}, false, fmt.Errorf("route file has no header")
	}
	header := strings.Fields(scanner.Text())
	if len(header) < 8 || header[0] != "Iface" || header[1] != "Destination" || header[2] != "Gateway" || header[3] != "Flags" || header[6] != "Metric" {
		return "", netip.Addr{}, false, fmt.Errorf("invalid route header")
	}
	var lowestMetric uint64
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 8 {
			return "", netip.Addr{}, false, fmt.Errorf("invalid route row")
		}
		destination, destErr := parseRouteAddress(fields[1])
		routeGateway, gatewayErr := parseRouteAddress(fields[2])
		mask, maskErr := parseRouteAddress(fields[7])
		flags, flagsErr := strconv.ParseUint(fields[3], 16, 32)
		metric, metricErr := strconv.ParseUint(fields[6], 10, 64)
		if destErr != nil || gatewayErr != nil || maskErr != nil || flagsErr != nil || metricErr != nil {
			return "", netip.Addr{}, false, fmt.Errorf("invalid route fields")
		}
		if destination != netip.AddrFrom4([4]byte{}) || mask != netip.AddrFrom4([4]byte{}) || flags&0x3 != 0x3 {
			continue
		}
		if !ok || metric < lowestMetric {
			iface, gateway, ok, lowestMetric = fields[0], routeGateway, true, metric
		}
	}
	if err := scanner.Err(); err != nil {
		return "", netip.Addr{}, false, err
	}
	return iface, gateway, ok, nil
}

func parseRouteAddress(value string) (netip.Addr, error) {
	if len(value) != 8 {
		return netip.Addr{}, fmt.Errorf("invalid route address")
	}
	n, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return netip.Addr{}, err
	}
	return netip.AddrFrom4([4]byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}), nil
}
