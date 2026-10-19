# Stackstead web UI: React migration and "Evolve v2" redesign

Date: 2026-10-19 · Status: approved for planning

Source material: `docs/superpowers/design_handoff_stackstead_react/` (README, `CLAUDE_CODE_PROMPT.md`, `Stackstead Evolve v2.dc.html`). The README is the visual and interaction reference. This document records the intent, the decisions taken on top of it, and how the work is sliced into PRs. Where the two disagree, this document wins for *how* and the README wins for *what it looks like*.

## 1. Intent

Stackstead is a self-hosted control panel for one Docker host. The web UI moves from Svelte 5 to React 19 and ships the "Evolve v2" design in the same effort. The redesign is health-first: the first thing you see is what is broken, since when, and what to do about it. It adds a stronger visual identity: LED status squares, stack towers, the chassis sidebar, and mono panel labels.

The user's own words on the new health timeline: it must show **when a service started failing and how often it restarted**.

## 2. Goals

1. The React app reaches feature parity with today's Svelte UI: routes, query params, guards, focus, titles, banners, accessibility, reduced motion, lazy editors and charts.
2. The Evolve v2 design is implemented pixel-accurately on all seven screens, plus the ⌘K palette and the mobile layout (< 760px).
3. New UX: health-first overview, split-pane stack detail with a 60-minute health timeline and a resizable live log console, containers with streaming inline log peek and j/k/↵/r/Esc keys, a live port-conflict check and compose preview in New stack, and a storage usage bar that drives the tables.
4. A minimal, additive backend endpoint for per-service state and health history over the last 60 minutes.
5. Svelte and its dependencies are removed. `make build`, `make check` and the Go tests pass, including the bundle and font budgets.

## 3. Non-goals

- No change to any existing API response shape or route. The only API change is the additive history endpoint (§7). Any other contract change means stopping to ask.
- No changes to the Go embed/serve contract. Output still goes to `web/dist`, embedded by `web/embed.go` and served as an SPA by `internal/api/spa.go`.
- No new screens beyond the design. `ContainerDetail`, `StackImport`, `StorageImage` and `StorageVolume` keep today's behaviour, restyled with the new tokens and identity elements.
- No server-side rendering, no state-management library, and no CSS-in-JS runtime.

## 4. Anti-goals (failure even if it "works")

- Behaviour regressions hidden by the rewrite: a lost unsaved-changes guard, a missing `?next=` redirect, focus that no longer moves to the h1, an SSE resume that drops or duplicates log lines.
- The entry bundle over budget, or CodeMirror or uPlot loaded eagerly.
- The prototype's HTML or `support.js` shipped, or any app file pointing into `docs/superpowers/design_handoff_stackstead_react/`.
- Animation that ignores `prefers-reduced-motion`.

## 5. Constraints

- React 19 (the prompt's "18+" is satisfied), TypeScript strict, Vite 8, React Router 8 in declarative `BrowserRouter` mode. Versions are pinned exactly, as the repo already does.
- New dependencies: `react`, `react-dom`, `react-router`. Dev dependencies: `@vitejs/plugin-react`, `@testing-library/react`, `@types/react`, `@types/react-dom`. Nothing else without a stated reason.
- Budgets in `web/scripts/check-bundle.mjs` stay unchanged: entry ≤ 120 KiB gz, total ≤ 400 KiB gz, fonts ≤ 200 KiB. Today's Svelte entry is 95.4 KiB gz, and React + React Router add roughly 60 KiB. **Route-level code splitting is therefore required.** Only the shell, the stores and the Stacks overview are eager. Every other route is `React.lazy`, and CodeMirror and uPlot stay lazy, as they are today.
- Fonts: self-hosted woff2 (IBM Plex Sans 400/500/600, Geist Mono 400/500), already in `web/src/assets/fonts/`. No Google Fonts.
- Icons: `web/src/lib/shell/icons.ts`, unchanged.
- Assets the design needs are copied into `web/public` or `web/src/assets` and are never referenced from the handoff folder. The handoff folder's logo and fonts are byte-identical copies of what the repo already has, so nothing new is needed today.
- The repo's process applies: test-first, conventional commits, one PR per plan, and plans with no code bodies.

## 6. Architecture decisions

### 6.1 Keep the controllers and replace only `svelte/store`

Every Svelte dependency in the TypeScript under `web/src/lib` is `svelte/store` (`writable`, `readable`, `derived`, `get`, `Readable`, `Writable`), across 21 files. This includes the "framework-agnostic" modules the prompt lists (`api/session`, `logs/buffer`, `containers/list`, `networks/networks`) and the controllers (`stores/live`, `stores/stacks`, `stores/refreshList`, `stores/toasts`, `stores/appearance`, `stores/guard`, `stores/media`, `stacks/{logs,metrics,liveMetrics,operation,compose,serviceEdit,serviceAdd}`, `storage/controller`, `dashboard/controller`, `router`).

**Decision:** add `web/src/lib/store.ts`, a dependency-free module with the same contract as `svelte/store`: `subscribe` calls the subscriber synchronously with the current value and returns an unsubscribe function; `writable` takes a start/stop notifier; `derived` handles single and multiple sources; `get` reads once. Every `lib` import then switches from `svelte/store` to `$lib/store`.

Svelte components accept any object that follows the store contract, so this switch lands **first, in the Svelte app, with no behaviour change**, and the existing tests prove it. React reads these stores through one hook, `useStore(readable)`, built on `useSyncExternalStore`. Live SSE, debounced refetch, running-ops polling with backoff, the unsaved-changes guard, toasts and appearance then behave the same because they *are* the same code. The prompt's "replace Svelte stores with React equivalents that behave the same" is met by keeping the store logic and replacing only the binding layer.

`router.ts` is the exception. Routing moves to React Router, and its pure helpers (`matchRoute`, link-click rules, `?next=` sanitising) are kept wherever React Router does not already cover them.

### 6.2 React and Svelte coexist until the cutover

- React code lives in `web/src/app/`: `main.tsx`, `App.tsx`, `shell/`, `routes/`, `components/`, `hooks/` and a `styles/` folder.
- `web/app.html` is a second HTML entry. `vite.app.config.ts` serves it in dev, with history fallback to `app.html` and the same `/api` proxy, and builds it into `web/dist-app/`, which is git-ignored and not embedded. `npm run dev:app` and `npm run build:app` run it. `npm test` runs `.test.ts` and `.test.tsx` across `src/` for both frameworks.
- `web/dist`, `web/embed.go` and `make build` keep shipping Svelte until the cutover PR. That PR makes `index.html` load the React entry, deletes the Svelte files, `svelte.config.js`, the second config and HTML entry, and the Svelte dependencies, and switches `npm run check` from `svelte-check` to `tsc --noEmit`.

### 6.3 Styling

CSS Modules: zero runtime and no new dependency. Tokens are CSS variables in `web/src/app/styles/tokens.css`, using today's names from `app.css` plus `--chassis`, `--hi` and the 0.16-alpha softs in dark, with light and dark blocks driven by the existing `data-theme` attribute that `public/theme.js` sets before first paint. `tokens.test.ts`'s contrast and coverage checks are ported to the new file. A global `@media (prefers-reduced-motion: reduce)` rule zeroes animation and transition durations, and components never animate through JS timers.

### 6.4 Testing

Vitest + jsdom + `@testing-library/react`. The existing `*.test.ts` files are the behavioural spec. Each screen PR ports the tests for the components it replaces into `.test.tsx` files next to the new components, written before the component (red first). Pure-module tests (`lib/**/*.test.ts` without component mounting) stay as they are. The Svelte tests are deleted with their components in the cutover PR. New UX (palette, timeline, log console, peek, port check, usage bar) gets new tests that cover behaviour, not markup.

## 7. Backend: health history endpoint (additive)

**Why:** `metric_samples` has no state or health. Docker `start`/`die`/`restart`/`health_status` events reach `internal/state` but are only forwarded live and never stored. Neither "when did it start failing" nor "how often did it restart" can be derived from existing data.

**Storage:** a migration adds `container_transitions (ts INTEGER, project TEXT, service TEXT, container TEXT, state TEXT, exit_code INTEGER NULL)` with an index on `(project, ts)`. `state` is one of `running`, `healthy`, `unhealthy`, `starting`, `restarting`, `exited`, `removed`. A recorder subscribes to the state cache's container change stream and writes a row when a container's derived state changes. Repeated identical states are not written, and periodic `health_status` noise is already filtered by the cache. On startup and on reconnect, it writes one baseline row per container so that windows have a starting state. Rows are pruned on the metrics retention via the sampler's existing `PruneStep` hook.

**Endpoint:** `GET /api/stacks/{name}/health?span=60m` (session required). `span` accepts 1m–24h and defaults to 60m. Response:

```
{ "from": 1760860000, "to": 1760863600,
  "services": [
    { "service": "home-assistant", "restarts": 3,
      "runs": [ { "state": "nodata",    "from": 1760860000, "to": 1760860420 },
                { "state": "healthy",   "from": 1760860420, "to": 1760862900 },
                { "state": "unhealthy", "from": 1760862900, "to": 1760863600 } ] } ] }
```

Runs are contiguous and cover `[from, to]` exactly. `nodata` covers time before the first known row. `restarts` counts `restart` events and `die` events followed by a `start` within the window. A stack with several replicas of a service reports the worst state per moment (`exited` > `unhealthy` > `restarting` > `starting` > `healthy`/`running`). An unknown stack returns 404, as the other stack routes do. The client refetches on the stack's SSE change event (debounced, as today) and extends the last run to "now" locally. Documented in `docs/api.md` under Observability.

**Limitation (accepted):** history starts when the new version first runs. Earlier time renders as a faint `nodata` run.

**Refinements from planning (author's calls, see Plan 15 "Spec divergences"):**
- Restarts are stored as marker rows (`state = 'restart'`) in the same table. A policy restart's `die`/`start` falls inside the cache's 250 ms debounce, so it cannot be derived from state rows.
- One restart = one `die → start` pair for the same container. The `restart` action itself is ignored, because Docker emits `kill, die, start, restart` for `docker restart` and counting both would double-count.
- Docker states the list above does not name: `created` → `starting`, `paused` → `running`, `dead`/`removing` → `exited`.
- A service with no recorded rows is omitted from `services`. The client already knows the stack's services and renders missing ones as `nodata`.

## 8. Screens and global UI

The README's "Screens", "Global", "Identity elements", "Design tokens" and "State" sections are adopted verbatim as requirements. Clarifications:

- **Timeline mapping:** `healthy`/`running` → success, `starting`/`restarting` → warning (legend "restart"), `unhealthy` → danger, `exited`/`removed` → muted "down", `nodata` → border-strong outline. Tooltip: `"<state> · <n> min"`. The last run pulses if it is danger.
- **Log console:** reuses `stacks/logs.ts` + `logs/buffer.ts` (SSE resume semantics unchanged). Level detection uses the `ERROR`/`WARN` tokens the design shows. Height is persisted under `stackstead.logsHeight`, clamped to 120–560.
- **Inline peek** (Containers): streams through `GET /api/containers/{id}/logs` with the same buffer, limited to the last 8 lines. One open at a time. Closing it releases the stream.
- **Port-conflict check** (New stack): client-side, against the live containers store's published ports. It is advisory only. The server's validation stays authoritative and its errors are mapped exactly as `StackCreate.svelte` does today.
- **Palette** results come from the live stacks and containers stores. Actions respect `lifecycleDisabledReason`, and disabled actions show their reason instead of running.
- **Issue cards** come from `/api/alerts` plus storage reclaimable, as the README says.

## 9. Delivery: plans and PRs

One plan and one PR per row. The branches stack in order, each opened against `main` once its predecessor merges, or against the predecessor's branch until then.

| # | Plan | Content |
|---|---|---|
| 15 | `stackstead-15-health-history` | §7 backend: migration, recorder, prune step, endpoint, `apitypes`, client contract test, `docs/api.md`. Independent of the web work. |
| 16 | `stackstead-16-react-foundation` | `$lib/store` + import switch (Svelte still ships); React scaffold (§6.2), tokens, fonts, `useStore`, routing with guards/focus/titles, shell (sidebar, top bar, mobile bars, banners), toasts, theme/density, ⌘K palette, primitives (LED, Button, Dialog, Tabs, chips, Sparkline, panel label). |
| 17 | `stackstead-17-react-overview` | Stacks overview. |
| 18 | `stackstead-18-react-stack-detail` | Stack detail: timeline (uses 15), split pane, log console, metrics/config/compose/operations, ComposeEditor/ServiceEditor/AddServicePanel ports. |
| 19 | `stackstead-19-react-containers` | Containers list with peek and keys, ContainerDetail restyle. |
| 20 | `stackstead-20-react-new-stack` | New stack with port check and preview, FileBrowser, Import. |
| 21 | `stackstead-21-react-storage` | Storage with usage bar, CleanupDialog, image and volume details. |
| 22 | `stackstead-22-react-networks-settings-auth` | Networks, Settings, Login, Setup. |
| 23 | `stackstead-23-react-cutover` | Switch the entry, delete Svelte, final test port, `make build`/`make check` green, docs (`docs/development.md`). |

Each plan is written when its PR starts, so it reflects the merged state of the previous one. Plans contain tasks, files, signatures, data shapes, examples and test cases, and no code bodies.

## 10. Calls made by the author (reviewable)

- React 19 instead of 18, because it is current and within "18+".
- CSS Modules over vanilla-extract: no build plugin, no dependency.
- `useSyncExternalStore` over a store library or Context, because the store logic already exists (§6.1).
- React Router 8 in declarative mode, because only history routing and params are needed and loaders/actions are not.
- The health history is persisted in SQLite rather than an in-memory ring, so it survives a Stackstead restart.
- Coexistence through a second HTML entry rather than a feature flag in the Go server, so the Go side stays untouched.
