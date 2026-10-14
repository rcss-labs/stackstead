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
)

const (
	SessionTTL        = 7 * 24 * time.Hour
	MinPasswordLength = 12
	maxUsernameLength = 64
)

var (
	ErrAdminExists        = errors.New("admin account already exists")
	ErrInvalidUsername    = errors.New("username must be 1-64 characters with no whitespace")
	ErrWeakPassword       = fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInvalidSession     = errors.New("invalid or expired session")
)

type User struct {
	ID       int64
	Username string
}

type Service struct {
	db  *sql.DB
	now func() time.Time
	// dummyHash is verified against when a username does not exist, so a failed
	// login costs the same time either way and does not reveal valid usernames.
	dummyHash string
}

func NewService(db *sql.DB, now func() time.Time) (*Service, error) {
	dummy, err := hashPassword(rand.Text())
	if err != nil {
		return nil, fmt.Errorf("prepare dummy hash: %w", err)
	}
	return &Service{db: db, now: now, dummyHash: dummy}, nil
}

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
	if username == "" || utf8.RuneCountInString(username) > maxUsernameLength || strings.ContainsFunc(username, unicode.IsSpace) {
		return ErrInvalidUsername
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return ErrWeakPassword
	}
	hash, err := hashPassword(password)
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

// Login verifies credentials and returns a new opaque session token. Only a
// SHA-256 of the token is stored, so a leaked database cannot be replayed.
func (s *Service) Login(ctx context.Context, username, password string) (string, error) {
	var id int64
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT id, password_hash FROM users WHERE username = ?`, username).Scan(&id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		_, _ = verifyPassword(s.dummyHash, password)
		return "", ErrInvalidCredentials
	}
	if err != nil {
		return "", fmt.Errorf("look up user: %w", err)
	}
	ok, err := verifyPassword(hash, password)
	if err != nil {
		return "", fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return "", ErrInvalidCredentials
	}
	token := rand.Text()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		hashToken(token), id, s.now().Add(SessionTTL).Unix()); err != nil {
		return "", fmt.Errorf("create session: %w", err)
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
	return u, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
