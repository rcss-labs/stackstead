package store_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/store"
)

func openTemp(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "stackstead.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func count(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestOpenCreatesSchema(t *testing.T) {
	db := openTemp(t)
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES ('admin', 'x', 0)`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ('t', 1, 0)`); err != nil {
		t.Fatalf("insert session: %v", err)
	}
}

func TestOpenPlacesDatabaseInDirectoryWithURICharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "100%25 a?b#c")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "stackstead.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database not at the literal path: %v", err)
	}
	db, err = store.OpenExisting(path, store.OpenOptions{})
	if err != nil {
		t.Fatalf("OpenExisting: %v", err)
	}
	db.Close()
}

func TestSessionRequiresTokenHash(t *testing.T) {
	db := openTemp(t)
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES ('admin', 'x', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (NULL, 1, 0)`); err == nil {
		t.Error("inserting a session with NULL token_hash succeeded, want error")
	}
}

func TestReopenKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stackstead.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES ('admin', 'x', 0)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db, err = store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	if n := count(t, db, "users"); n != 1 {
		t.Errorf("users = %d after reopen, want 1", n)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stackstead.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	_, err = store.Open(path)
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("err = %v, want refusal mentioning a newer schema", err)
	}
}

func TestDeletingUserDeletesSessions(t *testing.T) {
	db := openTemp(t)
	if _, err := db.Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES (1, 'admin', 'x', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ('t', 1, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, "sessions"); n != 0 {
		t.Errorf("sessions = %d after deleting user, want 0 (foreign keys not enforced?)", n)
	}
}

func TestOpenCreatesStacksTableAtLatestVersion(t *testing.T) {
	db := openTemp(t)
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 9 {
		t.Errorf("user_version = %d, want 9", version)
	}
	if _, err := db.Exec(`INSERT INTO stacks (name, compose_path, origin, project, created_at) VALUES ('a', '/a/compose.yaml', 'managed', 'a', 0)`); err != nil {
		t.Fatalf("insert stack: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO stacks (name, compose_path, origin, project, created_at) VALUES ('b', '/b/compose.yaml', 'bogus', 'b', 0)`); err == nil {
		t.Error("inserting a stack with an unknown origin succeeded, want CHECK failure")
	}
}

func TestOpenUpgradesVersion1Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stackstead.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the schema migration 1 produced, stamped as version 1.
	for _, stmt := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE sessions (token_hash TEXT NOT NULL PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, expires_at INTEGER NOT NULL)`,
		`INSERT INTO users (id, username, password_hash, created_at) VALUES (1, 'admin', 'x', 0)`,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ('t', 1, 0)`,
		`PRAGMA user_version = 1`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	raw.Close()

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open v1 database: %v", err)
	}
	defer db.Close()
	if n := count(t, db, "users"); n != 1 {
		t.Errorf("users = %d after upgrade, want 1", n)
	}
	if n := count(t, db, "sessions"); n != 1 {
		t.Errorf("sessions = %d after upgrade, want 1", n)
	}
	if n := count(t, db, "stacks"); n != 0 {
		t.Errorf("stacks = %d after upgrade, want 0", n)
	}
}

// v2Database builds a database exactly as migration 2 left it, plus stack rows.
func v2Database(t *testing.T, rows ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stackstead.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	stmts := []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE sessions (token_hash TEXT NOT NULL PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, expires_at INTEGER NOT NULL)`,
		`CREATE TABLE stacks (name TEXT NOT NULL PRIMARY KEY, compose_path TEXT NOT NULL UNIQUE, origin TEXT NOT NULL CHECK (origin IN ('managed', 'external')), project TEXT NOT NULL, created_at INTEGER NOT NULL)`,
	}
	stmts = append(stmts, rows...)
	stmts = append(stmts, `PRAGMA user_version = 2`)
	for _, stmt := range stmts {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return path
}

func TestOpenEnforcesUniqueProjectAfterUpgrade(t *testing.T) {
	path := v2Database(t, `INSERT INTO stacks VALUES ('a', '/a/compose.yaml', 'managed', 'a', 0)`)
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO stacks VALUES ('b', '/b/compose.yaml', 'managed', 'a', 0)`); err == nil {
		t.Error("duplicate project accepted, want unique violation")
	}
}

func TestOpenRefusesDuplicateProjectsWithClearMessage(t *testing.T) {
	path := v2Database(t,
		`INSERT INTO stacks VALUES ('a', '/a/compose.yaml', 'managed', 'shared', 0)`,
		`INSERT INTO stacks VALUES ('b', '/b/compose.yaml', 'external', 'shared', 0)`)
	_, err := store.Open(path)
	if err == nil || !strings.Contains(err.Error(), `"shared"`) || !strings.Contains(err.Error(), "a, b") {
		t.Fatalf("err = %v, want message naming project shared and stacks a, b", err)
	}
}

func TestOpenUpgradesVersion3DatabaseKeepingStacks(t *testing.T) {
	path := v2Database(t, `INSERT INTO stacks (name, compose_path, origin, project, created_at) VALUES ('a', '/a/compose.yaml', 'managed', 'a', 0)`)
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE UNIQUE INDEX stacks_project_unique ON stacks(project)`,
		`PRAGMA user_version = 3`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open v3 database: %v", err)
	}
	defer db.Close()
	if n := count(t, db, "stacks"); n != 1 {
		t.Errorf("stacks = %d after upgrade, want 1", n)
	}
	if n := count(t, db, "metric_samples"); n != 0 {
		t.Errorf("metric_samples = %d, want 0", n)
	}
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('k', 'v')`); err != nil {
		t.Fatalf("settings table missing: %v", err)
	}
}

func TestMigrationAddsPeerTables(t *testing.T) {
	db := openTemp(t)
	if got := store.SupportedSchema(); got != 9 {
		t.Fatalf("SupportedSchema = %d, want 9", got)
	}
	for _, table := range []string{"peers", "connection_tokens"} {
		count(t, db, table)
	}
	_, err := db.Exec(`INSERT INTO peers (instance_id, name, slug, url, created_at, state) VALUES ('i', 'n', 's', 'u', 0, 'removing')`)
	if err == nil {
		t.Fatal("insert with state 'removing' succeeded, want CHECK failure")
	}
}

func TestMigration8Tables(t *testing.T) {
	db := openTemp(t)
	if _, err := db.Exec(`INSERT INTO images (id, slug, name, source, created_at, updated_at) VALUES ('img1', 'tools', 'Tools', 'upload', 0, 0)`); err != nil {
		t.Fatalf("insert image: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO image_revisions (image_id, number, created_at, manifest) VALUES ('img1', 1, 0, '{}')`); err != nil {
		t.Fatalf("insert revision: %v", err)
	}
	insertVersion := func(id string) error {
		_, err := db.Exec(`INSERT INTO image_versions (id, image_id, tag, status, started_at) VALUES (?, 'img1', '1', 'building', 0)`, id)
		return err
	}
	if err := insertVersion("v1"); err != nil {
		t.Fatalf("insert first building version: %v", err)
	}
	if err := insertVersion("v2"); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("second live version with the same tag: err = %v, want UNIQUE violation", err)
	}
	if _, err := db.Exec(`UPDATE image_versions SET status = 'failed' WHERE id = 'v1'`); err != nil {
		t.Fatalf("mark v1 failed: %v", err)
	}
	if err := insertVersion("v3"); err != nil {
		t.Fatalf("insert after the earlier version failed: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM images WHERE id = 'img1'`); err != nil {
		t.Fatalf("delete image: %v", err)
	}
	if n := count(t, db, "image_revisions"); n != 0 {
		t.Errorf("image_revisions = %d after image delete, want 0 (cascade)", n)
	}
	if n := count(t, db, "image_versions"); n != 0 {
		t.Errorf("image_versions = %d after image delete, want 0 (cascade)", n)
	}
}
