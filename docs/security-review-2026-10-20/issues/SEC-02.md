<!-- stackstead-review:2026-10-20:SEC-02 -->

## Problem and impact

With a nonempty pin, custom TLS verification uses ConnectionState.ServerName as x509 DNSName. Go omits IP literals from SNI, leaving this empty, so the trusted-chain branch verifies no endpoint identity. An unrelated trusted certificate is accepted without matching the IP or the configured pin. Network interception plus a certificate trusted by the selected roots, a pin and an IP peer URL are required. Peer credentials are root-equivalent.

Priority: **P1**.

## Evidence

- [internal/peers/transport.go:71](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/internal/peers/transport.go#L71)
- [internal/peers/transport.go:92](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/internal/peers/transport.go#L92)
- [internal/api/forward.go:183](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/internal/api/forward.go#L183)

Actual NewHTTPClient accepted an unrelated locally trusted synthetic DNS-only certificate for a loopback IP URL and wrong pin on TLS 1.2 and 1.3. The ordinary verifier with the same roots rejected it. No real credential sent.

Security report: [security assessment](https://github.com/rcss-labs/stackstead/blob/main/docs/security-review-2026-10-20.md). [Reproduction harness](https://github.com/rcss-labs/stackstead/blob/main/docs/security-review-2026-10-20/reproduce.py); it intentionally exits 1 on the vulnerable baseline.

## Proposed work

Capture the expected URL hostname/IP in the verifier instead of relying on SNI. Preserve documented trusted-chain OR exact-pin fallback semantics; do not silently impose mandatory pins on all valid public chains.

## Acceptance criteria

- [ ] Red/green IPv4 and IPv6 tests: unrelated trusted certificate plus wrong pin must fail before any authenticated HTTP request.
- [ ] Exercise TLS 1.2 and 1.3 and positive controls for correct IP SAN, correct hostname certificate and exact-pin fallback.
- [ ] Run peer/forwarding suites and connected-server E2E.

## Review scoring and limits

TypeSafe jev-1.13.0 severity 3.00/4, concentration 1.00, support 0.87. Independent final source review confirmed the mechanism. Reviewer: High, P1.

No remediation is included in the audit. Security cases use synthetic credentials and local fixtures; do not test destructive operations against production workloads.
