// Package backup owns Stackstead's backup configuration, per-stack selections
// and the archive, schedule and restore logic that acts on them.
package backup

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"time"

	"github.com/rcss-labs/stackstead/internal/secret"
)

// Default values applied when a setting has never been saved.
const (
	defaultFrequency = "daily"
	defaultTime      = "03:00"
	defaultKeepLast  = 7
	maxKeepLast      = 1000
)

const (
	frequencyDaily  = "daily"
	frequencyWeekly = "weekly"
)

var timePattern = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// Schedule controls when automatic backups run, in the host's local time zone.
type Schedule struct {
	Enabled   bool         `json:"enabled"`
	Frequency string       `json:"frequency"`
	Weekday   time.Weekday `json:"weekday"`
	Time      string       `json:"time"`
}

// LocalDest is a backup folder on this host.
type LocalDest struct {
	Enabled  bool   `json:"enabled"`
	Path     string `json:"path"`
	KeepLast int    `json:"keepLast"`
}

// S3Dest is an S3-compatible bucket. SecretKey is stored apart from the rest
// of the configuration and is never written into its JSON form.
type S3Dest struct {
	Enabled     bool          `json:"enabled"`
	Bucket      string        `json:"bucket"`
	Prefix      string        `json:"prefix"`
	Region      string        `json:"region"`
	Endpoint    string        `json:"endpoint"`
	PathStyle   bool          `json:"pathStyle"`
	AccessKeyID string        `json:"accessKeyId"`
	SecretKey   secret.Secret `json:"-"`
}

// Config is the backup configuration as the settings screen edits it.
type Config struct {
	Schedule Schedule  `json:"schedule"`
	Local    LocalDest `json:"local"`
	S3       S3Dest    `json:"s3"`
	// Encryption only records whether archives are encrypted. The passphrase
	// lives in the key file (see keyfile.go), never in the settings table:
	// that table is inside every archive and every `db backup` copy.
	Encryption EncryptionSettings `json:"encryption"`
}

// EncryptionSettings is the archive encryption switch.
type EncryptionSettings struct {
	Enabled bool `json:"enabled"`
}

// StackSelection records what a backup of one stack includes.
type StackSelection struct {
	Stack      string
	Volumes    bool
	Images     bool
	StopDuring bool
}

// defaultConfig is the configuration in effect before anything is saved.
func defaultConfig() Config {
	return Config{
		Schedule: Schedule{Frequency: defaultFrequency, Time: defaultTime},
		Local:    LocalDest{KeepLast: defaultKeepLast},
	}
}

// Validate reports every problem with the configuration, joined into one error.
func (c Config) Validate() error {
	var problems []error
	if !timePattern.MatchString(c.Schedule.Time) {
		problems = append(problems, fmt.Errorf("schedule time %q must be HH:MM (24-hour)", c.Schedule.Time))
	}
	if c.Schedule.Frequency != frequencyDaily && c.Schedule.Frequency != frequencyWeekly {
		problems = append(problems, fmt.Errorf("schedule frequency %q must be daily or weekly", c.Schedule.Frequency))
	}
	if c.Schedule.Frequency == frequencyWeekly && (c.Schedule.Weekday < time.Sunday || c.Schedule.Weekday > time.Saturday) {
		problems = append(problems, fmt.Errorf("schedule weekday %d must be between 0 (Sunday) and 6 (Saturday)", c.Schedule.Weekday))
	}
	if c.Local.Enabled && !filepath.IsAbs(c.Local.Path) {
		problems = append(problems, fmt.Errorf("local backup path %q must be absolute", c.Local.Path))
	}
	if c.Local.KeepLast < 1 || c.Local.KeepLast > maxKeepLast {
		problems = append(problems, fmt.Errorf("local keep-last must be between 1 and %d", maxKeepLast))
	}
	if c.S3.Enabled && c.S3.Bucket == "" {
		problems = append(problems, errors.New("s3 bucket is required when S3 is enabled"))
	}
	if c.S3.Endpoint != "" && !isHTTPURL(c.S3.Endpoint) {
		problems = append(problems, fmt.Errorf("s3 endpoint %q must be an http or https URL", c.S3.Endpoint))
	}
	if c.Schedule.Enabled && !c.Local.Enabled && !c.S3.Enabled {
		problems = append(problems, errors.New("a scheduled backup needs at least one enabled destination"))
	}
	return errors.Join(problems...)
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
