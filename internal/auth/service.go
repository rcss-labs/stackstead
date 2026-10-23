// Package auth manages Stackstead's single admin account, its sessions, and
// login rate limiting.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rcss-labs/stackstead/internal/revocation"
)

const (
	SessionTTL        = 7 * 24 * time.Hour
	MinPasswordLength = 12
	maxUsernameLength = 64

	maxConcurrentHashes = 2

	defaultSessionRecheck = 30 * time.Second
)

var (
	ErrAdminExists        = errors.New("admin account already exists")
	ErrInvalidUsername    = errors.New("username must be 1-64 characters with no whitespace")
	ErrWeakPassword       = fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInvalidSession     = errors.New("invalid or expired session")
	ErrSamePassword       = errors.New("new password must differ from the current one")
	ErrUsernameMismatch   = errors.New("an admin account already exists under a different username")
)

type User struct {
	ID       int64
	Username string
	// SessionExpiresAt is when the session that authenticated this user ends;
	// zero for users not authenticated by a session.
	SessionExpiresAt time.Time
}

type Service struct {
	db  *sql.DB
	now func() time.Time
	// dummyHash is verified against when a username does not exist, so a failed
	// login costs the same time either way and does not reveal valid usernames.
	dummyHash string
	// hashSlots bounds concurrent argon2 work: each hash allocates 64 MiB, so
	// the worst case is maxConcurrentHashes x 64 MiB however many logins arrive.
	hashSlots chan struct{}
	params    argonParams
	// recheck is how often an open stream re-validates its session against
	// the database, which catches revocations made by another process (the
	// host-side reset command) and clock-based expiry.
	recheck time.Duration
	// watches holds the contexts of in-flight requests by session, so
	// revoking a session ends its open streams immediately.
	watches revocation.Registry[sessionKey]
}

type sessionKey struct {
	tokenHash string
	userID    int64
}

// Option customizes NewService.
type Option func(*Service)

// WithFastHashingForTests lowers the argon2 cost so tests that hash often run
// quickly. Production code must not use it; hashes stay verifiable because
// each one encodes its own parameters.
func WithFastHashingForTests() Option {
	return func(s *Service) { s.params = fastHashParams }
}

// WithSessionRecheckInterval sets how often an open stream re-validates its
// session (default 30s). Revocations inside this process end streams at once;
// this bounds the delay for ones it cannot see.
func WithSessionRecheckInterval(d time.Duration) Option {
	return func(s *Service) { s.recheck = d }
}

func NewService(db *sql.DB, now func() time.Time, opts ...Option) (*Service, error) {
	s := &Service{db: db, now: now, hashSlots: make(chan struct{}, maxConcurrentHashes), params: hashParams, recheck: defaultSessionRecheck}
	for _, opt := range opts {
		opt(s)
	}
	dummy, err := hashPasswordWith(s.params, rand.Text())
	if err != nil {
		return nil, fmt.Errorf("prepare dummy hash: %w", err)
	}
	s.dummyHash = dummy
	return s, nil
}

// hash and verify run argon2 under the hashSlots limit, waiting for a slot
// only as long as ctx allows. Neither holds a database connection.
func (s *Service) hash(ctx context.Context, password string) (string, error) {
	if err := s.acquireHashSlot(ctx); err != nil {
		return "", err
	}
	defer s.releaseHashSlot()
	return hashPasswordWith(s.params, password)
}

func (s *Service) verify(ctx context.Context, encoded, password string) (bool, error) {
	if err := s.acquireHashSlot(ctx); err != nil {
		return false, err
	}
	defer s.releaseHashSlot()
	return verifyPassword(encoded, password)
}

func (s *Service) acquireHashSlot(ctx context.Context) error {
	select {
	case s.hashSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) releaseHashSlot() { <-s.hashSlots }

func (s *Service) HasAdmin(ctx context.Context) (bool, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("check admin: %w", err)
	}
	return exists, nil
}

// CreateAdmin creates the one admin account. The insert is conditional on the
// users table being empty, so concurrent first-run requests create exactly one.
func (s *Service) CreateAdmin(ctx context.Context, username, password string) error {
	if err := validateNewCredentials(username, password); err != nil {
		return err
	}
	if has, err := s.HasAdmin(ctx); err != nil {
		return err
	} else if has {
		return ErrAdminExists // skip the expensive hash; the INSERT below still guards races
	}
	hash, err := s.hash(ctx, password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, created_at)
		 SELECT ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)`,
		username, hash, s.now().Unix())
	if err != nil {
		return fmt.Errorf("create admin: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("create admin: %w", err)
	}
	if n == 0 {
		return ErrAdminExists
	}
	return nil
}

func validateNewCredentials(username, password string) error {
	if username == "" || utf8.RuneCountInString(username) > maxUsernameLength || strings.ContainsFunc(username, unicode.IsSpace) {
		return ErrInvalidUsername
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return ErrWeakPassword
	}
	return nil
}

// AdminUsername returns the admin's username, or "" when none exists.
func (s *Service) AdminUsername(ctx context.Context) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT username FROM users ORDER BY id LIMIT 1`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("look up admin: %w", err)
	}
	return name, nil
}

// ResetPassword is the host-side recovery path. It creates the admin when none
// exists; otherwise username must match the existing admin, whose password
// hash is replaced and every session deleted (a leaked cookie dies with the
// old password). Policy checks run before the expensive hash.
func (s *Service) ResetPassword(ctx context.Context, username, password string) (created bool, err error) {
	if err := validateNewCredentials(username, password); err != nil {
		return false, err
	}
	existing, err := s.AdminUsername(ctx)
	if err != nil {
		return false, err
	}
	if existing == "" {
		switch err := s.CreateAdmin(ctx, username, password); {
		case err == nil:
			return true, nil
		case errors.Is(err, ErrAdminExists):
			// Lost a race with another creator: fall through to the reset path.
			if existing, err = s.AdminUsername(ctx); err != nil {
				return false, err
			}
		default:
			return false, err
		}
	}
	if existing != username {
		return false, ErrUsernameMismatch
	}
	hash, err := s.hash(ctx, password)
	if err != nil {
		return false, fmt.Errorf("hash password: %w", err)
	}
	// Only the two statements run while the single-connection transaction is open.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin password reset: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE username = ?`, hash, username); err != nil {
		return false, fmt.Errorf("update password: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return false, fmt.Errorf("delete sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit password reset: %w", err)
	}
	s.watches.Revoke(func(sessionKey) bool { return true })
	return false, nil
}

// RevokeAllSessions deletes every session and returns how many there were.
func (s *Service) RevokeAllSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions`)
	if err != nil {
		return 0, fmt.Errorf("delete sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete sessions: %w", err)
	}
	s.watches.Revoke(func(sessionKey) bool { return true })
	return n, nil
}

// Login verifies credentials and returns a new opaque session token. Only a
// SHA-256 of the token is stored, so a leaked database cannot be replayed.
func (s *Service) Login(ctx context.Context, username, password string) (string, error) {
	var id int64
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT id, password_hash FROM users WHERE username = ?`, username).Scan(&id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.verify(ctx, s.dummyHash, password); err != nil && ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrInvalidCredentials
	}
	if err != nil {
		return "", fmt.Errorf("look up user: %w", err)
	}
	ok, err := s.verify(ctx, hash, password)
	if err != nil {
		return "", fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return "", ErrInvalidCredentials
	}
	token := rand.Text()
	// Insert only while the hash we verified is still current: a password
	// change or reset that committed meanwhile has already revoked sessions,
	// and must not be followed by one minted from the old password. Hashes
	// carry random salts, so a replaced hash never compares equal.
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at)
		 SELECT ?, id, ? FROM users WHERE id = ? AND password_hash = ?`,
		hashToken(token), s.now().Add(SessionTTL).Unix(), id, hash)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	} else if n == 0 {
		return "", ErrInvalidCredentials
	}
	return token, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (User, error) {
	if token == "" {
		return User{}, ErrInvalidSession
	}
	var u User
	var expiresAt int64
	err := s.db.QueryRowContext(ctx,
		`SELECT u.id, u.username, s.expires_at
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = ?`, hashToken(token)).Scan(&u.ID, &u.Username, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrInvalidSession
	}
	if err != nil {
		return User{}, fmt.Errorf("look up session: %w", err)
	}
	if s.now().Unix() >= expiresAt {
		if err := s.Logout(ctx, token); err != nil {
			return User{}, err
		}
		return User{}, ErrInvalidSession
	}
	u.SessionExpiresAt = time.Unix(expiresAt, 0)
	return u, nil
}

// WatchSession returns a context derived from parent that ends when the
// session behind token is revoked (logout, password change or reset) or
// expires. Revocations in this process cancel it at once; the database is
// re-checked every recheck interval (and at expiry) to catch the rest, such
// as the host-side reset command. The returned stop function must be called
// when the request ends.
func (s *Service) WatchSession(parent context.Context, token string, user User) (context.Context, context.CancelFunc) {
	ctx, stop := s.watches.Watch(parent, sessionKey{tokenHash: hashToken(token), userID: user.ID})
	go func() {
		timer := time.NewTimer(s.nextRecheck(user.SessionExpiresAt))
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			if _, err := s.Authenticate(ctx, token); errors.Is(err, ErrInvalidSession) {
				stop()
				return
			}
			timer.Reset(s.nextRecheck(user.SessionExpiresAt))
		}
	}()
	return ctx, stop
}

// nextRecheck is the wait until the next look at a session: the recheck
// interval, or less when the session expires sooner.
func (s *Service) nextRecheck(expiresAt time.Time) time.Duration {
	wait := s.recheck
	if !expiresAt.IsZero() {
		if until := expiresAt.Sub(s.now()); until < wait {
			wait = max(until, time.Millisecond)
		}
	}
	return wait
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	tokenHash := hashToken(token)
	s.watches.Revoke(func(k sessionKey) bool { return k.tokenHash == tokenHash })
	return nil
}

// ChangePassword verifies current for userID, stores the argon2id hash of next,
// and deletes every session of the user except keepToken's, so a leaked cookie
// dies with the old password while the browser in use stays signed in.
// Errors: ErrInvalidCredentials (wrong current), ErrWeakPassword, ErrSamePassword.
func (s *Service) ChangePassword(ctx context.Context, userID int64, current, next, keepToken string) error {
	var hash string
	if err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidCredentials
		}
		return fmt.Errorf("look up user: %w", err)
	}
	ok, err := s.verify(ctx, hash, current)
	if err != nil {
		return fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return ErrInvalidCredentials
	}
	if utf8.RuneCountInString(next) < MinPasswordLength {
		return ErrWeakPassword
	}
	if next == current {
		return ErrSamePassword
	}
	newHash, err := s.hash(ctx, next)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	// The store has a single connection: only the two statements run while the
	// transaction is open.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin password change: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	// Compare-and-swap on the hash we verified: if another change landed
	// while we were hashing, this one must not silently overwrite it.
	res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ? AND password_hash = ?`, newHash, userID, hash)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("update password: %w", err)
	} else if n == 0 {
		return ErrInvalidCredentials
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash != ?`, userID, hashToken(keepToken)); err != nil {
		return fmt.Errorf("delete other sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit password change: %w", err)
	}
	keepHash := hashToken(keepToken)
	s.watches.Revoke(func(k sessionKey) bool { return k.userID == userID && k.tokenHash != keepHash })
	return nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
