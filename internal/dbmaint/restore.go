package dbmaint

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/rcss-labs/stackstead/internal/lockfile"
	"github.com/rcss-labs/stackstead/internal/store"
)

// Restore installs a verified snapshot while holding the stopped-server lock.
// failedDir must not exist. Failed DB/WAL/SHM bytes are retained there untouched
// for diagnosis; opening/checkpointing a failed migration could alter them.
func Restore(ctx context.Context, dataDir, src, failedDir string) error {
	if err := RefuseLiveDestination(dataDir, src); err != nil {
		return err
	}
	if err := checkOwnership(dataDir); err != nil {
		return err
	}
	lock, err := lockfile.Acquire(filepath.Join(dataDir, "stackstead.lock"))
	if err != nil {
		return err
	}
	defer lock.Release()
	if _, _, err := VerifyDatabaseFile(ctx, src); err != nil {
		return err
	}
	source, err := store.OpenExisting(src, store.OpenOptions{ReadOnly: true, AllowOlderSchema: true})
	if err != nil {
		return err
	}
	defer source.Close()
	f, err := os.CreateTemp(dataDir, ".restore-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	f.Close()
	os.Remove(tmp)
	defer os.Remove(tmp)
	if err := store.Backup(ctx, source, tmp); err != nil {
		return fmt.Errorf("copy backup: %w", err)
	}
	if _, _, err := VerifyDatabaseFile(ctx, tmp); err != nil {
		return err
	}
	if err := os.Mkdir(failedDir, 0700); err != nil {
		return err
	}
	if err := SyncDir(filepath.Dir(failedDir)); err != nil {
		return err
	}
	_, err = SwapInDatabase(filepath.Join(dataDir, "stackstead.db"), tmp, filepath.Join(failedDir, "stackstead.db"), SwapOptions{PreserveSHM: true})
	return err
}

// SwapOptions preserves the CLI's existing fault-injection seams and its SHM
// cleanup policy. Stopped updater recovery also retains SHM for diagnosis.
type SwapOptions struct {
	Rename      func(string, string) error
	SyncDir     func(string) error
	PreserveSHM bool
}

// SwapInDatabase publishes an already verified copy. A hard link retains the old
// main file until the final rename; failures restore moved sidecars.
func SwapInDatabase(current, tmp, bak string, opts SwapOptions) (string, error) {
	rename := opts.Rename
	if rename == nil {
		rename = os.Rename
	}
	syncDir := opts.SyncDir
	if syncDir == nil {
		syncDir = SyncDir
	}
	_, statErr := os.Stat(current)
	hadDB := statErr == nil
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return "", statErr
	}
	moved := []string{}
	undo := func(cause error) (string, error) {
		errs := []error{cause}
		for i := len(moved) - 1; i >= 0; i-- {
			s := moved[i]
			if e := rename(bak+s, current+s); e != nil {
				errs = append(errs, fmt.Errorf("could not put %s back: %w", current+s, e))
			}
		}
		return "", errors.Join(errs...)
	}
	suffixes := []string{"-wal"}
	if opts.PreserveSHM {
		suffixes = append(suffixes, "-shm")
	}
	for _, s := range suffixes {
		if hadDB || opts.PreserveSHM {
			if err := rename(current+s, bak+s); err == nil {
				moved = append(moved, s)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return undo(err)
			}
		} else if err := os.Remove(current + s); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return undo(err)
		}
	}
	if !opts.PreserveSHM {
		if err := os.Remove(current + "-shm"); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return undo(err)
		}
	}
	if hadDB {
		if err := os.Link(current, bak); err != nil {
			return undo(err)
		}
	}
	if err := rename(tmp, current); err != nil {
		if hadDB {
			if e := os.Remove(bak); e != nil {
				err = errors.Join(err, e)
			}
		}
		return undo(err)
	}
	if (hadDB || len(moved) > 0) && filepath.Dir(bak) != filepath.Dir(current) {
		if err := syncDir(filepath.Dir(bak)); err != nil {
			return bak, err
		}
	}
	if err := syncDir(filepath.Dir(current)); err != nil {
		return bak, err
	}
	if !hadDB && len(moved) == 0 {
		return "", nil
	}
	return bak, nil
}
