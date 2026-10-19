# Stackstead Plan 17a — React overview (a): shared pollers, running-ops store, overview derivations

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** tasks, names, signatures, test cases and examples only; no code bodies. Implementers write everything test-first.

**Goal:** Land everything the Stacks overview screen (Plan 17b) needs below the component layer, in `web/src/lib`, with the Svelte app still shipping and green: one shared host poller (the cross-plan ruling from 16b T3), a host sample history for the host-stats sparklines, one shared running-operations store with the backoff semantics `StackList.svelte` pins today, a dashboard controller that polls alerts, the metrics summary and the storage overview, and pure derivation modules for the status panel, stack towers, host stats, stack groups and issue cards. Plan 17b then only renders.

**Architecture:** `lib/dashboard/controller.ts` loses its `host` key (the Svelte `StackList` reads `lib/stores/host.ts` instead, so Svelte and React share one `/api/host` poller) and gains an optional `storage` key. `lib/stores/host.ts` additively records the last 30 snapshots as `hostHistory`. A new `lib/stores/runningOps.ts` owns the running-operations list for the whole React app (top bar pill, palette busy set, overview spinners) with debounced refetch on change events, an explicit refresh tick and backoff polling while anything runs; `useRunningOps` becomes a thin `useStore` wrapper. Pure modules `lib/dashboard/overview.ts`, `lib/dashboard/issues.ts` and additions to `lib/dashboard/tiles.ts` and `lib/stacks/summary.ts` turn store values into view models with no React or DOM.

**Tech Stack:** TypeScript strict, `$lib/store`, Vitest (Node, fake timers). No new dependencies. React 19.3.1 only in the one-line `useRunningOps` wrapper and the `App.tsx` wiring.

**Spec:** `docs/superpowers/specs/2026-10-19-stackstead-react-evolve-v2-design.md` §3 (no API change), §6.1 (store logic stays in lib), §6.4 (pure-module tests stay `.test.ts`), §8 "Issue cards come from `/api/alerts` plus storage reclaimable", §9 row 17. README "1. Stacks overview" (status panel, towers, host stats strip, issue cards, grouped list). Rulings: `.superpowers/sdd/rulings-cross-plan.md` lines 6 and 8 (shared host poller; entry ≤ 115 kB, size reported per task). **Depends on Plan 16b** (merged on this branch): `lib/stores/host.ts`, `needsAttention`, `useRunningOps`, `PaletteHost`.

## Spec divergences

Decisions scored with the TypeSafe `typesafe-ai` skill (Jev, `jev-1.13.0`); the chosen option and its probability are recorded so reviewers can disagree with the premise rather than the outcome.

1. **Per-stack sparklines and Top CPU come from `GET /api/metrics/summary`** polled every 30 s by the controller, as today (Jev 0.99). The README's "update every ~2s from the metrics stream" would need one live SSE stream per stack; the overview keeps the 30 s poll and the host strip ticks at the host poller's 10 s (16b divergence 3 already records this for the sidebar).
2. **"last event 3s ago"** is the newest entry of the eager `events` store rendered with `RelativeTime`; the fragment is omitted while there are no events (Jev 1.00).
3. **Host polling is shared by removing `host` from the dashboard controller** rather than extracting a generic poller (Jev 0.80). `createDashboard` becomes `{ alerts, summary, storage?, changes, doc? }`; `StackList.svelte` reads `lib/stores/host.ts`. The two controller host cases `host.test.ts` lacks move there. `errors.host` disappears; nothing read it.
4. **Running operations become a lib store** (`lib/stores/runningOps.ts`, Jev 0.93) so the top bar, the palette and the overview share one `/api/operations?state=running` poller. The Svelte list's backoff (2 s, 4 s, 8 s, capped 10 s, only while something runs) and the React hook's debounce on change events are both kept, in one place.
5. **Issue-card primary actions are stack-level with honest labels** (Jev 0.98): the API has no per-service action and `POST /api/containers/{id}/…` needs an id the alert does not carry and is documented for unmanaged containers only. `exited` → `Up stack`, `unhealthy` / `restart_loop` → `Restart stack`, `port_clash` / `override` → `Open stack`, `parse_error` → `Open compose`, storage → `Review cleanup`. Container-only alerts (no `stack`) link to `/containers/<name>`.
6. **`docker_disconnected` and `metrics_degraded` alerts are not issue cards** (Jev 0.77): the App banners already show them and they carry no stack.
7. **The storage card appears when summed `reclaimable` is at least 1 GiB (`RECLAIMABLE_CARD_MIN = 1024 ** 3` bytes)** — controller ruling overriding the planner's coin flip (Jev 0.53 vs 0.47): Docker hosts nearly always have some reclaimable bytes, so "any positive" would show the card permanently and train users to ignore it. Its tone is `info` and it sorts last.
8. **The status-panel label counts stacks, not services** (`degraded · 2 stacks`, Jev 0.86): `StackSummary` has no per-service state and alerts load later and may fail; counting stacks keeps the label consistent with the sidebar LED from first paint.
9. **Row sub-line** shows the failing service names from this stack's container alerts (`exited`, `unhealthy`, `restart_loop`) in warning colour when any, otherwise the compose path as today (Jev 0.94). Image names are not in `StackSummary`.
10. **The last-log-line box on issue cards is omitted** in Plan 17 (Jev 0.98): there is no tail endpoint and logs are SSE streams only. `Open logs` covers it; a later plan may add a one-shot tail.
11. **Groups replace the status chips** (Needs attention / Running / Stopped, plus Unknown only when present; Jev 0.99). The chip tests are ported as group tests in 17b. The search filter stays. 17a only provides `groupStacks`.
12. **Plan 17 is two PRs** (Jev 0.99): 17a (this file) is store and pure-module work reviewable without a screen; 17b is the screen.

## Global Constraints

- No API change (spec §3): every fetch is an existing `api.*` method; `types.ts` is untouched.
- Svelte keeps shipping: `make ui` (Svelte build), `make ui-test` and `make ui-check` stay green after every task; `StackList.test.ts` passes with its existing fixtures (it already serves `GET /api/host`).
- Store rules (16b): stores live in `lib`, React reads them through `useStore`; no React state mirrors a store; generation guards drop late responses after stop/hide; nothing polls while the tab is hidden; a failed fetch keeps the last value.
- Pure modules take plain values and return plain values; no `Date.now()` inside (pass `now`), no DOM, no React.
- Entry size: `npm run build:app` is run at the end of every task and its `bundle: entry … kB gz` line is recorded in the task report; 17a must not move it (nothing new is imported by the entry except `runningOps.ts`, which replaces hook code).
- Test-first, conventional commits, no code bodies in this plan.

## Review Focus

1. One `/api/host` poller for the whole app: with the Svelte list mounted, `GET /api/host` is requested once at mount and once per 10 s, not twice; with React, mounting the overview in 17b adds no second poller (17b checks the React side). (Task 1.)
2. Running-ops semantics preserved in one place: a burst of change events collapses into ≤ 2 fetches; while an operation runs the list is rechecked with backoff; polling stops the moment nothing runs; `refreshRunningOps()` fetches at once; stop clears the list and drops late responses. (Task 2.)
3. Issue-card derivation is exhaustive over the documented alert kinds, excludes the two banner kinds, never produces an `act` action for a container-only alert, and the storage card is built only from `reclaimable` figures. (Task 4.)
4. Status derivations degrade honestly: empty list, all-unknown (degraded), attention sets of 1 / 2 / 3+, towers whose counts do not add up (`running` includes unhealthy containers; `failed` beyond `serviceCount`). (Task 3.)
5. The controller's `storage` key is optional and off for Svelte: no `GET /api/storage` call from the Svelte list; with the key set, storage polls on the alerts interval and on visibility but not on change events. (Task 1.)

---

## File structure

| Area | Files | Tasks |
|---|---|---|
| Shared host poller + history | `web/src/lib/stores/host.ts` (+`host.test.ts`), `web/src/lib/dashboard/controller.ts` (+`controller.test.ts`), `web/src/routes/StackList.svelte` | T1 |
| Running operations store | `web/src/lib/stores/runningOps.ts` (new, +`runningOps.test.ts`), `web/src/app/hooks/useRunningOps.ts`, `web/src/app/App.tsx`, `web/src/app/App.test.tsx` | T2 |
| Overview derivations | `web/src/lib/dashboard/overview.ts` (new, +`overview.test.ts`), `web/src/lib/dashboard/tiles.ts` (+`tiles.test.ts`), `web/src/lib/stacks/summary.ts` (+`summary.test.ts`) | T3 |
| Issue cards | `web/src/lib/dashboard/issues.ts` (new, +`issues.test.ts`) | T4 |

Order: T1 → T2 → T3 → T4 (T3 and T4 are independent of each other). Sonnet implements test-first, Opus reviews each task, Fable reviews the branch.

---

### Task 1: One host poller, host history, controller without `host`, optional `storage`

Ruling (16b T3). Divergence 3. Review focus 1 and 5.

**Files:**
- Modify: `web/src/lib/stores/host.ts`, `host.test.ts`; `web/src/lib/dashboard/controller.ts`, `controller.test.ts`; `web/src/routes/StackList.svelte`

**Interfaces:**
- `host.ts` (additive): `export interface HostSample { t: number; cpu: number | null; mem: number | null; rx: number | null }` (`t` = `Date.parse(sampledAt)`, `cpu` = `cpu.percent`, `mem` = `memory.used`, `rx` = `network.rxBytesPerSec`; null when the section is null); `export const hostHistory: Readable<HostSample[]>` oldest-first, at most `HOST_HISTORY_MAX = 30` (five minutes at 10 s), appended on every successful fetch unless `sampledAt` equals the previous sample's, cleared with the value when the last holder releases. `startHost` and `host` unchanged.
- `controller.ts`: `interface DashboardDeps { alerts: () => Promise<Alert[]>; summary: () => Promise<MetricsSummary>; storage?: () => Promise<StorageOverview>; changes: Readable<LiveChange | null>; doc?: Document }`; `interface DashboardErrors { alerts?: string; summary?: string; storage?: string }`; return gains `storage: Readable<StorageOverview | undefined>` and loses `host`. Intervals: summary 30 s, alerts 60 s + debounced on change (flush on resync), storage 60 s (same tick as alerts) and on becoming visible, never on change events; all three fetched on `start()` when visible. Generation/busy semantics unchanged and applied to `storage`. With no `storage` dep the key is never fetched and `storage` stays `undefined`. New method `refreshAlerts(): void` — fetches alerts at once (the same flush a resync triggers), a no-op before `start()` and after `stop()`; 17b calls it after a successful lifecycle action.
- `StackList.svelte`: `createDashboard({ alerts: api.alerts, summary: api.metricsSummary, changes })`; `startHost({ host: api.host })` in the same `$effect` as `dashboard.start()`, released in its cleanup; `$hostStore` becomes `$host` from `$lib/stores/host`. No other change.

- [ ] **Step 1: Write the failing tests.** `host.test.ts` (+): `records one history sample per distinct sampledAt, oldest first, capped at 30`; `history null sections become null samples`; `history is cleared when the last holder releases`; moved from the controller: `a response that lands while the tab is hidden is ignored`, `a fetch slower than two intervals still lands and no second one starts meanwhile`. `controller.test.ts`: remove the host expectations from `fetches all three on start` (now `alerts` and `summary`, plus `storage` when given), `host every 10s…` becomes `summary every 30s alerts every 60s`, drop the host-specific stale cases moved above, keep `alerts refetch on change debounced and immediately on resync`, `pauses all timers while hidden and fetches once on visible` (now asserts alerts, summary and storage), `failed fetch keeps previous value and records error` (on `summary`), `stop clears timers and listener`, the two alerts stale-response cases; new: `storage is never fetched without a storage dep`, `storage fetches on start, every 60 s and on visible, but not on a change event` (focus 5), `a storage failure records errors.storage and keeps the value`, `refreshAlerts fetches alerts at once and is a no-op before start and after stop`. `StackList.test.ts` is unchanged and must stay green (its `dashboardFake` already serves `GET /api/host`); add one case there: `requests /api/host once at mount` (focus 1; counts `fake.calls` for `/api/host` after the first paint — exactly 1).
- [ ] **Step 2: Run** `cd web && npx vitest run src/lib/stores/host src/lib/dashboard/controller src/routes/StackList` — FAIL: `hostHistory` undefined, `storage` undefined, `host` still fetched twice.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make ui-test`, `make ui-check`, `make ui` (Svelte build and its budget), `npm run build:app` (record the `bundle: entry …` line; expected unchanged at 93.9 kB gz).
- [ ] **Step 5: Commit** `refactor(web): one host poller for Svelte and React, controller polls storage, host history for sparklines` (why: the overview must not run a second `/api/host` loop next to the sidebar's; moving the Svelte list onto the shared store proves the store is sufficient before React depends on it).

---

### Task 2: Running-operations store

Divergence 4. Review focus 2. Ports the Svelte list's backoff effect and the React hook's debounce into one store.

**Files:**
- Create: `web/src/lib/stores/runningOps.ts`, `runningOps.test.ts`
- Modify: `web/src/app/hooks/useRunningOps.ts`, `web/src/app/App.tsx`, `web/src/app/App.test.tsx`

**Interfaces:**
- `runningOps.ts`: `export const runningOps: Readable<Operation[]>`; `export function runningStackNames(ops: Operation[]): Set<string>` (pure); `export function startRunningOps(deps: { list: () => Promise<Operation[]>; changes: Readable<LiveChange | null>; refresh: Readable<number> }): () => void` — on start: immediate fetch; `changes`: the replayed current value is skipped, a `resync` flushes, any other change triggers the 300 ms `debounce` from `$lib/stores/debounce`; `refresh` (the existing `operationsRefresh` tick): fetch at once and reset the backoff attempt; after every successful response with at least one running operation a recheck is scheduled at `min(2000 · 2^attempt, 10000)` ms with `attempt` incremented, and `attempt` resets to 0 whenever a change or refresh starts a fetch; a response with none running schedules nothing; a failed fetch schedules a retry only when the previous response had running operations; sequence guard drops out-of-order responses; `stop()` cancels the debounce and timers, bumps the sequence and sets `[]`; idempotent start (a second start replaces the first, as `startStacks` does).
- `useRunningOps.ts`: `export function useRunningOps(): Operation[]` = `useStore(runningOps)`; the `authenticated` parameter is removed. `App.tsx` starts the store in the authenticated effect (`startRunningOps({ list: api.runningOperations, changes, refresh: operationsRefresh })`, stopped with the others) and calls the hook without arguments; `PaletteHost`/`CommandPalette` props (`runningOps: Operation[]`) are unchanged.

- [ ] **Step 1: Write the failing tests.** `runningOps.test.ts` (fake timers; fake `list` with a mutable result): `fetches once on start and is empty after stop`; `a burst of ten changes collapses into one debounced fetch, a resync fetches at once` (focus 2); `while an operation runs it rechecks at 2 s, 4 s, 8 s then every 10 s, and stops once nothing runs` (focus 2; counts calls after the result flips to `[]`, then advances 30 s and asserts no further call); `refreshRunningOps fetches immediately and resets the backoff`; `a failed recheck retries only while tracking a running operation`; `a late response after stop is dropped and the list stays empty`; `runningStackNames returns the distinct stack names`. `App.test.tsx`: `collapses a burst of changes into at most two running-operations refetches` and `shows the running operations count in the nav` stay as written and must pass against the store; new `stops polling operations after a session loss` (the 401 fixture with a running operation in the fake: once Login is shown, count `/api/operations` calls, wait 500 ms with the file's `sleep` helper, assert the count is unchanged). `CommandPalette.test.tsx` and `PaletteHost.test.tsx` unchanged.
- [ ] **Step 2: Run** `cd web && npx vitest run src/lib/stores/runningOps src/app/App` — FAIL: module missing; hook signature.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make ui-test`, `make ui-check`, `npm run build:app` (record the entry line; expected within ±0.5 kB of 93.9).
- [ ] **Step 5: Commit** `feat(web): shared running-operations store with backoff polling` (why: the overview, the top bar and the palette must agree on which stacks are busy; one poller with the Svelte list's backoff rules replaces a hook and a per-screen effect).

---

### Task 3: Overview derivations — status panel, towers, groups, host stats

README "Status panel card", "stack towers", "host stats strip", "Stacks list grouped". Divergences 2, 8, 11. Review focus 4.

**Files:**
- Create: `web/src/lib/dashboard/overview.ts`, `overview.test.ts`
- Modify: `web/src/lib/dashboard/tiles.ts`, `tiles.test.ts`; `web/src/lib/stacks/summary.ts`, `summary.test.ts`

**Interfaces:**
- `summary.ts` (additive): `export function stackNeedsAttention(s: StackSummary): boolean` (the existing private rule, exported) and `export function attentionStacks(items: StackSummary[]): StackSummary[]` (ordered `unavailable` → `partial` → health-only attention, then by name).
- `overview.ts`:
  - `export type PanelTone = 'danger' | 'success' | 'muted'`; `export function panelLabel(items: StackSummary[]): { text: string; tone: PanelTone; pulse: boolean }` — `[]` → `no stacks` muted; every stack `unknown` → `status unknown` muted; attention `n > 0` → `degraded · n stack` / `degraded · n stacks` danger + pulse; all `running` → `all running · n stacks` success; otherwise `healthy · r of n running` success (`r` = stacks with status `running`).
  - `export function statusSentence(items: StackSummary[]): string` — `No stacks yet`; `Stack status is unavailable` (all unknown); one attention stack → `<name> needs attention`; two → `<a> and <b> need attention`; three or more → `<a> and <k> more need attention` (`k = n − 1`); none with every stack running → `All stacks are running`; none otherwise → `Nothing needs attention`. Names come from `attentionStacks`.
  - `export function statusFragments(items: StackSummary[], host: HostSnapshot | undefined): string[]` — `<fully> of <n> stacks fully running` (`fully` = status `running`; singular `stack` when `n === 1`), then `<running> of <total> containers up` only when `host?.containers` is set. The last-event fragment is the component's (needs `RelativeTime`).
  - `export type TowerBlock = 'up' | 'unhealthy' | 'failed' | 'down' | 'off'`; `export function towerBlocks(s: StackSummary): TowerBlock[]` — length `serviceCount` (0 → `[]`), bottom-up order `up`, `unhealthy`, `failed`, then `down` (running/partial/unknown/unavailable stack) or `off` (stopped stack); counts: `unhealthy = min(serviceCount, health.unhealthy)`, `failed = min(remaining, health.failed)`, `up = clamp(running − unhealthy, 0, remaining)`, the rest `down`/`off`. `export function towerLabelTone(s): 'warning' | 'faint' | 'muted'` (attention → warning, stopped → faint, else muted). `export function towerTitle(s): string` = `<name> · <status>`.
  - `export type StackGroupId = 'attention' | 'running' | 'stopped' | 'unknown'`; `export interface StackGroup { id: StackGroupId; title: 'Needs attention' | 'Running' | 'Stopped' | 'Unknown'; items: StackSummary[] }`; `export function groupStacks(items: StackSummary[]): StackGroup[]` — attention = `stackNeedsAttention`; running = status `running` and not attention; stopped = `stopped`; unknown = everything else; items sorted by name; empty groups omitted; fixed order.
  - `export function rowPrimaryAction(s: StackSummary): { action: 'up' | 'restart'; label: 'Up' | 'Restart' }` — `up` for `stopped`, `unavailable`, `unknown`; `restart` for `running`, `partial`.
  - `export function filterByQuery(items: StackSummary[], query: string): StackSummary[]` = `filterStacks(items, 'all', query)` (name, project or path, case-insensitive).
- `tiles.ts` (additive): `export interface HostStat { id: 'cpu' | 'memory' | 'disk' | 'network' | 'containers' | 'uptime'; label: string; aux?: string; auxTone?: 'muted' | 'danger'; value: string; sub?: string; subTone?: 'muted' | 'warning' | 'danger'; series: (number | null)[]; tone: Sparkline tone; href?: string }`; `export function hostStats(h: HostSnapshot, history: HostSample[], now: Date): HostStat[]` in README order, one per non-null section: CPU (aux `<cores> cores` or `containers only` for docker source, value `formatPercent`, sub `load <l1> <l5> <l15>` when `uptime` is set, series = history cpu, tone accent); Memory (aux `formatBytes(total)`, value `formatBytes(used)`, sub `swap <used> / <total>` or `no swap` or `containers only`, series = history mem, tone info); Disk (aux `formatBytes(total)`, value `formatBytes(free)`, sub `free · <path> <used%> used`, series `[]`, tone warning at ≥ 75 % used else accent); Network (aux interface, value `formatRate(rx)` or `—`, sub `↑ <tx>` or `—`, series = history rx, tone info); Containers (aux `<bad> bad` danger when `bad > 0`, value `<running>/<total>`, sub `<restarts24h.total> restarts · 24h` when set, series `[]`, tone accent, href `/containers`); Uptime (value `formatDuration`, sub `since <date>` from `now − seconds` formatted `d MMM HH:mm` in local time, series `[]`, tone accent). `hostTiles` is left untouched for the Svelte list.

- [ ] **Step 1: Write the failing tests.** `summary.test.ts` (+): `attentionStacks orders unavailable, partial, then health-only, each by name`. `overview.test.ts`: `panelLabel: no stacks, all unknown, degraded singular and plural, all running, healthy with stopped`; `statusSentence for 0, 1, 2 and 3+ attention stacks, all running, some stopped, all unknown` (focus 4); `statusFragments omit the containers fragment without a host snapshot and use singular for one stack`; `towerBlocks: running stack 3/3 healthy → three up`; `partial with one unhealthy and one exited → up, unhealthy, down bottom-up` (`running: 2, serviceCount: 3, health.unhealthy: 1, failed: 0`); `stopped stack → all off`; `counts that exceed serviceCount are clamped` (focus 4; `failed: 9, serviceCount: 2`); `groupStacks puts health-only attention in Needs attention, omits empty groups and sorts by name`; `rowPrimaryAction table`; `filterByQuery matches name, project and path case-insensitively`. `tiles.test.ts` (+ `describe('hostStats')`): `six stats in README order, null sections omitted`; `series come from history and are empty for disk, containers and uptime`; `disk turns warning at 75 % used`; `containers aux is danger with bad > 0 and the sub carries restarts`; `uptime sub is since <date>` (fixed `now`); `docker source says containers only on cpu and memory`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/lib/dashboard/overview src/lib/dashboard/tiles src/lib/stacks/summary` — FAIL: unresolved imports.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make ui-test`, `make ui-check`, `npm run build:app` (entry line; unchanged — nothing imports these yet).
- [ ] **Step 5: Commit** `feat(web): pure overview derivations — status panel, towers, groups, host stats` (why: the sentence, the tower counts and the grouping are the overview's logic; pinning them without a DOM keeps 17b's component tests about rendering only).

---

### Task 4: Issue-card derivation

Spec §8 "Issue cards"; README issue cards. Divergences 5, 6, 7, 9. Review focus 3.

**Files:**
- Create: `web/src/lib/dashboard/issues.ts`, `issues.test.ts`

**Interfaces:**
- `export type IssueAction = { label: string; kind: 'navigate'; href: string } | { label: string; kind: 'act'; stack: string; action: 'up' | 'restart' }`
- `export interface IssueCard { id: string; kind: string; tone: 'danger' | 'warning' | 'info'; pulse: boolean; where?: { label: string; href: string; mono: boolean }; since?: string; title: string; message: string; primary?: IssueAction; secondary?: IssueAction }`
- `export const EXCLUDED_ALERT_KINDS: ReadonlySet<string>` = `docker_disconnected`, `metrics_degraded`.
- `export function issueCards(input: { alerts: Alert[] | undefined; storage: StorageOverview | undefined }): IssueCard[] | undefined` — `undefined` while `alerts` is undefined (not loaded or failed: say nothing, as `AlertsCard` does today); otherwise alert cards in server order, excluded kinds dropped, then the storage card when `reclaimableTotal(storage) >= RECLAIMABLE_CARD_MIN` (exported const, 1024 ** 3). Per kind (stack alerts): `exited` → title `<service> exited`, primary act `up` `Up stack`, no pulse; `unhealthy` → title `<service> is unhealthy`, primary act `restart` `Restart stack`, pulse; `restart_loop` → title `<service> is restart looping`, primary act `restart` `Restart stack`; `port_clash` → title `Port conflict`, primary navigate `/stacks/<stack>` `Open stack`; `override` → title `Override file ignored`, same primary; `parse_error` → title `Compose file cannot be parsed`, primary navigate `/stacks/<stack>?tab=compose` `Open compose`; unknown kinds → title = kind with underscores as spaces, primary `Open stack` when `stack` is set. `secondary` = `Open logs` → `/stacks/<stack>?tab=logs&service=<service>` when both are set. `where` = `{ label: stack, href: /stacks/<stack>, mono: true }`; container-only alerts (no `stack`, `container` set) → `where` is the container (`/containers/<container>`), primary navigate `Open container`, never `act`. `message` = `alert.message`; `since` = `alert.since`; `tone` from severity; `id` = `kind|stack|container|since|index`. Storage card: `kind: 'reclaimable'`, tone info, `where` `{ label: 'storage', href: '/storage', mono: false }`, no `since`, title `<formatBytes(total)> can be freed`, message = the positive kinds joined by ` · ` as `<size> dangling images`, `<size> container layers`, `<size> unused volumes`, `<size> build cache`, primary navigate `/storage` `Review cleanup`, no secondary. All hrefs use `encodeURIComponent` on names.
- `export function reclaimableTotal(storage: StorageOverview | undefined): number` — sum of non-null `reclaimable` across the four kinds, 0 when undefined.
- `export function failingServices(alerts: Alert[] | undefined, stack: string): string[]` — distinct `service` values of `exited` / `unhealthy` / `restart_loop` alerts for that stack, in alert order (divergence 9).

- [ ] **Step 1: Write the failing tests.** `issues.test.ts`: `undefined until alerts are loaded, and an empty list with no alerts and nothing reclaimable`; `every documented kind maps to its primary action and title` (focus 3; a table over the eight kinds from `docs/api.md`, asserting the two banner kinds are absent); `unhealthy pulses, exited does not`; `Open logs only when stack and service are known`; `a container-only alert links to the container and never acts` (focus 3; `stack` undefined, `container: 'lone one'` → href `/containers/lone%20one`); `names with spaces and # are encoded in hrefs`; `the storage card sums reclaimable, lists only positive kinds, sorts last, appears at exactly 1 GiB and is absent at 1 GiB − 1 byte, at zero or with null figures` (focus 3); `reclaimableTotal treats null as 0`; `failingServices is distinct, per stack, and ignores non-container kinds`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/lib/dashboard/issues` — FAIL: unresolved import.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make check` (Go + UI; the React shape test runs after `build:app`), `npm run build:app` (entry line; unchanged).
- [ ] **Step 5: Commit** `feat(web): derive overview issue cards from alerts and storage reclaimable` (why: the mapping from alert kind to the one action the API can actually perform is policy; it is pinned here so the card component in 17b only renders).

---

## Self-review notes

- Spec coverage: §3 no API change (every dep is an existing `api.*` method; `types.ts` untouched); §6.1 store logic in lib (T1, T2); §6.4 pure tests stay `.test.ts` (all tasks); §8 issue cards from alerts + storage reclaimable (T4); §9 row 17 (first half); README status panel/towers/host strip/grouped list → T3, issue cards → T4; ruling line 6 → T1; ruling line 8 → entry line recorded in every task.
- No code bodies: signatures, shapes, tables and test names only.
- Type consistency: `HostSample` (T1) is what `hostStats` (T3) consumes; `IssueAction.action` is the `'up' | 'restart'` subset of `Action` and matches `rowPrimaryAction` (T3), so 17b's `useStackAction` serves both; `StackGroup.items` are `StackSummary[]`, the same rows `towerBlocks` and `rowPrimaryAction` take; `runningStackNames` (T2) returns the `Set<string>` `lifecycleDisabledReason`'s `busy` is derived from; `DashboardDeps.storage` returns `StorageOverview`, the input of `issueCards` (T4).
- Review focus: 1 and 5 → T1 tests; 2 → T2 tests; 3 → T4 tests; 4 → T3 tests.
- Proportion: T1 touches Svelte and is the riskiest, so it is first and the Svelte list test gains the one assertion that matters; T2 is the only change to the React app and keeps the 16b App tests as its spec; T3/T4 are pure and small.
