package host

import "time"

// cpuPercent is the busy share between two readings, 0-100, or nil when no
// jiffies elapsed.
func cpuPercent(prev, cur cpuTimes) *float64 {
	if cur.total <= prev.total {
		return nil
	}
	dTotal := cur.total - prev.total
	var dIdle uint64
	if cur.idle > prev.idle {
		dIdle = cur.idle - prev.idle
	}
	if dIdle > dTotal {
		dIdle = dTotal
	}
	pct := float64(dTotal-dIdle) / float64(dTotal) * 100
	return &pct
}

// rate is the per-second change of a cumulative counter. It is nil when the
// readings are less than a second apart and 0 when the counter dropped
// (wrap or reset).
func rate(prev, cur uint64, dt time.Duration) *float64 {
	if dt < time.Second {
		return nil
	}
	var perSecond float64
	if cur >= prev {
		perSecond = float64(cur-prev) / dt.Seconds()
	}
	return &perSecond
}

// busiest names the interface with the largest rx+tx growth between two
// readings, or "" when none grew. Interfaces missing from either side and
// counter drops are ignored.
func busiest(prev, cur map[string][2]uint64) string {
	best, bestDelta := "", uint64(0)
	for name, c := range cur {
		p, ok := prev[name]
		if !ok || c[0] < p[0] || c[1] < p[1] {
			continue
		}
		delta := (c[0] - p[0]) + (c[1] - p[1])
		if delta > bestDelta || (delta == bestDelta && delta > 0 && name < best) {
			best, bestDelta = name, delta
		}
	}
	return best
}
