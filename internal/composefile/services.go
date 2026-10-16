package composefile

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

var ErrLastService = errors.New("the last service cannot be removed; delete the stack instead")

// DependentsError reports the services that still require Service.
type DependentsError struct {
	Service    string
	Dependents []string
}

func (e *DependentsError) Error() string {
	return fmt.Sprintf("service %s is required by %s (depends_on)", e.Service, strings.Join(e.Dependents, ", "))
}

// AddService appends a service and declarations for any undeclared networks,
// preserving the existing file's text through the structured edit pipeline.
func (f *File) AddService(name string, cfg ServiceConfig) (*File, error) {
	if name == "" {
		return nil, ErrInvalidServiceName
	}
	cfg = normalize(cfg)
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	services := mappingValue(f.root, "services")
	if services != nil && services.Kind == yaml.MappingNode && mappingValue(services, name) != nil {
		return nil, fmt.Errorf("%w: %s", ErrServiceExists, name)
	}
	edits := []edit{{path: []string{"services", name}, value: serviceNode(cfg)}}
	networks := mappingValue(f.root, "networks")
	for _, net := range cfg.Networks {
		if mappingValue(networks, net.Name) == nil {
			edits = append(edits, edit{path: []string{"networks", net.Name}, value: mapNode("external", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})})
		}
	}
	out, err := applyEdits(f.src, edits)
	if err != nil {
		return nil, err
	}
	return Parse(out)
}

// RemoveService removes a service and its attached head comments. Declarations
// such as top-level networks and volumes are deliberately retained.
func (f *File) RemoveService(name string) (*File, error) {
	names, err := f.Services()
	if err != nil {
		return nil, err
	}
	if !slices.Contains(names, name) {
		return nil, fmt.Errorf("%w: %s", ErrServiceNotFound, name)
	}
	if len(names) == 1 && !hasMergeKey(mappingValue(f.root, "services")) {
		return nil, ErrLastService
	}
	dependents, err := f.dependentsOf(name)
	if len(dependents) > 0 {
		return nil, &DependentsError{Service: name, Dependents: dependents}
	}
	if err != nil {
		return nil, err
	}
	out, err := applyEdits(f.src, []edit{{path: []string{"services", name}, removeHead: true}})
	if err != nil {
		return nil, err
	}
	return Parse(out)
}

// DependentsOf returns understood depends_on references in file order. Removal
// also checks the analysis error, refusing shapes this convenience API cannot
// represent safely.
func (f *File) DependentsOf(name string) []string {
	names, _ := f.dependentsOf(name)
	return names
}

func (f *File) dependentsOf(name string) ([]string, error) {
	services := mappingValue(f.root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil, ErrEditRefused
	}
	var names []string
	var analysisErr error
	for i := 0; i+1 < len(services.Content); i += 2 {
		key, svc := services.Content[i], services.Content[i+1]
		if key.ShortTag() == "!!merge" || key.Kind != yaml.ScalarNode {
			analysisErr = ErrEditRefused
			continue
		}
		if key.Value == name {
			continue
		}
		svc, err := resolveDependencyAlias(svc)
		if err != nil || svc.Kind != yaml.MappingNode {
			analysisErr = ErrEditRefused
			continue
		}
		if hasMergeKey(svc) {
			analysisErr = ErrEditRefused
		}
		dep := mappingValue(svc, "depends_on")
		if dep == nil {
			continue
		}
		found, err := dependencyContains(dep, name)
		if found {
			names = append(names, key.Value)
		}
		if err != nil {
			analysisErr = err
		}
	}
	return names, analysisErr
}

// Aliases have no effect on dependency meaning, but malformed or cyclic chains
// must not turn the safety check into unbounded work.
func resolveDependencyAlias(n *yaml.Node) (*yaml.Node, error) {
	seen := map[*yaml.Node]bool{}
	for depth := 0; n != nil && depth < 64; depth++ {
		if seen[n] {
			return nil, ErrEditRefused
		}
		if n.Kind != yaml.AliasNode {
			return n, nil
		}
		seen[n] = true
		n = n.Alias
	}
	return nil, ErrEditRefused
}

func hasMergeKey(n *yaml.Node) bool {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].ShortTag() == "!!merge" {
			return true
		}
	}
	return false
}

func dependencyContains(n *yaml.Node, name string) (bool, error) {
	n, err := resolveDependencyAlias(n)
	if err != nil {
		return false, err
	}
	found := false
	var analysisErr error
	switch n.Kind {
	case yaml.SequenceNode:
		for _, item := range n.Content {
			item, err = resolveDependencyAlias(item)
			if err != nil || item.Kind != yaml.ScalarNode {
				analysisErr = ErrEditRefused
				continue
			}
			found = found || item.Value == name
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.ShortTag() == "!!merge" {
				analysisErr = ErrEditRefused
				continue
			}
			found = found || key.Value == name
		}
	default:
		return false, ErrEditRefused
	}
	return found, analysisErr
}
