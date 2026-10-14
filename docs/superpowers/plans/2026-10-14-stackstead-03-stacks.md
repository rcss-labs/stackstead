# Stackstead Plan 3 — Stacks & Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** this plan specifies *what* to build: files, signatures, behaviour, test cases and acceptance criteria. It deliberately contains no full implementations; implementers write the code test-first. Short snippets show shapes only.

**Goal:** The first usable Stackstead. Through the JSON API a user can create a stack from a Dockerfile/image form, import an existing compose file, edit services via forms or raw YAML (validated by `docker compose config`), run up/down/restart/pull/build with live streamed output, and remove stacks. Ports and static IPs are validated before anything is written.

**Architecture:** New packages, each with one job:
- `internal/fsguard`: path confinement to `allowed_roots` and directory listing.
- `internal/composecli`: the only code that executes `docker compose`.
- `internal/engine`: a minimal Docker Engine API client over the unix socket. It is our own code, with no SDK.
- `internal/stacks`: the registry, validation, the operations manager, and the `Service` that orchestrates them.

`internal/api` gains the stack endpoints and SSE. Compose files stay the source of truth. The DB stores only registry rows.

**Tech Stack:** Go 1.26, stdlib `net/http` (unix-socket transport), `os/exec`, existing `internal/composefile`, `internal/store`, SQLite. No new module dependencies.

**Spec:** `docs/superpowers/specs/2026-10-14-stackstead-v1-design.md` (§4 components, §5.1–5.3 data flows, §6 error handling, §7 security, §8 testing).

## Decisions taken with the user (2026-10-14)

- **Docker access:** a small in-house client (`internal/engine`) speaks the Engine HTTP API over `/var/run/docker.sock` and models only the fields we read. The Docker SDK is not used (dependency weight).
- **Remove stack:** removal runs `docker compose down` (volumes only when explicitly requested) and then unregisters the stack. **Files on disk are never deleted** in v1, for managed stacks too.
- **Project names / no lock-in:** Stackstead never passes `-p` to compose. It lets compose derive the project name exactly as a manual `docker compose up` in that directory would (top-level `name:` or directory name), and records that name in the registry. Containers map to stacks via the `com.docker.compose.project` label.

## Global Constraints

- No new module dependencies. Must build with `CGO_ENABLED=0` for `linux/amd64` and `linux/arm64`.
- Spec: "`composecli` is the only place that runs `docker compose`". Output streams line by line. Only one operation runs per stack at a time; a second request gets `409`.
- Spec: operations have a timeout (`operation_timeout`, default `15m`) and are cancellable (SIGTERM, then SIGKILL after a grace period). There are no automatic retries. A non-zero exit is a failed operation, and its full output is retained.
- Spec: Docker unavailable → Stackstead stays up, the API reports degraded status, and lifecycle endpoints return `503`. A missing compose plugin → lifecycle endpoints `503`, read-only views work.
- Spec §5.1: before anything is written, validate a unique name, host-port conflicts (against running containers *and* other stacks), and a static IP inside the chosen network's subnet that is not already in use.
- Spec §5.1: a new stack from a Dockerfile gets `build: {context, dockerfile}` and `image: stackstead/<name>:latest`. It is validated with `docker compose config`. On failure, everything this request created is rolled back.
- Spec §5.3: every save is validated with `docker compose config` *before* it replaces the file. Saves use `composefile.Save` with the hash the client loaded (`409` on mismatch). Raw edits keep `.bak`.
- Spec §7: the file browser only lists inside `allowed_roots` (symlinks resolved). Import of an external compose path is allowed anywhere the service user can read. Paths are stored resolved (`EvalSymlinks`).
- JSON errors keep the existing shape `{"error": "..."}`. Validation failures add `"fields": [{"field": "...", "message": "..."}]`.
- composefile error → HTTP: `ErrServiceNotFound` 404; `ErrConflict` 409; `ErrInvalidConfig`, `*ReadOnlyFieldError`, `ErrNotMapping`, `ErrMultipleDocuments` 422; `ErrEditRefused` 422 with message "this change needs the raw YAML editor"; anything else 500.
- Test-first for every behaviour. Unit tests never need Docker: they use a fake `docker` executable for `composecli` and a fake Engine API on a unix socket for `engine`. Real-Docker tests live behind `//go:build integration`.
- Tests that fuzz use `-parallel 2` and ≤60s (the developer's laptop must stay responsive).
- Commits: conventional, explain why, end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **An imported stack must behave exactly as if the user ran `docker compose` in its directory.** Stackstead must use the same project name and the same containers, and must not create a duplicate project. (Tests: Task 3 `TestProjectNameMatchesCompose`; Task 9 integration `TestImportAdoptsRunningProject`.)
2. **A failed create leaves no trace.** Invalid compose, a port conflict or an IP conflict means no folder, no file and no registry row. A folder that already existed must never be deleted by rollback. (Task 6.)
3. **Cancel, timeout and server shutdown never leave orphan `docker compose` processes.** The whole process group is signalled. (Task 3 `TestCancelKillsProcessGroup`, Task 5.)
4. **Path confinement:** `../`, symlinks pointing outside, `/opt/stacks2` vs `/opt/stacks` prefix tricks and nonexistent paths are all refused by the file browser. (Task 2.)
5. **Docker down or slow:** listing stacks still works (status `unknown`), and lifecycle endpoints return `503`, not a hang. Engine calls have a short timeout. (Tasks 4, 7, 8.)

---

## File Structure

```
internal/store/store.go              + migration 2: stacks table
internal/config/config.go            + docker_socket (default /var/run/docker.sock), shutdown_grace (default 30s)
internal/fsguard/fsguard.go          Resolve, List — confinement to allowed roots
internal/composecli/runner.go        Runner: Version, Config, ProjectName, Run (stream, cancel, process group)
internal/composecli/proc_unix.go     process-group setup / kill (build tag unix)
internal/engine/client.go            unix-socket HTTP client, Ping, Containers, Networks, ErrUnavailable
internal/engine/types.go             Container, Port, Network, Subnet (only fields we use)
internal/stacks/registry.go          DB layer: Add, Get, List, Remove, SetProject
internal/stacks/validate.go          ValidateName, ParsePort, PortConflicts, CheckStaticIP, FieldError
internal/stacks/ops.go               Operations manager: per-stack lock, output buffer, subscribe, cancel
internal/stacks/service.go           Service: Sync, List, Get, Create, Import, UpdateService, SaveRaw, Act, Remove
internal/api/stacks.go               stack + operation + fs + networks handlers
internal/api/sse.go                  SSE writer (events, heartbeat)
cmd/stackstead/main.go               wiring, startup checks, graceful shutdown of operations
internal/stacks/integration_test.go  //go:build integration — real Docker
```

## Execution waves

| Wave | Tasks (parallel within a wave) | Depends on |
|---|---|---|
| 1 | T1 registry+config, T2 fsguard, T3 composecli, T4 engine, T5 ops manager | nothing (disjoint packages; T1 touches store+config only) |
| 2 | T6 validation | T4 types |
| 3 | T7 stacks.Service | T1–T6 |
| 4 | T8 API | T7 |
| 5 | T9 wiring + integration tests | T8 |

Symbol ownership across parallel tasks: T5 owns `stacks/ops.go`, T1 owns `stacks/registry.go`, T6 owns `stacks/validate.go`. These three tasks share package `stacks` but no symbols. Error names: T1 `ErrStackExists`, `ErrStackNotFound`; T5 `ErrBusy`, `ErrOperationNotFound`; T6 `FieldError`, `ValidationError`.

---

### Task 1: Registry storage and config additions

**Files:** modify `internal/store/store.go`, `internal/config/config.go` (+ their tests); create `internal/stacks/registry.go`, `internal/stacks/registry_test.go`.

**Interfaces:**
```go
// store: append migration 2 (never edit migration 1)
// stacks(name TEXT NOT NULL PRIMARY KEY, compose_path TEXT NOT NULL UNIQUE,
//        origin TEXT NOT NULL CHECK (origin IN ('managed','external')),
//        project TEXT NOT NULL, created_at INTEGER NOT NULL)

package stacks
type Origin string // OriginManaged = "managed", OriginExternal = "external"
type Entry struct { Name, ComposePath, Project string; Origin Origin; CreatedAt time.Time }
type Registry struct{ /* db *sql.DB, now func() time.Time */ }
func NewRegistry(db *sql.DB, now func() time.Time) *Registry
func (r *Registry) Add(ctx, Entry) error          // ErrStackExists on name OR path collision
func (r *Registry) Get(ctx, name string) (Entry, error) // ErrStackNotFound
func (r *Registry) List(ctx) ([]Entry, error)     // ordered by name
func (r *Registry) Remove(ctx, name string) error // ErrStackNotFound if absent
func (r *Registry) SetProject(ctx, name, project string) error

// config: DockerSocket string `yaml:"docker_socket"` (default "/var/run/docker.sock", env STACKSTEAD_DOCKER_SOCKET, must be absolute)
//         ShutdownGrace time.Duration `yaml:"shutdown_grace"` (default 30s, env STACKSTEAD_SHUTDOWN_GRACE, ≥1s)
```

**Behaviour / tests (write first):**
- Migration: opening a v1 database upgrades it to v2 and keeps the users and sessions rows. A fresh DB reaches v2 directly. The existing `TestOpenRefusesNewerSchema` still passes.
- Registry: add→get round-trips every field. A duplicate name gives `ErrStackExists`, and so does a duplicate path under a different name (UNIQUE constraint → mapped error, not a raw SQLite error). Removing a missing stack gives `ErrStackNotFound`. List is sorted.
- Config: defaults include the two new keys. An env override works. A relative socket path or `shutdown_grace: 0s` is rejected with an error naming the key. Update `deploy/config.example.yaml` with both keys (commented).

**Acceptance:** `go test -race ./internal/store/ ./internal/config/ ./internal/stacks/` green.

---

### Task 2: fsguard — confinement and directory listing

**Files:** create `internal/fsguard/fsguard.go`, `internal/fsguard/fsguard_test.go`.

**Interfaces:**
```go
var ErrOutsideRoots = errors.New("path is outside the allowed directories")
type Entry struct { Name string; IsDir bool; Size int64 }
// Resolve cleans p, requires it absolute, resolves symlinks, and returns the
// resolved path if it lies within (or equals) one of roots (roots resolved too).
func Resolve(roots []string, p string) (string, error)
// List returns the entries of dir (after Resolve): directories first, then files,
// each group sorted by name. Entries that are symlinks resolving outside roots are omitted.
func List(roots []string, dir string) ([]Entry, error)
```

**Behaviour / tests (write first)**, using a temp tree with roots `R` and a sibling `R2`:
- `R` and `R/a/b` → ok. Relative path → error. `R/../etc` → `ErrOutsideRoots`. `R2` when the root is `R` → `ErrOutsideRoots` (prefix trick; compare with a separator boundary). Symlink `R/link → /tmp/outside` → `ErrOutsideRoots`. Symlink `R/link → R/a` → ok and resolves to `R/a`. Nonexistent path → `fs.ErrNotExist`.
- List: dirs first, sorted. A symlink to outside is omitted. A file path (not a dir) → error.
- The root itself may be a symlink (e.g. `/opt/stacks → /srv/stacks`): paths under either spelling are accepted.

**Acceptance:** package tests green; no `filepath.HasPrefix`-style string checks without a separator boundary.

---

### Task 3: composecli — the only `docker compose` caller

**Files:** create `internal/composecli/runner.go`, `internal/composecli/proc_unix.go`, `internal/composecli/runner_test.go`, `internal/composecli/testdata/fakedocker.sh` (or a script generated by the tests).

**Interfaces:**
```go
type Runner struct {
    Binary string        // default "docker"
    Grace  time.Duration // SIGTERM→SIGKILL delay, default 10s
    Env    []string      // extra env (tests point Binary at a fake)
}
var ErrComposeUnavailable = errors.New("docker compose is not available")
type ExitError struct { Code int; Output string } // non-zero exit; Output = last ~200 lines
func (e *ExitError) ExitCode() int               // lets packages read the code without importing composecli
func (r *Runner) Version(ctx) (string, error)                         // `docker compose version --short`; ErrComposeUnavailable if binary/plugin missing
func (r *Runner) Config(ctx, file string) error                        // `docker compose -f FILE config --quiet`; *ExitError carries compose's message
func (r *Runner) ProjectName(ctx, file string) (string, error)         // `docker compose -f FILE config --format json` → .name
func (r *Runner) Run(ctx, file string, args []string, out func(line string)) error
```

**Behaviour:**
- Every command runs as `docker compose -f <file> ...` with the working directory set to the file's directory. Never `-p`.
- `Run` streams stdout and stderr merged, one callback per line, in order. It returns `nil` on exit 0, `*ExitError` otherwise, and `ctx.Err()` (wrapped) if cancelled or timed out.
- Cancellation sends SIGTERM to the **process group** (`Setpgid`) and SIGKILL after `Grace`. `Run` returns only after the process has exited and all output was delivered.
- Long lines (e.g. progress bars with `\r`) do not break the scanner: treat `\r` as a line break and cap each line at 64 KiB.

**Tests (write first):** the fake `docker` script reads its arguments and env to choose behaviour (print lines, exit N, sleep, spawn a child that sleeps).
- `TestRunStreamsLinesInOrder`
- `TestRunNonZeroExitReturnsExitErrorWithOutput`
- `TestCancelKillsProcessGroup`: the fake spawns a child that sleeps 60s. After cancel, neither process exists (check via `kill -0`). The fake ignores SIGTERM in one variant, so SIGKILL after Grace is exercised.
- `TestRunArgsAndWorkingDir`: the fake echoes its args and `pwd`. Assert `compose -f <file> up -d`, cwd = the file's directory, and no `-p`.
- `TestProjectNameMatchesCompose`: the fake returns JSON `{"name":"lab"}`, giving `lab`. Integration variant in Task 9.
- `TestVersionReportsUnavailable`: a nonexistent binary gives `ErrComposeUnavailable`.
- `TestConfigErrorCarriesMessage`.

**Acceptance:** tests green with `-race`. No goroutine leaks: a test reads all output, and the `Run` goroutines exit.

---

### Task 4: engine — minimal Docker Engine API client

**Files:** create `internal/engine/client.go`, `internal/engine/types.go`, `internal/engine/client_test.go`, `internal/engine/testdata/*.json` (canned responses captured from a real Docker, trimmed).

**Interfaces:**
```go
var ErrUnavailable = errors.New("docker engine is unavailable")
type Port struct { IP string; PrivatePort, PublicPort uint16; Type string }
type Container struct {
    ID, Name, State, Status, Image string  // Name without leading "/"
    Labels map[string]string
    Ports []Port
    Networks map[string]string            // network name → IPv4 address
}
func (c Container) Project() string     // Labels["com.docker.compose.project"]
func (c Container) ComposeService() string
type Network struct { ID, Name, Driver string; Subnets []netip.Prefix; Gateways []netip.Addr }
type Client struct{ /* http.Client with unix-socket DialContext, timeout */ }
func New(socketPath string, timeout time.Duration) *Client // API base "http://docker/v1.41"
func (c *Client) Ping(ctx) error
func (c *Client) Containers(ctx) ([]Container, error)  // GET /containers/json?all=1
func (c *Client) Networks(ctx) ([]Network, error)      // GET /networks (IPv4 subnets only)
```

**Behaviour:**
- A dial or connection error, or a timeout, is wrapped with `ErrUnavailable`. A non-2xx response is an error that includes the Docker `message` field.
- Pin API version `v1.41` (Docker 20.10+) so field shapes are stable. Ignore IPv6 subnets. Skip a malformed subnet rather than failing the whole call.

**Tests (write first):** a test server on a unix socket in `t.TempDir()` (`net.Listen("unix", ...)` + `http.Serve`) serving the canned JSON.
- Containers: fields mapped, names trimmed, ports including an unpublished one (PublicPort 0), multiple networks.
- Networks: bridge with a subnet, a user network with IPAM config, host/none with no subnets, a malformed subnet skipped.
- Socket missing → `ErrUnavailable`. Server that sleeps longer than the timeout → `ErrUnavailable`. 500 with `{"message":"boom"}` → error mentioning "boom".

**Acceptance:** tests green; the client makes no request without a context deadline.

---

### Task 5: Operations manager

**Files:** create `internal/stacks/ops.go`, `internal/stacks/ops_test.go`.

**Interfaces:**
```go
type OpState string // "running", "succeeded", "failed", "cancelled", "timed_out"
type Operation struct {
    ID, Stack, Action string
    State OpState
    StartedAt, EndedAt time.Time
    ExitCode int   // -1 when not applicable
    Error string   // human summary when not succeeded
}
var ErrBusy = errors.New("another operation is running for this stack")
var ErrOperationNotFound = errors.New("operation not found")
type OpFunc func(ctx context.Context, out func(line string)) error
type Ops struct{ /* ... */ }
func NewOps(timeout time.Duration, now func() time.Time) *Ops
func (o *Ops) Start(stack, action string, fn OpFunc) (Operation, error) // ErrBusy if stack has a running op
func (o *Ops) Get(id string) (Operation, error)
func (o *Ops) Lines(id string) ([]string, error)                          // full retained output
func (o *Ops) Subscribe(id string) (replay []string, live <-chan string, done <-chan Operation, cancel func(), err error)
func (o *Ops) Cancel(id string) error                                      // no-op if finished
func (o *Ops) Recent(stack string) []Operation                             // newest first, last 20 per stack
func (o *Ops) Shutdown(ctx) // cancel all running ops, wait until they finish or ctx expires
```

**Behaviour:**
- `fn` runs in its own goroutine with a context bounded by the timeout. The state is derived from `fn`'s error:
  - nil → succeeded
  - `context.Canceled` (by Cancel or Shutdown) → cancelled
  - `DeadlineExceeded` → timed_out
  - anything else → failed; ExitCode comes from any error in the chain that implements `interface{ ExitCode() int }` (via `errors.As`). Do NOT import composecli here: it is built in parallel.
- Output is retained up to 20 000 lines per operation, dropping the oldest. The first retained line notes how many lines were dropped.
- Subscribers get the replay plus live lines without gaps or duplicates. A slow subscriber must not block the operation: drop that subscriber and close its channel rather than block.
- IDs are random (`crypto/rand`, 16 hex characters).

**Tests (write first):** a second Start on a busy stack gives `ErrBusy`, and a different stack is fine. State mapping for each outcome. Cancel → cancelled. Timeout → timed_out. Line cap with the dropped-count note. A Subscribe midway gets exactly all lines. A slow subscriber gets dropped while the op completes. Shutdown cancels and waits. Recent keeps 20 per stack. All tests run under `-race`.

**Acceptance:** tests green with `-race`; no goroutine left after Shutdown (check with a goroutine count before and after, with a small tolerance).

---

### Task 6: Validation

**Files:** create `internal/stacks/validate.go`, `internal/stacks/validate_test.go`.

**Interfaces:**
```go
type FieldError struct { Field, Message string } // Field like "name", "ports[1]", "networks.lan.ipv4_address"
type ValidationError struct { Fields []FieldError } // Error() joins messages; API returns 400 with fields
func ValidateName(name string) error // compose project-name rules: ^[a-z0-9][a-z0-9_-]{0,62}$
type HostPort struct { IP netip.Addr /* zero = all interfaces */; Port uint16; Proto string }
func ParsePorts(spec string) ([]HostPort, error) // short syntax incl. ranges, IP, /udp; "80" (no host port) → none
type PortUse struct { HostPort; Owner string } // owner = "container <name>" or "stack <name>"
func PortConflicts(field string, wanted []string, inUse []PortUse) []FieldError
func CheckStaticIP(field, network, ip string, nets []engine.Network, used map[string][]string /* network→IPs */) []FieldError
```

**Behaviour:**
- `ParsePorts`: handles `"8080:80"`, `"127.0.0.1:8080:80"`, `"8000-8002:80-82"`, `"53:53/udp"`, `"[::1]:80:80"`, `"80"`, and `"${PORT:-80}:80"`. The variable form is unresolvable, so skip it with no conflict check rather than fail. Invalid input such as `"99999:80"` or `"a:b"` gives an error.
- Two ports conflict when the protocol matches, the port matches, and the IPs overlap. A zero IP overlaps everything.
- `CheckStaticIP`:
  - the network must exist (`"network lan not found"`) and have an IPv4 subnet;
  - the IP must parse and lie in a subnet;
  - it must not be the network address, the broadcast address or the gateway;
  - it must not be in `used` (`"192.168.50.10 is already used by container web"` — pass names via the used map values).

**Tests (write first):** table tests for every syntax above. Conflict / no conflict across IPs and protocols. A range overlapping a single port. Each IP rule.

**Acceptance:** tests green; no Docker needed.

---

### Task 7: stacks.Service — the orchestrator

**Files:** create `internal/stacks/service.go`, `internal/stacks/service_test.go`.

**Interfaces (consumers: API and main):**
```go
type Compose interface { // satisfied by *composecli.Runner; tests use a fake
    Version(ctx) (string, error); Config(ctx, file string) error
    ProjectName(ctx, file string) (string, error)
    Run(ctx, file string, args []string, out func(string)) error
}
type Engine interface { Ping(ctx) error; Containers(ctx) ([]engine.Container, error); Networks(ctx) ([]engine.Network, error) }
type Deps struct { Registry *Registry; Ops *Ops; Compose Compose; Engine Engine; StacksDir string; AllowedRoots []string; Now func() time.Time }
type Status string // "running", "partial", "stopped", "unavailable" (file missing/unparseable), "unknown" (engine down)
type Summary struct { Name, Path, Project string; Origin Origin; Status Status; Services, Running int; ChecksPartial bool }
type ContainerInfo struct { ID, Name, Service, State, Status string }
type ServiceDetail struct { Name string; Config composefile.ServiceConfig; ReadOnly []composefile.Field; Containers []ContainerInfo }
type Detail struct { Summary; Hash string; Services []ServiceDetail; ParseError string }
type CreateRequest struct { Name, Service string; Config composefile.ServiceConfig } // Service defaults to Name
func NewService(d Deps) *Service
func (s *Service) Sync(ctx) error                        // adopt <StacksDir>/<name>/compose.yaml folders not yet registered
func (s *Service) List(ctx) ([]Summary, error)
func (s *Service) Get(ctx, name string) (Detail, error)
func (s *Service) Raw(ctx, name string) (content []byte, hash string, err error)
func (s *Service) Create(ctx, CreateRequest) (Summary, error)
func (s *Service) Import(ctx, path string) (Summary, error)
func (s *Service) UpdateService(ctx, stack, service string, cfg composefile.ServiceConfig, hash string) (newHash string, err error)
func (s *Service) SaveRaw(ctx, stack string, content []byte, hash string) (newHash string, err error)
func (s *Service) Act(ctx, stack, action string) (Operation, error) // up|down|restart|pull|build
func (s *Service) Remove(ctx, stack string, withVolumes bool) (Operation, error)
func (s *Service) Networks(ctx) ([]engine.Network, error)
```

**Behaviour:**
- **Create:**
  1. `ValidateName`; no registry entry or folder may already exist (`ErrStackExists`).
  2. Default `Image` to `stackstead/<name>:latest` when `Build` is set and `Image` is empty. Default `Build.Context` to `.` when only a Dockerfile is given.
  3. Validate the Build context path through `fsguard.Resolve(AllowedRoots, ...)` when it is absolute.
  4. Run the port and IP checks against the engine's containers and networks *and* the ports declared by other registered stacks. If the engine is unavailable, run only the stack-declared port checks, and report that the checks were partial: return `Summary.ChecksPartial = true`, which the API exposes as the header `X-Stackstead-Checks: partial`.
  5. `composefile.New` → mkdir `<StacksDir>/<name>` (0755) → `Save(path, data, "")` → `Compose.Config`.
  6. Run `ProjectName` and add the registry row.
  On any failure after mkdir, remove exactly the file and folder this call created, then return the error. Config failures return a `*composecli.ExitError`, which the API maps to 422 with compose's message.
- **Import:** absolute path; `EvalSymlinks`; the file must exist and `composefile.Parse`; `Compose.Config` must pass; name = `ProjectName`; unique (`ErrStackExists`); origin external. **The file is never written.**
- **UpdateService:** `Load` → hash must match (`composefile.ErrConflict`) → `Parse` → `UpdateService` → validate the ports and IPs that changed against the engine and other stacks, excluding containers of this stack's own project → check the candidate bytes with `Compose.Config` by writing them to a temp file **in the same directory** (relative paths resolve the same way, and `.stackstead-check-*.yaml` is removed afterwards) → `Save(path, new, hash)` → refresh `SetProject` if `ProjectName` changed. Return the new hash.
- **SaveRaw:** the same pipeline from `Parse(content)` onward, without the form step.
- **Act:** map the action to compose args:
  - up → `up -d --build --remove-orphans`
  - down → `down`
  - restart → `restart`
  - pull → `pull`
  - build → `build`
  Start it via `Ops.Start`. A stack that is `unavailable` gives an error.
- **Remove:** `Ops.Start(stack, "remove", ...)` runs `down` (with `--volumes` if requested). On success the registry row is deleted. On failure the row stays. Files are never touched.
- **List/Get:** compute Status from the engine containers whose `Project()` equals the entry's Project:
  - all expected services running → running
  - some → partial
  - none → stopped
  - engine error → unknown
  - missing or unparseable file → unavailable, with ParseError set
  List must not fail because one stack is broken.

**Tests (write first)** with fake Compose and Engine, temp StacksDir, and a real store:
- **Create:** the happy path writes `compose.yaml` with the image tag and registers a row. A config failure leaves no folder, file or row. A port conflict with a running container gives a ValidationError with field `ports[0]` and writes nothing. A conflict with another stack's declared port is also caught. An existing folder gives `ErrStackExists` and the folder is untouched.
- **Import:** registers with the project name from Compose, and the file bytes are unchanged. A duplicate path gives `ErrStackExists`.
- **UpdateService:** a stale hash gives `ErrConflict`. A config failure leaves the file unchanged and no temp file. Success returns a new hash and creates `.bak`. A port check ignores the stack's own containers.
- **Act:** a busy stack gives `ErrBusy`. Correct args are passed for each action.
- **Remove:** success unregisters and leaves the files present. Failure keeps the row.
- **List:** one stack with a deleted file is shown as unavailable while the others are fine. Engine down gives status unknown for all.
- **Sync:** adopts a folder with `compose.yaml` and ignores folders without one.

**Acceptance:** tests green with `-race`.

---

### Task 8: HTTP API for stacks, operations, files and networks

**Files:** create `internal/api/stacks.go`, `internal/api/sse.go`, `internal/api/stacks_test.go`; modify `internal/api/server.go` (`Options` gains `Stacks StacksAPI`, an interface over the Service methods above, so tests can fake it).

**Routes (all require a session; JSON bodies require `Content-Type: application/json`):**

| Method & path | Body / query | Success |
|---|---|---|
| `GET /api/stacks` | | 200 `[Summary]` |
| `POST /api/stacks` | `CreateRequest` | 201 `Summary` |
| `POST /api/stacks/import` | `{"path": "..."}` | 201 `Summary` |
| `GET /api/stacks/{name}` | | 200 `Detail` (`hash` included) |
| `GET /api/stacks/{name}/compose` | | 200 `{"content": "...", "hash": "..."}` |
| `PUT /api/stacks/{name}/compose` | `{"content", "hash"}` | 200 `{"hash"}` |
| `PUT /api/stacks/{name}/services/{service}` | `{"config": ServiceConfig, "hash"}` | 200 `{"hash"}` |
| `POST /api/stacks/{name}/actions/{action}` | `{}` | 202 `Operation` |
| `DELETE /api/stacks/{name}?volumes=true` | | 202 `Operation` |
| `GET /api/stacks/{name}/operations` | | 200 `[Operation]` (recent) |
| `GET /api/operations/{id}` | | 200 `Operation` + `lines` |
| `GET /api/operations/{id}/stream` | | SSE |
| `POST /api/operations/{id}/cancel` | `{}` | 202 |
| `GET /api/fs?path=/abs/dir` | | 200 `[Entry]`; empty path → list of roots |
| `GET /api/networks` | | 200 `[{name, driver, subnets}]` |
| `GET /api/status` | | 200 `{"docker": "ok\|unavailable", "compose": "<version>\|unavailable"}` |

**Behaviour:**
- Error mapping as in Global Constraints, plus:
  - `ErrStackNotFound` / `ErrOperationNotFound` → 404
  - `ErrStackExists` / `ErrBusy` → 409
  - `*ValidationError` → 400 with `fields`
  - `*composecli.ExitError` from a config check → 422 with compose's output in `error`
  - `fsguard.ErrOutsideRoots` → 403
  - `engine.ErrUnavailable` / `composecli.ErrComposeUnavailable` on lifecycle → 503
- JSON field names are lowerCamel. Add `json` tags to the response types, either in `stacks` or as API DTOs. Prefer DTOs in `api`, so `stacks` stays transport-free.
- **SSE:**
  - headers `Content-Type: text/event-stream`, `Cache-Control: no-cache`
  - each line → `event: line` and `data: <json string>`
  - completion → `event: done` and `data: <Operation json>`
  - `: ping` comment every 15s
  - stop when the client disconnects (request context)
  - flush after each event
  - write deadlines via `http.ResponseController` (the server has no WriteTimeout)
- Raw content bodies are limited to 1 MiB (existing `maxBodyBytes`).

**Tests (write first)** with a fake `StacksAPI`:
- Every route requires a session (401 without one).
- Each error maps to its status. Validation 400 includes `fields`.
- SSE: the replay, then live lines, then `done`, and the stream ends. A client disconnect stops the handler; check that the fake's cancel or unsubscribe was called.
- `/api/fs` with an empty path returns the roots, and an outside path returns 403.
- A 1 MiB+1 body returns 400.

**Acceptance:** `go test -race ./internal/api/` green.

---

### Task 9: Wiring, startup checks, shutdown, integration tests

**Files:** modify `cmd/stackstead/main.go`, `README.md`; create `internal/stacks/integration_test.go` (`//go:build integration`).

**Behaviour:**
- **Startup:**
  1. build the `engine.Client` (2s request timeout) and the `composecli.Runner`;
  2. `Ping` and `Version` → log a warning if unavailable (the service still starts);
  3. `os.MkdirAll(StacksDir, 0o755)`;
  4. `Service.Sync`;
  5. pass the Service into `api.Options`.
- **Shutdown:** stop accepting HTTP, then `Ops.Shutdown` with `ShutdownGrace`, then close the DB. Containers are never stopped.
- **README:** API overview table, Docker/compose prerequisites, and a note that removing a stack never deletes files.

**Integration tests** (real Docker; `go test -tags integration ./internal/stacks/`; each test skips if Docker is unreachable; uses `busybox`/`alpine`, a temp StacksDir and unique stack names with cleanup `down -v`):
- `TestCreateBuildUpEditDown`:
  1. create from a temp Dockerfile context (`FROM busybox`, `CMD sleep 3600`);
  2. Act up → succeeded; the container is running with the project label;
  3. UpdateService memory limit 64M → Act up → the container is recreated and the limit is visible via the engine (or `docker inspect`);
  4. Remove → the container is gone, the folder still exists.
- `TestImportAdoptsRunningProject`: write a compose file in a temp dir and run `docker compose up -d` directly (outside Stackstead). Import shows the stack as running with the same containers, and the file bytes are unchanged.
- `TestCancelBuild`: a Dockerfile with `RUN sleep 120`; Act build; cancel after 2s → cancelled, and no `docker compose` or `docker-buildx` process for that project remains after 15s.
- `TestStaticIPConflict`: create a user network with a subnet; a container uses `.10`; creating a stack wanting `.10` → ValidationError on the ip field.

**Acceptance:**
- `go build ./... && go vet ./... && test -z "$(gofmt -l .)" && go test -race ./... && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` green.
- `go test -tags integration -race ./internal/stacks/` green on a machine with Docker. The controller runs this once after the wave.
- A manual smoke test with the real binary:
  1. create a stack via curl;
  2. stream `up` over SSE;
  3. `GET /api/stacks` shows running;
  4. delete the stack;
  5. record the output in the report.
