# Stackstead Plan 7 — Installers (Linux and macOS) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** this plan specifies *what* to build: files, interfaces, behaviour, test cases and acceptance criteria. It deliberately contains no full implementations or scripts; implementers write them test-first. Short snippets show shapes only.

**Goal:** One command installs Stackstead as a supervised service on Linux (systemd) or macOS (launchd), from verified GitHub Release artifacts: `install.sh` (POSIX sh), `.deb`/`.rpm` packages, and a macOS `.pkg`. Re-running upgrades in place without touching data; uninstall removes what was installed and nothing the user created. All artifacts are produced locally by Makefile targets (CI is deferred).

**Architecture:**
- `tools/mkrelease` (Go, stdlib) turns built binaries into deterministic tarballs and `SHA256SUMS`. Releases are not signed for now; the optional `SIGNING_KEY` hook in the Makefile is off by default.
- `install.sh` is one POSIX script with a platform switch: download → verify checksum (signature only when `--signers` is given) → stage files → service (systemd or launchd) → readiness probe → print the setup URL. A `--root DIR` staging mode makes every filesystem effect testable without root and without touching services.
- `deploy/` holds the packaging sources: hardened unit, maintainer scripts (deb and rpm argument conventions), `nfpm.yaml`, lintian/rpmlint overrides, the deb `copyright` file, the launchd plist template, `.pkg` scripts and distribution XML. `LICENSE` (AGPL-3.0) at the repo root is shipped in every artifact.
- `test/installer` (build tag `installer`) drives the script and packages in containers with unique names and labels, against a static HTTP server that mimics the GitHub Releases URL layout.

**Tech Stack:** POSIX sh (dash, busybox ash, macOS `/bin/sh`), systemd, launchd, nfpm (pinned, via `go run pkg@version`), `pkgbuild`/`productbuild`/`lipo`/`codesign` (`notarytool` only on the optional future signing path), OpenSSH `ssh-keygen -Y` (optional signature path and its tests only), shellcheck (pinned container), Go 1.26 stdlib for tools and tests. No new Go module dependencies.

**Spec:** `docs/superpowers/specs/2026-10-14-stackstead-installers-design.md` (this plan's design) and `docs/superpowers/specs/2026-10-14-stackstead-v1-design.md` §3 (native binary + systemd), §6 (first-run setup: loopback or setup token), §7 (Docker socket is root-equivalent; default bind localhost).

### Plan series (this is Plan 7; runs after Plans 5 and 6)

1–4 merged. 5 Web UI (`feat/ui`, in progress; its T1 creates the `Makefile` this plan extends). 6 CLI (planned in parallel; subcommands on the same binary — see **Dependencies on Plan 6**). **7 Installers (this plan).** Carry-overs landing here: the README "Install (systemd)" section becomes "Install" with the script and packages; `deploy/stackstead.service` is hardened and moved to `deploy/systemd/`.

## Decisions (controller, recorded for the user)

The global rules ask for TypeSafe-scored decisions; the TypeSafe skill needs an external API and credentials, so the choices are argued in writing (the design spec §3 has the full table). Summary of the ones implementers must not relitigate:

- **No release signing for now (user decision).** Releases ship `SHA256SUMS` only; `install.sh` always verifies the tarball checksum and there is no flag to skip it. Signature verification (`ssh-keygen -Y verify` over `SHA256SUMS.sig`) is implemented behind `--signers FILE` / `STACKSTEAD_INSTALL_SIGNERS_FILE`, off by default, no key embedded, and a missing `.sig` is never an error unless signers were requested. `make release` signs only when `SIGNING_KEY` is set. Future: generate a key, embed the public half, flip the default — a docs + key change, not code.
- **License: AGPL-3.0-only (user decision: AGPL-3.0; `-only` because nothing suggests `-or-later`).** `LICENSE` with the full text at the repo root (T1 creates it), shipped in tarballs, deb (`/usr/share/doc/stackstead/copyright`, DEP-5), rpm (`License: AGPL-3.0-only`, `%license`), and the `.pkg` payload. README notes the AGPL §13 network clause and that the public repo satisfies it for published versions.
- **Resolved by the user:** identifier `io.github.rcss-labs.stackstead`; rpm enables + starts on fresh install like deb; macOS stacks dir `~/Library/Application Support/Stackstead/stacks`; `Recommends: docker-ce | docker.io` (deb) / `Recommends: docker-ce` (rpm), never `Depends`; unsigned `.pkg` for now (identity-gated signing path kept optional, never in acceptance).
- **nfpm, pinned, via `go run github.com/goreleaser/nfpm/v2/cmd/nfpm@<exact version>`.** Verified by the Go sumdb, no `go.mod` change, builds deb and rpm on macOS. The version and the reason are recorded next to `NFPM_VERSION` in the Makefile; `NFPM=` overrides with a local binary.
- **macOS = LaunchAgent, per user, plist rendered with absolute paths.** No Go changes for macOS paths.
- **Universal macOS binary in one `.pkg`; tarballs stay per-arch.**
- **Packages enable and start on fresh install, `try-restart` on upgrade, deb `purge` removes config/data/user.** `/opt/stacks` and Docker resources are never removed by anything.
- **Script installs under `/usr/local`, packages under `/usr`; the script refuses to run over a package install (exit 8).**
- **`install.sh` lives at the repo root** and is also a release asset and part of every tarball (so `--uninstall` works offline from `/usr/local/share/stackstead/install.sh`).
- **Unsupported: `armv7`, non-systemd Linux service management, Alpine packages.** The script on a non-systemd host installs the binary, user, dirs and config, warns, and exits 0. Alpine stays in the test matrix as that case.
- **Version strings:** git tag `vX.Y.Z[-pre]` → artifact version `X.Y.Z[-pre]`; deb/rpm use `X.Y.Z~pre` for pre-releases (sorts before the final). Untagged builds are `0.0.0-dev.<short sha>` and are refused by `make release` unless `ALLOW_DEV_RELEASE=1`.

## Dependencies on Plan 6 (CLI) — to reconcile before execution

Plan 7 relies on exactly these interfaces of the binary, reconciled against `docs/superpowers/specs/2026-10-14-stackstead-cli-design.md` (Plan 6's design, §3–§4). If Plan 6 ships different names, update this section and the tasks that cite it (T2, T3, T5, T7); nothing else in Plan 7 depends on the CLI.

| Need | Interface Plan 7 codes against (per the CLI design) | Fallback if Plan 6 does not provide it |
|---|---|---|
| Service command line | `stackstead serve --config <path>`; long-running, exits non-zero on a fatal error, honours `STACKSTEAD_*` env, stops cleanly on SIGTERM, creates and migrates the database on first start (today's `store.Open` behaviour — installers never run `db init`) | The CLI design keeps `stackstead --config <path>` working with a deprecation notice; the unit, plist and docs change one token |
| Version for the upgrade check | `stackstead version --short` → the version string only (`v1.2.3`, `v1.2.3-N-g<sha>[-dirty]` or `dev`), added to the CLI design for installers. `stackstead version` → `stackstead <version> (<commit> <date>) <go> <os>/<arch>` | Second whitespace-separated field of `stackstead version` |
| Build-time version injection | Plan 6's `Makefile` already defines `VERSION`, `COMMIT`, `DATE` and `LDFLAGS` with `-X github.com/rcss-labs/stackstead/internal/version.{Version,Commit,Date}`; Plan 7 reuses them and `make build-go`, and extends `release` (adds `darwin/amd64`, archives, checksums, signing) keeping Plan 6's `bin/stackstead-<os>-<arch>` names | `debug.ReadBuildInfo` fallback yields a usable `dev`/vcs version when `-X` is unset |
| Unit `ExecStartPre` (adopted from the contract's suggestion) | `ExecStartPre=<bindir>/stackstead config validate --config /etc/stackstead/config.yaml` (exit 6 with the key name on an invalid config; runs as the service user, which can read the 0640 root:stackstead file) | Line omitted |
| Readiness after start | `GET /api/health` (unauthenticated, `{"ok":true}`, added by Plan 6) probed by the installer with curl/wget against `listen:` from the config it wrote. The installer does **not** gate on `stackstead health`: that command exits 7 when Docker is down (normal on a fresh host) and refuses to run as root over a service-owned DB | `GET /api/setup/status` (exists today, unauthenticated) |
| Post-install summary (optional) | `stackstead health` is run for its human output only, as the service user on Linux (`runuser -u stackstead --`) and as the target user on macOS; its exit code is reported, never fatal (7 = Docker missing = the installer's existing warning) | Skipped |
| Config validation (optional) | `stackstead config validate --config <path>` after writing a config (online-safe, no DB access) → exit 0/6 | Skipped |
| systemd readiness (optional) | `serve` sends `READY=1` on `$NOTIFY_SOCKET` once listening (not in the CLI design; nice-to-have) | Unit stays `Type=simple` (the plan's default) |
| Default config path | unchanged: `/etc/stackstead/config.yaml` when it exists | — |
| Build matrix | Plan 7 adds `darwin/amd64` to the `CGO_ENABLED=0` cross-compile set (the CLI design lists `darwin/arm64` only); `x/term` builds for both | — |

**Confirmed by the user:** bare `stackstead` prints help (exit 2), `stackstead serve` starts the server, `GET /api/health` → `{"ok":true}` unauthenticated, `golang.org/x/term` accepted. **Reconciled with the CLI design (same date):** installers never call `admin set-password` or `db init`; first-run setup stays in the UI (loopback or setup token) and the README names `admin set-password` only as the password-recovery path. Makefile split: Plan 6 owns `VERSION`/`COMMIT`/`DATE`/`LDFLAGS`/`build-go`; Plan 7 owns `release`, `dist`, `packages`, `pkg`, `lint-sh`, `package-lint`, `installer-test`, `verify-release`.

The installer's exit codes (Global Constraints) are its own and intentionally differ from the CLI's table; the README keeps the two tables apart.

## Global Constraints

- `install.sh`: `#!/bin/sh`, `set -eu`, no bashisms (`shellcheck --shell=sh` clean at `--severity=style`, `checkbashisms` clean when available, runs under dash and busybox ash). Functions, not inline sequences, for every side effect so `--dry-run` and `--root` can route them. Every exit path uses the documented codes: `0` ok / nothing to do, `1` unexpected failure, `2` usage, `3` unsupported OS or arch, `4` missing tool, `5` download failure, `6` verification failure, `7` install step failed (user, dirs, unit, service), `8` refused (not root, package-managed install present, root without `SUDO_USER` on macOS). `--help` prints usage with every flag, env var and exit code.
- Never print secrets: the script never echoes the setup token (it prints the `journalctl`/log command to find it). No `curl | sh` of anything else inside the script. Downloads go to a `mktemp -d` directory removed by a trap on every exit.
- Upgrades never overwrite `config.yaml`, never touch `data_dir`, `stacks_dir` or Docker resources. The unit file (ours) is overwritten on upgrade; user customisations go in drop-ins (`systemctl edit`), documented.
- Packages: no payload file outside `/usr`, `/etc/stackstead` and `/var/lib/stackstead` (`/opt/stacks` is created by the maintainer scripts, never shipped); the config is a conffile (`noreplace` on rpm); license metadata is `AGPL-3.0-only` everywhere a license is declared; maintainer scripts are `set -e` POSIX sh, idempotent, and pass `lintian --fail-on error` and `rpmlint` with the committed overrides. The unit starts without Docker present (the binary already degrades).
- Tests never touch pre-existing containers, networks or images: every test resource is named `stackstead-itest-<run id>-…` and labelled `stackstead.test=<run id>`; cleanup is registered before creation; a final sweep removes only resources with that label. Container tests skip (not fail) when Docker is unavailable; `.pkg` install tests skip without `STACKSTEAD_PKG_TEST_VOLUME`.
- `make test` stays Node-free and Docker-free. Everything in this plan is behind the `installer` build tag or dedicated Makefile targets.
- No new Go module dependencies; `tools/` and `test/` are stdlib only. `go vet ./...` and `gofmt` clean including tagged files (`go vet -tags installer ./...`).
- Commits: conventional, explain why, and end with exactly these two trailer lines:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01Th7wAiWALPK7jdrpSxLToT
  ```

## Review Focus

1. **A corrupted or truncated download must never be installed, and the absence of signatures must never block one.** A checksum mismatch and a `SHA256SUMS` that does not list the requested asset each stop with exit 6 before any file outside the temp dir is touched; there is no flag that skips the checksum. Without `--signers`, a release with no `.sig` installs normally; with `--signers`, a missing or bad signature is exit 6. (T2 `TestRefusesChecksumMismatch`, `TestAssetMissingFromSumsIsRefused`, `TestNoSigNoSignersInstalls`, `TestSignersRequiresValidSig`, `TestBadSignatureRefusedWithSigners`)
2. **Running the script twice is safe; running it over a newer version is explicit.** Second run with the same version changes nothing (file hashes, mtimes, unit, config identical) and exits 0; a different version replaces only the binary and unit and restarts; `--version` older than installed needs `--force`. (T2 `TestSecondRunIsNoop`, `TestUpgradeKeepsConfigAndData`, T6 `TestUpgradeInContainer`)
3. **The service must be able to start on a fresh host.** `/opt/stacks` must exist and be writable by the service user (the hardened unit cannot create it), the `docker` group must exist or systemd refuses the unit, `StateDirectory` creates `/var/lib/stackstead`, and the unit must start without Docker installed. (T3 `TestUnitHasNoUnknownDirectives`, T6 `TestFreshInstallLayout`, T9 systemd smoke)
4. **Package upgrade and removal semantics.** Upgrade: conffile untouched, `/var/lib/stackstead/stackstead.db` untouched, service restarted once. Remove: service stopped and disabled, config and data kept. Purge: config, data and user gone; `/opt/stacks` and its contents still there. Both deb word arguments and rpm count arguments take the right branch. (T8 `TestDebUpgradeKeepsData`, `TestDebPurgeKeepsStacks`, `TestRpmUpgradeRestartsOnce`, T3 `TestMaintainerScriptBranches`)
5. **macOS must land on the real user, not root.** Under `sudo`, config, data and the plist go to `$SUDO_USER`'s home with that user's ownership; the agent is bootstrapped in `gui/<uid>`; `docker_socket` matches `docker context inspect`; root without `SUDO_USER` is refused. (T5 `TestDarwinTargetsSudoUser`, `TestDarwinRefusesPlainRoot`, `TestPlistHasAbsolutePaths`)
6. **Dry-run and `--root` have zero side effects outside their directories.** With `--dry-run` the only writes are in the temp dir; with `--root` nothing outside it changes and no service command runs. (T2 `TestDryRunWritesNothing`, `TestRootModeNeverCallsServiceCommands` via a stubbed PATH)

---

## File Structure

```
install.sh                               POSIX installer (Linux + macOS), T2 creates, T5 extends
LICENSE                                  full AGPL-3.0 text (T1 creates; shipped in every artifact)
Makefile                                 (from Plan 5 T1) + release/packages/pkg/lint/test targets, T1/T4/T7
tools/mkrelease/main.go                  deterministic tar.gz + SHA256SUMS; version normalisation
tools/mkrelease/main_test.go
deploy/systemd/stackstead.service        hardened unit (moved from deploy/stackstead.service)
deploy/config.example.yaml               (existing) shipped as the conffile and the script's template
deploy/release/RELEASING.md              how to tag, build, `gh release create`, verify; "future: signing" section
deploy/nfpm.yaml                         deb + rpm definition (env-templated version/arch)
deploy/pkg/deb/{preinst,postinst,prerm,postrm}
deploy/pkg/rpm/{pre,post,preun,postun}
deploy/pkg/lintian-overrides             + deploy/pkg/rpmlintrc
deploy/pkg/copyright                     DEP-5 machine-readable copyright (AGPL-3.0-only) for the deb
deploy/pkg/scripts_test.go               //go:build installer — maintainer-script branch tests (test-only package; harmless to go build ./...)
deploy/macos/launchagent.plist.in        @BIN@ @HOME@ @CONFIG@ @LOG@ @LABEL@ placeholders
deploy/macos/distribution.xml.in         productbuild distribution (title, min OS, pkg-ref)
deploy/macos/scripts/postinstall         renders plist for the console user, bootstraps the agent
deploy/macos/resources/{welcome,conclusion}.html
test/installer/harness_test.go           //go:build installer — fake release builder, docker helpers, run ids
test/installer/script_test.go            host tests of install.sh with --root and an httptest release
test/installer/script_darwin_test.go     host tests of the macOS path (launchctl, dscl, sudo stubbed)
test/installer/lint_test.go              make packages + lintian/rpmlint in a container
test/installer/container_test.go         real-flow tests in Debian/Ubuntu/Rocky/Alpine containers
test/installer/package_test.go           deb/rpm install/upgrade/remove/purge in containers
test/installer/systemd_test.go           opt-in booted-systemd smoke (STACKSTEAD_SYSTEMD_TEST=1)
test/installer/pkg_darwin_test.go        pkgutil structure checks; opt-in installer -target test volume
test/installer/images/{debian,ubuntu,rocky,alpine,lint}.Dockerfile   test images (curl, openssh, systemd)
README.md                                Install section rewritten (T9)
docs/superpowers/plans/2026-10-14-stackstead-07-installers.md       tick boxes only
```

## Execution waves

Tasks in one wave touch disjoint files and run in parallel worktrees. The Makefile is owned by one task per wave (T1 in wave 1, T4 in wave 2, T7 in wave 3) and the others only *read* it; a task must not create a file another task owns.

| Wave | Tasks (parallel) | Depends on | File ownership |
|---|---|---|---|
| 1 | T1 release artifacts, T2 install script (Linux + core), T3 packaging sources | nothing | T1: `tools/mkrelease/**`, `Makefile`, `deploy/release/**`, `LICENSE`. T2: `install.sh`, `test/installer/harness_test.go`, `test/installer/script_test.go`. T3: `deploy/systemd/**`, `deploy/pkg/**`, `deploy/nfpm.yaml`, deletes `deploy/stackstead.service` |
| 2 | T4 package build + lint targets, T5 macOS script path, T6 script container tests | T4: T1, T3. T5: T2. T6: T1, T2, T3 | T4: `Makefile`, `test/installer/images/lint.Dockerfile`, `test/installer/lint_test.go`. T5: `install.sh`, `deploy/macos/launchagent.plist.in`, `test/installer/script_darwin_test.go`. T6: `test/installer/container_test.go`, `test/installer/images/{debian,ubuntu,rocky,alpine}.Dockerfile` |
| 3 | T7 macOS `.pkg`, T8 package container tests | T7: T4, T5. T8: T4, T6 | T7: `Makefile`, `deploy/macos/**` (except the plist template), `test/installer/pkg_darwin_test.go`. T8: `test/installer/package_test.go` |
| 4 | T9 systemd smoke, README, release runbook, end-to-end dry run | all | T9: `test/installer/systemd_test.go`, `README.md`, `deploy/release/RELEASING.md`, plan tick boxes |

Symbol ownership in package `installer_test`: T2 owns the harness (`newRun`, `buildBinary`, `fakeRelease`, `serveRelease`, `runScript`, `stubPath`); T6 owns `dockerRun`, `dockerBuildImage`, `withNetwork`; T8 owns `installPackage`, `packageVersion`.

---

### Task 1: LICENSE, release artifacts, checksums, Makefile `release`

**Wave:** 1.

**Files:**
- Create: `LICENSE` (the full, unmodified GNU Affero General Public License v3.0 text from gnu.org; no header edits), `tools/mkrelease/main.go`, `tools/mkrelease/main_test.go`, `deploy/release/RELEASING.md` (skeleton; T9 completes it).
- Modify: `Makefile` (replace Plan 5's `release` target; add `VERSION`, `LDFLAGS`, `DIST`, `dist-clean`, `verify-release`).

**Interfaces:**
```
mkrelease -version <git describe output> -dist <dir> -bin <dir> [-targets linux/amd64,linux/arm64,darwin/amd64,darwin/arm64] [-extra name=path ...] [-print-version]
  writes <dist>/stackstead_<ver>_<os>_<arch>.tar.gz for each target found in <bin> as stackstead-<os>-<arch>
  writes <dist>/SHA256SUMS over every regular file already in <dist> plus the archives (sorted, "<hex>  <name>")
  -print-version prints "<ver>\t<pkgver>\t<prerelease>" and exits (used by the Makefile and nfpm env)
```
Makefile variables: `VERSION`, `COMMIT`, `DATE`, `LDFLAGS` and `build-go` come from Plan 6 (reused, not redefined); new: `DIST ?= dist`, `RELEASE_TARGETS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64`, `SOURCE_DATE_EPOCH` from the commit time, `SIGNING_KEY ?=` (empty = unsigned, the default and the current policy). Targets: `release` (= `ui` + per-target `go build` into `bin/` + `mkrelease`; signs only if `SIGNING_KEY` is set), `verify-release` (`sha256sum -c` on Linux / per-line compare with `shasum -a 256` on macOS; also `ssh-keygen -Y verify` when a `.sig` and `SIGNERS=` file are present), `dist-clean`.

**Behaviour:**
- Version normalisation (table-tested): `v1.2.3` → ver `1.2.3`, pkgver `1.2.3`, prerelease empty; `v1.2.3-rc.1` → `1.2.3-rc.1`, `1.2.3~rc.1`, `rc.1`; `v1.2.3-5-gabc1234[-dirty]` → `1.2.3-dev.5.abc1234[.dirty]`, pkgver `1.2.3~dev.5.abc1234`; bare `abc1234` (no tag) → `0.0.0-dev.abc1234`. Any `-dev.` version makes `make release` fail unless `ALLOW_DEV_RELEASE=1` (tests pass it).
- Archives are deterministic: entries sorted by name, uid/gid 0, mode 0755 for the binary and 0644 otherwise, mtime from `SOURCE_DATE_EPOCH` or the git commit time (passed in by the Makefile), gzip header without name/time. Building twice yields byte-identical archives and `SHA256SUMS`.
- Linux archives contain `stackstead`, `install.sh`, `stackstead.service`, `config.example.yaml`, `README.md`, `LICENSE`; darwin archives contain `stackstead`, `install.sh`, `launchagent.plist.in`, `config.example.yaml`, `README.md`, `LICENSE` (the `-extra` flags name them; missing files are an error, so the Makefile fails loudly if T3/T5 outputs are absent — in T1's own worktree the test uses temp files).
- `SHA256SUMS` lists every asset in `dist/` including `install.sh` and, when present, packages and the `.pkg`, so `make release` is called **last** (after `packages`/`pkg`) or re-run; `mkrelease` regenerates the file idempotently. No `.sig` is produced by default. Optional (future) signing: when `SIGNING_KEY` is set, `ssh-keygen -Y sign -f $(SIGNING_KEY) -n stackstead-release $(DIST)/SHA256SUMS` → `SHA256SUMS.sig`; `verify-release` only checks a signature when `SIGNERS=<allowed_signers file>` is also given. No key file is committed in this plan.
- `LICENSE` is created in this task (full AGPL-3.0 text) and is a required `-extra` for every archive; the archive test asserts its first line reads `GNU AFFERO GENERAL PUBLIC LICENSE` and the README (T9) carries the license section.

**Tests (write first)** in `tools/mkrelease/main_test.go` (plain `go test`, no tag):
- `TestNormalizeVersion` table above.
- `TestArchiveIsDeterministic`: build twice from the same inputs with different wall clocks → identical bytes; entries are in sorted order with uid/gid 0 and expected modes.
- `TestArchiveContentsPerOS`: linux archive has the service file, darwin has the plist template, both have `install.sh` and `LICENSE` (AGPL header line).
- `TestLicenseFileIsAGPL3`: `LICENSE` exists at the repo root, first non-blank line is `GNU AFFERO GENERAL PUBLIC LICENSE`, second is `Version 3, 19 November 2007`.
- `TestChecksumFileFormat`: `sha256sum -c` parseable (`<64 hex>␣␣<name>`), sorted, covers every file in dist, excludes `SHA256SUMS*` themselves.
- `TestMissingExtraFileFails`.
- `TestDevVersionRefusedWithoutOverride`.

**Acceptance:** `go test ./tools/...` green; `make release VERSION=v0.0.1 ALLOW_DEV_RELEASE=1 RELEASE_TARGETS="linux/amd64"` on a tree with the UI built produces `dist/stackstead_0.0.1_linux_amd64.tar.gz` and `SHA256SUMS` and no `.sig`; `make verify-release` passes on checksums alone; additionally (optional path kept working, not a release requirement) with a throwaway key (`ssh-keygen -t ed25519 -f /tmp/k -N ''`) `make release SIGNING_KEY=/tmp/k …` produces a `.sig` that `make verify-release SIGNERS=<tmp allowed_signers>` accepts; `CGO_ENABLED=0` for every target; `go vet ./...`, `gofmt -l .` clean.

---

### Task 2: `install.sh` — Linux flow, verification, staging mode, host tests

**Wave:** 1. Creates the script and the test harness that T5, T6 and T8 build on.

**Files:** create `install.sh`, `test/installer/harness_test.go`, `test/installer/script_test.go` (both `//go:build installer`).

**Interfaces:**
```
install.sh [options]
  --version vX.Y.Z | STACKSTEAD_VERSION   pin a release (default: latest)
  --prefix DIR        default /usr/local (binary in DIR/bin, helper in DIR/share/stackstead)
  --no-start          install and enable but do not start/restart
  --signers FILE      also verify SHA256SUMS.sig with ssh-keygen -Y against FILE (off by default; no key shipped yet)
  --force             reinstall the same or an older version
  --base-url URL | STACKSTEAD_BASE_URL   default https://github.com/rcss-labs/stackstead
  --root DIR          stage into DIR (DESTDIR-style); no root needed; service commands are printed, not run
  --dry-run           download and verify, print the plan, change nothing
  --uninstall [--purge]   remove binary, unit, helper; --purge also config, data and the user
  --print-paths       print the resolved paths as KEY=VALUE and exit
  -h | --help
env: STACKSTEAD_INSTALL_SIGNERS_FILE (same as --signers)
(the checksum check has no opt-out flag)
```
Harness (`harness_test.go`):
```go
type run struct { ID string; Dir string }                 // unique id, temp dir, cleanup registered
func newRun(t *testing.T) *run
func buildBinary(t *testing.T, goos, goarch, version string) string   // go build with -X version; cached per test binary
func fakeRelease(t *testing.T, r *run, version string, bins map[string]string, opts releaseOpts) string // dist dir with tarballs + SHA256SUMS; opts.Sign adds .sig (throwaway ed25519 key) and an allowed_signers file
func serveRelease(t *testing.T, dist string, latest string) (baseURL string)  // httptest; /releases/download/v<ver>/<asset>, /releases/latest/download/<asset> → 302 to latest
func runScript(t *testing.T, args []string, env map[string]string, stubs map[string]string) (stdout, stderr string, code int)
func stubPath(t *testing.T, stubs map[string]string) string   // dir of stub executables (systemctl, launchctl, useradd, sudo…) that log their argv to a file
```

**Behaviour:**
- Platform: `uname -s` → `linux`|`darwin` (T5 implements darwin; T2 exits 3 for it with "macOS support arrives in a later task"), `uname -m` → `amd64`|`arm64`; anything else exits 3 naming the value.
- Tool check (exit 4 with the distro package hint): downloader (`curl` preferred, `wget` fallback), `tar`, a sha256 tool (`sha256sum`, `shasum -a 256` or `openssl dgst -sha256`). Only with `--signers`: `ssh-keygen` supporting `-Y` (probe: `ssh-keygen -Y 2>&1 | grep -q verify`; hint `openssh-client` / `openssh-clients` / `openssh-keygen`).
- Root: required unless `--root` or `--print-paths`/`--help`; message tells the user to use `sudo` (exit 8).
- Package detection: `/usr/bin/stackstead` listed by `dpkg -S` or `rpm -qf` → exit 8 "installed by a package; use apt/dnf to upgrade or remove".
- Download: `SHA256SUMS`, then the tarball named in it for this os/arch and version; latest is resolved by fetching `…/releases/latest/download/SHA256SUMS` and reading the version out of the asset names. HTTP errors → exit 5 with the URL. Verification: the tarball's sha256 is compared to the listed one; mismatch or an asset missing from the list → exit 6; nothing can skip this. With `--signers` only: `SHA256SUMS.sig` is downloaded too and verified first (`ssh-keygen -Y verify -n stackstead-release -I release@stackstead -f <signers> -s SHA256SUMS.sig < SHA256SUMS`); missing or bad → exit 6. Without `--signers` the script never requests `.sig` and prints nothing about signatures.
- Installed version: one `installed_version()` function wraps whichever form Plan 6 ships (`version --short`, else `-o json` + `sed`, else the second field of `version`), bounded with `timeout` when available; an unparseable answer counts as "unknown" and triggers a reinstall with a notice. Same → "stackstead vX already installed" exit 0 unless `--force`; newer installed than requested → exit 0 with a notice unless `--force`.
- `--dry-run` ends after verification with a plan listing each path to create/replace and each service command; no `--root` needed; exit 0.
- Staging: a `root()` helper prefixes every absolute path with `--root` (empty by default). Service operations (`systemctl …`) and account operations (`useradd`/`groupadd`/`adduser`/`addgroup`) go through `svc()`/`acct()` wrappers that print instead of run when `--root` is set. Under `--root`, ownership changes are skipped when not root.
- Install steps, in order, each idempotent: group `docker` (`getent group docker || groupadd --system docker`, or `addgroup -S` on busybox); user `stackstead` (`useradd --system --home-dir /var/lib/stackstead --no-create-home --shell <nologin path found>`, or `adduser -S -D -H -h … -s …`); dirs `/etc/stackstead` 0755, `/var/lib/stackstead` 0700 stackstead, `/opt/stacks` 2775 stackstead:docker; config from the tarball's `config.example.yaml` only if absent (0640 root:stackstead); unit from the tarball with `ExecStart=` rewritten to `<prefix>/bin/stackstead serve --config /etc/stackstead/config.yaml` into `/etc/systemd/system/stackstead.service` 0644 (overwritten on upgrade); helper copy `<prefix>/share/stackstead/install.sh` 0755; binary via `<prefix>/bin/stackstead.new` then `mv -f` (atomic; works while the old one runs).
- Service: systemd booted (`[ -d /run/systemd/system ]`) → `daemon-reload`; first install → `enable --now` (`enable` only with `--no-start`); upgrade → `restart` if the unit was active before, else nothing. systemd present but not booted → `systemctl enable` (offline symlink) and a warning that nothing was started. No `systemctl` → warning "no systemd; run `<prefix>/bin/stackstead serve --config …` under your supervisor", exit 0.
- Readiness: when the service was started, wait up to 15 s for `GET http://<listen>/api/health` (Plan 6; fallback `/api/setup/status`) to answer 200, using the same downloader as the release fetch; `<listen>` is read from the config with `sed` (`listen:` key, default `127.0.0.1:8420`). On success print `Open http://<listen>/ from this machine to create the admin account` and `Remote? use the setup token: journalctl -u stackstead | grep setup_token`; on timeout exit 7 with `systemctl status stackstead` and `journalctl -u stackstead -n 50` hints. `stackstead health` is never the gate (see Dependencies on Plan 6); when present it is run once as the service user for its summary, exit code reported only.
- Docker check (warning only, never installs): `command -v docker`, `docker compose version`, socket path from the config (default `/var/run/docker.sock`) exists; print the Docker install URL when missing and note Stackstead starts degraded.
- `--uninstall`: stop + disable the unit if present, remove unit, binary, helper dir, `daemon-reload`; keep config, data, stacks, user. `--purge` additionally removes `/etc/stackstead`, `/var/lib/stackstead` and the user (`userdel`/`deluser`), never `/opt/stacks` or anything in Docker; prints what it kept. Works offline (no download). `--dry-run` applies.
- Output: plain lines prefixed `==>` for steps, `warning:` and `error:` on stderr; no colour unless stdout is a TTY and `NO_COLOR` is unset.

**Tests (write first)**, `test/installer/script_test.go`, run on the host (macOS or Linux) with `--root` and stubbed `PATH` for `systemctl`/`useradd`/`groupadd`/`getent`; the fake release contains a real `linux/<host arch>` build (so the installed-version probe runs the real binary only if the host is Linux; on macOS the harness stubs `<root>/usr/local/bin/stackstead` via a wrapper file that answers the version command and the test asserts it was called):
- `TestHelpAndUsageErrors`: `--help` → exit 0, mentions every flag and exit code; unknown flag → exit 2; `--version` without value → 2.
- `TestUnsupportedArchExits3` (stub `uname`), `TestMissingToolExits4` (PATH without any sha256 tool → 4; PATH without `ssh-keygen` → proceeds by default, 4 only with `--signers`), `TestRefusesNonRootWithoutRoot` (exit 8 unless `--root`).
- `TestFreshInstallLayout`: all paths exist under `--root` with the expected modes; unit has the rewritten `ExecStart` and the `SupplementaryGroups=docker` line; config equals `config.example.yaml`; helper present; stub log shows `groupadd`, `useradd` (with `--system`, home `/var/lib/stackstead`, nologin shell) and `systemctl daemon-reload` + `enable --now`, in that order.
- `TestSecondRunIsNoop`: hashes and mtimes of every file identical; exit 0; stub log shows no `systemctl restart`.
- `TestUpgradeKeepsConfigAndData`: edit the staged config and drop a file in data; install a newer fake release → binary and unit replaced, config and data untouched, stub log has `restart` once (unit was active per stubbed `is-active`).
- `TestOlderVersionNeedsForce`, `TestVersionPinSelectsAsset` (env and flag; flag wins).
- `TestRefusesChecksumMismatch` (tarball altered after `SHA256SUMS` was written → 6, nothing staged), `TestAssetMissingFromSumsIsRefused`, `TestNoSigNoSignersInstalls` (release without `.sig`, no `--signers` → installs; the server log shows no request for `.sig`), `TestSignersRequiresValidSig` (`--signers` with a `.sig` from the throwaway key → installs; with no `.sig` → 6), `TestBadSignatureRefusedWithSigners` (`.sig` from another key → 6), `TestSignersEnvEqualsFlag`.
- `TestDryRunWritesNothing`: `--dry-run` without `--root` as non-root → exit 0, plan printed, `--root` dir (given anyway to catch writes) stays empty, temp dir removed.
- `TestRootModeNeverCallsServiceCommands`: stub `systemctl` log is empty; printed plan includes the commands instead.
- `TestLatestResolvesViaRedirect`, `TestDownloadFailureExits5` (server returns 500/404).
- `TestNoSystemdWarnsAndExitsZero` (no `systemctl` on PATH, no `/run/systemd/system` under root), `TestSystemdNotBootedEnablesOnly`.
- `TestBusyboxAccountCommands`: PATH with `adduser`/`addgroup` stubs and no `useradd` → correct busybox flags.
- `TestUninstallKeepsDataPurgeRemovesIt`, `TestUninstallNeverTouchesStacks`, `TestPackageInstallRefused` (stub `dpkg -S` success → 8).
- `TestShellcheckClean`: skips when neither `shellcheck` nor Docker is available; else runs `shellcheck --shell=sh --severity=style install.sh` (locally or `koalaman/shellcheck:v0.10.0`), and `dash -n`/`busybox sh -n` via the same container when available.

**Acceptance:** `go test -tags installer -run 'Script|Shellcheck' -timeout 300s ./test/installer/` green on macOS and Linux hosts; `shellcheck --shell=sh --severity=style install.sh` clean; `sh -n install.sh` clean; `go vet -tags installer ./test/...` clean.

---

### Task 3: Hardened unit, packaging sources, maintainer scripts, nfpm definition

**Wave:** 1. Pure sources; building and installing them is T4/T8.

**Files:** create `deploy/systemd/stackstead.service` (move + harden `deploy/stackstead.service`; delete the old path), `deploy/nfpm.yaml`, `deploy/pkg/deb/{preinst,postinst,prerm,postrm}`, `deploy/pkg/rpm/{pre,post,preun,postun}`, `deploy/pkg/lintian-overrides`, `deploy/pkg/rpmlintrc`, `deploy/pkg/copyright` (DEP-5: `Upstream-Name: stackstead`, `License: AGPL-3.0-only` with the short "see /usr/share/common-licenses/AGPL-3" reference Debian expects), `deploy/pkg/scripts_test.go` (`//go:build installer`; package `deploy_test`, runs the scripts with stubbed PATH and `STACKSTEAD_PKG_ROOT` for paths).

**Interfaces:**
```ini
# deploy/systemd/stackstead.service (shape)
[Unit] Description, Documentation, After=network-online.target docker.service, Wants=network-online.target
[Service] User/Group=stackstead, SupplementaryGroups=docker, Environment=DOCKER_CONFIG=/var/lib/stackstead/.docker,
  ExecStartPre=/usr/bin/stackstead config validate --config /etc/stackstead/config.yaml,
  ExecStart=/usr/bin/stackstead serve --config /etc/stackstead/config.yaml, Restart=on-failure, RestartSec=5,
  StateDirectory=stackstead, StateDirectoryMode=0700, UMask=0027, hardening per the design spec §3
[Install] WantedBy=multi-user.target
```
```yaml
# deploy/nfpm.yaml (shape; env-templated)
name: stackstead  arch: ${ARCH}  version: ${VERSION}  prerelease: ${PRERELEASE}  release: "1"
maintainer / description / homepage: https://github.com/rcss-labs/stackstead / license: AGPL-3.0-only / section: admin / priority: optional
recommends: [docker-ce | docker.io]      # deb; rpm override: recommends: [docker-ce] (weak deps, never depends)
contents: binary → /usr/bin/stackstead; unit → /usr/lib/systemd/system/; config.example.yaml → /etc/stackstead/config.yaml (type: config|noreplace, 0640 root:stackstead); dir /var/lib/stackstead (0700 stackstead); /opt/stacks is NOT in the payload (lintian `dir-or-file-in-opt`, and dpkg/rpm would delete it when empty on remove) — postinst/%post create it 2775 stackstead:docker; binary entry has `expand: true` (nfpm only expands `${BIN}` with it); lintian overrides → /usr/share/lintian/overrides/stackstead; deb: deploy/pkg/copyright → /usr/share/doc/stackstead/copyright; rpm: LICENSE → /usr/share/licenses/stackstead/LICENSE (type: license)
overrides: deb.scripts and rpm.scripts → deploy/pkg/<fmt>/*
```

**Behaviour:**
- Unit: not `ProtectSystem=strict`, no `ProtectHome` (external compose files); `ExecStartPre` validates the config so a typo fails with the key name in the journal instead of a generic exit 1; the script (T2) rewrites **both** `Exec*` paths for its prefix; `Documentation=` points at the GitHub repo and `man:` is omitted; `Type=simple` unless Plan 6 provides `READY=1` (then `Type=notify`). Every directive must be known to systemd ≥ 245 (Ubuntu 20.04) — `systemd-analyze verify` in a container (T8) catches typos.
- Maintainer scripts are POSIX `set -e`, share no sourced file, and branch explicitly on their arguments:
  - deb `preinst install|upgrade`: group + user (same commands as the script, `useradd` only — Debian/Ubuntu), the user is also a member of `docker` in `/etc/group` (`useradd --groups docker`, or `usermod -aG docker` when the user exists; the unit keeps `SupplementaryGroups=docker`). `postinst configure [old-version]`: `/opt/stacks` and ownership of `/var/lib/stackstead` (idempotent), `chgrp stackstead /etc/stackstead/config.yaml` + 0640, `systemctl daemon-reload`; old-version empty, or the `.disabled-by-remove` marker present in `/var/lib/stackstead` (left by `prerm remove`, because a reinstall from the `rc` state passes the old version) → `enable --now` + hint and delete the marker; else `try-restart`; `systemctl` missing or not booted (container builds, chroots) → skip silently. `prerm remove`: write the marker, `stop` + `disable`; `prerm upgrade`: nothing. `postrm remove`: `daemon-reload`; `postrm purge`: `rm -rf /etc/stackstead /var/lib/stackstead`, `userdel stackstead`, never `/opt/stacks`, never the `docker` group; `postrm upgrade|failed-upgrade|abort-*`: nothing destructive.
  - rpm `pre $1`: any → group + user (+ docker membership, as deb). `post $1`: `daemon-reload`; `$1 = 1` → `enable --now`; `$1 ≥ 2` → `try-restart`; dirs as above. `preun $1`: `$1 = 0` → `stop` + `disable`. `postun $1`: `daemon-reload`; `$1 = 0` → nothing else (rpm has no purge: the README documents `rm -rf /var/lib/stackstead; userdel stackstead` as the manual purge).
  - Every script prints the setup URL hint on fresh install (`postinst configure` with empty old version, `post 1`) and the Docker warning when `docker` is not on PATH.
  - All scripts honour `STACKSTEAD_PKG_ROOT` (test-only path prefix, empty in production) and tolerate a missing `systemctl`.
- Lintian overrides, each with a one-line justification in the file: `statically-linked-binary` (Go), `no-manual-page` (deferred), `no-changelog`/`changelog-file-missing-in-native-package` (nfpm), `missing-systemd-timer-for-cron-script` not applicable (do not add overrides speculatively; T4's lint run decides the final list). rpmlintrc filters the equivalents (`no-manual-page-for-binary`, `no-changelogname-tag`, `non-standard-dir-perm` if flagged).

**Tests (write first)** in `deploy/pkg/scripts_test.go` (host, no Docker; stub `PATH` with logging `systemctl`, `useradd`, `groupadd`, `getent`, `userdel`, `id`, `docker`):
- `TestMaintainerScriptBranches` table: every script × argument combination above → expected ordered command log (e.g. deb `postinst configure` with no old version → `daemon-reload`, `enable --now`; with `1.0.0` → `daemon-reload`, `try-restart`; rpm `preun 1` → nothing; deb `postrm purge` → `userdel` and both `rm -rf` targets under `STACKSTEAD_PKG_ROOT`, and `/opt/stacks` still present).
- `TestScriptsIdempotent`: running `postinst configure` twice logs the same commands and leaves the same tree.
- `TestScriptsSurviveMissingSystemctl`: PATH without `systemctl` → exit 0.
- `TestScriptsArePosix`: `sh -n` each; shellcheck when available (same skip rule as T2).
- `TestUnitHasNoUnknownDirectives`: parse the unit as INI and compare every key against an allow-list in the test (catches typos without systemd); `ExecStart` matches the Plan 6 command line; `SupplementaryGroups=docker` present; no `ProtectSystem=strict`, no `ProtectHome`.
- `TestNfpmYamlLists`: parse `nfpm.yaml` (yaml already a dependency) and assert the config entry is `type: config|noreplace`, the two dirs with owners, the binary at `/usr/bin/stackstead`, `license` is exactly `AGPL-3.0-only`, `recommends` is present and `depends` is absent, the deb `copyright` and rpm `license` entries exist, both override script sets exist on disk.
- `TestCopyrightIsDEP5`: `deploy/pkg/copyright` starts with the DEP-5 `Format:` URL and declares `License: AGPL-3.0-only`.

**Acceptance:** `go test -tags installer -timeout 120s ./deploy/...` green; `grep -rn deploy/stackstead.service README.md` has no hits left outside this plan's T9 scope (T3 updates the README path references in the Install section only if trivial, otherwise leaves a `TODO(T9)` note in the task report, not in the file); `git mv` used for the unit so history follows.

---

### Task 4: `make packages`, `make package-lint`

**Wave:** 2. Depends on T1 (version normalisation, `dist/`) and T3 (sources).

**Files:** modify `Makefile`; create `test/installer/images/lint.Dockerfile`, `test/installer/lint_test.go` (`//go:build installer`).

**Interfaces:**
```
make packages [VERSION=…] [PKG_ARCHES="amd64 arm64"]   → dist/stackstead_<ver>_<arch>.deb, dist/stackstead-<ver>-1.<x86_64|aarch64>.rpm
make package-lint                                       → lintian + rpmlint in a container against dist/
NFPM_VERSION := v2.x.y   # exact; NFPM ?= go run github.com/goreleaser/nfpm/v2/cmd/nfpm@$(NFPM_VERSION)
```

**Behaviour:**
- `packages` depends on the per-arch linux binaries from T1's build step (not on `release`, to keep `release` the last step that computes `SHA256SUMS`); exports `VERSION`, `PRERELEASE` (from `mkrelease -print-version`) and `ARCH` to nfpm; uses `--target` to force the exact file names above (nfpm's default rpm arch names are `x86_64`/`aarch64`, deb `amd64`/`arm64`).
- `package-lint` builds `lint.Dockerfile` (`debian:bookworm-slim` + `lintian` + `rpmlint`, pinned base tag) once and runs `lintian --fail-on error,warning --no-tag-display-limit` with the package's own overrides and `rpmlint -f deploy/pkg/rpmlintrc` on every package in `dist/`; a non-zero exit fails the target. Image name `stackstead-itest/lint:<hash of Dockerfile>` so a Dockerfile change rebuilds.
- `make dist` = `ui`, build all targets, `packages`, (`pkg` when on macOS and `SKIP_PKG` unset), then `release` (checksums over everything, sign).

**Tests (write first)** `test/installer/lint_test.go`:
- `TestPackagesBuild`: runs `make packages VERSION=v0.0.1 ALLOW_DEV_RELEASE=1` into a temp `DIST`; asserts the four files exist; `dpkg-deb --info`/`--contents` (in the lint container, or `ar`+`tar` locally) shows the unit, conffile entry, maintainer scripts; `rpm -qpl`/`-qp --scripts` shows the same for rpm.
- `TestPackageLintClean`: skips without Docker; runs the lint target and requires exit 0 (overrides from T3 may be amended here with a justification).
- `TestNfpmVersionPinned`: `NFPM_VERSION` in the Makefile matches `^v\d+\.\d+\.\d+$` (no `latest`, no range).

**Acceptance:** `make packages` on macOS without dpkg/rpm installed produces the four packages; `make package-lint` green; `go test -tags installer -run 'Packages|Lint|Nfpm' -timeout 600s ./test/installer/` green.

---

### Task 5: `install.sh` — macOS path (LaunchAgent)

**Wave:** 2. Depends on T2 (script structure, harness).

**Files:** modify `install.sh`; create `deploy/macos/launchagent.plist.in`, `test/installer/script_darwin_test.go` (`//go:build installer`; runs on any host — launchctl is stubbed — but the real-launchctl checks are manual).

**Interfaces:**
```
plist template placeholders: @LABEL@ (io.github.rcss-labs.stackstead), @BIN@, @CONFIG@, @LOG@, @HOME@
rendered keys: Label, ProgramArguments [@BIN@, serve, --config, @CONFIG@], RunAtLoad true, KeepAlive {SuccessfulExit: false},
  ThrottleInterval 5, StandardOutPath/StandardErrorPath @LOG@, EnvironmentVariables {HOME: @HOME@, PATH: /usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin}, ProcessType Background
install.sh additions: --user NAME (macOS; default $SUDO_USER or the invoking user), --label (hidden, tests)
```

**Behaviour:**
- Target user: `$SUDO_USER` when running as root, else the current user; root without `SUDO_USER` → exit 8 ("run as your user; sudo is requested for /usr/local only"). Home from `dscl . -read /Users/<u> NFSHomeDirectory` (stubbed in tests; fallback `eval echo ~user`).
- Privilege: when not root, every write under `/usr/local` is wrapped in `sudo` (prompt explained first); when root (via sudo), per-user writes are followed by `chown -R <user>` of the created files. Under `--root`, no `sudo` is invoked.
- Paths (design spec §4): `~/Library/Application Support/Stackstead/{config.yaml,data,stacks}`, `~/Library/Logs/Stackstead/stackstead.log`, `~/Library/LaunchAgents/<label>.plist`; `/usr/local/bin/stackstead`, `/usr/local/share/stackstead/{install.sh,launchagent.plist.in}`.
- Config generated only if absent: `listen: 127.0.0.1:8420`, `stacks_dir`, `data_dir`, `docker_socket` from `docker context inspect --format '{{(index .Endpoints "docker").Host}}'` run **as the target user** (strip `unix://`), fallback `/var/run/docker.sock` with a warning; `allowed_roots` left commented. Written with the user's ownership, mode 0600.
- Service: `launchctl bootout gui/<uid>/<label>` (ignore failure), then `launchctl bootstrap gui/<uid> <plist>`; with `--no-start`, only the plist is written. Upgrade (binary replaced) → `launchctl kickstart -k gui/<uid>/<label>`. All via the `svc()` wrapper (printed under `--root`/`--dry-run`). Agent commands run as the target user (`sudo -u <user>` when root) because `gui/<uid>` domains are per session.
- Readiness as on Linux (`GET /api/health` against the configured `listen`), then `stackstead health --config <user config>` as the target user for the summary (exit code reported, not fatal), then print `Open http://127.0.0.1:8420/`, `Logs: <log path>`, `Setup token (only needed when not on this Mac): grep setup_token <log>`.
- Docker check: `docker` on PATH (including `/usr/local/bin` and `/opt/homebrew/bin`), `docker compose version`, and the detected socket exists; warn that Docker Desktop/OrbStack/Colima must be running for stacks to work.
- Uninstall: bootout + remove plist, binary, share dir, `pkgutil --forget <label>` if the receipt exists; `--purge` removes the Application Support and Logs dirs (prints that stacks under them go too — the only place the stacks dir is removed, because it is under the per-user app dir; `--purge` asks for `--yes` or an interactive `y` here).
- Linux behaviour is unchanged (every Linux test from T2 still passes).

**Tests (write first)** `script_darwin_test.go` (stub `uname -s` → `Darwin`, `uname -m`, `dscl`, `id`, `sudo` (logs and executes), `launchctl`, `docker` (prints a fake context), `stat`):
- `TestDarwinTargetsSudoUser`: run with `SUDO_USER=alice`, stub home `<root>/Users/alice` → plist, config, data, logs under that home; `sudo` log empty (we are "root"); ownership calls name `alice`; launchctl log has `bootstrap gui/<alice uid>` with the rendered plist path.
- `TestDarwinRefusesPlainRoot` (root, no `SUDO_USER` → 8), `TestDarwinNonRootUsesSudoOnlyForUsrLocal` (every `sudo` log entry's target path starts with `/usr/local`).
- `TestPlistHasAbsolutePaths`: rendered plist is valid XML (`plutil -lint` when on macOS, else a Go `encoding/xml` well-formedness check), no `~` or `$HOME` literals, ProgramArguments match the Plan 6 command, PATH includes `/opt/homebrew/bin`.
- `TestDarwinConfigUsesDetectedSocket` (stubbed context → `docker_socket: /Users/alice/.docker/run/docker.sock`), `TestDarwinConfigFallbackSocketWarns`.
- `TestDarwinUpgradeKickstarts` (second install with a newer version → `kickstart -k`, config untouched), `TestDarwinSecondRunIsNoop`.
- `TestDarwinUninstallAndPurge`: uninstall keeps Application Support; purge removes it only with `--yes`; `pkgutil --forget` called only when the stub receipt exists.
- `TestDarwinDryRunWritesNothing`.
- Re-run the full T2 suite (Linux) to prove no regression.

**Acceptance:** `go test -tags installer -run 'Script|Darwin|Shellcheck' -timeout 300s ./test/installer/` green; shellcheck clean; **manual check on this Mac** recorded in the task report: `sh install.sh --base-url http://127.0.0.1:<port> --version v0.0.1` against a local fake release (harness exposes `serveRelease` via a `-run TestServeReleaseForManual` helper that prints the URL and blocks until Ctrl-C), then `launchctl print gui/$(id -u)/io.github.rcss-labs.stackstead` shows it running, `http://127.0.0.1:8420/api/setup/status` answers, `install.sh --uninstall` leaves no agent, `launchctl list | grep stackstead` empty.

---

### Task 6: Container tests for `install.sh` on Linux

**Wave:** 2. Depends on T1 (archives), T2 (script, harness), T3 (unit file in the archive).

**Files:** create `test/installer/container_test.go`, `test/installer/images/{debian,ubuntu,rocky,alpine}.Dockerfile` (`//go:build installer`).

**Interfaces:**
```go
func dockerBuildImage(t *testing.T, r *run, dockerfile string) (image string)   // tag stackstead-itest/<name>:<dockerfile hash>; cached across runs
func withNetwork(t *testing.T, r *run) (network string)                           // unique, labelled, removed on cleanup
func dockerRun(t *testing.T, r *run, image, network string, cmd []string, opts runOpts) (stdout, stderr string, code int)
   // runOpts: Mounts (release dir read-only at /release), Env, Privileged (false), Detach, KeepContainer
func serveReleaseInNetwork(t *testing.T, r *run, dist string, network string) (baseURL string)
   // busybox httpd (pinned image) serving dist/ with the GitHub path layout (releases/download/v<ver>/ and releases/latest/download/ built as directories by the harness)
```

**Behaviour:**
- Images: `debian:bookworm-slim`, `ubuntu:24.04`, `rockylinux:9-minimal` (or `rockylinux:9`), `alpine:3.21` — pinned tags; each installs `curl ca-certificates openssh-client|openssh-clients|openssh-keygen tar` and, except Alpine, `systemd` (so `systemctl` exists but is not booted → the "enable only" branch). One Dockerfile each; no Stackstead files baked in.
- The release used is built in-test: `buildBinary` for `linux/<docker server arch>` (from `docker version --format '{{.Server.Arch}}'`), `fakeRelease` with real T3 unit and T1 archive layout (`make release`-equivalent via `mkrelease`), unsigned like a real release; the script is run with `--base-url http://<server container>`.
- Each test runs the script inside a fresh container as root; assertions via a second `docker exec` running `sh -c` checks (`stat -c`, `getent`, `ls`, `cat`), never by inspecting the host.
- Cleanup: containers removed with `--rm` or on cleanup; network and the server container removed; images kept (cache) but labelled; a final `docker ps -a --filter label=stackstead.test=<id>` sweep asserts nothing is left and removes leftovers.

**Tests (write first)** (each distro × case where it makes sense; `t.Parallel` across distros, one network per test):
- `TestFreshInstallLayout` (debian, ubuntu, rocky): exit 0; `/usr/local/bin/stackstead version` reports the fake version; `getent passwd stackstead` → home `/var/lib/stackstead`, nologin shell; `getent group docker` exists; `/opt/stacks` is `2775 stackstead:docker`; `/var/lib/stackstead` `0700`; `/etc/stackstead/config.yaml` `0640 root:stackstead`; unit at `/etc/systemd/system/` with the `/usr/local/bin` ExecStart; `/etc/systemd/system/multi-user.target.wants/stackstead.service` symlink exists (offline `enable`); stdout contains the "nothing was started" warning (systemd not booted) and the Docker warning (no Docker in the container).
- `TestAlpineNoSystemd`: exit 0; binary, busybox-created user (`adduser -S`), dirs; no unit; warning names the manual `serve` command.
- `TestUpgradeInContainer` (debian): install v1, write a marker into `/var/lib/stackstead` and edit the config, install v2 → version v2, marker and config intact, unit rewritten (contains the v2 build's ExecStart, identical here), `install.sh` helper updated.
- `TestSameVersionIsNoop` (ubuntu): second run → "already installed", exit 0, no file mtimes changed (`find -newer` against a sentinel).
- `TestChecksumMismatchInContainer` (rocky): a tarball altered after `SHA256SUMS` was written → exit 6 and `/usr/local/bin/stackstead` absent.
- `TestSignersPathInContainer` (debian, with `openssh-client`): a signed fake release (`opts.Sign`) installs with `--signers /release/allowed_signers`; the same release with a tampered `.sig` is refused (exit 6). Keeps the future signing path exercised on a real distro.
- `TestUninstallAndPurge` (debian): `--uninstall` keeps `/etc/stackstead`, `/var/lib/stackstead`, user; `--purge` removes them; `/opt/stacks` and a file inside it survive both.
- `TestDryRunInContainer` (ubuntu): exit 0 and no `/usr/local/bin/stackstead`.
- `TestRefusesOverPackage` (debian): fake a dpkg-owned `/usr/bin/stackstead` (`dpkg-divert`-free: create `/var/lib/dpkg/info/stackstead.list` listing it) → exit 8.
- `TestDashAndBusyboxParse`: `dash -n install.sh` (debian) and `busybox sh -n` (alpine) succeed; `checkbashisms` from `devscripts` in the debian image reports nothing.

**Acceptance:** `go test -tags installer -run Container -timeout 900s -parallel 4 ./test/installer/` green with Docker Desktop on this Mac (arm64 images) and on a Linux amd64 host if available; `docker ps -a`, `docker network ls` show no `stackstead-itest-*` leftovers; the user's own containers are never listed by any test command.

---

### Task 7: macOS `.pkg` — unsigned build, structural tests (signing path optional)

**Wave:** 3. Depends on T4 (Makefile baseline, version), T5 (plist template, macOS script path for uninstall).

**Files:** modify `Makefile`; create `deploy/macos/distribution.xml.in`, `deploy/macos/scripts/postinstall`, `deploy/macos/resources/{welcome,conclusion}.html`, `test/installer/pkg_darwin_test.go` (`//go:build installer && darwin`).

**Interfaces:**
```
make pkg [VERSION=…]                      → dist/stackstead_<ver>_darwin.pkg   (universal binary, ad-hoc codesigned, unsigned pkg; prints the Gatekeeper note)
  optional/future: [MACOS_APP_IDENTITY=…] [MACOS_INSTALLER_IDENTITY=…] [NOTARY_PROFILE=…]  → signed + notarized + stapled (all three required together)
payload: /usr/local/bin/stackstead, /usr/local/share/stackstead/{install.sh,launchagent.plist.in,LICENSE}
pkg identifier: io.github.rcss-labs.stackstead   install-location: /   min OS: 12.0
postinstall args: $1 pkg path, $2 target location, $3 target volume, $4 root ("/" when installing to the boot volume)
```

**Behaviour:**
- `pkg` is macOS-only (`uname -s` guard with a clear error elsewhere): `lipo -create` the two darwin binaries → `codesign -s -` (ad-hoc) → payload root `pkgroot/usr/local/bin/stackstead`, `pkgroot/usr/local/share/stackstead/{install.sh,launchagent.plist.in,LICENSE}` → `pkgbuild --root --identifier --version <ver> --install-location / --scripts deploy/macos/scripts` → `productbuild --distribution --resources --package-path`. This unsigned flow is the deliverable. **Optional/future, not in acceptance:** when `MACOS_APP_IDENTITY`, `MACOS_INSTALLER_IDENTITY` and `NOTARY_PROFILE` are all set, codesign with `--sign "$MACOS_APP_IDENTITY" --options runtime --timestamp`, `productbuild --sign "$MACOS_INSTALLER_IDENTITY"`, `xcrun notarytool submit --keychain-profile $NOTARY_PROFILE --wait`, `xcrun stapler staple`; partial settings (one or two of the three) fail the target with a message rather than producing a half-signed pkg.
- `postinstall` is POSIX sh, `set -eu`: when `$3` is not `/` (test volume) it exits 0 after printing "staged only"; otherwise it determines the console user (`stat -f %Su /dev/console`; if `root` or empty → prints how to run `install.sh --user` and exits 0 without failing the install), then delegates to `/usr/local/share/stackstead/install.sh --from-pkg --user <u>` which performs the per-user steps (dirs, config, plist, bootstrap, health) with the already-installed binary — no download. `--from-pkg` is a hidden flag added here to `install.sh` (T5 owns the file; T7 adds only this flag and its test, coordinated by merge order: T5 before T7).
- Distribution XML: title "Stackstead", `allowed-os-versions` 12.0, `hostArchitectures="x86_64,arm64"`, welcome/conclusion pages (conclusion repeats the URL and the uninstall command), `rootVolumeOnly="false"` so the test-volume install is possible.
- Gatekeeper (documented in README, release notes and the pkg's conclusion page): double-clicking the unsigned pkg is blocked ("from an unidentified developer"); right-click → Open, or `sudo installer -pkg … -target /` from a terminal, installs it; `spctl --assess --type install` reports "rejected", which is expected; the installed binary runs normally (ad-hoc signature; no quarantine attribute is set by `installer` or `curl`). `make pkg` prints this block and `RELEASING.md` tells the maintainer to paste it into the release notes.

**Tests (write first)** `pkg_darwin_test.go` (macOS host only; builds the pkg into a temp `DIST` with `VERSION=v0.0.1 ALLOW_DEV_RELEASE=1`):
- `TestPkgPayloadAndScripts`: `pkgutil --expand-full` → payload has exactly `usr/local/bin/stackstead` (0755, `lipo -archs` prints `x86_64 arm64`, `codesign -dv` succeeds and reports an ad-hoc signature), `usr/local/share/stackstead/install.sh`, `…/launchagent.plist.in`, `…/LICENSE`; `Scripts/postinstall` present and executable; `Distribution` has the identifier, version, min OS and both host architectures; `PackageInfo` has `install-location="/"`.
- `TestPkgUnsignedPrintsGatekeeperNote`: default build output contains the Gatekeeper block; `pkgutil --check-signature` reports "no signature"; `spctl --assess --type install` rejects it (expected, asserted).
- `TestPkgPartialIdentityFails`: `MACOS_APP_IDENTITY` set alone → non-zero with the message, no pkg produced (cheap guard for the optional path; needs no certificate).
- `TestPostinstallStagesOnlyOnTestVolume`: run `postinstall` with `$3=/Volumes/x` and stubbed PATH → exit 0, no `launchctl` call.
- `TestPkgInstallOnTestVolume`: skipped unless `STACKSTEAD_PKG_TEST_VOLUME` names a mounted volume (`RELEASING.md` shows `hdiutil create -size 200m -fs APFS -volname sstest -attach`); runs `sudo installer -pkg <pkg> -target $VOLUME -dumplog`, asserts the binary landed under the volume, no agent was bootstrapped, `pkgutil --pkgs --volume $VOLUME` lists the identifier, then `pkgutil --forget --volume`.
- `TestShellcheckPostinstall`.

**Acceptance:** `make pkg` on this Mac produces an unsigned universal pkg with `LICENSE` in the payload; `go test -tags installer -run Pkg -timeout 600s ./test/installer/` green (volume test green once with the env set); the signed/notarized path is **not** exercised (no identity) and not required; **manual**: `sudo installer -pkg dist/stackstead_0.0.1_darwin.pkg -target /` on this Mac → agent running, UI answers, `sudo /usr/local/share/stackstead/install.sh --uninstall` → clean (`launchctl list`, `pkgutil --pkgs | grep stackstead` empty). Recorded in the task report.

---

### Task 8: Package container tests (deb/rpm lifecycle)

**Wave:** 3. Depends on T4 (packages), T6 (docker harness, images).

**Files:** create `test/installer/package_test.go` (`//go:build installer`).

**Interfaces:**
```go
func installPackage(t *testing.T, r *run, container, path string) (out string, code int)   // apt-get install -y ./x.deb | dnf install -y ./x.rpm (image-dependent)
func packageVersion(t *testing.T, r *run, container string) string                          // dpkg-query -W -f '${Version}' | rpm -q --qf '%{VERSION}'
```

**Behaviour:**
- Builds two versions (`v0.0.1`, `v0.0.2`) of the packages for the docker server arch into a temp `DIST` with `make packages`; mounts `DIST` read-only; uses the T6 debian/ubuntu/rocky images (systemd installed, not booted — maintainer scripts must cope; `systemd-analyze verify` is used for the unit).
- Assertions always via `docker exec` checks, as in T6.

**Tests (write first)**:
- `TestDebFreshInstall` (debian, ubuntu): exit 0; `/usr/bin/stackstead version` reports the package version; user/group/dirs/modes as T6 expects (same layout rules); `dpkg-query -W -f '${Conffiles}'` lists `/etc/stackstead/config.yaml`; unit at `/usr/lib/systemd/system/`; `systemd-analyze verify /usr/lib/systemd/system/stackstead.service` clean; stdout shows the setup URL hint and the Docker warning.
- `TestDebUpgradeKeepsData`: install v1, modify config, drop `stackstead.db` marker, install v2 → conffile unchanged (dpkg keeps the modified conffile without prompting because the packaged file did not change; the test also asserts no `*.dpkg-dist`), marker intact, version v2, `prerm upgrade`/`postinst configure 0.0.1` branch observed via a stub-free check: the unit's `wants` symlink still exists and the service was not disabled.
- `TestDebRemoveKeepsConfigAndData`, `TestDebPurgeKeepsStacks`: `apt-get remove` → config/data/user remain, unit gone; `apt-get purge` → config/data/user gone, `/opt/stacks/<file>` remains; `dpkg -l stackstead` shows `rc` then nothing.
- `TestRpmFreshInstall` (rocky): same checks; `rpm -qc stackstead` lists the config; `rpm -V stackstead` clean right after install.
- `TestRpmUpgradeRestartsOnce` (rocky): `dnf install v1`, `dnf install v2` → config `.rpmsave`/`.rpmnew` absent (noreplace and unchanged), data marker intact; `%preun 1` did not disable the unit (wants symlink present); `rpm -q` shows v2.
- `TestRpmRemoveKeepsData`: `dnf remove` → config kept as-is or saved as `.rpmsave` only if modified, data and `/opt/stacks` kept, user kept (documented manual purge).
- `TestReinstallSameVersionIsClean` (debian): `apt-get install --reinstall` → exit 0, scripts idempotent (no duplicate user error).
- `TestPackageWithoutDockerStartsUnit` is covered in T9 (needs booted systemd).

**Acceptance:** `go test -tags installer -run 'Deb|Rpm|Reinstall' -timeout 900s ./test/installer/` green; no leftovers (same sweep as T6).

---

### Task 9: Booted-systemd smoke (opt-in), README, release runbook, end-to-end dry run

**Wave:** 4. Depends on everything.

**Files:** create `test/installer/systemd_test.go` (`//go:build installer`); modify `README.md`, `deploy/release/RELEASING.md`, this plan (tick boxes).

**Behaviour:**
- **Systemd smoke** (skipped unless `STACKSTEAD_SYSTEMD_TEST=1`; documented as needing `--privileged`, `--cgroupns=host`, `-v /sys/fs/cgroup:/sys/fs/cgroup:rw`, `/sbin/init` as the entrypoint on the T6 debian image — works with Docker Desktop's cgroup v2 and on Linux hosts; if it proves unworkable on this Mac, the test stays as the documented manual VM check and the report says so): boot the container, wait for `systemctl is-system-running` (`running` or `degraded`), run `install.sh` (script case) and `apt-get install ./…deb` (package case) → `systemctl is-active stackstead` is `active` within 15 s, `curl -s http://127.0.0.1:8420/api/health` → `{"ok":true}` and `/api/setup/status` → `{"needsSetup":true}`, `journalctl -u stackstead` contains `setup_token` (the value is not asserted or printed by the test), the service runs as `stackstead` with supplementary group `docker` (`ps -o user,group,supgrp`), `systemctl show -p ProtectSystem,NoNewPrivileges` match the unit; upgrade → `systemctl show -p NRestarts`/`ActiveEnterTimestamp` changed once; `--uninstall`/`apt-get remove` → unit inactive and gone.
- **README** "Install" section replaces "Install (systemd)": the one-liner (`curl -fsSL https://raw.githubusercontent.com/rcss-labs/stackstead/main/install.sh | sudo sh`, with `-s -- --version vX.Y.Z` pinning), packages (`apt install ./…`, `dnf install ./…`), macOS (`.pkg` and the script; LaunchAgent semantics: runs while you are logged in; Gatekeeper note for unsigned pkgs), paths table per platform, what first run looks like (URL, token via `journalctl`/log file), Docker requirement and `sudo -u stackstead -H docker login` for private registries, upgrade/uninstall/purge semantics (never removes stacks or Docker resources; rpm manual purge), verifying a download by hand (`sha256sum -c SHA256SUMS --ignore-missing` / `shasum -a 256 -c`; a short "releases are not signed yet" sentence pointing at `--signers` for the future), the security paragraph (docker group = root; hardening is defense in depth), `systemctl edit` for unit overrides, and the exit-code table. A "License" section: AGPL-3.0-only, link to `LICENSE`, and the AGPL §13 note — if you run Stackstead for others over a network, the public repository satisfies the source offer for unmodified published versions (the version/commit is shown by `stackstead version` and `/api/status`); a modified network-served fork must publish its own source. The old manual install steps move under "Manual install" (still valid). Remove the "Until the web UI exists" paragraph if Plan 5 T23 has not already.
- **RELEASING.md**: prerequisites (Node, Go, macOS for `pkg`), `git tag -a vX.Y.Z`, `make dist VERSION=vX.Y.Z`, `make verify-release`, `make package-lint`, the test targets to run, `gh release create vX.Y.Z dist/* --notes-file …` with the notes template (always includes the Gatekeeper block and the `SHA256SUMS` contents), how to do the volume-based pkg test, and a **"Future: signing"** section (generate an ed25519 key, `SIGNING_KEY=`, publish the key in the script and `deploy/release/allowed_signers`, flip `--signers` to default; Apple Developer ID: the three variables and the notarytool profile). Nothing in the required flow references a key.
- **End-to-end dry run** of the runbook at `v0.0.1-rc.1` into a local `dist/` (no publishing): every artifact present and `LICENSE` inside each tarball, the deb (`copyright`) and the rpm (`rpm -qpL`), `verify-release` green, `install.sh --dry-run --base-url file-served-dist` on this Mac and in a debian container.

**Tests (write first):** the systemd smoke above, and `TestReadmeExitCodesMatchHelp` (added to `script_test.go`; T2's file, sequential by wave): the exit-code table in the README and the codes listed by `install.sh --help` are the same set. Nothing else in the docs is tested.

**Acceptance:** `make check` green; `go test -tags installer -timeout 1800s ./... ` green (systemd test run once with the env var, result recorded); `make dist VERSION=v0.0.1-rc.1 ALLOW_DEV_RELEASE=1` produces every artifact in the design spec §4 layout; README reviewed against the actual script output; plan tick boxes updated.

---

## Self-review notes

- **User decisions coverage:** script with mandatory checksum verification and a dormant, tested signature path, user/dirs/service, idempotent upgrade, uninstall, `--version`, dry-run, POSIX `set -eu`, help, exit codes (T2, T5, T6); deb/rpm with unit, user, upgrade-safe scripts, purge semantics, hardened unit with docker group and the documented implication, weak `Recommends` on Docker (T3, T4, T8, T9 README); nfpm evaluated and pinned (design §3, T4); macOS pkg with LaunchAgent, unsigned with Gatekeeper documented and an optional future signing path (T5, T7); GitHub Releases layout, checksums, Makefile-driven builds (T1, T9); AGPL-3.0-only `LICENSE` in the repo and every artifact, README license note (T1, T3, T7, T9); four targets, `CGO_ENABLED=0` (T1); Docker checked, never installed (T2, T3, T5); paths from `config.Defaults()` and FHS (design §4); first-run via the UI, no passwords in installers (T2/T5 output, README); tests: shellcheck, container runs against locally built artifacts on a private network with unique names and cleanup, lint in containers, pkg checks with `pkgutil` and an opt-in test volume (T2, T4, T6, T7, T8, T9).
- **Plan 6 coupling** is confined to the table in "Dependencies on Plan 6" and the four tasks it names; every item has a fallback.
- **Not covered on purpose:** apt/yum repos, Homebrew, apk, armv7, OpenRC, rootless Docker, CI (deferred), admin creation from installers.

## Open questions for the user

None outstanding. All earlier questions were decided on 2026-10-14 and are recorded under **Decisions** (license AGPL-3.0-only, no signing for now, unsigned `.pkg`, identifier, rpm start policy, macOS stacks dir, weak Docker `Recommends`, `armv7` out of scope) and in the design spec §8.

## Future work (explicitly not in this plan)

- **Release signing:** generate an ed25519 ssh key, embed the public half in `install.sh` and `deploy/release/allowed_signers`, make `--signers` the default, publish the fingerprint. The code path and tests exist after T2/T6; `RELEASING.md` carries the steps.
- **Apple Developer ID:** signed + notarized `.pkg` via the three Makefile variables kept in T7; needs the "Developer ID Application" and "Developer ID Installer" certificates and a `notarytool` keychain profile.
