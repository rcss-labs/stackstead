package composefile

import (
	"errors"
	"fmt"
	"math"
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
	if !sameValue(want, got) {
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

// sameValue is reflect.DeepEqual for decoded YAML, except that NaN equals NaN
// so a ".nan" anywhere in the file does not make every edit look wrong.
func sameValue(a, b any) bool {
	switch a := a.(type) {
	case float64:
		b, ok := b.(float64)
		return ok && (a == b || math.IsNaN(a) && math.IsNaN(b))
	case []any:
		b, ok := b.([]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !sameValue(a[i], b[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		b, ok := b.(map[string]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for k, v := range a {
			bv, found := b[k]
			if !found || !sameValue(v, bv) {
				return false
			}
		}
		return true
	case map[any]any:
		b, ok := b.(map[any]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for k, v := range a {
			bv, found := b[k]
			if !found || !sameValue(v, bv) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}
