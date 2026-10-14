# Releasing Stackstead

Everything is built locally by Makefile targets (CI is deferred) and published by hand with
`gh release create`. Releases are **unsigned**: they ship `SHA256SUMS` only. The flow below never
needs a key, a certificate or a notarization profile.

## Prerequisites

- A macOS host: `make dist` also builds the universal `.pkg` there (`pkgbuild`, `productbuild`,
  `lipo`, `codesign` from the Xcode command line tools). On Linux the `.pkg` step is skipped with a message,
  so a Linux-only release has no `.pkg`; do not publish one built that way as the official release.
- Go (see `go.mod`) and Node >= 22 for the UI that is embedded in the binary.
  A release always builds the UI; `SKIP_UI=1` exists for tests only.
- Docker, for `make package-lint` and the container tests.
- `gh`, authenticated to the repository.
- Network access the first time: `go run` fetches the pinned nfpm (`NFPM_VERSION` in the Makefile, verified
  by the Go checksum database).
- A clean checkout of the commit to release: an untracked or modified file makes the version `-dirty`, and
  dirty and untagged versions are refused (see "Version strings").

## Version strings

The git tag `vX.Y.Z` (or `vX.Y.Z-rc.N`) becomes artifact version `X.Y.Z` (`X.Y.Z-rc.N`); deb and rpm use
`X.Y.Z~rc.N`, which sorts before the final release. Untagged commits are `0.0.0-dev.<sha>` or
`vX.Y.Z-N-g<sha>`; `make release`, `make packages` and `make pkg` refuse them unless `ALLOW_DEV_RELEASE=1`
(only for dry runs and tests; never publish such a build).

## Steps

1. **Check the tree.** On the commit to release: `make check` (UI checks, vet, gofmt, Go tests) and the installer
   tests below.
2. **Tag.** `git tag -a vX.Y.Z -m "Stackstead X.Y.Z"`. Do not push the tag yet.
3. **Build everything.**

   ```sh
   make dist
   ```

   (`VERSION` comes from `git describe`; pass `VERSION=vX.Y.Z` to build for a tag that is not checked out.) It
   runs, in this order: `binaries` (UI, then `CGO_ENABLED=0 -trimpath` builds of linux and darwin on amd64 and arm64
   into `bin/`), `packages` (deb and rpm via nfpm), `pkg` (macOS only), and `release` (tarballs and `SHA256SUMS`).
   The order matters: `release` last, because `SHA256SUMS` covers every file in `dist/`. Each step refuses a
   `bin/` binary whose `version --short` does not match the release (a stale build), where the host can run it.
   Output in `dist/`:

   ```
   stackstead_X.Y.Z_{linux,darwin}_{amd64,arm64}.tar.gz   binary, install.sh, unit or plist template, config.example.yaml, README.md, LICENSE
   stackstead_X.Y.Z_{amd64,arm64}.deb
   stackstead-X.Y.Z-1.{x86_64,aarch64}.rpm
   stackstead_X.Y.Z_darwin.pkg
   install.sh
   SHA256SUMS
   ```

4. **Verify the checksums.** `make verify-release` checks every line of `dist/SHA256SUMS` and fails on a file in
   `dist/` that the sums do not list.
5. **Lint the packages.** `make package-lint` runs `lintian --fail-on error,warning` on the debs and
   `rpmlint --strict` on the rpms in a pinned container. Both must be clean.
6. **Run the installer tests.**

   ```sh
   make installer-test                                         # vet, host tests (sh and dash), container suites
   STACKSTEAD_SYSTEMD_TEST=1 go test -tags installer -count=1 -run SystemdSmoke -timeout 900s ./test/installer/
   ```

   The second one boots systemd in a privileged container and installs the real binary and the real `.deb`; it needs
   Docker with `--privileged` and `--cgroupns=host` (Docker Desktop on cgroup v2 and Linux hosts work). Always run
   with `-count=1`: these tests drive `make`, `install.sh` and `deploy/` in subprocesses that Go's test cache cannot see.
7. **Test the macOS package for real** (needs `sudo`; see the next section).
8. **Publish.**

   ```sh
   git push origin vX.Y.Z
   gh release create vX.Y.Z dist/* --title "Stackstead X.Y.Z" --notes-file RELEASE_NOTES.md   # add --prerelease for -rc
   ```

   The script's one-liner uses `releases/latest/download/`, which GitHub points at the newest non-prerelease. Check
   the release page afterwards: every file from step 3 is there, and
   `curl -fsSL .../releases/download/vX.Y.Z/SHA256SUMS | sha256sum -c --ignore-missing` passes on a downloaded copy.

### Release notes template

Always include the Gatekeeper block and the checksums:

````markdown
## Install

    curl -fsSL https://raw.githubusercontent.com/rcss-labs/stackstead/main/install.sh | sudo sh

Or use the `.deb`, `.rpm` or macOS `.pkg` below. Details: docs/install.md.

## macOS package

The `.pkg` is not signed with an Apple Developer ID. Double-clicking it shows "cannot be opened because it is
from an unidentified developer"; right-click the package and choose Open, or run
`sudo installer -pkg stackstead_X.Y.Z_darwin.pkg -target /` from a terminal. `spctl --assess --type install`
reports "rejected" for an unsigned package; that is expected. The installed binary runs normally (it is ad-hoc
signed, and neither installer nor curl sets a quarantine attribute).

## Checksums (SHA256SUMS)

Releases are not signed yet: verify downloads with `sha256sum -c SHA256SUMS --ignore-missing`
(`shasum -a 256 -c ...` on macOS).

    <paste dist/SHA256SUMS>

## Changes

...
````

## macOS .pkg (unsigned)

`make pkg` (macOS host only; part of `make dist` there) builds `dist/stackstead_<ver>_darwin.pkg` from
`bin/stackstead-darwin-{amd64,arm64}`: a `lipo`-merged universal binary, ad-hoc codesigned, in an unsigned
package. It prints the Gatekeeper note; paste it into the release notes (the template above has it).

**The `.pkg` is not reproducible.** The binary and its ad-hoc signature are, but `pkgbuild` and `productbuild`
stamp creation times into the xar table of contents, so two builds of the same commit differ byte for byte.
`make release` checksums the file as it exists in `dist/`: build the package once, run `make release` (as part
of `make dist`), and publish exactly that file. Do not re-run `make pkg` after `SHA256SUMS` exists, and if you
do, re-run `make release` and `make verify-release` before uploading. (`pkgbuild` may print a few
`write: Permission denied` lines in restricted shells; the package is fine.)

### Testing the pkg

Structural checks run on any macOS host as part of `make installer-test` (`pkgutil --expand-full`, scripts,
ownership, universal binary). Actually installing it needs root: the test calls `sudo installer` itself (run `go test` as
yourself, not under sudo, and expect a password prompt). Do this once per release, on a test volume
first so your own `/usr/local` stays untouched:

```sh
hdiutil create -size 200m -fs APFS -volname sstest -attach /tmp/sstest.dmg
STACKSTEAD_PKG_TEST_VOLUME=/Volumes/sstest go test -count=1 -tags installer -run TestPkgInstallOnTestVolume -timeout 600s ./test/installer/
hdiutil detach /Volumes/sstest
```

Then, for the real thing on a Mac you can afford to change: note `ls -ld /usr/local/bin /usr/local/share`,
run `sudo installer -pkg dist/stackstead_<ver>_darwin.pkg -target /`, open `http://127.0.0.1:8420/`, check
`launchctl print gui/$(id -u)/io.github.rcss-labs.stackstead`, compare `ls -ld /usr/local/bin /usr/local/share`
(owner and mode must not change; only `stackstead` and `share/stackstead` are added), and remove it with
`sudo /usr/local/share/stackstead/install.sh --uninstall --purge --yes`.

## Dry run

To rehearse a release without touching the repository or publishing anything, build into scratch directories
with a tag-like version (no `ALLOW_DEV_RELEASE` needed for `-rc.N`; use it for other untagged versions):

```sh
make dist VERSION=v0.0.1-rc.1 DIST=/tmp/ss-dist BIN=/tmp/ss-bin
make verify-release DIST=/tmp/ss-dist
make package-lint DIST=/tmp/ss-dist
```

To try `install.sh` against it, serve the files with GitHub's layout from a loopback server (the script accepts plain
`http` only for `127.0.0.1`), for example a busybox `httpd` container with `releases/download/v0.0.1-rc.1/` and
`releases/latest/download/` directories holding the files, and run
`sh install.sh --base-url http://127.0.0.1:<port> [--dry-run]` inside a Linux container (booted with systemd to
see the service start) or on your Mac with `--dry-run`. Never publish the output of a dry run.

## Signing policy

Releases are unsigned for now (a deliberate decision). `install.sh` always verifies the tarball against
`SHA256SUMS` and has no way to skip it; `SHA256SUMS` guards against corruption, not against a compromised
release page. Signing is built but off:

- With `SIGNING_KEY=<ssh private key>`, `make release` also writes `dist/SHA256SUMS.sig` (`ssh-keygen -Y sign -n stackstead-release`);
  `make verify-release SIGNERS=<allowed_signers file>` then verifies it. Without the variable a stale `.sig` is deleted.
- `install.sh --signers FILE` (or `STACKSTEAD_INSTALL_SIGNERS_FILE`) verifies it on install; a missing or bad
  signature then exits 6. Without `--signers` a missing `.sig` is never an error.

### Future: signing releases

1. Generate an ed25519 key and keep it out of the repository: `ssh-keygen -t ed25519 -f release-key -C release@stackstead`.
2. Build with `make dist SIGNING_KEY=/path/to/release-key` and upload `SHA256SUMS.sig` with the other files.
3. Publish the public half: add an `allowed_signers` line (`release@stackstead ssh-ed25519 AAAA...`) as
   `deploy/release/allowed_signers`, embed it in `install.sh`, make `--signers` the default, and state the fingerprint in the README and
   the release notes. This is a key and documentation change, not a code change; the verification path and its tests exist.
4. Cosign keyless signing is the alternative once CI builds the releases.

### Future: Apple Developer ID

Set all three of `MACOS_APP_IDENTITY` ("Developer ID Application: ..."), `MACOS_INSTALLER_IDENTITY` ("Developer ID
Installer: ...") and `NOTARY_PROFILE` (a `notarytool store-credentials` keychain profile) and run `make pkg`: the binary is
signed with the hardened runtime, the package is signed, notarized and stapled. A partial set fails the target.
Then drop the Gatekeeper block from the notes.
