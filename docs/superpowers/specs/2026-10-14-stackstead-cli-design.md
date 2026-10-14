# Stackstead CLI — Design

**Date:** 2026-10-14
**Status:** Draft, awaiting review
**Relation to v1:** new scope beyond `2026-10-14-stackstead-v1-design.md`, decided by the user. Implemented by Plan 6 after Plan 5 (web UI) merges. Plan 7 (installers) consumes the server-side commands defined here.

## 1. Purpose

One static `stackstead` binary gains subcommands so that:

1. **Remote use** — a user on a laptop manages a Stackstead host over its HTTP API from a terminal: list and inspect stacks, run lifecycle actions and watch their output, follow logs, read metrics, manage the Prometheus token and the admin password. The CLI is a second client of the same JSON/SSE API the web UI uses; it adds no server-side auth type.
2. **Host administration** — an operator on the host repairs or provisions an instance without the UI: set or reset the admin password, rotate the Prometheus token, back up and restore the database, validate the config, check health. These work against the data directory and must coexist safely with a running server.
3. **Server lifecycle** — `stackstead serve` runs the server (today's behaviour), `stackstead version` identifies the build, `stackstead config init|validate` provisions configuration. Plan 7's systemd unit and launchd plist call these.

Guiding constraints stay those of v1: fast and light, stdlib first, no CGO, `linux/{amd64,arm64}` and `darwin/arm64`.

## 2. Scope

### In
- Subcommands in the existing binary (`cmd/stackstead`), one module, `CGO_ENABLED=0`.
- Remote client: `login`/`logout`/`context`, `whoami`, `status`, `stacks ls|show|up|down|restart|pull|build|rm|import`, `stacks compose get|edit|put`, `ops ls|show|stream|cancel`, `logs [-f]`, `metrics [--live]`, `events`, `networks ls`, `settings metrics-token status|rotate|clear`, `settings password`.
- Local admin: `admin set-password`, `admin revoke-sessions`, `admin metrics-token status|rotate|clear`, `db init|check|backup|restore`, `config init|validate|show`, `health`.
- Server: `serve`, `version`, `help`.
- Human tables and `-o json`; defined exit codes; colour only on a TTY and never when `NO_COLOR` is set.
- Small backend additions the CLI needs: shared exported wire types, `GET /api/health`, `version` in `/api/status`, metrics-token cache revalidation.

### Out
- Creating stacks or editing services through form-style flags (the JSON `ServiceConfig` is large; `stacks compose edit` covers hand edits). Candidate for a later plan.
- Shell completion, man pages, a TUI.
- Any new server auth mechanism (API keys, tokens for the CLI). The CLI uses the session cookie.
- CI configuration (deferred, as in Plan 5; Makefile targets only).
- Windows.

## 3. Key decisions

| Decision | Choice | Reason |
|---|---|---|
| Command framework | Hand-rolled tree over stdlib `flag`, with interspersed flags (`stackstead logs web -f` works) | cobra/pflag add ~5 packages and reflection-free code is ~200 lines here; no supply-chain surface. |
| Terminal input and detection | `golang.org/x/term` (new dependency) | Correct no-echo password input needs per-OS termios ioctls; x/term is maintained by the Go team, depends only on `x/sys` (already in the graph via sqlite/crypto), is sumdb-verified and pinned exactly. Hand-rolling two build-tagged ioctl files buys nothing. |
| Tables, colour, JSON | stdlib `text/tabwriter`, ANSI codes, `encoding/json` | Enough for ≤ 10-column tables; no table library. |
| Wire types | New package `internal/apitypes` holding the exported JSON structs; `internal/api` switches to type aliases for its DTOs; `internal/client` imports only `apitypes` | One source of truth for the contract; the client never imports `stacks`, `composefile`, `engine` or `store`. `-o json` prints these shapes verbatim. |
| Remote auth | `stackstead login <url>` → `POST /api/login`, store the `stackstead_session` cookie in `$XDG_CONFIG_HOME/stackstead/contexts.json` (dir 0700, file 0600, atomic writes), one entry per named context | User decision; no backend change. The session is a secret: never printed, never in `-o json`, cleared locally on 401. |
| Contexts | kubectl-like: `--context NAME` / `STACKSTEAD_CONTEXT`, `context ls|current|use|rm`; `login` sets the current context | Several hosts (home lab + office) are the normal case. |
| TLS | System roots by default; per-context `--ca-file` (stored path) or `--insecure` (stored; warns on **every** use). Plain `http://` to a non-loopback host warns once at login | Home labs run private CAs and self-signed certs; the warning keeps the trade-off visible. |
| CSRF/JSON rules | Every POST sends `Content-Type: application/json` and a body (`{}` when empty); DELETE sends none; `User-Agent: stackstead-cli/<version>` | Matches the server's `decodeJSON` and the README. |
| Streams | In-house SSE reader (stdlib) with 45 s heartbeat timeout, jittered reconnect 1–30 s, `Last-Event-ID` resume: numeric line index for operations, comma-joined per-container cursors for logs, immediate reconnect on `dropped`, stop on 401/403/404 | Mirrors the web client (Plan 5 T5/T15/T17) so the two clients share one server contract. |
| Lifecycle commands wait by default | `stacks up web` starts the operation and streams it to completion; exit 9 if it did not succeed; `--detach` prints the operation id and returns | Behaves like `docker compose up`; scripts get a meaningful exit code. |
| Local admin vs running server | A flock on `<data_dir>/stackstead.lock` held by `serve`. Commands are classified **online-safe** (open the DB with the same WAL + `busy_timeout` settings; SQLite serialises the two processes) or **exclusive** (take the lock; refuse with exit 5 when the server holds it) | SQLite handles multi-process writers correctly; what is unsafe is replacing the file under a live connection (restore) or running two servers. |
| Online-safe set | `admin set-password`, `admin revoke-sessions`, `admin metrics-token *`, `db backup` (`VACUUM INTO` is a consistent snapshot), `db check`, `config *`, `health` | The server reads the password hash and sessions from the DB on every request, so a reset takes effect immediately. The metrics-token hash is cached in the server; the cache has a 5 s TTL checked on every request, so a local rotation or clear is picked up within 5 s even while the old token keeps matching, and request volume still costs ≤ 1 read / 5 s. |
| Exclusive set | `db restore`; `serve` itself (a second instance exits 5) | |
| Ownership safety | Admin and db commands open the DB with `store.OpenExisting` (never create, never migrate) and refuse to run as root when the DB belongs to another uid (hint: `sudo -u stackstead …`) | A root-created DB or WAL/SHM file locks the service user out. `db init` (run as the service user) is the explicit creation path; migrations stay the server's job. |
| Non-interactive secrets | `--password-stdin` / `--password-file`; interactive prompt only when stdin is a TTY; never a `--password` flag | Flags leak through `ps` and shell history. Scripts and password recovery use `--password-stdin`; Plan 7's installers do not touch the admin account (first-run setup stays in the UI, v1 spec §6) and only name `admin set-password` as the recovery path. |
| Bare `stackstead` | Prints help, exit 2. `stackstead --config P` (any leading flag) still runs the server with a one-line deprecation notice | A multi-command binary that silently starts a server on bare invocation is surprising; the flag form keeps the existing unit, scripts and `go run` loops working until Plan 7 ships `serve`. |
| Version | `internal/version` with `-ldflags -X` values from the Makefile, falling back to `debug.ReadBuildInfo` (vcs tags/revision) | `go install` builds and `make release` builds both identify themselves. |
| Health | Local `health` checks the DB (`quick_check`), the server (`GET /api/health`, unauthenticated, body `{"ok":true}`, no version or state), the Docker socket and `docker compose version` | systemd/compose healthchecks and uptime monitors need an unauthenticated liveness URL; everything else stays behind the session. |

## 4. Command tree and exit codes

```
stackstead
  serve [--config P]                 run the server (also: stackstead --config P)
  version [-o json] [--short]        --short prints the version string only (for installers)
  help [command]
  config init [--path P] [--listen A] [--stacks-dir D] [--data-dir D] [--force]
  config validate [--config P]
  config show [--config P] [-o json]
  db init|check|backup <dest> [--force]|restore <src> [--yes]   [--config P]
  admin set-password [--username U] [--password-stdin|--password-file F]   [--config P]
  admin revoke-sessions                                                     [--config P]
  admin metrics-token status|rotate|clear [-o json]                         [--config P]
  health [--config P] [-o json]
  login <url> [--context NAME] [--username U] [--password-stdin] [--ca-file F] [--insecure]
  logout
  context ls|current|use <name>|rm <name>
  whoami | status
  stacks ls|show <name>|up|down|restart|pull|build <name> [--detach]|rm <name> [--volumes] [--yes]|import <path>
  stacks compose get <name>|edit <name>|put <name> -f FILE --hash H
  ops ls [--running]|show <id>|stream <id>|cancel <id>
  logs <stack> [-f] [--tail N|all] [--since X] [--service S] [--timestamps]
  metrics <stack> [--from T] [--to T] [--step D] [--service S] [--live]
  events
  networks ls
  settings metrics-token status|rotate|clear
  settings password
global: --context NAME, -o table|json, --no-color, --timeout D (unary requests, default 30s)
```

| Exit | Meaning |
|---|---|
| 0 | success (for waited operations: the operation succeeded) |
| 1 | runtime error: 5xx, I/O, unexpected response |
| 2 | usage: bad flags or arguments, no command, stdin not a terminal when a prompt is needed |
| 3 | auth/permission: no context, 401 (session expired — the stored session is cleared), 403, refused as root |
| 4 | not found: 404, unknown context |
| 5 | conflict: 409 (busy, stale hash, exists), lock held by a running server, target exists without `--force` |
| 6 | validation: 400 with `fields`, 422, invalid config, weak or mismatched password |
| 7 | unavailable: 503, connection refused, Docker/compose down in `health` |
| 8 | rate limited: 429 |
| 9 | the waited operation ended `failed`, `cancelled` or `timed_out` |
| 130 | interrupted |

## 5. Security notes

- The session cookie is root-equivalent on the host. It lives only in `contexts.json` (0600) and the `Cookie` header; it is never logged, printed, or included in JSON output. Loose file permissions produce a warning naming the file.
- `--insecure` is remembered per context and warned about on every command that uses that context; it is never implied.
- Local admin commands need read/write access to the data directory, which already implies host-level trust; they add no network surface. Refusing to run as root over a service-owned DB prevents the most common self-inflicted outage.
- `admin set-password` revokes every session; `settings password` (remote) keeps the current one, exactly like the web UI.
- `GET /api/health` reveals only liveness.

## 6. Testing strategy

- Test-first per task. Unit tests never need Docker or a network: the client is tested against `httptest` servers (canned responses, scripted SSE including mid-stream disconnects) and once against the real `api.New` router with the existing in-package fakes (contract test). Commands are tested through an `Env` with in-memory stdio, a temp config dir and a fake `IsTerminal`.
- Local admin commands are tested against temp SQLite databases, including a concurrent writer to prove `busy_timeout` behaviour, and a held lock to prove refusal.
- One e2e suite behind `//go:build e2e` builds the real binary and drives `serve`, `admin set-password` (while the server runs), `login`, `stacks ls`, `settings metrics-token rotate` + `/metrics`, `db backup`, `db restore` refused then allowed, `health`, exit codes. No Docker.
- Every `go test` has `-timeout`. Cross-compile check for `linux/{amd64,arm64}` and `darwin/arm64` with `CGO_ENABLED=0`.

## 7. Decisions confirmed by the user (2026-10-14)

All questions raised by the draft are settled:

1. Bare `stackstead` prints help (exit 2); `stackstead --config X` keeps serving with a deprecation line.
2. `golang.org/x/term` is the single new dependency.
3. `GET /api/health` is unauthenticated and answers `{"ok":true}` only.
4. Local `admin metrics-token rotate|clear` is online-safe through the server's the server's 5 s TTL cache (≤ 1 DB read / 5 s).
5. Stack creation and form-style service edits from the CLI are out of scope; `stacks compose edit` via `$EDITOR` is in.
6. Loose permissions on `contexts.json` produce a warning, not a refusal.
7. Installers (Plan 7) never touch the admin account; `admin set-password` is the recovery path only. Installers call `serve`, `version --short`, `/api/health`, optionally `config validate` and `health` (summary only); never `db init`. Makefile split: Plan 6 owns `LDFLAGS` (the `internal/version` `-X` names) and `build-go`; Plan 7 owns `release`, `dist` and packaging.
8. **Licence:** AGPL-3.0 (Plan 7 adds `LICENSE`). `stackstead version` prints a `License: AGPL-3.0` line and `-o json` carries `"license"`, so a binary identifies its terms without the repository.
9. **Releases are unsigned for now** (checksums only). Nothing in the CLI verifies or produces signatures; Plan 7 owns checksums.
10. **Metrics semantics** (user decision 2026-10-14, applies to the server's history endpoint): per service and time bucket, gauges (CPU, memory) are averaged across a service's replicas and network/block-I/O rates are **totals** across the replicas. `stackstead metrics` labels rate columns as totals (`NET RX/s (total)` …) and the README says so; the CLI does not re-aggregate.

No open questions remain.
