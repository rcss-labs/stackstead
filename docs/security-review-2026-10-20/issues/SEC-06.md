<!-- stackstead-review:2026-10-20:SEC-06 -->

## Problem and impact

Default install/upgrade trusts an artifact and SHA256SUMS from the same release publisher. A publisher compromise could replace both before privileged execution. Build signing and install.sh --signers already exist as opt-in features, but no signer is shipped or required by default; CLI asset/installer download verifies SHA256 only. This is a documented hardening gap, not observed publisher compromise or a broken checksum.

Priority: **P2**.

## Evidence

- [deploy/release/RELEASING.md:3](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/deploy/release/RELEASING.md#L3)
- [internal/update/source.go:230](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/internal/update/source.go#L230)
- [install.sh:608](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/install.sh#L608)
- [Makefile:88](https://github.com/rcss-labs/stackstead/blob/18a23af72d3c74e54c814a9b59679387f80d4084/Makefile#L88)

Static inspection of release policy and verification flow; no malicious release published or installed.

Security report: [security assessment](https://github.com/rcss-labs/stackstead/blob/main/docs/security-review-2026-10-20.md). [Reproduction harness](https://github.com/rcss-labs/stackstead/blob/main/docs/security-review-2026-10-20/reproduce.py); it intentionally exits 1 on the vulnerable baseline.

## Proposed work

Define an independently distributed signer/provenance trust root, enforce authentication before executing the installer and artifacts, and specify key rotation/revocation and rollback policy. Reuse existing signing support where appropriate.

## Acceptance criteria

- [ ] Tampered artifact/manifest, missing or invalid required signature and wrong signer fail before execution.
- [ ] Synthetic valid signer and deliberate trust rotation succeed; no real signing secrets in tests.
- [ ] Update release/operator/upgrade documentation and preserve explicit local testing paths.

## Review scoring and limits

TypeSafe jev-1.13.0 severity 1.84/4, concentration 0.84, support 0.91. Reviewer: Medium hardening, P2.

No remediation is included in the audit. Security cases use synthetic credentials and local fixtures; do not test destructive operations against production workloads.
