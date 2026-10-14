package metrics_test

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/metrics"
	"github.com/rcss-labs/stackstead/internal/store"
)

var base = time.Unix(1_700_000_000, 0)

func newStore(t *testing.T) (*metrics.Store, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return metrics.NewStore(db, func() time.Time { return base }), db
}

func sample(ts time.Time, project, service string, cpu float64, mem, rx uint64) metrics.Sample {
	return metrics.Sample{TS: ts, Project: project, Service: service, Container: service + "-1",
		CPU: cpu, Mem: mem, MemLimit: 1000, NetRx: rx, NetTx: rx, BlkRead: rx, BlkWrite: rx}
}

func TestInsertBatchThousandRows(t *testing.T) {
	s, db := newStore(t)
	var batch []metrics.Sample
	for i := 0; i < 1000; i++ {
		batch = append(batch, sample(base.Add(time.Duration(i)*time.Second), "p", "web", 1, 1, 1))
	}
	start := time.Now()
	if err := s.InsertBatch(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("InsertBatch took %v", d)
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM metric_samples").Scan(&n)
	if n != 1000 {
		t.Errorf("rows = %d, want 1000", n)
	}
	if err := s.InsertBatch(context.Background(), nil); err != nil {
		t.Errorf("empty batch: %v", err)
	}
}

func TestQueryBucketsAveragesGaugesAndMaxesCounters(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	b := base.Truncate(time.Minute)
	err := s.InsertBatch(ctx, []metrics.Sample{
		sample(b.Add(10*time.Second), "p", "web", 10, 100, 5),
		sample(b.Add(20*time.Second), "p", "web", 30, 300, 9),
		sample(b.Add(70*time.Second), "p", "web", 50, 500, 12),
		sample(b.Add(10*time.Second), "p", "db", 2, 20, 1),
		sample(b.Add(10*time.Second), "other", "web", 99, 99, 99),
	})
	if err != nil {
		t.Fatal(err)
	}
	pts, err := s.Query(ctx, "p", "", b, b.Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 3 {
		t.Fatalf("points = %+v, want 3", pts)
	}
	// ordered by service then ts: db, web@0, web@1m
	if pts[0].Service != "db" || pts[1].Service != "web" || pts[2].Service != "web" {
		t.Fatalf("order = %+v", pts)
	}
	w := pts[1]
	if !w.TS.Equal(b) || w.CPU != 20 || w.Mem != 200 || w.NetRx != 9 || w.BlkWrite != 9 || w.MemLimit != 1000 {
		t.Errorf("web bucket 0 = %+v", w)
	}
	if !pts[2].TS.Equal(b.Add(time.Minute)) || pts[2].CPU != 50 {
		t.Errorf("web bucket 1 = %+v", pts[2])
	}

	only, _ := s.Query(ctx, "p", "web", b, b.Add(2*time.Minute), time.Minute)
	if len(only) != 2 {
		t.Errorf("service filter returned %d points, want 2", len(only))
	}
}

func TestQueryWindowIsFromInclusiveToExclusive(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	s.InsertBatch(ctx, []metrics.Sample{
		sample(base.Add(-time.Second), "p", "web", 1, 1, 1),
		sample(base, "p", "web", 2, 1, 1),
		sample(base.Add(time.Second), "p", "web", 3, 1, 1),
		sample(base.Add(2*time.Second), "p", "web", 4, 1, 1),
	})
	raw, err := s.Query(ctx, "p", "", base, base.Add(2*time.Second), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 || raw[0].CPU != 2 || raw[1].CPU != 3 {
		t.Errorf("raw = %+v, want cpu 2,3", raw)
	}
}

func TestPruneRemovesOnlyOlderRows(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	s.InsertBatch(ctx, []metrics.Sample{
		sample(base.Add(-2*time.Second), "p", "web", 1, 1, 1),
		sample(base.Add(-time.Second), "p", "web", 1, 1, 1),
		sample(base, "p", "web", 1, 1, 1),
	})
	n, err := s.Prune(ctx, base)
	if err != nil || n != 2 {
		t.Fatalf("Prune = %d, %v; want 2", n, err)
	}
	left, _ := s.Query(ctx, "p", "", base.Add(-time.Hour), base.Add(time.Hour), 0)
	if len(left) != 1 {
		t.Errorf("left = %d, want 1", len(left))
	}
}

func TestHugeCountersClampToInt64Max(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	if err := s.InsertBatch(ctx, []metrics.Sample{sample(base, "p", "web", 1, math.MaxUint64, math.MaxUint64)}); err != nil {
		t.Fatal(err)
	}
	pts, err := s.Query(ctx, "p", "", base, base.Add(time.Second), 0)
	if err != nil || len(pts) != 1 {
		t.Fatalf("%v %v", pts, err)
	}
	if pts[0].Mem != math.MaxInt64 || pts[0].NetRx != math.MaxInt64 {
		t.Errorf("clamped = %d, want MaxInt64", pts[0].Mem)
	}
}

func TestTokenHashSetGetClear(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	if h, err := s.TokenHash(ctx); err != nil || h != "" {
		t.Fatalf("unset = %q, %v", h, err)
	}
	if err := s.SetTokenHash(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTokenHash(ctx, "def"); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.TokenHash(ctx); h != "def" {
		t.Errorf("hash = %q, want def", h)
	}
	if err := s.SetTokenHash(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.TokenHash(ctx); h != "" {
		t.Errorf("cleared hash = %q", h)
	}
}

func TestQueryRawRowsOrderedByServiceThenTime(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	s.InsertBatch(ctx, []metrics.Sample{
		sample(base.Add(2*time.Second), "p", "web", 3, 1, 1),
		sample(base.Add(time.Second), "p", "web", 2, 1, 1),
		sample(base.Add(3*time.Second), "p", "db", 1, 1, 1),
	})
	pts, err := s.Query(ctx, "p", "", base, base.Add(time.Minute), -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 3 || pts[0].Service != "db" || pts[1].CPU != 2 || pts[2].CPU != 3 {
		t.Errorf("raw order = %+v, want db, web@1s, web@2s", pts)
	}
}

func TestInsertBatchIsAtomic(t *testing.T) {
	s, db := newStore(t)
	// Reject one row in the middle of the batch.
	if _, err := db.Exec(`CREATE TRIGGER reject_bad BEFORE INSERT ON metric_samples
		WHEN NEW.service = 'bad' BEGIN SELECT RAISE(ABORT, 'rejected'); END`); err != nil {
		t.Fatal(err)
	}
	batch := []metrics.Sample{
		sample(base, "p", "web", 1, 1, 1),
		sample(base, "p", "web", 2, 2, 2),
		sample(base, "p", "bad", 3, 3, 3),
		sample(base, "p", "web", 4, 4, 4),
	}
	if err := s.InsertBatch(context.Background(), batch); err == nil {
		t.Fatal("want error from the rejected row")
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM metric_samples").Scan(&n)
	if n != 0 {
		t.Errorf("rows = %d after failed batch, want 0 (earlier rows must roll back)", n)
	}
}

func TestInsertBatchCancelledContextWritesNothing(t *testing.T) {
	s, db := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.InsertBatch(ctx, []metrics.Sample{sample(base, "p", "web", 1, 1, 1)}); err == nil {
		t.Fatal("want error from cancelled context")
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM metric_samples").Scan(&n)
	if n != 0 {
		t.Errorf("rows = %d, want 0", n)
	}
}

func TestQueryContainersBucketsPerContainer(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	b := base.Truncate(time.Minute)
	two := sample(b.Add(10*time.Second), "p", "web", 10, 100, 50)
	two.Container = "web-2"
	err := s.InsertBatch(ctx, []metrics.Sample{
		sample(b.Add(10*time.Second), "p", "web", 10, 100, 5),
		sample(b.Add(20*time.Second), "p", "web", 30, 300, 9),
		two,
		sample(b.Add(70*time.Second), "p", "web", 50, 500, 12),
		sample(b.Add(10*time.Second), "other", "web", 99, 99, 99),
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueryContainers(ctx, "p", "", b, b.Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// ordered by service, container, ts: web-1@0, web-1@1m, web-2@0
	if len(rows) != 3 || rows[0].Container != "web-1" || rows[1].Container != "web-1" || rows[2].Container != "web-2" || !rows[0].TS.Before(rows[1].TS) {
		t.Fatalf("rows = %+v", rows)
	}
	if r := rows[0]; r.Samples != 2 || r.CPUSum != 40 || r.MemSum != 400 || r.MemLimitSum != 2000 || r.NetRx != 9 {
		t.Errorf("web-1 bucket 0 = %+v", r)
	}
	if r := rows[2]; r.Samples != 1 || r.NetRx != 50 {
		t.Errorf("web-2 bucket 0 = %+v", r)
	}
}
