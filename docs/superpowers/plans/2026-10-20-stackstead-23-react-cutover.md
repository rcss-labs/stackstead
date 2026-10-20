# Stackstead Plan 23 — React cutover

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** tasks, names, signatures, test cases and examples only; no code bodies. Implementers write everything test-first where behaviour changes; deletions and config moves are verified by the existing suites and the named checks.

**Goal:** Ship the React app. `web/index.html` loads `/src/app/main.tsx`, `make build` embeds the React build from `web/dist` and serves it at `/` with the SPA fallback exactly as today (`web/embed.go`, `internal/api/spa.go` untouched); the Svelte app, its tests, toolchain, config and second HTML entry are deleted in their own commits with a verification step before and after; `npm run check` becomes `tsc --noEmit`; the bundle checks keep running in `make check` against the shipped `web/dist` (entry ≤ 106 kB gz, today 104.3; total ≤ 400 kB gz; fonts ≤ 200 kB); `docs/development.md` describes the React-only build; and the carried revisit items are resolved: the Shell no longer remounts the page at 760 px (unsaved forms survive), palette keystrokes typed before the lazy chunk arrives are kept, `/settings#section` deep links scroll and focus, the avatar and the Settings `Log out` get distinct accessible names, Escape from a container log peek returns focus to its row, a storage detail that turns not-found moves focus to its heading, the 80 px metrics charts drop the time axis, the backend publishes a `change` for a restart that leaves the container listing identical (so restart counts refresh for every service), and a follow-mode log stream re-attaches after `docker restart` / recreate instead of ending.

**Architecture:** The cutover is a one-line entry switch plus a config merge: `web/vite.app.config.ts` (React plugin, `chunkFileNames`, the Rolldown `codeSplitting` eager group) folds into `web/vite.config.ts`, which keeps the `keepDistGitignore` plugin, the `$lib` alias, the `/api` proxy and the Vitest block. The Svelte plugin stays in that config for exactly one commit so the Svelte tests still run while the React entry already ships; the next commits delete the Svelte sources (pure `git rm`, green on its own) and then the Svelte toolchain (deps, `svelte.config.js`, `svelte-check`, `tsconfig.app.json` merged into `tsconfig.json`, `vitest-setup.ts` without the Svelte cleanup). Nothing under `web/src/lib/**/*.ts` is deleted: the planning run traced every non-test lib module to an importer under `web/src/app` (prod or test); `lib/router.ts` is trimmed to the two helpers React uses (`matchRoute`, `safeNext`). The UI fixes are local to the components named per task and keep every existing test green; the two backend fixes are isolated Go tasks with Go tests first (server behaviour only — see the flags). The Shell fix keeps one React tree and switches chrome by position (the page `children` stay at the same tree position in both layouts). The palette host replaces `React.lazy` + `Suspense` with an explicit `load()` promise in state so it can render a keystroke-capturing fallback and seed the palette's query.

**Tech Stack:** React 19.3.1, React Router 8.4.0, Vite 8.3.4 (Rolldown), Vitest 5.0.4, TypeScript 6.0.3, `@vitejs/plugin-react` 6.1.2, `@testing-library/react` 16.3.3 + `user-event` 14.6.8, CSS Modules; Go (`internal/state`, `internal/history`, `internal/api`), SQLite through `internal/store`. **Removed:** `svelte` 5.57.2, `@sveltejs/vite-plugin-svelte` 7.3.1, `@testing-library/svelte` 5.4.2, `svelte-check` 4.7.6. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-19-stackstead-react-evolve-v2-design.md` — §2 goal 5 (Svelte removed; `make build`, `make check`, Go tests, bundle and font budgets pass), §3 (no API change; embed/serve contract untouched: `web/dist`, `web/embed.go`, `internal/api/spa.go`), §4 (entry over budget, CodeMirror / uPlot eager, handoff folder referenced, reduced motion), §5 (budgets in `web/scripts/check-bundle.mjs` unchanged: entry ≤ 120 KiB gz, total ≤ 400 KiB gz, fonts ≤ 200 KiB; route-level splitting; self-hosted fonts), §6.2 (what coexistence set up and the cutover PR undoes: `index.html` loads the React entry, delete the Svelte files, `svelte.config.js`, the second config and HTML entry, the Svelte dependencies; `npm run check` → `tsc --noEmit`), §6.4 (the Svelte tests are deleted with their components; pure-module tests stay), §7 (`restarts` counts `die → start` pairs; the client refetches on the stack's `change`), §8 log console (SSE resume semantics unchanged), §9 row 23. Cross-plan rulings: entry ceiling 106 kB (18b T1); final / whole-branch review on Opus, effort high, live (user instruction 2026-10-19); the carried notes at lines 19–27 of `.superpowers/sdd/rulings-cross-plan.md`; Plan 19 / 20 / 21 / 22 ledgers' parked items (see "Carried items").

## Spec divergences (author's calls, reviewable)

1. **`lib/router.ts` is trimmed, not deleted.** Plan 16a said the file goes in Plan 23; React still imports `matchRoute` (`app/routes.tsx`) and `safeNext` (`app/auth/LoginScreen.tsx`, `app/hooks/useSessionGuard.ts`), so the module keeps those two pure helpers and their tests and loses the Svelte router (`route`, `navigate`, `link`, `startRouter`, `RouteState`).
2. **Three commits for the switch.** §6.2 describes one PR; this plan lands it as (a) entry + config switch with the Svelte plugin still loaded, (b) `git rm` of the Svelte sources, (c) toolchain removal — every commit builds and tests green, and (b) is a pure deletion so `git revert` / `git checkout <sha>^ -- <path>` recovers any file.
3. **The dev-server fallback goes.** `app/devFallback.ts` existed only because `app.html` was a second entry; with `index.html` as the entry Vite's default `appType: 'spa'` serves it for navigations, so the plugin, the module and its test are deleted.
4. **Follow-mode `end` means "nothing left to follow".** Today a follow stream ends when every container's Docker stream ends (a `docker restart` ends it). After Task 8 the stream stays open across restarts and recreation, and `end` is sent when the stack (or the filtered service, or the single container) has no listed container left. Event names, fields, ids and `Last-Event-ID` resume are unchanged; `docs/api.md` gains one sentence. **Flagged for the user** (see Global Constraints).
5. **A lifecycle event forces a `change`.** Today the cache publishes `change` only when the projected listing differs; a quick restart (running → running, same id) publishes nothing, so the client never refetches `/health` and the restart count stays stale until something else changes — the "first run counted only for the service whose health changed" symptom (the health-checked service flips `starting` → `healthy` and produces changes; the plain service does not). Task 7 publishes `change` for the project of every forwarded lifecycle event except `health_status`. `docs/api.md` wording is clarified; the contract ("refetch the affected stack on `change`") is unchanged.
6. **`guards.test.ts` keeps the `svelte/store` guard** after the dependency is gone (cheap insurance against a copy-paste import) and drops the `{@html` detector with the Svelte sources.
7. **The palette fallback lives in the eager chunk.** A `Dialog` with one input (~0.3 kB gz) is the price of not dropping keystrokes; the palette itself stays lazy and the entry stays ≤ 106 kB. The host also prefetches the palette chunk on idle (one `requestIdleCallback`, `setTimeout` 1500 ms fallback), so the fallback is rarely seen.
8. **The repo's "specs and plans for shipped features are removed once implemented" convention is not applied here.** Removing `docs/superpowers/specs/2026-10-19-stackstead-react-evolve-v2-design.md` and plans 15–23 is a one-line decision for the user after merge; this plan touches no plan or spec file.

## Global Constraints

- **API contract unchanged.** No route, method, status code, event name, field, id format or query parameter changes. Task 7 and Task 8 are **server behaviour changes with no API change** and are flagged in their headers; the user approves PR merges and no extra approval is needed for behaviour fixes. One item **needs the user's explicit OK before it is mandatory**: Task 8 rewrites the sentence in `docs/api.md` that says when `end` is sent in follow mode (divergence 4). If the user considers that a contract change, Task 8 is dropped from this plan and the item stays on the deferred list; nothing else depends on it.
- **Embed / serve contract unchanged:** `web/embed.go` (`all:dist`, placeholder when unbuilt), `internal/api/spa.go` (immutable `/assets/*`, dot-segment 404, `index.html` fallback with CSP) are not edited. `internal/api/server_test.go` and `cmd/stackstead/e2e_test.go` reference only `index.html`, `/assets/*`, `#app` and a woff2 preload, none of which is Svelte-specific, so they stay as they are (verified in Task 1).
- **Budgets:** `web/scripts/check-bundle.mjs` unchanged (entry ≤ 120 KiB gz, total ≤ 400 KiB gz, fonts ≤ 200 KiB; CodeMirror / uPlot never eager); `bundle.test.ts` keeps the 106 kB ceiling and every lazy-chunk pin, now against `web/dist`; every task records the `bundle: entry … kB gz` line from `npm run build`.
- **Deleting Svelte deletes nothing React imports.** Task 2 Step 1 is the pre-deletion verification (grep + the reachability list below); Step 5 is the post-deletion proof (`tsc --noEmit` over all of `src/`, `npm test`, `npm run build`, `make check`). The reachability result from planning, which Step 1 re-checks: every non-test `web/src/lib/**/*.ts` is imported from `web/src/app` (prod code or tests), `lib/storage/fixtures.ts` only from app tests (kept), `lib/router.ts` only for `matchRoute` and `safeNext`.
- **No code bodies in this plan;** test-first for every behaviour change (Tasks 3–8); conventional commits with explicit `git add <paths>` / `git rm <paths>` (never `git add -A`); every commit ends with the attribution trailer the environment prescribes.
- **Plan 22 lessons stay in force:** secret inputs stay uncontrolled (none touched here); CSS Modules have no top-level bare element selectors; mobile rules are CSS media queries only; a once-guard test clicks the same mounted button inside one `act`; every test passes alone (`npx vitest run <file> -t "<name>"`); guards are mutation-proof (a test must fail when the guard is removed).
- **Strings the existing tests assert are unchanged** except the avatar's accessible name (`Log out` → `Log out <username>`, Task 5; its `title` stays `<username> · Log out`).
- **Accessibility:** focus never lands on `body` after an action this plan touches (Escape from a peek, a detail turning not-found, a Settings deep link); the avatar's name is unique on `/settings`; hidden chart axes keep the `role="img"` summary and the legend table.
- **Reduced motion:** the Settings deep-link scroll uses `behavior: 'auto'` (follows the page's `scroll-behavior`, instant under reduced motion), as `goToSection` already does.
- **`make ui` / `make ui-test` rebuild `web/dist`:** run them only when no live review server is embedding `web/dist` (Plan 20 lesson).

## Review Focus

1. **The binary ships React, not a blank page or the Svelte build.** `web/embed_test.go` (`TestDistFallsBackToPlaceholder`: when built, `index.html` has `<div id="app"` and references `/assets/`, never `/src/`), `cmd/stackstead/e2e_test.go` web smoke (`index.html has #app`, every `/assets/*` reference served immutable, `/stacks/anything` and `/settings` return `index.html`, woff2 preload served as `font/woff2`), `web/src/app/html.test.ts` `mounts #app from the React entry`. (Task 1.)
2. **Deletion removed something React still imports, or left the toolchain half-removed.** Task 2 Step 1 grep set, `npm run check` (= `tsc --noEmit` over `src/**/*.ts(x)` including `lib/**/*.test.ts`), `npm test`, `npm run build`, `make check` after each of the two deletion commits, and the post-deletion grep (`SVELTE_REFS` below is empty: no import of `svelte`, `@sveltejs/*` or `@testing-library/svelte`, no `.svelte` path, no `svelte-check`, anywhere under `web/` outside the lockfile). (Task 2.)
3. **Budget or shape regression after the switch.** `bundle.test.ts` `keeps every screen and the palette out of the eager entry` (entry key `index.html`, every lazy name present) and the four `keeps … out of the eager entry` cases with `entry ≤ 106 * 1024`, now measured on `web/dist`; `check-bundle.mjs` runs inside `npm run build`. (Tasks 1, 2, 4.)
4. **Data loss at the breakpoint.** `shell.test.tsx` `crossing 760px keeps the page mounted with its state` (same `<input>` element identity and value after the media query flips both ways). (Task 3.)
5. **Backend regressions.** `internal/state/cache_test.go` `TestLifecycleEventPublishesChangeWhenTheSnapshotIsUnchanged` and `TestHealthStatusAloneDoesNotForceAChange`; `internal/api/observe_test.go` `TestLogsFollowReattachesAfterRestart`, `TestLogsFollowEndsWhenTheStackHasNoContainersLeft` and `TestLogsFollowPumpEndWithoutAChangeIsNotRetriedForever`, with the existing `TestLogsNoContainersEndsImmediately`, `TestLogsFollowDisconnectCancelsStreams`, `TestLogEventsCarryCursorIds` and every cursor / resume test still green. (Tasks 7, 8.)

---

## File structure

| Area | Files | Tasks |
|---|---|---|
| Entry switch | `web/index.html`, `web/vite.config.ts`, `web/package.json`, `web/tsconfig.app.json`, `Makefile`, `.gitignore`, `web/src/app/html.test.ts`, `web/src/bundle.test.ts`, `web/embed_test.go`; deleted: `web/app.html`, `web/vite.app.config.ts`, `web/src/app/devFallback.ts`, `web/src/app/devFallback.test.ts` | T1 |
| Svelte removal | deleted: `web/src/App.svelte`, `web/src/App.test.ts`, `web/src/main.ts`, `web/src/app.css`, `web/src/tokens.test.ts`, `web/src/routes/**`, `web/src/lib/components/**`, every `*.svelte` and Svelte-mounting `*.test.ts` under `web/src/lib/**`, `web/svelte.config.js`, `web/tsconfig.app.json`; edited: `web/package.json`, `web/package-lock.json`, `web/tsconfig.json`, `web/vite.config.ts`, `web/src/vite-env.d.ts`, `web/src/vitest-setup.ts`, `web/src/guards.test.ts`, `web/src/lib/router.ts`, `web/src/lib/router.test.ts`, seven comment lines that name deleted files | T2 |
| Shell | `web/src/app/shell/Shell.tsx`, `web/src/app/shell/shell.test.tsx` | T3 |
| Palette | `web/src/app/shell/PaletteHost.tsx`, `web/src/app/shell/PaletteHost.module.css` (new), `web/src/app/shell/PaletteHost.test.tsx`, `web/src/app/shell/CommandPalette.tsx`, `web/src/app/shell/CommandPalette.test.tsx` | T4 |
| Settings and avatar | `web/src/app/settings/SettingsScreen.tsx`, `web/src/app/settings/settings.test.tsx`, `web/src/app/shell/Sidebar.tsx`, `web/src/app/shell/shell.test.tsx`, `web/src/app/App.test.tsx` | T5 |
| Focus and chart polish | `web/src/app/containers/ContainersScreen.tsx`, `web/src/app/containers/containerKeys.test.tsx`, `web/src/app/storage/StorageImageScreen.tsx`, `web/src/app/storage/StorageVolumeScreen.tsx`, `web/src/app/storage/storageDetails.test.tsx`, `web/src/app/stacks/MetricsChart.tsx`, `web/src/app/stacks/metricsChart.test.tsx` | T6 |
| Backend: change on restart | `internal/state/cache.go`, `internal/state/cache_test.go`, `internal/history/recorder_test.go`, `docs/api.md` | T7 |
| Backend: log re-attach | `internal/api/observe.go`, `internal/api/observe_test.go`, `docs/api.md` | T8 |
| Docs and gate | `docs/development.md`, `Makefile` (comments) | T9 |

Order: T1 → T2 → T3 → T4 → T5 → T6 → T7 → T8 → T9. T1 and T2 are sequential (T2 relies on the React entry shipping). T3–T6 are independent of each other and of T7–T8 (parallel dispatch is fine after T2). Sonnet implements, Opus reviews each task; the final / whole-branch review is Opus (effort high, live) per the user instruction and runs the 390 / 1280 passes (Shell flip with a dirty New stack form, palette typed early, `/settings#settings-account` deep link, light and dark) and the live-host pass (`docker compose restart` of a two-service stack with one health check, `docker restart` of one service with the log console open, Stop → Start from the containers page with a peek open). Tasks contain no manual browser steps.

---

## Carried items: in or deferred

| Item (source) | Decision | Why |
|---|---|---|
| Shell remounts the page across 760 px, wiping unsaved forms and clearing the guard (Plan 20 I2, rulings l.25) | **In — T3** | data loss |
| Palette keystrokes typed before the lazy chunk loads are dropped; Enter runs the first entry (18b e2e, rulings l.19; 16b T6 minor) | **In — T4** | parity with a synchronous palette; a wrong action can run |
| `/settings#section` deep links stay at the top (Plan 22 final, rulings l.27) | **In — T5** | parity (today's anchors work) |
| Avatar and Settings `Log out` share an accessible name (Plan 22 T3, rulings l.26) | **In — T5** | a11y: two controls with one name on one page |
| Escape from inside a peek's log region loses focus (Plan 19 parked) | **In — T6a** | a11y: focus falls to `body` |
| Storage detail turning not-found leaves focus on an unmounted control (Plan 21, user's list) | **In — T6b** | a11y: focus falls to `body` after the page's own delete / a 404 refresh |
| uPlot axes at 80 px (18d/18e → "Plan 23 polish", rulings l.22/24) | **In — T6c** | cheap, design parity; no a11y loss (summary + legend table stay) |
| `docker compose restart` restart counts inconsistent (18b e2e, rulings l.19) | **In — T7** (server behaviour, no API change) | user's stated goal: when it failed and how often it restarted |
| Log stream never re-attaches after `docker restart` / Stop → Start (18c e2e, rulings l.20; Plan 19 F3) | **In — T8** (server behaviour; `end` wording needs the user's OK) | parity gap the console and peek both hit |
| dev-StrictMode double fetch on overview / stack detail (Plan 19 minor 1/4) | **Deferred** | dev-only (StrictMode double effects do not exist in production builds); shifting `ctl.start()` to a microtask would ripple through ~20 stack-detail tests for no shipped change |
| Disclosure state resets when a New stack card collapses (Plan 20 N1) | **Deferred** | no data loss (values live in `useCreateForm`); cosmetic |
| Settings `Log out` lands on `/login?next=%2Fsettings` instead of the rail's guarded logout (Plan 22 divergence 5) | **Deferred** | Svelte parity; harmless; needs the frame's guard plumbed into a route screen |
| Login does not autofocus the username field (Plan 22 note) | **Deferred** | Svelte parity; the final reviewer may ask for it as a one-liner |
| Plan 15 deferred minors (recorder map pruning, baseline retry flag, restart row lost on insert failure, timeline O(b×c×n)) | **Deferred** | none reproduces a user-visible defect; unrelated to the cutover |

---

### Task 1: Ship the React entry (`index.html` → `/src/app/main.tsx`)

Spec §6.2, §5 budgets, §3 embed/serve; divergence 3. Review focus 1 and 3.

**Files:**
- Modify: `web/index.html`, `web/vite.config.ts`, `web/package.json` (scripts only), `web/tsconfig.app.json` (`include`), `Makefile`, `.gitignore`, `web/src/app/html.test.ts`, `web/src/bundle.test.ts`, `web/embed_test.go`
- Delete (`git rm`): `web/app.html`, `web/vite.app.config.ts`, `web/src/app/devFallback.ts`, `web/src/app/devFallback.test.ts`

**Interfaces and data:**
- `web/index.html`: byte-identical to today's `web/app.html` (the only difference from today's `index.html` is the module script: `/src/main.ts` → `/src/app/main.tsx`); `theme.js` first in `<head>`, the three font preloads, `#app`.
- `web/vite.config.ts`: `plugins: [svelte(), react(), keepDistGitignore()]` (the Svelte plugin stays until Task 2); `resolve.alias.$lib` and the `VITEST` `conditions` unchanged; `build: { outDir: 'dist', emptyOutDir: true, target: 'es2022', manifest: true, rollupOptions: { output: { chunkFileNames: 'assets/[name]-[hash].js', codeSplitting: { groups: [<the `eager-shell` group moved verbatim from vite.app.config.ts, with its comment>] } } } }`; no `rollupOptions.input` (Vite's default entry is `index.html`); `server.proxy` and `test` unchanged. No fallback middleware.
- `web/package.json` scripts: delete `dev:app` and `build:app`; `build` stays `vite build && node scripts/check-bundle.mjs` (measures `dist`); `check` stays `svelte-check … && npm run check:app` until Task 2.
- `web/tsconfig.app.json` `include`: replace `vite.app.config.ts` with `vite.config.ts`.
- `Makefile`: delete the `ui-app` target and its `.PHONY` entry; `ui-test: ui ## Run the web UI unit tests (vitest; builds first so bundle.test.ts measures web/dist)`; the header comment lists `ui`, `build`, `release`, `ui-check`, `ui-test`, `check`; the `ui-check` comment says "(tsc after Task 2)" or is left for Task 2.
- `.gitignore`: delete the `web/dist-app/` line. Locally `rm -rf web/dist-app` (untracked build output).
- `web/src/app/html.test.ts`: one page, `index.html`. Cases: `loads /theme.js first in the head, before stylesheets and modules`; `does not reference Google Fonts`; `preloads exactly the three self-hosted fonts` (`['/src/assets/fonts/IBMPlexSans-Regular-latin.woff2', '/src/assets/fonts/IBMPlexSans-Medium-latin.woff2', '/src/assets/fonts/GeistMono-Regular-latin.woff2']`); `mounts #app from the React entry` (contains `<div id="app"></div>` and `<script type="module" src="/src/app/main.tsx"></script>`, does not contain `src="/src/main.ts"`); `the second HTML entry and its config are gone` (`existsSync('app.html')` and `existsSync('vite.app.config.ts')` both `false`, resolved from `process.cwd()`).
- `web/src/bundle.test.ts`: `const dist = join(process.cwd(), 'dist')`; `expect(Object.keys(manifest).find((k) => manifest[k].isEntry)).toBe('index.html')`; `describe('react app build shape')` → `describe('build shape')`; the chunk-name comment points at `vite.config.ts`. Lazy names, markers, the 106 kB ceiling and the `skipIf` guard are unchanged.
- `web/embed_test.go` `TestDistFallsBackToPlaceholder`: when `web.Built()`, the page must also contain `/assets/` and must not contain `/src/` (a built page, never the source HTML).

- [ ] **Step 1: Write the failing tests.** `html.test.ts` as above (red: `index.html` still loads `/src/main.ts`; `app.html` exists). `bundle.test.ts` change (red when run after today's Svelte `npm run build`: `dist` has no `StackDetail` / `CommandPalette` chunks). `embed_test.go` addition (it stays green on any built page; it is a guard for the e2e, not a red step).
- [ ] **Step 2: Implement** the entry switch and the config merge; delete the four files with `git rm`; `rm -rf web/dist-app`.
- [ ] **Step 3: Run** `cd web && npm run build` (record the `bundle: entry … kB gz` line — expected 104.3, ceiling 106 — and that `dist/index.html` references `/assets/*.js`), `npx vitest run src/app/html.test.ts src/bundle.test.ts` (green), `npm run check`, `npm test` (every suite, Svelte included, still green), then from the repo root `make build` (`bin/stackstead`), `go test ./web/ ./internal/api/`, and `make e2e` (the web smoke proves `#app`, immutable `/assets/*`, SPA fallback for `/stacks/anything` and `/settings`, `theme.js` no-cache, woff2 `font/woff2`; Docker / Chrome parts skip when absent).
- [ ] **Step 4: Commit**
  ```bash
  git rm web/app.html web/vite.app.config.ts web/src/app/devFallback.ts web/src/app/devFallback.test.ts
  git add web/index.html web/vite.config.ts web/package.json web/tsconfig.app.json Makefile .gitignore web/src/app/html.test.ts web/src/bundle.test.ts web/embed_test.go
  git commit -m "feat(web): ship the React app from index.html and build it into web/dist"
  ```
  (why: the switch is one line in `index.html` plus moving the React build settings into the one Vite config; the Svelte plugin stays loaded for this commit so every existing test still runs, which keeps the commit independently green and the switch reviewable on its own.)

---

### Task 2: Delete the Svelte app, then its toolchain

Spec §6.2, §6.4; divergences 1, 2, 6. Review focus 2.

**Files:**
- Delete (`git rm`, commit A — sources and Svelte-mounting tests only):
  - `web/src/App.svelte`, `web/src/App.test.ts`, `web/src/main.ts`, `web/src/app.css`, `web/src/tokens.test.ts` (its port is `web/src/app/styles/tokens.test.ts`)
  - `web/src/routes/` (whole directory: `ContainerDetail`, `ContainerList`, `Login`, `NetworkList`, `Settings`, `Setup`, `StackCreate`, `StackDetail`, `StackImport`, `StackList`, `Storage`, `StorageImage`, `StorageVolume` `.svelte` files and the eleven `*.test.ts` beside them incl. `StorageDetails.test.ts`)
  - `web/src/lib/components/` (whole directory: the sixteen `.svelte` files incl. `FieldFixture.test-fixture.svelte`, and `components.test.ts`)
  - `web/src/lib/containers/ContainerActions.svelte`, `web/src/lib/containers/ContainerActions.test.ts`
  - `web/src/lib/dashboard/AlertsCard.svelte`, `EventsFeed.svelte`, `HostTile.svelte`, `TopConsumersCard.svelte`, `dashboard.test.ts`
  - `web/src/lib/networks/CreateNetworkDialog.svelte`, `CreateNetworkDialog.test.ts`
  - `web/src/lib/settings/PasswordForm.svelte`, `PasswordForm.test.ts`, `UpdateCard.svelte`, `UpdateCard.test.ts`
  - `web/src/lib/shell/Rail.svelte`, `TopBar.svelte`, `shell.test.ts`
  - `web/src/lib/stacks/`: `ActionsBar.svelte` + `.test.ts`, `AddServicePanel.svelte` + `.test.ts`, `ComposeEditor.svelte` + `.test.ts`, `DiffView.svelte` + `.test.ts`, `FileBrowser.svelte` + `.test.ts`, `LogsViewer.svelte` + `.test.ts`, `MetricsChart.svelte` + `.test.ts`, `MetricsPanel.svelte` + `.test.ts`, `NetworkPicker.svelte` + `.test.ts`, `OperationPanel.svelte` + `.test.ts`, `OperationsList.svelte` + `.test.ts`, `ServiceCard.svelte` + `.test.ts`, `ServiceEditor.svelte` + `.test.ts`, `ServiceForm.svelte` + `.test.ts`, `ServiceFormHost.test-fixture.svelte`, `StackAside.svelte` + `.test.ts`, `YamlEditor.svelte`, `YamlEditor.test.ts`, `YamlEditor.changed.test.ts`
  - `web/src/lib/storage/CleanupDialog.svelte`, `CleanupDialog.test.ts`
  - Kept on purpose (pure modules and their tests, all imported by React): everything else under `web/src/lib/**/*.ts`, including `cm.ts` / `cm.test.ts`, `console.ts`, `detail.ts`, `storage/fixtures.ts`, `store.ts`, `stores/*`, `shell/breadcrumb.ts`, `shell/icons.ts`.
- Delete (`git rm`, commit B — toolchain): `web/svelte.config.js`, `web/tsconfig.app.json`
- Modify (commit B): `web/package.json`, `web/package-lock.json`, `web/tsconfig.json`, `web/vite.config.ts`, `web/src/vite-env.d.ts`, `web/src/vitest-setup.ts`, `web/src/guards.test.ts`, `web/src/lib/router.ts`, `web/src/lib/router.test.ts`, `web/src/lib/stacks/detail.ts` (three comments), `web/src/app/routes/NotFound.tsx` (one comment), `web/src/app/hooks/useSessionGuard.ts` (one comment), `web/src/app/hooks/useUnsavedGuard.ts` (one comment), `web/src/app/App.test.tsx` (header comment)

**Interfaces and data (commit B):**
- `package.json`: `dependencies` lose `svelte`; `devDependencies` lose `@sveltejs/vite-plugin-svelte`, `@testing-library/svelte`, `svelte-check`; scripts: `"check": "tsc --noEmit"`, `check:app` deleted; `dev`, `build`, `preview`, `test`, `test:watch` unchanged. The lockfile changes come from `npm uninstall svelte @sveltejs/vite-plugin-svelte @testing-library/svelte svelte-check` (exact pins elsewhere untouched; `guards.test.ts` `pins every dependency to an exact version` stays green).
- `tsconfig.json` (merged shape): `compilerOptions` keep today's `target`, `module`, `moduleResolution`, `lib`, `strict`, `noUncheckedIndexedAccess`, `verbatimModuleSyntax`, `isolatedModules`, `skipLibCheck`, `noEmit`, `paths`; add `"jsx": "react-jsx"`; `types: ["vite/client", "node"]`; `include: ["src/**/*.ts", "src/**/*.tsx", "vite.config.ts"]`; no `exclude` (the `lib/**/*.test.ts` files are now type-checked by `tsc` — if `tsc` reports errors in them, fix the test typing in place; do not exclude them and add no `any`).
- `vite.config.ts`: `plugins: [react(), keepDistGitignore()]`; the `VITEST` `conditions` spread and its comment go (it existed for Svelte's server/browser builds); everything else as left by Task 1.
- `web/src/vite-env.d.ts`: `/// <reference types="vite/client" />` only.
- `web/src/vitest-setup.ts`: imports `cleanup` from `@testing-library/react` at the top and registers `afterEach(cleanup)` unconditionally; the `testPath.endsWith('.tsx')` gate, the dynamic import and the comment about Svelte timing are removed; the fake fetch / stream exports are unchanged.
- `web/src/guards.test.ts`: `sourceFiles` matches `/\.(ts|tsx)$/`; `findHtmlTag` and its unit test and the repository case `has no {@html anywhere in src/` are removed; `findRawHtml` keeps the `dangerouslySetInnerHTML` check for `.ts` / `.tsx` (its unit-test fixtures rename `d.svelte` → drop that fixture; the expected list becomes `['a.tsx', 'c.ts']`); `findSvelteStoreImports`, `findHandoffReferences`, `findRangedVersions` and their cases stay (the svelte/store unit-test fixture `c.svelte` becomes `c.tsx` with the same import text).
- `web/src/lib/router.ts`: exports only `matchRoute(pattern: string, path: string): Record<string, string> | null` and `safeNext(next: string | null): string` (plus any type they need); `route`, `navigate`, `link`, `startRouter`, `RouteMatch` / `RouteState` and the `$lib/store` import go. `router.test.ts` keeps the `matchRoute` and `safeNext` cases verbatim and drops the navigation / link / history cases.
- Comments: the seven lines that name a deleted file stop doing so — `lib/stacks/detail.ts` lines 1, 120, 377 ("ported from the previous UI's stack detail page", "mirrors the component's lifetime", "as in the previous page"), `app/routes/NotFound.tsx` line 4 ("the previous app's fallback"), `app/hooks/useSessionGuard.ts` line 37 and `app/hooks/useUnsavedGuard.ts` line 29 ("The previous app's session guards" / "link guard"), the `App.test.tsx` header ("Port of the previous app's frame tests"). Other comments that mention Svelte as history (`lib/store.ts` describing the `svelte/store` contract, `lib/stacks/metrics.ts`, `liveMetrics.ts`, `stores/stacks.ts`, `app/hooks/useStore.ts`, `app/components/ListEditor.tsx`, `app/storage/StorageScreen.tsx` and three test-kit comments) are accurate and stay.

- [ ] **Step 1: Verify before deleting** (record the outputs in the task report): `git grep -n "\.svelte['\"]" -- web/src/app` → empty; `git grep -n -i svelte -- web/src/app web/src/lib ':!*.test.ts' ':!*.svelte'` → comments only (the planning run saw 16 lines across 11 files, none an import; the seven naming deleted files are reworded in commit B); define `SVELTE_REFS` = `git grep -n -E "from ['\"](svelte|@sveltejs/|@testing-library/svelte)|import\(['\"]svelte|\.svelte['\"]|svelte-check|svelte\.config" -- web ':!web/package-lock.json' ':!web/src/guards.test.ts'` (today it lists `vite.config.ts`, `svelte.config.js`, `tsconfig.json`, `package.json`, `vitest-setup.ts`, `vite-env.d.ts`, `App.test.ts`, `tokens.test.ts` and the Svelte tests); for `web/src/lib/router.ts`: `git grep -n "lib/router" -- web/src/app` → `routes.tsx` (`matchRoute`), `auth/LoginScreen.tsx` and `hooks/useSessionGuard.ts` (`safeNext`) only; for every non-test `web/src/lib/**/*.ts` module, `git grep -l "<path without extension, as $lib/… or relative>" -- web/src/app web/src/lib` finds at least one importer outside the deletion list (a scratchpad script may automate this; it is not committed). Record `npm test` totals (files / tests) and the `bundle: entry` line before deletion.
- [ ] **Step 2: Commit A — the sources.**
  ```bash
  git rm web/src/App.svelte web/src/App.test.ts web/src/main.ts web/src/app.css web/src/tokens.test.ts
  git rm -r web/src/routes web/src/lib/components
  git rm web/src/lib/containers/ContainerActions.svelte web/src/lib/containers/ContainerActions.test.ts
  git rm web/src/lib/dashboard/AlertsCard.svelte web/src/lib/dashboard/EventsFeed.svelte web/src/lib/dashboard/HostTile.svelte web/src/lib/dashboard/TopConsumersCard.svelte web/src/lib/dashboard/dashboard.test.ts
  git rm web/src/lib/networks/CreateNetworkDialog.svelte web/src/lib/networks/CreateNetworkDialog.test.ts
  git rm web/src/lib/settings/PasswordForm.svelte web/src/lib/settings/PasswordForm.test.ts web/src/lib/settings/UpdateCard.svelte web/src/lib/settings/UpdateCard.test.ts
  git rm web/src/lib/shell/Rail.svelte web/src/lib/shell/TopBar.svelte web/src/lib/shell/shell.test.ts
  git rm web/src/lib/stacks/ActionsBar.svelte web/src/lib/stacks/ActionsBar.test.ts web/src/lib/stacks/AddServicePanel.svelte web/src/lib/stacks/AddServicePanel.test.ts web/src/lib/stacks/ComposeEditor.svelte web/src/lib/stacks/ComposeEditor.test.ts web/src/lib/stacks/DiffView.svelte web/src/lib/stacks/DiffView.test.ts web/src/lib/stacks/FileBrowser.svelte web/src/lib/stacks/FileBrowser.test.ts web/src/lib/stacks/LogsViewer.svelte web/src/lib/stacks/LogsViewer.test.ts web/src/lib/stacks/MetricsChart.svelte web/src/lib/stacks/MetricsChart.test.ts web/src/lib/stacks/MetricsPanel.svelte web/src/lib/stacks/MetricsPanel.test.ts web/src/lib/stacks/NetworkPicker.svelte web/src/lib/stacks/NetworkPicker.test.ts web/src/lib/stacks/OperationPanel.svelte web/src/lib/stacks/OperationPanel.test.ts web/src/lib/stacks/OperationsList.svelte web/src/lib/stacks/OperationsList.test.ts web/src/lib/stacks/ServiceCard.svelte web/src/lib/stacks/ServiceCard.test.ts web/src/lib/stacks/ServiceEditor.svelte web/src/lib/stacks/ServiceEditor.test.ts web/src/lib/stacks/ServiceForm.svelte web/src/lib/stacks/ServiceForm.test.ts web/src/lib/stacks/ServiceFormHost.test-fixture.svelte web/src/lib/stacks/StackAside.svelte web/src/lib/stacks/StackAside.test.ts web/src/lib/stacks/YamlEditor.svelte web/src/lib/stacks/YamlEditor.test.ts web/src/lib/stacks/YamlEditor.changed.test.ts
  git rm web/src/lib/storage/CleanupDialog.svelte web/src/lib/storage/CleanupDialog.test.ts
  git commit -m "chore(web): delete the Svelte app, its components and their tests"
  ```
  Then run `cd web && npm run check && npm test && npm run build` — green on this commit alone (the Svelte plugin and `svelte-check` still exist and simply find no `.svelte` files); the test total equals the Step 1 total minus the deleted files' tests, no React test changed; the entry line is unchanged.
  (why: a pure deletion commit is recoverable file by file with `git checkout <sha>^ -- <path>` and is reviewable as a list; nothing else moves in it.)
- [ ] **Step 3: Implement commit B** (`npm uninstall …`, the config merge, `vitest-setup.ts`, `guards.test.ts`, `router.ts` trim, the comments). Red step for the one behaviour-bearing edit: `npx vitest run src/guards.test.ts` must fail on the renamed fixtures before `findRawHtml` / the fixtures are adjusted, and `src/lib/router.test.ts` must fail to compile until the trimmed cases are removed.
- [ ] **Step 4: Run** `cd web && npm run check` (now `tsc --noEmit`; fix any typing it reports in `lib/**/*.test.ts`), `npm test`, `npm run build` (entry line unchanged), then `make check` and `make build` from the root; `SVELTE_REFS` (Step 1) → empty; `git grep -n -E "svelte" -- web/package.json web/tsconfig.json web/vite.config.ts web/src/vite-env.d.ts web/src/vitest-setup.ts` → empty; `git grep -n -i svelte -- docs README.md Makefile ':!docs/superpowers'` → only `docs/development.md` (Task 9 rewrites it); `git grep -n "dist-app\|build:app\|check:app\|dev:app\|app\.html\|vite\.app\.config" -- . ':!docs/superpowers' ':!.superpowers'` → only `docs/development.md`.
- [ ] **Step 5: Commit B — the toolchain.**
  ```bash
  git rm web/svelte.config.js web/tsconfig.app.json
  git add web/package.json web/package-lock.json web/tsconfig.json web/vite.config.ts web/src/vite-env.d.ts web/src/vitest-setup.ts web/src/guards.test.ts web/src/lib/router.ts web/src/lib/router.test.ts web/src/lib/stacks/detail.ts web/src/app/routes/NotFound.tsx web/src/app/hooks/useSessionGuard.ts web/src/app/hooks/useUnsavedGuard.ts web/src/app/App.test.tsx
  git commit -m "chore(web): drop the Svelte toolchain and type-check the React tree with tsc"
  ```
  (why: the dependency and config removal is the step that can break the build, so it sits apart from the deletion and is proved by `tsc` over every file plus the full suites.)

---

### Task 3: Shell keeps one tree across the 760 px breakpoint (I2)

Rulings l.25 (Plan 20 I2). Review focus 4.

**Files:**
- Modify: `web/src/app/shell/Shell.tsx`, `web/src/app/shell/shell.test.tsx`

**Interfaces:**
- `ShellProps` unchanged. `Shell` renders one root `div` whose `className` is `styles.mobile` or `styles.desktop`, with children at fixed positions in both layouts: position 0 is `MobileTopBar` (mobile) or `Sidebar` (desktop); position 1 is always `div.column` containing position 0 `TopBar` (desktop) or `null` (mobile), position 1 `banners`, position 2 `div.content` with `children`; position 2 is `TabBar` (mobile) or `null`. `Shell.module.css` is unchanged (`.column` is `flex: 1; min-width: 0; display: flex; flex-direction: column`, which fills a column parent as well as a row parent, so the mobile layout is visually identical).

- [ ] **Step 1: Write the failing test** in `shell.test.tsx` `describe('Mobile')`: `crossing 760px keeps the page mounted with its state` — `vi.stubGlobal('matchMedia', …)` returning `{ matches: false, media, addEventListener(type, fn) { listener = fn }, removeEventListener() { listener = undefined } }`; `renderIn(<Shell {...shellProps} username="ana"><main><input aria-label="Draft" /></main></Shell>)`; `await user.type(screen.getByRole('textbox', { name: 'Draft' }), 'hello')`; `const before = screen.getByRole('textbox', { name: 'Draft' })`; `act(() => listener?.({ matches: true }))`; expect `screen.getByRole('textbox', { name: 'Draft' })` `toBe(before)` and `.value === 'hello'`; the tab bar is present (`within(screen.getByRole('navigation', { name: 'Main' })).getAllByRole('link')` has five entries) and `screen.queryByRole('button', { name: /^Log out/ })` is `null`; `act(() => listener?.({ matches: false }))` → still the same element with `hello`, the avatar button is back, `screen.getAllByRole('main')` has length 1 throughout. Red today: the second `getByRole` returns a new element with an empty value.
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/shell/shell.test.tsx` — FAIL on the identity assertion.
- [ ] **Step 3: Implement** the single-tree render. The two existing Mobile cases (`under 760px the sidebar is replaced …`, `on desktop the sidebar shows …`) stay green unchanged.
- [ ] **Step 4: Run** `npx vitest run src/app/shell src/app/App.test.tsx`, `make ui-check`, `npm run build` (entry line recorded; expected unchanged).
- [ ] **Step 5: Commit**
  ```bash
  git add web/src/app/shell/Shell.tsx web/src/app/shell/shell.test.tsx
  git commit -m "fix(web): keep the page mounted when the shell switches layout at 760px"
  ```
  (why: a resize or a device rotation must never discard a form the user is filling in; the fix is structural, so one identity test pins it.)

---

### Task 4: Palette keystrokes are kept while the chunk loads

Rulings l.19 (18b e2e), 16b T6 minor; divergence 7. Review focus 3 (entry stays ≤ 106 kB).

**Files:**
- Modify: `web/src/app/shell/PaletteHost.tsx`, `web/src/app/shell/PaletteHost.test.tsx`, `web/src/app/shell/CommandPalette.tsx`, `web/src/app/shell/CommandPalette.test.tsx`
- Create: `web/src/app/shell/PaletteHost.module.css`

**Interfaces:**
- `CommandPaletteProps` gains `initialQuery?: string`; `CommandPalette` seeds `query` with it once at mount (`useState(initialQuery ?? '')`); everything else unchanged.
- `PaletteHost` keeps its props (`open`, `runningOps`, `onClose`, `onNavigate`, `load`). State: `mod: PaletteModule | undefined` (the resolved module), `loading: boolean`, `draft: string` (what was typed before the module arrived; reset to `''` on close). Behaviour: on the first `open` without `mod`, call `load()`; success → `setMod`; failure → `onClose()` and the existing toast `Could not open the command palette`, and the next open calls `load()` again. While `open && !mod` render `PaletteFallback`; once `mod` exists render `<mod.default open={open} runningOps onClose onNavigate initialQuery={draft} />` and keep it mounted (today's "mounted after first open" behaviour). `React.lazy`, `Suspense` and the `ErrorBoundary` wrapper are removed from this file.
- `PaletteFallback` (not exported; in `PaletteHost.tsx`): `Dialog open onClose label="Command palette"` containing one `input` (`className` from `PaletteHost.module.css`, `aria-label="Search or run a command"`, `placeholder="Jump to a stack, container or run an action…"`, `autoFocus`, `value={draft}`, `onChange`) whose `onKeyDown` swallows `Enter` (`preventDefault`, nothing runs before the palette exists); Escape closes through `Dialog`.
- Prefetch: a module-level `prefetched` flag plus one mount effect: when not yet prefetched, schedule `load()` through `requestIdleCallback` when it exists, else `setTimeout(…, 1500)`; the result goes to `setMod`, a rejection is swallowed (no toast); the timer / idle callback is cancelled on unmount. The flag makes StrictMode's double effect call `load` once.
- `PaletteHost.module.css`: `.input` (full width, 40 px tall, 15 px, tokens only) and `.hint` (12 px muted `Loading…`). No top-level bare element selectors.

- [ ] **Step 1: Write the failing tests.** `PaletteHost.test.tsx` (reuse the file's existing render helper and the stacks seeding from `CommandPalette.test.tsx`; seed stacks `redis` and `web`): `keys typed before the palette module arrives seed its query` (`load` = `vi.fn(() => deferred.promise)`; render open; `await user.type(screen.getByRole('textbox', { name: 'Search or run a command' }), 'red')`; `await user.keyboard('{Enter}')` → `onNavigate` not called; `deferred.resolve({ default: CommandPalette })`; `await screen.findByRole('listbox', { name: 'Results' })`; the palette's input value is `red` and the results list `redis` only); `Escape while the module is loading closes the host` (`onClose` called once, `load` not re-called); `a failed first load toasts, closes and retries on the next open` (today's two cases, adapted to the promise: `load` rejects → toast `Could not open the command palette`, `onClose` once; re-open → `load` called again); `prefetches the palette once on idle without opening it` (`vi.useFakeTimers()`; render closed under `<StrictMode>`; `vi.advanceTimersByTime(1500)` → `load` called once; `screen.queryByRole('dialog')` is `null`; set `open` → the listbox is present without a fallback textbox); `a failed prefetch is silent` (`load` rejects during prefetch → `get(toasts)` is empty; a later open calls `load` again). `CommandPalette.test.tsx`: `initialQuery seeds the input and filters the results` (`initialQuery="red"` → input value `red`, results `redis` only).
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/shell/PaletteHost.test.tsx src/app/shell/CommandPalette.test.tsx` — FAIL: no textbox before the module resolves; `initialQuery` ignored; `load` not called on idle.
- [ ] **Step 3: Implement** the host, the fallback, the prefetch and the prop.
- [ ] **Step 4: Run** `npx vitest run src/app/shell src/app/App.test.tsx src/app/App.chunkError.test.tsx`, `make ui-check`, `npm run build` then `npx vitest run src/bundle.test.ts` (the `CommandPalette` chunk is still lazy; record the entry line — expected +0.3 kB at most, ceiling 106).
- [ ] **Step 5: Commit**
  ```bash
  git add web/src/app/shell/PaletteHost.tsx web/src/app/shell/PaletteHost.module.css web/src/app/shell/PaletteHost.test.tsx web/src/app/shell/CommandPalette.tsx web/src/app/shell/CommandPalette.test.tsx
  git commit -m "fix(web): keep keystrokes typed before the command palette chunk loads"
  ```
  (why: a palette that drops the first characters and runs the first entry on Enter is a wrong-action risk; buffering the draft and prefetching the chunk removes it without making the palette eager.)

---

### Task 5: `/settings#section` deep links and distinct `Log out` names

Rulings l.26–27 (Plan 22 T3 and final). Global Constraints (accessibility).

**Files:**
- Modify: `web/src/app/settings/SettingsScreen.tsx`, `web/src/app/settings/settings.test.tsx`, `web/src/app/shell/Sidebar.tsx`, `web/src/app/shell/shell.test.tsx`, `web/src/app/App.test.tsx`

**Interfaces:**
- `SettingsScreen.tsx`: extract `scrollToSection(sectionId: string): boolean` from `goToSection` (finds `document.getElementById(sectionId)`, `scrollIntoView({ behavior: 'auto' })`, focuses its `h2` with `preventScroll: true`; returns whether the element existed); `goToSection` calls it and then `replaceState` as today. New: `const { hash } = useLocation()` and a `useLayoutEffect` keyed on `hash` that, when `hash` is `#<one of the five section ids the nav links use>` and the element exists, calls `scrollToSection(hash.slice(1))`. Unknown hashes and no hash do nothing.
- `Sidebar.tsx`: the avatar button's `aria-label` becomes `` `Log out ${username}` `` when `username` is set, else `Log out`; its `title` stays `` `${username ?? ''} · Log out` ``.

- [ ] **Step 1: Write the failing tests.** `settings.test.tsx`: `a #section deep link scrolls to the card and focuses its heading once it is mounted` (`const scroll = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})` — jsdom has no implementation; `mountAt('/settings#settings-account')`; `await screen.findByRole('heading', { level: 2, name: 'Account' })`; `expect(scroll).toHaveBeenCalledTimes(1)` and `scroll.mock.instances[0]` is `document.getElementById('settings-account')`; `document.activeElement` is the Account `h2`; `scroll.mockRestore()` in `afterEach`); `without a hash nothing scrolls` (`mountAt('/settings')` → `scroll` not called); `an unknown hash is ignored` (`/settings#settings-nope` → not called, no error). `shell.test.tsx`: `the avatar is named after the user so it never collides with the Settings Log out button` (`screen.getByRole('button', { name: 'Log out ana' })` has `title` `ana · Log out`); update the three existing lookups (`shell.test.tsx` lines 146 and 404 → `'Log out ana'`; line 388 → `queryByRole('button', { name: /^Log out/ })`). `App.test.tsx`: lines 235, 331, 470 → `'Log out admin'`; add `on /settings the two logout controls have distinct accessible names` (`getByRole('button', { name: 'Log out' })` is inside the Account card — `within(screen.getByRole('region', { name: 'Account' }))` or `closest('section')` check; `getByRole('button', { name: 'Log out admin' })` is inside `nav[aria-label="Main"]`). `settings.test.tsx` line 284 (`Log out` on the page) is unchanged.
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/settings/settings.test.tsx src/app/shell/shell.test.tsx src/app/App.test.tsx` — FAIL: no scroll call; `Log out ana` not found.
- [ ] **Step 3: Implement** both changes.
- [ ] **Step 4: Run** the three files again, `make ui-test`, `make ui-check`, `npm run build` (entry line recorded).
- [ ] **Step 5: Commit**
  ```bash
  git add web/src/app/settings/SettingsScreen.tsx web/src/app/settings/settings.test.tsx web/src/app/shell/Sidebar.tsx web/src/app/shell/shell.test.tsx web/src/app/App.test.tsx
  git commit -m "fix(web): scroll Settings deep links to their card and name the avatar after the user"
  ```
  (why: `/settings#settings-account` is a link people bookmark and the two `Log out` controls read identically to a screen reader; both are small, both are parity or a11y.)

---

### Task 6: Focus after Escape and after not-found; compact chart axes

Plan 19 parked (Esc-from-log), Plan 21 (detail turns not-found), rulings l.22/24 (uPlot at 80 px).

**Files:**
- Modify: `web/src/app/containers/ContainersScreen.tsx`, `web/src/app/containers/containerKeys.test.tsx`, `web/src/app/storage/StorageImageScreen.tsx`, `web/src/app/storage/StorageVolumeScreen.tsx`, `web/src/app/storage/storageDetails.test.tsx`, `web/src/app/stacks/MetricsChart.tsx`, `web/src/app/stacks/metricsChart.test.tsx`

**Interfaces:**
- (a) `ContainersScreen` `useContainerKeys({ … onClose })`: when a peek is open, close it and focus the row element of the focused container (`rowRefs.current.get(visible[focused]?.id)?.focus()`); when no peek is open, nothing changes.
- (b) `StorageImageScreen` and `StorageVolumeScreen`: the `h1` gets `tabIndex={-1}` and a ref; an effect that runs when `item` becomes `null` (not-found) focuses it with `preventScroll: true`. The `title` strings are unchanged (`Image not found`, `Volume not found`).
- (c) `MetricsChart`: `const compact = height < 120`; `axes: [{ ...axis, show: !compact }, { ...axis, size: compact ? 48 : 70, values }]`; nothing else in the options changes; the `role="img"` summary and the legend table are untouched.

- [ ] **Step 1: Write the failing tests.** `containerKeys.test.tsx`: `Escape from inside the log region closes the peek and returns focus to its row` (open the first row's peek with Enter as the existing `Escape closes the peek even from inside its log region …` case does; `screen.getByRole('log').focus()`; `await user.keyboard('{Escape}')` → `screen.queryByRole('log')` is `null` and `document.activeElement` is the first row element the kit focuses in `j and k move the focused row …`). `storageDetails.test.tsx`: `when the image disappears the not-found heading takes focus` and `when the volume disappears the not-found heading takes focus` (first `GET` answers the fixture; trigger a refresh the way the file's `navigating from one image to another reloads the page` / the kit's live mock does; the second `GET` answers `404` → `await screen.findByRole('heading', { level: 1, name: 'Image not found' })` (resp. `Volume not found`) and `document.activeElement` is that heading). `metricsChart.test.tsx`: `an 80px chart hides the time axis and narrows the value axis` (`height={80}` → `optsOf(createPlot).axes[0].show === false`, `axes[1].size === 48`) and `a full-height chart keeps both axes` (no `height` prop → `axes[0].show !== false`, `axes[1].size === 70`).
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/containers/containerKeys.test.tsx src/app/storage/storageDetails.test.tsx src/app/stacks/metricsChart.test.tsx` — FAIL: `document.activeElement` is `body`; `axes[0].show` undefined.
- [ ] **Step 3: Implement** (a), (b), (c).
- [ ] **Step 4: Run** `make ui-test`, `make ui-check`, `npm run build` (entry line recorded; `uPlot.esm` still lazy).
- [ ] **Step 5: Commit**
  ```bash
  git add web/src/app/containers/ContainersScreen.tsx web/src/app/containers/containerKeys.test.tsx web/src/app/storage/StorageImageScreen.tsx web/src/app/storage/StorageVolumeScreen.tsx web/src/app/storage/storageDetails.test.tsx web/src/app/stacks/MetricsChart.tsx web/src/app/stacks/metricsChart.test.tsx
  git commit -m "fix(web): return focus after a peek closes or a detail disappears, and trim axes on 80px charts"
  ```
  (why: both focus cases drop a keyboard user on `body`; the axis trim is the deferred design polish and costs two option values.)

---

### Task 7: Backend — publish `change` for a restart that leaves the listing identical

**Server behaviour change, no API change.** Spec §7 (client refetches `/health` on `change`), divergence 5. Review focus 5.

**Files:**
- Modify: `internal/state/cache.go`, `internal/state/cache_test.go`, `internal/history/recorder_test.go`, `docs/api.md` (Events bullet, one clause)

**Design:**
- `session()` keeps `touched map[string]struct{}`: for every forwarded event whose `Action` is in `ForwardedActions` and is not `health_status`, with `ev.Labels["com.docker.compose.project"] != ""`, the project is added between arming the debounce timer and its firing. `exec_*` events stay ignored.
- `resync(ctx, forced []string)` (or a field read under `c.mu`): publishes `changedProjects(old, fresh)` united with `forced`, deduplicated and sorted, then clears `touched`. The initial resync and the reconnect baseline pass no forced projects. A forced project missing from both snapshots still publishes (clients refetch and get the stack's current answer).
- The recorder is untouched: it inserts the `restart` marker on the `start` event, before the debounce timer fires, so the client's refetch on the forced `change` sees the row.
- `docs/api.md` Events bullet: "`change` (`project`, `seq`) when a project's containers changed — including a restart that leaves the listing identical — refetch the affected stack on `change`."

- [ ] **Step 1: Write the failing tests** in `internal/state/cache_test.go` (helpers `start`, `waitFor`, `ctr` (sets the project label), `next`, `noChange` exist): `TestLifecycleEventPublishesChangeWhenTheSnapshotIsUnchanged` (`src` with `ctr("a", "a1", "running")`; subscribe; send `die` then `start` for `a1` with `Labels` project `a`; the listing is not changed; `next(t, changes).Project == "a"`; then `noChange(t, changes, 60*time.Millisecond)`); `TestHealthStatusAloneDoesNotForceAChange` (send `health_status` with `Health: "healthy"` for `a1`, listing unchanged → `noChange`); `TestForcedChangeIsCoalescedWithTheDiff` (`src.set` `a1` → `exited` and send `die` → exactly one change for `a`, then `noChange`); `TestEventWithoutProjectLabelForcesNothing` (`die` with no labels, listing unchanged → `noChange`). `internal/history/recorder_test.go`: `TestRecorder_ComposeRestartCountsEachService` (containers `db` with a health check and `app` without, project `web`; events in Docker's order `kill, die, start, restart` for `db` then for `app`, then `health_status healthy` for `db` → the journal holds exactly two `restart` rows, services `db` and `app`; this one is a pin and is expected to pass before the fix).
- [ ] **Step 2: Run** `go test ./internal/state/ -run 'TestLifecycleEvent|TestHealthStatusAlone|TestForcedChange|TestEventWithoutProject'` — FAIL on the first and third (no change published / wrong count); `go test ./internal/history/ -run TestRecorder_ComposeRestart` — PASS (pin).
- [ ] **Step 3: Implement** the `touched` set and the forced publish; update the `docs/api.md` clause.
- [ ] **Step 4: Run** `go test -race ./internal/state/ ./internal/history/ ./internal/api/`, `go vet ./...`, `gofmt -l .` empty.
- [ ] **Step 5: Commit**
  ```bash
  git add internal/state/cache.go internal/state/cache_test.go internal/history/recorder_test.go docs/api.md
  git commit -m "fix(state): publish a change for lifecycle events even when the container listing is unchanged"
  ```
  (why: a quick `docker restart` leaves the projected listing identical, so no `change` reached the browser and the health timeline's restart count went stale for exactly the services without a health check; forcing the change makes the client refetch and costs one extra stack fetch per restart. Server behaviour only; routes, events and fields unchanged.)

---

### Task 8: Backend — follow-mode log streams re-attach after restart or recreation

**Server behaviour change, no API change** (event names, fields, ids and `Last-Event-ID` resume unchanged). **The follow-mode `end` timing changes and `docs/api.md` is reworded — needs the user's OK (divergence 4); if declined, skip this task.** Rulings l.20 (18c), Plan 19 F3. Review focus 5.

**Files:**
- Modify: `internal/api/observe.go`, `internal/api/observe_test.go`, `docs/api.md` (Logs bullet; the `/api/containers/{id}/logs` paragraph)

**Design (follow mode only; `follow=false` keeps today's code path and `end` after the last line):**
- `streamContainerLogs(w, r, cs, opts, match func(engine.Container) bool)`: the stack route passes `match` = same `Project` as the stack and, when `?service=` is set, the same compose service; the container route passes `match` = same `ID`.
- Per-container state `pump { c engine.Container; cursor logCursor; active bool; quiet bool }` in a map keyed by container id, guarded by a mutex; the initial pumps are today's (resume from `Last-Event-ID` cursors), and every delivered line updates the pump's `cursor` (the value already computed for the event id).
- The handler subscribes to `s.opts.Observe.SubscribeChanges()` for the request's lifetime (cancelled on return). A `Change` with `Project == <the stack's project>` or `Project == ""` triggers a reconcile; a closed changes channel (slow-subscriber drop) resubscribes and reconciles.
- Reconcile: `Containers(ctx)` (on error: skip, wait for the next change); for each listed container with `match(c)` and `c.State == "running"` and no active pump, start a pump — a known id with a cursor resumes with `Tail: -1`, `Since: time.Unix(0, cursor.TS)` and the ordinal skip (today's resume logic); a new id uses the request's `opts`. Afterwards, if no pump is active and no listed container matches, send `end` and return.
- Pump end (its Docker stream closed or errored): mark inactive (an error still emits today's `error` event first) and reconcile once immediately. A pump started by a pump-end reconcile that ends without delivering a line sets `quiet`, and the pump-end reconcile does not start it again; the next `Change` clears `quiet` and reconciles normally. This bounds a stale listing to one retry.
- The container passed to `Logs` is the listed one (full labels and TTY information), never one built from an event.
- `docs/api.md` Logs bullet, appended: "With `follow`, the stream outlives restarts and recreation: a container's lines continue after it starts again (resumed from its cursor, so nothing repeats), a replacement container with a new id is attached from `tail`, and `end` is sent only when the stack — or the service named in `service` — has no container left. Without `follow`, `end` follows the last line as before." The `/api/containers/{id}/logs` paragraph: "… with `follow` it re-attaches after a restart and ends when the container is removed."
- The `handleLogs` doc comment is updated to the same effect.

**Fake additions (`observe_test.go`):** `fakeObserve.endLogs(id string)` closes a per-call `stop` channel so a following stream for `id` ends on its own (the fake's follow wait becomes `select { case <-ctx.Done(): case <-stop: }`); `logCallsFor(id string) int`; `setLogs(id string, spec fakeLogSpec)`; `setContainers([]engine.Container)`; `pushChange(ch state.Change)` (writes to the buffered `changes` channel); `changeSubscriptions() int` (counts `SubscribeChanges` calls; a test can close `changes` to simulate a drop and the fake hands out a fresh channel on the next call).

- [ ] **Step 1: Write the failing tests** in `observe_test.go` (helpers `twoContainerFake`, `newObserveEnv`, `openSSE`, `nextEvent`, `cursorID`, `t0` exist): `TestLogsFollowReattachesAfterRestart` (`/api/stacks/web/logs?follow=1&tail=200`; read the three lines; `setLogs("c-app", lines t0 "app one", t0 "app two", t0+1s "app three")`; `endLogs("c-app")`; `pushChange(state.Change{Project: "web"})` → the next `log` event is `app three` with id `cursorID("c-app", t0.Add(time.Second), 1)`, no `end`, `logCallsFor("c-app") == 2`, and the second call's options are `Tail == -1`, `Since.Equal(t0)`, `Follow == true`); `TestLogsFollowAttachesARecreatedContainer` (`endLogs("c-app")`; `setContainers` with `c-app` replaced by `container("c-app2", "web-app-1", "web", "app")` and `setLogs("c-app2", one line t0+2s "app fresh")`; `pushChange` → one `log` event `app fresh` from container `web-app-1` with the `c-app2` cursor prefix; `logOpts["c-app2"].Tail == 200`); `TestLogsFollowServiceFilterAppliesToReattach` (`?service=app`; a new `db` replica after a change is never read: `logCallsFor` for it stays 0); `TestLogsFollowEndsWhenTheStackHasNoContainersLeft` (`endLogs` both; `setContainers` with only `c-other`; `pushChange` → `end` and the response body closes); `TestLogsFollowPumpEndWithoutAChangeIsNotRetriedForever` (`endLogs("c-app")` while the listing still says `running` and the second call's spec has no lines and ends at once → within 300 ms `logCallsFor("c-app") == 2` exactly, no `end`; then `pushChange` → `3`); `TestLogsFollowDroppedChangeSubscriptionResubscribes` (close the fake's `changes` channel → `changeSubscriptions() == 2` and a reconcile happens: `Containers` called again); `TestContainerLogsFollowReattachesSameID` (`/api/containers/c-app/logs?follow=1`; `endLogs`, `pushChange` → second call with the resume options); `TestContainerLogsFollowEndsWhenRemoved` (`setContainers` without `c-app`, `pushChange` → `end`). Existing cases must stay green: `TestLogsNoContainersEndsImmediately`, `TestLogsEmptyProjectEndsWithoutStreams`, `TestLogsFollowDisconnectCancelsStreams`, `TestLogsStreamErrorIsReportedThenEnds` (if it follows, it keeps its `error` assertion and the stream stays open until the container is unlisted — adjust that case's tail accordingly and say so in the report), every cursor / resume case and `TestLogsCapsContainersAtFifty`.
- [ ] **Step 2: Run** `go test ./internal/api/ -run 'TestLogsFollow|TestContainerLogsFollow'` — FAIL: `end` after the first stream ends; one `Logs` call per container.
- [ ] **Step 3: Implement** the pump map, the change subscription, the reconcile and the `end` rule; update `docs/api.md` and the doc comment.
- [ ] **Step 4: Run** `go test -race -count=2 ./internal/api/` (the new tests involve goroutines and timing; `-count=2` catches flakiness), `go vet ./...`, `gofmt -l .` empty, `make test`.
- [ ] **Step 5: Commit**
  ```bash
  git add internal/api/observe.go internal/api/observe_test.go docs/api.md
  git commit -m "fix(api): keep follow-mode log streams attached across container restarts and recreation"
  ```
  (why: the log console and the containers peek both went silent after a restart because the server only ever read the containers present when the request started; reconciling on the cache's `change` events re-attaches with the existing cursor resume, so nothing repeats. Server behaviour only; routes, events, fields and ids unchanged; the follow-mode `end` wording in `docs/api.md` is the one documented sentence that changes.)

---

### Task 9: Docs and the final gate

Spec §2 goal 5, §9 row 23.

**Files:**
- Modify: `docs/development.md`, `Makefile` (comments only, if any still name `ui-app` or `svelte-check`)

**Edits (`docs/development.md`):**
- Make targets table: `make ui` → "build the React UI into `web/dist` and enforce the bundle budgets (…)"; `make check` → "`go vet`, gofmt, UI type-check (`tsc --noEmit`) and unit tests, Go tests".
- "Dev loop": replace the `dev:app` / `build:app` / `check:app` / `dist-app` paragraph with: `npm run dev` serves the React UI at `/` with the `/api` proxy; `npm run check` is `tsc --noEmit` over `web/src`; tests are `*.test.ts` (pure modules) and `*.test.tsx` (components) and run under `make ui-test`, which builds first so `web/src/bundle.test.ts` measures the shipped `web/dist`.
- Replace `vite.app.config.ts` with `vite.config.ts` in the code-splitting paragraph; delete "(the future replacement)", "Plan 23 switches the entry", "unify with the rail in Plan 23" (→ "as the previous UI did"), and the "which Svelte's `bind:value` never did" clause (→ "which the previous UI never did"); the sentence "With this plan every screen of the spec is React" becomes "Every screen is React; the Svelte app was removed in the cutover."
- Add one paragraph after the log-console paragraph: a follow-mode stream re-attaches after a restart or recreation and ends only when the stack (or service) has no container left; the state cache publishes a `change` for every lifecycle event, including a restart that leaves the listing identical, so restart counts refresh (link to `docs/api.md` Observability).
- Fonts / theme / tests sections unchanged. README: `git grep -n -i svelte README.md` is empty today — confirm and leave it.

- [ ] **Step 1: Edit** the docs as above.
- [ ] **Step 2: Final gate.** From the repo root: `make build` (record the `bundle:` line — entry ≤ 106 kB gz, total ≤ 400 kB, fonts ≤ 200 kB — and `bin/stackstead` present), `make check` (exit 0; record Go and UI totals), `make e2e`; `SVELTE_REFS` (Task 2 Step 1) → empty; `git grep -n -i svelte -- docs README.md Makefile ':!docs/superpowers'` → empty; `git grep -n "dist-app\|build:app\|check:app\|dev:app\|app\.html\|vite\.app\.config\|svelte-check" -- . ':!docs/superpowers' ':!.superpowers' ':!web/package-lock.json'` → empty; `ls web/dist-app` → absent; `git status` clean apart from the untracked `.idea/` and the handoff folder.
- [ ] **Step 3: Commit**
  ```bash
  git add docs/development.md Makefile
  git commit -m "docs: describe the React-only web build and the follow-mode log behaviour"
  ```
  (why: `docs/development.md` is the contributor entry point and still describes the coexistence setup; the gate run is the evidence the final reviewer starts from.)

---

## Self-review notes

- **Spec coverage.** §2 goal 5: T2 removes Svelte and its dependencies; T1 / T9 prove `make build` and `make check` with the budgets. §3: `web/embed.go` and `internal/api/spa.go` are not touched; their tests are framework-neutral (verified: `server_test.go` uses `fstest.MapFS` with `index.html`, `assets/app.js`, `favicon.svg`; `e2e_test.go` checks `#app`, `/assets/*`, SPA fallback, woff2). §4: `bundle.test.ts` keeps CodeMirror / uPlot lazy and `guards.test.ts` keeps the handoff-folder guard; reduced motion untouched. §5: budgets unchanged, entry recorded per task, nothing new eager except the ~0.3 kB palette fallback (divergence 7). §6.2: `index.html` → React entry (T1), Svelte files + `svelte.config.js` + second config + `app.html` + dependencies deleted (T1 / T2), `npm run check` → `tsc --noEmit` (T2). §6.4: Svelte tests deleted with their components; pure-module tests kept (the deletion list contains only Svelte-mounting tests, identified by their `@testing-library/svelte` / `.svelte` imports). §7 / §8: T7 and T8 keep the endpoint and event contracts. §9 row 23: entry switched, Svelte deleted, tests ported (nothing left to port: every screen's tests were ported in Plans 17–22; T2 deletes the originals), build / check green, docs updated.
- **User hard rules.** No code bodies (signatures, shapes, option values, test names with exact values and commands only); API contracts unchanged, backend tasks isolated with Go tests first and flagged "server behaviour change, no API change" (T7, T8), the one documented-sentence change (follow-mode `end`) flagged "needs user approval" with the task droppable; deletions in their own commit with a before / after verification (T2 Steps 1, 2, 4); budgets ≤ 106 kB entry, bundle checks in `make check` through `ui-test: ui`; embed / serve contract proved by `embed_test.go`, `server_test.go` and `make e2e`; nine tasks; no manual browser steps (the live passes belong to the final Opus review).
- **Type consistency.** `PaletteModule = { default: ComponentType<CommandPaletteProps> }` is today's type; `initialQuery?: string` is additive; `scrollToSection(id: string): boolean` is a pure extraction of `goToSection`; `streamContainerLogs` gains one `match func(engine.Container) bool` parameter and both callers pass one; `resync` gains the forced projects and both call sites pass none; the fake methods added in T8 extend `fakeObserve` without changing `ObserveAPI`.
- **Review focus mapping.** 1 → T1 (`embed_test.go`, e2e, `html.test.ts`); 2 → T2 Steps 1 / 4; 3 → `bundle.test.ts` on `dist` (T1) and the entry lines of T4 / T6; 4 → T3's identity test; 5 → T7 / T8 Go tests.
- **Proportion and known limits.** T1 / T2 are mechanical but gated hard; T3–T6 are small, each pinned by one or two behaviour tests; T7 is a ten-line cache change whose test is the diagnosis (if `TestLifecycleEventPublishesChangeWhenTheSnapshotIsUnchanged` passes before the change, the hypothesis is wrong — stop and report rather than guess); T8 is the one sizeable backend change and is `-race -count=2` gated. Deferred items and their reasons are in the table above. The repo's "remove shipped specs and plans" convention is left for the user (divergence 8).
