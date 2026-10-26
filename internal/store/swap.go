package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CheckError reports a database file that failed PRAGMA quick_check.
type CheckError struct {
	Path     string
	Problems []string
}

func (e *CheckError) Error() string {
	return fmt.Sprintf("%s failed quick_check: %s", e.Path, strings.Join(e.Problems, "; "))
}

// VerifyFile opens path read-only, runs quick_check and returns its schema
// version. An older schema is accepted (the server migrates it at start); a
// newer one fails with *SchemaMismatchError, a damaged file with *CheckError.
func VerifyFile(ctx context.Context, path string) (int, error) {
	db, err := OpenExisting(path, OpenOptions{ReadOnly: true, AllowOlderSchema: true})
	if err != nil {
		return 0, fmt.Errorf("verify %s: %w", path, err)
	}
	defer db.Close()
	problems, err := QuickCheck(ctx, db)
	if err != nil {
		return 0, fmt.Errorf("verify %s: %w", path, err)
	}
	if len(problems) > 0 {
		return 0, &CheckError{Path: path, Problems: problems}
	}
	return SchemaVersion(ctx, db)
}

// CheckpointBeforeSwap folds a WAL left by a crashed server into the current
// database file, so committed rows live in the main file that is kept as the
// .bak and are not orphaned in a -wal if the swap is interrupted. Without a
// -wal there is nothing to do. A database that cannot be opened or
// checkpointed (garbage, corrupt, newer schema) is exactly what a restore
// replaces, so that comes back as a warning: the swap then moves the -wal
// next to the .bak. A busy database is an error, since a server may still be
// using it.
func CheckpointBeforeSwap(ctx context.Context, current string) (warning, err error) {
	if _, err := os.Stat(current + "-wal"); err != nil {
		return nil, nil
	}
	if _, err := os.Stat(current); err != nil {
		return nil, nil
	}
	// Plain read-write open: no schema gate, no migration, never creates.
	db, err := sql.Open("sqlite", FileURI(current)+"?mode=rw&_pragma=busy_timeout(5000)")
	if err != nil {
		return err, nil
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var busy, logFrames, checkpointed int
	if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		if IsBusy(err) {
			return nil, fmt.Errorf("checkpoint the current database before restoring: database is busy (is the server still running?): %w", err)
		}
		return err, nil
	}
	if busy != 0 {
		return nil, errors.New("checkpoint the current database before restoring: database is busy (is the server still running?)")
	}
	return nil, nil
}

// SwapFiles installs the verified file staged as the current database. The
// database path always exists: the old file is hard-linked to its .bak name
// and staged is renamed over it, so any failure before that rename leaves the
// current database (and its -wal) as it was. It returns the path the previous
// database was kept at ("" when there was none). rename is os.Rename except
// in tests that inject failures.
func SwapFiles(current, staged string, now time.Time, rename func(from, to string) error) (bak string, err error) {
	_, statErr := os.Stat(current)
	hadDB := statErr == nil
	wal, shm := current+"-wal", current+"-shm"

	// Sidecars first: a stale WAL must never be replayed into the new file. A
	// WAL left by a crash belongs to the old file, so it is kept with it.
	walMoved := false
	if hadDB {
		bak = fmt.Sprintf("%s.bak-%s", current, now.UTC().Format("20060102T150405Z"))
		if err := rename(wal, bak+"-wal"); err == nil {
			walMoved = true
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	} else if err := os.Remove(wal); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	undo := func(cause error) (string, error) {
		errs := []error{cause}
		if walMoved {
			if err := rename(bak+"-wal", wal); err != nil {
				errs = append(errs, fmt.Errorf("could not put %s back: %w", wal, err))
			}
		}
		return "", errors.Join(errs...)
	}
	if err := os.Remove(shm); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return undo(err)
	}
	if hadDB {
		if err := os.Link(current, bak); err != nil {
			return undo(err)
		}
	}
	if err := rename(staged, current); err != nil {
		if hadDB {
			if rerr := os.Remove(bak); rerr != nil {
				err = errors.Join(err, rerr)
			}
		}
		return undo(err)
	}
	if err := syncDir(filepath.Dir(current)); err != nil {
		return bak, err
	}
	return bak, nil
}

// SwapIn verifies staged, checkpoints the current database and installs
// staged in its place, keeping the old database as <current>.bak-<UTC ts>
// (with its -wal, if any, beside it). The server must not be using current.
func SwapIn(current, staged string, now time.Time) error {
	ctx := context.Background()
	if _, err := VerifyFile(ctx, staged); err != nil {
		return err
	}
	if _, err := CheckpointBeforeSwap(ctx, current); err != nil {
		return err
	}
	_, err := SwapFiles(current, staged, now, os.Rename)
	return err
}

// IsBusy reports SQLITE_BUSY / SQLITE_LOCKED errors.
func IsBusy(err error) bool {
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

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
