package composefile

import (
	"fmt"
	"math"
	"reflect"

	"go.yaml.in/yaml/v3"
)

var errVerification = fmt.Errorf("%w: edited file does not match the intended change", ErrEditRefused)

// verifyEdits checks that edited means exactly what orig means with edits
// applied: both are decoded (resolving anchors and merge keys), the edits are
// applied to the decoded original, and the results must be deeply equal. It
// guards against any text-splicing mistake changing something else.
func verifyEdits(orig, edited []byte, edits []edit) error {
	var want, got any
	if err := yaml.Unmarshal(orig, &want); err != nil {
		return fmt.Errorf("%w: decode original: %v", ErrEditRefused, err)
	}
	if err := yaml.Unmarshal(edited, &got); err != nil {
		return fmt.Errorf("%w: result is not valid YAML: %v", errVerification, err)
	}
	// Mappings with a non-string key (a service named 2048) decode as
	// map[any]any; compare everything as string-keyed maps.
	want, got = stringKeys(want), stringKeys(got)
	for _, e := range edits {
		var v any
		if e.value != nil {
			if err := e.value.Decode(&v); err != nil {
				return fmt.Errorf("%w: decode edit value: %v", errVerification, err)
			}
			v = stringKeys(v)
		}
		var err error
		if want, err = applyToValue(want, e.path, e.value != nil, v); err != nil {
			return err
		}
	}
	if !sameValue(want, got) {
		return errVerification
	}
	return nil
}

// stringKeys converts every map[any]any in a decoded YAML value to a
// map[string]any keyed by fmt.Sprint of the original key.
func stringKeys(v any) any {
	switch v := v.(type) {
	case map[any]any:
		m := make(map[string]any, len(v))
		for k, val := range v {
			m[fmt.Sprint(k)] = stringKeys(val)
		}
		return m
	case map[string]any:
		for k, val := range v {
			v[k] = stringKeys(val)
		}
		return v
	case []any:
		for i := range v {
			v[i] = stringKeys(v[i])
		}
		return v
	}
	return v
}

// applyToValue sets (or deletes) path in a decoded YAML value, creating
// intermediate mappings as needed when setting. Setting through an existing
// value that is neither a mapping nor null is an error rather than an
// overwrite.
func applyToValue(node any, path []string, set bool, v any) (any, error) {
	m, ok := node.(map[string]any)
	if !ok {
		if !set {
			return node, nil
		}
		if node != nil {
			return nil, fmt.Errorf("%w: cannot set %s through a non-mapping value", errVerification, path[0])
		}
		m = map[string]any{}
	}
	if len(path) == 1 {
		if set {
			m[path[0]] = v
		} else {
			delete(m, path[0])
		}
		return m, nil
	}
	child, exists := m[path[0]]
	if !exists && !set {
		return m, nil
	}
	updated, err := applyToValue(child, path[1:], set, v)
	if err != nil {
		return nil, err
	}
	m[path[0]] = updated
	return m, nil
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
