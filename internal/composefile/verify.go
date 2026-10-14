package composefile

import (
	"errors"
	"fmt"
	"reflect"

	"go.yaml.in/yaml/v3"
)

var errVerification = errors.New("edited file does not match the intended change")

// verifyEdits checks that edited means exactly what orig means with edits
// applied: both are decoded (resolving anchors and merge keys), the edits are
// applied to the decoded original, and the results must be deeply equal. It
// guards against any text-splicing mistake changing something else.
func verifyEdits(orig, edited []byte, edits []edit) error {
	var want, got any
	if err := yaml.Unmarshal(orig, &want); err != nil {
		return fmt.Errorf("decode original: %w", err)
	}
	if err := yaml.Unmarshal(edited, &got); err != nil {
		return fmt.Errorf("%w: result is not valid YAML: %v", errVerification, err)
	}
	for _, e := range edits {
		var v any
		if e.value != nil {
			if err := e.value.Decode(&v); err != nil {
				return fmt.Errorf("decode edit value: %w", err)
			}
		}
		want = applyToValue(want, e.path, e.value != nil, v)
	}
	if !reflect.DeepEqual(want, got) {
		return errVerification
	}
	return nil
}

// applyToValue sets (or deletes) path in a decoded YAML value, creating
// intermediate mappings as needed when setting.
func applyToValue(node any, path []string, set bool, v any) any {
	m, ok := node.(map[string]any)
	if !ok {
		if !set {
			return node
		}
		m = map[string]any{}
	}
	if len(path) == 1 {
		if set {
			m[path[0]] = v
		} else {
			delete(m, path[0])
		}
		return m
	}
	child, exists := m[path[0]]
	if !exists && !set {
		return m
	}
	m[path[0]] = applyToValue(child, path[1:], set, v)
	return m
}
