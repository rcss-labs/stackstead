# Stackstead Plan 16a — React foundation (a): store contract, scaffold, `useStore`, tokens

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** tasks, names, signatures, test cases and examples only; no code bodies. Implementers write everything test-first.

**Goal:** Replace the only Svelte dependency of the TypeScript under `web/src/lib` (`svelte/store`) with a dependency-free, contract-identical `$lib/store`, prove it with the unchanged test suite while the Svelte app keeps shipping; then stand up the React 19 scaffold next to Svelte (second HTML entry, second Vite config, TSX type-check and tests, bundle budget on the React build from day one), the `useStore` hook, and the Evolve v2 design tokens, fonts and base CSS.

**Architecture:** `web/src/lib/store.ts` implements `writable` (start/stop notifier, queued notification), `readable`, `derived` (single and multiple sources, sync and `set` forms), `get` and the `Readable`/`Writable` types with `svelte/store` semantics; every `svelte/store` import in `web/src` (21 lib modules including `router.ts`, 40 test files, one `.svelte` file) switches to it. React lives in `web/src/app/` and is served by `web/app.html` + `vite.app.config.ts` (dev history fallback to `app.html`, same `/api` + `/metrics` proxy, build to the git-ignored `web/dist-app/` measured by `scripts/check-bundle.mjs`). `useStore(readable)` binds any store to React through `useSyncExternalStore`. Tokens, fonts, reset and the reduced-motion rule live in `web/src/app/styles/` and are pinned by the ported `tokens.test.ts` plus an `app.html` contract test.

**Tech Stack:** Svelte 5 (still shipping), React 19.3.1, react-dom 19.3.1, react-router 8.4.0 (installed here, used in Plan 16b), @vitejs/plugin-react 6.1.2, @testing-library/react 16.3.3, @types/react 19.3.0, @types/react-dom 19.3.0, Vite 8, Vitest 5, TypeScript 6 strict. All versions pinned exactly (`guards.test.ts` enforces it). Nothing else.

**Spec:** `docs/superpowers/specs/2026-10-19-stackstead-react-evolve-v2-design.md` §5 (constraints, budgets), §6.1 (store contract), §6.2 (coexistence), §6.3 (tokens, reduced motion), §6.4 (testing), §9 row 16, §10. Design reference: `docs/superpowers/design_handoff_stackstead_react/README.md` "Design tokens". **Plan 16b** (`2026-10-19-stackstead-16b-react-foundation.md`) builds routing, shell, palette, toasts and primitives on top of this plan and must merge after it.

## Spec divergences

1. **Row 16 is split into 16a and 16b** (this file and `-16b-`). Items 1–4 of the row (store, scaffold, `useStore`, tokens) are ~12 files plus a mechanical 62-file import switch; items 5–7 (routing, shell, palette, primitives) are ~50 new files. Two stacked PRs keep each reviewable. Decision scored with the typesafe-ai skill: split warranted 0.91; the 1–4 / 5–7 cut 0.69 over "store switch alone" 0.19 and three PRs 0.12.
2. **`router.ts` gets the import switch only.** Spec §6.1 calls it "the exception" because routing moves to React Router. It is still a Svelte-app module until cutover, so it switches to `$lib/store` like the other 20 files and changes nothing else; its pure `matchRoute` is what Plan 16b reuses, and the file is deleted in Plan 23. (typesafe-ai: 0.75 over moving pure helpers into a new module now, 0.18.)
3. **Test files switch too.** Spec §6.1 names the 21 lib modules; the 40 `*.test.ts` files and `lib/stacks/AddServicePanel.svelte` that import `get`/`writable` from `svelte/store` switch as well, so that `svelte/store` appears nowhere under `web/src` and a guard test can pin that. The tests then also exercise the new module's `get` and `writable` against Svelte components, which is the strongest "no behaviour change" evidence available.
4. **Type-checking React:** `npm run check` becomes `svelte-check` followed by `tsc --noEmit -p tsconfig.app.json` (new file) so `.tsx` is checked strictly without relying on svelte-check's TSX coverage; Plan 23 drops the svelte-check half. (typesafe-ai: 0.99.)
5. **Radii tokens take the README's values** (`--radius-lg` 12px cards, `--radius-md` 9px inner panels, `--radius` 7px buttons/inputs, `--radius-xs` 6px small buttons, `--radius-sm` 4px tags, `--radius-led` 2px, `--radius-pill` 999px) instead of today's 4/6/8/10. The names stay so the token-coverage test and later screen plans read naturally; only `web/src/app/styles/tokens.css` carries them, so the Svelte app is untouched.
6. **Series and storage-kind colours:** the README gives one set (`#60a5fa #f97316 #a78bfa #14b8a6`; Images/Volumes/Layers/Build cache `#60a5fa #a78bfa #14b8a6 #f97316`). `--series-1..4` take those in both themes, `--series-5..8` keep today's per-theme values, and four new `--kind-*` tokens carry the storage colours. Series colours are not in the AA contrast set (they colour marks, not text), as today.

## Global Constraints

- The Svelte app ships unchanged: `web/dist`, `web/embed.go`, `internal/api/spa.go`, `make build`, `make ui` and the e2e smoke are not touched. `web/dist-app/` is git-ignored and never embedded.
- `svelte/store` semantics are the contract (§6.1): synchronous first delivery on subscribe; `set` skips notification only when the new value is a primitive equal to the old one (objects and functions always notify; `NaN` equals `NaN`); sets issued during a notification round are queued and delivered to every subscriber after the round, in order; the start notifier runs on the first subscriber and its returned stop on the last unsubscribe; `derived` subscribes to its sources lazily and releases them when unused.
- No new runtime dependency beyond the seven listed. No Google Fonts; fonts are the existing woff2 files in `web/src/assets/fonts/`.
- Budgets unchanged (`ENTRY_BUDGET` 120 KiB gz, `TOTAL_BUDGET` 400 KiB gz, `FONT_BUDGET` 200 KiB) and enforced on **both** builds: `npm run build` (Svelte, as today) and `npm run build:app` (React).
- Nothing under `web/` references `docs/superpowers/design_handoff_stackstead_react/` (spec §4); a guard test pins it.
- No `{@html}` in Svelte and no `dangerouslySetInnerHTML` in React; the guard test covers `.tsx`.
- Tokens are the only place with literal colours: `*.module.css` and `base.css` use `var(--…)`.
- Test-first, conventional commits, no code bodies in this plan.

## Review Focus

1. `writable.set` with an equal primitive must not notify, but `set` with the same object reference must (Svelte's `safe_not_equal`); `update(v => v)` on an object store therefore notifies. A "smart" equality would silently change `stacks`, `live` and `refreshList` behaviour. (Task 1.)
2. A `set` issued from inside a subscriber during a notification round is delivered after the current round to every subscriber, in order, never re-entrantly or out of order; subscribers added during a round do not receive the in-flight value twice. (Task 1.)
3. `derived` over several sources never calls `fn` before every source has delivered, and when its last subscriber leaves it unsubscribes from every source (otherwise `effectiveTheme` keeps the media-query store alive and `mediaQuery` listeners leak). (Task 1.)
4. The scaffold leaves the Svelte build byte-for-byte equivalent in behaviour: `npm run build` still reads `vite.config.ts`, the Svelte entry budget is unchanged, `web/dist` content is the same set of files, and `git status` shows no `dist-app` entries after `npm run build:app`. (Task 3.)
5. Token parity: the light `:root` block, the explicit dark block and the media-query dark block define the same token names (39 colour-class names: today's 32 plus `--chassis`, `--hi`, `--overlay` and the four `--kind-*`), the media block equals the explicit dark block exactly, and every AA pair passes including `--text`/`--muted` on the new `--chassis` surface. (Task 5.)

---

## File structure

| Area | Files | Tasks |
|---|---|---|
| Store contract | `web/src/lib/store.ts` (new), `web/src/lib/store.test.ts` (new) | T1 |
| Import switch | 21 lib modules (`api/session.ts`, `containers/list.ts`, `dashboard/controller.ts`, `logs/buffer.ts`, `networks/networks.ts`, `router.ts`, `stacks/{compose,liveMetrics,logs,metrics,operation,serviceAdd,serviceEdit}.ts`, `storage/controller.ts`, `stores/{appearance,guard,live,media,refreshList,stacks,toasts}.ts`), 40 `*.test.ts` files, `lib/stacks/AddServicePanel.svelte`, `web/src/guards.test.ts` | T2 |
| React scaffold | `web/package.json`, `web/package-lock.json`, `web/app.html` (new), `web/vite.app.config.ts` (new), `web/vite.config.ts`, `web/tsconfig.json`, `web/tsconfig.app.json` (new), `web/src/vitest-setup.ts`, `web/src/app/main.tsx` (new), `web/src/app/App.tsx` (new, placeholder), `web/src/app/App.test.tsx` (new, smoke), `web/src/app/html.test.ts` (new), `web/src/guards.test.ts`, `.gitignore`, `docs/development.md` | T3 |
| `useStore` | `web/src/app/hooks/useStore.ts` (new), `web/src/app/hooks/useStore.test.tsx` (new) | T4 |
| Tokens, fonts, base | `web/src/app/styles/tokens.css`, `fonts.css`, `base.css` (new), `web/src/app/styles/tokens.test.ts` (new), `web/app.html` (preloads), `web/src/app/main.tsx` (imports) | T5 |

Order: T1 → T2 → T3 → T4 → T5. Sonnet implements test-first, Opus reviews each task, Fable reviews the branch.

---

### Task 1: `$lib/store` — the `svelte/store` contract without Svelte

Spec §6.1. Review focus 1, 2 and 3.

**Files:**
- Create: `web/src/lib/store.ts`
- Test: `web/src/lib/store.test.ts` (pure; no DOM)

**Interfaces:**
- Produces: `type Subscriber<T> = (value: T) => void`; `type Unsubscriber = () => void`; `type Updater<T> = (value: T) => T`; `type StartStopNotifier<T> = (set: (value: T) => void, update: (fn: Updater<T>) => void) => void | (() => void)`; `interface Readable<T> { subscribe(run: Subscriber<T>, invalidate?: () => void): Unsubscriber }`; `interface Writable<T> extends Readable<T> { set(value: T): void; update(fn: Updater<T>): void }`; `function writable<T>(value?: T, start?: StartStopNotifier<T>): Writable<T>`; `function readable<T>(value?: T, start?: StartStopNotifier<T>): Readable<T>`; `type Stores = Readable<unknown> | [Readable<unknown>, ...Readable<unknown>[]] | Readable<unknown>[]`; `type StoresValues<S>` (the value for a single store, the tuple of values for an array); `function derived<S extends Stores, T>(stores: S, fn: (values: StoresValues<S>, set: (value: T) => void, update: (fn: Updater<T>) => void) => Unsubscriber | void, initial?: T): Readable<T>` and the overload `derived<S extends Stores, T>(stores: S, fn: (values: StoresValues<S>) => T, initial?: T): Readable<T>` (the form is chosen by `fn.length < 2`, as Svelte does); `function get<T>(store: Readable<T>): T`; `function safeNotEqual(a: unknown, b: unknown): boolean` (exported for the test; Svelte's rule: `NaN` equals `NaN`, otherwise `!==`, and any object or function counts as changed).
- The exported names and type parameters match `svelte/store` one-to-one so that T2 is an import-path change only.

- [ ] **Step 1: Write the failing tests** in `store.test.ts`:
  - `describe('writable')`: `subscribe delivers the current value synchronously and returns an unsubscriber`; `set notifies every subscriber in subscription order`; `set with an equal primitive does not notify, NaN counts as equal` (focus 1); `set with the same object reference notifies, and update(v => v) on an object notifies` (focus 1); `update passes the current value and sets the result`; `start runs on the first subscriber only, stop on the last unsubscribe, and a set inside start is the first delivered value`; `start receives set and update`; `set from inside a subscriber is delivered after the current round to all subscribers in order` (focus 2: two subscribers, the first sets a new value when it sees `1`; both see `1` then both see `2`); `a subscriber added during a round does not get the in-flight value twice`; `invalidate, when given, is called before run on each set`; `unsubscribing twice is harmless`.
  - `describe('readable')`: `has no set or update`; `start/stop lifecycle like writable`; `a readable without start keeps its initial value`.
  - `describe('derived')`: `single source, sync form, maps values and tracks updates`; `multiple sources deliver a tuple and never call fn before every source has delivered` (focus 3: a source whose start sets asynchronously; assert `fn` is not called with `undefined` for it); `set form: fn receives set and update, may return a cleanup that runs before the next fn and on stop`; `initial value is used until set is called in the set form`; `derived equality: an unchanged primitive result does not notify, an object result does`; `unsubscribing the last subscriber unsubscribes from every source` (focus 3: count source stop calls); `get on a derived subscribes, reads and unsubscribes the sources`.
  - `describe('get')`: `returns the current value of a store with a start notifier and leaves it stopped afterwards`.
  - `describe('safeNotEqual')`: table — `[1, 1] → false`, `[NaN, NaN] → false`, `['a', 'b'] → true`, `[obj, obj] → true`, `[fn, fn] → true`, `[null, undefined] → true`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/lib/store.test.ts` — FAIL: `Failed to resolve import "./store"`.
- [ ] **Step 3: Implement** `store.ts` (no imports; module comment states it mirrors `svelte/store` and why).
- [ ] **Step 4: Run** — pass; `npm run check` — 0 errors.
- [ ] **Step 5: Commit** `feat(web): add $lib/store, a dependency-free svelte/store contract` (why: the controllers must outlive Svelte, and React will read them through one hook; keeping the exact notification rules is what makes the switch behaviour-neutral).

---

### Task 2: Switch every `svelte/store` import to `$lib/store`

Spec §6.1 ("lands first, in the Svelte app, with no behaviour change, and the existing tests prove it"). Divergences 2 and 3.

**Files:**
- Modify: the 21 lib modules, the 40 `*.test.ts` files and `web/src/lib/stacks/AddServicePanel.svelte` listed in the file structure (one import line each; `get`, `writable`, `readable`, `derived`, `type Readable`, `type Writable` keep their names); `web/src/guards.test.ts`
- Nothing else changes: no logic, no formatting churn.

**Interfaces:**
- Consumes (T1): the `$lib/store` exports. Relative imports (`'./store'`, `'../store'`) are acceptable where a file already uses relative lib imports; the alias `$lib/store` is preferred.
- Produces: `guards.test.ts` gains `it('imports no svelte/store anywhere in src/')` scanning `.ts`, `.tsx` and `.svelte` under `web/src` for the string `'svelte/store'` (detector `findSvelteStoreImports(files): string[]` tested on an inline fixture like the existing detectors).

- [ ] **Step 1: Write the failing test** in `guards.test.ts`: the detector unit test (fixture with one `.ts` importing `svelte/store`, one importing `$lib/store`, one `.svelte` importing `svelte/store` → the first and third are flagged) and the repository test `has no svelte/store import in src/`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/guards.test.ts` — FAIL: the repository test lists 62 files.
- [ ] **Step 3: Implement** the switch (a scripted `sed`/`perl` over the listed files is fine; review the diff is import lines only).
- [ ] **Step 4: Run** `make ui-test` (all 79 test files green, same count of tests as before the task — note the number in the PR), `make ui-check` (0 errors), `make ui` (build and budget pass; entry size printed, expected unchanged within noise since the new module is smaller than the removed `svelte/store` import).
- [ ] **Step 5: Commit** `refactor(web): read stores through $lib/store instead of svelte/store` (why: the only Svelte import in the TypeScript layer is gone, so the same controllers can feed both UIs during the migration; the unchanged suite is the proof).

---

### Task 3: React scaffold beside Svelte

Spec §6.2, §5 (budgets, pinned versions), §4 (handoff folder never referenced). Review focus 4. Divergence 4.

**Files:**
- Modify: `web/package.json` (dependencies `react`, `react-dom`, `react-router`; devDependencies `@vitejs/plugin-react`, `@testing-library/react`, `@types/react`, `@types/react-dom`, exact versions from the Tech Stack line; scripts `dev:app`, `build:app`, `check:app`, and `check` chained), `web/package-lock.json` (via `npm install --save-exact`), `web/vite.config.ts` (`test.include` adds `src/**/*.test.tsx`; the automatic JSX runtime for `.tsx` under Vitest — Vite 8's default transform already uses it, so add the `oxc`/`esbuild` `jsx: 'automatic'` setting only if the smoke test proves it necessary), `web/tsconfig.json` (unchanged apart from excluding `src/app/**` so svelte-check does not double-report; `include` stays Svelte-centred), `web/src/vitest-setup.ts` (the `afterEach` calls `cleanup` from `@testing-library/svelte` **and** from `@testing-library/react`), `web/src/guards.test.ts`, `.gitignore` (root; add `web/dist-app/`), `docs/development.md` (one paragraph under the dev-server section: `npm run dev:app` serves the React UI at `app.html`, `npm run build:app` builds and budget-checks it into `web/dist-app/`, which is not embedded until the cutover)
- Create: `web/app.html`, `web/vite.app.config.ts`, `web/tsconfig.app.json`, `web/src/app/main.tsx`, `web/src/app/App.tsx` (placeholder: one `<main>` with `<h1>Stacks</h1>`, replaced in Plan 16b), `web/src/app/App.test.tsx`, `web/src/app/html.test.ts`

**Interfaces:**
- `web/app.html`: same head as `index.html` (`<script src="/theme.js">` **first**, charset, viewport, the three font preloads, favicon, `<title>Stackstead</title>`), body `<div id="app"></div>` and `<script type="module" src="/src/app/main.tsx">`.
- `vite.app.config.ts` (`defineConfig` from `vite`, not `vitest/config`; tests stay on `vite.config.ts`): `plugins: [react(), appHtmlFallback()]` where `appHtmlFallback(): Plugin` is a dev-only `configureServer` middleware that rewrites navigation requests (method GET, `Accept` includes `text/html`, path not starting with `/api`, `/metrics`, `/@`, `/src`, `/node_modules`, and without a file extension) to `/app.html`; `resolve.alias.$lib` as in `vite.config.ts`; `build: { outDir: 'dist-app', emptyOutDir: true, target: 'es2022', manifest: true, rollupOptions: { input: 'app.html' } }`; `server.proxy` identical to `vite.config.ts` (`/api`, `/metrics` → `http://127.0.0.1:8420`).
- Scripts: `dev:app` = `vite -c vite.app.config.ts`; `build:app` = `vite build -c vite.app.config.ts && node scripts/check-bundle.mjs dist-app`; `check:app` = `tsc --noEmit -p tsconfig.app.json`; `check` = `svelte-check --tsconfig ./tsconfig.json && npm run check:app`.
- `tsconfig.app.json`: `extends ./tsconfig.json`; `compilerOptions: { jsx: 'react-jsx', types: ['vite/client', 'node'] }`; `include: ['src/app/**/*', 'src/lib/**/*.ts', 'src/vitest-setup.ts', 'src/vite-env.d.ts', 'vite.app.config.ts']`; `exclude: ['src/lib/**/*.test.ts']` (those mount Svelte components). `vite/client` supplies the `*.module.css` module types used from Plan 16b on.
- `main.tsx`: imports `./styles/fonts.css`, `./styles/tokens.css`, `./styles/base.css` (created in T5; until then import nothing), calls `initAppearance()` from `$lib/stores/appearance`, then `createRoot(document.getElementById('app')).render(<StrictMode><App /></StrictMode>)`; throws `missing #app mount point` like `main.ts`.
- `guards.test.ts` additions: `findRawHtml` extended to flag `dangerouslySetInnerHTML` in `.tsx`/`.ts` (detector fixture: a `.tsx` with the prop is flagged, one without is not); `findHandoffReferences(files): string[]` flagging any source containing `design_handoff_stackstead_react`; `sourceFiles` scans `.tsx` too; repository tests `has no dangerouslySetInnerHTML in src/` and `never references the design handoff folder`.
- `html.test.ts` (reads `web/app.html` and `web/index.html` from `process.cwd()`): both files load `/theme.js` as the **first** element of `<head>` (before any stylesheet or module), both preload the same three woff2 files, `app.html` mounts `#app` from `/src/app/main.tsx`, and neither contains `googleapis`.
- `App.test.tsx` (smoke, replaced by the real suite in 16b): renders `<App />` with `@testing-library/react`, finds `heading level 1 'Stacks'`, and asserts exactly one `main` landmark. It proves TSX transform, JSX runtime, RTL cleanup and `tsconfig.app.json` together.

- [ ] **Step 1: Write the failing tests** — `App.test.tsx`, `html.test.ts`, the `guards.test.ts` additions.
- [ ] **Step 2: Run** `cd web && npx vitest run src/app src/guards.test.ts` — FAIL: `App.test.tsx` is not collected (include pattern) or cannot resolve `react`; `html.test.ts` fails on the missing `app.html`.
- [ ] **Step 3: Implement**: `npm install --save-exact` the seven packages; the configs, scripts, `app.html`, `main.tsx`, placeholder `App.tsx`, `.gitignore`, the doc paragraph.
- [ ] **Step 4: Run** `make ui-test` (the new tests pass; the count grows only by the new tests), `make ui-check` (svelte-check 0 errors, then `tsc` 0 errors), `make ui` (Svelte build and budget unchanged — focus 4), `npm run build:app` (prints `bundle: entry … kB gz (budget 120.0)` for `dist-app`; passes), `git status --porcelain | grep dist-app` (empty), `npm run dev:app` manually: `/`, `/stacks/web` and `/settings` all serve `app.html`; `/api/status` is proxied (401 without a session is the expected answer).
- [ ] **Step 5: Commit** `feat(web): scaffold the React app beside Svelte with its own entry, config and budget check` (why: React can be built, tested and budget-checked from the first commit while the Go side and the shipped UI stay untouched).

---

### Task 4: `useStore`

Spec §6.1 ("React reads these stores through one hook, `useStore(readable)`, built on `useSyncExternalStore`").

**Files:**
- Create: `web/src/app/hooks/useStore.ts`, `web/src/app/hooks/useStore.test.tsx`

**Interfaces:**
- Consumes (T1): `Readable<T>`, `get`.
- Produces: `function useStore<T>(store: Readable<T>): T` — subscribes through `useSyncExternalStore(subscribe, getSnapshot, getSnapshot)` where `subscribe` adapts `store.subscribe` (ignoring the initial synchronous call, calling React's `onStoreChange` on later ones) and `getSnapshot` returns the latest value delivered by the store (a per-store cache kept in a `WeakMap<Readable, { value }>` or a `useRef`, so `getSnapshot` is stable and never reads through a fresh subscription on every render); a store whose identity changes between renders resubscribes.
- Rule for callers (documented in the module comment): the value returned must be treated as immutable; stores that `update` with a new object (all of ours) re-render, a store that `set`s the same object re-renders too (focus 1 of T1 applies).

- [ ] **Step 1: Write the failing tests** (`render` + `act` from `@testing-library/react`; a fixture component that renders the store value as text):
  - `renders the current value on first render without a flash of undefined` (store with a start notifier that sets synchronously);
  - `re-renders on set and not on an equal primitive set` (count renders with a spy);
  - `unsubscribes on unmount` (a writable with a stop spy; stop is called after unmount);
  - `switching to another store instance resubscribes` (rerender with a second store; the first store's stop was called, the text shows the second value);
  - `two components on one store share one subscription at a time and both update` (subscriber count on a writable with a start spy stays 1 while both are mounted);
  - `works with derived and with get-only readers` (a derived of two writables; update one; text follows);
  - `is safe under StrictMode double-invocation` (render inside `StrictMode`; after settle the store has exactly one subscriber).
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/hooks` — FAIL: `Failed to resolve import "./useStore"`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — pass; `npm run check:app` — 0 errors.
- [ ] **Step 5: Commit** `feat(web): useStore hook binding $lib/store readables to React` (why: one binding layer for every existing controller means the live stream, debounce and guard logic is reused, not re-implemented).

---

### Task 5: Tokens, fonts, base styles and reduced motion

Spec §6.3; README "Design tokens", "Motion". Review focus 5. Divergences 5 and 6.

**Files:**
- Create: `web/src/app/styles/tokens.css`, `web/src/app/styles/fonts.css`, `web/src/app/styles/base.css`, `web/src/app/styles/tokens.test.ts`
- Modify: `web/src/app/main.tsx` (import the three stylesheets, fonts first), `web/app.html` (nothing further; preloads were added in T3)

**Interfaces:**
- `tokens.css` structure mirrors `app.css` so the ported parser works unchanged: a `:root {` block (light, `color-scheme: light`), `:root[data-density='comfortable'] {`, `@media (prefers-color-scheme: dark) { :root:not([data-theme='light']) { … } }`, and `:root[data-theme='dark'] {` with the **same body** as the media block. Non-colour tokens in `:root` only: `--sans`, `--mono` (today's stacks), `--pad: 6px`, `--row: 36px` (comfortable: 10px / 44px), `--radius-led: 2px`, `--radius-sm: 4px`, `--radius-xs: 6px`, `--radius: 7px`, `--radius-md: 9px`, `--radius-lg: 12px`, `--radius-pill: 999px`, `--space-1 … --space-7` (today's rem values), `--overlay` (palette/dialog backdrop; see table), `--sidebar-w: 232px`, `--topbar-h: 48px`, `--mobile-bar-h: 52px`. The 760px mobile breakpoint is deliberately **not** a token: media queries cannot read custom properties, so it is written literally as `max-width: 759px` wherever needed and pinned by the shell tests in 16b.
- Colour tokens (data; light / dark):

| token | light | dark |
|---|---|---|
| `--bg` | `#f3f5f7` | `#0f1216` |
| `--chassis` | `#e9edf1` | `#0b0e12` |
| `--surface` | `#ffffff` | `#151a20` |
| `--raised` | `#f7f9fa` | `#1c2129` |
| `--hover` | `#eef1f4` | `#1f2530` |
| `--border` | `#dfe4ea` | `#262d36` |
| `--border-strong` | `#c9d1da` | `#3a4350` |
| `--border-input` | `#78828f` | `#5b6573` |
| `--text` | `#161a20` | `#e6eaf0` |
| `--muted` | `#5f6b7a` | `#8b95a5` |
| `--faint` | `#626c7a` | `#848d9c` |
| `--accent`, `--success` | `#047857` | `#34d399` |
| `--accent-ink` | `#ffffff` | `#062a1c` |
| `--accent-soft` | `rgba(4, 120, 87, 0.12)` | `rgba(52, 211, 153, 0.14)` |
| `--warning` | `#9c4707` | `#fbbf24` |
| `--danger` | `#b91c1c` | `#f87171` |
| `--info` | `#1d4ed8` | `#60a5fa` |
| `--success-soft` | `rgba(4, 120, 87, 0.12)` | `rgba(52, 211, 153, 0.16)` |
| `--warning-soft` | `rgba(156, 71, 7, 0.12)` | `rgba(251, 191, 36, 0.16)` |
| `--danger-soft` | `rgba(185, 28, 28, 0.12)` | `rgba(248, 113, 113, 0.16)` |
| `--info-soft` | `rgba(29, 78, 216, 0.12)` | `rgba(96, 165, 250, 0.16)` |
| `--muted-soft` | `rgba(95, 107, 122, 0.12)` | `rgba(139, 149, 165, 0.14)` |
| `--hi` | `0 1px 2px rgba(16, 24, 40, 0.05)` | `inset 0 1px 0 rgba(255, 255, 255, 0.035)` |
| `--shadow` | `0 1px 2px rgba(16, 24, 40, 0.06)` | `0 1px 0 rgba(0, 0, 0, 0.4)` |
| `--shadow-pop` | `0 16px 48px rgba(16, 24, 40, 0.18)` | `0 16px 48px rgba(0, 0, 0, 0.5)` |
| `--overlay` | `rgba(16, 24, 40, 0.35)` | `rgba(5, 8, 12, 0.55)` |
| `--series-1 … -4` | `#60a5fa #f97316 #a78bfa #14b8a6` | same |
| `--series-5 … -8` | `#be185d #6d28d9 #0f766e #be123c` | `#ec4899 #8b5cf6 #14b8a6 #f43f5e` |
| `--kind-images`, `--kind-volumes`, `--kind-layers`, `--kind-cache` | `#60a5fa #a78bfa #14b8a6 #f97316` | same |

- `fonts.css`: the five `@font-face` rules from `app.css` (IBM Plex Sans 400/500/600, Geist Mono 400/500, `font-display: swap`, the same `unicode-range`), `src: url('../../assets/fonts/<file>') format('woff2')`.
- `base.css`: `*, ::before, ::after { box-sizing }`, `html { -webkit-text-size-adjust: 100% }`, `body { margin 0; background var(--bg); color var(--text); font: 13px/1.45 var(--sans); font-variant-numeric: tabular-nums }`, `h1 { 24px/1.15, 600, letter-spacing -0.015em; 20px below 760px }`, `h2 { 14px/600 }`, zeroed margins on headings/p/lists/figure, form controls inherit font and colour, `img, svg { max-width: 100% }`, `code, pre, kbd { font-family: var(--mono) }`, `:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px }`, `.visually-hidden` (the same rule as `app.css`), `a { color: inherit; text-decoration: none }`, and the global reduced-motion rule `@media (prefers-reduced-motion: reduce) { *, ::before, ::after { animation: none !important; transition: none !important; scroll-behavior: auto !important } }`. Keyframes are **not** global: each CSS module declares the keyframes it uses (CSS Modules scope animation names), which is why the reduced-motion rule is the global `*` form.
- `tokens.test.ts` (ported from `web/src/tokens.test.ts`, reading `src/app/styles/tokens.css`): the same `block`/`parseTokens`/contrast helpers; `old token names are gone` scans `src/app/**` only; `light and dark blocks define the same token names` with the colour-name regex extended by `chassis`, `hi`, `overlay` and `kind-(images|volumes|layers|cache)` and the expected length updated to 39 (today's 32 plus those seven); the non-colour list extended with `--radius-xs`, `--radius-led`, `--overlay`, `--sidebar-w`, `--topbar-h`, `--mobile-bar-h`; `media-query dark block matches the explicit dark block`; the AA matrix as today plus `--text on --chassis >= 4.5`, `--muted on --chassis >= 4.5`, `--faint on --chassis >= 4.5`, `border-input vs chassis >= 3`, and the soft-fill pairs with the new 0.16 alphas; `no literal colours outside tokens.css` scanning `src/app/**/*.css` (module files and `base.css`) for `#[0-9a-f]{3,8}\b`, `rgb(`, `rgba(`, `hsl(` — the only allowed file is `tokens.css`; `fonts.css declares exactly the five faces and no remote url` (every `url(` is relative, none contains `http`).
- `web/src/theme.test.ts` stays as is (it tests `public/theme.js`, shared by both entries); `html.test.ts` from T3 already pins `app.html`'s pre-paint order.

- [ ] **Step 1: Write the failing test** `src/app/styles/tokens.test.ts` as described.
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/styles` — FAIL: `ENOENT … tokens.css`.
- [ ] **Step 3: Implement** the three stylesheets and the `main.tsx` imports.
- [ ] **Step 4: Run** `make ui-test` (all green), `make ui-check`, `npm run build:app` (fonts `… kB (budget 200.0)` equals the Svelte build's font figure, since the same five files are emitted; entry still under budget), `npm run dev:app` manually in a browser with `?theme=light` and `?theme=dark`: no flash of the wrong theme, the fonts load from `/src/assets/fonts/`, `prefers-reduced-motion` in devtools leaves no running animation (there are none yet; the rule is in place for 16b).
- [ ] **Step 5: Commit** `feat(web): Evolve v2 design tokens, fonts and base styles for the React app` (why: every later screen plan builds on these names and values; pinning parity and contrast now prevents each screen from re-deciding them).

---

## Self-review notes

- Spec coverage: §6.1 store contract T1, import switch T2 (router.ts included per divergence 2), `useStore` T4; §6.2 second entry, second config, scripts, test include, coexistence T3; §6.3 tokens, `data-theme`, `theme.js`, reduced motion T5; §6.4 `.test.tsx` pipeline T3/T4; §5 pinned versions and budgets T3 (`guards.test.ts`, `build:app`), fonts T5; §4 handoff-folder and budget anti-goals T3; §9 row 16 items 1–4 = this plan, 5–7 = Plan 16b.
- No code bodies: signatures, props, data table, test names and assertions only.
- Type consistency: `Readable`/`Writable`/`Unsubscriber`/`StartStopNotifier` (T1) are what T4's `useStore<T>(store: Readable<T>)` consumes and what 16b imports; `tsconfig.app.json` (T3) is what `check:app` and 16b's `.tsx` compile against; the token names in the T5 table are the names 16b's CSS modules use.
- Review focus: each of the five is pinned by a named test in its owning task (1–3 → `store.test.ts`, 4 → T3's verification steps and `html.test.ts`/`guards.test.ts`, 5 → `tokens.test.ts`).
- Proportion: T2 is large in file count but one line per file; T1 and T5 carry the risk and have the densest tests.
