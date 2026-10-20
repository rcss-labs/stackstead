package composefile

import (
	"bytes"
	"encoding/json"
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
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return Extra{}, nil
	}
	// json.Valid keeps YAML-only syntax (anchors, bare keys, several
	// documents) out before the YAML parser, which is used only to keep key order.
	if trimmed[0] != '{' || !json.Valid(trimmed) {
		return Extra{}, ErrInvalidExtra
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(trimmed, &doc); err != nil || len(doc.Content) != 1 {
		return Extra{}, ErrInvalidExtra
	}
	body := doc.Content[0]
	if body.Kind != yaml.MappingNode || hasDuplicateKeys(body) {
		return Extra{}, ErrInvalidExtra
	}
	return Extra{node: plainStyle(blockCopy(body))}, nil
}

func hasDuplicateKeys(n *yaml.Node) bool {
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			if seen[n.Content[i].Value] {
				return true
			}
			seen[n.Content[i].Value] = true
		}
	}
	for _, c := range n.Content {
		if hasDuplicateKeys(c) {
			return true
		}
	}
	return false
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

// mergeError is an ErrInvalidExtra whose message names the colliding key path.
type mergeError struct{ msg string }

func (e *mergeError) Error() string { return e.msg }
func (e *mergeError) Unwrap() error { return ErrInvalidExtra }

func kindName(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "mapping"
	case yaml.SequenceNode:
		return "sequence"
	}
	return "scalar"
}

// mergeExtra deep-merges extra into base; path names base for error messages.
// Mappings merge recursively; scalars and sequences of the same kind keep the
// form's value; a kind mismatch is an error so no extra is dropped silently.
// Keys only in extra are appended in order.
func mergeExtra(base, extra *yaml.Node, path string) error {
	if extra == nil {
		return nil
	}
	for i := 0; i+1 < len(extra.Content); i += 2 {
		key, value := extra.Content[i], extra.Content[i+1]
		keyPath := key.Value
		if path != "" {
			keyPath = path + "." + key.Value
		}
		existing := mappingValue(base, key.Value)
		if existing == nil {
			base.Content = append(base.Content, blockCopy(key), blockCopy(value))
			base.Style &^= yaml.FlowStyle // an empty {} that gained keys must print as a block
			continue
		}
		if existing.Kind != value.Kind {
			return &mergeError{fmt.Sprintf("extra: %s: cannot merge %s into %s", keyPath, kindName(value), kindName(existing))}
		}
		if existing.Kind == yaml.MappingNode {
			if err := mergeExtra(existing, value, keyPath); err != nil {
				return err
			}
		}
	}
	return nil
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
