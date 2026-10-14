package metrics_test

import (
	"context"
	"database/sql"
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
