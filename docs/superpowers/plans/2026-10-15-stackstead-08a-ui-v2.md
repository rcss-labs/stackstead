# Stackstead Plan 8a — UI v2: restyle, shell, components, detail page, list table

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** this plan specifies *what* to build: files, signatures, behaviour, test cases and acceptance criteria. It deliberately contains no full implementations; implementers write the code test-first. Short snippets show shapes only.

**Goal:** The web UI looks and behaves like the v2 mock-up (dense operator console: icon rail, top bar with status pills, dark/light/system theme and two densities, restyled routes and components, a stack table with health, ports and inline actions, a stack detail page with an aside of containers, current resources and recent operations), backed by the small §10.1 wire additions. Nothing the Plan 5 UI does is lost.

**Architecture:**
- Colours, fonts and sizes become CSS tokens on `:root` in `web/src/app.css` (spec §4.1), switched by `data-theme` / `data-density` on `<html>`. A ~300-byte classic script `web/public/theme.js` sets those attributes before first paint, so the strict CSP stays unchanged. A small `appearance` store owns the runtime choice and its `localStorage` persistence.
- `Nav.svelte` is replaced by `Rail.svelte` + `TopBar.svelte`; `App.svelte` keeps every guard, effect and dialog it has today and only changes layout.
- Pure helpers carry the logic the new screens need (breadcrumb, lifecycle disabled reason, health/ports cell text, service state tone, changed compose lines, media query store) so components stay thin and tests hit behaviour, not markup.
- The detail page owns one live-metrics subscription (`createStackLiveMetrics`) shared by the aside and the Metrics tab, extracted from today's `createMetricsController`.
- Go changes are §10.1 only: container health and stack health counts, declared host ports on the summary, Docker version and metrics settings on `/api/status`, `font/woff2` MIME.

**Tech Stack:** Svelte 5, Vite, TypeScript, Vitest + @testing-library/svelte (jsdom), CodeMirror 6 (`@codemirror/merge` `diff()` for changed lines), uPlot. Go 1.26 stdlib only. No new npm or Go dependencies. Fonts: IBM Plex Sans and Geist Mono, OFL-1.1, self-hosted woff2.

**Spec:** `docs/superpowers/specs/2026-10-15-stackstead-ui-v2-design.md` — this plan implements §13 "8a": §3 decisions, §4 tokens/typography/contrast, §5 theme/density/CSP, §6 shell and screens minus the 8b items, §7 accessibility, §8 behaviour mapping, §9 responsive rules, §10.1 API, §11 (8a rows), §12 tests (8a parts), §14 docs (8a parts). Mock-up: `docs/Stackstead DevOps UI redesign/Stackstead v2.dc.html` (icon path data, exact spacing when the spec is silent).

### Plan series

1–7 merged. **8a (this plan)** ships first and merges to `main` after the final review. 8b (dashboard data: `/api/host`, `/api/alerts`, `/api/metrics/summary`, `event` messages, host tiles, sparklines, alerts, top consumers, events feed) is a separate plan; its backend may start in parallel on its own branch and rebases after 8a merges.

## Decisions (controller, recorded for the user)

The TypeSafe skill is an SDK for an external judgment API and cannot run here without network access and credentials, so these decisions are argued in writing (as Plans 5–7 did). The spec's §3 table holds the design decisions; these are the plan-level ones.

- **Health and ports are computed per project, not per service.** `health` counts every container carrying the stack's compose project label, including orphans and including when the compose file does not parse (the containers are real even if the file is broken). `ports` comes only from the file, so it is `[]` on a parse error. This keeps the list honest about a broken stack whose containers are still running.
- **`engine.Container.Health()` returns `"none"`, never `""`.** The state cache's `view.Health` takes the same value; the cache only compares views for equality, so the vocabulary change cannot alter change detection.
- **`StatusFunc` returns a struct.** `type StatusFunc func(ctx) StatusReport` with `Docker, DockerVersion, Compose string` replaces the two-string return; only `serve.go` and the api tests construct one. The probe calls a new `engine.Client.Version(ctx)` (`GET /version`, unversioned path); if that fails it falls back to `Ping` so an engine or proxy that refuses `/version` still reports `docker: ok` with an empty version.
- **One shared live-metrics source per detail page.** Today `createMetricsController` opens `/metrics/live` itself. The SSE, aggregator, `dropped` and idle-flush code moves to `createStackLiveMetrics` (ref-counted `acquire()`), and the controller takes an optional `live` dependency; without it the controller builds and acquires its own, so every existing `metrics.test.ts` case keeps working unchanged.
- **Lifecycle disabled reason becomes one helper** (`lifecycleDisabledReason`) used by `ActionsBar` (detail) and the list's inline actions, so the two can never disagree.
- **Font files come from the Fontsource npm tarballs**, fetched with `npm pack` into the scratchpad (not added as dependencies): they ship latin-subset woff2 files and the OFL text. Package name, exact version and sha256 of each copied file go in the commit message.
- **Changed-line highlighting is tested through the pure `changedLines()` helper and the CodeMirror decoration**, not through the fallback textarea (spec §12 says "fallback textarea path", but a textarea cannot carry line decorations; the helper is where the logic lives).
- **Operation durations tick once a second only while a running operation is on screen.** Spec §11 "no new timers on the detail page" is about data polling; this ticker fetches nothing and stops when no running row is rendered.
- **Logo rasters are made with `sips`** (macOS, preinstalled): `sips --resampleWidth 336` / `68`, then checked against the size caps by a test. If a cap is missed, re-encode with a lower width rather than adding a tool.

## Global Constraints

- Spec §1: every existing route, API call, keyboard path, ARIA role and Vitest/Go test behaviour still works; tests change only where they assert markup the restyle replaces (CSS classes, column order, visible label text the spec changes), never where they assert behaviour.
- Spec §1 / §4.3: dark and light themes pass WCAG AA (4.5:1 text, 3:1 UI components) for every token pair used; token values are exactly those in spec §4.1 (including the §4.3 fixes: dark `--faint #848d9c`, dark `--hover #1f2530`, light `--faint #626c7a`, light `--accent #047857`, light `--warning #9c4707`, `--border-input #5b6573` / `#78828f`).
- Spec §5: theme and density apply before first paint; CSP string unchanged: `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`. No inline scripts, no third-party origins.
- Spec §3: `localStorage` keys `stackstead.theme` (`dark`|`light`, absent = system) and `stackstead.density` (`comfortable`, absent = compact); every access inside `try/catch`. `?theme=` is a one-page-load override and is never persisted.
- Budgets: entry JS ≤ 120 kB gz, total JS ≤ 400 kB gz, fonts (`dist/assets/*.woff2`) ≤ 200 kB; CodeMirror and uPlot stay lazy. `logo-art.png` ≤ 40 kB at 336 px wide; `containers.png` ≤ 8 kB at 68 px.
- No new npm or Go dependencies. `go test ./...` passes without Node; `CGO_ENABLED=0 go build ./...` passes with and without a built UI.
- Old token names `--surface-raised`, `--accent-contrast`, `--font-sans`, `--font-mono` must not appear anywhere under `web/src` after Task 2.
- No `{@html}` (the existing grep test stays green). Untrusted text (container names, paths, log and operation lines) is only interpolated.
- 8a renders no 8b sections: no hostname on the Stacks heading, no host tiles, no CPU/Memory/Net columns, nothing below the table. No placeholders, no dash columns for missing data.
- Test-first for every behaviour change: write the test, run it, see it fail for the expected reason, implement, pass, commit. Pure restyles covered by existing tests need no new test; run the affected suite.
- `make check` (svelte-check, Vitest, `go vet`, gofmt, Go tests) and `make ui` (build + budgets) pass at the end of every task that touches their inputs.
- Commits: conventional, explain why, and end with exactly these two trailer lines:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01ADj7g1VRggm8Nhf44kQ2F9
  ```

## Review Focus

1. **Storage blocked, cleared, or overridden by `?theme=`.** With `localStorage` throwing (Safari private mode, blocked site data) the page still follows the OS theme with no flash and the toggles still work for the page's lifetime; `?theme=light` shows light for that load only and a reload without it returns to the stored choice. (T2 `theme.js survives a throwing localStorage`, T2 `theme.js query override wins but is not stored`, T4 `setTheme with throwing storage still updates the DOM`, T4 `query override is visible but never written`)
2. **A stack whose compose file no longer parses, or uses odd port specs.** Health counts still reflect its running containers; ports are `[]`; ranges render `8000-8010`, protocols are dropped, duplicates across services appear once, `${PORT}` interpolations and container-only ports are skipped, order is numeric (`80` before `443` before `8096`). (T1 `TestDescribeHealthCountsOnParseError`, T1 `TestDeclaredPorts` table)
3. **Inline list actions racing operations or a degraded daemon.** Up/Down/Restart are soft-disabled with the same reason the detail page shows (compose missing, Docker unreachable, stack unavailable, operation running on that stack); a double click sends one request; an API error toasts like the detail page and leaves the row usable. (T14 `inline action is disabled while an operation runs on that stack`, T14 `double click sends one act`, T14 `act failure toasts and re-enables`)
4. **Narrow windows and tab switches on the detail page.** Below 1100 px the aside is not rendered and no live-metrics stream opens unless the Metrics tab is active; widening the window or switching to Metrics opens exactly one stream; leaving both closes it. (T15 `no live stream when narrow and not on Metrics`, T15 `one stream shared by aside and Metrics tab`, T8 `acquire is ref-counted`)
5. **Docker down or an old daemon at boot.** The Docker pill reads `Docker unavailable` with a danger dot when `docker !== 'ok'`, plain `Docker` when ok but the version is empty, and `Docker 27.1.1` otherwise; the server never fails `/api/status` because `/version` failed. (T3 `TestStatusProbeFallsBackToPing`, T6 `docker pill reflects version and availability`)

---

## File Structure

```
web/
  index.html                         + <script src="/theme.js">, font preloads (T2)
  public/theme.js                    first-paint theme/density (T2)
  public/logo-art.png, containers.png  downscaled logos (T2)
  public/favicon.svg                 recoloured #34d399 (T2)
  public/licenses/IBMPlexSans-OFL.txt, GeistMono-OFL.txt (T2)
  scripts/check-bundle.mjs           + FONT_BUDGET (T2)
  src/app.css                        tokens §4.1, typography §4.2, @font-face, reduced motion (T2)
  src/assets/fonts/*.woff2 + OFL-*.txt (T2)
  src/main.ts                        + initAppearance() before mount (T4)
  src/App.svelte                     layout: Rail + TopBar + banners + main (T6)
  src/lib/api/types.ts               + ContainerHealth, HealthCounts, ports, Status fields (T1, T3)
  src/lib/stores/live.ts             + dockerVersion, metrics settings (T3)
  src/lib/stores/appearance.ts       theme/density store (T4)
  src/lib/stores/media.ts            mediaQuery(query) readable (T15)
  src/lib/shell/Rail.svelte, TopBar.svelte, breadcrumb.ts, icons.ts (T6)
  src/lib/components/*.svelte        restyled; Button size/icon, StatusBadge tones, Field mono, Tabs badges (T5)
  src/lib/components/Nav.svelte      deleted (T6)
  src/lib/stacks/lifecycle.ts        lifecycleDisabledReason() (T5)
  src/lib/stacks/summary.ts          healthCell(), portChips(), serviceState() (T14, T15)
  src/lib/stacks/liveMetrics.ts      createStackLiveMetrics() (T8)
  src/lib/stacks/metrics.ts          controller takes optional live source (T8)
  src/lib/stacks/MetricsPanel.svelte, MetricsChart.svelte  restyle, range buttons, series tokens (T8)
  src/lib/stacks/changedLines.ts     changedLines(original, draft) (T9)
  src/lib/stacks/cm.ts, YamlEditor.svelte, ComposeEditor.svelte, DiffView.svelte  (T9)
  src/lib/stacks/LogsViewer.svelte   restyle + initial service prop (T10)
  src/lib/stacks/OperationsList.svelte  new, extracted from StackDetail (T11)
  src/lib/stacks/OperationPanel.svelte  restyle, duration, Copy output (T11)
  src/lib/stacks/ServiceForm.svelte, NetworkPicker.svelte, FileBrowser.svelte  restyle (T12)
  src/lib/stacks/StackAside.svelte   new aside (T15)
  src/lib/stacks/ServiceCard.svelte  new card, extracted from StackDetail (T15)
  src/lib/settings/PasswordForm.svelte  restyle (T13)
  src/routes/*.svelte                restyled per spec §6 (T7, T12, T13, T14, T15)
internal/engine/types.go             Container.Health(), ExitCode() (T1)
internal/engine/client.go            Client.Version(ctx) (T3)
internal/state/cache.go              view uses Health()/ExitCode() (T1)
internal/stacks/service.go           ContainerInfo.Health, Summary.Health/Ports, countHealth, declaredPorts (T1)
internal/apitypes/types.go           HealthCounts, health/ports fields; Status fields (T1, T3)
internal/api/stacks_dto.go           mapping (T1)
internal/api/stacks.go, server.go    StatusReport, Options.MetricsInterval/Retention (T3)
internal/api/spa.go                  font/woff2 MIME (T2)
internal/cli/serve.go                probe + options wiring (T3)
internal/cli/cmd_login.go            status prints docker version (T3)
cmd/stackstead/e2e_test.go           theme screenshots, content types (T16)
docs/api.md, development.md, security.md, cli.md, README.md (T3, T16)
```

## Execution waves

Tasks in one wave touch disjoint files and may run as parallel subagents in separate worktrees. Shared names (TS types, store and helper signatures) are pinned in each task's **Interfaces** block; a task must not create a file another task owns.

| Wave | Tasks | Depends on |
|---|---|---|
| 1 | T1 Go health + ports, T2 tokens/fonts/theme.js/budgets | — (T1: `internal/engine/types.go`, `internal/state`, `internal/stacks/service.go`, `internal/apitypes`, `internal/api/stacks_dto.go` + tests, `web/src/lib/api/types.ts` health/ports block only. T2: `web/index.html`, `web/public/*`, `web/scripts/check-bundle.mjs`, `web/src/bundle.test.ts`, `web/src/app.css`, `web/src/assets/fonts`, `internal/api/spa.go` + `server_test.go`, and the mechanical token-rename sweep inside `web/src/**/*.svelte`/`cm.ts`) |
| 2 | T3 Go status, T4 appearance store, T5 shared components | T3: T1 (same `apitypes`/`types.ts` files). T4: T2. T5: T2 |
| 3 | T6 shell, T7 Login/Setup, T8 live metrics + Metrics tab, T9 Compose tab, T10 Logs, T11 Operations, T12 New/Import, T13 Settings | T6: T3, T4, T5. T13: T3, T4, T5. Others: T5 |
| 4 | T14 Stacks list, T15 Stack detail | T14: T1, T5, T6. T15: T1, T5, T8, T9, T10, T11 |
| 5 | T16 E2E + docs | all |

Every subagent: Sonnet implements test-first; Opus reviews each task (tests written first, behaviour covered, affected suite green); Fable does the final whole-branch review (spec coverage, contrast table, budgets, e2e).

---

### Task 1: Container health, stack health counts and declared ports (Go + types)

Spec §3 (Container health, Declared ports), §10.1.

**Files:**
- Modify: `internal/engine/types.go` (+ `internal/engine/client_test.go` or a new `types_test.go`)
- Modify: `internal/state/cache.go` (`project()` uses the new methods; `cache_test.go` unchanged unless it asserts `Health == ""`)
- Modify: `internal/stacks/service.go` (+ `service_test.go`)
- Modify: `internal/apitypes/types.go`, `internal/api/stacks_dto.go` (+ `stacks_test.go`, `client_contract_test.go`)
- Modify: `web/src/lib/api/types.ts` (+ fixtures in existing tests that build `StackSummary` / `ContainerInfo` objects)

**Interfaces:**
- Produces (Go):
  ```go
  // engine
  func (c Container) Health() string        // "healthy" | "unhealthy" | "starting" | "none"
  func (c Container) ExitCode() (int, bool) // from "Exited (N)", ok=false otherwise
  // stacks
  type ContainerInfo struct{ ID, Name, Service, State, Status, Health string }
  type HealthCounts struct{ Healthy, Unhealthy, Starting, None, Failed int }
  // Summary gains: Health HealthCounts; Ports []string
  // apitypes
  type HealthCounts struct {
      Healthy int `json:"healthy"`; Unhealthy int `json:"unhealthy"`; Starting int `json:"starting"`
      None int `json:"none"`; Failed int `json:"failed"`
  }
  // StackSummary and StackDetail gain: Health HealthCounts `json:"health"`; Ports []string `json:"ports"`
  // ContainerInfo gains: Health string `json:"health"`
  ```
- Produces (TS, `types.ts`):
  ```ts
  export type ContainerHealth = 'healthy' | 'unhealthy' | 'starting' | 'none';
  export interface HealthCounts { healthy: number; unhealthy: number; starting: number; none: number; failed: number }
  // StackSummary: + health: HealthCounts; ports: string[]
  // ContainerInfo: + health: ContainerHealth
  ```

**Behaviour:**
- `Health()`: `"none"` unless `State == "running"`; then `(unhealthy)` → `unhealthy`, `(healthy)` → `healthy`, `(health: starting)` → `starting`, else `none`. Check `unhealthy` before `healthy` (substring).
- `ExitCode()`: regex `^Exited \((-?\d+)\)` on `Status` (moved from `state`). `state.project()` uses both methods; `view.ExitCode` stays a string (`strconv.Itoa` when ok).
- `countHealth(project string, cs []engine.Container) HealthCounts` counts every container whose project label equals `project` (empty project → zero counts): running → by `Health()` into healthy/unhealthy/starting/none; `State` `dead` or `restarting` → failed; `exited` with non-zero `ExitCode()` → failed; exited 0, `created`, `paused` → nowhere.
- `declaredPorts(f *composefile.File) []string`: for each service's `Config.Ports` entry, `ParsePorts(spec)`; skip errors and empty results (container-only ports, `0`, `$` interpolation); a multi-port result renders `"<first>-<last>"`, single `"<port>"`; dedupe; sort by the numeric low port, then the string. IP and protocol are dropped (`127.0.0.1:8080:80/udp` → `"8080"`).
- `describe` fills `Health` always (also on parse error and on engine error with whatever containers it has; on engine error that is none) and `Ports` only when the file parsed. `fillServices` sets `ContainerInfo.Health`. `List` and `Get` agree because both use `describe`.
- JSON: `ports` is always an array (`[]`, never `null`); `health` always present.

- [ ] **Step 1: Write the failing tests**
  - `internal/engine`: `TestContainerHealth` table — `Up 5 days (healthy)` running → healthy; `Up 1 minute (unhealthy)` → unhealthy; `Up 3 seconds (health: starting)` → starting; `Up 2 hours` → none; `Exited (0) 1 hour ago` with state exited → none. `TestContainerExitCode` — `Exited (137) 2 minutes ago` → 137,true; `Exited (-1)` → -1,true; `Up 2 hours` → 0,false. Reuse `testdata/containers.json` where it already has the case.
  - `internal/stacks`: `TestDescribeHealthCounts` — project with healthy, unhealthy, starting, running-no-check, dead, restarting, exited(1), exited(0), created containers plus one container of another project → `{1,1,1,1,3}`. `TestDescribeHealthCountsOnParseError` — broken file, two running containers with the project label → counts present, `Ports` empty, status `unavailable`. `TestDeclaredPorts` table — `["8096:8096","80:80","443:443/tcp"]` → `["80","443","8096"]`; `"8000-8010:80"` → `"8000-8010"`; `"127.0.0.1:8080:80/udp"` → `"8080"`; same port in two services → once; `"80"` (container only) and `"${WEB_PORT}:80"` → skipped; one invalid spec among valid ones → valid ones kept. `TestListAndGetAgreeOnHealthAndPorts`.
  - `internal/api`: extend `stacks_test.go` list/detail JSON assertions with `health` and `ports` (`[]` when nil); `client_contract_test.go` decodes both into `apitypes`.
  - Example shape for the Go table:
    ```go
    {name: "range", ports: []string{"8000-8010:80"}, want: []string{"8000-8010"}},
    ```
- [ ] **Step 2: Run to verify they fail**
  Run: `go test ./internal/engine ./internal/stacks ./internal/api -run 'Health|ExitCode|DeclaredPorts|ListAndGet|Stack' -v`
  Expected: compile errors for the missing methods/fields (`c.Health undefined`, `unknown field Health`), not unrelated failures.
- [ ] **Step 3: Implement** engine methods, state switch-over, `countHealth`, `declaredPorts`, DTO mapping, `types.ts` additions; update TS test fixtures that construct `StackSummary`/`ContainerInfo` literals (add `health`/`ports`) so `svelte-check` passes.
- [ ] **Step 4: Run** `go test ./...` and `cd web && npm run check && npm test` — all pass.
- [ ] **Step 5: Commit** — `feat: report container health, stack health counts and declared ports` (why: the v2 list shows health and ports without extra Docker calls; ports come from the file so stopped stacks still show them).

---

### Task 2: Tokens, typography, fonts, theme.js, budgets, logos

Spec §3 (first paint, fonts, font budget, logo assets), §4, §5 (theme.js, CSP, reduced motion), §14 licence files.

**Files:**
- Create: `web/public/theme.js`, `web/public/licenses/IBMPlexSans-OFL.txt`, `web/public/licenses/GeistMono-OFL.txt`, `web/public/logo-art.png`, `web/public/containers.png`
- Create: `web/src/assets/fonts/IBMPlexSans-{Regular,Medium,SemiBold}-latin.woff2`, `GeistMono-{Regular,Medium}-latin.woff2`, `OFL-IBM-Plex-Sans.txt`, `OFL-Geist-Mono.txt`
- Create: `web/src/theme.test.ts` (theme.js behaviour), `web/src/tokens.test.ts` (contrast + old-name guard)
- Modify: `web/index.html`, `web/public/favicon.svg`, `web/src/app.css`, `web/scripts/check-bundle.mjs`, `web/src/bundle.test.ts`
- Modify: `internal/api/spa.go`, `internal/api/server_test.go`
- Modify (mechanical sweep): every `web/src/**/*.svelte` and `web/src/lib/stacks/cm.ts` using `--surface-raised`, `--accent-contrast`, `--font-sans`, `--font-mono` → `--raised`, `--accent-ink`, `--sans`, `--mono`

**Interfaces:**
- Produces: the token names of spec §4.1 (`--bg --surface --raised --hover --border --border-strong --border-input --text --muted --faint --accent --accent-ink --accent-soft --success --warning --danger --info --{success,warning,danger,info,muted}-soft --shadow --shadow-pop --series-1..8 --sans --mono --pad --row --radius-sm --radius --radius-md --radius-lg --radius-pill --space-1..7`) and the `<html>` attributes `data-theme="dark|light"`, `data-density="comfortable"`.
- Produces: `check-bundle.mjs` exports `FONT_BUDGET = 200 * 1024`; `measure()` result gains `fonts: number` (raw bytes summed over `assets/*.woff2`, not gzipped — woff2 is already compressed); `verdict()` adds `fonts <n> B exceeds <budget> B`.

**Behaviour:**
- `app.css`: light set on `:root`; dark set under `@media (prefers-color-scheme: dark) { :root:not([data-theme="light"]) { … } }` and again under `:root[data-theme="dark"]`; `color-scheme` follows. Density: `--pad: 6px; --row: 36px`, `:root[data-density="comfortable"]` → `10px` / `44px`. Body `13px/1.45 var(--sans)` on `var(--bg)`. Headings, table header, mono usage and control sizes per §4.2 as global base rules where they are global (body, headings, `code/pre/kbd`, `:focus-visible` with `outline-offset: 1px`). `@font-face` for the five files with `font-display: swap`, `unicode-range` latin. Reduced-motion block also stops `.live` (`ss-pulse`) and the spinner rotation (the existing global rule already zeroes animation durations; add explicit `animation: none` for those two so the end state is the static one).
- Keep `.visually-hidden`, `.stack`, `.cluster` utilities; drop `.container` max-width usage only in T6 (shell), not here.
- `theme.js`: exactly the spec §5 snippet's behaviour (stored theme, stored density, `?theme=` override last; storage errors swallowed). No other globals.
- `index.html`: `<script src="/theme.js"></script>` is the first element in `<head>`, then three `<link rel="preload" as="font" type="font/woff2" crossorigin>` for Plex 400, Plex 500, Geist Mono 400, then the existing tags. Preload hrefs must resolve to the hashed `/assets/*.woff2` files the CSS uses. Verify after `npm run build` by reading `dist/index.html`; if Vite does not rewrite the preload hrefs, add a tiny `transformIndexHtml` hook in `vite.config.ts` that injects the preloads from the bundle's woff2 file names (no new dependency).
- Fonts: `npm pack @fontsource/ibm-plex-sans@<exact>` and `@fontsource/geist-mono@<exact>` into the scratchpad, copy the latin `400/500/600-normal` and `400/500-normal` woff2 files under the spec names, plus the `LICENSE` text as the OFL files (both locations). Not added to `package.json`.
- Logos: from `docs/Stackstead DevOps UI redesign/assets/logo-art.png` and `containers.png`, e.g. `sips --resampleWidth 336 <in> --out web/public/logo-art.png`. Favicon: replace the blue fill with `#34d399`.
- `spa.go`: `func init() { mime.AddExtensionType(".woff2", "font/woff2") }` (Go's table lacks it and `nosniff` is global). `/theme.js` is a regular public file and already gets `no-cache` — pin it with a test.
- Sweep: after renaming, `grep -rnE -- '--(surface-raised|accent-contrast|font-sans|font-mono)\b' web/src` prints nothing. Components keep their current look as far as the new tokens allow; real restyles come in T5+.

- [ ] **Step 1: Write the failing tests**
  - `web/src/theme.test.ts` (load `public/theme.js` text with `readFileSync`, run it with `new Function` against jsdom after arranging `localStorage` and `history.replaceState` for the query):
    - `applies stored dark theme and comfortable density`
    - `absent keys leave no attributes (system, compact)`
    - `ignores junk values` (`stackstead.theme = 'blue'`)
    - `theme.js query override wins but is not stored` (`?theme=light` with stored dark → `data-theme=light`, storage still `dark`)
    - `theme.js survives a throwing localStorage` (stub `Storage.prototype.getItem` to throw; with `?theme=dark` the override still applies)
  - `web/src/tokens.test.ts`:
    - `old token names are gone` — walk `src/` for `.svelte`, `.ts`, `.css` and fail on any of the four old names.
    - `contrast pairs meet AA` — parse `app.css` token blocks (light block and the `:root[data-theme="dark"]` block), compute WCAG relative-luminance ratios, and assert the spec §4.3 pairs: `text/muted/faint/accent/success/warning/danger/info` on `bg/surface/raised/hover` ≥ 4.5; `accent-ink` on `accent` ≥ 4.5; `--border-input` vs `--bg` ≥ 3. (Soft fills are rgba: composite over `--surface` before measuring tone-on-soft ≥ 4.5.) Small helper inside the test file, e.g.:
      ```ts
      const ratio = (a: RGB, b: RGB) => (Math.max(L(a), L(b)) + 0.05) / (Math.min(L(a), L(b)) + 0.05);
      ```
    - `light and dark blocks define the same token names`.
  - `web/src/bundle.test.ts`: `counts woff2 bytes against FONT_BUDGET and keeps them out of entry/total`; `fails when fonts exceed the budget`; `public logos stay within their size caps` (reads `public/logo-art.png` ≤ 40 960 B, `public/containers.png` ≤ 8 192 B, and checks the PNG IHDR width is 336 / 68).
  - `internal/api/server_test.go`: `TestFontsServedAsFontWoff2` (MapFS with `assets/x.woff2` → `Content-Type: font/woff2`, immutable cache); `TestThemeScriptIsNoCache` (MapFS `theme.js` → `no-cache`, JS content type, no CSP header needed).
- [ ] **Step 2: Run to verify they fail** — `cd web && npx vitest run src/theme.test.ts src/tokens.test.ts src/bundle.test.ts` (missing file / old names found / `FONT_BUDGET` undefined) and `go test ./internal/api -run 'Fonts|ThemeScript'` (wrong content type).
- [ ] **Step 3: Implement** tokens, fonts, theme.js, index.html, budget, MIME, logos, favicon, sweep.
- [ ] **Step 4: Run** `make check` and `make ui` (prints the font total under 200 kB); open `npm run dev` once in both OS themes to eyeball no flash.
- [ ] **Step 5: Commit** — `feat: v2 design tokens, self-hosted fonts and first-paint theme` (why: one token set drives both themes under the unchanged CSP; fonts self-hosted to keep `default-src 'self'` and Pi bandwidth bounded). Include font package versions and sha256s in the body.

---

### Task 3: Docker version and metrics settings on `/api/status`

Spec §10.1 (`Status`), §6.1 (Docker pill), §6.7 (System status), §14 (`docs/cli.md`, `docs/api.md` for this field).

**Files:**
- Modify: `internal/engine/client.go` (+ `client_test.go`)
- Modify: `internal/api/stacks.go`, `internal/api/server.go`, `internal/apitypes/types.go` (+ `status_version_test.go`, `client_contract_test.go`)
- Modify: `internal/cli/serve.go`, `internal/cli/cmd_login.go` (+ its test)
- Modify: `web/src/lib/api/types.ts` (`Status`), `web/src/lib/stores/live.ts` (+ `live.test.ts`)
- Modify: `docs/cli.md`, `docs/api.md` (`/api/status` section only)

**Interfaces:**
- Produces (Go):
  ```go
  func (c *Client) Version(ctx context.Context) (string, error) // GET /version → .Version
  type StatusReport struct{ Docker, DockerVersion, Compose string }
  type StatusFunc func(ctx context.Context) StatusReport
  // api.Options gains: MetricsInterval, MetricsRetention time.Duration
  // apitypes.Status gains:
  //   DockerVersion string `json:"dockerVersion"`
  //   MetricsIntervalSeconds int64 `json:"metricsIntervalSeconds"`
  //   MetricsRetentionSeconds int64 `json:"metricsRetentionSeconds"`
  ```
- Produces (TS): `Status` gains `dockerVersion: string; metricsIntervalSeconds: number; metricsRetentionSeconds: number`. `LiveState` gains `dockerVersion: string` (`''` = unknown), `metricsIntervalSeconds?: number`, `metricsRetentionSeconds?: number`; `refreshStatus` fills them; `stopLive` resets them.

**Behaviour:**
- `Version` uses the unversioned request path (like `Ping`), decodes only the `Version` field, and errors on a non-2xx or undecodable body.
- `serve.go` probe: `v, err := dockerEngine.Version(ctx)`; ok → `Docker:"ok", DockerVersion:v`; error → `Ping`; ping ok → `Docker:"ok"`, version `""`; else `"unavailable"`. Compose unchanged. The startup warning reads the struct. Options get `cfg.MetricsInterval` / `cfg.MetricsRetention`.
- `handleStatus` writes the three new fields (seconds as integers; `0` when unset). No other route changes.
- CLI `status`: adds a `docker version` row after `docker` only when `st.DockerVersion != ""` (sanitised with `cleanText`); `--json` includes the fields automatically.

- [ ] **Step 1: Write the failing tests**
  - `engine`: `TestVersionDecodes` (fake socket server answering `/version` with `{"Version":"27.1.1","ApiVersion":"1.46"}`), `TestVersionErrorsOnServerError`.
  - `api`: `TestStatusReportsDockerVersionAndMetricsSettings` (Options with `MetricsInterval: 10*time.Second, MetricsRetention: 72*time.Hour` → `10`, `259200`); existing `TestStatusReportsVersion` adapted to the struct return.
  - `cli`: `TestStatusProbeFallsBackToPing` — extract the probe into a small function in `serve.go` taking interfaces (`Version`, `Ping`, compose `Version`) so it is testable without Docker: version error + ping ok → `{ok, "", …}`; both fail → `unavailable`. `TestStatusCommandPrintsDockerVersion` and `…OmitsEmptyDockerVersion`.
  - `client_contract_test.go`: the CLI client decodes the new fields.
  - `live.test.ts`: `refreshStatus fills dockerVersion and metrics settings`; `stopLive clears dockerVersion`.
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/engine ./internal/api ./internal/cli -run 'Version|Status' -v`; `cd web && npx vitest run src/lib/stores/live.test.ts`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./...`, `cd web && npm run check && npm test`.
- [ ] **Step 5: Commit** — `feat: report the Docker engine version and metrics settings in /api/status` (why: the top bar and Settings show them; `/version` failing must not mark Docker down).

---

### Task 4: Appearance store

Spec §3 (theme default, persistence), §5 (store, rail toggle, MetricsChart observer).

**Files:**
- Create: `web/src/lib/stores/appearance.ts`, `web/src/lib/stores/appearance.test.ts`
- Modify: `web/src/main.ts`

**Interfaces:**
- Produces:
  ```ts
  export type ThemeChoice = 'dark' | 'light' | 'system';
  export type EffectiveTheme = 'dark' | 'light';
  export type Density = 'compact' | 'comfortable';
  export const theme: Readable<ThemeChoice>;
  export const effectiveTheme: Readable<EffectiveTheme>;
  export const density: Readable<Density>;
  export function setTheme(t: ThemeChoice): void;
  export function setDensity(d: Density): void;
  /** Reads what theme.js set; listens to the colour-scheme media query. Returns a disposer. */
  export function initAppearance(deps?: {
    root?: HTMLElement; storage?: () => Storage | null; matchMedia?: (q: string) => MediaQueryList;
  }): () => void;
  ```

**Behaviour:**
- `initAppearance` reads `root.dataset.theme` / `.density` (so a `?theme=` override shows as the current choice) and subscribes to `(prefers-color-scheme: dark)` `change`. `effectiveTheme` = explicit choice, else the media query result (`light` when `matchMedia` is missing).
- `setTheme('dark'|'light')` sets `data-theme` and writes the key; `setTheme('system')` removes both. `setDensity('comfortable')` sets attribute + key; `compact` removes both. Every storage access in `try/catch`; DOM and store update even when storage throws.
- Nothing writes storage during `initAppearance`.
- `main.ts` calls `initAppearance()` before `mount`.

- [ ] **Step 1: Write the failing tests** — `reads initial attributes set by theme.js`; `setTheme('light') writes attribute and storage`; `system removes attribute and key`; `setDensity round trip`; `setTheme with throwing storage still updates the DOM`; `query override is visible but never written` (attribute `dark` present, storage empty → `theme` is `dark`, storage still empty after init); `media change flips effectiveTheme only in system`; `disposer removes the media listener`.
- [ ] **Step 2: Run to verify they fail** — `npx vitest run src/lib/stores/appearance.test.ts` (module not found).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm run check && npm test`.
- [ ] **Step 5: Commit** — `feat: appearance store for theme and density` (why: one owner for the DOM attributes and storage so the rail toggle and Settings agree).

---

### Task 5: Shared components and the lifecycle helper

Spec §6.8, §4.2 (control sizes), §7.

**Files:**
- Modify: `web/src/lib/components/{Button,StatusBadge,Banner,Tabs,Field,Toasts,Dialog,ConfirmDialog,EmptyState,Spinner,CopyButton,ListEditor,RelativeTime}.svelte`, `components.test.ts`
- Create: `web/src/lib/stacks/lifecycle.ts`, `lifecycle.test.ts`
- Modify: `web/src/lib/stacks/ActionsBar.svelte` (+ `ActionsBar.test.ts`) — uses `lifecycle.ts`, icons, sizes

**Interfaces:**
- Produces:
  ```ts
  // Button props: + size?: 'md' | 'sm' | 'xs' (default 'md'); icon?: boolean
  // StatusBadge: status: StackStatus | OpState | ContainerHealth | 'restarting' | 'exited'
  // Tabs: tabs: { id: string; label: string; badge?: string | number }[]
  // Field: + mono?: boolean
  // CopyButton: + variant?: 'ghost' | 'icon'; size?: 'xs' | 'sm'  (text, label unchanged)
  // lifecycle.ts
  export interface LifecycleInput {
    live: Pick<LiveState, 'compose' | 'docker' | 'events'>;
    status?: StackStatus; parseError?: string; busy?: boolean;
  }
  export function lifecycleDisabledReason(i: LifecycleInput): string | undefined;
  ```
- `lifecycleDisabledReason` order and texts (detail page's existing strings): compose unavailable → `docker compose plugin not found`; docker unavailable or events disconnected → `Docker is unreachable`; `parseError` or status `unavailable` → `the compose file cannot be parsed`; `busy` → `an operation is already running`; else `undefined`.

**Behaviour:** per spec §6.8 for every component. Notable:
- Button variants: `primary` (accent bg, accent-ink, 600), `secondary`, `danger` outlined (never solid red; inside `ConfirmDialog` always with `--danger-soft` fill — pass a `confirm` class or prop from `ConfirmDialog`), `ghost`. Heights 30/26/24 px; `icon` makes it square (26×26 for `sm`). Existing `aria-disabled` / `aria-busy` / reason `aria-describedby` and spinner unchanged.
- StatusBadge: no glyphs; uppercase 11 px; tone map spec §6.8 (running/succeeded/healthy → success; partial/timed_out/starting/restarting → warning; stopped/cancelled/unknown/exited/none → muted; unavailable/failed/unhealthy → danger). Label for `timed_out` stays `timed out`.
- Tabs: badge rendered in mono on `--raised` inside the tab button, after the label, `aria-hidden` is **not** set (the count is useful to AT; the accessible name becomes e.g. "Services 3" — check existing tests that select tabs by name use regexes or update them to `{ name: /^Services/ }`).
- Field: `mono` adds a class on the wrapper; the control styling is applied through `.field :global(input, select, textarea)`; `aria-invalid` → danger border; readonly/disabled → raised + muted. Error `✖` glyph kept.
- Spinner: 16 px ring, accent arc; reduced motion shows the static ring (from T2 CSS).
- ActionsBar: icons on Up (▶, primary) and labels as today; uses `lifecycleDisabledReason` with the props it already receives (keep its public props so StackDetail compiles unchanged until T15).

- [ ] **Step 1: Write the failing tests** (in `components.test.ts` / `lifecycle.test.ts`):
  - `StatusBadge tone class for every status` (table over every value of the union → class `success|warning|muted|danger`, text label) and `renders no decorative glyph`.
  - `Button sizes` (`size="sm"` → class `sm`), `icon button keeps aria-label as its name`, `danger is outlined by default and filled inside ConfirmDialog`.
  - `Field mono applies to the control` (render `FieldFixture` with `mono`, assert wrapper class).
  - `Tabs render badges and keep roving tabindex`.
  - `lifecycleDisabledReason` table: each condition alone, precedence when several hold, `undefined` when healthy.
  - Existing component tests stay green (only class-name assertions may be updated).
- [ ] **Step 2: Run to verify they fail** — `npx vitest run src/lib/components src/lib/stacks/lifecycle.test.ts src/lib/stacks/ActionsBar.test.ts`.
- [ ] **Step 3: Implement** behaviour, then the CSS restyle of the remaining components (Banner, Toasts, Dialog, EmptyState, CopyButton, ListEditor, RelativeTime) per §6.8 with no behaviour changes.
- [ ] **Step 4: Run** `npm run check && npm test`.
- [ ] **Step 5: Commit** — `feat: restyle shared components and share the lifecycle disabled reason`.

---

### Task 6: Shell — Rail, TopBar, App layout

Spec §6.1, §8 (global rows), §9.

**Files:**
- Create: `web/src/lib/shell/Rail.svelte`, `TopBar.svelte`, `breadcrumb.ts`, `icons.ts`, `shell.test.ts`
- Modify: `web/src/App.svelte`, `web/src/App.test.ts` (only where markup moved)
- Delete: `web/src/lib/components/Nav.svelte` (move its tests in `components.test.ts` to `shell.test.ts`, same assertions)

**Interfaces:**
- Consumes: `appearance` (T4), `LiveState.dockerVersion` (T3), Button (T5), `link`/`route` (router).
- Produces:
  ```ts
  // breadcrumb.ts
  export interface Crumb { label: string; href?: string; mono?: boolean }
  export function breadcrumb(path: string): Crumb[];
  // icons.ts — SVG path data from the mock-up, keyed by name
  export const ICONS: Record<'stacks' | 'plus' | 'import' | 'settings' | 'sun' | 'moon' | 'play' | 'stop' | 'restart' | 'file' | 'copy' | 'search', string>;
  // Rail props: { username?: string; current: string; onlogout: () => void }
  // TopBar props: { path: string; runningOps: number }
  ```

**Behaviour:**
- `breadcrumb`: `/` → `[Stacks]`; `/stacks/:name` → `[Stacks(href /), name(mono)]` (decoded); `/stacks/new` → `[Stacks, New stack]`; `/stacks/import` → `[Stacks, Import]`; `/settings` → `[Settings]`; anything else → `[Stacks, Not found]`.
- Rail per §6.1: logo link `aria-label="Stackstead"`; four links with `aria-label` = label, `title` = label, `aria-current="page"` (Stacks is current on `/` and `/stacks/:name` but not on `/stacks/new` or `/stacks/import`); theme toggle `aria-label="Switch to light theme"` when effective theme is dark, else `"Switch to dark theme"`, calls `setTheme(opposite)`; avatar button with first two characters of the username upper-cased, `aria-label="Log out"`, `title="<username> · Log out"`, `onclick=onlogout`.
- TopBar: breadcrumb `<nav aria-label="Breadcrumb">` (last crumb `aria-current="page"`); pills: `N running` (`role="status"`, only when > 0, text exactly `${n} running`), Docker pill (`Docker <version>` / `Docker` / `Docker unavailable` with tone dot), compose pill (`compose <version>` / `compose unavailable`; hidden ≤ 900 px; Docker pill collapses to its dot with `aria-label` ≤ 900 px; breadcrumb shows only the last crumb ≤ 720 px).
- App: `display:flex` with the rail, a column holding TopBar, banners (inside main's horizontal padding, unchanged texts and roles) and `<main>` (`padding: 20px 20px 40px`, 16 px ≤ 720 px, `min-width:0`). Login/Setup/loading render without rail or top bar. Every effect, guard, title map, 404 and the unsaved-changes dialog unchanged; `requestLogout` still goes through the guard.
- Example shape for the Rail item list:
  ```ts
  const items = [{ href: '/', label: 'Stacks', icon: 'stacks', match: (p: string) => p === '/' || /^\/stacks\/(?!new$|import$)[^/]+$/.test(p) }, …];
  ```

- [ ] **Step 1: Write the failing tests** (`shell.test.ts`): `breadcrumb per route` (table incl. encoded names like `/stacks/my%20stack`); `rail links keep their accessible names and aria-current` (incl. Stacks current on a detail path); `theme toggle label flips and calls setTheme`; `avatar shows initials and logs out through onlogout`; `docker pill reflects version and availability` (ok+version, ok+empty, unavailable); `compose pill`; `running pill only when > 0`. Existing `App.test.ts` nav tests (`getByRole('navigation', {name:'Main'})`, link names, `button 'Log out'`, `/2 running/`) pass unchanged.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement**, delete `Nav.svelte`.
- [ ] **Step 4: Run** `npm run check && npm test && npm run build`.
- [ ] **Step 5: Commit** — `feat: icon rail and top bar replace the text nav`.

---

### Task 7: Login and Setup

Spec §6.2.

**Files:** Modify `web/src/routes/Login.svelte`, `Setup.svelte` (+ their tests only if they assert replaced markup).

**Behaviour:** centred card (360 / 400 px max, `--radius-lg`, 28 px padding, 16 px gap) on `--bg`; header with `/logo-art.png` at 168 px (`alt=""`, decorative — the wordmark is the text), wordmark `Stack<b>stead</b>`-style (600 then 400 weight), `window.location.host` in muted 12 px; fields via `Field` with unchanged ids/labels/autocomplete; full-width 34 px primary submit; `role="alert"` error above the fields; footer copy per §6.2. Setup keeps four fields, validation, focus-on-error and 403/409 handling.

- [ ] **Step 1: Write the failing test** — `Login shows the host and the localhost note` (the only new visible behaviour); existing Login/Setup tests stay as they are.
- [ ] **Step 2: Run to verify it fails.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npx vitest run src/routes/Login.test.ts src/routes/Setup.test.ts && npm run check`.
- [ ] **Step 5: Commit** — `feat: v2 login and setup cards`.

---

### Task 8: Shared live-metrics source and the Metrics tab

Spec §3 (Detail "Resources · now"), §6.4 Metrics, §4.1 series tokens.

**Files:**
- Create: `web/src/lib/stacks/liveMetrics.ts`, `liveMetrics.test.ts`
- Modify: `web/src/lib/stacks/metrics.ts` (+ `metrics.test.ts` stays green unchanged), `MetricsPanel.svelte` (+ test), `MetricsChart.svelte` (+ test)

**Interfaces:**
- Produces:
  ```ts
  export interface StackLiveMetrics {
    status: Readable<SSEStatus>;
    /** Newest aggregated point per service (rates present once two samples exist). */
    latest: Readable<Map<string, SeriesPoint>>;
    /** Every aggregated point as it is produced; returns an unsubscribe. */
    onPoints(fn: (service: string, points: SeriesPoint[]) => void): () => void;
    /** Opens the stream on the first holder, closes it after the last release. */
    acquire(): () => void;
    close(): void;
  }
  export function createStackLiveMetrics(stackName: string, deps?: { open?: typeof openSSE }): StackLiveMetrics;
  export function totalsNow(latest: Map<string, SeriesPoint>): { cpu?: number; mem?: number; netRx?: number; netTx?: number };
  // metrics.ts: MetricsDeps gains live?: StackLiveMetrics
  // MetricsPanel props gain live?: StackLiveMetrics; metricsIntervalSeconds?: number; metricsRetentionSeconds?: number
  ```

**Behaviour:**
- Move the SSE connect, per-service aggregators, `dropped` reconnect (same delay), idle flush and close logic from `createMetricsController` into `createStackLiveMetrics`, unchanged in semantics.
- Controller with `deps.live`: acquires it for its lifetime; on `reload()` clears its own live set and ignores points until the history for the new generation has landed (replaces today's stop/connect around the history fetch), then joins as today. Without `deps.live` it creates one from `deps.open` — existing tests unchanged.
- `totalsNow`: sums per-service latest `cpu`, `mem`; sums `netRx`/`netTx` only over services that have them (undefined if none). Values are undefined for an empty map.
- MetricsPanel restyle per §6.4: segmented range buttons `1h 6h 24h 72h` (`aria-pressed`, replacing the select — the `range` store contract is unchanged), `sampled every <n> s · retention <h> h` when the settings are known, live status `role="status"`, six `<figure>` cards with the stack total in the caption. MetricsChart reads `--series-n` from `app.css` (remove its private colour block), axes `--muted`, grid `--border`, keeps its `MutationObserver` on `data-theme` and the media listener.

- [ ] **Step 1: Write the failing tests** — `liveMetrics.test.ts`: `acquire is ref-counted` (two acquires → one `open`; first release keeps it; second closes); `latest keeps the newest point per service`; `dropped reconnects once`; `totalsNow sums services and leaves rates undefined until present`. `metrics.test.ts`: `controller with a shared live source does not open its own stream` (new case). `MetricsPanel.test.ts`: `range buttons switch range with aria-pressed`; `shows sampling settings when known`. `MetricsChart.test.ts`: `series colours come from the tokens` (set `--series-1` on `document.documentElement` style and assert the plot option).
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npx vitest run src/lib/stacks src/lib/metrics && npm run check && npm run build` (uPlot still lazy).
- [ ] **Step 5: Commit** — `refactor: share one live metrics stream per stack page` then `feat: v2 metrics tab` (two commits if the refactor lands green first).

---

### Task 9: Compose tab — toolbar, editor theme, changed lines

Spec §3 (changed-line highlight), §6.4 Compose.

**Files:**
- Create: `web/src/lib/stacks/changedLines.ts`, `changedLines.test.ts`
- Modify: `web/src/lib/stacks/cm.ts`, `YamlEditor.svelte`, `ComposeEditor.svelte`, `DiffView.svelte` (+ their tests)

**Interfaces:**
- Produces:
  ```ts
  /** 1-based line numbers in `draft` that differ from `original` (inserted or modified lines). */
  export function changedLines(original: string, draft: string): number[];
  // YamlEditor props gain: original?: string  (baseline for decorations; none when absent)
  ```

**Behaviour:**
- `changedLines` uses `diff()` from `@codemirror/merge` on the two strings and maps each change's `fromB..toB` to draft line numbers; deletions mark the line where the deletion happened (so a removed line is still visible as a change). It lives in the lazy editor chunk only (imported by `cm.ts`/`YamlEditor`), never from the entry.
- YamlEditor: a `StateField`/`ViewPlugin` adds line decoration class `cm-changed-line` (`--success-soft` background) and a gutter marker for those lines, recomputed 150 ms after the last change.
- Editor theme in `cm.ts`: gutter and content on `--surface`, line numbers `--faint`, keys/property names `--info`, strings `--success`, comments `--faint`, numbers `--warning`, no own border.
- ComposeEditor toolbar per §6.4: basename of `composePath` in mono, `· modified` in `--warning` while dirty, `⌘S / Ctrl+S to save` hint, `Preview changes` secondary `sm`, `Save` primary `sm`, `Apply` after save; footer line about `compose.yaml.bak`. All phases (loading, failed, conflict, previewing, saving, saved) and the shortcut unchanged.

- [ ] **Step 1: Write the failing tests** — `changedLines`: identical → `[]`; one modified line; inserted block; deleted line marks its neighbour; CRLF input behaves like LF; trailing newline only. `YamlEditor.test.ts`: `marks changed lines after an edit` (count `.cm-changed-line` after the debounce with fake timers). `ComposeEditor.test.ts`: `toolbar shows the file name and modified state`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npx vitest run src/lib/stacks && npm run build` (merge/CodeMirror chunks still lazy).
- [ ] **Step 5: Commit** — `feat: v2 compose tab with changed-line highlighting`.

---

### Task 10: Logs viewer

Spec §6.4 Logs.

**Files:** Modify `web/src/lib/stacks/LogsViewer.svelte` (+ `LogsViewer.test.ts`), `web/src/lib/stacks/logs.ts` if a pure tone helper fits there (+ `logs.test.ts`).

**Interfaces:**
- Produces: LogsViewer props `{ stackName: string; services: string[]; service?: string }` — `service` preselects the Service select on mount (unknown names fall back to all services). `export function lineTone(text: string): 'danger' | 'warning' | undefined` (`[Error]`/`ERROR` → danger, `[Warn]`/`WARN` → warning; display only).

**Behaviour:** toolbar per §6.4 (selects 26 px on `--raised`, labels become `aria-label`s `Service`, `Stream`, `Tail`, `Search logs`; Follow as a checkbox chip; status text `live · following` / `paused` / `connecting` / `reconnecting` / `ended` / errors with `role="status"`); notes between toolbar and pane unchanged; pane `role="log"`, `tabindex="0"`, grid rows time/service/stream/text, service hue `hsl(h 60% 65%)` dark / `hsl(h 60% 42%)` light (pick by `effectiveTheme`), stderr in `--danger`, `<mark>` search matches, "N new lines / Jump to latest" unchanged.

- [ ] **Step 1: Write the failing tests** — `service prop preselects the stream filter` (the first request carries `service=<name>`); `unknown service prop falls back to all`; `lineTone` table; controls still found by their (now aria-) labels.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npx vitest run src/lib/stacks/LogsViewer.test.ts src/lib/stacks/logs.test.ts && npm run check`.
- [ ] **Step 5: Commit** — `feat: v2 logs viewer and preselected service`.

---

### Task 11: Operations list and output panel

Spec §6.4 Operations.

**Files:**
- Create: `web/src/lib/stacks/OperationsList.svelte`, `OperationsList.test.ts`
- Modify: `web/src/lib/stacks/OperationPanel.svelte` (+ `OperationPanel.test.ts`), `web/src/lib/stacks/operation.ts` if the line tone helper fits there

**Interfaces:**
- Produces:
  ```ts
  // OperationsList props: { operations: Operation[]; selected?: string; href: (id: string) => string }
  export function outputTone(line: string): 'success' | 'danger' | undefined; // ✔ → success; ✘, Error, error:, failed → danger
  ```

**Behaviour:**
- OperationsList: one `<a use:link>` row per operation in the five-column grid; action mono, state dot + uppercase tone text, `docker compose <action>` muted with ellipsis, `RelativeTime`, duration mono via `formatDuration(endedAt − startedAt)`; running rows tick once a second (single interval, started only while at least one running row is rendered, cleared on destroy); selected row `aria-current="true"`; empty: `No operations yet.`
- OperationPanel header: `docker compose <action>`, tone state, `<duration> · exit <code>` (`exit —` while running), Cancel (unchanged), `Copy output` (`CopyButton` `xs`, text = lines joined with `\n`); `op.error` in danger; pane `--bg`, mono, `max-height:260px`; note/gap entries italic muted; Jump to latest and the polite live region unchanged.

- [ ] **Step 1: Write the failing tests** — `rows show duration for finished ops`; `running row ticks and stops ticking when finished` (fake timers); `selected row has aria-current`; `Copy output copies joined lines` (stub `navigator.clipboard.writeText`); `exit dash while running`; `outputTone` table.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npx vitest run src/lib/stacks && npm run check`.
- [ ] **Step 5: Commit** — `feat: v2 operations list and output panel with durations and copy`.

---

### Task 12: New stack and Import

Spec §6.5, §6.6, §6.8 (ListEditor, FileBrowser).

**Files:** Modify `web/src/routes/StackCreate.svelte`, `StackImport.svelte`, `web/src/lib/stacks/ServiceForm.svelte`, `NetworkPicker.svelte`, `FileBrowser.svelte` (+ tests only where they assert replaced markup).

**Behaviour:** pure restyle per §6.5/§6.6 with the copy changes the spec lists (lead texts, hints, group titles and hints, `+ Add` `xs` ghost, 30×30 `×` remove buttons with `aria-label="Remove item N"` kept, segmented Image | Dockerfile radiogroup, footer with Start now checkbox, Cancel link, `Create stack` primary). Import: path field mono + `Browse…`, hint text, the existing `.compose-error` `role="alert"` restyled as the bordered "Compose file · cannot import" box, footer Cancel / `Import stack`. All validation, server error mapping, 503 banner, focus-first-error and FileBrowser behaviour unchanged.

- [ ] **Step 1: Write the failing test** — `import error renders in the compose-file box with its message` (asserts the `cannot import` label and the server message inside the `role="alert"` element). Everything else is covered by existing tests.
- [ ] **Step 2: Run to verify it fails.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npx vitest run src/routes/StackCreate.test.ts src/routes/StackImport.test.ts src/lib/stacks && npm run check`.
- [ ] **Step 5: Commit** — `feat: v2 new stack and import forms`.

---

### Task 13: Settings

Spec §6.7, §8 (`/settings` row).

**Files:** Modify `web/src/routes/Settings.svelte` (+ `Settings.test.ts`), `web/src/lib/settings/PasswordForm.svelte`.

**Behaviour:** four cards per §6.7:
1. System status: Docker `<version> · ok` (or `ok` when the version is empty) / `unavailable`; Compose `<version>`; Events `connected`/`disconnected`; Metrics `ok · <interval> s` / `degraded`; tone dot each; `data-testid`s kept; `Refresh` secondary `sm`.
2. Prometheus token: header badge `configured` / `not configured`; every state and dialog unchanged; `Disable` uses the outlined danger button; `id="rotate-token"` kept; footer `docs/api.md` reference.
3. Appearance: Theme segmented `Dark | Light | System`, Density `Compact | Comfortable`, buttons with `aria-pressed` bound to the appearance store.
4. Account: signed-in line, PasswordForm (three fields in a grid, rate-limit banner unchanged), `Change password` primary, `Log out` secondary, footer "Remote access and hardening: docs/security.md." The old About card is removed (its links live in the two footers).

- [ ] **Step 1: Write the failing tests** — `appearance buttons reflect and set the theme and density`; `system status shows docker version and metrics interval`; update existing `status-*` assertions to the new value texts (record in the commit that only displayed text changed, testids unchanged).
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npx vitest run src/routes/Settings.test.ts src/lib/settings && npm run check`.
- [ ] **Step 5: Commit** — `feat: v2 settings with appearance controls`.

---

### Task 14: Stacks list

Spec §6.3 (8a parts), §8 (`/` row), §9, §13 (what 8a shows).

**Files:**
- Create: `web/src/lib/stacks/summary.ts`, `summary.test.ts` (list helpers; T15 adds `serviceState` to the same file — T14 owns creation)
- Modify: `web/src/routes/StackList.svelte`, `StackList.test.ts`

**Interfaces:**
- Consumes: `HealthCounts`, `ports` (T1), `lifecycleDisabledReason` (T5), Button `icon`/`sm` (T5), `ICONS` (T6).
- Produces:
  ```ts
  export function healthCell(h: HealthCounts): { empty: boolean; good: number; bad: number; starting: number; title: string };
  export function portChips(ports: string[], max?: number /* 3 */): { shown: string[]; more: number; title: string };
  export type ChipFilter = 'all' | StackStatus;
  export function filterStacks(items: StackSummary[], chip: ChipFilter, query: string): StackSummary[];
  export function chipCounts(items: StackSummary[]): Record<ChipFilter, number>;
  ```

**Behaviour:**
- Heading row: `h1#stacks-heading` "Stacks", spacer, `Import` secondary link-button, `New stack` primary with plus icon. No hostname (8b).
- Chips All / Running / Partial / Stopped, plus Unavailable / Unknown only when their count > 0; `aria-pressed`; counts in mono. Search (`aria-label="Search stacks"`, placeholder "Filter by name, project or path") matches name, project and path, case-insensitive; combined with the chip.
- Table columns: Stack (status square, name link with `title="project <project>"`, origin tag, path line with full path in `title`, per-row Spinner `Operation running on <name>`), Status, Services (`running/serviceCount` + cells), Health (`healthCell`: `—` when `empty` i.e. all counts zero; title lists the four counts plus failed), Ports (`portChips`, `+N`, title lists all), actions (Up ▶ / Down ■ / Restart ↻ icon buttons, `aria-label="Up <name>"` etc.).
- Inline actions: reason = `lifecycleDisabledReason({ live: $live, status: s.status, busy: runningStacks.has(s.name) || acting.has(s.name) })`; click → `api.act(name, action)` → `navigate('/stacks/<name>?tab=operations&op=<id>')` and `refreshRunningOps()`; errors toast like the detail page (`409` warning, else danger; `503` triggers `refreshStatus()`); a per-row `acting` set prevents a second request while one is in flight.
- States per §6.3: loading spinner inside the card, error banner + Retry when empty, degraded banner, `EmptyState` with New stack / Import buttons, "No stacks match the current filter." Rows `height: var(--row)`, padding `var(--pad) 12px`. ≤ 720 px table → cards (existing technique, `data-label`s), actions stay horizontal, filter row wraps. Nothing rendered after the table.
- Sorted by name as today; the running-operations backoff effect unchanged.

- [ ] **Step 1: Write the failing tests** — `summary.test.ts`: `healthCell` (all zero → empty; healthy+none = good; unhealthy+failed = bad; title text); `portChips` (≤3 none hidden; 5 → 3 + `more 2`; title joins all); `filterStacks` (chip × query, path match); `chipCounts`. `StackList.test.ts`: `chips filter and count`; `unavailable chip only when present`; `search matches path`; `health cell numbers and dash`; `ports +N with title`; `inline Up calls api.act and opens the operation`; `inline action is disabled while an operation runs on that stack`; `inline actions disabled when compose is unavailable`; `double click sends one act`; `act failure toasts and re-enables`; `no 8b sections render` (no CPU/Memory columns, nothing after the table card). Existing list tests updated only for removed columns (Origin/Project/Running headers) and the count moving into the All chip.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npx vitest run src/routes/StackList.test.ts src/lib/stacks/summary.test.ts && npm run check && npm run build` (entry budget).
- [ ] **Step 5: Commit** — `feat: v2 stack list with health, ports and inline actions`.

---

### Task 15: Stack detail page

Spec §6.4 (header, two columns, tabs, Services, aside), §8 (`/stacks/:name` row), §9, §11 (detail live metrics row).

**Files:**
- Create: `web/src/lib/stores/media.ts` (+ `media.test.ts`), `web/src/lib/stacks/ServiceCard.svelte`, `web/src/lib/stacks/StackAside.svelte` (+ `StackAside.test.ts`)
- Modify: `web/src/lib/stacks/summary.ts` (+ `serviceState`), `web/src/routes/StackDetail.svelte`, `StackDetail.test.ts`, `ActionsBar.svelte` if its props change

**Interfaces:**
- Consumes: `createStackLiveMetrics`, `totalsNow` (T8), `OperationsList` (T11), LogsViewer `service` (T10), ComposeEditor (T9), `lifecycleDisabledReason` (T5), Tabs badges (T5).
- Produces:
  ```ts
  export function mediaQuery(query: string): Readable<boolean>; // false when matchMedia is missing
  export function serviceState(svc: ServiceDetail): { tone: 'success' | 'danger' | 'warning' | 'muted'; text: string };
  // StackAside props: { detail: StackDetail; operations: Operation[]; live: StackLiveMetrics; opsHref: string }
  // ServiceCard props: { stack: string; service: ServiceDetail; onedit: () => void; logsHref: string }
  ```

**Behaviour:**
- Header row 1: mono `h1`, StatusBadge, meta `running/serviceCount services up · origin · project <mono>`, ActionsBar (Up primary with icon; Down/Restart/Pull/Build secondary; Remove danger; Remove dialog with volumes checkbox unchanged; `lifecycleReason` now from `lifecycleDisabledReason({ live, parseError, busy })` — same strings as before). Row 2: file icon, mono path, CopyButton icon `aria-label="Copy path"`. Banners below unchanged.
- Two columns `minmax(0,1fr) 300px`; the aside is rendered only while `mediaQuery('(min-width: 1100px)')` is true.
- Tabs: Services badge = `serviceCount`, Operations badge = `operations.length`; parse-error filtering, `?tab=`, `?op=`, roving focus and the unsaved-changes guard unchanged.
- ServiceCard per §6.4: dot from `serviceState` (any container running → success, unless one is unhealthy/restarting/dead or exited non-zero → danger; starting → warning; no containers or all exited 0 → muted), mono `h2`, state text (`<health> · <status>` of the first container, health omitted when `none`; `No containers` when empty), `Logs` ghost `sm` link to `?tab=logs&service=<name>` (goes through `guarded()` like other tab changes), `Edit` secondary `sm` `aria-label="Edit <service>"`; `<dl>` Image / Ports / Volumes / Restart / Limits (`—` when empty); read-only note; container list. The inline ServiceEditor + "Close editor" flow unchanged.
- Logs tab passes `service={$route.query.get('service') ?? undefined}`.
- Operations tab uses `OperationsList` + `OperationPanel` as today.
- Live metrics: the page creates one `createStackLiveMetrics(name)`; it holds an `acquire()` while `(asideShown && !parseError) || activeTab === 'metrics'`, released when that becomes false and on destroy; it passes the source to `MetricsPanel` (`live` prop) and `StackAside`.
- StackAside: Containers (all containers of all services; dot by state/health; `No containers.`), Resources · now (`totalsNow`; `—` until values exist; CPU `formatPercent`, Memory `formatBytes`, Net `formatRate(rx) · formatRate(tx)`), Recent operations (three newest, `All` link to `?tab=operations`, `None yet.`).

- [ ] **Step 1: Write the failing tests** — `media.test.ts`: `follows matchMedia changes`, `false without matchMedia`. `summary.test.ts`: `serviceState` table (running healthy; one unhealthy; restarting; exited(1); all exited(0); starting; none). `StackAside.test.ts`: `lists containers of all services`; `resources show dash until samples then totals`; `recent operations shows three newest and links to all`. `StackDetail.test.ts` (mock `matchMedia` and the SSE `open`): `aside hidden below 1100 px`; `no live stream when narrow and not on Metrics`; `one stream shared by aside and Metrics tab`; `stream closes when leaving both`; `Logs button preselects the service`; `tab badges show service and operation counts`; `copy path button`. Every existing `StackDetail.test.ts` case passes unchanged.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement** (extract ServiceCard first, keeping tests green, then the aside and live wiring).
- [ ] **Step 4: Run** `npm run check && npm test && npm run build`.
- [ ] **Step 5: Commit** — `feat: v2 stack detail with service cards and summary aside`.

---

### Task 16: E2E theme render, documentation, final checks

Spec §12 (E2E, budgets), §14 (8a parts).

**Files:**
- Modify: `cmd/stackstead/e2e_test.go`
- Modify: `docs/api.md` (health on containers and stacks, `ports`), `docs/development.md`, `docs/security.md`, `README.md`

**Behaviour:**
- E2E `chrome render` subtest gains, for `?theme=dark` and `?theme=light` at `375,812` and `1280,800`: `--screenshot=<tmp>/<name>.png --window-size=W,H` plus `--dump-dom`; assert `<html … data-theme="dark|light"` in the DOM dump, the login form present, no placeholder page, and the pixel at (4,4) decoded with `image/png` equals `#0f1216` (dark) / `#f3f5f7` (light) (allow ±1 per channel for colour management). New HTTP checks: `/theme.js` → 200, JS content type, `no-cache`; one `/assets/*.woff2` (found from `index.html` preload hrefs) → 200 `font/woff2`. Example shape:
  ```go
  for _, tc := range []struct{ theme string; want color.RGBA; w, h int }{{"dark", rgb(0x0f,0x12,0x16), 375, 812}, …} { … }
  ```
- Docs: `api.md` (container `health`, stack `health` + `ports`; `/api/status` already done in T3); `development.md` (fonts + licences + Fontsource source, font budget, `theme.js` and `?theme=`, e2e screenshots, `web/public` files); `security.md` (CSP unchanged, `theme.js` same-origin classic script, `localStorage` holds only theme and density); README ("Accessible" bullet adds theme toggle and density; "Third-party" line naming IBM Plex Sans and Geist Mono, OFL-1.1, served at `/licenses/…`; `make ui` mentions the font budget).

- [ ] **Step 1: Write the failing e2e assertions**, run `make ui && STACKSTEAD_E2E_CHROME=<chrome> go test -tags e2e ./cmd/stackstead -run E2E -v` — expect the new assertions to fail only if something upstream is wrong (they exercise T2's behaviour; if they pass first time, confirm they would fail by temporarily removing `theme.js` from `index.html`, then restore).
- [ ] **Step 2: Write the docs.**
- [ ] **Step 3: Final checks** — `make check`, `make ui` (entry ≤ 120 kB, total ≤ 400 kB, fonts ≤ 200 kB, no eager CodeMirror/uPlot), `go test ./...` without Node on PATH (`env PATH=/usr/bin:/bin go test ./...`), `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`, the e2e above, and a manual pass through every route in both themes and both densities at 375 px and 1280 px (no horizontal page scroll; focus visible on rail, chips, table actions and panes).
- [ ] **Step 4: Commit** — `test: render both themes in the e2e smoke` and `docs: UI v2 theme, fonts, health and ports`.
- [ ] **Step 5: Hand off** for the Fable final review (spec §8 mapping table walked route by route, §4.3 contrast test green, budgets, e2e screenshots attached), then merge to `main`.
