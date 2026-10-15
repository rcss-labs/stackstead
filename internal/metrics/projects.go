package metrics

import (
	"fmt"
	"time"
)

// ProjectSeries is one stack's sparkline data. CPU and Mem have one entry per
// bucket (nil for an empty bucket; Mem in bytes). The *Now fields are the
// newest non-nil bucket. NetRx/NetTx are bytes per second summed across the
// project's containers, nil when no container has a rate.
type ProjectSeries struct {
	CPU, Mem       []*float64
	CPUNow, MemNow *float64
	NetRx, NetTx   *float64
}

// AggregateProjects builds a series per project from QueryAll rows. Bucket i
// covers [from+i*step, from+(i+1)*step); rows outside [0, buckets) are
// ignored. A bucket's value is the sum over the project's containers of each
// container's average (sum/samples). The network rate is the sum over
// containers of the rate between the container's last two buckets, subject to
// the same gap rule as AggregateServices. rows must be ordered by project,
// container, TS, as QueryAll returns them.
func AggregateProjects(rows []ProjectBucket, from time.Time, step time.Duration, buckets int) map[string]ProjectSeries {
	type containerKey struct{ project, container string }
	type lastTwo struct {
		prev, last ProjectBucket
		n          int
	}
	cpu := map[string][]float64{}
	mem := map[string][]float64{}
	filled := map[string][]bool{}
	tails := map[containerKey]*lastTwo{}
	var projectOrder []string

	for _, r := range rows {
		idx := int(r.TS.Sub(from) / step)
		if r.TS.Before(from) || idx >= buckets || r.Samples == 0 {
			continue
		}
		if _, ok := filled[r.Project]; !ok {
			cpu[r.Project] = make([]float64, buckets)
			mem[r.Project] = make([]float64, buckets)
			filled[r.Project] = make([]bool, buckets)
			projectOrder = append(projectOrder, r.Project)
		}
		n := float64(r.Samples)
		cpu[r.Project][idx] += r.CPUSum / n
		mem[r.Project][idx] += float64(r.MemSum) / n
		filled[r.Project][idx] = true

		k := containerKey{r.Project, r.Container}
		t := tails[k]
		if t == nil {
			t = &lastTwo{}
			tails[k] = t
		}
		t.prev, t.last = t.last, r
		t.n++
	}

	out := make(map[string]ProjectSeries, len(projectOrder))
	rates := map[string]*[2]float64{}
	for k, t := range tails {
		if t.n < 2 {
			continue
		}
		dt := t.last.TS.Sub(t.prev.TS)
		if dt <= 0 || dt > maxRateGapBuckets*step {
			continue
		}
		acc := rates[k.project]
		if acc == nil {
			acc = &[2]float64{}
			rates[k.project] = acc
		}
		acc[0] += counterRate(t.last.NetRx, t.prev.NetRx, dt.Seconds())
		acc[1] += counterRate(t.last.NetTx, t.prev.NetTx, dt.Seconds())
	}
	for _, p := range projectOrder {
		s := ProjectSeries{CPU: make([]*float64, buckets), Mem: make([]*float64, buckets)}
		for i := 0; i < buckets; i++ {
			if !filled[p][i] {
				continue
			}
			c, m := cpu[p][i], mem[p][i]
			s.CPU[i], s.Mem[i] = &c, &m
			s.CPUNow, s.MemNow = &c, &m
		}
		if acc := rates[p]; acc != nil {
			rx, tx := acc[0], acc[1]
			s.NetRx, s.NetTx = &rx, &tx
		}
		out[p] = s
	}
	return out
}

// ParseSummaryWindow maps the summary endpoint's window parameter to a
// duration: "" is 15m; only 5m, 15m and 1h are accepted.
func ParseSummaryWindow(s string) (time.Duration, error) {
	switch s {
	case "", "15m":
		return 15 * time.Minute, nil
	case "5m":
		return 5 * time.Minute, nil
	case "1h":
		return time.Hour, nil
	}
	return 0, fmt.Errorf("invalid window %q: want 5m, 15m or 1h", s)
}
