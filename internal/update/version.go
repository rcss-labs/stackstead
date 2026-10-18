package update

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string
	Post                uint64
	IsPost              bool
}

var ErrNotRelease = errors.New("not a release version")

var postSuffix = regexp.MustCompile(`-([0-9]+)-g[0-9a-f]+$`)

// Parse accepts X.Y.Z[-pre][+build] with an optional leading v. A trailing
// -N-g<hex> (git describe after a tag) marks a build newer than its tag.
func Parse(s string) (Version, error) {
	var v Version
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	if m := postSuffix.FindStringSubmatchIndex(s); m != nil {
		n, err := strconv.ParseUint(s[m[2]:m[3]], 10, 32)
		if err != nil {
			return v, ErrNotRelease
		}
		v.IsPost, v.Post = true, n
		s = s[:m[0]]
	}
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, ErrNotRelease
	}
	nums := [3]*uint64{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		n, ok := parseNumeric(p)
		if !ok {
			return v, ErrNotRelease
		}
		*nums[i] = n
	}
	if hasPre {
		for _, id := range strings.Split(pre, ".") {
			if !validIdentifier(id) {
				return v, ErrNotRelease
			}
			v.Pre = append(v.Pre, id)
		}
	}
	return v, nil
}

// parseNumeric accepts 1-10 digits without a leading zero.
func parseNumeric(s string) (uint64, bool) {
	if s == "" || len(s) > 10 || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func validIdentifier(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-') {
			return false
		}
	}
	return !isNumeric(id) || id == "0" || id[0] != '0'
}

// Compare orders core numerically, then pre-release < release < post-tag;
// it matches install.sh's ver_cmp so the script and the command agree.
func Compare(a, b Version) int {
	for _, p := range [][2]uint64{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if p[0] != p[1] {
			return cmpUint(p[0], p[1])
		}
	}
	if ra, rb := a.rank(), b.rank(); ra != rb {
		return cmpUint(uint64(ra), uint64(rb))
	}
	if a.IsPost {
		return cmpUint(a.Post, b.Post)
	}
	return comparePre(a.Pre, b.Pre)
}

func (v Version) rank() int {
	switch {
	case v.IsPost:
		return 2
	case len(v.Pre) == 0:
		return 1
	}
	return 0
}

func comparePre(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := compareIdentifier(a[i], b[i]); c != 0 {
			return c
		}
	}
	return cmpUint(uint64(len(a)), uint64(len(b)))
}

func compareIdentifier(x, y string) int {
	nx, ny := isNumeric(x), isNumeric(y)
	switch {
	case nx && ny:
		a, _ := strconv.ParseUint(x, 10, 64)
		b, _ := strconv.ParseUint(y, 10, 64)
		return cmpUint(a, b)
	case nx:
		return -1
	case ny:
		return 1
	}
	return strings.Compare(x, y)
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (v Version) IsStable() bool { return len(v.Pre) == 0 && !v.IsPost }

// String is the release form without a leading v; the post-tag hash is not kept.
func (v Version) String() string {
	s := strconv.FormatUint(v.Major, 10) + "." + strconv.FormatUint(v.Minor, 10) + "." + strconv.FormatUint(v.Patch, 10)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}
