package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Run statuses and triggers stored in backup_runs.
const (
	runStatusRunning = "running"
	runStatusFailed  = "failed"
	interruptedError = "interrupted: server stopped"
)

// Run is one backup attempt and its per-item outcomes.
type Run struct {
	ID           int64
	StartedAt    time.Time
	EndedAt      time.Time
	Trigger      string
	Status       string
	Archive      string
	Size         int64
	Destinations []string
	Items        []ItemResult
	Error        string
}

// RunStore records backup runs in the backup_runs table.
type RunStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewRunStore returns a RunStore over db. now supplies the start and end times.
func NewRunStore(db *sql.DB, now func() time.Time) *RunStore {
	return &RunStore{db: db, now: now}
}

// Begin records a new running run and returns its id.
func (s *RunStore) Begin(ctx context.Context, trigger string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO backup_runs(started_at, trigger, status) VALUES(?, ?, ?)`,
		s.now().Unix(), trigger, runStatusRunning)
	if err != nil {
		return 0, fmt.Errorf("begin backup run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("begin backup run: %w", err)
	}
	return id, nil
}

// Finish stores the final state of run, identified by run.ID. A zero EndedAt is
// replaced with the current time.
func (s *RunStore) Finish(ctx context.Context, run Run) error {
	endedAt := run.EndedAt
	if endedAt.IsZero() {
		endedAt = s.now()
	}
	destinations, err := encodeJSONList(run.Destinations)
	if err != nil {
		return fmt.Errorf("finish backup run %d: %w", run.ID, err)
	}
	items, err := encodeJSONList(run.Items)
	if err != nil {
		return fmt.Errorf("finish backup run %d: %w", run.ID, err)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE backup_runs
		SET ended_at = ?, trigger = ?, status = ?, archive = ?, size = ?, destinations = ?, items = ?, error = ?
		WHERE id = ?`,
		endedAt.Unix(), run.Trigger, run.Status, run.Archive, run.Size, destinations, items, run.Error, run.ID)
	if err != nil {
		return fmt.Errorf("finish backup run %d: %w", run.ID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("finish backup run %d: no such run", run.ID)
	}
	return nil
}

// Recent returns up to limit runs, newest first, including running ones.
func (s *RunStore) Recent(ctx context.Context, limit int) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, runSelect+` ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read backup runs: %w", err)
	}
	defer rows.Close()
	var runs []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read backup runs: %w", err)
	}
	return runs, nil
}

// Last returns the most recent finished run. found is false when none has finished.
func (s *RunStore) Last(ctx context.Context) (run Run, found bool, err error) {
	row := s.db.QueryRowContext(ctx, runSelect+` WHERE ended_at IS NOT NULL ORDER BY id DESC LIMIT 1`)
	run, err = scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	return run, true, nil
}

// MarkInterrupted marks every running run as failed. It runs at startup, when
// no run started before the restart can still be in progress.
func (s *RunStore) MarkInterrupted(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backup_runs SET status = ?, error = ?, ended_at = ? WHERE status = ?`,
		runStatusFailed, interruptedError, s.now().Unix(), runStatusRunning)
	if err != nil {
		return fmt.Errorf("mark interrupted backup runs: %w", err)
	}
	return nil
}

const runSelect = `SELECT id, started_at, ended_at, trigger, status, archive, size, destinations, items, error FROM backup_runs`

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRun(row rowScanner) (Run, error) {
	var (
		run                 Run
		startedAt           int64
		endedAt             sql.NullInt64
		archive, errText    sql.NullString
		size                sql.NullInt64
		destinations, items string
	)
	err := row.Scan(&run.ID, &startedAt, &endedAt, &run.Trigger, &run.Status, &archive, &size, &destinations, &items, &errText)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, err
		}
		return Run{}, fmt.Errorf("read backup run: %w", err)
	}
	run.StartedAt = time.Unix(startedAt, 0)
	if endedAt.Valid {
		run.EndedAt = time.Unix(endedAt.Int64, 0)
	}
	run.Archive = archive.String
	run.Size = size.Int64
	run.Error = errText.String
	if err := json.Unmarshal([]byte(destinations), &run.Destinations); err != nil {
		return Run{}, fmt.Errorf("decode backup run %d destinations: %w", run.ID, err)
	}
	if err := json.Unmarshal([]byte(items), &run.Items); err != nil {
		return Run{}, fmt.Errorf("decode backup run %d items: %w", run.ID, err)
	}
	return run, nil
}

// encodeJSONList encodes a list as a JSON array, using [] for a nil list.
func encodeJSONList[T any](list []T) (string, error) {
	if list == nil {
		list = []T{}
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
