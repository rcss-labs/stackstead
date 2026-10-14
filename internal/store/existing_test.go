package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/store"
	_ "modernc.org/sqlite"
)

// olderDB builds a database with the real first n migrations.
func olderDB(t *testing.T, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old.db")
	if err := store.CreateAtVersionForTest(path, n); err != nil {
		t.Fatal(err)
	}
	return path
}

// rawDBAtVersion creates a database file at the given user_version without
// going through store.Open, so no migration runs.
func rawDBAtVersion(t *testing.T, version int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec("CREATE TABLE marker (x INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA user_version = " + itoa(version)); err != nil {
		t.Fatal(err)
	}
	return path
}

func itoa(n int) string { return strconv.Itoa(n) }

func hasTable(t *testing.T, path, name string) bool {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func userVersion(t *testing.T, path string) int {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var v int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestOpenExistingNeverCreatesOrMigrates(t *testing.T) {
	supported := store.SupportedSchema()

	t.Run("missing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nope.db")
		_, err := store.OpenExisting(path, store.OpenOptions{})
		if !errors.Is(err, store.ErrNoDatabase) {
			t.Fatalf("err = %v, want ErrNoDatabase", err)
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("file was created: %v", statErr)
		}
	})

	t.Run("older schema refused and untouched", func(t *testing.T) {
		path := olderDB(t, 2)
		_, err := store.OpenExisting(path, store.OpenOptions{})
		var mm *store.SchemaMismatchError
		if !errors.As(err, &mm) || mm.Found != 2 || mm.Supported != supported {
			t.Fatalf("err = %v, want SchemaMismatchError{2, %d}", err, supported)
		}
		if v := userVersion(t, path); v != 2 {
			t.Fatalf("user_version = %d after refused open, want 2", v)
		}
	})

	t.Run("older schema allowed but not migrated", func(t *testing.T) {
		path := olderDB(t, 2)
		db, err := store.OpenExisting(path, store.OpenOptions{AllowOlderSchema: true})
		if err != nil {
			t.Fatalf("OpenExisting: %v", err)
		}
		v, err := store.SchemaVersion(context.Background(), db)
		db.Close()
		if err != nil || v != 2 {
			t.Fatalf("SchemaVersion = %d, %v; want 2", v, err)
		}
		if v := userVersion(t, path); v != 2 {
			t.Fatalf("user_version = %d, want 2", v)
		}
		if hasTable(t, path, "metric_samples") {
			t.Fatal("migration 4 was applied to an older database")
		}
	})

	t.Run("newer schema refused even when older allowed", func(t *testing.T) {
		path := rawDBAtVersion(t, supported+1)
		_, err := store.OpenExisting(path, store.OpenOptions{AllowOlderSchema: true})
		var mm *store.SchemaMismatchError
		if !errors.As(err, &mm) || mm.Found != supported+1 {
			t.Fatalf("err = %v, want SchemaMismatchError for newer schema", err)
		}
		if !strings.Contains(err.Error(), "newer") {
			t.Errorf("message %q should say newer", err)
		}
	})

	t.Run("current schema opens with one connection", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cur.db")
		seed, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		seed.Close()
		db, err := store.OpenExisting(path, store.OpenOptions{})
		if err != nil {
			t.Fatalf("OpenExisting: %v", err)
		}
		defer db.Close()
		if got := db.Stats().MaxOpenConnections; got != 1 {
			t.Errorf("MaxOpenConnections = %d, want 1", got)
		}
		if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('k', 'v')`); err != nil {
			t.Errorf("write failed: %v", err)
		}
	})
}

func TestOpenExistingReadOnlyRefusesWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ro.db")
	writer, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`INSERT INTO settings (key, value) VALUES ('a', 'b')`); err != nil {
		t.Fatal(err)
	}
	ro, err := store.OpenExisting(path, store.OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("OpenExisting ro: %v", err)
	}
	defer ro.Close()
	var n int
	if err := ro.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("read = %d, %v; want 1 committed row", n, err)
	}
	_, err = ro.Exec(`INSERT INTO settings (key, value) VALUES ('x', 'y')`)
	if err == nil {
		t.Fatal("INSERT on a read-only connection succeeded")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("INSERT failed for the wrong reason: %v", err)
	}

	// Readers are not blocked by a writer that is mid-transaction.
	tx, err := writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('c', 'd')`); err != nil {
		t.Fatal(err)
	}
	if err := ro.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("read during writer tx = %d, %v; want 1", n, err)
	}
}

func TestBackupUnderConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writer, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			writer.Exec(`INSERT INTO metric_samples (ts, project, service, container, cpu, mem, mem_limit, net_rx, net_tx, blk_read, blk_write)
				VALUES (?, 'p', 's', 'c', 0, 0, 0, 0, 0, 0, 0)`, i)
		}
	}()
	time.Sleep(50 * time.Millisecond)
	before := count(t, db, "metric_samples")

	dest := filepath.Join(t.TempDir(), "copy.db")
	err = store.Backup(context.Background(), db, dest)
	after := count(t, db, "metric_samples")
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}

	copyDB, err := store.OpenExisting(dest, store.OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("open copy: %v", err)
	}
	defer copyDB.Close()
	if problems, err := store.QuickCheck(context.Background(), copyDB); err != nil || problems != nil {
		t.Fatalf("QuickCheck = %v, %v", problems, err)
	}
	if v, _ := store.SchemaVersion(context.Background(), copyDB); v != store.SupportedSchema() {
		t.Errorf("copy user_version = %d, want %d", v, store.SupportedSchema())
	}
	if n := count(t, copyDB, "metric_samples"); n < before || n > after {
		t.Errorf("copy has %d samples, want between %d and %d", n, before, after)
	}
	if info, err := os.Stat(dest); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("dest mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	assertOnlyFile(t, filepath.Dir(dest), "copy.db")
	header := make([]byte, 20)
	f, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	f.ReadAt(header, 0)
	f.Close()
	if header[18] != 1 || header[19] != 1 {
		t.Errorf("header journal bytes = %d/%d, want 1/1 (not WAL mode)", header[18], header[19])
	}
}

func TestBackupRefusesExistingDest(t *testing.T) {
	db := openTemp(t)
	dest := filepath.Join(t.TempDir(), "exists.db")
	if err := os.WriteFile(dest, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Backup(context.Background(), db, dest); err == nil {
		t.Fatal("Backup over an existing file succeeded")
	}
	if got, _ := os.ReadFile(dest); string(got) != "keep me" {
		t.Errorf("existing file was modified: %q", got)
	}
}

func TestBackupCleansTempOnError(t *testing.T) {
	db := openTemp(t)
	dir := t.TempDir()
	// A cancelled context makes VACUUM INTO fail after the temp file exists.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Backup(ctx, db, filepath.Join(dir, "x.db")); err == nil {
		t.Fatal("Backup with cancelled context succeeded")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("left files behind: %v", entries)
	}

	// An unwritable destination directory fails before anything is created.
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := store.Backup(context.Background(), db, filepath.Join(ro, "x.db")); err == nil && os.Geteuid() != 0 {
		t.Fatal("Backup into an unwritable directory succeeded")
	}
	if entries, _ := os.ReadDir(ro); len(entries) != 0 {
		t.Errorf("left files in unwritable dir: %v", entries)
	}
}

func TestQuickCheckReportsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2000; i++ {
		if _, err := db.Exec(`INSERT INTO metric_samples (ts, project, service, container, cpu, mem, mem_limit, net_rx, net_tx, blk_read, blk_write)
			VALUES (?, 'p', 's', 'c', 0, 0, 0, 0, 0, 0, 0)`, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Zero one data page in the middle of the file: the page header becomes
	// invalid, which quick_check reports as problem rows.
	const pageSize = 4096
	mid := (len(data) / pageSize / 2) * pageSize
	for i := mid; i < mid+pageSize; i++ {
		data[i] = 0
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	bad, err := store.OpenExisting(path, store.OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("open corrupt db: %v", err)
	}
	defer bad.Close()
	problems, err := store.QuickCheck(context.Background(), bad)
	if err != nil || len(problems) == 0 {
		t.Fatalf("QuickCheck = %v, %v; want problem rows", problems, err)
	}
}

func TestBackupFallsBackWhenHardLinksUnsupported(t *testing.T) {
	db := openTemp(t)
	restore := store.SetLinkFileForTest(func(_, _ string) error {
		return &os.LinkError{Op: "link", Err: syscall.EPERM}
	})
	defer restore()
	dir := t.TempDir()
	dest := filepath.Join(dir, "copy.db")
	if err := store.Backup(context.Background(), db, dest); err != nil {
		t.Fatalf("Backup with fallback: %v", err)
	}
	copyDB, err := store.OpenExisting(dest, store.OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("open fallback copy: %v", err)
	}
	copyDB.Close()
	assertOnlyFile(t, dir, "copy.db")
	if info, _ := os.Stat(dest); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestBackupFallbackNeverOverwrites(t *testing.T) {
	db := openTemp(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "copy.db")
	restore := store.SetLinkFileForTest(func(_, newname string) error {
		// Simulate a destination that appears between the check and the publish.
		if err := os.WriteFile(newname, []byte("keep me"), 0o600); err != nil {
			t.Fatal(err)
		}
		return &os.LinkError{Op: "link", Err: syscall.EPERM}
	})
	defer restore()
	if err := store.Backup(context.Background(), db, dest); err == nil {
		t.Fatal("Backup overwrote or ignored an existing destination")
	}
	if got, _ := os.ReadFile(dest); string(got) != "keep me" {
		t.Errorf("destination was modified: %q", got)
	}
}

func TestBackupOtherLinkErrorIsReported(t *testing.T) {
	db := openTemp(t)
	restore := store.SetLinkFileForTest(func(_, _ string) error {
		return &os.LinkError{Op: "link", Err: syscall.EIO}
	})
	defer restore()
	dir := t.TempDir()
	if err := store.Backup(context.Background(), db, filepath.Join(dir, "x.db")); err == nil {
		t.Fatal("Backup succeeded despite a link failure that is not 'unsupported'")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("left files: %v", entries)
	}
}

func assertOnlyFile(t *testing.T, dir, name string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != name {
		t.Errorf("directory has %v, want only %s (no temp file)", entries, name)
	}
}
