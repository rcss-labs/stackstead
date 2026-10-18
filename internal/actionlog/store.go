// Package actionlog persists storage cleanup results independently of Docker.
package actionlog

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"time"

	"github.com/rcss-labs/stackstead/internal/stacks"
)

// Entry records one item's result. Bytes is nil when the size is unknown.
// Timestamps are stored with one-second resolution; ID is assigned by SQLite.
type Entry struct {
	ID          int64
	TS          time.Time
	Username    string
	Kind        string
	Scope       string
	Target      string
	Detail      string
	Bytes       *int64
	Result      string
	OperationID string
}

// Filter selects recent entries. Empty prefix and scope match all entries.
// Limit defaults to 50 when zero and otherwise is clamped to [1, 500].
type Filter struct {
	KindPrefix, Scope string
	Limit             int
}

// Store persists cleanup entries in a migrated database.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

var kindPattern = regexp.MustCompile(`^[a-z]+\.[a-z]+$`)

// Append validates and records an entry; a zero timestamp defaults to now.
func (s *Store) Append(ctx context.Context, e Entry) error {
	if !kindPattern.MatchString(e.Kind) {
		return fmt.Errorf("action log: invalid kind %q", e.Kind)
	}
	if e.Scope != "host" {
		if err := stacks.ValidateName(e.Scope); err != nil {
			return fmt.Errorf("action log: invalid scope: %w", err)
		}
	}
	if e.Username == "" {
		return fmt.Errorf("action log: username is required")
	}
	if e.Result == "" {
		return fmt.Errorf("action log: result is required")
	}
	if e.TS.IsZero() {
		e.TS = s.now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO action_log (ts, username, kind, scope, target, detail, bytes, result, operation_id)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, e.TS.Unix(), e.Username, e.Kind, e.Scope, e.Target, e.Detail, e.Bytes, e.Result, e.OperationID)
	if err != nil {
		return fmt.Errorf("append action log: %w", err)
	}
	return nil
}

// Recent returns newest entries first, breaking timestamp ties by newest ID.
// Prefixes are case-sensitive literal strings, never LIKE patterns.
func (s *Store) Recent(ctx context.Context, f Filter) ([]Entry, error) {
	limit := f.Limit
	if limit == 0 {
		limit = 50
	}
	limit = max(1, min(500, limit))
	rows, err := s.db.QueryContext(ctx, `SELECT id, ts, username, kind, scope, target, detail, bytes, result, operation_id
 FROM action_log WHERE substr(kind, 1, length(?)) = ? AND (? = '' OR scope = ?)
 ORDER BY ts DESC, id DESC LIMIT ?`, f.KindPrefix, f.KindPrefix, f.Scope, f.Scope, limit)
	if err != nil {
		return nil, fmt.Errorf("query action log: %w", err)
	}
	defer rows.Close()
	out := make([]Entry, 0)
	for rows.Next() {
		var e Entry
		var ts int64
		var bytes sql.NullInt64
		if err := rows.Scan(&e.ID, &ts, &e.Username, &e.Kind, &e.Scope, &e.Target, &e.Detail, &bytes, &e.Result, &e.OperationID); err != nil {
			return nil, fmt.Errorf("scan action log: %w", err)
		}
		e.TS = time.Unix(ts, 0).UTC()
		if bytes.Valid {
			e.Bytes = &bytes.Int64
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query action log: %w", err)
	}
	return out, nil
}

// Prune removes entries strictly older than olderThan and returns their count.
func (s *Store) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM action_log WHERE ts < ?`, olderThan.Unix())
	if err != nil {
		return 0, fmt.Errorf("prune action log: %w", err)
	}
	return res.RowsAffected()
}
