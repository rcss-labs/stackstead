// Package store owns Stackstead's SQLite database and its schema migrations.
package store

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// migrations run in order; migration i brings the schema to version i+1, which
// is recorded in PRAGMA user_version. Append only — never edit a shipped entry.
var migrations = []string{
	`CREATE TABLE users (
		id            INTEGER PRIMARY KEY,
		username      TEXT    NOT NULL UNIQUE,
		password_hash TEXT    NOT NULL,
		created_at    INTEGER NOT NULL
	);
	CREATE TABLE sessions (
		token_hash TEXT    NOT NULL PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at INTEGER NOT NULL
	);`,
	`CREATE TABLE stacks (
		name         TEXT    NOT NULL PRIMARY KEY,
		compose_path TEXT    NOT NULL UNIQUE,
		origin       TEXT    NOT NULL CHECK (origin IN ('managed', 'external')),
		project      TEXT    NOT NULL,
		created_at   INTEGER NOT NULL
	);`,
	// Two stacks sharing a compose project would act on each other's
	// containers. migrate() refuses, with a clear message, a database that
	// already has duplicates (see checkUniqueProjects); there are no deployed
	// users to migrate, so we do not guess which row to drop.
	`CREATE UNIQUE INDEX stacks_project_unique ON stacks(project);`,
	// Container resource samples (ts is unix seconds; net/blk are Docker's
	// cumulative counters) and a small key/value table for app settings.
	`CREATE TABLE metric_samples (
		ts        INTEGER NOT NULL,
		project   TEXT    NOT NULL,
		service   TEXT    NOT NULL,
		container TEXT    NOT NULL,
		cpu       REAL    NOT NULL,
		mem       INTEGER NOT NULL,
		mem_limit INTEGER NOT NULL,
		net_rx    INTEGER NOT NULL,
		net_tx    INTEGER NOT NULL,
		blk_read  INTEGER NOT NULL,
		blk_write INTEGER NOT NULL
	);
	CREATE INDEX metric_samples_project_ts ON metric_samples(project, ts);
	CREATE TABLE settings (
		key   TEXT NOT NULL PRIMARY KEY,
		value TEXT NOT NULL
	);`,
}

// preMigration checks run inside the migration's transaction, before its SQL.
var preMigration = map[int]func(tx *sql.Tx) error{
	2: checkUniqueProjects,
}

func checkUniqueProjects(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT project, group_concat(name, ', ') FROM
		(SELECT project, name FROM stacks ORDER BY name)
		GROUP BY project HAVING COUNT(*) > 1 ORDER BY project`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var project, names string
		if err := rows.Scan(&project, &names); err != nil {
			return err
		}
		problems = append(problems, fmt.Sprintf("compose project %q is used by stacks %s", project, names))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("cannot enforce unique compose projects: %s; remove or fix the duplicate stacks and start again", strings.Join(problems, "; "))
	}
	return nil
}

// Open opens (or creates) the database at path and migrates it to the latest schema.
func Open(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// A single connection serializes writes so our own goroutines never hit
	// SQLITE_BUSY; Stackstead's load is far below what one connection serves.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", i+1, err)
		}
		if check := preMigration[i]; check != nil {
			if err := check(tx); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d: %w", i+1, err)
			}
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("record schema version %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", i+1, err)
		}
	}
	return nil
}
