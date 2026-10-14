// Package stacks keeps the registry of compose stacks Stackstead manages.
package stacks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Origin says whether Stackstead created a stack or imported an existing one.
type Origin string

const (
	OriginManaged  Origin = "managed"
	OriginExternal Origin = "external"
)

var (
	ErrStackExists   = errors.New("a stack with this name or compose file already exists")
	ErrStackNotFound = errors.New("stack not found")
)

// Entry is one registered stack. Project is the compose project name as
// derived by compose itself; Stackstead never chooses it.
type Entry struct {
	Name        string
	ComposePath string
	Origin      Origin
	Project     string
	CreatedAt   time.Time
}

type Registry struct {
	db  *sql.DB
	now func() time.Time
}

func NewRegistry(db *sql.DB, now func() time.Time) *Registry {
	return &Registry{db: db, now: now}
}

// Add registers e, stamping CreatedAt from the registry clock.
func (r *Registry) Add(ctx context.Context, e Entry) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO stacks (name, compose_path, origin, project, created_at) VALUES (?, ?, ?, ?, ?)`,
		e.Name, e.ComposePath, string(e.Origin), e.Project, r.now().Unix())
	if isUniqueViolation(err) {
		return ErrStackExists
	}
	if err != nil {
		return fmt.Errorf("add stack %q: %w", e.Name, err)
	}
	return nil
}

func (r *Registry) Get(ctx context.Context, name string) (Entry, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT name, compose_path, origin, project, created_at FROM stacks WHERE name = ?`, name)
	e, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrStackNotFound
	}
	if err != nil {
		return Entry{}, fmt.Errorf("get stack %q: %w", name, err)
	}
	return e, nil
}

// List returns all stacks ordered by name.
func (r *Registry) List(ctx context.Context) ([]Entry, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT name, compose_path, origin, project, created_at FROM stacks ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list stacks: %w", err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("list stacks: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list stacks: %w", err)
	}
	return out, nil
}

func (r *Registry) Remove(ctx context.Context, name string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM stacks WHERE name = ?`, name)
	return expectOneRow(res, err, "remove", name)
}

func (r *Registry) SetProject(ctx context.Context, name, project string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE stacks SET project = ? WHERE name = ?`, project, name)
	return expectOneRow(res, err, "set project of", name)
}

func expectOneRow(res sql.Result, err error, verb, name string) error {
	if err != nil {
		return fmt.Errorf("%s stack %q: %w", verb, name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s stack %q: %w", verb, name, err)
	}
	if n == 0 {
		return ErrStackNotFound
	}
	return nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanEntry(s rowScanner) (Entry, error) {
	var e Entry
	var origin string
	var created int64
	if err := s.Scan(&e.Name, &e.ComposePath, &origin, &e.Project, &created); err != nil {
		return Entry{}, err
	}
	e.Origin = Origin(origin)
	e.CreatedAt = time.Unix(created, 0).UTC()
	return e, nil
}

func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	c := se.Code()
	return c == sqlite3.SQLITE_CONSTRAINT_UNIQUE || c == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}
