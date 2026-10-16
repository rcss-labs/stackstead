package api

import (
	"fmt"
	"net/netip"
	"regexp"

	"github.com/rcss-labs/stackstead/internal/apitypes"
	"github.com/rcss-labs/stackstead/internal/engine"
	"github.com/rcss-labs/stackstead/internal/stacks"
)

var networkNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
var interfaceNameRe = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,15}$`)

// validateNetworkCreate is pure; each field contributes at most one error.
func validateNetworkCreate(req apitypes.NetworkCreateRequest, existing []engine.Network) []apitypes.FieldError {
	fields := make([]apitypes.FieldError, 0)
	fail := func(field, message string) {
		fields = append(fields, apitypes.FieldError{Field: field, Message: message})
	}
	if !networkNameRe.MatchString(req.Name) {
		fail("name", "name must be 1–63 letters, digits, underscores, dots or hyphens, starting with a letter or digit")
	}
	if req.Driver != "bridge" && req.Driver != "macvlan" && req.Driver != "ipvlan" {
		fail("driver", "driver must be bridge, macvlan or ipvlan")
	}
	subnet, err := netip.ParsePrefix(req.Subnet)
	validSubnet := err == nil && subnet.Addr().Is4() && subnet == subnet.Masked()
	switch {
	case req.Subnet == "":
		if req.Driver == "macvlan" || req.Driver == "ipvlan" {
			fail("subnet", "subnet is required for macvlan and ipvlan")
		}
	case err != nil || !subnet.Addr().Is4():
		fail("subnet", "subnet must be an IPv4 CIDR")
	case subnet != subnet.Masked():
		fail("subnet", "subnet must be in masked form")
	default:
	overlap:
		for _, n := range existing {
			// An exact duplicate is a name conflict, including repeated identical creates.
			if n.Name == req.Name {
				continue
			}
			for _, s := range n.Subnets {
				if s.Prefix.Addr().Is4() && subnet.Overlaps(s.Prefix) {
					fail("subnet", fmt.Sprintf("subnet overlaps network %s", n.Name))
					break overlap
				}
			}
		}
	}
	if req.Gateway != "" {
		if !validSubnet {
			fail("gateway", "gateway requires a valid subnet")
		} else {
			synthetic := []engine.Network{{Name: req.Name, Subnets: []engine.Subnet{{Prefix: subnet}}}}
			for _, f := range stacks.CheckStaticIP("gateway", req.Name, req.Gateway, synthetic, nil) {
				fields = append(fields, apitypes.FieldError(f))
			}
		}
	}
	if req.IPRange != "" {
		p, e := netip.ParsePrefix(req.IPRange)
		switch {
		case !validSubnet:
			fail("ipRange", "ipRange requires a valid subnet")
		case e != nil || !p.Addr().Is4():
			fail("ipRange", "ipRange must be an IPv4 CIDR")
		case p.Bits() < subnet.Bits() || !subnet.Contains(p.Masked().Addr()):
			fail("ipRange", "ipRange must be inside subnet")
		}
	}
	switch {
	case req.Driver == "bridge" && req.Parent != "":
		fail("parent", "parent is forbidden for bridge networks")
	case (req.Driver == "macvlan" || req.Driver == "ipvlan") && req.Parent == "":
		fail("parent", "parent is required for macvlan and ipvlan")
	case req.Parent != "" && !interfaceNameRe.MatchString(req.Parent):
		fail("parent", "parent must be a valid interface name of at most 15 characters")
	}
	if req.IPvlanMode != "" {
		if req.Driver != "ipvlan" {
			fail("ipvlanMode", "ipvlanMode is only allowed for ipvlan")
		} else if req.IPvlanMode != "l2" && req.IPvlanMode != "l3" {
			fail("ipvlanMode", "ipvlanMode must be l2 or l3")
		}
	}
	return fields
}

func toNetworkSpec(req apitypes.NetworkCreateRequest) engine.NetworkSpec {
	spec := engine.NetworkSpec{Name: req.Name, Driver: req.Driver, Subnet: req.Subnet, Gateway: req.Gateway, IPRange: req.IPRange, Internal: req.Internal}
	if req.Driver == "macvlan" || req.Driver == "ipvlan" {
		spec.Parent = req.Parent
	}
	if req.Driver == "ipvlan" {
		spec.IPvlanMode = req.IPvlanMode
		if spec.IPvlanMode == "" {
			spec.IPvlanMode = "l2"
		}
	}
	return spec
}
