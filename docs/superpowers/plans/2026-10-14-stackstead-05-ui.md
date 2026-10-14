# Stackstead Plan 5 — Web UI (Svelte) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** this plan specifies *what* to build: files, signatures, behaviour, test cases and acceptance criteria. It deliberately contains no full implementations; implementers write the code test-first. Short snippets show shapes only.

**Goal:** A user can do everything the v1 spec promises from a browser: first-run setup and login, see every stack with live status, create a stack from a Dockerfile or image through forms, register an existing compose file, edit services through forms (with a diff preview) or the raw YAML editor, run lifecycle actions and watch their output, read live logs, look at metrics history with live updates, and manage the Prometheus token — all from the single static binary.

**Architecture:**
- `web/` holds a Svelte 5 + Vite single-page app written in TypeScript. No SvelteKit, no router dependency, no state library: a ~100-line History-API router and Svelte stores.
- Three transport primitives carry every screen: a `fetch` wrapper that speaks the backend's JSON/error contract, an in-house SSE client built on `fetch` + `ReadableStream` (so it sees `: ping` comments and can resend `Last-Event-ID`), and a `live` store that multiplexes `/api/events` into status flags and "project changed" notifications.
- Heavy widgets (CodeMirror for YAML, uPlot for charts) are dynamically imported so the stack list paints with a small bundle.
- The Go side changes minimally: `/assets/*` 404s, cache and security headers, an embed that tolerates an unbuilt UI, four small API additions (form-edit preview, metrics-token status, exact log-stream resume via event ids, password change), a Makefile that builds the UI before the Go binary, and an `e2e` smoke test against the real binary.

**Tech Stack:** Svelte 5, Vite, TypeScript, Vitest + @testing-library/svelte (jsdom), CodeMirror 6 (`@codemirror/*` + `@codemirror/merge` for diffs), uPlot. Go 1.26 stdlib only on the backend (no new Go modules). Node 22 LTS pinned.

**Spec:** `docs/superpowers/specs/2026-10-14-stackstead-v1-design.md`:
- §1 success criteria (Dockerfile → running container with static IP through the UI, no YAML)
- §2 scope (SPA, single admin; Playwright E2E explicitly out)
- §3 UI decision (Svelte 5 + Vite, `go:embed`)
- §5.1–5.6 data flows (file browser, pre-write validation, form/raw edit, Apply, hash conflicts, live status without polling, logs, metrics history + live)
- §6 error handling (Docker unavailable banner, compose missing disables lifecycle, unparseable YAML → raw-only mode, structured field errors, compose stderr verbatim, metrics warning)
- §7 security, §8 testing (Vitest for non-trivial logic)

**Additions beyond the v1 spec, decided by the user on 2026-10-14:** a password-change endpoint and form (the spec has a single admin account but no way to change its password), `GET /api/settings/metrics-token` so the Settings page can show whether a token is configured, and `id:` on log SSE events so a reconnect resumes exactly. CI configuration is deferred (not part of this plan).

### Plan series (this is Plan 5 of 5)

1. Foundation — merged. 2. composefile — merged. 3. Stacks & lifecycle — merged. 4. Observability — on `feat/observability` (Task 7 text is the API contract this plan codes against). 5. **Web UI (this plan).** Deferred items from earlier plans that land here: `/assets/*` must 404 instead of falling back to `index.html` (Plan 1); diff preview before saving compose edits, routing `ErrEditRefused` (422 "this change needs the raw YAML editor") to the raw editor, read-only fields in forms, 409 conflict → reload prompt (Plans 2/3).

## Decisions (controller, recorded for the user)

The global rules ask for TypeSafe-scored design decisions. The TypeSafe skill is an SDK for an external judgment API and cannot be run here without network access and credentials, so these decisions are argued in writing instead.

- **Plain Svelte 5 + Vite, not SvelteKit (static adapter).** The app has eight routes, no SSR, no server endpoints and one HTML entry; SvelteKit would add `@sveltejs/kit`, `adapter-static`, its own router/loader conventions, prerender config and ~10 transitive packages for nothing we use. A ~100-line in-house History-API router is tested in isolation and has no supply-chain surface. The Go `spaHandler` already serves `index.html` for unknown paths, which is all a client router needs.
- **Dependency policy.** Runtime deps: `svelte`, `@codemirror/{state,view,commands,language,lang-yaml,merge}`, `uplot`. Dev deps: `vite`, `@sveltejs/vite-plugin-svelte`, `typescript`, `svelte-check`, `vitest`, `jsdom`, `@testing-library/svelte`, `@testing-library/user-event`. Nothing else (no router, no store library, no chart wrapper, no diff library, no HTTP client, no msw). Exact versions (`.npmrc` has `save-exact=true` and `ignore-scripts=true`; esbuild works without its postinstall because its platform binary comes from an optional dependency), `package-lock.json` committed, `npm ci` everywhere. New packages after this plan need a reason in the commit message.
- **Diff preview uses `@codemirror/merge`** (same maintainer as the editor we already pull in) rather than a separate diff library or an in-house LCS.
- **Charts use uPlot** (single package, no deps, ~15 KB gz, built for time series with live appends) rather than Chart.js or an in-house SVG renderer. CPU, memory, network and block I/O get one chart each.
- **Embedding without requiring Node for Go work:** `web/embed.go` embeds `all:dist`. `web/dist/` is gitignored except for its own `.gitignore`, so an un-built tree still compiles (the directory is never empty). `web.Dist()` returns the built app when `dist/index.html` exists and otherwise an embedded placeholder page (`web/placeholder/index.html`, committed) that says the UI was not built; `web.Built()` reports which. No build tags: `go build ./...`, `go test ./...` and cross-compiles work on a fresh clone with no Node, and a release build is simply `make ui` then `go build`. The committed `web/dist/index.html` placeholder from Plan 1 is removed (it would otherwise be overwritten by the build and show up as modified).
- **The Makefile is the build entry point:** `make build` = `make ui` + `CGO_ENABLED=0 go build`; `make release` builds linux/amd64 and linux/arm64; `make e2e` runs the smoke. **CI is deferred** (user decision): no workflow files in this plan; the Makefile targets are written so a later CI can call `make test` without Node and `make build` with it, and `make e2e` once the UI is built. The binary logs a warning at startup when the UI is not built.
- **Form-edit diff preview needs one new endpoint.** Structured edits are applied server-side (`composefile.UpdateService`), so the browser cannot compute the candidate text. `POST /api/stacks/{name}/services/{service}/preview` returns the candidate file bytes without validating with compose or writing. It reuses the same load/hash-check/parse/edit steps as `UpdateService` so the preview is exactly what the save would write (minus project re-qualification, which does not change bytes).
- **Metrics-token status endpoint.** `GET /api/settings/metrics-token` → `{configured: bool}` (only whether a hash is stored; never the hash). Settings shows "Create token" or "Rotate / Disable" accordingly.
- **Exact log-stream resume via per-container cursors.** Docker log lines carry nanosecond timestamps and the logs API accepts `since` with nanosecond precision (`<sec>.<nsec>`, inclusive). Each `log` SSE event gets `id: <container id, 12 hex>:<unix nanoseconds>:<ordinal>`, where `ordinal` is how many lines with that exact timestamp this container has delivered so far on this stream (1 for the first). The client keeps the latest cursor per container and on reconnect sends them all, comma-separated, as `Last-Event-ID`. For each container with a cursor the server asks Docker for `since=<ts>` with `tail=all` and skips the first `ordinal` lines whose timestamp equals `<ts>`; containers without a cursor (new replicas, malformed entries) start with the normal `tail`. Residual limits, which are Docker's: lines evicted by log rotation (`max-size`) cannot be replayed; a recreated container has a new id and starts from `tail`; drivers with coarser clocks (journald, µs) only make the ordinal do more work. This was chosen over a stream-local sequence (which cannot survive a new HTTP request) and over a `since=<last time>` resume (which repeats or skips lines sharing a timestamp).
- **Password change.** `POST /api/password` (flat naming like `/api/login`, `/api/logout`, `/api/me`) with `{current, new}`. The current password is verified with the same argon2 slot-bounded path as login, the new one must pass `MinPasswordLength`, and wrong attempts count against the existing per-IP `LoginLimiter` (429 after 5 in 15 min). On success **every other session of the user is deleted and the current one is kept**: changing the password is the remedy for a suspected cookie leak, so other sessions must die, while logging the user out of the browser they are using would be confusing and gives no extra safety (they just proved possession of the new password). A wrong current password is `403` (not `401`, which the client treats as "session gone").
- **SSE over `fetch`, not `EventSource`.** `EventSource` hides comment lines, so the backend's `: ping` heartbeat is invisible to it and a half-open TCP connection is never detected; it also cannot add headers or be told to stop retrying on 401. The in-house client parses the stream, times out when nothing (events or comments) arrives for 45 s (3× the 15 s server heartbeat), reconnects with jittered backoff resending `Last-Event-ID`, stops on 401/403/404, and aborts when the component unmounts.
- **Live status without polling:** `/api/events` drives everything. `change` → refetch the list (debounced) and the open stack if its project matches; `status` → banners; reconnect → full refetch. `/api/status` (docker/compose/version) is fetched at boot, after every live reconnect, after a 503, and when the `events` flag flips — never on a timer.
- **E2E smoke without Playwright** (spec §2 keeps Playwright out of v1): a Go test behind `//go:build e2e` builds and starts the real binary with the built UI and checks the serving contract over HTTP (index, hashed assets, 404s, headers, deep links, setup + login round-trip). When `STACKSTEAD_E2E_CHROME` names a Chrome binary it also renders the app with `chrome --headless=new --dump-dom` and asserts the login form is in the DOM. The Docker lifecycle part (create, start, observe, remove a throwaway stack) is optional and skips when Docker is missing; the app then renders its degraded banners.
- **Security headers from Go:** `index.html` responses carry a CSP (`default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`), `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`. `'unsafe-inline'` for styles is needed by CodeMirror's `style-mod`; scripts stay external.
- **CSRF model (unchanged backend):** session cookie is `SameSite=Strict` + `HttpOnly`; every mutating JSON route requires `Content-Type: application/json` (blocks HTML form posts); `DELETE` routes trigger a CORS preflight the server never answers. The client always sends `credentials: 'same-origin'`, never a `*/*` wildcard origin, and sends `{}` for body-less POSTs. No token endpoint is needed.
- **No `{@html}` anywhere.** Log lines, compose errors and operation output are rendered as text nodes. A unit test greps `src/` and fails on `{@html`.

## Global Constraints

- Spec §3: "Svelte 5 + Vite SPA, embedded via `go:embed`". Spec §1: "fast and light" — JS shipped to the stack list (entry + shared chunks) ≤ 120 KB gzipped; CodeMirror and uPlot are route-level dynamic imports; total ≤ 400 KB gzipped. The Vite build prints sizes; T22 asserts the budget.
- No new Go module dependencies. `CGO_ENABLED=0 GOOS=linux GOARCH={amd64,arm64} go build ./...` must pass with and without a built UI.
- `go test ./...` must pass on a machine without Node. Nothing under `internal/` or `cmd/` reads `web/dist` at test time except the `e2e`-tagged smoke.
- Node 22 LTS, pinned in `web/.nvmrc` and `package.json#engines`. Exact dependency versions, `npm ci`, `ignore-scripts=true`.
- Spec §5.3: every save sends the hash it loaded; a 409 means reload-and-retry, never overwrite. Spec §5.3: after a form save the user clicks **Apply** to run `up`.
- Spec §6: Docker unavailable → degraded banner, the UI stays usable; compose unavailable → lifecycle buttons disabled with a reason, read-only views work; unparseable YAML → raw editor only with the parse error shown; validation errors are shown next to their fields; compose stderr is shown verbatim (as text); metrics degraded → warning banner.
- Spec §7: the file browser only lists `allowed_roots`; the import path is free text (anywhere readable) with the browser as a helper.
- Spec §8: Vitest for non-trivial logic; no browser E2E suite. Every task is test-first: write the test, run it, see it fail for the expected reason, implement, pass, commit.
- Accessibility baseline: every input has a label; dialogs trap focus, restore it on close and close on Escape; tabs are `role=tablist` with arrow keys; status changes and toasts use `aria-live=polite`; colour is never the only status signal (badge text + icon); focus is visible; `prefers-reduced-motion` disables animations. Responsive baseline: usable at 375 px wide with a 16 px gutter and no horizontal scroll; tables collapse to cards or scroll inside their container.
- Security: no `{@html}`; untrusted text is only ever interpolated; the API client never follows redirects into other origins; token values are shown once and never stored.
- Commits: conventional, explain why, and end with exactly these two trailer lines:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01Th7wAiWALPK7jdrpSxLToT
  ```

## Review Focus

1. **Session expires or the user logs out while streams are open.** Every SSE client must stop (not retry forever against 401), the API client must send the user to `/login?next=…` exactly once, and the live store must not spawn a second `/api/events` connection after login. (T4 `TestUnauthorizedRoutesToLoginOnce`, T5 `TestStopsOn401`, T5 `TestStartLiveIsIdempotent`)
2. **A log flood (10 000 lines/s) must not freeze the tab.** Lines are coalesced per animation frame, the buffer caps at 5 000 lines, the DOM renders at most the buffer, and search filters the buffer not the DOM. (T7 `TestRingBufferCapsAndCountsDropped`, T7 `TestCoalesceFlushesOncePerFrame`, T17 `TestFloodRendersBoundedRows`)
3. **The file changed under the editor (hand edit, second tab).** A 409 on save must show the server's message, offer Reload, and after reload the editor shows the new content with the new hash and the user's draft is preserved in a diff so nothing is silently lost. The preview must be computed against the hash that will be sent. (T16 `TestConflictOffersReloadAndKeepsDraft`, T20 `TestPreviewAndSaveUseSameHash`)
4. **Live and history metrics must join cleanly.** No duplicate point at the seam, counter reset after a container restart gives rate 0 not a negative spike, per-service aggregation of replicas (gauges averaged, counter rates summed across replicas) matches the server's history semantics, and reconnect after `dropped` does not double-append. (T8 `TestJoinDropsOverlap`, T8 `TestCounterResetGivesZeroRate`, T8 `TestReplicaAggregationMatchesServer`, T18 `TestDroppedReconnectsWithoutDuplicates`)
5. **Status must converge after `/api/events` disconnects.** Changes missed while reconnecting are recovered by a full refetch on `open`; a `dropped` event reconnects immediately; the stack list never shows a stale "running" after Docker restarted. (T5 `TestReconnectEmitsResync`, T12 `TestResyncRefetchesList`)
6. **Stream resume must be exact.** After `dropped` or a network blip, the operation panel reconnects with `Last-Event-ID` and shows every line exactly once, including the "N earlier lines dropped" note, and ends on `done`; the logs viewer reconnects with every container's cursor and neither repeats nor skips lines that share a timestamp. (T15 `TestResumeSendsLastEventId`, T15 `TestNoDuplicateLinesAcrossReconnect`, T2b `TestResumeSkipsOrdinalAtSameTimestamp`, T17 `TestResumeSendsCompositeCursor`)
8. **Changing the password must not lock the user out or leave a stolen cookie alive.** The current session survives, every other session is gone immediately, wrong current passwords are rate-limited like logins, and the form never echoes either password. (T2c `TestChangePasswordKeepsCurrentSessionDropsOthers`, T2c `TestChangePasswordIsRateLimited`, T19 `TestPasswordFormClearsFieldsAndShowsResult`)
7. **An unbuilt or half-built UI must fail loudly, not silently.** `/assets/missing.js` returns 404 (never `index.html` as JavaScript, which produces a MIME error and a blank page), the placeholder page says the UI is not built, and the binary logs it. (T1 `TestAssetsMissingIs404`, T1 `TestDistFallsBackToPlaceholder`, T23 e2e)

---

## File Structure

```
web/
  package.json, package-lock.json, .npmrc, .nvmrc, tsconfig.json, vite.config.ts, svelte.config.js
  index.html                     Vite entry (<div id="app">, <script type="module" src="/src/main.ts">)
  public/favicon.svg
  embed.go                       go:embed all:dist + placeholder fallback; Dist(), Built()
  embed_test.go
  placeholder/index.html         served when the UI was not built
  dist/.gitignore                "*" + "!.gitignore" (keeps the dir, ignores builds)
  src/main.ts                    mounts App
  src/App.svelte                 route table, auth gate, layout (nav, banners, toasts, <main>)
  src/app.css                    design tokens, reset, layout utilities, dark scheme
  src/vitest-setup.ts            jest-dom-free matchers helpers, fake fetch/EventSource installers
  src/lib/api/types.ts           DTOs mirrored from the Go API (the shared contract)
  src/lib/api/client.ts          request(), ApiError, checksPartial, 401 handling
  src/lib/api/session.ts         session store: me/login/logout/setup
  src/lib/sse/client.ts          openSSE(), parser, heartbeat timeout, backoff, Last-Event-ID
  src/lib/sse/parse.ts           pure SSE line parser
  src/lib/stores/live.ts         /api/events multiplexer: status flags, changes, resync, /api/status
  src/lib/stores/toasts.ts       toast queue
  src/lib/router.ts              matchRoute(), route store, navigate(), link action
  src/lib/logs/buffer.ts         ring buffer, filter, rAF coalescer
  src/lib/metrics/series.ts      history → series, live aggregator, join, formatters, ranges
  src/lib/compose/form.ts        ServiceConfig ⇄ form model, client validation, server error mapping
  src/lib/format.ts              bytes, durations, relative time
  src/lib/components/            Banner, Toasts, Dialog, ConfirmDialog, Field, Button, StatusBadge,
                                 Tabs, ListEditor, CopyButton, Spinner, EmptyState, RelativeTime, Nav
  src/lib/stacks/FileBrowser.svelte, NetworkPicker.svelte, ServiceForm.svelte,
                 OperationPanel.svelte, ActionsBar.svelte, YamlEditor.svelte, DiffView.svelte,
                 ComposeEditor.svelte, ServiceEditor.svelte, LogsViewer.svelte, MetricsPanel.svelte,
                 MetricsChart.svelte
  src/routes/Login.svelte, Setup.svelte, StackList.svelte, StackCreate.svelte, StackImport.svelte,
             StackDetail.svelte, Settings.svelte
  (tests live next to their subject as *.test.ts)
internal/api/spa.go              /assets 404, cache + security headers (+ server_test.go changes)
internal/api/stacks.go           + preview route; stacks_dto.go + DTO; stacks_test.go
internal/stacks/service_edit.go  + PreviewService (+ service_test.go)
internal/api/observe.go          + log event ids / Last-Event-ID cursors, GET metrics-token status (+ observe_test.go)
internal/engine/stream.go        since with nanosecond precision (+ stream_test.go)
internal/auth/service.go         + ChangePassword (+ service_test.go)
internal/api/handlers_password.go  POST /api/password (+ password_test.go); server.go registers it
cmd/stackstead/main.go           warn when !web.Built()
cmd/stackstead/e2e_test.go       //go:build e2e — smoke against the real binary
Makefile, .gitignore (node_modules), README.md
```

## Execution waves

Tasks in one wave touch disjoint files and are run by parallel subagents in separate worktrees. Shared contracts (TS types, store and function signatures) are pinned in this plan so parallel tasks compile against the same names once merged; a task must not create a file another task owns.

| Wave | Tasks (parallel) | Depends on |
|---|---|---|
| 1 | T1 Go static serving + embed + Makefile, T2 Go preview endpoint, T2b Go log event ids + token status, T2c Go password change, T3 frontend scaffold + types + test harness | nothing (T1: `web/embed*.go`, `web/placeholder`, `web/dist/.gitignore`, `internal/api/spa.go`, `internal/api/server_test.go`, `cmd/stackstead/main.go`, Makefile, root `.gitignore`. T2: `internal/stacks/service_edit.go` + test, `internal/api/stacks*.go`. T2b: `internal/engine/stream.go` + test, `internal/api/observe.go` + `observe_test.go`. T2c: `internal/auth/service.go` + test, `internal/api/handlers_password.go`, `internal/api/password_test.go`, one route line in `internal/api/server.go`. T3: everything else under `web/` it creates. README is edited only by T23.) |
| 2 | T4 API client + session, T5 SSE client + live store, T6 router, T7 logs buffer, T8 metrics series, T9 shared components, T10 service form model | T3 (scaffold, `types.ts`, test setup) |
| 3 | T11 Login + Setup, T12 Stack list, T13 FileBrowser + NetworkPicker, T14 ServiceForm, T15 OperationPanel + ActionsBar, T16 YAML editor + diff + conflict, T17 Logs viewer, T18 Metrics charts, T19 Settings | wave 2 (each task lists which) |
| 4 | T20 Service edit flow (preview → save → apply), T21 Create + Import screens | T20: T2, T14, T15, T16. T21: T13, T14, T15 |
| 5 | T22 Stack detail + App wiring + nav + bundle budget | all of waves 3–4 |
| 6 | T23 E2E smoke, README, a11y/responsive audit | T22 |

Symbol ownership (never define these elsewhere): T3 owns `src/lib/api/types.ts` and `src/vitest-setup.ts`; T4 owns `ApiError`, `request`, `session`; T5 owns `openSSE`, `parseSSELines`, `live`; T6 owns `router`; T9 owns `toasts` (`src/lib/stores/toasts.ts`) and every file in `src/lib/components/`; T10 owns `src/lib/compose/form.ts`.

---

### Task 1: Static serving, embed fallback, Makefile

**Wave:** 1.

**Files:**
- Modify: `web/embed.go`; create `web/embed_test.go`, `web/placeholder/index.html`, `web/dist/.gitignore`; delete `web/dist/index.html` (Plan 1 placeholder).
- Modify: `internal/api/spa.go`, `internal/api/server_test.go` (`TestSPAFallbackAndAPINotFound` changes: `/assets/` is now 404).
- Modify: `cmd/stackstead/main.go` (startup warning), root `.gitignore` (`node_modules/`).
- Create: `Makefile`. (No CI workflow: CI is deferred.)

**Interfaces:**
```go
package web
func Dist() fs.FS    // built app when dist/index.html exists, else the placeholder FS
func Built() bool    // true when dist/index.html is embedded
```

**Behaviour:**
- `//go:embed all:dist` plus `//go:embed placeholder`. `Dist()` returns `fs.Sub(dist, "dist")` when `dist/index.html` is present, else `fs.Sub(placeholder, "placeholder")`. The placeholder page states that the UI was not built and names `make ui`.
- `spaHandler`:
  - `/assets/<anything>`: serve the file with `Cache-Control: public, max-age=31536000, immutable`; a missing file is a plain `404 Not Found` (text/plain), never `index.html`. `/assets/` and `/assets` themselves are 404.
  - any other path that is an existing regular file (`favicon.svg`): serve it with `Cache-Control: no-cache`.
  - everything else (`/`, `/stacks/x`, directories): `index.html` with `Cache-Control: no-cache` and the security headers from Decisions (CSP, `X-Content-Type-Options`, `Referrer-Policy`).
  - Path traversal is already blocked by `path.Clean` + `fs.FS`; keep it and add a test for `/assets/../index.html`.
- `main.go`: after building Options, `if !web.Built() { logger.Warn("web UI not built; serving placeholder page (run make ui)") }`.
- `Makefile` targets: `ui` (`cd web && npm ci && npm run build`), `ui-check` (`svelte-check`), `ui-test` (`vitest run`), `build` (`ui` then `CGO_ENABLED=0 go build -o bin/stackstead ./cmd/stackstead`), `release` (both `GOARCH`es into `bin/stackstead-linux-{amd64,arm64}`), `test` (`go test -race -timeout 300s ./...`, no Node needed), `check` (`go vet`, `gofmt -l` empty, `test`, `ui-check`, `ui-test`), `e2e` (`go test -tags e2e -timeout 600s ./cmd/stackstead/`; the smoke includes an optional Docker lifecycle, hence the 600 s Makefile timeout; needs a built UI, not Node itself), `clean`. `help` lists them. Use `.PHONY`. Targets are plain enough that a future CI can call them one per job; no workflow file is added.

**Tests (write first):**
- `web/embed_test.go`: `TestDistFallsBackToPlaceholder` — when `Built()` is false, `Dist()` has an `index.html` containing "not been built"; when true, it contains `<div id="app"` (the test checks whichever state the tree is in and asserts the matching content, so it passes with and without Node).
- `internal/api/server_test.go` (`fstest.MapFS` with `index.html`, `assets/app.js`, `favicon.svg`):
  - `TestAssetsMissingIs404`: `/assets/nope.js`, `/assets/`, `/assets` → 404 with a non-HTML body.
  - `TestAssetsAreImmutable`: `/assets/app.js` → 200, body `console.log(1)`, `Cache-Control` immutable.
  - `TestIndexHasSecurityHeaders`: `/` and `/stacks/web` → 200 `index.html`, `no-cache`, CSP contains `script-src 'self'`, `X-Content-Type-Options: nosniff`.
  - `TestRootFilesServedNoCache`: `/favicon.svg` → its bytes, `no-cache`.
  - `TestAssetTraversalIs404`: `/assets/../index.html` → not `index.html` body via the assets branch (either 404 or, after `path.Clean`, the SPA index with no-cache — assert it is not served with the immutable header).
- `Makefile`: no unit test; acceptance runs `make help`, `make test`, and `make build` on a tree without `web/node_modules` to confirm `ui` is the only Node-needing target.

**Acceptance:** `go build ./... && go vet ./... && test -z "$(gofmt -l .)" && go test -race -timeout 180s ./web/ ./internal/api/ ./cmd/...` green on a tree with no `web/dist` build; `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` green; `git status` shows `web/dist/` only as its `.gitignore`.

---

### Task 2: Form-edit preview endpoint (Go)

**Wave:** 1.

**Files:**
- Modify: `internal/stacks/service_edit.go`, `internal/stacks/service_test.go`.
- Modify: `internal/api/stacks.go` (interface + route + handler), `internal/api/stacks_dto.go` (`previewResponse`), `internal/api/stacks_test.go`. (README rows for all wave-1 endpoints are written by T23.)

**Interfaces:**
```go
// stacks
func (s *Service) PreviewService(ctx context.Context, stack, service string, cfg composefile.ServiceConfig, hash string) (candidate []byte, err error)
// api: StacksAPI gains PreviewService with the same signature
// POST /api/stacks/{name}/services/{service}/preview  body {"config": ServiceConfig, "hash": "..."}
//   200 {"content": "<candidate yaml>", "hash": "<hash the preview was computed against>"}
```

**Behaviour:**
- `PreviewService` takes the stack lock (`lockedEntry`), loads the file, checks the hash (`composefile.ErrConflict` on mismatch), parses, applies `UpdateService`, and returns `edited.Bytes()`. It does **not** call `checkChanges`, `requireCompose`, `validateCandidate`, or write anything; it must leave no temp file.
- Errors map exactly as the save does (404 unknown stack/service, 409 conflict/unavailable, 422 `ErrEditRefused` "this change needs the raw YAML editor" / `ReadOnlyFieldError` / invalid config).
- The returned `hash` echoes the request hash so the client can assert the save uses the same one.

**Tests (write first):**
- stacks: `TestPreviewReturnsCandidateWithoutWriting` — preview of a memory-limit change returns bytes containing the new limit; the file on disk and its mtime are unchanged; no `.stackstead-check-*` or `.bak` appears; the fake Compose recorded no `Config` call. `TestPreviewStaleHashConflicts`. `TestPreviewReadOnlyFieldIsRefused` (anchored env → `ErrEditRefused` or `*ReadOnlyFieldError`).
- api: route requires a session; 200 body shape; 409 and 422 mapping; bad JSON → 400.

**Acceptance:** `go test -race -timeout 180s ./internal/stacks/ ./internal/api/` green.

---

### Task 2b: Log event ids with exact resume, and metrics-token status (Go)

**Wave:** 1. File-disjoint from T1, T2 and T2c. The log handler is Plan 4 T7's `internal/api/observe.go` (merged before this plan starts); engine `Logs` already sniffs TTY framing.

**Files:**
- Modify: `internal/engine/stream.go`, `internal/engine/stream_test.go` (nanosecond `since`).
- Modify: `internal/api/observe.go`, `internal/api/observe_test.go` (cursors on `log` events, `Last-Event-ID` parsing, token status route).

**Interfaces:**
```go
// engine: LogOptions is unchanged; streamLogs encodes a non-zero Since as "<sec>.<nsec 9 digits>" (Docker parses fractional seconds; the filter is inclusive: lines with Time >= Since are returned).

// api (unexported helpers; names used by tests)
type logCursor struct { Container string /* 12-hex prefix */; TS int64 /* unix ns */; Ordinal int }
func (c logCursor) String() string                         // "<id12>:<ts>:<ordinal>"
func parseLogCursors(header string) map[string]logCursor   // comma-separated; malformed entries skipped; later duplicates win
// GET /api/settings/metrics-token  (session) → 200 {"configured": true|false}
```

**Behaviour:**
- **Engine:** `since` now carries nanoseconds. A `Since` with zero nanoseconds still encodes as `<sec>.000000000` (one format, easier to test). `Tail` semantics unchanged.
- **Log events:** every `log` event carries `id: <cursor>` for the container that produced it. Per container the handler keeps `(lastTS, ordinal)`: when a line's `Time.UnixNano()` equals `lastTS` the ordinal increments, otherwise it resets to 1. `error` and `end` events carry no id (nothing to resume there).
- **Resume:** `Last-Event-ID` is parsed with `parseLogCursors`. For a container whose 12-hex id prefix has a cursor: request `Since = time.Unix(0, ts)` and `Tail = -1` (never the user's tail, which would cut lines), ignore the user's `since`, and drop the first `ordinal` lines whose `UnixNano() == ts` (lines with a later timestamp pass straight through; a line with an *earlier* timestamp is impossible but, if seen, passes through too). Containers without a cursor use the request's `tail`/`since` as today. The ordinal counter for a resumed container starts from the cursor's values so the next ids continue the sequence.
- **Token status:** `handleMetricsTokenStatus` answers `{"configured": hash != ""}` using `Observe.TokenHash`; registered next to the POST/DELETE in `registerObserveRoutes`; `Cache-Control: no-store`.

**Tests (write first)** with the fake log source from Plan 4 T7's tests:
- engine: `TestLogsSinceHasNanoseconds` (the fake records the query string; `Since = time.Unix(1700000000, 123456789)` → `since=1700000000.123456789`).
- api:
  - `TestLogEventsCarryCursorIds`: three lines from one container, two sharing a timestamp → ids `c:ts1:1`, `c:ts2:1`, `c:ts2:2`.
  - `TestResumeSkipsOrdinalAtSameTimestamp`: `Last-Event-ID: c:ts2:1` → the fake is asked for `Since == ts2`, `Tail == -1`, and the client receives only the second `ts2` line and later ones; a second container without a cursor is asked with the request's `tail`.
  - `TestResumeIgnoresMalformedCursors`: `Last-Event-ID: garbage,c:ts:1` → only `c` resumes; `garbage` does not break the stream.
  - `TestResumeOrdinalContinues`: after resuming at `c:ts2:1`, the next line at `ts2` has id `c:ts2:2`.
  - `TestResumeNeverTruncatesWithTail`: a cursor plus `?tail=1` still requests `Tail -1` for that container.
  - `TestMetricsTokenStatus`: 401 without a session; `{"configured": false}` with no hash; `true` after a create; `false` after delete; `no-store` header.

**Acceptance:** `go test -race -timeout 240s ./internal/engine/ ./internal/api/` green; the existing logs tests keep passing unchanged except for added `id:` lines.

---

### Task 2c: Password change (Go)

**Wave:** 1. File-disjoint from T1, T2 and T2b (T1 edits `server_test.go`, so this task's API tests live in their own file).

**Files:**
- Modify: `internal/auth/service.go`, `internal/auth/service_test.go`.
- Create: `internal/api/handlers_password.go`, `internal/api/password_test.go`.
- Modify: `internal/api/server.go` (one line: `mux.Handle("POST /api/password", s.requireSession(s.handleChangePassword))`).

**Interfaces:**
```go
// auth
// ChangePassword verifies current for userID, stores the argon2id hash of next,
// and deletes every session of the user except keepToken's. Errors:
// ErrInvalidCredentials (wrong current), ErrWeakPassword (policy), ErrSamePassword.
var ErrSamePassword = errors.New("new password must differ from the current one")
func (s *Service) ChangePassword(ctx context.Context, userID int64, current, next, keepToken string) error

// api
// POST /api/password  (session)  body {"current": "...", "new": "..."}
//   204 on success; 400 ErrWeakPassword / ErrSamePassword / bad JSON (message from the error);
//   403 {"error": "current password is incorrect"}; 429 when the per-IP login limiter is exhausted.
```

**Behaviour:**
- `ChangePassword`: look up the hash by `userID`; `verify` under the hash slot (as `Login`); on mismatch return `ErrInvalidCredentials` (same timing as a wrong login); policy check (`MinPasswordLength`, and `next == current` → `ErrSamePassword`) **before** hashing the new password; hash; then in one transaction `UPDATE users SET password_hash = ?` and `DELETE FROM sessions WHERE user_id = ? AND token_hash != ?`. The store has one connection: the transaction only runs these two statements, no queries while open.
- Handler: `decodeJSON` (the request struct uses `json:"current"` and `json:"new"`); `Limiter.Attempt(ip)` first (429 when exhausted, same message as login); call `ChangePassword` with `userFrom(ctx).ID` and the session cookie value; on `ErrInvalidCredentials` → 403 (the limiter keeps the attempt); on success `Limiter.Reset(ip)` and 204. The response never includes either password; the handler never logs them.

**Tests (write first):**
- auth: `TestChangePasswordKeepsCurrentSessionDropsOthers` (login twice → two tokens; change with token A → `Authenticate(A)` ok, `Authenticate(B)` is `ErrInvalidSession`; `Login` with the old password fails, with the new one succeeds); wrong current → `ErrInvalidCredentials` and nothing changed (old password still logs in, both sessions alive); too short → `ErrWeakPassword` without touching the DB; same password → `ErrSamePassword`.
- api: 401 without a session; 204 happy path and the old cookie from a second login is then 401 on `/api/me` while the current one still works; 403 on a wrong current; `TestChangePasswordIsRateLimited` (5 wrong attempts → the 6th is 429 even with the right password; after a success the counter is reset); 400 for a weak password with the policy message; 415 without the JSON content type.

**Acceptance:** `go test -race -timeout 180s ./internal/auth/ ./internal/api/` green.

---

### Task 3: Frontend scaffold, shared types, test harness, design tokens

**Wave:** 1. Everything in wave 2 builds on this; keep it small and finish first.

**Files (create):** `web/package.json`, `web/package-lock.json`, `web/.npmrc`, `web/.nvmrc`, `web/tsconfig.json`, `web/vite.config.ts`, `web/svelte.config.js`, `web/index.html`, `web/public/favicon.svg`, `web/src/main.ts`, `web/src/App.svelte` (shell only: renders "Stackstead" heading and a `<main>`), `web/src/app.css`, `web/src/vitest-setup.ts`, `web/src/lib/api/types.ts`, `web/src/lib/format.ts` (+ `format.test.ts`), `web/src/guards.test.ts`.

**Interfaces (the shared contract — mirror the Go DTOs exactly; later tasks import these names):**
```ts
export type StackStatus = 'running'|'partial'|'stopped'|'unavailable'|'unknown';
export type Origin = 'managed'|'external';
export interface StackSummary { name; path; project; origin: Origin; status: StackStatus; serviceCount: number; running: number }
export interface ContainerInfo { id; name; service; state; status }
export type Field = 'image'|'build'|'ports'|'volumes'|'environment'|'networks'|'network_mode'|'limits'|'restart'|'healthcheck';
export interface ServiceConfig { image?; build?: {context; dockerfile?}; ports?: string[]; volumes?: string[];
  environment?: {name; value}[]; networks?: {name; ipv4Address?}[]; networkMode?; limits: {cpus?; memory?};
  restart?; healthcheck?: {test?: string[]; interval?; timeout?; startPeriod?; retries?: number} }
export interface ServiceDetail { name; config: ServiceConfig; readOnly: Field[]; containers: ContainerInfo[] }
export interface StackDetail extends StackSummary { hash: string; services: ServiceDetail[]; parseError?: string }
export type OpState = 'running'|'succeeded'|'failed'|'cancelled'|'timed_out';
export interface Operation { id; stack; action; state: OpState; startedAt: string; endedAt?: string; exitCode?: number; error?: string }
export interface OperationDetail extends Operation { lines: string[] }
export interface FSEntry { name; isDir: boolean; size: number }
export interface Network { name; driver; subnets: string[] }
export interface Status { docker: 'ok'|'unavailable'; compose: string /* version or 'unavailable' */; events: 'connected'|'disconnected'; metricsDegraded: boolean }
export interface FieldError { field: string; message: string }
export interface RawCompose { content: string; hash: string }
export interface LogEvent { service; container; stream: 'stdout'|'stderr'; time: string; text: string }
export interface LogError { service; container; error }
export interface MetricPoint { time: string; service; cpu: number; mem: number; memLimit: number;
  netRxBytesPerSec?: number; netTxBytesPerSec?: number; blkReadBytesPerSec?: number; blkWriteBytesPerSec?: number }
export interface Sample { time: string; service; container; cpu; mem; memLimit; netRx; netTx; blkRead; blkWrite }
export interface Change { project: string; seq: number }
export interface StatusFlags { events: 'connected'|'disconnected'; metricsDegraded: boolean }
export interface SetupStatus { needsSetup: boolean }   export interface Me { username: string }
export interface MetricsTokenStatus { configured: boolean }   export interface MetricsToken { token: string }
export const ACTIONS = ['up','down','restart','pull','build'] as const;
```
`format.ts`: `formatBytes(n)` (`1.5 GiB`), `formatRate(bytesPerSec)`, `formatDuration(ms)`, `relativeTime(iso, now)`, `formatPercent(x)`.

**Behaviour:**
- `vite.config.ts`: Svelte plugin; `build.outDir: 'dist'`, `emptyOutDir: true` (the dist `.gitignore` is re-created by the build via a tiny plugin `closeBundle` hook, or `emptyOutDir` is set to false and `assets/` is cleaned by the Makefile — pick the plugin hook and test it by building twice); `build.target: 'es2022'`; `server.proxy` for `/api` and `/metrics` to `http://127.0.0.1:8420` (SSE works through Vite's proxy as-is; set `proxy.timeout: 0`? not needed — document that dev uses the proxy so the Strict cookie is same-origin); `test` block: `environment: 'jsdom'`, `setupFiles`, `include: ['src/**/*.test.ts']`.
- `tsconfig.json`: strict, `moduleResolution: bundler`, `$lib` alias → `src/lib` (also in Vite `resolve.alias`).
- `package.json` scripts: `dev`, `build`, `preview`, `check` (`svelte-check --tsconfig ./tsconfig.json`), `test` (`vitest run`), `test:watch`. `engines.node: "22.x"`.
- `app.css`: CSS custom properties on `:root` (surface, text, muted, border, accent, success, warning, danger, info; spacing scale; radii; font stacks incl. a monospace stack for logs/ops) with a `prefers-color-scheme: dark` override; a minimal reset; `.visually-hidden`; focus-visible outline; `@media (prefers-reduced-motion: reduce)` kills transitions; layout utilities (`.stack`, `.cluster`, `.container` with 16 px gutter, max-width 1200 px).
- `vitest-setup.ts`: exports `installFakeFetch(routes)` (a `fetch` replacement taking a map of `METHOD path` → response or handler, recording calls, returning `Response` objects with JSON and headers) and `installFakeStream(url → chunks[])` for SSE tests (returns a `Response` whose body is a `ReadableStream` the test can push into and close). No network, no msw.
- `guards.test.ts`: walks `src/` and fails if any `.svelte` or `.ts` file contains `{@html`; fails if `package.json` has a `^` or `~` in any dependency version.

**Tests (write first):** `format.test.ts` tables for each formatter (zero, exact KiB boundaries, negative → `0 B`, invalid date → `—`). `guards.test.ts` as above. `App.svelte` renders the heading (smoke test for the harness).

**Acceptance:** `npm ci && npm run check && npm test && npm run build` green; `web/dist/` contains `index.html` and `assets/*` after build; `git status` clean apart from intended files; the Go `web` package still builds in this worktree (dist contents are ignored).

---

### Task 4: API client and session store

**Wave:** 2. Depends on T3.

**Files:** create `web/src/lib/api/client.ts`, `client.test.ts`, `web/src/lib/api/session.ts`, `session.test.ts`.

**Interfaces:**
```ts
export class ApiError extends Error { status: number; fields: FieldError[]; body?: unknown }
export interface RequestResult<T> { data: T; checksPartial: boolean }
export function request<T>(method: 'GET'|'POST'|'PUT'|'DELETE', path: string, body?: unknown, opts?: {signal?: AbortSignal}): Promise<RequestResult<T>>
export const api = {   // thin typed wrappers, one per backend route
  setupStatus(), setup(username, password, setupToken?), login(u, p), logout(), me(),
  stacks(), stack(name), createStack(req), importStack(path), compose(name), saveCompose(name, content, hash),
  updateService(name, service, config, hash), previewService(name, service, config, hash),
  act(name, action), remove(name, withVolumes), stackOperations(name), operation(id), cancel(id),
  runningOperations(), fs(path?), networks(), status(), metricsTokenStatus(), createMetricsToken(), deleteMetricsToken(),
  changePassword(current, next), metricsHistory(name, {from, to, step, service}) };
export function onUnauthorized(handler: () => void): void   // App registers the redirect
```
```ts
// session.ts
export const session: Readable<{state: 'unknown'|'anonymous'|'needs-setup'|'authenticated'; username?: string}>
export async function bootstrapSession(): Promise<void>   // GET /api/me → authenticated; 401 → GET /api/setup/status → needs-setup | anonymous
export async function login(u, p): Promise<void>; export async function logout(): Promise<void>; export async function setup(u, p, token?): Promise<void>
```

**Behaviour:**
- `request`: `credentials: 'same-origin'`, `Accept: application/json`; a body (including `{}` for body-less POSTs — `act`, `cancel`, `createMetricsToken`) is JSON with `Content-Type: application/json`; `DELETE` sends no body. Non-2xx → `ApiError` with `message` from `body.error` (fallback `HTTP <status>`), `fields` from `body.fields` (default `[]`). 204 → `data: undefined`. `checksPartial` is true when the `X-Stackstead-Checks` header equals `partial`. Network failure → `ApiError{status: 0, message: 'network error'}`.
- 401 on any route except `/api/login`, `/api/setup*`, `/api/me` calls the registered `onUnauthorized` handler **once per session** (a flag reset by a successful login) and still rejects with the error. A 403 never triggers it (`/api/password` answers 403 for a wrong current password).
- `session`: `logout` clears the state to `anonymous` even if the request fails (cookie may already be gone). `login` resets the once-flag. `setup` then logs in automatically.

**Tests (write first)** with `installFakeFetch`:
- JSON body and headers are set on POST, `{}` is sent for `act`; DELETE has no body; `credentials` is `same-origin`.
- Error body `{error, fields}` → `ApiError` with both; a 500 with HTML body → message `HTTP 500`.
- `X-Stackstead-Checks: partial` → `checksPartial: true`.
- `TestUnauthorizedRoutesToLoginOnce`: two concurrent 401s call the handler once; after `login()` succeeds, a later 401 calls it again.
- `bootstrapSession`: 200 me → authenticated with username; 401 + `needsSetup: true` → `needs-setup`; 401 + false → `anonymous`; network error → `anonymous` with a thrown error surfaced to the caller.

**Acceptance:** `npm test -- client session` green; `npm run check` clean.

---

### Task 5: SSE client and live store

**Wave:** 2. Depends on T3 (and T4 only for `api.status()` and `api.runningOperations()` — import them by name; they exist once wave 2 merges; in this task's worktree, type-check against a local stub is not allowed — instead the live store takes a `fetchStatus` function parameter with the default bound in `App` by T22. Keep the store free of `client.ts` imports).

**Files:** create `web/src/lib/sse/parse.ts`, `parse.test.ts`, `web/src/lib/sse/client.ts`, `client.test.ts`, `web/src/lib/stores/live.ts`, `live.test.ts`.

**Interfaces:**
```ts
// parse.ts (pure)
export interface SSEMessage { event: string; data: string; id?: string; retry?: number }
export interface ParserState { /* buffered partial line, current fields */ }
export function createParser(): { feed(chunk: string): {messages: SSEMessage[]; comments: number}; flush(): SSEMessage[] }

// client.ts
export type SSEStatus = 'connecting'|'open'|'reconnecting'|'closed'|'unauthorized'|'gone';
export interface SSEOptions { url: string; lastEventId?: string | (() => string | undefined) /* a function is asked on every reconnect; it overrides the id tracked internally */; onMessage(m: SSEMessage): void;
  onStatus?(s: SSEStatus, info?: {attempt: number; error?: string}): void;
  heartbeatTimeoutMs?: number /* 45000 */; backoff?: {minMs: number; maxMs: number} /* 1000..30000 */;
  fetchImpl?: typeof fetch; now?: () => number; setTimeoutImpl?; }
export function openSSE(o: SSEOptions): { close(): void; readonly status: SSEStatus; readonly lastEventId?: string }

// live.ts
export interface LiveState { connection: SSEStatus; events: 'connected'|'disconnected'|'unknown'; metricsDegraded: boolean;
  docker: 'ok'|'unavailable'|'unknown'; compose: string|'unavailable'|'unknown' }
export const live: Readable<LiveState>
export const changes: Readable<{project: string; seq: number; resync?: true} | null>  // resync:true is emitted on every (re)open
export function startLive(deps: {fetchStatus: () => Promise<Status>; open?: typeof openSSE}): void  // idempotent
export function stopLive(): void
export function refreshStatus(): Promise<void>   // re-fetch /api/status (used after a 503)
```

**Behaviour:**
- Parser follows the SSE spec: `\n`, `\r\n` and `\r` line ends; fields `event`, `data` (multi-line joined by `\n`), `id`, `retry`; lines starting with `:` are comments (counted, not delivered); a blank line dispatches; a message with empty data and no event is still dispatched only if `data` was present; partial lines are buffered across chunks (test every split point of a sample stream); `id` persists as the last-event-id only when the field is present.
- Client: `fetch(url, {headers: {Accept: 'text/event-stream', ...(lastEventId && {'Last-Event-ID': lastEventId})}, credentials: 'same-origin', signal})`. Non-200 → `unauthorized` (401/403) or `gone` (404): **no retry**, `onStatus` called, `close()` is implicit. Other non-200/network error → `reconnecting` with jittered exponential backoff (`min * 2^attempt`, capped, ±20 %). A heartbeat watchdog resets on any bytes (comment or event); when nothing arrives for `heartbeatTimeoutMs`, abort and reconnect. `close()` aborts the fetch, cancels timers, sets `closed`, and guarantees no `onMessage`/`onStatus` after it returns. Reconnects resend the latest `id` seen. The server's `retry:` field, if present, overrides `minMs`.
- Live store: `startLive` opens `/api/events` once (second call is a no-op while open). Maps `status` events → `events`/`metricsDegraded`; `change` → `changes` store; `dropped` → close and reopen immediately (attempt 0); every `open` → emits `{resync: true}` on `changes` and calls `fetchStatus()` to refresh docker/compose; `unauthorized` → `stopLive()` (App then routes to login). `refreshStatus` is also triggered when `events` flips between connected/disconnected.

**Tests (write first)** with `installFakeStream` and fake timers:
- Parser: split-point table; CRLF; multi-line data; comment counted; `id` retained across messages.
- Client: `TestReconnectSendsLastEventId` (stream delivers id `7`, dies, the second request has header `Last-Event-ID: 7`); `TestLastEventIdProviderWins` (a `lastEventId` function returning `a:1:1,b:2:1` is used instead of the tracked id; returning `undefined` sends no header); `TestHeartbeatTimeoutReconnects` (45 s of silence → abort + second fetch; a comment at 30 s resets the timer); `TestBackoffIsCappedAndJittered`; `TestStopsOn401` (one fetch, status `unauthorized`, no second fetch after 60 s); `TestCloseStopsEverything` (no callbacks after close, fetch signal aborted).
- Live: `TestStartLiveIsIdempotent`; `TestReconnectEmitsResync`; `TestDroppedReopensImmediately`; status flag mapping; `fetchStatus` called on open and on `events` flip only (not on every change).

**Acceptance:** `npm test -- sse live` green with no real timers (tests finish < 2 s).

---

### Task 6: Router

**Wave:** 2. Depends on T3.

**Files:** create `web/src/lib/router.ts`, `router.test.ts`.

**Interfaces:**
```ts
export interface RouteMatch { params: Record<string,string>; query: URLSearchParams; path: string }
export function matchRoute(pattern: string, path: string): Record<string,string> | null   // '/stacks/:name' ; ':name' matches one segment, decoded
export const route: Readable<{path: string; query: URLSearchParams}>
export function navigate(to: string, opts?: {replace?: boolean}): void
export function link(node: HTMLAnchorElement): {destroy(): void}   // Svelte action: intercept same-origin left-clicks without modifiers
export function startRouter(): () => void   // listens to popstate; returns stop
```

**Behaviour:** paths are compared after decoding each segment; trailing slash is ignored; query is preserved separately; `navigate` pushes/replaces history and updates the store synchronously; `link` ignores clicks with modifier keys, middle button, `target=_blank`, `download`, or external hrefs. Stack names may contain `_`/`-`; a name containing `/` is impossible (compose rules) but the matcher must still not match across segments.

**Tests (write first):** match table (`/`, `/stacks/:name` with `web`, `my-stack_2`, `a%2Fb` → `a/b` single segment, no match for `/stacks/a/b`); `navigate` updates `route` and `history.length`; `popstate` updates the store; `link` action intercepts a plain click and not a ctrl-click.

**Acceptance:** tests green.

---

### Task 7: Logs buffer

**Wave:** 2. Depends on T3.

**Files:** create `web/src/lib/logs/buffer.ts`, `buffer.test.ts`.

**Interfaces:**
```ts
export interface LogLine extends LogEvent { seq: number }
export function createLogBuffer(cap = 5000): { push(lines: LogEvent[]): void; snapshot(): LogLine[]; dropped(): number; clear(): void; subscribe: Readable<LogLine[]>['subscribe'] }
export interface LogFilter { service?: string; stream?: 'stdout'|'stderr'; search?: string }
export function filterLines(lines: LogLine[], f: LogFilter): LogLine[]   // search is case-insensitive substring; no regex
export function createCoalescer<T>(flush: (items: T[]) => void, schedule = requestAnimationFrame): { add(item: T): void; add(items: T[]): void; cancel(): void }
```

**Behaviour:** a ring buffer (array + head index or a two-array swap — not `Array.shift` per line); `push` of more than `cap` lines keeps the newest `cap`; `dropped()` counts evicted lines since `clear`; `snapshot()` returns lines in order with monotonically increasing `seq`; the store notifies once per `push`. The coalescer batches `add`s and calls `flush` once per frame with everything added since the last flush; `cancel` drops pending items.

**Tests (write first):** `TestRingBufferCapsAndCountsDropped` (push 12 000 lines into cap 5 000 → 5 000 kept, newest last, `dropped() === 7000`); push of 10 000 lines completes in < 50 ms (performance guard, generous); `filterLines` service + stream + search combinations; `TestCoalesceFlushesOncePerFrame` with a fake scheduler (3 adds → one flush with 3 items; add during flush schedules the next frame).

**Acceptance:** tests green.

---

### Task 8: Metrics series

**Wave:** 2. Depends on T3.

**Files:** create `web/src/lib/metrics/series.ts`, `series.test.ts`.

**Interfaces:**
```ts
export type Range = '1h'|'6h'|'24h'|'72h';
export function rangeToQuery(range: Range, now: Date): {from: string; to: string; step: string}  // step chosen for ≤ 720 points
export interface SeriesPoint { t: number /* unix ms */; cpu: number; mem: number; memLimit: number; netRx?: number; netTx?: number; blkRead?: number; blkWrite?: number }
export type SeriesSet = Map<string /* service */, SeriesPoint[]>
export function historyToSeries(points: MetricPoint[]): SeriesSet          // grouped by service, sorted by t
export function createLiveAggregator(): { ingest(samples: Sample[]): SeriesPoint[] /* one per service for this tick */; reset(): void }
export function appendLive(set: SeriesSet, service: string, p: SeriesPoint, windowMs: number): void   // drop points older than now-window; ignore p if t <= last t
export function joinHistoryAndLive(history: SeriesSet, live: SeriesSet): SeriesSet   // live points with t <= last history t are dropped
export function toUplotData(set: SeriesSet, metric: keyof SeriesPoint, services: string[]): [number[], ...(number|null)[][]]  // aligned on the union of timestamps; null gaps
```

**Behaviour:**
- `createLiveAggregator` keeps the previous cumulative counters **per container**; a tick's rate per container is `(cur - prev) / dt`, `0` when `cur < prev` (restart) or `dt <= 0`; the first sample of a container has no rate (undefined). Per service for one tick: `cpu`, `mem` and `memLimit` are averages across containers (not the max), each rate the **sum** across containers (decision 2026-10-14: rates are totals across replicas, computed per container then summed; the server history uses the same formula, so live must match). Samples are grouped into a tick by identical `time`; a sample whose time differs from the batch by < 1 s joins the same tick.
- `rangeToQuery` picks `step` so that `range / step ≤ 720` and is a whole number of seconds (`1h`→`5s`, `6h`→`30s`, `24h`→`2m`, `72h`→`6m`).
- `toUplotData` produces uPlot's `[xs, ys1, ys2…]` layout with `null` where a service has no point at a timestamp.

**Tests (write first):** `historyToSeries` groups and sorts; `TestCounterResetGivesZeroRate`; `TestReplicaAggregationMatchesServer` (two containers of one service: cpu averaged, netRx rate summed); `TestJoinDropsOverlap` (a live point equal to the last history timestamp is dropped; later ones kept); `appendLive` trims the window and ignores out-of-order points; `rangeToQuery` table; `toUplotData` alignment with gaps.

**Acceptance:** tests green.

---

### Task 9: Shared components and toasts

**Wave:** 2. Depends on T3.

**Files:** create under `web/src/lib/components/`: `Banner.svelte`, `Toasts.svelte`, `Dialog.svelte`, `ConfirmDialog.svelte`, `Field.svelte`, `Button.svelte`, `StatusBadge.svelte`, `Tabs.svelte`, `ListEditor.svelte`, `CopyButton.svelte`, `Spinner.svelte`, `EmptyState.svelte`, `RelativeTime.svelte`, `Nav.svelte`, plus `components.test.ts`; `web/src/lib/stores/toasts.ts`.

**Interfaces (props):**
```ts
Banner { kind: 'info'|'warning'|'danger'; title: string; children; dismissible?: boolean }   // role=status (info) or role=alert (danger)
toasts: { push(t: {kind; message; timeoutMs?}): number; dismiss(id): void; subscribe }   Toasts renders them in an aria-live=polite region
Dialog { open: boolean; title: string; onclose(): void; children; labelledBy? }   // focus trap, Escape closes, returns focus to the opener, aria-modal
ConfirmDialog { open; title; message; confirmLabel; danger?: boolean; onconfirm(); oncancel() }
Field { label: string; id: string; error?: string; hint?: string; readonly?: boolean; readonlyReason?: string; children }  // associates label/aria-describedby/aria-invalid
Button { variant: 'primary'|'secondary'|'danger'|'ghost'; loading?: boolean; disabled?: boolean; disabledReason?: string (rendered as title + aria-describedby); type?; onclick? }
StatusBadge { status: StackStatus | OpState }   // text + colour + icon glyph; never colour-only
Tabs { tabs: {id; label}[]; selected: string; onselect(id) }   // role=tablist/tab/tabpanel, arrow keys, Home/End
ListEditor<T> { items: T[]; onchange(items: T[]); addLabel; row: Snippet<[T, index, update]>; readonly?: boolean; errors?: Record<number,string> }
CopyButton { text: string; label?: string }   // navigator.clipboard with a textarea fallback; announces "Copied"
RelativeTime { iso: string }   // "3 min ago", title shows the absolute time
Nav { username?: string; onlogout(); runningOps: number }  // links to /, /stacks/new, /stacks/import, /settings; current page aria-current
```

**Behaviour:** all components are keyboard-operable; `Dialog` uses `inert` on the app root while open (fallback to focus trap if unsupported); `ListEditor` moves focus to the new row's first input on add and to the previous row on remove; `Button` with `disabledReason` is `aria-disabled` (still focusable so the reason is discoverable) rather than `disabled`.

**Tests (write first,** Testing Library): `Dialog` traps Tab, closes on Escape, restores focus; `Field` wires `for`/`id`, `aria-invalid` and `aria-describedby` to the error; `Tabs` arrow keys move selection and focus; `ListEditor` add/remove focus management; `StatusBadge` renders the status text for each value; `toasts` auto-dismiss with fake timers; `Button` `disabledReason` exposes the reason to AT.

**Acceptance:** tests green; `npm run check` clean.

---

### Task 10: Service form model and validation

**Wave:** 2. Depends on T3.

**Files:** create `web/src/lib/compose/form.ts`, `form.test.ts`.

**Interfaces:**
```ts
export interface ServiceForm { source: 'image'|'build'; image: string; buildContext: string; dockerfile: string;
  ports: string[]; volumes: string[]; env: {name: string; value: string}[]; networks: {name: string; ipv4Address: string}[];
  networkMode: string; cpus: string; memory: string; restart: ''|'no'|'always'|'unless-stopped'|'on-failure';
  healthcheck: {enabled: boolean; test: string; interval: string; timeout: string; startPeriod: string; retries: string};
  readOnly: Set<Field> }
export function emptyForm(): ServiceForm
export function toForm(cfg: ServiceConfig, readOnly: Field[]): ServiceForm
export function fromForm(f: ServiceForm): ServiceConfig      // omits empty values; healthcheck.test split on whitespace unless it starts with "CMD"/"CMD-SHELL" → kept as one shell string under ["CMD-SHELL", ...]
export type FormErrors = { [path: string]: string }            // paths: 'name', 'image', 'build.context', 'ports[1]', 'env[0].name', 'networks[1].ipv4Address', 'limits.memory' …
export function validateForm(f: ServiceForm, opts: {name?: string; requireName?: boolean}): FormErrors
export function mapServerErrors(fields: FieldError[], f: ServiceForm): FormErrors   // 'ports[1]'→'ports[1]'; 'networks.lan.ipv4_address'→'networks[<index of lan>].ipv4Address'; unknown → '_form'
export function fieldOf(path: string): Field | 'name'   // which group a path belongs to (for the read-only check and raw-editor hint)
```

**Behaviour / rules (client-side, mirror the backend's documented rules so most errors show before a round-trip):**
- `name`: `^[a-z0-9][a-z0-9_-]{0,62}$`.
- exactly one of image/build; `build.context` non-empty when `source==='build'`.
- ports: the short syntaxes Plan 3 lists (`8080:80`, `127.0.0.1:8080:80`, `8000-8002:80-82`, `53:53/udp`, `[::1]:80:80`, `80`, `${PORT:-80}:80` accepted unchecked); ports ≤ 65535; ranges of equal length.
- env names: `^[A-Za-z_][A-Za-z0-9_]*$`; duplicate names flagged on the second occurrence.
- `cpus`: positive decimal; `memory`: `^\d+(\.\d+)?[kKmMgG]?[bB]?$`.
- `ipv4Address`: dotted quad, each octet ≤ 255, only when the network row has a name.
- healthcheck: when enabled, `test` non-empty; durations `^\d+(ms|s|m|h)$`; retries integer ≥ 0.
- Read-only groups are never validated (their values are not editable) and `fromForm` must copy them through unchanged from the original config so a save does not clear them: `toForm` keeps a private copy of the original `ServiceConfig` under `_orig` (not enumerable in tests) and `fromForm` merges read-only groups from it.

**Tests (write first):** round-trip `toForm`/`fromForm` for a full config and an empty one (`limits: {}` always present); each rule's accept/reject table; `mapServerErrors` for `ports[1]`, `networks.lan.ipv4_address`, `name`, an unknown field → `_form`; read-only groups survive the round-trip untouched even when the form shows them blank.

**Acceptance:** tests green.

---

### Task 11: Login and first-run setup screens

**Wave:** 3. Depends on T4, T6, T9.

**Files:** create `web/src/routes/Login.svelte`, `Login.test.ts`, `web/src/routes/Setup.svelte`, `Setup.test.ts`.

**Behaviour:**
- Login: username + password fields (autocomplete `username`/`current-password`), submit disabled while pending, 401 → "invalid username or password" inline (`role=alert`), 429 → the server's message, network error → generic message; success → `navigate(next || '/')` where `next` comes from `?next=` and must be a same-origin path starting with `/` (otherwise `/`).
- Setup: shown when `session.state === 'needs-setup'`; username, password (min 12 chars client-side mirroring `MinPasswordLength`; show the rule), confirm password, optional setup token with the hint "needed unless you open this page from the machine itself"; 403 → explain the token requirement; 400 → server message; success → logs in and navigates to `/`. If `needsSetup` is false (someone else finished), route to `/login`.
- Both are centred single-column cards, fine at 375 px; the page `<title>` updates.

**Tests (write first):** Login: submit calls `login` with the typed values; 401 shows the error and keeps the username; `next` sanitisation (`/stacks/web` kept, `https://evil` and `//evil` rejected); Enter submits. Setup: mismatch blocks submit with an inline error; 403 message; success navigates.

**Acceptance:** tests green; `npm run check` clean.

---

### Task 12: Stack list screen

**Wave:** 3. Depends on T4, T5, T6, T9.

**Files:** create `web/src/routes/StackList.svelte`, `StackList.test.ts`, `web/src/lib/stores/stacks.ts`, `stacks.test.ts`.

**Interfaces:**
```ts
// stacks.ts
export const stacks: Readable<{items: StackSummary[]; loading: boolean; error?: string}>
export function startStacks(deps: {list: () => Promise<StackSummary[]>; changes: Readable<Change & {resync?: true} | null>}): () => void   // subscribes; refetch on resync or on a change whose project matches a known stack OR is unknown (new stack appeared); debounced 300 ms
export function refreshStacks(): Promise<void>
```

**Behaviour:** a table (cards under 640 px) with name, status badge, `running/serviceCount`, origin, project, path (truncated with full title), and quick actions (Up/Down/Restart → T15's `ActionsBar` is not available here; the list links to the detail page; keep only a "running operation" spinner per stack from `/api/operations?state=running`). Empty state links to create/import. A `unavailable` row shows a warning icon and links to the detail where the error is shown. Sort by name; a search box filters by name/project (client-side). Header shows the count. A `degraded` note appears when the list came back with status `unknown` for all stacks (engine down).

**Tests (write first):** `TestResyncRefetchesList` (resync → list called again); change for an unknown project → refetch (new stack); change for a known project → refetch debounced (3 changes in 100 ms → 1 call); renders badges and the empty state; search filters; running-op spinner present when `/api/operations?state=running` includes the stack.

**Acceptance:** tests green.

---

### Task 13: FileBrowser and NetworkPicker

**Wave:** 3. Depends on T4, T9.

**Files:** create `web/src/lib/stacks/FileBrowser.svelte`, `FileBrowser.test.ts`, `web/src/lib/stacks/NetworkPicker.svelte`, `NetworkPicker.test.ts`.

**Interfaces:**
```ts
FileBrowser { open: boolean; mode: 'dir'|'file'; title; onpick(path: string): void; onclose(); filter?: (e: FSEntry) => boolean }
NetworkPicker { value: {name: string; ipv4Address: string}[]; onchange(v): void; readonly?: boolean; errors?: Record<number,string> }
```

**Behaviour:**
- FileBrowser is a `Dialog` listing `GET /api/fs` (roots when no path), breadcrumbs for the current path (clickable up to the root), directories first, a "Use this directory" button in `dir` mode, click-to-pick a file in `file` mode (default filter: files named `Dockerfile*` or `*.Dockerfile` shown first, others still listed), keyboard navigation (list items are buttons), 403 → "outside the allowed directories", 404 → "no such directory". Never shows paths outside roots because the server does not return them.
- NetworkPicker loads `GET /api/networks` once, offers a select of network names with their subnets as hint text, an optional static IPv4 input per attached network with the subnet shown as placeholder (first subnet), add/remove rows (via `ListEditor`), and shows errors per row. If the networks call fails (engine down) it degrades to a free-text network name with a note.

**Tests (write first):** FileBrowser: roots listed; entering a dir fetches `?path=`; breadcrumbs navigate back; `dir` mode pick returns the directory; `file` mode pick returns the file path; 403 message. NetworkPicker: options from the API with subnet hint; static IP input appears only when a network is chosen; engine down fallback.

**Acceptance:** tests green.

---

### Task 14: ServiceForm component

**Wave:** 3. Depends on T9, T10 (and T13's `NetworkPicker` + `FileBrowser` by name — this task renders a `networks` slot/prop placeholder: it accepts `networkPicker: Snippet` and `onBrowse(mode, cb)` callbacks so it does not import T13 files; T21/T20 pass them in).

**Files:** create `web/src/lib/stacks/ServiceForm.svelte`, `ServiceForm.test.ts`.

**Interfaces:**
```ts
ServiceForm { form: ServiceForm (bindable); errors: FormErrors; showName?: boolean; name?: string (bindable); nameReadonly?: boolean;
  onbrowse?(mode: 'dir'|'file', cb: (path: string) => void): void; networkPicker?: Snippet<[ServiceForm]>; onRawEditor?(): void }
```

**Behaviour:** sections: Source (image or build; build has context + Dockerfile with "Browse…" buttons when `onbrowse` is given), Ports, Volumes, Environment (name/value rows; paste of `KEY=VALUE` lines into an empty name field splits into rows), Networks (`networkPicker` snippet or a plain list editor), Network mode (select: default/host/none/bridge/custom text), Resources (cpus, memory), Restart (select), Healthcheck (toggle + fields). A group in `form.readOnly` renders its inputs disabled with the hint "This field uses YAML the form can't edit — open the raw YAML editor" and a button calling `onRawEditor`. `errors['_form']` renders at the top in `role=alert`. Field ids are stable (`svc-ports-1`) so error links can focus them.

**Tests (write first):** read-only group disabled with hint and raw-editor button; errors render next to their field with `aria-invalid`; switching source clears the other source's errors; env paste splits rows; a change event updates the bound form; the first error is focused when `errors` changes (call `focusFirstError()` export).

**Acceptance:** tests green.

---

### Task 15: OperationPanel and ActionsBar

**Wave:** 3. Depends on T4, T5, T9.

**Files:** create `web/src/lib/stacks/OperationPanel.svelte`, `OperationPanel.test.ts`, `web/src/lib/stacks/ActionsBar.svelte`, `ActionsBar.test.ts`, `web/src/lib/stacks/operation.ts` (store factory), `operation.test.ts`.

**Interfaces:**
```ts
// operation.ts
export function createOperationStream(id: string, deps: {open?: typeof openSSE; get: (id) => Promise<OperationDetail>}): { lines: Readable<string[]>; op: Readable<Operation|undefined>; status: Readable<SSEStatus>; close(): void }
// components
OperationPanel { operationId: string; oncancel?(id): void; onfinished?(op: Operation): void; autoscroll?: boolean }
ActionsBar { stack: StackSummary; lifecycleDisabledReason?: string; busy?: boolean; onact(action: typeof ACTIONS[number]): void; onremove(withVolumes: boolean): void }
```

**Behaviour:**
- `createOperationStream` opens `/api/operations/{id}/stream`; `line` events append (data is a JSON string — parse it); ids are tracked so a reconnect sends `Last-Event-ID`; `done` sets `op` and closes; `dropped` reconnects immediately with the last id; the "N earlier lines dropped" note (no id) is shown as a dimmed first line. Client buffer cap 20 000 lines (matches the server). If the stream ends without `done` (gone/unauthorized), fetch `GET /api/operations/{id}` once to show the final state.
- `OperationPanel` renders a monospace `<pre>`-styled scroll region (`role=log`, `aria-live=off` to avoid flooding screen readers; a visually-hidden status line announces state changes), header with action, stack, state badge, elapsed time, exit code, and a Cancel button while running. Auto-scroll follows unless the user scrolled up (then a "Jump to latest" button).
- `ActionsBar`: Up, Down, Restart, Pull, Build buttons and a Remove button opening a `ConfirmDialog` with a "Also remove volumes" checkbox and the text that files on disk are never deleted. All lifecycle buttons use `disabledReason` when `lifecycleDisabledReason` is set (compose/engine unavailable) or `busy` (another operation running → "an operation is already running"). A 409 from `onact` is the caller's concern (toast).

**Tests (write first):** `TestResumeSendsLastEventId` (after a forced disconnect, the second open passes `lastEventId` = last seen id); `TestNoDuplicateLinesAcrossReconnect` (server replays lines 3–4 after id 2 → 5 lines total, no repeats); `done` ends the stream and calls `onfinished`; `dropped` reconnects immediately; Cancel calls `oncancel`; ActionsBar disables with reason; Remove dialog passes the volumes choice.

**Acceptance:** tests green.

---

### Task 16: YAML editor, diff view, compose editor with conflict handling

**Wave:** 3. Depends on T4, T9. Implementer loads CodeMirror via dynamic `import()` inside `onMount` so the chunk is split.

**Files:** create `web/src/lib/stacks/YamlEditor.svelte`, `YamlEditor.test.ts`, `web/src/lib/stacks/DiffView.svelte`, `DiffView.test.ts`, `web/src/lib/stacks/ComposeEditor.svelte`, `ComposeEditor.test.ts`, `web/src/lib/stacks/compose.ts` (state machine), `compose.test.ts`.

**Interfaces:**
```ts
YamlEditor { value: string (bindable); readonly?: boolean; ariaLabel: string; onchange?(v: string): void }     // CodeMirror 6: lang-yaml, line numbers, history, basic keymap; textarea fallback until the chunk loads
DiffView { original: string; modified: string; ariaLabel: string }   // @codemirror/merge unifiedMergeView, read-only, collapsed unchanged regions (context 3)
// compose.ts
export type ComposeState = { phase: 'loading'|'editing'|'previewing'|'saving'|'conflict'|'saved'; content: string; draft: string; hash: string; error?: string; conflictMessage?: string }
export function createComposeEditor(stackName: string, deps: {load: () => Promise<RawCompose>; save: (content, hash) => Promise<{hash: string}>}): { state: Readable<ComposeState>; setDraft(v): void; preview(): void; cancelPreview(): void; save(): Promise<boolean>; reload(opts?: {keepDraft: boolean}): Promise<void> }
ComposeEditor { stackName: string; parseError?: string; onsaved?(hash: string): void; onapply?(): void }
```

**Behaviour:**
- Load → editor shows `content`; `draft` starts equal. "Preview changes" opens a `DiffView` of `content` → `draft` with a Save button; "Save" (also available from preview) sends `{content: draft, hash}`.
- 409 → `conflict` phase: the server message (file changed, stack unavailable, or busy — all 409s are shown verbatim) with **Reload** (loads the new content + hash, keeps the draft, and shows a diff *new content → draft* so the user can re-apply) and **Discard my changes**. `TestConflictOffersReloadAndKeepsDraft`.
- 422 → the compose error text shown verbatim in a `<pre>` under the editor (text, not HTML) and the editor keeps the draft; the first `line N` number found in the message, if any, moves the cursor there (best-effort).
- Success → `content = draft`, new hash, toast "Saved — previous version kept as compose.yaml.bak", an **Apply** button appears (`onapply` → the caller runs `up`). `checksPartial` → warning toast "port and IP checks were partial (Docker unreachable)".
- Unsaved-draft guard: `beforeunload` while `draft !== content`; the tab switch in T22 asks via `ConfirmDialog`.
- `parseError` prop (stack in raw-only mode) shows a danger banner above the editor with the parse error text.
- Ctrl/Cmd+S saves.

**Tests (write first):** state machine table (load → edit → preview → save ok; 409 → conflict → reload keeps draft and new hash; 422 keeps draft + message; network error); `TestConflictOffersReloadAndKeepsDraft`; YamlEditor renders the textarea fallback synchronously and swaps to CodeMirror after the dynamic import (mock the import in jsdom by stubbing `createEditor`); DiffView shows added/removed markers for a one-line change (assert the modified text is present and the unified view container exists); the Apply button appears only after a successful save.

**Acceptance:** tests green; `npm run build` shows CodeMirror in its own chunk (check the asset list for a `codemirror`-named chunk via `manualChunks` or the dynamic import's own chunk).

---

### Task 17: Logs viewer

**Wave:** 3. Depends on T4, T5, T7, T9.

**Files:** create `web/src/lib/stacks/LogsViewer.svelte`, `LogsViewer.test.ts`, `web/src/lib/stacks/logs.ts` (stream controller), `logs.test.ts`.

**Interfaces:**
```ts
// logs.ts
export function createLogStream(stackName: string, opts: {service?: string; tail: number|'all'; follow: boolean}, deps: {open?: typeof openSSE; buffer?: ReturnType<typeof createLogBuffer>}): { buffer; status: Readable<SSEStatus>; errors: Readable<LogError[]>; ended: Readable<boolean>; restart(opts): void; close(): void; cursors(): string /* composite Last-Event-ID */ }
export function mergeCursor(cursors: Map<string,string>, id: string): void   // pure: "<c>:<ts>:<ord>" → cursors.set(c, id); malformed ids ignored
LogsViewer { stackName: string; services: string[] }
```

**Behaviour:**
- Query: `?service=&tail=&follow=1&since=`; the controller feeds `log` events through the coalescer into the buffer, records `error` events (shown as a dismissible line per container), and sets `ended` on `end`. Every `log` event's `id` (`<container>:<ts>:<ordinal>`, see T2b) updates a per-container cursor map; the SSE client is opened with a `lastEventId` provider so that on reconnect it sends `Last-Event-ID: <all cursors joined by ",">` (T5's `openSSE` must accept `lastEventId` as a string **or** a `() => string | undefined` function; T5 implements both forms — this is the one place that needs the function form). The reconnect never adds `since` and keeps the buffer. `restart(opts)` (user changed service/tail) clears the cursors and the buffer.
- UI: toolbar with service select (All + each service), tail select (100/200/1000/all — `all` warns), follow toggle (default on), stdout/stderr filter, search box (filters the buffer; highlighted matches via text splitting, never HTML), Pause/Resume, Clear, and a "N older lines dropped" note. Lines render as `<div>` rows with time (local, `title` has UTC), service tag (colour per service, text too), stream tag, and text in a `<span>` (text node). The list follows the bottom unless the user scrolled up; while paused, a badge shows "N new lines" and a jump button. Connection state shown as a small status (connecting/live/reconnecting/ended).
- Backpressure: the coalescer flushes per animation frame; the buffer caps at 5 000; the DOM shows the filtered buffer (≤ 5 000 rows). Search runs on the buffer snapshot, debounced 150 ms.

**Tests (write first):** `TestFloodRendersBoundedRows` (push 20 000 lines via the fake stream → ≤ 5 000 rows and the dropped note); `TestResumeSendsCompositeCursor` (lines from containers `a` and `b` with ids `a:10:1`, `b:11:1`, `a:12:2`; the stream dies; the second request carries `Last-Event-ID` containing exactly `a:12:2` and `b:11:1` and no `since`); `mergeCursor` ignores malformed ids; service change restarts the stream with the new query and empty cursors; search filters rows and the match text is still a text node (`container.querySelector('span').textContent` equals the raw line containing `<script>`); error events render per container; `end` without follow shows "end of log".

**Acceptance:** tests green.

---

### Task 18: Metrics charts

**Wave:** 3. Depends on T4, T5, T8, T9. Implementer loads the `dataviz` skill before writing chart code (palette, axes, accessibility) and imports uPlot dynamically.

**Files:** create `web/src/lib/stacks/MetricsPanel.svelte`, `MetricsPanel.test.ts`, `web/src/lib/stacks/MetricsChart.svelte`, `MetricsChart.test.ts`, `web/src/lib/stacks/metrics.ts` (controller), `metrics.test.ts`.

**Interfaces:**
```ts
// metrics.ts
export function createMetricsController(stackName: string, deps: {history: (q) => Promise<{points: MetricPoint[]}>; open?: typeof openSSE; now?: () => Date}): { series: Readable<SeriesSet>; range: Writable<Range>; services: Readable<string[]>; status: Readable<SSEStatus>; degraded: Readable<boolean>; reload(): Promise<void>; close(): void }
MetricsChart { title: string; series: SeriesSet; metric: 'cpu'|'mem'|'netRx'|'netTx'|'blkRead'|'blkWrite'; unit: 'percent'|'bytes'|'bytesPerSec'; services: string[]; memLimit?: number }
MetricsPanel { stackName: string; metricsDegraded: boolean }
```

**Behaviour:**
- On mount and on range change: fetch history (`rangeToQuery`), build `SeriesSet`, then open `/api/stacks/{name}/metrics/live`; `sample` events go through the aggregator and `appendLive` (window = range); `dropped` → reconnect (the aggregator's previous counters are kept so the next rate is still right; points with `t <=` last are ignored, so no duplicates). `joinHistoryAndLive` on every history reload.
- Charts: CPU % (y from 0, max of 100 or data), Memory (bytes, with a dashed limit line when all services share one limit, else per-service), Network rx/tx (bytes/s, two charts or one with two series per service — pick one chart with rx positive/tx as a second series per service), Block I/O read/write. One colour per service (legend with text labels, same colours in the logs viewer's service tags); hover tooltip with time + values; axes formatted by `format.ts`; resize via `ResizeObserver`; `prefers-reduced-motion` respected (uPlot has no animations anyway).
- Empty history + no live → "No samples yet — the sampler records every 10 s"; `metricsDegraded` → warning banner "Metrics are not being recorded (database write failures); stack management is unaffected".
- An accessible summary table (`<table>` visually hidden or toggleable "Show as table") lists the latest value per service per metric, so the charts are not the only representation.

**Tests (write first):** controller: history then live appended; range change refetches with the new `step`; `TestDroppedReconnectsWithoutDuplicates`; `services` derived from the union of history and live; `MetricsChart` renders the summary table values (uPlot itself is stubbed in jsdom — `MetricsChart` receives a `createPlot` dep defaulting to the real one); degraded banner; empty state text.

**Acceptance:** tests green; uPlot is a separate chunk in the build output.

---

### Task 19: Settings screen

**Wave:** 3. Depends on T4, T5, T9 (and the T2b/T2c endpoint contracts).

**Files:** create `web/src/routes/Settings.svelte`, `Settings.test.ts`, `web/src/lib/settings/PasswordForm.svelte`, `PasswordForm.test.ts`.

**Behaviour:**
- **Prometheus token**: on mount `GET /api/settings/metrics-token`. Not configured → explains that `/metrics` answers 404 until a token exists, with a "Create token" button. Configured → "A token is configured" with "Rotate token" (opens a `ConfirmDialog`: the old token stops working immediately) and "Disable" (`DELETE`, confirm dialog, then shows the not-configured state). After create/rotate the new token is shown **once** in a read-only input with `CopyButton`, an "I have saved it" button that hides it, and a ready-to-copy `scrape_configs` snippet (`authorization: {credentials: <token>}`, target = `window.location.host`). The token is never written to storage; navigating away clears it. A failed status fetch shows an inline error with Retry, not a guess.
- **System status**: docker/compose/version, events connection, metrics degraded, from the `live` store plus a "Refresh" button calling `refreshStatus`.
- **Account**: username, Logout button, and `PasswordForm`: current password, new password (hint: at least 12 characters), confirm; `autocomplete` `current-password`/`new-password`; submit disabled until all three are filled and new = confirm; client-side length check; `POST /api/password`. 403 → "current password is incorrect" on the current field; 400 → server message on the new field; 429 → the server message as a banner with the form disabled for 15 min hint; success → all fields cleared, toast "Password changed. Other sessions were signed out." Fields are cleared on any outcome except a validation error, and the component never logs or stores values.
- **About**: links to the README sections on reverse proxies and the security model (static text).

**Tests (write first):** status fetch drives which buttons render (not configured → Create; configured → Rotate + Disable); create shows the token once and the snippet contains it; "I have saved it" hides it and it is not in the DOM afterwards; rotate requires confirmation; delete requires confirmation and flips to the not-configured state; status values render from the store; logout calls the session action; `TestPasswordFormClearsFieldsAndShowsResult` (mismatch blocks submit; 403 puts the error on the current field and clears the password inputs; success clears and toasts; 429 banner).

**Acceptance:** tests green.

---

### Task 20: Service edit flow (form → preview → save → apply)

**Wave:** 4. Depends on T2, T14, T15, T16 (uses `DiffView`), T10.

**Files:** create `web/src/lib/stacks/ServiceEditor.svelte`, `ServiceEditor.test.ts`, `web/src/lib/stacks/serviceEdit.ts`, `serviceEdit.test.ts`.

**Interfaces:**
```ts
export function createServiceEdit(stack: string, service: string, initial: {config: ServiceConfig; readOnly: Field[]; hash: string}, deps: {preview; update}): { form: Writable<ServiceForm>; errors: Readable<FormErrors>; phase: Readable<'editing'|'previewing'|'saving'|'conflict'|'saved'>; preview(): Promise<void>; save(): Promise<boolean>; reload(detail: StackDetail): void; previewText: Readable<{original: string; modified: string}|undefined> }
ServiceEditor { stack: StackDetail; service: ServiceDetail; onsaved(hash: string): void; onapply(): void; onRawEditor(): void; onreload(): Promise<StackDetail> }
```

**Behaviour:**
- Edit in `ServiceForm` (with `FileBrowser` for build paths and `NetworkPicker` wired in). "Preview changes" → client validation first (`validateForm`; errors block) → `POST …/preview` with `fromForm(form)` and the detail's hash → `DiffView` of the current raw compose (`GET …/compose` fetched once alongside, same hash check: if the compose hash differs from the detail hash, reload first) against the preview content. "Save" from the preview → `PUT …/services/{service}` with the **same hash** the preview used (`TestPreviewAndSaveUseSameHash`).
- 400 → `mapServerErrors` into the form (focus the first); 422 "this change needs the raw YAML editor" → an inline banner with a button that calls `onRawEditor()` (T22 switches to the Compose tab with the draft untouched); other 422 (read-only field, invalid config) → message next to the group (`fieldOf`) or at the top; 409 → conflict: Reload (calls `onreload`, re-seeds read-only sets and hash, keeps the user's form values, re-runs preview) or Discard.
- Success → toast, `onsaved(newHash)`, Apply button (`onapply` → `up`), `checksPartial` warning toast. The Apply button is disabled with a reason when lifecycle is unavailable (prop from the page).
- Unsaved guard like T16.

**Tests (write first):** validation errors block preview without a request; preview request body equals `fromForm`; `TestPreviewAndSaveUseSameHash`; 422 raw-editor message routes to `onRawEditor`; 409 → reload keeps form values and new hash; server `fields` land on the right inputs; read-only groups are sent unchanged (deep-equal to the original config for those groups).

**Acceptance:** tests green.

---

### Task 21: Create and Import screens

**Wave:** 4. Depends on T13, T14, T15 (for streaming the first `up`), T10, T4.

**Files:** create `web/src/routes/StackCreate.svelte`, `StackCreate.test.ts`, `web/src/routes/StackImport.svelte`, `StackImport.test.ts`.

**Behaviour:**
- **Create** (spec §5.1): name (with the rule as hint; the service name defaults to the stack name and is editable), `ServiceForm` with `FileBrowser` (`dir` for context, `file` for the Dockerfile — a picked Dockerfile inside the context is stored relative to it, else absolute) and `NetworkPicker`. Submit → client validation → `POST /api/stacks` → on 201: toast, navigate to `/stacks/{name}?tab=operations` and (if the user left "Start now (up --build)" checked, default on) call `act(name,'up')` and show the `OperationPanel` on the detail page via `?op=<id>`. 400 → field errors; 409 name exists → `name` error; 422 → compose error verbatim under the form; 403 (context outside roots) → `build.context` error; 503 → banner "Docker/compose unavailable: the stack was not created". `checksPartial` → warning toast. Image-only stacks are allowed (source = image).
- **Import** (spec §5.2): free-text absolute path with a "Browse…" helper (allowed roots only; explains the path may also be outside them), submit → `POST /api/stacks/import` → 201 navigate to the detail; 404 "no such file"; 409 already registered / override file present (message verbatim); 422 compose error; 403 permission denied. A note: "the file is not modified until you edit it".

**Tests (write first):** Create: happy path posts the expected `CreateRequest` (name, service, config with `build` and `image` omitted), navigates and starts `up`; "Start now" unchecked → no `act`; 409 → name error; 422 → verbatim text; 403 → context error. Import: posts the path; 404/409 messages; browse picks a file into the input.

**Acceptance:** tests green.

---

### Task 22: Stack detail page, App wiring, navigation, bundle budget

**Wave:** 5. Depends on everything in waves 3–4.

**Files:** create `web/src/routes/StackDetail.svelte`, `StackDetail.test.ts`; modify `web/src/App.svelte`, create `App.test.ts`; modify `web/src/main.ts` if needed; create `web/scripts/check-bundle.mjs` (reads `dist/.vite/manifest.json` or the asset list and gzip sizes; fails over budget) and wire `npm run build` → `vite build && node scripts/check-bundle.mjs` (enable `build.manifest: true`).

**Behaviour:**
- **App**: `startRouter`, `bootstrapSession`, then: `needs-setup` → Setup; `anonymous` → Login (with `next` = current path); `authenticated` → `startLive({fetchStatus: api.status})`, `startStacks`, and the route table: `/` StackList, `/stacks/new`, `/stacks/import`, `/stacks/:name`, `/settings`, else a NotFound view with a link home. `onUnauthorized` → `stopLive`, session → anonymous, `navigate('/login?next=…')`. Layout: `Nav` (with the running-operations count from `/api/operations?state=running`, refreshed on `change` events and on operation `done`), global banners from `live`: Docker unavailable (`docker==='unavailable'` or `events==='disconnected'` → "Docker is unreachable — status may be stale; lifecycle actions are disabled"), compose unavailable ("docker compose plugin not found — read-only mode"), metrics degraded, live-updates reconnecting (`connection==='reconnecting'`), then `<main>` and `Toasts`. Page `<title>` per route. Logout in Nav.
- **StackDetail**: loads `GET /api/stacks/{name}` (404 → NotFound with back link); refetches when `changes` matches its project or on resync. Header: name, status badge, origin, project, path, `ActionsBar` (lifecycle disabled reason derived from `live`: compose unavailable / docker unavailable; `busy` when a running operation exists for this stack). `onact` → `act()` → opens the Operations tab with the new op streaming; 409 busy → toast; 503 → `refreshStatus()` + toast. `onremove` → `remove()` → stream, and on `done` succeeded → navigate home with a toast.
  Tabs (`?tab=`): **Services** (one card per service: config summary, containers with state/status; "Edit" opens `ServiceEditor` inline or in a dialog; read-only hints), **Compose** (`ComposeEditor`, `parseError` passed through), **Logs** (`LogsViewer`), **Metrics** (`MetricsPanel`), **Operations** (recent list from `GET …/operations` with state badges and relative times; selecting one shows `OperationPanel`; `?op=` preselects). A stack in `unavailable` state with a parse error shows only Compose (raw-only mode) plus Operations, with a banner; a missing file (`parseError` says so or `path` unreadable) shows the message and no editor. Switching tabs away from an unsaved editor asks for confirmation. Only the active tab's component is mounted (so streams close on tab change — "end on navigation").
- `onRawEditor` from the service editor switches to the Compose tab.
- Bundle check: entry + eager chunks ≤ 120 KB gz; total ≤ 400 KB gz; CodeMirror and uPlot are lazy chunks.

**Tests (write first):** App: unauthenticated → Login rendered with `next`; needs-setup → Setup; 401 from any call → `stopLive` and Login once; banners appear for each `live` state. StackDetail: loads and renders tabs; `?tab=logs` mounts only LogsViewer (others absent); `change` for the project refetches; unavailable with parse error → raw-only mode; `onact` opens the operation; busy reason on buttons; remove success navigates home; unmount closes streams (spy on `close`).

**Acceptance:** `npm run check && npm test && npm run build` green including the bundle check; manual run with the Go backend: every route works after a browser refresh (deep links), at 375 px wide and at 1440 px, keyboard-only through create → up → logs.

---

### Task 23: E2E smoke, README, accessibility/responsive audit

**Wave:** 6. Depends on T22.

**Files:** create `cmd/stackstead/e2e_test.go` (`//go:build e2e`); modify `README.md` (UI section: build with `make build`, dev loop with `npm run dev` + `go run`, first-run setup now done in the browser — update the "Until the web UI exists" paragraph —, Prometheus token workflow from the Settings page, password change, keyboard/a11y notes, the `/assets` 404 and cache behaviour; API table rows for `POST …/services/{service}/preview`, `GET /api/settings/metrics-token`, `POST /api/password`, and the `id:`/`Last-Event-ID` cursor contract of the logs stream; a one-line "CI: deferred — `make test` needs no Node, `make build`/`make e2e` do"); `docs/superpowers/plans/2026-10-14-stackstead-05-ui.md` (tick boxes only).

**E2E smoke behaviour** (Go test, no Docker, no new modules):
1. Skip unless `web.Built()`; build the binary into `t.TempDir()` with `go build` (`CGO_ENABLED=0`).
2. Start it with `STACKSTEAD_LISTEN=127.0.0.1:0`-style free port (pick a free port first, pass it), temp `DATA_DIR`/`STACKS_DIR`, `STACKSTEAD_DOCKER_SOCKET=<tempdir>/nonexistent.sock`; wait for `/api/setup/status` to answer; kill on cleanup; capture stderr and assert it contains "listening" and does not contain "not built".
3. Assert: `GET /` → 200 `text/html`, CSP header, body has `<div id="app"` and at least one `/assets/*.js` script; every `src`/`href` under `/assets/` → 200 with a JS/CSS content type and the immutable `Cache-Control`; `/assets/missing.js` → 404; `/stacks/anything` and `/settings` → the same `index.html`; `/api/me` → 401 JSON; `POST /api/setup` from loopback → 204; `POST /api/login` → cookie; `GET /api/me` with it → 200; `GET /api/status` → `docker: unavailable`; `GET /api/settings/metrics-token` → `{"configured": false}`; `POST /api/password` with a wrong current → 403, with the right one → 204 and the cookie still works.
4. If `STACKSTEAD_E2E_CHROME` is set: run `<chrome> --headless=new --disable-gpu --no-sandbox --virtual-time-budget=10000 --dump-dom http://127.0.0.1:<port>/` with a 60 s timeout and assert the DOM contains `autocomplete="username"` (the login form rendered by Svelte, i.e. scripts executed) and no text "not been built".

**Audit checklist (manual, recorded in the task report):** keyboard-only pass through setup → login → create → up → logs → edit → save → apply → settings → logout; VoiceOver or NVDA announces status badges and toasts; 375 px width shows no horizontal scroll on every route; dark scheme has readable contrast on banners and badges (≥ 4.5:1 for text); `prefers-reduced-motion` honoured; Lighthouse accessibility ≥ 95 on the list and detail pages.

**Acceptance:**
- `make check` green (Go + UI).
- `make build` then `make e2e` green locally, once with `STACKSTEAD_E2E_CHROME` set.
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` green with the built UI; the binary size is recorded in the report.
- Manual smoke with the real binary and Docker: create an nginx:alpine stack from the UI, watch `up` stream, see it running in the list without refreshing, tail its logs while curling it, see metrics after 30 s, edit the memory limit via the form with the diff preview, Apply, rotate the metrics token and scrape `/metrics` with curl, remove the stack.

---

## Self-review notes

- **Spec coverage:** §5.1 create (T13, T14, T21), §5.2 import (T21), §5.3 form/raw edit + Apply + hash conflicts + `.bak` note (T2, T16, T20), §5.4 live status (T5, T12, T22), §5.5 logs (T7, T17), §5.6 metrics (T8, T18), §6 banners/degraded/raw-only/validation/compose stderr (T9, T16, T20, T22), §7 security (T1 headers, T4 CSRF model, guards test), §8 Vitest + no Playwright (T3, T23), first-run setup and login (T11), Prometheus token shown once (T19), `/assets` 404 (T1), embed without Node (T1), lifecycle streaming + cancel + running indicator (T15, T22).
- **Type consistency:** every component and function name used across tasks is defined in exactly one task's Interfaces block; later tasks reference them by those names (`openSSE`, `createLogBuffer`, `createCoalescer`, `live`, `changes`, `refreshStatus`, `ApiError`, `request`, `api`, `toForm`/`fromForm`/`validateForm`/`mapServerErrors`, `DiffView`, `OperationPanel`, `ActionsBar`, `ServiceForm`, `FileBrowser`, `NetworkPicker`).
- **Beyond the spec, at the user's request:** password change (T2c, T19), metrics-token status (T2b, T19), exact log resume (T2b, T17).
- **Not covered on purpose:** multi-user, alerts, registry credentials (out of v1); CI configuration (deferred). Logs are not searchable server-side (spec: in-browser).
