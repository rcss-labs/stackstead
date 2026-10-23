// Package store owns Stackstead's SQLite database and its schema migrations.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

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
	`CREATE TABLE action_log (
		id INTEGER PRIMARY KEY,
		ts INTEGER NOT NULL,
		username TEXT NOT NULL,
		kind TEXT NOT NULL,
		scope TEXT NOT NULL,
		target TEXT NOT NULL,
		detail TEXT NOT NULL DEFAULT '',
		bytes INTEGER,
		result TEXT NOT NULL,
		operation_id TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX action_log_ts ON action_log(ts);
	CREATE INDEX action_log_kind_ts ON action_log(kind, ts);`,
	// Container state changes (ts is unix seconds) that health history is
	// built from; 'restart' is a marker row, not a state.
	`CREATE TABLE container_transitions (
		ts        INTEGER NOT NULL,
		project   TEXT    NOT NULL,
		service   TEXT    NOT NULL,
		container TEXT    NOT NULL,
		state     TEXT    NOT NULL CHECK (state IN ('running','healthy','unhealthy','starting','restarting','exited','removed','restart')),
		exit_code INTEGER NULL
	);
	CREATE INDEX container_transitions_project_ts ON container_transitions(project, ts);`,
	// Connected servers: paired peers and their one-time connection tokens
	// (hash is the SHA-256 of the token; the plaintext is never stored).
	`CREATE TABLE peers (
		instance_id    TEXT    NOT NULL PRIMARY KEY,
		name           TEXT    NOT NULL,
		slug           TEXT    NOT NULL UNIQUE,
		url            TEXT    NOT NULL,
		ca_pem         TEXT    NOT NULL DEFAULT '',
		pin_sha256     TEXT    NOT NULL DEFAULT '',
		plaintext      INTEGER NOT NULL DEFAULT 0,
		out_credential TEXT    NOT NULL DEFAULT '',
		in_hash        TEXT    NOT NULL DEFAULT '',
		version        TEXT    NOT NULL DEFAULT '',
		stack_count    INTEGER NOT NULL DEFAULT 0,
		created_at     INTEGER NOT NULL,
		last_seen_at   INTEGER,
		last_error     TEXT    NOT NULL DEFAULT '',
		state          TEXT    NOT NULL CHECK (state IN ('pending','active','removed'))
	);
	CREATE UNIQUE INDEX peers_in_hash ON peers(in_hash) WHERE in_hash != '';
	CREATE TABLE connection_tokens (
		hash       TEXT    NOT NULL PRIMARY KEY,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	);`,
	// Image library: built container images, their revisions (source snapshots),
	// versions (tagged builds), registries and push targets. A version is
	// unique per tag among non-failed rows, so a failed build frees its tag.
	`CREATE TABLE images (
		id                 TEXT    PRIMARY KEY,
		slug               TEXT    NOT NULL UNIQUE,
		name               TEXT    NOT NULL,
		source             TEXT    NOT NULL CHECK (source IN ('upload','github')),
		dockerfile_path    TEXT    NOT NULL DEFAULT 'Dockerfile',
		github_repo        TEXT    NOT NULL DEFAULT '',
		github_branch      TEXT    NOT NULL DEFAULT '',
		github_path        TEXT    NOT NULL DEFAULT '',
		build_args         TEXT    NOT NULL DEFAULT '{}',
		retention_keep_last INTEGER NOT NULL DEFAULT 0,
		retention_keep_days INTEGER NOT NULL DEFAULT 0,
		created_at         INTEGER NOT NULL,
		updated_at         INTEGER NOT NULL
	);
	CREATE TABLE image_revisions (
		image_id   TEXT    NOT NULL REFERENCES images(id) ON DELETE CASCADE,
		number     INTEGER NOT NULL,
		created_at INTEGER NOT NULL,
		summary    TEXT    NOT NULL DEFAULT '',
		manifest   TEXT    NOT NULL,
		PRIMARY KEY (image_id, number)
	);
	CREATE TABLE image_versions (
		id           TEXT    PRIMARY KEY,
		image_id     TEXT    NOT NULL REFERENCES images(id) ON DELETE CASCADE,
		tag          TEXT    NOT NULL,
		status       TEXT    NOT NULL CHECK (status IN ('building','succeeded','failed')),
		revision     INTEGER NOT NULL DEFAULT 0,
		commit_sha   TEXT    NOT NULL DEFAULT '',
		build_args   TEXT    NOT NULL DEFAULT '{}',
		arch         TEXT    NOT NULL DEFAULT '',
		image_digest TEXT    NOT NULL DEFAULT '',
		size         INTEGER NOT NULL DEFAULT 0,
		started_at   INTEGER NOT NULL,
		ended_at     INTEGER,
		error        TEXT    NOT NULL DEFAULT '',
		operation_id TEXT    NOT NULL DEFAULT ''
	);
	CREATE UNIQUE INDEX image_versions_tag_live ON image_versions(image_id, tag) WHERE status != 'failed';
	CREATE INDEX image_versions_image_started ON image_versions(image_id, started_at);
	CREATE TABLE registries (
		id                TEXT    PRIMARY KEY,
		name              TEXT    NOT NULL UNIQUE,
		kind              TEXT    NOT NULL CHECK (kind IN ('dockerhub','ecr')),
		host              TEXT    NOT NULL DEFAULT '',
		config            TEXT    NOT NULL DEFAULT '{}',
		secrets           TEXT    NOT NULL DEFAULT '{}',
		created_at        INTEGER NOT NULL,
		updated_at        INTEGER NOT NULL,
		last_test_at      INTEGER,
		last_test_ok      INTEGER NOT NULL DEFAULT 0,
		last_test_message TEXT    NOT NULL DEFAULT ''
	);
	CREATE TABLE push_targets (
		id          TEXT    PRIMARY KEY,
		image_id    TEXT    NOT NULL REFERENCES images(id) ON DELETE CASCADE,
		registry_id TEXT    NOT NULL REFERENCES registries(id) ON DELETE CASCADE,
		repository  TEXT    NOT NULL,
		auto_push   INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE image_pushes (
		version_id TEXT    NOT NULL REFERENCES image_versions(id) ON DELETE CASCADE,
		target_id  TEXT    NOT NULL REFERENCES push_targets(id) ON DELETE CASCADE,
		status     TEXT    NOT NULL CHECK (status IN ('pending','pushing','pushed','failed')),
		digest     TEXT    NOT NULL DEFAULT '',
		error      TEXT    NOT NULL DEFAULT '',
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (version_id, target_id)
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

// FileURI returns the SQLite "file:" URI prefix for a filesystem path, escaping
// the characters that would otherwise be read as URI syntax.
func FileURI(path string) string {
	return "file:" + strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
}

// Open opens (or creates) the database at path and migrates it to the latest schema.
func Open(path string) (*sql.DB, error) {
	dsn := FileURI(path) + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
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

// ErrNoDatabase is returned by OpenExisting when the database file is missing.
var ErrNoDatabase = errors.New("database does not exist")

// SchemaMismatchError reports a database whose schema version this binary
// will not use without migrating it.
type SchemaMismatchError struct{ Found, Supported int }

func (e *SchemaMismatchError) Error() string {
	if e.Found > e.Supported {
		return fmt.Sprintf("database schema is version %d, newer than this binary supports (%d): upgrade stackstead", e.Found, e.Supported)
	}
	return fmt.Sprintf("database schema is version %d, this binary supports %d: start the server (or run stackstead db init) to migrate", e.Found, e.Supported)
}

// OpenOptions tunes OpenExisting.
type OpenOptions struct {
	// ReadOnly opens with mode=ro so the connection can never write the
	// database. It is not a guarantee that no files are touched: reading a
	// WAL-mode database may still create the -wal/-shm files, which as root
	// would leave them owned by root. Callers running as root must refuse
	// before opening (the CLI's ownership check does).
	ReadOnly bool
	// AllowOlderSchema accepts a database older than this binary (it is
	// never migrated); a newer one is always refused.
	AllowOlderSchema bool
}

// SupportedSchema is the schema version this binary migrates to.
func SupportedSchema() int { return len(migrations) }

// OpenExisting opens a database that must already exist. It never creates the
// file and never migrates: only the server and `db init` (Open) do that.
func OpenExisting(path string, opts OpenOptions) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNoDatabase, path)
		}
		return nil, fmt.Errorf("open database: %w", err)
	}
	mode := "rw"
	pragmas := "&_pragma=journal_mode(WAL)"
	if opts.ReadOnly {
		mode = "ro"
		pragmas = "" // switching journal mode needs write access
	}
	dsn := FileURI(path) + "?mode=" + mode + pragmas + "&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	version, err := SchemaVersion(context.Background(), db)
	if err != nil {
		db.Close()
		return nil, err
	}
	supported := SupportedSchema()
	if version > supported || (version != supported && !opts.AllowOlderSchema) {
		db.Close()
		return nil, &SchemaMismatchError{Found: version, Supported: supported}
	}
	return db, nil
}

// SchemaVersion returns PRAGMA user_version.
func SchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

// QuickCheck runs PRAGMA quick_check and returns its problem lines, or nil
// when the database is fine.
func QuickCheck(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return nil, fmt.Errorf("quick check: %w", err)
	}
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			return nil, fmt.Errorf("quick check: %w", err)
		}
		lines = append(lines, line)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("quick check: %w", err)
	}
	if len(lines) == 1 && lines[0] == "ok" {
		return nil, nil
	}
	return lines, nil
}

// Backup writes a consistent copy of the live database to dest with VACUUM
// INTO (safe next to a running server: it reads inside one transaction). The
// copy is built in a private temp file beside dest, synced, and linked into
// place, so dest never exists half-written and is never overwritten.
func Backup(ctx context.Context, db *sql.DB, dest string) (err error) {
	if _, statErr := os.Lstat(dest); statErr == nil {
		return fmt.Errorf("backup destination %s already exists", dest)
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return fmt.Errorf("backup destination: %w", statErr)
	}
	tmp := fmt.Sprintf("%s.tmp-%d", dest, os.Getpid())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create backup file: %w", err)
	}
	f.Close()
	defer func() {
		if err != nil {
			os.Remove(tmp)
		}
	}()
	if _, err = db.ExecContext(ctx, "VACUUM INTO ?", tmp); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if err = syncPath(tmp); err != nil {
		return fmt.Errorf("sync backup: %w", err)
	}
	if err = publish(tmp, dest); err != nil {
		return err
	}
	os.Remove(tmp)
	if d, openErr := os.Open(filepath.Dir(dest)); openErr == nil {
		d.Sync() //nolint:errcheck // best effort
		d.Close()
	}
	return nil
}

// linkFile is a seam so tests can simulate a filesystem without hard links.
var linkFile = os.Link

// publish moves tmp to dest without ever overwriting dest. A hard link is
// atomic; on filesystems that cannot link (FAT, some network mounts) it falls
// back to an exclusive create plus copy, removing the partial dest on failure.
func publish(tmp, dest string) error {
	err := linkFile(tmp, dest)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.ENOTSUP) && !errors.Is(err, syscall.EOPNOTSUPP) {
		return fmt.Errorf("publish backup: %w", err)
	}
	if err := copyExclusive(tmp, dest); err != nil {
		return fmt.Errorf("publish backup (destination filesystem does not support hard links): %w", err)
	}
	return nil
}

func copyExclusive(src, dest string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.Remove(dest)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err = out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func syncPath(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
