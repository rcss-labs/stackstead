package peers

import (
	"regexp"
	"strconv"
)

var versionPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)`)

// majorMinor parses a leading vMAJOR.MINOR (the v is optional).
func majorMinor(version string) (major, minor int, ok bool) {
	m := versionPattern.FindStringSubmatch(version)
	if m == nil {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(m[1])
	minor, err2 := strconv.Atoi(m[2])
	return major, minor, err1 == nil && err2 == nil
}

// Compatible reports whether two servers can work together: the same
// MAJOR.MINOR, or, when either version does not parse (a dev build), the
// identical string.
func Compatible(local, remote string) bool {
	lMajor, lMinor, lok := majorMinor(local)
	rMajor, rMinor, rok := majorMinor(remote)
	if !lok || !rok {
		return local == remote
	}
	return lMajor == rMajor && lMinor == rMinor
}
