<!-- stackstead-review:2026-10-20:SEC-04 -->

## Problem and impact

The limiter keys only on TCP RemoteAddr, so all clients behind one proxy share five attempts per 15 minutes. Five failures from one unauthenticated client block another client's correct login and also consume the password-change verification bucket. Documented X-Forwarded-For forwarding does not affect this code.

Priority: **P2**.

## Evidence

- [internal/api/handlers_auth.go:97](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/internal/api/handlers_auth.go#L97)
- [internal/api/handlers_password.go:21](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/internal/api/handlers_password.go#L21)
- [internal/cli/serve.go:230](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/internal/cli/serve.go#L230)

Real handler regression with shared loopback RemoteAddr and distinct forwarded addresses returns 429 for the second client's correct password after five wrong attempts from the first.

Security report: [security assessment](https://github.com/rcss-labs/stackstead/blob/main/docs/security-review-2026-10-20.md). [Reproduction harness](https://github.com/rcss-labs/stackstead/blob/main/docs/security-review-2026-10-20/reproduce.py); it intentionally exits 1 on the vulnerable baseline.

## Proposed work

Define explicit trusted-proxy handling, validate a trusted client-address chain, or enforce a documented per-client gateway limit with an appropriate backend policy. Never trust spoofable X-Forwarded-For unconditionally. Preserve Argon2 concurrency bounds and protect recovery.

## Acceptance criteria

- [ ] Red/green two-client shared-proxy regression: client A cannot throttle client B's correct login/password change.
- [ ] Direct untrusted callers cannot spoof limiter identity; cover malformed/multi-hop headers.
- [ ] Cover fixed-window expiry and concurrent attempts; update validated deployment examples.

## Review scoring and limits

TypeSafe jev-1.13.0 severity 1.85/4, concentration 0.88, support 0.88. Reviewer: Medium, P2.

No remediation is included in the audit. Security cases use synthetic credentials and local fixtures; do not test destructive operations against production workloads.
