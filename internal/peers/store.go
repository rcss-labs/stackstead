package peers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrPeerNotFound  = errors.New("peer not found")
	ErrAlreadyPaired = errors.New("server is already paired")
)

// Secret is a credential that must not leak into logs or API output; Task 2
// adds the methods that enforce that.
type Secret string

type PeerState string

const (
	StatePending PeerState = "pending"
	StateActive  PeerState = "active"
	StateRemoved PeerState = "removed"
)

// TLSOptions say how to verify a peer's HTTPS certificate.
type TLSOptions struct {
	CAPEM     string
	PinSHA256 string
	Plaintext bool
}

// Peer is a paired (or pairing) remote Stackstead server.
type Peer struct {
	InstanceID    string
	Name          string
	Slug          string
	URL           string
	Version       string
	LastError     string
	TLS           TLSOptions
	OutCredential Secret
	StackCount    int
	State         PeerState
	CreatedAt     time.Time
	LastSeenAt    *time.Time
}

// Store persists identity, peers and connection tokens in the shared database.
type Store struct {
	db        *sql.DB
	now       func() time.Time
	consumeMu sync.Mutex
}

func NewStore(db *sql.DB, now func() time.Time) *Store {
	return &Store{db: db, now: now}
}

const peerColumns = `instance_id, name, slug, url, ca_pem, pin_sha256, plaintext, out_credential,
	version, stack_count, created_at, last_seen_at, last_error, state`

type rowScanner interface{ Scan(dest ...any) error }

func scanPeer(row rowScanner) (Peer, error) {
	var (
		p        Peer
		cred     string
		state    string
		created  int64
		lastSeen sql.NullInt64
	)
	err := row.Scan(&p.InstanceID, &p.Name, &p.Slug, &p.URL, &p.TLS.CAPEM, &p.TLS.PinSHA256, &p.TLS.Plaintext,
		&cred, &p.Version, &p.StackCount, &created, &lastSeen, &p.LastError, &state)
	if err != nil {
		return Peer{}, err
	}
	p.OutCredential = Secret(cred)
	p.State = PeerState(state)
	p.CreatedAt = time.Unix(created, 0)
	if lastSeen.Valid {
		t := time.Unix(lastSeen.Int64, 0)
		p.LastSeenAt = &t
	}
	return p, nil
}

func nullableUnix(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}

// Peers lists all peers in the order they were added.
func (s *Store) Peers(ctx context.Context) ([]Peer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+peerColumns+` FROM peers ORDER BY created_at, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Peer
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Peer(ctx context.Context, id string) (Peer, error) {
	return s.onePeer(ctx, `instance_id = ?`, id)
}

// PeerByInboundHash finds the peer whose inbound credential hashes to hash.
func (s *Store) PeerByInboundHash(ctx context.Context, hash string) (Peer, error) {
	if hash == "" {
		return Peer{}, ErrPeerNotFound
	}
	return s.onePeer(ctx, `in_hash = ?`, hash)
}

func (s *Store) onePeer(ctx context.Context, where string, arg any) (Peer, error) {
	p, err := scanPeer(s.db.QueryRowContext(ctx, `SELECT `+peerColumns+` FROM peers WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return Peer{}, ErrPeerNotFound
	}
	return p, err
}

func (s *Store) CountPeers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM peers`).Scan(&n)
	return n, err
}

// InsertPeer adds a peer, assigning a slug unique among peers: the name's
// slug, then -2, -3, ... A peer with the same instance ID yields ErrAlreadyPaired.
func (s *Store) InsertPeer(ctx context.Context, p Peer, inHash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM peers WHERE instance_id = ?`, p.InstanceID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return ErrAlreadyPaired
	}
	base := Slugify(p.Name)
	slug := base
	for n := 2; ; n++ {
		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM peers WHERE slug = ?`, slug).Scan(&taken); err != nil {
			return err
		}
		if taken == 0 {
			break
		}
		suffix := fmt.Sprintf("-%d", n)
		trimmed := base
		if len(trimmed)+len(suffix) > maxSlugLength {
			trimmed = strings.TrimRight(trimmed[:maxSlugLength-len(suffix)], "-")
		}
		slug = trimmed + suffix
	}
	created := p.CreatedAt
	if created.IsZero() {
		created = s.now()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO peers (instance_id, name, slug, url, ca_pem, pin_sha256, plaintext,
		out_credential, in_hash, version, stack_count, created_at, last_seen_at, last_error, state)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.InstanceID, p.Name, slug, p.URL, p.TLS.CAPEM, p.TLS.PinSHA256, p.TLS.Plaintext, string(p.OutCredential),
		inHash, p.Version, p.StackCount, created.Unix(), nullableUnix(p.LastSeenAt), p.LastError, string(p.State)); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdatePeer loads the peer, applies fn and writes the result back. Slug,
// instance ID and the inbound hash are not changed here.
func (s *Store) UpdatePeer(ctx context.Context, id string, fn func(*Peer) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := scanPeer(tx.QueryRowContext(ctx, `SELECT `+peerColumns+` FROM peers WHERE instance_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPeerNotFound
	}
	if err != nil {
		return err
	}
	if err := fn(&p); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE peers SET name = ?, url = ?, ca_pem = ?, pin_sha256 = ?, plaintext = ?,
		out_credential = ?, version = ?, stack_count = ?, last_seen_at = ?, last_error = ?, state = ?
		WHERE instance_id = ?`,
		p.Name, p.URL, p.TLS.CAPEM, p.TLS.PinSHA256, p.TLS.Plaintext, string(p.OutCredential), p.Version,
		p.StackCount, nullableUnix(p.LastSeenAt), p.LastError, string(p.State), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetInboundHash(ctx context.Context, id, hash string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE peers SET in_hash = ? WHERE instance_id = ?`, hash, id)
	return affectedOrNotFound(res, err)
}

func (s *Store) DeletePeer(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM peers WHERE instance_id = ?`, id)
	return affectedOrNotFound(res, err)
}

func affectedOrNotFound(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrPeerNotFound
	}
	return nil
}
