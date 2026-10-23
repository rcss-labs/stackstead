package metrics_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/metrics"
	"github.com/rcss-labs/stackstead/internal/store"
)

const (
	benchContainers = 100
	benchProjects   = 20
	benchInterval   = 10 * time.Second
	benchWindow     = 72 * time.Hour
)

// fillSamples inserts benchContainers containers sampled every benchInterval
// across benchWindow ending at end (about 2.59M rows), one batch per 100
// ticks, and returns how long the bulk load took.
func fillSamples(b *testing.B, ms *metrics.Store, end time.Time) time.Duration {
	b.Helper()
	ctx := context.Background()
	ticks := int(benchWindow / benchInterval)
	start := time.Now()
	var batch []metrics.Sample
	for tick := 0; tick < ticks; tick++ {
		ts := end.Add(-benchWindow + time.Duration(tick)*benchInterval)
		for c := 0; c < benchContainers; c++ {
			project := fmt.Sprintf("proj-%d", c%benchProjects)
			batch = append(batch, metrics.Sample{TS: ts, Project: project, Service: fmt.Sprintf("svc-%d", c),
				Container: fmt.Sprintf("%s-svc-%d-1", project, c), CPU: float64(c%7) + 0.5, Mem: uint64(1<<20) * uint64(c+1), MemLimit: 1 << 30,
				NetRx: uint64(tick) * 1000, NetTx: uint64(tick) * 900, BlkRead: uint64(tick) * 10, BlkWrite: uint64(tick) * 5})
		}
		if (tick+1)%100 == 0 || tick == ticks-1 {
			if err := ms.InsertBatch(ctx, batch); err != nil {
				b.Fatal(err)
			}
			batch = batch[:0]
		}
	}
	return time.Since(start)
}

func explain(b *testing.B, db *sql.DB, query string, args ...any) string {
	b.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		b.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			b.Fatal(err)
		}
		lines = append(lines, detail)
	}
	return strings.Join(lines, " | ")
}

func dbSize(b *testing.B, db *sql.DB) string {
	b.Helper()
	var pages, size int64
	if err := db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		b.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA page_size").Scan(&size); err != nil {
		b.Fatal(err)
	}
	return fmt.Sprintf("%.0f MiB", float64(pages*size)/(1<<20))
}

// tickWriteCost is the time to insert 20 consecutive ticks of the whole fleet,
// one transaction per tick as the sampler does.
func tickWriteCost(b *testing.B, ms *metrics.Store, from time.Time) time.Duration {
	b.Helper()
	start := time.Now()
	for tick := 0; tick < 20; tick++ {
		ts := from.Add(time.Duration(tick) * benchInterval)
		batch := make([]metrics.Sample, 0, benchContainers)
		for c := 0; c < benchContainers; c++ {
			project := fmt.Sprintf("proj-%d", c%benchProjects)
			batch = append(batch, metrics.Sample{TS: ts, Project: project, Service: fmt.Sprintf("svc-%d", c),
				Container: fmt.Sprintf("%s-svc-%d-1", project, c), CPU: 1, Mem: 1 << 20, MemLimit: 1 << 30})
		}
		if err := ms.InsertBatch(context.Background(), batch); err != nil {
			b.Fatal(err)
		}
	}
	return time.Since(start) / 20
}

// BenchmarkGlobalTimeFilters measures the plans and timings of the queries
// that filter by time only (QueryAll over a window, Prune) on a synthetic
// 72-hour dataset, with the shipped index set and with a candidate ts index.
// It is a measurement, not a regression test: run it with
//
//	go test ./internal/metrics -run '^$' -bench GlobalTimeFilters -benchtime 1x -v
func BenchmarkGlobalTimeFilters(b *testing.B) {
	if testing.Short() {
		b.Skip("builds a 2.6M-row dataset")
	}
	ctx := context.Background()
	end := time.Now().Truncate(time.Second)
	db, err := store.Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ms := metrics.NewStore(db, nil)
	load := fillSamples(b, ms, end)
	var n int64
	if err := db.QueryRow("SELECT COUNT(*) FROM metric_samples").Scan(&n); err != nil {
		b.Fatal(err)
	}
	b.Logf("dataset: %d samples, %d containers, %d projects, every %s over %s; bulk load %s", n, benchContainers, benchProjects, benchInterval, benchWindow, load.Round(time.Millisecond))

	const queryAll = `SELECT ts - ts % ?, project, container, COUNT(*), SUM(cpu), SUM(mem), MAX(net_rx), MAX(net_tx)
		FROM metric_samples WHERE ts >= ? AND ts < ?
		GROUP BY project, container, ts - ts % ? ORDER BY project, container, 1`
	const pruneSel = `SELECT COUNT(*) FROM metric_samples WHERE ts < ?`

	measure := func(label string, writeFrom time.Time, pruneHours time.Duration) {
		b.Helper()
		for _, w := range []struct {
			name string
			dur  time.Duration
			step time.Duration
		}{{"QueryAll 1h/10s", time.Hour, 10 * time.Second}, {"QueryAll 24h/5m", 24 * time.Hour, 5 * time.Minute}, {"QueryAll 72h/15m", 72 * time.Hour, 15 * time.Minute}} {
			from, to, secs := end.Add(-w.dur), end, int64(w.step/time.Second)
			plan := explain(b, db, queryAll, secs, from.Unix(), to.Unix(), secs)
			start := time.Now()
			got, err := ms.QueryAll(ctx, from, to, w.step)
			if err != nil {
				b.Fatal(err)
			}
			b.Logf("[%s] %s: %d buckets in %s\n    plan: %s", label, w.name, len(got), time.Since(start).Round(time.Millisecond), plan)
		}
		cutoff := end.Add(-benchWindow + pruneHours)
		b.Logf("[%s] prune plan: %s", label, explain(b, db, pruneSel, cutoff.Unix()))
		start := time.Now()
		removed, err := ms.Prune(ctx, cutoff)
		if err != nil {
			b.Fatal(err)
		}
		b.Logf("[%s] Prune(oldest 1h): %d rows in %s", label, removed, time.Since(start).Round(time.Millisecond))
		cold := tickWriteCost(b, ms, writeFrom)
		warm := tickWriteCost(b, ms, writeFrom.Add(time.Hour))
		b.Logf("[%s] insert per 100-container tick: %s first run, %s second run; database %s", label,
			cold.Round(time.Microsecond), warm.Round(time.Microsecond), dbSize(b, db))
	}

	measure("existing index (project, ts)", end.Add(time.Minute), time.Hour)

	start := time.Now()
	if _, err := db.Exec(`CREATE INDEX metric_samples_ts ON metric_samples(ts)`); err != nil {
		b.Fatal(err)
	}
	b.Logf("candidate index metric_samples(ts) built in %s", time.Since(start).Round(time.Millisecond))
	// The second pass prunes the next hour so it too deletes about 36k rows.
	measure("plus ts index", end.Add(10*time.Minute), 2*time.Hour)
}
