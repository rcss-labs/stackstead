# Security assessment and penetration-test review — 20 October 2026

## Executive assessment

**Security posture: 6.2/10.** Stackstead has substantial security engineering for a small, single-admin Docker manager, but its peer TLS verification and authentication recovery need correction. Do not treat it as ready for unrestricted internet exposure. Keep access restricted to trusted networks or an authenticated TLS gateway while addressing the priority findings.

The most consequential application defect is **SEC-02**: when a peer uses an IP address and a certificate pin is configured, an unrelated certificate trusted by the selected roots is accepted without matching the IP or the pin. A suitably positioned network attacker could receive a root-equivalent peer credential. Two password-recovery weaknesses and a proxy-wide authentication lockout were also reproduced. The local compiler has known security advisories affecting used standard-library symbols; release artifacts need a patched build and an enforceable compiler policy.

This assessment contains **five new actionable security defects/exposures (SEC-01–SEC-05), one documented supply-chain hardening gap (SEC-06), and two overlapping defense-in-depth findings (SEC-07–SEC-08)**. SEC-07 shares the earlier engineering review's F10; SEC-08 is already tracked as [issue #18](https://github.com/rcss-labs/stackstead/issues/18). These are not eight independent newly discovered vulnerabilities. No broadly exploitable unauthenticated host takeover was established.

The earlier [codebase review](codebase-review-2026-10-20.md) scored engineering quality **7.2/10** at an older baseline. That score and its security subscore were not a penetration test; the scores measure different things. No remediation is included in this documentation change.

## Baseline, authorization and boundaries

- Repository: `rcss-labs/stackstead`; reviewed branch: `main`.
- Audited application commit: `18a23af72d3c74e54c814a9b59679387f80d4084`.
- Earlier review baseline: `885dbbd1a26258a978569e7fba8d867baef44211`. Changes between that baseline and this audit were inspected; the reported underlying backend paths remain unchanged. Later UI changes do not resolve the listed F findings.
- Environment: macOS arm64, Go **1.26.4**, Node **24.14.0**. Local execution is evidence for this checkout, not proof of the toolchain used for released/deployed binaries.
- User authorized this repository/app review and creation of GitHub issues, then granted session-wide execution approval. Test activity used temporary data and loopback listeners. No real Docker workload, production host, cloud resource or production credential was targeted.
- This is a repository-wide security review with focused local penetration-style validation. It is not an externally deployed infrastructure assessment or a guarantee that every line/path has been tested.
- The principal review was independently challenged by a `gpt-6-astra`, high-effort final reviewer. Three initial delegated inspections stopped on usage limits; they are not represented as completed independent audits.
- Untracked `.idea/` content was present before the audit and was left untouched. No direct reads/writes under `.git/` were performed.
- Application code and dependencies were not changed. Report, reproducibility tooling and backlog artifacts live under `docs/`.

### Threat model

The intended user is a trusted machine administrator. Stackstead login, paired-server credentials and Docker socket access are deliberately root-equivalent. Arbitrary Compose commands, host mounts and Dockerfile build commands available to that admin are **intended powers**, not authentication bypasses.

Assets requiring protection are the Docker host, its persistent volumes, peer credentials stored in SQLite, browser/CLI sessions, backups and the release/install trust chain. Relevant attackers include unauthenticated network clients, a holder of an old password or stolen cookie during recovery, an interceptor of peer traffic, an attacker controlling release publishing, and a local actor able to modify allowed build-context paths.

Trust crossings examined: browser → HTTP API → auth/database → Docker/Compose/filesystem; gateway → paired server; CLI → remote API and local database; release source → root installer. A paired server is explicitly trusted as root on both hosts. The peer settings denylist is an accident-prevention control, not a tenant boundary.

## Scoring method

The overall score is a reviewer judgment using this explicit weighted rubric. It is not CVSS, an external certification, a measured exploitation probability or a TypeSafe-generated overall score. Higher is better. Scores below 5 imply major uncontrolled exposure; 5–6 indicate material security gaps; 7–8 require targeted fixes; 9–10 require strong demonstrated controls and operational assurance.

| Dimension | Weight | Score / 10 | Evidence |
| --- | ---: | ---: | --- |
| Authentication and recovery | 25% | 6.5 | Strong Argon2id/session design; recovery race and active-stream revocation failures |
| Peer transport and trust | 20% | 5.0 | Redirect/credential protections; reproduced identity verification failure for pinned IP peers |
| Filesystem and destructive-operation controls | 15% | 7.5 | Atomic guarded writes and fresh storage checks; known conditional context symlink gap |
| Availability and resource limits | 15% | 6.0 | Deadlines/bounded work; proxy lockout, retained-output lifetime risk and vulnerable compiler HTTP code |
| Release and delivery assurance | 15% | 5.0 | Pins/checksums; old compiler, unsigned defaults, no CI or protected-main gate observed |
| Frontend and data handling | 10% | 8.0 | React text rendering, CSP, HTTP-only cookies, same-origin requests; no demonstrated XSS |
| **Weighted total** | **100%** | **6.2** | 1.625 + 1.000 + 1.125 + 0.900 + 0.750 + 0.800 |

Finding severity and engineering priority are separate. P1 means address before the next relevant release/exposure; P2 means planned corrective work; P3 means conditional/measurement-led improvement. No CVSS vectors are assigned because several impacts depend on deployment and attacker position that were not observed.

## Coverage and execution evidence

Inventory at the audited commit: **874 tracked files**, including 136 production Go files (31,012 lines), 158 Go test files (51,860 lines), 224 non-test TypeScript/TSX files (23,777 lines), and 131 TypeScript/TSX test files (27,499 lines). Counts describe inventory, not inspected-line or test-coverage percentages.

| Area | Inspection and validation | Outcome / limits |
| --- | --- | --- |
| Auth, setup and HTTP API | Entry points, hashing/session lifecycle, limiter, JSON limits, setup guards, SSE, password endpoints; isolated tests | SEC-01, SEC-03, SEC-04 reproduced |
| Peering and gateway forwarding | Pairing tokens, credential storage, certificate verification, DNS/private-address checks, redirects, path/header handling | SEC-02 reproduced; active peer-stream revocation inferred from shared paths |
| Frontend | Request/session/SSE clients, URL/HTML sinks, storage use, CSP/static serving, current UI changes and existing tests | No demonstrated XSS; React escapes text and requests reject redirects |
| Compose/filesystem/operations | Runner argv, cancellation, build contexts, Dockerfile confinement, atomic writes, operation retention | Existing safeguards preserved; SEC-07 and SEC-08 tracked conditionally |
| Storage cleanup/Docker client | Plan/recheck/execute flow, identity and use checks, Unix socket transport and deadlines | No destructive live operations performed; no new storage-authorization bypass established |
| Config/data/CLI | SQL binding, state/contexts permissions, maintenance locks, backup/update execution paths, host/history/metrics code and prior findings | Prior engineering defects carried forward; real filesystem/daemon combinations not exhaustively tested |
| Installer/release/deployment | Script/package trust, optional signing, fixed argv/PATH, systemd hardening and release runbook | SEC-06; CI-01; no actual installation/upgrade performed |
| Dependencies | npm audit and Go vulnerability database scan with per-advisory triage | npm clean; Go findings described below |
| Repository controls | Current tracked-file patterns, workflow inventory, GitHub branch metadata and existing issues | No high-specificity credential-pattern matches; main unprotected; no tracked workflows |

### Commands actually executed

| Check | Result |
| --- | --- |
| `make -n check` | Inspected dry run before execution |
| `make check` | Exit 0: TypeScript, production UI build/budgets, Vitest, go vet, gofmt gate, Go race suite; some Go packages cached |
| `npm audit --prefix web --json --ignore-scripts` | Exit 0; **zero reported known vulnerabilities**, 143 total dependency entries in audit metadata |
| `go run golang.org/x/vuln/cmd/govulncheck@latest -json ./...` | Scanner v1.9.0, source/symbol mode; DB last modified 2026-10-20T00:20:56Z; **24 finding IDs, 18 with symbol traces** |
| `python3 docs/security-review-2026-10-20/reproduce.py` | **Exit 1 intentionally**: four security assertions fail across eight subcases, reproducing SEC-01–SEC-04 |
| `go test -tags e2e -count=1 -timeout 180s -run '^(TestE2ECLI\|TestE2EConnectedServers)$' ./cmd/stackstead` | Exit 0; 16.076 seconds; real temporary server processes and synthetic credentials; no Docker daemon used |
| Current-file credential-pattern check | 862 tracked text files ≤2 MB; private-key/AWS/GitHub/Slack high-specificity patterns; zero matches; output contained locations only |
| `gh api repos/rcss-labs/stackstead/branches/main` | `protected: false` at review time |
| `git ls-files '.github/*'` | No tracked workflows |

The Go scanner was run twice: the first JSON stream was too large to review directly; the second was parsed in memory into advisory/reachability summaries. Its JSON-mode **exit 0 does not mean no vulnerabilities**. Tool downloads/caches are not repository dependency changes. For a repeatable scanner version, use `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -json ./...`.

The PoC harness replaces test files only in a temporary Go overlay. It retains all original test source and appends security assertions; no production file is modified. Temporary files/databases/listeners are cleaned up. Failures are the evidence, not claims of successful remediation. Existing tests passing does not contradict uncovered security defects.

**Not performed:** production deployment or remote scanning; real Docker integration/destructive cleanup; complete tagged E2E suite; privileged installer/package/systemd tests; browser-driven manual testing; production proxy/TLS configuration inspection; full Git-history/entropy-based secret scanning; fuzzing; CVE denial-of-service payload execution; load/heap profiling; release-binary download/inspection; cloud/IAM assessment. No claim of finding-free coverage is made for these areas.

## Findings register

| ID | Finding | Severity / priority | Evidence | Disposition |
| --- | --- | --- | --- | --- |
| SEC-01 | Login can issue a session after old-password recovery | Medium / P2 | Reproduced, change and CLI reset paths | New corrective issue |
| SEC-02 | Pinned IP peers accept an unrelated trusted certificate | High / P1 | Reproduced with TLS 1.2 and 1.3 | New corrective issue |
| SEC-03 | Open streams survive logout, password rotation and expiry | Medium / P2 | Events reproduced; other stream paths inspected | New corrective issue |
| SEC-04 | Proxy clients share one login/password-change lockout bucket | Medium / P2 | Login cross-client lockout reproduced | New corrective issue |
| SEC-05 | Local Go 1.26.4 is vulnerable; releases lack a patch-floor gate | High exposure / P1 | Source scanner + relevant call sites, no deployed artifact proof | New compiler/release-policy issue |
| SEC-06 | Default release verification lacks independent publisher authenticity | Medium hardening / P2 | Documented current policy and verifier source | New hardening issue; acknowledged existing gap |
| SEC-07 | Retained operation output lacks global byte/lifetime bounds | Low / P3 | Static upper-bound analysis; no measured OOM | Merge with code review F10 |
| SEC-08 | Relative extra build context remains vulnerable to later symlink retargeting | Low / P3 | Source + pre-existing issue | Reuse GitHub #18 |

### SEC-01 — In-flight old-password login survives password recovery

**Evidence:** [auth/service.go](../internal/auth/service.go) lines 234–260 (read 237, verify 247, unconditional insert 255–257); password change lines 299–347, especially compare-and-swap/update 333 and deletion 342; reset lines 203–215. Existing `TestChangePasswordLosesRaceAgainstConcurrentChange` protects competing password changes, not session issuance.

**Root cause:** Login verifies a snapshot of the old hash outside a database transaction, then inserts a session without checking that the verified hash is still current. Password recovery correctly updates the hash and deletes sessions transactionally, but cannot delete a session that has not yet been inserted.

**Attack sequence:** An attacker who knows the old password starts login. Login reads the old hash. The legitimate administrator changes or resets the password and revokes sessions. Login finishes verification against its snapshot and inserts a new session. The new session authenticates for up to seven days. This is a recovery race, not a way to log in without knowing a password.

**Local reproduction:** The harness uses the existing `deriveKey` seam to commit real `ChangePassword` or `ResetPassword` after login read the hash but before its verification returns. Both security assertions fail with “old-password login minted a valid session after recovery committed.” It checks the returned token with the real authentication service; no credential value is printed.

**Impact/confidence:** High confidence in the defect; Medium severity because exploitation requires the old password and concurrent recovery. Successful exploitation retains the application's root-equivalent privileges. A stolen cookie alone does not trigger this race.

**Remediation:** Make session issuance conditional on the hash/version verified, atomically with insertion. An `INSERT … SELECT` constrained by user ID and the verified hash, followed by a checked row count, or an authentication generation checked transactionally, can close the race. Include reset and explicit session revocation semantics. Do not hold the single database connection throughout Argon2 computation.

**Acceptance:** Deterministic regressions for change and reset fail before the fix and pass after it. Test both orderings: insertion before revocation must be removed, insertion after revocation must be refused. Correct new-password login and the deliberately retained owner session must continue working. Run auth/API/CLI suites and the race-enabled affected suite.

### SEC-02 — Pinned IP peer connection omits endpoint identity verification

**Evidence:** [peers/transport.go](../internal/peers/transport.go) lines 71–108; [api/forward.go](../internal/api/forward.go):183 attaches the peer credential. With a pin, `tlsConfig` disables normal verification and accepts either `verifyChain(cs.PeerCertificates, cs.ServerName, roots)` or the fingerprint.

**Root cause:** Go's client-side `ConnectionState.ServerName` reflects the SNI name. An IP literal is omitted from SNI, leaving it empty. Passing that empty string as `x509.VerifyOptions.DNSName` verifies chain trust but performs no hostname/IP verification. A trusted, unrelated certificate therefore bypasses both the intended endpoint check and the configured pin. This was confirmed against the installed Go TLS/X.509 implementation as well as application requests.

**Prerequisites:** A peer URL using an IP literal; nonempty configured pin; ability to intercept the connection or route it to an attacker endpoint; a certificate chaining to the chosen public/custom roots. A self-signed untrusted certificate with the wrong pin still fails. Ordinary unpinned verification and hostname-based URLs are not demonstrated vulnerable by this finding.

**Local reproduction:** For TLS 1.2 and TLS 1.3, a local server presents a synthetic trusted certificate for `unrelated.example.invalid`, with no loopback IP SAN. The normal client with the same CA rejects the IP mismatch. Adding a deliberately nonmatching pin makes the actual application client accept it. The harness sends no real credential.

**Impact/confidence:** High severity and high confidence. An attacker in the stated position can receive a peer credential or pairing material; credentials authorize root-equivalent management on the peer. No real interception or host takeover was executed.

**Remediation:** Supply the expected URL hostname/IP to the verification closure explicitly, rather than deriving expected identity from SNI. Verify it in the normal-chain branch before accepting that branch. Preserve the documented policy that a correct pin can intentionally accept an otherwise untrusted certificate; changing to mandatory pin matching for all publicly trusted certificates would be a separate product-policy change.

**Acceptance:** Reject an unrelated trusted certificate and wrong pin for IPv4 and IPv6 URLs on TLS 1.2/1.3, with no authenticated HTTP request reaching the test server. Include positive controls for a valid IP SAN, a valid hostname certificate, and the intended exact-pin fallback. Run peer/forwarding and connected-server E2E tests. Prefer a supported patched Go compiler as required by SEC-05.

### SEC-03 — Session revocation and expiry do not terminate active streams

**Evidence:** [api/session.go](../internal/api/session.go) lines 48–75 authenticates only on entry; [api/sse.go](../internal/api/sse.go) lines 75–100 checks cancellation, shutdown, writes and producer events; [api/observe.go](../internal/api/observe.go) uses that loop for logs/events/metrics. Forward streams terminate on request/shutdown rather than revocation ([forward.go](../internal/api/forward.go):150–160). Unpair removes credentials but does not associate existing requests with a revocation signal.

**Root cause:** Authorization is a one-time admission check for indefinitely long reads. Deleting the database session, expiring it, or removing a peer prevents new requests but does not invalidate the context already given to a stream.

**Local reproduction:** Open `/api/events`; separately log out that session, change its password from another session, or advance the fake clock past session TTL. Confirm a new `/api/me` request using the original cookie returns 401. Publish a distinct event. In all three cases the old stream still delivers it.

**Impact/confidence:** Medium confidentiality/recovery defect. An attacker with an already-open stolen-session stream retains subsequent event/log/metric visibility. Only event delivery is dynamically demonstrated; follow logs, metrics, operation streams, peer removal and gateway forwarding are supported by code inspection and need dedicated regressions. This does not permit arbitrary new API calls after revocation.

**Remediation:** Tie authenticated stream contexts to session/peer revocation and expiry. Use bounded, documented revalidation or push invalidation; ensure busy streams cannot starve periodic checks. Handle direct and forwarded streams on both the gateway and peer. Avoid a database query per output line.

**Acceptance:** Events and log streams close within a defined revocation bound after logout, reset/change, explicit revocation, expiry and unpair. Publish a new marker after that bound and assert it is not delivered. Verify producer unsubscribe/cancellation and fresh-request rejection. Test peer streams and forwarding, healthy reconnect behavior and shutdown.

### SEC-04 — One unauthenticated proxy client can lock out other clients

**Evidence:** [handlers_auth.go](../internal/api/handlers_auth.go) lines 97–115 derives limiter identity only from `RemoteAddr`; [handlers_password.go](../internal/api/handlers_password.go):21–24 reuses it. Production config is five attempts per 15 minutes ([cli/serve.go](../internal/cli/serve.go):230). Security documentation recommends passing `X-Forwarded-For`, but the limiter does not consume it.

**Local reproduction:** Two different forwarded client addresses share the proxy's loopback TCP address. Five wrong passwords from client A cause client B's correct password to receive 429. The test exercises real API handlers and the real limiter with synthetic users/data.

**Impact/confidence:** Medium availability/recovery defect, high confidence. Any client able to reach the same proxy can block other proxied users' login and current-password verification for the remainder of the 15-minute window. The same shared limiter means password recovery through the UI can be interrupted. Failed requests after exhaustion do not extend the fixed window, although an attacker can exhaust the next one.

**Remediation:** Define an explicit trusted-proxy policy and derive client identity only from validated headers supplied by those proxies, or enforce a documented per-client limit at the gateway and revise backend limiting accordingly. Never blindly trust arbitrary `X-Forwarded-For`; doing so permits rate-limit evasion. Preserve useful global Argon2 concurrency protection.

**Acceptance:** One proxied client cannot consume another's login/password-change bucket. Direct clients cannot spoof forwarded identity; malformed and multi-hop headers have deterministic handling. Test fixed-window expiry and concurrent requests. Cover deployment examples/config validation.

### SEC-05 — Known vulnerable compiler in this checkout; no minimum patched release gate

**Evidence:** `go version` reports `go1.26.4 darwin/arm64`; [go.mod](../go.mod):3 requires only 1.26.0; Makefile release paths invoke the selected local Go compiler without enforcing a security patch floor. Source scanning identified 24 matching advisory IDs, 18 with symbol traces. Some call-graph traces are conservative and include methods reached through interfaces.

The clearest relevant HTTP path is **GO-2026-6609 / CVE-2026-78667**: unauthenticated static files are served by `FileServerFS`/`ServeFileFS` ([spa.go](../internal/api/spa.go):31,48,63), and the advisory describes excessive CPU parsing many small ranges. It is fixed in Go 1.26.9. No denial-of-service payload was run. [Official advisory](https://pkg.go.dev/vuln/GO-2026-6609).

The **os.Root** advisory GO-2026-4970 affects an API used by Dockerfile writes and is fixed in 1.26.5, but this application's path normalization/final-symlink checks may exclude the specific trailing-slash trigger. It is a reason to patch, not an independently reproduced Stackstead escape. [Official advisory](https://pkg.go.dev/vuln/GO-2026-4970).

**Impact/confidence:** High confidence in local vulnerable-toolchain matches and missing patch policy; deployment exploitability is conditional. High remediation priority is based on network-facing used library code and the ease/value of compiler patching. There is no evidence that a particular published binary was built with this compiler. Do not call all 24 IDs exploitable Stackstead vulnerabilities.

**Remediation:** Build and release with at least Go **1.26.9** on the 1.26 branch, or a supported newer branch with all corresponding fixes. Enforce the chosen patch policy in development/release automation and check actual compiler metadata in release binaries. Rerun govulncheck with a current database, triage remaining findings explicitly and rebuild/redeploy affected artifacts. Updating `go.mod` alone does not replace an installed vulnerable binary.

**Acceptance:** The release build refuses an older compiler, records the actual compiler version, and passes source plus available binary scanning after triage. A patched rebuild passes `make check` and relevant E2E/installer verification. Preserve supported OS/architecture coverage. Match scanner evidence against deployed artifact metadata before claiming deployed exposure or remediation.

### SEC-06 — Default checksum verification does not authenticate release publisher

**Evidence:** [release runbook](../deploy/release/RELEASING.md) explicitly publishes unsigned artifacts; [update/source.go](../internal/update/source.go) verifies a SHA-256 from the same release as the downloaded asset. Makefile supports signing, and [install.sh](../install.sh) supports **opt-in** `--signers` verification; these existing features should be reused, not reported missing. No signer ships in the default path. CLI upgrade verifies downloaded installers/assets by checksum before execution.

**Attack path:** If the release publisher/account or release build environment is compromised, an attacker can replace an artifact and its colocated checksum. Default verification accepts the pair; installing/upgrading can execute attacker code with installer privileges.

**Classification:** Medium **documented hardening gap**, not evidence of publisher compromise, TLS interception, broken SHA-256 or an undisclosed new bypass. HTTPS, redirect restrictions, checksum/size checks and fixed installer argv still provide real protection.

**Remediation:** Establish an independently distributed trust root and an enforced signed-manifest/provenance policy across install, upgrade and package publication. Authenticate the installer itself before execution. Define key rotation/revocation and downgrade/rollback policy; fail closed for missing/invalid required signatures.

**Acceptance:** Tampered artifact, tampered manifest, missing/invalid signature and wrong signer are rejected before execution; valid pinned trust and intentional key rotation succeed. Tests use synthetic signing material only. Update operator/release documentation and preserve explicit local test-source workflows.

### SEC-07 — Retained operation output can accumulate without a global lifetime budget

**Evidence:** [stacks/ops.go](../internal/stacks/ops.go):28–31,145–153,213–224,386–399. Limits are 20 operations per stack, 20,000 lines per operation and about 8 KiB per line. Removing a stack ([service.go](../internal/stacks/service.go):597–606) does not evict its operation records.

**Impact:** Theoretical payload retention is about **156 MiB per operation / 3.05 GiB per stack**, before object overhead and truncation suffixes. Unique-stack churn has no global expiry/budget. This can amplify a noisy build or operator mistake into process memory pressure. These figures are upper bounds, not observed heap usage or measured OOM. A logged-in attacker already has root-equivalent powers; this is availability hardening, not a new privilege boundary.

**Disposition/remediation:** One issue shared with earlier F10. Add per-operation and global byte budgets plus TTL/LRU eviction and removed-stack lifecycle handling. Keep active streaming/subscribers correct. Measure synthetic bounded-output and churn behavior, then heap retention; avoid a dangerous maximum-size stress test on the user's host.

**Acceptance:** Byte/lifetime limits are observable and tested; completed/removed stacks do not retain unbounded history, active subscribers remain correct, and replay dropped-line accounting stays accurate. Profile before/after using a controlled budget.

### SEC-08 — Relative extra build context can follow a later-repointed symlink

**Evidence:** [service_create.go](../internal/stacks/service_create.go):382–410 resolves external contexts but rewrites only absolute contexts. Lines 531–535 reject changed symlink resolution only for absolute extra contexts. Relative extra contexts and `additional_contexts` can remain lexical. Existing [issue #18](https://github.com/rcss-labs/stackstead/issues/18) already documents this condition.

**Trigger:** An initially allowed relative context traverses a host symlink outside the new stack folder. After creation-time validation, an actor able to modify that allowed host path repoints the link. A subsequent build resolves a different target.

**Impact/classification:** Low, conditional confinement/accident-prevention defect. Requires host filesystem write access and a later operator build. No unauthenticated exploit or standalone tenant-isolation bypass is established, given the root-equivalent admin model.

**Disposition:** Reuse #18 rather than create a duplicate. Extend the same real-path/no-link policy to relative external contexts and additional contexts, or persist resolved paths where semantics allow. Test initial validation, retargeting and ordinary contexts; a broader runtime revalidation policy needs a deliberate design.

## Chained attack paths and defensive priorities

1. **Network interception → SEC-02 → peer credential → Docker host control.** The TLS identity check is the choke point. Prioritize it before releasing or using pinned-IP peering on an untrusted network. MITRE ATT&CK mapping: adversary-in-the-middle T1557; valid accounts T1078; container administration consequences T1610. These are threat-model mappings, not techniques executed against a real host.
2. **Known old password → concurrent recovery → SEC-01 → renewed session.** Atomic issuance/recovery is the choke point. Rate limiting reduces attempts but does not remove the interleaving.
3. **Stolen cookie + already-open stream → SEC-03 → continued observation after recovery.** Session deletion must invalidate active contexts. Newly issued credentials appearing in application logs could increase consequences, but such disclosure was not demonstrated.
4. **Reachable reverse proxy → five failed logins → SEC-04 → admin login/password-change denial.** Client identity/limiter design is the choke point; protect recovery separately.
5. **Compromised release publisher → SEC-06 → default install/upgrade.** Independent release authenticity is the choke point. No upstream compromise was observed.

Detection opportunities: alert on sustained authentication throttling, password-reset/session-revocation events, peer pairing/removal and TLS failures. Centralize relevant action/auth audit data without recording passwords, cookies or peer credentials. Existing action logging is useful but was not validated as a complete tamper-resistant security audit trail.

## Controls worth retaining

- Loopback-only default listener; setup requires direct loopback with host/forwarding-header checks or a one-time setup token.
- Argon2id password hashes, random opaque sessions hashed at rest, HttpOnly/SameSite=Strict cookies and two concurrent hash slots.
- JSON content-type/body limits and absence of permissive CORS. No demonstrated cross-origin CSRF or HTML injection was found.
- Same-origin script CSP, frame denial, nosniff, React text rendering and redirect-rejecting browser API/SSE clients.
- Pairing tokens expire, are single-use and hashed at rest; credentials are not returned by ordinary management APIs; peer request/response headers and forwarded routes have deliberate filtering.
- DNS rebinding protection in the plaintext peer **dialer**, not merely initial URL validation; credential-bearing clients reject redirects.
- Fixed subprocess argv, process-group cancellation, guarded Dockerfile writes, stale hashes/backups, exclusive temporary publication and fsync.
- Storage plans recheck use, ownership/identity and declarations before exact non-force removal.
- Service hardening and private state/CLI contexts; HTTPS/checksum/size/redirect restrictions on updates.

These are source/test-backed controls, not evidence of perfect deployment. Explicitly accepted risks remain root-equivalent peers, opt-in private-network plaintext, sensitive database backups and single-admin operation. MFA/RBAC/multitenancy would change the product model and are not required as fixes for this audit.

## Go advisory triage appendix

The table records scanner matching, not exploitation. “Symbol” means at least one reported function trace. Every row links to the primary advisory. Fix versions refer to the Go 1.26 branch.

| Advisory | Match | Fix | Relevance qualification |
| --- | --- | --- | --- |
| [GO-2026-4970](https://pkg.go.dev/vuln/GO-2026-4970) | Symbol | 1.26.5 | os.Root API used; trailing-slash escape not reproduced through normalized application paths |
| [GO-2026-5026](https://pkg.go.dev/vuln/GO-2026-5026) | Symbol | 1.26.6 | URL/IDNA networking path; exact malicious-label impact not reproduced |
| [GO-2026-5856](https://pkg.go.dev/vuln/GO-2026-5856) | Symbol | 1.26.5 | ECH-related TLS path; app ECH configuration not established |
| [GO-2026-5942](https://pkg.go.dev/vuln/GO-2026-5942) | Package | 1.26.6 | DNS parser; no symbol trace |
| [GO-2026-5972](https://pkg.go.dev/vuln/GO-2026-5972) | Symbol | 1.26.6 | ASN.1/certificate handling path; malformed-input exploit not attempted |
| [GO-2026-6088](https://pkg.go.dev/vuln/GO-2026-6088) | Module | 1.26.6 | XML decoder; no symbol trace |
| [GO-2026-6089](https://pkg.go.dev/vuln/GO-2026-6089) | Symbol | 1.26.6 | Unencrypted HTTP/2 timeout; configured protocol exposure needs verification |
| [GO-2026-6090](https://pkg.go.dev/vuln/GO-2026-6090) | Symbol | 1.26.6 | TLS post-handshake resource handling; no malicious peer test |
| [GO-2026-6091](https://pkg.go.dev/vuln/GO-2026-6091) | Module | 1.26.6 | html/template; no symbol trace or app template exploitation |
| [GO-2026-6218](https://pkg.go.dev/vuln/GO-2026-6218) | Symbol | 1.26.6 | URL resolution path; no complexity payload executed |
| [GO-2026-6599](https://pkg.go.dev/vuln/GO-2026-6599) | Module | 1.26.9 | html/template; no symbol trace |
| [GO-2026-6600](https://pkg.go.dev/vuln/GO-2026-6600) | Module | 1.26.9 | html/template; no symbol trace |
| [GO-2026-6603](https://pkg.go.dev/vuln/GO-2026-6603) | Symbol | 1.26.9 | HTTP/2 server memory; server uses plain ListenAndServe, so HTTP/2 endpoint not established |
| [GO-2026-6604](https://pkg.go.dev/vuln/GO-2026-6604) | Symbol | 1.26.9 | Windows os.Root behavior; supported release targets are Linux/macOS |
| [GO-2026-6605](https://pkg.go.dev/vuln/GO-2026-6605) | Symbol | 1.26.9 | CONNECT client behavior; peer transports disable proxies; no vulnerable CONNECT flow demonstrated |
| [GO-2026-6607](https://pkg.go.dev/vuln/GO-2026-6607) | Symbol | 1.26.9 | ECH parsing; required application configuration not established |
| [GO-2026-6608](https://pkg.go.dev/vuln/GO-2026-6608) | Symbol | 1.26.9 | MIME/header parsing via HTTP paths; no multipart application handler identified |
| [GO-2026-6609](https://pkg.go.dev/vuln/GO-2026-6609) | Symbol | 1.26.9 | Direct unauthenticated static-file API match; Range CPU condition not executed |
| [GO-2026-6610](https://pkg.go.dev/vuln/GO-2026-6610) | Symbol | 1.26.9 | HTTP/2 client framing; outbound peer transport enables HTTP/2 |
| [GO-2026-6611](https://pkg.go.dev/vuln/GO-2026-6611) | Symbol | 1.26.9 | HTTP/2 server flow control; server deployment/protocol preconditions not established |
| [GO-2026-6612](https://pkg.go.dev/vuln/GO-2026-6612) | Symbol | 1.26.9 | HTTP/2 server flow control; same qualification |
| [GO-2026-6613](https://pkg.go.dev/vuln/GO-2026-6613) | Symbol | 1.26.9 | 2xx CONNECT server behavior; no explicit CONNECT endpoint; not asserted exploitable |
| [GO-2026-6617](https://pkg.go.dev/vuln/GO-2026-6617) | Symbol | 1.26.9 | HTTP/2 server crash; server protocol preconditions not established |
| [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932) | Module only | None | Unmaintained x/crypto/openpgp; app uses Argon2, no openpgp symbol trace |

## TypeSafe evaluation and independent review

Applied [TypeSafe AI skill](https://docs.typesafe.ai/agent-skill.md) using live documentation and `jev-latest`, resolved to **jev-1.13.0**. Only summarized finding evidence and explicit prerequisites were transmitted, without repository source or credentials. The service did not independently read or execute the app.

The test-plan Choice selected isolated local validation with probability/confidence **1.0/1.0**. Its separate scope judgment assigned **0.94** to rejecting intended admin Docker powers as unauthorized escalation. This planning request used 797 input / 69 output tokens.

The findings request used 3,194 input / 355 output tokens. Reproducible [request](security-review-2026-10-20/typesafe-request.json) and [raw response](security-review-2026-10-20/typesafe-results.json) are preserved. Ordered severity levels are 0 informational, 1 low, 2 medium, 3 high and 4 critical; fractional scores are weighted positions. Confidence is distribution concentration, not the probability a finding is correct. Support is a separate Noul judgment about the qualified evidence summary.

| ID | Severity / 4 | Confidence | Evidence support |
| --- | ---: | ---: | ---: |
| SEC-01 | 1.90 | 0.90 | 0.89 |
| SEC-02 | 3.00 | 1.00 | 0.87 |
| SEC-03 | 1.95 | 0.96 | 0.89 |
| SEC-04 | 1.85 | 0.88 | 0.88 |
| SEC-05 | 2.46 | 0.54 | 0.87 |
| SEC-06 | 1.84 | 0.84 | 0.91 |
| SEC-07 | 0.88 | 0.90 | 0.87 |
| SEC-08 | 0.98 | 0.97 | 0.89 |
| CI-01 | 1.82 | 0.85 | 0.90 |

The lower concentration on SEC-05 reflects deployment uncertainty; it is explicitly a local compiler/release exposure, not proof of 24 exploitable production flaws. The independent final reviewer confirmed SEC-01–SEC-04 from source and challenged their prerequisites/impact. The principal then dynamically reproduced them as described above. No overall score or finding was attributed to the failed delegated inspections.

### Publication coverage validation

The publication check first received only finding IDs and a short summary; its coverage Noul was 0.69. A second request compared the full finding descriptions with all issue drafts and existing #18: every one of the 25 finding IDs selected `covered`, with overall coverage Noul 0.81. CI-01 had weaker support (covered 0.70 / partial 0.30, concentration 0.55). The final reviewer identified a real wording ambiguity: merely documenting tagged jobs could omit runnable checks even where infrastructure supports them.

CI-01 now explicitly requires PR `make check` as a required check, runnable tagged integration/installer jobs where supported, and a documented deferred/manual policy otherwise. A focused comparison of that complete recommendation and the clarified draft returned `covered` with probability 1.0 and concentration 1.0 using jev-1.13.0; the [raw focused response](security-review-2026-10-20/publication-typesafe-result.json) is retained. This resolves publication coverage, not implementation or remediation. The earlier uncertainty and severity evaluations remain recorded rather than replaced.

## Remediation and unified backlog

1. Correct SEC-02 and rebuild with a patched compiler (SEC-05) before the next affected release.
2. Close the session-issuance race and implement bounded active-stream revocation (SEC-01/SEC-03).
3. Define trusted-proxy limiting and protect password recovery (SEC-04).
4. Address release authenticity (SEC-06), CI and branch protection (CI-01).
5. Fix earlier F1–F9 with focused behavioral regressions; address retention/render/transport work F10–F12 with measurements as appropriate.
6. Measure sampling cadence, query plans, repeated composition scans and recorder bookkeeping before choosing broader optimizations. Reuse existing #18 for the build-context issue.

A corrective change is done only after a focused test demonstrates the defect before implementation, the fix passes it, affected checks pass and broader validation runs where warranted. Documentation/configuration/measurement tasks use appropriate checks rather than artificial new tests. This audit does not mark any vulnerability fixed.

The [unified issue index](security-review-2026-10-20/issues.md) maps every actionable security item, all 12 earlier F findings, the earlier CI recommendation and all four measurement/lifetime follow-ups to GitHub. SEC-07/F10 is one issue; SEC-08 reuses #18. Earlier F findings remain their original qualified engineering findings, not newly executed security PoCs.

The complete issue descriptions, publisher manifest and confirmed GitHub URLs are kept alongside the report so scope and acceptance criteria remain reviewable even if GitHub is unavailable. The [unified issue index](security-review-2026-10-20/issues.md) records the published issues and the reused finding. Both review reports and these publication artifacts are maintained on `main`; issue creation does not mark a finding remediated.
