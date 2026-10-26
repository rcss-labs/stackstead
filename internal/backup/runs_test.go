package backup

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/store"
)

func newTestRunStore(t *testing.T, now time.Time) (*RunStore, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewRunStore(db, func() time.Time { return now }), db
}

func TestRunLifecycle(t *testing.T) {
	started := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
	rs, _ := newTestRunStore(t, started)
	ctx := context.Background()

	id, err := rs.Begin(ctx, "schedule")
	if err != nil {
		t.Fatal(err)
	}
	items := []ItemResult{
		{Kind: "base", Stack: "web", Name: "web", Status: "ok", Bytes: 10},
		{Kind: "volume", Stack: "web", Name: "data", Status: "failed", Note: "disk full"},
	}
	run := Run{
		ID:           id,
		EndedAt:      started.Add(90 * time.Second),
		Trigger:      "schedule",
		Status:       "partial",
		Archive:      "backup.tar.gz",
		Size:         1024,
		Destinations: []string{"local"},
		Items:        items,
	}
	if err := rs.Finish(ctx, run); err != nil {
		t.Fatal(err)
	}

	last, found, err := rs.Last(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("Last found = false, want true")
	}
	if last.ID != id || last.Status != "partial" || last.Archive != "backup.tar.gz" {
		t.Errorf("Last = %+v, want id %d partial backup.tar.gz", last, id)
	}
	if !reflect.DeepEqual(last.Items, items) {
		t.Errorf("Items = %+v, want %+v", last.Items, items)
	}
	if !reflect.DeepEqual(last.Destinations, []string{"local"}) {
		t.Errorf("Destinations = %v, want [local]", last.Destinations)
	}
}

func TestRecentNewestFirst(t *testing.T) {
	rs, _ := newTestRunStore(t, time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC))
	ctx := context.Background()

	first, err := rs.Begin(ctx, "manual")
	if err != nil {
		t.Fatal(err)
	}
	second, err := rs.Begin(ctx, "schedule")
	if err != nil {
		t.Fatal(err)
	}

	recent, err := rs.Recent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Fatalf("Recent returned %d runs, want 2", len(recent))
	}
	if recent[0].ID != second || recent[1].ID != first {
		t.Errorf("Recent IDs = %d, %d; want %d, %d", recent[0].ID, recent[1].ID, second, first)
	}
}

func TestMarkInterrupted(t *testing.T) {
	rs, _ := newTestRunStore(t, time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC))
	ctx := context.Background()

	if _, err := rs.Begin(ctx, "schedule"); err != nil {
		t.Fatal(err)
	}
	if err := rs.MarkInterrupted(ctx); err != nil {
		t.Fatal(err)
	}

	recent, err := rs.Recent(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 {
		t.Fatalf("Recent returned %d runs, want 1", len(recent))
	}
	if recent[0].Status != "failed" || recent[0].Error != "interrupted: server stopped" {
		t.Errorf("run = status %q error %q, want failed with interrupted error", recent[0].Status, recent[0].Error)
	}
}
