// Package dbmaint provides verified SQLite maintenance for stopped app instances.
// The caller must establish the registered app UID and run as that UID. It must
// provide app-accessible, bounded snapshot/archive paths; root-private updater
// staging cannot be passed directly to a credential-dropped process.
package dbmaint

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/rcss-labs/stackstead/internal/lockfile"
	"github.com/rcss-labs/stackstead/internal/store"
)

// Snapshot holds the server lock and includes committed WAL transactions using
// SQLite's backup operation, never a filesystem copy of a live database.
func Snapshot(ctx context.Context, dataDir, dest string) (schema int, digest string, err error) {
	if err = RefuseLiveDestination(dataDir, dest); err != nil {
		return
	}
	if err = checkOwnership(dataDir); err != nil {
		return
	}
	lock, err := lockfile.Acquire(filepath.Join(dataDir, "stackstead.lock"))
	if err != nil {
		return 0, "", err
	}
	defer lock.Release()
	db, err := store.OpenExisting(filepath.Join(dataDir, "stackstead.db"), store.OpenOptions{ReadOnly: true, AllowOlderSchema: true})
	if err != nil {
		return 0, "", err
	}
	defer db.Close()
	if err = store.Backup(ctx, db, dest); err != nil {
		return 0, "", err
	}
	schema, _, err = VerifyDatabaseFile(ctx, dest)
	if err != nil {
		os.Remove(dest)
		return 0, "", err
	}
	f, err := os.Open(dest)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return 0, "", err
	}
	if err = SyncDir(filepath.Dir(dest)); err != nil {
		return 0, "", err
	}
	return schema, fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// FileOwner returns the effective filesystem owner without a root exception.
func FileOwner(path string) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("cannot determine the owner of %s", path)
	}
	return int(st.Uid), nil
}
func checkOwnership(dataDir string) error {
	for _, p := range []string{dataDir, filepath.Join(dataDir, "stackstead.db"), filepath.Join(dataDir, "stackstead.db-wal"), filepath.Join(dataDir, "stackstead.db-shm"), filepath.Join(dataDir, "stackstead.lock")} {
		uid, err := FileOwner(p)
		if os.IsNotExist(err) && p != dataDir {
			continue
		}
		if err != nil {
			return err
		}
		if uid != os.Geteuid() {
			return fmt.Errorf("%s belongs to uid %d; run as that user", p, uid)
		}
	}
	return nil
}

// SyncDir persists directory entry changes.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// resolvePath makes p absolute and resolves symlinks as far as they exist.
func resolvePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if full, err := filepath.EvalSymlinks(abs); err == nil {
		return full
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(dir, filepath.Base(abs))
	}
	return abs
}

// refuseLiveDestination keeps a backup from being written over the live
// database, its WAL/SHM sidecars or the server lock, however it is spelled
// (relative, through a symlink or a hard link).
func RefuseLiveDestination(dataDir, dest string) error {
	db := filepath.Join(dataDir, "stackstead.db")
	protected := []string{db, db + "-wal", db + "-shm", filepath.Join(dataDir, "stackstead.lock")}
	resolved := resolvePath(dest)
	destInfo, destErr := os.Stat(dest)
	for _, p := range protected {
		same := resolved == resolvePath(p)
		if info, err := os.Stat(p); err == nil && destErr == nil && os.SameFile(destInfo, info) {
			same = true
		}
		if same {
			return fmt.Errorf("%s is the live database or one of its files; choose another destination", dest)
		}
	}
	return nil
}
