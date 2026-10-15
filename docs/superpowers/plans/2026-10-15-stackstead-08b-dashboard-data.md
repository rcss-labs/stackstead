# Stackstead Plan 8b — UI v2 dashboard data

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** this plan specifies *what* to build: files, signatures, behaviour, test cases and acceptance criteria. It deliberately contains no full implementations; implementers write the code test-first. Short snippets show shapes only.

**Goal:** The Stacks page becomes a host dashboard: hostname in the heading, host tiles (containers, CPU, memory, disk, network, disk I/O, volumes, uptime/load, restarts), CPU/Memory sparklines and Net RX/TX columns in the stack table, and below it Alerts, Top consumers and a live Docker events feed — all backed by three new session-gated endpoints and an `event` message on the existing `/api/events` stream, cheap while nobody looks and cached server-side.

**Architecture:**
- Two new stdlib-only Go packages: `internal/host` (host snapshot collector: `/proc` + `Statfs` on Linux, Docker `/info` + sampler fallback elsewhere, single-flight 5 s cache, 60 s async sub-caches for `system/df` and the 24 h restart scan) and `internal/alerts` (derives alerts on request from data the server already holds, 5 s cache, in-memory first-seen map).
- `internal/metrics` gains a cross-project bucket query and an aggregator for per-stack sparklines; `internal/engine` gains `/info`, `/system/df`, a bounded events window, and richer `Event` attributes; `internal/state.Cache` fans container events out to subscribers.
- `internal/api` registers `GET /api/host`, `GET /api/alerts`, `GET /api/metrics/summary` and merges an `event` message into `/api/events`. `serve.go` wires one collector and one alerts engine.
- The web list page owns a small dashboard controller (visibility-aware timers) and renders `HostTile`s, `Sparkline`s, `AlertsCard`, `TopConsumersCard` and `EventsFeed` from `web/src/lib/dashboard/`, built on 8a's tokens and components.

**Tech Stack:** Go 1.26 stdlib only (no new modules). Svelte 5, TypeScript, Vitest (jsdom). No new npm dependencies.

**Spec:** `docs/superpowers/specs/2026-10-15-stackstead-ui-v2-design.md` — this plan implements §13 "8b": §3 rows "Host metrics source", "`/api/host` work model", "Alerts", "Per-stack sparklines", "Docker events feed"; §6.3 items 1 (hostname), 2 (host tiles), 4 (CPU/Memory/Net columns), 5 (dashboard sections) and the responsive rules; §6.8 Sparkline, HostTile, AlertsCard, TopConsumersCard, EventsFeed; §10.2–10.6; §11 (8b rows); §12 (8b tests); §14 (8b docs). Mock-up: `docs/Stackstead DevOps UI redesign/Stackstead v2.dc.html` (tile, sparkline and card markup when the spec is silent).

### Plan series

1–7 and **8a are merged** (`main` at `8b3515a`, which adds the user's README redesign and `web/src/assets/logos/` on top of 8a's `08e353d`: tokens, components, shell, list table with Stack/Status/Services/Health/Ports/actions, detail page, `lifecycleDisabledReason`, `summary.ts`, `liveMetrics.ts`, appearance store). **8b (this plan)** builds on them and merges to `main` after the final review.

## Decisions (controller, recorded for the user)

The TypeSafe skill is an SDK for an external judgment API and cannot run here without network access and credentials, so decisions are argued in writing (as in 8a). The spec's §3 table holds the design decisions; these are the plan-level ones.

- **Interfaces at the package edges, fakes in tests.** `host.Collector` depends on small interfaces (`DockerAPI`, `SamplerLatest`, `ContainerSource`, `Statfser`) and an injected clock, plus `procRoot`/`sysRoot` strings, so every rule in §10.3 is tested with fixtures and fakes, never a real daemon. `alerts.Engine` depends on `StackDetails`, `ContainerSource`, `RestartCounts`, `Connectivity` and `MetricsHealth` interfaces the same way.
- **Alerts read stacks through one new service method.** `stacks.Service.Details(ctx) ([]Detail, error)` returns every registered stack's `Detail` from one container listing (same `describe` path as `Get`), and `Detail` gains `Overrides []string` (from the existing `overrideFiles`). This keeps alert rules out of the stacks package and avoids N `Get` calls that would each re-list containers. `Overrides` is not added to the wire DTO.
- **Restart counts are shared, not recomputed.** The host collector's 24 h restart sub-cache is the single source; `alerts.Engine` reads it through `RestartCounts` (`map[containerID]int`, `nil` before the first refresh). An alert never waits for a Docker events scan.
- **Single-flight caches stay local to each package.** `internal/host/flight.go` holds a ~40-line generic `cached[T]` with a synchronous single-flight `Get` and a stale-while-revalidate `GetStale`; `internal/alerts` and the summary handler each use a smaller synchronous single-flight of their own. Three call sites with two different semantics do not justify a shared package.
- **Event attributes are decoded in `engine`, filtered in `state`.** `engine.Event` gains `Name`, `Image`, `ExitCode *int`, `Health`; `health_status: unhealthy` normalises `Action` to `health_status`. `state.Cache.SubscribeEvents()` forwards only the spec's action set (`create start die stop kill restart destroy pause unpause oom health_status`) and drops `exec_*`; the existing resync debounce is untouched.
- **The SSE `event` message is fan-out only.** No ids, no replay; a drop on either channel sends `dropped` and ends the stream (current semantics), so Plan 5/8a clients that ignore unknown events keep working.
- **Frontend timers live in one controller, not in components.** `createDashboard(deps)` in `web/src/lib/dashboard/controller.ts` owns the host (10 s), summary (30 s) and alerts (60 s + on change/resync, debounced like the list) refreshes, pauses on `visibilitychange` → hidden, resumes with an immediate fetch, and stops on destroy. Components are pure renderers of its stores. Events come from the existing `live` store (new `events` readable, 50 kept), not a second SSE.
- **Mock data never ships.** Tiles whose section is `null` are not rendered; rate fields that are `null` render `—` in a tile that stays; the table's CPU/Memory cells show `—` with a flat line when a stack has no samples (spec §6.3).

## Global Constraints

- Spec §1: `/api/host`, `/api/alerts`, `/api/metrics/summary` and the `event` stream message cost nothing while nobody is looking and are cached server-side so ten open tabs are no worse than one: no background goroutine collects host data on a timer; `/api/host` ≤ 1 collection / 5 s; `/api/alerts` ≤ 1 computation / 5 s; `/api/metrics/summary` ≤ 1 query / 10 s; `system/df` and the 24 h events scan are 60 s sub-caches refreshed asynchronously, never on a request path.
- Spec §10: every new route is registered with `requireSession`; unknown `/api/*` keeps answering a JSON 404 after auth; error bodies keep `{error}`; `/api/health` is untouched.
- Spec §10.2 security: `/api/host` exposes no IP addresses, kernel version, mount table, environment or paths beyond `stacks_dir`; `hostname` is `os.Hostname()`; the response has no key outside the documented set (guarded by a test).
- Spec §10.2: `/api/host` is always 200; Docker unavailable → `containers`, `volumes`, `restarts24h` null, and on `source: "docker"` also `cpu`, `memory`, `network`, `diskIO` null. Each Docker call has a 5 s timeout.
- Spec §10.4: `/api/alerts` always 200 (`[]` when empty); registry read failure → 500 `{error}`. Sorted by severity (`danger`, `warning`, `info`) then `since` descending. Nothing persisted.
- Spec §10.5: `window` ∈ `5m | 15m | 1h` (default `15m`; anything else → 400); thirty buckets, `step = window / 30`; store error → 500.
- Spec §10.6: forwarded actions exactly `create start die stop kill restart destroy pause unpause oom health_status`; `exitCode` only on `die`; `health` only on `health_status`; `project`/`service` empty strings for non-compose containers.
- Spec §11: no new timers on detail, create, import or settings pages; list-page timers cleared on unmount and on `visibilitychange` → hidden, resumed with an immediate fetch on visible.
- Spec §6.3 / §13: no placeholder sections; a `null` host section hides its tile; `source: "docker"` tiles show the sub-line "containers only".
- Budgets unchanged: entry JS ≤ 120 kB gz, total ≤ 400 kB gz, fonts ≤ 200 kB; CodeMirror and uPlot stay lazy (the dashboard must not import uPlot — sparklines are hand-built SVG paths).
- No new npm or Go dependencies. `go test ./...` passes without Node; `CGO_ENABLED=0 GOOS=linux GOARCH={amd64,arm64} go build ./...` and `GOOS=darwin go build ./...` pass (`statfs_unix.go` is `//go:build linux || darwin`, with a stub elsewhere).
- 8a rulings stay in force (notably R4: `live.docker === 'unknown'` is neutral; R12: the lifecycle reason helper is unchanged).
- No `{@html}`; container names, images, messages and event text are only interpolated.
- Test-first for every behaviour change: write the test, run it, see it fail for the expected reason, implement, pass, commit. Pure restyles covered by existing tests need no new test.
- `make check` and `make ui` pass at the end of every task that touches their inputs.
- Commits: conventional, explain why, and end with exactly these two trailer lines:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01ADj7g1VRggm8Nhf44kQ2F9
  ```

## Review Focus

1. **Ten tabs, or a tab left open overnight.** Concurrent `/api/host` calls must collapse into one collection per 5 s; a hidden tab must stop polling entirely and a returning tab must fetch once immediately, not burst. (T4 `TestSnapshotSingleFlight50`, T4 `TestSnapshotCachedFor5s`, T9 `pauses all timers while hidden and fetches once on visible`)
2. **A slow or wedged Docker daemon.** `docker system df` taking 30 s or `/events` hanging must never block `/api/host`; stale sub-cache values are served and exactly one background refresh runs; each Docker call times out at 5 s; before the first refresh completes the section is `null`, not an error. (T4 `TestSubCacheServesStaleAndRefreshesOnce`, T4 `TestSubCacheNullBeforeFirstRefresh`, T1 `TestDockerCallsHonourTimeout`)
3. **Counters that wrap, reset or go backwards** (container restart, NIC reset, 32-bit counters on old kernels, a host reboot between two requests). Rates must be `0`, never negative or astronomically large, and the first reading after a gap averages over the gap as documented. (T2 `TestRateWrapIsZero`, T2 `TestRateFirstReadingNull`, T3 `TestAggregateProjectsCounterReset`)
4. **Alert keys flapping.** An alert that persists across computations keeps its original `since`; one that disappears and comes back gets a new `since`; a container recreated with a new id under the same service keeps the key (keys use names, not ids) — the user sees a stable list, not one that reorders every refresh. (T6 `TestSinceStableAcrossComputations`, T6 `TestClearedAlertForgotten`, T6 `TestRecreatedContainerKeepsKey`)
5. **macOS / Docker Desktop and other non-proc hosts.** With no readable `/proc`, the collector must report `source: "docker"`, hide `uptime`, compute CPU as percent of host from container samples, and the UI must say "containers only" — never show the Linux VM's numbers as if they were the Mac's. (T4 `TestFallbackMatrixDockerSource`, T10 `docker source shows containers only sub-line`)

---

## File Structure

```
internal/engine/types.go            Event gains Name, Image, ExitCode, Health; Info, VolumeUsage types (T1)
internal/engine/stream.go           event attribute decoding; EventsWindow(ctx, since, until, actions) (T1)
internal/engine/client.go           Info(ctx), VolumesDiskUsage(ctx) (T1)
internal/engine/testdata/           events_window.json, info.json, system_df_volumes.json, system_df_legacy.json (T1)
internal/host/                      NEW package
  proc.go, rates.go                 /proc parsers, rate math, wrap handling (T2)
  testdata/proc/{a,b,nomemavail}/…, testdata/sys/class/net/… (T2)
  flight.go                         single-flight cache with async refresh (T4)
  collector.go                      Collector, Snapshot, fallback matrix, restart counting (T4)
  statfs_unix.go / statfs_other.go  Statfser (T4)
internal/metrics/store.go           QueryAll(ctx, from, to, step) (T3)
internal/metrics/projects.go        ProjectBucket, ProjectSeries, AggregateProjects, ParseSummaryWindow (T3)
internal/state/cache.go             SubscribeEvents() fan-out (T5)
internal/stacks/service.go          Details(ctx), Detail.Overrides (T6)
internal/alerts/                    NEW package: alerts.go, rules.go, engine.go (+ tests) (T6)
internal/apitypes/types.go          HostSnapshot, Alert, MetricsSummary DTOs (T7); EventMessage (T8)
internal/api/dashboard.go           handleHost, handleAlerts, handleMetricsSummary (T7)
internal/api/server.go              Options gain Host, Alerts, Summary (T7)
internal/cli/serve.go               wiring (T7)
internal/api/observe.go             ObserveAPI gains SubscribeEvents; handleEvents merges `event` (T8)
web/src/lib/api/types.ts, client.ts HostSnapshot, Alert, MetricsSummary, DockerEvent; api.host/alerts/metricsSummary (T9)
web/src/lib/stores/live.ts          `events` readable from `event` messages, 50 kept (T9)
web/src/lib/dashboard/controller.ts visibility-aware refresh controller (T9)
web/src/lib/format.ts               sparklinePath(values, w, h) (T10)
web/src/lib/dashboard/tiles.ts, HostTile.svelte, Sparkline.svelte (T10)
web/src/lib/dashboard/AlertsCard.svelte, TopConsumersCard.svelte, EventsFeed.svelte (T11)
web/src/routes/StackList.svelte     hostname, tiles, CPU/Memory/Net columns (T10); dashboard sections (T11)
cmd/stackstead/e2e_test.go          8b endpoint checks with Docker (T12)
docs/api.md, docs/security.md, README.md (T12)
```

## Execution waves

| Wave | Tasks | Depends on |
|---|---|---|
| 1 | T1 engine, T2 host parsers, T3 metrics summary query | — (disjoint packages) |
| 2 | T4 host collector, T5 state SubscribeEvents, T6 alerts + stacks.Details | T4: T1, T2. T5: T1. T6: T1 (declares its own `RestartCounts` interface; T4 satisfies it) |
| 3 | T7 API routes + serve wiring, T8 `event` on /api/events | T7: T3, T4, T6. T8: T5 |
| 4 | T9 web data layer, then T10 tiles + columns and T11 dashboard sections | T9: T7, T8. T10, T11: T9 (both edit StackList.svelte — run sequentially) |
| 5 | T12 e2e + docs + final checks | all |

Execution is sequential in task order; the wave table shows which tasks could run in parallel worktrees and what each consumes.

Every task: Sonnet implements test-first; Opus reviews (tests written first, behaviour covered, suite green); Fable does the final whole-branch review.

---

### Task 1: Engine — `/info`, volume disk usage, bounded events window, event attributes

Spec §10.2 (caching, restarts24h), §10.3 (`docker` source), §10.6 (event attributes), §12 (engine tests).

**Files:**
- Modify: `internal/engine/types.go`, `internal/engine/stream.go`, `internal/engine/client.go`
- Create: `internal/engine/testdata/events_window.json`, `info.json`, `system_df_volumes.json`, `system_df_legacy.json`
- Test: `internal/engine/client_test.go`, `internal/engine/stream_test.go`

**Interfaces:**
- Produces:
  ```go
  // Event gains (decoded from Actor.Attributes):
  //   Name, Image string; ExitCode *int (only on "die"); Health string ("healthy"|"unhealthy", only on health_status)
  // and Action "health_status: unhealthy" is normalised to "health_status".
  type Info struct{ NCPU int; MemTotal uint64 }
  type VolumeUsage struct{ Count int; Size uint64; Dangling int }
  func (c *Client) Info(ctx context.Context) (Info, error)
  func (c *Client) VolumesDiskUsage(ctx context.Context) (VolumeUsage, error)
  // Returns every container event with since <= time < until whose action is in actions (nil = all);
  // the stream ends because `until` is set. Bounded by the client timeout.
  func (c *Client) EventsWindow(ctx context.Context, since, until time.Time, actions []string) ([]Event, error)
  ```

**Behaviour:**
- `VolumesDiskUsage`: `GET /system/df?type=volume` on negotiated API ≥ 1.42 (`type` filter exists from 1.42; the spec says fall back below 1.45 — use the negotiated version string the client already holds; if the typed call returns 400 or the version is older, call `/system/df` and read `Volumes`). `Size` sums `UsageData.Size` where `≥ 0` (Docker reports `-1` for "not computed"); `Dangling` counts volumes with `UsageData.RefCount == 0`.
- `EventsWindow`: query `filters={"type":["container"],"event":[…actions]}` plus `since`/`until` (unix seconds); decode the finite stream into a slice with the same attribute decoding as `Events`; respect ctx and the client timeout (5 s from the collector's ctx).
- Attribute decoding: `name`, `image`, `exitCode` (parse int; ignore if absent or non-numeric), `health_status: X` → `Action = "health_status"`, `Health = X` (`healthy`/`unhealthy`; others ignored). Existing `Labels` field keeps the full attribute map (the state cache uses compose labels from it).

- [ ] **Step 1: Write the failing tests**
  - `TestInfoDecodes` (fake socket serving `info.json`: `NCPU` 8, `MemTotal`), `TestInfoHTTPError`.
  - `TestVolumesDiskUsageTyped` (sum skips `-1`, dangling = RefCount 0), `TestVolumesDiskUsageLegacyFallback` (typed call → 400 ⇒ untyped `/system/df` used).
  - `TestEventsWindowDecodesAttributes` from `events_window.json` containing: `die` with `exitCode: "1"`, `health_status: unhealthy`, `exec_start: sh`, `start` without exitCode, a non-compose container — assert names, images, exit code only on die, normalised health action, and that the request carried `since`, `until` and the action filter.
  - `TestEventsAttributesOnLiveStream` (existing `Events` path also fills the new fields).
  - `TestDockerCallsHonourTimeout` (server that never answers `/info` → error within the client timeout, using a short test timeout).
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/engine -run 'Info|VolumesDiskUsage|EventsWindow|EventsAttributes|Timeout' -v` (compile errors on missing methods/fields).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/engine ./internal/state` (state uses `Event`; must stay green) and `go vet ./...`.
- [ ] **Step 5: Commit** — `feat: engine info, volume usage, bounded event windows and event attributes` (why: the host collector and the events feed need these without extra per-container calls).

---

### Task 2: Host `/proc` parsers and rate math

Spec §10.3 (Linux column), §12 (`internal/host` parser tests).

**Files:**
- Create: `internal/host/proc.go`, `internal/host/rates.go`, `internal/host/proc_test.go`, `internal/host/rates_test.go`
- Create fixtures: `internal/host/testdata/proc/a/{stat,meminfo,loadavg,uptime,net/dev,diskstats}`, `testdata/proc/b/…` (a later reading), `testdata/proc/nomemavail/meminfo`, `testdata/sys/class/net/{eth0/device,lo,docker0,br-1a2b,veth9f1,wg0}` (only `eth0` has a `device` entry)

**Interfaces:**
- Produces:
  ```go
  type cpuTimes struct{ total, idle uint64 }       // idle includes iowait
  type reading struct {
      at                 time.Time
      cpu                cpuTimes
      cores              int
      netRx, netTx       uint64; netByIface map[string][2]uint64
      diskRead, diskWrite uint64                     // bytes (sectors × 512)
  }
  func readStat(procRoot string) (cpuTimes, int, error)
  func readMeminfo(procRoot string) (used, total, swapUsed, swapTotal uint64, ok bool, err error) // ok=false when MemAvailable is missing
  func readLoadavg(procRoot string) (l1, l5, l15 float64, err error)
  func readUptime(procRoot string) (seconds float64, err error)
  func readNetDev(procRoot, sysRoot string) (rx, tx uint64, perIface map[string][2]uint64, err error)
  func readDiskstats(procRoot string) (readBytes, writeBytes uint64, err error)
  func cpuPercent(prev, cur cpuTimes) *float64                   // nil when Δtotal == 0
  func rate(prev, cur uint64, dt time.Duration) *float64          // nil when dt < 1s; 0 when cur < prev
  func busiest(prev, cur map[string][2]uint64) string             // iface with the largest Δ(rx+tx)
  ```

**Behaviour:** exactly spec §10.3 Linux rules:
- `stat`: `cpu` line → `total = sum of all fields`, `idle = idle + iowait`; `busy = total − idle`; cores = count of `cpu\d+` lines.
- `meminfo`: kB × 1024; `used = MemTotal − MemAvailable`; `swapUsed = SwapTotal − SwapFree`; `ok=false` if `MemAvailable` missing.
- `net/dev`: sum rx/tx byte columns over interfaces that have `sysRoot/class/net/<if>/device`; if none qualify, all except `lo`, `docker*`, `br-*`, `veth*`.
- `diskstats`: whole disks matching `^(sd[a-z]+|vd[a-z]+|xvd[a-z]+|hd[a-z]+|nvme\d+n\d+|mmcblk\d+)$`; fields 6 (sectors read) and 10 (sectors written) × 512; partitions, `loop`, `ram`, `zram`, `dm-`, `md` excluded.
- Rates: `(cur − prev)/Δt`; `0` when a counter dropped (wrap/reset); `nil` when `Δt < 1 s`.
- Parsers tolerate trailing whitespace and unknown lines; malformed numeric fields → error naming the file.

- [ ] **Step 1: Write the failing tests** — `TestReadStat` (cores 8 from fixture, idle includes iowait), `TestReadMeminfo` and `TestReadMeminfoWithoutMemAvailable`, `TestReadLoadavg`, `TestReadUptime`, `TestReadNetDevPhysicalOnly` (only eth0 counted), `TestReadNetDevFallbackExcludesVirtual` (sys tree without any `device` → lo/docker0/br-/veth excluded, wg0 included), `TestReadDiskstatsWholeDisksOnly` (sda + nvme0n1 counted; sda1, loop0, dm-0 excluded; ×512), `TestCPUPercentBetweenReadings` (fixtures a→b give the hand-computed value), `TestRateWrapIsZero`, `TestRateFirstReadingNull` (dt < 1 s → nil), `TestBusiest`, `TestParserErrorsNameFile`.
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/host -v` (package does not exist yet).
- [ ] **Step 3: Implement** (stdlib only; read with `os.ReadFile`, parse with `strings.Fields`).
- [ ] **Step 4: Run** `go test ./internal/host -v`, `go vet ./internal/host`.
- [ ] **Step 5: Commit** — `feat: host /proc parsers and counter rates` (why: stdlib-only Linux host metrics for the dashboard tiles).

---

### Task 3: Metrics — cross-project buckets and per-stack sparklines

Spec §3 "Per-stack sparklines", §10.5, §12 (`internal/metrics` tests).

**Files:**
- Modify: `internal/metrics/store.go` (+ `store_test.go`)
- Create: `internal/metrics/projects.go`, `internal/metrics/projects_test.go`

**Interfaces:**
- Produces:
  ```go
  type ProjectBucket struct {
      TS time.Time; Project, Container string
      Samples uint64; CPUSum float64; MemSum uint64
      NetRx, NetTx uint64 // bucket max of the counters
  }
  func (s *Store) QueryAll(ctx context.Context, from, to time.Time, step time.Duration) ([]ProjectBucket, error) // ordered by project, container, TS
  type ProjectSeries struct {
      CPU, Mem         []*float64 // len == buckets; nil = empty bucket (Mem as float64 bytes)
      CPUNow, MemNow   *float64   // newest non-nil bucket
      NetRx, NetTx     *float64   // bytes/s, nil when no container has a rate
  }
  func AggregateProjects(rows []ProjectBucket, from time.Time, step time.Duration, buckets int) map[string]ProjectSeries
  func ParseSummaryWindow(s string) (time.Duration, error) // "" → 15m; "5m","15m","1h"; else error
  ```

**Behaviour:**
- `QueryAll`: same SQL shape as `QueryContainers` without the project/service filter, grouped by project, container, bucket.
- `AggregateProjects`: bucket index = `(TS − from)/step`; `cpu[i] = Σ over the project's containers of CPUSum/Samples`; `mem[i]` likewise for Mem; empty bucket → nil; `cpuNow`/`memNow` = newest non-nil; net rate = Σ per-container `(max − prevMax)/step` using the container's **last two buckets** with a gap ≤ 3 buckets (same rule as `AggregateServices`), counter decrease → 0; nil when no container has a rate.

- [ ] **Step 1: Write the failing tests** (real SQLite store as `store_test.go` already does): `TestQueryAllGroupsByProjectContainerBucket`, `TestAggregateProjectsSumsContainers` (two containers in one project summed, another project separate), `TestAggregateProjectsNullBuckets` (gaps → nil, `cpuNow` = newest non-nil), `TestAggregateProjectsNetRateFromLastTwoBuckets`, `TestAggregateProjectsGapRule` (gap of 4 buckets → no rate), `TestAggregateProjectsCounterReset` (decrease → 0, not negative), `TestParseSummaryWindow` table (`""`→15m, `5m`, `15m`, `1h`, `30m`→error, `1d`→error).
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/metrics -run 'QueryAll|AggregateProjects|ParseSummaryWindow' -v`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/metrics`.
- [ ] **Step 5: Commit** — `feat: cross-project metric buckets for stack sparklines` (why: one query feeds every row's sparkline instead of one query per stack).

---

### Task 4: Host collector — snapshot, fallback matrix, caching, restart counts

Spec §3 rows "Host metrics source", "`/api/host` work model"; §10.2; §10.3; §12.

**Files:**
- Create: `internal/host/flight.go`, `internal/host/collector.go`, `internal/host/statfs_unix.go` (`//go:build linux || darwin`), `internal/host/statfs_other.go` (`//go:build !(linux || darwin)`)
- Test: `internal/host/flight_test.go`, `internal/host/collector_test.go`

**Interfaces:**
- Consumes: Task 1 `engine.Info`, `VolumeUsage`, `EventsWindow`, `Event`; Task 2 parsers and rate helpers; `metrics.ExpositionSample` (`Sampler.Latest()`).
- Produces:
  ```go
  type DockerAPI interface {
      Info(ctx context.Context) (engine.Info, error)
      VolumesDiskUsage(ctx context.Context) (engine.VolumeUsage, error)
      EventsWindow(ctx context.Context, since, until time.Time, actions []string) ([]engine.Event, error)
  }
  type SamplerLatest interface{ Latest() []metrics.ExpositionSample }
  type ContainerSource interface {
      Containers(ctx context.Context) ([]engine.Container, error) // state.Cache semantics: error while disconnected
  }
  type Statfser interface{ Statfs(path string) (free, total uint64, err error) }
  type Config struct {
      ProcRoot, SysRoot, StacksDir string
      Docker DockerAPI; Sampler SamplerLatest; Containers ContainerSource; Statfs Statfser
      Now func() time.Time; Hostname func() (string, error); GOOS string
      CallTimeout time.Duration // default 5s
  }
  type Collector struct{ /* … */ }
  func New(cfg Config) *Collector
  func (c *Collector) Snapshot(ctx context.Context) Snapshot      // single-flight, 5 s cache, never errors
  func (c *Collector) RestartCounts() map[string]int              // container id → restarts in 24 h; nil before first refresh
  type Snapshot struct {
      Hostname, OS, Source string; SampledAt time.Time
      Containers *ContainerCounts; CPU *CPU; Memory *Memory; Disk *Disk; Network *Network
      DiskIO *DiskIO; Volumes *Volumes; Uptime *Uptime; Restarts24h *Restarts
  }
  // section structs mirror spec §10.2 JSON exactly (rates as *float64)
  ```

**Behaviour:**
- `Source`: `"proc"` when `ProcRoot/stat` is readable, else `"docker"`. Hostname from `os.Hostname()`; `OS` = `GOOS`.
- Fallback matrix exactly as spec §10.3 (cores/percent/memory/disk/network/diskIO/uptime/volumes/restarts/containers per source). On `docker` source: CPU percent = Σ container CPU ÷ `NCPU`; memory used = Σ container Mem, total = `MemTotal`, swap 0; network/diskIO = Σ container counter rates from consecutive `Latest()` readings; `interface = "containers"`; `uptime` nil.
- `Containers`: from the cache (`total`, `running`, `exited`, `bad = unhealthy + failed` using 8a's `engine.Container.Health()`/`ExitCode()` rules); nil when the cache errors.
- Docker unavailable: `containers`, `volumes`, `restarts24h` nil; on `docker` source also `cpu`, `memory`, `network`, `diskIO` nil. Never an error.
- Caching (`flight.go`): `cached[T]` with `Get(ctx, maxAge, load)` single-flight (concurrent callers share one load) and `GetStale(maxAge, loadAsync)` (returns the last value immediately; if older than maxAge starts exactly one background refresh; returns nil-equivalent before the first value). Snapshot uses `Get` with 5 s; `volumes` and `restarts24h` use `GetStale` with 60 s; background refreshes use a detached context with `CallTimeout`.
- Rates use the collector's own previous reading (Task 2 `rate`); first snapshot → rate fields nil.
- Restarts: `EventsWindow(now−24h, now, ["start"])`; per container id `count = max(0, starts − 1)`; `total` = Σ; `top` = two highest with `service`/`project` from `com.docker.compose.*` labels and `container` from `Name`. `RestartCounts()` returns the per-id map from the same sub-cache.
- `Statfs(StacksDir)`: `free = Bavail×Bsize`, `total = Blocks×Bsize`; `statfs_other.go` returns an error so `disk` is nil.

- [ ] **Step 1: Write the failing tests** with fakes for every interface and the Task 2 fixtures:
  - `TestSnapshotProcSource` (fixture a then b with a fake clock 10 s apart → cpu percent, network/diskIO rates, interface = busiest, uptime/load, memory incl. swap, disk from fake statfs).
  - `TestSnapshotFirstReadingRatesNull`.
  - `TestMemoryNullWithoutMemAvailable`.
  - `TestFallbackMatrixDockerSource` (no proc → source docker, cores from NCPU, cpu = Σ/NCPU, memory used = Σ Mem, total = MemTotal, interface "containers", uptime nil).
  - `TestDockerUnavailableNullsSections` (proc and docker sources each).
  - `TestContainerCountsFromCache` (bad = unhealthy + failed) and `…NullWhenDisconnected`.
  - `TestSnapshotSingleFlight50` (50 goroutines → one proc read: count fake reads via a counting FS wrapper or by counting `Docker.Info` calls on the docker source).
  - `TestSnapshotCachedFor5s` (second call within 5 s returns the same `SampledAt`; after 5 s a new one).
  - `TestSubCacheServesStaleAndRefreshesOnce` (slow fake `VolumesDiskUsage` blocks; concurrent snapshots return the stale value and the fake is called once).
  - `TestSubCacheNullBeforeFirstRefresh`.
  - `TestRestartCounting` (starts per id 1→0, 3→2, explicit restart → +1; top two; labels mapped) and `TestRestartCountsExposed`.
  - `TestSnapshotNeverErrors` (every dependency failing → sections nil, hostname/os/source/sampledAt set).
  - `flight_test.go`: `TestGetSingleFlight`, `TestGetStaleOneRefresh`, `TestGetStaleRefreshFailureKeepsOld`.
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/host -run 'Snapshot|Fallback|Docker|Container|SubCache|Restart|Get' -v`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/host` (concurrency is the point of this task), `GOOS=windows go vet ./internal/host` (stub builds), `GOOS=darwin go build ./...`.
- [ ] **Step 5: Commit** — `feat: host snapshot collector with single-flight caching and docker fallback` (why: request-triggered, cheap with many tabs, honest on non-Linux hosts).

---

### Task 5: State cache — fan out container events

Spec §3 row "Docker events feed", §10.6.

**Files:** Modify `internal/state/cache.go` (+ `cache_test.go`).

**Interfaces:**
- Consumes: Task 1 `engine.Event` attributes.
- Produces:
  ```go
  // Same buffered, drop-on-slow semantics as Subscribe: a full buffer closes the channel.
  func (c *Cache) SubscribeEvents() (events <-chan engine.Event, cancel func())
  var ForwardedActions = []string{"create","start","die","stop","kill","restart","destroy","pause","unpause","oom","health_status"}
  ```

**Behaviour:** inside `session`, every received event whose `Action` is in `ForwardedActions` (after Task 1's normalisation) is published to event subscribers without blocking, before the existing debounce logic; `exec_*` and any other action are not forwarded. Resync behaviour and `Subscribe`/`Change` semantics are unchanged. Subscribers persist across reconnects (like `Subscribe`). Cancel is idempotent.

- [ ] **Step 1: Write the failing tests** — `TestSubscribeEventsForwardsContainerEvents` (fake source emits start, die with exitCode, health_status, exec_start → subscriber gets three, in order, with attributes), `TestSubscribeEventsDropsSlowSubscriber` (fill buffer → channel closed, other subscribers unaffected), `TestSubscribeEventsSurvivesReconnect`, `TestSubscribeEventsCancelIdempotent`, and the existing change/debounce tests still pass unchanged.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/state`.
- [ ] **Step 5: Commit** — `feat: fan container events out from the state cache` (why: the events feed reuses the one Docker event stream the server already holds).

---

### Task 6: Alerts engine and `stacks.Service.Details`

Spec §3 row "Alerts", §10.4, §12 (`internal/alerts` tests).

**Files:**
- Modify: `internal/stacks/service.go` (+ `service_test.go`): `Details`, `Detail.Overrides`
- Create: `internal/alerts/alerts.go`, `rules.go`, `engine.go`, `alerts_test.go`, `engine_test.go`

**Interfaces:**
- Consumes: 8a `stacks.Detail` (with `Summary.Ports`, services' `Config.Ports`/`Restart`, `ParseError`), `engine.Container` (`Health()`, `ExitCode()`, compose labels, `Ports`). `RestartCounts` is declared here and satisfied by Task 4's `Collector.RestartCounts()`; tests use a fake.
- Produces:
  ```go
  // stacks
  func (s *Service) Details(ctx context.Context) ([]Detail, error) // registry error → error; engine error → details with Status unknown
  // Detail gains: Overrides []string
  // alerts
  type Alert struct {
      Kind, Severity, Stack, Service, Container, Message string
      Since time.Time
  }
  type StackDetails interface{ Details(ctx context.Context) ([]stacks.Detail, error) }
  type ContainerSource interface{ Containers(ctx context.Context) ([]engine.Container, error) }
  type RestartCounts interface{ RestartCounts() map[string]int }
  type Connectivity interface{ Connected() bool }   // state.Cache
  type MetricsHealth interface{ Degraded() bool }   // metrics.Sampler
  type Engine struct{ /* … */ }
  func NewEngine(st StackDetails, cs ContainerSource, rc RestartCounts, conn Connectivity, mh MetricsHealth, now func() time.Time) *Engine
  func (e *Engine) Alerts(ctx context.Context) ([]Alert, error) // 5 s single-flight cache; error only when Details fails
  func Evaluate(details []stacks.Detail, containers []engine.Container, restarts map[string]int, connected, degraded bool) []Alert // pure rules, Since zero
  ```

**Behaviour:** rules exactly per spec §10.4 table (kinds `exited`, `unhealthy`, `restart_loop`, `port_clash`, `override`, `parse_error`, `docker_disconnected`, `metrics_degraded`; severities; message formats including the optional policy clause for `always`/`unless-stopped` and `; <n> restarts in 24h` when n > 0). Notes:
- `exited` only for non-zero exit of a container of a registered project. `restart_loop` when restarts ≥ 3. `port_clash`: a declared host port (8a `Summary.Ports` semantics, per service from `Config.Ports` via `stacks.ParsePorts`) declared by two registered stacks, or published by a running container of another project or of no project — message names the other stack or `a non-compose container (<name>)`. `parse_error` message = first line of `ParseError`.
- Key = `kind|stack|service|containerName` (names, not ids, so a recreated container keeps its key). `since` = first time this engine observed the key; keys absent from a computation are forgotten. Sort: severity order `danger, warning, info`, then `since` descending, then key for stability.
- `Alerts` caches the result 5 s (single-flight); a `Details` error returns `(nil, err)` and does not poison the cache. Containers error with the cache disconnected still yields `docker_disconnected` plus file-based alerts.

- [ ] **Step 1: Write the failing tests:**
  - `stacks`: `TestDetailsListsAllStacksWithOneContainerListing` (counting fake source → 1 call), `TestDetailsOverrides` (temp dir with `compose.override.yaml`).
  - `alerts` table `TestEvaluate` — one case per kind with the exact message, plus: exit 0 → no alert; policy clause present only for `always`/`unless-stopped`; restarts clause only when > 0; port clash between two stacks and with a non-compose container (name in message); parse error first line only.
  - `TestSeverityOrder`, `TestSinceStableAcrossComputations`, `TestClearedAlertForgotten` (gone then back → new since), `TestRecreatedContainerKeepsKey`, `TestAlertsCachedFor5s`, `TestAlertsSingleFlight`, `TestDetailsErrorNotCached`, `TestDisconnectedStillReportsFileAlerts`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/alerts ./internal/stacks`.
- [ ] **Step 5: Commit** — `feat: derive dashboard alerts from stacks, containers and restart counts` (why: alerts without storage or notifications, stable across refreshes).

---

### Task 7: API — `/api/host`, `/api/alerts`, `/api/metrics/summary`, wiring

Spec §10.2, §10.4, §10.5, §12 (`internal/api` contract tests); `docs/api.md` for these routes is Task 12.

**Files:**
- Modify: `internal/apitypes/types.go` (+ `types_test.go`), `internal/api/server.go`, `internal/api/observe.go` (ObserveAPI/ObserveSources only if `Latest` is needed for `top`; it already exists)
- Create: `internal/api/dashboard.go`, `internal/api/dashboard_test.go`
- Modify: `internal/cli/serve.go` (+ test if wiring is unit-tested), `internal/api/client_contract_test.go`

**Interfaces:**
- Consumes: Task 4 `host.Collector`, Task 6 `alerts.Engine`, Task 3 `QueryAll`/`AggregateProjects`/`ParseSummaryWindow`, `Sampler.Latest()`.
- Produces:
  ```go
  // api.Options gains:
  Host    HostAPI    // interface{ Snapshot(ctx) host.Snapshot }
  Alerts  AlertsAPI  // interface{ Alerts(ctx) ([]alerts.Alert, error) }
  Summary SummaryAPI // interface{ QueryAll(ctx, from, to, step) ([]metrics.ProjectBucket, error); Latest() []metrics.ExpositionSample }
  // apitypes: HostSnapshot (+ section structs, all sections pointers with omitempty OFF so null is explicit),
  //           Alert, MetricsSummary {Window, Step, From, To, Stacks map[string]ProjectSummary, Top {CPU, Mem []TopEntry}}
  ```

**Behaviour:**
- Routes registered with `requireSession` only when their option is non-nil; unknown `/api/*` → JSON 404 unchanged.
- `GET /api/host` → 200 with the §10.2 body: `null` sections explicit; keys exactly the documented set.
- `GET /api/alerts` → 200 array (`[]` when empty, never `null`); engine error → 500 `{error}`.
- `GET /api/metrics/summary?window=` → 400 `{error}` for invalid window; `from = now − window` aligned down to `step`, `to = from + window`; 30 buckets; `stacks` keyed by project, absent projects omitted; `top.cpu`/`top.mem` = five highest from `Latest()`; 10 s single-flight cache per window value; store error → 500.
- `serve.go`: build `host.New(host.Config{ProcRoot:"/proc", SysRoot:"/sys", StacksDir: cfg.StacksDir, Docker: dockerEngine, Sampler: sampler, Containers: cache, Statfs: host.OSStatfs{}, Now: time.Now, Hostname: os.Hostname, GOOS: runtime.GOOS})`, `alerts.NewEngine(stackSvc, cache, collector, cache, sampler, time.Now)`, a summary adapter over `metricsStore` + `sampler`; pass in Options.

- [ ] **Step 1: Write the failing tests** (`dashboard_test.go`, fakes for the three interfaces):
  - `TestHostRouteRequiresSession` / `TestAlertsRouteRequiresSession` / `TestSummaryRouteRequiresSession` (401 without cookie).
  - `TestHostBodyKeysAreExactlyDocumented` (decode into `map[string]any`, compare top-level and per-section key sets to the spec list; nulls present for nil sections).
  - `TestAlertsEmptyIsArray`, `TestAlertsErrorIs500`.
  - `TestSummaryWindowValidation` (`30m` → 400 `{error}`), `TestSummaryShape` (window/step/from/to, 30-length arrays, nulls, top five ordered), `TestSummaryCachedFor10s`, `TestSummaryStoreErrorIs500`.
  - `TestUnknownAPIPathStillJSON404`.
  - `client_contract_test.go`: the CLI client package can decode the three bodies into `apitypes`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./...`, `go vet ./...`, `gofmt -l .`.
- [ ] **Step 5: Commit** — `feat: host, alerts and metrics summary endpoints` (why: the dashboard's three data sources, session-gated and cached).

---

### Task 8: `event` messages on `/api/events`

Spec §10.6, §12 (`/api/events` interleaving tests).

**Files:** Modify `internal/api/observe.go` (+ `observe_test.go`), `internal/apitypes/types.go` (`EventMessage`).

**Interfaces:**
- Consumes: Task 5 `Cache.SubscribeEvents()`.
- Produces: `ObserveAPI` gains `SubscribeEvents() (<-chan engine.Event, func())`; `ObserveSources` implements it via the cache. `apitypes.EventMessage{Action, Container, Project, Service, Image string; Time time.Time; ExitCode *int "exitCode,omitempty"; Health string "health,omitempty"}`.

**Behaviour:** `handleEvents` subscribes to both channels; the existing goroutine selects on both; an engine event becomes `event: event` with the DTO (project/service from compose labels, `""` otherwise; container = `Name`); a closed channel on either side sends `dropped` and ends the stream; `status` and `change` unchanged; order preserved per source.

- [ ] **Step 1: Write the failing tests** — `TestEventsStreamCarriesEventMessages` (fake SubscribeEvents emits die with exit code and health_status → two `event` messages with exact JSON, interleaved with a `change` and the initial `status`), `TestEventsDroppedOnEventChannelClose`, `TestEventsDroppedOnChangeChannelClose` (existing behaviour kept), `TestEventMessageOmitsExitCodeExceptDie`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/api`.
- [ ] **Step 5: Commit** — `feat: forward Docker container events on the existing events stream` (why: one SSE per tab stays the rule).

---

### Task 9: Web data layer — types, client, live events, dashboard controller

Spec §10 (DTO shapes), §11 (refresh policy), §12 (Vitest: visibility pauses timers).

**Files:**
- Modify: `web/src/lib/api/types.ts`, `web/src/lib/api/client.ts` (+ `client.test.ts`), `web/src/lib/stores/live.ts` (+ `live.test.ts`)
- Create: `web/src/lib/dashboard/controller.ts`, `controller.test.ts`

**Interfaces:**
- Consumes: Task 7/8 wire shapes.
- Produces:
  ```ts
  export interface HostSnapshot { hostname: string; os: string; source: 'proc' | 'docker'; sampledAt: string;
    containers: { total: number; running: number; exited: number; bad: number } | null;
    cpu: { percent: number | null; cores: number } | null; memory: {…} | null; disk: {…} | null;
    network: { rxBytesPerSec: number | null; txBytesPerSec: number | null; interface: string } | null;
    diskIO: {…} | null; volumes: {…} | null; uptime: {…} | null; restarts24h: {…} | null }
  export interface Alert { kind: string; severity: 'danger' | 'warning' | 'info'; stack?: string; service?: string; container?: string; message: string; since: string }
  export interface MetricsSummary { window: '5m'|'15m'|'1h'; step: number; from: string; to: string;
    stacks: Record<string, { cpu: (number|null)[]; mem: (number|null)[]; cpuNow: number|null; memNow: number|null; netRxBytesPerSec: number|null; netTxBytesPerSec: number|null }>;
    top: { cpu: TopEntry[]; mem: TopEntry[] } }
  export interface DockerEvent { action: string; container: string; project: string; service: string; image: string; time: string; exitCode?: number; health?: 'healthy'|'unhealthy' }
  // client
  api.host(): Promise<HostSnapshot>; api.alerts(): Promise<Alert[]>; api.metricsSummary(window?: '5m'|'15m'|'1h'): Promise<MetricsSummary>
  // live.ts
  export const events: Readable<DockerEvent[]>        // newest first, at most 50; cleared on stopLive
  // controller.ts
  export interface DashboardDeps { host; alerts; summary; changes: Readable<LiveChange|null>; doc?: Document; now?: () => number }
  export function createDashboard(deps: DashboardDeps): {
    host: Readable<HostSnapshot | undefined>; alerts: Readable<Alert[] | undefined>; summary: Readable<MetricsSummary | undefined>;
    errors: Readable<{ host?: string; alerts?: string; summary?: string }>; start(): void; stop(): void }
  ```

**Behaviour:**
- `live.ts`: on `event` messages, parse and prepend (keep 50); malformed data ignored; `stopLive` clears.
- Controller per spec §11: on `start()` fetch all three immediately; host every 10 s, summary every 30 s, alerts every 60 s and on every `changes` value (debounced 300 ms, max 1 s — reuse `stores/debounce.ts`; resync flushes immediately). On `visibilitychange` → hidden: clear every timer and cancel the pending debounce; → visible: fetch all three once and restart timers. A failed fetch keeps the previous value and records the error; the next tick retries. `stop()` clears everything and removes the listener. No timers exist before `start()`.

- [ ] **Step 1: Write the failing tests:** `client.test.ts` (paths, `window` query, 400 surfaces as ApiError); `live.test.ts` `event messages prepend and cap at 50`, `malformed event ignored`, `stopLive clears events`; `controller.test.ts` (fake timers + fake document): `fetches all three on start`, `host every 10s summary every 30s alerts every 60s`, `alerts refetch on change debounced and immediately on resync`, `pauses all timers while hidden and fetches once on visible`, `failed fetch keeps previous value and records error`, `stop clears timers and listener`, `no timers before start`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `cd web && npm run check && npm test`.
- [ ] **Step 5: Commit** — `feat: dashboard data layer with visibility-aware refresh` (why: one owner for the list page's timers, paused when nobody looks).

---

### Task 10: Host tiles, sparklines and table columns

Spec §6.3 items 1, 2, 4 (8b columns) and responsive rules; §6.8 Sparkline, HostTile; §10.3 sub-lines.

**Files:**
- Create: `web/src/lib/dashboard/tiles.ts` (+ `tiles.test.ts`), `HostTile.svelte`, `Sparkline.svelte` (+ `dashboard.test.ts`)
- Modify: `web/src/lib/format.ts` (+ `format.test.ts`): `sparklinePath`
- Modify: `web/src/routes/StackList.svelte` (+ `StackList.test.ts`)

**Interfaces:**
- Consumes: Task 9 controller stores, 8a `summary.ts`, `formatBytes`, `formatRate`, `formatPercent`, `formatDuration`.
- Produces:
  ```ts
  export function sparklinePath(values: (number | null)[], w = 60, h = 18): string // "" when no points; nulls break the path ("M…L…M…")
  export interface TileModel { id: string; label: string; value: string; sub?: string; subTone?: 'muted'|'success'|'warning'|'danger'; ratio?: number /* 0..1 */ }
  export function hostTiles(h: HostSnapshot): TileModel[] // null sections omitted; order per mock-up
  // Sparkline props: { values: (number|null)[]; tone: 'accent'|'info' }
  // HostTile props: { tile: TileModel }
  ```

**Behaviour:**
- Heading: hostname in `--muted` after `h1` when `host` is loaded.
- Tiles (`repeat(auto-fit, minmax(160px,1fr)); gap 8px`; ≤ 720 px two per row `minmax(140px,1fr)`): one per non-null section — Containers (`running/total`, sub `bad` in danger when > 0), CPU (`percent` or `—`, sub `<cores> cores` or "containers only" for docker source, bar ratio), Memory (`used/total`, sub swap or "containers only", bar), Disk (`free` of `total` for `stacks_dir`, bar = used ratio), Network (`rx / tx` rates or `—`, sub interface), Disk I/O (`read / write` or `—`), Volumes (`count · size`, sub `<n> dangling` in warning when > 0), Uptime (`formatDuration`, sub `load l1 l5 l15`), Restarts 24h (`total`, sub top container names). Value 17 px/600 mono; label 11.5 px muted ellipsis; sub 11 px mono coloured by `subTone`; 3 px bar on `--border` track when `ratio` set.
- Table: insert **CPU** and **Memory** (sparkline 56×18 SVG `viewBox 0 0 60 18`, stroke 1.2 `--accent`/`--info`, `preserveAspectRatio="none"`, `aria-hidden`; value mono right-aligned, `min-width` 38/48 px; `—` and a flat line when the stack's project has no samples) and **Net RX / TX** (`rx / tx` mono 11.5 px muted) before the actions column; project → stack via `StackSummary.project`; card-layout `data-label`s added.
- 8a's `no 8b sections render` test is replaced by tests asserting the new columns and tiles (8b supersedes it).

- [ ] **Step 1: Write the failing tests:** `format.test.ts` `sparklinePath` (nulls break the path, single point → flat line across the width, all nulls → "", scaling to h with max); `tiles.test.ts` `null sections are omitted`, `docker source shows containers only sub-line`, `rates null render dash but tile stays`, `bad containers sub-line is danger`, `ratios clamp to 0..1`; `dashboard.test.ts` `HostTile renders label, value, sub and bar`, `Sparkline is aria-hidden and uses the tone`; `StackList.test.ts` `hostname shows after heading`, `tiles render from host snapshot`, `CPU and Memory cells show sparkline and value`, `stack without samples shows dash and flat line`, `Net RX/TX cell`, `columns keep card data-labels`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement** (StackList creates the controller on mount, `start()`/`stop()` with the component).
- [ ] **Step 4: Run** `npm run check && npm test && npm run build` (entry budget; no uPlot import).
- [ ] **Step 5: Commit** — `feat: host tiles and per-stack sparklines on the stack list`.

---

### Task 11: Alerts, Top consumers and Docker events sections

Spec §6.3 item 5, §6.8, §7 (events feed not a live region; alerts count in heading).

**Files:**
- Create: `web/src/lib/dashboard/AlertsCard.svelte`, `TopConsumersCard.svelte`, `EventsFeed.svelte` (+ tests in `dashboard.test.ts` or per-component files)
- Modify: `web/src/routes/StackList.svelte` (+ test)

**Interfaces:**
- Consumes: Task 9 `alerts`, `summary.top`, `live.events`, `live.connection`; 8a `RelativeTime`, router `link`.
- Produces: `AlertsCard` props `{ alerts: Alert[] }`; `TopConsumersCard` props `{ top: { cpu: TopEntry[]; mem: TopEntry[] } }`; `EventsFeed` props `{ events: DockerEvent[]; connection: SSEStatus }`.

**Behaviour:** per spec §6.3 item 5:
- Grid `repeat(auto-fit, minmax(280px, 1fr)); gap: 10px; align-items: start` after the table card.
- **Alerts**: `h3` "Alerts" with the count in mono `--warning`; `<ul>` rows: 7 px tone dot, stack name link (mono, `/stacks/<name>`; plain text when no stack), kind uppercase 11 px in tone, message `--muted` with `overflow-wrap: anywhere`, relative `since` in `--faint` 11 px; empty "No alerts."
- **Top consumers**: `h3` with a CPU | Memory segmented toggle (18 px, `aria-pressed`); five rows `container · stack` mono ellipsis, 4 px bar scaled to the top value (`--accent` CPU, `--info` memory), value mono right (`formatPercent` / `formatBytes`); empty "No samples yet."
- **Docker events**: `h3` with a pulsing `live` dot while the events stream is `open`, "reconnecting" in `--warning` otherwise; mono 11.5 px rows `62px 58px 1fr`: `HH:MM:SS` (local) `--faint`, action in tone (die/oom/kill/unhealthy → danger, stop/pause/restart → warning, start/create/healthy → success, else muted), container text ellipsis (`container` plus ` exit <code>` on die); newest first; empty "Waiting for events…". The `<ul>` is **not** a live region.

- [ ] **Step 1: Write the failing tests:** `alerts render with links and count in heading`, `alert without stack renders plain text`, `empty alerts message`, `top consumers toggle switches metric and bars scale to top`, `top consumers empty`, `events feed prepends newest and caps at 50` (driving `live`), `events feed status shows reconnecting when not open`, `events feed is not a live region`, `die shows exit code`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm run check && npm test && npm run build`.
- [ ] **Step 5: Commit** — `feat: alerts, top consumers and docker events on the stack list`.

---

### Task 12: E2E, documentation, final checks

Spec §12 (8b e2e), §14 (8b docs).

**Files:** Modify `cmd/stackstead/e2e_test.go`, `docs/api.md`, `docs/security.md`, `README.md` (dashboard bullet), `docs/development.md` if it lists packages.

The README's centred header, badges and feature table (user edit, `8b3515a`) stay exactly as they are; only the feature list gains the dashboard line. `web/src/assets/logos/*.png` are README-only sources (not imported by the app) and must stay unimported so they never enter the bundle.

**Behaviour:**
- E2E (with Docker, after the smoke stack is up): `GET /api/host`, `/api/alerts`, `/api/metrics/summary` answer 200 with the documented top-level keys; `/api/metrics/summary?window=30m` → 400; without a session each → 401.
- `docs/api.md`: the three routes with bodies, nullability, caching, the fallback matrix summary, the alert kinds table, the `event` message.
- `docs/security.md`: `/api/host` discloses hostname, core/memory totals, `stacks_dir` free space and a NIC name to authenticated users only; nothing else from `/info` is exposed.
- Final checks: `make check`, `make ui`, `go test ./...` without Node on PATH, `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`, `GOOS=darwin go build ./...`, `GOOS=windows go vet ./internal/host`, the e2e above, and a headless-Chrome screenshot pass of `/` in both themes at 375 and 1280 with a throwaway stack (scratchpad config, cleaned up after) — report visual issues, don't fix them here.

- [ ] **Step 1: Write the failing e2e assertions**; run `make ui && STACKSTEAD_E2E_CHROME=<chrome> go test -tags e2e ./cmd/stackstead -run E2E -v` (expect failures until the binary includes 8b routes — they exist after T7, so prove failure by temporarily pointing at a wrong path if they pass first time, then restore).
- [ ] **Step 2: Write the docs.**
- [ ] **Step 3: Final checks** as listed.
- [ ] **Step 4: Commit** — `test: e2e checks for the dashboard endpoints` and `docs: dashboard endpoints, host data exposure`.
- [ ] **Step 5: Hand off** for the Fable final review, then merge to `main`.
