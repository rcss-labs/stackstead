package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Disk forecast tuning.
const (
	// DiskSampleInterval is the minimum spacing of stored disk samples.
	DiskSampleInterval = 15 * time.Minute
	// minForecastSpan is the history needed before a forecast is offered.
	minForecastSpan = 6 * time.Hour
	// flatGrowthPerDay is the growth, as a fraction of the disk, below which
	// usage counts as flat (noise from logs and temp files).
	flatGrowthPerDay = 0.001
	// maxForecastDays hides forecasts too far away to mean anything.
	maxForecastDays = 3650
)

// DiskSample is the used and total bytes of the stacks volume at one time.
type DiskSample struct {
	TS          time.Time
	Used, Total uint64
}

// DiskForecast is the result of ForecastDisk. DaysLeft is nil when there is no
// meaningful forecast; SampledDays is how much history it was based on.
type DiskForecast struct {
	DaysLeft    *float64
	SampledDays float64
}

// ForecastDisk fits a straight line to used bytes over time (least squares)
// and extrapolates it to the disk's current total. Samples must be oldest
// first. It returns a nil DaysLeft with under 6 hours of samples, and when
// usage is flat or falling.
func ForecastDisk(samples []DiskSample) DiskForecast {
	if len(samples) < 2 {
		return DiskForecast{}
	}
	first, last := samples[0], samples[len(samples)-1]
	span := last.TS.Sub(first.TS)
	out := DiskForecast{SampledDays: span.Hours() / 24}
	if span < minForecastSpan {
		return out
	}
	// Seconds since the first sample keep the sums well inside float64.
	var n, sx, sy, sxx, sxy float64
	for _, s := range samples {
		x, y := s.TS.Sub(first.TS).Seconds(), float64(s.Used)
		n, sx, sy, sxx, sxy = n+1, sx+x, sy+y, sxx+x*x, sxy+x*y
	}
	den := n*sxx - sx*sx
	if den <= 0 {
		return out
	}
	slopePerDay := (n*sxy - sx*sy) / den * 86400
	if slopePerDay <= float64(last.Total)*flatGrowthPerDay {
		return out
	}
	free := float64(last.Total) - float64(last.Used)
	days := 0.0
	if free > 0 {
		days = free / slopePerDay
	}
	if days > maxForecastDays {
		return out
	}
	out.DaysLeft = &days
	return out
}

// RecordDiskSample stores s unless a sample newer than DiskSampleInterval
// before it already exists. It reports whether a row was written.
func (s *Store) RecordDiskSample(ctx context.Context, d DiskSample) (bool, error) {
	var latest int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(ts), 0) FROM host_disk_samples`).Scan(&latest); err != nil {
		return false, fmt.Errorf("read latest disk sample: %w", err)
	}
	if ts := d.TS.Unix(); latest != 0 && ts-latest < int64(DiskSampleInterval/time.Second) && ts >= latest {
		return false, nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO host_disk_samples (ts, used, total) VALUES (?, ?, ?)`,
		d.TS.Unix(), clamp(d.Used), clamp(d.Total))
	if err != nil {
		return false, fmt.Errorf("write disk sample: %w", err)
	}
	return true, nil
}

// DiskSamples returns the samples at or after since, oldest first.
func (s *Store) DiskSamples(ctx context.Context, since time.Time) ([]DiskSample, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ts, used, total FROM host_disk_samples WHERE ts >= ? ORDER BY ts`, since.Unix())
	if err != nil {
		return nil, fmt.Errorf("read disk samples: %w", err)
	}
	defer rows.Close()
	var out []DiskSample
	for rows.Next() {
		var ts, used, total int64
		if err := rows.Scan(&ts, &used, &total); err != nil {
			return nil, fmt.Errorf("read disk samples: %w", err)
		}
		out = append(out, DiskSample{TS: time.Unix(ts, 0), Used: uint64(max(used, 0)), Total: uint64(max(total, 0))})
	}
	return out, rows.Err()
}

// PruneDiskSamples deletes samples older than olderThan; it is a PruneStep body.
func (s *Store) PruneDiskSamples(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM host_disk_samples WHERE ts < ?`, olderThan.Unix())
	if err != nil {
		return 0, fmt.Errorf("prune disk samples: %w", err)
	}
	return res.RowsAffected()
}

// DiskRecorder stores a disk sample every DiskSampleInterval. A failed read
// or write is logged and skipped; it never stops the recorder.
type DiskRecorder struct {
	Store *Store
	// Statfs returns free and total bytes of the volume to watch.
	Statfs func() (free, total uint64, err error)
	Now    func() time.Time
	Log    *slog.Logger
}

// RecordOnce takes one sample now.
func (r *DiskRecorder) RecordOnce(ctx context.Context) {
	log := r.Log
	if log == nil {
		log = slog.Default()
	}
	free, total, err := r.Statfs()
	if err != nil {
		log.Warn("disk forecast: reading disk usage failed", "err", err)
		return
	}
	if total == 0 {
		return
	}
	used := uint64(0)
	if total > free {
		used = total - free
	}
	if _, err := r.Store.RecordDiskSample(ctx, DiskSample{TS: r.Now(), Used: used, Total: total}); err != nil && ctx.Err() == nil {
		log.Warn("disk forecast: storing sample failed", "err", err)
	}
}

// Run samples at start and then every DiskSampleInterval until ctx ends.
func (r *DiskRecorder) Run(ctx context.Context) {
	t := time.NewTicker(DiskSampleInterval)
	defer t.Stop()
	r.RecordOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.RecordOnce(ctx)
		}
	}
}

// DiskForecaster serves the forecast from stored samples within retention.
type DiskForecaster struct {
	Store     *Store
	Retention time.Duration
	Now       func() time.Time
}

// DiskForecast reads the retained samples and forecasts from them.
func (d DiskForecaster) DiskForecast(ctx context.Context) (DiskForecast, error) {
	samples, err := d.Store.DiskSamples(ctx, d.Now().Add(-d.Retention))
	if err != nil {
		return DiskForecast{}, err
	}
	return ForecastDisk(samples), nil
}
