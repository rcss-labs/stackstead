# Stackstead Reachability checks — Design

**Date:** 2026-10-18
**Status:** Draft, awaiting review. Adds application reachability checks (HTTP, TCP and DNS) that Stackstead runs on a schedule, beyond Docker's container healthchecks. It shows the results next to container health, stores their history, and raises alerts on them. Implemented by Plan R1 (backend, API, CLI) and Plan R2 (UI). Builds on `2026-10-14-stackstead-v1-design.md`, `2026-10-15-stackstead-ui-v2-design.md`, `2026-10-15-stackstead-networks-multiservice-design.md` and `2026-10-14-stackstead-cli-design.md`.

## 1. Purpose

A container can be `running (healthy)` while its users cannot reach it: the port is not published, the reverse proxy routes to the wrong place, the TLS certificate expired, or DNS stopped answering. Docker healthchecks run inside the container and cannot see any of this. Stackstead already knows each stack's published ports and container addresses, so it can check the service from outside the container.

Success criteria:

- From stack detail an operator can add an HTTP check (`https://nas.lan/`, expect 200–399, body contains `Sign in`, warn 14 days before the certificate expires), a TCP check (service `db`, port 5432) and a DNS check (`pihole.lan` A `192.168.0.30`). They can test each one before saving and see it go **up** within one interval.
- The checks are stored in the stack's `compose.yaml` under `x-stackstead-checks`. `docker compose config` still accepts the file, and saving a check never recreates a container.
- A check whose target stops answering turns **down** after 2 consecutive failures (the default) and raises a `check_down` alert. It returns to **up** on the next success. A certificate inside the warning window turns the check **warning** and raises `cert_expiring`.
- The stack list shows a Checks cell next to Health. Stack status and container health counts mean exactly what they mean today.
- Results are kept for `checks_retention` (default 7 days). The UI shows a 24 h uptime bar and latency history per check.
- Checks never reach link-local, multicast, unspecified or broadcast addresses, whether directly, through a redirect or through DNS rebinding. They never send credentials, and they never store response bodies.
- "Suggest checks" proposes checks from the compose file's published ports, and the admin chooses which ones to add.
- `stackstead checks list [stack]` and `stackstead checks run <stack> <check>` work against a running server.
- `/metrics` exposes per-check up, latency and certificate-expiry gauges.

## 2. Scope

### In
- `internal/checks`: definitions, validation, HTTP/TCP/DNS runners, target policy, service-target resolution, scheduler, result store, suggestions.
- `composefile`: generic read, set and remove of entries in a top-level extension mapping.
- SQLite migration for `check_results`; config key `checks_retention`.
- API: check CRUD (hash-protected file edits), preview, test, run now, results history, suggestions, all-stacks status. `checks` counts on stack summaries. `checks` message on `/api/events`. Two new alert kinds. Prometheus gauges.
- CLI: `stackstead checks list [stack]`, `stackstead checks run <stack> <check>`.
- UI: Checks tab, check form, suggestions dialog, aside block, a line on each service card, a Checks cell in the stack list.
- Tests, E2E smoke extension, docs.

### Out
- A global `/checks` screen or rail entry.
- Custom request headers, basic/bearer auth, client certificates, request bodies, methods other than GET/HEAD.
- A choice of DNS server: DNS checks use Stackstead's system resolver only. A DNS server such as Pi-hole is tested by pointing the host's resolver at it, or with a TCP check on port 53.
- Probes run from inside a stack's network (probe containers, `docker exec`).
- UDP checks other than DNS, ICMP ping, TCP banner matching, gRPC, WebSocket.
- Editing checks from the CLI.
- Notifications (sibling spec; see §12).

## 3. Key decisions

The TypeSafe skill named in the global rules is an SDK for an external judgment API and cannot run here, so these decisions are argued in writing. Each judgment call was put to the user, and the option chosen is recorded.

| Decision | Choice | Reason |
|---|---|---|
| Where checks are defined | A top-level `x-stackstead-checks:` mapping in the stack's compose file, edited through `composefile` with the existing hash/stale flow | User decision. Follows v1 §4: "compose files are the truth; the DB stores only registry entries". Compose ignores `x-` keys, so editing never recreates a container. The checks travel with the file and appear in config history. Accepted costs: every check edit writes the user's file (and records a config-history revision, as every save does), and a file that does not parse runs no checks. Rejected: a SQLite table (breaks the file-is-truth rule), service labels (a label change recreates the container), a hybrid (two sources of truth). |
| Where checks run from | The Stackstead process's own network namespace. Two target forms: **literal** (URL, host:port or DNS name as written) and **service** (`service` + container `port` + optional `network`, resolved per run to that container's IP from the engine snapshot) | User decision. Native Linux, the supported deployment, reaches bridge-network container IPs, so unpublished ports can be checked without new machinery. Rejected: probe containers or `docker exec`, which mean image pulls, a container per run, and code running inside user containers. |
| Limits of service targets | Classified up front, without dialing, as `unreachable_from_stackstead`: macvlan/ipvlan networks (the host cannot reach them, networks spec §6.2) and Stackstead on macOS (container IPs live in Docker Desktop's VM). Shown as **warning**, never **down** | User decision (Q2). A result that is wrong by construction must not page anyone. Stackstead in a container (unsupported, installers spec §2) is documented: only networks that container is attached to are reachable, and `localhost` is the Stackstead container itself. |
| Status model | Reachability is a separate dimension. Check states are `pending`, `up`, `warning`, `down`, `paused` and `idle`. Stack summaries gain `checks: {up, warning, down, pending}`. `status` and `health` are unchanged | User decision. External reachability and in-container health are different facts. Folding them together would change the meaning of existing API fields and of the list chips. |
| Stopped stacks | A check on a stack with no running containers is `idle` and is not run. A check bound to a `service` with no running container is also `idle` | A deliberate `down` must not raise alerts. This follows from the separate-dimension model. |
| Target restrictions | A deny-list enforced at dial time by `net.Dialer.Control`: link-local (169.254.0.0/16, fe80::/10), multicast, unspecified and limited broadcast, including IPv4-mapped forms. Loopback and private ranges are allowed. http/https only | User decision. Enforcing at dial time covers literal IPs, hostnames, DNS rebinding and every redirect hop with one hook. Homelab targets are private by nature. The cloud metadata address is the classic SSRF prize. |
| Redirects and credentials | At most 5 redirects, no https→http downgrade, each hop through the same dialer. URL userinfo is rejected. No custom headers, no cookie jar, proxy environment ignored. Bodies are read up to 64 KiB for matching and never stored. Messages are built from classified errors, with query strings redacted | User decision (Q4). An environment proxy would bypass the dial-time policy. Messages are shown to any signed-in user, so they must not echo tokens from URLs. |
| Scheduling | Per-check `interval` (default 60 s, min 15 s, max 1 h), `timeout` (default 5 s, max 30 s, less than the interval), `failAfter` (default 2, range 1–10). Up after 1 success. One scheduler with concurrency 4 and ±10 % jitter. At most 50 checks per stack and 200 in total (user decision: bounds storage on a Pi to about 2 M rows per week at 60 s). Run now is limited to 1 run per check per 5 s | User decision. A concurrency of 4 matches the metrics sampler. Jitter keeps checks from bunching on the same tick on a Pi. |
| History | Every result is a row in `check_results`, retained for `checks_retention` (default `168h`) and pruned hourly. Current state is rebuilt from the newest rows at start-up | User decision. "Was it flapping last week" needs more than the 72 h metrics window. At 60 s that is about 10 k rows per check per week. |
| Suggestions | On demand only: the admin ticks the proposals to add | User decision. Nothing writes to a user's file unasked. |
| UI placement | Checks tab, aside block, a line on each service card, a stack-list cell. Alerts go through the existing AlertsCard. No global page | User decision. |
| DNS | System resolver only. Fields `name`, `type` (A/AAAA/CNAME/TXT) and `expect`. Passes when every expected value is in the answer, or, with no `expect`, when there is at least one answer | User decision (Q9 C). No new dependency (`net.Resolver`). |
| CLI | `checks list [stack]` and `checks run <stack> <check>`; no editing | User decision. |
| Prometheus | `stackstead_check_up`, `stackstead_check_latency_seconds` and `stackstead_check_cert_expiry_timestamp_seconds` on the existing `/metrics` | User decision. Lets existing Alertmanager setups alert without the notifications feature. |
| Alerts | Derived on request from the checks engine's in-memory state, like every other kind (ui-v2 §10.4) | Follows the existing alerts design. Nothing new is persisted for alerts. |
| Check identity | The mapping key, `[a-z0-9][a-z0-9-]{0,62}`, unique per stack. Results are keyed by `(stack, check_id)`. Renaming a key starts a new history | The key is the only stable identity a hand-edited file gives. The old rows age out through retention. |
| Unknown or invalid entries | Validated strictly (unknown keys rejected). An invalid entry is reported in `errors` and not run. The compose file is still accepted | The rest of the stack keeps working. The UI points at the Compose tab to fix it. |

## 4. Check definitions

### 4.1 Compose shape

```yaml
x-stackstead-checks:
  home:                      # check id
    type: http
    url: https://nas.lan/
    expectStatus: "200-399"
    bodyContains: Sign in
    certExpiryWarnDays: 14
  db:
    type: tcp
    service: db              # service target
    port: 5432
    network: backend         # optional; required when the container is on several networks
    interval: 30s
  pihole-a:
    type: dns
    name: pihole.lan
    recordType: A
    expect: [192.168.0.30]
    failAfter: 3
```

Durations are strings with units (as `rejectBareDurations` already requires for config). Optional keys in every entry are `name` (display label, ≤ 80 chars), `interval`, `timeout`, `failAfter` and `enabled` (default `true`; `false` means **paused**). On a literal-target check, `service` is optional and only groups the check under that service in the UI.

### 4.2 Types and options (`internal/checks/model.go`)

```go
type Definition struct {
    ID, Name, Type string        // Type: "http" | "tcp" | "dns"
    Service        string        // grouping, or the service target
    Interval, Timeout time.Duration
    FailAfter      int
    Enabled        bool
    HTTP *HTTPOptions; TCP *TCPOptions; DNS *DNSOptions
}
type ServiceTarget struct{ Service, Network string; Port uint16 }
```

| Type | Fields | Rules |
|---|---|---|
| `http` | `method` (`GET` default, `HEAD`); `url` **or** service target + `scheme` (`http`/`https`) + `path` (default `/`); `expectStatus` (default `"200-399"`; comma list of codes and `lo-hi` ranges, 100–599); `bodyContains` (≤ 256 bytes, GET only); `tlsVerify` (default `true`); `tlsServerName` (service targets over https); `certExpiryWarnDays` (default 14, 0 = off, https only); `followRedirects` (default `true`); `maxRedirects` (0–5, default 5) | `url` is http/https, ≤ 2048 chars, has a host and no userinfo. Over https, a service target with `tlsVerify: true` requires `tlsServerName` (a certificate cannot match a container IP). User-Agent `Stackstead-Check/<version>`. No keep-alive: each run opens a new connection. |
| `tcp` | `host` + `port` **or** service target | Success = connect within `timeout`, then close. |
| `dns` | `name` (hostname, ≤ 253), `recordType` (`A` default, `AAAA`, `CNAME`, `TXT`), `expect` (≤ 16 values) | No service target. Comparison: A/AAAA as parsed IPs, CNAME lower-cased without the trailing dot, TXT exact. |

`func Parse(id string, node *yaml.Node) (Definition, []FieldError)` and `func Encode(Definition) *yaml.Node` (canonical key order, defaults omitted). `func Validate(Definition) []FieldError` uses field paths relative to the check (`url`, `expectStatus`, `port`, …).

### 4.3 Results and states

```go
type Result struct {
    Stack, CheckID string
    TS time.Time
    OK bool
    State string          // state after this result
    Latency time.Duration // http: to response headers; tcp: connect; dns: lookup
    StatusCode int        // http only
    ErrorClass, Message string
    CertNotAfter *time.Time // https leaf certificate, also with tlsVerify false
    Container string      // resolved container for service targets
}
```

Error classes: `timeout`, `refused`, `unreachable`, `dns`, `nxdomain`, `tls`, `cert_expired`, `status`, `body`, `answer`, `blocked`, `redirect_limit`, `downgrade`, `config`, `unreachable_from_stackstead`, `docker_unavailable`. `Message` is ≤ 200 characters, built per class (for example `status 502 not in 200-399`, `connection refused to 10.0.3.4:5432`, `blocked: 169.254.169.254 is link-local`). The URL's query string is shown as `?…`. Raw response text is never included.

State machine (`internal/checks/state.go`), per check:

- `paused` while `enabled: false`. `idle` while the stack has no running container, or a service-bound check's service has none. Neither runs.
- `pending` until the first counted result.
- A failure increments `consecutiveFailures`. At `failAfter` the state becomes `down`; before that it keeps its previous value.
- A success sets `up`, or `warning` when the certificate expires within `certExpiryWarnDays` (with `tlsVerify: false`, an already expired certificate is also `warning`; with verification on it is a `cert_expired` failure).
- `unreachable_from_stackstead` gives `warning` and does not count as a failure. `docker_unavailable` (no snapshot to resolve a service target) is recorded, leaves the state unchanged and does not count.
- `since` is the time of the last state change. At start-up, state, `since` and `consecutiveFailures` are rebuilt from the newest `failAfter` rows and the latest state change in `check_results`.

### 4.4 Service-target resolution (`internal/checks/resolve.go`)

`func ResolveTarget(project string, t ServiceTarget, containers []engine.Container, drivers map[string]string, goos string) (addr, container string, class string, err error)`

- The candidates are running containers with the project and service labels. The first by name is used (replica 1), and its name goes into `Result.Container`. If there is none, the check is `idle`.
- Network: the network named `network` is looked up as `<project>_<network>` first, then by its bare name (external). With no `network` set, the container must be on exactly one network; otherwise the result is class `config` with the message "service is on several networks; set network".
- If the network's driver (from the cached `/api/networks` data) is `macvlan` or `ipvlan`, or `goos == "darwin"`, the result is `unreachable_from_stackstead` and nothing is dialed.

## 5. Running checks

### 5.1 Target policy (`internal/checks/policy.go`)

`func DialControl(network, address string, _ syscall.RawConn) error` refuses (class `blocked`) 169.254.0.0/16, fe80::/10, 224.0.0.0/4, ff00::/8, 0.0.0.0/8, ::, 255.255.255.255, and the IPv4-mapped forms of these. Every HTTP and TCP dial goes through one `net.Dialer{Control: DialControl}`. DNS checks use the system resolver and are not dialed by Stackstead.

HTTP client (`internal/checks/http.go`): `Transport{Proxy: nil, DialContext: policyDialer, DisableKeepAlives: true, TLSClientConfig: {InsecureSkipVerify: !tlsVerify, ServerName: tlsServerName}}`, `Jar: nil`, and a `CheckRedirect` that enforces `maxRedirects`, refuses https→http, and drops nothing because nothing sensitive is sent. The body is read through `io.LimitReader(64 KiB)` only when `bodyContains` is set, then discarded.

Runner seams for tests:

```go
type Runner interface{ Run(ctx context.Context, d Definition, target Target) Result }
type Resolver interface {
    LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
    LookupCNAME(ctx context.Context, host string) (string, error)
    LookupTXT(ctx context.Context, host string) ([]string, error)
}
type ContextDialer interface{ DialContext(ctx context.Context, network, addr string) (net.Conn, error) }
```

### 5.2 Scheduler (`internal/checks/engine.go`)

`func NewEngine(src DefinitionSource, containers ContainerSource, networks NetworkDrivers, store *Store, runner Runner, now func() time.Time, logger *slog.Logger) *Engine`

- Definitions come from `stacks.Service.Details` (each stack's parsed file). They are reloaded every 30 s and immediately after a check edit or compose save (`Engine.Reload(stack)`). A stack whose file hash has not changed keeps its schedule.
- Each check's next run is `last + interval ± 10 % jitter`. A new check runs within 5 s of being loaded. A pool of 4 workers runs them; when the pool is busy, runs queue and are never run twice in a row to catch up.
- `RunNow(ctx, stack, id) (Result, error)` runs immediately, stores the result and updates state. It returns `ErrRateLimited` within 5 s of the previous run of that check.
- `Test(ctx, stack string, d Definition) Result` runs once without storing anything or changing state (same 5 s limit, keyed by stack).
- `States(stack string) []Status`, `Counts(stack string) Counts`, and `SubscribeTransitions() (<-chan Transition, func())` (buffered, drop-on-slow, as `state.Cache.Subscribe`) for `/api/events`.
- Limits: a stack with more than 50 entries, or a total above 200, runs the first ones in file and name order. The rest are reported in `errors` as "check limit reached".
- Shutdown: the context is cancelled, in-flight runs are abandoned, and their results are dropped.

### 5.3 Storage (`internal/store` migration, `internal/checks/store.go`)

Appended migration:

```sql
CREATE TABLE check_results (
  ts INTEGER NOT NULL, stack TEXT NOT NULL, check_id TEXT NOT NULL,
  ok INTEGER NOT NULL, state TEXT NOT NULL, latency_ms INTEGER NOT NULL,
  status_code INTEGER NOT NULL DEFAULT 0, error_class TEXT NOT NULL DEFAULT '',
  message TEXT NOT NULL DEFAULT '', cert_not_after INTEGER, container TEXT NOT NULL DEFAULT '');
CREATE INDEX check_results_stack_check_ts ON check_results(stack, check_id, ts);
```

`Store` methods: `Insert(ctx, Result)`, `Recent(ctx, stack, id string, limit int) ([]Result, error)`, `Buckets(ctx, stack, id string, from, to time.Time, step time.Duration) ([]Bucket, error)` (`Bucket{TS; Total, OK int; LatencyAvg, LatencyMax time.Duration; Worst string}`), `Uptime(ctx, stack, id string, since time.Time) (float64, bool, error)`, `Prune(ctx, olderThan time.Time) (int64, error)` (run hourly by the engine), and `DeleteStack(ctx, stack string)` (called when a stack is removed). Write failures are logged, the result is still applied to the in-memory state, and `/api/status` gains `checksDegraded`.

Config: `checks_retention` (default `168h`, env `STACKSTEAD_CHECKS_RETENTION`, added to `durationKeys` and to the config template). Retention is by time only; there is no per-check row cap (user decision). Sizing note for `docs/api.md`: 20 checks at 60 s is about 200 k rows per week, and the 200-check ceiling about 2 M.

### 5.4 composefile

Generic and check-agnostic:

- `(*File).ExtensionEntries(block string) ([]Entry, error)`, where `Entry{Key string; Node *yaml.Node}` in file order. A missing block returns empty. A block that is not a mapping, or that uses anchors or aliases, returns `ErrReadOnlyExtension` (shown as "edit in the Compose tab").
- `(*File).SetExtensionEntry(block, key string, value *yaml.Node) (*File, error)`: replaces the value of `key` or appends it. If the block is missing, it is created at the end of the document. The replaced entry's own inner comments are not kept; everything else is byte-identical.
- `(*File).RemoveExtensionEntry(block, key string) (*File, error)`: removes the key and its attached comments. If the block becomes empty, the block key is removed too.

## 6. Suggestions (`internal/checks/suggest.go`)

`func Suggest(project string, services []stacks.ServiceDetail, existing []Definition) []Definition`, driven by the compose file's published ports (`stacks.ParsePorts`):

- Each fixed host port on TCP gives `tcp` to `<bind IP or 127.0.0.1>:<hostPort>`, id `<service>-<hostPort>`. Port ranges and UDP ports are skipped, except UDP or TCP 53, which gives a `dns` proposal with `name` left empty (the form requires it before saving).
- A container port in {80, 8080, 8000, 3000, 5000, 9000} also gives `http` `GET http://<host>:<hostPort>/` expecting 200–399. A container port of 443 or 8443 gives the same check with `https` and `tlsVerify: true`.
- A proposal is skipped when an existing check has the same type and target. Ids get `-2`, `-3`… suffixes on collision.
- Endpoint: `GET /api/stacks/{name}/checks/suggestions` returns `[{check, reason}]` (reason: `published port 8080 → container 80`). Nothing is written until the admin saves the selection: one `POST …/checks` per ticked proposal, done in sequence by the UI, re-using each response's `hash`.

## 7. API

All routes use `requireSession`. Mutating routes need JSON (the existing CSRF rule). Errors are `{error}`, and validation errors are `{error, fields: [{field, message}]}` with field paths prefixed `check.` (for example `check.expectStatus`). Routes are registered only when `Options.Checks` (a `ChecksAPI` interface satisfied by `*checks.Engine` together with the stacks service) is set.

| Route | Behaviour |
|---|---|
| `GET /api/stacks/{name}/checks` | `{hash, checks: [{check, status}], errors: [{id, message}]}`. `status = {state, since, lastRun, lastResult, consecutiveFailures, uptime24h, note}`, where `note` explains `unreachable_from_stackstead` and `idle`. A file that does not parse returns `checks: []` and one error. |
| `GET /api/checks` | `[{stack, check, status}]` for all stacks. Used by `checks list` with no stack. |
| `POST /api/stacks/{name}/checks` | `{check, hash}`, where `check` carries `id`. 409 if the id exists or the hash is stale. 400 on validation. Writes with `SetExtensionEntry`, runs compose validation, and saves through the stacks service's `commit` path under the stack lock, so config history records a `save` revision (no `.bak`; see the config-history spec). Then calls `Engine.Reload`. Returns `{hash, check, status}` with 201. |
| `PUT /api/stacks/{name}/checks/{id}` | Same as POST for an existing id (404 if unknown). The body's `check.id` must equal `{id}`. Renaming is a delete plus a create. |
| `DELETE /api/stacks/{name}/checks/{id}?hash=` | 404 if unknown, 409 if stale. Returns `{hash}`. |
| `POST /api/stacks/{name}/checks/preview` | `{op: "create"\|"update"\|"delete", id, check, hash}` returns `{content, hash}` without writing, as `services/preview` does. |
| `POST /api/stacks/{name}/checks/test` | `{check}` runs once and returns the `Result`. Nothing is stored. 429 within 5 s of the previous test on this stack. 400 on validation. |
| `POST /api/stacks/{name}/checks/{id}/run` | Run now: returns the stored `Result` and the new `status`. 429 when rate-limited. 409 when the check is `paused` or `idle` (the message says why). |
| `GET /api/stacks/{name}/checks/{id}/results?from=&to=&step=` | `{points: [bucket]}`. The default window is 24 h, and `step` defaults to 30 min. Both clamp to the window, and the step is at least `interval`. |
| `GET /api/stacks/{name}/checks/{id}/history?limit=` | The newest raw results (default 20, max 200). |
| `GET /api/stacks/{name}/checks/suggestions` | §6. |

Other wire changes:

- `StackSummary` gains `checks: {up, warning, down, pending}`, filled in `stacks.describe` from the engine's counts. All zeros means the stack has no running checks, and the UI shows `—`.
- `/api/events` gains `checks` messages `{stack, check, state, seq}` on every state transition, from `SubscribeTransitions`, merged like `event`. A drop sends `dropped` and ends the stream, as today.
- `/api/alerts` gains `check_down` (danger, `<check> is down: <message>`, with `stack` and `service`) and `cert_expiring` (warning, `<check>: certificate expires in <n> days`). `alerts.NewEngine` takes a `CheckStates` source. The alert's `subject` is the check id, so its key (`kind|stack|service|container|subject`) is stable per stack and check.
- `/metrics`: `stackstead_check_up{stack,check,type}` (1 up, 0 down, -1 pending, idle, paused or warning from `unreachable_from_stackstead`; a certificate warning reports 1), `stackstead_check_latency_seconds{…}` (last result), and `stackstead_check_cert_expiry_timestamp_seconds{…}` (https only).
- `/api/status` gains `checksDegraded`.

## 8. CLI

`internal/cli/cmd_checks.go`, with client methods `Checks(ctx, stack)`, `AllChecks(ctx)` and `RunCheck(ctx, stack, id)` in `internal/client`.

- `stackstead checks list [stack]` prints a table: `STACK CHECK TYPE TARGET STATE SINCE LATENCY UPTIME24H` (TARGET has its query redacted). It honours the existing `--output json` and exits 0. An unknown stack exits 4.
- `stackstead checks run <stack> <check>` prints state, latency, status code and message. It exits 0 when the result is OK, `ExitOperationFailed` (9) when the check failed, 4 when the check is unknown, 5 when it is paused or idle (409), and 8 when rate-limited.
- Both are documented in `docs/cli.md`, with usage text in the command help.

## 9. UI

All new components live in `web/src/lib/checks/`, and their types in `web/src/lib/api/types.ts`.

- **Checks tab** (`ChecksPanel.svelte`, `?tab=checks&check=<id>`) sits after Metrics, with a badge counting down checks. The toolbar has `Add check` (primary), `Suggest checks` and the file hash notice. It shows a table with one row per check: `StatusBadge` (new tones: up → success, warning → warning, down → danger, pending/idle/paused → muted), id with name underneath, type, target (mono, ellipsis, full text in `title`), latency, `UptimeBar` (48 half-hour cells from `/results`, coloured by `Worst`, with a `title` per cell), and actions (Run now, Edit, Pause/Resume, Remove with a confirm). Expanding a row shows a latency `Sparkline` and the last 20 results. Messages are rendered as text, never as HTML. An `errors` banner lists invalid entries with "Fix in the Compose tab". Empty state: "No checks. Suggest checks from published ports or add one."
- **Check form** (`CheckForm.svelte`, inline panel like `AddServicePanel`) has a type segmented control (HTTP | TCP | DNS) and, for HTTP/TCP, a target segmented control (Address | Service). Service mode offers the stack's services, a port (prefilled from the service's `expose`/ports container side when there is one), and a network select when the service has several. The type-specific fields from §4.2 follow, and then Advanced (interval, timeout, failAfter, enabled, display name). `Test` shows the `Result` inline. `Preview` shows a `DiffView`. `Save` uses the current hash, and a 409 goes through the existing stale-file banner. Server field errors map onto fields, with focus on the first one. When the server returns a `note` (macvlan, macOS), it appears under the target as a persistent hint.
- **Suggestions dialog** (`SuggestDialog.svelte`) is a `Dialog` with a checklist of proposals (reason text, target, type). Ticking a DNS proposal opens it in the form to fill in the name. `Add selected` saves them one by one and reports partial failure per item.
- **Aside block** "Checks": counts (`3 up · 1 down`) and up to 5 non-up checks linking to the tab, or "No checks." when there are none.
- **Service card**: one line, `Checks 2 up · 1 down`, for checks whose `service` is that service, linking to the tab filtered by `service`.
- **Stack list**: a **Checks** cell after Health, styled like the Health cell (green dot + up, red dot + down, amber + warning when > 0), `—` when there are no checks, and a `title` listing the four counts.
- **Refresh**: the tab and aside refetch `/checks` on mount and on `checks` events for this stack (debounced like `change`). History loads when a row expands. There are no new timers. The list page already refetches stacks on `change`, and `checks` events trigger the same refetch.
- **Accessibility**: the uptime bar is `role="img"` with an `aria-label` (`99.3 % up over 24 h`), the segmented controls use `aria-pressed`, and the row expanders use `aria-expanded`. Colour is never the only signal (badges carry text).

## 10. Security

Additions to `docs/security.md`:

- Checks run as the Stackstead user from the host's network. They can reach anything the host can reach except the denied ranges (§5.1), and that rule holds for redirects and DNS rebinding because it is enforced on every dial. Loopback and private networks are allowed on purpose.
- Checks send no credentials: userinfo in URLs is rejected, and there are no custom headers, cookies or proxy. Response bodies are read (at most 64 KiB) only to match `bodyContains` and are never stored or returned. Stored messages redact URL query strings.
- Any signed-in user can see check targets and results, and run or test checks (rate-limited). This is the same trust level as every other action; a session is already root-equivalent.
- Check definitions live in the compose file, so anyone who can edit that file defines what Stackstead dials.
- Abuse limits: 50 checks per stack and 200 in total, a 15 s minimum interval, a 30 s timeout cap, 5 redirects, and a 5 s limit on Run now and Test.

## 11. Testing

Test-first for every behaviour (global rules). Each test below catches a distinct regression.

- **`internal/checks` policy**: `TestDialControlBlocks` covers 169.254.169.254, fe80::1, 224.0.0.1, 0.0.0.0, 255.255.255.255, ::ffff:169.254.169.254 and `::`. `TestDialControlAllows` covers 127.0.0.1, ::1, 10.0.0.5 and 192.168.0.30. `TestRebindingBlocked` uses a fake `Resolver` in the dialer that returns 169.254.169.254 for `evil.test`, and expects class `blocked`.
- **HTTP** (`httptest` servers): `TestHTTPStatusRanges` (`200-399` passes 302 with redirects off; `200,204` fails 201), `TestHTTPBodyContains` (found, missing, substring beyond 64 KiB not found, HEAD rejected by validation), `TestHTTPRedirects` (5 allowed, the 6th gives `redirect_limit`; https→http gives `downgrade`; a redirect to `http://169.254.169.254/` gives `blocked`), `TestHTTPTLS` (`NewTLSServer`, where verification on gives `tls` and off passes; a generated certificate expiring in 3 days with warn 14 gives `warning`; an expired certificate with verification on gives `cert_expired`), `TestHTTPTimeout` (the handler sleeps past the timeout), `TestHTTPIgnoresProxyEnv` (`HTTP_PROXY` points at a recording server, which gets no hits), `TestHTTPNoCredentials` (the server asserts there is no `Authorization` or `Cookie` header and that the User-Agent is set), `TestMessageRedactsQuery`.
- **TCP**: `TestTCPConnect` (a listening port is up; a closed port is `refused`) and `TestTCPTimeout` (a fake `ContextDialer` that blocks).
- **DNS** (fake `Resolver`): `TestDNSExpectAll` (all present passes; one missing fails `answer`), `TestDNSNormalisation` (AAAA `0:0::1` equals `::1`; CNAME trailing dot and case), `TestDNSTXT`, `TestDNSNXDomain` (a `net.DNSError` with `IsNotFound` gives `nxdomain`), `TestDNSEmptyAnswer`.
- **Validation and parsing**: `TestValidateDefinition` is a table (every rule in §4.2, defaults applied, unknown key rejected, durations without units rejected, interval/timeout bounds, https service target without `tlsServerName`). `TestEncodeRoundTrip` checks that `Parse(Encode(d))` equals `d`.
- **Resolution**: `TestResolveTarget` covers one network, several networks with and without `network`, project-prefixed versus external names, macvlan giving `unreachable_from_stackstead`, `goos` darwin, and no running replica giving idle.
- **State and scheduler** (fake clock, fake runner): `TestFailAfterTransitions` (fail, fail → down; success → up; warning on cert), `TestUnreachableIsWarningNotDown`, `TestDockerUnavailableDoesNotCount`, `TestIdleWhenStackStopped`, `TestPaused`, `TestJitterBounds`, `TestConcurrencyCap` (a blocking runner sees at most 4 in flight), `TestRunNowRateLimit`, `TestReloadOnHashChange` (an unchanged hash keeps next-run times), `TestStateRebuiltFromStore`, `TestCheckLimits` (entry 51 of a stack is reported, not run; check 201 across stacks is reported, not run).
- **Store**: `TestBucketsAndUptime`, `TestPruneByRetention`, `TestDeleteStack`, and `TestWriteFailureSetsDegraded` (a closed DB).
- **composefile** (byte-exact goldens): `TestSetExtensionEntryAppendsBlock`, `TestSetExtensionEntryReplacesKeepingNeighbours` (comments on the other entries and the rest of the file are unchanged), `TestRemoveExtensionEntryDropsEmptyBlock`, `TestExtensionAnchorsReadOnly`.
- **Suggestions**: `TestSuggest` is a table: bind IP versus default, ranges and UDP skipped, 53 gives a DNS proposal, 443 gives https, existing checks skipped, id collisions suffixed.
- **config**: `TestChecksRetention` covers the default, YAML, env override and a bare number rejected.
- **api** (`httptest`, real router, fake engine): `TestChecksRoutesRequireSession`, `TestCreateCheck` (201, file content, one new `save` history revision), `TestCreateCheckConflicts` (id exists, stale hash), `TestUpdateCheckIDMismatch`, `TestDeleteCheckUnknown`, `TestCheckValidationFieldPaths`, `TestTestCheckDoesNotStore`, `TestRunNowStates` (429, 409 idle), `TestCheckResultsWindow`, `TestStackSummaryCheckCounts`, `TestAlertsCheckKinds` (in `internal/alerts`), `TestPromCheckGauges` (in `internal/metrics`, against fixed states), `TestEventsChecksMessage`.
- **CLI**: `TestChecksListTable`, `TestChecksListJSON` and `TestChecksRunExitCodes` (0, 9, 4, 5, 8) against a fake server, in the existing CLI test style.
- **Integration (Docker, `//go:build integration`)**: an `nginx:alpine` stack with an unpublished port 80. A service-target HTTP check run now is up. A `down` of the stack turns the check idle.
- **E2E (`TestE2ESmoke`)**: on the smoke stack (publish a port on `web` if it has none), add an HTTP check on the published port, run it now, and expect `up`. Add a TCP check with `failAfter: 1` to a closed loopback port, run it now, and expect `down`, `/api/alerts` containing `check_down`, and `stackstead_check_up 0` on `/metrics`. The compose file still passes `docker compose config`.
- **web (vitest)**: `ChecksPanel` (rows per state, badges, errors banner, run now and its 429 toast), `CheckForm` (fields per type and target mode, server error mapping, Test result, note hint), `SuggestDialog` (selection, DNS proposal opens the form, partial failure), `UptimeBar` (cell colours, label), the aside counts, the service card line and the stack list cell (`—` and the counts).

Acceptance: every test above passes, `go test ./...`, `go vet` and the web suite pass, the bundle budgets hold (the new components are in the lazy detail chunk), and the success criteria in §1 are shown in the E2E smoke.

## 12. Integration points with sibling specs

- **Notifications** consume the `check_down` / `cert_expiring` alert kinds (notifications §6.1, §12), not `SubscribeTransitions()`. This spec sends nothing itself.
- **Image updates** can call `Engine.RunStack(ctx, stack) []Result` (runs every enabled check of the stack now, ignoring the per-check rate limit, once per update) to verify a stack after it is updated.
- **Config history** (`2026-10-18-stackstead-config-history-design.md`) replaces `compose.yaml.bak`. Check create, update and delete save through the same `commit` path as other structured edits, so each one records a `save` revision, and restoring a revision restores its checks (the restore path calls `Engine.Reload(stack)`, as a compose save does). Preview does not write and records nothing. Test and Run now never touch the file.
- **Richer compose forms** must keep the `x-stackstead-checks` block. `composefile` edits already preserve unknown top-level keys.
- **Volume diagnostics** and **images/storage inventory** have no interaction with this spec.

## 13. Documentation

- `docs/api.md`: all §7 routes, states and error classes, the `checks` event message, the alert kinds, the Prometheus gauges, the storage sizing note, and where checks run from with the macvlan, macOS and in-container limits.
- `docs/security.md`: §10.
- `docs/cli.md`: §8.
- README feature list: "Reachability checks: HTTP, TCP and DNS checks with history and alerts".

## 14. Delivery

- **Plan R1 — backend**: `internal/checks`, composefile extension entries, migration, config key, API routes, summary counts, events, alerts, Prometheus, CLI, and the Go, integration and E2E tests and docs for those.
- **Plan R2 — UI**: everything in §9 and its Vitest tests. It starts once R1's API types are merged, or against fixtures in parallel.

## 15. Open questions

None. The check ceiling (200 total, 50 per stack), time-only retention, and DNS through the system resolver only (with the workaround in §2) were confirmed by the user.
