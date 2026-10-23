package images

import (
	"bufio"
	"bytes"
	"fmt"
	"path"
	"strings"
)

type ignoreRule struct {
	segments []string
	negate   bool
}

// Ignore is a parsed .dockerignore.
type Ignore struct{ rules []ignoreRule }

// ParseDockerignore reads .dockerignore lines: comments, blank lines, "!"
// negation, "*", "?", "[...]" per path segment and "**" across segments.
func ParseDockerignore(src []byte) (*Ignore, error) {
	ig := &Ignore{}
	sc := bufio.NewScanner(bytes.NewReader(src))
	sc.Buffer(make([]byte, 0, 64*1024), maxPathBytes*2)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule := ignoreRule{}
		if strings.HasPrefix(line, "!") {
			rule.negate = true
			line = strings.TrimSpace(line[1:])
		}
		line = strings.TrimLeft(path.Clean(line), "/")
		if line == "" || line == "." {
			continue
		}
		rule.segments = strings.Split(line, "/")
		for _, s := range rule.segments {
			if s == "**" {
				continue
			}
			if _, err := path.Match(s, ""); err != nil {
				return nil, fmt.Errorf(".dockerignore: bad pattern %q: %w", line, err)
			}
		}
		ig.rules = append(ig.rules, rule)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf(".dockerignore: %w", err)
	}
	return ig, nil
}

func matchSegments(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchSegments(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], segs[0])
	return err == nil && ok && matchSegments(pat[1:], segs[1:])
}

// matches reports whether the rule matches the path or any folder above it
// (a matching folder covers everything inside).
func (r ignoreRule) matches(segs []string) bool {
	for n := 1; n <= len(segs); n++ {
		if matchSegments(r.segments, segs[:n]) {
			return true
		}
	}
	return false
}

// Excluded reports whether the build context leaves out the file at p. The
// last matching rule wins; Dockerfile and .dockerignore are always kept.
func (i *Ignore) Excluded(p string) bool {
	if p == "Dockerfile" || p == ".dockerignore" {
		return false
	}
	segs := strings.Split(strings.Trim(p, "/"), "/")
	excluded := false
	for _, r := range i.rules {
		if r.matches(segs) {
			excluded = !r.negate
		}
	}
	return excluded
}
