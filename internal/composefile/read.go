package composefile

import (
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// fieldReader parses one field's value node into cfg, returning false (and
// leaving cfg untouched) when the value has a shape the model cannot hold.
type fieldReader struct {
	field Field
	key   string
	read  func(v *yaml.Node, cfg *ServiceConfig) bool
}

var fieldReaders = []fieldReader{
	{FieldImage, "image", func(v *yaml.Node, cfg *ServiceConfig) bool { return readString(v, &cfg.Image) }},
	{FieldBuild, "build", readBuild},
	{FieldPorts, "ports", func(v *yaml.Node, cfg *ServiceConfig) bool { return readStringList(v, &cfg.Ports) }},
	{FieldVolumes, "volumes", func(v *yaml.Node, cfg *ServiceConfig) bool { return readStringList(v, &cfg.Volumes) }},
	{FieldEnvironment, "environment", readEnvironment},
	{FieldNetworks, "networks", readNetworks},
	{FieldNetworkMode, "network_mode", func(v *yaml.Node, cfg *ServiceConfig) bool { return readString(v, &cfg.NetworkMode) }},
	{FieldLimits, "deploy", readLimits},
	{FieldRestart, "restart", func(v *yaml.Node, cfg *ServiceConfig) bool { return readString(v, &cfg.Restart) }},
	{FieldHealthcheck, "healthcheck", readHealthcheck},
}

// legacyLimitKeys set limits outside deploy.resources; editing deploy limits
// while they exist would leave two conflicting sources of truth.
var legacyLimitKeys = []string{"cpus", "mem_limit"}

// Service reads one service's form-editable settings.
func (f *File) Service(name string) (Service, error) {
	node, err := f.serviceNode(name)
	if err != nil {
		return Service{}, err
	}
	svc := Service{Name: name}

	var inherited map[string]any // keys the service gets from merge keys ("<<")
	if mappingValue(node, "<<") != nil {
		var full map[string]any
		if err := node.Decode(&full); err == nil {
			inherited = full
		}
	}

	for _, fr := range fieldReaders {
		v := mappingValue(node, fr.key)
		var readOnly bool
		switch {
		case v == nil:
			_, readOnly = inherited[fr.key]
		case usesAnchors(v):
			readOnly = true
		default:
			readOnly = !fr.read(v, &svc.Config)
		}
		if fr.field == FieldLimits && hasAnyKey(node, legacyLimitKeys) {
			svc.Config.Limits = Limits{}
			readOnly = true
		}
		if readOnly {
			svc.ReadOnly = append(svc.ReadOnly, fr.field)
		}
	}
	return svc, nil
}

func usesAnchors(n *yaml.Node) bool {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return true
	}
	for _, c := range n.Content {
		if usesAnchors(c) {
			return true
		}
	}
	return false
}

func hasAnyKey(m *yaml.Node, keys []string) bool {
	for _, k := range keys {
		if mappingValue(m, k) != nil {
			return true
		}
	}
	return false
}

func isScalar(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.ShortTag() != "!!null"
}

func readString(v *yaml.Node, dst *string) bool {
	if !isScalar(v) {
		return false
	}
	*dst = v.Value
	return true
}

func readStringList(v *yaml.Node, dst *[]string) bool {
	if v.Kind != yaml.SequenceNode {
		return false
	}
	items := make([]string, 0, len(v.Content))
	for _, item := range v.Content {
		if !isScalar(item) {
			return false
		}
		items = append(items, item.Value)
	}
	*dst = items
	return true
}

// onlyKeys reports whether every key of mapping m is in allowed and every
// value is a non-null scalar.
func onlyScalarKeys(m *yaml.Node, allowed ...string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		ok := false
		for _, a := range allowed {
			ok = ok || m.Content[i].Value == a
		}
		if !ok || !isScalar(m.Content[i+1]) {
			return false
		}
	}
	return true
}

func readBuild(v *yaml.Node, cfg *ServiceConfig) bool {
	switch {
	case isScalar(v):
		cfg.Build = &Build{Context: v.Value}
		return true
	case v.Kind == yaml.MappingNode && onlyScalarKeys(v, "context", "dockerfile"):
		b := &Build{}
		readString(orNull(mappingValue(v, "context")), &b.Context)
		readString(orNull(mappingValue(v, "dockerfile")), &b.Dockerfile)
		cfg.Build = b
		return true
	}
	return false
}

func orNull(n *yaml.Node) *yaml.Node {
	if n == nil {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}
	}
	return n
}

func readEnvironment(v *yaml.Node, cfg *ServiceConfig) bool {
	var vars []EnvVar
	switch v.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(v.Content); i += 2 {
			if !isScalar(v.Content[i+1]) {
				return false // null means "pass through from the host", which the model cannot express
			}
			vars = append(vars, EnvVar{Name: v.Content[i].Value, Value: v.Content[i+1].Value})
		}
	case yaml.SequenceNode:
		for _, item := range v.Content {
			if !isScalar(item) {
				return false
			}
			name, value, ok := strings.Cut(item.Value, "=")
			if !ok {
				return false
			}
			vars = append(vars, EnvVar{Name: name, Value: value})
		}
	default:
		return false
	}
	cfg.Environment = vars
	return true
}

func readNetworks(v *yaml.Node, cfg *ServiceConfig) bool {
	var nets []NetworkAttachment
	switch v.Kind {
	case yaml.SequenceNode:
		for _, item := range v.Content {
			if !isScalar(item) {
				return false
			}
			nets = append(nets, NetworkAttachment{Name: item.Value})
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(v.Content); i += 2 {
			att := NetworkAttachment{Name: v.Content[i].Value}
			switch opts := v.Content[i+1]; {
			case opts.Kind == yaml.ScalarNode && opts.ShortTag() == "!!null":
			case opts.Kind == yaml.MappingNode && onlyScalarKeys(opts, "ipv4_address"):
				readString(orNull(mappingValue(opts, "ipv4_address")), &att.IPv4Address)
			default:
				return false
			}
			nets = append(nets, att)
		}
	default:
		return false
	}
	cfg.Networks = nets
	return true
}

// readLimits reads deploy.resources.limits.{cpus,memory}; other deploy
// settings are left alone and do not make the field read-only.
func readLimits(deploy *yaml.Node, cfg *ServiceConfig) bool {
	var lim Limits
	node := deploy
	for _, key := range []string{"resources", "limits"} {
		if node.Kind != yaml.MappingNode {
			return false
		}
		if node = mappingValue(node, key); node == nil {
			cfg.Limits = lim
			return true
		}
	}
	if node.Kind != yaml.MappingNode {
		return false
	}
	if v := mappingValue(node, "cpus"); v != nil && !readString(v, &lim.CPUs) {
		return false
	}
	if v := mappingValue(node, "memory"); v != nil && !readString(v, &lim.Memory) {
		return false
	}
	cfg.Limits = lim
	return true
}

func readHealthcheck(v *yaml.Node, cfg *ServiceConfig) bool {
	if v.Kind != yaml.MappingNode {
		return false
	}
	h := &Healthcheck{}
	for i := 0; i+1 < len(v.Content); i += 2 {
		key, val := v.Content[i].Value, v.Content[i+1]
		var ok bool
		switch key {
		case "test":
			if isScalar(val) {
				h.Test, ok = []string{"CMD-SHELL", val.Value}, true
			} else {
				ok = readStringList(val, &h.Test)
			}
		case "interval":
			ok = readString(val, &h.Interval)
		case "timeout":
			ok = readString(val, &h.Timeout)
		case "start_period":
			ok = readString(val, &h.StartPeriod)
		case "retries":
			if isScalar(val) {
				n, err := strconv.Atoi(val.Value)
				h.Retries, ok = n, err == nil
			}
		}
		if !ok {
			return false
		}
	}
	cfg.Healthcheck = h
	return true
}
