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
