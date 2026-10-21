<!-- stackstead-review:2026-10-20:SEC-05 -->

## Problem and impact

This checkout uses Go 1.26.4. govulncheck v1.9.0 with the 2026-10-20 database reported 24 advisory IDs, 18 with symbol traces, fixed through Go 1.26.9. The clearest relevant path is GO-2026-6609 (CVE-2026-78667): unauthenticated static serving calls affected file APIs. go.mod only requires 1.26.0; release targets have no patch floor. This does not establish the toolchain of any deployed binary, and many HTTP/2/Windows/CONNECT/template scanner matches are conditional or unused.

Priority: **P1**.

## Evidence

- [go.mod:3](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/go.mod#L3)
- [Makefile:70](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/Makefile#L70)
- [internal/api/spa.go:31](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/internal/api/spa.go#L31)

Observed local go1.26.4; source scan, not CVE payload exploitation. JSON scanner exit 0 is not a clean result. Official advisories: https://pkg.go.dev/vuln/GO-2026-6609 and https://pkg.go.dev/vuln/GO-2026-4970 .

Security report: [security assessment](https://github.com/rcss-labs/stackstead/blob/main/docs/security-review-2026-10-20.md). [Reproduction harness](https://github.com/rcss-labs/stackstead/blob/main/docs/security-review-2026-10-20/reproduce.py); it intentionally exits 1 on the vulnerable baseline.

## Proposed work

Choose and enforce a supported patched compiler (at least 1.26.9 on the 1.26 branch), record binary compiler metadata, scan source/artifacts, rebuild and redeploy actually affected releases. Triage remaining advisories by real call flow. Updating go.mod alone cannot patch an installed executable.

## Acceptance criteria

- [ ] An old compiler is refused by the release gate and actual compiler metadata is recorded.
- [ ] Rerun current govulncheck with documented dispositions; verify release-binary metadata before declaring deployed exposure/fix.
- [ ] Patched build passes make check and relevant E2E/installer checks across supported targets.

## Review scoring and limits

TypeSafe jev-1.13.0 severity 2.46/4, concentration 0.54, support 0.87. Lower concentration reflects deployment/precondition uncertainty. Reviewer: High local exposure/release priority, P1.

No remediation is included in the audit. Security cases use synthetic credentials and local fixtures; do not test destructive operations against production workloads.
