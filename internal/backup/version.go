package backup

import (
	"strconv"
	"strings"
)

// archiveNewer reports whether an archive written by Stackstead version
// archive is newer than the installed version. A version that is not
// MAJOR[.MINOR[.PATCH]][-pre] (such as "dev") is unknown and never counts as
// newer.
func archiveNewer(archive, installed string) bool {
	a, ok := parseVersion(archive)
	if !ok {
		return false
	}
	b, ok := parseVersion(installed)
	if !ok {
		return false
	}
	for i := range a.nums {
		if a.nums[i] != b.nums[i] {
			return a.nums[i] > b.nums[i]
		}
	}
	switch {
	case a.pre == b.pre:
		return false
	case a.pre == "":
		return true // a release is newer than its pre-releases
	case b.pre == "":
		return false
	default:
		return a.pre > b.pre
	}
}

type version struct {
	nums [3]int
	pre  string
}

func parseVersion(s string) (version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s, _, _ = strings.Cut(s, "+")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if core == "" || len(parts) > 3 {
		return version{}, false
	}
	var v version
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return version{}, false
		}
		v.nums[i] = n
	}
	v.pre = pre
	return v, true
}
