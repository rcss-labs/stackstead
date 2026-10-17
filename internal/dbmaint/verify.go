package dbmaint

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"os"
	"strings"

	"github.com/rcss-labs/stackstead/internal/store"
)

// VerifyDatabaseFile opens a copy read-only and runs quick_check on it.
func VerifyDatabaseFile(ctx context.Context, path string) (schema int, size int64, err error) {
	db, err := store.OpenExisting(path, store.OpenOptions{ReadOnly: true, AllowOlderSchema: true})
	if err != nil {
		return 0, 0, fmt.Errorf("verify %s: %w", path, err)
	}
	defer db.Close()
	problems, err := store.QuickCheck(ctx, db)
	if err != nil {
		return 0, 0, fmt.Errorf("verify %s: %w", path, err)
	}
	if len(problems) > 0 {
		return 0, 0, fmt.Errorf("%s failed quick_check: %s", path, strings.Join(problems, "; "))
	}
	if schema, err = store.SchemaVersion(ctx, db); err != nil {
		return 0, 0, err
	}
	return schema, sizeOf(path), nil
}

// CheckpointCurrent folds a WAL left by a crashed server into the current
// database file, so committed rows live in the main file that is kept as the
// .bak and are not orphaned in a -wal if the swap is interrupted. Without a
// -wal there is nothing to do. A database that cannot be opened or
// checkpointed (garbage, corrupt, newer schema) is exactly what restore is
// for, so that only warns: the swap then moves the -wal next to the .bak. A
// busy database is refused, since a server may still be using it.
func CheckpointCurrent(ctx context.Context, current string, warn func(error) error) error {
	if _, err := os.Stat(current + "-wal"); err != nil {
		return nil
	}
	if _, err := os.Stat(current); err != nil {
		return nil
	}
	// Plain read-write open: no schema gate, no migration, never creates.
	escaped := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(current)
	db, err := sql.Open("sqlite", "file:"+escaped+"?mode=rw&_pragma=busy_timeout(5000)")
	if err != nil {
		return warn(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var busy, logFrames, checkpointed int
	if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		if IsBusyError(err) {
			return fmt.Errorf("checkpoint the current database before restoring: database is busy (is the server still running?): %w", err)
		}
		return warn(err)
	}
	if busy != 0 {
		return fmt.Errorf("checkpoint the current database before restoring: database is busy (is the server still running?)")
	}
	return nil
}

// IsBusyError reports SQLITE_BUSY / SQLITE_LOCKED errors.
func IsBusyError(err error) bool {
	if err == nil {
		return false
	}
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		switch coded.Code() & 0xff { // primary result code
		case 5, 6: // SQLITE_BUSY, SQLITE_LOCKED
			return true
		}
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "SQLITE_LOCKED") || strings.Contains(msg, "is locked")
}

func sizeOf(path string) int64 {
	if i, e := os.Stat(path); e == nil {
		return i.Size()
	}
	return 0
}
