package dbmaint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/rcss-labs/stackstead/internal/lockfile"
	"github.com/rcss-labs/stackstead/internal/store"
)

// stoppedDatabase reproduces a server crash after its final WAL-only commit.
func stoppedDatabase(t *testing.T) (string, map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "stackstead.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO settings(key,value) VALUES ('final','committed')"); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		files[suffix], err = os.ReadFile(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(files["-wal"]) == 0 {
		t.Fatal("fixture has no WAL")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	for suffix, data := range files {
		if err = os.WriteFile(path+suffix, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, files
}
func assertFinalRow(t *testing.T, path string) {
	t.Helper()
	db, err := store.OpenExisting(path, store.OpenOptions{ReadOnly: true, AllowOlderSchema: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err = db.QueryRow("SELECT value FROM settings WHERE key='final'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "committed" {
		t.Fatalf("final row=%q", value)
	}
}
func TestStoppedSnapshotIncludesFinalWalCommit(t *testing.T) {
	dir, _ := stoppedDatabase(t)
	dest := filepath.Join(t.TempDir(), "snapshot.db")
	schema, hash, err := Snapshot(context.Background(), dir, dest)
	if err != nil {
		t.Fatal(err)
	}
	if schema != store.SupportedSchema() {
		t.Fatalf("schema=%d", schema)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if hash != fmt.Sprintf("%x", sha256.Sum256(data)) {
		t.Fatal("snapshot digest mismatch")
	}
	assertFinalRow(t, dest)
	lock, err := lockfile.Acquire(filepath.Join(dir, "stackstead.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, _, err = Snapshot(context.Background(), dir, filepath.Join(t.TempDir(), "locked.db")); err == nil {
		t.Fatal("snapshot accepted server lock")
	}
}
func TestRestorePreservesFailedWalAndOwnership(t *testing.T) {
	dir, files := stoppedDatabase(t)
	src := filepath.Join(t.TempDir(), "snapshot.db")
	// The source is a verified pre-migration snapshot containing the final row.
	db, err := store.OpenExisting(filepath.Join(dir, "stackstead.db"), store.OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Backup(context.Background(), db, src); err != nil {
		t.Fatal(err)
	}
	db.Close()
	// Simulate a failed migration, including commits only in its WAL.
	writer, err := store.OpenExisting(filepath.Join(dir, "stackstead.db"), store.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Exec("PRAGMA wal_autocheckpoint=0; UPDATE settings SET value='failed-migration' WHERE key='final'; PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	for suffix := range files {
		files[suffix], err = os.ReadFile(filepath.Join(dir, "stackstead.db") + suffix)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	for suffix, data := range files {
		if err = os.WriteFile(filepath.Join(dir, "stackstead.db")+suffix, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	failed := filepath.Join(t.TempDir(), "failed")
	lock, err := lockfile.Acquire(filepath.Join(dir, "stackstead.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err = Restore(context.Background(), dir, src, failed); err == nil {
		t.Fatal("restore accepted server lock")
	}
	lock.Release()
	if _, err = os.Stat(failed); !os.IsNotExist(err) {
		t.Fatal("locked restore wrote archive")
	}
	if err = Restore(context.Background(), dir, src, failed); err != nil {
		t.Fatal(err)
	}
	for suffix, data := range files {
		p := filepath.Join(failed, "stackstead.db") + suffix
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("failed %s changed", suffix)
		}
	}
	for _, p := range []string{filepath.Join(dir, "stackstead.db"), filepath.Join(dir, "stackstead.lock"), filepath.Join(failed, "stackstead.db"), filepath.Join(failed, "stackstead.db-wal"), filepath.Join(failed, "stackstead.db-shm")} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if int(info.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
			t.Fatalf("wrong owner: %s", p)
		}
	}
	assertFinalRow(t, filepath.Join(dir, "stackstead.db"))
}
