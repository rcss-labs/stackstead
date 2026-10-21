<!-- stackstead-review:2026-10-20:SEC-03 -->

## Problem and impact

Auth middleware checks once, then SSE/forward streams continue until request cancellation, shutdown, write failure or producer completion. Logout, password recovery, expiry and unpair do not invalidate existing stream contexts. An already-open stolen-session reader can keep receiving new data; new requests are rejected normally.

Priority: **P2**.

## Evidence

- [internal/api/session.go:48](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/internal/api/session.go#L48)
- [internal/api/sse.go:75](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/internal/api/sse.go#L75)
- [internal/api/observe.go:323](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/internal/api/observe.go#L323)
- [internal/api/forward.go:150](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/internal/api/forward.go#L150)

Real HTTP /api/events delivered new changes after logout, second-session password change and fake-clock expiry. A fresh /api/me with the same cookie returned 401 in all three controls. Other stream types and unpair are source-supported and still need dedicated tests.

Security report: [security assessment](https://github.com/rcss-labs/stackstead/blob/main/docs/security-review-2026-10-20.md). [Reproduction harness](https://github.com/rcss-labs/stackstead/blob/main/docs/security-review-2026-10-20/reproduce.py); it intentionally exits 1 on the vulnerable baseline.

## Proposed work

Associate stream contexts with session/peer revocation and expiry, using bounded revalidation or push invalidation. Define the bound, cover gateway and remote streams, avoid per-line DB queries, and prevent busy streams from starving checks.

## Acceptance criteria

- [ ] Red/green event/log tests for logout, reset/change, explicit revocation, TTL expiry and peer removal.
- [ ] After the defined bound, publish a marker and assert no delivery plus producer cancellation/unsubscribe.
- [ ] Cover forwarded streams, fresh-request rejection, reconnect and shutdown behavior.

## Review scoring and limits

TypeSafe jev-1.13.0 severity 1.95/4, concentration 0.96, support 0.89. Reviewer: Medium, P2.

No remediation is included in the audit. Security cases use synthetic credentials and local fixtures; do not test destructive operations against production workloads.
