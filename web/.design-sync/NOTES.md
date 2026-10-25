# design-sync notes (stackstead web)

- Run everything from `web/`. The design system is `src/app/components` inside the app; there is no library build.
- `buildCmd` (`node .design-sync/build-pkg.mjs`) generates `.ds-pkg/` (gitignored): a barrel `index.ts`, `tsc` declarations in `types/`, and `global.css` (tokens.css + base.css concatenated). Run it before every converter/driver run, because it's the only thing that picks up component source changes.
- Converter invocation: `--node-modules ./node_modules --entry ./.ds-pkg/index.ts --out ./ds-bundle`. `--entry` is required: without it the converter looks for `node_modules/stackstead-ui` and crashes with ENOENT.
- Tokens ship inside `_ds_bundle.css` via `cssEntry`, not `tokens/`. `tokensGlob` only reads from a tokens npm package, and `cssEntry` must sit under the package dir (`.ds-pkg/`), so tokens.css is concatenated into `global.css`.
- Fonts: `extraFonts` points at `src/app/styles/fonts.css`. IBM Plex Sans and Geist Mono woff2 ship in `fonts/`.
- Component CSS modules go through esbuild's `local-css` (hashed class names) and land in `_ds_bundle.css`.
- Groups come from `docsMap` stubs in `.design-sync/groups/*.md` (frontmatter `category` only). A new component needs a `docsMap` entry, or it lands in `general`.
- `ErrorBoundary` is excluded (`componentSrcMap: null`) because it renders nothing visual.
- Dialog and ConfirmDialog use `cardMode: single` with a fixed viewport so the portal renders inside the card.
- Playwright: the cached chromium build 1248 matches `playwright@1.64.0` (installed into `.ds-sync/`). Run validate/capture with `NODE_PATH=.ds-sync/node_modules`.

## Known render warns
- None open. A harness heuristic flags any cell whose text *starts* with `⚠` as an error cell. A title-only `Banner kind="warning"` trips it, so the TitleOnly cell uses `kind="info"`.

## Re-sync risks
- `build-pkg.mjs` picks up every non-test `.tsx` in `src/app/components`. A new non-component helper file there becomes an export; exclude it with `componentSrcMap: { Name: null }`.
- The tokens.css + base.css concatenation is a copy taken at build time. Always rerun `buildCmd` before syncing, or tokens go stale.
- `RelativeTime` previews compute timestamps from `Date.now()`, so the rendered text is stable but the absolute times aren't.
- `Led pulse` is captured mid-animation, so the screenshot shows a faded LED. That's expected.
- The conventions header lists every token family by name. If tokens.css gains or renames tokens, re-validate the header.
