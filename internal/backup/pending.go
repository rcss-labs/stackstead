package backup

import (
	"context"
	"database/sql"
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
	postApplyFile   = "post-apply.json"
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

func writePending(dataDir string, p Pending) error { return writeMarker(dataDir, pendingFile, p) }

// writeMarker durably replaces <data>/restore/<name> with v as JSON.
func writeMarker(dataDir, name string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := restoreDir(dataDir)
	final := filepath.Join(dir, name)
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
// removes the staged files and returns the marker for the post-start work.
// When the returned PauseSchedule is set, the caller runs
// PauseScheduleAfterRestore once the server has migrated the database.
// Without a marker it returns nil, nil. It is safe to run again after a crash
// part-way through.
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
	// The post-start work is recorded before the pending marker goes, so a
	// crash or a failed step from here on is finished by a later start.
	if err := WritePostApply(dataDir, PostApply{StartStacks: p.StartStacks, PauseSchedule: p.PauseSchedule}); err != nil {
		return nil, fmt.Errorf("record post-restore work: %w", err)
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

// PauseScheduleAfterRestore turns the backup schedule off in a restored
// database and flags it for the UI, so a clone does not write over the old
// host's archives. db must already be migrated (store.Open).
func PauseScheduleAfterRestore(ctx context.Context, db *sql.DB) error {
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

// PostApply is the work left after the restored database is installed.
type PostApply struct {
	StartStacks   []string `json:"startStacks"`
	PauseSchedule bool     `json:"pauseSchedule"`
}

// ReadPostApply returns the post-start work a host restore left, or nil when
// there is none.
func ReadPostApply(dataDir string) (*PostApply, error) {
	raw, err := os.ReadFile(filepath.Join(restoreDir(dataDir), postApplyFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read post-restore work: %w", err)
	}
	var p PostApply
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("read post-restore work: %w", err)
	}
	return &p, nil
}

// WritePostApply records the work still to do; with nothing left it removes
// the marker.
func WritePostApply(dataDir string, p PostApply) error {
	if !p.PauseSchedule && len(p.StartStacks) == 0 {
		err := os.Remove(filepath.Join(restoreDir(dataDir), postApplyFile))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeMarker(dataDir, postApplyFile, p)
}
