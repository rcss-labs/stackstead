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
