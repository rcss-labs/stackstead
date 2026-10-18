# Stackstead Plan 11 — Update notice and `stackstead upgrade`

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** this plan specifies *what* to build: files, signatures, behaviour, test cases and acceptance criteria. It deliberately contains no full implementations; implementers write the code test-first. Short snippets show shapes only.

**Goal:** Settings shows whether a newer stable Stackstead exists (current → latest, release notes, the exact terminal command for this machine with a copy button), and one new command, `stackstead upgrade`, performs the upgrade on the host through the native installer after strict `SHA256SUMS` verification. Nothing installs from the browser; no daemon, socket or helper is added.

**Architecture:**
- `internal/update` (new, ~6 files): version parsing/comparison, `SHA256SUMS` parsing, release `Source` (fixed GitHub origin, same `releases/latest/download/SHA256SUMS` lookup as `install.sh`), verified `Download`, installer `Detect`/`UpgradeCommand`, native `Commands`/`Apply`, and a `Checker` the server runs once a day.
- `internal/api/update.go`: `GET /api/settings/update`, `POST /api/settings/update/check` over `Options.Updates`.
- `internal/cli/cmd_upgrade.go`: `upgrade [--check] [--version] [--force] [--yes] [--base-url] [-o json]`.
- `internal/config`: `update_check` (default on).
- `web/src/lib/settings/UpdateCard.svelte` + `updateState.ts`, mounted in `Settings.svelte`.

**Tech Stack:** Go 1.26 stdlib only (`net/http`, `crypto/sha256`, `os/exec`, `syscall` for the dpkg lock probe); Svelte 5, TypeScript, Vitest. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-18-stackstead-update-notice-design.md` (§ references below). Branch `feat/update-notice` from `main` `5099f12`. The abandoned branch `feat/self-update` is read-only reference; salvage is listed in spec §13.

## Decisions (controller, recorded for the user)

Spec §3 holds the product decisions; §14 lists five points the user should confirm (default-on check, `--check` flag vs a second command, absolute binary path in the command text, `--force` downgrades for packages, no automatic backup). Plan-level decisions:

- **No tar extraction anywhere.** Script installs run the release's own `install.sh` (verified asset), which downloads and verifies the tarball itself. The old `extract.go` is not salvaged.
- **`apitypes.UpdateStatus` is the one DTO**: the API body, the checker's `Status()`, and `upgrade --check -o json` all print it, so there is one contract test row and one TypeScript interface.
- **The checker never fetches for dev builds** (`version.Get().Version` not parseable as a release): state `unknown`. `upgrade --check` fetches regardless (the operator asked); `upgrade` needs `--force`.
- **Error codes, not messages, cross the API** (`network|timeout|http|malformed`); sentences live in the UI.
- **The dpkg frontend-lock probe is the only package-manager lock handling.** `dnf` waits for its own lock and says so; `rpm -U` fails with its own text (exit 1).
- **`apt-get`, not `apt`**; `dnf` with `rpm -U` fallback (the rocky test image ships `microdnf` only, so containers exercise the fallback and the Lima `alma9` VM exercises `dnf`).
- **Container tests install the real binary as v1** (`buildRealServer` from `systemd_test.go`) because v1 must have the `upgrade` command; v2 can be the fast fake, which answers `version --short`.
- **Settings tests get a default `GET /api/settings/update` fake route** so the new card does not change their outcome.
- **TypeSafe AI skill:** the installed `typesafe-ai` skill is an AI-feature SDK, not a decision scorer; agents proceed without it and say so in handoffs (same as Plans 10a/10b).

## Global Constraints

- Spec §1 success criteria and §10 security: the browser cannot choose a version, URL, installer or command; the server's command string is built from `os.Executable()` and fixed words only, checked printable ASCII ≤ 512 bytes; the checker's HTTP client has the redirect allowlist and size caps of §4.1; downloads are `O_EXCL`, verified before use, and the temp dir is removed on every exit path.
- Exit codes come from `internal/cli/exit.go` constants only (spec §7 table); `install.sh` exit codes are mapped (6→6, 5→7, 8→3, 130→130, other→1).
- Never run `install.sh`, `apt-get`, `dnf`, `rpm` or `installer` from a Go unit test or on the developer's Mac: all unit tests go through the `Runner`/exec seams; only the `installer`-tagged container tests execute real installers, inside Docker.
- Test-first for every behaviour change: write the test, run it, see it fail for the expected reason, implement, pass, commit. No tests for test helpers.
- No `{@html}`; server strings are interpolated as text. The notes link is rendered only when `safeNotesURL` accepts it.
- `go test ./...` passes without Node and without network (every Go test uses `httptest` or a fake); `CGO_ENABLED=0 GOOS=linux GOARCH={amd64,arm64} go build ./...` and `GOOS=darwin go build ./...` pass (no cgo, no platform-only imports outside `_linux.go`/`_darwin.go` files; the dpkg probe uses `syscall.FcntlFlock`, available on both).
- `make check` and `make ui` pass at the end of every task that touches their inputs; bundle budgets unchanged (the card adds no dependency).
- Files not touched: `install.sh`, `deploy/pkg/*`, `deploy/macos/*`, `deploy/nfpm.yaml`, `Makefile` release targets, `tools/mkrelease` (spec §2 Out: no release-build change).
- Commits: conventional, explain why, and end with exactly these trailer lines:
  ```
  Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01Sa7ZQwLC7Fduu8fF8hAwfs
  ```

## Review Focus

1. **Honesty of the card.** `up-to-date` appears only after a successful check; a failed check with an earlier success keeps the earlier result *and* shows the failure sentence; no state ever hides the command. (T9 `UpdateCard.test.ts`; T5 `TestCheckerKeepsLastSuccessAcrossFailure`, `TestCheckerAvailableOnlyAfterSuccess`.)
2. **Nothing changes before verification.** Checksum mismatch, missing asset, malformed `SHA256SUMS`, unstable latest, non-root, no `--yes` without a terminal: each exits with its code, runs no installer, and leaves no file outside the removed temp dir. (T2 `TestDownloadChecksumMismatchRemovesFile`; T8 `TestUpgradeRefusals*`, `TestUpgradeTempDirRemovedOnFailure`; T10 `TestUpgradeChecksumMismatchInContainer`.)
3. **The right native installer with the right argv.** Table in T4 (`TestCommands`) is the contract; T10 proves it on debian (script), ubuntu (apt-get), rocky (rpm -U); pkg argv is asserted through the seam only (T8 `TestUpgradePkgPlanRunsInstaller`).
4. **No input reaches a command line.** `--version` is validated by `update.Parse`; `--base-url` by `NewSource`; argv lists are fixed slices; the server command string is derived, not received. (T1 `TestUpgradeCommandQuotesOddPaths`; T6 `TestUpdateStatusDropsUnprintableCommand`; T8 `TestUpgradeRejectsBadVersionFlag`.)
5. **Traffic discipline.** Disabled or dev build → zero requests; 10 s / 24 h / 1 h cadence; `CheckNow` ≤ 1 fetch per 60 s; one fetch per check. (T5 `TestCheckerDisabledNeverFetches`, `TestCheckerCadence`, `TestCheckNowRateLimited`.)
6. **Restart and schema story is documented, not invented.** `upgrade` restarts nothing itself; docs state what each installer does (spec §7) and the `--force` downgrade schema caveat. (T11 docs review.)

## Out of scope

Everything in spec §2 Out: browser-triggered installs, helper daemon/IPC/LaunchDaemon, rollback/snapshots/probation, release-build changes, auto-install schedules, non-stable channels, a Go client method or `stackstead status` field, publisher signing, a rail badge for "update available", `update_check` interval tuning, proxy settings beyond the Go default `HTTP(S)_PROXY` environment.

## File Structure

New:
- `internal/update/version.go`, `version_test.go` — `Version`, `Parse`, `Compare`, `IsStable`.
- `internal/update/sums.go`, `sums_test.go` — `ChecksumFor`, `VerifyFile`.
- `internal/update/installer.go`, `installer_test.go` — `Kind`, `Detect`, `Executable`, `UpgradeCommand`, `shellQuote`, `prefixOf`.
- `internal/update/source.go`, `source_test.go` — `Source`, `Release`, `Latest`, `Release(version)`, `AssetName`, `NotesURL`, `Download`, errors.
- `internal/update/apply.go`, `apply_test.go`, `dpkglock.go` — `Plan`, `Runner`, `Commands`, `Apply`, `dpkgFrontendLocked`.
- `internal/update/checker.go`, `checker_test.go` — `Checker`, `CheckerOptions`.
- `internal/api/update.go`, `update_test.go` — routes, `UpdateAPI`.
- `internal/cli/cmd_upgrade.go`, `cmd_upgrade_test.go`.
- `web/src/lib/settings/UpdateCard.svelte`, `UpdateCard.test.ts`, `updateState.ts`, `updateState.test.ts`.
- `test/installer/upgrade_test.go`.

Modified:
- `internal/apitypes/types.go`, `types_test.go` — `UpdateStatus` + contract row.
- `internal/config/config.go`, `config_test.go`, `template.go`, `template.yaml`, `template_test.go`; `deploy/config.example.yaml`.
- `internal/api/server.go` — `Options.Updates`, route registration.
- `internal/cli/serve.go` — build and run the checker; `internal/cli/commands.go` — register `upgrade`.
- `web/src/lib/api/types.ts`, `client.ts`, `client.test.ts`; `web/src/routes/Settings.svelte`, `Settings.test.ts`.
- `test/installer/harness_test.go` (`releaseOpts.Packages`), `package_test.go` (package builder takes the binary builder).
- `cmd/stackstead/cli_e2e_test.go`.
- Docs: `docs/cli.md`, `docs/install.md`, `docs/api.md`, `docs/security.md`, `README.md`, `deploy/release/RELEASING.md`.

## Parallelism

| Wave | Tasks (independent files) |
|---|---|
| 0 | T1 (`internal/update` pure parts + apitypes), T3 (config), T9 (web card; works from the spec's DTO) |
| 1 (after T1) | T2 (source/download), T4 (apply), T6 (API routes, with a fake `UpdateAPI`) |
| 2 | T5 (checker; after T2), T8 (CLI; after T2 and T4) |
| 3 | T7 (serve wiring; after T3, T5, T6), T10 (container tests + e2e; after T8) |
| 4 | T11 (docs, final checks, Lima pass) |

Each task is one commit. Tasks in the same wave touch disjoint files and can run in separate worktrees.

---

### Task 1: `internal/update` foundations and the DTO

**Files:** `internal/update/version.go`, `version_test.go`, `sums.go`, `sums_test.go`, `installer.go`, `installer_test.go`; `internal/apitypes/types.go`, `types_test.go`. Spec §4.2–4.4, §6.

**Shapes:**

```go
type Version struct { Major, Minor, Patch uint64; Pre []string; Post uint64; IsPost bool }
var ErrNotRelease = errors.New("not a release version")
func Parse(s string) (Version, error)
func Compare(a, b Version) int
func (v Version) IsStable() bool
func (v Version) String() string            // "0.3.0", "0.3.0-rc.1"

var ErrMalformed = errors.New("malformed SHA256SUMS")
var ErrAssetMissing = errors.New("asset not listed in SHA256SUMS")
func ChecksumFor(sums []byte, name string) (string, error)
func VerifyFile(path, wantHex string) error  // ErrChecksum on mismatch

type Kind string
const (KindScript Kind = "script"; KindDeb = "deb"; KindRPM = "rpm"; KindPkg = "pkg")
func Detect(goos, exe string, exists func(string) bool) Kind
func Executable() string                      // os.Executable + EvalSymlinks; "stackstead" on error
func UpgradeCommand(kind Kind, goos, exe string) string
func prefixOf(exe string) (string, error)     // "/usr/local/bin/stackstead" → "/usr/local"; ErrLayout otherwise

// apitypes
type UpdateStatus struct {
    Current string `json:"current"`; Installer string `json:"installer"`; Command string `json:"command"`
    State string `json:"state"`; Latest string `json:"latest"`; NotesURL string `json:"notesURL"`
    CheckedAt time.Time `json:"checkedAt,omitempty"`; Error string `json:"error"`
}
```

Behaviour: spec §4.2 (strict line format, lower-cased digest), §4.3 (`-N-g<hex>` is post-tag and newer than its tag; `+build` ignored; leading zeros and >10 digits rejected; `dev`, `dev-dirty`, `""` → `ErrNotRelease`), §4.4 (receipt/dpkg/rpm-db heuristics; `sudo <exe> upgrade`, darwin+script without `sudo`; quoting of odd paths).

- [ ] **Step 1: Write the failing tests** — `TestParseAndCompare` (table: `v0.2.0` < `0.3.0`; `0.3.0-rc.1` < `0.3.0`; `0.3.0` < `0.3.0-2-gabcdef` (post); `0.3.0-rc.2` > `0.3.0-rc.10`? no: numeric identifiers compare numerically so `rc.2` < `rc.10`; `1.0.0+build` == `1.0.0`; `01.0.0`, `1.0`, `dev`, `dev-dirty`, `""` → `ErrNotRelease`; `IsStable` false for pre and post), `TestChecksumFor` (salvaged cases: two-space and `*` separators accepted; short line, non-hex, name with `/`, duplicate name, `..` → `ErrMalformed`; absent name → `ErrAssetMissing`; digest returned lower-case), `TestVerifyFile` (ok, mismatch → `ErrChecksum`, missing file error), `TestDetect` (table: darwin + receipt → pkg; linux + dpkg list → deb; linux `/usr/bin/stackstead` + `/var/lib/rpm` → rpm; same with `/usr/lib/sysimage/rpm`; linux `/usr/local/bin/stackstead` + rpm db → script; dpkg list wins over rpm db; darwin without receipt → script), `TestUpgradeCommand` (linux script `/usr/local/bin/stackstead` → `sudo /usr/local/bin/stackstead upgrade`; linux deb → `sudo /usr/bin/stackstead upgrade`; darwin pkg → `sudo /usr/local/bin/stackstead upgrade`; darwin script → `/usr/local/bin/stackstead upgrade`), `TestUpgradeCommandQuotesOddPaths` (`/opt/my tools/bin/stackstead` → single-quoted), `TestPrefixOf` (`/usr/local/bin/stackstead` → `/usr/local`; `/opt/stackstead` → `ErrLayout`), `TestJSONTagsMatchContract` row `{"UpdateStatus", ..., "checkedAt,command,current,error,installer,latest,notesURL,state"}`.
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/update ./internal/apitypes -run 'Parse|Checksum|VerifyFile|Detect|UpgradeCommand|PrefixOf|JSONTags' -v` (compile errors on the new package first).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/update ./internal/apitypes`, `go vet ./...`, `gofmt -l .`, `GOOS=darwin go build ./...`.
- [ ] **Step 5: Commit** — `feat: update package foundations: versions, SHA256SUMS, installer detection` (why: the checker, the API and `upgrade` all need one agreed notion of "newer", one strict sums parser and one installer heuristic).

---

### Task 2: Release source and verified download

**Files:** `internal/update/source.go`, `source_test.go`. Spec §4.1. Depends on T1.

**Shapes:**

```go
const DefaultBaseURL = "https://github.com/rcss-labs/stackstead"
var (ErrUnavailable, ErrNotStable, ErrChecksum, ErrBadBaseURL error)
type Source struct{ /* base string; client *http.Client; ua string */ }
func NewSource(baseURL, userAgent string) (*Source, error)
func (s *Source) IsDefault() bool
func (s *Source) Latest(ctx context.Context) (Release, error)
func (s *Source) Release(ctx context.Context, version string) (Release, error)
type Release struct { Version Version; Sums []byte; base string }
func (r Release) NotesURL() string
func (r Release) AssetName(kind Kind, goos, goarch string) (string, error)
func (s *Source) Download(ctx context.Context, r Release, name, dir string) (path string, err error)
func ErrorCode(err error) string   // "network" | "timeout" | "http" | "malformed" | "" (for the checker and the CLI)
```

Behaviour: base URL rule (https, or http to loopback; no userinfo); client timeout 20 s; redirect policy (https only, allowed hosts, ≤ 10 hops, strip `Authorization`/`Cookie`); document cap 1 MiB, asset cap 512 MiB; `Latest` parses the version from a `stackstead_<ver>_<os>_<arch>.tar.gz` line and requires `IsStable`; `Release(v)` fetches the pinned `SHA256SUMS`; `AssetName` per spec table (`install.sh` for script); `Download` checks `ChecksumFor` before any request, writes `O_CREATE|O_EXCL` 0644, hashes while streaming, deletes on mismatch, `fsync`s on success; HTTP non-200 → `ErrUnavailable` wrapping the status; `context.DeadlineExceeded`/`net.Error.Timeout()` → code `timeout`.

- [ ] **Step 1: Write the failing tests** — `TestNewSourceRejectsBadBaseURL` (http to a non-loopback host, userinfo, `ftp://`), `TestLatestParsesVersionFromTarballNames` (httptest serving `/releases/latest/download/SHA256SUMS` with deb/rpm/pkg/tarball lines → `0.3.0`; `NotesURL` empty for a non-default base), `TestLatestRefusesPrerelease` (`0.3.0-rc.1` tarball names → `ErrNotStable`), `TestLatestMalformed` (no tarball line; invalid sums line → `ErrMalformed`), `TestLatestHTTPErrors` (500 → `ErrUnavailable`, code `http`; server closed → code `network`; slow handler with a short ctx → code `timeout`), `TestDocumentSizeCap` (1 MiB + 1 → `ErrMalformed`), `TestRedirectPolicy` (httptest redirecting to `http://evil.test` → error; to another path on the same test server over http is refused as non-https unless loopback — assert the loopback case passes), `TestReleasePinned` (`/releases/download/v0.2.5/SHA256SUMS` fetched; version echoed), `TestAssetName` (table of four kinds × arches incl. `aarch64`), `TestDownloadVerifies` (bytes equal, file mode 0644, path inside dir), `TestDownloadChecksumMismatchRemovesFile` (dir empty afterwards, `ErrChecksum`), `TestDownloadRefusesUnlistedAssetWithoutRequest` (request counter stays 0, `ErrAssetMissing`), `TestDownloadSizeCap` (small cap via an unexported seam), `TestDownloadRefusesExistingFile` (`O_EXCL`).
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/update -run 'Source|Latest|Release|AssetName|Download|Redirect|SizeCap' -v`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/update`, `go vet ./...`, `gofmt -l .`.
- [ ] **Step 5: Commit** — `feat: resolve and verify releases from the fixed source` (why: the same lookup and the same strict checksum rule as install.sh, so the command and the script never disagree about what "latest" is).

---

### Task 3: `update_check` configuration key

**Files:** `internal/config/config.go`, `config_test.go`, `template.go`, `template.yaml`, `template_test.go`; `deploy/config.example.yaml`. Spec §9. Independent.

**Shapes:** `Config.UpdateCheck bool \`yaml:"update_check"\``, default `true`; env `STACKSTEAD_UPDATE_CHECK` parsed with `strconv.ParseBool` semantics limited to `true|false|1|0` (error text names the variable); template line `{{.UpdateCheckLine}}` rendered as `# update_check: true` by default and `update_check: false` when set off (same `optional` helper as `docker_socket`), preceded by the three comment lines of spec §9; the same block in `deploy/config.example.yaml`.

- [ ] **Step 1: Write the failing tests** — `TestUpdateCheckDefaultsOn`, `TestUpdateCheckFromYAML` (`update_check: false`), `TestUpdateCheckFromEnv` (`0`, `false`, `TRUE`; `maybe` → error mentioning `STACKSTEAD_UPDATE_CHECK`), `TestTemplateUpdateCheckLine` (default commented; off → active line; the comment block present; rendered file loads back with `Load`).
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/config -run UpdateCheck -v`.
- [ ] **Step 3: Implement** (and mirror the block in `deploy/config.example.yaml`; `config validate` of the example must still pass: `go run ./cmd/stackstead config validate --config deploy/config.example.yaml`).
- [ ] **Step 4: Run** `go test -race ./internal/config`, `go vet ./...`, `gofmt -l .`.
- [ ] **Step 5: Commit** — `feat: update_check config key` (why: air-gapped and privacy-conscious hosts must be able to turn the daily release lookup off).

---

### Task 4: Native installer commands (`Apply`)

**Files:** `internal/update/apply.go`, `apply_test.go`, `dpkglock.go`. Spec §4.5. Depends on T1.

**Shapes:**

```go
type Plan struct { Kind Kind; Version Version; Asset, Prefix, BaseURL string; Force, HaveDNF bool }
type Runner func(ctx context.Context, name string, args, env []string) error
var ErrBusy = errors.New("package manager is busy")
var ErrLayout = errors.New("binary is not in a <prefix>/bin/stackstead layout")
func Commands(p Plan) (argv []string, extraEnv []string, err error)
func Apply(ctx context.Context, p Plan, run Runner, locked func() (bool, error)) error
func dpkgFrontendLocked() (bool, error)   // non-blocking F_SETLK write lock on /var/lib/dpkg/lock-frontend; absent file → false
```

Behaviour (spec §4.5 table): script → `sh <Asset> --version v<ver> --prefix <Prefix> [--force] [--base-url <BaseURL>]` (`--base-url` only when non-empty; `Prefix` empty → `ErrLayout`); deb → `apt-get install -y [--allow-downgrades --reinstall] <Asset>` with `DEBIAN_FRONTEND=noninteractive`, `Apply` calls `locked()` first and returns `ErrBusy` without running; rpm → `dnf install -y <Asset>` when `HaveDNF` else `rpm -U <Asset>`; with `Force` always `rpm -U --oldpackage --replacepkgs <Asset>`; pkg → `installer -pkg <Asset> -target /`. `Asset` must be absolute (else error). `Apply` hands `ctx` through unchanged; the CLI (T8) does not cancel a started child (it runs `exec.Command`, not `CommandContext`). `Commands` is pure so the table test is the contract.

- [ ] **Step 1: Write the failing tests** — `TestCommands` (table: the eight rows above, exact argv slices and env), `TestCommandsRejectsRelativeAsset`, `TestCommandsScriptNeedsPrefix` (`ErrLayout`), `TestApplyDebBusyRunsNothing` (fake `locked` → true; fake runner not called; `ErrBusy`), `TestApplyRunsOnce` (fake runner records exactly one call with the `Commands` argv), `TestApplyPropagatesRunnerError` (`*exec.ExitError` style error is returned unwrapped so the CLI can map it), `TestDpkgFrontendLockedAbsentFile` (temp path → false, nil), `TestDpkgFrontendLockedUnlocked` (existing unlocked file → false). The held case cannot be produced inside one Go process (fcntl locks are per process) and is covered by T10 `TestUpgradeDpkgLockBusyInContainer`.
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/update -run 'Commands|Apply|DpkgFrontend' -v`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/update`, `go vet ./...`, `gofmt -l .`, `GOOS=darwin go build ./...` and `GOOS=linux go build ./...`.
- [ ] **Step 5: Commit** — `feat: native installer commands for stackstead upgrade` (why: each install format already has a correct upgrader; the command's job is to pick it and hand it a verified file, never to reimplement it).

---

### Task 5: The checker

**Files:** `internal/update/checker.go`, `checker_test.go`. Spec §5. Depends on T1, T2.

**Shapes:**

```go
type CheckerOptions struct {
    Source *Source; Enabled bool; Current string; Kind Kind; GOOS, Exe string
    Now func() time.Time; After func(time.Duration) <-chan time.Time; Logger *slog.Logger
}
type Checker struct{ /* opts, mu, status apitypes.UpdateStatus, lastAttempt time.Time, loggedVersion string, failing bool */ }
func NewChecker(o CheckerOptions) *Checker
func (c *Checker) Run(ctx context.Context)
func (c *Checker) Status() apitypes.UpdateStatus
func (c *Checker) CheckNow(ctx context.Context) apitypes.UpdateStatus
const (initialDelay = 10*time.Second; successInterval = 24*time.Hour; failureInterval = time.Hour; minForcedGap = 60*time.Second)
```

Behaviour: spec §5 and the `state` derivation; `Status()` is a copy under the mutex; `Installer` and `Command` are filled from `Kind`/`UpgradeCommand` at construction (command blanked when not printable ASCII ≤ 512); `Current` is the version string with a leading `v` stripped; a `Run` on a disabled or dev-build checker returns immediately; one check at a time; `CheckNow` within `minForcedGap` of the last attempt returns the cached status; logging rules (Info once per distinct latest, Warn on the first failure of a streak then Debug).

- [ ] **Step 1: Write the failing tests** (fake `After` channel the test feeds, fake `Now`, `httptest` source with a request counter) — `TestCheckerDisabledNeverFetches` (`Run` returns; counter 0; state `disabled`; command still present), `TestCheckerDevBuildNeverFetches` (`Current: "dev-dirty"` → state `unknown`, `Current` echoed, counter 0), `TestCheckerCadence` (initial delay 10 s, then 24 h after success, 1 h after failure: assert the durations passed to `After` and the request count), `TestCheckerAvailableOnlyAfterSuccess` (before any attempt `Status()` is `state: "unknown"`, `error: ""`, no `checkedAt`; the first failure makes it `failed`; only a success makes it `available`/`up-to-date`), `TestCheckerUpToDate` (same version → `up-to-date`, `latest` set, `checkedAt` = fake now), `TestCheckerAvailable` (newer → `available`, `notesURL` for the default-shaped base; for the httptest base `notesURL` is `""`), `TestCheckerKeepsLastSuccessAcrossFailure` (success then 500 → state stays `available`, `error: "http"`, `checkedAt` unchanged), `TestCheckerFailureCodes` (network/timeout/http/malformed), `TestCheckNowRateLimited` (two calls 10 s apart → one request; 61 s apart → two), `TestCheckNowSerialised` (concurrent calls → one request), `TestCheckerLogsAvailableOnce` (slog handler capturing: one Info for `0.3.0`, none on the repeat), `TestCheckerFailureLoggedOnceAtWarn`.
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/update -run Check -v`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race -count=3 ./internal/update` (the fake clock must make it deterministic), `go vet ./...`, `gofmt -l .`.
- [ ] **Step 5: Commit** — `feat: daily update check with cached status` (why: Settings needs an instant, offline-safe answer, and GitHub must see one small request a day, not one per browser tab).

---

### Task 6: API routes

**Files:** `internal/api/update.go`, `update_test.go`, `server.go`. Spec §6. Depends on T1 (DTO); the test uses a fake, so it does not wait for T5.

**Shapes:**

```go
type UpdateAPI interface {
    Status() apitypes.UpdateStatus
    CheckNow(ctx context.Context) apitypes.UpdateStatus
}
// Options.Updates UpdateAPI — routes registered only when non-nil
func (s *server) registerUpdateRoutes(mux *http.ServeMux)   // GET /api/settings/update, POST /api/settings/update/check
func publishable(st apitypes.UpdateStatus) apitypes.UpdateStatus  // blanks Command when not printable ASCII ≤ 512; blanks NotesURL unless https://github.com/...
```

Behaviour: both routes behind `requireSession`; the POST uses `decodeJSON` into an empty struct (415 without JSON, 400 on unknown fields, `{}` accepted); the handler calls `CheckNow(r.Context())` and writes the status; errors never carry Go error text (the DTO has none). `Updates == nil` → the routes are absent (the catch-all 404 answers).

- [ ] **Step 1: Write the failing tests** (`internal/api/update_test.go`, `fakeUpdates` with a `checkCalls` counter) — `TestUpdateStatusRequiresSession` (401), `TestUpdateStatusBody` (JSON equals the fake's status; `checkedAt` omitted when zero), `TestUpdateCheckRequiresJSON` (415; `{}` → 200 and `checkCalls == 1`; unknown field → 400 and `checkCalls == 0`), `TestUpdateRoutesAbsentWithoutUpdates` (404 JSON from the catch-all), `TestUpdateStatusDropsUnprintableCommand` (fake command with `\x1b` → `""`), `TestUpdateStatusDropsForeignNotesURL` (`http://` or another host → `""`).
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/api -run Update -v`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/api`, `go vet ./...`, `gofmt -l .`.
- [ ] **Step 5: Commit** — `feat: update status and check routes` (why: the card needs a read endpoint that is always instant and a bounded way to ask for a fresh check).

---

### Task 7: Run the checker in `serve`

**Files:** `internal/cli/serve.go`, `serve_test.go` (if a wiring test is practical; otherwise the e2e covers it). Depends on T3, T5, T6.

**Behaviour:** build `update.NewChecker` with `Source` = `NewSource(update.DefaultBaseURL, "stackstead/"+version)`, `Enabled: cfg.UpdateCheck`, `Current: version.Get().Version`, `Kind: update.Detect(runtime.GOOS, exe, exists)`, `Exe: update.Executable()`, default clock; add `checker.Run` to `startBackground(...)`; set `api.Options.Updates: checker`. Log at Info on start: `update checks enabled|disabled`.

- [ ] **Step 1: Write the failing test** — in `cmd/stackstead/cli_e2e_test.go` (T10 owns the file; coordinate: T7 adds only this assertion) `GET /api/settings/update` with a session answers 200 with `state: "unknown"` (e2e binary version `e2e-test`), `installer: "script"` and a `command` ending in `stackstead upgrade`; with `STACKSTEAD_UPDATE_CHECK=false` the state is `disabled`. If T10 is in flight, put the assertion in a new `TestE2EUpdateStatus` function in the same file to avoid conflicts.
- [ ] **Step 2: Run to verify it fails** — `make ui` once, then `go test -tags e2e -count=1 -run 'TestE2E' ./cmd/stackstead/` (404 before wiring).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the e2e again, `go test -race ./internal/cli`, `go vet ./...`.
- [ ] **Step 5: Commit** — `feat: run the update checker in serve` (why: the routes are useless without a checker behind them; the opt-out must reach the goroutine).

---

### Task 8: `stackstead upgrade`

**Files:** `internal/cli/cmd_upgrade.go`, `cmd_upgrade_test.go`, `commands.go`. Spec §7. Depends on T1, T2, T4.

**Shapes:**

```go
func upgradeCommand() *Command          // Name "upgrade", Group "Host admin", Usage per spec §7, MaxArgs 0
type upgradeFlags struct { check, force, yes bool; version, baseURL string }
// Seams (package vars, like geteuid in local.go) so tests run with no root, no network beyond httptest and no installer:
// upgradeExecutable, upgradeExists, upgradeLookPath, upgradeLocked, upgradeRunner (default: exec.Command with
// stdout/stderr → env, never CommandContext), upgradeTempDir (default: os.MkdirTemp "stackstead-upgrade.").
func runUpgrade(ctx context.Context, env *Env, g *globalFlags, f upgradeFlags) error
func installerExit(err error) int       // install.sh exit → CLI exit (6→6, 5→7, 8→3, 130→130, else 1)
func checkStatus(cur string, kind update.Kind, goos, exe string, rel update.Release, err error) apitypes.UpdateStatus
```

Behaviour: the nine steps of spec §7; messages quoted there are the contract (tests match on them); `--check` prints the three-line text (`installed`, `latest` with notes URL when any, `update: available — run: <cmd>` / `update: none, v… is the latest`) or `-o json` of `apitypes.UpdateStatus` with `state` from the comparison (`available`, `up-to-date`, `unknown` for dev builds); `--version` must satisfy `update.Parse` (exit 2); `--base-url` must satisfy `NewSource` (exit 2) and prints the script's warning when non-default; the backup tip line; prompt via `prompt.go` helpers (`[y/N]`, default no → exit 0 with `aborted`); temp dir removed with `defer os.RemoveAll` and also on the signal path (cli.Main cancels ctx; `Download` honours it); after `Apply` the summary line; `-o json` on success prints `{"from","to","installer"}`.

- [ ] **Step 1: Write the failing tests** (`newTestEnv`, httptest source serving `SHA256SUMS` for `latest` and `v0.3.0`, a recorded fake runner, `geteuid` seam) — `TestUpgradeCheckText` (available: three lines incl. `run: sudo /usr/bin/stackstead upgrade` for a deb layout; up to date), `TestUpgradeCheckJSON` (`state`, `latest`, `installer`, `command`), `TestUpgradeCheckSourceDown` (exit 7), `TestUpgradeCheckUnstableLatest` (exit 6), `TestUpgradeCheckDevBuild` (`version.Version` seam `dev-dirty` → `state: unknown`, still fetches), `TestUpgradeCheckRejectsForceAndYes` (exit 2), `TestUpgradeRejectsBadVersionFlag` (exit 2, no request), `TestUpgradeRejectsBadBaseURL` (exit 2), `TestUpgradeSameVersionIsNoop` (exit 0, message, runner not called, no temp dir), `TestUpgradeRefusesDowngradeWithoutForce` (exit 5, message), `TestUpgradeRefusesDevBuildWithoutForce` (exit 5), `TestUpgradeNeedsRootOnLinux` (euid 1000 → exit 3, message contains the exact command), `TestUpgradeDarwinPkgNeedsRoot`, `TestUpgradeDarwinScriptAsUserOK` (euid 501, runner called), `TestUpgradeDarwinScriptRootWithoutSudoUser` (exit 3), `TestUpgradeNoTerminalNeedsYes` (exit 2, no download: request counter unchanged after sums), `TestUpgradePromptNoAborts` (stdin `n`, exit 0, runner not called), `TestUpgradeScriptPlan` (deb? no: script layout `/usr/local/bin/stackstead` → runner saw `sh <tmp>/install.sh --version v0.3.0 --prefix /usr/local --base-url <httptest>`; the file at `<tmp>/install.sh` had the served bytes; temp dir gone afterwards), `TestUpgradeDebPlan` (`apt-get install -y <tmp>/stackstead_0.3.0_amd64.deb`, env has `DEBIAN_FRONTEND=noninteractive`), `TestUpgradeRPMPlanWithAndWithoutDNF` (`upgradeLookPath` seam), `TestUpgradeForceDowngradePlans` (`--allow-downgrades --reinstall`; `rpm -U --oldpackage --replacepkgs`; `install.sh --force`), `TestUpgradePkgPlanRunsInstaller` (`installer -pkg <tmp>/stackstead_0.3.0_darwin.pkg -target /`), `TestUpgradeChecksumMismatchIsExit6` (served asset bytes differ from sums; runner not called; temp dir gone), `TestUpgradeAssetMissingIsExit6`, `TestUpgradeDownloadFailureIsExit7`, `TestUpgradeDpkgBusyIsExit5` (`upgradeLocked` seam true; runner not called), `TestInstallerExitMapping` (table), `TestUpgradePackageManagerFailureIsExit1` (runner returns an `*exec.ExitError`-like error with code 100 → exit 1 and message names `apt-get` and `100`), `TestUpgradeSuccessSummary` (text and `-o json`), `TestUpgradeTempDirRemovedOnFailure`, `TestUpgradeBaseURLWarning` (stderr line), `TestHelpListsUpgradeUnderHostAdmin` (extend the existing help test).
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/cli -run 'Upgrade|InstallerExit' -v`.
- [ ] **Step 3: Implement** (register in `commands.go` under "Host admin" after `health`).
- [ ] **Step 4: Run** `go test -race ./internal/cli`, `go test ./...`, `go vet ./...`, `gofmt -l .`, `GOOS=darwin go build ./...`.
- [ ] **Step 5: Commit** — `feat: stackstead upgrade` (why: one command that knows how this host was installed, verifies before touching anything and hands over to the native installer).

---

### Task 9: Settings card

**Files:** `web/src/lib/api/types.ts`, `client.ts`, `client.test.ts`, `web/src/lib/settings/updateState.ts`, `updateState.test.ts`, `UpdateCard.svelte`, `UpdateCard.test.ts`, `web/src/routes/Settings.svelte`, `Settings.test.ts`. Spec §8. Independent (contract from spec §6).

**Shapes:**

```ts
export type UpdateState = 'available' | 'up-to-date' | 'failed' | 'disabled' | 'unknown';
export interface UpdateStatus { current: string; installer: 'script' | 'deb' | 'rpm' | 'pkg'; command: string;
  state: UpdateState; latest: string; notesURL: string; checkedAt?: string; error: '' | 'network' | 'timeout' | 'http' | 'malformed' | string; }
api.updateStatus: () => get<UpdateStatus>('/api/settings/update');
api.checkUpdate: () => post<UpdateStatus>('/api/settings/update/check');
// updateState.ts
export function safeNotesURL(raw: string): string | undefined;      // https + hostname github.com only
export function errorSentence(code: string): string;                // four sentences + generic fallback, never the slug
export function describe(st: UpdateStatus, now: Date): { pill: string; tone: 'success' | 'accent' | 'danger' | 'muted'; headline: string; detail?: string };
```

Behaviour: the §8 table verbatim; `relativeTime` from `web/src/lib/format.ts` for "Checked 3 h ago"; "Check now" button (`loading` while in flight, hidden for `disabled`); the command in `<pre data-testid="upgrade-command">` + `CopyButton text={status.command}`; the backup/restart note under `available`; GET failure → `Banner` + Retry (same pattern as the token card); `unknown` with a release `current` but no `checkedAt` reads "Not checked yet" (initial state before the first check lands, spec §5/T5 decision).

- [ ] **Step 1: Write the failing tests** — `client.test.ts`: `updateStatus GETs /api/settings/update`, `checkUpdate POSTs /api/settings/update/check with {}`; `updateState.test.ts`: `safeNotesURL` (https github.com ok; http, other host, userinfo → undefined), `errorSentence` (four codes; `weird-slug` → generic, no slug substring), `describe` table for the five states ± error ± checkedAt; `UpdateCard.test.ts`: `up to date shows the version and checked time, and the command`, `available shows from→to, the notes link with rel noopener, the command and the restart note`, `available with a non-https notes URL renders no link`, `failed shows the sentence and still the command`, `disabled explains update_check and hides Check now`, `dev build explains and shows the command`, `an earlier success plus a new failure keeps the result and adds the failure sentence`, `Check now posts and re-renders the answer`, `GET failure shows the message and Retry reloads`, `copy button carries exactly the command`; `Settings.test.ts`: add the default fake route and one assertion that the card renders under System status.
- [ ] **Step 2: Run to verify they fail** — `cd web && npm test -- client updateState UpdateCard Settings`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `cd web && npm run check && npm test && npm run build` (budgets), and a visual pass at 375 px and 1280 px in both themes (`?theme=`), fixing anything that wraps badly.
- [ ] **Step 5: Commit** — `feat: Settings shows available updates and the upgrade command` (why: operators learn about releases where they already look, and get the exact command for their machine instead of a guess).

---

### Task 10: Container tests and CLI e2e

**Files:** `test/installer/upgrade_test.go` (new), `test/installer/harness_test.go` (`releaseOpts.Packages`), `test/installer/package_test.go` (generalise the package builder), `cmd/stackstead/cli_e2e_test.go`. Spec §11. Depends on T8 (and T7 for the e2e status assertion if not already added).

**Harness changes (no tests for these):**
- `releaseOpts.Packages map[string]string` — asset name → local file; `fakeRelease` copies each into `v<ver>/` and adds its `SHA256SUMS` line.
- `buildPackagesWith(t, versions []string, build func(t, r, arch, version) string) string` extracted from `testPackages` (which becomes `buildPackagesWith([]string{pkgV1, pkgV2}, buildFakeServer)` under its `Once`); the upgrade tests call it once with `buildRealServer` for v1 and `buildFakeServer` for v2 under their own `Once`.
- A `scenario` helper `upgradeAs(user string, args ...string)` running `runuser -u <user> -- <exe> upgrade ...` (util-linux is in every image) and `upgrade(args...)` as root, both with `--base-url http://<release host>` and `--yes`.

**Tests** (`//go:build installer`, each `t.Parallel()`, v1 = real binary at `0.0.1`, v2 = fake at `0.0.2`, `FakeSystemd: true`):
- `TestUpgradeScriptInContainer` (debian): install v1 with `install.sh --version v0.0.1`; seed config edit and data marker; `upgrade` → exit 0, `version --short` = `v0.0.2`, `restart stackstead` in the fake systemctl log, no `enable`, config edit kept, marker kept, `/tmp` holds no `stackstead-upgrade.*`.
- `TestUpgradeDebInContainer` (ubuntu, `pkgScenario`-style with real-v1 packages): `apt-get install ./v1.deb`; `upgrade` → `dpkg-query` reports `0.0.2`, `try-restart` logged, output contains `apt-get`.
- `TestUpgradeRpmInContainer` (rocky): `rpm -U v1.rpm`; `upgrade` → `rpm -q` reports `0.0.2` (fallback path, since no `dnf`); output contains `rpm -U`.
- `TestUpgradeChecksumMismatchInContainer` (debian script; `Tamper` appends to `install.sh` after sums were written — add a `tamperInstallSh` helper next to `appendToTarballs`): exit 6, version still `v0.0.1`, no restart logged, no temp dir left.
- `TestUpgradeSameVersionIsNoopInContainer` (fake release where latest is v1): exit 0, `already installed` in output, no systemctl calls.
- `TestUpgradeRefusesDowngradeInContainer` (ubuntu deb: real v0.0.1 installed, fake release also holds `0.0.0`; `upgrade --version v0.0.0`): exit 5 with `use --force`; then `--force` → `apt-get ... --allow-downgrades` succeeds and `dpkg-query` says `0.0.0`.
- `TestUpgradeNonRootRefusedInContainer` (`upgradeAs("nobody")`): exit 3, stderr contains `sudo /usr/local/bin/stackstead upgrade`, nothing changed.
- `TestUpgradeCheckAsNonRootInContainer`: `upgradeAs("nobody", "--check", "-o", "json")` → exit 0, `state: available`, `latest: 0.0.2`.
- `TestUpgradeDpkgLockBusyInContainer` (ubuntu deb): background `sh -c 'exec 9>/var/lib/dpkg/lock-frontend; flock -x 9; sleep 60'`; `upgrade` → exit 5 within a few seconds, `busy` in stderr, `dpkg-query` still `0.0.1`.
- CLI e2e (`cli_e2e_test.go`): `upgrade --check -o json --base-url <httptest>` → exit 0, JSON `latest == "9.9.9"`, `state == "unknown"` (binary version `e2e-test`), `current == "e2e-test"`; `upgrade --yes --base-url <httptest>` as the test user → exit 5 (dev-build refusal, spec §7 step 3, which precedes the privilege check) on every platform, and nothing executed.

- [ ] **Step 1: Write the tests** (they fail until the harness pieces exist; the e2e assertions fail with "unknown command" against a binary built before T8 only if run on `main`, so prove failure by temporarily asserting a wrong exit code, then restore).
- [ ] **Step 2: Run** `go vet -tags installer ./...`, then `go test -tags installer -count=1 -run 'Upgrade.*InContainer' -timeout 1800s ./test/installer/` with Docker; `make installer-test` once at the end (full suite, ~15 min); `make e2e`.
- [ ] **Step 3: Fix** whatever the real installers reveal (apt `_apt` warning noise is acceptable; an error is not).
- [ ] **Step 4: Commit** — `test: upgrade paths in the installer containers and the CLI e2e` (why: the only proof that the verified file really drives apt, rpm and install.sh the way the unit argv table says).

---

### Task 11: Docs, final checks, native pass

**Files:** `docs/cli.md`, `docs/install.md`, `docs/api.md`, `docs/security.md`, `README.md`, `deploy/release/RELEASING.md`. Spec §12. Depends on all.

- [ ] **Step 1: Write the docs** — `cli.md`: `upgrade` row in the Host admin table (`any` for server running; "download, verify and run the native installer"), a short "Upgrading" section (flags; per installer what runs; "restarts only what the installer restarts"; schema note; exit-code mapping sentence) and the exit-code table gains the `upgrade` meanings in the existing rows (5: downgrade/reinstall without `--force`, dpkg busy; 6: verification; 7: release source unreachable). `install.md`: Upgrade section leads with `sudo stackstead upgrade` (macOS script: without `sudo`), keeps the manual ways, adds the 0.2.0 note, the `--force` downgrade schema caveat and the backup tip; `--check`. `api.md`: two rows and a note under the JSON rule. `security.md`: bullet on the daily check (what, where, UA, opt-out). `README.md`: one sentence in Install ("Later, `sudo stackstead upgrade` updates it; Settings tells you when a release is out.") and one feature line under "What you get". `RELEASING.md`: asset names are a contract for `stackstead upgrade`; after publishing, run `stackstead upgrade --check` on a host and expect the new version. Fix the README install snippet's note on verification to mention the same rule applies to `upgrade`.
- [ ] **Step 2: Final checks** — `make check`, `make ui`, `make e2e`, `make installer-test`, `go run ./cmd/stackstead config validate --config deploy/config.example.yaml`, `git diff --stat main...HEAD` reviewed against the File Structure (nothing from the forbidden list touched).
- [ ] **Step 3: Native pass on the Lima VMs** (manual; record the exact outputs in the handoff). Prepare a release directory in GitHub's layout from `make dist SKIP_UI=1 ALLOW_DEV_RELEASE=1 VERSION=v0.0.2` (plus a `v0.0.1` build of the branch binary), serve it with any static server inside each VM (copy `releases/download/v0.0.2/SHA256SUMS` to `releases/latest/download/SHA256SUMS`), then: `stackstead-debian12` — script install v0.0.1, `sudo /usr/local/bin/stackstead upgrade --base-url http://127.0.0.1:8000`, service restarted, UI reachable; then `.deb` v0.0.1 via `apt`, `upgrade` through `apt-get`. `stackstead-alma9` — `.rpm` via `dnf`, `upgrade` through `dnf`. `stackstead-alpine` — script, `upgrade` prints the supervisor note, binary replaced. `stackstead-macos15` — script as the user (`/usr/local/bin/stackstead upgrade`, sudo prompt from `install.sh`, agent kickstarted) and `.pkg` (`sudo installer` first, then `sudo /usr/local/bin/stackstead upgrade` with the `.pkg` asset; check `pkgutil --pkg-info io.github.rcss-labs.stackstead` version). In each VM also open Settings: the server uses the fixed GitHub source only, so the card shows the real GitHub answer (or `failed`/`network` offline); verify the sentence and that the command text matches the one just run.
- [ ] **Step 4: Commit** — `docs: update notice and stackstead upgrade` (why: the command and the card are only safe if operators know what restarts and what a downgrade does to the database).
- [ ] **Step 5: Hand off** for the final review (Fable): confirm tests were written first, the installer container suite and e2e passed, the Lima outputs are attached, and the five open points of spec §14 were answered or defaulted as recorded.

## Spec coverage

| Spec | Task |
|---|---|
| §4.2 sums, §4.3 versions, §4.4 detection/command, §6 DTO | T1 |
| §4.1 source, download, redirect/size policy | T2 |
| §9 config | T3 |
| §4.5 apply, lock probe | T4 |
| §5 checker, cadence, logging | T5 |
| §6 routes, publish rules | T6 |
| serve wiring, opt-out reaches the goroutine | T7 |
| §7 command, exit codes, prompts, mapping | T8 |
| §8 card, sentences, accessibility | T9 |
| §11 container tests, e2e | T10 |
| §7 restart/schema facts, §10, §12 docs; Lima pass | T11 |
