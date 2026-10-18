# Stackstead Notifications — Design

**Date:** 2026-10-18
**Status:** Draft, awaiting review. Adds phone notifications for unhealthy services, restart loops, failed operations and low disk space, delivered to Telegram or a generic webhook. Builds on `2026-10-14-stackstead-v1-design.md` (which put alerts and notifications out of v1) and `2026-10-15-stackstead-ui-v2-design.md` (§10.4 the derived `/api/alerts`, §10.6 the `event` stream). Implemented by one plan (number assigned when the plan is written).

## 1. Purpose

The dashboard already knows when something is wrong. `internal/alerts` derives `unhealthy`, `restart_loop` and `docker_disconnected` from state the server holds, and `internal/host` knows free disk space. Nobody learns about it unless a browser tab is open on the stack list. A homelab operator wants their phone to buzz when Jellyfin stays unhealthy, when Loki is crash-looping, when a 3 a.m. `pull` failed, or when the disk is nearly full. They also want a second message once it is fixed.

Success criteria:

- With one channel configured and nothing else changed, a container that turns `unhealthy` produces exactly one notification once the grace period passes. Once it has stayed healthy for the recovery hold, exactly one recovery notification follows.
- A crash-looping container produces one notification, not one per restart.
- A compose operation that ends `failed` or `timed_out` produces one notification naming the stack, the action and the error. `cancelled` and `succeeded` produce none.
- Low free space on a watched filesystem produces one notification and, later, one recovery.
- Restarting Stackstead while problems are active does not send them again. A problem that cleared while the server was down gets its recovery message.
- A healthcheck that flips more than 4 times in 30 minutes produces one "flapping" message, then silence until it has been stable for 30 minutes.
- Warning-level messages arrive silently. Danger-level ones ring.
- A per-service override (for example "mute `unhealthy` for `media/sidecar`") takes effect at the next evaluation. The stack's Notifications tab shows the effective value and where it comes from.
- **Send test** reaches the phone, or shows the channel's error inline within 15 s.
- A failing channel never blocks detection, the API or shutdown. Its last error is visible in Settings and on the dashboard, and the remaining healthy channels are told once (rate-limited).
- No token, secret or secret-bearing URL is ever returned by the API, logged, or included in an error message.
- When no channel is enabled, the feature costs nothing: no goroutine wakes and no Docker call is made on its behalf.

## 2. Scope

### In
- `internal/notify` (new): detector, incident state machine with grace, recovery hold and flapping, rule resolution, dispatcher and channel workers, Telegram and webhook senders, channel-failure relay, persistence.
- Any number of channels, each with its own minimum severity. Optional HMAC-SHA256 signing and custom headers for webhooks. An optional public URL for links in messages. A Telegram **Find chat** helper.
- `internal/alerts`: new kind `disk_low` (filesystems of `stacks_dir`, `data_dir` and the Docker root). The restart threshold becomes a parameter resolved per container instead of the `restartLoopThreshold` constant. `Alert.Key()` is exported.
- `internal/stacks`: an operation-finished hook on `Ops`, and deleting a stack's notification rules when the stack is removed.
- `internal/store`: one migration (four tables).
- API under `/api/notifications/*` plus `GET /api/stacks/{name}/notifications` (§7).
- UI: a **Notifications** card in Settings for channels and global defaults, and a **Notifications** tab on the stack detail page for overrides (§8).
- CLI: `stackstead settings notifications status|test`, matching the existing `settings metrics-token` commands.
- Docs: `docs/api.md`, `docs/security.md`, `docs/cli.md`, README feature line.

### Out
- ntfy, Gotify, Pushover, email/SMTP and Apprise-style multi-service backends (user decision: Telegram and a generic webhook only). The webhook reaches anything that accepts an HTTP POST, including a self-run bridge.
- A faster restart-loop detector. Restart loops reuse the dashboard's 24 h counter (§6.2).
- A rule for `exited` containers. Only the kinds in §6.1 notify.
- `port_clash` and `override` notifications. They stay on the dashboard only.
- Quiet hours and digests (user decision: silent delivery for warnings plus the phone's do-not-disturb).
- Repeat reminders for alerts that stay open.
- User-chosen extra disk paths (media or backup mounts).
- Acknowledging alerts from the phone, escalation, multi-user routing.
- CPU and memory threshold alerts (use Prometheus and Alertmanager against `/metrics`).
- Sibling features' detection logic. Their alert kinds (`mount_problem`, `check_down`, `cert_expiring`) are notified (§6.1), but the conditions themselves are defined in their own specs (§12).

## 3. Key decisions

The TypeSafe skill is an SDK for an external judgment API and cannot run here without network access and credentials, so these decisions are argued in writing, as the earlier specs did. Decisions marked *(user)* were put to the user, with options and trade-offs.

| Decision | Choice | Reason |
|---|---|---|
| Channels *(user)* | Telegram Bot API and a generic JSON webhook | Telegram is free, needs no self-hosted server and has good phone apps. The webhook covers everything else (Home Assistant, n8n, a self-run ntfy or Gotify through their HTTP APIs, Slack-compatible endpoints). Both are plain `net/http` POSTs, so no new dependency. |
| Secret storage *(user)* | Plaintext in SQLite, write-only through the API; the API shows `configured` and the last 4 characters | The database lives in the 0700 state directory (`StateDirectoryMode=0700`, `MkdirAll(DataDir, 0o700)`). Anyone who can read it already has the `stackstead` user, which is in the `docker` group and therefore root-equivalent. Encrypting with a key file next to it adds code and key-loss failure modes without protecting against that reader. `stackstead db backup` copies include the secrets; `docs/security.md` says so. |
| Notifiable kinds *(user)* | Default on: `unhealthy`, `restart_loop`, `op_failed`, `disk_low`, `docker_disconnected`, plus the sibling kinds `mount_problem`, `check_down`, `cert_expiring` (§12). Opt-in: `metrics_degraded`, `parse_error`. Never: `port_clash`, `override` | Covers "something broke while I was away". When Docker is unreachable every other rule goes blind, so that has to reach the phone. Port clashes and override files are configuration findings for the dashboard. |
| Rule configurability *(user)* | Every setting can be overridden per stack and per service | User decision. The rule model (§6.4) limits each kind to the scopes where it makes sense: host-level kinds are global only. |
| Precedence *(user)* | Field-level inheritance: service, then stack, then global. Each field is either set or `inherit` | A service can override one field (for example the restart threshold) and inherit the rest, and later global changes still apply to fields it did not override. |
| Rule storage *(user)* | SQLite `notify_rules`, edited through the UI and API only; compose files untouched | Keeps the v1 promise that Stackstead only writes compose files when the user edits services. No `.bak` or stale-hash conflicts for a notification toggle. |
| Rule editor *(user)* | Channels and global defaults in Settings › Notifications; overrides in a **Notifications** tab on the stack page | Overrides live where the operator looks at the stack. The grid shows effective values with their source. |
| Restart-loop detection *(user)* | Reuse the dashboard's counter: restarts in the last 24 h from the host collector's `/events` scan (refreshed when older than 60 s); threshold overridable, default 3 | One definition for the dashboard and the phone. **Accepted cost:** a loop is reported up to about 60 s after it crosses the threshold, and its recovery only arrives once the 24 h window drops below the threshold, which can be up to 24 h after the loop stopped. Messages say "in the last 24h" so this is not misleading. |
| Debounce and flapping *(user)* | Grace before `open`: `unhealthy` 2 min, `docker_disconnected` 1 min, every other kind 0. Recovery hold 2 min for every kind. Flapping: more than 4 open/recover transitions in 30 min sends one `flapping` message and suppresses the incident until it has been stable for 30 min. No reminders | Healthcheck blips and container recreates never reach the phone, every new problem is heard once, and a noisy service costs one message per half hour at most. All six values are rule fields, so they can be overridden per stack and service. |
| Quiet hours *(user)* | None. `danger` events ring; `warning` and `info` events and all recoveries are sent silently (Telegram `disable_notification`, webhook `"silent": true`) | No timezone or scheduling code. The phone's do-not-disturb handles the night. Recoveries are good news and need no sound; the `open` already rang if it mattered. |
| Low disk *(user)* | Filesystems of `stacks_dir`, `data_dir` and the Docker root (`/info` `DockerRootDir`), deduplicated by device. Fires when free < 10 % **or** free < 5 GiB. Recovers only when free ≥ 15 % **and** free ≥ 7 GiB | Docker's root (images, container logs) is the disk that usually fills; the data dir holds SQLite. The 5-point / 2 GiB margin stops a disk hovering at the line from flapping. |
| Channel cardinality *(user)* | Any number of channels, each `telegram` or `webhook`, each with `enabled` and `minSeverity` (`danger` / `warning` / `info`, default `warning`) | For example, the phone gets danger only and a group chat gets everything. |
| Webhook auth *(user)* | Optional HMAC-SHA256 signature over `<timestamp>.<body>` in `X-Stackstead-Signature: sha256=<hex>` with `X-Stackstead-Timestamp`, plus up to 5 custom headers (values write-only) | The receiver can check who sent the request, and the timestamp lets it refuse replays. Custom headers cover `Authorization: Bearer …` receivers. |
| Links *(user)* | Optional `publicUrl` setting; when set, every message carries `<publicUrl>/stacks/<name>` (operations: `?tab=operations&op=<id>`) | Behind a reverse proxy the server cannot know its own external address, and guessing it from a request is wrong for a background sender. |
| Telegram chat id *(user)* | **Find chat** button: the server calls `getUpdates` with the entered token and lists recent chats | Finding a chat id by hand is the usual setup hurdle. The call is read-only and uses the same redaction. |
| Failing channel relay *(user)* | When a channel has been failing for 10 min, the other healthy channels receive one `channel_failing` message; at most one per failing channel per 6 h, plus one `channel_recovered` | With several channels, a silent channel should not go unnoticed. The 10 min delay skips transient blips, and the 6 h cap keeps a long outage from spamming. |
| Detection source | The notifier reads the same `alerts.Engine.Alerts()` the dashboard reads, plus the new `Ops` hook for failed operations | No parallel rule code. Phone and dashboard cannot disagree about whether something is wrong. `disk_low` is added to `alerts` so the dashboard shows it too. |
| Thresholds in `alerts` | `alerts.Evaluate` takes a `Thresholds` resolver; the dashboard and notifier use the same resolver | A per-service restart threshold changes both views, keeping "one definition" true under overrides. |
| Evaluation cadence | Background loop: every 30 s, plus an early wake (debounced 2 s) on `die`, `oom` and `health_status` events from `state.Cache.SubscribeEvents` and on `Ops` finish | 30 s bounds detection latency without measurable cost (the alerts computation is in-memory except the cached restart scan). Event wakes make unhealthy and failure reports near-immediate once their grace has passed. |
| Running only when needed | The detector goroutine starts when at least one channel is enabled and stops when the last is disabled or deleted | Keeps the UI v2 rule "costs nothing while nobody is looking". |
| Incident persistence | Open incidents in SQLite (`notify_incidents`), keyed by the alert key | Required for "restart does not resend, cleared-while-down gets a recovery". |
| Delivery history | The last delivery result per channel lives in the channel row; the last 50 deliveries are kept in an in-memory ring for Settings | Answers "is it working?" without another growing table. Incidents are what must survive a restart, and they do. |
| Message format | Plain text (Telegram without `parse_mode`) | Stack, service and error text are user-controlled. Plain text needs no escaping and cannot inject markup or links. |
| Failed operations | One-shot events (no recovery message), keyed by operation id; `failed` and `timed_out` only | An operation has no "healthy again" state. A cancel is a deliberate user action. |
| CLI | `stackstead settings notifications status` and `… test [--channel ID]` | Same shape as `settings metrics-token`. Channel and rule editing stays in the UI and API (JSON bodies are clumsy as flags). |

## 4. Architecture

```
                 ┌───────────── internal/notify ─────────────┐
alerts.Engine ──►│ Detector (30 s tick + event wake)         │
 (uses host      │   resolve rules (Resolver)                │
  collector:     │   incident FSM ──► notify_incidents       │──► Dispatcher ──► worker per channel ──► Telegram / webhook
  restarts,disk) │                                           │         (bounded queue, retry, redaction)
state.Cache  ───►│ wake on die/oom/health_status             │
stacks.Ops   ───►│ OnFinish(failed|timed_out) ──► one-shot   │
                 └───────────────────────────────────────────┘
```

### 4.1 Packages and types (shapes only)

`internal/notify`:

```go
type Kind string // "unhealthy" | "restart_loop" | "op_failed" | "disk_low" | "docker_disconnected" | "metrics_degraded" | "parse_error" | "mount_problem" | "check_down" | "cert_expiring"

type Event struct {           // what a channel sends
    Type      string          // "open" | "recovered" | "flapping" | "channel_failing" | "channel_recovered" | "test"
    Kind      Kind
    Severity  string          // alerts.SeverityDanger | Warning | Info
    Stack, Service, Container string
    Message   string
    Since, At time.Time
    Silent    bool            // §6.6
    URL       string          // "" unless publicUrl is set
}

type Sender interface {
    Send(ctx context.Context, ev Event) error // returns *DeliveryError
}

type DeliveryError struct {
    Permanent  bool          // 4xx except 408/429; bad config
    RetryAfter time.Duration // from 429
    Msg        string        // already redacted
}

type Service struct { /* store, alerts, ops hook, cache, clock, logger */ }
func New(deps Deps) *Service
func (s *Service) Run(ctx context.Context)                  // started by startBackground; idles until a channel is enabled
func (s *Service) OperationFinished(op stacks.Operation)    // registered via Ops.OnFinish
func (s *Service) Test(ctx context.Context, ch ChannelInput) error
func (s *Service) TelegramChats(ctx context.Context, botToken string) ([]TelegramChat, error)
func (s *Service) Status() Status
```

Files: `notify/service.go` (lifecycle, enable/disable), `detector.go` (tick, wake, diff), `incident.go` (FSM, pure, clock injected), `rules.go` (`Resolver`, merge), `store.go` (SQL), `dispatch.go` (queues, retry), `telegram.go`, `webhook.go`, `redact.go`, `message.go` (text rendering).

Changes elsewhere:
- `engine.Info` gains `DockerRootDir string` (already in the `/info` body; volume diagnostics §4.1 adds the same field, and whichever plan lands first adds it).
- `alerts`: `KindDiskLow = "disk_low"`. `type Thresholds interface { RestartLoop(stack, service string) int; Disk() DiskThreshold }`. `NewEngine` gains `Disk DiskSource` and `Thresholds`. `func (a Alert) Key() string` is the existing `kind|stack|service|container|subject` key (`subject` moves onto `Alert` as an unexported-but-keyed field, or `Key` is carried alongside; the plan picks whichever keeps `/api/alerts` JSON unchanged).
- `stacks.Ops`: `func (o *Ops) OnFinish(fn func(Operation))`, called from `finish` after the state is recorded, outside `o.mu` (in a goroutine-free call that must not block; `notify` only enqueues).
- `stacks.Service.Remove`: deletes `notify_rules` rows for the stack through a small `RuleCleaner` interface (nil-safe).
- `cli/serve.go`: build `notify.New(...)`, `ops.OnFinish(notifier.OperationFinished)`, add `notifier.Run` to `startBackground`, pass it to `api.Options.Notify`.
- `cli/cmd_db.go`: add the four tables to `dbCountTables`.

### 4.2 Detector loop

1. Disabled (no enabled channel): block on a `reconfigure` channel. No timer.
2. Enabled: every 30 s, or 2 s after a wake (`die`, `oom`, `health_status` events, operation finished, rules or channels changed), call `alerts.Engine.Alerts(ctx)`. The 5 s cache and single flight are shared with dashboard requests.
3. Filter to notifiable kinds, resolve each alert's effective rule (§6.4), and drop disabled ones.
4. Feed the set into the incident FSM (§6.3) with `now`. It returns transitions (`open`, `recovered`, `flapping`), which are persisted first and then enqueued to every eligible channel.
5. Gating: while `docker_disconnected` is open, or the container listing failed, container-derived incidents are frozen, never closed. `restart_loop` incidents are not judged until the collector's first restart scan exists (`RestartCounts() != nil`). This avoids false recoveries after startup or a Docker blip; `alerts.Engine` already keeps container-derived alerts' `since` across listing failures.

Startup: open incidents are loaded from SQLite. The first evaluation that passes the gating treats an incident missing from the current set as "clearing", so a problem that ended while Stackstead was down gets its recovery after the normal hold. Incidents whose open message was never accepted by any channel (`notified_at IS NULL`) are re-enqueued.

## 5. Channels

### 5.1 Telegram

- Config: `botToken` (secret), `chatId` (string: a numeric id or `@channelname`).
- Request: `POST https://api.telegram.org/bot<token>/sendMessage`, JSON `{chat_id, text, disable_notification, link_preview_options: {is_disabled: true}}`. `disable_notification` is `Event.Silent` (§6.6).
- Response handling: `{"ok":true}` means delivered. 429 sets `RetryAfter` from `parameters.retry_after`. 401 is permanent ("bot token rejected"). 400 with `chat not found` or `bot was blocked` is permanent and shows Telegram's `description`, truncated to 200 characters. 5xx and network errors are retried.
- The token is part of the URL path, and Go's `*url.Error` prints the full URL. Every error from this sender goes through `redact` (§10) before it is returned, stored or logged. A test asserts this.
- Text (≤ 4096 characters, truncated with `…`), for example:
  `🔴 media/jellyfin unhealthy` / `jellyfin is unhealthy` / `since 14:02 (5m)` / `https://stackstead.lan/stacks/media` (only with `publicUrl`). Recovery: `✅ media/jellyfin recovered after 12m`. Flapping: `🟠 media/jellyfin is flapping (5 changes in 30m); muted until stable`. Times are rendered in the server's local zone.
- **Find chat**: `GET https://api.telegram.org/bot<token>/getUpdates?allowed_updates=["message","channel_post","my_chat_member"]`, with no `offset`, so nothing is confirmed and the user's own bot use is not disturbed. Returns the distinct chats seen as `TelegramChat{ID int64; Type, Title string}` (`Title` = title, or `@username`, or first name). An empty list comes with the hint "Send any message to the bot (or add it to the group), then try again"; Telegram keeps updates for 24 h. A 409 (the bot has a webhook set) is reported as "this bot uses a webhook; enter the chat id by hand".

### 5.2 Generic webhook

- Config: `url` (secret, because tokens often live in it), `method` fixed to `POST`. Optional `signingSecret` (secret, 16–256 characters). Optional `headers`: up to 5 `{name, value}`. Names are RFC 7230 tokens and must not be `Host`, `Content-Length`, `Content-Type`, `User-Agent` or `X-Stackstead-*`. Values are secret (write-only, shown as `configured · …last4`).
- Signing, when `signingSecret` is set: `X-Stackstead-Timestamp: <unix seconds>` and `X-Stackstead-Signature: sha256=<hex(HMAC-SHA256(secret, timestamp + "." + body))>`. `docs/api.md` documents how to verify it (constant-time compare, reject timestamps more than 5 min old).
- Body (`Content-Type: application/json`, `User-Agent: stackstead/<version>`):

  ```json
  {"version":1,"type":"open","kind":"unhealthy","severity":"danger","stack":"media","service":"jellyfin",
   "container":"media-jellyfin-1","message":"jellyfin is unhealthy","since":"2026-10-18T14:02:00Z","at":"2026-10-18T14:04:00Z",
   "silent":false,"url":null,"channel":null}
  ```
  Omitted fields are `""` or `null`. `channel` is the failing channel's name on `channel_failing` and `channel_recovered`. `version` is bumped on any incompatible change.
- 2xx means delivered. 408, 429 (honouring `Retry-After` up to 15 min) and 5xx are retried. Other 4xx are permanent. Response bodies are read up to 4 KiB and never stored. Redirects are not followed (`CheckRedirect` returns `http.ErrUseLastResponse`, so a 3xx counts as a permanent "redirect not followed" error).
- `http` and `https` are allowed (LAN receivers often have no TLS). The Settings form warns when the URL is `http` and not loopback or RFC 1918.

### 5.3 Channel model

Any number of channels (at most 20). A channel has `id`, `name` (1–40 characters, unique), `type`, `enabled`, `minSeverity`, `config` (non-secret fields), `secrets` (write-only), `createdAt` and delivery status (`lastOkAt`, `lastErrorAt`, `lastError`, `failingSince`).

Routing: an `open` or `flapping` event goes to every enabled channel whose `minSeverity` the event's severity meets (`danger` ≥ `warning` ≥ `info`). A `recovered` event goes to exactly the channels that accepted the matching `open`, recorded per incident in `notify_incident_channels`, so a channel whose severity floor was raised in between still hears the recovery. `test` goes only to the channel being tested. `channel_failing` and `channel_recovered` are `warning`, go to every other enabled channel that is not itself failing, and ignore `minSeverity`, because a missing channel matters to anyone listening.

### 5.4 Storage (one appended migration; number assigned at merge)

```sql
notify_channels(id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL, type TEXT NOT NULL CHECK (type IN ('telegram','webhook')),
  enabled INTEGER NOT NULL, min_severity TEXT NOT NULL, config TEXT NOT NULL /* JSON */, secrets TEXT NOT NULL /* JSON */,
  created_at INTEGER NOT NULL, last_ok_at INTEGER, last_error_at INTEGER, last_error TEXT, failing_since INTEGER, relayed_at INTEGER)
notify_rules(level TEXT NOT NULL, stack TEXT NOT NULL DEFAULT '', service TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL,
  fields TEXT NOT NULL /* JSON RuleFields */, PRIMARY KEY (level, stack, service, kind))
notify_incidents(key TEXT PRIMARY KEY, kind TEXT NOT NULL, severity TEXT NOT NULL, stack TEXT, service TEXT, container TEXT,
  message TEXT NOT NULL, state TEXT NOT NULL, first_seen INTEGER NOT NULL, opened_at INTEGER, changed_at INTEGER NOT NULL,
  notified_at INTEGER, flap_changes TEXT NOT NULL DEFAULT '[]' /* unix times in the last 30 min */, muted_until INTEGER)
notify_incident_channels(incident_key TEXT REFERENCES notify_incidents(key) ON DELETE CASCADE, channel_id INTEGER
  REFERENCES notify_channels(id) ON DELETE CASCADE, PRIMARY KEY (incident_key, channel_id))
```

`publicUrl` is the `settings` row `notify.public_url`. Closed incidents are deleted (one-shot `op_failed` events are never stored as incidents).

## 6. Rules and detection

### 6.1 Kinds

| Kind | Source | Severity | Scopes | Default | Recovery |
|---|---|---|---|---|---|
| `unhealthy` | `alerts` (container health `unhealthy`) | danger | global, stack, service | on | yes |
| `restart_loop` | `alerts` (24 h restart count ≥ threshold) | warning | global, stack, service | on, threshold 3 | yes, when the 24 h count drops below the threshold (up to 24 h, §3) |
| `op_failed` | `Ops.OnFinish`, state `failed` or `timed_out` | danger | global, stack | on | no (one-shot) |
| `disk_low` | `alerts` (new, from the disk source) | danger | global | on | yes, with hysteresis (§6.5) |
| `docker_disconnected` | `alerts` | danger | global | on | yes |
| `metrics_degraded` | `alerts` | warning | global | off | yes |
| `parse_error` | `alerts` | warning | global, stack | off | yes |
| `mount_problem` | `alerts` (volume diagnostics' danger findings except `low_space`, one per finding; low disk is `disk_low`'s job) | danger | global, stack, service | on | yes, when the next mount scan no longer reports the finding |
| `check_down` | `alerts` (reachability: check `down` after `failAfter` failures) | danger | global, stack, service (a check without a service uses the stack rule) | on | yes, on the check's next success plus the hold |
| `cert_expiring` | `alerts` (reachability: certificate inside the check's warning window) | warning | global, stack, service | on | yes, when the certificate is renewed |

Containers outside registered stacks (`unhealthy` and `restart_loop` alerts with no `stack`) use the global rule. `op_failed` messages: `<stack>: docker compose <action> failed (exit <code>): <first line of op.Error>` or `… timed out after <duration>`.

### 6.2 Restart loop

The rule is `alerts`' existing one, with the threshold resolved per container: `restarts24h(container) ≥ RestartLoop(stack, service)`. The count is `host.Collector.RestartCounts()`: a `/events` scan over the last 24 h counting `max(0, starts − 1)` per container id, served from a sub-cache refreshed when older than 60 s. The notifier's 30 s tick keeps that scan warm while notifications are enabled. That is one `/events` request per minute, the same cost the dashboard already accepts. Latency and recovery as in §3. A recreated container gets a new id, so its count restarts. The alert key uses names, so the incident stays the same one.

### 6.3 Debounce and flapping

The incident FSM is a pure function `Step(inc Incident, active bool, rule Effective, now time.Time) (Incident, []Transition)` with states `pending → open → clearing → closed`, plus `muted` for flapping:
- `pending` turns `open` when active for at least `rule.Grace`, and only then is `open` sent.
- `open` turns `clearing` when the alert is absent. `clearing` turns `closed` (`recovered` sent) after `rule.RecoveryHold`, or back to `open` without a message if the alert returns.
- An incident closed while still `pending` sends nothing.
- Flapping: every sent `open` and `recovered` appends `now` to `flap_changes`, pruned to `rule.FlapWindow`. When the count would exceed `rule.FlapCount`, the incident instead sends one `flapping` event and enters `muted`. A muted incident sends nothing. It leaves `muted` once its active/absent state has not changed for `rule.FlapWindow`, then sends its current state once: `open` if still active, `recovered` if it was open before muting and is absent now. If it was never opened, it closes silently.
- No reminders: an open incident sends nothing more until it changes.

| Field | `unhealthy` | `restart_loop` | `disk_low` | `docker_disconnected` | `metrics_degraded`, `parse_error`, `mount_problem`, `check_down`, `cert_expiring` |
|---|---|---|---|---|---|
| `Grace` | 2 min | 0 | 0 | 1 min | 0 |
| `RecoveryHold` | 2 min | 2 min | 2 min | 2 min | 2 min |
| `FlapCount` / `FlapWindow` | 4 / 30 min | same | same | same | same |

`check_down` gets no grace because the reachability spec's `failAfter` (default 2 consecutive failures) already debounces it. `mount_problem` and `cert_expiring` come from periodic scans, so their state changes slowly by nature. `mount_problem` and `check_down` count as container-derived for the gating in §4.2: they freeze while Docker is unreachable, because a check against a service target and a mount scan both depend on Docker.

Because evaluation runs every 30 s (plus wakes), effective grace and hold are rounded up to the next evaluation. Tests use the injected clock. `op_failed` bypasses the FSM: it has no grace, hold or flapping, and repeated failures of the same stack and action within 10 min are collapsed into one message (`… failed again (3× in 10m)` on the next one after the window), so a retry loop from the CLI cannot spam.

### 6.4 Rule model and precedence

```go
type RuleFields struct {        // nil = inherit
    Enabled          *bool
    Grace            *Duration  // 0..1h; every FSM kind
    RecoveryHold     *Duration  // 0..1h
    FlapCount        *int       // 2..50
    FlapWindow       *Duration  // 5m..6h
    RestartThreshold *int       // restart_loop only, 1..100
    DiskFreePercent  *float64   // disk_low only, 1..50 (default 10)
    DiskFreeBytes    *uint64    // disk_low only, ≥ 1 GiB (default 5 GiB)
}
type Scope struct{ Level string; Stack, Service string } // "global" | "stack" | "service"

func Resolve(global, stack, service RuleFields, kind Kind) Effective // field by field: service, then stack, then global, then built-in default
type Effective struct {
    Enabled bool; Grace, RecoveryHold, FlapWindow time.Duration; FlapCount, RestartThreshold int
    DiskFreePercent float64; DiskFreeBytes uint64
    Source map[string]string // field -> "service" | "stack" | "global" | "default"
}
```

- Built-in defaults are code constants. A global row stores only what the user changed, so "Reset to default" deletes the field.
- Setting a field the kind does not support, or a scope the kind does not allow (§6.1), is a 422 `{field, message}`.
- Orphans: a `service` row whose service is no longer in the stack's compose file stays stored and is reported with `orphaned: true`, so the tab can offer **Remove**. It applies again if the service returns. Stack removal deletes the stack's rows. A renamed stack keeps nothing (rows are keyed by name, and rename is remove plus import).

### 6.5 Low disk

- `alerts` gains `DiskSource interface{ Filesystems(ctx context.Context) []FSUsage }` with `FSUsage{Paths []string; Free, Total uint64}`, implemented in `internal/host` (`host.DiskWatcher`) with the existing `Statfser` plus an `os.Stat` device lookup.
- Paths: `stacks_dir`, `data_dir`, and `DockerRootDir` from `engine.Info`. The root dir is cached for 10 min and is absent while Docker is unreachable or when `/info` has no value. Paths are grouped by device id (`st_dev`), so filesystems shared between paths produce one alert listing all their paths. A path whose `Statfs` fails is skipped and logged once per process. For example, Docker Desktop's root is inside its VM, so on macOS only the local paths are watched.
- Thresholds: global `disk_low` rule. **Fire** when `free/total < DiskFreePercent` **or** `free < DiskFreeBytes`. **Clear** only when `free/total ≥ DiskFreePercent + 5` **and** `free ≥ DiskFreeBytes + 2 GiB`. Between the two lines, the alert keeps its previous state. `alerts.Engine` keeps that last state per device, so the dashboard shows the same hysteresis.
- `Filesystems` is cached for 30 s (one `Statfs` per path per 30 s). The dashboard's `/api/host` `disk` tile is unchanged.
- Message: `low disk on <paths>: <free> free of <total> (<pct>%)`, using the existing byte formatter.

### 6.6 Quiet hours and silent delivery

No quiet hours. `Event.Silent` is true for `warning` and `info` events (including `flapping` of a warning kind, and `channel_failing`) and for every `recovered` and `channel_recovered` event. `danger` opens and `test` events ring; a test rings so the user can confirm the sound works. Telegram sends silent events with `disable_notification: true`. The webhook carries `"silent"` for the receiver to honour. The phone's own do-not-disturb covers the night.

## 7. API

All routes use `requireSession`, JSON bodies and `{error}` or `{field, message}[]` (422) errors, like the rest of `/api`. They exist only when `api.Options.Notify` is set.

| Route | Body / response |
|---|---|
| `GET /api/notifications/channels` | `[{id, name, type, enabled, minSeverity, config, secrets: {botToken: {configured: true, hint: "…x9Qa"}}, lastOkAt, lastErrorAt, lastError, failingSince}]`. Webhook `config.headers` lists names only; values appear under `secrets.headers.<name>`. |
| `POST /api/notifications/channels` | `{name, type, enabled, minSeverity, config, secrets}` → 201 channel. 422 on validation (unknown type, more than 20 channels, duplicate name, bad URL, bad header name, more than 5 headers). |
| `PUT /api/notifications/channels/{id}` | Same body. A secret omitted or `null` keeps the stored value, and a string replaces it. `""` clears an optional secret (`signingSecret`, a header value) and is a 422 for a required one (`botToken`, `url`). Removing a header from `config.headers` drops its value. → 200 |
| `DELETE /api/notifications/channels/{id}` | 204. Open incidents stay; other channels still get their recoveries. |
| `POST /api/notifications/test` | `{channelId?, name?, type, config, secrets}`. Sends a `test` event using the body, filling omitted secrets from `channelId`, so an unsaved form can be tested. 200 `{ok: true}`, or 502 `{error}` with the redacted channel error. 15 s timeout, no retries. Rate-limited to 1 per 5 s per session (429). |
| `POST /api/notifications/telegram/chats` | `{botToken?, channelId?}` (token from the body, or the stored one) → 200 `[{id, type, title}]`, or 502 `{error}` (redacted). Shares the test rate limit. |
| `GET`/`PUT /api/notifications/settings` | `{publicUrl}`. `""` clears it. It must be an absolute `http(s)` URL without query, fragment or userinfo; a trailing `/` is trimmed. 422 otherwise. |
| `GET /api/notifications/rules` | `{defaults: {kind: Effective}, global: {kind: RuleFields}}` |
| `PUT /api/notifications/rules/global/{kind}` | `RuleFields` (only the fields being set; `null` = inherit) → 200 |
| `GET /api/stacks/{name}/notifications` | `{stack: {kind: {fields, effective}}, services: {svc: {kind: {fields, effective, orphaned}}}}` for the tab |
| `PUT`/`DELETE /api/stacks/{name}/notifications/{kind}` | stack-level fields / clear all |
| `PUT`/`DELETE /api/stacks/{name}/services/{service}/notifications/{kind}` | service-level fields / clear all |
| `GET /api/notifications/status` | `{enabled, channels: [{id, name, queued, dropped, lastOkAt, lastError, failingSince}], incidents: [{key, kind, stack, service, state, openedAt, mutedUntil}], recent: [{at, channelId, type, kind, stack, ok, error}]}` (50 newest, in memory) |

Rule changes wake the detector, so they apply within about 2 s. `apitypes` gains the matching types, and `internal/client` gains `NotificationsStatus` and `NotificationsTest` for the CLI.

## 8. UI

### 8.1 Settings › Notifications card

Placed after Prometheus token. Header badge: `on · N channels` (`--success`) or `off` (`--muted`).
- **Public URL**: one mono input, "Used for links in messages, e.g. https://stackstead.lan. Leave empty for no links.", plus **Save**.
- **Channels**: one row per channel with the type icon, mono name, a `minSeverity` chip (`danger+`, `warning+`, `all`), enabled toggle, last result (`ok · 3m ago` in `--success`, or `--danger` with the error in `title` and an expandable line), and **Send test**, **Edit** and **Delete** (ConfirmDialog) buttons. **Add channel** opens a `Dialog` with a segmented Telegram | Webhook control, name, a "Send" select (`Danger only`, `Warnings and above` (default), `Everything`), and the type's fields.
  - Telegram: Bot token, then Chat id with a **Find chat** secondary button. It calls the chats route with the entered (or stored) token and shows the chats as a radio list; picking one fills Chat id. Empty and error states use the hints in §5.1.
  - Webhook: URL, an optional Signing secret with a **Generate** button (32 random bytes, base64url, generated client-side with `crypto.getRandomValues` and shown until saved so it can be copied to the receiver), and a `ListEditor` of up to 5 headers (name, value). Secret inputs are `type=password`, show `configured · …x9Qa` with a **Replace** link when editing, and are never pre-filled. The dialog has **Send test** (unsaved values) and **Save**.
- **Defaults**: a table with one row per kind (§6.1) and columns Enabled (toggle), Grace, Recovery, Flapping (`4 in 30m`), Threshold (restart count, or `10% · 5 GiB` for disk), each showing the effective value and editable inline. A field changed from the default shows a small `Reset` ghost button. Kinds marked opt-in show "off by default".
- **Recent deliveries**: a collapsed `<details>` with the in-memory ring from `/status`.
- Telegram help text: "Create a bot with @BotFather, send it a message, then press Find chat." A note says Telegram receives stack and service names.

### 8.2 Stack detail › Notifications tab

A new tab after Operations, with a badge showing the number of overrides on the stack. The grid has rows for the stack, then each service (mono), with orphaned rows last, flagged `orphaned` plus **Remove**. Columns are the kinds allowed at that scope. Each cell shows the effective state (`on` / `off`, plus threshold or grace), greyed and labelled `inherited from global/stack` when inherited, and in `--text` with an `override` dot when set here. Clicking a cell opens a small popover editor: per field `Inherit (value) | On | Off` or a number or duration input, with **Save** and **Clear override**. While no channel is enabled, a `Banner` (info) reads "Notifications are off. Add a channel in Settings." with a link. Rules can still be edited.

Accessibility: the grid is a `<table>` with row and column headers. Cells are buttons with `aria-label="unhealthy for jellyfin: on, inherited from global"`. The popover is a `Dialog` with focus trap. Below 720 px the grid becomes one card per row (the existing table-to-cards technique).

### 8.3 Dashboard

`AlertsCard` gains `disk_low` and `notify_failing` (warning: `Telegram "phone" failing: <redacted error>`, raised from the notifier's status through the alerts engine's existing source interfaces). No other dashboard change.

## 9. Failure handling

- **Per-channel worker** with a bounded queue (64 events). When full, the oldest `recovered` event is dropped first, then the oldest of any type. `dropped` is counted in status.
- **Retries**: up to 4 attempts with backoff 10 s, 60 s, 5 min (or `RetryAfter`, capped at 15 min). Each attempt has a 10 s timeout. Permanent errors stop immediately. The final outcome updates `lastOkAt` or `lastError*` on the channel row.
- **Failing channel**: a channel is *failing* from the first final failure (after retries) until the next success (`failing_since`).
  - The dashboard shows `notify_failing` for it as soon as it is failing.
  - After 10 min of failing, the other enabled, non-failing channels get one `channel_failing` event (`Telegram "phone" has failed for 10m: <redacted error>`). `relayed_at` limits this to once per channel per 6 h. Both timings are fixed constants (user decision), not rule fields.
  - When it succeeds again after a relay was sent, those channels get one `channel_recovered`.
  - With a single channel, or when all channels fail together (usually the host's network is down), nothing is relayed. The dashboard alert is the only signal.
  - Relay events are never relayed again (no loops).
- **Incident bookkeeping**: transitions are written before enqueueing. `notified_at` is set when any channel accepts the `open` event, so an open that no channel accepted before shutdown is re-sent at startup.
- **SQLite write failure**: the transition is not enqueued (no message without a record, which avoids repeats after restart). The error is logged and retried on the next tick.
- **Docker unavailable**: `docker_disconnected` opens after its grace. Container incidents are frozen (§4.2) and resume being judged after reconnect.
- **Shutdown**: `Run` returns when the background context is cancelled. In-flight HTTP requests are cancelled. Queued events are discarded, but incidents are persisted, so recovery is correct after restart. Shutdown never waits on a slow receiver beyond the existing `background.stop(grace)`.
- **Clock**: all timing uses the injected `now` and monotonic durations, so a wall-clock jump does not open or close incidents.

## 10. Security

- Stored secrets: Telegram `botToken`, the webhook `url`, `signingSecret` and header values. Never returned (only `configured` and the last 4 characters), never logged, never in `/api/notifications/status`.
- `redact.go`: `Redact(msg string, secrets ...string) string` replaces every secret (and its URL-escaped form) with `[redacted]`. It is applied to every `DeliveryError.Msg` and every log line from senders. Senders build errors only through it. A unit test with a fake server echoing the URL asserts no secret appears in the error, in status or in logs.
- Outbound requests: no redirects, 4 KiB response cap, 10 s timeout, `User-Agent: stackstead/<version>`, no cookies or proxy credentials beyond the environment's `HTTPS_PROXY`. Webhook URLs may point at the LAN or loopback. That is intended, and `docs/security.md` notes that any signed-in user (already root-equivalent) can make the server send requests to arbitrary URLs.
- Message content: names, states, exit codes and the first line of an operation error. Never log lines, environment, compose content or paths beyond the stack name. Plain text only.
- Webhook signing uses `crypto/hmac` with SHA-256. The secret never leaves the server after it is saved. The timestamp in the signed string lets receivers reject replays. The `publicUrl` only builds links; the server never fetches it.
- **Find chat** sends the bot token only to `api.telegram.org`, never to a host the user supplies. The response chat list is returned to the signed-in user only and is not stored.
- Telegram receives stack and service names and error summaries. This is a third party, and the Settings card says so.
- CSRF: unchanged model (SameSite=Strict cookie plus required `application/json`). The test-send rate limit keeps a stolen session from using the server as a spam relay at volume.
- `systemd` unit: no change needed. Outbound network is not restricted today.

## 11. Testing strategy

Test-first throughout (user rules). Only tests that catch a distinct regression are added.

**Go unit (no Docker):**
- `notify/incident_test.go`: `TestStep` table: grace not reached gives no open; grace reached gives one open; absent then back within the hold gives no message; absent past the hold gives recovered; closed while pending gives nothing; the fifth change in 30 min sends `flapping` and mutes; muted and stable for 30 min sends the current state once; muted and never opened closes silently; no reminder after 24 h open; per-kind default grace (unhealthy 2 min, docker 1 min, disk 0).
- `notify/route_test.go`: `minSeverity` filtering per event; a recovery goes to the channels that got the open even after `minSeverity` was raised; `test` only to its channel; relay goes to non-failing others and ignores `minSeverity`; `Silent` per §6.6.
- `notify/relay_test.go` (fake clock): no relay before 10 min; one relay at 10 min; none again within 6 h; `channel_recovered` after success; no relay with a single channel or when all fail; relay events never trigger relays.
- `notify/opfailed_test.go`: three failures of the same stack and action within 10 min give one message, then a collapsed "3× in 10m".
- `notify/rules_test.go`: `TestResolve`: service over stack over global over default, per field; disallowed scope and field give 422 errors; orphan detection against a `stacks.Detail`.
- `notify/detector_test.go` with a fake alerts source, a fake clock and a fake sender: `TestDetectorOneNotificationPerIncident`, `TestDetectorFreezesContainerIncidentsWhileDisconnected`, `TestDetectorWaitsForFirstRestartScan`, `TestDetectorStartupNoResend` (open incident in the DB plus active alert gives no send), `TestDetectorStartupRecoversClearedWhileDown`, `TestDetectorResendsUnnotifiedOpen`, `TestDetectorIdleWhenNoChannel` (no `Alerts` call over 5 simulated minutes), `TestOpFailedOneShot` (failed and timed_out send, cancelled and succeeded do not).
- `notify/telegram_test.go` and `webhook_test.go` against `httptest.Server`: request shape (golden JSON), 2xx, 429 with `retry_after` and `Retry-After`, 401 and 400 permanent, 5xx retried, redirect not followed, 4 KiB body cap, `TestTelegramErrorNeverContainsToken` (server closes the connection, so the `url.Error` includes the URL); `disable_notification` follows `Silent`; `TestWebhookSignature` (a known secret, timestamp and body give a known hex, verified independently); no signature headers without a secret; custom headers sent and reserved names rejected; `TestTelegramChats` (dedup across message, channel_post and my_chat_member; empty list hint; 409 webhook message); `publicUrl` links (set, unset, stack name escaped, operation link).
- `notify/dispatch_test.go`: queue overflow drop order, retry schedule with a fake clock, permanent stop, status counters, shutdown cancels in-flight.
- `notify/store_test.go` (real SQLite): migration, channel CRUD with secrets kept on `PUT` without them, rules upsert and delete, cascade on stack removal.
- `alerts`: `disk_low` cases: percent and bytes triggers each alone; between the lines the state is kept (hysteresis, both directions); clears only when both clear lines are met; three paths on one device give one alert listing all three; a failing `Statfs` path is skipped; no Docker root while Docker is disconnected. `host.DiskWatcher` with a fake `Statfser` and device map; per-container restart thresholds via a fake `Thresholds` (an override of 10 suppresses at 5, an override of 1 fires at 1).
- `notify/kinds_test.go`: `TestSiblingKindDefaults`: `mount_problem` and `check_down` are danger, on, grace 0 and frozen while Docker is disconnected; `cert_expiring` is warning, on and sent silently; a `check_down` alert without a service resolves the stack rule.
- `stacks`: `OnFinish` called once per operation with the final state, including after a panic and on timeout.
- `api`: contract tests for every route, session required, 422 shapes, secrets never in any response (`TestNotificationResponsesHaveNoSecrets` scans all GET bodies for the stored values), test-send 502 with a redacted error, 429 rate limit.

**Vitest:** the Settings card (add, edit with keep-secret, test-send success and error display, delete confirm, defaults reset, Find chat fills the chat id and shows the empty hint, Generate signing secret, the 5-header limit, public URL validation message); the Notifications tab (inherited vs override rendering, the popover saves only changed fields, Clear override, orphaned rows, off banner, cards layout under 720 px); `aria-label` text on grid cells.

**E2E (`cmd/stackstead/e2e_test.go`):** start a local `httptest` webhook receiver, create a webhook channel with a signing secret via the API, `POST /api/notifications/test`, and assert one request with the documented body and a valid signature. With Docker: run a smoke service with a failing healthcheck and a short override (grace 0) and assert one `open` webhook, then fix it and assert one `recovered`.

**Definition of done:** tests written first, the full `make check` passes, and every success criterion in §1 maps to at least one test above.

## 12. Integration with sibling features

The notifier consumes alerts by kind, so a sibling only needs its alert kind in `internal/alerts` and a row in §6.1:

- **Volume diagnostics** (`2026-10-18-stackstead-volume-diagnostics-design.md`): `mount_problem` (danger, one alert per finding except `low_space`, keyed by finding id). Message links use `/stacks/<name>?tab=storage`.
- **Reachability checks** (`2026-10-18-stackstead-reachability-checks-design.md`): `check_down` (danger) and `cert_expiring` (warning), one alert per check (`subject` = check id, so the key is stable per stack and check). The notifier uses the alert kinds, not `SubscribeTransitions`, so dedup, grace, flapping and restart persistence work as for every other kind. Detection latency is the notifier's 30 s tick on top of the check interval.
- **Image updates**: failed `update` and `rollback` operations notify through `op_failed`. Available updates are not alerts (that spec's decision), so they do not notify.
- **Storage inventory, config history**: nothing to notify.
- **Richer compose forms**: must not write notification settings into compose files (§3).

Kinds added later by a sibling default to danger → on, warning → on, info → off, with global, stack and service scopes, unless their row says otherwise.

## 13. Open questions

None. The user settled relay timings (fixed constants), recovery sound (always silent, including danger-level) and the caps (20 channels, 5 webhook headers).
