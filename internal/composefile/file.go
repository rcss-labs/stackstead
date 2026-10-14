// Package composefile reads and edits Docker Compose files while preserving
// everything the author wrote that an edit does not touch: comments, blank
// lines, key order, quoting, indentation and keys Stackstead does not model.
package composefile

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"
)

var (
	ErrNotMapping        = errors.New("compose file must be a YAML mapping")
	ErrMultipleDocuments = errors.New("compose file must contain a single YAML document")
	ErrServiceNotFound   = errors.New("service not found")
)

// File is a parsed compose file. It keeps the original bytes so edits can be
// applied as minimal text changes.
type File struct {
	src  []byte
	root *yaml.Node // top-level mapping
}

func Parse(src []byte) (*File, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, ErrNotMapping
		}
		return nil, fmt.Errorf("parse compose file: %w", err)
	}
	for {
		var extra yaml.Node
		err := dec.Decode(&extra)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse compose file: %w", err)
		}
		if !isEmptyDocument(&extra) { // a trailing "---" with nothing after it is harmless
			return nil, ErrMultipleDocuments
		}
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, ErrNotMapping
	}
	return &File{src: src, root: doc.Content[0]}, nil
}

// Bytes returns the file's current content.
func (f *File) Bytes() []byte { return bytes.Clone(f.src) }

// Services returns the service names in file order.
func (f *File) Services() ([]string, error) {
	services := mappingValue(f.root, "services")
	if services == nil {
		return nil, nil
	}
	if services.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: services must be a mapping", ErrNotMapping)
	}
	names := make([]string, 0, len(services.Content)/2)
	for i := 0; i < len(services.Content); i += 2 {
		names = append(names, services.Content[i].Value)
	}
	return names, nil
}

func (f *File) serviceNode(name string) (*yaml.Node, error) {
	services := mappingValue(f.root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: %s", ErrServiceNotFound, name)
	}
	svc := mappingValue(services, name)
	if svc == nil {
		return nil, fmt.Errorf("%w: %s", ErrServiceNotFound, name)
	}
	if svc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: service %s must be a mapping", ErrNotMapping, name)
	}
	return svc, nil
}

// mappingValue returns the value for key in mapping m, or nil (also when m is nil).
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// isEmptyDocument reports whether doc has no content (the decoder yields an
// empty null scalar for a bare "---").
func isEmptyDocument(doc *yaml.Node) bool {
	if len(doc.Content) == 0 {
		return true
	}
	n := doc.Content[0]
	return len(doc.Content) == 1 && n.Kind == yaml.ScalarNode && n.Value == "" && n.ShortTag() == "!!null"
}
