package metrics_test

import (
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/metrics"
)

const pStep = 10 * time.Second

// pb builds a single-sample bucket at bucket index i (from = base).
func pb(i int, project, container string, cpu float64, mem, rx, tx uint64) metrics.ProjectBucket {
	return metrics.ProjectBucket{TS: base.Add(time.Duration(i) * pStep), Project: project, Container: container,
		Samples: 1, CPUSum: cpu, MemSum: mem, NetRx: rx, NetTx: tx}
}

func f(v float64) *float64 { return &v }

func eqPtr(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func eqSeries(a, b []*float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !eqPtr(a[i], b[i]) {
			return false
		}
	}
	return true
}

func TestAggregateProjectsSumsContainers(t *testing.T) {
	rows := []metrics.ProjectBucket{
		pb(0, "a", "a-1", 10, 100, 0, 0),
		pb(0, "a", "a-2", 20, 200, 0, 0),
		pb(0, "b", "b-1", 5, 50, 0, 0),
	}
	// a-1 has two samples in the bucket: its average is CPUSum/Samples.
	rows[0].Samples, rows[0].CPUSum, rows[0].MemSum = 2, 10, 300
	got := metrics.AggregateProjects(rows, base, pStep, 3)
	a := got["a"]
	if want := []*float64{f(25), nil, nil}; !eqSeries(a.CPU, want) { // 10/2 + 20
		t.Errorf("a.CPU = %v", a.CPU)
	}
	if want := []*float64{f(350), nil, nil}; !eqSeries(a.Mem, want) { // 300/2 + 200
		t.Errorf("a.Mem = %v", a.Mem)
	}
	if !eqPtr(a.CPUNow, f(25)) || !eqPtr(a.MemNow, f(350)) {
		t.Errorf("a now = %v %v", a.CPUNow, a.MemNow)
	}
	if b := got["b"]; !eqSeries(b.CPU, []*float64{f(5), nil, nil}) {
		t.Errorf("b.CPU = %v", b.CPU)
	}
}

func TestAggregateProjectsNullBuckets(t *testing.T) {
	rows := []metrics.ProjectBucket{
		pb(1, "a", "a-1", 3, 30, 0, 0),
		pb(3, "a", "a-1", 7, 70, 0, 0),
	}
	a := metrics.AggregateProjects(rows, base, pStep, 6)["a"]
	if want := []*float64{nil, f(3), nil, f(7), nil, nil}; !eqSeries(a.CPU, want) {
		t.Errorf("CPU = %v", a.CPU)
	}
	if !eqPtr(a.CPUNow, f(7)) || !eqPtr(a.MemNow, f(70)) {
		t.Errorf("now = %v %v", a.CPUNow, a.MemNow)
	}
}

func TestAggregateProjectsIgnoresRowsOutsideWindow(t *testing.T) {
	rows := []metrics.ProjectBucket{
		pb(-1, "a", "a-1", 1, 1, 0, 0),
		pb(2, "a", "a-1", 2, 2, 0, 0),
		pb(3, "a", "a-1", 3, 3, 0, 0), // index == buckets
	}
	a := metrics.AggregateProjects(rows, base, pStep, 3)["a"]
	if want := []*float64{nil, nil, f(2)}; !eqSeries(a.CPU, want) {
		t.Errorf("CPU = %v", a.CPU)
	}
	if got := metrics.AggregateProjects([]metrics.ProjectBucket{pb(-1, "x", "x-1", 1, 1, 0, 0)}, base, pStep, 3); len(got) != 0 {
		t.Errorf("project with only out-of-window rows present: %v", got)
	}
}

func TestAggregateProjectsNetRateFromLastTwoBuckets(t *testing.T) {
	rows := []metrics.ProjectBucket{
		pb(0, "a", "a-1", 1, 1, 0, 0),
		pb(1, "a", "a-1", 1, 1, 1000, 500),
		pb(2, "a", "a-1", 1, 1, 1100, 700), // last two: +100 rx, +200 tx over 10s
		pb(1, "a", "a-2", 1, 1, 0, 0),
		pb(2, "a", "a-2", 1, 1, 50, 100), // +50 rx, +100 tx
		pb(2, "b", "b-1", 1, 1, 9, 9),    // single bucket: no rate
	}
	got := metrics.AggregateProjects(rows, base, pStep, 3)
	if a := got["a"]; !eqPtr(a.NetRx, f(15)) || !eqPtr(a.NetTx, f(30)) {
		t.Errorf("a net = %v %v", a.NetRx, a.NetTx)
	}
	if b := got["b"]; b.NetRx != nil || b.NetTx != nil {
		t.Errorf("b net = %v %v, want nil", b.NetRx, b.NetTx)
	}
}

func TestAggregateProjectsGapRule(t *testing.T) {
	step := pStep
	at3 := []metrics.ProjectBucket{pb(0, "a", "a-1", 1, 1, 0, 0), pb(3, "a", "a-1", 1, 1, 300, 300)}
	if a := metrics.AggregateProjects(at3, base, step, 5)["a"]; !eqPtr(a.NetRx, f(10)) {
		t.Errorf("gap of 3 buckets: rate = %v, want 10", a.NetRx)
	}
	at4 := []metrics.ProjectBucket{pb(0, "a", "a-1", 1, 1, 0, 0), pb(4, "a", "a-1", 1, 1, 400, 400)}
	if a := metrics.AggregateProjects(at4, base, step, 5)["a"]; a.NetRx != nil || a.NetTx != nil {
		t.Errorf("gap of 4 buckets: rate = %v %v, want nil", a.NetRx, a.NetTx)
	}
}

func TestAggregateProjectsCounterReset(t *testing.T) {
	rows := []metrics.ProjectBucket{pb(0, "a", "a-1", 1, 1, 5000, 5000), pb(1, "a", "a-1", 1, 1, 10, 20)}
	a := metrics.AggregateProjects(rows, base, pStep, 2)["a"]
	if !eqPtr(a.NetRx, f(0)) || !eqPtr(a.NetTx, f(0)) {
		t.Errorf("reset net = %v %v, want 0", a.NetRx, a.NetTx)
	}
}

func TestParseSummaryWindow(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 15 * time.Minute, false},
		{"5m", 5 * time.Minute, false},
		{"15m", 15 * time.Minute, false},
		{"1h", time.Hour, false},
		{"30m", 0, true},
		{"1d", 0, true},
	}
	for _, c := range cases {
		got, err := metrics.ParseSummaryWindow(c.in)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("ParseSummaryWindow(%q) = %v, %v", c.in, got, err)
		}
	}
}
