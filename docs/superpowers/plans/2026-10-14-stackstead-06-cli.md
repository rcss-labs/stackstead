# Stackstead Plan 6 — CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** this plan specifies *what* to build: files, signatures, behaviour, test cases and acceptance criteria. It deliberately contains no full implementations; implementers write the code test-first. Short snippets show shapes only.

**Goal:** The one static `stackstead` binary becomes a complete command-line tool: a remote client for the HTTP API (stacks, lifecycle actions with streamed output, logs with exact resume, metrics, status, settings), a set of host-side admin commands that work safely next to a running server (admin password, Prometheus token, database backup/restore, config, health), and explicit server lifecycle commands (`serve`, `version`, `config init|validate`) that Plan 7's installers call.

**Architecture:**
- `internal/apitypes` holds the exported JSON wire types. `internal/api` keeps its handlers and converters but its DTOs become type aliases to `apitypes`. `internal/client` is a typed HTTP + SSE client built only on `apitypes` and the stdlib.
- `internal/cli` is the command framework (command tree, interspersed `flag` parsing, `Env` for testable stdio, tables/JSON/colour, exit-code mapping, prompts, the contexts store) plus every command, one file per command group. The server wiring moves from `cmd/stackstead/main.go` into `internal/cli/serve.go`; `main.go` becomes a few lines.
- Local admin commands reuse `internal/store`, `internal/auth`, `internal/metrics` and `internal/config` through a few new, small functions (`store.OpenExisting`, `store.Backup`, `auth.ResetPassword`, `config.Template`), plus a new `internal/lockfile`.
- `internal/version` carries build identity for `version`, the `User-Agent`, the startup log and `/api/status`.

**Tech Stack:** Go 1.26 stdlib, existing modules, plus `golang.org/x/term` (exact pin; the only new module). `CGO_ENABLED=0`.

**Spec:** `docs/superpowers/specs/2026-10-14-stackstead-cli-design.md` (this plan's design) and `docs/superpowers/specs/2026-10-14-stackstead-v1-design.md` §4 configuration, §6 auth/error handling, §7 security.

### Plan series (this is Plan 6; Plan 7 is installers, planned in parallel)

1–4 merged. 5 Web UI — on `feat/ui`; **this plan starts after Plan 5 merges to `main`** and codes against these Plan 5 backend additions: `POST /api/password` (T2c), `GET /api/settings/metrics-token` (T2b), log `id:` cursors and `Last-Event-ID` resume (T2b), `POST …/services/{service}/preview` (T2, not used by the CLI but part of `apitypes`), the `Makefile` and `cmd/stackstead/e2e_test.go` (T1, T23). Plan 7 depends on the **Contract for Plan 7** section at the end of this plan.

## Decisions (controller, recorded for the user)

The global rules ask for TypeSafe-scored design decisions. The TypeSafe skill is an SDK for an external judgment API and cannot be run here without network access and credentials, so these decisions are argued in writing instead. The design spec has the full table; the ones that shape the code:

- **Stdlib `flag` with interspersed parsing, no cobra.** The parser loops: parse, take one positional, parse again, so `stackstead logs web -f` and `stackstead logs -f web` both work. `-h`/`--help` on any command prints its usage. About 200 lines, fully tested, no new packages.
- **`golang.org/x/term` is the single new dependency**, for `ReadPassword`, `IsTerminal` and `GetSize`. Supply chain: Go-team maintained, depends only on `golang.org/x/sys` (already indirect at v0.49.0 via sqlite and crypto), verified through the Go checksum database, pinned exactly in `go.mod`; `go mod verify` is part of acceptance. Rejected: hand-written termios ioctls (two build-tagged files, easy to get subtly wrong on darwin), and reading the password with echo (unacceptable).
- **`internal/apitypes` + aliases** rather than duplicating structs in the client or importing `internal/api` into it. The alias change in `internal/api` is mechanical (`type summaryDTO = apitypes.StackSummary`); converters and handlers do not change, so every existing API test stays green unchanged and proves the wire format did not move.
- **The contexts file** is `$XDG_CONFIG_HOME/stackstead/contexts.json` (default `~/.config/stackstead/`), version 1, written atomically (temp + rename) with dir 0700 and file 0600. The session value is the cookie value, nothing else is derived from it. A 401 from any authenticated call clears that context's session and exits 3 with `not logged in to <name> (<url>): run \`stackstead login <url>\``.
- **Lock file** `<data_dir>/stackstead.lock` via `flock(LOCK_EX|LOCK_NB)`, pid written for messages. `serve` takes it (a second server exits 5 with the holder's pid). `db restore` takes it. Everything else is online-safe by construction (see spec §3) and does not take it.
- **Metrics-token cache revalidation in the server** (≤ 1 DB read per 5 s, failure paths only) makes the local `admin metrics-token` commands online-safe. Chosen over refusing while the server runs because Plan 7 provisioning and Prometheus setup are simpler when the command just works.
- **Root refusal.** `admin *` and `db *` commands refuse (exit 3) when `geteuid() == 0` and the database file's owner uid is not 0, printing `sudo -u <owner> stackstead …`. They never create the database (`store.OpenExisting`); `db init` is the explicit creation path and is expected to run as the service user.
- **Bare `stackstead` prints help (exit 2).** `stackstead --config P` or any argument list whose first element starts with `-` is parsed as `serve` flags and runs the server, printing `note: run "stackstead serve" (bare flags are deprecated)` to stderr once. Plan 7 ships `serve` in the unit; the dev loop in the README changes to `go run ./cmd/stackstead serve`.
- **Lifecycle commands wait** (stream the operation; exit 0/9) unless `--detach`. `stacks rm` asks for confirmation on a TTY unless `--yes`; without a TTY `--yes` is required (exit 2 otherwise). `--volumes` is spelled out in the confirmation text.
- **JSON output = wire types.** `-o json` prints the API body (or the `apitypes` struct the local command builds) with `encoding/json`, two-space indent. Streaming commands with `-o json` print NDJSON: one object per event, `{"event": "log", "id": "…", "data": {…}}` (`id` omitted when the event has none).
- **Colour** only when stdout is a terminal, `NO_COLOR` is unset (any value disables, per no-color.org), `TERM` is not `dumb`, and `--no-color` is absent. Colour is never the only signal: states are also words.
- **Licence line.** The project is AGPL-3.0 (Plan 7 adds `LICENSE`); `stackstead version` prints `License: AGPL-3.0` as its second line and `-o json` includes `"license": "AGPL-3.0"` so a shipped binary states its terms. `--short` stays the bare version string.
- **Metrics rates are totals across replicas** (user decision 2026-10-14, implemented server-side in the history endpoint: gauges averaged, counter rates summed over a service's replicas). The CLI prints what the API returns and labels rate columns `(total)`; it never re-aggregates.
- **Releases are unsigned for now** (checksums only, owned by Plan 7). The CLI neither produces nor verifies signatures.
- **Timeouts.** Unary requests use `--timeout` (default 30 s) as the `http.Client` timeout. Streams use no client timeout; the SSE reader applies a 45 s no-bytes deadline (3× the server's 15 s heartbeat) and reconnects.

## Global Constraints

- One binary, one module. `CGO_ENABLED=0 GOOS=linux GOARCH={amd64,arm64} go build ./...` and `CGO_ENABLED=0 GOOS=darwin GOARCH={amd64,arm64} go build ./...` must pass with and without a built UI (Plan 7 releases all four). `go test ./...` passes without Node and without Docker.
- No new Go module other than `golang.org/x/term`. Nothing under `internal/client` or `internal/apitypes` may import `internal/api`, `internal/stacks`, `internal/composefile`, `internal/engine`, `internal/store`, `internal/auth`, `internal/metrics` or `internal/state` (a test asserts this with `go list -deps`).
- The HTTP contract of every existing route is unchanged, except: `/api/status` gains `version`; `GET /api/health` is new; `/metrics` may re-read the token hash on a failed match (bounded). Existing API tests must pass without edits other than the alias rename.
- CSRF/JSON rules (README, Plan 5 Decisions): JSON content type and a body on every POST, no body on DELETE, cookie sent as `Cookie: stackstead_session=<v>`; the client never follows redirects to another origin (`CheckRedirect` refuses a host change).
- Secrets: the session value, passwords and the plaintext metrics token are never logged; the token is printed exactly once by the command that created it; `-o json` of `login` carries `{"context", "url", "username", "expiresAt"}` and nothing else.
- The store has ONE connection per process; local commands open their own and never hold a transaction while running another query. Migrations stay append-only and are only ever run by `serve` and `db init`.
- **Tests:** test-first; unit tests never need Docker or the network (`httptest` servers, temp databases, fake terminal); real-binary tests go behind `//go:build e2e`; every `go test` gets `-timeout`; fuzzing (if any) uses `-parallel 2` and ≤ 60 s.
- Commits: conventional, explain why, and end with exactly these two trailer lines:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01Th7wAiWALPK7jdrpSxLToT
  ```

## Review Focus

1. **The session cookie must never leak.** Not in stdout, stderr, `-o json`, logs, error messages, or a world-readable file; a 401 clears it locally. (T5 `TestContextsFileIs0600`, T5 `TestSaveIsAtomicAndKeepsMode`, T6 `TestLoginJSONHasNoSession`, T6 `TestUnauthorizedClearsSessionAndExits3`, T10 e2e grep of all captured output for the cookie value)
2. **Stream resume must be exact after a dropped connection.** Operation output shows every line once and ends on `done`; `logs -f` resends every container's cursor and neither repeats nor skips lines sharing a timestamp; a `dropped` event reconnects immediately; a 401 mid-stream stops with exit 3 rather than retrying forever. (T4 `TestStreamReconnectsWithLastEventID`, T4 `TestStreamStopsOn401`, T8 `TestUpWaitsAcrossDisconnectWithoutDuplicateLines`, T9 `TestLogsFollowResumesWithAllCursors`)
3. **Local admin commands next to a running server.** `admin set-password` succeeds while another process holds an open WAL connection and is writing; `db backup` produces a consistent copy under concurrent writes; `db restore` is refused while the lock is held and succeeds afterwards, leaving no stale `-wal`/`-shm`; nothing ever creates or migrates the DB except `serve`/`db init`; the root-over-foreign-owner refusal fires. (T3 `TestOpenExistingNeverCreatesOrMigrates`, T3 `TestBackupUnderConcurrentWrites`, T7 `TestRestoreRefusedWhileLocked`, T7 `TestAdminRefusesRootOverForeignOwner`, T10 e2e)
4. **TLS and transport.** System roots by default; `--ca-file` verifies against that CA only; `--insecure` is remembered and warned about on every use; plain http to a non-loopback host warns at login; redirects to another host are refused. (T4 `TestCAFileVerifies`, T4 `TestInsecureSkipsVerify`, T4 `TestRedirectToOtherHostRefused`, T6 `TestInsecureContextWarnsEveryCommand`)
5. **Exit codes and messages.** Every HTTP status in the table maps to the documented code; 429 says how long to wait; 409 on `stacks up` says the stack is busy; 503 says Docker or compose is unavailable; Ctrl-C exits 130 and cancels the running stream without an error dump. (T2 `TestExitCodeMapping`, T8 `TestUpBusyExits5`, T10 e2e)
6. **Compatibility.** `stackstead --config P` still serves; `go build` on a tree without `web/dist` still works; a tag-less `go build` still reports a version; the moved `main_test.go` tests still pass. (T2 `TestBareFlagsRunServe`, T2 `TestVersionFallsBackToBuildInfo`)

---

## File Structure

```
internal/apitypes/types.go           exported wire structs (no imports beyond time); Actions, OpStates
internal/apitypes/types_test.go      JSON tag golden + no-forbidden-imports test
internal/api/stacks_dto.go           DTOs become aliases to apitypes (converters unchanged)
internal/api/observe.go              log/metric/sample/status/change DTO aliases; token cache revalidation; health
internal/api/handlers_auth.go        request/response aliases; GET /api/health
internal/api/handlers_password.go    request alias
internal/api/server.go               Options.Version; health route registered before the /api/ catch-all
internal/api/stacks.go               handleStatus adds version
internal/api/health_test.go, token_cache_test.go, status_version_test.go
internal/api/client_contract_test.go client.Client against the real router with in-package fakes (T4)
internal/metrics/token.go            NewToken, HashToken (+ test) — shared by the server and the local admin command
internal/version/version.go          Version/Commit/Date vars, Info(), String() (+ test)
internal/lockfile/lockfile.go        Acquire/Release, ErrHeld (+ test; //go:build unix)
internal/store/store.go              OpenExisting, ErrNoDatabase, ErrSchemaMismatch, SchemaVersion, SupportedSchema, QuickCheck, Backup (+ tests)
internal/auth/service.go             ResetPassword, RevokeAllSessions, AdminUsername, ErrUsernameMismatch (+ tests)
internal/config/template.yaml        embedded text/template of the config file
internal/config/template.go          Template(cfg) []byte; Effective(cfg) (ordered key/value for show) (+ tests incl. deploy/config.example.yaml equality)
internal/client/client.go            Client, Config, New, every unary method
internal/client/errors.go            Error, ErrNoSession, status helpers
internal/client/sse.go               SSE reader, Stream with reconnect/resume
internal/client/*_test.go            httptest-based tests, scripted SSE
internal/cli/command.go              Command, Env, Run, Main, help rendering
internal/cli/parse.go                interspersed flag parsing, globalFlags
internal/cli/exit.go                 exit codes, ExitError, codeFor(err)
internal/cli/output.go               Printer, Table, JSON, NDJSON, colour
internal/cli/format.go               bytes, rates, durations, relative time
internal/cli/prompt.go               ReadPassword, ReadSecret(--password-stdin/-file), Confirm, IsTerminal seam
internal/cli/contexts.go             contexts file store
internal/cli/serve.go                the server (moved from cmd/stackstead/main.go) + lock
internal/cli/serve_test.go           moved main_test.go tests + bare-flags compat test
internal/cli/cmd_version.go          version (T2)
internal/cli/local.go                config loading, DB opening with the ownership check, exclusive lock (T7)
internal/cli/cmd_config.go, cmd_db.go, cmd_admin.go, cmd_health.go   (T7)
internal/cli/cmd_login.go            login, logout, context, whoami, status; apiClient helper
internal/cli/cmd_settings.go         settings metrics-token, settings password
internal/cli/cmd_stacks.go           stacks ls/show/up/down/restart/pull/build/rm/import, compose get/edit/put, networks ls
internal/cli/cmd_ops.go              ops ls/show/stream/cancel; shared operation streamer
internal/cli/cmd_logs.go             logs
internal/cli/cmd_metrics.go          metrics, events
internal/cli/commands.go             the root command tree (wiring, T10)
internal/cli/testing_test.go         shared test helpers: newEnv, fake terminal, httptest API stub
cmd/stackstead/main.go               calls cli.Main
cmd/stackstead/cli_e2e_test.go       //go:build e2e — CLI smoke against the real binary (T10)
Makefile                             VERSION/LDFLAGS, build-go target, e2e includes the CLI suite
deploy/stackstead.service            ExecStart … serve --config …
deploy/config.example.yaml           regenerated from the template (byte-identical by test)
README.md                            CLI section, admin section, exit codes, dev loop
go.mod, go.sum                       + golang.org/x/term
```

## Execution waves

Tasks in one wave touch disjoint files and are run by parallel subagents in separate worktrees. Shared names are pinned in each task's Interfaces block; a task must not create a file another task owns. Each wave merges to the plan branch before the next starts.

| Wave | Tasks (parallel) | Depends on |
|---|---|---|
| 1 | T1 wire types + small API additions; T2 CLI framework, version, serve move, Makefile, `x/term`; T3 store/auth/config/lockfile additions | nothing (T1: `internal/apitypes`, `internal/api/*`, `internal/metrics/token.go`. T2: `internal/cli/{command,parse,exit,output,format,serve}.go` + tests, `internal/version`, `cmd/stackstead/main.go`, `cmd/stackstead/main_test.go` (deleted), `Makefile`, `go.mod`/`go.sum`. T3: `internal/store`, `internal/auth`, `internal/config`, `internal/lockfile`, `deploy/config.example.yaml`.) |
| 2 | T4 client package; T5 prompts + contexts store; T7 local admin commands (config, db, admin, health) | T4: T1. T5: T2. T7: T2, T3 (T7 also needs `metrics.NewToken` from T1). |
| 3 | T6 login/logout/context/whoami/status/settings + `apiClient` | T4, T5 |
| 4 | T8 stacks + ops commands; T9 logs, metrics, events commands | T6 (for `apiClient`, `globalFlags`) |
| 5 | T10 wiring (`commands.go`), serve lock + version, e2e, README, deploy unit, Plan 7 contract check | all |

Symbol ownership (never define these elsewhere): T1 owns everything in `apitypes` and `metrics.NewToken/HashToken`; T2 owns `cli.Command`, `cli.Env`, `cli.Run`, `cli.Main`, `cli.ExitError`, `cli.Printer`, `cli.Table`, `cli.globalFlags`, `cli.parseInterspersed`, `version.*`; T3 owns `store.OpenExisting/Backup/QuickCheck/SchemaVersion/SupportedSchema`, `auth.ResetPassword/RevokeAllSessions/AdminUsername`, `config.Template/Effective`, `lockfile.*`; T4 owns `client.*`; T5 owns `cli.ContextStore`, `cli.Contexts`, `cli.Context`, `cli.ReadPassword`, `cli.ReadSecret`, `cli.Confirm`; T6 owns `cli.apiClient`; T8 owns `cli.streamOperation` (used by T9? no — T9 uses only `client.Stream`); T10 owns `cli.commands.go`.

---

### Task 1: Shared wire types, `/api/health`, status version, token cache revalidation (Go, server side)

**Wave:** 1.

**Files:**
- Create: `internal/apitypes/types.go`, `internal/apitypes/types_test.go`.
- Create: `internal/metrics/token.go`, `internal/metrics/token_test.go`.
- Modify: `internal/api/stacks_dto.go`, `internal/api/observe.go`, `internal/api/handlers_auth.go`, `internal/api/handlers_password.go`, `internal/api/server.go`, `internal/api/stacks.go`.
- Create: `internal/api/health_test.go`, `internal/api/token_cache_test.go`, `internal/api/status_version_test.go`.

**Interfaces:**
```go
package apitypes // imports only "time"
type StackSummary struct { Name, Path, Project, Origin, Status string; ServiceCount, Running int }   // json tags exactly as today's summaryDTO
type ContainerInfo struct { ID, Name, Service, State, Status string }
type ServiceConfig struct { Image string; Build *Build; Ports, Volumes []string; Environment []EnvVar; Networks []NetworkAttachment; NetworkMode string; Limits Limits; Restart string; Healthcheck *Healthcheck }
type Build, EnvVar, NetworkAttachment, Limits, Healthcheck … // field-for-field the current buildDTO, envVarDTO, attachmentDTO, limitsDTO, healthcheckDTO
type ServiceDetail struct { Name string; Config ServiceConfig; ReadOnly []string; Containers []ContainerInfo }
type StackDetail struct { Name, Path, Project, Origin, Status string; Running, ServiceCount int; Hash string; Services []ServiceDetail; ParseError string `json:",omitempty"` }
type Operation struct { ID, Stack, Action, State string; StartedAt time.Time; EndedAt *time.Time; ExitCode *int; Error string }
type OperationDetail struct { Operation; Lines []string }
type Network struct { Name, Driver string; Subnets []string }
type FSEntry struct { Name string; IsDir bool; Size int64 }
type Status struct { Docker, Compose, Events string; MetricsDegraded bool; Version string `json:"version"` }
type FieldError struct { Field, Message string }
type ErrorBody struct { Error string; Fields []FieldError `json:"fields,omitempty"` }
type RawCompose struct { Content, Hash string }
type HashResponse struct { Hash string }
type PreviewResponse struct { Content, Hash string }
type CreateStackRequest struct { Name, Service string; Config ServiceConfig }
type ImportRequest struct { Path string }
type UpdateServiceRequest struct { Config ServiceConfig; Hash string }
type LogEvent struct { Service, Container, Stream string; Time time.Time; Text string }
type LogError struct { Service, Container, Error string }
type MetricPoint struct { Time, Service string; CPU float64; Mem, MemLimit uint64; NetRx, NetTx, BlkRead, BlkWrite *float64 /* …BytesPerSec tags */ }
type MetricsHistory struct { Points []MetricPoint }
type Sample struct { Time, Service, Container string; CPU float64; Mem, MemLimit, NetRx, NetTx, BlkRead, BlkWrite uint64 }
type Change struct { Project string; Seq uint64 }
type StatusFlags struct { Events string; MetricsDegraded bool }
type SetupStatus struct { NeedsSetup bool }
type SetupRequest struct { Username, Password, SetupToken string }
type LoginRequest struct { Username, Password string }
type Me struct { Username string }
type ChangePasswordRequest struct { Current string `json:"current"`; New string `json:"new"` }
type MetricsTokenStatus struct { Configured bool }
type MetricsToken struct { Token string }
type Health struct { OK bool `json:"ok"` }
var Actions = []string{"up", "down", "restart", "pull", "build"}
const (OpRunning = "running"; OpSucceeded = "succeeded"; OpFailed = "failed"; OpCancelled = "cancelled"; OpTimedOut = "timed_out")

package metrics
func NewToken() (plain, hashHex string, err error)  // 32 random bytes, base64.RawURLEncoding; hashHex = HashToken(plain)
func HashToken(plain string) string                 // hex(sha256)

package api
// Options gains: Version string  // reported by /api/status; "" means unknown
// GET /api/health  (no session) → 200 {"ok": true}, Cache-Control: no-store
```

**Behaviour:**
- Every JSON tag is copied verbatim from the current DTOs (the existing API tests are the oracle). `internal/api` replaces each DTO declaration with `type xDTO = apitypes.X`; converters (`toSummary`, `toDetail`, `toOperation`, …) and `serviceConfig.toDomain` keep working on the alias. `fieldErrorBody` becomes an alias of `apitypes.ErrorBody` with `stacks.FieldError` converted (or `apitypes.FieldError` given the same tags — pick the conversion; `stacks.FieldError` must not leak into `apitypes`).
- `handleCreateMetricsToken` uses `metrics.NewToken`; `handlePrometheus` uses `metrics.HashToken`. Behaviour unchanged.
- **Token cache revalidation:** `tokenHash` keeps the cache but records `loadedAt`. On the `/metrics` failure paths only — cache empty, or the presented token does not match — if `loadedAt` is older than 5 s, re-read the hash from the store once, update the cache, and compare again before answering. Successful matches never re-read. The token endpoints still write through. A test clock seam (`Options.Now`, defaulting to `time.Now`) is acceptable if it already exists; otherwise inject via an unexported field set by tests.
- `/api/status` adds `"version": Options.Version`.
- `GET /api/health` is registered unconditionally in `New`, before the `/api/` catch-all, without `requireSession`; body `{"ok":true}`; never includes versions, flags or Docker state.

**Tests (write first):**
- `apitypes`: `TestJSONTagsMatchContract` — encode a fully populated fixture of each type and compare the key set against a golden list (`stacks ls` and `stack show` keys from the README and Plan 5 `types.ts`). `TestNoServerImports` — `go list -deps ./internal/apitypes` contains no `internal/` package other than itself (run via `exec` in the test with a 60 s timeout; skip if `go` is absent).
- `metrics`: `TestNewTokenIsURLSafeAndHashes` (43 chars, no padding; `HashToken` is 64 hex, deterministic).
- `api`: all existing tests green without modification except the alias rename. `TestHealthIsPublic` (no cookie → 200 `{"ok":true}`, `no-store`; `/api/healthz` → 401 JSON 404 behaviour unchanged). `TestStatusReportsVersion`. `TestMetricsTokenCacheRevalidates` — create a token through the store directly (not the endpoint) while the cache holds `""`: the first `/metrics` call re-reads and answers 200 with the right token; rotate in the store again within 5 s → still the old answer (bounded); advance the clock 5 s → picks up the new hash. `TestMetricsCacheNotReReadOnSuccess` (store read counter stays flat across 100 good requests).

**Acceptance:** `go build ./... && go vet ./... && test -z "$(gofmt -l .)" && go test -race -timeout 240s ./internal/apitypes/ ./internal/metrics/ ./internal/api/` green.

---

### Task 2: CLI framework, version, server move, Makefile, `x/term`

**Wave:** 1.

**Files:**
- Create: `internal/cli/command.go`, `parse.go`, `exit.go`, `output.go`, `format.go`, `serve.go`, `cmd_version.go` and tests (`command_test.go`, `parse_test.go`, `exit_test.go`, `output_test.go`, `format_test.go`, `serve_test.go`, `cmd_version_test.go`, `testing_test.go`).
- Create: `internal/version/version.go`, `version_test.go`.
- Modify: `cmd/stackstead/main.go` (becomes `func main() { os.Exit(cli.Main(context.Background(), os.Args[1:], cli.OSEnv())) }` plus signal handling inside `Main`); delete `cmd/stackstead/main_test.go` (its tests move to `internal/cli/serve_test.go`).
- Modify: `Makefile`, `go.mod`, `go.sum` (`go get golang.org/x/term@<latest>`; record the version in the commit message).

**Interfaces:**
```go
package cli
type Env struct {
    Stdin io.Reader; Stdout, Stderr io.Writer
    Getenv func(string) string; Now func() time.Time
    IsTerminal func(fd uintptr) bool     // x/term in OSEnv; tests inject
    ConfigDir string                     // "" → $XDG_CONFIG_HOME/stackstead or ~/.config/stackstead
    Args0 string                         // program name for usage
}
func OSEnv() *Env
type Command struct {
    Name, Short, Usage string           // Usage: "stacks up <name> [--detach]"
    Long string                         // optional paragraph
    Flags func(fs *flag.FlagSet)        // command-local flags
    MinArgs, MaxArgs int                // -1 = unbounded
    Run func(ctx context.Context, env *Env, g *globalFlags, args []string) error
    Sub []*Command
    Hidden bool
}
type globalFlags struct { Context string; Output string /* table|json */; NoColor bool; Timeout time.Duration }
func Run(ctx context.Context, root *Command, args []string, env *Env) error   // resolves subcommands, parses, runs
func Main(ctx context.Context, args []string, env *Env) int                    // signal.NotifyContext, Run(rootCommand()), codeFor
func parseInterspersed(fs *flag.FlagSet, args []string) (positional []string, err error)
type ExitError struct { Code int; Err error }                                   // Error() = Err.Error()
func exitf(code int, format string, a ...any) error
func codeFor(err error) int                                                     // ExitError → Code; context.Canceled → 130; flag.ErrHelp → 0; *client.Error → table (T6 adds this case); else 1
const (ExitOK=0; ExitError=1; ExitUsage=2; ExitAuth=3; ExitNotFound=4; ExitConflict=5; ExitValidation=6; ExitUnavailable=7; ExitRateLimited=8; ExitOperationFailed=9; ExitInterrupted=130)
type Printer struct { JSON bool; Color bool; Out io.Writer }
func newPrinter(env *Env, g *globalFlags) *Printer
func (p *Printer) Table(header []string, rows [][]string)      // tabwriter, header upper-case, dimmed when Color
func (p *Printer) JSONValue(v any) error                        // 2-space indent + newline
func (p *Printer) NDJSON(event, id string, data any) error
func (p *Printer) Status(s string) string                       // colours running/succeeded green, partial/running(op) yellow, failed/stopped/unavailable red, unknown dim
func formatBytes(n uint64) string; func formatRate(bps float64) string; func formatDuration(d time.Duration) string; func relativeTime(t, now time.Time) string; func formatPercent(x float64) string

package version
var Version = "dev"; var Commit, Date string   // -ldflags -X
const License = "AGPL-3.0"
type Info struct { Version, Commit, Date, Go, OS, Arch, License string }  // json: version, commit, date, go, os, arch, license
func Get() Info      // ldflags first; else debug.ReadBuildInfo: Main.Version unless "(devel)", vcs.revision (12 chars), vcs.time, "+dirty" suffix when vcs.modified; License is the constant
func (i Info) String() string   // "stackstead v1.2.3 (abc123456789 2026-10-14T10:00:00Z) go1.26.5 linux/amd64\nLicense: AGPL-3.0"
func UserAgent() string         // "stackstead-cli/<version>"
```

**Behaviour:**
- `Run`: walks `Sub` by name; unknown subcommand → `ExitUsage` with the parent's usage on stderr; `help [cmd…]`, `-h`, `--help` print usage to stdout (exit 0). Usage lists subcommands with `Short`, then flags from `Flags` plus the global flags. Global flags are registered on every `FlagSet` so they may appear anywhere; `STACKSTEAD_CONTEXT` seeds `--context`. `MinArgs`/`MaxArgs` violations → `ExitUsage` with the usage line.
- `parseInterspersed`: loop `fs.Parse(rest)`; if `fs.NArg() > 0` and the first remaining arg does not start with `-` (or is exactly `-`), take it as positional and continue parsing the rest; `--` ends flag parsing. Errors from `flag` map to `ExitUsage`; `flag.ErrHelp` to exit 0 after printing usage.
- `Main`: `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`; on `context.Canceled` from a command print nothing extra and return 130. Errors print as `stackstead: <message>` on stderr (no stack traces). If `args` is empty → root usage on stderr, exit 2. If `args[0]` starts with `-` and is not `-h`/`--help` → treat as `serve` with those args, printing the deprecation note once to stderr.
- `serve.go`: the current `run()` body, renamed `serve(ctx, env, args)`, with `buildRunner`, `startBackground`, `shutdownServices` moved verbatim and their tests moved. Logging goes to `env.Stderr`. The startup log line gains `"version", version.Get().Version`. (The lock file and `api.Options.Version` are added in T10, after T1/T3 merge.)
- `cmd_version.go`: prints `version.Get().String()`; `-o json` prints `Info`; `--short` prints only `Info.Version` (Plan 7's installer parses this; `--short` with `-o json` is a usage error).
- Colour decision lives in `newPrinter`: `env.IsTerminal(stdout fd) && env.Getenv("NO_COLOR") == "" && env.Getenv("TERM") != "dumb" && !g.NoColor`. `Stdout` must be an `*os.File` for the fd check; otherwise no colour.
- `Makefile`: `VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)`, `COMMIT`, `DATE` (UTC RFC3339), `LDFLAGS := -s -w -X …/internal/version.Version=$(VERSION) -X …Commit=… -X …Date=…`; `build` and the existing `release` use `$(LDFLAGS)`; new `build-go` (binary without the UI step, for CLI work: `CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/stackstead ./cmd/stackstead`); `e2e` unchanged (the CLI suite shares the tag and directory); `help` lists the new target. Plan 7 owns the `release`/`dist`/packaging targets and the target list (incl. darwin); this task only threads `LDFLAGS` through the existing `release` recipe.

**Tests (write first):**
- `parse_test.go`: table — `[-f web]`, `[web -f]`, `[web --tail 5 -f]`, `[-- -notaflag]`, `[web extra]` (positional count), unknown flag → `ExitUsage` message names the flag; `-h` → `flag.ErrHelp`.
- `command_test.go`: nested resolution (`stacks up web`), `help stacks` output lists `up` with its Short, global flag before and after the subcommand both set `g.Context`, `STACKSTEAD_CONTEXT` seeds it and a flag overrides it, `MinArgs` violation exits 2 and prints the usage line, `TestBareInvocationPrintsHelpExit2`, `TestBareFlagsRunServe` (root receives `--config x` → the serve command's `Run` is invoked with those args; a stub serve is injected via a package variable).
- `exit_test.go`: `TestExitCodeMapping` table over `ExitError`, `context.Canceled`, `flag.ErrHelp`, plain error.
- `output_test.go`: table alignment golden; `NO_COLOR=1` disables colour even on a TTY; `TERM=dumb`; `--no-color`; non-TTY; `NDJSON` line shape with and without id; `JSONValue` indent.
- `format_test.go`: tables mirroring Plan 5's `format.ts` cases (0 B, exact KiB boundaries, `1.5 GiB`, rates, `relativeTime` `—` for zero time).
- `serve_test.go`: the three moved tests unchanged; `TestServeLogsVersion` (a `serve` against a non-existent docker socket and temp dirs, cancelled after the "listening" line, logs the version — bounded by a 20 s context).
- `cmd_version_test.go`: human output (two lines, the second `License: AGPL-3.0`), `-o json` has the seven keys incl. `license`, `--short` prints exactly `<version>\n`, `--short -o json` → exit 2.
- `version_test.go`: `TestLdflagsWin` (set the vars, check `String()`); `TestVersionFallsBackToBuildInfo` (vars at defaults → `Get().Version` is non-empty and `Go` equals `runtime.Version()`).

**Acceptance:** `go build ./... && go vet ./... && test -z "$(gofmt -l .)" && go test -race -timeout 240s ./internal/cli/ ./internal/version/ ./cmd/...` green; `go mod verify` ok; `go mod tidy` is a no-op; `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` and `GOOS=darwin GOARCH=arm64` green; `make build-go && bin/stackstead version` prints a git-describe version; `bin/stackstead --config /nonexistent` fails with the config error (proving the compat path reaches serve) and prints the deprecation note.

---

### Task 3: Store, auth, config and lockfile additions for local admin

**Wave:** 1.

**Files:**
- Modify: `internal/store/store.go`, `internal/store/store_test.go`.
- Modify: `internal/auth/service.go`, `internal/auth/service_test.go`.
- Create: `internal/config/template.yaml`, `internal/config/template.go`, `internal/config/template_test.go`.
- Create: `internal/lockfile/lockfile.go`, `internal/lockfile/lockfile_test.go` (both `//go:build unix`).
- Modify: `deploy/config.example.yaml` only if the template test shows a difference (the template is derived from it; the two must stay byte-identical).

**Interfaces:**
```go
package store
var ErrNoDatabase = errors.New("database does not exist")
type SchemaMismatchError struct { Found, Supported int }   // Error(): "database schema is version N, this binary supports M: start the server (or run stackstead db init) to migrate" / "…newer than this binary"
type OpenOptions struct { ReadOnly bool; AllowOlderSchema bool }
func OpenExisting(path string, opts OpenOptions) (*sql.DB, error)
//   same pragmas/MaxOpenConns(1) as Open; DSN mode=rw (never creates), mode=ro when ReadOnly (never creates -wal/-shm as a foreign user);
//   ErrNoDatabase when the file is missing; SchemaMismatchError when user_version > SupportedSchema(), or != unless AllowOlderSchema
func SupportedSchema() int                          // len(migrations)
func SchemaVersion(ctx context.Context, db *sql.DB) (int, error)
func QuickCheck(ctx context.Context, db *sql.DB) ([]string, error)   // PRAGMA quick_check rows; nil when "ok"
func Backup(ctx context.Context, db *sql.DB, dest string) error      // VACUUM INTO a temp file next to dest (0600), fsync, rename; dest must not exist

package auth
var ErrUsernameMismatch = errors.New("an admin account already exists under a different username")
func (s *Service) AdminUsername(ctx context.Context) (string, error)  // "" when none
// ResetPassword creates the admin when none exists, else (username must match) replaces the hash and deletes every session.
func (s *Service) ResetPassword(ctx context.Context, username, password string) (created bool, err error)
func (s *Service) RevokeAllSessions(ctx context.Context) (int64, error)

package config
func Template(cfg Config) ([]byte, error)             // renders template.yaml with cfg's values (listen, stacks_dir, data_dir, durations, docker_socket); comments preserved
type Setting struct { Key, Value, Source string }     // Source: "default" | "file" | "env"
func Effective(cfg Config, path string, getenv func(string) string) ([]Setting, error)   // ordered as in the template; AllowedRoots joined by ", "

package lockfile
type Lock struct{ /* f *os.File, path */ }
type HeldError struct { Path string; PID int }        // Error(): "<path> is held by pid N (is the server running?)"
func Acquire(path string) (*Lock, error)              // O_CREATE|O_RDWR 0600, flock LOCK_EX|LOCK_NB; on success truncates and writes the pid; EWOULDBLOCK → *HeldError (pid read from the file, 0 if unreadable)
func (l *Lock) Release() error                        // unlock, close; the file stays (never deleted, to avoid the unlink race)
```

**Behaviour:**
- `OpenExisting` never runs `migrate`. The DSN adds `mode=rw` (or `mode=ro`) so SQLite does not create the file; a missing file is detected by `os.Stat` first for a clean error. `ReadOnly` opens with `mode=ro`; a WAL database stays readable while the server writes.
- `Backup`: `VACUUM INTO ?` works on a WAL database with concurrent writers (it runs inside a read transaction). The temp file is `<dest>.tmp-<pid>`; on any error it is removed. The result is a plain (non-WAL-mode-journal) file with the same `user_version`.
- `ResetPassword`: policy checks before hashing (`MinPasswordLength`, username rules via the same validation as `CreateAdmin`); when no admin exists call `CreateAdmin` semantics (conditional insert) and return `created=true`; when one exists with another username return `ErrUsernameMismatch`; otherwise one transaction with `UPDATE users SET password_hash` and `DELETE FROM sessions` (two statements, no queries while open).
- `Template`: the template's values are `{{.Listen}}` etc.; rendering with `Defaults()` reproduces `deploy/config.example.yaml` byte for byte (so move the example's text into the template with placeholders, then regenerate the example and check the diff is only the placeholder substitution).
- `Effective`: loads defaults, the file (if `path != ""`) and env the same way `Load` does, and reports where each key's value came from by comparing the three stages. Durations print as Go durations (`72h0m0s` → prefer `time.Duration.String()` trimmed of zero units: `72h`, `10s`, `15m`).
- `lockfile`: `syscall.Flock`; the lock is per open file description, so a second `Acquire` in the same process also fails (good: tests can use that). `Release` is idempotent.

**Tests (write first):**
- store: `TestOpenExistingNeverCreatesOrMigrates` (missing file → `ErrNoDatabase` and no file created; a v2 DB → `SchemaMismatchError{2, N}` and `user_version` still 2; with `AllowOlderSchema` it opens and `user_version` is still 2; a v(N+1) DB is refused even with `AllowOlderSchema`; a current DB opens and has `MaxOpenConns == 1` by observing that two concurrent transactions serialise); `TestOpenExistingReadOnlyRefusesWrites` (`ReadOnly` → an INSERT fails; reads work while another `Open` connection is mid-transaction); `TestBackupUnderConcurrentWrites` (a goroutine inserts `metric_samples` rows continuously via a second `Open`; `Backup` succeeds; the copy passes `QuickCheck`, has the same `user_version` and a row count between the start and end counts); `TestBackupRefusesExistingDest`; `TestBackupCleansTempOnError` (dest dir unwritable → no `.tmp-` file left).
- auth: `TestResetPasswordCreatesAdminWhenNone`; `TestResetPasswordReplacesHashAndRevokesSessions` (two logins, reset, both tokens → `ErrInvalidSession`, old password fails, new succeeds); `TestResetPasswordUsernameMismatch`; `TestResetPasswordWeakIsCheckedBeforeHashing` (hash counter seam from existing tests stays flat); `TestRevokeAllSessionsCount`.
- config: `TestTemplateWithDefaultsEqualsDeployExample` (reads `../../deploy/config.example.yaml`); `TestTemplateSubstitutesValues` (custom listen/dirs appear; comments intact); `TestTemplateOutputLoads` (`Load` of the rendered file round-trips the values); `TestEffectiveReportsSources` (file sets `listen`, env sets `STACKSTEAD_DATA_DIR` → sources `file`, `env`, others `default`).
- lockfile: `TestAcquireTwiceFails` (second → `*HeldError` with the first's pid); `TestReleaseAllowsReacquire`; `TestHeldErrorPIDFromFile` (pre-written pid, locked by a child `exec` of the test binary using the `TestHelperProcess` pattern, or simply by a second `Acquire` in-process asserting the pid equals `os.Getpid()`).

**Acceptance:** `go test -race -timeout 240s ./internal/store/ ./internal/auth/ ./internal/config/ ./internal/lockfile/` green; `git diff deploy/config.example.yaml` empty or explained in the commit.

---

### Task 4: Typed API client with SSE streaming

**Wave:** 2. Depends on T1.

**Files:** create `internal/client/client.go`, `errors.go`, `sse.go`, `client_test.go`, `sse_test.go`, `tls_test.go`, `deps_test.go`; create `internal/api/client_contract_test.go` (package `api`, uses the fakes already in `internal/api/*_test.go`).

**Interfaces:**
```go
package client
type Config struct {
    BaseURL   *url.URL        // scheme://host[:port][/prefix]; path prefix kept for reverse proxies
    Session   string          // cookie value; "" allowed for login/setup/health only
    CAFile    string          // PEM bundle; replaces system roots when set
    Insecure  bool            // InsecureSkipVerify
    Timeout   time.Duration   // unary requests; 0 = 30s
    UserAgent string
}
type Client struct{ /* http.Client for unary, another without Timeout for streams */ }
func New(cfg Config) (*Client, error)   // loads CAFile now, so a bad file fails early
type Error struct { Status int; Message string; Fields []apitypes.FieldError; RetryAfter time.Duration }  // Error(): message; RetryAfter from the header when 429
var ErrNoSession = errors.New("no session for this context")
func (e *Error) Is(target error) bool   // supports errors.Is(err, ErrUnauthorized) etc. via sentinel vars for 401/403/404/409/429/503
type Partial[T any] struct { Data T; ChecksPartial bool }

// Unary (each: JSON body on POST/PUT, "{}" when no body; DELETE no body; Accept: application/json; Cookie when Session != "")
func (c *Client) Health(ctx) error
func (c *Client) SetupStatus(ctx) (apitypes.SetupStatus, error)
func (c *Client) Setup(ctx, apitypes.SetupRequest) error
func (c *Client) Login(ctx, username, password string) (session string, expires time.Time, err error)  // from Set-Cookie stackstead_session + Max-Age
func (c *Client) Logout(ctx) error
func (c *Client) Me(ctx) (apitypes.Me, error)
func (c *Client) Status(ctx) (apitypes.Status, error)
func (c *Client) Stacks(ctx) ([]apitypes.StackSummary, error)
func (c *Client) Stack(ctx, name string) (apitypes.StackDetail, error)
func (c *Client) Import(ctx, path string) (Partial[apitypes.StackSummary], error)
func (c *Client) Compose(ctx, name string) (apitypes.RawCompose, error)
func (c *Client) SaveCompose(ctx, name, content, hash string) (Partial[apitypes.HashResponse], error)
func (c *Client) Act(ctx, name, action string) (apitypes.Operation, error)
func (c *Client) Remove(ctx, name string, volumes bool) (apitypes.Operation, error)
func (c *Client) StackOperations(ctx, name string) ([]apitypes.Operation, error)
func (c *Client) Operations(ctx, state string) ([]apitypes.Operation, error)
func (c *Client) Operation(ctx, id string) (apitypes.OperationDetail, error)
func (c *Client) Cancel(ctx, id string) (apitypes.Operation, error)
func (c *Client) Networks(ctx) ([]apitypes.Network, error)
func (c *Client) FS(ctx, path string) ([]apitypes.FSEntry, error)
func (c *Client) MetricsHistory(ctx, name string, q MetricsQuery) (apitypes.MetricsHistory, error)   // MetricsQuery{From, To time.Time; Step time.Duration; Service string}; zero values omitted
func (c *Client) MetricsTokenStatus(ctx) (apitypes.MetricsTokenStatus, error)
func (c *Client) CreateMetricsToken(ctx) (apitypes.MetricsToken, error)
func (c *Client) DeleteMetricsToken(ctx) error
func (c *Client) ChangePassword(ctx, current, next string) error

// Streams
type Event struct { Name, ID string; Data json.RawMessage }
type StreamOptions struct {
    LastEventID func() string          // asked on every (re)connect; "" sends no header
    HeartbeatTimeout time.Duration     // 0 → 45s
    Backoff struct{ Min, Max time.Duration }   // 0 → 1s..30s, full jitter
    OnReconnect func(attempt int, err error)   // for a stderr note
}
var ErrStreamDone = errors.New("stream done")      // handler returns it to stop cleanly
// Stream connects to path (query already encoded), parses SSE, calls fn per event. It reconnects on EOF, read timeout or 5xx;
// stops with the error on 401/403/404 (as *Error) or when ctx ends; stops with nil when fn returns ErrStreamDone.
// An "event: dropped" is passed to fn and then triggers an immediate reconnect (no backoff) unless fn stopped.
func (c *Client) Stream(ctx, path string, opts StreamOptions, fn func(Event) error) error
func (c *Client) StreamOperation(ctx, id string, opts StreamOptions, fn func(Event) error) error   // /api/operations/{id}/stream
func (c *Client) StreamLogs(ctx, name string, q LogsQuery, opts StreamOptions, fn func(Event) error) error  // LogsQuery{Follow bool; Tail string; Since, Service string}
func (c *Client) StreamMetricsLive(ctx, name string, opts StreamOptions, fn func(Event) error) error
func (c *Client) StreamEvents(ctx, opts StreamOptions, fn func(Event) error) error
```

**Behaviour:**
- Non-2xx → `*Error` with `Message` from `{"error"}` (fallback `HTTP <status>`), `Fields` from `fields`, `RetryAfter` parsed from `Retry-After` (seconds) when present. Network errors are returned as-is wrapped (`fmt.Errorf("connect to %s: %w", host, err)`); `errors.Is(err, syscall.ECONNREFUSED)` stays true so the CLI maps it to exit 7.
- Methods that need a session return `ErrNoSession` without a request when `Session == ""` (Health, SetupStatus, Setup, Login are exempt).
- `Login`: 204 with `Set-Cookie`; missing cookie → `*Error{Status: 204, Message: "login succeeded but no session cookie was set"}`. Expiry = now + `Max-Age`.
- `CheckRedirect` refuses when the next request's host differs from `BaseURL.Host` (returns an error; the CLI maps it to exit 1 with the message).
- TLS: `CAFile` → `x509.NewCertPool` + `AppendCertsFromPEM` (error if none parsed), set as `RootCAs`; `Insecure` → `InsecureSkipVerify`. Never both silently: `Insecure` wins and the CLI warns.
- SSE parser: lines split on `\n` (strip `\r`), `event:`, `data:` (multi-line joined with `\n`), `id:`, `retry:` (ignored), `:` comments count as liveness, blank line dispatches; a partial last line is buffered across reads. The read deadline is reset on every byte via a `net.Conn`-free approach: read in a goroutine into a channel and apply the timeout with a timer (streams use an `http.Client` without `Timeout`; `Transport.ResponseHeaderTimeout` = unary timeout).
- Reconnect: full-jitter exponential backoff between `Min` and `Max`, reset after a successful connect that delivered at least one event or comment; `LastEventID()` is asked each time. The `Last-Event-ID` header is only sent when non-empty.
- `Stream` ends with `nil` on a clean server close only if `fn` already returned `ErrStreamDone`; otherwise a server EOF is a reconnect (the server ends operation streams after `done`, so `fn` must stop on `done`/`end`; the CLI commands do).

**Tests (write first)** with `httptest` servers:
- Unary: headers and bodies (`Content-Type`, `{}` body on `Act`, no body on `DeleteMetricsToken`, `Cookie`, `User-Agent`); `ErrNoSession`; error body mapping incl. `fields` and `Retry-After`; `Login` cookie + Max-Age → expires; missing cookie error; `TestRedirectToOtherHostRefused` (302 to another `httptest` server); `MetricsHistory` query encoding (RFC3339 UTC, step as seconds).
- TLS: `TestCAFileVerifies` (`httptest.NewTLSServer`; its cert written to a temp PEM → ok; a different PEM → `x509` error); `TestInsecureSkipsVerify`; `TestSystemRootsByDefault` (the TLS test server without CAFile fails verification).
- SSE: parser table (multi-line data, comments, `\r\n`, split across reads at every byte boundary of a 3-event fixture); `TestStreamReconnectsWithLastEventID` (the server sends ids 0–2 then closes; the second request must carry `Last-Event-ID: 2`; then `done`); `TestStreamStopsOn401` and 404 (no reconnect, `*Error`); `TestStreamHeartbeatTimeout` (a server that sends nothing for longer than a 200 ms test timeout → reconnect counted); `TestDroppedReconnectsImmediately` (no backoff sleep observed via an injected sleeper); `TestStreamCtxCancelReturnsCanceled` within 100 ms while the server is mid-stream; goroutine count returns to baseline.
- `deps_test.go`: `go list -deps ./internal/client` has no forbidden `internal/` packages (same approach as T1).
- `internal/api/client_contract_test.go`: start `api.New` with the package's fake stacks/ops/observe and a real `auth.Service` on a temp DB; `Login` → `Stacks` → `Stack` → `Act` → `StreamOperation` to `done` → `Operations("running")` → `MetricsTokenStatus`/`CreateMetricsToken`/`DeleteMetricsToken` → `ChangePassword` (wrong current → `*Error{403}`) → `Logout` → `Me` is 401. This is the one place the client meets the real router.

**Acceptance:** `go test -race -timeout 240s ./internal/client/ ./internal/api/` green; the contract test is the oracle that `apitypes` decodes real responses.

---

### Task 5: Prompts and the contexts store

**Wave:** 2. Depends on T2.

**Files:** create `internal/cli/prompt.go`, `prompt_test.go`, `internal/cli/contexts.go`, `contexts_test.go`.

**Interfaces:**
```go
package cli
// Secrets: exactly one of the sources; never a flag value.
type secretSource struct { Stdin bool; File string }
func readSecret(env *Env, src secretSource, prompt string, confirm bool) (string, error)
//   Stdin: read one line from env.Stdin, strip one trailing "\r\n"/"\n", reject empty.
//   File: read the file, same trimming. Refuse a file with group/other permission bits? No: just read (the user owns it); but reject a directory.
//   Neither: if env.IsTerminal(stdin) → term.ReadPassword with the prompt on Stderr, and when confirm, a second prompt that must match (ExitValidation "passwords do not match"); else ExitUsage "stdin is not a terminal: use --password-stdin or --password-file".
func confirm(env *Env, question string, yes bool) (bool, error)   // yes → true; TTY → "<question> [y/N] " on Stderr, reads a line; non-TTY → ExitUsage "use --yes"

type Context struct { URL, Username, Session string; ExpiresAt time.Time; CAFile string; Insecure bool }   // json: url, username, session, expiresAt, caFile, insecure
type Contexts struct { Version int; Current string; Contexts map[string]Context }
type ContextStore struct { Path string }
func newContextStore(env *Env) *ContextStore   // env.ConfigDir, else $XDG_CONFIG_HOME/stackstead, else $HOME/.config/stackstead; file contexts.json
func (s *ContextStore) Load() (Contexts, error)   // missing file → empty Contexts{Version: 1}; unknown Version → ExitError(1)
func (s *ContextStore) Save(c Contexts) error     // MkdirAll 0700; temp file 0600 in the same dir; rename; chmod 0600 enforced
func (s *ContextStore) Update(fn func(*Contexts) error) error   // Load, fn, Save
func (s *ContextStore) Warnings(env *Env) []string  // "contexts file <path> is readable by others (mode 0644); run chmod 600" when group/other bits are set
func resolveContext(c Contexts, name string) (string, Context, error)  // name or Current; "" and no Current → ExitAuth "no context: run stackstead login <url>"; unknown → ExitNotFound
```

**Behaviour:**
- `term.ReadPassword` is only called when `env.IsTerminal(stdin)`; the prompt goes to stderr so `-o json` stdout stays clean; a newline is written after the hidden input.
- Context names: `[a-z0-9][a-z0-9-]*`, ≤ 32 chars (ExitUsage otherwise). The default name for `login` is the URL's hostname with dots replaced by `-`.
- `Save` writes `{"version":1,"current":…,"contexts":{…}}` with stable key order (`encoding/json` sorts map keys) and a trailing newline; the file never contains anything but these fields.
- `Load` tolerates extra fields (forward compatibility) and treats a corrupt file as an error naming the path (exit 1), never silently overwriting it.

**Tests (write first)** with `env.ConfigDir = t.TempDir()` and an `IsTerminal` stub:
- `readSecret`: stdin source trims one newline and rejects empty; file source; non-TTY without a source → exit 2 message; TTY path uses an injected `readPassword` seam (package var) and the confirm mismatch → exit 6.
- `confirm`: `yes`, TTY `y`/`Y`/`n`/empty, non-TTY → exit 2.
- `TestContextsFileIs0600` and the directory 0700; `TestSaveIsAtomicAndKeepsMode` (a pre-existing 0600 file replaced; no temp file left; a failing `fn` leaves the old content); `TestLoadMissingIsEmpty`; `TestLoadCorruptErrorsNamesPath`; `TestWarningsOnLoosePerms`; `TestResolveContextDefaultsAndErrors`; `TestXDGConfigHomeHonoured`.

**Acceptance:** `go test -race -timeout 120s ./internal/cli/` green.

---

### Task 6: `login`, `logout`, `context`, `whoami`, `status`, `settings`, and the `apiClient` helper

**Wave:** 3. Depends on T4, T5.

**Files:** create `internal/cli/cmd_login.go`, `cmd_login_test.go`, `internal/cli/cmd_settings.go`, `cmd_settings_test.go`; modify `internal/cli/exit.go` (`codeFor` learns `*client.Error`: 401/403 → 3, 404 → 4, 409 → 5, 400-with-fields/422 → 6, 429 → 8, 503 → 7, other 4xx → 1, 5xx → 1; `syscall.ECONNREFUSED`/DNS errors → 7; `client.ErrNoSession` → 3) and `exit_test.go`.

**Interfaces:**
```go
func apiClient(env *Env, g *globalFlags) (c *client.Client, name string, cx Context, err error)
//   loads contexts, resolves g.Context, builds client.Config{BaseURL, Session, CAFile, Insecure, Timeout: g.Timeout, UserAgent: version.UserAgent()};
//   prints store.Warnings and, when cx.Insecure, "warning: TLS verification is disabled for context <name>" to Stderr;
//   ExitAuth when the context has no session.
func onUnauthorized(env *Env, store *ContextStore, name string, err error) error
//   when err is *client.Error{401}: clear that context's session (Update), return ExitError{3, "not logged in to <name> (<url>): run stackstead login <url>"}; else return err unchanged
// commands: login <url>, logout, context ls|current|use <name>|rm <name>, whoami, status,
//           settings metrics-token status|rotate|clear, settings password
```

**Behaviour:**
- `login <url> [--context NAME] [--username U] [--password-stdin] [--ca-file F] [--insecure]`: validate the URL (scheme http/https, host present); `--ca-file` is made absolute and must exist; if the scheme is `http` and the host is not loopback print `warning: <url> is not TLS; the session cookie will be sent in clear text`; username defaults to `admin` unless given (prompt on a TTY when `--username` absent? keep simple: default `admin`, flag to override); password via `readSecret`; call `Login` with a session-less client; on success `Update` the store (create/replace the context, set `Current`), print `logged in to <name> (<url>) as <user>; session expires <relative>`; `-o json` → `{"context","url","username","expiresAt"}`. 401 → exit 3 `invalid username or password`; 429 → exit 8 with the server message and `RetryAfter` when known; 403 from `/api/setup` is not relevant here.
- `logout`: `POST /api/logout` best effort (a 401 is fine), then clear the session locally; exit 0 either way; message to stderr.
- `context ls` table: `NAME  URL  USER  SESSION (valid until … | expired | none)  TLS (system | ca:<file> | INSECURE)` with `*` on the current; `context use` sets `Current` (unknown → exit 4); `context rm` removes (and clears `Current` if it was current); `context current` prints the name (exit 4 when none).
- `whoami`: `GET /api/me` → `<username> @ <url> (context <name>)`; JSON = `Me`.
- `status`: `GET /api/status` → key/value lines (`docker`, `compose`, `events`, `metrics`, `server version`, plus `client version`), colouring `unavailable`/`disconnected`/`degraded` red; a server version different from the client's prints a note on stderr. JSON = `Status` with an added `clientVersion`? No: JSON prints the server's `Status` verbatim (the wire type), the note stays on stderr.
- `settings metrics-token status|rotate|clear`: `rotate` prints the token once on stdout (`-o json` → `MetricsToken`), with a stderr reminder that it is shown once and that the old token stopped working; `clear` → `DELETE`; `status` → `configured: true|false`.
- `settings password`: prompts current, new, confirm (TTY only; `--password-stdin` reads **two** lines: current then new, for scripts); 403 → exit 3 `current password is incorrect`; 400 → exit 6 with the server message; 429 → 8. Prints `password changed; other sessions were signed out`.
- Every authenticated command routes its error through `onUnauthorized`.

**Tests (write first)** with `httptest` API stubs and a temp config dir:
- `TestLoginStoresContextAndSetsCurrent` (file content: url, username, session, expiresAt; `Current`); `TestLoginJSONHasNoSession`; `TestLoginDefaultsContextNameFromHost`; `TestLoginWarnsOnPlainHTTPToRemoteHost` and not for `http://127.0.0.1`; `TestLoginWrongPasswordExits3`; `TestLoginRateLimitedExits8`; `TestLoginCAFileMustExist`.
- `TestUnauthorizedClearsSessionAndExits3` (`whoami` against a 401 stub → session empty in the file, exit 3, message names the context and url).
- `TestInsecureContextWarnsEveryCommand`; `TestLooseContextsPermsWarn`.
- `TestLogoutClearsEvenWhen401`; `TestContextLsUseRm`; `TestStatusTableAndJSON`; `TestStatusNotesVersionMismatch`.
- `TestMetricsTokenRotatePrintsOnce` (stdout has the token exactly once; stderr the reminder; JSON shape); `TestSettingsPasswordStdinTwoLines`; `TestSettingsPassword403Exits3`.
- `exit_test.go`: the `*client.Error` mapping table incl. `ECONNREFUSED` → 7.

**Acceptance:** `go test -race -timeout 180s ./internal/cli/` green.

---

### Task 7: Local admin commands — `config`, `db`, `admin`, `health`

**Wave:** 2. Depends on T2, T3 (and `metrics.NewToken` from T1).

**Files:** create `internal/cli/cmd_config.go`, `cmd_db.go`, `cmd_admin.go`, `cmd_health.go`, `local.go` (shared: config loading, DB opening with the ownership check, lock), and their tests.

**Interfaces:**
```go
type localFlags struct { Config string }   // --config P; "" → /etc/stackstead/config.yaml if it exists (same rule as serve)
func loadLocalConfig(env *Env, lf localFlags) (config.Config, string /* path used */, error)
func openLocalDB(env *Env, cfg config.Config, opts store.OpenOptions) (*sql.DB, error)
//   path := filepath.Join(cfg.DataDir, "stackstead.db"); ownership check (see Behaviour); store.OpenExisting(path, opts); ErrNoDatabase → ExitError{4, "no database at <path>: start the server once or run stackstead db init"}; SchemaMismatchError → ExitError{5, its message}
func withExclusiveLock(cfg config.Config, fn func() error) error   // lockfile.Acquire(<data_dir>/stackstead.lock); *HeldError → ExitError{5, "the stackstead server is running (pid N): stop it first"}
var geteuid = os.Geteuid; var fileOwner = func(path string) (uid int, err error)   // seams for tests
// commands: config init|validate|show; db init|check|backup|restore; admin set-password|revoke-sessions|metrics-token status|rotate|clear; health
```

**Behaviour:**
- **Ownership check** (`openLocalDB`, and `db init` before creating): if `geteuid() == 0` and the DB file exists with owner uid ≠ 0 → `ExitError{3, "<path> belongs to uid N; run this as that user: sudo -u <name> stackstead …"}` (name via `os/user.LookupId`, fallback to the uid). If the DB does not exist and euid is 0 and `data_dir` exists owned by another uid → same refusal (so `db init` as root cannot create a root-owned DB in the service's directory).
- `config init [--path P] [--listen] [--stacks-dir] [--data-dir] [--force]`: `P` defaults to `/etc/stackstead/config.yaml`; refuses to overwrite without `--force` (exit 5); renders `config.Template(cfg)` where `cfg` = `Defaults()` with the flag overrides, validated with `config.Load`-equivalent checks before writing (absolute paths; exit 6); creates the parent dir 0755; writes 0644 atomically; prints the path. Never touches `data_dir`/`stacks_dir`.
- `config validate [--config P]`: `config.Load(path, env.Getenv)`; ok → `config <path> is valid` (or `no config file; defaults and environment are valid`), exit 0; error → the loader's message, exit 6. Note: env overrides are applied, matching what `serve` would do.
- `config show`: `config.Effective` as a table `KEY  VALUE  SOURCE`; `-o json` → `[{key,value,source}]`.
- `db init`: `store.Open` (creates + migrates) after the ownership check; prints `database <path> at schema N`; idempotent.
- `db check [-o json]`: `OpenExisting`, `QuickCheck`, `SchemaVersion`, file size, counts of `users`, `sessions`, `stacks`, `metric_samples`, `settings`, WAL file size if present; exit 1 when quick_check reports problems; JSON `{path, schema, supportedSchema, sizeBytes, walBytes, ok, problems, counts{…}}`.
- `db backup <dest> [--force]`: online-safe; `dest` must not exist unless `--force` (then the old file is removed only after the new one is complete: write to temp, rename over); prints path, size and schema; runs `QuickCheck` on the copy (open it read-only with `OpenExisting`) and fails with exit 1 if not ok.
- `db restore <src> [--yes]`: exclusive (`withExclusiveLock`); validates `src` first with `store.OpenExisting(src, OpenOptions{ReadOnly: true, AllowOlderSchema: true})` (an older schema is fine: the server migrates it at its next start; a newer one is refused, exit 6) and `QuickCheck` (problems → exit 1); confirmation text names both paths and says sessions will be whatever the backup holds; copies `src` to `<db>.restore-tmp`, fsyncs, renames the current DB to `stackstead.db.bak-<UTC timestamp>` (kept; printed), renames the temp into place, removes stale `stackstead.db-wal` and `-shm`; prints the backup path and the restored schema version. Any failure before the final rename leaves the current DB untouched.
- `admin set-password [--username U] [--password-stdin|--password-file F]`: online-safe; `U` defaults to the existing admin's username (`AdminUsername`) or `admin` when none; secret via `readSecret` with confirm on a TTY; `ResetPassword`; prints `admin account <U> created` or `password for <U> updated; all sessions were signed out`; `ErrWeakPassword`/`ErrUsernameMismatch`/`ErrInvalidUsername` → exit 6. Reminder on stderr when the server is not running (lock not held — checked with a non-blocking `Acquire` + immediate `Release`): none needed; the change takes effect at the next login either way.
- `admin revoke-sessions`: prints the count.
- `admin metrics-token status|rotate|clear [-o json]`: `metrics.NewStore(db, env.Now)`; `rotate` → `NewToken`, `SetTokenHash`, print the plaintext once (JSON `MetricsToken`), stderr note `the server picks this up within 5 s`; `clear` → `SetTokenHash("")`; `status` → `configured`.
- `health [-o json]`: checks, each with `ok | fail: <reason>` and overall exit: `config` (loads), `database` (`OpenExisting` + `QuickCheck`), `server` (`GET http://<listen>/api/health` with `0.0.0.0`/`::` mapped to loopback, 3 s timeout, via `client.New` without a session), `docker` (`engine.New(cfg.DockerSocket, 3s).Ping`), `compose` (`buildRunner(cfg).Version`). Exit 0 when all ok; 7 when server, docker or compose fail; 1 when config or database fail. JSON `{ok, checks:[{name, ok, detail}]}`. `health` opens the DB with `OpenOptions{ReadOnly: true}` and still applies the ownership refusal when running as root (a read-only open can create `-shm`; refusing is simpler than reasoning about it, and `health` as root over a service-owned DB reports the `database` check as `skipped: run as <owner>` rather than failing the whole command).

**Tests (write first)** with temp dirs, temp DBs created through `store.Open`, and the `geteuid`/`fileOwner` seams:
- `config`: `TestConfigInitWritesTemplateAndRefusesOverwrite`; `TestConfigInitRejectsRelativeDirs` (exit 6); `TestConfigValidateReportsLoaderError` (bare duration → exit 6 with the key name); `TestConfigShowSources`.
- `db`: `TestDBInitCreatesAndIsIdempotent`; `TestDBCheckJSON`; `TestDBBackupProducesValidCopy` (+ `--force` path, + refuses existing); `TestRestoreRefusedWhileLocked` (hold the lock in-test → exit 5, DB untouched); `TestRestoreReplacesDBAndKeepsBak` (restore an older snapshot: the users row count changes, `.bak-` exists, no `-wal`/`-shm`); `TestRestoreRejectsNewerSchema` and corrupt source (exit 6/1) without touching the DB; `TestRestoreNeedsYesWithoutTTY`.
- `admin`: `TestSetPasswordCreatesThenResets` (first run creates; a session created via `auth.Login` is gone after reset; `--password-stdin`); `TestSetPasswordUsernameMismatchExits6`; `TestSetPasswordWhileAnotherConnectionWrites` (a goroutine holding a second `store.Open` inserting rows in a loop; the command succeeds within the 5 s busy timeout); `TestAdminRefusesRootOverForeignOwner` (`geteuid` → 0, `fileOwner` → 1001 → exit 3, message contains `sudo -u`); `TestMetricsTokenRotatePrintsOnceAndStoresHash` (the stored hash equals `metrics.HashToken(printed)`); `TestRevokeSessionsCount`.
- `health`: `TestHealthAllOK` (an `httptest` server for `/api/health`, a fake unix-socket engine answering `_ping`, a fake `docker` binary on PATH as in Plan 3's composecli tests); `TestHealthServerDownExits7`; `TestHealthJSONShape`.

**Acceptance:** `go test -race -timeout 240s ./internal/cli/ ./internal/store/` green.

---

### Task 8: `stacks` and `ops` commands

**Wave:** 4. Depends on T6.

**Files:** create `internal/cli/cmd_stacks.go`, `cmd_stacks_test.go`, `internal/cli/cmd_ops.go`, `cmd_ops_test.go`.

**Interfaces:**
```go
// streamOperation streams one operation to completion (shared by up/down/…/rm and `ops stream`).
// Human mode: raw lines to Stdout; a stderr note on reconnect; on done a one-line summary on Stderr.
// JSON mode: NDJSON {event, id, data}. Returns ExitError{9, "<action> <stack> <state>: <error>"} unless state == succeeded.
func streamOperation(ctx context.Context, env *Env, p *Printer, c *client.Client, op apitypes.Operation) error
// commands: stacks ls | show <name> | up|down|restart|pull|build <name> [--detach] | rm <name> [--volumes] [--yes] [--detach] | import <path>
//           stacks compose get <name> | edit <name> | put <name> -f FILE --hash H
//           ops ls [--running] | show <id> | stream <id> | cancel <id>
//           networks ls
```

**Behaviour:**
- `stacks ls`: table `NAME  STATUS  SERVICES  RUNNING  ORIGIN  PATH` sorted by name (the API order); status coloured; empty list → `no stacks` on stderr, empty table on stdout (JSON `[]`).
- `stacks show <name>`: header lines (name, project, origin, path, status, hash), then a `SERVICE  IMAGE/BUILD  PORTS  LIMITS  READ-ONLY` table and a `CONTAINER  SERVICE  STATE  STATUS` table; `parseError` printed in red when present. JSON = `StackDetail`.
- Lifecycle: `Act` → 202 `Operation`; `--detach` prints `operation <id> started` (JSON = `Operation`); otherwise `streamOperation`. The `Last-Event-ID` for the stream is the highest numeric line id seen (the `dropped` note has no id and is printed as-is). 409 → exit 5 `stack <name> is busy: another operation is running` (the server's message is used verbatim when present); 503 → exit 7; 404 → exit 4.
- `stacks rm`: confirmation `remove stack <name> (compose down[, and delete its volumes]); files stay on disk? [y/N]`; then `Remove` and stream like an action.
- `stacks import <path>`: `Import`; prints the summary; `ChecksPartial` → stderr `note: port checks were partial (Docker unavailable)`.
- `stacks compose get`: prints the YAML to stdout (JSON = `RawCompose`); `put -f FILE --hash H` (`-` for stdin): `SaveCompose`; 409 → exit 5 `compose file changed on the server: run stacks compose get and retry`; 422 → exit 6 with the compose message verbatim; prints the new hash. `edit`: `get` → temp file `stackstead-<name>-*.yaml` (0600) → `$VISUAL` or `$EDITOR` (exit 2 when neither is set; no TTY → exit 2) via `exec.Command` with the terminal attached → unchanged content → `no changes`; else `SaveCompose` with the original hash; on 409/422 keep the temp file and print its path with the error (exit 5/6); on success delete it.
- `ops ls [--running]`: `Operations(state)` table `ID  STACK  ACTION  STATE  STARTED  DURATION  EXIT`; `ops show <id>`: header + lines; `ops stream <id>`: `Operation(id)` then `streamOperation` (an already finished op prints its lines and the summary; exit by its state); `ops cancel <id>`: prints the returned state.
- `networks ls`: `NAME  DRIVER  SUBNETS`.

**Tests (write first)** with `httptest` stubs (JSON fixtures from `apitypes`; SSE scripted with `http.Flusher`):
- `TestStacksLsTableAndJSON`; `TestStacksShowRendersServicesAndContainers`; `TestUpStreamsAndExits0` (lines appear in order; summary on stderr); `TestUpFailedOperationExits9`; `TestUpDetachPrintsID`; `TestUpBusyExits5`; `TestUpUnavailableExits7`; `TestUpWaitsAcrossDisconnectWithoutDuplicateLines` (server closes after line 2; second request has `Last-Event-ID: 2`; output has lines 0–4 once; `done`); `TestUpDroppedReconnectsAndReplays`; `TestRmNeedsConfirmation` (non-TTY without `--yes` → exit 2; `--yes` → `DELETE …?volumes=true` observed); `TestImportNotesPartialChecks`; `TestComposePutStaleHashExits5`; `TestComposeEditNoChanges` and `TestComposeEditSavesWithOriginalHash` (`EDITOR` = a tiny script written by the test that appends a line); `TestOpsLsRunningFilter`; `TestOpsStreamFinishedOp`; `TestOpsCancel`; `TestNetworksLs`; JSON NDJSON shape for a streamed op.

**Acceptance:** `go test -race -timeout 240s ./internal/cli/` green.

---

### Task 9: `logs`, `metrics`, `events`

**Wave:** 4. Depends on T6.

**Files:** create `internal/cli/cmd_logs.go`, `cmd_logs_test.go`, `internal/cli/cmd_metrics.go`, `cmd_metrics_test.go` (`events` lives in `cmd_metrics.go`).

**Interfaces:**
```go
// logs <stack> [-f] [--tail N|all] [--since X] [--service S] [--timestamps]
// metrics <stack> [--from T] [--to T] [--step D] [--service S] [--live]
// events
type logCursors struct{ /* map[container12]string last id */ }   // String() joins all ids with ","; set from each log event's id
```

**Behaviour:**
- `logs`: `StreamLogs` with the query; human lines `[<service>] <text>` (service names coloured from a fixed palette by hash, stderr lines dimmed red when colour is on; `--timestamps` prefixes the RFC3339 time); `error` events → stderr `log stream error for <container>: <msg>`; `end` → stop (`ErrStreamDone`). With `-f`, `LastEventID` returns `logCursors.String()` so a reconnect resumes every container exactly; a reconnect note goes to stderr. Without `-f`, the stream ends on `end`. `--tail` validates `N ≥ 0` or `all`; `--since` is passed through (the server validates). JSON: NDJSON of `log`/`error`/`end` events.
- `metrics` (history): `MetricsHistory` with `--from`/`--to` (RFC3339 or Unix seconds, or a duration for `--from` meaning "that long ago"), `--step`, `--service`; table `TIME  SERVICE  CPU (avg)  MEM (avg)  LIMIT  NET RX/s (total)  NET TX/s (total)  BLK R/s (total)  BLK W/s (total)` with `-` for absent rates (the labels state the server's per-service semantics: gauges averaged, rates summed across replicas; the CLI does not re-aggregate), times in local time (`--utc` flag? keep local; JSON keeps the wire RFC3339 UTC). `--live`: `StreamMetricsLive`, one row per `sample` with cumulative counters shown as totals (the live feed has no rates), `dropped` → reconnect. Default window is the server's (last hour).
- `events`: `StreamEvents`; prints `status events=<connected|disconnected> metrics=<ok|degraded>` and `change <project> seq=<n>` lines with a local timestamp; JSON NDJSON. Runs until Ctrl-C (exit 130).

**Tests (write first)** with scripted SSE stubs:
- `TestLogsPrintsServicePrefixedLines` (+ stderr tagging, `--timestamps`); `TestLogsEndsOnEndWithoutFollow`; `TestLogsFollowResumesWithAllCursors` (two containers `aaaa…`, `bbbb…`; the server closes after sending ids `a:ts1:1`, `b:ts2:1`, `a:ts2:2`; the second request's `Last-Event-ID` contains exactly `aaaaaaaaaaaa:ts2:2` and `bbbbbbbbbbbb:ts2:1`, order-insensitive); `TestLogsErrorEventGoesToStderr`; `TestLogsJSONNDJSON`; `TestLogs401MidStreamExits3`.
- `TestMetricsHistoryTable` (rates formatted, first point `-`, header carries `(total)` on the four rate columns and `(avg)` on CPU/MEM); `TestMetricsFromDuration` (`--from 30m` → `from` within 1 s of now-30m); `TestMetricsLiveRows`; `TestEventsLines`; `TestEventsCtrlCExits130` (cancel the context mid-stream; exit 130, no error text).

**Acceptance:** `go test -race -timeout 240s ./internal/cli/` green.

---

### Task 10: Wiring, serve lock and version, e2e, README, deploy unit

**Wave:** 5. Depends on all.

**Files:** create `internal/cli/commands.go` (root tree), `cmd/stackstead/cli_e2e_test.go` (`//go:build e2e`); modify `internal/cli/serve.go` (+ `serve_test.go`), `deploy/stackstead.service`, `README.md`, `Makefile` (only if `e2e` needs a change), and this plan's checkboxes.

**Behaviour:**
- `commands.go`: `rootCommand()` registers every command from T2, T6–T9 in the tree of the design spec, with `Short` strings; `help` output is grouped: *Server*, *Remote*, *Host admin*. A test asserts that every command name in the spec's tree resolves and that no two subcommands share a name.
- `serve`: acquire `lockfile.Acquire(<data_dir>/stackstead.lock)` right after `MkdirAll(DataDir)`; `*HeldError` → `ExitError{5, "another stackstead server is running (pid N)"}`; release on return. Pass `version.Get().Version` into `api.Options.Version`. Startup log includes the version (already from T2).
- `deploy/stackstead.service`: `ExecStart=/usr/local/bin/stackstead serve --config /etc/stackstead/config.yaml`. Everything else unchanged (Plan 7 hardens it and moves it to `deploy/systemd/`; if Plan 7 has already merged, edit the unit at its new path instead).
- **README:** a "Command line" section (remote: login/contexts, the command list with one example each, `-o json`, exit-code table, TLS flags, where contexts are stored and that the file holds the session); a "Host administration" section (which commands are online-safe vs need the server stopped, the root/ownership rule, `db backup`/`restore` procedure, `admin set-password` for a forgotten password, `health`); "Build" gains `make build-go` and `stackstead version`; the metrics paragraph says rates in history are totals across a service's replicas and gauges are averages (and that `stackstead metrics` labels them so); "Install" switches to `serve`; first-run setup stays in the UI (Plan 7 owns the install section), and a "Forgot the admin password" paragraph shows `sudo -u stackstead stackstead admin set-password --config … --password-stdin` (replacing the curl setup example if Plan 5 T23 has not already removed it); "Develop" uses `go run ./cmd/stackstead serve`; the API table gains `GET /api/health` and the `version` field of `/api/status`.
- **e2e (`cli_e2e_test.go`, no Docker, no Node needed — the placeholder UI is fine):**
  1. Build the binary into `t.TempDir()` with `CGO_ENABLED=0` and `-ldflags -X …Version=e2e-test` (reuse Plan 5's helper if `e2e_test.go` exports one in the package; otherwise a private `buildCLI`).
  2. `version` → contains `e2e-test` and `License: AGPL-3.0`; `version --short` → exactly `e2e-test`; `version -o json` has the seven keys.
  3. `config init --path <tmp>/config.yaml --listen 127.0.0.1:<free port> --stacks-dir <tmp>/stacks --data-dir <tmp>/data` → exit 0; again → exit 5; `config validate --config …` → 0; `config show -o json` → `listen` source `file`.
  4. `health --config …` → exit 7 (server down) and the `server` check failed; `db check` → exit 4 (no DB).
  5. `db init --config …` → 0; `admin metrics-token rotate -o json` → a token `T1`.
  6. Start `serve --config … ` with `STACKSTEAD_DOCKER_SOCKET=<tmp>/none.sock`; wait for `GET /api/health`; capture stderr. A second `serve` → exit 5 within 5 s.
  7. While it runs: `admin set-password --config … --username admin --password-stdin` (stdin `correct horse battery staple`) → 0, "created"; `login http://127.0.0.1:<port> --context e2e --password-stdin` → 0; `whoami` → `admin`; `status -o json` → `docker: unavailable`, `version: e2e-test`; `stacks ls -o json` → `[]`; `ops ls` → empty; `settings metrics-token rotate -o json` → `T2`; `GET /metrics` with `Bearer T2` → 200 and with `T1` → 401; `admin metrics-token rotate --config …` → `T3`; within 6 s `GET /metrics` with `T3` → 200; `admin metrics-token clear` → within 6 s `/metrics` → 404; `settings password --password-stdin` (two lines) → 0, then `login` again with the new password → 0; `db backup <tmp>/b.db` → 0 and `db check` on it ok (via `db restore`'s validation path or a direct `QuickCheck`); `db restore <tmp>/b.db --yes` → exit 5 (server running); `logout` → 0; `whoami` → exit 3; `admin set-password` with a 5-char password → 6; `logs nope` after a fresh login → 4; `stacks up nope` → 4; `health` → exit 7 (docker down) with `server ok` in the output.
  8. SIGTERM the server; wait; `db restore <tmp>/b.db --yes` → 0, `.bak-` file exists, no `-wal`/`-shm`; `health` → 7 with `server` failed and `database ok`.
  9. Grep every captured stdout/stderr and the contexts file's directory listing for the session cookie value (read from the contexts file before logout) — it must appear only inside `contexts.json`; the contexts file mode is 0600.
- **Plan 7 contract check:** a test in `commands_test.go` asserts the flags and exit codes listed in the contract section below exist (`serve --config`, `version --short`, `version -o json`, `config validate --config`, `config init --path/--listen/--stacks-dir/--data-dir/--force`, `admin set-password --username/--password-stdin/--password-file`, `admin metrics-token rotate -o json`, `db init`, `health -o json`).

**Acceptance:**
- `go build ./... && go vet ./... && test -z "$(gofmt -l .)" && go test -race -timeout 300s ./...` green on a tree without Node and without Docker.
- `go mod verify`; `CGO_ENABLED=0 GOOS=linux GOARCH=amd64|arm64 go build ./...` and `GOOS=darwin GOARCH=arm64` green; binary sizes recorded in the report (expected growth over Plan 5's binary: well under 1 MB).
- `make build-go && make e2e` green (both the Plan 5 UI smoke, which skips without a built UI, and the CLI suite).
- Manual smoke with Docker, from a second machine or a second user account: `login`, create a stack through the web UI, then from the CLI `stacks ls`, `stacks up`, `logs -f` while curling the service (kill the server's network briefly or restart the server to see the resume note), `metrics`, `settings metrics-token rotate` + Prometheus curl, `stacks down`, `stacks rm --yes`; on the host `db backup`, `health`.
- Delete `cmd/stackstead/main_test.go` is confirmed (moved in T2). Plan checkboxes ticked.

---

## Contract for Plan 7 (installers)

Plan 7 (systemd unit, launchd plist, install scripts) may rely on exactly these interfaces. Changing any of them is a Plan 6 change that must be coordinated.

**Binary and layout**
- One static binary `stackstead` for `linux/amd64`, `linux/arm64`, `darwin/arm64` (`make release` writes `bin/stackstead-<os>-<arch>` with version ldflags; `make build-go` builds the host binary without Node). `CGO_ENABLED=0`; no runtime files besides the config, `data_dir` and `stacks_dir`.
- Config file: YAML at `/etc/stackstead/config.yaml` by default (probed only when `--config` is absent); every key overridable by `STACKSTEAD_<KEY>`. Keys and defaults: `listen 127.0.0.1:8420`, `stacks_dir /opt/stacks`, `data_dir /var/lib/stackstead`, `allowed_roots [stacks_dir]`, `metrics_retention 72h`, `metrics_interval 10s`, `operation_timeout 15m`, `docker_socket /var/run/docker.sock`, `shutdown_grace 30s`. All paths must be absolute. On macOS pass a config with user paths and `docker_socket: $HOME/.docker/run/docker.sock`.
- `data_dir` holds `stackstead.db` (+ `-wal`/`-shm` while open) and `stackstead.lock`. It must be owned and writable by the service user (`StateDirectory=stackstead` in systemd does this).

**Server**
- `stackstead serve --config /etc/stackstead/config.yaml` — foreground, logs to stderr (slog text; journald/launchd capture it), exits 1 on a fatal error (config, DB, port), exits 5 when another instance holds the lock. Stops cleanly on SIGTERM/SIGINT within `shutdown_grace` + 10 s. Bare `stackstead --config P` still serves (deprecated); do not use it in new units.
- Liveness: `GET http://<listen>/api/health` → `200 {"ok":true}` without authentication. Readiness beyond that (Docker, compose) is `stackstead health` or the authenticated `/api/status`.
- First start without an admin prints a setup token to stderr (unchanged). Installers that provision non-interactively use the admin command below instead; the token then becomes irrelevant (setup refuses once an admin exists).

**Version**
- `stackstead version` → line 1 `stackstead <version> (<commit> <date>) <go> <os>/<arch>`, line 2 `License: AGPL-3.0`; `stackstead version --short` → `<version>` and a newline, nothing else (the installer's upgrade check); `stackstead version -o json` → `{"version","commit","date","go","os","arch","license"}`. `version` is the git tag (`v1.2.3`), `v1.2.3-N-g<sha>` or `-dirty` suffixed; `dev` when built without ldflags and without VCS info.
- Build-time injection: `-ldflags "-X github.com/rcss-labs/stackstead/internal/version.Version=<v> -X …/internal/version.Commit=<sha> -X …/internal/version.Date=<RFC3339 UTC>"`. The Makefile's `LDFLAGS` variable is the only place these names appear; Plan 7's `release`/`dist` targets reuse it.
- No `sd_notify`/`READY=1`: use `Type=simple`; readiness is `GET /api/health`.
- Release artefacts are unsigned for now (checksums only); the binary neither verifies nor produces signatures. Plan 7 owns checksums and the `LICENSE` file (AGPL-3.0).

**Host commands (run on the host; installers use only the ones marked ✓)**

Installers (Plan 7) stay out of the admin account: first-run setup happens in the UI (loopback or setup token). `admin set-password` and `db init` are documented recovery/scripting paths, not installer steps.
- `stackstead config init --path /etc/stackstead/config.yaml [--listen A] [--stacks-dir D] [--data-dir D] [--force]` — writes the commented template (mode 0644, parent dir created); exit 0 written, 5 exists (without `--force`), 6 invalid values. Does not create `data_dir`/`stacks_dir`.
- ✓ `stackstead config validate --config P` — exit 0 valid, 6 invalid (message names the key). No DB access; safe as root.
- ✓ `stackstead health --config P [-o json]` (run as the service user; summary only, never an install gate) → exit 0 all good, 7 server/Docker/compose unreachable, 1 config/DB problem. JSON `{"ok":bool,"checks":[{"name","ok","detail"}]}` with names `config`, `database`, `server`, `docker`, `compose`. As root over a service-owned DB the `database` check reports `skipped` instead of failing.
- `sudo -u <service user> stackstead db init --config P` — creates and migrates the DB (idempotent, exit 0). Only for scripts that want the DB before the first `serve`; `serve` creates it itself.
- `sudo -u <service user> stackstead admin set-password --config P --username admin --password-stdin` — reads one line from stdin (trailing newline stripped; ≥ 12 characters), creates the admin or resets its password and signs out every session. Works while the server runs (takes effect immediately). Exit 0 ok, 6 policy/username mismatch, 4 no DB (run `db init` or start the server once), 3 when run as root over a DB owned by the service user (the message gives the `sudo -u` form), 5 schema mismatch (the server needs to migrate first). README names this as the "forgot the admin password" path.
- `sudo -u <service user> stackstead admin metrics-token rotate --config P -o json` → `{"token":"…"}` once; the running server honours it within 5 s. `… clear` disables `/metrics`.
- `sudo -u <service user> stackstead db backup <dest>` (online-safe) and `stackstead db restore <src> --yes` (server must be stopped; exit 5 otherwise; keeps `stackstead.db.bak-<timestamp>`).

**Exit codes:** the table in the design spec §4 (0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 130).

**The CLI's own files:** `$XDG_CONFIG_HOME/stackstead/contexts.json` (default `~/.config/stackstead/`), per user, created by `login`. Installers must not create, template or chmod it.

**Suggested unit lines (Plan 7 decides the rest):** `ExecStart=/usr/local/bin/stackstead serve --config /etc/stackstead/config.yaml`, `ExecStartPre=/usr/local/bin/stackstead config validate --config /etc/stackstead/config.yaml`, `StateDirectory=stackstead`, `SupplementaryGroups=docker`, `Restart=on-failure`. For launchd: `ProgramArguments` = `[stackstead, serve, --config, <path>]`, `KeepAlive` with `SuccessfulExit=false`, stderr to a log file.

---

## Self-review notes

- **Scope coverage against the user's decisions:** remote client for every documented route the CLI should expose (T4, T6, T8, T9) including operation streaming with resume (T4/T8) and `logs -f` with composite `Last-Event-ID` (T4/T9), metrics (T9), status (T6), settings incl. the metrics token and password (T6); local admin: password reset, token rotate/clear, config show/validate/init, DB backup/restore/check/init, health (T3, T7) with an explicit online-safe/exclusive classification and the lock (T3, T7, T10); server lifecycle: `serve`, `version`, `config init|validate` (T2, T7, T10); one static binary with subcommands (T2); bare-invocation decision justified (Decisions); auth via `login` + cookie in `~/.config/stackstead` with 0600 and contexts (T5, T6), no new backend auth, CSRF/JSON rules, 401/429 handling (T4, T6); TLS roots, `--ca-file`, `--insecure` (T4, T6); stdlib-only except `x/term` with reasoning (Decisions); tables + `-o json`, exit codes, colour rules (T2); DTO reuse through `apitypes` without coupling the client to server internals (T1, T4, deps tests).
- **Dependencies on Plan 5:** `POST /api/password` (T4 `ChangePassword`, T6 `settings password`), `GET /api/settings/metrics-token` (T6), log event ids/cursors (T4/T9), `Makefile` and `e2e_test.go` (T2, T10). Plan 6 starts after Plan 5 merges.
- **Backend changes are deliberately tiny:** aliases (no wire change), `version` in `/api/status`, `GET /api/health`, bounded token-cache revalidation, four store functions, three auth functions, a config template, a lock file.
- **Name consistency:** every cross-task symbol is defined in exactly one Interfaces block and listed under Symbol ownership; waves respect the dependency order (`apitypes` → `client` → `apiClient` → remote commands; framework → prompts/contexts → commands; store/auth/config/lockfile → local commands → serve lock).
- **Not covered on purpose:** stack creation and form edits from the CLI, shell completion, Windows, CI configuration (deferred), any token-based API auth.
