package metrics_test

import (
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/metrics"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func cb(container string, bucket int, step time.Duration, rx uint64) metrics.ContainerBucket {
	return metrics.ContainerBucket{
		TS: t0.Add(time.Duration(bucket) * step), Service: "app", Container: container,
		Samples: 1, CPUSum: 1, MemSum: 10, MemLimitSum: 100,
		NetRx: rx, NetTx: rx, BlkRead: rx, BlkWrite: rx,
	}
}

func rateOf(t *testing.T, p *float64) float64 {
	t.Helper()
	if p == nil {
		t.Fatal("rate is nil, want a value")
	}
	return *p
}

const step = 10 * time.Second

func TestAggregateSumsRatesAcrossReplicas(t *testing.T) {
	const mb = 1_000_000
	pts := metrics.AggregateServices([]metrics.ContainerBucket{
		cb("a", 0, step, 0), cb("a", 1, step, 10*mb),
		cb("b", 0, step, 5*mb), cb("b", 1, step, 15*mb),
	}, step)
	if len(pts) != 2 {
		t.Fatalf("points = %+v", pts)
	}
	if pts[0].NetRx != nil {
		t.Errorf("all replicas first -> no rate, got %v", *pts[0].NetRx)
	}
	if got := rateOf(t, pts[1].NetRx); got != 2*mb {
		t.Errorf("net rx = %v, want 2 MB/s (two replicas at 1 MB/s)", got)
	}
	if rateOf(t, pts[1].BlkWrite) != 2*mb || rateOf(t, pts[1].NetTx) != 2*mb || rateOf(t, pts[1].BlkRead) != 2*mb {
		t.Errorf("all counters must sum: %+v", pts[1])
	}
}

func TestAggregateRestartOfOneReplicaContributesZero(t *testing.T) {
	pts := metrics.AggregateServices([]metrics.ContainerBucket{
		cb("a", 0, step, 1000), cb("a", 1, step, 50), // reset
		cb("b", 0, step, 0), cb("b", 1, step, 100),
	}, step)
	if got := rateOf(t, pts[1].NetRx); got != 10 {
		t.Errorf("net rx = %v, want 10 (restarted replica adds 0, other adds 10)", got)
	}
}

func TestAggregateNewReplicaFirstBucketContributesNothing(t *testing.T) {
	pts := metrics.AggregateServices([]metrics.ContainerBucket{
		cb("a", 0, step, 0), cb("a", 1, step, 100),
		cb("b", 1, step, 999999), // new replica: no previous bucket
	}, step)
	if got := rateOf(t, pts[1].NetRx); got != 10 {
		t.Errorf("net rx = %v, want 10 (new replica ignored)", got)
	}
}

func TestAggregateGapUpToThreeBucketsUsesLongerDelta(t *testing.T) {
	pts := metrics.AggregateServices([]metrics.ContainerBucket{
		cb("a", 0, step, 0), cb("a", 3, step, 300),
	}, step)
	if len(pts) != 2 {
		t.Fatalf("points = %+v", pts)
	}
	if got := rateOf(t, pts[1].NetRx); got != 10 {
		t.Errorf("net rx = %v, want 10 (300 bytes over 30s)", got)
	}
}

func TestAggregateGapOverThreeBucketsIsTreatedAsFirst(t *testing.T) {
	pts := metrics.AggregateServices([]metrics.ContainerBucket{
		cb("a", 0, step, 0), cb("a", 4, step, 400),
	}, step)
	if pts[1].NetRx != nil || pts[1].BlkRead != nil {
		t.Errorf("rate after a >3 bucket gap must be absent, got %+v", pts[1])
	}
}

func TestAggregateGaugesAverageAcrossContainersAndSamples(t *testing.T) {
	a := cb("a", 0, step, 0)
	a.Samples, a.CPUSum, a.MemSum, a.MemLimitSum = 2, 20, 200, 2000 // avg 10 / 100 / 1000
	b := cb("b", 0, step, 0)
	b.Samples, b.CPUSum, b.MemSum, b.MemLimitSum = 1, 40, 400, 1000
	pts := metrics.AggregateServices([]metrics.ContainerBucket{a, b}, step)
	if len(pts) != 1 {
		t.Fatalf("points = %+v", pts)
	}
	// 3 samples: cpu 60/3, mem 600/3, limit 3000/3
	if p := pts[0]; p.CPU != 20 || p.Mem != 200 || p.MemLimit != 1000 {
		t.Errorf("gauges = %+v, want 20/200/1000", p)
	}
}

func TestAggregateOrdersByServiceThenTime(t *testing.T) {
	d := cb("d", 0, step, 0)
	d.Service = "db"
	pts := metrics.AggregateServices([]metrics.ContainerBucket{
		cb("a", 0, step, 0), cb("a", 1, step, 10), d,
	}, step)
	if len(pts) != 3 || pts[0].Service != "app" || pts[1].Service != "app" || pts[2].Service != "db" || !pts[0].TS.Before(pts[1].TS) {
		t.Errorf("order = %+v", pts)
	}
}
