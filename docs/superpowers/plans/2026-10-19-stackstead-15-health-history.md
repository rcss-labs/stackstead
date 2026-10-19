# Stackstead Plan 15 — Health history endpoint

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** tasks, names, signatures, test cases and examples only; no code bodies. Implementers write everything test-first.

**Goal:** Persist every container state transition and every restart so that `GET /api/stacks/{name}/health?span=60m` can say, per service, **when it started failing** (contiguous state runs over the window) and **how often it restarted** (a count), surviving Stackstead restarts and pruned with the metrics retention.

**Architecture:** A new package `internal/history` owns a `Store` (rows in a new `container_transitions` table on the shared SQLite db), a `Recorder` (a goroutine that follows the state cache: it diffs `Containers()` snapshots on every project `Change` to write deduped state rows, and consumes the raw event stream to write restart marker rows), and a pure `Build` that turns rows into per-service runs with worst-state-wins across replicas. `internal/api/history.go` adds the one route behind an optional `Options.History`; `apitypes`, the Go client, the web client types and `docs/api.md` gain the matching contract. The package is new rather than inside `state` (engine-only, store-free) or `metrics` (resource samples), mirroring how `actionlog` has its own store on the shared db.

**Tech Stack:** Go 1.26 standard library + `modernc.org/sqlite` (already a dependency); TypeScript types only on the web side (Svelte UI untouched). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-19-stackstead-react-evolve-v2-design.md` §7 (and §9 row 15). Independent of the React plans; Plan 18 consumes the endpoint.

## Spec divergences

1. **There is no per-container change stream in the cache.** `state.Cache` publishes `Change{Project, Seq}` after a debounced (250 ms) resync whenever a project's projected views differ (`cache.go:188-205`, `view` is private), plus raw Docker events through `SubscribeEvents()` (`ForwardedActions`, forwarded before the resync, not deduped). The recorder therefore subscribes to `Subscribe()` and diffs `Containers()` snapshots against its own last-known per-container state; the cache is not modified. Dedupe falls out of the diff. A `Change` with an empty `Project` is a connect/disconnect transition (`Connected()` tells which); every such change that finds the cache connected triggers a baseline, which also covers a disconnect-then-reconnect that raced the subscriber. A closed subscription (slow subscriber) is handled by resubscribing and writing a baseline, since rows may have been missed.
2. **Restarts need their own rows.** A restart-policy restart emits `die` then `start` within the debounce, so the snapshot diff sees `running → running` and writes nothing; `restarts` cannot be derived from state rows. Restart events are persisted as marker rows in the same table with `state = 'restart'` (exempt from dedupe, ignored by the run builder, counted by the endpoint). The `CHECK` lists the seven states plus `restart`. One table, one index, one prune.
3. **Counting rule.** Docker emits `kill, die, start, restart` for `docker restart` and `die, start` for a policy restart, so counting both `restart` events and `die→start` pairs would double count. The recorder writes one `restart` row on a `start` event that follows a `die` for the same container id (the pair) and ignores the `restart` action. A second pair for the same container within the same second is not written twice (event replay after a reconnect is at most one second, `cache.go:134`). TypeSafe scored the earlier two-trigger rule 1.19/2 on miscount risk; this single-trigger rule is what the tests pin.
4. **Derived state mapping beyond the spec's list.** Docker states the spec does not name are mapped explicitly: `created` → `starting`, `paused` → `running`, `dead` and `removing` → `exited`. `running` maps through `Container.Health()`: `healthy`/`unhealthy`/`starting` when present, else `running`. `exited` carries `Container.ExitCode()` when parsed, else `NULL`.
5. **Services listed** are those with at least one row in or before the window; a service that has never been recorded is absent from `services` and the client renders it as `nodata`. Containers in a `removed` state contribute to the worst-state only when every container of the service is removed.
6. **Schema version tests hardcode 5** (`internal/store/actionlog_test.go:33`, `store_test.go:111`); they move to 6 in Task 1.
7. `golangci-lint` is not configured; verification is `go vet ./...`, `gofmt -l .`, `make test`, `make ui-test`, `make ui-check` (as in Plans 13–14).

## Global Constraints

- Additive only: no existing route, response shape or `apitypes` field changes (§3). The Svelte UI is untouched except the shared `types.ts`/`client.ts` additions.
- Table `container_transitions (ts INTEGER NOT NULL, project TEXT NOT NULL, service TEXT NOT NULL, container TEXT NOT NULL, state TEXT NOT NULL CHECK (...), exit_code INTEGER NULL)`, index `container_transitions_project_ts ON (project, ts)`. `ts` is Unix seconds. Migrations are append-only (`store.go:19-21`).
- States: `running`, `healthy`, `unhealthy`, `starting`, `restarting`, `exited`, `removed`, plus the marker `restart`. Timeline states add `nodata`.
- Identical consecutive states per container are never written twice; a baseline row per container is written on startup and on every reconnect regardless.
- Prune runs through `metrics.PruneStep` with the metrics retention (`cfg.MetricsRetention`, default 72 h), registered in `serve.go` next to the action-log step.
- `span`: a Go duration, `1m ≤ span ≤ 24h`, default `60m`; anything else is 400 `{error}`. `to` is the server's now truncated to the second, `from = to - span`.
- Response `{from, to, services:[{service, restarts, runs:[{state, from, to}]}]}` with Unix seconds; `runs` are contiguous, `runs[0].from == from`, `runs[last].to == to`, each `run.from < run.to`; `services` and `runs` are never `null`; services sorted by name.
- Worst state per moment across replicas: `exited` > `unhealthy` > `restarting` > `starting` > `healthy`/`running` > `removed` (only when nothing else) > `nodata`.
- Unknown stack → 404 through `writeStackError` (`stacks.ErrStackNotFound`), as `/metrics` does. Session required.
- Recorder failures never stop the process: a failed insert is logged (rate-limited to one warning per minute, like the sampler) and the container's last-known state is left unchanged so the next change retries.

## Review Focus

1. A restart loop (`die`,`start` every 100 ms) writes exactly one `restart` row per cycle and no state rows beyond the first `restarting`/`running` transition; `docker restart` (`kill, die, start, restart`) counts once. (Task 4.)
2. A window that starts after the only known row: the run before it is `nodata` and the first state's `from` equals the window's `from`, not the row's `ts` (the latest row *before* `from` is the starting state). (Task 3.)
3. Two replicas where one is `exited` and the other `healthy`: the service is `exited` for that overlap only, and goes back to `healthy` once the exited container is `removed` (compose recreate). (Task 3.)
4. The cache drops the recorder as a slow subscriber: the recorder resubscribes and writes a baseline, so a transition missed meanwhile is at worst a late row, never a wrong state forever. (Task 4.)
5. `span=0s`, `span=25h`, `span=abc`, `span=59s` are 400 with the message `span must be a duration between 1m and 24h`; `span=1m`, `span=24h` and no `span` succeed. (Task 5.)

---

## File structure

| Area | Files | Tasks |
|---|---|---|
| Migration | `internal/store/store.go`, `internal/store/transitions_test.go` (new), `actionlog_test.go`, `store_test.go` | T1 |
| Store | `internal/history/store.go`, `store_test.go` (new package) | T2 |
| Timeline | `internal/history/timeline.go`, `timeline_test.go` | T3 |
| Recorder | `internal/history/recorder.go`, `recorder_test.go` | T4 |
| API and contract | `internal/api/history.go` (new), `history_test.go` (new), `internal/api/server.go`, `internal/apitypes/types.go`, `types_test.go`, `internal/client/client.go`, `internal/api/client_contract_test.go` | T5 |
| Wiring, web types, docs | `internal/cli/serve.go`, `web/src/lib/api/types.ts`, `client.ts`, `client.test.ts`, `docs/api.md` | T6 |

Order: T1 → T2 → T3 → T4 → T5 → T6 (T3 and T4 only need T2). Sonnet implements test-first, Opus reviews each task, Fable reviews the branch.

---

### Task 1: Migration

Spec §7 Storage. Divergences 2 and 6.

**Files:**
- Modify: `internal/store/store.go:19-78` (append migration 6)
- Modify: `internal/store/actionlog_test.go:33-35`, `internal/store/store_test.go:111-113` (expected schema 5 → 6)
- Create: `internal/store/transitions_test.go`

**Interfaces:**
- Consumes: `store.CreateAtVersionForTest(path, n)`, `store.Open`, `store.SupportedSchema()`.
- Produces: migration 6 — table `container_transitions` and index `container_transitions_project_ts` exactly as in Global Constraints, `CHECK (state IN ('running','healthy','unhealthy','starting','restarting','exited','removed','restart'))`; `SupportedSchema() == 6`.

- [ ] **Step 1: Write the failing test** `TestContainerTransitionsMigrationFromVersion5` in `transitions_test.go`, modelled on `TestActionLogMigrationFromVersion4`: create a v5 database with one user and one `action_log` row; `Open` → `PRAGMA user_version == 6` and `SupportedSchema() == 6`; the user and the action row survive; an insert `(ts 1, 'p', 'web', 'p-web-1', 'exited', 137)` and one with `exit_code NULL` succeed; an insert with state `'bogus'` fails; `pragma_index_info('container_transitions_project_ts')` lists `project, ts` in that order. Update the two hardcoded `5`s to `6`.
- [ ] **Step 2: Run** `go test -race ./internal/store/` — FAIL: `schema = 5 supported = 5, want 6` (and the new test fails on `user_version`).
- [ ] **Step 3: Implement** migration 6 appended to `migrations`.
- [ ] **Step 4: Run** `go test -race ./internal/store/ ./internal/cli/` — `ok` (the `db` CLI tests read `SupportedSchema()` dynamically, `cmd_db_test.go:92`; nothing else hardcodes 5).
- [ ] **Step 5: Commit** `feat(store): add the container_transitions table` (why: health history must survive a Stackstead restart, and the events it is built from are not stored anywhere today).

---

### Task 2: `history.Store`

Spec §7 Storage (rows, prune).

**Files:**
- Create: `internal/history/store.go`, `internal/history/store_test.go` (real `store.Open` on a temp file, as `actionlog` tests do)

**Interfaces:**
- Produces: `type Row struct { TS time.Time; Project, Service, Container, State string; ExitCode *int }`; state constants `StateRunning = "running"`, `StateHealthy`, `StateUnhealthy`, `StateStarting`, `StateRestarting`, `StateExited`, `StateRemoved`, `StateRestart = "restart"` (marker), `StateNoData = "nodata"` (timeline only, never stored); `func NewStore(db *sql.DB) *Store`; `func (s *Store) Insert(ctx context.Context, rows []Row) error` (one transaction; `TS` stored as `Unix()`; an empty slice is a no-op; the `CHECK` surfaces as an error); `func (s *Store) Query(ctx context.Context, project string, from, to time.Time) ([]Row, error)` (the latest non-marker row per container with `ts < from`, each at its real `TS`, followed by every row with `from <= ts <= to`; ordered by `container, ts, rowid`; `restart` rows before `from` are never returned); `func (s *Store) Prune(ctx context.Context, olderThan time.Time) (int64, error)` (`DELETE … WHERE ts < ?`, rows affected).

- [ ] **Step 1: Write the failing tests.** `TestStore_InsertAndQueryWindow`: insert for project `p` container `a` rows at t=100 `running`, t=200 `unhealthy`, t=250 `restart`, t=300 `exited` (code 1), container `b` at t=150 `healthy`, and project `q` at t=200 `running`; `Query("p", 220, 320)` returns `a@200 unhealthy` (the pre-window baseline, not `a@250 restart`), `a@250 restart`, `a@300 exited` with `ExitCode == &1`, `b@150 healthy`; nothing from `q`; `Query("p", 50, 90)` returns nothing. `TestStore_QueryAtWindowEdges`: rows exactly at `from` and at `to` are included; a row at `to+1` is not. `TestStore_InsertEmptyIsNoop`. `TestStore_InsertRejectsUnknownState` (error mentions `CHECK`). `TestStore_PruneRemovesOlderRows` (rows at 100, 200; `Prune(150)` returns 1 and leaves the row at 200).
- [ ] **Step 2: Run** `go test -race ./internal/history/` — FAIL: `undefined: history.NewStore`.
- [ ] **Step 3: Implement.** The baseline part of `Query` is one `GROUP BY container` subquery with `MAX(ts)` joined back by `rowid` (ties resolved by the highest rowid), the window part a plain range scan on the index.
- [ ] **Step 4: Run** — `ok`.
- [ ] **Step 5: Commit** `feat(history): store container transitions with a window query that carries the prior state` (why: a window must know the state in force at its start, which lives in the last row before it).

---

### Task 3: Timeline builder

Spec §7 Endpoint semantics (runs, nodata, restarts, worst state). Review focus 2 and 3.

**Files:**
- Create: `internal/history/timeline.go`, `internal/history/timeline_test.go`

**Interfaces:**
- Consumes (T2): `Row`, state constants, `Store.Query`.
- Produces: `type Run struct { State string; From, To time.Time }`; `type ServiceTimeline struct { Service string; Restarts int; Runs []Run }`; `type Timeline struct { From, To time.Time; Services []ServiceTimeline }`; `func Build(rows []Row, from, to time.Time) Timeline` (pure: per container, a state timeline from its rows (baseline row clamped to `from`); per service, boundaries are every distinct row `TS` in `[from, to]` plus `from` and `to`; on each segment the state is the worst across the service's containers per the rank in Global Constraints, `nodata` when no container has a state yet; adjacent equal segments merge; `Restarts` counts `restart` rows with `from <= TS <= to`; services sorted by name; `Runs` and `Services` non-nil); `func Rank(state string) int` (exported for the test table: `exited 6, unhealthy 5, restarting 4, starting 3, healthy 2, running 2, removed 1, nodata 0`); `func (s *Store) Timeline(ctx context.Context, project string, from, to time.Time) (Timeline, error)` (`Query` then `Build`).

- [ ] **Step 1: Write the failing tests** as a table in `TestBuild` with the spec's example as one case: window `[1760860000, 1760863600]`, rows `home-assistant` container at `1760860420 healthy` and `1760862900 unhealthy` plus three `restart` rows inside the window → exactly the spec's three runs and `restarts == 3`. More cases: **baseline before window** (row at 500 `running`, window `[1000, 2000]` → one run `running [1000, 2000]`, no `nodata`); **no rows** → `Services` is an empty non-nil slice; **restart rows only** before `from` are not counted (they are not even returned by `Query`, but `Build` must ignore any `restart` row outside the window); **replicas** (`a`: `healthy` from 0, `exited` at 100, `removed` at 200; `b`: `healthy` from 0; window `[0, 300]` → `healthy [0,100]`, `exited [100,200]`, `healthy [200,300]`); **all replicas removed** → `removed`; **running vs healthy** (one replica `running`, one `healthy` → `healthy`, since both rank 2 the earlier-listed container wins — pin `healthy` by making the test's first container `healthy`; document that the tie goes to the alphabetically first container); **two rows at the same second** for one container (the later rowid wins, no zero-length run); **row at exactly `to`** produces no empty run; every case asserts contiguity (`runs[i].To == runs[i+1].From`), `runs[0].From == from`, `runs[last].To == to`. `TestRank` pins the order.
- [ ] **Step 2: Run** `go test -race ./internal/history/ -run 'TestBuild|TestRank'` — FAIL: `undefined: Build`.
- [ ] **Step 3: Implement** `Build`, `Rank`, `Store.Timeline`.
- [ ] **Step 4: Run** `go test -race ./internal/history/` — `ok`.
- [ ] **Step 5: Commit** `feat(history): build per-service state runs with worst-state-wins across replicas` (why: the timeline must answer "since when" for a service, not a container, and a stack with replicas must show its worst one).

---

### Task 4: Recorder

Spec §7 Storage (recorder, dedupe, baseline, reconnect). Divergences 1, 3, 4. Review focus 1 and 4.

**Files:**
- Create: `internal/history/recorder.go`, `internal/history/recorder_test.go` (fake source driven by channels; a recording `Writer` that can be made to fail)

**Interfaces:**
- Consumes: `state.Change`, `engine.Container` (`Project()`, `ComposeService()`, `Health()`, `ExitCode()`, `State`), `engine.Event` (`Action`, `ContainerID`, `Name`, `Labels`), T2 `Row`/constants.
- Produces: `type Source interface { Containers(ctx context.Context) ([]engine.Container, error); Connected() bool; Subscribe() (<-chan state.Change, func()); SubscribeEvents() (<-chan engine.Event, func()) }` (satisfied by `*state.Cache`); `type Writer interface { Insert(ctx context.Context, rows []Row) error }` (satisfied by `*Store`); `func NewRecorder(src Source, w Writer, logger *slog.Logger, now func() time.Time) *Recorder` (nil logger → `slog.Default()`, nil now → `time.Now`); `func (r *Recorder) Run(ctx context.Context)` (returns when ctx is done; both subscriptions cancelled); `func DeriveState(c engine.Container) (state string, exitCode *int)` (mapping in divergence 4); `const warnEvery = time.Minute`.
- Behaviour: on every `Change` with a non-empty `Project`, read `Containers()` and, for that project only, compare each compose container's `DeriveState` with the last-known map (keyed by container id): a new id or a different state writes a row at `now()`; an id in the map but absent from the snapshot writes `removed` and forgets it. On a `Change` with empty `Project` and `Connected() == true`, write one row for every compose container (all projects) regardless of the map and rebuild the map; `removed` rows for ids that vanished during the disconnect. If `Containers()` returns an error, skip. When the change channel is closed, resubscribe and treat it as a connect. Events: `die` sets a pending flag for the id; `start` with the flag set writes a `restart` row (project and service from the event labels `com.docker.compose.project` / `com.docker.compose.service`, container from `ev.Name`) and clears the flag, unless the last restart row for that id has the same `now()` second; events for non-compose containers (empty project label) are ignored; a closed event channel is resubscribed. Rows of one change are inserted in one `Insert` call; on error the map is not updated for those containers and a warning is logged at most once per `warnEvery`.

- [ ] **Step 1: Write the failing tests.** `TestDeriveState` (table: `running`+`Up 5s (healthy)` → `healthy`; `running`+`Up` → `running`; `running`+`(health: starting)` → `starting`; `restarting` → `restarting`; `exited`+`Exited (137) 2s ago` → `exited`, code 137; `exited`+`Exited` without a code → `exited`, nil; `created` → `starting`; `paused` → `running`; `dead` → `exited`). `TestRecorder_BaselineOnConnect` (two compose containers and one non-compose: a connect `Change{""}` writes exactly two rows, one each, at `now`). `TestRecorder_DedupesUnchangedState` (a project `Change` with the same snapshot writes nothing; status text change `Up 5m` → `Up 6m` writes nothing). `TestRecorder_WritesTransitionAndRemoved` (`healthy` → `unhealthy` writes one row; the container vanishing writes `removed`; reappearing with a new id writes `running` for the new id). `TestRecorder_ReconnectRewritesBaseline` (after a baseline, a second `Change{""}` with `Connected()` true writes every container again even though nothing changed; a `Change{""}` with `Connected()` false writes nothing). `TestRecorder_ResubscribesAfterDrop` (the fake closes the change channel; the recorder calls `Subscribe` again and writes a baseline; same for the events channel, which must not write a baseline). `TestRecorder_CountsDieStartPairsOnce` (sequences, each on a fresh recorder with the container already in the baseline: `die,start` → 1 restart row with the event's project/service/name; `kill,die,start,restart` → 1; `create,start` → 0; `start` alone → 0; `die,start,die,start` with the clock advanced a second between pairs → 2; the same two pairs within one second → 1; a `die` without a project label → 0). `TestRecorder_InsertFailureIsRetriedAndRateLimited` (writer fails: the row is attempted again on the next change for the same container; two failures within a minute log one warning). `TestRecorder_StopsOnCancel` (both cancel funcs called, `Run` returns within a second).
- [ ] **Step 2: Run** `go test -race ./internal/history/ -run 'TestRecorder|TestDeriveState'` — FAIL: `undefined: NewRecorder`.
- [ ] **Step 3: Implement.** One goroutine, one `select` over the two channels and `ctx.Done()`; no locks beyond the map being goroutine-local.
- [ ] **Step 4: Run** `go test -race -count=3 ./internal/history/` — `ok` (the count catches channel races).
- [ ] **Step 5: Commit** `feat(history): record state transitions and restarts from the state cache` (why: the cache only tells clients to refetch; what a service was doing a minute ago is lost unless something writes it down).

---

### Task 5: API route, `apitypes`, Go client and contract test

Spec §7 Endpoint. Review focus 5.

**Files:**
- Create: `internal/api/history.go`, `internal/api/history_test.go`
- Modify: `internal/api/server.go:18-74` (`History HistoryAPI` option, documented like `Summary`), `:116-134` (register the route when `Stacks != nil && History != nil`, next to the dashboard routes), `internal/apitypes/types.go` (after `MetricsHistory`), `internal/apitypes/types_test.go` (three key rows), `internal/client/client.go` (after `MetricsHistory`), `internal/api/client_contract_test.go` (one call after `MetricsHistory`; `newObserveEnvWith` gets a fake history through the `tweak` closure or a sibling helper)

**Interfaces:**
- Consumes (T3): `history.Timeline`, `history.ServiceTimeline`, `history.Run`.
- Produces: `type HistoryAPI interface { Timeline(ctx context.Context, project string, from, to time.Time) (history.Timeline, error) }` (satisfied by `*history.Store`); route `GET /api/stacks/{name}/health`; `const defaultHealthSpan = time.Hour`, `minHealthSpan = time.Minute`, `maxHealthSpan = 24 * time.Hour`; `func parseHealthSpan(v string) (time.Duration, error)` (empty → default; otherwise `time.ParseDuration` and the bounds, error text `span must be a duration between 1m and 24h`); handler: span → `Stacks.Get(name)` (404/503 via `writeStackError`) → `History.Timeline(detail.Project, from, to)` with `to = Now().Truncate(time.Second)` → DTO; a stack with an empty `Project` answers `{from, to, services: []}` without querying. DTOs in `apitypes`: `HealthHistory{From int64 "from"; To int64 "to"; Services []ServiceHealth "services"}`, `ServiceHealth{Service string "service"; Restarts int "restarts"; Runs []HealthRun "runs"}`, `HealthRun{State string "state"; From int64 "from"; To int64 "to"}`. Client: `func (c *Client) HealthHistory(ctx context.Context, name string, span time.Duration) (apitypes.HealthHistory, error)` (`span` 0 omits the query; otherwise `span=<d.String()>`).

- [ ] **Step 1: Write the failing tests.** `history_test.go`: `TestHealthHistoryRequiresSession` (401 anonymous); `TestHealthHistoryShape` (fake timeline with the spec's example → the exact JSON of §7, `from`/`to` as integers, and `from == to - 3600` with the env's fake clock; empty timeline → `"services":[]`, not `null`); `TestHealthHistorySpan` (table from review focus 5; the fake records `to - from` and gets `60s` for `1m`, `86400s` for `24h`, `3600s` for none); `TestHealthHistoryUnknownStack` (fake `Get` returning `stacks.ErrStackNotFound` → 404 `{error}`); `TestHealthHistoryEmptyProjectSkipsStore` (detail with `Project == ""` → 200 empty services and the fake was not called); `TestHealthHistoryStoreError` → 500 generic body; `TestHealthHistoryNotRegisteredWithoutHistory` (404 from the `/api/` fallback). `types_test.go`: `{"HealthHistory", …, "from,services,to"}`, `{"ServiceHealth", …, "restarts,runs,service"}`, `{"HealthRun", …, "from,state,to"}`. Contract test: `c.HealthHistory(ctx, "web", 30*time.Minute)` decodes without error and `len(Services) == 1` from the fake.
- [ ] **Step 2: Run** `go test -race ./internal/api/ ./internal/apitypes/ -run 'HealthHistory|JSONTags|ClientAgainstRealRouter'` — FAIL: `unknown field History in struct literal`.
- [ ] **Step 3: Implement** `history.go`, the option, the DTOs, the client method.
- [ ] **Step 4: Run** `go test -race ./internal/api/ ./internal/apitypes/ ./internal/client/`, `go vet ./...`, `gofmt -l .` — clean.
- [ ] **Step 5: Commit** `feat(api): per-service health history route` (why: the timeline the redesign leads with needs one additive read, scoped and 404-mapped like the other stack routes).

---

### Task 6: Wiring, web client types, docs

Spec §7 prune hook, "Documented in `docs/api.md` under Observability", §9 row 15.

**Files:**
- Modify: `internal/cli/serve.go:160-170` (build `history.NewStore(db)` and `history.NewRecorder(cache, hist, logger, time.Now)` before the sampler; add a second `PruneStep` deleting rows older than `cfg.MetricsRetention`; add `recorder.Run` to `startBackground` after `cache.Run`), `:228-234` (`History: hist` in `api.Options` next to `Summary`), `web/src/lib/api/types.ts` (after `MetricsHistory`), `web/src/lib/api/client.ts` (after `metricsHistory`), `web/src/lib/api/client.test.ts:167-184` (one more path in "encodes paths and queries"), `docs/api.md:251-260` (new **Health history** bullet after **Metrics history**)

**Interfaces:**
- Consumes: T2 `NewStore`, `Store.Prune`; T4 `NewRecorder`, `Recorder.Run`; T5 `HistoryAPI`, DTO keys.
- Produces (web): `export type HealthState = 'running' | 'healthy' | 'unhealthy' | 'starting' | 'restarting' | 'exited' | 'removed' | 'nodata'`; `export interface HealthRun { state: HealthState; from: number; to: number }`; `export interface ServiceHealth { service: string; restarts: number; runs: HealthRun[] }`; `export interface HealthHistory { from: number; to: number; services: ServiceHealth[] }` (doc comments say Unix seconds); `healthHistory: (name: string, p?: { span?: string }, signal?: AbortSignal) => Promise<HealthHistory>` requesting `/api/stacks/${seg(name)}/health${query({ span: p?.span })}`.
- Docs bullet content: route and `span` rule (1m–24h, default 60m, 400 otherwise); the response fields with Unix seconds; runs contiguous over `[from, to]`, `nodata` before the first known row; the eight run states and their meaning (`removed` = all replicas gone); worst-state-wins order; `restarts` = `die`→`start` pairs in the window (covers `docker restart` and restart policies, counted once); history starts with the first run of this version and is kept for `metrics_retention`; refetch on the stack's `change` event; 404 for an unknown stack.

- [ ] **Step 1: Write the failing web test.** Add `await api.healthHistory('web', { span: '30m' })` to "encodes paths and queries" expecting `/api/stacks/web/health?span=30m`, and a second call without params expecting `/api/stacks/web/health`.
- [ ] **Step 2: Run** `cd web && npm run test -- client` — FAIL: `api.healthHistory is not a function`.
- [ ] **Step 3: Implement** the web types and client method; wire `serve.go`; write the docs bullet.
- [ ] **Step 4: Run** `make ui-test`, `make ui-check` (0 errors), `go build ./...`, `make test`, `go vet ./...`, `gofmt -l .` — clean. Manual check if Docker is at hand: `make build-go`, run `bin/stackstead serve` against a temp data dir, `docker restart` a compose container, `GET /api/stacks/<name>/health?span=5m` shows `restarts: 1` and a `running`/`healthy` run.
- [ ] **Step 5: Commit** in two commits: `feat(serve): record health history and prune it with the metrics retention` and `docs(api): health history route and web client types` (why: the recorder is useless unless it runs in the server, and the React work in Plan 18 consumes the typed client).

---

## Self-review notes

- Spec coverage: §7 Why → divergence 2 explains the data source; §7 Storage migration T1, recorder/dedupe/baseline/reconnect T4, prune T6; §7 Endpoint span/400/404/response T5, runs/nodata/restarts/worst-state T3, docs T6, client refetch is the React client's job (Plan 18) and is stated in the docs bullet; §7 Limitation recorded in the docs bullet; §9 row 15 items (migration, recorder, prune step, endpoint, apitypes, client contract test, docs/api.md) → T1, T4, T6, T5, T5, T5, T6.
- No code bodies: every step gives names, signatures, data and assertions only.
- Type consistency: `Row`, `Timeline`, `ServiceTimeline`, `Run`, `Build`, `Rank`, `Store.Timeline`, `Source`, `Writer`, `NewRecorder`, `DeriveState`, `HistoryAPI`, `HealthHistory`/`ServiceHealth`/`HealthRun` (Go and TS), `parseHealthSpan`, `healthHistory` are spelled the same in every task.
- Review focus: 1 → T4 `TestRecorder_CountsDieStartPairsOnce`; 2 → T3 "baseline before window" and the spec example; 3 → T3 "replicas"; 4 → T4 `TestRecorder_ResubscribesAfterDrop`; 5 → T5 `TestHealthHistorySpan`.
- Proportion: six tasks for one spec section; the plan is shorter than the spec's parent document and about twice §7, which is the detail §7 leaves to the implementation (state source, restart rule, state mapping, tie rules).
- Existing tests change only by the schema version bump (T1), the three `types_test.go` rows and the one contract-test call (T5), and one web path assertion (T6).
