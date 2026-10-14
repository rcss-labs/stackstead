package composefile

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// lines indexes a source document by line so yaml.v3 node positions
// (1-based line and character column) can be turned into byte offsets.
type lines struct {
	src   []byte
	start []int // byte offset where each line begins; index 0 is line 1
}

func splitLines(src []byte) lines {
	l := lines{src: src, start: []int{0}}
	for i, b := range src {
		if b == '\n' && i+1 < len(src) {
			l.start = append(l.start, i+1)
		}
	}
	return l
}

func (l lines) count() int { return len(l.start) }

// text returns line n (1-based) without its newline.
func (l lines) text(n int) string {
	end := len(l.src)
	if n < len(l.start) {
		end = l.start[n]
	}
	return strings.TrimSuffix(string(l.src[l.start[n-1]:end]), "\n")
}

// lineEnd returns the byte offset of the end of line n's content (its newline, if any).
func (l lines) lineEnd(n int) int { return l.start[n-1] + len(l.text(n)) }

// column converts a 1-based character column on line n to a byte index within that line.
func (l lines) column(n, col int) int {
	t := l.text(n)
	i := 0
	for c := 1; c < col && i < len(t); c++ {
		_, size := utf8.DecodeRuneInString(t[i:])
		i += size
	}
	return i
}

func indentOf(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

// entryEnd returns the last line of the mapping entry whose key is on keyLine
// at keyIndent: every following line indented deeper belongs to it, as do
// compact sequence items ("- x" at the key's own indentation) when the value
// is such a sequence. Trailing blank lines and shallower comments do not.
func (l lines) entryEnd(keyLine, keyIndent int, compactSeq bool) int {
	end := keyLine
	for n := keyLine + 1; n <= l.count(); n++ {
		t := l.text(n)
		if isBlank(t) {
			continue
		}
		ind := indentOf(t)
		rest := t[ind:]
		if ind > keyIndent || (compactSeq && ind == keyIndent && (rest == "-" || strings.HasPrefix(rest, "- "))) {
			end = n
			continue
		}
		break
	}
	return end
}

// inlineEnd returns the index just past the single-line scalar or flow
// collection that starts at index i of line, or -1 if it continues past the line.
func inlineEnd(line string, i int) int {
	if i >= len(line) {
		return i
	}
	switch line[i] {
	case '"':
		for j := i + 1; j < len(line); j++ {
			if line[j] == '\\' {
				j++
				continue
			}
			if line[j] == '"' {
				return j + 1
			}
		}
		return -1
	case '\'':
		for j := i + 1; j < len(line); j++ {
			if line[j] == '\'' {
				if j+1 < len(line) && line[j+1] == '\'' {
					j++
					continue
				}
				return j + 1
			}
		}
		return -1
	case '[', '{':
		depth := 0
		var quote byte
		for j := i; j < len(line); j++ {
			c := line[j]
			switch {
			case quote == '"' && c == '\\':
				j++
			case quote != 0:
				if c == quote {
					quote = 0
				}
			case c == '"' || c == '\'':
				quote = c
			case c == '[' || c == '{':
				depth++
			case c == ']' || c == '}':
				depth--
				if depth == 0 {
					return j + 1
				}
			}
		}
		return -1
	default:
		end := len(line)
		for j := i + 1; j < len(line); j++ {
			if line[j] == '#' && (line[j-1] == ' ' || line[j-1] == '\t') {
				end = j
				break
			}
		}
		return i + len(strings.TrimRight(line[i:end], " \t"))
	}
}

// colonAfterKey returns the index of the ':' that ends the mapping key starting
// at index i of line, or -1.
func colonAfterKey(line string, i int) int {
	j := i
	if c := line[i]; c == '"' || c == '\'' {
		if j = inlineEnd(line, i); j < 0 {
			return -1
		}
	}
	for ; j < len(line); j++ {
		if line[j] == ':' && (j+1 == len(line) || line[j+1] == ' ') {
			return j
		}
	}
	return -1
}

// trailingComment returns the comment ("# ...") that follows only whitespace
// from index i of line, or "".
func trailingComment(line string, i int) string {
	if i < 0 || i > len(line) {
		return ""
	}
	rest := strings.TrimLeft(line[i:], " \t")
	if strings.HasPrefix(rest, "#") {
		return rest
	}
	return ""
}

// flowEndLine returns the line holding the bracket that closes the flow
// collection starting at the 1-based line and character column, scanning the
// source text so indentation does not matter.
func (l lines) flowEndLine(line, col int) (int, error) {
	pos := l.start[line-1] + l.column(line, col)
	src := l.src
	if pos >= len(src) || (src[pos] != '[' && src[pos] != '{') {
		return 0, errUnsupportedLayout
	}
	depth := 0
	var quote byte
	for j := pos; j < len(src); j++ {
		c := src[j]
		switch {
		case quote == '"' && c == '\\':
			j++
		case quote == '\'' && c == '\'' && j+1 < len(src) && src[j+1] == '\'':
			j++
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#' && j > 0 && (src[j-1] == ' ' || src[j-1] == '\t' || src[j-1] == '\n'):
			for j < len(src) && src[j] != '\n' {
				j++
			}
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
			if depth == 0 {
				// the line index is the last start offset at or before j
				n := sort.Search(len(l.start), func(k int) bool { return l.start[k] > j })
				return n, nil
			}
		}
	}
	return 0, errUnsupportedLayout
}

// entryEndFor is entryEnd extended so a multi-line flow collection value is
// kept whole whatever indentation its closing bracket has.
func (l lines) entryEndFor(keyLine, keyIndent int, compactSeq bool, val *yaml.Node) (int, error) {
	end := l.entryEnd(keyLine, keyIndent, compactSeq)
	if (val.Kind == yaml.MappingNode || val.Kind == yaml.SequenceNode) && val.Style&yaml.FlowStyle != 0 {
		fe, err := l.flowEndLine(val.Line, val.Column)
		if err != nil {
			return 0, fmt.Errorf("%w: cannot find end of flow collection", err)
		}
		if fe > end {
			end = fe
		}
	}
	return end, nil
}
