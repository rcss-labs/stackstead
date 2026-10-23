package awsauth

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// ParseINI reads an AWS shared-config style file into section -> key -> value.
// Section names and values are trimmed, keys are lowercased, lines starting
// with # or ; are comments, and lines without "=" are ignored. Errors never
// quote line content, since it may hold credentials.
func ParseINI(r io.Reader) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	var cur map[string]string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if n == 1 {
			line = strings.TrimPrefix(line, utf8BOM)
		}
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				return nil, fmt.Errorf("awsauth: line %d: unterminated section header", n)
			}
			name := strings.TrimSpace(line[1:end])
			if out[name] == nil {
				out[name] = map[string]string{}
			}
			cur = out[name]
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("awsauth: line %d: setting outside a section", n)
		}
		cur[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("awsauth: read ini: %w", err)
	}
	return out, nil
}

const utf8BOM = "\xef\xbb\xbf"
