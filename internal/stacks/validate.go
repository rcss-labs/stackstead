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
// publishes. Specs that publish no fixed host port ("80", "127.0.0.1::80",
// "0:80") and specs containing unresolved variables ("${PORT:-80}:80",
// "$PORT:80") yield nil, nil: they cannot conflict, or cannot be checked.
func ParsePorts(spec string) ([]HostPort, error) {
	p, err := parseSpec(spec)
	return p.ports, err
}

// parsedSpec is a port spec's fixed host ports. When anyOf is set, Docker
// binds just ONE free port out of ports ("8000-8010:80").
type parsedSpec struct {
	ports []HostPort
	anyOf bool
}

func parseSpec(spec string) (parsedSpec, error) {
	bad := func(format string, a ...any) (parsedSpec, error) {
		return parsedSpec{}, fmt.Errorf("invalid port %q: %s", spec, fmt.Sprintf(format, a...))
	}
	if strings.TrimSpace(spec) == "" {
		return bad("empty")
	}
	if strings.Contains(spec, "$") {
		return parsedSpec{}, nil
	}

	body, proto := spec, "tcp"
	if i := strings.LastIndex(spec, "/"); i >= 0 {
		body, proto = spec[:i], strings.ToLower(spec[i+1:])
		switch proto {
		case "":
			proto = "tcp"
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
	} else if strings.Count(rest, ":") >= 2 {
		// ip:host:container, where an unbracketed IPv6 ip may contain colons:
		// split on the last two.
		last := strings.LastIndex(rest, ":")
		prev := strings.LastIndex(rest[:last], ":")
		ipStr, rest = rest[:prev], rest[prev+1:]
	}
	var hostStr, containerStr string
	switch parts := strings.Split(rest, ":"); len(parts) {
	case 1:
		containerStr = parts[0]
		if ipStr != "" {
			return bad("expected [ip]:host:container")
		}
	case 2:
		hostStr, containerStr = parts[0], parts[1]
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
	if hostStr == "" || hostStr == "0" {
		return parsedSpec{}, nil // random host port
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
	return parsedSpec{ports: out, anyOf: hHi > hLo && cHi == cLo}, nil
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

// isWildcard reports whether ip means "all interfaces": the zero value, or
// the unspecified 0.0.0.0 / :: that Docker reports for such bindings. Docker
// publishes on both families, so :: also overlaps IPv4 addresses.
func isWildcard(ip netip.Addr) bool { return !ip.IsValid() || ip.IsUnspecified() }

func (a HostPort) conflictsWith(b HostPort) bool {
	if a.Proto != b.Proto || a.Port != b.Port {
		return false
	}
	return isWildcard(a.IP) || isWildcard(b.IP) || a.IP == b.IP
}

// PortConflicts reports invalid specs and specs whose host ports are already
// in use, by inUse or by an earlier entry of wanted. field is the list's name;
// errors are attributed to "field[i]".
func PortConflicts(field string, wanted []string, inUse []PortUse) []FieldError {
	var errs []FieldError
	taken := append([]PortUse(nil), inUse...)
	for i, spec := range wanted {
		at := fmt.Sprintf("%s[%d]", field, i)
		parsed, err := parseSpec(spec)
		if err != nil {
			errs = append(errs, FieldError{Field: at, Message: err.Error()})
			continue
		}
		if c, ok := conflict(parsed, taken); ok {
			errs = append(errs, FieldError{
				Field:   at,
				Message: fmt.Sprintf("port %d/%s is already used by %s", c.Port, c.Proto, c.Owner),
			})
		}
		// A "pick any free port" range reserves nothing specific.
		if !parsed.anyOf {
			for _, p := range parsed.ports {
				taken = append(taken, PortUse{HostPort: p, Owner: at})
			}
		}
	}
	return errs
}

// conflict returns a blocking use. For an any-of range it conflicts only when
// every port in the range is taken; otherwise any single clash is a conflict.
func conflict(s parsedSpec, taken []PortUse) (PortUse, bool) {
	var first PortUse
	found := false
	for _, p := range s.ports {
		hit := false
		for _, u := range taken {
			if p.conflictsWith(u.HostPort) {
				hit = true
				if !found {
					first, found = u, true
				}
				break
			}
		}
		if s.anyOf && !hit {
			return PortUse{}, false
		}
		if !s.anyOf && hit {
			return first, true
		}
	}
	return first, found
}

// IPUse is an address already taken on a network.
type IPUse struct {
	Addr  netip.Addr
	Owner string // e.g. "container web"; may be empty
}

// CheckStaticIP validates a static IPv4 address for a compose network.
// used maps network name to the addresses already taken on it.
func CheckStaticIP(field, network, ip string, nets []engine.Network, used map[string][]IPUse) []FieldError {
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

	for _, u := range used[network] {
		if u.Addr != addr {
			continue
		}
		if u.Owner != "" {
			return fail("%s is already used by %s", ip, u.Owner)
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
