# Stackstead update notice and `stackstead upgrade` — Design

**Date:** 2026-10-18
**Status:** Draft for review. Replaces the abandoned self-update design on branch `feat/self-update` (`docs/superpowers/specs/2026-10-17-stackstead-self-update-design.md` there). Builds on `2026-10-14-stackstead-cli-design.md` and `2026-10-14-stackstead-installers-design.md`. Baseline: `main` at `5099f12` (v0.2.0).

## 1. Purpose

Operators have no way to learn from Stackstead that a newer release exists, and upgrading means remembering which installer they used and its exact invocation. The previous attempt solved this with an in-browser updater (root helper daemon, IPC socket, job journal, probation and rollback: 37 k lines on `feat/self-update`). This design keeps the two things operators actually need and drops everything else:

1. **Settings tells the truth about updates.** The server checks the fixed release source once a day; the Settings page shows "up to date", "update available" (current → latest, release notes link) or "could not check", and the exact terminal command for *this* machine, with a copy button.
2. **One command upgrades.** `sudo stackstead upgrade` resolves the newest stable release, downloads the right asset for the way Stackstead was installed, verifies it strictly against the release's `SHA256SUMS` (the existing distribution trust contract), and runs the matching native installer: `install.sh` for script installs, `apt-get`/`dnf` for `.deb`/`.rpm` files, `installer -pkg` for the macOS package. `stackstead upgrade --check` only reports.

Success criteria:

- Settings never claims "up to date" without a successful check and never shows an invented command; failures say why in one sentence and still show the command.
- On a host installed by any of the four installers, the command shown in Settings upgrades to the newest stable release, or exits with a specific non-zero code and nothing changed.
- A corrupted or tampered download is refused before anything on the host is touched.
- No new daemon, socket, privileged helper, browser-triggered job or release-build change.

## 2. Scope

### In
- `internal/update`: release resolution from the fixed source, `SHA256SUMS` parsing and verification, version comparison, installer-type detection, per-installer apply commands, the periodic checker.
- `GET /api/settings/update`, `POST /api/settings/update/check`; `apitypes.UpdateStatus`.
- `stackstead upgrade [--check] [--version vX.Y.Z] [--force] [--yes] [--base-url URL] [-o json]` (Host admin group).
- Config key `update_check` (default `true`), env `STACKSTEAD_UPDATE_CHECK`.
- Settings card `UpdateCard.svelte`.
- Docs: `docs/cli.md`, `docs/install.md`, `docs/api.md`, `docs/security.md`, README, `deploy/config.example.yaml`, `deploy/release/RELEASING.md` (asset-name stability note).
- Tests: Go unit tests, web unit tests, CLI e2e (`--check`), Docker container tests for the script, deb and rpm upgrade paths, a manual native checklist on the Lima VMs.

### Out
- Updating from the browser in any form (no POST that installs, no job, no progress).
- Root helper daemon, IPC socket, LaunchDaemon, systemd updater unit, maintainer-script lifecycle hooks, installation IDs or locators.
- Automatic rollback, database snapshots taken by the updater, startup probation, write freezes. The native installer's own behaviour applies (§7).
- Release-build changes: no `stackstead-update.json` manifest, no build-tag identity, no mkrelease matrix checks. `upgrade` consumes the assets releases already ship.
- Scheduled automatic installs, channels other than stable, a Go client method or a remote `stackstead status` field (follow-up if wanted).
- Arbitrary release URLs or versions from the browser; the browser can only ask the server to re-check.
- Publisher signing (releases stay unsigned; `SHA256SUMS` guards integrity, not authenticity, exactly as today).

## 3. Key decisions

The global rules name a TypeSafe decision scorer; the installed `typesafe-ai` skill is an AI-feature SDK, not a scorer, so these were decided on the evidence in the code, with no scores claimed (same note as the Plan 10 specs).

| Decision | Choice | Reason |
|---|---|---|
| Release lookup | `GET <base>/releases/latest/download/SHA256SUMS`, version parsed from the tarball names; no `api.github.com` | Exactly what `install.sh` already does (`resolve_and_download`): GitHub's `latest` is the newest non-draft, non-prerelease; no API rate limit (60/h anonymous) to manage; one 1–2 kB document per check. |
| Stable only | The version read from `latest` must be plain `X.Y.Z`; `--version` may pin any valid tag (including `-rc.N`) | Operators who type a tag asked for it; the automatic path never offers a pre-release. |
| Where the check runs | In the server (background goroutine): 10 s after start, then every 24 h; 1 h after a failure. `POST .../check` forces one, at most once per 60 s | The browser cannot (CSP allows only the same origin) and should not (every tab would hit GitHub). Settings load reads the cached result, so it is instant and offline-safe. |
| Opt-out | `update_check: false` (`STACKSTEAD_UPDATE_CHECK=false`) | Air-gapped hosts and privacy: one outbound HTTPS GET a day to github.com is disclosed in `security.md`. Default on, because the feature is otherwise invisible. |
| Dev builds | A non-release `version` (`dev`, `dev-dirty`, `v0.2.0-3-gabcdef`) is state `unknown`; the checker does not fetch; `upgrade` needs `--force` | A dev binary cannot be compared and usually is not an installed service; developers should not generate daily GitHub traffic from `go run`. |
| One command, one flag for read-only | `stackstead upgrade` installs; `stackstead upgrade --check` reports and needs no root | "One new subcommand" per the user; `--check` mirrors `install.sh --dry-run`'s "download nothing, change nothing" role. |
| Command text | `sudo <exe> upgrade` with the server's canonical binary path, e.g. `sudo /usr/local/bin/stackstead upgrade`; macOS script installs: `<exe> upgrade` without `sudo` | RHEL/Rocky leave `/usr/local/bin` out of sudo's `secure_path` (install.md already warns). On macOS the script install is per-user and `install.sh` asks for sudo itself only for the prefix; the `.pkg` needs root for `installer`. The path is the server's own `os.Executable()`, never input. |
| Script installs run the release's `install.sh` | The asset for a script install is `install.sh` from the pinned release, verified against that release's `SHA256SUMS`, run with `--version vX.Y.Z --prefix <prefix>` | `install.sh` already does the upgrade correctly (unit rewrite, restart only if active, macOS kickstart, config untouched). Downloading the tarball twice is avoided; `install.sh` verifies the tarball itself. No tar extraction code is needed. |
| Package installs run the package manager | `.deb` → `apt-get install -y <file>`; `.rpm` → `dnf install -y <file>` (or `rpm -U <file>` when `dnf` is absent); `.pkg` → `installer -pkg <file> -target /` | There is no apt/dnf repository; local files are supported by all three. `apt-get` (not `apt`) because its CLI is stable for scripts. |
| Downgrade and reinstall | Refused (exit 5) unless `--force`; `--force` maps to `install.sh --force`, `apt-get --allow-downgrades --reinstall`, `rpm -U --oldpackage --replacepkgs`; the `.pkg` reinstalls anyway | Same rule as `install.sh`. `--force` warns about the schema risk (§7). |
| Confirmation | Terminal prompt `Upgrade v0.2.0 → v0.3.0 with apt-get? The service restarts. [y/N]`; `--yes` skips; no terminal and no `--yes` → exit 2 | The `stacks rm` convention. |
| Download location | `os.MkdirTemp` dir 0755, files 0644, `O_EXCL`, removed on every exit path | Release artifacts are public; apt downloads as `_apt` and warns when it cannot read the file. Only root can write in the dir, so verify-then-use is safe. |
| Error text to the browser | Fixed codes (`network`, `timeout`, `http`, `malformed`) and version strings; never URLs, paths or Go error text | Lesson kept from the old `api/update.go`: server error strings can carry paths. |
| CSRF for the POST | The existing rule: session cookie (`SameSite=Strict`) plus `Content-Type: application/json` and a JSON body (`decodeJSON`) | Same as every other mutation (`POST /api/settings/metrics-token`). The marker header and Origin check from the old branch are not carried over: a forced check is a rate-limited outbound GET, and a divergent mechanism for one route is not worth it. |
| Testing installers | Docker containers (existing `test/installer` harness) for script, deb and rpm; exec seams for the pkg path; never on the developer's Mac; a manual pass on the Lima VMs before merge | User policy. |

## 4. Release source and verification (`internal/update`)

### 4.1 Source

```go
type Source struct{ /* baseURL, *http.Client, userAgent */ }
func NewSource(baseURL string) (*Source, error)
func (s *Source) Latest(ctx context.Context) (Release, error)
func (s *Source) Release(ctx context.Context, version string) (Release, error)
type Release struct { Version string; Sums []byte; base string }
func (r Release) NotesURL() string
func (r Release) AssetName(kind Kind, goos, goarch string) (string, error)
func (s *Source) Download(ctx context.Context, r Release, name, dir string) (string, error)
```

- `DefaultBaseURL = "https://github.com/rcss-labs/stackstead"`. `NewSource` accepts `https://` or plain `http://` to `127.0.0.1`, `localhost`, `[::1]` only, no userinfo (the `check_base_url` rule of `install.sh`). The server always uses the default; `--base-url` exists on the CLI for tests and mirrors, and prints the same "non-default release source" warning as the script.
- HTTP client: 20 s timeout per request, `CheckRedirect` allowing only `https` to `github.com`, `objects.githubusercontent.com`, `release-assets.githubusercontent.com` (salvaged from the old `releaseRedirect`, which also strips `Authorization`/`Cookie` on redirect), at most 10 hops. Documents capped at 1 MiB, assets at 512 MiB (`io.LimitReader`, over-limit is `ErrMalformed`). `User-Agent: stackstead/<version>`.
- `Latest`: fetch `<base>/releases/latest/download/SHA256SUMS`; the version is the `X.Y.Z` between `stackstead_` and `_<os>_<arch>.tar.gz` of any tarball line (same awk as `install.sh`); it must parse and be stable (`ErrNotStable` otherwise). `Release(version)`: fetch `<base>/releases/download/v<version>/SHA256SUMS`.
- `NotesURL`: `https://github.com/rcss-labs/stackstead/releases/tag/v<version>` for the default source, `""` for any other.
- `AssetName` (salvaged `officialAssetName`): script → `install.sh`; deb → `stackstead_<ver>_<arch>.deb`; rpm → `stackstead-<ver>-1.<x86_64|aarch64>.rpm`; pkg → `stackstead_<ver>_darwin.pkg`. `RELEASING.md` gains a sentence that these names are a contract `upgrade` relies on.
- `Download`: the name must be listed in `Sums` (`ErrAssetMissing`); stream to `<dir>/<name>` (`O_CREATE|O_EXCL`, 0644) through `sha256`; on mismatch delete the file and return `ErrChecksum`; `fsync` and return the path. No partial file ever leaves `dir`, and `dir` is removed by the caller on every path.

### 4.2 `SHA256SUMS` parsing

`ChecksumFor(sums []byte, name string) (string, error)` is the old `checksumFor` verbatim: `<64 hex><space><space|*><name>` per line, no `/`, `\`, whitespace or control characters in names, no duplicates, lower-cased digest; anything else is `ErrMalformed`.

### 4.3 Versions

`Parse(s string) (Version, error)` strips one leading `v`, requires `X.Y.Z` (no leading zeros, ≤ 10 digits each), accepts `-<prerelease>` identifiers and ignores `+build`; a `-N-g<hex>` suffix (git describe after a tag) parses as `Post=true`. `dev`, `dev-dirty`, empty and anything else return `ErrNotRelease`. `Compare(a, b)`: core numerically; then rank pre-release < release < post-tag; pre-releases by SemVer identifier rules; post-tag by `N`. This matches `install.sh`'s `ver_cmp` so the script and the command agree on "newer". `IsStable()` = no pre-release and not post-tag.

### 4.4 Installer detection

```go
type Kind string // "script" | "deb" | "rpm" | "pkg"
func Detect(goos, exe string, exists func(string) bool) Kind
func UpgradeCommand(kind Kind, goos, exe string) string
```

Salvaged with the review lessons: darwin and `/var/db/receipts/io.github.rcss-labs.stackstead.bom` → `pkg`; linux and `/var/lib/dpkg/info/stackstead.list` → `deb`; linux, `exe == /usr/bin/stackstead` and (`/var/lib/rpm` or `/usr/lib/sysimage/rpm`) → `rpm`; else `script`. `exe` is `os.Executable()` after `EvalSymlinks`. `UpgradeCommand` returns `sudo <exe> upgrade`, or `<exe> upgrade` for darwin+script; `exe` is shell-quoted when it is not `[A-Za-z0-9_./+:@%=-]+` (salvaged `shellQuote`). The server also refuses to publish a command that is not printable ASCII ≤ 512 bytes (empty string instead).

### 4.5 Apply

```go
type Plan struct { Kind Kind; Version, Asset, Prefix, BaseURL string; Force bool }
type Runner func(ctx context.Context, name string, args, env []string) error
func Commands(p Plan) (argv [][]string, env []string)   // pure
func Apply(ctx context.Context, p Plan, run Runner) error
```

| Kind | Command | Notes |
|---|---|---|
| script | `sh <Asset> --version v<Version> --prefix <Prefix> [--force] [--base-url <BaseURL>]` | `Prefix` is `exe` minus `/bin/stackstead` (`/usr/local`); any other layout is `ErrLayout`. `install.sh` re-fetches `SHA256SUMS` and the tarball from the same pinned release and verifies them itself. On Linux without systemd it prints its "start it under your own supervisor" note; `upgrade` relays it. |
| deb | `apt-get install -y [--allow-downgrades --reinstall] <Asset>`; env adds `DEBIAN_FRONTEND=noninteractive` | Before running, a non-blocking `fcntl` write lock probe on `/var/lib/dpkg/lock-frontend` (what apt does); held → `ErrBusy`, nothing run. |
| rpm | `dnf install -y <Asset>` when `dnf` is in `PATH`, else `rpm -U <Asset>`; with `Force`: `rpm -U --oldpackage --replacepkgs <Asset>` | `dnf` waits for its own lock and says so; `upgrade` relays. |
| pkg | `installer -pkg <Asset> -target /` | The package's `postinstall` hands the LaunchAgent setup to the console user as today; with nobody at the console it prints the "finish by hand" line, relayed verbatim. |

`Apply` streams the child's stdout/stderr to the terminal, uses fixed argv (no shell), and does not pass `ctx` to the child once it has started: a Ctrl-C reaches the installer through the terminal's process group as usual, and `upgrade` waits for it and reports its exit (killing `dpkg` or `installer` mid-transaction is worse than finishing).

## 5. The checker (server side)

```go
type Checker struct{ /* source, now, installer kind, exe, current version, state */ }
func NewChecker(o CheckerOptions) *Checker        // Source, Now, Logger, Enabled, Current, Kind, Exe
func (c *Checker) Run(ctx context.Context)         // periodic loop; returns on ctx
func (c *Checker) Status() apitypes.UpdateStatus   // cached, never blocks on the network
func (c *Checker) CheckNow(ctx context.Context) apitypes.UpdateStatus
```

- `Run`: if disabled or `Current` is not a release version, return at once (nothing to do; `Status()` reports `disabled` / `unknown`). Otherwise wait 10 s, check, then sleep 24 h after a success or 1 h after a failure; the clock is injected (`Now` plus a `Sleep`/timer seam) so tests do not wait.
- `CheckNow`: if the last attempt (success or failure) was < 60 s ago, return `Status()` unchanged; otherwise perform one check synchronously (bounded by the 20 s request timeout) and return the new status. One check runs at a time (mutex); concurrent callers get the result of the one in flight.
- A check = `Source.Latest(ctx)` → compare with `Current`. Success records `latest`, `notesURL`, `checkedAt = now`, clears `error`. Failure records `error` as one of `network` (dial/DNS/TLS), `timeout`, `http` (non-200), `malformed` (bad sums, no tarball name, invalid or unstable version) and keeps the last successful `latest`/`checkedAt`.
- Logging: `Info "update available" version=` once per distinct latest version; the first failure of a streak at `Warn` with the Go error, following ones at `Debug` (offline hosts must not fill the journal daily).
- `state` derivation (also used by `upgrade --check -o json`): `disabled` when off; `unknown` when `Current` is not a release; `available` when the last successful check found a newer version; `up-to-date` when it found none; `failed` when no check has ever succeeded and the last one failed. `error` is set whenever the most recent attempt failed, including on `available`/`up-to-date` from an earlier success.

## 6. API

| Method and path | Purpose |
| --- | --- |
| `GET /api/settings/update` | the cached update status (session) |
| `POST /api/settings/update/check` | force a check (session, JSON body `{}`); answers the status; checks less than 60 s apart answer the cached status |

`apitypes.UpdateStatus` (JSON tags, sorted: `checkedAt,command,current,error,installer,latest,notesURL,state`):

```json
{
  "current": "0.2.0",
  "installer": "deb",
  "command": "sudo /usr/bin/stackstead upgrade",
  "state": "available",
  "latest": "0.3.0",
  "notesURL": "https://github.com/rcss-labs/stackstead/releases/tag/v0.3.0",
  "checkedAt": "2026-10-18T09:12:03Z",
  "error": ""
}
```

- `current` is `version.Get().Version` without the `v` (the raw string for dev builds). `latest`, `notesURL` are `""` and `checkedAt` is omitted (`omitempty`, zero time) until a check succeeds. `error` is one of the four codes or `""`.
- Both routes exist only when `api.Options.Updates` is set (always in `serve`; tests may leave it nil). Unknown JSON fields are rejected like everywhere else. Errors are `{error}` with the usual statuses (401, 415, 400).
- The server trusts nothing from the client: there is no body field that selects a version, URL or installer.

## 7. `stackstead upgrade`

```
stackstead upgrade [--check] [--version vX.Y.Z] [--force] [--yes] [--base-url URL] [-o json]
```

Host admin group. No `--config`: the command needs no configuration file (the backup tip names the default config path form). `--check` is incompatible with `--force` and `--yes` (exit 2). `-o json` applies to `--check` (it prints `apitypes.UpdateStatus`, the same shape as the API, with `installer`, `command` and `state` filled from this host) and to the final line of a successful upgrade.

Flow (every step that can fail says what it was doing and changes nothing):

1. Detect `kind` and `exe` (§4.4); `current` from `version.Get()`.
2. Resolve: `Latest` or `Release(--version)` (exit 7 unavailable, 6 malformed/unstable, 2 for a `--version` that is not `vX.Y.Z[-pre]`).
3. Compare. Same version → `stackstead v0.3.0 is already installed` exit 0 (unless `--force`). Installed newer → exit 5 `installed v0.4.0 is newer than v0.3.0; use --force to downgrade`. Dev build → exit 5 `this is a development build (dev-dirty); use --force to install v0.3.0 over it`.
4. `--check` stops here: prints `installed`, `latest` (with notes URL), `installer`, `update: available — run: <command>` / `up to date`; exit 0 in both cases (7/6 only when the source could not be read).
5. Privileges: Linux needs euid 0 (exit 3: `needs root: run sudo <exe> upgrade`); darwin+pkg needs euid 0; darwin+script accepts a plain user or `sudo` with `SUDO_USER` set (a root login without it is exit 3 with the same message `install.sh` gives).
6. Print the backup tip (`before upgrading you can back up the database: sudo -u stackstead <exe> db backup /var/backups/stackstead-<date>.db`; on macOS without `sudo -u`), then confirm (terminal prompt or `--yes`; neither → exit 2).
7. Download the asset into a fresh temp dir and verify (exit 6 on checksum mismatch or asset missing from `SHA256SUMS`, 7 on download failure); the dir is removed on every exit.
8. `Apply` (§4.5). `ErrBusy` → exit 5. Exit codes of `install.sh` map: 6 → 6, 5 → 7, 8 → 3, 130 → 130, anything else → 1 (`install.sh exited 7; see its output above`). `apt-get`/`dnf`/`rpm`/`installer` non-zero → exit 1 with the tool's name and exit status; their own output is on the terminal.
9. Print `upgraded stackstead v0.2.0 → v0.3.0` (plus `the service was restarted` only when the installer said so; `install.sh` prints "Stackstead is running" for that case and the package scripts `try-restart` silently, so the text is `the installer restarted the service if it was running`).

Exit codes reuse `docs/cli.md`'s table: 0 success or nothing to do; 1 installer or package manager failed, I/O; 2 usage, no terminal without `--yes`; 3 needs root; 5 downgrade or reinstall without `--force`, dpkg lock held; 6 verification (checksum, missing asset, malformed `SHA256SUMS`, unstable latest); 7 release source unreachable; 130 interrupted.

### What restarts, and what the operator should know

Verified on `main`:

- `install.sh` upgrade: `systemctl daemon-reload` then `restart` only if the unit was active (never re-enables a disabled unit); macOS `launchctl kickstart -k` (bootout + bootstrap when the plist changed); no systemd → prints the supervisor note and starts nothing.
- `.deb` `postinst configure <old>`: `try-restart`. `.rpm` `%post 2`: `try-restart`. A stopped or disabled service stays that way.
- `.pkg` `postinstall` → `install.sh --from-pkg` as the console user → `kickstart -k`.

So `stackstead upgrade` never restarts anything itself. When it runs on the server's own host, the server restarts under it; the Settings page reconnects on its own (the SSE store already retries), and the card's copy says so.

Database: `serve` runs `store.Open`, which applies pending migrations one per transaction at startup and refuses a database whose schema is newer than the binary (`SchemaMismatchError`). There is no automatic pre-migration backup. Consequences the docs and the command state plainly: an upgrade migrates forward on the first start and cannot be undone by reinstalling the older binary; after a `--force` downgrade across a schema change the service refuses to start until the database from before the upgrade is restored (`db restore`). Hence the backup tip in step 6 and a louder warning when `--force` downgrades.

## 8. Frontend

`web/src/lib/settings/UpdateCard.svelte`, mounted in `Settings.svelte` directly under "System status". Loads `GET /api/settings/update` on mount; a **Check now** button posts `/check` (disabled while in flight; hidden when `state` is `disabled`).

| `state` | Heading pill | Body |
|---|---|---|
| loading | — | spinner |
| `up-to-date` | `up to date` (success) | `Stackstead v0.2.0 is the latest release. Checked 3 h ago.` If `error` is set: `The last check failed (<sentence>); showing the result from 3 h ago.` |
| `available` | `update available` (accent) | `v0.2.0 → v0.3.0`, **Release notes** link (`safeNotesURL`: https + `github.com` host only, `rel="noopener noreferrer"`), then the command in a `<pre>` with `CopyButton`, then `The service restarts; this page reconnects by itself. Back up first if you want to be able to roll back: ...`. |
| `failed` | `check failed` (danger) | `Could not reach the release source (<sentence>). Nothing is known about newer releases.` plus the command and the note `You can still upgrade from the terminal.` |
| `disabled` | `checks off` (muted) | `Update checks are disabled (update_check: false).` plus the command. |
| `unknown` | `dev build` (muted) | `This is a development build (dev-dirty); versions cannot be compared.` plus the command. |
| request error (GET failed) | `check failed` | the `ApiError` message and a Retry button, like the token card. 401 is handled app-wide. |

Sentences for `error` codes live in `updateState.ts` (`network` → "no route to github.com", `timeout` → "the release source did not answer in time", `http` → "the release source answered with an error", `malformed` → "the release listing could not be read"); unknown codes get a generic sentence, never the raw slug (salvaged `readable`). The command is rendered as text only (`{status.command}`), no `{@html}`. "Checked N ago" uses the existing relative-time helper in `web/src/lib/format.ts` if one fits, else a 10-line local one.

Accessibility: the card is a `section` with `aria-labelledby`; the pill is text, not colour alone; the copy button is the existing `CopyButton` (announces "Copied"). Fits 375 px: the command `<pre>` scrolls horizontally inside the card.

## 9. Configuration

`update_check: true` in `config.Config` (`UpdateCheck bool`, YAML `update_check`), env `STACKSTEAD_UPDATE_CHECK` accepting `true|false|1|0` (anything else is a load error naming the variable, like the duration keys). `internal/config/template.yaml` and `deploy/config.example.yaml` gain a commented block:

```yaml
# Once a day the server fetches the latest release's SHA256SUMS from
# https://github.com/rcss-labs/stackstead to show "update available" in Settings.
# Set to false on air-gapped hosts. Nothing is ever installed automatically.
# update_check: true
```

## 10. Security

- Outbound traffic: one HTTPS GET of `SHA256SUMS` per day (plus forced checks, ≥ 60 s apart) to the fixed origin, with `User-Agent: stackstead/<version>`. Disclosed in `security.md` with the opt-out. No other data is sent.
- The browser cannot pick a version, URL, installer or command. The command string is built server-side from `os.Executable()` and fixed words, checked to be printable ASCII, and displayed as text.
- `upgrade` trusts: TLS to the fixed origin (or an explicit `--base-url`, warned about), `SHA256SUMS` of the pinned release, and the native installer. This is the same contract as `install.sh` today and still does not authenticate the publisher; the docs keep saying so.
- Downloads: `O_EXCL` files in a fresh root-owned 0755 temp dir; verification before any use; the dir is removed on success, failure and interrupt. Nothing is extracted; nothing is executed from a user-writable prefix as root (the old macOS `/usr/local` ownership lesson): the only script run is the freshly downloaded, verified `install.sh` in the temp dir.
- Redirect policy and size caps (§4.1) bound what a compromised or misbehaving mirror can do: at most a wrong file that fails the checksum.
- `upgrade` needs root for Linux and the macOS `.pkg`; it refuses otherwise with the exact command instead of trying `sudo` itself. It never runs a shell; argv is fixed.
- Log and terminal output from the installer is relayed raw (it is the operator's own terminal); API output is fixed codes.

## 11. Testing

Per the user's policy: test-first for behaviour, focused tests that catch a distinct regression, no tests of test helpers, no installer ever runs on the developer's Mac.

- **`internal/update` (unit):** version parse/compare table (incl. `v0.2.0-3-gabc` > `v0.2.0` > `v0.2.0-rc.1`, `dev` → `ErrNotRelease`); `ChecksumFor` strictness (salvaged cases: `*` marker, duplicate, path in name, short line); `Source.Latest` against `httptest` (version from tarball names, unstable latest refused, non-200 → `ErrUnavailable`, oversize → `ErrMalformed`, redirect to a foreign host refused, `--base-url` validation); `Download` (checksum mismatch deletes the file, missing asset refused before any request, size cap); `Detect` table (pkg receipt, dpkg list, rpm db at both paths, fallthrough); `UpgradeCommand` table incl. quoting; `Commands(Plan)` table for the four kinds with and without `Force`, `DEBIAN_FRONTEND` in env, `dnf` fallback; `Checker` with a fake clock and source (disabled never fetches, dev build never fetches, 24 h / 1 h cadence, `CheckNow` 60 s gate, error codes, `available` only after success, last success kept across a failure).
- **`internal/config`:** key default, YAML `false`, env `0`/`false`, bad env value error.
- **`internal/apitypes`:** `TestJSONTagsMatchContract` row.
- **`internal/api`:** GET needs a session; POST needs JSON (415) and rejects unknown fields; status body shape; `Updates` nil → 404 like other absent routes; a status whose `command` is not printable ASCII is published as `""`.
- **`internal/cli` (`cmd_upgrade_test.go`, seams for euid, exec, terminal, source URL):** `--check` text and JSON against `httptest` (available, up to date, source down → 7, unstable → 6); same version exit 0 and no exec; downgrade exit 5, `--force` proceeds; dev build exit 5; non-root exit 3 with the exact command; no terminal without `--yes` exit 2 and no download; `install.sh` exit mapping table; temp dir removed after failure; pkg plan runs `installer -pkg <tmp>/stackstead_X_darwin.pkg -target /` through the seam only.
- **Web:** `client.test.ts` paths; `UpdateCard.test.ts`: one test per state in §8, `Check now` posts and re-renders, error-code sentences never show the slug, non-https notes URL not linked, GET failure shows Retry, copy button text equals `command`. `Settings.test.ts` gets a default fake route for `GET /api/settings/update`.
- **CLI e2e (`cmd/stackstead/cli_e2e_test.go`):** real binary, `upgrade --check -o json --base-url <httptest>` exit 0 with `state: available`; `upgrade` as non-root exit 3 (Linux/macOS, nothing executed).
- **Installer containers (`test/installer/upgrade_test.go`, tag `installer`, Docker):** the installed v1 is the **real** binary (`buildRealServer`, already used by the systemd smoke) so it has `upgrade`; v2 may be the fast fake. Script path on debian (fake systemd: `restart stackstead` logged, config and data kept, version v2); deb on ubuntu via `apt-get` (packages built around the real v1 binary; `try-restart` logged); rpm on rocky (`rpm -U` fallback, since the image has `microdnf` only); checksum mismatch → exit 6 and version unchanged; same version → exit 0; downgrade → 5 then `--force` → 0; non-root → 3; dpkg lock held (`flock` in the background) → 5; `--check` as non-root → 0. The fake release gains `.deb`/`.rpm` assets in `SHA256SUMS`.
- **Native, manual, before merge (Lima):** `stackstead-debian12` (script and `.deb` via real apt), `stackstead-alma9` (`.rpm` via real `dnf`), `stackstead-alpine` (script without systemd: supervisor note), `stackstead-macos15` (script as user, `.pkg` via `sudo`), each against a local release directory served in GitHub's layout. Recorded in the handoff, not automated.

## 12. Documentation

- `docs/cli.md`: `upgrade` in the Host admin table and a short section (flags, what runs per installer, restart behaviour, schema note, exit codes mapping).
- `docs/install.md`: the Upgrade section leads with `sudo stackstead upgrade` (per-platform lines, macOS without sudo for script installs), keeps the manual ways, adds "from 0.2.0 use the installer once (no `upgrade` yet)", the downgrade/schema caveat and the backup tip.
- `docs/api.md`: the two routes and a note under the session/JSON rule.
- `docs/security.md`: the daily check, what is sent, `update_check: false`.
- `README.md`: one line in Install ("Upgrade later with `sudo stackstead upgrade`") and the Settings feature mention.
- `deploy/release/RELEASING.md`: asset names are a contract for `upgrade`; check `stackstead upgrade --check` on a host after publishing.
- `deploy/config.example.yaml`, `internal/config/template.yaml`: the key.

## 13. Salvaged from `feat/self-update` and what is not carried over

Carried over (trimmed, re-homed in `internal/update`): `checksumFor` (`verify.go`); `stableVersion` plus `newerVersion` as the core of §4.3; the redirect allowlist, header stripping and document/artifact size caps of `release.go`; `officialAssetName`; `detectInstaller` and `shellQuote` (`cmd_update.go`); `safeNotesURL` and the "readable sentence, never the slug" rule (`updateState.ts`); the fixed-error-code idea and the printable-ASCII command check (`api/update.go`).

Not carried over, with the reason: updater and bootstrap binaries, `internal/updatehost`, `internal/updateipc`, `internal/updaterelease`, `tools/mkupdate` (no privileged helper exists); `stackstead-update.json` manifest, build-tag identity (`a70c46c`), mkrelease partial/complete matrix and stale-binary diagnostics (no release-build change is needed: `upgrade` reads the assets releases already have, and `make dist` already refuses stale binaries); helper registration, locators, installation IDs, LaunchDaemon and updater unit, maintainer-script lifecycle hooks, Debian/RPM transaction preservation (no managed lifecycle); jobs, phases, journal, probation, rollback budget, recovery, `update status|recover` (no browser-triggered install); write freeze / maintenance admission; POST begin/cancel routes, `X-Stackstead-Request` marker and Origin check (existing CSRF rule suffices for a rate-limited check); tar extraction (`extract.go`) and payload validation (nothing is unpacked: `install.sh` is the asset for script installs); UI job polling, localStorage resume hints, confirm dialog; DB snapshot/rollback integration (§7 explains the real behaviour instead).

## 14. Open points for the user

1. Default `update_check: true` (one HTTPS GET a day to github.com, disclosed in security.md) — or default off?
2. `stackstead upgrade --check` rather than a separate `stackstead update check` command — acceptable?
3. Command text uses the absolute binary path (`sudo /usr/local/bin/stackstead upgrade`) for `secure_path` safety — or the shorter `sudo stackstead upgrade` with a footnote?
4. `--force` downgrades for packages (`apt-get --allow-downgrades`, `rpm -U --oldpackage`) — keep, or print the manual package-manager command and stop (even simpler)?
5. No automatic backup before an upgrade; a printed tip only — confirmed?
