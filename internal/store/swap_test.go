package store_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/store"
)

func dbWithSetting(t *testing.T, path, value string) {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO settings(key, value) VALUES('marker', ?)`, value); err != nil {
		t.Fatal(err)
	}
}

func settingIn(t *testing.T, path string) string {
	t.Helper()
	db, err := store.OpenExisting(path, store.OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'marker'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSwapInKeepsBackupOfOld(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "stackstead.db")
	dbWithSetting(t, current, "old")
	stagedDir := filepath.Join(dir, "restore")
	if err := os.Mkdir(stagedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(stagedDir, "stackstead.db")
	dbWithSetting(t, staged, "new")
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	if err := store.SwapIn(current, staged, now); err != nil {
		t.Fatal(err)
	}
	if got := settingIn(t, current); got != "new" {
		t.Errorf("current holds %q, want the staged database", got)
	}
	bak := current + ".bak-20261026T120000Z"
	if got := settingIn(t, bak); got != "old" {
		t.Errorf("backup holds %q, want the old database", got)
	}
	if _, err := os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staged file still exists: %v", err)
	}
}

func TestSwapInWithoutCurrentDatabase(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "stackstead.db")
	staged := filepath.Join(dir, "staged.db")
	dbWithSetting(t, staged, "new")
	if err := store.SwapIn(current, staged, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := settingIn(t, current); got != "new" {
		t.Errorf("current holds %q", got)
	}
	if m, _ := filepath.Glob(current + ".bak-*"); len(m) != 0 {
		t.Errorf("unexpected backup %v", m)
	}
}

func TestSwapInRefusesNewerStagedSchema(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "stackstead.db")
	dbWithSetting(t, current, "old")
	staged := filepath.Join(dir, "staged.db")
	dbWithSetting(t, staged, "new")
	raw, err := sql.Open("sqlite", "file:"+staged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	err = store.SwapIn(current, staged, time.Now())
	var mismatch *store.SchemaMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want SchemaMismatchError", err)
	}
	if got := settingIn(t, current); got != "old" {
		t.Errorf("current replaced: %q", got)
	}
}
