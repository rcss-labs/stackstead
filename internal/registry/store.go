package registry

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Store persists registries in SQLite. Non-secret settings go in the config
// column, credentials in the secrets column.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// NewStore returns a Store over db; now supplies timestamps.
func NewStore(db *sql.DB, now func() time.Time) *Store { return &Store{db: db, now: now} }

type configJSON struct {
	HubUsername string  `json:"hubUsername,omitempty"`
	Region      string  `json:"region,omitempty"`
	Mode        ECRMode `json:"mode,omitempty"`
	AccessKeyID string  `json:"accessKeyId,omitempty"`
	Profile     string  `json:"profile,omitempty"`
	AccountID   string  `json:"accountId,omitempty"`
}

type secretsJSON struct {
	HubToken        string `json:"hubToken,omitempty"`
	SecretAccessKey string `json:"secretAccessKey,omitempty"`
	SessionToken    string `json:"sessionToken,omitempty"`
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (s *Store) stamp() time.Time { return time.Unix(s.now().Unix(), 0) }

func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	c := se.Code()
	return c == sqlite3.SQLITE_CONSTRAINT_UNIQUE || c == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}

func encode(r Registry) (config, secrets string, err error) {
	c, err := json.Marshal(configJSON{
		HubUsername: r.Hub.Username, Region: r.ECR.Region, Mode: r.ECR.Mode,
		AccessKeyID: r.ECR.AccessKeyID, Profile: r.ECR.Profile, AccountID: r.ECR.AccountID,
	})
	if err != nil {
		return "", "", err
	}
	sc, err := json.Marshal(secretsJSON{
		HubToken: r.Hub.Token.Reveal(), SecretAccessKey: r.ECR.SecretAccessKey.Reveal(),
		SessionToken: r.ECR.SessionToken.Reveal(),
	})
	if err != nil {
		return "", "", err
	}
	return string(c), string(sc), nil
}

// Create inserts r with a new ID and returns it. ErrNameTaken on a duplicate name.
func (s *Store) Create(ctx context.Context, r Registry) (Registry, error) {
	id, err := newID()
	if err != nil {
		return Registry{}, err
	}
	config, secrets, err := encode(r)
	if err != nil {
		return Registry{}, err
	}
	now := s.stamp()
	_, err = s.db.ExecContext(ctx, `INSERT INTO registries
		(id, name, kind, host, config, secrets, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)`,
		id, r.Name, string(r.Kind), r.Host, config, secrets, now.Unix(), now.Unix())
	if err != nil {
		if isUniqueViolation(err) {
			return Registry{}, ErrNameTaken
		}
		return Registry{}, fmt.Errorf("create registry: %w", err)
	}
	r.ID, r.CreatedAt, r.UpdatedAt = id, now, now
	return r.withFlags(), nil
}

const selectCols = `id, name, kind, host, config, secrets, created_at, updated_at, last_test_at, last_test_ok, last_test_message`

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Registry, error) {
	var (
		r                     Registry
		kind, config, secrets string
		created, updated      int64
		testAt                sql.NullInt64
		testOK                int
	)
	if err := row.Scan(&r.ID, &r.Name, &kind, &r.Host, &config, &secrets, &created, &updated, &testAt, &testOK, &r.LastTestMessage); err != nil {
		return Registry{}, err
	}
	var c configJSON
	var sc secretsJSON
	if err := json.Unmarshal([]byte(config), &c); err != nil {
		return Registry{}, fmt.Errorf("decode registry config: %w", err)
	}
	if err := json.Unmarshal([]byte(secrets), &sc); err != nil {
		return Registry{}, errors.New("decode registry secrets: malformed")
	}
	r.Kind = Kind(kind)
	r.Hub = Hub{Username: c.HubUsername, Token: secretOf(sc.HubToken)}
	r.ECR = ECR{Region: c.Region, Mode: c.Mode, AccessKeyID: c.AccessKeyID, Profile: c.Profile, AccountID: c.AccountID,
		SecretAccessKey: secretOf(sc.SecretAccessKey), SessionToken: secretOf(sc.SessionToken)}
	r.CreatedAt, r.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	if testAt.Valid {
		t := time.Unix(testAt.Int64, 0)
		r.LastTestAt = &t
	}
	r.LastTestOK = testOK != 0
	return r.withFlags(), nil
}

// Get returns one registry including its secrets (internal use only).
func (s *Store) Get(ctx context.Context, id string) (Registry, error) {
	r, err := scan(s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM registries WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Registry{}, ErrNotFound
	}
	return r, err
}

// List returns all registries by name, including secrets (internal use only).
func (s *Store) List(ctx context.Context) ([]Registry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+selectCols+` FROM registries ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list registries: %w", err)
	}
	defer rows.Close()
	var out []Registry
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Update replaces the settings and secrets of r.ID.
func (s *Store) Update(ctx context.Context, r Registry) error {
	config, secrets, err := encode(r)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE registries SET name=?, host=?, config=?, secrets=?, updated_at=? WHERE id=?`,
		r.Name, r.Host, config, secrets, s.stamp().Unix(), r.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrNameTaken
		}
		return fmt.Errorf("update registry: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a registry; its push targets go with it.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM registries WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete registry: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordTest stores the outcome of the latest connection test.
func (s *Store) RecordTest(ctx context.Context, id string, ok bool, message string) error {
	okInt := 0
	if ok {
		okInt = 1
	}
	res, err := s.db.ExecContext(ctx, `UPDATE registries SET last_test_at=?, last_test_ok=?, last_test_message=? WHERE id=?`,
		s.stamp().Unix(), okInt, message, id)
	if err != nil {
		return fmt.Errorf("record registry test: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
