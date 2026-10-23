package images

import (
	"regexp"
	"strconv"
	"strings"
)

var versionLikePattern = regexp.MustCompile(`^(v?)(\d+(?:\.\d+)*)$`)

// SuggestTag proposes the next tag after latest. When it cannot, it returns
// "" and the latest tag as the hint.
func SuggestTag(latest string) (suggest, hint string) {
	if latest == "" {
		return "1", ""
	}
	m := versionLikePattern.FindStringSubmatch(latest)
	if m == nil {
		return "", latest
	}
	parts := strings.Split(m[2], ".")
	last := len(parts) - 1
	n, err := strconv.ParseUint(parts[last], 10, 63)
	if err != nil {
		return "", latest
	}
	parts[last] = strconv.FormatUint(n+1, 10)
	return m[1] + strings.Join(parts, "."), ""
}
