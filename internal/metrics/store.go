// Package metrics stores and serves container resource samples.
package metrics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// Sample is one reading of one container. Net and block counters are the
// cumulative values Docker reports; rates are derived by readers.
type Sample struct {
	TS                              time.Time
	Project, Service, Container     string
	CPU                             float64
	Mem, MemLimit                   uint64
	NetRx, NetTx, BlkRead, BlkWrite uint64
}

// Point is a Query result row: per (service, bucket) the gauges (CPU, Mem,
// MemLimit) are averaged and the cumulative counters take their maximum.
type Point struct {
	TS                              time.Time
	Service                         string
	CPU                             float64
	Mem, MemLimit                   uint64
	NetRx, NetTx, BlkRead, BlkWrite uint64
}

// Store persists samples in SQLite. SQLite integers are signed 64-bit, so
// uint64 values above math.MaxInt64 are clamped to math.MaxInt64 on insert
// (real memory and byte counters never get near 8 EiB).
type Store struct {
	db *sql.DB
}

const tokenHashKey = "metrics_token_hash"

// NewStore wraps db. The now parameter is unused (callers pass explicit times
// to every method); it is kept so existing call sites keep compiling.
func NewStore(db *sql.DB, _ func() time.Time) *Store {
	return &Store{db: db}
}

func clamp(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// InsertBatch writes all samples in a single transaction. Timestamps are
// stored with one-second resolution.
func (s *Store) InsertBatch(ctx context.Context, samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin metrics insert: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO metric_samples
		(ts, project, service, container, cpu, mem, mem_limit, net_rx, net_tx, blk_read, blk_write)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare metrics insert: %w", err)
	}
	defer stmt.Close()
	for _, m := range samples {
		if _, err := stmt.ExecContext(ctx, m.TS.Unix(), m.Project, m.Service, m.Container, m.CPU,
			clamp(m.Mem), clamp(m.MemLimit), clamp(m.NetRx), clamp(m.NetTx), clamp(m.BlkRead), clamp(m.BlkWrite)); err != nil {
			return fmt.Errorf("insert metric sample: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit metrics insert: %w", err)
	}
	return nil
}

// Query returns samples of project with from <= ts < to (whole seconds),
// ordered by service then time. An empty service means all services. With
// step > 0 rows are aggregated per service into step-sized buckets (a Point's
// TS is its bucket start; steps are rounded down to whole seconds, minimum 1);
// with step <= 0 the raw rows are returned.
func (s *Store) Query(ctx context.Context, project, service string, from, to time.Time, step time.Duration) ([]Point, error) {
	var query string
	args := []any{project, service, service, from.Unix(), to.Unix()}
	if step > 0 {
		secs := int64(step / time.Second)
		if secs < 1 {
			secs = 1
		}
		query = `SELECT ts - ts % ?, service, AVG(cpu), CAST(AVG(mem) AS INTEGER), CAST(AVG(mem_limit) AS INTEGER),
				MAX(net_rx), MAX(net_tx), MAX(blk_read), MAX(blk_write)
			FROM metric_samples
			WHERE project = ? AND (? = '' OR service = ?) AND ts >= ? AND ts < ?
			GROUP BY service, ts - ts % ? ORDER BY service, 1`
		args = append([]any{secs}, args...)
		args = append(args, secs)
	} else {
		query = `SELECT ts, service, cpu, mem, mem_limit, net_rx, net_tx, blk_read, blk_write
			FROM metric_samples
			WHERE project = ? AND (? = '' OR service = ?) AND ts >= ? AND ts < ?
			ORDER BY service, ts`
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query metrics: %w", err)
	}
	defer rows.Close()
	var out []Point
	for rows.Next() {
		var p Point
		var ts int64
		var mem, limit, rx, tx, br, bw int64
		if err := rows.Scan(&ts, &p.Service, &p.CPU, &mem, &limit, &rx, &tx, &br, &bw); err != nil {
			return nil, fmt.Errorf("scan metrics: %w", err)
		}
		p.TS = time.Unix(ts, 0)
		p.Mem, p.MemLimit, p.NetRx, p.NetTx, p.BlkRead, p.BlkWrite = uint64(mem), uint64(limit), uint64(rx), uint64(tx), uint64(br), uint64(bw)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query metrics: %w", err)
	}
	return out, nil
}

// QueryContainers returns, for project (and service unless empty), each
// container's samples with from <= ts < to grouped into step-sized buckets
// (minimum 1s; TS is the bucket start), ordered by service, container, TS.
// Gauges come back as sums with a sample count and counters as the bucket's
// maximum, the shape AggregateServices consumes.
func (s *Store) QueryContainers(ctx context.Context, project, service string, from, to time.Time, step time.Duration) ([]ContainerBucket, error) {
	secs := int64(step / time.Second)
	if secs < 1 {
		secs = 1
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ts - ts % ?, service, container, COUNT(*), SUM(cpu), SUM(mem), SUM(mem_limit),
			MAX(net_rx), MAX(net_tx), MAX(blk_read), MAX(blk_write)
		FROM metric_samples
		WHERE project = ? AND (? = '' OR service = ?) AND ts >= ? AND ts < ?
		GROUP BY service, container, ts - ts % ? ORDER BY service, container, 1`,
		secs, project, service, service, from.Unix(), to.Unix(), secs)
	if err != nil {
		return nil, fmt.Errorf("query metrics: %w", err)
	}
	defer rows.Close()
	var out []ContainerBucket
	for rows.Next() {
		var b ContainerBucket
		var ts, n, mem, limit, rx, tx, br, bw int64
		if err := rows.Scan(&ts, &b.Service, &b.Container, &n, &b.CPUSum, &mem, &limit, &rx, &tx, &br, &bw); err != nil {
			return nil, fmt.Errorf("scan metrics: %w", err)
		}
		b.TS = time.Unix(ts, 0)
		b.Samples, b.MemSum, b.MemLimitSum = uint64(n), uint64(mem), uint64(limit)
		b.NetRx, b.NetTx, b.BlkRead, b.BlkWrite = uint64(rx), uint64(tx), uint64(br), uint64(bw)
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query metrics: %w", err)
	}
	return out, nil
}

// Prune deletes samples with ts < olderThan and returns how many were removed.
func (s *Store) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM metric_samples WHERE ts < ?`, olderThan.Unix())
	if err != nil {
		return 0, fmt.Errorf("prune metrics: %w", err)
	}
	return res.RowsAffected()
}

// TokenHash returns the stored /metrics bearer-token hash, or "" when unset.
func (s *Store) TokenHash(ctx context.Context) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, tokenHashKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read token hash: %w", err)
	}
	return v, nil
}

// SetTokenHash stores the hash; an empty hash clears it.
func (s *Store) SetTokenHash(ctx context.Context, hash string) error {
	var err error
	if hash == "" {
		_, err = s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, tokenHashKey)
	} else {
		_, err = s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, tokenHashKey, hash)
	}
	if err != nil {
		return fmt.Errorf("write token hash: %w", err)
	}
	return nil
}
