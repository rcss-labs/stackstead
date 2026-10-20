package composefile

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

var ErrInvalidServiceName = errors.New("service name must not be empty")

var ErrInvalidConfig = errors.New("invalid service config")

var ErrServiceExists = errors.New("service already exists")

// ValidateConfig rejects configs that would render duplicate or empty mapping
// keys, which Docker Compose refuses, or environment names that would not
// read back as written.
func ValidateConfig(cfg ServiceConfig) error {
	if err := validateEnvironment(cfg.Environment); err != nil {
		return err
	}
	return validateNetworks(cfg.Networks)
}

func validateEnvironment(vars []EnvVar) error {
	seen := map[string]bool{}
	for _, v := range vars {
		if v.Name == "" {
			return fmt.Errorf("%w: environment variable name is empty", ErrInvalidConfig)
		}
		if strings.Contains(v.Name, "=") || strings.ContainsFunc(v.Name, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r)
		}) {
			return fmt.Errorf("%w: environment variable name %q must not contain '=', whitespace or control characters", ErrInvalidConfig, v.Name)
		}
		if seen[v.Name] {
			return fmt.Errorf("%w: duplicate environment variable %q", ErrInvalidConfig, v.Name)
		}
		seen[v.Name] = true
	}
	return nil
}

func validateNetworks(nets []NetworkAttachment) error {
	seen := map[string]bool{}
	for _, n := range nets {
		if n.Name == "" {
			return fmt.Errorf("%w: network name is empty", ErrInvalidConfig)
		}
		if seen[n.Name] {
			return fmt.Errorf("%w: duplicate network %q", ErrInvalidConfig, n.Name)
		}
		seen[n.Name] = true
	}
	return nil
}

// strNode is a string scalar; the !!str tag makes the encoder quote values
// such as "true" or "8080" that would otherwise read back as other types.
func strNode(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

func quotedNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle}
}

func intNode(n int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(n)}
}

func emptyMapNode() *yaml.Node { return &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle} }

// mapNode builds a mapping from alternating key strings and value nodes,
// skipping pairs whose value is nil.
func mapNode(pairs ...any) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}
	for i := 0; i+1 < len(pairs); i += 2 {
		if v := pairs[i+1].(*yaml.Node); v != nil {
			m.Content = append(m.Content, strNode(pairs[i].(string)), v)
		}
	}
	return m
}

func listNode(items []string, item func(string) *yaml.Node) *yaml.Node {
	s := &yaml.Node{Kind: yaml.SequenceNode}
	for _, it := range items {
		s.Content = append(s.Content, item(it))
	}
	return s
}

// optional returns a node for v, or nil when v is empty.
func optional(v string, node func(string) *yaml.Node) *yaml.Node {
	if v == "" {
		return nil
	}
	return node(v)
}

func buildNode(b Build) *yaml.Node {
	if b.Dockerfile == "" {
		return strNode(b.Context)
	}
	return mapNode("context", strNode(b.Context), "dockerfile", strNode(b.Dockerfile))
}

func environmentNode(vars []EnvVar, asList bool) *yaml.Node {
	if asList {
		s := &yaml.Node{Kind: yaml.SequenceNode}
		for _, v := range vars {
			s.Content = append(s.Content, strNode(v.Name+"="+v.Value))
		}
		return s
	}
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, v := range vars {
		m.Content = append(m.Content, strNode(v.Name), strNode(v.Value))
	}
	return m
}

// networksNode uses the list form unless an attachment has a static IP or
// asMap asks to keep an existing mapping form.
func networksNode(nets []NetworkAttachment, asMap bool) *yaml.Node {
	for _, n := range nets {
		asMap = asMap || n.IPv4Address != ""
	}
	if !asMap {
		s := &yaml.Node{Kind: yaml.SequenceNode}
		for _, n := range nets {
			s.Content = append(s.Content, strNode(n.Name))
		}
		return s
	}
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, n := range nets {
		opts := emptyMapNode()
		if n.IPv4Address != "" {
			opts = mapNode("ipv4_address", strNode(n.IPv4Address))
		}
		m.Content = append(m.Content, strNode(n.Name), opts)
	}
	return m
}

func healthcheckNode(h Healthcheck) *yaml.Node {
	var retries *yaml.Node
	if h.Retries != 0 {
		retries = intNode(h.Retries)
	}
	var test *yaml.Node
	if len(h.Test) > 0 {
		test = listNode(h.Test, quotedNode)
		test.Style = yaml.FlowStyle
	}
	return mapNode(
		"test", test,
		"interval", optional(h.Interval, strNode),
		"timeout", optional(h.Timeout, strNode),
		"start_period", optional(h.StartPeriod, strNode),
		"retries", retries,
	)
}

func limitsNode(l Limits) *yaml.Node {
	return mapNode("cpus", optional(l.CPUs, quotedNode), "memory", optional(l.Memory, strNode))
}

// serviceNode renders a whole service in Stackstead's canonical key order.
func serviceNode(cfg ServiceConfig) *yaml.Node {
	cfg = normalize(cfg)
	var build, ports, volumes, env, nets, deploy, health *yaml.Node
	if cfg.Build != nil {
		build = buildNode(*cfg.Build)
	}
	if len(cfg.Ports) > 0 {
		ports = listNode(cfg.Ports, quotedNode)
	}
	if len(cfg.Volumes) > 0 {
		volumes = listNode(cfg.Volumes, strNode)
	}
	if len(cfg.Environment) > 0 {
		env = environmentNode(cfg.Environment, false)
	}
	if len(cfg.Networks) > 0 {
		nets = networksNode(cfg.Networks, false)
	}
	if cfg.Limits != (Limits{}) {
		deploy = mapNode("resources", mapNode("limits", limitsNode(cfg.Limits)))
	}
	if cfg.Healthcheck != nil {
		health = healthcheckNode(*cfg.Healthcheck)
	}
	return mapNode(
		"image", optional(cfg.Image, strNode),
		"build", build,
		"restart", optional(cfg.Restart, strNode),
		"ports", ports,
		"volumes", volumes,
		"environment", env,
		"networks", nets,
		"network_mode", optional(cfg.NetworkMode, strNode),
		"deploy", deploy,
		"healthcheck", health,
	)
}

// normalize maps every "not set" representation to one canonical zero value,
// so configs from forms (empty slices) compare equal to configs read from files (nil).
func normalize(cfg ServiceConfig) ServiceConfig {
	if len(cfg.Ports) == 0 {
		cfg.Ports = nil
	}
	if len(cfg.Volumes) == 0 {
		cfg.Volumes = nil
	}
	if len(cfg.Environment) == 0 {
		cfg.Environment = nil
	}
	if len(cfg.Networks) == 0 {
		cfg.Networks = nil
	}
	if cfg.Build != nil && *cfg.Build == (Build{}) {
		cfg.Build = nil
	}
	if cfg.Healthcheck != nil {
		h := *cfg.Healthcheck
		if len(h.Test) == 0 {
			h.Test = nil
		}
		if h.Test == nil && h.Interval == "" && h.Timeout == "" && h.StartPeriod == "" && h.Retries == 0 {
			cfg.Healthcheck = nil
		} else {
			cfg.Healthcheck = &h
		}
	}
	return cfg
}

// New renders a new compose file holding services in request order. Networks
// the services join are declared external once, sorted by name: Stackstead
// attaches services to networks that already exist on the host.
func New(services []NamedService) ([]byte, error) {
	return NewStack(services, Extra{})
}

// NewStack renders like New, then deep-merges each service's extra into its
// service and top into the file. The form's values win where both set a leaf.
// A network declared in top replaces the automatic external declaration.
func NewStack(services []NamedService, top Extra) ([]byte, error) {
	if len(services) == 0 {
		return nil, fmt.Errorf("%w: no services", ErrInvalidConfig)
	}
	if mappingValue(top.node, "services") != nil || mappingValue(top.node, "name") != nil {
		return nil, fmt.Errorf("%w: extra may not set services or name", ErrInvalidExtra)
	}
	declared := map[string]bool{}
	if top.node != nil {
		if nets := mappingValue(top.node, "networks"); nets != nil && nets.Kind == yaml.MappingNode {
			for i := 0; i < len(nets.Content); i += 2 {
				declared[nets.Content[i].Value] = true
			}
		}
	}
	serviceMap := &yaml.Node{Kind: yaml.MappingNode}
	seenServices := map[string]bool{}
	networkNames := map[string]bool{}
	for _, service := range services {
		if service.Name == "" {
			return nil, ErrInvalidServiceName
		}
		if seenServices[service.Name] {
			return nil, fmt.Errorf("%w: %s", ErrServiceExists, service.Name)
		}
		seenServices[service.Name] = true
		cfg := normalize(service.Config)
		if err := ValidateConfig(cfg); err != nil {
			return nil, fmt.Errorf("service %s: %w", service.Name, err)
		}
		node := serviceNode(cfg)
		if extraNets := mappingValue(service.Extra.node, "networks"); extraNets != nil && extraNets.Kind == yaml.MappingNode && len(cfg.Networks) > 0 {
			// the extra carries per-network options (aliases, ipv4_address), so
			// the form's networks must be a mapping for them to merge into
			for i := 0; i+1 < len(node.Content); i += 2 {
				if node.Content[i].Value == "networks" {
					node.Content[i+1] = networksNode(cfg.Networks, true)
				}
			}
		}
		if err := mergeExtra(node, service.Extra.node, "services."+service.Name); err != nil {
			return nil, err
		}
		serviceMap.Content = append(serviceMap.Content, strNode(service.Name), node)
		for _, network := range cfg.Networks {
			if !declared[network.Name] {
				networkNames[network.Name] = true
			}
		}
	}
	root := mapNode("services", serviceMap)
	if len(networkNames) > 0 {
		names := make([]string, 0, len(networkNames))
		for name := range networkNames {
			names = append(names, name)
		}
		sort.Strings(names)
		nets := &yaml.Node{Kind: yaml.MappingNode}
		for _, name := range names {
			nets.Content = append(nets.Content, strNode(name), mapNode("external", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}))
		}
		root.Content = append(root.Content, strNode("networks"), nets)
	}
	if err := mergeExtra(root, top.node, ""); err != nil {
		return nil, err
	}
	return []byte(encode(root, 2) + "\n"), nil
}
