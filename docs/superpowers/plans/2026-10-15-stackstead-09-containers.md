# Stackstead Plan 9 — Containers screen

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** this plan specifies *what* to build: files, signatures, behaviour, test cases and acceptance criteria. It deliberately contains no full implementations; implementers write the code test-first. Short snippets show shapes only.

**Goal:** A host-wide **Containers** screen: `/containers` lists every container on the host (state, health, image, ports, CPU, memory, 24 h restarts, problems first) and `/containers/:name` shows one container's details and logs; containers outside registered stacks can be started, stopped and restarted, containers inside a registered stack link to their stack.

**Architecture:**
- Backend: `engine.Client` gains a POST helper, `ContainerAction` and `InspectContainer`; the sampler samples every running container (non-compose samples stay live-only); `internal/api/containers.go` adds a cached list (`/api/containers`), details (`/api/containers/{id}`), actions (`POST …/{start|stop|restart}`, refused with 409 for registered-stack containers) and a container logs SSE that shares the stack logs streaming core.
- Frontend: rail + routes `/containers` and `/containers/:name`; `ContainerList.svelte` (chips, search, table, row actions, visibility-aware refresh) and `ContainerDetail.svelte` (header, actions or "Open stack", Details/Logs tabs); `LogsViewer` gains a `source` prop; existing dashboard/stack screens link into the new pages.

**Tech Stack:** Go 1.26 stdlib only; Svelte 5, TypeScript, Vitest (jsdom). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-15-stackstead-containers-design.md` (all sections). Builds on the UI v2 spec and v0.1.0 (`main` at `7cd6027`).

## Decisions (controller, recorded for the user)

The spec's §3 table holds the product decisions (made with the user). Plan-level ones:

- **The list joins at the API layer, not in `state`.** `internal/api/containers.go` composes the state-cache snapshot, `Sampler.Latest()`, `host.Collector.RestartCounts()` and a project → stack map from `StacksAPI.List` behind a 2 s single-flight cache (same panic-safe, detached-context flight shape as the 8b summary). No new package: the join is presentation of data other packages own.
- **Ownership check is a pure function.** `containerManaged(c engine.Container, projectToStack map[string]string) (managed, stack string)` is the single rule used by the list, the details and the action guard, so the UI and the server cannot disagree.
- **The action handler re-reads the registry and the container on every request** (no cache), so a stack registered a second ago is protected.
- **Docker 304 (already started/stopped) counts as success**; the handler returns the fresh row either way.
- **Logs:** the per-container goroutine/merge/cursor logic in `handleLogs` moves into `streamContainerLogs(w, r, containers []engine.Container, opts)`; the stack route selects containers by project/service, the container route passes one container. Existing stack-logs tests must pass unchanged.
- **Frontend log source:** `createLogStream` (in `web/src/lib/stacks/logs.ts`) takes a `LogSource = { kind: 'stack'; name: string } | { kind: 'container'; id: string }` instead of a stack name; `LogsViewer` takes `source` and `services` (empty for a container source, which hides the selector). Existing stack callers pass `{ kind: 'stack', name }`.
- **Container page refresh** reuses the list fetch (`api.containers()`), not a separate per-container poll; the details endpoint is fetched on open, on `?tab=details` entry, on a `change` for the container's project (or any change when it has none), and on resync.
- **List refresh controller** is a small `createContainerList(deps)` store (like 8b's dashboard controller): fetch on start, refetch on `changes` (debounce 300 ms / max 1 s; resync immediate), every 10 s while visible, pause when hidden, per-fetch generation guard and in-flight skip (the 8b fixes).

## Global Constraints

- Spec §1: problems sorted first; any container's logs one click away; start/stop/restart only for containers not in a registered stack — **server-enforced (409)**; CPU/memory for every running container; nothing polls while hidden; the list costs ≤ 1 server computation per 2 s regardless of tabs.
- Spec §4: every new route `requireSession`; unknown `/api/*` JSON 404 after auth; errors `{error}`; mutating routes JSON-only (existing CSRF rule) and accept `{}`; list 503 while the state cache is disconnected; details/actions 404 unknown id, 503 Docker unavailable, 502 Docker refusal with Docker's message; stop/restart timeout 10 s; actions bounded by 30 s.
- Spec §4.2 / §7: details NEVER include `Env`, `Cmd`, `Entrypoint`, `Config.User` or `HostConfig` beyond restart policy and mounts — guarded by a test on the JSON body.
- Spec §4.5: non-compose samples appear in `Latest()` and fan-out but are never written by `InsertBatch`.
- Spec §5.5: icon-only controls have `aria-label` and `title`; status never by colour alone; focus to `h1` after navigation; no horizontal page scroll at 375 px; explicit table roles + `data-label`s for the ≤ 720 px card layout.
- No `{@html}`; Docker error messages, names, images, labels and log lines are interpolated as text.
- Budgets unchanged: entry JS ≤ 120 kB gz, total ≤ 400 kB gz, fonts ≤ 200 kB; CodeMirror and uPlot stay lazy (the new pages import neither).
- No new npm or Go dependencies. `go test ./...` passes without Node; `CGO_ENABLED=0 GOOS=linux GOARCH={amd64,arm64} go build ./...` and `GOOS=darwin go build ./...` pass.
- README: the user's centred header, badges and feature table stay unchanged; only one feature-list line is added.
- Test-first for every behaviour change: write the test, run it, see it fail for the expected reason, implement, pass, commit.
- `make check` and `make ui` pass at the end of every task that touches their inputs.
- Commits: conventional, explain why, and end with exactly:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01ADj7g1VRggm8Nhf44kQ2F9
  ```

## Review Focus

1. **A stack container reached by bypassing the UI.** A crafted `POST /api/containers/<id>/stop` for a container whose project is a registered stack must answer 409 with the stack name and leave the container running — including for a stack registered moments ago, and for any id form (12-hex, full id). Containers of an *unregistered* compose project stay actionable. (T4 `TestActionRefusesRegisteredStackContainer`, T4 `TestActionGuardReadsRegistryEveryRequest`, T4 `TestActionAllowsUnregisteredComposeProject`)
2. **Docker says no, or says "already".** Starting a container whose port is taken returns 502 with Docker's text shown verbatim; stopping an already stopped container (Docker 304) is a success that returns the fresh row; a container removed between list and click is 404, not 500. (T1 `TestContainerActionMapsStatus`, T4 `TestActionDockerRefusalIs502`, T4 `TestActionAlreadyStoppedIsSuccess`)
3. **Secrets leaking through details.** The inspect fixture carries `Env` with a password, a `Cmd` with a token and `HostConfig` extras; none of it may appear in `/api/containers/{id}`'s JSON. (T1 `TestInspectContainerDropsSecrets`, T4 `TestDetailsBodyHasNoEnvOrCmd`)
4. **A recreated container while its page is open.** `docker compose up` (or `docker run --name`) replaces a container with a new id under the same name; the page must re-resolve the name, show the new state, and target the new id for actions and logs — and show "This container no longer exists" only when the name is truly gone. (T8 `re-resolves the id after the container is recreated`, T8 `shows no longer exists only when the name is gone`)
5. **A host with many containers.** With 200 running containers the sampler stays within `maxStatsConcurrency`, a slow stats call doesn't delay the list, and the list endpoint answers from one cached computation for 50 concurrent requests. (T2 `TestSamplerSamplesAllRunningContainersWithinConcurrency`, T3 `TestContainerListSingleFlight50`)

---

## File Structure

```
internal/engine/client.go            post helper; ContainerAction; InspectContainer (T1)
internal/engine/types.go             ContainerInspect, Mount (T1)
internal/engine/testdata/            inspect_container.json (T1)
internal/metrics/sampler.go          sample every running container; store compose only (T2)
internal/apitypes/types.go           ContainerRow, ContainerDetail, ContainerMount (T3, T4)
internal/api/containers.go           list (T3); details, actions (T4); container logs route (T5)
internal/api/containers_test.go      (T3–T5)
internal/api/observe.go              streamContainerLogs extracted from handleLogs (T5)
internal/api/server.go               Options gain Containers/Restarts sources if needed (T3)
internal/cli/serve.go                wiring (T3)
web/src/lib/api/types.ts, client.ts  ContainerRow, ContainerDetail; api.containers/container/containerAction (T6)
web/src/lib/stacks/logs.ts           LogSource; createLogStream(source, opts) (T6)
web/src/lib/stacks/LogsViewer.svelte source prop (T6)
web/src/lib/containers/list.ts       createContainerList controller; filter/sort/count helpers (T7)
web/src/lib/shell/Rail.svelte, breadcrumb.ts, icons.ts  Containers entry (T7)
web/src/App.svelte                   routes /containers, /containers/:name; titles (T7)
web/src/routes/ContainerList.svelte  list page (T7)
web/src/routes/ContainerDetail.svelte container page (T8)
web/src/lib/containers/ContainerActions.svelte  shared row/header actions + confirm (T7, used by T8)
web/src/lib/dashboard/*, lib/stacks/StackAside.svelte, ServiceCard.svelte  links (T9)
cmd/stackstead/e2e_test.go           containers e2e (T10)
docs/api.md, docs/security.md, README.md (T10)
```

## Execution waves

| Wave | Tasks | Depends on |
|---|---|---|
| 1 | T1 engine, T2 sampler | — |
| 2 | T3 list endpoint + wiring | T1 (types), T2 (Latest includes all) |
| 3 | T4 details + actions, T5 container logs | T1, T3 |
| 4 | T6 web data + log source | T3–T5 |
| 5 | T7 shell + list page, then T8 container page, then T9 links | T6 (T8 uses T7's `ContainerActions`; T9 needs both routes) |
| 6 | T10 e2e + docs + visual | all |

Execution is sequential in task order. Every task: Sonnet implements test-first; Opus reviews; Fable does the final whole-branch review.

---

### Task 1: Engine — POST helper, container actions, inspect

Spec §4.4, §4.2, §4.5.

**Files:**
- Modify: `internal/engine/client.go`, `internal/engine/types.go`
- Create: `internal/engine/testdata/inspect_container.json`
- Test: `internal/engine/client_test.go`

**Interfaces:**
- Produces:
  ```go
  type Mount struct{ Type, Source, Destination string; ReadOnly bool }
  type ContainerInspect struct {
      ID, Name, Image, State, Status, Health string // Name without leading "/"; Health "healthy|unhealthy|starting|none"
      StartedAt, FinishedAt time.Time               // zero when unset ("0001-01-01…" from Docker)
      ExitCode int; Running bool
      RestartPolicy string
      Labels map[string]string
      Ports []Port; Networks map[string]string      // same shapes as Container
      Mounts []Mount
  }
  var ErrNotFound = errors.New("docker engine: no such container") // wrapped by 404s
  type ActionError struct{ Status int; Message string }             // Docker's non-2xx answer (message from {"message":…})
  func (c *Client) ContainerAction(ctx context.Context, id, action string, stopTimeout int) error // action ∈ start|stop|restart; 304 → nil
  func (c *Client) InspectContainer(ctx context.Context, id string) (ContainerInspect, error)
  ```

**Behaviour:**
- POST helper: versioned path (`/v<api>/containers/<id>/<action>?t=<n>` for stop/restart), empty body, explicit timeout (callers pass 30 s), reuses the client's transport, version negotiation and error classification. Docker 204 → nil; 304 → nil; 404 → wraps `ErrNotFound`; other non-2xx → `*ActionError` with Docker's `message`.
- `InspectContainer`: `GET /containers/<id>/json` with the short client timeout; decodes **only** the fields above (no `Env`, `Cmd`, `Entrypoint`, `User`); state `Health.Status` → `Health` (`none` when absent); ports from `NetworkSettings.Ports` published bindings; networks from `NetworkSettings.Networks[*].IPAddress`; 404 → `ErrNotFound`.
- Any unknown action → error without calling Docker.

- [ ] **Step 1: Write the failing tests** — `TestContainerActionMapsStatus` (fake socket table: start 204 → nil, stop 304 → nil, restart 404 → `errors.Is(ErrNotFound)`, start 500 `{"message":"port is already allocated"}` → `*ActionError` with that message; asserts method POST, path, `t=10` only on stop/restart), `TestContainerActionRejectsUnknownAction` (no request made), `TestInspectContainerDecodes` (fixture → every field), `TestInspectContainerDropsSecrets` (fixture has `Env:["DB_PASSWORD=hunter2"]`, `Cmd:["--token","abc"]`; the struct has no such fields — assert via reflection that no field holds those strings), `TestInspectContainerNotFound`.
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/engine -run 'ContainerAction|InspectContainer' -v` (compile errors on missing names).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/engine ./internal/state`, `go vet ./...`, `gofmt -l .`.
- [ ] **Step 5: Commit** — `feat: engine container actions and safe inspect` (why: the Containers screen needs start/stop/restart and details without exposing env or commands).

---

### Task 2: Sampler — sample every running container, store compose only

Spec §4.5, Review Focus 5.

**Files:** Modify `internal/metrics/sampler.go` (+ `sampler_test.go`).

**Behaviour:** `tick` targets every container with `State == "running"` (drop the `Project() != ""` filter). Samples carry `Project`/`Service` from labels (empty for non-compose). `latest` and live fan-out include all samples (`SubscribeSamples(project)` unchanged — non-compose samples have project `""` and no subscriber asks for it). The batch passed to `InsertBatch` excludes samples with an empty `Project`. Concurrency and timeouts unchanged (`maxStatsConcurrency`, `statsTimeout`).

- [ ] **Step 1: Write the failing tests** — `TestSamplerSamplesNonComposeContainersLive` (fake lister: one compose + one plain running container → `Latest()` has both), `TestSamplerStoresOnlyComposeSamples` (fake store records batches → only the compose sample), `TestSamplerSamplesAllRunningContainersWithinConcurrency` (200 running containers, fake stats tracks max in-flight ≤ `maxStatsConcurrency`; all 200 in `Latest()`), existing sampler tests unchanged.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/metrics ./internal/api` (dashboard top consumers may now include non-compose rows; adjust only test fixtures that assumed otherwise, and say which).
- [ ] **Step 5: Commit** — `feat: sample every running container, store compose history only`.

---

### Task 3: `GET /api/containers` — cached host-wide list

Spec §4.1, Review Focus 5.

**Files:**
- Create: `internal/api/containers.go`, `internal/api/containers_test.go`
- Modify: `internal/apitypes/types.go` (+ `types_test.go` key contract), `internal/api/server.go` (Options), `internal/cli/serve.go` (wiring)

**Interfaces:**
- Consumes: `ObserveAPI.Containers` (state cache), `ObserveAPI.Latest()`, the host collector's `RestartCounts()` (new `api.Options.Restarts interface{ RestartCounts() map[string]int }`, nil → zeros), `StacksAPI.List` for the project → stack map.
- Produces:
  ```go
  // apitypes
  type ContainerRow struct {
      ID, Name, Image, State, Status, Health string // ID = first 12 hex
      ExitCode *int `json:"exitCode"`; CreatedAt time.Time `json:"createdAt"`
      Project, Service, Stack, Managed string        // Managed: "stack" | "compose" | "none"
      Ports []string `json:"ports"`                  // "hostIP:hostPort->containerPort/proto", never null
      Networks map[string]string `json:"networks"`    // never null
      CPU *float64 `json:"cpu"`; Mem *uint64 `json:"mem"`; MemLimit *uint64 `json:"memLimit"`
      Restarts24h int `json:"restarts24h"`
  }
  // api (package-private)
  func containerManaged(c engine.Container, projectToStack map[string]string) (managed, stack string)
  func formatPorts(ps []engine.Port) []string // published only; v4/v6 duplicates collapsed; sorted by host port
  ```
- `engine.Container` needs `Created time.Time` if not present — add it in this task (decode `Created` unix seconds from `/containers/json`) with a test.

**Behaviour:** route `GET /api/containers` registered with `requireSession` when Stacks and Observe are set; body sorted by name; cached 2 s, single-flight on a detached context with a 10 s bound, panic-safe, errors not cached; state-cache error → 503 `{error}`; `StacksAPI.List` error → 500. JSON field names exactly spec §4.1 (camelCase).

- [ ] **Step 1: Write the failing tests** — `TestContainerManaged` table (registered project → stack + name; unregistered compose → compose; no label → none), `TestFormatPorts` (unpublished dropped, `0.0.0.0` and `::` duplicate collapsed, sorted, udp kept), `TestContainerListJoinsSources` (cache rows + Latest + restarts + stacks → exact JSON incl. nulls for stopped containers, `ports: []`, `networks: {}`), `TestContainerListCachedFor2s`, `TestContainerListSingleFlight50`, `TestContainerList503WhenDisconnected`, `TestContainerListRequiresSession`, apitypes key contract updated.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement** (+ serve.go passes the collector as `Restarts`).
- [ ] **Step 4: Run** `go test -race ./internal/api ./internal/apitypes ./internal/engine`, `go test ./...`, `go vet ./...`.
- [ ] **Step 5: Commit** — `feat: host-wide container list endpoint`.

---

### Task 4: Details and actions endpoints

Spec §4.2, §4.4, §6, §7; Review Focus 1–3.

**Files:** Modify `internal/api/containers.go` (+ test), `internal/apitypes/types.go`; `api.Options` gains `Engine` actions source if `ObserveSources` lacks it (`interface{ ContainerAction(...); InspectContainer(...) }` satisfied by `*engine.Client`) — wire in `serve.go`.

**Interfaces:**
- Produces: `apitypes.ContainerDetail` (spec §4.2 fields: id full, name, image, state, status, health, startedAt/finishedAt `*time.Time` (null when zero), exitCode `*int` (null while running), restartPolicy, restarts24h, project, service, stack, managed, ports, networks, mounts `[]ContainerMount{type, source, destination, readOnly}`, labels); `POST` returns `apitypes.ContainerRow`.

**Behaviour:**
- `GET /api/containers/{id}`: `{id}` must match `^[0-9a-f]{12,64}$` (else 404); inspect → `ContainerDetail` (managed/stack via `containerManaged` with a fresh `StacksAPI.List`; restarts from `RestartCounts`); `ErrNotFound` → 404; Docker unavailable → 503.
- `POST /api/containers/{id}/{action}` (`start|stop|restart` via three patterns or one validated segment; JSON-only): inspect the container → `ErrNotFound` 404; fresh `StacksAPI.List` → if `containerManaged` says `stack` → 409 `{error: "<name> belongs to stack <stack>; use the stack's actions"}`; else `ContainerAction(ctx(30 s), id, action, 10)`; `*ActionError` → 502 with its message; unavailable → 503; success (incl. 304) → re-inspect and return the row (CPU/mem from `Latest()`), 200. Log each action at Info with container name and action (no secrets).
- Body guard: the details JSON must not contain keys `env`, `cmd`, `entrypoint`, `user`, `hostConfig`.

- [ ] **Step 1: Write the failing tests** — `TestDetailsMapsInspect`, `TestDetailsBodyHasNoEnvOrCmd` (decode to `map[string]any`, assert key set equals the spec list), `TestDetailsBadIdIs404`, `TestDetailsUnavailableIs503`, `TestActionRefusesRegisteredStackContainer` (409, fake engine records no action), `TestActionGuardReadsRegistryEveryRequest` (stack registered between two requests → second is 409), `TestActionAllowsUnregisteredComposeProject`, `TestActionDockerRefusalIs502` (message verbatim), `TestActionAlreadyStoppedIsSuccess` (engine returns nil for 304 → 200 + row), `TestActionUnknownContainerIs404`, `TestActionRequiresJSON` (form post → 415/400 per existing rule), `TestActionRoutesRequireSession`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/api`, `go test ./...`, `go vet ./...`.
- [ ] **Step 5: Commit** — `feat: container details and guarded start/stop/restart`.

---

### Task 5: Container logs SSE

Spec §4.3.

**Files:** Modify `internal/api/observe.go` (extract `streamContainerLogs`), `internal/api/containers.go` (route), tests in `containers_test.go`; existing `observe_test.go` logs tests unchanged.

**Behaviour:** `GET /api/containers/{id}/logs` — validate id as in T4, find the container in `ObserveAPI.Containers` by id prefix (404 if absent; 503 when the cache errors), parse options with the existing `parseLogOptions`, then `streamContainerLogs(w, r, []engine.Container{c}, opts)` — identical event names, `id:` cursors, `Last-Event-ID` resume, end/error semantics as the stack route. `handleLogs` keeps its selection logic and calls the same helper.

- [ ] **Step 1: Write the failing tests** — `TestContainerLogsStreamsOneContainer` (fake logs for two containers → only the requested one's lines), `TestContainerLogsResumeFromCursor`, `TestContainerLogsUnknownIs404`, `TestContainerLogsRequiresSession`; run the existing stack logs tests to confirm they're unchanged.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement** (pure extraction first — stack logs tests green — then the new route).
- [ ] **Step 4: Run** `go test -race ./internal/api`.
- [ ] **Step 5: Commit** — `refactor: share the logs streaming core` + `feat: container logs stream` (two commits).

---

### Task 6: Web data layer and log source

Spec §4 (wire shapes), §5.3 (Logs tab).

**Files:** Modify `web/src/lib/api/types.ts`, `client.ts` (+ `client.test.ts`), `web/src/lib/stacks/logs.ts` (+ `logs.test.ts`), `LogsViewer.svelte` (+ `LogsViewer.test.ts`), callers in `StackDetail.svelte`.

**Interfaces:**
- Produces:
  ```ts
  export type Managed = 'stack' | 'compose' | 'none';
  export interface ContainerRow { id; name; image; state; status; health: ContainerHealth; exitCode: number | null; createdAt;
    project; service; stack; managed: Managed; ports: string[]; networks: Record<string, string>;
    cpu: number | null; mem: number | null; memLimit: number | null; restarts24h: number }
  export interface ContainerDetail { …spec §4.2… mounts: { type: string; source: string; destination: string; readOnly: boolean }[]; labels: Record<string, string> }
  api.containers(): Promise<ContainerRow[]>; api.container(id): Promise<ContainerDetail>;
  api.containerAction(id: string, action: 'start'|'stop'|'restart'): Promise<ContainerRow>;
  export type LogSource = { kind: 'stack'; name: string } | { kind: 'container'; id: string };
  createLogStream(source: LogSource, opts) // URL /api/stacks/<name>/logs or /api/containers/<id>/logs
  // LogsViewer props: { source: LogSource; services: string[]; service?: string }  (services [] hides the selector)
  ```

**Behaviour:** mirror Go JSON tags exactly (`internal/apitypes/types.go` is authoritative); actions POST `{}`; `StackDetail` passes `{ kind: 'stack', name }`; a container source never sends `service`.

- [ ] **Step 1: Write the failing tests** — client paths/methods/body; `createLogStream` URL per source and that a container source omits `service`; `LogsViewer` with a container source hides the Service select and opens the container URL; existing LogsViewer/logs tests updated only for the new prop shape.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `cd web && npm run check && npm test && npm run build`.
- [ ] **Step 5: Commit** — `feat: container API client and log source`.

---

### Task 7: Shell entry and the Containers list page

Spec §5.1, §5.2, §5.5; Global Constraints (refresh, a11y, responsive).

**Files:**
- Create: `web/src/lib/containers/list.ts` (+ `list.test.ts`), `web/src/lib/containers/ContainerActions.svelte` (+ test), `web/src/routes/ContainerList.svelte` (+ `ContainerList.test.ts`)
- Modify: `web/src/lib/shell/Rail.svelte`, `breadcrumb.ts`, `icons.ts` (+ `shell.test.ts`), `web/src/App.svelte` (+ `App.test.ts`)

**Interfaces:**
- Produces:
  ```ts
  // list.ts
  export type ContainerChip = 'all' | 'running' | 'stopped' | 'problems' | 'unmanaged';
  export function isProblem(c: ContainerRow): boolean   // unhealthy, restarting, dead, exited with exitCode ≠ 0
  export function sortContainers(rows: ContainerRow[]): ContainerRow[] // problems, running, stopped; then name
  export function filterContainers(rows, chip, query): ContainerRow[]   // query over name, image, stack/project, ports text
  export function chipCounts(rows): Record<ContainerChip, number>
  export function createContainerList(deps: { list: () => Promise<ContainerRow[]>; changes: Readable<LiveChange | null>; doc?: Document }):
    { rows: Readable<ContainerRow[] | undefined>; error: Readable<string | undefined>; refresh(): void; start(): void; stop(): void }
  // ContainerActions.svelte props: { row: ContainerRow; size?: 'icon' | 'md'; onchanged?: (row: ContainerRow) => void }
  ```

**Behaviour:**
- Rail: "Containers" after Stacks (`/containers`, current on `/containers` and `/containers/<name>`), box icon (stroke 1.8, 18 px, from the mock-up icon family); breadcrumb `Containers` / `Containers / <name>` (mono, decoded); App routes `/containers` and `/containers/:name` (the latter rendered by T8 — until then a temporary minimal placeholder is NOT allowed; register only `/containers` in this task and add the detail route in T8); titles "Containers · Stackstead".
- List page per spec §5.2 (heading counts, chips, search, table columns, ports `+N`, CPU/memory `—` when null, restarts warning ≥ 3, card layout ≤ 720 px).
- `ContainerActions`: `managed === 'stack'` → a "Manage in stack →" link to `/stacks/<stack>`; else Start (not running) or Stop (running) + Restart; Stop/Restart through `ConfirmDialog` naming the container; soft-disabled with reason when `$live.docker === 'unavailable'` or the cache is disconnected (`lifecycleDisabledReason` docker part) or an action on this row is in flight; on success toast "`<name>` stopped" etc. and `onchanged(row)`; on error toast the server message (409 → "belongs to stack …" text from the server).
- Refresh controller per the Decisions section (start/stop with the page; generation guard; in-flight skip; hidden → no timers).

- [ ] **Step 1: Write the failing tests** — `list.test.ts`: `isProblem` table, sort order, filter by each chip and query field, counts, controller (fetch on start, change debounce, resync immediate, 10 s interval visible only, pause/resume, stale response ignored, in-flight skip). `ContainerActions.test.ts`: stack row shows link not buttons; running non-stack shows Stop + Restart; stopped shows Start + Restart; confirm before stop/restart; disabled reason when Docker down; double click sends one request; 409 and 502 toasts. `ContainerList.test.ts`: renders rows sorted problems-first, chips/search, `—` for null CPU, ports `+N`, stack link vs unregistered tag vs dash, empty state, error + Retry. `shell.test.ts`: rail item and current state, breadcrumbs. `App.test.ts`: `/containers` renders the page and title.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm run check && npm test && npm run build`.
- [ ] **Step 5: Commit** — `feat: Containers list page`.

---

### Task 8: Container page

Spec §5.3, §5.5; Review Focus 4.

**Files:** Create `web/src/routes/ContainerDetail.svelte` (+ `ContainerDetail.test.ts`); modify `web/src/App.svelte` (route `/containers/:name`, title `<name> · Stackstead`) and `breadcrumb.ts` if not already handled.

**Behaviour:**
- Resolve `:name` (decoded) against `api.containers()` (shared refresh via `createContainerList` instance owned by the page); not found on first load → `EmptyState` "Container not found" + link to `/containers`; previously found and now absent after a refresh → "This container no longer exists" + Back link; same name with a new id → keep showing, switch id (details refetch, logs reconnect via `{#key id}`).
- Header per spec §5.3 (mono `h1`, StatusBadge, health, meta line with stack link / unregistered project / "not in a stack", `ContainerActions` with `size="md"` or "Open stack" + note for stack containers; full id + CopyButton "Copy id").
- Tabs `details` (default) and `logs` via `?tab=`; Details card from `api.container(id)` (refetch rules in Decisions), `<details>` for labels with `com.docker.compose.*` first, Resources · now box from the row (`—` when null); Logs tab `LogsViewer` with `{ kind: 'container', id }` and `services=[]`, mounted only while the tab is active.
- Focus to `h1` after navigation (App handles it); `role="group"` on actions; no horizontal scroll at 375 px.

- [ ] **Step 1: Write the failing tests** — not found; header meta per managed kind; stack container shows "Open stack" and no action buttons; non-stack shows actions; tabs switch and `?tab=logs` opens the logs viewer with the container URL; details render mounts/networks/labels and never an env field (fixture with extra keys ignored); `re-resolves the id after the container is recreated`; `shows no longer exists only when the name is gone`; copy id button.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm run check && npm test && npm run build`.
- [ ] **Step 5: Commit** — `feat: container page with details and logs`.

---

### Task 9: Links from existing screens

Spec §5.4.

**Files:** Modify `web/src/lib/dashboard/tiles.ts` (`TileModel.href?: string`) + `HostTile.svelte` (renders an `<a use:link>` card when `href` is set), `EventsFeed.svelte`, `TopConsumersCard.svelte`, `AlertsCard.svelte`, `web/src/lib/stacks/StackAside.svelte`, `ServiceCard.svelte` (+ their tests).

**Behaviour:** Containers tile → `/containers`; events feed container text, top-consumer container names, and alerts that have a container but no stack → `/containers/<encoded name>` (mono link, `use:link`); stack detail container names (aside and service cards) → `/containers/<name>`. Links keep existing text, tone and truncation; no layout change at 375 px.

- [ ] **Step 1: Write the failing tests** — one per component asserting the link and href; tile renders as a link only when `href` is set; existing tests stay green.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm run check && npm test && npm run build`.
- [ ] **Step 5: Commit** — `feat: link containers from the dashboard and stack pages`.

---

### Task 10: E2E, docs, final checks, visual pass

Spec §8 (E2E, visual), §9.

**Files:** Modify `cmd/stackstead/e2e_test.go`, `docs/api.md`, `docs/security.md`, `README.md`.

**Behaviour:**
- E2E (with Docker): `docker run -d --label io.stackstead.e2e=1 --name ss-e2e-plain busybox sleep 300` (cleanup in `t.Cleanup`, also removes on failure); `/api/containers` lists it with `managed:"none"`; `POST …/stop` → 200 and state `exited`; `POST …/start` → 200 and `running`; `POST …/stop` on the smoke stack's container → 409; `GET /api/containers/<id>` has no `env` key; `/api/containers/<id>/logs?tail=10&follow=false` delivers an `end` event; every route without a session → 401.
- Docs per spec §9 (api.md: five routes, fields, `managed`, caching, error codes, sampler change; security.md: §7 paragraph; README: one feature-list line, header untouched).
- Final checks: `make check`; `make ui` (budgets); `env PATH=/usr/bin:/bin:$(go env GOROOT)/bin go test ./...`; `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`; `GOOS=darwin go build ./...`; the e2e.
- Visual pass (headless Chrome, scratchpad config/data, a throwaway compose stack plus one plain container): `/containers` and `/containers/<name>` (Details and Logs) at 1280 and 375, dark and light; report issues, don't fix; clean up containers and stop processes.

- [ ] **Step 1: Write the e2e assertions**, run them (prove they can fail by a temporary wrong expectation if they pass first time, then restore).
- [ ] **Step 2: Write the docs.**
- [ ] **Step 3: Final checks and visual pass.**
- [ ] **Step 4: Commit** — `test: containers e2e` and `docs: containers screen API and security notes`.
- [ ] **Step 5: Hand off** for the Fable final review (after the user's "fix everything first" rule: any visual findings are fixed before that review), then merge to `main`.
