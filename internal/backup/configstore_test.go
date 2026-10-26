package backup

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/store"
)

func newTestConfigStore(t *testing.T) (*ConfigStore, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewConfigStore(db), db
}

func TestConfigDefaults(t *testing.T) {
	cs, _ := newTestConfigStore(t)
	cfg, err := cs.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Local.KeepLast != 7 {
		t.Errorf("KeepLast = %d, want 7", cfg.Local.KeepLast)
	}
	if cfg.Schedule.Time != "03:00" {
		t.Errorf("Time = %q, want 03:00", cfg.Schedule.Time)
	}
	if cfg.Schedule.Frequency != "daily" {
		t.Errorf("Frequency = %q, want daily", cfg.Schedule.Frequency)
	}
	if cfg.Schedule.Enabled {
		t.Error("Schedule.Enabled = true, want false")
	}
}

func TestConfigRoundTripKeepsSecret(t *testing.T) {
	cs, db := newTestConfigStore(t)
	ctx := context.Background()
	cfg := Config{S3: S3Dest{Enabled: true, Bucket: "b", AccessKeyID: "AKIA", SecretKey: "s1"}}
	if err := cs.Save(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.S3.SecretKey = ""
	if err := cs.Save(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := cs.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.S3.SecretKey.Reveal(); got != "s1" {
		t.Errorf("SecretKey = %q, want s1", got)
	}
	var raw string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'backup_config'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "s1") {
		t.Error("backup_config row contains the secret")
	}
}

func TestConfigValidate(t *testing.T) {
	validConfig := func() Config {
		return Config{
			Schedule: Schedule{Enabled: true, Frequency: "daily", Time: "03:00"},
			Local:    LocalDest{Enabled: true, Path: "/var/backups", KeepLast: 7},
		}
	}
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"valid", func(*Config) {}, false},
		{"time 24:00", func(c *Config) { c.Schedule.Time = "24:00" }, true},
		{"relative local path", func(c *Config) { c.Local.Path = "backups" }, true},
		{"keep last zero", func(c *Config) { c.Local.KeepLast = 0 }, true},
		{"s3 enabled without bucket", func(c *Config) {
			c.Local.Enabled = false
			c.S3 = S3Dest{Enabled: true}
		}, true},
		{"schedule without destination", func(c *Config) { c.Local.Enabled = false }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestSelectionCascadeOnStackRemove(t *testing.T) {
	cs, db := newTestConfigStore(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO stacks (name, compose_path, origin, project, created_at) VALUES ('app', '/app/compose.yaml', 'managed', 'app', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := cs.SetSelection(ctx, StackSelection{Stack: "app", Volumes: true, Images: true, StopDuring: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM stacks WHERE name = 'app'`); err != nil {
		t.Fatal(err)
	}
	sels, err := cs.Selections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sels) != 0 {
		t.Errorf("Selections = %v, want none after stack removal", sels)
	}
}
