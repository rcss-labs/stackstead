package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/rcss-labs/stackstead/internal/store"
)

const (
	restoreDirName  = "restore"
	pendingFile     = "pending.json"
	stagedDBName    = "stackstead.db"
	liveDBName      = "stackstead.db"
	stagedConfigYML = "config.yaml"

	// settingPausedAfterRestore tells the UI to show the "backups paused" banner.
	settingPausedAfterRestore = "backup_paused_after_restore"
)

// Pending is the marker a host restore leaves in <data>/restore/pending.json.
// The next start installs the staged database and then starts StartStacks.
type Pending struct {
	StagedDB      string   `json:"stagedDb"`
	StartStacks   []string `json:"startStacks"`
	PauseSchedule bool     `json:"pauseSchedule"`
}

func restoreDir(dataDir string) string { return filepath.Join(dataDir, restoreDirName) }

func writePending(dataDir string, p Pending) error {
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	dir := restoreDir(dataDir)
	final := filepath.Join(dir, pendingFile)
	tmp := final + partialSuffix
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(dir)
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// ApplyPending finishes a host restore at startup, before the database is
// opened: it swaps the staged database in (keeping the old one as a .bak),
// pauses the backup schedule in it, removes the staged files and returns the
// marker for the post-start work. Without a marker it returns nil, nil. It is
// safe to run again after a crash part-way through.
func ApplyPending(dataDir string, now time.Time) (*Pending, error) {
	dir := restoreDir(dataDir)
	markerPath := filepath.Join(dir, pendingFile)
	raw, err := os.ReadFile(markerPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read pending restore: %w", err)
	}
	var p Pending
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("read pending restore: %w", err)
	}
	staged := filepath.Join(dir, stagedDBName)
	if filepath.Clean(p.StagedDB) != staged {
		return nil, fmt.Errorf("pending restore names %q, not the staged database %s", p.StagedDB, staged)
	}
	current := filepath.Join(dataDir, liveDBName)
	switch _, err := os.Stat(staged); {
	case err == nil:
		if err := store.SwapIn(current, staged, now); err != nil {
			return nil, fmt.Errorf("install restored database: %w", err)
		}
	case errors.Is(err, fs.ErrNotExist):
		// Swapped by an earlier start that stopped before removing the marker.
	default:
		return nil, fmt.Errorf("install restored database: %w", err)
	}
	if p.PauseSchedule {
		if err := pauseSchedule(current); err != nil {
			return nil, err
		}
	}
	for _, name := range []string{staged + "-wal", staged + "-shm", markerPath} {
		if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("remove staged restore files: %w", err)
		}
	}
	if err := syncDir(dir); err != nil {
		return nil, err
	}
	return &p, nil
}

// pauseSchedule turns the backup schedule off in the restored database and
// flags it for the UI, so a clone does not write over the old host's archives.
func pauseSchedule(dbPath string) error {
	ctx := context.Background()
	db, err := store.OpenExisting(dbPath, store.OpenOptions{AllowOlderSchema: true})
	if err != nil {
		return fmt.Errorf("pause backup schedule: %w", err)
	}
	defer db.Close()
	cs := NewConfigStore(db)
	cfg, err := cs.Load(ctx)
	if err != nil {
		return fmt.Errorf("pause backup schedule: %w", err)
	}
	cfg.Schedule.Enabled = false
	if err := cs.Save(ctx, cfg); err != nil {
		return fmt.Errorf("pause backup schedule: %w", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, 'true')
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, settingPausedAfterRestore); err != nil {
		return fmt.Errorf("pause backup schedule: %w", err)
	}
	return nil
}
