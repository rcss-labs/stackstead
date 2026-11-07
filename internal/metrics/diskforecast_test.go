package metrics_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/metrics"
)

const gib = 1 << 30

// growing returns samples every 15 minutes over span, with usage rising by
// perDay bytes a day from start, on a disk of total bytes.
func growing(span time.Duration, start, perDay, total uint64) []metrics.DiskSample {
	var out []metrics.DiskSample
	for at := time.Duration(0); at <= span; at += 15 * time.Minute {
		used := start + uint64(float64(perDay)*at.Hours()/24)
		out = append(out, metrics.DiskSample{TS: base.Add(at), Used: used, Total: total})
	}
	return out
}

func TestDiskForecastNeedsHistory(t *testing.T) {
	// Five hours is under the six-hour minimum, however steep the growth.
	f := metrics.ForecastDisk(growing(5*time.Hour, 10*gib, 50*gib, 100*gib))
	if f.DaysLeft != nil {
		t.Errorf("DaysLeft = %v, want nil", *f.DaysLeft)
	}
	if f.SampledDays <= 0 || f.SampledDays > 0.25 {
		t.Errorf("SampledDays = %v", f.SampledDays)
	}
	if f := metrics.ForecastDisk(nil); f.DaysLeft != nil || f.SampledDays != 0 {
		t.Errorf("empty = %+v", f)
	}
	one := []metrics.DiskSample{{TS: base, Used: 1, Total: 2}}
	if f := metrics.ForecastDisk(one); f.DaysLeft != nil {
		t.Errorf("single sample = %+v", f)
	}
}

func TestDiskForecastFlatUsageIsNull(t *testing.T) {
	flat := growing(48*time.Hour, 40*gib, 0, 100*gib)
	if f := metrics.ForecastDisk(flat); f.DaysLeft != nil || f.SampledDays < 1.9 {
		t.Errorf("flat = %+v", f)
	}
	falling := growing(48*time.Hour, 80*gib, 0, 100*gib)
	for i := range falling {
		falling[i].Used -= uint64(i) * (gib / 8)
	}
	if f := metrics.ForecastDisk(falling); f.DaysLeft != nil {
		t.Errorf("falling = %v", *f.DaysLeft)
	}
}

func TestDiskForecastLinear(t *testing.T) {
	// 10 GiB a day for 2 days, ending at 70 of 100 GiB used: 3 days left.
	f := metrics.ForecastDisk(growing(48*time.Hour, 50*gib, 10*gib, 100*gib))
	if f.DaysLeft == nil || math.Abs(*f.DaysLeft-3) > 0.05 {
		t.Fatalf("DaysLeft = %v, want about 3", f.DaysLeft)
	}
	if math.Abs(f.SampledDays-2) > 0.01 {
		t.Errorf("SampledDays = %v, want 2", f.SampledDays)
	}
}

func TestDiskForecastAlreadyFullIsZero(t *testing.T) {
	f := metrics.ForecastDisk(growing(24*time.Hour, 90*gib, 10*gib, 100*gib))
	if f.DaysLeft == nil || *f.DaysLeft != 0 {
		t.Errorf("DaysLeft = %v, want 0", f.DaysLeft)
	}
}

func TestRecordDiskSampleAtMostEvery15Minutes(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	put := func(at time.Duration) bool {
		ok, err := s.RecordDiskSample(ctx, metrics.DiskSample{TS: base.Add(at), Used: 1, Total: 2})
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !put(0) || put(14*time.Minute) || !put(15*time.Minute) || put(20*time.Minute) {
		t.Error("samples closer than 15 minutes must be skipped")
	}
	got, err := s.DiskSamples(ctx, base.Add(-time.Hour))
	if err != nil || len(got) != 2 {
		t.Fatalf("samples = %v, %v", got, err)
	}
}

func TestDiskSamplesRespectRetention(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	for _, d := range []time.Duration{0, 24 * time.Hour, 48 * time.Hour} {
		if _, err := s.RecordDiskSample(ctx, metrics.DiskSample{TS: base.Add(d), Used: 1, Total: 2}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.PruneDiskSamples(ctx, base.Add(12*time.Hour)); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	got, _ := s.DiskSamples(ctx, base.Add(-time.Hour))
	if len(got) != 2 {
		t.Errorf("samples after prune = %v", got)
	}
}

func TestDiskRecorderSurvivesReadFailure(t *testing.T) {
	s, _ := newStore(t)
	fail := true
	r := &metrics.DiskRecorder{Store: s, Now: func() time.Time { return base },
		Statfs: func() (uint64, uint64, error) {
			if fail {
				return 0, 0, errors.New("statfs: no such device")
			}
			return 60 * gib, 100 * gib, nil
		}}
	r.RecordOnce(context.Background()) // must not panic or write
	if got, _ := s.DiskSamples(context.Background(), base.Add(-time.Hour)); len(got) != 0 {
		t.Fatalf("recorded despite failure: %v", got)
	}
	fail = false
	r.RecordOnce(context.Background())
	got, _ := s.DiskSamples(context.Background(), base.Add(-time.Hour))
	if len(got) != 1 || got[0].Used != 40*gib || got[0].Total != 100*gib {
		t.Errorf("samples = %v", got)
	}
}
