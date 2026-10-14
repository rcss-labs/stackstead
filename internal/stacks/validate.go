package stacks

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/rcss-labs/stackstead/internal/engine"
)

// FieldError describes one invalid input field, e.g. "name", "ports[1]" or
// "networks.lan.ipv4_address".
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationError carries every problem found; the API answers 400 with Fields.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	msgs := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		msgs[i] = f.Message
	}
	return strings.Join(msgs, "; ")
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ValidateName enforces docker compose project-name rules.
func ValidateName(name string) error {
	if nameRe.MatchString(name) {
		return nil
	}
	return &ValidationError{Fields: []FieldError{{
		Field:   "name",
		Message: "name must start with a lowercase letter or digit, contain only lowercase letters, digits, '-' and '_', and be at most 63 characters",
	}}}
}

// HostPort is one published host-side port.
type HostPort struct {
	IP    netip.Addr // zero value = all interfaces
	Port  uint16
	Proto string // "tcp", "udp" or "sctp"
}

// PortUse is a host port held by an existing container or stack.
type PortUse struct {
	HostPort
	Owner string // "container <name>" or "stack <name>"
}

// ParsePorts expands a Compose short-syntax port spec into the host ports it
// publishes. Specs that publish no fixed host port ("80", "127.0.0.1::80") and
// specs containing unresolved variables ("${PORT:-80}:80") yield nil, nil:
// they cannot conflict, or cannot be checked.
func ParsePorts(spec string) ([]HostPort, error) {
	bad := func(format string, a ...any) ([]HostPort, error) {
		return nil, fmt.Errorf("invalid port %q: %s", spec, fmt.Sprintf(format, a...))
	}
	if strings.TrimSpace(spec) == "" {
		return bad("empty")
	}
	if strings.Contains(spec, "${") {
		return nil, nil
	}

	body, proto := spec, "tcp"
	if i := strings.LastIndex(spec, "/"); i >= 0 {
		body, proto = spec[:i], spec[i+1:]
		switch proto {
		case "tcp", "udp", "sctp":
		default:
			return bad("unknown protocol %q", proto)
		}
	}

	var ipStr string
	rest := body
	if strings.HasPrefix(rest, "[") {
		end := strings.Index(rest, "]:")
		if end < 0 {
			return bad("malformed IPv6 address")
		}
		ipStr, rest = rest[1:end], rest[end+2:]
	}
	parts := strings.Split(rest, ":")
	var hostStr, containerStr string
	switch {
	case ipStr != "" && len(parts) == 2:
		hostStr, containerStr = parts[0], parts[1]
	case ipStr != "":
		return bad("expected [ip]:host:container")
	case len(parts) == 1:
		containerStr = parts[0]
	case len(parts) == 2:
		hostStr, containerStr = parts[0], parts[1]
	case len(parts) == 3:
		ipStr, hostStr, containerStr = parts[0], parts[1], parts[2]
	default:
		return bad("too many ':' separators")
	}

	var ip netip.Addr
	if ipStr != "" {
		a, err := netip.ParseAddr(ipStr)
		if err != nil {
			return bad("invalid IP %q", ipStr)
		}
		if !a.IsUnspecified() {
			ip = a
		}
	}

	cLo, cHi, err := parsePortRange(containerStr)
	if err != nil {
		return bad("%v", err)
	}
	if hostStr == "" {
		return nil, nil // random host port
	}
	hLo, hHi, err := parsePortRange(hostStr)
	if err != nil {
		return bad("%v", err)
	}
	if cHi != cLo && cHi-cLo != hHi-hLo {
		return bad("host and container ranges differ in length")
	}

	out := make([]HostPort, 0, hHi-hLo+1)
	for p := hLo; p <= hHi; p++ {
		out = append(out, HostPort{IP: ip, Port: uint16(p), Proto: proto})
	}
	return out, nil
}

// parsePortRange parses "80" or "8000-8002" with ports in 1..65535.
func parsePortRange(s string) (lo, hi int, err error) {
	loStr, hiStr, isRange := strings.Cut(s, "-")
	lo, err = parsePort(loStr)
	if err != nil {
		return 0, 0, err
	}
	if !isRange {
		return lo, lo, nil
	}
	hi, err = parsePort(hiStr)
	if err != nil {
		return 0, 0, err
	}
	if hi < lo {
		return 0, 0, fmt.Errorf("range %q is descending", s)
	}
	return lo, hi, nil
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("%q is not a port between 1 and 65535", s)
	}
	return n, nil
}

func (a HostPort) conflictsWith(b HostPort) bool {
	if a.Proto != b.Proto || a.Port != b.Port {
		return false
	}
	return !a.IP.IsValid() || !b.IP.IsValid() || a.IP == b.IP
}

// PortConflicts reports invalid specs and specs whose host ports are already
// in use. field is the list's name; errors are attributed to "field[i]".
func PortConflicts(field string, wanted []string, inUse []PortUse) []FieldError {
	var errs []FieldError
	for i, spec := range wanted {
		at := fmt.Sprintf("%s[%d]", field, i)
		ports, err := ParsePorts(spec)
		if err != nil {
			errs = append(errs, FieldError{Field: at, Message: err.Error()})
			continue
		}
		if c, ok := firstConflict(ports, inUse); ok {
			errs = append(errs, FieldError{
				Field:   at,
				Message: fmt.Sprintf("port %d/%s is already used by %s", c.Port, c.Proto, c.Owner),
			})
		}
	}
	return errs
}

func firstConflict(ports []HostPort, inUse []PortUse) (PortUse, bool) {
	for _, p := range ports {
		for _, u := range inUse {
			if p.conflictsWith(u.HostPort) {
				return u, true
			}
		}
	}
	return PortUse{}, false
}

// CheckStaticIP validates a static IPv4 address for a compose network.
// used maps network name to addresses already taken; each entry is either
// "<ip>" or "<ip> <owner>" (e.g. "192.168.50.10 container web") so the error
// can name the owner.
func CheckStaticIP(field, network, ip string, nets []engine.Network, used map[string][]string) []FieldError {
	fail := func(format string, a ...any) []FieldError {
		return []FieldError{{Field: field, Message: fmt.Sprintf(format, a...)}}
	}

	var net *engine.Network
	for i := range nets {
		if nets[i].Name == network {
			net = &nets[i]
			break
		}
	}
	if net == nil {
		return fail("network %s not found", network)
	}
	var subnets []engine.Subnet
	for _, s := range net.Subnets {
		if s.Prefix.Addr().Is4() {
			subnets = append(subnets, s)
		}
	}
	if len(subnets) == 0 {
		return fail("network %s has no IPv4 subnet", network)
	}

	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.Is4() {
		return fail("%q is not a valid IPv4 address", ip)
	}
	var sub *engine.Subnet
	for i := range subnets {
		if subnets[i].Prefix.Contains(addr) {
			sub = &subnets[i]
			break
		}
	}
	if sub == nil {
		return fail("%s is not in a subnet of network %s", ip, network)
	}

	prefix := sub.Prefix.Masked()
	// /31 and /32 have no separate network or broadcast address.
	if prefix.Bits() < 31 {
		if addr == prefix.Addr() {
			return fail("%s is the network address of %s", ip, prefix)
		}
		if addr == lastAddr(prefix) {
			return fail("%s is the broadcast address of %s", ip, prefix)
		}
	}
	if sub.Gateway.IsValid() && addr == sub.Gateway {
		return fail("%s is the gateway of network %s", ip, network)
	}

	for _, entry := range used[network] {
		usedIP, owner, _ := strings.Cut(entry, " ")
		if usedIP != addr.String() {
			continue
		}
		if owner != "" {
			return fail("%s is already used by %s", ip, owner)
		}
		return fail("%s is already used", ip)
	}
	return nil
}

// lastAddr returns the highest address in an IPv4 prefix.
func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Addr().As4()
	host := 32 - p.Bits()
	for i := 3; i >= 0 && host > 0; i-- {
		n := min(host, 8)
		b[i] |= byte(1<<n - 1)
		host -= n
	}
	return netip.AddrFrom4(b)
}
