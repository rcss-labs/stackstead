package actionlog_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/actionlog"
	"github.com/rcss-labs/stackstead/internal/store"
)

var now = time.Unix(1800000000, 0).UTC()

func logStore(t *testing.T) (*actionlog.Store, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "actions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return actionlog.NewStore(db, func() time.Time { return now }), db
}

func entry() actionlog.Entry {
	return actionlog.Entry{Username: "admin", Kind: "storage.delete", Scope: "host", Target: "volume/old", Result: "deleted"}
}

func TestAppendValidationAndRoundTrip(t *testing.T) {
	st, _ := logStore(t)
	tests := []struct {
		name, kind, scope, username, result string
		valid                               bool
	}{
		{"delete", "storage.delete", "host", "admin", "deleted", true},
		{"prune", "storage.prune", "stack_1-a", "admin", "skipped", true},
		{"uppercase", "Storage.delete", "host", "admin", "deleted", false},
		{"no dot", "storage", "host", "admin", "deleted", false},
		{"extra dot", "storage.delete.x", "host", "admin", "deleted", false},
		{"traversal", "storage.delete", "../x", "admin", "deleted", false},
		{"empty scope", "storage.delete", "", "admin", "deleted", false},
		{"long scope", "storage.delete", strings.Repeat("a", 64), "admin", "deleted", false},
		{"empty user", "storage.delete", "host", "", "deleted", false},
		{"empty result", "storage.delete", "host", "admin", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := entry()
			e.Kind, e.Scope, e.Username, e.Result = tt.kind, tt.scope, tt.username, tt.result
			err := st.Append(context.Background(), e)
			if (err == nil) != tt.valid {
				t.Fatalf("Append = %v, valid %v", err, tt.valid)
			}
		})
	}
	bytes := int64(42)
	e := entry()
	e.Bytes = &bytes
	e.Detail = "unused"
	e.OperationID = "op_1"
	e.TS = now.Add(-time.Second)
	if err := st.Append(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	got, err := st.Recent(context.Background(), actionlog.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("rows = %d, want 3", len(got))
	}
	if !got[0].TS.Equal(now) || got[0].Bytes != nil || got[0].ID <= got[1].ID {
		t.Fatalf("default timestamp/null bytes/id order: %+v", got)
	}
	last := got[2]
	if last.ID == 0 || !last.TS.Equal(e.TS) || last.Bytes == nil || *last.Bytes != 42 || last.Detail != e.Detail || last.OperationID != e.OperationID || last.Target != e.Target {
		t.Fatalf("roundtrip = %+v", last)
	}
}

func TestRecentFiltersLimitsAndPrune(t *testing.T) {
	st, _ := logStore(t)
	ctx := context.Background()
	for i := 0; i < 510; i++ {
		e := entry()
		e.TS = now.Add(time.Duration(i) * time.Second)
		if i%2 == 0 {
			e.Kind = "storage.prune"
			e.Scope = "stack"
		}
		if err := st.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		filter actionlog.Filter
		want   int
	}{
		{actionlog.Filter{}, 50}, {actionlog.Filter{Limit: -1}, 1}, {actionlog.Filter{Limit: 1000}, 500},
		{actionlog.Filter{KindPrefix: "storage.", Scope: "stack", Limit: 500}, 255},
		{actionlog.Filter{KindPrefix: "storage.delete", Scope: "host", Limit: 500}, 255},
		{actionlog.Filter{Scope: "sta", Limit: 500}, 0}, {actionlog.Filter{KindPrefix: "stor%"}, 0}, {actionlog.Filter{KindPrefix: "stor_"}, 0}, {actionlog.Filter{KindPrefix: "Storage"}, 0},
	} {
		rows, err := st.Recent(ctx, tt.filter)
		if err != nil {
			t.Fatal(err)
		}
		if rows == nil || len(rows) != tt.want {
			t.Fatalf("filter %+v count %d want %d (nil=%v)", tt.filter, len(rows), tt.want, rows == nil)
		}
		for i := 1; i < len(rows); i++ {
			if !rows[i-1].TS.After(rows[i].TS) {
				t.Fatal("not newest first")
			}
		}
	}
	n, err := st.Prune(ctx, now.Add(10*time.Second))
	if err != nil || n != 10 {
		t.Fatalf("Prune = %d, %v", n, err)
	}
	rows, err := st.Recent(ctx, actionlog.Filter{Limit: 500})
	if err != nil || len(rows) != 500 || !rows[499].TS.Equal(now.Add(10*time.Second)) {
		t.Fatalf("prune boundary rows %d err %v", len(rows), err)
	}
}

func TestBackupPreservesActions(t *testing.T) {
	st, db := logStore(t)
	if err := st.Append(context.Background(), entry()); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := store.Backup(context.Background(), db, dest); err != nil {
		t.Fatal(err)
	}
	restored, err := store.OpenExisting(dest, store.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	rows, err := actionlog.NewStore(restored, nil).Recent(context.Background(), actionlog.Filter{})
	if err != nil || len(rows) != 1 || rows[0].Target != "volume/old" {
		t.Fatalf("restored = %+v, %v", rows, err)
	}
}
