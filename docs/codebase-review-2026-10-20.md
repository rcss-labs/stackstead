# Codebase review — 20 October 2026

## Assessment

**Overall score: 7.2/10.** Stackstead has a sound foundation for a small self-hosted Docker manager: clear package boundaries, careful file persistence, bounded concurrent work, substantial behavioral tests, and a deliberately small deployment footprint. The best next investment is correcting history and cross-server behavior, then controlling retained memory and automating the existing checks. A broad rewrite would add risk without addressing these specific problems.

This is a point-in-time engineering audit, not a security certification or a claim that every code path was inspected. No application behavior was changed as part of the review.

### Scope and method

Reviewed commit: `885dbbd1a26258a978569e7fba8d867baef44211` on `main`. The checkout advanced from `0869445` during discovery; the additional commit corrects an installer health-check expectation and is included in this baseline. Source references and line numbers below refer to this baseline.

The review combined three independent architectural reviews with consolidation and caller/test inspection. Examined areas include Go API/auth/peering, Compose editing and operations, Docker state, storage, metrics/history/alerts, CLI, update/install/package tooling, React controllers/components, routing, streaming, dependencies, and development documentation. Counts under `internal/` and `web/src/`: 133 production Go files (30,597 lines), 143 Go test files (42,440 lines), 221 other TypeScript/TSX files (23,441 lines), and 129 web test files (27,179 lines). These are inventory figures, not coverage percentages; CLI/tool test files outside these directories are additional.

Findings are based on source, callers, and existing tests. Concrete trigger examples below are reasoned reproductions, not newly executed regression tests. Performance estimates are marked explicitly. The existing verification suite was executed separately.

### Scoring rubric

Scores are reviewer judgments informed by the evidence, not benchmark results or TypeSafe-generated overall ratings.

| Dimension | Weight | Score / 10 | Reason |
| --- | ---: | ---: | --- |
| Architecture and maintainability | 20% | 8.0 | Useful package/controller boundaries; some repeated policy and large orchestration units |
| Correctness and reliability | 25% | 6.5 | History, cross-server, CLI, and installation edge cases remain |
| Efficiency and resource control | 20% | 6.5 | Good concurrency/cache controls; retained output and repeated work need attention |
| Security engineering | 15% | 8.0 | Strong authentication/file protections; Docker access remains root-equivalent |
| Tests and local verification | 10% | 8.5 | Broad behavioral and race coverage; specific missing cases identified below |
| Delivery and operational practices | 10% | 6.0 | Pinned dependencies and careful packaging, but CI is deferred and install transitions need work |

Weighted result: 7.175, rounded to **7.2/10**. Scores of 5–6 indicate useful code with material gaps; 7–8 indicate a strong foundation needing targeted work; 9–10 require stronger demonstrated correctness and operational evidence.

## Verification evidence

`make -n check` was inspected before running `make check`. The latter completed with exit code 0 on this baseline.

| Check | Result |
| --- | --- |
| TypeScript, `tsc --noEmit` | Passed |
| Production UI build and budget enforcement | Passed |
| Vitest | 129 test files; 2,363 tests passed |
| `go vet ./...` | Passed |
| Repository `gofmt -l` gate | Passed |
| `go test -race -timeout 300s ./...` | Passed; some package results came from Go's cache |

Build measurements from the repository budget script: eager JavaScript **107.5 KiB gzip / 120 KiB**, total JavaScript **372.3 KiB gzip / 400 KiB**, fonts **94.9 KiB / 200 KiB**. The build-shape tests enforce a tighter 110 KiB eager limit. Total JavaScript has approximately 6.9% budget headroom, so preserve lazy boundaries when adding features.

Environment: macOS arm64, Go 1.26.4, Node 24.14.0, npm 11.9.0.

**Not run:** Docker integration tests, tagged end-to-end tests, installer/package/container suites, Linux/systemd transition experiments, browser interaction checks, dependency vulnerability scanning, heap/CPU profiling, or production load tests. Passing `make check` does not validate those paths or prove the findings below have been fixed. No new tests were added because the deliverable changes documentation only.

## Correctness findings

P2 means a meaningful defect or conditional reliability risk to fix in planned engineering work. No P0/P1 issue is established by this review. Priority and confidence must be revisited if runtime evidence changes the impact. The security score reflects inspected safeguards, not a comprehensive security assessment.

### F1 — Health retention deletes necessary baseline state (P2)

**Evidence:** [history/store.go](../internal/history/store.go):69–87, 137–147; [timeline.go](../internal/history/timeline.go):65–100.

Pruning retains each container's latest state overall, rather than the latest non-marker state before the retention boundary. With healthy at time 100, exited at 300, and a cutoff of 200, healthy@100 is deleted. Reconstructing [200,400] then shows no data until 300, even though the system previously knew the container was healthy.

Preserve the last non-marker predecessor before the cutoff, as well as all retained-window rows; still expire containers whose removal is fully outside retention. Existing prune tests cover a container with an old unchanged baseline, and one expectation currently allows deletion of the needed predecessor. Add a boundary-transition regression before changing the query.

### F2 — Rolling restart counts undercount long-running containers (P2)

**Evidence:** [host/collector.go](../internal/host/collector.go):517–556; [alerts/rules.go](../internal/alerts/rules.go):104–105.

The collector loads start events inside the last 24 hours and subtracts one for each container. If the first-ever start happened two days ago and one restart happens today, today's only start is subtracted and the count is zero. Three restarts become two, suppressing the threshold-three restart-loop alert.

Use durable lifecycle observations with explicit coverage boundaries instead of treating the first start in every rolling window as the initial start. The history recorder already recognizes die→start pairs, but currently records only Compose projects, so reuse needs to preserve standalone-container support. Add cases for an initial start outside the window, starts inside it, and incomplete event history. Docker also documents a finite recent-event history: [Docker events reference](https://docs.docker.com/reference/cli/docker/system/events/).

### F3 — Remote service port links target the gateway host (P2)

**Evidence:** [PortLinks.tsx](../web/src/app/overview/PortLinks.tsx):17; [StackRow.tsx](../web/src/app/overview/StackRow.tsx):97; remote/fleet callers.

Port links use `window.location.hostname`, including rows belonging to another server. Viewing host B through host A and clicking B's port therefore opens A's port, potentially a different application.

Carry the owning service host through the server scope. An advertised management URL is not necessarily the correct application host; if no usable address is known, render a port without a misleading link. Test local, remote, and fleet rows, including the same port on two hosts. Existing remote fixtures have no ports.

### F4 — Slow fleet polling can discard every completed response (P2)

**Evidence:** [servers/fleet.ts](../web/src/lib/servers/fleet.ts):29–46.

Every five seconds a new request increments a sequence counter. Only the newest request can publish its result. If responses consistently take six seconds, each is invalidated by the next poll before it completes; the page remains loading or stale and requests overlap. The peer timeout does not bound browser-side/network queueing.

Skip interval refreshes while a load is pending, or schedule the next interval after completion. Preserve generation checks for stop/restart invalidation. Existing host/list controllers provide a useful pattern. Add a deferred-response test exceeding the interval and verify that a result appears with bounded concurrent requests.

### F5 — Browser navigation can discard unsaved work (P2)

**Evidence:** [useUnsavedGuard.ts](../web/src/app/hooks/useUnsavedGuard.ts):28–30; [useCreateForm.ts](../web/src/app/create/useCreateForm.ts):173–174.

The shared guard intercepts links and logout but explicitly excludes Back/Forward. New stack marks itself dirty without installing an unload handler. A dirty draft can disappear on Back or reload without confirmation; existing detail editors have some unload protection, so behavior varies across forms.

Use shared history-navigation blocking and dirty-state unload handling. Test Stay/Discard, Back/Forward, New stack reload, and clean navigation. Avoid layering further independent click guards onto the existing partial solution.

### F6 — Live CLI metrics silently ignore the service filter (P2)

**Evidence:** [cmd_metrics.go](../internal/cli/cmd_metrics.go):93–99, 161–185.

`metrics --live --service web` accepts the flag, then drops it when calling the live handler. Both table and NDJSON output include other services. Historical metrics correctly apply the filter.

Carry the service into the live handler and filter before either output branch, or explicitly reject an unsupported combination. Add a two-service stream regression for both output formats; current tests cover history filtering and live output separately.

### F7 — Database creation treats path characters as SQLite URI syntax (P2)

**Evidence:** [store.go](../internal/store/store.go):147–150, 228–244.

`Open` concatenates a raw filesystem path into a `file:` URI. `OpenExisting` correctly escapes percent, question-mark, and hash characters. A valid configured data directory containing URI syntax can therefore fail creation or select an unintended path, while backup/admin commands look for the literal filename.

Use a shared SQLite URI builder across creation and existing-open/checkpoint paths. Test exact file placement, reopening, and backup with `%`, `?`, and `#` in directory names. This is a configuration-triggered path correctness issue; arbitrary database compromise is not established.

### F8 — Database initialization lacks maintenance exclusion (P2)

**Evidence:** [cmd_db.go](../internal/cli/cmd_db.go):79–101; the migration path in [store.go](../internal/store/store.go):164–195.

`db init` can perform migrations without the application lock used by serving and restoring. A newer CLI run against an older active server can migrate its live schema; initialization can also race restore publication. SQLite transaction locks do not replace the application's maintenance exclusion.

Acquire the exclusive maintenance lock before opening/migrating. The CLI documentation explicitly permits online initialization today, so revise that contract deliberately. Add a held-lock test proving refusal and unchanged schema. The hazardous outcome depends on a version difference or concurrent restore, not ordinary same-version initialization alone.

### F9 — Switching from script to package installation can keep the old executable active (P2)

**Evidence:** [install.sh](../install.sh):190, 808–810; [nfpm.yaml](../deploy/nfpm.yaml):29–35; Debian/RPM preinstall scripts under [deploy/pkg](../deploy/pkg/).

The script writes `/etc/systemd/system/stackstead.service` pointing at its `/usr/local/bin` binary. Packages install the binary under `/usr/bin` and a lower-precedence unit under `/usr/lib/systemd/system`. Installing a package over a script installation has no rejection/migration guard, so the old unit can continue launching the old executable while package operations appear successful. This follows the documented [systemd unit load precedence](https://www.freedesktop.org/software/systemd/man/latest/systemd.unit.html).

Either reject mixed layouts with non-purge migration instructions, or migrate only a positively identified installer-owned unit. Preserve administrator overrides. Add a script→package transition test asserting the actual running executable and version. This was traced statically, not exercised on systemd during this review.

## Code waste and efficiency

### F10 — Operation output needs byte and global lifetime budgets (P3; measure impact)

**Evidence:** [stacks/ops.go](../internal/stacks/ops.go):28–31, 145–153, 213–224, 386–399; stack removal in [service.go](../internal/stacks/service.go):597–606.

Retention caps are 20 operations per stack, 20,000 lines per operation, and approximately 8 KiB per line. That permits roughly **156 MiB per operation / 3.05 GiB per stack** of retained text before object overhead. These are theoretical maxima, not observed heap usage. Completed histories expire only when newer operations on the same stack displace them; removing a stack does not release its records. Churn across unique stack names has no global retention bound.

Add per-operation byte limits, a global completed-output budget, and TTL/LRU eviction; define removed-stack cleanup while preserving active subscribers. Keep live streaming independent of retained replay size. A fixed circular buffer would also make line retention clearer than repeated slice-head eviction. Validate bounded bytes and repeated create/build/remove churn, then measure heap behavior.

### F11 — Peer polling repeatedly rebuilds transports (P3)

**Evidence:** [api/fleet.go](../internal/api/fleet.go):122–126; [peers/service.go](../internal/peers/service.go):404–409; contrast [api/forward.go](../internal/api/forward.go):29–66.

Fleet requests and checker hello calls create new transports and close idle connections every call. This discards cross-poll connection reuse and repeats TCP/TLS setup. The forwarding path already caches transports by peer address and trust configuration.

Consider a shared, bounded per-peer client cache with trust/address invalidation, removal cleanup, and shutdown cleanup. Retain request-specific deadlines. Benchmark handshake/request rates before claiming latency improvements; this is demonstrated repeated work, not a measured bottleneck.

### F12 — Collapsed log output still performs full rendering work (P3)

**Evidence:** [LogConsole.tsx](../web/src/app/detail/LogConsole.tsx):173–197; [LogLines.tsx](../web/src/app/detail/LogLines.tsx):119–147; [metrics.ts](../web/src/lib/stacks/metrics.ts):26–29.

The collapsed console keeps the log body mounted. Each published update maps up to 5,000 buffered rows; per-row color lookup copies and sorts the same service list even though the caller already sorted it.

Precompute a service-to-color map and avoid reconciling invisible rows while retaining the stream and buffer. Profile a full-buffer stream before choosing virtualization. Preserve search, keyboard navigation, pause/resume, scroll position, and stable colors. No frame-time or CPU regression was measured.

### Measure before larger changes

- **Sampling cadence and timestamps:** [sampler.go](../internal/metrics/sampler.go):184–213 performs blocking Docker stats requests in batches of four. [engine/stream.go](../internal/engine/stream.go):270–274 documents a one-to-two-second measurement window. At 100 containers, the resulting 25–50 seconds is an estimate, exceeding the default ten-second interval. Missed ticks are dropped, and results use the tick's starting time rather than `Stats.Read`. Instrument tick duration/sample age; evaluate persistent streams or local deltas before raising concurrency.
- **Metrics query indexing:** [store.go](../internal/store/store.go):60 indexes project/time, while global queries and pruning filter time without a project. At 100 Compose containers, ten-second sampling, and 72-hour retention, the order of magnitude is 2.59 million samples. Use representative data and `EXPLAIN QUERY PLAN` before adding a timestamp index; weigh write overhead.
- **Compose/container scans:** [stacks/service.go](../internal/stacks/service.go):376–389, 423–441 rereads compose files and scans containers per service. Prefer a per-computation project/service index before adding a long-lived file cache with complex invalidation.
- **Recorder bookkeeping:** [history/recorder.go](../internal/history/recorder.go):46–48 retains `dying` and `lastRS` maps. Review destroyed-container cleanup as a smaller lifetime improvement.

No repository-wide dead-code deletion is justified by this audit. Similar-looking DTOs and server/client validation often protect different boundaries. Avoid deduplication that conflates API contracts, security checks, and UI feedback. Prefer removing demonstrated repeated work over reorganizing files for their own sake.

## Practices worth preserving

- Small Go dependency surface; exact web dependency versions and a committed lockfile.
- React-independent controllers and focused UI components; route-level lazy loading keeps CodeMirror/uPlot out of the eager bundle.
- Password hashing has bounded concurrency, opaque sessions are hashed in storage, and setup/auth paths have deliberate protections.
- API caches share in-flight computation; storage cleanup rechecks identity/use before execution.
- Compose writes use stale-file hashes, backups, atomic publication, and fsync.
- Operations serialize by stack; subprocess handling and streams include cancellation, slow-consumer behavior, and bounded shutdown.
- Backup/restore and update downloads have ownership, checksum, size, redirect, and publication safeguards.
- Behavioral tests cover many cancellation, stale-response, reconnect, and concurrency cases. Accessibility includes focus handling and chart alternatives.

## Recommended sequence

1. Fix F1–F7 with focused red/green regressions. Prioritize history accuracy, lost drafts, and wrong server destinations; ensure each test exposes the intended failure before implementation.
2. Address F8–F9 with explicit maintenance/install contracts and relevant lock/systemd transition tests.
3. Bound operation replay memory (F10), then remove transport/render repetition (F11–F12) with before/after measurements.
4. Automate the existing `make check` as a pull-request check. CI is explicitly deferred today; verify branch protection separately. Add tagged integration/installer jobs according to available infrastructure rather than pretending unit tests cover installation.
5. Instrument sampling and global metrics query cost before changing architecture or adding dependencies.

A fix is done when the focused regression first demonstrates the defect, the correction passes it, the affected suite passes, and broader checks run where the impact warrants them. The completed audit is not a completed remediation backlog.

## TypeSafe evaluation record

The TypeSafe AI skill was used with `jev-latest`, resolved by the service to **jev-1.13.0**. Requests sent only summarized evidence, not source code or credentials. The model evaluated priority/support from those summaries; it did not independently inspect or execute the repository.

Severity uses five ordered levels: 0=P4 optional, 1=P3 low urgency, 2=P2 planned defect/risk, 3=P1 urgent serious impact, 4=P0 catastrophic. Scores are weighted positions; they are not the overall 7.2/10 rating. Confidence describes distribution concentration, not the probability a finding is correct. Support is the separate Noul probability that the supplied summary supports a concrete finding.

| Finding | Severity score | Confidence | Support |
| --- | ---: | ---: | ---: |
| F1 health baseline | 2.19 | 0.83 | 0.91 |
| F2 restart counts | 1.98 | 0.96 | 0.90 |
| F3 remote port host | 2.00 | 0.97 | 0.91 |
| F4 fleet polling | 1.93 | 0.93 | 0.90 |
| F5 unsaved navigation | 1.98 | 0.96 | 0.89 |
| F6 live service filter | 1.98 | 0.98 | 0.93 |
| F7 SQLite URI | 1.92 | 0.93 | 0.91 |
| F8 init maintenance lock | 2.23 | 0.67 | Not asked |
| F9 mixed installation | 1.91 | 0.85 | Not asked |
| F10 operation memory | 1.37 | 0.61 | 0.88 |
| F11 transport churn | 1.03 | 0.93 | 0.89 |
| F12 log rendering | 0.81 | 0.82 | 0.80 |
| Sampling cadence estimate | 1.57 | 0.64 | Not asked |
| Deferred CI | 0.89 | 0.81 | 0.70 |

Two scoring requests used 3,454 input and 461 output tokens in total. Lower-concentration assessments for operation memory, initialization, and sampling are kept conditional and require targeted evidence; they are not grounds for an unmeasured rewrite. Reviewer priority places F10 at P3 pending impact measurement.

Reference: [TypeSafe confidence semantics](https://docs.typesafe.ai/confidence).
