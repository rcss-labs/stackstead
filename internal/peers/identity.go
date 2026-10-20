// Package peers holds the identity, paired-server records and connection
// tokens behind Stackstead's connected servers.
package peers

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
)

const (
	settingInstanceID    = "instance_id"
	settingServerName    = "server_name"
	settingAdvertisedURL = "advertised_url"
	maxSlugLength        = 40
)

// Identity is how this server presents itself to peers.
type Identity struct {
	InstanceID    string
	Name          string
	Slug          string
	AdvertisedURL string
}

// Slugify lowercases name and turns every run of characters outside [a-z0-9]
// into a single "-", trimmed and capped at 40 characters. An empty result
// becomes "server".
func Slugify(name string) string {
	var b strings.Builder
	pendingDash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingDash = false
			b.WriteRune(r)
		} else {
			pendingDash = true
		}
	}
	slug := b.String()
	if len(slug) > maxSlugLength {
		slug = strings.TrimRight(slug[:maxSlugLength], "-")
	}
	if slug == "" {
		return "server"
	}
	return slug
}

// Identity returns this server's identity, creating the instance ID on first
// use. The name defaults to fallbackName until SetIdentity changes it.
func (s *Store) Identity(ctx context.Context, fallbackName string) (Identity, error) {
	id, err := s.setting(ctx, settingInstanceID)
	if err != nil {
		return Identity{}, err
	}
	if id == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return Identity{}, err
		}
		candidate := hex.EncodeToString(raw[:])
		// INSERT OR IGNORE keeps the first ID if two callers race.
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?)`, settingInstanceID, candidate); err != nil {
			return Identity{}, err
		}
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?)`, settingServerName, fallbackName); err != nil {
			return Identity{}, err
		}
		if id, err = s.setting(ctx, settingInstanceID); err != nil {
			return Identity{}, err
		}
	}
	name, err := s.setting(ctx, settingServerName)
	if err != nil {
		return Identity{}, err
	}
	if name == "" {
		name = fallbackName
	}
	url, err := s.setting(ctx, settingAdvertisedURL)
	if err != nil {
		return Identity{}, err
	}
	return Identity{InstanceID: id, Name: name, Slug: Slugify(name), AdvertisedURL: url}, nil
}

// SetIdentity stores the display name and advertised URL; the instance ID is
// never changed.
func (s *Store) SetIdentity(ctx context.Context, name, advertisedURL string) (Identity, error) {
	for key, value := range map[string]string{settingServerName: name, settingAdvertisedURL: advertisedURL} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			return Identity{}, err
		}
	}
	return s.Identity(ctx, name)
}

func (s *Store) setting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}
