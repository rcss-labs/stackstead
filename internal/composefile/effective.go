package composefile

import (
	"fmt"
	"slices"
	"strings"
)

// BuildRef is a service's build reference as written in the file.
type BuildRef struct {
	Context    string // as written; "" means the compose default "."
	Dockerfile string // "" means "Dockerfile"
	Inline     bool   // dockerfile_inline is set
	// AdditionalContexts maps build.additional_contexts names to their values
	// as written (paths, URLs or docker-image:// references).
	AdditionalContexts map[string]string
}

// decodedService returns the service with aliases and merge keys resolved.
func (f *File) decodedService(name string) (map[string]any, error) {
	node, err := f.serviceNode(name)
	if err != nil {
		return nil, err
	}
	var full map[string]any
	if err := node.Decode(&full); err != nil {
		return nil, fmt.Errorf("%w: service %s: %v", ErrNotMapping, name, err)
	}
	return normalizeKeys(full).(map[string]any), nil
}

// normalizeKeys converts the map[any]any that yaml.v3 produces for mappings
// with non-string keys (e.g. a numeric network name) into map[string]any, so
// no entry is silently dropped.
func normalizeKeys(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, item := range x {
			x[k] = normalizeKeys(item)
		}
		return x
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			out[fmt.Sprint(k)] = normalizeKeys(item)
		}
		return out
	case []any:
		for i, item := range x {
			x[i] = normalizeKeys(item)
		}
		return x
	}
	return v
}

// scalarString renders a decoded YAML scalar; maps and lists are an
// ErrNotMapping-wrapped error, never a formatted Go value.
func scalarString(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	case map[string]any, []any:
		return "", fmt.Errorf("%w: expected a scalar value", ErrNotMapping)
	default:
		return fmt.Sprint(x), nil
	}
}

// BuildOf reads a service's build reference in any syntax. ok is false when
// the service has no build.
func (f *File) BuildOf(service string) (ref BuildRef, ok bool, err error) {
	full, err := f.decodedService(service)
	if err != nil {
		return BuildRef{}, false, err
	}
	raw, present := full["build"]
	if !present || raw == nil {
		return BuildRef{}, false, nil
	}
	switch b := raw.(type) {
	case map[string]any:
		for _, k := range []struct {
			key string
			dst *string
		}{{"context", &ref.Context}, {"dockerfile", &ref.Dockerfile}} {
			s, err := scalarString(b[k.key])
			if err != nil {
				return BuildRef{}, false, fmt.Errorf("service %s: build.%s: %w", service, k.key, err)
			}
			*k.dst = s
		}
		if ref.AdditionalContexts, err = additionalContexts(b["additional_contexts"]); err != nil {
			return BuildRef{}, false, fmt.Errorf("service %s: build.additional_contexts: %w", service, err)
		}
		if v, set := b["dockerfile_inline"]; set && v != nil {
			ref.Inline = true
		}
		return ref, true, nil
	case []any:
		return BuildRef{}, false, fmt.Errorf("%w: service %s: build must be a string or mapping", ErrNotMapping, service)
	default:
		s, _ := scalarString(b)
		return BuildRef{Context: s}, true, nil
	}
}

// additionalContexts reads build.additional_contexts in mapping form
// (name: path) or list form ("name=path").
func additionalContexts(raw any) (map[string]string, error) {
	var out map[string]string
	switch c := raw.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		out = make(map[string]string, len(c))
		for name, v := range c {
			s, err := scalarString(v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			out[name] = s
		}
	case []any:
		out = make(map[string]string, len(c))
		for _, item := range c {
			s, err := scalarString(item)
			if err != nil {
				return nil, err
			}
			name, value, found := strings.Cut(s, "=")
			if !found {
				return nil, fmt.Errorf("%w: %q is not name=path", ErrNotMapping, s)
			}
			out[name] = value
		}
	default:
		return nil, fmt.Errorf("%w: must be a mapping or list", ErrNotMapping)
	}
	return out, nil
}

// UsesExtends reports whether the service has an extends key.
func (f *File) UsesExtends(service string) bool {
	node, err := f.serviceNode(service)
	return err == nil && mappingValue(node, "extends") != nil
}

// UsesInclude reports whether the file has a top-level include.
func (f *File) UsesInclude() bool {
	return mappingValue(f.root, "include") != nil
}

// EffectiveService is Service(name).Config plus, for the build, ports and
// networks groups that Service marked read-only, the values read from any
// syntax (long form, aliases, merge keys). Security checks use it so that
// values the form model cannot hold are still seen.
func (f *File) EffectiveService(name string) (ServiceConfig, error) {
	svc, err := f.Service(name)
	if err != nil {
		return ServiceConfig{}, err
	}
	cfg := svc.Config
	full, err := f.decodedService(name)
	if err != nil {
		return ServiceConfig{}, err
	}
	for _, field := range svc.ReadOnly {
		switch field {
		case FieldBuild:
			ref, ok, err := f.BuildOf(name)
			if err != nil {
				return ServiceConfig{}, err
			}
			if ok {
				cfg.Build = &Build{Context: ref.Context, Dockerfile: ref.Dockerfile}
			}
		case FieldPorts:
			if cfg.Ports, err = effectivePorts(full["ports"]); err != nil {
				return ServiceConfig{}, fmt.Errorf("service %s: %w", name, err)
			}
		case FieldNetworks:
			if cfg.Networks, err = f.effectiveNetworks(name, full["networks"]); err != nil {
				return ServiceConfig{}, fmt.Errorf("service %s: %w", name, err)
			}
		}
	}
	return cfg, nil
}

func effectivePorts(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: ports must be a list", ErrNotMapping)
	}
	ports := make([]string, 0, len(list))
	for _, entry := range list {
		switch e := entry.(type) {
		case map[string]any:
			spec, err := longPortSpec(e)
			if err != nil {
				return nil, err
			}
			ports = append(ports, spec)
		default:
			s, err := scalarString(e)
			if err != nil {
				return nil, err
			}
			ports = append(ports, s)
		}
	}
	return ports, nil
}

// longPortSpec renders [host_ip:]published:target[/protocol], or the bare
// target when nothing is published.
func longPortSpec(e map[string]any) (string, error) {
	var parts [4]string // target, published, host_ip, protocol
	for i, key := range []string{"target", "published", "host_ip", "protocol"} {
		s, err := scalarString(e[key])
		if err != nil {
			return "", fmt.Errorf("port %s: %w", key, err)
		}
		parts[i] = s
	}
	target, published, hostIP, protocol := parts[0], parts[1], parts[2], strings.ToLower(parts[3])
	if target == "" {
		return "", fmt.Errorf("%w: long port entry has no target", ErrNotMapping)
	}
	if published == "" {
		return target, nil
	}
	spec := published + ":" + target
	if hostIP != "" {
		if strings.Contains(hostIP, ":") {
			hostIP = "[" + hostIP + "]"
		}
		spec = hostIP + ":" + spec
	}
	if protocol != "" && protocol != "tcp" {
		spec += "/" + protocol
	}
	return spec, nil
}

// effectiveNetworks reads attachments in list or mapping form. File order is
// kept when the service writes networks itself; attachments inherited through
// a merge key come back sorted by name.
func (f *File) effectiveNetworks(service string, raw any) ([]NetworkAttachment, error) {
	var names []string
	switch n := raw.(type) {
	case []any:
		var out []NetworkAttachment
		for _, item := range n {
			s, err := scalarString(item)
			if err != nil {
				return nil, err
			}
			out = append(out, NetworkAttachment{Name: s})
		}
		return out, nil
	case map[string]any:
		names = f.networkOrder(service, n)
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("%w: networks must be a list or mapping", ErrNotMapping)
	}
	out := make([]NetworkAttachment, 0, len(names))
	attachments := raw.(map[string]any)
	for _, name := range names {
		att := NetworkAttachment{Name: name}
		if cfg, ok := attachments[name].(map[string]any); ok {
			s, err := scalarString(cfg["ipv4_address"])
			if err != nil {
				return nil, fmt.Errorf("ipv4_address: %w", err)
			}
			att.IPv4Address = s
		}
		out = append(out, att)
	}
	return out, nil
}

// networkOrder returns the keys of the decoded networks mapping in the order
// the service's own networks node lists them, then any remaining keys sorted.
func (f *File) networkOrder(service string, decoded map[string]any) []string {
	var ordered []string
	if node, err := f.serviceNode(service); err == nil {
		if v := mappingValue(node, "networks"); v != nil {
			if v.Alias != nil {
				v = v.Alias
			}
			for i := 0; i+1 < len(v.Content); i += 2 {
				if _, ok := decoded[v.Content[i].Value]; ok {
					ordered = append(ordered, v.Content[i].Value)
				}
			}
		}
	}
	var rest []string
	for k := range decoded {
		if !slices.Contains(ordered, k) {
			rest = append(rest, k)
		}
	}
	slices.Sort(rest)
	return append(ordered, rest...)
}
