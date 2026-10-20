<!-- stackstead-review:2026-10-20:SEC-01 -->

## Problem and impact

Login reads a password hash, verifies it without holding a database transaction, then inserts a session unconditionally. A password change/reset can commit hash replacement and session deletion in between. The late login then creates a valid seven-day session using the old password. Exploitation requires knowledge of that old password and concurrent recovery; this is not a passwordless bypass.

Priority: **P2**.

## Evidence

- [internal/auth/service.go:234](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/internal/auth/service.go#L234)
- [internal/auth/service.go:299](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/internal/auth/service.go#L299)
- [internal/auth/service.go:203](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/internal/auth/service.go#L203)

Reproduced against real auth.Service through a temporary Go test overlay. Both change/reset cases minted a token that subsequently authenticated.

Security report: [security assessment](https://github.com/rcss-labs/stackstead/blob/main/docs/security-review-2026-10-20.md). [Reproduction harness](https://github.com/rcss-labs/stackstead/blob/main/docs/security-review-2026-10-20/reproduce.py); it intentionally exits 1 on the vulnerable baseline.

## Proposed work

Atomically condition session insertion on the hash/auth generation verified. Check affected row count and reject a stale snapshot. Cover change, host-side reset and explicit revocation semantics without holding the single DB connection during Argon2.

## Acceptance criteria

- [ ] Add deterministic red/green regressions for both ChangePassword and ResetPassword interleavings.
- [ ] A session inserted before recovery must be revoked; insertion attempted after recovery must be rejected.
- [ ] New-password login and intentionally retained current-owner session keep working; run auth/API/CLI and affected race tests.

## Review scoring and limits

TypeSafe jev-1.13.0 severity 1.90/4, concentration 0.90, support 0.89. Reviewer: Medium, P2.

No remediation is included in the audit. Security cases use synthetic credentials and local fixtures; do not test destructive operations against production workloads.
