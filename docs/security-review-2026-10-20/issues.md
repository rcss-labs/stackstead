# Unified review issue index — 20 October 2026

[Security assessment](../security-review-2026-10-20.md) · [Earlier codebase review](../codebase-review-2026-10-20.md)

23 backlog items published or reused by stable marker. SEC-07/F10 share one issue; SEC-08 reuses pre-existing #18. No finding is marked remediated by issue creation.

| Finding | GitHub issue |
| --- | --- |
| SEC-01 | [#19 — [P2][SEC-01] Prevent old-password login from issuing a session after password recovery](https://github.com/rcss-labs/stackstead/issues/19) |
| SEC-02 | [#20 — [P1][SEC-02] Verify the expected IP identity in pinned peer TLS connections](https://github.com/rcss-labs/stackstead/issues/20) |
| SEC-03 | [#21 — [P2][SEC-03] Terminate authenticated streams when their session or peer is revoked](https://github.com/rcss-labs/stackstead/issues/21) |
| SEC-04 | [#22 — [P2][SEC-04] Separate reverse-proxy clients in authentication rate limiting](https://github.com/rcss-labs/stackstead/issues/22) |
| SEC-05 | [#23 — [P1][SEC-05] Require a patched Go toolchain and rebuild affected release artifacts](https://github.com/rcss-labs/stackstead/issues/23) |
| SEC-06 | [#24 — [P2][SEC-06] Enforce independent release authenticity for default install and upgrade](https://github.com/rcss-labs/stackstead/issues/24) |
| F1 | [#25 — [P2][F1] Preserve the predecessor health state at the retention boundary](https://github.com/rcss-labs/stackstead/issues/25) |
| F2 | [#26 — [P2][F2] Count rolling restarts correctly when initial start predates the window](https://github.com/rcss-labs/stackstead/issues/26) |
| F3 | [#27 — [P2][F3] Link remote service ports to their owning server](https://github.com/rcss-labs/stackstead/issues/27) |
| F4 | [#28 — [P2][F4] Allow slow fleet polls to publish completed responses](https://github.com/rcss-labs/stackstead/issues/28) |
| F5 | [#29 — [P2][F5] Protect dirty drafts against browser history navigation and reload](https://github.com/rcss-labs/stackstead/issues/29) |
| F6 | [#30 — [P2][F6] Apply the service filter to live CLI metrics output](https://github.com/rcss-labs/stackstead/issues/30) |
| F7 | [#31 — [P2][F7] Escape filesystem paths consistently when opening SQLite databases](https://github.com/rcss-labs/stackstead/issues/31) |
| F8 | [#32 — [P2][F8] Take the maintenance lock before database initialization and migration](https://github.com/rcss-labs/stackstead/issues/32) |
| F9 | [#33 — [P2][F9] Handle script-to-package installation transitions without retaining the old executable](https://github.com/rcss-labs/stackstead/issues/33) |
| F10/SEC-07 | [#34 — [P3][F10] Bound retained operation output by bytes and global lifetime](https://github.com/rcss-labs/stackstead/issues/34) |
| F11 | [#35 — [P3][F11] Reuse peer transports for fleet polling and hello checks](https://github.com/rcss-labs/stackstead/issues/35) |
| F12 | [#36 — [P3][F12] Reduce collapsed log rendering and repeated service-color computation](https://github.com/rcss-labs/stackstead/issues/36) |
| CI-01 | [#37 — [P2][CI-01] Enforce repository verification and security checks on pull requests](https://github.com/rcss-labs/stackstead/issues/37) |
| M1 | [#38 — [P3][M1] Measure sampler duration and sample age before changing metrics concurrency](https://github.com/rcss-labs/stackstead/issues/38) |
| M2 | [#39 — [P3][M2] Measure global metrics query and pruning plans before adding indexes](https://github.com/rcss-labs/stackstead/issues/39) |
| M3 | [#40 — [P3][M3] Profile repeated Compose reads and per-service container scans](https://github.com/rcss-labs/stackstead/issues/40) |
| M4 | [#41 — [P3][M4] Check recorder bookkeeping cleanup for destroyed containers](https://github.com/rcss-labs/stackstead/issues/41) |
| SEC-08 (pre-existing) | [#18 — Refuse relative extra build contexts that leave the stack folder through a symlink](https://github.com/rcss-labs/stackstead/issues/18) |

Local issue bodies are under `issues/`; the manifest and confirmed URLs are stored in `issues-manifest.json` and `github-issues.json`. Re-running with `--dry-run` verifies reuse without posting anything.
