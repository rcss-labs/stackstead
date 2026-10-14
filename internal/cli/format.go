package cli

import (
	"fmt"
	"math"
	"time"
)

// The formatters mirror web/src/lib/format.ts so the CLI and the UI show the
// same numbers the same way.

var byteUnits = []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}

func formatBytes(n uint64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	value, unit := float64(n), 0
	for value >= 1024 && unit < len(byteUnits)-1 {
		value /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %s", value, byteUnits[unit])
}

// formatRate formats a bytes-per-second rate; negative and non-finite input
// is shown as zero.
func formatRate(bps float64) string {
	if math.IsNaN(bps) || math.IsInf(bps, 0) || bps <= 0 {
		return "0 B/s"
	}
	return formatBytes(uint64(bps)) + "/s"
}

// formatDuration shows the two most significant units: "2m 5s", "1h 2m", "1d 1h".
func formatDuration(d time.Duration) string {
	if d < 0 {
		return "0s"
	}
	total := int64(d / time.Second)
	days, hours, mins, secs := total/86400, total%86400/3600, total%3600/60, total%60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	case mins > 0:
		return fmt.Sprintf("%dm %ds", mins, secs)
	}
	return fmt.Sprintf("%ds", secs)
}

// relativeTime renders "5m ago"; ages under 5s and future times (clock skew)
// are "just now" and the zero time is "—".
func relativeTime(t, now time.Time) string {
	if t.IsZero() {
		return "—"
	}
	sec := int64(now.Sub(t) / time.Second)
	switch {
	case sec < 5:
		return "just now"
	case sec < 60:
		return fmt.Sprintf("%ds ago", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm ago", sec/60)
	case sec < 86400:
		return fmt.Sprintf("%dh ago", sec/3600)
	}
	return fmt.Sprintf("%dd ago", sec/86400)
}

// formatPercent takes a value that is already a percentage (12.3 → "12.3%").
func formatPercent(x float64) string {
	if math.IsNaN(x) || math.IsInf(x, 0) || x < 0 {
		return "0.0%"
	}
	return fmt.Sprintf("%.1f%%", x)
}
