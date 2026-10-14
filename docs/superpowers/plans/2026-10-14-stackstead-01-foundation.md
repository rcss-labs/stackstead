# Stackstead Plan 1 — Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A runnable `stackstead` binary that loads config, opens its SQLite database, supports first-run admin setup, login/logout with sessions, and serves an embedded (placeholder) SPA — the base every later plan builds on.

**Architecture:** Single Go binary. `internal/config` loads YAML + env overrides; `internal/store` opens SQLite (pure-Go `modernc.org/sqlite`) and runs append-only migrations; `internal/auth` owns the admin account, argon2id hashing, sessions and login rate limiting; `internal/api` is a stdlib `net/http` router exposing JSON endpoints and serving the SPA from an `fs.FS`; `web` embeds the built SPA; `cmd/stackstead` wires it together with graceful shutdown.

**Tech Stack:** Go 1.26, stdlib `net/http` routing (method patterns), `modernc.org/sqlite`, `golang.org/x/crypto/argon2`, `go.yaml.in/yaml/v3`, stdlib `testing` only (no assertion libraries).

**Spec:** `docs/superpowers/specs/2026-10-14-stackstead-v1-design.md`

### Plan series (this is Plan 1 of 5)

The spec covers several subsystems, so it is split into plans that each produce working, testable software:

1. **Foundation** (this plan) — config, store, auth, API skeleton, embedded SPA placeholder, systemd unit.
2. **composefile** — YAML node-tree read/edit preserving comments/order/unknown keys; golden-file tests.
3. **Stacks & lifecycle** — registry (managed + external), `composecli`, `engine`, validation (ports/IP/allowed roots), lifecycle API + SSE operation output.
4. **Observability** — events-driven status cache, logs streaming, metrics sampler + retention, `/metrics` endpoint.
5. **Web UI** — Svelte 5 + Vite SPA built into `web/dist`.

## Global Constraints

- Module path: `github.com/rcss-labs/stackstead`; `go 1.26` in `go.mod`.
- Must build with `CGO_ENABLED=0` for `linux/amd64` and `linux/arm64` (no CGO dependencies).
- YAML library: `go.yaml.in/yaml/v3` (maintained successor of `gopkg.in/yaml.v3`).
- Config file: `/etc/stackstead/config.yaml`; every key overridable by `STACKSTEAD_<KEY>`.
- Defaults (verbatim from spec): `listen` `127.0.0.1:8420`, `stacks_dir` `/opt/stacks`, `data_dir` `/var/lib/stackstead`, `allowed_roots` `[stacks_dir]`, `metrics_retention` `72h`, `metrics_interval` `10s`, `operation_timeout` `15m`.
- Password hashing: argon2id. Session cookie: HttpOnly, SameSite=Strict, Secure when the request arrived over TLS (directly or via `X-Forwarded-Proto: https`).
- First-run setup allowed only from localhost or with a one-time setup token printed to the service log.
- Login is rate-limited per IP.
- JSON error responses use the shape `{"error": "<message>"}`.
- Tests assert observable behavior; write each test before its implementation and watch it fail.
- Commits use conventional-commit prefixes and explain why.

## Review Focus

1. **First-run setup through a same-host reverse proxy** — the proxy connects from `127.0.0.1`, so a naive loopback check would let anyone on the internet claim the instance. Requests carrying `Forwarded`, `X-Forwarded-For` or `X-Real-IP` must be treated as non-local and require the setup token. (Test: Task 5, `TestSetupThroughLocalProxyRequiresToken`.)
2. **Login with an unknown username** — must return exactly the same status and body as a wrong password, and still pay the hashing cost, so usernames can't be enumerated. (Tests: Task 3 `TestLoginFailuresAreIndistinguishable`, Task 5 `TestLoginFailuresLookIdentical`.)
3. **Two first-run setup requests at the same time** — exactly one admin is created; the other gets "already exists". (Test: Task 3, `TestConcurrentCreateAdminCreatesExactlyOne`.)
4. **Expired session cookie** — rejected with 401 and the session row removed, never silently accepted. (Tests: Task 3 `TestSessionExpires`, Task 5 `TestExpiredSessionIsRejected`.)
5. **Browser refresh on an SPA deep link** (`/stacks/web`) — serves `index.html`; but unknown `/api/...` paths must return a JSON 404, never the HTML page. (Test: Task 5, `TestSPAFallbackAndAPINotFound`.)

Known, accepted limitation for v1: behind a reverse proxy, every client shares the proxy's IP for login rate limiting, so repeated bad logins can temporarily lock out the admin. A `trusted_proxies` setting can fix this later.

---

## File Structure

```
go.mod / go.sum
cmd/stackstead/main.go            wiring, flags, setup token, graceful shutdown
internal/config/config.go         Config type, defaults, Load (file + env), validation
internal/config/config_test.go
internal/store/store.go           Open(path) *sql.DB, migrations via PRAGMA user_version
internal/store/store_test.go
internal/auth/password.go         argon2id hash/verify (unexported)
internal/auth/service.go          Service: HasAdmin, CreateAdmin, Login, Authenticate, Logout
internal/auth/ratelimit.go        LoginLimiter
internal/auth/main_test.go        TestMain lowering argon2 cost for tests
internal/auth/service_test.go
internal/auth/ratelimit_test.go
internal/api/server.go            Options, New(), route table
internal/api/respond.go           writeJSON, writeError, decodeJSON
internal/api/session.go           cookie helpers, requireSession middleware, user context
internal/api/handlers_auth.go     setup/login/logout/me handlers, loopback check
internal/api/spa.go               SPA file server with index.html fallback
internal/api/server_test.go
web/embed.go                      go:embed of web/dist
web/dist/index.html               placeholder until Plan 5
deploy/stackstead.service         systemd unit
deploy/config.example.yaml
```

---

### Task 1: Module scaffold and config loading

**Files:**
- Create: `go.mod`, `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  ```go
  package config
  type Config struct {
      Listen           string        `yaml:"listen"`
      StacksDir        string        `yaml:"stacks_dir"`
      DataDir          string        `yaml:"data_dir"`
      AllowedRoots     []string      `yaml:"allowed_roots"`
      MetricsRetention time.Duration `yaml:"metrics_retention"`
      MetricsInterval  time.Duration `yaml:"metrics_interval"`
      OperationTimeout time.Duration `yaml:"operation_timeout"`
  }
  func Defaults() Config
  func Load(path string, getenv func(string) string) (Config, error) // path "" = no file
  ```

- [ ] **Step 1: Confirm the module is initialised**

The controller's setup commit already created `go.mod` (`module github.com/rcss-labs/stackstead`, `go 1.26`) and added `bin/` to `.gitignore`. Confirm with `head -3 go.mod`; do not re-run `go mod init`.

- [ ] **Step 2: Write the failing tests**

Create `internal/config/config_test.go`:

```go
package config_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/config"
)

func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadWithoutFileUsesDefaults(t *testing.T) {
	cfg, err := config.Load("", env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := config.Config{
		Listen:           "127.0.0.1:8420",
		StacksDir:        "/opt/stacks",
		DataDir:          "/var/lib/stackstead",
		AllowedRoots:     []string{"/opt/stacks"},
		MetricsRetention: 72 * time.Hour,
		MetricsInterval:  10 * time.Second,
		OperationTimeout: 15 * time.Minute,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got  %+v\nwant %+v", cfg, want)
	}
}

func TestLoadFileOverridesOnlyKeysItSets(t *testing.T) {
	path := writeConfig(t, "listen: 0.0.0.0:9000\nmetrics_retention: 24h\n")
	cfg, err := config.Load(path, env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != "0.0.0.0:9000" {
		t.Errorf("Listen = %q, want 0.0.0.0:9000", cfg.Listen)
	}
	if cfg.MetricsRetention != 24*time.Hour {
		t.Errorf("MetricsRetention = %s, want 24h", cfg.MetricsRetention)
	}
	if cfg.StacksDir != "/opt/stacks" {
		t.Errorf("StacksDir = %q, want default /opt/stacks", cfg.StacksDir)
	}
}

func TestLoadEmptyFileUsesDefaults(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, ""), env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != "127.0.0.1:8420" {
		t.Errorf("Listen = %q, want default", cfg.Listen)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	path := writeConfig(t, "listen: 0.0.0.0:9000\n")
	cfg, err := config.Load(path, env(map[string]string{
		"STACKSTEAD_LISTEN":            "127.0.0.1:7000",
		"STACKSTEAD_OPERATION_TIMEOUT": "30m",
		"STACKSTEAD_ALLOWED_ROOTS":     "/srv/a, /srv/b",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != "127.0.0.1:7000" {
		t.Errorf("Listen = %q, want env value", cfg.Listen)
	}
	if cfg.OperationTimeout != 30*time.Minute {
		t.Errorf("OperationTimeout = %s, want 30m", cfg.OperationTimeout)
	}
	if want := []string{"/srv/a", "/srv/b"}; !reflect.DeepEqual(cfg.AllowedRoots, want) {
		t.Errorf("AllowedRoots = %v, want %v", cfg.AllowedRoots, want)
	}
}

func TestAllowedRootsDefaultFollowsStacksDir(t *testing.T) {
	cfg, err := config.Load("", env(map[string]string{"STACKSTEAD_STACKS_DIR": "/srv/stacks"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := []string{"/srv/stacks"}; !reflect.DeepEqual(cfg.AllowedRoots, want) {
		t.Errorf("AllowedRoots = %v, want %v", cfg.AllowedRoots, want)
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		env     map[string]string
		wantErr string
	}{
		{name: "unknown key", file: "lisen: x\n", wantErr: "lisen"},
		{name: "empty listen", file: "listen: \"\"\n", wantErr: "listen"},
		{name: "relative stacks dir", env: map[string]string{"STACKSTEAD_STACKS_DIR": "stacks"}, wantErr: "stacks_dir"},
		{name: "relative allowed root", file: "allowed_roots: [rel]\n", wantErr: "allowed_roots"},
		{name: "zero interval", env: map[string]string{"STACKSTEAD_METRICS_INTERVAL": "0s"}, wantErr: "metrics_interval"},
		{name: "unparseable duration", env: map[string]string{"STACKSTEAD_METRICS_RETENTION": "three days"}, wantErr: "STACKSTEAD_METRICS_RETENTION"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := ""
			if tt.file != "" {
				path = writeConfig(t, tt.file)
			}
			_, err := config.Load(path, env(tt.env))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want error mentioning %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadMissingFileFails(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "missing.yaml"), env(nil))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist", err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/config/`
Expected: FAIL — build error `undefined: config.Load` / `undefined: config.Config`.

- [ ] **Step 4: Implement**

```bash
go get go.yaml.in/yaml/v3
```

Create `internal/config/config.go`:

```go
// Package config loads Stackstead's configuration: built-in defaults, then an
// optional YAML file, then STACKSTEAD_* environment variable overrides.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Listen           string        `yaml:"listen"`
	StacksDir        string        `yaml:"stacks_dir"`
	DataDir          string        `yaml:"data_dir"`
	AllowedRoots     []string      `yaml:"allowed_roots"`
	MetricsRetention time.Duration `yaml:"metrics_retention"`
	MetricsInterval  time.Duration `yaml:"metrics_interval"`
	OperationTimeout time.Duration `yaml:"operation_timeout"`
}

func Defaults() Config {
	return Config{
		Listen:           "127.0.0.1:8420",
		StacksDir:        "/opt/stacks",
		DataDir:          "/var/lib/stackstead",
		MetricsRetention: 72 * time.Hour,
		MetricsInterval:  10 * time.Second,
		OperationTimeout: 15 * time.Minute,
	}
}

// Load builds the configuration. An empty path skips the file; a non-empty
// path that does not exist is an error. AllowedRoots defaults to StacksDir.
func Load(path string, getenv func(string) string) (Config, error) {
	cfg := Defaults()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if err := applyEnv(&cfg, getenv); err != nil {
		return Config{}, err
	}
	if len(cfg.AllowedRoots) == 0 {
		cfg.AllowedRoots = []string{cfg.StacksDir}
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	cfg.StacksDir = filepath.Clean(cfg.StacksDir)
	cfg.DataDir = filepath.Clean(cfg.DataDir)
	for i, root := range cfg.AllowedRoots {
		cfg.AllowedRoots[i] = filepath.Clean(root)
	}
	return cfg, nil
}

func applyEnv(cfg *Config, getenv func(string) string) error {
	for key, dst := range map[string]*string{
		"STACKSTEAD_LISTEN":     &cfg.Listen,
		"STACKSTEAD_STACKS_DIR": &cfg.StacksDir,
		"STACKSTEAD_DATA_DIR":   &cfg.DataDir,
	} {
		if v := getenv(key); v != "" {
			*dst = v
		}
	}
	for key, dst := range map[string]*time.Duration{
		"STACKSTEAD_METRICS_RETENTION": &cfg.MetricsRetention,
		"STACKSTEAD_METRICS_INTERVAL":  &cfg.MetricsInterval,
		"STACKSTEAD_OPERATION_TIMEOUT": &cfg.OperationTimeout,
	} {
		if v := getenv(key); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			*dst = d
		}
	}
	if v := getenv("STACKSTEAD_ALLOWED_ROOTS"); v != "" {
		cfg.AllowedRoots = nil
		for _, root := range strings.Split(v, ",") {
			if root = strings.TrimSpace(root); root != "" {
				cfg.AllowedRoots = append(cfg.AllowedRoots, root)
			}
		}
	}
	return nil
}

func (c Config) validate() error {
	if c.Listen == "" {
		return errors.New("listen must not be empty")
	}
	for name, p := range map[string]string{"stacks_dir": c.StacksDir, "data_dir": c.DataDir} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("%s must be an absolute path, got %q", name, p)
		}
	}
	for _, root := range c.AllowedRoots {
		if !filepath.IsAbs(root) {
			return fmt.Errorf("allowed_roots entries must be absolute paths, got %q", root)
		}
	}
	for name, d := range map[string]time.Duration{
		"metrics_retention": c.MetricsRetention,
		"metrics_interval":  c.MetricsInterval,
		"operation_timeout": c.OperationTimeout,
	} {
		if d <= 0 {
			return fmt.Errorf("%s must be positive, got %s", name, d)
		}
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS for all tests.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add go.mod go.sum .gitignore internal/config
git commit -m "feat: add config loading with file and env overrides

Every later component reads its paths and timings from one validated Config,
so misconfiguration fails at startup instead of mid-operation."
```

---

### Task 2: SQLite store with migrations

> Note (implementation): `sessions.token_hash` is `NOT NULL` (migration 1 edited before first release).

**Files:**
- Create: `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  ```go
  package store
  func Open(path string) (*sql.DB, error) // creates file if missing, applies migrations
  ```
  Schema after migration 1: `users(id INTEGER PK, username TEXT UNIQUE, password_hash TEXT, created_at INTEGER)`, `sessions(token_hash TEXT PK, user_id INTEGER → users ON DELETE CASCADE, expires_at INTEGER)`.
  The returned DB has **one** open connection: never hold a transaction while issuing another query on the same `*sql.DB`, or it will deadlock. Later plans add tables by **appending** to `migrations`.

- [ ] **Step 1: Write the failing tests**

Create `internal/store/store_test.go`:

```go
package store_test

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rcss-labs/stackstead/internal/store"
)

func openTemp(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "stackstead.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func count(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestOpenCreatesSchema(t *testing.T) {
	db := openTemp(t)
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES ('admin', 'x', 0)`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ('t', 1, 0)`); err != nil {
		t.Fatalf("insert session: %v", err)
	}
}

func TestReopenKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stackstead.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES ('admin', 'x', 0)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db, err = store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	if n := count(t, db, "users"); n != 1 {
		t.Errorf("users = %d after reopen, want 1", n)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stackstead.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	_, err = store.Open(path)
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("err = %v, want refusal mentioning a newer schema", err)
	}
}

func TestDeletingUserDeletesSessions(t *testing.T) {
	db := openTemp(t)
	if _, err := db.Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES (1, 'admin', 'x', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ('t', 1, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, "sessions"); n != 0 {
		t.Errorf("sessions = %d after deleting user, want 0 (foreign keys not enforced?)", n)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/store/`
Expected: FAIL — build error `undefined: store.Open`.

- [ ] **Step 3: Implement**

```bash
go get modernc.org/sqlite
```

Create `internal/store/store.go`:

```go
// Package store owns Stackstead's SQLite database and its schema migrations.
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// migrations run in order; migration i brings the schema to version i+1, which
// is recorded in PRAGMA user_version. Append only — never edit a shipped entry.
var migrations = []string{
	`CREATE TABLE users (
		id            INTEGER PRIMARY KEY,
		username      TEXT    NOT NULL UNIQUE,
		password_hash TEXT    NOT NULL,
		created_at    INTEGER NOT NULL
	);
	CREATE TABLE sessions (
		token_hash TEXT    PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at INTEGER NOT NULL
	);`,
}

// Open opens (or creates) the database at path and migrates it to the latest schema.
func Open(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// A single connection serializes writes so our own goroutines never hit
	// SQLITE_BUSY; Stackstead's load is far below what one connection serves.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("record schema version %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", i+1, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/ -v`
Expected: PASS for all four tests.

- [ ] **Step 5: Verify pure-Go cross-compilation still works**

Run: `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./... && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...`
Expected: no output, exit 0.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add go.mod go.sum internal/store
git commit -m "feat: add SQLite store with versioned migrations

Pure-Go SQLite keeps the binary CGO-free for arm64 cross-builds, and
user_version-based migrations let later plans add tables safely."
```

---

### Task 3: Auth service — admin account, password hashing, sessions

**Files:**
- Create: `internal/auth/password.go`, `internal/auth/service.go`
- Test: `internal/auth/main_test.go`, `internal/auth/service_test.go`

**Interfaces:**
- Consumes: `store.Open(path string) (*sql.DB, error)` (tests only); `fakeClock` with `Now()`/`Advance(d)` from `internal/auth/clock_test.go` (Task 4).
- Produces:
  ```go
  package auth
  const SessionTTL = 7 * 24 * time.Hour
  const MinPasswordLength = 12
  var ErrAdminExists, ErrInvalidUsername, ErrWeakPassword, ErrInvalidCredentials, ErrInvalidSession error
  type User struct { ID int64; Username string }
  func NewService(db *sql.DB, now func() time.Time) (*Service, error)
  func (s *Service) HasAdmin(ctx context.Context) (bool, error)
  func (s *Service) CreateAdmin(ctx context.Context, username, password string) error
  func (s *Service) Login(ctx context.Context, username, password string) (token string, err error)
  func (s *Service) Authenticate(ctx context.Context, token string) (User, error)
  func (s *Service) Logout(ctx context.Context, token string) error
  ```

- [ ] **Step 1: Write the failing tests**

Create `internal/auth/main_test.go`:

```go
package auth

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Production argon2 cost (64 MiB per hash) makes the suite slow. Same
	// algorithm, cheaper parameters; hashes still verify via their encoded params.
	hashParams = argonParams{memory: 8 * 1024, iterations: 1, threads: 1, keyLen: 32, saltLen: 16}
	os.Exit(m.Run())
}
```

Create `internal/auth/service_test.go`:

```go
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/store"
)

const goodPassword = "correct horse battery"

// fakeClock is defined in clock_test.go (created by Task 4).

func newTestService(t *testing.T) (*Service, *sql.DB, *fakeClock) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	svc, err := NewService(db, clock.Now)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, db, clock
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateAdminThenLogin(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	has, err := svc.HasAdmin(ctx)
	if err != nil || has {
		t.Fatalf("HasAdmin before setup = %v, %v; want false, nil", has, err)
	}
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if has, _ := svc.HasAdmin(ctx); !has {
		t.Fatal("HasAdmin after setup = false")
	}
	token, err := svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	user, err := svc.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if user.Username != "admin" {
		t.Errorf("Username = %q, want admin", user.Username)
	}
}

func TestCreateAdminOnlyOnce(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	if err := svc.CreateAdmin(ctx, "other", goodPassword); !errors.Is(err, ErrAdminExists) {
		t.Errorf("second CreateAdmin err = %v, want ErrAdminExists", err)
	}
}

func TestConcurrentCreateAdminCreatesExactlyOne(t *testing.T) {
	svc, db, _ := newTestService(t)
	const attempts = 8
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Go(func() {
			errs <- svc.CreateAdmin(context.Background(), fmt.Sprintf("admin%d", i), goodPassword)
		})
	}
	wg.Wait()
	close(errs)

	created := 0
	for err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, ErrAdminExists):
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if created != 1 {
		t.Errorf("created %d admins, want exactly 1", created)
	}
	if n := countRows(t, db, "users"); n != 1 {
		t.Errorf("users table has %d rows, want 1", n)
	}
}

func TestCreateAdminValidatesInput(t *testing.T) {
	tests := []struct {
		name, username, password string
		want                     error
	}{
		{"empty username", "", goodPassword, ErrInvalidUsername},
		{"username with space", "ad min", goodPassword, ErrInvalidUsername},
		{"username too long", strings.Repeat("a", 65), goodPassword, ErrInvalidUsername},
		{"short password", "admin", "short", ErrWeakPassword},
		{"11 chars", "admin", "abcdefghijk", ErrWeakPassword},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _ := newTestService(t)
			if err := svc.CreateAdmin(context.Background(), tt.username, tt.password); !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestStoredPasswordIsArgon2idHash(t *testing.T) {
	svc, db, _ := newTestService(t)
	if err := svc.CreateAdmin(context.Background(), "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRow(`SELECT password_hash FROM users`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "$argon2id$v=19$") || strings.Contains(stored, goodPassword) {
		t.Errorf("stored hash %q is not an argon2id PHC string", stored)
	}
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(ctx, "admin", "wrong password!"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password err = %v, want ErrInvalidCredentials", err)
	}
	if _, err := svc.Login(ctx, "nobody", goodPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown user err = %v, want ErrInvalidCredentials", err)
	}
}

func TestSessionExpires(t *testing.T) {
	svc, db, clock := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	token, err := svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatal(err)
	}

	clock.Advance(SessionTTL - time.Minute)
	if _, err := svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("Authenticate just before expiry: %v", err)
	}

	clock.Advance(time.Minute)
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Errorf("Authenticate at expiry err = %v, want ErrInvalidSession", err)
	}
	if n := countRows(t, db, "sessions"); n != 0 {
		t.Errorf("sessions = %d after expiry, want expired row removed", n)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.CreateAdmin(ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	token, err := svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Errorf("Authenticate after logout err = %v, want ErrInvalidSession", err)
	}
}

func TestAuthenticateRejectsUnknownToken(t *testing.T) {
	svc, _, _ := newTestService(t)
	for _, token := range []string{"", "not-a-real-token"} {
		if _, err := svc.Authenticate(context.Background(), token); !errors.Is(err, ErrInvalidSession) {
			t.Errorf("Authenticate(%q) err = %v, want ErrInvalidSession", token, err)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/auth/`
Expected: FAIL — build errors `undefined: hashParams`, `undefined: argonParams`, `undefined: NewService`.

- [ ] **Step 3: Implement password hashing**

```bash
go get golang.org/x/crypto/argon2
```

Create `internal/auth/password.go`:

```go
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

type argonParams struct {
	memory     uint32 // KiB
	iterations uint32
	threads    uint8
	keyLen     uint32
	saltLen    int
}

// hashParams follow the OWASP argon2id baseline. Verification reads the
// parameters back from each stored hash, so raising them later is safe.
var hashParams = argonParams{memory: 64 * 1024, iterations: 3, threads: 2, keyLen: 32, saltLen: 16}

var errMalformedHash = errors.New("malformed password hash")

// hashPassword returns a PHC-format string: $argon2id$v=19$m=..,t=..,p=..$salt$key
func hashPassword(password string) (string, error) {
	p := hashParams
	salt := make([]byte, p.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.iterations, p.memory, p.threads, p.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.iterations, p.threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errMalformedHash
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false, errMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errMalformedHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, errMalformedHash
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
```

- [ ] **Step 4: Implement the service**

Create `internal/auth/service.go`:

```go
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
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/auth/ -race -v`
Expected: PASS for all tests, no race reports.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go vet ./...
git add go.mod go.sum internal/auth
git commit -m "feat: add admin account and session management

Docker socket access is root-equivalent, so the UI needs real auth: argon2id
passwords, hashed session tokens, atomic single-admin creation, and
indistinguishable login failures to prevent username enumeration."
```

---

### Task 4: Login rate limiter

> Erratum (implementation): Allowed/RecordFailure were replaced by an atomic `Attempt(ip) bool` + `Reset(ip)` after review found concurrent requests bypassed the limit; see internal/auth/ratelimit.go.

**Files:**
- Create: `internal/auth/ratelimit.go`
- Test: `internal/auth/ratelimit_test.go`, `internal/auth/clock_test.go`

**Interfaces:**
- Consumes: nothing. Runs in parallel with Tasks 1–2 and before Task 3; it creates the shared test clock `internal/auth/clock_test.go` that Task 3's tests reuse.
- Produces:
  ```go
  func NewLoginLimiter(maxFailures int, window time.Duration, now func() time.Time) *LoginLimiter
  func (l *LoginLimiter) Allowed(ip string) bool
  func (l *LoginLimiter) RecordFailure(ip string)
  func (l *LoginLimiter) Reset(ip string)
  ```

- [ ] **Step 1: Write the failing tests**

Create `internal/auth/clock_test.go` (shared test clock; Task 3's tests use it too):

```go
package auth

import "time"

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }
```

Create `internal/auth/ratelimit_test.go`:

```go
package auth

import (
	"testing"
	"time"
)

func newTestLimiter() (*LoginLimiter, *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return NewLoginLimiter(3, 15*time.Minute, clock.Now), clock
}

func TestLimiterBlocksAfterMaxFailures(t *testing.T) {
	l, _ := newTestLimiter()
	for i := range 3 {
		if !l.Allowed("10.0.0.1") {
			t.Fatalf("blocked after %d failures, want allowed until 3", i)
		}
		l.RecordFailure("10.0.0.1")
	}
	if l.Allowed("10.0.0.1") {
		t.Error("allowed after 3 failures, want blocked")
	}
}

func TestLimiterUnblocksAfterWindow(t *testing.T) {
	l, clock := newTestLimiter()
	for range 3 {
		l.RecordFailure("10.0.0.1")
	}
	clock.Advance(15 * time.Minute)
	if !l.Allowed("10.0.0.1") {
		t.Error("still blocked after the window elapsed")
	}
}

func TestLimiterResetClearsFailures(t *testing.T) {
	l, _ := newTestLimiter()
	for range 2 {
		l.RecordFailure("10.0.0.1")
	}
	l.Reset("10.0.0.1")
	l.RecordFailure("10.0.0.1")
	l.RecordFailure("10.0.0.1")
	if !l.Allowed("10.0.0.1") {
		t.Error("blocked after reset + 2 failures, want allowed")
	}
}

func TestLimiterTracksIPsIndependently(t *testing.T) {
	l, _ := newTestLimiter()
	for range 3 {
		l.RecordFailure("10.0.0.1")
	}
	if !l.Allowed("10.0.0.2") {
		t.Error("other IP blocked by 10.0.0.1's failures")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/auth/ -run Limiter`
Expected: FAIL — build error `undefined: LoginLimiter` / `undefined: NewLoginLimiter`.

- [ ] **Step 3: Implement**

Create `internal/auth/ratelimit.go`:

```go
package auth

import (
	"sync"
	"time"
)

// LoginLimiter blocks an IP once it reaches maxFailures failed logins within
// window. The window starts at the first failure and is not extended by later ones.
type LoginLimiter struct {
	mu          sync.Mutex
	maxFailures int
	window      time.Duration
	now         func() time.Time
	failures    map[string]*failureWindow
}

type failureWindow struct {
	count int
	start time.Time
}

func NewLoginLimiter(maxFailures int, window time.Duration, now func() time.Time) *LoginLimiter {
	return &LoginLimiter{maxFailures: maxFailures, window: window, now: now, failures: map[string]*failureWindow{}}
}

func (l *LoginLimiter) Allowed(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.failures[ip]
	if !ok {
		return true
	}
	if l.now().Sub(w.start) >= l.window {
		delete(l.failures, ip)
		return true
	}
	return w.count < l.maxFailures
}

func (l *LoginLimiter) RecordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	// Drop expired entries so a stream of distinct IPs cannot grow the map forever.
	for key, w := range l.failures {
		if now.Sub(w.start) >= l.window {
			delete(l.failures, key)
		}
	}
	if w, ok := l.failures[ip]; ok {
		w.count++
		return
	}
	l.failures[ip] = &failureWindow{count: 1, start: now}
}

func (l *LoginLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/auth/ -race -v`
Expected: PASS for all auth tests including the four limiter tests.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/auth/ratelimit.go internal/auth/ratelimit_test.go internal/auth/clock_test.go
git commit -m "feat: add per-IP login rate limiter

Slows password guessing against the admin account; expired entries are
pruned so memory stays bounded."
```

---

### Task 5: HTTP API — setup, login, sessions, SPA serving

**Files:**
- Create: `internal/api/server.go`, `internal/api/respond.go`, `internal/api/session.go`, `internal/api/handlers_auth.go`, `internal/api/spa.go`
- Test: `internal/api/server_test.go`

**Interfaces:**
- Consumes: `auth.Service` (all methods from Task 3), `auth.LoginLimiter` (Task 4), `auth.SessionTTL`, auth error values, `store.Open`.
- Produces:
  ```go
  package api
  type Options struct {
      Auth       *auth.Service
      Limiter    *auth.LoginLimiter
      SetupToken string // "" once an admin exists
      Static     fs.FS  // SPA files; must contain index.html
      Logger     *slog.Logger
  }
  func New(opts Options) http.Handler
  ```
  Routes:
  | Method & path | Auth | Success | Errors |
  |---|---|---|---|
  | `GET /api/setup/status` | none | 200 `{"needsSetup": bool}` | |
  | `POST /api/setup` `{username,password,setupToken}` | loopback or token | 204 | 400 invalid input, 403 not allowed, 409 admin exists, 415 |
  | `POST /api/login` `{username,password}` | none | 204 + `stackstead_session` cookie | 401, 429, 415 |
  | `POST /api/logout` | session | 204, cookie cleared | 401 |
  | `GET /api/me` | session | 200 `{"username": "..."}` | 401 |
  | any other `/api/...` | session | 404 JSON | 401 |
  | everything else | none | static file, or `index.html` fallback | |

  Later plans register new routes in `New` and wrap them with `s.requireSession`; handlers read the user with `userFrom(ctx)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/api/server_test.go`:

```go
package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/store"
)

const adminPassword = "correct horse battery"

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

type testEnv struct {
	handler http.Handler
	auth    *auth.Service
	clock   *fakeClock
}

func newTestEnv(t *testing.T, setupToken string) *testEnv {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	svc, err := auth.NewService(db, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	static := fstest.MapFS{
		"index.html":    {Data: []byte("<html>spa</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	h := api.New(api.Options{
		Auth:       svc,
		Limiter:    auth.NewLoginLimiter(5, 15*time.Minute, clock.Now),
		SetupToken: setupToken,
		Static:     static,
		Logger:     slog.New(slog.DiscardHandler),
	})
	return &testEnv{handler: h, auth: svc, clock: clock}
}

func fromLoopback(r *http.Request) { r.RemoteAddr = "127.0.0.1:50000" }

func withHeader(key, value string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(key, value) }
}

func withCookie(c *http.Cookie) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(c) }
}

// do sends a request; httptest's default RemoteAddr (192.0.2.1) is non-loopback.
func (e *testEnv) do(t *testing.T, method, target, body string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, opt := range opts {
		opt(req)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func (e *testEnv) createAdmin(t *testing.T) {
	t.Helper()
	if err := e.auth.CreateAdmin(context.Background(), "admin", adminPassword); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) login(t *testing.T) *http.Cookie {
	t.Helper()
	rec := e.do(t, http.MethodPost, "/api/login", fmt.Sprintf(`{"username":"admin","password":%q}`, adminPassword))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("login status = %d, body %s", rec.Code, rec.Body)
	}
	return sessionCookie(t, rec)
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == "stackstead_session" {
			return c
		}
	}
	t.Fatal("no stackstead_session cookie in response")
	return nil
}

func setupBody(token string) string {
	return fmt.Sprintf(`{"username":"admin","password":%q,"setupToken":%q}`, adminPassword, token)
}

func TestSetupFromLoopbackCreatesAdmin(t *testing.T) {
	env := newTestEnv(t, "tok-123")
	if rec := env.do(t, http.MethodGet, "/api/setup/status", ""); !strings.Contains(rec.Body.String(), `"needsSetup":true`) {
		t.Fatalf("status before setup = %s", rec.Body)
	}
	if rec := env.do(t, http.MethodPost, "/api/setup", setupBody(""), fromLoopback); rec.Code != http.StatusNoContent {
		t.Fatalf("setup status = %d, body %s", rec.Code, rec.Body)
	}
	if rec := env.do(t, http.MethodGet, "/api/setup/status", ""); !strings.Contains(rec.Body.String(), `"needsSetup":false`) {
		t.Errorf("status after setup = %s", rec.Body)
	}
}

func TestSetupFromRemoteRequiresToken(t *testing.T) {
	env := newTestEnv(t, "tok-123")
	if rec := env.do(t, http.MethodPost, "/api/setup", setupBody("")); rec.Code != http.StatusForbidden {
		t.Errorf("no token: status = %d, want 403", rec.Code)
	}
	if rec := env.do(t, http.MethodPost, "/api/setup", setupBody("wrong")); rec.Code != http.StatusForbidden {
		t.Errorf("wrong token: status = %d, want 403", rec.Code)
	}
	if rec := env.do(t, http.MethodPost, "/api/setup", setupBody("tok-123")); rec.Code != http.StatusNoContent {
		t.Errorf("correct token: status = %d, want 204; body %s", rec.Code, rec.Body)
	}
}

func TestSetupThroughLocalProxyRequiresToken(t *testing.T) {
	env := newTestEnv(t, "tok-123")
	for _, header := range []string{"X-Forwarded-For", "Forwarded", "X-Real-IP"} {
		rec := env.do(t, http.MethodPost, "/api/setup", setupBody(""), fromLoopback, withHeader(header, "203.0.113.9"))
		if rec.Code != http.StatusForbidden {
			t.Errorf("loopback with %s: status = %d, want 403", header, rec.Code)
		}
	}
}

func TestSetupRejectsEmptyTokenWhenServerHasNone(t *testing.T) {
	env := newTestEnv(t, "")
	if rec := env.do(t, http.MethodPost, "/api/setup", setupBody("")); rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestSetupWhenAdminExists(t *testing.T) {
	env := newTestEnv(t, "tok-123")
	env.createAdmin(t)
	if rec := env.do(t, http.MethodPost, "/api/setup", setupBody(""), fromLoopback); rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestSetupRejectsWeakPassword(t *testing.T) {
	env := newTestEnv(t, "")
	rec := env.do(t, http.MethodPost, "/api/setup", `{"username":"admin","password":"short","setupToken":""}`, fromLoopback)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestSetupRequiresJSONContentType(t *testing.T) {
	env := newTestEnv(t, "")
	rec := env.do(t, http.MethodPost, "/api/setup", setupBody(""), fromLoopback, withHeader("Content-Type", "text/plain"))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

func TestLoginSetsSessionCookieAndMeReturnsUser(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	cookie := env.login(t)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure {
		t.Errorf("cookie flags: HttpOnly=%v SameSite=%v Secure=%v; want true, Strict, false over plain HTTP",
			cookie.HttpOnly, cookie.SameSite, cookie.Secure)
	}
	rec := env.do(t, http.MethodGet, "/api/me", "", withCookie(cookie))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"username":"admin"`) {
		t.Errorf("GET /api/me = %d %s", rec.Code, rec.Body)
	}
}

func TestLoginCookieIsSecureBehindHTTPSProxy(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	rec := env.do(t, http.MethodPost, "/api/login",
		fmt.Sprintf(`{"username":"admin","password":%q}`, adminPassword),
		withHeader("X-Forwarded-Proto", "https"))
	if !sessionCookie(t, rec).Secure {
		t.Error("cookie not Secure behind HTTPS proxy")
	}
}

func TestLoginFailuresLookIdentical(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	wrongPass := env.do(t, http.MethodPost, "/api/login", `{"username":"admin","password":"wrong password!"}`)
	unknownUser := env.do(t, http.MethodPost, "/api/login", fmt.Sprintf(`{"username":"nobody","password":%q}`, adminPassword))
	if wrongPass.Code != http.StatusUnauthorized || unknownUser.Code != http.StatusUnauthorized {
		t.Fatalf("statuses = %d, %d; want 401, 401", wrongPass.Code, unknownUser.Code)
	}
	if wrongPass.Body.String() != unknownUser.Body.String() {
		t.Errorf("bodies differ: %q vs %q", wrongPass.Body, unknownUser.Body)
	}
}

func TestLoginIsRateLimited(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	for range 5 {
		env.do(t, http.MethodPost, "/api/login", `{"username":"admin","password":"wrong password!"}`)
	}
	rec := env.do(t, http.MethodPost, "/api/login", fmt.Sprintf(`{"username":"admin","password":%q}`, adminPassword))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status after 5 failures = %d, want 429", rec.Code)
	}
}

func TestProtectedRoutesRequireSession(t *testing.T) {
	env := newTestEnv(t, "")
	for _, target := range []string{"/api/me", "/api/anything"} {
		if rec := env.do(t, http.MethodGet, target, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without session = %d, want 401", target, rec.Code)
		}
	}
	if rec := env.do(t, http.MethodPost, "/api/logout", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /api/logout without session = %d, want 401", rec.Code)
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	cookie := env.login(t)
	env.clock.Advance(auth.SessionTTL + time.Second)
	if rec := env.do(t, http.MethodGet, "/api/me", "", withCookie(cookie)); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/me with expired session = %d, want 401", rec.Code)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	cookie := env.login(t)
	if rec := env.do(t, http.MethodPost, "/api/logout", "", withCookie(cookie)); rec.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d", rec.Code)
	}
	if rec := env.do(t, http.MethodGet, "/api/me", "", withCookie(cookie)); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/me after logout = %d, want 401", rec.Code)
	}
}

func TestSPAFallbackAndAPINotFound(t *testing.T) {
	env := newTestEnv(t, "")
	env.createAdmin(t)
	cookie := env.login(t)

	if rec := env.do(t, http.MethodGet, "/assets/app.js", ""); rec.Body.String() != "console.log(1)" {
		t.Errorf("asset body = %q", rec.Body)
	}
	for _, target := range []string{"/", "/stacks/web", "/assets/"} {
		rec := env.do(t, http.MethodGet, target, "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "spa") {
			t.Errorf("GET %s = %d %q, want index.html", target, rec.Code, rec.Body)
		}
	}

	rec := env.do(t, http.MethodGet, "/api/nope", "", withCookie(cookie))
	var body map[string]string
	if rec.Code != http.StatusNotFound || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body["error"] == "" {
		t.Errorf("GET /api/nope = %d %q, want JSON 404", rec.Code, rec.Body)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/api/`
Expected: FAIL — build error `undefined: api.New` / `undefined: api.Options`.

- [ ] **Step 3: Implement response helpers**

Create `internal/api/respond.go`:

```go
package api

import (
	"encoding/json"
	"mime"
	"net/http"
)

const maxBodyBytes = 1 << 20

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// decodeJSON requires a JSON content type, which also blocks cross-site HTML
// form posts, and rejects unknown fields so client typos fail loudly.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func (s *server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.opts.Logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
```

- [ ] **Step 4: Implement session handling**

Create `internal/api/session.go`:

```go
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/rcss-labs/stackstead/internal/auth"
)

const sessionCookieName = "stackstead_session"

type userKey struct{}

func userFrom(ctx context.Context) auth.User {
	u, _ := ctx.Value(userKey{}).(auth.User)
	return u
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *server) requireSession(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		user, err := s.opts.Auth.Authenticate(r.Context(), cookie.Value)
		if errors.Is(err, auth.ErrInvalidSession) {
			clearSessionCookie(w, r)
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	})
}
```

- [ ] **Step 5: Implement auth handlers**

Create `internal/api/handlers_auth.go`:

```go
package api

import (
	"crypto/subtle"
	"errors"
	"net"
	"net/http"

	"github.com/rcss-labs/stackstead/internal/auth"
)

func (s *server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	has, err := s.opts.Auth.HasAdmin(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needsSetup": !has})
}

type setupRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	SetupToken string `json:"setupToken"`
}

func (s *server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.setupAllowed(r, req.SetupToken) {
		writeError(w, http.StatusForbidden, "setup is only allowed from this machine or with the setup token from the service log")
		return
	}
	err := s.opts.Auth.CreateAdmin(r.Context(), req.Username, req.Password)
	switch {
	case errors.Is(err, auth.ErrAdminExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, auth.ErrInvalidUsername), errors.Is(err, auth.ErrWeakPassword):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		s.internalError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *server) setupAllowed(r *http.Request, token string) bool {
	if s.opts.SetupToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.opts.SetupToken)) == 1 {
		return true
	}
	return isDirectLoopback(r)
}

// isDirectLoopback reports whether the request came from this machine without
// passing through a proxy. A reverse proxy on the same host connects from
// loopback too, so any forwarding header means the real client may be remote.
func isDirectLoopback(r *http.Request) bool {
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Real-Ip"} {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	ip := net.ParseIP(remoteHost(r))
	return ip != nil && ip.IsLoopback()
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ip := remoteHost(r)
	if !s.opts.Limiter.Allowed(ip) {
		writeError(w, http.StatusTooManyRequests, "too many failed login attempts, try again later")
		return
	}
	token, err := s.opts.Auth.Login(r.Context(), req.Username, req.Password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		s.opts.Limiter.RecordFailure(ip)
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.opts.Limiter.Reset(ip)
	setSessionCookie(w, r, token, auth.SessionTTL)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(sessionCookieName) // present: requireSession checked it
	if err := s.opts.Auth.Logout(r.Context(), cookie.Value); err != nil {
		s.internalError(w, r, err)
		return
	}
	clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"username": userFrom(r.Context()).Username})
}
```

- [ ] **Step 6: Implement SPA serving and the router**

Create `internal/api/spa.go`:

```go
package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// spaHandler serves static files and falls back to index.html for any path
// that is not a regular file (client-side routes, directories), so routes
// survive a browser refresh and directory listings are never exposed.
func spaHandler(static fs.FS) http.Handler {
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if info, err := fs.Stat(static, name); err != nil || info.IsDir() {
			http.ServeFileFS(w, r, static, "index.html")
			return
		}
		files.ServeHTTP(w, r)
	})
}
```

Create `internal/api/server.go`:

```go
// Package api is Stackstead's HTTP layer: JSON endpoints under /api and the
// embedded single-page app for everything else.
package api

import (
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/rcss-labs/stackstead/internal/auth"
)

type Options struct {
	Auth       *auth.Service
	Limiter    *auth.LoginLimiter
	SetupToken string // empty once an admin exists
	Static     fs.FS  // SPA build output; must contain index.html
	Logger     *slog.Logger
}

type server struct {
	opts Options
}

func New(opts Options) http.Handler {
	s := &server{opts: opts}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/setup/status", s.handleSetupStatus)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/login", s.handleLogin)

	mux.Handle("POST /api/logout", s.requireSession(s.handleLogout))
	mux.Handle("GET /api/me", s.requireSession(s.handleMe))

	// Unknown API paths get a JSON 404 (after auth, so they don't reveal which
	// routes exist) instead of falling through to the SPA's index.html.
	mux.Handle("/api/", s.requireSession(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	}))
	mux.Handle("/", spaHandler(opts.Static))
	return mux
}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/api/ -race -v`
Expected: PASS for all tests.

- [ ] **Step 8: Commit**

```bash
gofmt -l . && go vet ./...
git add internal/api
git commit -m "feat: add HTTP API for setup, login and SPA serving

First-run setup treats proxied requests as remote so a same-host reverse
proxy cannot be used to claim an unconfigured instance, and unknown /api
paths return JSON 404s instead of the SPA page."
```

---

### Task 6: Binary wiring, embedded SPA placeholder, systemd unit

**Files:**
- Create: `web/embed.go`, `web/dist/index.html`, `cmd/stackstead/main.go`, `deploy/stackstead.service`, `deploy/config.example.yaml`
- Modify: `README.md`

**Interfaces:**
- Consumes: `config.Load`, `store.Open`, `auth.NewService`, `auth.NewLoginLimiter`, `(*auth.Service).HasAdmin`, `api.New`, `api.Options`.
- Produces: `web.Dist() fs.FS` (Plan 5 replaces `web/dist` with the Vite build output); the `stackstead` binary with flag `--config <path>`.

**Testing note:** `main` is thin wiring of already-tested units; an automated test would mostly re-test them. It is verified by the manual smoke run in Step 5 instead, which exercises the real binary end to end (config from env, DB creation, setup, login, SPA, graceful shutdown).

- [ ] **Step 1: Add the embedded SPA placeholder**

Create `web/dist/index.html`:

```html
<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Stackstead</title></head>
<body><p>Stackstead is running. The web UI has not been built yet.</p></body>
</html>
```

Create `web/embed.go`:

```go
// Package web embeds the built single-page app served by Stackstead.
package web

import (
	"embed"
	"io/fs"
)

//go:embed dist
var dist embed.FS

// Dist returns the SPA build output rooted at its index.html.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // "dist" is embedded at compile time; this cannot fail
	}
	return sub
}
```

- [ ] **Step 2: Write `main.go`**

Create `cmd/stackstead/main.go`:

```go
// Command stackstead runs the Stackstead web server.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rcss-labs/stackstead/internal/api"
	"github.com/rcss-labs/stackstead/internal/auth"
	"github.com/rcss-labs/stackstead/internal/config"
	"github.com/rcss-labs/stackstead/internal/store"
	"github.com/rcss-labs/stackstead/web"
)

const shutdownGrace = 10 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(ctx, os.Args[1:], logger); err != nil {
		logger.Error("stackstead exited", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, logger *slog.Logger) error {
	flags := flag.NewFlagSet("stackstead", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to config.yaml (optional; STACKSTEAD_* env vars override it)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	cfg, err := config.Load(*configPath, os.Getenv)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	db, err := store.Open(filepath.Join(cfg.DataDir, "stackstead.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	authSvc, err := auth.NewService(db, time.Now)
	if err != nil {
		return err
	}
	hasAdmin, err := authSvc.HasAdmin(ctx)
	if err != nil {
		return err
	}
	var setupToken string
	if !hasAdmin {
		setupToken = rand.Text()
		logger.Warn("no admin account yet: open the UI from this machine, or enter this setup token",
			"setup_token", setupToken)
	}

	srv := &http.Server{
		Addr: cfg.Listen,
		Handler: api.New(api.Options{
			Auth:       authSvc,
			Limiter:    auth.NewLoginLimiter(5, 15*time.Minute, time.Now),
			SetupToken: setupToken,
			Static:     web.Dist(),
			Logger:     logger,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	logger.Info("stackstead listening", "addr", cfg.Listen)

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
```

- [ ] **Step 3: Add deployment files**

Create `deploy/stackstead.service`:

```ini
[Unit]
Description=Stackstead Docker stack manager
Documentation=https://github.com/rcss-labs/stackstead
After=network-online.target docker.service
Wants=network-online.target docker.service

[Service]
User=stackstead
Group=stackstead
SupplementaryGroups=docker
ExecStart=/usr/local/bin/stackstead --config /etc/stackstead/config.yaml
Restart=on-failure
RestartSec=5
StateDirectory=stackstead
StateDirectoryMode=0700
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full

[Install]
WantedBy=multi-user.target
```

(`Wants=` rather than `Requires=docker.service`: per the spec Stackstead stays up while Docker is down and reconnects, so a Docker restart must not stop it.)

Create `deploy/config.example.yaml`:

```yaml
# Stackstead configuration. Every key can be overridden with an environment
# variable: STACKSTEAD_<KEY in upper case>, e.g. STACKSTEAD_LISTEN.

# Address the web UI listens on. Keep it on localhost and put a reverse proxy
# with TLS in front if you need remote access — UI access is root on this host.
listen: 127.0.0.1:8420

# Where stacks created by Stackstead live: <stacks_dir>/<name>/compose.yaml
stacks_dir: /opt/stacks

# SQLite database and other state.
data_dir: /var/lib/stackstead

# Directories the file browser may open (for picking Dockerfiles).
# Defaults to [stacks_dir]. Env form: comma-separated.
# allowed_roots:
#   - /opt/stacks
#   - /home/me/projects

metrics_retention: 72h
metrics_interval: 10s
operation_timeout: 15m
```

Replace `README.md` with:

````markdown
# stackstead

A lightweight Docker Compose management platform for home labs and small teams:
a single Go binary with a web UI for creating, running and debugging stacks.

Design: `docs/superpowers/specs/2026-10-14-stackstead-v1-design.md`

## Build

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/stackstead ./cmd/stackstead   # or GOARCH=arm64
```

## Install (systemd)

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin stackstead
sudo install -m 0755 bin/stackstead /usr/local/bin/stackstead
sudo install -d /etc/stackstead && sudo install -m 0644 deploy/config.example.yaml /etc/stackstead/config.yaml
sudo install -d -o stackstead -g stackstead /opt/stacks
sudo install -m 0644 deploy/stackstead.service /etc/systemd/system/stackstead.service
sudo systemctl daemon-reload && sudo systemctl enable --now stackstead
```

On first start, open the UI from the same machine, or use the setup token
printed in `journalctl -u stackstead`.

## Develop

```bash
go test ./...
STACKSTEAD_DATA_DIR=$PWD/.dev/data STACKSTEAD_STACKS_DIR=$PWD/.dev/stacks go run ./cmd/stackstead
```
````

Also append `.dev/` to `.gitignore`:

```bash
printf '.dev/\n' >> .gitignore
```

- [ ] **Step 4: Build and run the full suite**

Run: `go build ./... && go vet ./... && test -z "$(gofmt -l .)" && go test -race ./...`
Expected: exit 0; all packages `ok`.

- [ ] **Step 5: Manual smoke test of the real binary**

```bash
go build -o bin/stackstead ./cmd/stackstead
SMOKE=$(mktemp -d)
STACKSTEAD_DATA_DIR=$SMOKE/data STACKSTEAD_STACKS_DIR=$SMOKE/stacks ./bin/stackstead 2>$SMOKE/log &
PID=$!; sleep 1
curl -s localhost:8420/api/setup/status                       # {"needsSetup":true}
grep -c setup_token $SMOKE/log                                  # 1  (token logged; do not paste it anywhere)
curl -s -o /dev/null -w '%{http_code}\n' -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"correct horse battery","setupToken":""}' localhost:8420/api/setup   # 204
curl -s -o /dev/null -w '%{http_code}\n' -c $SMOKE/jar -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"correct horse battery"}' localhost:8420/api/login                   # 204
curl -s -b $SMOKE/jar localhost:8420/api/me                    # {"username":"admin"}
curl -s localhost:8420/stacks/anything | grep -c 'Stackstead is running'   # 1
kill -TERM $PID; wait $PID; echo "exit=$?"                      # exit=0
ls $SMOKE/data                                                  # stackstead.db (+ -wal/-shm)
rm -rf $SMOKE
```

Expected: each command prints the value in its comment.

- [ ] **Step 6: Commit**

```bash
git add web cmd deploy README.md .gitignore
git commit -m "feat: wire stackstead binary with embedded UI and systemd unit

Produces the first runnable build: config, database, first-run setup and
login work end to end, and the systemd unit runs it as an unprivileged
user in the docker group as the spec requires."
```
