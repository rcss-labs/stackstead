package store_test

import (
	"database/sql"
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
	if version != 2 {
		t.Errorf("user_version = %d, want 2", version)
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
