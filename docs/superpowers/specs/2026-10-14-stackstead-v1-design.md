# Stackstead v1 — Design

**Date:** 2026-10-14
**Status:** Draft, awaiting review

## 1. Purpose

Stackstead is a lightweight Docker management platform for home labs and SMB DevOps engineers who run several containers on a single machine. It lets a user define, run, and debug Docker Compose stacks through a web UI without hand-writing YAML, while keeping Compose files as the single source of truth.

Primary user is the author; the guiding constraint is **fast and light**. Existing tools (Portainer, Dockge, Komodo) overlap; Stackstead's aim is to combine all of the following in one small binary:

1. Form-driven configuration (resources, ports, volumes, env, network mode, static IP) with validation.
2. Dockerfile-first: build images from local Dockerfiles, not only pull registry images.
3. Built-in observability: live and short-history metrics, live logs.
4. Single Go binary, no external database, low footprint, runs on arm64 (Raspberry Pi) and amd64.

### Success criteria

- A user can go from a local Dockerfile to a running, resource-limited container with a static IP through the UI, without touching YAML.
- An existing hand-written `compose.yaml` can be registered and managed; editing it through forms preserves its comments, key ordering, and unknown keys.
- Every stack remains operable by hand with `docker compose` — no lock-in.
- CPU/memory/network/block-I/O history is visible for at least the retention window (default 72h).
- Idle Stackstead process memory stays small (target: under 50 MB RSS with ~20 containers).

## 2. Scope

### In v1
- Single Docker host (the machine Stackstead runs on).
- Web UI (SPA) with a single admin account.
- Managed stacks directory **and** registered external compose files.
- Stack lifecycle: create, import, edit (form and raw YAML), up, down, restart, pull, build.
- Live status, live logs, metrics with embedded short history, Prometheus `/metrics` endpoint.

### Explicitly out of v1
- Multi-host management (planned later; the JSON API is designed so a remote agent can be added).
- Alerts/notifications.
- Multi-user, roles, OIDC.
- Building from git repositories (v1 builds from local Dockerfile contexts only).
- Registry credential management (relies on the host's `docker login`).
- Container-image distribution of Stackstead itself.
- Frontend end-to-end (Playwright) tests.

## 3. Key decisions

| Decision | Choice | Reason |
|---|---|---|
| Compose file ownership | Managed dir + registered external paths | Create new stacks cleanly; adopt existing ones in place. |
| Editing hand-written files | Edit `yaml.Node` tree in place | Preserves comments/order/unknown keys; rewriting user files would break trust. |
| Driving Compose | Shell out to `docker compose` CLI for lifecycle; Docker Engine Go SDK for reads | Identical behavior to manual use; avoids the large, unstable Compose Go library. |
| Metrics | 10s sampling into embedded SQLite, rolling retention; Prometheus endpoint | Answers "what happened at 3am" without becoming a TSDB. |
| Logs | Read on demand from Docker; not stored | No duplication of Docker's log driver. |
| Auth | Single admin account, argon2id, session cookie | Docker socket access is root-equivalent; must not be open. |
| Deployment | Native binary + systemd unit | Full filesystem access to external compose files; no path-mapping issues. |
| UI | Svelte 5 + Vite SPA, embedded via `go:embed` | Rich interactivity with a small, runtime-free bundle. |
| Storage | SQLite via `modernc.org/sqlite` (pure Go) | No CGO; trivial cross-compilation to arm64. |

## 4. Architecture

```
stackstead (single Go binary, systemd service)
├── web/          Svelte SPA, built by Vite, embedded via go:embed
├── api/          HTTP router: JSON REST + SSE streams, session auth middleware
├── auth/         admin account, argon2id hashing, sessions, rate limiting
├── stacks/       stack registry: managed dir + registered external paths
├── composefile/  YAML node-tree read/edit, preserving formatting
├── composecli/   runs `docker compose` subprocesses, streams output
├── engine/       Docker Engine SDK wrapper: containers, events, logs, stats
├── metrics/      sampler → SQLite rolling window; Prometheus exposition
└── store/        SQLite: users, sessions, settings, stack registry, samples
```

### Components

- **stacks** — Single source of the stack list. A stack is `{name, composePath, origin: managed|external}`. Managed stacks live at `<stacks_dir>/<name>/compose.yaml`. Compose files are the truth; the DB stores only the registry entries. Files are re-read on startup and on detected change.
- **composefile** — Loads YAML into a `yaml.Node` tree. Exposes typed get/set for the modeled fields: image or build (context, dockerfile), ports, volumes, environment, networks, `ipv4_address`, `network_mode`, CPU and memory limits, restart policy, healthcheck. Writes change only the touched nodes. Fields it does not model are shown read-only in forms and editable via raw YAML.
- **composecli** — The only component that executes `docker compose`. Operations: `up -d [--build]`, `down`, `restart`, `pull`, `build`, `config`. Streams stdout/stderr line by line. Holds a per-stack lock so only one operation runs per stack.
- **engine** — Read-side truth about running state. Maps containers to stacks via the `com.docker.compose.project` label (so stacks started outside Stackstead also appear). Maintains one Docker `events` subscription feeding an in-memory state cache.
- **metrics** — One sampler goroutine collects CPU %, memory usage/limit, network RX/TX, and block I/O per container every 10s; batch-writes to SQLite; prunes past retention (default 72h, configurable) hourly. Serves history via REST, live samples via SSE, and current values at `/metrics` (Prometheus format, separate bearer token).
- **auth** — Single admin user. Password hashed with argon2id. Session cookie (HttpOnly, SameSite=Strict, Secure when behind TLS). Per-IP login rate limiting.
- **api** — JSON REST for CRUD and actions; SSE for operation output, logs, status changes, and live metrics. All routes except login/setup require a session.

### Configuration

`/etc/stackstead/config.yaml`, each key overridable by env var (`STACKSTEAD_<KEY>`):

- `listen` (default `127.0.0.1:8420`)
- `stacks_dir` (default `/opt/stacks`)
- `data_dir` (default `/var/lib/stackstead`, holds SQLite DB)
- `allowed_roots` (directories the file browser may access; default `[stacks_dir]`)
- `metrics_retention` (default `72h`), `metrics_interval` (default `10s`)
- `operation_timeout` (default `15m`)

### Deployment

Native binary for linux/amd64 and linux/arm64, plus a systemd unit that runs it as a dedicated `stackstead` user in the `docker` group. TLS is delegated to a reverse proxy.

## 5. Data flows

### 5.1 Create a stack from a Dockerfile
1. User selects build context and Dockerfile via a server-side file browser restricted to `allowed_roots`, then fills the form (name, resources, ports, volumes, env, network mode / static IP).
2. Pre-write validation: unique name; host-port conflicts against running containers and other stacks; static IP within the chosen network's subnet and not in use.
3. `composefile` writes `<stacks_dir>/<name>/compose.yaml` with `build: {context, dockerfile}` and `image: stackstead/<name>:latest`.
4. `docker compose config` validates; on failure the file is removed and the error returned to the form.
5. `docker compose up -d --build` runs; output streams to the UI over SSE.

### 5.2 Import an existing stack
User supplies a compose file path. It is registered as `origin: external`, parsed, its services listed, and running containers matched by project label. Nothing on disk is modified until the user edits.

### 5.3 Edit
- **Form edit:** patch YAML nodes → `compose config` validation → atomic save → user clicks "Apply" → `up -d` (recreates only changed services).
- **Raw edit:** CodeMirror YAML editor → `compose config` validation → atomic save.
- Every save keeps the previous file as `compose.yaml.bak`.
- Optimistic concurrency: the client sends the content hash it loaded; if the file on disk differs, the save is rejected with a reload prompt.

### 5.4 Live status
Engine events → in-memory state cache → one SSE stream to the UI. No polling.

### 5.5 Logs
Engine logs API (follow + tail N) streamed over SSE, filterable per service, searchable in-browser. Not persisted.

### 5.6 Metrics
Sampler → batched SQLite writes → hourly prune. Charts load history via REST, then append live SSE samples.

## 6. Error handling

- **Docker unavailable:** Stackstead stays up, UI shows a degraded banner; engine reconnects with backoff and resyncs full state after reconnect. Missing `docker compose` plugin disables lifecycle actions; read-only views still work.
- **Compose operations:** non-zero exit surfaces as a failed operation with full captured output retained. Concurrent operation on the same stack returns `409 Conflict`. Operations time out (`operation_timeout`) and are cancellable (SIGTERM, then SIGKILL after a grace period). No automatic retries.
- **File integrity:** atomic writes (temp file in same dir → fsync → rename). Missing or unreadable external file marks the stack "unavailable" without unregistering it. Unparseable YAML puts the stack in raw-editor-only mode with the parse error shown.
- **Validation:** structured field errors `{field, message}` for forms; `compose config` stderr passed through verbatim for raw edits.
- **Metrics:** per-container sampling failures are logged and skipped. SQLite write failures (e.g., disk full) drop samples and raise a UI warning; stack management is unaffected.
- **Auth:** per-IP login rate limiting. If no admin exists, first-run setup is allowed only from localhost or with a one-time setup token printed to the journal.
- **Shutdown:** on SIGTERM, stop accepting requests, wait for in-flight compose operations up to a grace period, flush metrics. Containers are never stopped by Stackstead's shutdown.

## 7. Security notes

- Access to Stackstead is equivalent to root on the host (Docker socket). Default bind is localhost; exposing it requires an explicit `listen` change.
- File browser and import paths are resolved (including symlinks) and must fall within `allowed_roots` for browsing; import of external compose paths is allowed anywhere the `stackstead` user can read, and is an explicit admin action.
- `/metrics` uses a separate bearer token so Prometheus scraping does not need the admin session.

## 8. Testing strategy

Test-first development; tests assert observable behavior.

**Unit (no Docker):**
- `composefile` golden-file tests: input YAML with comments, unusual ordering, and unknown keys → edit → byte-exact expected output.
- Validation: port conflicts, IP-in-subnet, name rules, `allowed_roots` enforcement including `../` traversal, symlink escape, and absolute-path escape.
- `auth`: hashing, session expiry, rate limiting, first-run gating.
- `metrics`: retention pruning; Prometheus output against fixed samples.

**Integration (real Docker, `//go:build integration`, tiny images like `busybox`/`alpine`):**
- Full lifecycle: create from test Dockerfile → builds and runs → edit memory limit → apply → container recreated with new limit → teardown.
- Import external compose file → detected with running containers → file byte-identical afterward.
- Cancel a long-running build → subprocess cleaned up.
- Docker reconnect behavior where feasible.

**API:** `httptest` against the real router; fake engine only where Docker is irrelevant. Covers auth on every route, `409` on concurrent operations, structured validation errors.

**Frontend:** Vitest for non-trivial logic (form state/validation helpers, chart data merging). No UI E2E in v1.

**CI (later):** `go test ./...`, `go vet`, `golangci-lint`; separate integration job on a Docker-enabled runner; cross-compile check for linux/amd64 and linux/arm64.
