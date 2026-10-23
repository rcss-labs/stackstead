// Package history records container state transitions and builds health
// timelines from them.
package history

import (
	"context"
	"database/sql"
	"time"
)

// Container states as stored. StateRestart is a marker row, not a state.
// StateNoData appears only in timelines and is never stored.
const (
	StateRunning    = "running"
	StateHealthy    = "healthy"
	StateUnhealthy  = "unhealthy"
	StateStarting   = "starting"
	StateRestarting = "restarting"
	StateExited     = "exited"
	StateRemoved    = "removed"
	StateRestart    = "restart"
	StateNoData     = "nodata"
)

// Row is one container state change. ExitCode is nil when not applicable.
type Row struct {
	TS                                 time.Time
	Project, Service, Container, State string
	ExitCode                           *int
}

// Store persists transitions in a migrated database.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Insert writes all rows in one transaction.
func (s *Store) Insert(ctx context.Context, rows []Row) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO container_transitions(ts,project,service,container,state,exit_code) VALUES(?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		var code any
		if r.ExitCode != nil {
			code = *r.ExitCode
		}
		if _, err := stmt.ExecContext(ctx, r.TS.Unix(), r.Project, r.Service, r.Container, r.State, code); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Query returns, per container, the latest non-marker row before from (the
// state in force when the window opens) followed by every row in [from, to],
// ordered by container, ts, insertion order.
func (s *Store) Query(ctx context.Context, project string, from, to time.Time) ([]Row, error) {
	f, t := from.Unix(), to.Unix()
	rows, err := s.db.QueryContext(ctx, `
SELECT ts, project, service, container, state, exit_code FROM (
	SELECT c.rowid AS rid, c.* FROM container_transitions c
	WHERE c.rowid IN (
		SELECT MAX(c2.rowid) FROM container_transitions c2
		JOIN (
			SELECT container, MAX(ts) AS mts FROM container_transitions
			WHERE project = ? AND ts < ? AND state != 'restart'
			GROUP BY container
		) m ON m.container = c2.container AND m.mts = c2.ts
		WHERE c2.project = ? AND c2.state != 'restart'
		GROUP BY c2.container
	)
	UNION ALL
	SELECT rowid AS rid, * FROM container_transitions
	WHERE project = ? AND ts >= ? AND ts <= ?
) ORDER BY container, ts, rid`, project, f, project, project, f, t)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

// Latest returns every container's state in force — its latest non-marker
// row — across all projects, leaving out containers already marked removed.
func (s *Store) Latest(ctx context.Context) ([]Row, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ts, project, service, container, state, exit_code
FROM container_transitions WHERE state != 'removed' AND rowid IN (`+latestRowIDs+`)`)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

func scanRows(rows *sql.Rows) ([]Row, error) {
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var ts int64
		var code sql.NullInt64
		if err := rows.Scan(&ts, &r.Project, &r.Service, &r.Container, &r.State, &code); err != nil {
			return nil, err
		}
		r.TS = time.Unix(ts, 0)
		if code.Valid {
			c := int(code.Int64)
			r.ExitCode = &c
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// latestRowIDs selects, per container, the rowid of its latest non-marker row
// (ties: the last inserted) — the state in force now.
const latestRowIDs = `
SELECT MAX(c2.rowid) FROM container_transitions c2
JOIN (
	SELECT container, MAX(ts) AS mts FROM container_transitions
	WHERE state != 'restart'
	GROUP BY container
) m ON m.container = c2.container AND m.mts = c2.ts
WHERE c2.state != 'restart'
GROUP BY c2.container`

// Prune deletes rows older than olderThan and reports how many were removed.
// Each container keeps its predecessor: the latest non-marker row before the
// cutoff, which is the state in force when the retained window opens, so
// timelines starting at the cutoff know their baseline. A container that never
// changes therefore keeps its single row however old it is. A removed
// container's predecessor goes once it ages out, so dead containers do not
// accumulate.
func (s *Store) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	cutoff := olderThan.Unix()
	res, err := s.db.ExecContext(ctx, `DELETE FROM container_transitions
WHERE ts < ? AND (state = 'removed' OR rowid NOT IN (
	SELECT MAX(c2.rowid) FROM container_transitions c2
	JOIN (
		SELECT container, MAX(ts) AS mts FROM container_transitions
		WHERE ts < ? AND state != 'restart'
		GROUP BY container
	) m ON m.container = c2.container AND m.mts = c2.ts
	WHERE c2.state != 'restart'
	GROUP BY c2.container
))`, cutoff, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
