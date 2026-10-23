package images

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/rcss-labs/stackstead/internal/secret"
)

// Settings keys owned by the image library.
const (
	settingGitHubToken = "github_token"
	settingGitHubLogin = "github_login"
)

// SettingsStore reads and writes image-library settings in the shared
// settings table, keyed by setting name.
type SettingsStore struct{ db *sql.DB }

// NewSettingsStore returns a SettingsStore over db.
func NewSettingsStore(db *sql.DB) *SettingsStore { return &SettingsStore{db: db} }

func (s *SettingsStore) get(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read setting %s: %w", key, err)
	}
	return v, nil
}

// set stores value under key; an empty value removes the setting.
func (s *SettingsStore) set(ctx context.Context, key, value string) error {
	var err error
	if value == "" {
		_, err = s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	} else {
		_, err = s.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	}
	if err != nil {
		return fmt.Errorf("write setting %s: %w", key, err)
	}
	return nil
}

// GitHubToken returns the stored GitHub token, empty when none is set.
func (s *SettingsStore) GitHubToken(ctx context.Context) (secret.Secret, error) {
	v, err := s.get(ctx, settingGitHubToken)
	return secret.Secret(v), err
}

// SetGitHubToken stores the token; an empty token clears it and the stored login.
func (s *SettingsStore) SetGitHubToken(ctx context.Context, t secret.Secret) error {
	if err := s.set(ctx, settingGitHubToken, t.Reveal()); err != nil {
		return err
	}
	if t == "" {
		return s.set(ctx, settingGitHubLogin, "")
	}
	return nil
}

// GitHubLogin returns the login recorded by the last successful token test.
func (s *SettingsStore) GitHubLogin(ctx context.Context) (string, error) {
	return s.get(ctx, settingGitHubLogin)
}

// SetGitHubLogin records (or with "" clears) the login of the tested token.
func (s *SettingsStore) SetGitHubLogin(ctx context.Context, login string) error {
	return s.set(ctx, settingGitHubLogin, login)
}
