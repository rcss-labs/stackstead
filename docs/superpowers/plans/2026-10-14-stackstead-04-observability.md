# Stackstead Plan 4 — Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** this plan specifies *what* to build: files, signatures, behaviour, test cases and acceptance criteria. It deliberately contains no full implementations; implementers write the code test-first. Short snippets show shapes only.

**Goal:** Users see what their stacks are doing without polling Docker.
- Live container status is driven by Docker events.
- Logs are streamed per stack or service.
- CPU, memory, network and block-I/O metrics are kept for a rolling window, with a live feed.
- A Prometheus `/metrics` endpoint lets people with Grafana go further.

**Architecture:**
- `internal/engine` gains three streaming calls: events, one-shot stats, and logs with stream demultiplexing.
- `internal/state` is a status cache fed by the events stream, with reconnect, backoff and full resync. It replaces the per-request `engine.Containers` call in `stacks.Service.describe` and powers a live status SSE feed.
- `internal/metrics` holds:
  - the sampler: every `metrics_interval`, bounded concurrency;
  - the SQLite sample store with batched writes and retention pruning;
  - a live fan-out;
  - Prometheus text exposition.
- `internal/api` gains endpoints for logs, metrics history and live data, status events, global running operations, and `/metrics` with a bearer token. The SSE plumbing is extracted into a reusable writer first.

**Tech Stack:** Go 1.26, stdlib only (no new modules), SQLite (existing store), Docker Engine API over the unix socket (existing in-house client).

**Spec:** `docs/superpowers/specs/2026-10-14-stackstead-v1-design.md`:
- §4 engine/metrics components
- §5.4 live status
- §5.5 logs
- §5.6 metrics
- §6 error handling (Docker unavailable, metrics degrade first)

Also uses the Plan 3 final-review recommendations: a reusable SSE writer with `id:`, a global running-operations endpoint, the event cache replacing per-request container listing, and a cached status flag.

## Decisions (controller, recorded for the user)

- **Logs are never stored.** They are read on demand from Docker's log driver (spec §5.5). A stack's log stream merges every container of the project, prefixing each line with its service name.
- **Metrics samples** go into SQLite, one row per container per tick. The default 72h retention at 10s is about 26k rows per container. Pruning runs hourly. If disk or the database fails, the sampler drops samples and sets a `metricsDegraded` flag that `/api/status` reports. Stack management is never affected.
- **The Prometheus token** is shown once, when it is created or rotated. Only its SHA-256 is stored. With no token configured, `/metrics` returns 404, so it is not exposed by default.
- **CPU %** uses Docker's own formula from one-shot stats (`stream=false` returns `precpu_stats`): (cpu_delta / system_delta) × online_cpus × 100.

## Global Constraints

- No new module dependencies. Must build with `CGO_ENABLED=0` for `linux/amd64` and `linux/arm64`.
- Spec §6:
  - Docker unavailable: Stackstead stays up; the engine layer reconnects with backoff, the events subscription resumes, and a full state resync follows.
  - Sampler failures for one container are logged and skipped, never fatal.
  - SQLite write failures drop samples and raise a UI warning; stacks keep working.
- Spec §5.6: the sampler collects CPU %, memory and limit, net RX/TX and block I/O per container every `metrics_interval`; it writes to SQLite in batches; an hourly job prunes anything past `metrics_retention`. History comes over REST, live samples over SSE. `/metrics` exposes current values in Prometheus format, protected by a separate bearer token.
- **Streams always end when the request ends,** with no goroutine leaks. Every long-lived Docker stream is bounded by the request or the service context, with no client-wide timeout on streams.
- The store has ONE DB connection, so never hold a transaction while running another query. Migrations are append-only (next: migration 4).
- JSON errors keep `{"error": ...}`. Existing error mapping is reused. All new routes require a session, except `/metrics`, which uses the bearer token instead.
- **Tests:**
  - test-first;
  - unit tests never need Docker (a fake Engine API on a unix socket, as in Plan 3);
  - real-Docker tests go behind `//go:build integration`, with unique names and cleanup registered before creation;
  - never touch the user's pre-existing containers (e.g. `packntag-*`);
  - fuzzing uses `-parallel 2` and ≤60s;
  - every `go test` gets `-timeout`.
- Commits: conventional, explain why, end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **Docker restarts while Stackstead runs.** The status cache must reconnect, resync and converge to the truth: no stale "running" after containers died while disconnected, and no duplicate subscriptions. (T5)
2. **A slow or stalled browser on a logs or metrics stream** must not block the sampler, the events consumer or other clients, and must not leak goroutines or Docker connections. (T3, T7)
3. **Logs from TTY and non-TTY containers** are both decoded correctly. Multiplexed frames are split, partial lines are buffered across reads, and over-long lines are capped. (T1)
4. **Disk full or DB errors** in the sampler degrade only metrics, and `/api/status` says so. (T6)
5. **`/metrics` without, or with a wrong, bearer token** returns 401 (or 404 when no token is configured), never data, and compares tokens in constant time. (T7)

---

## File Structure

```
internal/engine/stream.go          Events, Stats, Logs (+ demux) on the existing Client
internal/engine/stream_test.go
internal/store/store.go            + migration 4: metric_samples, settings
internal/metrics/store.go          Store: InsertBatch, Query, Latest, Prune; token hash get/set
internal/metrics/prom.go           WriteExposition (text format 0.0.4)
internal/metrics/sampler.go        Sampler: tick, collect (bounded), compute, batch write, prune, fan-out, degraded flag
internal/state/cache.go            Cache: events consumer, resync, backoff, Snapshot, Subscribe
internal/api/sse.go                reusable sseWriter (headers, deadlines, heartbeat, event+id) — handleStreamOperation refactored onto it
internal/api/observe.go            logs, metrics, status-events, running-ops, /metrics, token endpoints
internal/stacks/service.go         describe uses a ContainerSource (the cache) instead of Engine.Containers
cmd/stackstead/main.go             wiring
internal/metrics/integration_test.go, internal/state/integration_test.go  //go:build integration
```

## Execution waves

| Wave | Tasks (parallel) | Depends on |
|---|---|---|
| 1 | T0 Plan-3 carry-overs, T1 engine streams, T2 metrics store + migration, T3 SSE writer refactor, T4 Prometheus exposition | nothing (disjoint files; T0 touches stacks/composefile/composecli/cmd only; T2 and T4 share package `metrics` but not files or symbols) |
| 2 | T5 status cache, T6 sampler | T5: T0, T1. T6: T1, T2 |
| 3 | T7 API endpoints | T3, T4, T5, T6 |
| 4 | T8 wiring + integration + README | all |

Symbol ownership in `metrics`: T2 owns `Store`, `Sample`, `Point` and the `ErrNo*` errors. T4 owns `WriteExposition`, `ExpositionSample`. T6 owns `Sampler` and `Live`.

---

### Task 0: Plan 3 carry-overs (parked minors from the Plan 3 final re-review)

**Files:** `internal/stacks/service.go`, `internal/stacks/service_edit.go`, `internal/stacks/versioncache.go` (+ tests), `internal/composefile/file.go` (+ test), `internal/composecli/runner_test.go`, `README.md`.

**Behaviour / tests (each test-first):**
1. **Act and Remove take the stack lock** (`lockedEntry`: stack lock, then projectMu, the same order as `commit`) around readFile, refreshProject and Ops.Start. This removes the race where a concurrent edit's project claim is reverted.
   - Test: an edit renaming the project runs concurrently with Act (use a hook). The final registry project equals the file's project.
2. **The version cache never holds its mutex across the subprocess.** Use single-flight, and give the probe its own timeout of 3s. Waiters honour their own ctx.
   - Tests: a hung `docker compose version` doesn't block a second caller beyond its ctx, and `/api/status` stays bounded.
3. **`composefile.LocalNetworks` decodes `external` as a YAML bool,** so `True`, `true` and `yes` (whatever yaml.v3 resolves as bool true) all count as external.
   - Table test.
4. **`revertProject` logs a failed revert at error level** whatever error is returned to the caller. Inject the logger via Deps if not already present.
   - Test with a capturing slog handler.
5. **composecli `TestRunWithCancelledContextDoesNotStart`:** set `r.Grace = 5*time.Second` in that test, and fix the misleading comment (the runner uses exec.Command plus its own group-SIGTERM goroutine, not CommandContext).
6. **README:** scope the "POST requires JSON body" note to the stacks/operations endpoints (logout takes no body).

**Acceptance:** `go test -race -timeout 180s ./internal/stacks/ ./internal/composefile/ ./internal/composecli/ ./internal/api/` green.

---

### Task 1: Engine streaming calls

**Files:** create `internal/engine/stream.go`, `internal/engine/stream_test.go` (+ testdata JSON).

**Interfaces:**
```go
type Event struct { Type, Action, ContainerID string; Labels map[string]string; Time time.Time } // Type "container"
func (c *Client) Events(ctx context.Context, since time.Time) (<-chan Event, <-chan error) // GET /events?since=&filters={"type":["container"]}; channels close when the stream ends; one error then close
type Stats struct {
    Read time.Time
    CPUPercent float64          // Docker formula from cpu_stats/precpu_stats; 0 when deltas are 0
    MemUsage, MemLimit uint64   // usage minus inactive_file (cgroup v2) / cache (v1) when present
    NetRx, NetTx uint64         // summed over interfaces
    BlkRead, BlkWrite uint64    // summed io_service_bytes_recursive Read/Write (case-insensitive op)
}
func (c *Client) Stats(ctx context.Context, containerID string) (Stats, error) // GET /containers/{id}/stats?stream=false&one-shot=false
type LogLine struct { Stream string /* "stdout"|"stderr" */; Time time.Time; Text string }
func (c *Client) Logs(ctx context.Context, containerID string, opts LogOptions) (<-chan LogLine, <-chan error)
type LogOptions struct { Follow bool; Tail int /* -1 = all */; Since time.Time; TTY bool }
```

**Behaviour:**
- **Streams** (Events, Logs with Follow) use the same unix-socket transport and API-version negotiation, but have no per-request timeout. They are bound only by ctx. The client's timeout still bounds connection setup and the response headers.
- **Events** decode a JSON object stream. Unknown types are skipped. Ending: ctx cancel → channels close with no error; the daemon closing the stream → `io.EOF` delivered as an error, wrapped with ErrUnavailable.
- **Logs:**
  - Request `timestamps=1`.
  - When `!TTY`, demultiplex the 8-byte frame header (stream byte, then a big-endian uint32 length) and handle frames split across reads. When TTY, the stream is raw.
  - Split into lines on `\n`, strip `\r`, cap each line at 16 KiB on a UTF-8 boundary, parse the RFC3339Nano timestamp prefix, and keep a partial final line until more data arrives or the stream ends (then flush it).
- **Stats:** handle cgroup v1 and v2 shapes and missing fields (stopped container → error from Docker; a missing `networks` key gives 0).
- **Callers' cancellation is not ErrUnavailable,** consistent with Plan 3's classify().

**Tests (write first),** with the fake API on a unix socket:
- Events: a stream of 3 events, then an EOF error; ctx cancel closes without error; unknown types are skipped.
- Logs:
  - multiplexed frames split at every byte boundary (table over split points);
  - a TTY raw stream;
  - a partial last line flushed at EOF;
  - an over-long line capped;
  - timestamps parsed;
  - stderr vs stdout tagged.
- Stats: CPU% from a canned v2 payload (assert the computed value); v1 payload with `cache`; zero deltas → 0; network sum across 2 interfaces; blkio case-insensitive.
- Streams have no client timeout: a fake that dribbles one event per 100ms for longer than the client timeout still delivers all events.
- Goroutines return after ctx cancel (count check).

**Acceptance:** `go test -race -timeout 120s ./internal/engine/` green.

---

### Task 2: Metrics store and migration 4

**Files:** modify `internal/store/store.go` (+ test); create `internal/metrics/store.go`, `internal/metrics/store_test.go`.

**Schema (migration 4, appended):**
```sql
CREATE TABLE metric_samples (
  ts INTEGER NOT NULL,              -- unix seconds
  project TEXT NOT NULL, service TEXT NOT NULL, container TEXT NOT NULL,
  cpu REAL NOT NULL, mem INTEGER NOT NULL, mem_limit INTEGER NOT NULL,
  net_rx INTEGER NOT NULL, net_tx INTEGER NOT NULL, blk_read INTEGER NOT NULL, blk_write INTEGER NOT NULL
);
CREATE INDEX metric_samples_project_ts ON metric_samples(project, ts);
CREATE TABLE settings (key TEXT NOT NULL PRIMARY KEY, value TEXT NOT NULL);
```

**Interfaces:**
```go
type Sample struct { TS time.Time; Project, Service, Container string; CPU float64; Mem, MemLimit, NetRx, NetTx, BlkRead, BlkWrite uint64 }
type Store struct{ /* db, now */ }
func NewStore(db *sql.DB, now func() time.Time) *Store
func (s *Store) InsertBatch(ctx, []Sample) error              // one transaction, prepared statement
func (s *Store) Query(ctx, project, service string, from, to time.Time, step time.Duration) ([]Point, error)
type Point struct { TS time.Time; Service string; CPU float64; Mem, MemLimit, NetRx, NetTx, BlkRead, BlkWrite uint64 } // averaged per (service, bucket); counters use max within bucket
func (s *Store) Prune(ctx, olderThan time.Time) (int64, error)
func (s *Store) TokenHash(ctx) (string, error)                 // "" when unset
func (s *Store) SetTokenHash(ctx, hash string) error           // "" clears
```

**Behaviour:**
- Query groups by service and `step` bucket, so a 72h chart at 2-minute steps returns ~2160 points per service rather than 26k. `step <= 0` returns raw rows.
- Results are ordered by service, then ts.
- An empty `service` filter means all services.
- Counters (net, blk) are cumulative values from Docker. Storing them raw is fine; the API computes rates in T7.

**Tests (write first):**
- Migration from a v3 DB keeps the stacks rows.
- An InsertBatch of 1000 rows is one transaction (fast; check the row count).
- Query bucketing: averages for gauges, max for counters; the service filter; the time window bounds are inclusive and exclusive as documented.
- Prune removes only older rows and returns the count.
- Token hash get/set/clear.

**Acceptance:** `go test -race -timeout 120s ./internal/store/ ./internal/metrics/` green.

---

### Task 3: Reusable SSE writer (refactor)

**Files:** modify `internal/api/sse.go`, `internal/api/stacks.go` (stream handler), and the tests.

**Interfaces (package api, unexported):**
```go
type sseWriter struct { /* w, rc, writeTimeout */ }
func startSSE(w http.ResponseWriter, writeTimeout time.Duration) *sseWriter // sets headers, 200, ": stream open"
func (s *sseWriter) Event(name string, id string, data any) error           // data JSON-encoded; id omitted when ""
func (s *sseWriter) Comment(text string) error
// loop helper:
func (s *server) runSSE(r *http.Request, sw *sseWriter, step func() (stop bool, err error), wake <-chan struct{}) // or similar: heartbeat + Closing + ctx handling in one place
```

**Behaviour:**
- `handleStreamOperation` keeps its exact external contract: the same events, payloads and termination. In addition, every `line` event carries `id: <line index>`, and a `Last-Event-ID` request header resumes after that index instead of replaying everything. The resume check must hold atomically with the subscription (replay is sliced from the index).
- The heartbeat, `Options.Closing`, the request ctx and per-write deadlines are handled in ONE helper that T7's streams reuse.

**Tests:**
- All existing SSE tests stay green.
- New: `Last-Event-ID: 2` against an op with lines 0..4 delivers only 3 and 4, then done. Each line event has a matching `id:`. A malformed Last-Event-ID falls back to a full replay.

**Acceptance:** `go test -race -timeout 180s ./internal/api/` green; no behaviour change other than ids and resume.

---

### Task 4: Prometheus exposition

**Files:** create `internal/metrics/prom.go`, `internal/metrics/prom_test.go`.

**Interfaces:**
```go
type ExpositionSample struct { Project, Service, Container string; CPU float64; Mem, MemLimit, NetRx, NetTx, BlkRead, BlkWrite uint64 }
func WriteExposition(w io.Writer, samples []ExpositionSample, degraded bool) error
```

**Behaviour:** text format 0.0.4, with `# HELP` and `# TYPE` for each family:
- gauges: `stackstead_container_cpu_percent`, `stackstead_container_memory_bytes`, `stackstead_container_memory_limit_bytes`;
- counters: `stackstead_container_network_receive_bytes_total`, `…_transmit_bytes_total`, `stackstead_container_blkio_read_bytes_total`, `…_write_bytes_total`;
- `stackstead_metrics_degraded` 0/1.

Labels are `project`, `service` and `container`, with values escaped per spec (`\\`, `\"`, `\n`). Output is deterministic, sorted by labels.

**Tests (write first):**
- A golden output for 2 samples.
- Label escaping.
- Empty input still emits HELP/TYPE and the degraded gauge.
- The output parses line by line with a tiny in-test validator: metric name regex, label syntax, float value.

**Acceptance:** tests green.

---

### Task 5: Status cache (events-driven)

**Files:** create `internal/state/cache.go`, `internal/state/cache_test.go`; modify `internal/stacks/service.go` (describe takes containers from a `ContainerSource`) + tests.

**Interfaces:**
```go
type Source interface { // satisfied by *engine.Client
    Containers(ctx) ([]engine.Container, error)
    Events(ctx, since time.Time) (<-chan engine.Event, <-chan error)
}
type Cache struct{ /* … */ }
func New(src Source, logger *slog.Logger, backoff Backoff) *Cache
func (c *Cache) Run(ctx context.Context)                       // blocks until ctx done
func (c *Cache) Containers(ctx context.Context) ([]engine.Container, error) // snapshot; ErrUnavailable-wrapped when never synced or currently disconnected and stale
func (c *Cache) Connected() bool
func (c *Cache) Subscribe() (changes <-chan Change, cancel func())   // non-blocking fan-out; slow subscribers dropped (closed)
type Change struct { Project string; Seq uint64 }               // "something in this project changed"; clients refetch
type Backoff struct { Min, Max time.Duration }                  // exponential with jitter; default 500ms..30s
// in stacks:
type ContainerSource interface { Containers(ctx) ([]engine.Container, error) }
```

**Behaviour:**
- **Run loop:**
  1. Subscribe to events from `now`.
  2. Then do a full resync via Containers. Subscribing first ensures no gap.
  3. Apply each event by refetching the full list, debounced to 250ms. This is simpler and always correct; Docker events carry too little state to patch reliably.
  4. Publish a Change for each affected project, including projects that disappeared.
- **On stream error:** mark disconnected, back off, and repeat from step 1. Reconnecting always does a full resync.
- **Containers while disconnected:** return an ErrUnavailable-wrapped error rather than the stale snapshot, so stacks reports status `unknown` (truthful).
- **Service.describe** uses the cache instead of Engine.Containers (Plan 3 behaviour is otherwise unchanged). Validation paths that need fresh data (Create port checks) keep calling the engine directly.

**Tests (write first)** with a fake Source:
- Initial resync publishes the snapshot.
- An event triggers a refetch, and a Change is published for the right project.
- Event bursts are debounced (10 events within 100ms → ≤2 refetches).
- Stream error → disconnected → Containers returns unavailable → reconnect after backoff → resync → connected.
- A container that died while disconnected is not running after the resync.
- A slow subscriber is dropped and does not block.
- Run returns on ctx cancel, with no goroutine leaks (count).
- In stacks: status from the cache; cache unavailable → `unknown`.

**Acceptance:** `go test -race -timeout 120s ./internal/state/ ./internal/stacks/` green.

---

### Task 6: Sampler

**Files:** create `internal/metrics/sampler.go`, `internal/metrics/sampler_test.go`.

**Interfaces:**
```go
type StatsSource interface { Stats(ctx, id string) (engine.Stats, error) }
type ContainerLister interface { Containers(ctx) ([]engine.Container, error) } // the state cache
type Sampler struct{ /* … */ }
func NewSampler(st *Store, lister ContainerLister, stats StatsSource, interval, retention time.Duration, logger *slog.Logger, now func() time.Time) *Sampler
func (s *Sampler) Run(ctx context.Context)
func (s *Sampler) Degraded() bool
func (s *Sampler) Latest() []ExpositionSample                 // last tick's values (for /metrics)
func (s *Sampler) Subscribe(project string) (<-chan Sample, func()) // live fan-out per project; slow subscribers dropped
```

**Behaviour:**
- **Each tick:**
  1. List running containers that have a compose project label; non-compose containers are skipped in v1.
  2. Fetch Stats with concurrency ≤ 4 and a per-call timeout of `min(interval, 5s)`.
  3. Skip and log containers that fail; never abort the tick.
- **Writes:** one `InsertBatch` per tick. On a write error: `Degraded()=true`, log at most once per minute, drop the batch. On the next successful write, `Degraded()=false`.
- **Prune** hourly, plus once at start.
- **Overlap:** ticks never overlap. If a tick overruns the interval, skip the missed ticks and don't queue them.
- **Fan-out:** live subscribers get the tick's samples for their project.

**Tests (write first)** with fakes and a fake clock or manual tick trigger:
- One tick writes N samples.
- A failing container is skipped while the others are written.
- Concurrency never exceeds 4 (counter in the fake).
- An insert failure → Degraded true, then recovery → false.
- Prune is called hourly.
- No overlapping ticks when the stats fetch is slow.
- A slow live subscriber is dropped.
- Run returns on cancel with no leaks.

**Acceptance:** `go test -race -timeout 120s ./internal/metrics/` green.

---

### Task 7: Observability API

**Files:** create `internal/api/observe.go`, `internal/api/observe_test.go`; modify `internal/api/server.go` (Options gains `Observe ObserveAPI`, an interface over the cache, sampler, store and log source; nil disables the routes) and `/api/status`.

**Routes:**

| Method & path | Auth | Response |
|---|---|---|
| `GET /api/stacks/{name}/logs?service=&tail=200&since=&follow=1` | session | SSE `event: log` data `{service, container, stream, time, text}`; ends with `event: end` when not following or when all containers' streams end |
| `GET /api/stacks/{name}/metrics?service=&from=&to=&step=` | session | `{points: [...]}`; counters converted to per-second rates between consecutive points (first point rate omitted); default window last 1h, default step chosen to give ≤ 720 points |
| `GET /api/stacks/{name}/metrics/live` | session | SSE `event: sample` per tick for that project |
| `GET /api/events` | session | SSE `event: change` data `{project, seq}` from the status cache; `event: status` when connected/degraded flags change |
| `GET /api/operations?state=running` | session | `[Operation]` across all stacks |
| `GET /metrics` | **bearer token** | Prometheus text; 404 when no token configured; 401 on missing/wrong token (constant-time compare of SHA-256) |
| `POST /api/settings/metrics-token` | session | `{token}`: creates or rotates, shown once; stores only the hash |
| `DELETE /api/settings/metrics-token` | session | 204, disables `/metrics` |
| `GET /api/status` | session | adds `"events": "connected\|disconnected"`, `"metricsDegraded": bool` |

**Behaviour:**
- **Logs:**
  - Resolve the stack's containers from the cache (project label), optionally filtered by service.
  - Open one engine Logs stream per container (with the TTY flag from container inspect, or from the cache if T5 stores it; otherwise assume non-TTY and fall back if the first frame header is invalid).
  - Merge the streams into the SSE writer.
  - A stack with no containers → `event: end` immediately.
  - Cap the number of containers per stream at 50.
  - Closing the request cancels all Docker streams.
- All streams use T3's helper (heartbeat, Closing, write deadlines).
- Unknown stack → 404, the same as Plan 3. Engine down on logs → 503.

**Tests (write first)** with fakes:
- Auth on every new route (except `/metrics`, which uses the token).
- Logs: two containers merged, the service filter, non-follow ends with `end`, client disconnect cancels the fake streams (assert their ctx is done), 404 and 503 mapping.
- Metrics: window and step defaults, rate conversion (counter 100→160 over 10s = 6/s), the first point has no rates.
- Live samples stream.
- `/api/events` change and status events.
- Running operations filter.
- `/metrics`:
  - 404 with no token;
  - 401 with a missing or wrong token;
  - 200 with the right token;
  - the body contains the degraded gauge.
- Token create returns a token once; a second create rotates (the old token now gets 401); delete → 404.

**Acceptance:** `go test -race -timeout 240s ./internal/api/` green.

---

### Task 8: Wiring, integration tests, README

**Files:** modify `cmd/stackstead/main.go`, `README.md`; create `internal/state/integration_test.go`, `internal/metrics/integration_test.go` (`//go:build integration`).

**Behaviour:**
- **Startup:**
  1. Start `state.Cache.Run` in a goroutine.
  2. Start the `Sampler.Run`.
  3. Pass the cache as stacks' ContainerSource and the observe dependencies to `api.Options`.
- **Shutdown order:**
  1. HTTP shutdown (Closing ends streams).
  2. ops.Shutdown.
  3. Cancel the sampler and cache context and wait for them to return (bounded by ShutdownGrace).
  4. DB close.
- **README:** the observability endpoints, the `/metrics` token workflow (example Prometheus `scrape_config` with `authorization: credentials`), what is stored and for how long, and that logs are not stored.

**Integration tests** (real Docker; skip if unavailable; unique names; cleanup registered first; never touch pre-existing containers):
- `TestCacheSeesStartStop`: `docker run -d busybox sleep 60` with a compose project label → the cache reports it within 2s; `docker stop` → not running within 2s.
- `TestLogsStream`: a container printing 3 lines to stdout and 1 to stderr → engine Logs returns all 4 with the correct streams, in both TTY and non-TTY variants.
- `TestSamplerWritesSamples`: run the sampler with a 1s interval for ~3s against a labelled busybox → ≥2 rows for that container in a temp DB; CPU and mem are plausible (mem > 0).
- Run them once. Afterwards `docker ps -a` and `docker network ls` show only pre-existing resources.

**Acceptance:**
- `go build ./... && go vet ./... && test -z "$(gofmt -l .)" && go test -race ./... && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` green.
- One integration run green (both new suites plus Plan 3's).
- Manual smoke test with the real binary:
  1. create and start an nginx:alpine stack;
  2. stream its logs while curling it;
  3. see metrics history after 30s;
  4. rotate the metrics token and scrape `/metrics` with curl;
  5. stop and clean up.
