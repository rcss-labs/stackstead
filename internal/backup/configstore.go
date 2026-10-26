package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rcss-labs/stackstead/internal/secret"
)

// Settings keys owned by the backup feature.
const (
	settingBackupConfig = "backup_config"
	settingS3SecretKey  = "backup_s3_secret_key"
)

// ConfigStore reads and writes the backup configuration, the S3 secret and the
// per-stack selections.
type ConfigStore struct{ db *sql.DB }

// NewConfigStore returns a ConfigStore over db.
func NewConfigStore(db *sql.DB) *ConfigStore { return &ConfigStore{db: db} }

// Load returns the saved configuration with its S3 secret, or the defaults
// when nothing has been saved.
func (s *ConfigStore) Load(ctx context.Context) (Config, error) {
	cfg := defaultConfig()
	raw, err := s.get(ctx, settingBackupConfig)
	if err != nil {
		return Config{}, err
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return Config{}, fmt.Errorf("read setting %s: %w", settingBackupConfig, err)
		}
	}
	key, err := s.get(ctx, settingS3SecretKey)
	if err != nil {
		return Config{}, err
	}
	cfg.S3.SecretKey = secret.Secret(key)
	return cfg, nil
}

// Save stores cfg. An empty cfg.S3.SecretKey keeps the stored secret; use
// ClearS3Secret to remove it. Save does not validate; callers run Validate first.
func (s *ConfigStore) Save(ctx context.Context, cfg Config) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode %s: %w", settingBackupConfig, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("write setting %s: %w", settingBackupConfig, err)
	}
	defer tx.Rollback()
	if err := upsertSetting(ctx, tx, settingBackupConfig, string(raw)); err != nil {
		return err
	}
	if cfg.S3.SecretKey != "" {
		if err := upsertSetting(ctx, tx, settingS3SecretKey, cfg.S3.SecretKey.Reveal()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClearS3Secret removes the stored S3 secret key.
func (s *ConfigStore) ClearS3Secret(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, settingS3SecretKey); err != nil {
		return fmt.Errorf("write setting %s: %w", settingS3SecretKey, err)
	}
	return nil
}

// Selections returns the backup selection of every stack that has one.
func (s *ConfigStore) Selections(ctx context.Context) ([]StackSelection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT stack, volumes, images, stop_during FROM backup_stacks ORDER BY stack`)
	if err != nil {
		return nil, fmt.Errorf("read backup selections: %w", err)
	}
	defer rows.Close()
	var out []StackSelection
	for rows.Next() {
		var sel StackSelection
		if err := rows.Scan(&sel.Stack, &sel.Volumes, &sel.Images, &sel.StopDuring); err != nil {
			return nil, fmt.Errorf("read backup selections: %w", err)
		}
		out = append(out, sel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read backup selections: %w", err)
	}
	return out, nil
}

// SetSelection stores the backup selection for one stack, replacing any
// previous one.
func (s *ConfigStore) SetSelection(ctx context.Context, sel StackSelection) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO backup_stacks(stack, volumes, images, stop_during)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(stack) DO UPDATE SET volumes = excluded.volumes,
			images = excluded.images, stop_during = excluded.stop_during`,
		sel.Stack, sel.Volumes, sel.Images, sel.StopDuring)
	if err != nil {
		return fmt.Errorf("write backup selection for %s: %w", sel.Stack, err)
	}
	return nil
}

func (s *ConfigStore) clearPausedAfterRestore(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, settingPausedAfterRestore); err != nil {
		return fmt.Errorf("clear setting %s: %w", settingPausedAfterRestore, err)
	}
	return nil
}

func (s *ConfigStore) get(ctx context.Context, key string) (string, error) {
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

func upsertSetting(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("write setting %s: %w", key, err)
	}
	return nil
}
