package composefile

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// edit is one change to a compose document: set the value at path (a chain
// of mapping keys from the root), or delete that key when value is nil.
type edit struct {
	path  []string
	value *yaml.Node
}

var (
	errUnsupportedLayout = errors.New("unsupported YAML layout")
	errSharedNode        = errors.New("value is shared through a YAML anchor or alias")
	errPathNotMapping    = errors.New("path does not lead through mappings")
)

// applyEdits rewrites src so that only the text belonging to each edited key
// changes; comments, blank lines, ordering and formatting elsewhere are kept
// byte-for-byte. Edits are applied in order. The result is verified to mean
// exactly src with the edits applied; otherwise an errVerification is returned.
func applyEdits(src []byte, edits []edit) ([]byte, error) {
	if bytes.Contains(src, []byte("\r\n")) {
		return nil, fmt.Errorf("%w: CRLF line endings", errUnsupportedLayout)
	}
	out := src
	for _, e := range edits {
		var err error
		if out, err = applyEdit(out, e); err != nil {
			return nil, fmt.Errorf("edit %s: %w", strings.Join(e.path, "."), err)
		}
	}
	// The splicing works on text, so check the result means exactly src with
	// the edits applied; any misread of the layout becomes a refusal.
	if err := verifyEdits(src, out, edits); err != nil {
		return nil, err
	}
	return out, nil
}

func applyEdit(src []byte, e edit) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode || doc.Content[0].Style&yaml.FlowStyle != 0 {
		return nil, fmt.Errorf("%w: document root must be a block mapping", errUnsupportedLayout)
	}
	l := splitLines(src)
	lay := detectLayout(doc.Content[0])

	m := doc.Content[0]
	var mKey *yaml.Node // key whose value is m; nil for the root
	for i, name := range e.path {
		if m.Anchor != "" {
			return nil, errSharedNode
		}
		if m.Style&yaml.FlowStyle != 0 {
			// Flow mappings cannot be edited line by line; rewrite the whole
			// mapping in block style with the edit applied.
			if hasAnchorOrAlias(m) {
				return nil, errSharedNode
			}
			updated := blockCopy(m)
			if err := setInTree(updated, e.path[i:], e.value, e.path[:i]); err != nil {
				return nil, err
			}
			return replaceValue(l, mKey, m, updated, lay)
		}
		key, val := lookup(m, name)
		if key == nil {
			if e.value == nil {
				return src, nil
			}
			return insertEntry(l, m, nested(e.path[i:], e.value), lay)
		}
		if val.Kind == yaml.AliasNode || val.Anchor != "" {
			return nil, errSharedNode
		}
		if i == len(e.path)-1 {
			if hasAnchorOrAlias(val) {
				return nil, errSharedNode
			}
			if e.value != nil {
				return replaceValue(l, key, val, e.value, lay)
			}
			if len(m.Content) == 2 && mKey != nil {
				// Deleting the only key would leave a null; keep an empty mapping.
				return replaceValue(l, mKey, m, &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}, lay)
			}
			return deleteEntry(l, key, val)
		}
		if val.Kind == yaml.ScalarNode && val.ShortTag() == "!!null" {
			if e.value == nil {
				return src, nil
			}
			return replaceValue(l, key, val, nested(e.path[i+1:], e.value), lay)
		}
		if val.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%w: %s", errPathNotMapping, strings.Join(e.path[:i+1], "."))
		}
		m, mKey = val, key
	}
	return nil, fmt.Errorf("%w: empty path", errPathNotMapping)
}

func lookup(m *yaml.Node, name string) (key, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == name {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// hasAnchorOrAlias reports whether n or any descendant is an anchor or alias.
func hasAnchorOrAlias(n *yaml.Node) bool {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return true
	}
	for _, c := range n.Content {
		if hasAnchorOrAlias(c) {
			return true
		}
	}
	return false
}

// setInTree applies an edit to an in-memory node tree (used for flow mappings).
func setInTree(m *yaml.Node, path []string, value *yaml.Node, done []string) error {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != path[0] {
			continue
		}
		child := m.Content[i+1]
		switch {
		case len(path) > 1 && child.Kind == yaml.MappingNode:
			return setInTree(child, path[1:], value, append(done[:len(done):len(done)], path[0]))
		case len(path) > 1:
			if child.Kind == yaml.ScalarNode && child.ShortTag() == "!!null" {
				if value != nil {
					m.Content[i+1] = nested(path[1:], value)
				}
				return nil
			}
			return fmt.Errorf("%w: %s", errPathNotMapping, strings.Join(append(done[:len(done):len(done)], path[0]), "."))
		case value == nil:
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
		default:
			m.Content[i+1] = value
		}
		return nil
	}
	if value != nil {
		m.Content = append(m.Content, nested(path, value).Content...)
	}
	return nil
}

func splice(src []byte, start, end int, text string) []byte {
	out := make([]byte, 0, len(src)-(end-start)+len(text))
	out = append(out, src[:start]...)
	out = append(out, text...)
	return append(out, src[end:]...)
}

// replaceValue rewrites the value of the entry whose key node is key.
func replaceValue(l lines, key, old, value *yaml.Node, lay layout) ([]byte, error) {
	keyLine, keyIndent := key.Line, key.Column-1
	line := l.text(keyLine)
	colon := colonAfterKey(line, l.column(keyLine, key.Column))
	if colon < 0 {
		return nil, fmt.Errorf("%w: cannot find ':' after key %q", errUnsupportedLayout, key.Value)
	}
	compact := old.Kind == yaml.SequenceNode && old.Style&yaml.FlowStyle == 0 && old.Column-1 == keyIndent
	end, err := l.entryEndFor(keyLine, keyIndent, compact, old)
	if err != nil {
		return nil, err
	}
	blockScalar := old.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0
	// "key:" with nothing after it is a null with no text of its own.
	implicitNull := old.Kind == yaml.ScalarNode && old.Value == "" && old.Style == 0 && old.ShortTag() == "!!null"
	valueOnKeyLine := old.Line == keyLine && !blockScalar && !implicitNull

	// Where the old value's text ends on the key line, so a trailing comment can be kept.
	afterValue := colon + 1
	if valueOnKeyLine {
		afterValue = inlineEnd(line, l.column(keyLine, old.Column))
	}
	inline, isInline := renderInline(value, lay.unit)

	// Fast path: single-line value replaced by a single-line value.
	if isInline && end == keyLine && valueOnKeyLine && afterValue >= 0 {
		start := l.start[keyLine-1]
		return splice(l.src, start+l.column(keyLine, old.Column), start+afterValue, inline), nil
	}

	comment := ""
	if afterValue >= 0 {
		comment = trailingComment(line, afterValue)
	}
	if comment != "" {
		comment = " " + comment
	}

	var text string
	switch {
	case value.Kind == yaml.ScalarNode || isInline:
		// A scalar's first line goes after the key; block scalar content follows indented.
		rendered := encode(value, lay.unit)
		first, rest, multi := strings.Cut(rendered, "\n")
		text = " " + first + comment
		if multi {
			text += "\n" + indentLines(rest, keyIndent)
		}
	default:
		indent := keyIndent + lay.unit
		switch {
		case old.Kind == value.Kind && old.Style&yaml.FlowStyle == 0 && old.Line > keyLine:
			indent = old.Column - 1 // replacing a block collection: keep its indentation
		case value.Kind == yaml.SequenceNode && lay.compactSeq:
			indent = keyIndent
		}
		text = comment + "\n" + indentLines(encode(value, lay.unit), indent)
	}
	return splice(l.src, l.start[keyLine-1]+colon+1, l.lineEnd(end), text), nil
}

// insertEntry adds entry (a single-key mapping node) after m's last entry.
func insertEntry(l lines, m, entry *yaml.Node, lay layout) ([]byte, error) {
	lastKey, lastVal := m.Content[len(m.Content)-2], m.Content[len(m.Content)-1]
	keyIndent := m.Content[0].Column - 1
	compact := lastVal.Kind == yaml.SequenceNode && lastVal.Style&yaml.FlowStyle == 0 && lastVal.Column == lastKey.Column
	end, err := l.entryEndFor(lastKey.Line, lastKey.Column-1, compact, lastVal)
	if err != nil {
		return nil, err
	}
	text := "\n" + indentLines(encode(entry, lay.unit), keyIndent)
	at := l.lineEnd(end)
	return splice(l.src, at, at, text), nil
}

// deleteEntry removes the lines of the entry whose key node is key.
func deleteEntry(l lines, key, val *yaml.Node) ([]byte, error) {
	compact := val.Kind == yaml.SequenceNode && val.Style&yaml.FlowStyle == 0 && val.Column == key.Column
	end, err := l.entryEndFor(key.Line, key.Column-1, compact, val)
	if err != nil {
		return nil, err
	}
	stop := len(l.src)
	if end < l.count() {
		stop = l.start[end]
	}
	return splice(l.src, l.start[key.Line-1], stop, ""), nil
}
