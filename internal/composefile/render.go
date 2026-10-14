package composefile

import (
	"bytes"
	"strings"

	"go.yaml.in/yaml/v3"
)

// layout is the indentation style detected from a document, so inserted
// text matches what the author wrote.
type layout struct {
	unit       int  // spaces per nesting level
	compactSeq bool // sequence items written at the parent key's indentation
}

func detectLayout(root *yaml.Node) layout {
	lay := layout{unit: 2}
	unitFound, seqFound := false, false
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode && n.Style&yaml.FlowStyle == 0 {
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if v.Line > k.Line && v.Style&yaml.FlowStyle == 0 {
					switch v.Kind {
					case yaml.MappingNode:
						if d := v.Column - k.Column; d > 0 && (!unitFound || d < lay.unit) {
							lay.unit, unitFound = d, true
						}
					case yaml.SequenceNode:
						if !seqFound {
							lay.compactSeq, seqFound = v.Column == k.Column, true
						}
					}
				}
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(root)
	return lay
}

// encode renders a node the way yaml.v3 would, without the final newline.
func encode(n *yaml.Node, unit int) string {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(unit)
	_ = enc.Encode(n) // encoding an in-memory node tree cannot fail
	_ = enc.Close()
	return strings.TrimSuffix(buf.String(), "\n")
}

// renderInline returns the single-line text for a scalar or flow node, or
// false if the node needs block layout.
func renderInline(n *yaml.Node, unit int) (string, bool) {
	if n.Kind != yaml.ScalarNode && n.Style&yaml.FlowStyle == 0 {
		return "", false
	}
	s := encode(n, unit)
	return s, !strings.Contains(s, "\n")
}

// indentLines prefixes every non-empty line of s with n spaces.
func indentLines(s string, n int) string {
	pad := strings.Repeat(" ", n)
	ls := strings.Split(s, "\n")
	for i, line := range ls {
		if line != "" {
			ls[i] = pad + line
		}
	}
	return strings.Join(ls, "\n")
}

// nested wraps value in mappings keyed by path: nested([a b], v) is {a: {b: v}}.
func nested(path []string, value *yaml.Node) *yaml.Node {
	if len(path) == 0 {
		return value
	}
	return &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: path[0]},
		nested(path[1:], value),
	}}
}

// blockCopy deep-copies n, switching flow collections to block style.
func blockCopy(n *yaml.Node) *yaml.Node {
	c := *n
	if c.Kind == yaml.MappingNode || c.Kind == yaml.SequenceNode {
		c.Style &^= yaml.FlowStyle
	}
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		c.Content[i] = blockCopy(child)
	}
	return &c
}
