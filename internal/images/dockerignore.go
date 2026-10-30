package images

import (
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
	// Split rather than scan: Docker accepts lines of any length and src is
	// already bounded by the file size limit.
	for _, rawLine := range bytes.Split(src, []byte("\n")) {
		line := strings.TrimSpace(string(rawLine))
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
		for _, s := range strings.Split(line, "/") {
			// Consecutive "**" are equivalent to one.
			if s == "**" {
				if n := len(rule.segments); n > 0 && rule.segments[n-1] == "**" {
					continue
				}
				rule.segments = append(rule.segments, s)
				continue
			}
			rule.segments = append(rule.segments, s)
			if _, err := path.Match(s, ""); err != nil {
				return nil, fmt.Errorf(".dockerignore: bad pattern %q: %w", line, err)
			}
		}
		ig.rules = append(ig.rules, rule)
	}
	return ig, nil
}

// matches reports whether the rule matches the path or any folder above it
// (a matching folder covers everything inside).
//
// It runs one pass over the pattern, tracking which path positions can be
// reached after each pattern element: O(pattern x path) however many "**"
// the pattern holds. reach[j] means the first j path segments are consumed.
func (r ignoreRule) matches(segs []string) bool {
	reach := make([]bool, len(segs)+1)
	next := make([]bool, len(segs)+1)
	reach[0] = true
	for _, elem := range r.segments {
		clear(next)
		if elem == "**" {
			seen := false
			for j := range reach {
				seen = seen || reach[j]
				next[j] = seen
			}
		} else {
			for j := 0; j < len(segs); j++ {
				if !reach[j] {
					continue
				}
				if ok, err := path.Match(elem, segs[j]); err == nil && ok {
					next[j+1] = true
				}
			}
		}
		reach, next = next, reach
	}
	// A match on any non-empty prefix of the path excludes it.
	for _, ok := range reach[1:] {
		if ok {
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
