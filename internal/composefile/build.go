package composefile

import (
	"errors"
	"fmt"
	"strconv"

	"go.yaml.in/yaml/v3"
)

var ErrInvalidServiceName = errors.New("service name must not be empty")

var ErrInvalidConfig = errors.New("invalid service config")

// ValidateConfig rejects configs that would render duplicate or empty mapping
// keys, which Docker Compose refuses.
func ValidateConfig(cfg ServiceConfig) error {
	seenEnv := map[string]bool{}
	for _, v := range cfg.Environment {
		if v.Name == "" {
			return fmt.Errorf("%w: environment variable name is empty", ErrInvalidConfig)
		}
		if seenEnv[v.Name] {
			return fmt.Errorf("%w: duplicate environment variable %q", ErrInvalidConfig, v.Name)
		}
		seenEnv[v.Name] = true
	}
	seenNet := map[string]bool{}
	for _, n := range cfg.Networks {
		if n.Name == "" {
			return fmt.Errorf("%w: network name is empty", ErrInvalidConfig)
		}
		if seenNet[n.Name] {
			return fmt.Errorf("%w: duplicate network %q", ErrInvalidConfig, n.Name)
		}
		seenNet[n.Name] = true
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

// New renders a new compose file holding one service. Networks the service
// joins are declared external: Stackstead attaches services to networks that
// already exist on the host.
func New(name string, cfg ServiceConfig) ([]byte, error) {
	if name == "" {
		return nil, ErrInvalidServiceName
	}
	cfg = normalize(cfg)
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	root := mapNode("services", mapNode(name, serviceNode(cfg)))
	if len(cfg.Networks) > 0 {
		nets := &yaml.Node{Kind: yaml.MappingNode}
		for _, n := range cfg.Networks {
			nets.Content = append(nets.Content, strNode(n.Name), mapNode("external", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}))
		}
		root.Content = append(root.Content, strNode("networks"), nets)
	}
	return []byte(encode(root, 2) + "\n"), nil
}
