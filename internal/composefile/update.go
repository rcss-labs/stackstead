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
// cfg. Only fields whose value changed are rewritten, each as a minimal text
// edit; the result is verified to differ from the original in exactly those
// fields before it is returned.
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
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	at := func(keys ...string) []string { return append([]string{"services", name}, keys...) }

	var edits []edit
	for _, fd := range []struct {
		field    Field
		old, new any
		edits    func() []edit
	}{
		{FieldImage, old.Image, cfg.Image, func() []edit { return scalarEdit(at("image"), cfg.Image, strNode) }},
		{FieldBuild, old.Build, cfg.Build, func() []edit {
			if cfg.Build == nil {
				return []edit{{at("build"), nil}}
			}
			return []edit{{at("build"), buildNode(*cfg.Build)}}
		}},
		{FieldPorts, old.Ports, cfg.Ports, func() []edit { return listEdit(at("ports"), cfg.Ports, quotedNode) }},
		{FieldVolumes, old.Volumes, cfg.Volumes, func() []edit { return listEdit(at("volumes"), cfg.Volumes, strNode) }},
		{FieldEnvironment, old.Environment, cfg.Environment, func() []edit {
			if len(cfg.Environment) == 0 {
				return []edit{{at("environment"), nil}}
			}
			current := mappingValue(node, "environment")
			asList := current != nil && current.Kind == yaml.SequenceNode
			return []edit{{at("environment"), environmentNode(cfg.Environment, asList)}}
		}},
		{FieldNetworks, old.Networks, cfg.Networks, func() []edit {
			if len(cfg.Networks) == 0 {
				return []edit{{at("networks"), nil}}
			}
			current := mappingValue(node, "networks")
			asMap := current != nil && current.Kind == yaml.MappingNode
			return []edit{{at("networks"), networksNode(cfg.Networks, asMap)}}
		}},
		{FieldNetworkMode, old.NetworkMode, cfg.NetworkMode, func() []edit {
			return scalarEdit(at("network_mode"), cfg.NetworkMode, strNode)
		}},
		{FieldLimits, old.Limits, cfg.Limits, func() []edit { return limitsEdits(node, at, old.Limits, cfg.Limits) }},
		{FieldRestart, old.Restart, cfg.Restart, func() []edit { return scalarEdit(at("restart"), cfg.Restart, strNode) }},
		{FieldHealthcheck, old.Healthcheck, cfg.Healthcheck, func() []edit {
			if cfg.Healthcheck == nil {
				return []edit{{at("healthcheck"), nil}}
			}
			return []edit{{at("healthcheck"), healthcheckNode(*cfg.Healthcheck)}}
		}},
	} {
		if reflect.DeepEqual(fd.old, fd.new) {
			continue
		}
		for _, ro := range cur.ReadOnly {
			if ro == fd.field {
				return nil, &ReadOnlyFieldError{Field: fd.field}
			}
		}
		edits = append(edits, fd.edits()...)
	}
	if len(edits) == 0 {
		return f, nil
	}

	out, err := applyEdits(f.src, edits)
	if err != nil {
		return nil, err
	}
	if err := verifyEdits(f.src, out, edits); err != nil {
		return nil, err
	}
	return Parse(out)
}

func scalarEdit(path []string, v string, node func(string) *yaml.Node) []edit {
	if v == "" {
		return []edit{{path, nil}}
	}
	return []edit{{path, node(v)}}
}

func listEdit(path []string, items []string, item func(string) *yaml.Node) []edit {
	if len(items) == 0 {
		return []edit{{path, nil}}
	}
	return []edit{{path, listNode(items, item)}}
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
			return []edit{{limitPath("cpus"), nil}, {limitPath("memory"), nil}}
		case hasOtherKeys(resources, "limits"):
			return []edit{{at("deploy", "resources", "limits"), nil}}
		case hasOtherKeys(deploy, "resources"):
			return []edit{{at("deploy", "resources"), nil}}
		default:
			return []edit{{at("deploy"), nil}}
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
