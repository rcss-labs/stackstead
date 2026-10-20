package composefile

import (
	"bytes"
	"errors"
	"fmt"

	"go.yaml.in/yaml/v3"
)

// ErrInvalidExtra reports an extra that is not a JSON object or that sets
// keys the server owns.
var ErrInvalidExtra = errors.New("extra must be a JSON object")

// Extra holds compose keys the forms do not model, to be merged into a
// service or the top level of a rendered file. The zero value is empty.
type Extra struct {
	node *yaml.Node // block-style mapping, nil when empty
}

// ParseExtra reads a JSON object. Empty input and null give the zero Extra;
// anything but an object is ErrInvalidExtra. Key order is kept.
func ParseExtra(raw []byte) (Extra, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return Extra{}, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return Extra{}, fmt.Errorf("%w: %v", ErrInvalidExtra, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return Extra{}, ErrInvalidExtra
	}
	body := doc.Content[0]
	if body.Kind == yaml.ScalarNode && body.Tag == "!!null" {
		return Extra{}, nil
	}
	if body.Kind != yaml.MappingNode {
		return Extra{}, ErrInvalidExtra
	}
	return Extra{node: plainStyle(blockCopy(body))}, nil
}

func (e Extra) IsZero() bool { return e.node == nil }

// Keys lists the top-level keys in order.
func (e Extra) Keys() []string {
	if e.node == nil {
		return nil
	}
	keys := make([]string, 0, len(e.node.Content)/2)
	for i := 0; i < len(e.node.Content); i += 2 {
		keys = append(keys, e.node.Content[i].Value)
	}
	return keys
}

// mergeExtra deep-merges extra into base. Two mappings merge recursively; at
// any other collision base wins. Keys only in extra are appended in order.
func mergeExtra(base, extra *yaml.Node) {
	if extra == nil {
		return
	}
	for i := 0; i+1 < len(extra.Content); i += 2 {
		key, value := extra.Content[i], extra.Content[i+1]
		if existing := mappingValue(base, key.Value); existing != nil {
			if existing.Kind == yaml.MappingNode && value.Kind == yaml.MappingNode {
				mergeExtra(existing, value)
			}
			continue
		}
		base.Content = append(base.Content, blockCopy(key), blockCopy(value))
		base.Style &^= yaml.FlowStyle // an empty {} that gained keys must print as a block
	}
}

// plainStyle drops the double quotes JSON puts on every string so keys and
// values render the way a person would write YAML; the encoder re-quotes the
// strings that need it, such as "2" or "true".
func plainStyle(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.ScalarNode {
		n.Style &^= yaml.DoubleQuotedStyle
	}
	for _, c := range n.Content {
		plainStyle(c)
	}
	return n
}
