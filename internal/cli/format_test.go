package cli

import (
	"math"
	"testing"
	"time"
)

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		n    uint64
		want string
	}{
		{0, "0 B"}, {1, "1 B"}, {1023, "1023 B"}, {1024, "1.0 KiB"}, {1536, "1.5 KiB"},
		{1 << 20, "1.0 MiB"}, {3 << 29, "1.5 GiB"}, {1 << 40, "1.0 TiB"}, {math.MaxUint64, "16.0 EiB"},
	}
	for _, c := range cases {
		if got := formatBytes(c.n); got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestFormatRate(t *testing.T) {
	cases := []struct {
		bps  float64
		want string
	}{{0, "0 B/s"}, {2048, "2.0 KiB/s"}, {-1, "0 B/s"}, {math.NaN(), "0 B/s"}}
	for _, c := range cases {
		if got := formatRate(c.bps); got != c.want {
			t.Errorf("formatRate(%v) = %q, want %q", c.bps, got, c.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"}, {999 * time.Millisecond, "0s"}, {time.Second, "1s"}, {59 * time.Second, "59s"},
		{time.Minute, "1m 0s"}, {125 * time.Second, "2m 5s"}, {time.Hour, "1h 0m"},
		{3725 * time.Second, "1h 2m"}, {25 * time.Hour, "1d 1h"}, {-time.Second, "0s"},
	}
	for _, c := range cases {
		if got := formatDuration(c.d); got != c.want {
			t.Errorf("formatDuration(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		t    time.Time
		want string
	}{
		{now, "just now"}, {now.Add(-30 * time.Second), "30s ago"}, {now.Add(-5 * time.Minute), "5m ago"},
		{now.Add(-3 * time.Hour), "3h ago"}, {now.Add(-48 * time.Hour), "2d ago"},
		{now.Add(30 * time.Second), "just now"}, {time.Time{}, "—"},
	}
	for _, c := range cases {
		if got := relativeTime(c.t, now); got != c.want {
			t.Errorf("relativeTime(%v) = %q, want %q", c.t, got, c.want)
		}
	}
}

func TestFormatPercent(t *testing.T) {
	cases := []struct {
		x    float64
		want string
	}{{0, "0.0%"}, {12.345, "12.3%"}, {100, "100.0%"}, {-1, "0.0%"}, {math.NaN(), "0.0%"}}
	for _, c := range cases {
		if got := formatPercent(c.x); got != c.want {
			t.Errorf("formatPercent(%v) = %q, want %q", c.x, got, c.want)
		}
	}
}
