package composefile

import (
	"fmt"
	"reflect"
	"slices"

	"go.yaml.in/yaml/v3"
)

// ReadOnlyFieldError reports an attempt to change a field that can only be
// edited as raw YAML (see Service.ReadOnly).
type ReadOnlyFieldError struct {
	Field Field
}

func (e *ReadOnlyFieldError) Error() string {
	return fmt.Sprintf("field %q can only be edited as raw YAML", e.Field)
}

// UpdateService returns a new File in which service name has the settings in
// cfg. Only fields whose value changed are rewritten (and validated), each as
// a minimal text edit; the result is verified to differ from the original in
// exactly those fields before it is returned. When nothing changes it returns
// f itself.
//
// Callers can expect these errors: ErrServiceNotFound, *ReadOnlyFieldError (a
// changed field can only be edited as raw YAML), ErrInvalidConfig (a changed
// field has invalid values) and ErrEditRefused (the edit cannot be applied
// safely; use the raw YAML editor).
func (f *File) UpdateService(name string, cfg ServiceConfig) (*File, error) {
	cur, err := f.Service(name)
	if err != nil {
		return nil, err
	}
	node, err := f.serviceNode(name)
	if err != nil {
		return nil, err
	}
	old, cfg := normalize(cur.Config), normalize(cfg)
	at := func(keys ...string) []string { return append([]string{"services", name}, keys...) }

	var edits []edit
	for _, fd := range []struct {
		field    Field
		old, new any
		validate func() error // nil when the field needs no validation
		edits    func() []edit
	}{
		{FieldImage, old.Image, cfg.Image, nil, func() []edit { return scalarEdit(at("image"), cfg.Image, strNode) }},
		{FieldBuild, old.Build, cfg.Build, nil, func() []edit {
			if cfg.Build == nil {
				return []edit{{at("build"), nil, false}}
			}
			return []edit{{at("build"), buildNode(*cfg.Build), false}}
		}},
		{FieldPorts, old.Ports, cfg.Ports, nil, func() []edit { return listEdit(at("ports"), cfg.Ports, quotedNode) }},
		{FieldVolumes, old.Volumes, cfg.Volumes, nil, func() []edit { return listEdit(at("volumes"), cfg.Volumes, strNode) }},
		{FieldEnvironment, old.Environment, cfg.Environment, func() error { return validateEnvironment(cfg.Environment) }, func() []edit {
			if len(cfg.Environment) == 0 {
				return []edit{{at("environment"), nil, false}}
			}
			current := mappingValue(node, "environment")
			asList := current != nil && current.Kind == yaml.SequenceNode
			if perKey, ok := environmentKeyEdits(current, at("environment"), old.Environment, cfg.Environment); ok {
				return perKey
			}
			return []edit{{at("environment"), environmentNode(cfg.Environment, asList), false}}
		}},
		{FieldNetworks, old.Networks, cfg.Networks, func() error { return validateNetworks(cfg.Networks) }, func() []edit {
			if len(cfg.Networks) == 0 {
				return []edit{{at("networks"), nil, false}}
			}
			current := mappingValue(node, "networks")
			asMap := current != nil && current.Kind == yaml.MappingNode
			return []edit{{at("networks"), networksNode(cfg.Networks, asMap), false}}
		}},
		{FieldNetworkMode, old.NetworkMode, cfg.NetworkMode, nil, func() []edit {
			return scalarEdit(at("network_mode"), cfg.NetworkMode, strNode)
		}},
		{FieldLimits, old.Limits, cfg.Limits, nil, func() []edit { return limitsEdits(node, at, old.Limits, cfg.Limits) }},
		{FieldRestart, old.Restart, cfg.Restart, nil, func() []edit { return scalarEdit(at("restart"), cfg.Restart, strNode) }},
		{FieldHealthcheck, old.Healthcheck, cfg.Healthcheck, nil, func() []edit {
			if cfg.Healthcheck == nil {
				return []edit{{at("healthcheck"), nil, false}}
			}
			return []edit{{at("healthcheck"), healthcheckNode(*cfg.Healthcheck), false}}
		}},
	} {
		if reflect.DeepEqual(fd.old, fd.new) {
			continue
		}
		if fd.validate != nil {
			if err := fd.validate(); err != nil {
				return nil, err
			}
		}
		for _, ro := range cur.ReadOnly {
			if ro == fd.field {
				return nil, &ReadOnlyFieldError{Field: fd.field}
			}
		}
		edits = append(edits, fd.edits()...)
		if fd.field == FieldNetworks {
			edits = append(edits, undeclaredNetworkEdits(f.root, cfg.Networks)...)
		}
	}
	if len(edits) == 0 {
		return f, nil
	}

	out, err := applyEdits(f.src, edits)
	if err != nil {
		return nil, err
	}
	return Parse(out)
}

// undeclaredNetworkEdits declares each attached network that the file does not
// yet declare as external, as AddService does; existing declarations are never
// touched. Without it the project fails validation (undefined network).
func undeclaredNetworkEdits(root *yaml.Node, attached []NetworkAttachment) []edit {
	declared := mappingValue(root, "networks")
	var edits []edit
	for _, net := range attached {
		if mappingValue(declared, net.Name) == nil {
			edits = append(edits, edit{path: []string{"networks", net.Name}, value: mapNode("external", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})})
		}
	}
	return edits
}

func scalarEdit(path []string, v string, node func(string) *yaml.Node) []edit {
	if v == "" {
		return []edit{{path, nil, false}}
	}
	return []edit{{path, node(v), false}}
}

func listEdit(path []string, items []string, item func(string) *yaml.Node) []edit {
	if len(items) == 0 {
		return []edit{{path, nil, false}}
	}
	return []edit{{path, listNode(items, item), false}}
}

// environmentKeyEdits edits a block-mapping environment one variable at a
// time, so comments, blank lines and quoting around untouched variables
// survive. It applies only when the existing variables keep their relative
// order and any new ones come last (insertion appends); otherwise ok is false
// and the caller rewrites the whole field.
func environmentKeyEdits(current *yaml.Node, path []string, old, new []EnvVar) (edits []edit, ok bool) {
	if current == nil || current.Kind != yaml.MappingNode || current.Style&yaml.FlowStyle != 0 {
		return nil, false
	}
	oldValue := make(map[string]string, len(old))
	for _, v := range old {
		oldValue[v.Name] = v.Value
	}
	var kept []string // existing names in desired order, then check against old order
	var added []EnvVar
	for _, v := range new {
		if _, exists := oldValue[v.Name]; exists {
			if len(added) > 0 {
				return nil, false // an existing name after a new one: order would change
			}
			kept = append(kept, v.Name)
		} else {
			added = append(added, v)
		}
	}
	var oldKept []string
	for _, v := range old {
		if slices.ContainsFunc(new, func(n EnvVar) bool { return n.Name == v.Name }) {
			oldKept = append(oldKept, v.Name)
		}
	}
	if !slices.Equal(kept, oldKept) {
		return nil, false
	}
	keyPath := func(name string) []string { return append(slices.Clone(path), name) }
	for _, v := range old {
		if !slices.Contains(kept, v.Name) {
			edits = append(edits, edit{keyPath(v.Name), nil, false})
		}
	}
	for _, v := range new {
		if value, exists := oldValue[v.Name]; exists && value != v.Value {
			edits = append(edits, edit{keyPath(v.Name), strNode(v.Value), false})
		}
	}
	for _, v := range added {
		edits = append(edits, edit{keyPath(v.Name), strNode(v.Value), false})
	}
	return edits, true
}

// limitsEdits sets or removes deploy.resources.limits.{cpus,memory}. When
// every limit is cleared it deletes the outermost mapping that existed only to
// hold them, so no empty "deploy:" shell is left behind.
func limitsEdits(svc *yaml.Node, at func(...string) []string, old, new Limits) []edit {
	limitPath := func(key string) []string { return at("deploy", "resources", "limits", key) }
	if new == (Limits{}) {
		deploy := mappingValue(svc, "deploy")
		resources := mappingValue(deploy, "resources")
		limits := mappingValue(resources, "limits")
		switch {
		case hasOtherKeys(limits, "cpus", "memory"):
			return []edit{{limitPath("cpus"), nil, false}, {limitPath("memory"), nil, false}}
		case hasOtherKeys(resources, "limits"):
			return []edit{{at("deploy", "resources", "limits"), nil, false}}
		case hasOtherKeys(deploy, "resources"):
			return []edit{{at("deploy", "resources"), nil, false}}
		default:
			return []edit{{at("deploy"), nil, false}}
		}
	}
	var edits []edit
	if old.CPUs != new.CPUs {
		edits = append(edits, scalarEdit(limitPath("cpus"), new.CPUs, quotedNode)...)
	}
	if old.Memory != new.Memory {
		edits = append(edits, scalarEdit(limitPath("memory"), new.Memory, strNode)...)
	}
	return edits
}

// hasOtherKeys reports whether mapping m has any key not in keys.
func hasOtherKeys(m *yaml.Node, keys ...string) bool {
	if m == nil {
		return false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !slices.Contains(keys, m.Content[i].Value) {
			return true
		}
	}
	return false
}
