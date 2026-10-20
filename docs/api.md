# API and observability reference

Everything the web UI does goes through this JSON API, so you can script it
too. Back to the [README](../README.md).

## API overview

All routes except health, setup and login need a session cookie. Errors are
`{"error": "..."}`, with `"fields": [{field, message}]` for validation.

| Method and path | Purpose |
| --- | --- |
| `GET /api/health` | liveness, no authentication: `{"ok": true}` (used by installers and monitors) |
| `GET /api/stacks` | list stacks with status |
| `POST /api/stacks` | create with `{name, services: [{service, config}]}` or legacy `{name, service, config}` |
| `POST /api/stacks/import` | register an existing compose file without modifying it |
| `GET /api/stacks/{name}` | detail with services and containers |
| `GET`/`PUT /api/stacks/{name}/compose` | raw YAML, saved with the hash from the read (409 if stale) |
| `POST /api/stacks/{name}/services` | add a service with `{service, config, hash}`; returns stack detail (200) |
| `POST /api/stacks/{name}/services/preview` | preview an addition with `{service, config, hash}`; returns `{content, hash}` without writing |
| `DELETE /api/stacks/{name}/services/{service}?hash=` | remove a service from the file; returns stack detail (200) |
| `PUT /api/stacks/{name}/services/{service}` | structured service edit |
| `POST /api/stacks/{name}/services/{service}/preview` | the file a structured edit would produce, `{content, hash}`, without writing it; the save must send that same `hash` |
| `POST /api/stacks/{name}/actions/{action}` | `up`, `down`, `restart`, `pull`, `build`; returns an operation (202) |
| `DELETE /api/stacks/{name}?volumes=true` | `compose down` then unregister (202) |
| `GET /api/stacks/{name}/operations` | recent operations |
| `GET /api/operations/{id}`, `/stream`, `POST .../cancel` | output, live SSE stream, cancel |
| `GET /api/fs?path=`, `GET /api/status` | file browser, status (`version` of the server, `docker`, `dockerVersion` (engine version, empty when unreadable), `compose`, `events`: `connected` or `disconnected` for the container event stream, `metricsDegraded`: true while samples cannot be stored; `metricsIntervalSeconds` and `metricsRetentionSeconds`: the sampling settings, 0 when unset) |
| `GET /api/operations?state=running` | operations across all stacks, optionally only running ones |
| `GET /api/stacks/{name}/logs` | live container logs (SSE); every `log` event has an `id`, and `Last-Event-ID` resumes exactly |
| `GET /api/stacks/{name}/metrics`, `.../metrics/live` | resource history (JSON) and live samples (SSE) |
| `GET /api/host` | host tiles data: hostname, containers, CPU, memory, disk, network, disk I/O, volumes, uptime and restarts; see [Dashboard data](#dashboard-data) |
| `GET /api/alerts` | active problems, most urgent first; see [Dashboard data](#dashboard-data) |
| `GET /api/metrics/summary?window=15m` | per-project sparkline series and top consumers; see [Dashboard data](#dashboard-data) |
| `GET /api/containers`, `GET /api/containers/{id}` | every container on the host (all states) and one container's details; see [Containers](#containers) |
| `GET /api/containers/{id}/logs` | one container's logs (SSE), the same contract as the stack logs |
| `POST /api/containers/{id}/{start\|stop\|restart}` | act on a container outside the registered stacks; returns its row (200) |
| `GET /api/networks`, `POST /api/networks` | list Docker networks; create a bridge, macvlan or ipvlan network (201); see [Networks](#networks) |
| `DELETE /api/networks/{id}` | remove an unused, unmanaged network (204), by ID or name |
| `GET /api/host/interfaces` | IPv4 host interfaces for LAN network setup; see [Networks](#networks) |
| `GET /api/events` | container changes and Docker container events as they happen (SSE) |
| `POST /api/password` | change the signed-in user's password (`{current, new}`; 204) |
| `GET /api/settings/metrics-token` | whether a scrape token exists, `{configured}` (never the token) |
| `POST`/`DELETE /api/settings/metrics-token` | create or rotate, or revoke, the Prometheus scrape token |
| `GET /api/settings/update` | the cached update status: `{current, installer, command, state, latest, notesURL, checkedAt, error}`; see the update status notes below |
| `POST /api/settings/update/check` | check now (JSON body `{}`); answers the status; checks less than 60 seconds apart answer the cached status |
| `GET /metrics` | Prometheus exposition, bearer token auth instead of a session |

Update status:

- `state` is `up-to-date`, `available`, `failed` (the last check could not read
  the release source and no earlier success is cached), `disabled`
  (`update_check: false`) or `unknown` (a development build, which cannot be
  compared). `latest`, `notesURL` and `checkedAt` are empty or absent until a
  check has succeeded; after a later failure the last success is kept and
  `error` says why the latest check failed: `network`, `timeout`, `http` or
  `malformed`.
- `current` is the server's version without the leading `v`. `command` is the
  terminal command for this machine (`sudo /usr/bin/stackstead upgrade`).
  Nothing in the request chooses a version, URL or installer, and the API
  installs nothing: upgrades run only from the terminal.
- The server checks once a day, and once an hour after a failure. Both routes
  are always present on a running server; with `update_check: false` they
  answer `state: disabled`.

Notes on the stacks API:

- **Multi-service stacks.** Create with `{name, services: [{service, config}]}`
  for 1–20 services, or the legacy `{name, service, config}` body for one.
  In the services form, an omitted first service name defaults to the stack
  name; later entries need a name. Legacy omitted service also defaults to the
  stack name. The presence of both `services` and either `service` or `config`
  returns 400, even when the values are empty or null. `services: null` and
  `services: []` return a count error on `services`. Validation errors use
  `services[i].` field paths in the services form and unprefixed paths in the
  legacy form. Duplicate names, host ports and static IPs across entries are
  reported on the later entry. External networks are declared once in a
  shared top-level `networks:` block, deduplicated and sorted.

  New structured create/add service names must match
  `^[a-z0-9][a-z0-9_-]{0,62}$`: lowercase letters, digits, underscores and
  hyphens, starting with a letter or digit, at most 63 characters. This is an
  API compatibility narrowing: legacy create requests with formerly
  Compose-valid uppercase, dotted or longer service names are now rejected.
  Existing imported/raw-defined services can still be edited and removed by
  their existing names without this new-name validation.
- **Adding and removing services.** Send the hash from the last read with
  `{service, config, hash}` to add or preview an addition; remove with the
  mandatory `?hash=` query and no request body. A stale hash returns 409 and
  never overwrites the file. Adding an existing service, removing the last
  service, and removing a service referenced by another service's `depends_on`
  also return 409 (the latter names its dependents). Unknown stacks/services
  return 404. Successful add/remove returns the updated stack detail. Preview
  returns candidate content and its base hash; save still performs full
  validation, so preview success does not guarantee save success.

  Add/remove validates the candidate compose file but runs no lifecycle
  action. Run **Up** to start an added service; a removed service's running
  container is removed on the next **Up**. Both POST routes require
  a session, `Content-Type: application/json`, and a JSON body; unknown fields
  are rejected. Bodyless DELETE requires a session and hash, follows the
  existing DELETE convention, and does not decode JSON or require a JSON
  content type.
- **Partial checks.** Creating or editing a stack checks new host ports and
  static IPs against the Docker engine. If the engine is unreachable only the
  ports declared by other stacks are checked, and the response carries the
  header `X-Stackstead-Checks: partial`.
- **POST needs JSON.** Every `POST` to the stacks and operations endpoints
  (including `actions/{action}` and `operations/{id}/cancel`) requires
  `Content-Type: application/json`, and a body, which may be just `{}`. Logout
  takes no body.
- **Operation stream.** `GET /api/operations/{id}/stream` is server-sent
  events: `line` (one output line, as a JSON string; earlier output is replayed
  first), `done` (the final operation as JSON, after which the stream ends) and
  `dropped` (the client was too slow and was cut off; reconnect, which replays
  the retained output again, or fetch `GET /api/operations/{id}`).
- **Override files are not supported yet.** Stackstead runs compose with
  `-f <file>`, which turns off the automatic merge of `compose.override.yaml`
  (and `.yml`, `docker-compose.override.*`). Importing a file with an override
  next to it is refused, startup sync skips such folders with a warning, and
  lifecycle actions and edits are refused if one appears later. Merge or
  remove the override file.

- **Health and ports.** Each stack in `GET /api/stacks` and `GET
  /api/stacks/{name}` carries `health`, counts of its containers by healthcheck
  result (`healthy`, `unhealthy`, `starting`, `none` for running containers
  without a healthcheck, and `failed` for dead, restarting and non-zero-exit
  containers; clean exits, created and paused containers count nowhere), and
  `ports`, the distinct host ports its compose file publishes, sorted by port
  number (a range as `"8000-8010"`; container-only ports are left out). Each
  container in the detail has its own `health`: `healthy`, `unhealthy`,
  `starting` or `none`.

Removing a stack never deletes files: the compose file and its folder stay on
disk, and volumes are removed only when `volumes=true`. Stopping Stackstead
never stops containers.

## Storage

The Storage screen (`/storage`) has Overview, Images, Volumes and Build cache
tabs, plus image and volume detail pages. Container writable layers are shown
for information; cleanup never removes containers.

| Method and path | Response or purpose |
| --- | --- |
| `GET /api/storage` | `images`, `containers`, `volumes`, `buildCache` totals, five `topContainers`, and `declarationsUnknown` stack names |
| `GET /api/storage/images` | `{images, totals}` |
| `GET /api/storage/images/{id}` | image plus architecture, OS, layer count and labels; use 12–64 hex ID characters without `sha256:`; ambiguous prefixes give 409 |
| `GET /api/storage/volumes` | `{volumes, totals}` |
| `GET /api/storage/volumes/{name}` | volume plus mountpoint and labels |
| `GET /api/storage/build-cache` | `{records, totals}` |
| `POST /api/storage/refresh` | `{}` starts background size refresh; 202 `{startedAt}`; within 60 seconds, 429 `{error, retryAfter}` with `Retry-After` |
| `POST /api/storage/plans` | preview the exact proposed cleanup; 201 plan |
| `GET /api/storage/plans/{id}` | read an unconsumed, unexpired plan |
| `POST /api/storage/plans/{id}/confirm` | `{confirm: "<exact phrase>"}`; 202 operation |
| `GET /api/actions?kind=storage.&scope=host&limit=50` | newest persistent storage actions; available even when Docker is down |

All routes require a session. Mutations require JSON. Lists share a 2-second
cache and answer without waiting for disk usage. Each kind's totals has `count`,
nullable `size` and `reclaimable`, nullable `sizesAt`, `refreshing` and `error`.
Unknown bytes are `null`, not zero. Size snapshots refresh in the background:
images and container layers every minute, volumes every 5 minutes, build cache
every 10 minutes. The dashboard Volumes tile shares the same 5-minute volume
cache. A stale successful snapshot remains visible during refresh or failure;
`sizesAt` describes the last successful reading.

Images show tags/digests, container consumers, declarations, dangling status and
sizes. Volumes show driver, scope, project, labels, consumers, declarations,
protection and size metadata. Detail responses omit image environment, command
and entrypoint and volume driver options. Declarations come from each registered
stack's single compose file and adjacent `.env`; interpolated compose output is
never returned.

Plan bodies are `{kind:"image", ref:"tag-or-id"}` or
`{kind:"volume", name:"volume-name"}` for one item; bulk bodies are
`{kind:"images", selection:"dangling"}`, `{kind:"volumes", selection:"unused"}`
or `{kind:"build-cache", selection:"unused"}`. Tagged images are excluded from
bulk cleanup. A single tag removes that reference, leaving other tags intact.
An image with several tags cannot be planned by ID (409): Docker refuses an
unforced delete by ID, so remove its tags one at a time.

The plan includes `id`, `createdAt`, `expiresAt`, `items`, `protected`, `warnings`,
`reclaim:{min,max,exact}` and `confirmPhrase`. Plans expire after 5 minutes, live
only in memory and are consumed on confirmation; at most 32 are retained. Copy
`confirmPhrase` byte for byte, including whitespace. Wrong phrases give 400 with
`fields:[{field:"confirm",message:...}]`; missing/expired plans give 404;
protected/in-use targets, empty plans and busy cleanup give 409. Docker or fresh
size-reading failures give 503. Plans require a fresh size reading bounded to
60 seconds; stale browsing sizes cannot authorize cleanup.

Volumes declared by registered stacks remain protected even after those stacks
are stopped. Registered compose-project labels also protect volumes. If any
stack's declarations are unknown, otherwise unprotected volume plans fail closed
with 503 naming the stacks; repair their compose configuration before retrying.
Volumes without a creation timestamp cannot be identified safely and are refused.
Images used by any container, including stopped containers, and volumes in use
cannot be deleted. These checks run again for each item; changed identities,
new declarations or unknown declarations cause skips. Docker removals never
force deletion. Items created after an exact preview are outside that plan.

Build cache has two paths. The by-ID capability starts **off**; enabling it
requires a separate real-engine preservation check. With it enabled, cleanup
sends only the rechecked record IDs. Otherwise the plan has `mode:"all-unused"`
and `reclaim.exact:false`: its list is **expected items**, and confirmation can
remove all build cache unused at that later moment, including records outside
the preview. The UI and CLI label this fallback **not exact** beside the phrase.
Docker's aggregate reclaimed bytes are emitted once. By-ID action rows record
per-record plan estimates, never copies of the aggregate reclaimed bytes;
fallback writes one `build-cache/all-unused` row with the actual aggregate.

Cleanup uses the existing operation routes, streams and cancellation. Host
operations have `"stack":""` and `"action":"storage-cleanup"`; the UI/CLI
show `host`. Cancellation stops before the next item. Item failures are logged
and make the operation fail; changed items are skipped with reasons.

The action route returns rows with `id`, `ts`, `username`, `kind`, `scope`,
`target`, `detail`, nullable `bytes`, `result` and `operationId`. `kind` is a
literal lowercase prefix, `scope` an exact match, and `limit` is 1–500 (default
50). Single cleanup uses `storage.delete`, bulk cleanup `storage.prune`, scope
`host`; results are `deleted`, `untagged`, `skipped` or `failed`. Image/volume
bytes are plan estimates. Rows persist for 365 days and are pruned hourly.
A failed log write is reported in operation output but cannot undo a deletion.

## Observability

Stackstead follows Docker's event stream to keep container status current, so
the UI does not poll `docker ps`, and samples resource usage of every
running container.

- **Logs.** `GET /api/stacks/{name}/logs?follow=true&tail=200&since=10m&service=web`
  is server-sent events: `log` (`service`, `container`, `stream` as `stdout`
  or `stderr`, `time`, `text`), `error` (one container's stream failed; the
  others continue) and `end`. `tail` is a number or `all`; `since` is an
  RFC3339 time, a Unix timestamp or a duration. Containers with a TTY do not
  separate stderr from stdout. **Logs are never stored by Stackstead**; they
  are read from Docker on demand, so they are as old as Docker's own log
  driver keeps them. With `follow`, the stream outlives restarts and
  recreation: a container's lines continue after it starts again (resumed from
  its cursor, so nothing repeats), a replacement container with a new id is
  attached from `tail`, and `end` is sent only when the stack, or the service
  named in `service`, has no container left. Without `follow`, `end` follows
  the last line as before.
- **Log resume.** Each `log` event carries `id: <container id, 12 hex>:<unix
  nanoseconds>:<ordinal>`, where `ordinal` counts the lines with that exact
  timestamp delivered so far on the stream. A client reconnects with the
  latest id of every container, comma-separated, as `Last-Event-ID`, and the
  server continues each container from that point without repeating or
  skipping lines (the UI does this automatically). Containers without a cursor
  (new replicas, malformed entries) start from the normal `tail`. Residual
  limits come from Docker: lines already evicted by log rotation cannot be
  replayed, a host clock that steps backwards can hide lines on resume, a
  recreated container has a new id and starts from `tail`, and log drivers with coarse clocks only make the ordinal do more work. Resume
  reads the container's log from the start (`tail=all` plus `since`) and skips
  to the cursor, so its cost grows with the log: configure log rotation
  (for example `max-size`) on stacks you tail for long.
- **Metrics history.** `GET /api/stacks/{name}/metrics?from=&to=&step=&service=`
  returns `{points}` with, per service and time bucket, `cpu` (percent of one
  core), `mem`, `memLimit` and per-second network and block I/O rates. The
  default window is the last hour. Rates are totals across a service's
  replicas (computed per container, then summed); `cpu`, `mem` and `memLimit`
  are averages across replicas. The UI charts use the same formulas for live
  samples, so history and live data join without a step.
  Memory excludes page cache (`inactive_file` on cgroup v2, `cache` on v1),
  like `docker stats`. `metrics/live` streams new samples as they are taken
  (`sample` events). History and live `time` are both UTC RFC3339.
- **Health history.** `GET /api/stacks/{name}/health?span=` returns
  `{from, to, services}` (Unix seconds); `span` is a duration from `1m` to
  `24h` (default `60m`, anything else is 400). Each service has `restarts`
  and `runs`, contiguous `{state, from, to}` stretches covering `[from, to]`;
  time before the first known row is `nodata`. States are `running`
  (no health check), `healthy`, `unhealthy`, `starting`, `restarting`,
  `exited`, `removed` (all replicas gone) and `nodata`. With several
  replicas the worst state wins: `exited` > `unhealthy` > `restarting` >
  `starting` > `healthy`/`running` > `removed` > `nodata`. `restarts` counts
  distinct `die` followed by `start` pairs in the window, once per restart,
  whether from `docker restart` or a restart policy; restarts that happen
  while Stackstead is down are not counted. History starts with the first
  run of this version and is kept for `metrics_retention`, except that each
  live container's current state is kept however old it is. Services with no
  recorded rows are omitted: the client knows the stack's services and
  renders the missing ones as `nodata`. A service whose containers were all
  removed shows `removed` until its rows age out, so clients should
  intersect `services` with the stack's current services. A container
  recreated while Stackstead was down is marked `removed` at the next
  startup. Refetch on the stack's `change` event. An unknown stack is 404.
- **Events.** `GET /api/events` sends `status` (`events`, `metricsDegraded`)
  when it connects or changes, and `change` (`project`, `seq`) when a
  project's containers changed, including a restart that leaves the listing
  identical; refetch the affected stack on `change`. It
  also sends `event`, one Docker container event: `action`, `container`
  (name), `project` and `service` (empty strings for a container that is not
  part of a compose project), `image` and `time`, plus
  `exitCode` (a number, only on `die`, and only when Docker reports one) and
  `health` (only on `health_status`, and only `healthy` or `unhealthy`).
  `time` is UTC RFC 3339 and may include fractional seconds. Only these actions
  are forwarded: `create`, `start`, `die`, `stop`, `kill`, `restart`,
  `destroy`, `pause`, `unpause`, `oom` and `health_status`. Events are not
  replayed: a client sees only what happens while it is connected.
  `/api/events` and `metrics/live` send `event: dropped` and then end when the
  client falls behind; reconnect. A stack without a compose project has no
  logs: the logs stream sends just `end`.

### Dashboard data

Three read-only routes feed the stack list's dashboard. They cost nothing
while nobody is looking (no background collection) and are cached on the
server, so ten open tabs cost the same as one.

**`GET /api/host`** is always `200`. The body has `hostname`, `os`
(`linux`, `darwin`, ...), `source` (`proc` or `docker`, see below) and
`sampledAt`, then these sections, each an object or `null` when its source is
unavailable:

| Key | Contents |
| --- | --- |
| `containers` | `total`, `running`, `exited`, `bad` (unhealthy, dead, restarting, or exited non-zero) |
| `cpu` | `percent`, `cores`. On `source: "proc"` `percent` is null until two readings exist; on `docker` it is available immediately (null only if the engine reports 0 cores) |
| `memory` | `used`, `total`, `swapUsed`, `swapTotal` (bytes); on `docker` the swap fields are always 0 |
| `disk` | `path` (the stacks directory), `free`, `total` (bytes) |
| `network` | `rxBytesPerSec`, `txBytesPerSec` (null until two readings exist), `interface` (on `proc` it may be `""` until the second reading) |
| `diskIO` | `readBytesPerSec`, `writeBytesPerSec` (null until two readings exist) |
| `volumes` | `count`, `size` (bytes), `dangling` |
| `uptime` | `seconds`, `load1`, `load5`, `load15` |
| `restarts24h` | `total` and `top`: the two busiest containers as `{container, service, project, count}` |

`source` is `proc` when `/proc/stat` is readable: CPU, memory, network, disk
I/O and uptime then describe the whole host, and `network.interface` is the
busiest interface by traffic. Otherwise (macOS, Docker Desktop, Windows) it is
`docker`: CPU, memory, network and disk I/O are summed over the running
compose containers using the engine's core count and total memory, with
`network.interface` set to `containers`, and `uptime` is `null`. On `docker`
the network and disk I/O rates come from the sampler's counters: they are
null on the first reading, then 0 B/s while containers are idle; between
sampler ticks the last rates are held for at most 30 seconds. When Docker is
unreachable, `containers`, `volumes` and `restarts24h` are `null`, and with
`source: "docker"` so are `cpu`, `memory`, `network` and `diskIO`. `disk` is
`null` only when the stacks directory cannot be read. The Docker `/info` call
used on the request path has a short client timeout (2 seconds in the
server); the background volume and restart refreshes get 30 seconds each, and
never block a request.

The snapshot is collected at most once every 5 seconds. `volumes` and
`restarts24h` (a 24-hour scan of `start` events: the first start of a
container is not a restart) are refreshed in the background, `volumes` at
most every 5 minutes and `restarts24h` at most every 60 seconds, and are
`null` until the first refresh finishes. A failed refresh is not retried
before its interval has passed. `restarts24h` and the `restart_loop` alert
only see the container events Docker still holds in memory, which on a busy
host can be less than 24 hours.

**`GET /api/alerts`** is a JSON array, `[]` when nothing is wrong. Each alert
has `kind`, `severity` (`danger`, `warning` or `info`), `message`, `since`
(RFC3339) and, where they apply, `stack`, `service` and `container`. The list
is sorted by severity, then by `since`, newest first. `since` is the start
of the alert's current continuous occurrence, as seen by this Stackstead
process: it resets if the alert clears and later returns, and it survives
Docker disconnects for container-derived alerts. Nothing is stored, so it
restarts with the process. Distinct problems (several override files, several
holders of a clashing port) are separate alerts. The list is recomputed at
most once every 5 seconds; a failure reading the registry is a `500`
`{error}`. `port_clash` compares the host port and protocol, not the bind
address.

| `kind` | Severity | Message |
| --- | --- | --- |
| `exited` | danger | `web exited (137)`, with ` and restart policy is always` (or `unless-stopped`) and `; 4 restarts in 24h` when they apply. Registered stacks only, non-zero exit, not one-off runs |
| `unhealthy` | danger | `web is unhealthy`. Applies to every container on the host; `stack` and `service` are set only for registered stacks, otherwise the message names the container |
| `restart_loop` | warning | `web restarted 5 times in 24h` (3 or more). Same scope and naming as `unhealthy` |
| `port_clash` | warning | `web wants 8080 but api holds it`: another stack, or a running container, holds a host port the service declares |
| `override` | warning | `compose.override.yaml found; override files are not supported yet` |
| `parse_error` | warning | the first line of the compose parse error |
| `docker_disconnected` | danger | `Docker is unreachable; status may be stale` |
| `metrics_degraded` | warning | `Metrics are not being stored (database write failures)` |

**`GET /api/metrics/summary`** takes `window` as `5m`, `15m` (the default) or
`1h`; anything else is a `400` `{error}`, and a store failure is a `500`. The
body is `window`, `step` (seconds, window divided by 30), `from`, `to`
(UTC), `stacks` and `top`. The range is aligned to the step: `to` is now
truncated to `step`, plus one `step`, and `from` is `to` minus `window`.
`stacks` is keyed by compose project; each value has `cpu` and `mem` (30
stored buckets each, `null` for an empty bucket), `cpuNow`, `memNow`,
`netRxBytesPerSec` and `netTxBytesPerSec`. `cpuNow` and `memNow` are the newest
non-empty stored bucket. The network rates are computed across each
container's last two stored buckets and summed per project. The `null` rule is
per container: a container has no rate when fewer than two buckets exist or the
gap between them exceeds 3 buckets, and a project's value is `null` only when
none of its containers has a rate.
Units: `cpu`, `cpuNow` and `top.cpu` values are percent where 100 is one core;
`mem`, `memNow` and `top.mem` values are bytes. Only `top` comes from the
sampler's latest readings: `cpu` and `mem` each list the five heaviest
containers as `{container, service, project, value}`; containers outside
compose are included, with empty `service` and `project`.
Results are cached for 10 seconds per window.

### Containers

`GET /api/containers` returns one row per container Docker reports, in every
state, sorted by name: `id` (12 hex), `name`, `image`, `state`, `status`,
`health` (`healthy`, `unhealthy`, `starting`, `none`), `exitCode` (a number
once exited, else `null`), `createdAt`, `project`, `service`, `stack`,
`managed`, `ports` (`hostIP:hostPort->containerPort/proto`, duplicates
collapsed, sorted by host port), `networks` (network to IP), `cpu` (percent
of one core), `mem` and `memLimit` (bytes), and `restarts24h`. `cpu`, `mem` and
`memLimit` are `null` while the container has no current sample (stopped, or
not sampled yet). `managed` says who owns the container: `stack` when its
compose project belongs to a registered stack (`stack` then holds that stack's
name), `compose` when it has a compose project that no registered stack uses,
and `none` for a plain container. `project`, `service` and `stack` are empty
strings where they do not apply. `status` is Docker's own text ("Up 3
minutes"), read from a fresh Docker listing, not from the event-fed state
cache. The list is computed at most once every 2 seconds however many clients
ask, and answers `503` while Docker is unavailable.

`GET /api/containers/{id}` returns `id` (the full 64 hex), `name`, `image`,
`state`, `status`, `health`, `startedAt` and `finishedAt` (`null` when Docker
reports none), `exitCode` (`null` while running), `restartPolicy`,
`memoryLimit` (the container's configured memory limit in bytes, from
`HostConfig.Memory`; `null` when unlimited), `restarts24h`, `project`,
`service`, `stack`, `managed`, `ports`, `networks`, `mounts` (`type`,
`source`, `destination`, `readOnly`) and `labels`. It has no `createdAt`,
`cpu`, `mem` or `memLimit`: live usage comes from the list. It never includes
environment variables, the command, the entrypoint, the user or other host
configuration.

`GET /api/containers/{id}/logs` takes `tail`, `follow` and `since` (there
is no `stream` or `service` filter) and sends the same `log`, `error` and `end`
events, ids and `Last-Event-ID` resume as the stack logs; with `follow` it
re-attaches after a restart and ends when the container is removed.

`POST /api/containers/{id}/start`, `stop` or `restart` takes a JSON body
(`{}`), waits for Docker (at most 30 seconds; `stop` and `restart` give the
container 10 seconds to exit) and returns the container's row, read fresh from
Docker, or `204 No Content` when the action removed the container (for
example a `--rm` container that was stopped). Containers of a registered stack
are refused: use the stack's actions.

`{id}` is a container id, or an unambiguous prefix, of 12 to 64 hex
characters; it is never a name (look the id up in the list). Errors: `404`
for an unknown or ambiguous id, `409` for an action on a stack container,
`502` when Docker refuses the action (its message is passed on), and `503`
when Docker is unavailable.

### Networks

`GET /api/networks` returns an array, cached for 2 seconds. Each network has
`id` (12 hexadecimal characters), `name`, `driver`, `scope`, `subnets`,
`gateways`, `ipRange`, `parent`, `ipvlanMode`, `internal`, `builtin`, `project`,
`stack` and `containers`. The arrays are never null; `gateways[i]` corresponds
to `subnets[i]`. Each container has `name` and `ipv4` (a bare IPv4 address,
or an empty string when unavailable).
`builtin` identifies Docker's `bridge`, `host` and `none` networks. `project`
is the Compose project label, or an empty string; `stack` is the registered
Stackstead stack owning that project, or an empty string. Successful creates
and removals invalidate the cache immediately.

`POST /api/networks` requires JSON with `name` and `driver` (`bridge`,
`macvlan` or `ipvlan`). Optional fields are `subnet`, `gateway`, `ipRange`,
`parent`, `ipvlanMode` and `internal` (boolean). Unknown fields are rejected.
Names are 1–63 letters, digits, underscores, dots or hyphens and start with a
letter or digit. A supplied subnet must be a masked IPv4 CIDR and must not
overlap any existing network. Macvlan and ipvlan require a subnet and parent;
bridge permits an omitted subnet (Docker allocates one) and forbids parent.
A gateway requires a subnet, must lie inside it and cannot be its network or
broadcast address. An IP range requires a subnet and must be an IPv4 CIDR
inside it. Parent names are 1–15 letters, digits, underscores, dots, colons or
hyphens; Docker validates that the interface exists. `ipvlanMode` is accepted
only for ipvlan, is `l2` or `l3`, and defaults to `l2`. Created networks carry
`dev.stackstead.created=true`. Success is 201 with the network object.

`DELETE /api/networks/{id}` accepts a name, full ID or unique 12–64 character
hexadecimal ID prefix and returns 204. Removal refuses built-in networks,
networks belonging to a registered stack, and networks with attached
containers (409, with the stack or container names in the error). These
checks run on the server against fresh Docker state.

Validation returns 400 `{error, fields}`. Duplicate names and Docker
conflicts return 409; other Docker create refusals return 400 with Docker's
message. An unknown removal target is 404. Docker unavailable is 503 for the
network routes. All these routes require a session.

`GET /api/host/interfaces` returns an array of `{name, ipv4, subnet,
gateway, default}`: IPv4 address with prefix length, masked subnet CIDR, optional
gateway (empty string when unknown), and a boolean identifying the default route. Only up,
non-loopback, non-virtual IPv4 interfaces are included, with the default
interface first. This is Linux-only; other systems return `[]`. In a
container, these are the interfaces of Stackstead's network namespace, so
host networking is needed to see host interfaces. The route works without
Docker. Interface enumeration uses the OS interface API and reads only
`/proc/net/route` for routing information; it does not change host networking.

### What is stored

Every `metrics_interval` (default `10s`) every running container is sampled,
and one row per running compose container is written to the SQLite database in
the data directory: CPU, memory and limit, and cumulative network and block
I/O. Rows older than `metrics_retention` (default `72h`) are pruned hourly. At
the default settings that is about 26,000 rows per container. Containers
outside compose are sampled for live values only (the Containers screen, Top
consumers and `/metrics`) and never stored. Sampling runs with a concurrency
of 4 and is sized for about 40 running containers per 10 s tick; beyond that a
tick can overrun and the next ticks are skipped, so samples get sparser. If
the database cannot be written, `/api/status` reports `metricsDegraded` and
live data keeps working.

### Prometheus

`GET /metrics` serves the latest sample of every running container (those
outside compose have empty `project` and `service` labels)
(`stackstead_container_cpu_percent`, `..._memory_bytes`,
`..._memory_limit_bytes`, `..._network_receive_bytes_total`,
`..._network_transmit_bytes_total`, `..._blkio_read_bytes_total`,
`..._blkio_write_bytes_total`, labelled `project`, `service`, `container`)
and `stackstead_metrics_degraded`. It does not use the session; it needs a
bearer token: the endpoint answers 404 until a token exists, then 401 for a
missing or wrong one.

Create (or rotate; the old token stops working) a token while logged in. The
Settings page does this for you and shows the token once; `GET
/api/settings/metrics-token` only reports whether one exists. The
plaintext is shown once and only its SHA-256 is stored:

```bash
curl -b cookies.txt -X POST -H 'Content-Type: application/json' -d '{}' \
  http://127.0.0.1:8420/api/settings/metrics-token       # {"token": "..."}
curl -b cookies.txt -X DELETE http://127.0.0.1:8420/api/settings/metrics-token   # revoke
```

```yaml
scrape_configs:
  - job_name: stackstead
    scrape_interval: 15s
    static_configs:
      - targets: ['stackstead.example.com:8420']
    authorization:
      type: Bearer
      credentials: <token>           # or: credentials_file: /etc/prometheus/stackstead.token
```

Serve `/metrics` over TLS when it leaves the host: the token is a password.
