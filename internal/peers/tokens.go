package peers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const (
	tokenPrefix    = "ssc_"
	tokenLifetime  = 15 * time.Minute
	maxOutstanding = 5
)

// ErrTokenInvalid covers unknown, expired and already-used connection tokens.
var ErrTokenInvalid = errors.New("connection token is invalid or expired")

// HashSecret is the hex SHA-256 of a plaintext secret; only hashes are stored.
func HashSecret(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func randomToken(prefix string) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// CreateToken issues a single-use connection token and returns its plaintext
// once. Beyond five outstanding tokens the oldest is evicted.
func (s *Store) CreateToken(ctx context.Context) (string, time.Time, error) {
	plain, err := randomToken(tokenPrefix)
	if err != nil {
		return "", time.Time{}, err
	}
	now := s.now()
	expires := now.Add(tokenLifetime)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM connection_tokens WHERE expires_at <= ?`, now.Unix()); err != nil {
		return "", time.Time{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO connection_tokens (hash, created_at, expires_at) VALUES (?, ?, ?)`,
		HashSecret(plain), now.UnixNano(), expires.Unix()); err != nil {
		return "", time.Time{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM connection_tokens WHERE hash NOT IN
		(SELECT hash FROM connection_tokens ORDER BY created_at DESC, rowid DESC LIMIT ?)`, maxOutstanding); err != nil {
		return "", time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return "", time.Time{}, err
	}
	return plain, expires, nil
}

// ConsumeToken validates the token and runs consume (the pairing attempt).
// The token is deleted only when consume succeeds, so a failed attempt can be
// retried; consume's error is returned unchanged. Calls are serialized so one
// token can never complete two pairings, and consume may use the store.
func (s *Store) ConsumeToken(ctx context.Context, plain string, consume func() error) error {
	s.consumeMu.Lock()
	defer s.consumeMu.Unlock()
	hash := HashSecret(plain)
	var expires int64
	err := s.db.QueryRowContext(ctx, `SELECT expires_at FROM connection_tokens WHERE hash = ?`, hash).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTokenInvalid
	}
	if err != nil {
		return err
	}
	if s.now().After(time.Unix(expires, 0)) {
		return ErrTokenInvalid
	}
	if err := consume(); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM connection_tokens WHERE hash = ?`, hash)
	return err
}
