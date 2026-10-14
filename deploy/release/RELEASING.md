# Releasing Stackstead

Skeleton; the end-to-end runbook is completed in Plan 7 Task 9.

## Build the artifacts

```sh
make release VERSION=vX.Y.Z      # tarballs + SHA256SUMS in dist/ (unsigned)
make verify-release              # checks dist/SHA256SUMS
```

`make release` must run last (after `packages` and `pkg`), because `SHA256SUMS`
covers every file in `dist/`. Re-running it regenerates the file idempotently.
Untagged builds (`-dev.` versions) are refused unless `ALLOW_DEV_RELEASE=1`.

## Signing (optional, off by default)

Releases are unsigned for now. With `SIGNING_KEY=<ssh private key>`, `make release`
also writes `dist/SHA256SUMS.sig` (`ssh-keygen -Y sign -n stackstead-release`);
`make verify-release SIGNERS=<allowed_signers file>` then verifies it.

## macOS .pkg (unsigned)

`make pkg` (macOS host only; part of `make dist` there, skipped elsewhere) builds
`dist/stackstead_<ver>_darwin.pkg` from `bin/stackstead-darwin-{amd64,arm64}`: a universal,
ad-hoc codesigned binary in an unsigned package. It prints the Gatekeeper note: paste it into
the release notes. The package contents are reproducible, the `.pkg` bytes are not (xar
timestamps), so build it once and let `make release` checksum that file.

Test it without touching your own Mac's `/usr/local`:

```sh
hdiutil create -size 200m -fs APFS -volname sstest -attach /tmp/sstest.dmg
STACKSTEAD_PKG_TEST_VOLUME=/Volumes/sstest go test -count=1 -tags installer -run Pkg -timeout 600s ./test/installer/
hdiutil detach /Volumes/sstest
```

Optional/future signing: set all three of `MACOS_APP_IDENTITY`, `MACOS_INSTALLER_IDENTITY`
and `NOTARY_PROFILE` (a partial set fails the target); the pkg is then signed, notarized and stapled.
