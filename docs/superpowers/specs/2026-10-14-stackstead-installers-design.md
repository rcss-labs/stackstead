# Stackstead Installers — Design

**Date:** 2026-10-14
**Status:** Draft, awaiting review. Scope added by the user beyond the v1 spec (`2026-10-14-stackstead-v1-design.md`), implemented by Plan 7 after Plans 5 (UI) and 6 (CLI).

## 1. Purpose

Get Stackstead from a GitHub Release onto a Linux server or a macOS workstation in one command, as a supervised service, without the user hand-copying files from the README. Three delivery formats, one layout each platform, one uninstall story, and verification of what was downloaded.

Success criteria:

- `curl -fsSL <url>/install.sh | sudo sh` on Debian/Ubuntu/Fedora/Rocky (amd64 or arm64) leaves `stackstead` running under systemd, prints the URL to finish first-run setup, and running it again is a no-op or an in-place upgrade.
- `apt install ./stackstead_<ver>_<arch>.deb` and `dnf install ./stackstead-<ver>-1.<arch>.rpm` do the same; upgrades never touch data or config; `apt purge` removes exactly what the package owns.
- On macOS, the same script or a `.pkg` installs a per-user LaunchAgent that reaches Docker Desktop's socket.
- Every artifact is listed in `SHA256SUMS`, and the script refuses to install anything whose checksum does not match. Releases are not signed for now (user decision); signature verification is designed in but disabled by default and never blocks an install.
- Nothing installs Docker. Installers detect it and warn.

## 2. Scope

### In
- `install.sh` (POSIX sh) for Linux (systemd) and macOS (launchd): install, upgrade, `--version` pin, `--dry-run`, `--uninstall [--purge]`, `--root` staging.
- `.deb` and `.rpm` (amd64, arm64) built with nfpm, with maintainer scripts and a hardened unit.
- macOS `.pkg` (universal binary) built with `pkgbuild`/`productbuild`, unsigned for now; an identity-gated signing + notarization path is kept in the Makefile as optional/future, not part of any acceptance criterion.
- Release layout on GitHub Releases with `SHA256SUMS` (no signatures for now), Makefile targets producing all of it locally.
- `LICENSE` (AGPL-3.0) at the repo root, shipped in every tarball, package and the `.pkg`.
- Automated tests: shellcheck, host-level script tests with `--root` staging, container tests for the script and the packages, package lint, `.pkg` structural checks.

### Out
- CI workflows (deferred, as in Plan 5). Publishing to GitHub Releases is manual (`gh release create`), documented.
- apt/yum repositories, Homebrew formula/cask, Alpine `.apk`, 32-bit ARM (`armv7`), Windows, container images (v1 spec §2).
- Installing Docker, OpenRC/SysV init scripts, rootless Docker setups (documented: set `docker_socket`).
- Creating the admin account from the installer (first-run setup stays in the UI: loopback or setup token, v1 spec §6).

## 3. Key decisions

| Decision | Choice | Reason |
|---|---|---|
| Verification | SHA-256 checksums only: `install.sh` always verifies the downloaded tarball against `SHA256SUMS` (exit 6 on mismatch or when the asset is not listed); there is no way to skip it | User decision: no release signing for now. The checksum still catches truncated, mirrored-wrong, or wrong-architecture downloads; it does not authenticate the publisher (that is TLS to GitHub, see §6). |
| Signature support (designed, disabled) | The script keeps an optional `--signers FILE` / `STACKSTEAD_INSTALL_SIGNERS_FILE` path that verifies `SHA256SUMS.sig` with `ssh-keygen -Y verify` when (and only when) a signers file is given; no key is embedded; absence of `.sig` is not an error unless signers were requested. `make release` signs only when `SIGNING_KEY` is set (off by default) | Keeps the future switch cheap: `ssh-keygen -Y` (OpenSSH ≥ 8.2) is on every target without a second download, which is why it is preferred over minisign/cosign for a later "signed releases" step; cosign keyless becomes attractive once CI exists. The design is tested with a throwaway key so turning it on is a key + docs change, not a code change. |
| License | AGPL-3.0-only (SPDX `AGPL-3.0-only`): `LICENSE` at the repo root with the full text; `license: AGPL-3.0-only` in nfpm (rpm `License:` tag, `%license` file), DEP-5 `copyright` file in the deb, `LICENSE` in tarballs and in the `.pkg` payload | User decision (AGPL-3.0). `-only` is the plain reading of "AGPL-3.0" and nothing in the v1 spec suggests `-or-later`; it can be widened later, not narrowed. Implication: AGPL §13 requires offering the source to users who interact with Stackstead over a network; the public repository satisfies this as long as the running build corresponds to a published version — the README says so and recommends not stripping `stackstead version` (which names the commit) from forks. |
| Package builder | nfpm, pinned to an exact version, run with `go run github.com/goreleaser/nfpm/v2/cmd/nfpm@<ver>` | One YAML produces deb and rpm on macOS without dpkg/rpm toolchains; `go run pkg@ver` is verified by the Go checksum database and never touches `go.mod`. Hand-rolled `dpkg-deb`/`rpmbuild` would need Linux containers for every build and two sets of metadata to keep in sync. nfpm is build-time only. |
| Linux service user | `stackstead` system user, home `/var/lib/stackstead`, in the unit via `SupplementaryGroups=docker` (group created if missing) | Keeps the socket access visible in the unit. The home dir lets `sudo -u stackstead -H docker login` persist registry credentials (v1 spec: relies on host `docker login`). The `docker` group must exist or systemd refuses to start the unit, so installers create it. |
| Unit hardening | `NoNewPrivileges`, `ProtectSystem=full`, `PrivateTmp`, `ProtectKernel*`, `ProtectControlGroups`, `RestrictSUIDSGID`, `RestrictRealtime`, `LockPersonality`, `SystemCallArchitectures=native`, `CapabilityBoundingSet=`, `UMask=0027`, `StateDirectory=stackstead` | Defense in depth against bugs in Stackstead's own process. **Not** `ProtectSystem=strict` or `ProtectHome`: the service edits external compose files anywhere the user registers them (v1 spec §3 "full filesystem access"). Documented plainly: Docker-socket access is root-equivalent; the hardening is not a security boundary. |
| Linux paths | Packages: `/usr/bin/stackstead`, `/etc/stackstead/config.yaml` (conffile), `/var/lib/stackstead`, `/opt/stacks`, `/usr/lib/systemd/system/stackstead.service`. Script: `/usr/local/bin`, `/usr/local/share/stackstead/`, `/etc/systemd/system/stackstead.service`, same config/data/stacks dirs | FHS: packages own `/usr`, local installs own `/usr/local`. Config and data paths are the existing `config.Defaults()`. The script refuses to run over a package-managed install (exit 8) so the two never fight over the unit. |
| macOS service type | LaunchAgent (per logged-in user), never a LaunchDaemon | Docker Desktop, OrbStack and Colima run in the user's session and expose per-user sockets (`~/.docker/run/docker.sock` etc.); `/var/run/docker.sock` is an optional symlink that only exists while that user's Docker is running. Compose also needs the user's `~/.docker/config.json` and `docker` CLI. A daemon would run as root with no reliable socket. Consequence (documented): Stackstead runs while that user is logged in; macOS is a workstation target, Linux is the server target. |
| macOS paths | Binary `/usr/local/bin/stackstead` (sudo, same for script and pkg); per user: `~/Library/Application Support/Stackstead/{config.yaml,data,stacks}`, log `~/Library/Logs/Stackstead/stackstead.log`, plist `~/Library/LaunchAgents/<label>.plist` with absolute paths baked in at install time | launchd does not expand `~` or env vars in `ProgramArguments`, so the plist is generated per user from `deploy/macos/launchagent.plist.in`. No Go change needed. `docker_socket` is detected with `docker context inspect` at install time. |
| macOS universal binary | One `.pkg` with a `lipo`-merged binary, re-signed ad-hoc (or with Developer ID) | One download for both Macs; Go's cross-compiled slices are ad-hoc signed already, lipo keeps that but re-signing is cheap insurance. |
| Unsigned pkg (for now) | `make pkg` builds an unsigned, ad-hoc-codesigned universal pkg and prints the Gatekeeper note; the identity-gated path (`MACOS_INSTALLER_IDENTITY` + `MACOS_APP_IDENTITY` + `NOTARY_PROFILE` → sign, notarize, staple) stays in the Makefile as optional/future and is not required by any acceptance | User decision: no Apple Developer ID for now. Gatekeeper behaviour, documented in the README and the release notes: double-clicking an unsigned pkg shows "cannot be opened because it is from an unidentified developer"; right-click → Open, or `sudo installer -pkg … -target /` from a terminal, installs it; `spctl --assess --type install` reports it as rejected. The binary itself runs: Go's cross-compiled darwin binaries carry an ad-hoc signature, and neither `installer` nor `curl` sets the quarantine attribute. |
| Release naming | goreleaser-compatible: `stackstead_<ver>_<os>_<arch>.tar.gz`, `stackstead_<ver>_<arch>.deb`, `stackstead-<ver>-1.<x86_64\|aarch64>.rpm`, `stackstead_<ver>_darwin.pkg`, `SHA256SUMS`, `install.sh` (`<ver>` without `v`; a future `SHA256SUMS.sig` slots in without renaming anything) | Lets a later CI move to goreleaser without changing URLs the script and docs rely on. |
| Latest-version resolution | `https://github.com/rcss-labs/stackstead/releases/latest/download/SHA256SUMS` (GitHub redirects); the version is read from the asset names inside | No API calls, no JSON parsing in sh, no rate limit. |
| Package start semantics | Fresh install: enable and start (deb and rpm alike). Upgrade: `daemon-reload` + `try-restart`. Remove: stop, disable, keep config/data. deb `purge`: remove `/etc/stackstead`, `/var/lib/stackstead`, the user. Never remove `/opt/stacks` (user's compose files) or anything in Docker | One behaviour on both formats is easier to document than following Fedora's "don't start on install" convention; the deviation is stated in the README. Compose files and containers are user data by the v1 spec ("Removing a stack never deletes files"). |
| Tarball/checksum tool | `tools/mkrelease` (Go, stdlib) writes deterministic tar.gz and `SHA256SUMS` | GNU `tar` and macOS `bsdtar` differ in every determinism flag; a 150-line Go program is portable and unit-testable. |
| Test strategy | shellcheck + `sh -n`; host tests run the script with `--root <tmp>` against an `httptest` fake release; container tests (Debian, Ubuntu, Rocky, Alpine) run the real flow against a static server on a private Docker network; package tests install/upgrade/remove/purge the real deb/rpm; lint in containers; `.pkg` checked with `pkgutil --expand-full`, optional `installer -target <test volume>` | Everything is behind the `installer` build tag or opt-in Makefile targets; `make test` stays Node-free and Docker-free. Unique names and labels on every test container and network. |

## 4. Layouts

### Release (GitHub Releases, tag `vX.Y.Z`)

```
stackstead_X.Y.Z_linux_amd64.tar.gz     stackstead, install.sh, stackstead.service, config.example.yaml, README.md, LICENSE
stackstead_X.Y.Z_linux_arm64.tar.gz
stackstead_X.Y.Z_darwin_amd64.tar.gz    stackstead, install.sh, launchagent.plist.in, config.example.yaml, README.md, LICENSE
stackstead_X.Y.Z_darwin_arm64.tar.gz
stackstead_X.Y.Z_amd64.deb              stackstead_X.Y.Z_arm64.deb
stackstead-X.Y.Z-1.x86_64.rpm           stackstead-X.Y.Z-1.aarch64.rpm
stackstead_X.Y.Z_darwin.pkg             universal, unsigned (ad-hoc codesigned binary); LICENSE in the payload
SHA256SUMS                              sha256sum format, one line per asset above (and install.sh)
install.sh                              also fetched from the repo at a stable raw URL
(future) SHA256SUMS.sig                 ssh-keygen -Y sign -n stackstead-release; only once a key exists
```

### Linux

| | Package | Script |
|---|---|---|
| binary | `/usr/bin/stackstead` | `/usr/local/bin/stackstead` |
| unit | `/usr/lib/systemd/system/stackstead.service` | `/etc/systemd/system/stackstead.service` |
| config | `/etc/stackstead/config.yaml` (conffile, 0640 root:stackstead) | same, written only if absent |
| data | `/var/lib/stackstead` (0700 stackstead) | same |
| stacks | `/opt/stacks` (2775 stackstead:docker) | same |
| uninstall helper | — (package manager) | `/usr/local/share/stackstead/install.sh --uninstall` |

`ExecStartPre=<bindir>/stackstead config validate --config /etc/stackstead/config.yaml` and `ExecStart=<bindir>/stackstead serve --config /etc/stackstead/config.yaml` (Plan 6 CLI contract; `version` output's second field is the tag the script compares on upgrade).

### macOS (per user `$HOME`)

```
/usr/local/bin/stackstead                                   root-owned, 0755
/usr/local/share/stackstead/{install.sh,launchagent.plist.in,LICENSE}
~/Library/Application Support/Stackstead/config.yaml        listen, stacks_dir, data_dir, docker_socket
~/Library/Application Support/Stackstead/data/              stackstead.db
~/Library/Application Support/Stackstead/stacks/
~/Library/Logs/Stackstead/stackstead.log                    launchd StandardOut/ErrorPath; setup token lands here
~/Library/LaunchAgents/io.github.rcss-labs.stackstead.plist RunAtLoad, KeepAlive, PATH incl. /usr/local/bin and /opt/homebrew/bin
```

## 5. Flows

### install.sh (Linux)
1. Parse flags; require root (exit 8) unless `--root`; detect `uname -s/-m` (exit 3 if unsupported); check tools: `curl`|`wget`, `tar`, `sha256sum`|`shasum`|`openssl` (exit 4 when missing, with the package name per distro); `ssh-keygen -Y` is required only when `--signers` is given.
2. Refuse if `/usr/bin/stackstead` is owned by dpkg/rpm (exit 8, "use apt/dnf").
3. Resolve version (`--version`, `STACKSTEAD_VERSION`, else latest via the redirect), download `SHA256SUMS` and the tarball into a temp dir; verify the tarball checksum against the listed one (exit 6 on mismatch or when the asset is not listed). Only with `--signers FILE`: also fetch `SHA256SUMS.sig` and verify it first (exit 6 if missing or bad).
4. Compare with the installed `stackstead version --short`; same version and not `--force` → "already installed", exit 0. `--dry-run` stops here and prints the plan.
5. Create group `docker` if missing, user `stackstead` (`useradd` or busybox `adduser`), dirs, config if absent, unit (ExecStart path rewritten for the prefix), copy `install.sh` to the share dir; replace the binary atomically (`.new` + `mv`).
6. If systemd is booted: `daemon-reload`, first install → `enable --now`, upgrade → `restart` (or nothing with `--no-start`); if systemd is installed but not running (container): `enable` only, warn; no systemd: warn, exit 0.
7. Readiness: poll `GET http://<listen>/api/health` (Plan 6's unauthenticated liveness route; fallback `/api/setup/status`) for up to 15 s with the same downloader; print the setup URL, the `journalctl` hint for the token, the Docker warning if Docker/compose were not found. `stackstead health` is not the gate: it exits 7 when Docker is absent and refuses to run as root over the service user's database (CLI design §3); it is run once as the service user for its summary only.

### install.sh (macOS)
Same download/verify; target user is `$SUDO_USER` or the invoking user (root without `SUDO_USER` → exit 8); `sudo` is used only for `/usr/local`; the plist is rendered from the template with the user's absolute paths and the detected socket; `launchctl bootout` (ignore failure) then `bootstrap gui/<uid>`; readiness via `/api/health`; print `http://127.0.0.1:8420/`.

### Relation to the CLI design
Plan 7 uses `serve`, `version --short`, `/api/health` and optionally `config validate` (as `ExecStartPre`) and `health` (summary only). It does **not** call `admin set-password` or `db init`: installers print the setup URL and the token hint, and the README names `admin set-password` only as the password-recovery path. The CLI design (`2026-10-14-stackstead-cli-design.md`, §3 and §7) records the same split, including Makefile ownership (Plan 6: `LDFLAGS`, `build-go`; Plan 7: `release`, `dist`, packaging).

### Packages
`preinst`/`%pre`: group + user. Payload: binary, unit, conffile, dirs, lintian overrides. `postinst`/`%post`: `/opt/stacks`, `daemon-reload`, enable+start or try-restart, print the URL. `prerm`/`%preun`: stop+disable on remove only (not on upgrade). `postrm`/`%postun`: `daemon-reload`; deb `purge` removes config, data and the user. Both argument conventions (deb words, rpm counts) are handled explicitly.

### .pkg
`preinstall`: none. Payload: `/usr/local/bin/stackstead`, `/usr/local/share/stackstead/*`. `postinstall`: when the target volume is `/`, render the plist for the console user (`stat -f %Su /dev/console`), create dirs and config, bootstrap the agent; otherwise (test volume) only stage files. Uninstall: `sudo /usr/local/share/stackstead/install.sh --uninstall` (also `pkgutil --forget`).

## 6. Supply-chain reasoning

- **Trust roots (current):** TLS to GitHub and the repository itself (the script lives there). This is the same root every `curl | sh` has. The checksum protects against truncated, corrupted, mirrored-wrong or wrong-architecture downloads; it does **not** authenticate the publisher — anyone who can edit the release page can edit `SHA256SUMS` too. Users who want more pin a version (`--version`) and compare `SHA256SUMS` against the one in the release notes or the repo tag.
- **Future: signing.** No key exists yet (user decision). When one is created: ed25519 ssh key held outside the repo, `make release SIGNING_KEY=…` produces `SHA256SUMS.sig`, the public key goes into the script and `deploy/release/allowed_signers`, and `--signers` becomes the default. The verification code path and its tests (throwaway key) are built now so the switch is a key-management and documentation change. Cosign keyless (OIDC provenance) is the alternative once CI builds releases.
- **Build tools:** nfpm via `go run pkg@version` (sumdb-verified, no `go.mod` change); shellcheck via a pinned container tag; Apple tools are part of Xcode CLT. No tool is a runtime dependency.
- **Reproducibility:** `CGO_ENABLED=0 -trimpath -ldflags "-s -w -X …"`; `tools/mkrelease` writes archives with fixed owner/mtime/order, so a rebuild at the same commit and toolchain yields the same `SHA256SUMS` (asserted in a test). `go version -m` on an installed binary shows the module hashes.
- **Install-time hooks:** `--base-url` (mirrors, tests, air-gapped hosts) and `--signers` exist and are documented; the script prints a notice whenever a non-default base URL is in effect.

## 7. Security notes for the docs

- The `stackstead` user is in the `docker` group, which is root-equivalent on the host; the unit hardening limits accidents, not a determined attacker who already controls the UI. Keep `listen` on localhost or behind an authenticating TLS proxy (v1 spec §7).
- On macOS the service runs as your own user; the UI is as powerful as your shell.
- Uninstall never removes compose files, images, containers or volumes; it says so.
- License: AGPL-3.0-only. The README states that running Stackstead for others over a network (AGPL §13) is covered by the public repository for unmodified published versions, and that modified network-served forks must publish their source.

## 8. Resolved decisions (user, 2026-10-14)

Identifier `io.github.rcss-labs.stackstead` (launchd label and pkg id); rpm enables and starts on fresh install like deb; macOS stacks dir `~/Library/Application Support/Stackstead/stacks`; weak dependency `Recommends: docker-ce | docker.io` (deb) and `Recommends: docker-ce` (rpm), never `Depends`; `armv7` out of scope; no release signing and an unsigned `.pkg` for now; AGPL-3.0. From Plan 6: bare `stackstead` prints help, `stackstead serve` runs the server, `/api/health` is the unauthenticated liveness route, `golang.org/x/term` is accepted as the CLI's dependency.
