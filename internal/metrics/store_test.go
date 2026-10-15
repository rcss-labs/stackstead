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

func TestQueryContainersWindowIsFromInclusiveToExclusive(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	b := base.Truncate(10 * time.Second)
	err := s.InsertBatch(ctx, []metrics.Sample{
		sample(b.Add(-time.Second), "p", "web", 1, 1, 1),
		sample(b, "p", "web", 2, 1, 1),
		sample(b.Add(9*time.Second), "p", "web", 3, 1, 1),
		sample(b.Add(10*time.Second), "p", "web", 4, 1, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Window [b, b+10s): the sample at from is included, the one at to is not,
	// and the one before from is not; both kept samples share the bucket at b.
	rows, err := s.QueryContainers(ctx, "p", "", b, b.Add(10*time.Second), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Samples != 2 || rows[0].CPUSum != 5 || !rows[0].TS.Equal(b) {
		t.Errorf("rows = %+v, want one bucket at %v with cpu sum 5 over 2 samples", rows, b)
	}
}

func TestQueryContainersBucketsAreEpochAligned(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	b := base.Truncate(time.Minute)
	// A window starting mid-bucket still buckets on epoch multiples of step.
	err := s.InsertBatch(ctx, []metrics.Sample{
		sample(b.Add(25*time.Second), "p", "web", 1, 1, 1),
		sample(b.Add(35*time.Second), "p", "web", 1, 1, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueryContainers(ctx, "p", "", b.Add(25*time.Second), b.Add(time.Minute), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || !rows[0].TS.Equal(b) || !rows[1].TS.Equal(b.Add(30*time.Second)) {
		t.Errorf("rows = %+v, want buckets at %v and %v", rows, b, b.Add(30*time.Second))
	}
}

func TestPruneRemovesOnlyOlderRows(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	if err := s.InsertBatch(ctx, []metrics.Sample{
		sample(base.Add(-2*time.Second), "p", "web", 1, 1, 1),
		sample(base.Add(-time.Second), "p", "web", 1, 1, 1),
		sample(base, "p", "web", 1, 1, 1),
	}); err != nil {
		t.Fatal(err)
	}
	n, err := s.Prune(ctx, base)
	if err != nil || n != 2 {
		t.Fatalf("Prune = %d, %v; want 2", n, err)
	}
	var left int
	var ts int64
	if err := db.QueryRow("SELECT COUNT(*), MIN(ts) FROM metric_samples").Scan(&left, &ts); err != nil {
		t.Fatal(err)
	}
	if left != 1 || ts != base.Unix() {
		t.Errorf("left = %d rows, oldest ts %d; want 1 row at %d", left, ts, base.Unix())
	}
}

func TestHugeCountersClampToInt64Max(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	if err := s.InsertBatch(ctx, []metrics.Sample{sample(base, "p", "web", 1, math.MaxUint64, math.MaxUint64)}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueryContainers(ctx, "p", "", base, base.Add(time.Second), time.Second)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	if rows[0].NetRx != math.MaxInt64 || rows[0].BlkWrite != math.MaxInt64 {
		t.Errorf("clamped counters = %d/%d, want MaxInt64", rows[0].NetRx, rows[0].BlkWrite)
	}
}

func TestQueryAllGroupsByProjectContainerBucket(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	step := 10 * time.Second
	mk := func(off time.Duration, project, container string, cpu float64, mem, rx uint64) metrics.Sample {
		return metrics.Sample{TS: base.Add(off), Project: project, Service: "svc", Container: container, CPU: cpu, Mem: mem, NetRx: rx, NetTx: rx * 2}
	}
	if err := s.InsertBatch(ctx, []metrics.Sample{
		mk(1*time.Second, "b", "b-1", 1, 10, 100),
		mk(2*time.Second, "a", "a-2", 2, 20, 200),
		mk(3*time.Second, "a", "a-1", 4, 40, 300),
		mk(4*time.Second, "a", "a-1", 6, 60, 500), // same bucket as previous
		mk(12*time.Second, "a", "a-1", 8, 80, 700),
		mk(-1*time.Second, "a", "a-1", 9, 90, 900), // before from
		mk(30*time.Second, "a", "a-1", 9, 90, 900), // at to (exclusive)
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.QueryAll(ctx, base, base.Add(30*time.Second), step)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		p, c string
		ts   int64
		n    uint64
		cpu  float64
		mem  uint64
		rx   uint64
		tx   uint64
	}
	want := []row{
		{"a", "a-1", base.Unix(), 2, 10, 100, 500, 1000},
		{"a", "a-1", base.Unix() + 10, 1, 8, 80, 700, 1400},
		{"a", "a-2", base.Unix(), 1, 2, 20, 200, 400},
		{"b", "b-1", base.Unix(), 1, 1, 10, 100, 200},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		have := row{g.Project, g.Container, g.TS.Unix(), g.Samples, g.CPUSum, g.MemSum, g.NetRx, g.NetTx}
		if have != w {
			t.Errorf("row %d = %+v, want %+v", i, have, w)
		}
	}
}
