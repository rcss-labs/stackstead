# Stackstead UI v2 — Design

**Date:** 2026-10-15
**Status:** Draft, awaiting review. Restyles the Plan 5 web UI (`web/src`) to the mock-up in `docs/Stackstead DevOps UI redesign/Stackstead v2.dc.html` and adds the dashboard data it needs. Implemented by Plan 8a (restyle, shell, components, detail page, list table) and Plan 8b (dashboard data). Builds on `2026-10-14-stackstead-v1-design.md`.

## 1. Purpose

Make the UI look and feel like an operator's console: dense, monospaced where values live, one icon rail instead of a text nav, a host summary above the stack list, and a stack detail page that shows containers, current resources and recent operations without changing tabs. Nothing the current UI does is removed; §8 lists every feature per route and where it lives now.

Success criteria:

- Every existing route, API call, keyboard path, ARIA role and Vitest/Go test behaviour still works; tests change only where they assert markup that the restyle replaces (CSS classes, column order), never where they assert behaviour.
- Dark and light themes both pass WCAG AA (4.5:1 text, 3:1 UI components) for every token pair the UI uses; §4.3 lists the pairs and the fixes.
- Theme and density apply before first paint under the existing strict CSP (no inline scripts).
- Entry JS budget stays 120 kB gz, total 400 kB gz; fonts get their own 200 kB budget.
- `/api/host`, `/api/alerts`, `/api/metrics/summary` and the `event` stream message cost nothing while nobody is looking and are cached server-side so ten open tabs are no worse than one.

## 2. Scope

### In
- Tokens, typography, self-hosted fonts, theme (dark/light/system) and density (compact/comfortable) with persistence and no first-paint flash.
- Shell: 52 px icon rail, 44 px top bar with breadcrumb and status pills.
- Restyled routes: Login, Setup, Stacks list, Stack detail (five tabs plus a 300 px aside), New stack, Import stack, Settings, 404.
- Restyled components: Button, StatusBadge, Banner, Tabs, Field, Toasts, Dialog, ConfirmDialog, EmptyState, Spinner, CopyButton, ListEditor, FileBrowser, YamlEditor/DiffView themes, MetricsChart card, OperationPanel, LogsViewer.
- 8a API additions: container health and stack health counts, declared host ports on the stack summary, `dockerVersion` and metrics settings on `/api/status`.
- 8b API additions: `GET /api/host`, `GET /api/alerts`, `GET /api/metrics/summary`, and an `event` message on `/api/events`.

### Out
- New stack features (import preview endpoint, compose override support, notifications, stored alert history, multi-host). The mock-up's import "Preview" box is rendered only for the error it already has (§6.6).
- A global operations page; the "N running" pill stays a status, not a link.
- Playwright. End-to-end stays the headless-Chrome smoke in `cmd/stackstead/e2e_test.go`, extended (§12).
- Changing the CSP beyond what §5 needs (nothing).

## 3. Key decisions

The TypeSafe skill is an SDK for an external judgment API and cannot run here without network access and credentials, so these decisions are argued in writing (as Plan 5 did).

| Decision | Choice | Reason |
|---|---|---|
| First-paint theme without inline script | External classic script `web/public/theme.js`, loaded as the first `<script src="/theme.js">` in `<head>` before the stylesheet link | `script-src 'self'` already allows it; no CSP change and no hash to keep in sync with Vite's emitted `index.html` (a hash would have to be recomputed on every build and injected into the Go constant). The file is ~300 bytes, same-origin, served `no-cache` by `spaHandler` (it is a regular public file). The render-blocking cost is one local round trip on a page that already waits for `index.html` with `no-cache`. A CSP hash would save that round trip but couples the Go server to the UI build output, which nothing else does today. |
| Theme default | Follow `prefers-color-scheme` until the user picks Dark or Light; "System" returns to following | Matches the current UI's behaviour (README promises it) and the user decision. CSS alone handles the system case, so a cleared or blocked `localStorage` degrades to the OS theme, never to a flash. |
| Persistence | `localStorage` keys `stackstead.theme` (`dark`\|`light`, absent = system) and `stackstead.density` (`comfortable`, absent = compact), every access in `try/catch` | Per browser, as decided. No server round trip, works before login. |
| Fonts | Self-hosted woff2, latin subset: IBM Plex Sans 400/500/600, Geist Mono 400/500; `@font-face` with `font-display: swap`; `<link rel="preload">` for Plex 400/500 and Geist Mono 400 | Both are OFL-1.1. Google Fonts would violate `default-src 'self'` and leak page loads to a third party. Vite hashes the files into `/assets/` (immutable caching). The three preloads cover the first paint; 600 and Mono 500 load on first use. |
| Font budget | New `FONT_BUDGET = 200 kB` in `scripts/check-bundle.mjs`, summing `dist/assets/*.woff2`; JS budgets unchanged | Fonts are not JS and must not eat the entry budget, but they are still bytes the Pi serves. Latin subsets of the five files are ~150 kB. |
| Container health | Parsed from the Docker `Status` string (`(healthy)`, `(unhealthy)`, `(health: starting)`), exactly as `internal/state` already does; moved to `engine.Container.Health()` | Zero extra Docker calls; the list endpoint stays one cached snapshot. `/containers/{id}/json` per container would be N inspects per list. |
| Declared ports on the summary | From the compose file (`stacks.ParsePorts` on each service's `ports`), not from running containers | The file is the truth (v1 spec §3); stopped stacks still show their ports; no engine dependency. |
| Host metrics source | Linux: `/proc` and `syscall.Statfs`, stdlib only. Elsewhere: Docker `/info` plus summed container samples; tiles without a source are hidden, never faked | User decision. Hiding beats guessing: a macOS workstation sees a Docker-VM view and the UI says so (`source: "docker"` → sub-line "containers only"). |
| `/api/host` work model | Request-triggered, single-flight, 5 s cache; `system/df` and the 24 h events scan are 60 s sub-caches refreshed asynchronously when stale | No goroutine burns CPU while nobody looks; ten tabs polling every 10 s cost one `/proc` read per 5 s. `docker system df` can take seconds on big hosts, so it never sits on a request path. |
| Alerts | Derived on request from state the server already holds (containers, compose configs, registry, status flags, restart counts); 5 s cache; `since` = first time this server process observed the alert | User decision: not stored, no notifications. Keyed in memory so `since` is stable across refreshes and cleared alerts disappear. |
| Per-stack sparklines | One `GET /api/metrics/summary?window=15m` for all stacks, 30 buckets, from the SQLite samples; top consumers from `Sampler.Latest()` | One query instead of one per stack; the list page never opens a metrics SSE. |
| Docker events feed | `event` messages on the existing `/api/events` stream, fanned out by `state.Cache` | One SSE per tab stays the rule (user decision); the cache already consumes the Docker event stream. |
| Detail "Resources · now" | The detail page owns one `/api/stacks/{name}/metrics/live` subscription shared by the aside and the Metrics tab | Existing data (ships in 8a); one stream per detail page, opened only while the aside is visible or the Metrics tab is active. |
| Changed-line highlight in the Compose tab | Line decorations computed with `diff()` from `@codemirror/merge` (already a dependency) between the loaded content and the draft, debounced 150 ms | No new dependency; the diff is line-based so it is cheap on compose files. |
| Button `danger` variant | Outlined (danger text, `--danger-soft` fill on hover and inside dialogs), never a solid red | Solid red with white text fails 4.5:1 in the dark palette (`#f87171`); the mock-up uses outlined Remove/Disable. |
| Breadcrumb for Settings | "Settings" alone, not "Stacks / settings" | Settings is not under Stacks; the other routes are. |
| Logo assets | `web/public/logo-art.png` (336 px wide, ≤ 40 kB) and `web/public/containers.png` (68 px, ≤ 8 kB), downscaled from `assets/`; `favicon.svg` recoloured to `#34d399` | The originals are 550–750 kB each; a 2× raster at display size is enough. `img-src 'self'` already covers them. |
| 8a list before 8b | The stack table ships without the CPU, Memory and Net RX/TX columns; host tiles, alerts, top consumers and the events feed are not rendered at all until 8b | A column of dashes conveys nothing and a hidden section costs nothing; adding columns is layout-only. §13 lists exactly what 8a shows. |

## 4. Design tokens and typography

### 4.1 Tokens (`web/src/app.css`)

All colours live on `:root`. Light is the default set; the dark set applies under `@media (prefers-color-scheme: dark) { :root:not([data-theme="light"]) }` and again under `:root[data-theme="dark"]`. `color-scheme` is set to `light` / `dark` alongside so native controls follow.

| Token | Dark | Light | Notes |
|---|---|---|---|
| `--bg` | `#0f1216` | `#f3f5f7` | page, inputs, log panes |
| `--surface` | `#161a20` | `#ffffff` | cards, rail, top bar |
| `--raised` | `#1c2129` | `#f7f9fa` | chips, selects, tab badges |
| `--hover` | `#1f2530` | `#eef1f4` | row/button hover (dark value changed from the mock's `#222934`, §4.3) |
| `--border` | `#2a313b` | `#dfe4ea` | dividers, card borders |
| `--border-strong` | `#3a4350` | `#c9d1da` | secondary buttons, segmented controls |
| `--border-input` | `#5b6573` | `#78828f` | text inputs, selects, textareas (new, §4.3) |
| `--text` | `#e6eaf0` | `#161a20` | |
| `--muted` | `#8b95a5` | `#5f6b7a` | labels, secondary text |
| `--faint` | `#848d9c` | `#626c7a` | timestamps, paths, hints, placeholders (changed from `#5c6675` / `#8b95a5`, §4.3) |
| `--accent` | `#34d399` | `#047857` | links, primary buttons, active rail/tab/chip (light changed from `#059669`, §4.3) |
| `--accent-ink` | `#062a1c` | `#ffffff` | text on `--accent` |
| `--accent-soft` | `rgba(52,211,153,.14)` | `rgba(4,120,87,.12)` | |
| `--success` | `#34d399` | `#047857` | |
| `--warning` | `#fbbf24` | `#9c4707` | light changed from `#b45309`, §4.3 |
| `--danger` | `#f87171` | `#b91c1c` | |
| `--info` | `#60a5fa` | `#1d4ed8` | |
| `--success-soft` | `rgba(52,211,153,.14)` | `rgba(4,120,87,.12)` | |
| `--warning-soft` | `rgba(251,191,36,.14)` | `rgba(156,71,7,.12)` | |
| `--danger-soft` | `rgba(248,113,113,.14)` | `rgba(185,28,28,.12)` | |
| `--info-soft` | `rgba(96,165,250,.14)` | `rgba(29,78,216,.12)` | |
| `--muted-soft` | `rgba(139,149,165,.14)` | `rgba(95,107,122,.12)` | |
| `--shadow` | `0 1px 0 rgba(0,0,0,.4)` | `0 1px 2px rgba(16,24,40,.06)` | cards |
| `--shadow-pop` | `0 8px 24px rgba(0,0,0,.25)` | same | toasts, dialogs |
| `--series-1..8` | `#3b82f6 #f97316 #10b981 #eab308 #ec4899 #8b5cf6 #14b8a6 #f43f5e` | `#2563eb #c2410c #047857 #a16207 #be185d #6d28d9 #0f766e #be123c` | chart lines; moved out of `MetricsChart.svelte` (`COLOR_SLOTS` stays 8); every value ≥ 4.1:1 on its surface |

Non-colour tokens: `--sans: 'IBM Plex Sans', system-ui, sans-serif`; `--mono: 'Geist Mono', ui-monospace, Menlo, Consolas, monospace`; `--pad: 6px` and `--row: 36px` (compact), `10px` / `44px` under `:root[data-density="comfortable"]`; `--radius-sm: 4px`, `--radius: 6px`, `--radius-md: 8px`, `--radius-lg: 10px`, `--radius-pill: 999px`; `--space-1..7` unchanged (0.25/0.5/0.75/1/1.5/2/3 rem). Old names (`--surface-raised`, `--accent-contrast`, `--font-sans`, `--font-mono`) are removed and every usage updated; `MetricsChart` and `cm.ts` read the new names.

### 4.2 Typography and sizes

- `body`: `13px/1.45 var(--sans)`, `background: var(--bg)`, `color: var(--text)`. `-webkit-text-size-adjust: 100%` stays.
- Headings: page `h1` 18 px/600 (mono for stack names); card `h2` 14 px/600 (mono for service names) or 13 px/600 for settings cards; section `h3` 11 px/600 uppercase, letter-spacing .05em, `--muted`, with a 9 px 12 px padding and a bottom border.
- Table header: 11 px/500 uppercase, letter-spacing .04em, `--muted`. Table body 13 px; mono cells 12 px; chips 10.5–11.5 px mono.
- Mono (`--mono`) is used for: stack, service, container and project names; paths; ports; image references; volumes; restart/limit values; numbers in tiles, cells, badges' counts; log and operation output; YAML; timestamps in feeds; the Prometheus snippet.
- Controls: inputs 32 px high (34 px on Login/Setup), padding `0 10px`, radius `--radius`; buttons 30 px (`sm` 26 px, `xs` 24 px) with 12 px / 10 px / 8 px horizontal padding and 13 / 12 / 12 px text; icon buttons 26×26 radius 5; pills 24 px high, `--radius-pill`; badges 20 px high.
- Cards: `--surface`, 1 px `--border`, `--radius-md`, `--shadow`. Sections inside a card are separated by 1 px `--border`.
- Focus: `outline: 2px solid var(--accent); outline-offset: 1px` on `:focus-visible` (global, unchanged in spirit).

Fonts: `web/src/assets/fonts/IBMPlexSans-{Regular,Medium,SemiBold}-latin.woff2`, `GeistMono-{Regular,Medium}-latin.woff2`, plus `OFL-IBM-Plex-Sans.txt` and `OFL-Geist-Mono.txt` next to them. The two licence texts are also committed as `web/public/licenses/IBMPlexSans-OFL.txt` and `web/public/licenses/GeistMono-OFL.txt` so the binary serves them, and the README gets a "Third-party" line naming both. `spaHandler` registers `mime.AddExtensionType(".woff2", "font/woff2")` because Go's built-in table lacks it and `nosniff` is on.

### 4.3 Contrast (WCAG 2.1, relative luminance, mock-up values)

Checked: `text`, `muted`, `faint`, `accent`, `success`, `warning`, `danger`, `info` on `bg`, `surface`, `raised`, `hover`; `accent-ink` on `accent`; each tone on its `-soft` fill over `surface`; input borders against the input background. Passing pairs (≥ 4.5:1) are not listed. Failures and fixes:

| Pair (mock-up) | Ratio | Used for | Fix | After |
|---|---|---|---|---|
| dark `faint #5c6675` on surface / raised / hover | 3.00 / 2.78 / 2.52 | paths, timestamps, "+N", hints, placeholders (10.5–12 px text) | `--faint: #848d9c`; `--hover: #1f2530` (the mock's `#222934` leaves even that at 4.37) | bg 5.61, surface 5.21, raised 4.83, hover 4.59 |
| light `faint #8b95a5` on bg / surface / raised / hover | 2.77 / 3.03 / 2.87 / 2.67 | same | `--faint: #626c7a` | 4.87 / 5.32 / 5.04 / 4.70 |
| light `accent #059669` on surface / bg; white on it | 3.77 / 3.45 / 3.77 | links, active chips and tabs, primary button label | `--accent: #047857` (= light `--success`, mirroring dark where accent = success); soft `rgba(4,120,87,.12)` | 5.48 / 5.02; white on accent 5.48; accent on accent-soft 4.63 |
| light `warning #b45309` on warning-soft | 4.26 | uppercase 11 px badge text, alert kinds | `--warning: #9c4707`; soft `rgba(156,71,7,.12)` | 5.29 (on white 6.33) |
| dark input border `#2a313b` vs `bg`; light `#dfe4ea` vs `bg` | 1.28 / 1.20 | input boundary (non-text, needs 3:1) | new `--border-input` `#5b6573` / `#78828f` for inputs, selects, textareas only | 3.18 / 3.57 |

Everything else passes: dark muted on the new hover 5.08, dark faint on hover 4.59 (lowest text pair), dark danger on danger-soft 5.17, light muted on bg 4.96, light success on success-soft 4.63, light info on info-soft 5.56, accent-ink on accent 8.06 (dark). The services "cells" strip and the tile bars are decorative: the adjacent text carries the value. Sparkline strokes (`--accent`, `--info`, 1.2 px) sit next to the mono value and need no contrast of their own. The rail's active icon (accent on accent-soft) is a UI component: 6.92 dark, 4.63 light.

## 5. Theme, density and CSP

- `web/public/theme.js` (classic script, no module, no dependencies):

  ```js
  (function () {
    var h = document.documentElement, q = new URLSearchParams(location.search).get('theme');
    try {
      var t = localStorage.getItem('stackstead.theme');
      if (t === 'dark' || t === 'light') h.dataset.theme = t;
      if (localStorage.getItem('stackstead.density') === 'comfortable') h.dataset.density = 'comfortable';
    } catch (e) {}
    if (q === 'dark' || q === 'light') h.dataset.theme = q; // one-off, not persisted; used by the e2e render
  })();
  ```

  `index.html` loads it as the first element in `<head>` (`<script src="/theme.js"></script>`, Vite leaves non-module scripts alone), then the three font preloads, then the module entry. The `?theme=` override only sets the attribute for that page load; the store never persists it.
- `web/src/lib/stores/appearance.ts`: `theme` (`'dark' | 'light' | 'system'`), `effectiveTheme` (`'dark' | 'light'`, from `theme` or `matchMedia('(prefers-color-scheme: dark)')`, updated on the media query's `change`), `density` (`'compact' | 'comfortable'`). `setTheme`/`setDensity` write `document.documentElement.dataset.*` (removing the attribute for `system`/`compact`) and `localStorage` inside `try/catch`. On start the store reads the attributes `theme.js` already set, so store and DOM agree without a second paint; a `?theme=` override is therefore visible in Settings for that page load but is never written to storage.
- Rail toggle: sets `theme` to the opposite of `effectiveTheme` (explicit). Settings › Appearance: segmented Dark | Light | System and Compact | Comfortable.
- `MetricsChart` keeps its `MutationObserver` on `data-theme` plus the media query listener, so uPlot rebuilds with the new series colours. CodeMirror reads tokens through `var()` and needs nothing.
- CSP stays `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`: fonts fall under `default-src 'self'`, `theme.js` and the logos are same-origin. `docs/security.md` notes that `localStorage` holds only the theme and density choice.
- Reduced motion: the existing global `prefers-reduced-motion` rule stays and also disables the `.live` pulse (`ss-pulse`, 1.6 s ease-in-out) and the spinner rotation; the live dot then renders solid and the spinner shows a static ring with the accent arc, so state is still visible.

## 6. Shell and screens

### 6.1 Shell (`App.svelte`, `Rail.svelte` replaces `Nav.svelte`, `TopBar.svelte`)

Layout: `display:flex; min-height:100vh`. Login and Setup render without rail or top bar (centred card on `--bg`). Authenticated routes:

- **Rail** `<nav aria-label="Main">`: 52 px wide, `--surface`, 1 px right border, sticky `top:0; height:100vh`, column, `padding:10px 0; gap:4px`, items centred. Top: logo link (36×36, `containers.png` at 34 px, `aria-label="Stackstead"`, href `/`), 10 px margin below. Items, in order: Stacks (`/`, also current on `/stacks/:name`), New stack (`/stacks/new`), Import (`/stacks/import`), Settings (`/settings`). Each is `<a use:link>` 36×36, radius 7, 18 px stroke-1.8 icon from the mock-up's path data, `aria-label` = label (so `getByRole('link', { name: 'Settings' })` keeps working), `title` = label, `aria-current="page"` when current; current = `--accent` on `--accent-soft`, hover `--hover`. Bottom (`margin-top:auto`): theme toggle button 36×36 (`aria-label="Switch to light theme"` / `"Switch to dark theme"`, sun/moon paths from the mock-up), then the avatar button 28×28 round, `--raised`, 1 px `--border-strong`, 11 px/600 initials (first two characters of the username, upper-cased), `aria-label="Log out"`, `title="<username> · Log out"`, `onclick` = the existing `requestLogout` (unsaved-changes guard included). Keyboard: Tab order top to bottom; no roving tabindex (four links).
- **Top bar** `<header>`: 44 px, `--surface`, bottom border, sticky `top:0; z-index:5`, `padding:0 16px; gap:12px`. Left: breadcrumb (`<nav aria-label="Breadcrumb">`): `Stacks` link in `--muted` (hover `--text`), then `/` in `--faint` and the current crumb in 500 weight (mono for stack names, `white-space:nowrap; overflow:hidden; text-overflow:ellipsis`). Right: `N running` pill (`role="status"`, `--info-soft`/`--info`, live dot, only when `runningOps > 0`; text stays `${n} running`), Docker pill (`Docker <dockerVersion>` with a `--success` dot; `Docker unavailable` with a `--danger` dot when `$live.docker !== 'ok'`), compose pill (`compose <version>` or `compose unavailable`). Pills: 24 px, 1 px `--border`, `--muted`, 12 px.
- **Banners** (Docker unreachable, compose plugin missing, metrics degraded, reconnecting) render between the top bar and `<main>`, inside `main`'s horizontal padding, unchanged roles.
- **Main**: `flex:1; padding:20px 20px 40px; min-width:0`. The focus-after-navigation effect, `document.title` map, 404 `EmptyState` and the unsaved-changes `ConfirmDialog` are unchanged.

Routes → breadcrumb: `/` → `Stacks`; `/stacks/:name` → `Stacks / <name>`; `/stacks/new` → `Stacks / New stack`; `/stacks/import` → `Stacks / Import`; `/settings` → `Settings`; 404 → `Stacks / Not found`.

Responsive: ≤ 900 px the compose pill is hidden and the Docker pill shows only its dot with `title`/`aria-label` carrying the text; ≤ 720 px the breadcrumb keeps only the last crumb (the Stacks link is still in the rail). The rail never collapses: 52 px is affordable on phones and keeps the four destinations reachable.

### 6.2 Login and Setup

Card 360 px (Setup 400 px) max, `--surface`, 1 px `--border`, radius `--radius-lg`, padding 28 px, gap 16 px, centred with 24 px page padding. Header: `logo-art.png` at 168 px, wordmark "Stack**stead**" 24 px (600 then 400), then `window.location.host` in `--muted` 12 px (no hostname: `/api/host` needs a session). Fields use `Field` (labels `Username`, `Password`, …, ids and autocompletes unchanged). Submit: primary button 34 px full width. Footer: "Localhost only. For remote access use a TLS reverse proxy." in `--faint` 11 px (Setup: "Needed unless you open this page from the machine itself." stays as the token hint). The `role="alert"` error paragraph stays above the fields. Setup keeps its four fields, validation, focus-on-error and 403/409 handling.

### 6.3 Stacks list (`StackList.svelte`)

Order top to bottom, `gap:16px`:

1. Heading row: `h1` "Stacks", hostname in `--muted` (8b, from `/api/host`; omitted in 8a), spacer, `Import` secondary button-link, `New stack` primary button-link with a 14 px plus icon. The visible count moves into the "All N" chip.
2. Host tiles (8b): `grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap:8px`. Tile: card, `padding:10px 12px`, label 11.5 px `--muted` (ellipsis), value 17 px/600 mono, sub-line 11 px mono coloured by state, 3 px bar (`--border` track) when the tile has a ratio. Tiles and sources in §10.3; a tile whose section is `null` is not rendered.
3. Filter row: chips All / Running / Partial / Stopped, plus Unavailable and Unknown only when their count > 0 (26 px, pill, 12 px/500, count in mono `--muted`; active = `--accent` border, `--accent-soft` fill, `--accent` text; `aria-pressed`). Spacer. Search input 260 px (max 100%) with a 14 px magnifier, placeholder "Filter by name, project or path", `aria-label="Search stacks"` (the visually-hidden label stays); matches name, project and path.
4. Table card (`overflow-x:auto`). `<table aria-labelledby="stacks-heading">` with the explicit `role` attributes kept for the card layout. Columns (8a): **Stack** (8×8 radius-2 status square in the status tone; name link 500 weight, hover `--accent`, `title="project <project>"`; origin tag 10.5 px in a 1 px `--border` box; path underneath in 10.5 px mono `--faint`, `max-width:220px`, ellipsis, full path in `title`; the existing per-row `Spinner` with label `Operation running on <name>` sits after the origin tag while an operation runs), **Status** (`StatusBadge`), **Services** (`running/serviceCount` mono 12 px and a strip of 10×6 cells: `running` in `--success`, the rest `--border-strong`), **Health** (green dot + `healthy + none`, red dot + `unhealthy + failed`; amber dot + `starting` only when > 0; `—` when the stack has no containers; `title` lists the four counts), **Ports** (first three declared host ports as 10.5 px mono chips on `--raised`, then `+N` in `--faint`; `title` lists all), actions (26×26 icon buttons Up ▶, Down ■, Restart ↻ with `aria-label="Up <name>"` etc.; hover tone success/danger/text; disabled with the same `disabledReason` as the detail page: `$live.compose === 'unavailable'`, Docker unreachable, status `unavailable`, or an operation running on that stack; clicking calls `api.act` and opens `/stacks/<name>?tab=operations&op=<id>`, errors toast like the detail page). 8b inserts **CPU** and **Memory** (56×18 sparkline SVG `viewBox 0 0 60 18`, stroke 1.2 `--accent` / `--info`, `preserveAspectRatio:none`, `aria-hidden`; mono value right-aligned, `min-width` 38 / 48 px; `—` and a flat line when no samples) and **Net RX / TX** (`rx / tx` mono 11.5 px `--muted`) before the actions column. Rows: `height:var(--row)`, cell padding `var(--pad) 12px` (8 px for numeric columns), 1 px bottom border, hover `--hover`. Sorted by name (unchanged).
   States: loading → `Spinner` inside the card; error → existing danger Banner above the card plus Retry when the list is empty; degraded (every stack `unknown`) → existing warning Banner; no stacks → `EmptyState` ("No stacks yet", New stack primary, Import secondary) in place of the card; no match → "No stacks match the current filter." 16 px padding `--muted` under the header row.
5. Dashboard sections (8b): `grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap:10px; align-items:start`. **Alerts** (h3 with the count in mono `--warning`; `<ul>` of rows: 7 px tone dot, stack name link (mono, to `/stacks/<name>`), kind in uppercase 11 px tone, message in `--muted` with `overflow-wrap:anywhere`, relative `since` in `--faint` 11 px; empty: "No alerts." in `--muted`). **Top consumers** (h3 with a CPU | Memory segmented toggle, 18 px, `aria-pressed`; five rows: `container · stack` mono with ellipsis, 4 px bar scaled to the top value in `--accent` (CPU) or `--info` (memory), value mono right; empty: "No samples yet."). **Docker events** (h3 with a pulsing `live` dot while the events stream is `open`, "reconnecting" in `--warning` otherwise; mono 11.5 px rows `62px 58px 1fr`: `HH:MM:SS` in `--faint`, action in tone, container text with ellipsis; newest first, 50 kept per page; empty: "Waiting for events…").

Responsive: ≤ 720 px the table becomes cards (existing technique: `tr` as a bordered card, cells as `label: value` rows via `data-label`, header visually hidden); the actions row stays horizontal; tiles collapse to two per row (`minmax(140px,1fr)`); the filter row wraps and the search goes full width.

### 6.4 Stack detail (`StackDetail.svelte`)

`gap:14px`. **Header**: row 1 — `h1` mono stack name, `StatusBadge`, meta in `--muted` 12 px (`running/serviceCount services up · origin · project <mono>`), spacer, `ActionsBar` (Up primary with ▶ icon; Down, Restart, Pull, Build secondary; Remove `danger`; 30 px; `role="group" aria-label="Actions for <name>"`; `disabledReason` semantics unchanged; Remove confirm dialog with the volumes checkbox unchanged). Row 2 — file icon, path in mono 12 px `--muted`, `CopyButton` as a 13 px icon button (`aria-label="Copy path"`, the existing live-region feedback stays). Banners (load error, parse error / missing file) keep their text and roles below the header.

**Two columns**: `grid-template-columns: minmax(0,1fr) 300px; gap:16px; align-items:start`; the aside column is dropped below 1100 px (`grid-template-columns: minmax(0,1fr)` and the `<aside>` is not rendered, decided by a `matchMedia('(min-width: 1100px)')` listener — its content is reachable elsewhere, §8).

**Tabs** (`Tabs.svelte`): 34 px, `padding:0 12px`, 13 px/500, `--muted` → `--text` when selected, 2 px bottom border `--accent` on the selected tab (`margin-bottom:-1px` over the 1 px `--border` rule), optional badge (mono 11 px on `--raised`, radius 4): Services shows `serviceCount`, Operations shows the operations count. Roving tabindex, `aria-controls`, manual activation and the parse-error tab filtering are unchanged; `?tab=` and `?op=` query handling unchanged.

- **Services**: one `<article aria-label="<service>">` card per service, `gap:8px`. Header (`padding:10px 14px`, bottom border): 8 px dot (running `--success`, exited non-zero/dead/restarting `--danger`, exited 0 / no containers `--muted`, starting `--warning`), `h2` mono 14 px, health text in `--muted` 12 px (`healthy · up 3d` built from the first container's health and `status`), spacer, `Logs` ghost `sm` button (navigates to `?tab=logs&service=<name>`; `LogsViewer` takes an initial `service` prop) and `Edit` secondary `sm` (`aria-label="Edit <service>"`, opens the inline `ServiceEditor` in place of the card, with the existing "Close editor" ghost button under it). Body `<dl>` grid `90px minmax(0,1fr)`, 12.5 px: Image (mono, `overflow-wrap:anywhere`; `build: <context>` when building), Ports and Volumes (mono chips 11.5 px on `--raised`; `—` when none), Restart (mono, `—`), Limits (`<cpus> cpus · <memory>` or `—`). Below the `<dl>`, when present: the read-only note ("Read-only in the form: …") in `--muted`, and the container list (`<ul>`: 7 px dot, mono name, `--muted` state and status) — the same rows the aside shows, kept here because the aside disappears under 1100 px. Empty: `EmptyState` "No services".
- **Compose**: card with a toolbar (`padding:8px 12px`, bottom border, 12 px): `compose.yaml` mono (basename of the path), `· modified` in `--warning` while dirty, spacer, `⌘S / Ctrl+S to save` hint in `--faint` mono (the shortcut exists), `Preview changes` secondary `sm`, `Save` primary `sm`, and `Apply` after a save (same `disabledReason`s as today). The editor (CodeMirror, fallback textarea) sits in the card body with no own border: gutter `--surface` with `--faint` numbers, content `--surface`, highlight style keys/property names `--info`, strings `--success`, comments `--faint`, numbers `--warning`; changed lines get a `--success-soft` line background and gutter marker (`diff()` of loaded content vs draft, 150 ms debounce). Footer line: "Saving keeps the previous version as `compose.yaml.bak`. Comments and key order are preserved." Phases unchanged: loading (`role="status"`), load failed (Banner + Retry), conflict (Banner, Reload / Discard my changes), previewing (`DiffView` replaces the editor; "Back to editing"), saving, saved. The parse-error Banner stays above the card.
- **Logs**: card, toolbar wraps (`padding:8px 12px`, gap 8): Service select (`All services` + names), Stream select (`stdout + stderr`, `stdout`, `stderr`), Tail select (`Tail 100|200|1000|all`), Follow checkbox chip, Search input (180 px), spacer, status (`live · following` with pulsing dot in `--success`; `paused`, `connecting`, `reconnecting` in `--warning`, `ended` and the error labels in `--muted`; `role="status"`), Pause/Resume secondary `sm`, Clear ghost `sm`, Reconnect when ended. Selects are 26 px on `--raised`. Labels become `aria-label`s (`Service`, `Stream`, `Tail`, `Search logs`). Notes (`tail=all` warning, dropped lines, per-container errors with Dismiss) sit between toolbar and pane. Pane: `role="log"`, `tabindex="0"`, `--bg`, `height: clamp(280px, 60vh, 720px)`, mono 11.5 px/18 px, rows `grid-template-columns: 72px 90px 44px minmax(0,1fr); gap:10px; padding:0 12px`, hover `--surface`: time `--faint`, service in its hue (`hsl(h 60% 65%)` dark / `hsl(h 60% 42%)` light), stream `--faint` or `--danger` for stderr, text `pre-wrap` with `<mark>` matches; a line containing `[Error]`/`ERROR` is `--danger`, `[Warn]`/`WARN` is `--warning` (display only). The "N new lines / Jump to latest" bar stays.
- **Metrics**: toolbar: four range buttons `1h 6h 24h 72h` (26 px, mono, segmented, `aria-pressed`), spacer, `sampled every <metricsIntervalSeconds> s · retention <metricsRetentionSeconds as h>` in `--muted`, live status (`Live: open|reconnecting|…`, `role="status"`). Grid `repeat(auto-fit, minmax(280px, 1fr)); gap:10px` of six `<figure>` cards (`padding:12px 14px`): `<figcaption>` title 12.5 px/500 and latest value mono `--muted` (the sum of each service's latest value: the stack total, the same number the aside shows), uPlot 140 px high with axes in `--muted`, grid `--border`, series `--series-n`, legend restyled to 11 px `--muted` (uPlot's live legend kept). Banners (degraded, history failed), first-load `Spinner`, "No samples yet" `EmptyState` unchanged.
- **Operations**: list card: one `<a use:link>` row per operation, `grid-template-columns: 70px 100px minmax(0,1fr) 110px 60px; gap:12px; min-height:var(--row); padding:var(--pad) 12px`, 12.5 px: action mono 500; state as a dot + uppercase 11 px tone text; `docker compose <action>` in `--muted` (ellipsis); `RelativeTime`; duration mono (`formatDuration(endedAt − startedAt)`, ticking while running). Selected row (`?op=`) gets `aria-current="true"` and `--accent-soft`. Empty: "No operations yet." Output card (`OperationPanel`): header 12 px — `docker compose <action>` mono 500, state in tone uppercase, `<duration> · exit <code>` (`exit —` while running), spacer, `Cancel` (while running, unchanged), `Copy output` (`CopyButton` over the joined lines, `xs`); `op.error` in `--danger` under the header; pane `--bg`, mono 11.5 px/18 px, `max-height:260px`, lines `pre-wrap`; a line containing `✔` is `--success`, `✘`, `Error`, `error:` or `failed` `--danger` (display only); `note`/`gap` entries italic `--muted`; "Jump to latest" unchanged; the polite live region unchanged.

**Aside** (`<aside aria-label="Stack summary">`, `gap:12px`): **Containers** (h3; `<ul>` rows 6 px 12 px: 7 px dot by state/health, mono name with ellipsis, `--muted` status; all containers of all services; "No containers." when empty). **Resources · now** (h3; `<dl>` `auto 1fr`: CPU `<sum of latest per-service cpu>%`, Memory `formatBytes(sum)`, Net RX/TX `formatRate(rx) · formatRate(tx)`; `—` until the live stream has delivered a sample (rates need two); from the page-level `metrics/live` subscription). **Recent operations** (h3 with an `All` link to `?tab=operations`; three newest: dot, action mono 500, state `--muted`, `RelativeTime` in `--faint`; "None yet.").

### 6.5 New stack (`StackCreate.svelte`)

`max-width:720px`. `h1` "New stack", lead in `--muted`: "One service to start with. Stackstead writes `compose.yaml` into a folder named after the stack in its stacks directory." (the UI does not know `stacks_dir`, so no path is shown). One card, sections separated by borders, `padding:14px`: (1) Stack name (mono input, hint "Lowercase letters, digits, '-' and '_'; starts with a letter or digit; at most 63 characters.") and Service name side by side (`repeat(auto-fit, minmax(220px,1fr))`); (2) Source: label + segmented Image | Dockerfile (the existing `radiogroup` restyled: the active segment is `--accent-soft`/`--accent`), then Image, or Build context + Browse…, Dockerfile + Browse…, Image tag; (3) Ports, (4) Volumes, (5) Environment, (6) Networks (`NetworkPicker`), (7) Network mode, (8) Healthcheck — each `ListEditor` group has a 12.5 px/600 title, a `--faint` hint (`host:container`, `host:container[:ro]`, `paste a .env to add many`), `+ Add` `xs` ghost button on the right, rows of mono inputs with a 30×30 `×` remove button (`aria-label="Remove item N"` kept); (9) Restart policy select, CPUs, Memory (`minmax(160px,1fr)`); (10) footer: "Start now (`up --build`)" checkbox, spacer, Cancel link, `Create stack` primary. Field-level errors, the `_form` alert, the 503 "unavailable" banner, focus-first-error and the `FileBrowser` dialog are unchanged. The server-side `ServiceForm` read-only notes are not shown here (new stack has none).

### 6.6 Import stack (`StackImport.svelte`)

`max-width:640px`. `h1` "Import stack", lead "Register a compose file where it already lives. Nothing is moved or changed until you edit it." Card `padding:14px`: path field (mono input + `Browse…` secondary; hint "Absolute path. Browse lists the allowed directories only; the path may also point outside them."), then — only when the last submit returned a 422 or an override refusal — a bordered `--bg` box (`padding:10px 12px`, 12 px) with a header row (`Compose file` in `--muted` on the left, `cannot import` in `--danger` on the right) and the server message in mono below (this is the existing `.compose-error` `role="alert"` element restyled; there is no preview call). Footer: Cancel link, `Import stack` primary. 503 banner and `FileBrowser` unchanged.

### 6.7 Settings (`Settings.svelte`)

`max-width:720px`, `h1` "Settings", four cards with 13 px/600 `h2` headers (`padding:10px 14px`, bottom border), bodies 12.5 px `padding:12px 14px`:

1. **System status**: `<dl>` grid `repeat(auto-fit, minmax(150px,1fr))`: Docker (`<version> · ok` or `unavailable`), Compose (`<version>`), Events (`connected`/`disconnected`), Metrics (`ok · <interval> s` / `degraded`); each value mono with a 7 px tone dot; `data-testid`s kept; `Refresh` secondary `sm` button.
2. **Prometheus token**: header badge `configured` (`--success`) / `not configured` (`--muted`) uppercase 11 px. Body: explanation line, then the states as today — loading `Spinner`; error Banner + Retry; token shown once (warning Banner, readonly mono input + `CopyButton`, the scrape snippet in a `<pre>` on `--bg` with `Copy snippet`, "I have saved it" primary); configured (`Rotate token` secondary `id="rotate-token"`, `Disable` danger, both with the confirm dialogs); not configured ("No token exists, so `/metrics` answers 404 until you create one." + `Create token` primary). Footer line keeps the `docs/api.md` reference.
3. **Appearance**: Theme segmented Dark | Light | System and Density segmented Compact | Comfortable (26 px, `aria-pressed`), `repeat(auto-fit, minmax(200px,1fr))`.
4. **Account**: "Signed in as **user**. Changing the password signs out other sessions." then `PasswordForm` (three fields in `repeat(auto-fit, minmax(180px,1fr))`, rate-limit Banner unchanged), buttons `Change password` primary and `Log out` secondary. Footer line: "Remote access and hardening: docs/security.md." (the old About card's second link).

### 6.8 Shared components

- **Button**: props unchanged (`variant`, `loading`, `disabled`, `disabledReason`, `type`, rest) plus `size: 'md' | 'sm' | 'xs'` (default `md`) and `icon?: boolean` (square, no padding). Variants: `primary` (`--accent` bg, `--accent-ink`, 600, no border), `secondary` (`--surface`, 1 px `--border-strong`, hover `--hover`), `danger` (transparent, 1 px `--border`, `--danger` text, hover `--danger-soft`; inside `ConfirmDialog` it renders with the `--danger-soft` fill always so the primary action is visible), `ghost` (transparent, 1 px `--border`, `--muted` text, hover `--hover` + `--text`). `aria-disabled`/`aria-busy`/`aria-describedby` reason and the spinner stay.
- **StatusBadge**: `display:inline-flex; height:20px; padding:0 7px; border-radius:4px; font:600 11px; letter-spacing:.02em; text-transform:uppercase`, background `--<tone>-soft`, colour `--<tone>`; no icon glyph (the old `aria-hidden` glyphs go). Tones: running/succeeded/healthy → success; partial/timed_out/starting/restarting → warning; stopped/cancelled/unknown/exited/none → muted; unavailable/failed/unhealthy → danger. Labels unchanged (`timed out` keeps its space).
- **Banner**: `--<kind>-soft` fill, 3 px left border in the tone, 1 px `--border` otherwise, radius `--radius-md`, `padding:10px 12px`, title 13 px/600, body `--muted` 12.5 px; roles (`alert` for danger, `status` otherwise) and `dismissible` unchanged.
- **Tabs**: §6.4. **Field**: label 12 px/500 `--muted`; hint 11.5 px `--faint`; error 12 px `--danger` with the `✖` glyph kept; inputs/selects/textareas `height:32px; padding:0 10px; background:var(--bg); border:1px solid var(--border-input); border-radius:var(--radius)`, `aria-invalid` → `--danger` border, readonly/disabled → `--raised` and `--muted`; new `mono?: boolean` prop applies `--mono` to the control.
- **Toasts**: fixed `right:16px; bottom:16px`, `--surface`, 1 px `--border-strong`, radius `--radius-md`, `--shadow-pop`, `padding:10px 14px`, 12.5 px, 7 px dot per kind, `×` button (`aria-label="Dismiss notification"`); portal, `aria-live`, timeouts unchanged.
- **Dialog / ConfirmDialog**: backdrop `rgba(0,0,0,.5)`, dialog `--surface`, 1 px `--border-strong`, radius `--radius-lg`, `--shadow-pop`, `padding:20px`, `h2` 14 px/600; focus trap, inert root, Escape stack, Cancel-first order unchanged.
- **EmptyState**: dashed 1 px `--border-strong`, radius `--radius-md`, `padding:32px 16px`, `h2` 14 px/600, description `--muted`. **Spinner**: 16 px ring, 2 px `--border` with `--accent` arc; label unchanged. **CopyButton**: ghost `xs` or icon; feedback unchanged. **ListEditor**: rows `gap:8px`, remove button 30×30 ghost with `×`; add button ghost `xs`. **FileBrowser**: dialog with crumb buttons as links, entries as 30 px full-width ghost rows (📁/📄 glyphs kept `aria-hidden`), footer Cancel / Use this directory. **RelativeTime** unchanged.
- **Sparkline** (8b, `lib/components/Sparkline.svelte` + pure `sparklinePath(values, w=60, h=18)` in `lib/format.ts`): nulls break the path; a single point draws a flat line; `aria-hidden` with the value rendered beside it.
- **HostTile**, **AlertsCard**, **TopConsumersCard**, **EventsFeed** (8b, `lib/dashboard/`).

## 7. Accessibility

- Every icon-only control has `aria-label`; `title` duplicates it for mouse users. Rail links: `aria-current="page"`. Segmented controls and filter chips are buttons with `aria-pressed`. Tab list keeps roving tabindex and manual activation. Tables keep explicit roles so the card layout under 720 px stays a table to AT. The events feed `<ul>` is not a live region (it would chatter); the Alerts count is in the heading text.
- Focus is visible on every control (`:focus-visible`, 2 px `--accent`, offset 1 px) including rail links, chips, table action buttons and log/output panes (`tabindex="0"`).
- Colour never carries meaning alone: status squares/dots sit next to a badge or text; Health cells have numbers; the services strip has `running/count`; tile bars have values; sparklines have values.
- Contrast per §4.3. Hover and selected states keep ≥ 4.5:1 for text (`--hover` was adjusted for this).
- Reduced motion per §5. `prefers-contrast` is not special-cased: the palette passes AA as is.
- Keyboard: nothing new traps focus; dialogs unchanged; `Ctrl/⌘+S` in the Compose tab unchanged; `Escape` closes dialogs only.

## 8. Behaviour mapping (nothing lost)

| Route | Current feature | Now |
|---|---|---|
| global | Nav links Stacks / New stack / Import / Settings with `aria-current` | Rail (§6.1) |
| global | `N running` badge, username, Log out button | Top-bar pill; avatar button (initials, `aria-label="Log out"`, username in `title` and in Settings › Account) |
| global | Docker/compose/metrics/reconnecting banners; 404; focus to `h1` after navigation; unsaved-changes guard and dialog; toasts; titles | Unchanged, restyled |
| `/login`, `/setup` | fields, `role="alert"` error, `?next=` safety, 403/409/429 handling | Unchanged in the new card |
| `/` | heading, count, search (name, project), degraded/error banners, retry, loading, empty state, table (Name, Status, Running, Origin, Project, Path), per-row operation spinner, "No stacks match" | Count → All chip; search also matches path; Name+Origin+Path → Stack cell; Running → Services cell; Project → row `title` on the name and in detail meta; spinner kept; new Health, Ports and actions columns (8a), CPU/Memory/Net (8b) |
| `/stacks/:name` | header (name, badge, origin/project/path), ActionsBar incl. Remove + volumes, banners, tabs with parse-error filtering, services cards (summary, restart, read-only note, containers, Edit → inline editor, Close editor), compose editor phases + preview + apply + shortcut, logs toolbar/options/pause/clear/reconnect/search/errors/jump, metrics range/live/degraded/empty, operations list + panel + cancel + finished handling, `?tab`/`?op`, removal flow | All present (§6.4); containers also in the aside; recent ops also in the aside; path has a copy button; Logs button on service cards is new |
| `/stacks/new` | all fields, validation, server errors, 503 banner, file browser, start-now, title | Unchanged inside one card |
| `/stacks/import` | path, browse, 422/503/400 handling, title | Unchanged; the 422 message renders in the "Preview" box |
| `/settings` | Prometheus token states + dialogs, System status + Refresh, Account (signed in as, Log out, change password with rate limit), About links | Cards (§6.7); About folded into the Prometheus and Account footers |
| `Settings.test`, `App.test`, `components.test` selectors | `getByRole('navigation', {name:'Main'})`, link names, `button 'Log out'`, `/2 running/`, `data-testid`s, labels, badge text | All preserved by the `aria-label`s and texts above |

## 9. Responsive rules

| Width | Rule |
|---|---|
| ≥ 1100 px | Detail aside shown (300 px). Tiles auto-fit at 160 px. |
| < 1100 px | Aside not rendered; Containers live in the service cards, Resources in the Metrics tab, Recent operations in the Operations tab. |
| ≤ 900 px | Compose pill hidden; Docker pill is a dot with `aria-label`. Table card scrolls horizontally. |
| ≤ 720 px | Breadcrumb shows the last crumb only; table → cards; tiles two per row; toolbars wrap; forms single column; New/Import cards full width; toasts span `inset-inline:16px`. |
| any | Rail stays 52 px; main padding 20 px (16 px ≤ 720 px); no horizontal page scroll (`min-width:0` everywhere a grid child holds text). |

## 10. API changes

All new routes are registered with `requireSession`; unknown `/api/*` keeps answering a JSON 404 after auth. Error bodies keep `{error}`. Nothing new is reachable without a session; `/api/health` is untouched.

### 10.1 Plan 8a

**`ContainerInfo`** gains `"health": "healthy" | "unhealthy" | "starting" | "none"` (parsed from `Status`; `none` when the container has no healthcheck or is not running). `engine.Container.Health()` holds the parser; `state.view` uses it.

**`StackSummary`** (and `StackDetail`, which flattens it) gains:

```json
"health": {"healthy": 2, "unhealthy": 0, "starting": 0, "none": 1, "failed": 1},
"ports": ["80", "443", "8096"]
```

`health` counts the project's containers: `healthy`/`unhealthy`/`starting` from the health string for running containers, `none` = running without a check, `failed` = state `dead` or `restarting`, or `exited` with a non-zero code (`Exited (N)` parse from `Status`). Containers exited with code 0 count nowhere (a stopped stack shows `—`). `ports` = declared host ports of all services via `stacks.ParsePorts` (ranges kept as `lo-hi`, protocol dropped, deduplicated, sorted numerically by `lo`); empty when the file cannot be parsed. Both are computed in `stacks.describe`, so `List` and `Get` agree.

**`Status`** gains `"dockerVersion": "27.1.1"` (from `GET /version` → `Version`, fetched by the same probe that pings; `""` when unavailable), `"metricsIntervalSeconds": 10` and `"metricsRetentionSeconds": 259200` (config echoes, via `api.Options`). The CLI's `status` output prints the version when present (one line in `cmd_login.go`); `docs/cli.md` mentions it. On the client, `LiveState` gains `dockerVersion`, `metricsIntervalSeconds` and `metricsRetentionSeconds`, filled by `refreshStatus` like `docker` and `compose`.

### 10.2 Plan 8b: `GET /api/host`

```json
{
  "hostname": "docker-host-01",
  "os": "linux",
  "source": "proc",
  "sampledAt": "2026-10-15T14:02:30Z",
  "containers": {"total": 14, "running": 10, "exited": 4, "bad": 1},
  "cpu": {"percent": 18.2, "cores": 8},
  "memory": {"used": 6656000000, "total": 17179869184, "swapUsed": 0, "swapTotal": 2147483648},
  "disk": {"path": "/opt/stacks", "free": 442381631488, "total": 1073741824000},
  "network": {"rxBytesPerSec": 16000000, "txBytesPerSec": 4000000, "interface": "eth0"},
  "diskIO": {"readBytesPerSec": 1200000, "writeBytesPerSec": 400000},
  "volumes": {"count": 14, "size": 40802189312, "dangling": 2},
  "uptime": {"seconds": 1830240, "load1": 1.42, "load5": 1.10, "load15": 0.95},
  "restarts24h": {"total": 3, "top": [{"container": "monitoring-loki-1", "service": "loki", "project": "monitoring", "count": 2}]}
}
```

- `source` is `proc` (Linux `/proc` readable) or `docker`. Every section except `hostname`, `os`, `source`, `sampledAt` is nullable; a `null` section means "no source" and the tile is hidden. Rate fields (`network`, `diskIO`, `cpu.percent`) are `null` until the collector has two readings at least 1 s apart; the tile then shows `—` but stays.
- `containers` comes from the state cache (`bad` = `unhealthy` + `failed` as in §10.1); `null` while the cache is disconnected.
- Always 200. Docker unavailable → `containers`, `volumes`, `restarts24h` null; on `docker` source also `cpu`, `memory`, `network`, `diskIO` null. The UI shows the remaining tiles and the global Docker banner says why.
- Security: no IP addresses, kernel version, mount table, environment or paths beyond `stacks_dir` (already shown in the UI). `hostname` is `os.Hostname()`. The handler is session-gated like every `/api` route.
- Caching: `host.Collector.Snapshot(ctx)` is single-flight and returns the cached snapshot when it is < 5 s old. Collecting reads `/proc` files (or Docker `/info` + `Sampler.Latest()`), `Statfs`, and the cache; `volumes` (`GET /system/df?type=volume`, falling back to `/system/df` on API < 1.45) and `restarts24h` (`GET /events?since=<now−24h>&until=<now>&filters={"type":["container"],"event":["start"]}`) are 60 s sub-caches: when stale, the old value is served and one background refresh runs; before the first refresh completes they are `null`. Each Docker call has a 5 s timeout. Collector state (previous counters, sub-caches, first-seen map) lives in one struct wired in `serve.go`.
- `restarts24h`: per container id, `count = max(0, starts − 1)` where `starts` is the number of `start` events in the window (a restart loop is die+start pairs; an explicit `docker restart` is one extra start; a fresh container's first start is not a restart). `total` sums counts; `top` is the two highest, with the compose service/project labels from the event's attributes (`Actor.Attributes["com.docker.compose.service"]`), name from `Actor.Attributes["name"]`.

### 10.3 Linux collection and fallback matrix

`internal/host` (new package, stdlib only). `procRoot` and `sysRoot` are parameters (default `/proc`, `/sys`) so tests use fixtures.

| Field | Linux (`source: proc`) | Other (`source: docker`) |
|---|---|---|
| `cpu.cores` | count of `cpu\d+` lines in `/proc/stat` | Docker `/info` `NCPU` |
| `cpu.percent` | `/proc/stat` line `cpu`: `busy = total − idle − iowait`; `Δbusy/Δtotal × 100` between readings | Σ container `CPU` from `Sampler.Latest()` ÷ `NCPU` (percent of host); sub-line "containers only" |
| `memory` | `/proc/meminfo`: `used = MemTotal − MemAvailable`, `swapUsed = SwapTotal − SwapFree` (kB × 1024); `null` if `MemAvailable` is missing | `total` = `/info` `MemTotal`, `used` = Σ container `Mem`; `swap*` 0; sub-line "containers only" |
| `disk` | `syscall.Statfs(stacks_dir)`: `free = Bavail×Bsize`, `total = Blocks×Bsize` | same (`statfs_unix.go`, `//go:build linux \|\| darwin`); `null` elsewhere or on error |
| `network` | `/proc/net/dev` rx/tx byte columns summed over interfaces that have `/sys/class/net/<if>/device` (physical and virtio NICs; excludes `lo`, bridges, veth, tun/wg); if none qualify, all except `lo`, `docker*`, `br-*`, `veth*`; `interface` = the busiest included NIC by Δbytes | Σ container net rates from consecutive `Sampler.Latest()` readings; `interface` = `"containers"` |
| `diskIO` | `/proc/diskstats` sectors read (field 6) and written (field 10) × 512, summed over whole-disk names matching `^(sd[a-z]+\|vd[a-z]+\|xvd[a-z]+\|hd[a-z]+\|nvme\d+n\d+\|mmcblk\d+)$` (partitions, `loop`, `ram`, `zram`, `dm-`, `md` excluded to avoid double counting) | Σ container block rates, same method |
| `uptime` | `/proc/uptime` first field; `/proc/loadavg` first three fields | `null` |
| `volumes` | Docker `/system/df` on both | same |
| `restarts24h` | Docker `/events` on both | same |
| `containers` | state cache on both | same |

Rates use the collector's own previous reading (`(cur − prev) / Δt`, `0` when a counter wrapped or dropped); the first request after a quiet period therefore averages over the idle gap, which is acceptable for a dashboard tile and documented.

### 10.4 Plan 8b: `GET /api/alerts`

```json
[{"kind": "exited", "severity": "danger", "stack": "monitoring", "service": "loki", "container": "monitoring-loki-1",
  "message": "loki exited (1) and restart policy is unless-stopped; 2 restarts in 24h", "since": "2026-10-15T12:05:41Z"}]
```

`stack`, `service`, `container` are omitted when not applicable. Sorted by severity (`danger`, `warning`, `info`) then `since` descending. Kinds and rules (evaluated over the registry, each stack's parsed compose file, the container snapshot, the host collector's restart counts and the status flags):

| kind | severity | when | message |
|---|---|---|---|
| `exited` | danger | a container of a registered project is `exited` with a non-zero code (exit 0 is a deliberate stop; `unless-stopped` and `always` both stay down after one, so the policy cannot tell the cases apart) | `<service> exited (<code>)[ and restart policy is <policy>][; <n> restarts in 24h]` — the policy clause appears when it is `always` or `unless-stopped` |
| `unhealthy` | danger | container health `unhealthy` | `<service> is unhealthy` |
| `restart_loop` | warning | ≥ 3 restarts in 24 h for one container | `<service> restarted <n> times in 24h` |
| `port_clash` | warning | a declared host port (§10.1) is declared by two registered stacks, or is published by a running container of another project or of no project | `<service> wants <port> but <other stack \| a non-compose container (<name>)> holds it` |
| `override` | warning | an override file sits next to the compose file (`stacks.overrideFiles`) | `<file> found; override files are not supported yet` |
| `parse_error` | warning | `Detail.ParseError` non-empty | the first line of the error |
| `docker_disconnected` | danger | `Cache.Connected()` is false | `Docker is unreachable; status may be stale` |
| `metrics_degraded` | warning | `Sampler.Degraded()` | `Metrics are not being stored (database write failures)` |

Cached 5 s, single-flight. `since` is the first time this process observed the alert key (`kind|stack|service|container`); keys not present in a computation are forgotten. Nothing is persisted. Always 200 (an empty list is `[]`); a registry read failure is a 500 `{error}`.

### 10.5 Plan 8b: `GET /api/metrics/summary?window=15m`

`window` ∈ `5m | 15m | 1h` (default `15m`; other values → 400). Thirty buckets, `step = window / 30`.

```json
{
  "window": "15m", "step": 30, "from": "2026-10-15T13:47:30Z", "to": "2026-10-15T14:02:30Z",
  "stacks": {
    "media": {"cpu": [4.1, 4.3, null, …], "mem": [1900000000, …], "cpuNow": 4.2, "memNow": 1900000000,
              "netRxBytesPerSec": 12000000, "netTxBytesPerSec": 600000}
  },
  "top": {"cpu": [{"container": "media-jellyfin-1", "service": "jellyfin", "project": "media", "value": 3.1}],
          "mem": [{"container": "media-jellyfin-1", "service": "jellyfin", "project": "media", "value": 1174405120}]}
}
```

- Keyed by compose project (the client maps project → stack via `StackSummary.project`); projects without samples in the window are absent.
- `cpu[i]`/`mem[i]` = Σ over the project's containers of the container's bucket average (`CPUSum/Samples`, `MemSum/Samples`); `null` for empty buckets. `cpuNow`/`memNow` = the newest non-null bucket. Net rates = Σ of per-container `(max − prevMax)/step` from the last two buckets each container has (same rule as `AggregateServices`, gap ≤ 3 buckets); `null` when none. New `Store.QueryAll(ctx, from, to, step) ([]ProjectBucket)` groups by `project, container, bucket`; `metrics.AggregateProjects` folds it.
- `top`: five highest containers by `CPU` and by `Mem` from `Sampler.Latest()` (already in memory, no query).
- Cached 10 s, single-flight. Store error → 500; the UI keeps the previous data and retries on the next interval.

### 10.6 Plan 8b: `event` messages on `GET /api/events`

```
event: event
data: {"action":"die","container":"monitoring-loki-1","project":"monitoring","service":"loki","image":"grafana/loki:3.1.1","time":"2026-10-15T12:05:41Z","exitCode":1}
```

- `engine.Event` gains `Name`, `Image`, `ExitCode` (`*int`) and `Health` (`healthy|unhealthy` for `health_status: …` actions, whose `action` is normalised to `health_status`) from `Actor.Attributes`. `state.Cache` gains `SubscribeEvents() (<-chan engine.Event, func())` with the same buffered, drop-on-slow semantics as `Subscribe`; it forwards container events except `exec_*` (and still debounces resyncs as today). `handleEvents` merges both channels; a drop on either sends `dropped` and ends the stream.
- Forwarded actions: `create start die stop kill restart destroy pause unpause oom health_status`. `exitCode` only on `die`; `health` only on `health_status`. `project`/`service` are empty strings for non-compose containers.
- No `id` and no replay: the feed is informational; a reconnect starts fresh (the UI keeps what it has). `status` and `change` messages are unchanged, so Plan 5 clients keep working.

## 11. Refresh policy and performance

| Data | Trigger | Server cost |
|---|---|---|
| Stack list | `change`/`resync` (debounced 300 ms, max 1 s) — unchanged | one cache snapshot |
| Running ops | unchanged (debounced on changes, backoff poll while running) | — |
| `/api/host` | list page mounted and `document.visibilityState === 'visible'`: every 10 s; paused when hidden | ≤ 1 collection / 5 s regardless of tabs |
| `/api/metrics/summary` | list page visible: every 30 s, plus once on mount | ≤ 1 query / 10 s |
| `/api/alerts` | list page visible: on every `change`/`resync` (same debounce as the list) and every 60 s | ≤ 1 computation / 5 s |
| Events feed | the existing `/api/events` stream | fan-out only |
| Detail live metrics | one `metrics/live` SSE per detail page while the aside is visible or the Metrics tab is active | existing sampler fan-out |
| Theme/density | local | none |

No new timers run on the detail, create, import or settings pages. All list-page timers are cleared on unmount and on `visibilitychange` to hidden, and resumed (with an immediate fetch) when visible again. The entry chunk gains the rail, top bar, tiles, chips, sparklines and the appearance store (all small, no new dependencies); CodeMirror and uPlot stay lazy. Fonts are outside the JS budgets (§3).

## 12. Testing strategy

Test-first throughout (user rules). Only tests that catch a distinct regression are added.

**Go (unit, no Docker):**
- `internal/engine`: `Container.Health()` and exit-code parsing table; `Event` attribute decoding from a recorded `/events` JSON fixture (die with exit code, `health_status: unhealthy`, exec_ noise); `/version` decoding.
- `internal/stacks`: `health` counts and `ports` on `describe` (healthy/unhealthy/starting/none/failed/exited-0, ranges, protocols, duplicates, parse error → empty).
- `internal/host`: parsers against `testdata/proc/*` fixtures (`stat`, `meminfo` with and without `MemAvailable`, `loadavg`, `uptime`, `net/dev` with lo/veth/eth0 and a `sys/class/net` tree, `diskstats` with sda, sda1, nvme0n1, loop0, dm-0); rate computation between two fixture sets, wrap → 0; collector caching (5 s, single-flight under 50 concurrent calls = one read), sub-cache staleness (served stale, refreshed once); fallback matrix with a fake Docker (`/info`, `/system/df`, `/events`) and a fake sampler; restart counting rule; `Statfs` via an interface with a fake.
- `internal/alerts`: a table of snapshots → expected alerts (each kind, severity order, `since` stability across two computations, disappearance).
- `internal/metrics`: `QueryAll` + `AggregateProjects` against the real SQLite store (per-project sums, nulls, rate from the last two buckets, gap rule); `window` validation.
- `internal/api`: contract tests for every new field and route (`observe_test`, `stacks_test`, `client_contract_test` so the CLI client decodes the new fields); `event` messages interleaved with `change`/`status` on `/api/events` using a fake `SubscribeEvents`; `dropped` on either channel; session required on every new route; `/api/host` body has no key outside the documented set (guards against leaking `/info` wholesale); woff2 served with `font/woff2` and `/theme.js` with `no-cache`.

**Vitest:**
- `appearance` store: reads initial attributes; `setTheme('light')` writes attribute and storage; `system` removes both; `localStorage` throwing (private mode) does not break it; media-query change flips `effectiveTheme` only in `system`.
- Rail/TopBar: existing `App.test` and `components.test` nav tests pass as written (names preserved); new: theme toggle label flips; Docker/compose pills render from `$live`; breadcrumb per route.
- `StackList`: chips filter and count; search matches path; Health cell numbers and `—`; Ports `+N` and `title`; inline Up/Down/Restart call `api.act`, navigate to the operation, respect `disabledReason`; (8b) tiles hidden for `null` sections, "containers only" sub-line for `source: 'docker'`, rates `—` while `null`; sparkline path for nulls/single point; alerts render and link; top consumers toggle; events feed caps at 50 and prepends; visibility pauses timers (fake timers).
- `StackDetail`: aside hidden below 1100 px (mock `matchMedia`), Containers/Resources/Recent operations content; `Logs` button preselects the service; Operations rows show duration; `Copy output` copies joined lines; everything in `StackDetail.test` unchanged.
- Components: `StatusBadge` tone classes for every status; `Button` sizes and `danger` outline; `Field` `mono`; changed-line decoration count in `ComposeEditor` after a draft edit (fallback textarea path).
- `bundle.test`: font budget counted and enforced; fonts excluded from `entry`/`total`.

**E2E (`cmd/stackstead/e2e_test.go`, `chrome render`):** render `/login?theme=dark` and `/login?theme=light` with `--screenshot` (375×812 and 1280×800) and `--dump-dom`; assert `data-theme` on `<html>`, the login form, no placeholder page, and that the screenshot's pixel at (4,4) is `#0f1216` (dark) and `#f3f5f7` (light) (decoded with `image/png`); assert `/theme.js` and one `/assets/*.woff2` return 200 with the right content types; with Docker, after the smoke stack is up, `GET /api/host`, `/api/alerts`, `/api/metrics/summary` answer 200 with the documented keys (8b).

**Budgets:** `make ui` fails when entry > 120 kB gz, total > 400 kB gz, fonts > 200 kB, or a CodeMirror/uPlot chunk is eager. `make check` runs `svelte-check`, Vitest, `go vet`, gofmt and the Go tests.

## 13. Delivery: Plans 8a and 8b

**8a — restyle, shell, components, detail, list table.** Tokens, fonts, licences, `theme.js`, appearance store, Rail/TopBar, every route and component in §6 except the 8b items, `Logs` button on service cards, detail aside (Containers, Resources · now from the page-level live stream, Recent operations), §10.1 wire changes (`health`, `ports`, `dockerVersion`, metrics settings), `mime` registration, logo assets, `favicon.svg`, e2e theme render, font budget, docs (§14). Ships first and merges to `main` after the final review.

What 8a shows where 8b data does not exist yet: the Stacks heading has no hostname; no host tiles; the table has Stack, Status, Services, Health, Ports and actions (no CPU, Memory, Net RX/TX columns); nothing is rendered below the table; the aside's Resources · now works (it uses existing live samples); the Docker events feed, alerts and top consumers do not exist in the UI. No placeholder sections and no dashes-for-missing-data: the page simply ends after the table.

**8b — dashboard data.** Backend: `internal/host`, `internal/alerts`, `Store.QueryAll` + `AggregateProjects`, `engine.Event` attributes, `Cache.SubscribeEvents`, the three routes and the `event` message, `serve.go` wiring, Go tests. Frontend: hostname, HostTile grid, CPU/Memory/Net columns with `Sparkline`, AlertsCard, TopConsumersCard, EventsFeed, the list-page refresh policy (§11), Vitest. Docs for the new endpoints.

Dependencies: 8b's backend touches `apitypes/types.go`, `observe.go`, `stacks_dto.go` and `serve.go`, which 8a also edits for §10.1; the conflicts are additive and trivial. The 8b backend can start on a branch from `main` in parallel with 8a and rebases after 8a merges; the 8b frontend starts after 8a merges because it uses 8a's tokens, cards and `Button`/`StatusBadge`. Both plans follow the user's process: Sonnet implements test-first, Opus reviews each task, Fable does the final review, and the branch merges autonomously when it passes.

## 14. Documentation updates

- `docs/api.md`: `health` on containers and stacks, `ports`, `dockerVersion` and metrics settings on `/api/status` (8a); `/api/host`, `/api/alerts`, `/api/metrics/summary`, the `event` message and the fallback matrix summary (8b); refresh/caching notes.
- `docs/development.md`: fonts and licences, the font budget, `theme.js` and the `?theme=` override, the e2e theme screenshots, the `web/public` files.
- `docs/security.md`: CSP unchanged; `theme.js` is a same-origin classic script; `localStorage` holds only theme and density; `/api/host` discloses hostname, core/memory totals, `stacks_dir` free space and a NIC name to authenticated users only.
- `docs/cli.md`: `status` shows the Docker version.
- `README.md`: the "Accessible" bullet adds the theme toggle and density; a "Third-party" line names IBM Plex Sans and Geist Mono (OFL-1.1) and where the licences are served; `make ui` mentions the font budget.
- `docs/superpowers/plans/2026-10-15-stackstead-08a-ui-v2.md` and `…-08b-dashboard-data.md` are written from this spec (not part of it).
