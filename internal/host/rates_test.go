package host

import (
	"math"
	"testing"
	"time"
)

// Hand computation for fixtures a -> b, dt = 10 s:
//
//	cpu a: 1000+100+400+8000+500 = 10000 total, idle 8000+500 = 8500
//	cpu b: 1600+100+700+8800+700+100 = 12000 total, idle 8800+700 = 9500
//	Δtotal 2000, Δidle 1000, busy 1000 -> 50 %
//	eth0 rx 10000000 -> 14000000 = 400000 B/s, tx 4000000 -> 5000000 = 100000 B/s
//	disk read: (2000+4000)*512 -> (4048+4000)*512, Δ = 2048*512 = 1048576 -> 104857.6 B/s
//	disk write: (1000+3000)*512 -> (1100+3200)*512, Δ = 300*512 = 153600 -> 15360 B/s
func TestCPUPercentBetweenReadings(t *testing.T) {
	a, _, err := readStat(procA)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := readStat(procB)
	if err != nil {
		t.Fatal(err)
	}
	got := cpuPercent(a, b)
	if got == nil || math.Abs(*got-50) > 1e-9 {
		t.Fatalf("cpuPercent = %v, want 50", got)
	}
	if cpuPercent(a, a) != nil {
		t.Error("cpuPercent with Δtotal 0 must be nil")
	}
}

func TestRatesBetweenFixtures(t *testing.T) {
	dt := 10 * time.Second
	rxA, txA, _, _ := readNetDev(procA, sysTree)
	rxB, txB, _, _ := readNetDev(procB, sysTree)
	if r := rate(rxA, rxB, dt); r == nil || *r != 400000 {
		t.Errorf("rx rate = %v", r)
	}
	if r := rate(txA, txB, dt); r == nil || *r != 100000 {
		t.Errorf("tx rate = %v", r)
	}
	drA, dwA, _ := readDiskstats(procA)
	drB, dwB, _ := readDiskstats(procB)
	if r := rate(drA, drB, dt); r == nil || math.Abs(*r-104857.6) > 1e-6 {
		t.Errorf("disk read rate = %v", r)
	}
	if r := rate(dwA, dwB, dt); r == nil || *r != 15360 {
		t.Errorf("disk write rate = %v", r)
	}
}

func TestRateWrapIsZero(t *testing.T) {
	r := rate(1000, 10, 5*time.Second)
	if r == nil || *r != 0 {
		t.Errorf("rate on counter drop = %v, want 0", r)
	}
}

func TestRateFirstReadingNull(t *testing.T) {
	if rate(0, 100, 999*time.Millisecond) != nil {
		t.Error("dt < 1s must give nil")
	}
	if rate(0, 100, 0) != nil {
		t.Error("dt 0 must give nil")
	}
	if r := rate(0, 100, time.Second); r == nil || *r != 100 {
		t.Errorf("dt = 1s rate = %v, want 100", r)
	}
}

func TestBusiest(t *testing.T) {
	prev := map[string][2]uint64{"eth0": {100, 100}, "wg0": {0, 0}, "gone": {5, 5}}
	cur := map[string][2]uint64{"eth0": {200, 150}, "wg0": {400, 0}, "new": {9999, 9999}}
	// eth0 Δ=150, wg0 Δ=400; "new" has no previous reading, "gone" no current.
	if got := busiest(prev, cur); got != "wg0" {
		t.Errorf("busiest = %q, want wg0", got)
	}
	if got := busiest(nil, cur); got != "" {
		t.Errorf("busiest without previous = %q, want empty", got)
	}
}

// guest and guest_nice are already counted inside user and nice by the kernel,
// so total only sums the first eight fields.
//
//	a: 1000+100+400+8000+500 = 10000 total, idle 8500
//	b: 1800+100+400+9200+500 = 12000 total, idle 9700 (guest 600 ignored)
//	Δtotal 2000, Δidle 1200, busy 800 -> 40 % (double counting guest would give 50 %)
func TestCPUPercentIgnoresGuestDoubleCount(t *testing.T) {
	a, _, err := readStat("testdata/proc/guest-a")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := readStat("testdata/proc/guest-b")
	if err != nil {
		t.Fatal(err)
	}
	got := cpuPercent(a, b)
	if got == nil || math.Abs(*got-40) > 1e-9 {
		t.Fatalf("cpuPercent = %v, want 40", got)
	}
}
