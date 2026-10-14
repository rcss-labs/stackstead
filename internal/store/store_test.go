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
