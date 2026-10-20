# Building and development

Back to the [README](../README.md).

## Build

```bash
make build     # build the UI (Node >= 22), then bin/stackstead
make build-go  # the host binary without the UI step (no Node needed)
make release   # the same for linux/amd64 and linux/arm64
```

Node is needed only to build the UI (`make ui`, `make build`, `make release`).
The built UI is embedded from `web/dist`; without it the binary still builds
(`go build ./cmd/stackstead`) and serves a placeholder page saying the UI was
not built, and logs a warning.

## Make targets

| Command | Needs | What it does |
| --- | --- | --- |
| `make ui` | Node >= 22 | build the web UI into `web/dist` and enforce the bundle budgets (entry JS <= 120 kB gzipped, total JS <= 400 kB gzipped, fonts <= 200 kB) |
| `make build` | Node >= 22, Go | UI, then the static binary in `bin/` |
| `make test` | Go only | Go tests with the race detector; no Node |
| `make check` | Node, Go | `go vet`, gofmt, UI type-check and unit tests, Go tests |
| `make build-go` | Go only | the host binary without building the UI |
| `make e2e` | Go (UI for the web part) | smoke tests against the real binary (see below) |

## Fonts and theme

The UI uses IBM Plex Sans (Regular, Medium, SemiBold) and Geist Mono (Regular,
Medium), self-hosted as Latin-subset woff2 files in `web/src/assets/fonts/`.
Nothing is loaded from a third-party origin. Both families are under the SIL
Open Font License 1.1; the licence texts sit next to the fonts and are served
at `/licenses/IBMPlexSans-OFL.txt` and `/licenses/GeistMono-OFL.txt` (from
`web/public/licenses/`). The files come from the Fontsource npm packages: fetch
them with `npm pack` (for example `npm pack @fontsource/ibm-plex-sans`) and copy
the `latin` woff2 files, so no npm dependency is added. `make ui` fails when the
built `dist/assets/*.woff2` files exceed `FONT_BUDGET` (200 kB, in
`web/scripts/check-bundle.mjs`).

`web/public/` holds the files served as they are, at the site root:

- `theme.js`: a classic script loaded first in `<head>` that sets
  `data-theme` and `data-density` on `<html>` from `localStorage` before the
  first paint. It is served with `Cache-Control: no-cache`. A `?theme=dark` or
  `?theme=light` query overrides the theme for that page load only and is never
  saved; the e2e render uses it.
- `favicon.svg`, `logo-art.png` and `containers.png`: the favicon, the artwork
  of the login and setup pages, and the logo in the navigation rail.
- `licenses/`: the two font licences.

## Dev loop

Run the backend and the Vite dev server side by side. The dev server proxies
`/api` to the backend.

```bash
STACKSTEAD_DATA_DIR=$PWD/.dev/data STACKSTEAD_STACKS_DIR=$PWD/.dev/stacks go run ./cmd/stackstead serve
cd web && npm run dev
```

`npm run dev:app` serves the React UI (the future replacement) at `app.html`
beside the Svelte one, with the same proxy. `npm run build:app` builds it and
runs the same bundle budget check into `web/dist-app/`, which is git-ignored
and not embedded in the binary until the cutover.

React tests live beside their components as `web/src/app/**/*.test.tsx` and run
with the rest under `make ui-test`; `npm run check:app` type-checks the React
tree. The React entry budget is enforced by `npm run build:app`: only the shell,
the stores and the Overview screen are eager, every other screen and the
command palette are route-level `React.lazy` chunks, and `src/bundle.test.ts`
pins that shape (run it after a build) so a stray static import fails early.

The React overview is the only eager screen. Its data comes from the shared
`stacks`, `host` and `runningOps` stores plus the dashboard controller
(alerts, metrics summary, storage), and its events from the live `events` store;
the only filter state is the query; no store is mirrored in React state. After
the side column landed the entry measured 102.6 kB gz (ceiling 115, gate 110),
so the side column stays in the entry and nothing inside the screen is lazy.

The React stack detail is a lazy chunk keyed on the stack name. Its data comes
from the `lib/stacks/detail.ts` controller (detail, operations, health, compose),
the health timeline from `/health`, and the live CPU and memory totals from one
shared `metrics/live` stream acquired by viewport width or the Metrics tab. The
Operations pane lists the stack's operations and streams the selected one
through `lib/stacks/operation.ts`. With the detail shell landed the entry
measured 104.9 kB gz (baseline 104.7 after the react/react-router split, ceiling
106).

In-flight operations are tracked by `lib/stores/operationTracker.ts`: every entry
point that starts one calls `trackOperation`, and the tracker resolves the
toast from `runningOps` plus one `GET /api/operations/{id}`. A stack removal is
the exception: the stack page reports its outcome itself ("Removed x" or the
failure), so it is not tracked and shows a single toast.

The stack page's log console keeps one log stream per open stack page (tail 200,
follow). It opens once the stack has loaded and closes when the page is left.
Only a service change (chip, pane `Logs` button or `?tab=logs`) or Reconnect
restarts it. A collapsed console keeps the stream running, and a parse error
appearing after load unmounts the console and closes it. The console height
persists under `stackstead.logsHeight`. `?tab=logs` (and the service pane's
`Logs` button) expands the console on the chosen service.

The stack detail's Metrics tab (four per-service charts) and the compose editor
load uPlot and CodeMirror on first use; both stay lazy chunks (`uPlot.esm`,
`cm`) pinned out of the entry by the shape test in `web/src/bundle.test.ts`. The
Config tab slices the service's block from the raw compose file
(`composeText.serviceBlock`) and falls back to the formatted config. `Edit YAML`
(from the Config tab or the Compose file item) opens the compose editor, which
reports `dirty` to the screen; the screen alone feeds `setUnsavedChanges`, and
every in-page navigation (rail, chips, timeline, tabs, header actions) goes
through `guarded()`, so leaving with unsaved edits asks first.

The service editors of the stack detail live under `web/src/app/stacks/`
(`ServiceForm`, `FileBrowser`, `NetworkPicker`, `CreateNetworkDialog`; shared with
New stack and meant to be reused by the container and network screens of Plans
20 and 22) and `web/src/app/detail/` (`ServiceEditor` behind `Edit`,
`AddServicePanel` behind `+ Add` on the rail and chips). The state machines in
`lib/stacks/serviceEdit.ts` and `serviceAdd.ts` own the data, validation and
conflict handling; the React components only render them. Each editor reports a
`dirty` flag to `StackDetailScreen` (`serviceDirty` while a service is being
edited, `addDirty` while the add panel is open); together with the compose
editor's flag they feed the one `unsavedChanges` store, and every navigation
goes through `guarded()`. Opening the add panel, the editor or another item
closes the others, `discardAndLeave` closes both, and the pane's `Logs` button
is not guarded because it leaves the editor open. The editors add nothing to the
entry; the add panel and service editor ride in the lazy `StackDetail` chunk.

The React containers screens live under `web/src/app/containers/`. The list is
the unchanged `lib/containers/list.ts` controller through `useContainerList`;
actions share `useContainerAction` and one confirm dialog per page (also behind
the `r` key). The inline peek opens one `useLogStream` with `tail: 8` per open
row and closes it when the row closes, is hidden by a filter, or the page is
left; the detail's Logs tab reuses `LogConsole` with a container source. Both
screens ride in the lazy `ContainerList` / `ContainerDetail` chunks, and
`bundle.test.ts` pins that nothing of theirs is eager (no eager chunk named
after them, entry at most 106 kB gz, and no container-screen strings in the
eager chunks). Because Rolldown stops merging entry-reachable shared chunks into
the entry once two lazy routes share modules (about +2 kB gz of glue),
`vite.app.config.ts` has an eager `codeSplitting` group that puts every module
reachable from the entry into one `app` chunk, which keeps the entry under that
106 kB ceiling.

The React New stack and Import screens live under `web/src/app/create/`. The New
stack form state is `useCreateForm` (today's card identities, validation and
error mapping through `lib/compose/form.ts`); the port-conflict verdicts and the
preview text are the pure `lib/stacks/portCheck.ts` and
`lib/stacks/createPreview.ts`. The check reads the live containers list through
`useContainerList`, is advisory and never blocks a request (the server stays
authoritative). The preview is `serviceConfigYaml` over `fromForm` in a `<pre>`,
not CodeMirror. The shared `ServiceForm` gained the optional `advanced`
disclosure, `portNote` and volume `Browse…` props; the stack-detail editors use
neither of the first two. The New stack form feeds the one `unsavedChanges`
store, and `Start now` tracks the `up` operation through `trackOperation`.
Import is a one-field form over `api.importStack` with the shared `FileBrowser`
in file mode. Both screens ride in the lazy `StackCreate` / `StackImport`
chunks, and `bundle.test.ts` pins that nothing of theirs, nor `ServiceForm`,
`FileBrowser`, `NetworkPicker` or `cm`, is eager.

The React storage screens live under `web/src/app/storage/`. The Storage screen
reads today's `lib/storage/controller.ts` through `useStorageRefresh` (one
controller per mounted screen, `useStore` for its stores) and the pure
`lib/storage/usage.ts` (kind table, `?tab=` parsing with `layers` added and
unknown values selecting Images, bar shares, the lead summary over
`reclaimableTotal`, largest-first sort). The usage bar segments and the kind
tiles both select the kind through the URL, and the table card announces the
switch. Chips, search, 50-row paging and the protection rules are
`lib/storage/storage.ts`, unchanged. Every delete and prune goes through the
ported `CleanupDialog` (plan preview, typed phrase, inline `OperationPanel`),
which emits the single end toast itself and is therefore the second exception
to `trackOperation`. The image and volume pages are keyed on their param, share
`DetailSections`, and keep the dialog outside their data branches so a refresh
to 404 / 503 never unmounts a running cleanup. The three screens ride in the
lazy `Storage` / `StorageImage` / `StorageVolume` chunks, and `bundle.test.ts`
pins that nothing of theirs, nor the dialog, `usage.ts` or `OperationPanel`, is
eager.

The React Networks, Settings and auth screens live under
`web/src/app/networks/`, `app/settings/` and `app/auth/`. Networks reads
today's `createNetworkList` through `useNetworkList` and renders cards (built-ins
fold their containers behind today's toggle), reuses the shared
`CreateNetworkDialog` and confirms removal through `ConfirmDialog` with a once
guard. Settings is the five cards on the existing `live`, `session` and
`appearance` stores, with `PasswordForm` and `UpdateCard` ported one-to-one and
the one-time metrics token held only in component state (never in a `value`
attribute). The password inputs are uncontrolled (read through refs): a
controlled input mirrors what is typed into the DOM `value` attribute, where
page scripts, extensions and DOM snapshots can read it, which Svelte's
`bind:value` never did; the Login, Setup and password-change tests assert the
typed password never appears in the markup. Login and Setup share `AuthCard`,
stay outside the shell (mounted by `App` from the session state), title
themselves, pass `?next=` through `safeNext`, and the session guard never
carries `/setup` as `next`. Settings `Log out` keeps today's direct `logout()`
(it lands on `/login?next=%2Fsettings`, as before; unify with the rail in Plan
23). The four screens ride in the lazy `NetworkList` / `Settings` / `Login` /
`Setup` chunks, and `bundle.test.ts` pins that nothing of theirs, nor
`AuthCard`, `PasswordForm`, `UpdateCard` or `CreateNetworkDialog`, is eager.
With this plan every screen of the spec is React; Plan 23 switches the entry.

## Tests

```bash
go test ./...
go test -tags integration -race -timeout 300s ./...   # needs a running Docker; creates and removes itest*/stackstead-it-* resources
```

`make e2e` builds the binary, starts it on a free port with temporary data
and stacks directories, and drives it over HTTP: serving the embedded UI and
its assets, setup, login, metrics token and `/metrics`, and the password
change. It also creates, starts, observes (logs ids, metrics history) and
removes a throwaway `e2e-smoke-*` stack when Docker is available, and renders
the login page in headless Chrome when a Chrome/Chromium is found (set
`STACKSTEAD_E2E_CHROME` to choose one). Those two parts skip cleanly when
Docker or Chrome is missing. The render covers both themes (`?theme=dark` and
`?theme=light`) at 375x812 and 1280x800: it takes a screenshot of each, checks
`data-theme` in the DOM dump and that the page background pixel equals `--bg`
of the theme. The e2e also checks that `/theme.js` is served as JavaScript with
`no-cache` and that a font preloaded by `index.html` is served as `font/woff2`.

The same target runs the CLI end to end (`cmd/stackstead/cli_e2e_test.go`):
it builds the binary with a version stamp and drives it as separate processes
against a real server: `version`, `config`, `db`, `admin`, `login`, `status`,
`stacks`, `settings`, token rotation from both sides, backup and a refused
then successful restore, `health` and the exit codes, then scans every
captured output for the password, session and tokens. With Docker it also runs
a throwaway `e2e-cli-*` stack through `stacks up|show|down|rm`, `logs` and
`metrics`. It never touches other containers, and uses its own ports.

**CI: deferred.** There is no CI configuration yet. `make test` needs no
Node; `make build` does. `make e2e` does not run Node itself but needs the UI
to be built already (`make ui`); without it the web smoke test skips (the CLI
tests do not need it).

## Design documents

Design specs for features not built yet live in `docs/superpowers/specs/`. Specs and plans for shipped features are removed once implemented; find them in git history (`git log --diff-filter=D -- docs/superpowers`).
