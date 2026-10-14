package metrics

import (
	"sort"
	"time"
)

// ContainerBucket is one container's samples within one step-sized bucket:
// gauge sums (with the sample count, so readers can average across containers
// and samples) and the maximum of each cumulative counter.
type ContainerBucket struct {
	TS                              time.Time
	Service, Container              string
	Samples                         uint64
	CPUSum                          float64
	MemSum, MemLimitSum             uint64
	NetRx, NetTx, BlkRead, BlkWrite uint64
}

// ServicePoint is a service's bucket: averaged gauges and counter rates in
// bytes per second summed across the service's replicas. A rate is nil when
// no replica has one (every replica's first bucket).
type ServicePoint struct {
	TS                              time.Time
	Service                         string
	CPU                             float64
	Mem, MemLimit                   uint64
	NetRx, NetTx, BlkRead, BlkWrite *float64
}

// maxRateGapBuckets is how many buckets back a container's previous bucket may
// be and still be used for a rate; further back it counts as a fresh start
// (the live client forgets a container after 3 unseen ticks, likewise).
const maxRateGapBuckets = 3

// AggregateServices turns per-container buckets into per-service points.
// rows must be ordered by service, container, then TS (as QueryContainers
// returns them) and step is the bucket width used to build them.
//
// Gauges (CPU, Mem, MemLimit) are averaged over all samples of all of a
// service's containers in the bucket. Counter rates are computed per
// container between its consecutive buckets: (cur-prev)/dt, or 0 when the
// counter dropped (a restart). A container's first bucket, or the first after
// a gap of more than 3 buckets, has no rate. A service's rate is the SUM of
// its containers' rates, so network and block I/O are totals across replicas;
// it is nil when no container has a rate. Points are ordered by service, TS.
func AggregateServices(rows []ContainerBucket, step time.Duration) []ServicePoint {
	type key struct {
		service string
		ts      int64
	}
	type acc struct {
		samples             uint64
		cpu                 float64
		mem, limit          uint64
		rx, tx, read, write float64
		hasRate             bool
	}
	accs := map[key]*acc{}
	var order []key
	var prev ContainerBucket
	havePrev := false
	for _, r := range rows {
		k := key{r.Service, r.TS.Unix()}
		a := accs[k]
		if a == nil {
			a = &acc{}
			accs[k] = a
			order = append(order, k)
		}
		a.samples += r.Samples
		a.cpu += r.CPUSum
		a.mem += r.MemSum
		a.limit += r.MemLimitSum

		if havePrev && prev.Service == r.Service && prev.Container == r.Container {
			dt := r.TS.Sub(prev.TS)
			if dt > 0 && dt <= maxRateGapBuckets*step {
				secs := dt.Seconds()
				a.rx += counterRate(r.NetRx, prev.NetRx, secs)
				a.tx += counterRate(r.NetTx, prev.NetTx, secs)
				a.read += counterRate(r.BlkRead, prev.BlkRead, secs)
				a.write += counterRate(r.BlkWrite, prev.BlkWrite, secs)
				a.hasRate = true
			}
		}
		prev, havePrev = r, true
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].service != order[j].service {
			return order[i].service < order[j].service
		}
		return order[i].ts < order[j].ts
	})
	out := make([]ServicePoint, 0, len(order))
	for _, k := range order {
		a := accs[k]
		p := ServicePoint{TS: time.Unix(k.ts, 0), Service: k.service}
		if a.samples > 0 {
			n := float64(a.samples)
			p.CPU, p.Mem, p.MemLimit = a.cpu/n, a.mem/a.samples, a.limit/a.samples
		}
		if a.hasRate {
			rx, tx, read, write := a.rx, a.tx, a.read, a.write
			p.NetRx, p.NetTx, p.BlkRead, p.BlkWrite = &rx, &tx, &read, &write
		}
		out = append(out, p)
	}
	return out
}

func counterRate(cur, prev uint64, seconds float64) float64 {
	if cur < prev { // the container restarted and its counter reset
		return 0
	}
	return float64(cur-prev) / seconds
}
