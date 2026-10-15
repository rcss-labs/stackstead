# API and observability reference

Everything the web UI does goes through this JSON API, so you can script it
too. Back to the [README](../README.md).

## API overview

All routes except health, setup and login need a session cookie. Errors are
`{"error": "..."}`, with `"fields": [{field, message}]` for validation.

| Method and path | Purpose |
| --- | --- |
| `GET /api/health` | liveness, no authentication: `{"ok": true}` (used by installers and monitors) |
| `GET /api/stacks`, `POST /api/stacks` | list stacks with status; create a stack from an image or Dockerfile |
| `POST /api/stacks/import` | register an existing compose file without modifying it |
| `GET /api/stacks/{name}` | detail with services and containers |
| `GET`/`PUT /api/stacks/{name}/compose` | raw YAML, saved with the hash from the read (409 if stale) |
| `PUT /api/stacks/{name}/services/{service}` | structured service edit |
| `POST /api/stacks/{name}/services/{service}/preview` | the file a structured edit would produce, `{content, hash}`, without writing it; the save must send that same `hash` |
| `POST /api/stacks/{name}/actions/{action}` | `up`, `down`, `restart`, `pull`, `build`; returns an operation (202) |
| `DELETE /api/stacks/{name}?volumes=true` | `compose down` then unregister (202) |
| `GET /api/stacks/{name}/operations` | recent operations |
| `GET /api/operations/{id}`, `/stream`, `POST .../cancel` | output, live SSE stream, cancel |
| `GET /api/fs?path=`, `GET /api/networks`, `GET /api/status` | file browser, Docker networks, status (`version` of the server, `docker`, `dockerVersion` (engine version, empty when unreadable), `compose`, `events`: `connected` or `disconnected` for the container event stream, `metricsDegraded`: true while samples cannot be stored; `metricsIntervalSeconds` and `metricsRetentionSeconds`: the sampling settings, 0 when unset) |
| `GET /api/operations?state=running` | operations across all stacks, optionally only running ones |
| `GET /api/stacks/{name}/logs` | live container logs (SSE); every `log` event has an `id`, and `Last-Event-ID` resumes exactly |
| `GET /api/stacks/{name}/metrics`, `.../metrics/live` | resource history (JSON) and live samples (SSE) |
| `GET /api/events` | container changes as they happen (SSE) |
| `POST /api/password` | change the signed-in user's password (`{current, new}`; 204) |
| `GET /api/settings/metrics-token` | whether a scrape token exists, `{configured}` (never the token) |
| `POST`/`DELETE /api/settings/metrics-token` | create or rotate, or revoke, the Prometheus scrape token |
| `GET /metrics` | Prometheus exposition, bearer token auth instead of a session |

Notes on the stacks API:

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

## Observability

Stackstead follows Docker's event stream to keep container status current, so
the UI does not poll `docker ps`, and samples resource usage of running
compose containers.

- **Logs.** `GET /api/stacks/{name}/logs?follow=true&tail=200&since=10m&service=web`
  is server-sent events: `log` (`service`, `container`, `stream` as `stdout`
  or `stderr`, `time`, `text`), `error` (one container's stream failed; the
  others continue) and `end`. `tail` is a number or `all`; `since` is an
  RFC3339 time, a Unix timestamp or a duration. Containers with a TTY do not
  separate stderr from stdout. **Logs are never stored by Stackstead**; they
  are read from Docker on demand, so they are as old as Docker's own log
  driver keeps them.
- **Log resume.** Each `log` event carries `id: <container id, 12 hex>:<unix
  nanoseconds>:<ordinal>`, where `ordinal` counts the lines with that exact
  timestamp delivered so far on the stream. A client reconnects with the
  latest id of every container, comma-separated, as `Last-Event-ID`, and the
  server continues each container from that point without repeating or
  skipping lines (the UI does this automatically). Containers without a cursor
  (new replicas, malformed entries) start from the normal `tail`. Residual
  limits come from Docker: lines already evicted by log rotation cannot be
  replayed, a recreated container has a new id and starts from `tail`, and
  log drivers with coarse clocks only make the ordinal do more work. Resume
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
- **Events.** `GET /api/events` sends `status` (`events`, `metricsDegraded`)
  when it connects or changes, and `change` (`project`, `seq`) when a
  project's containers changed; refetch the affected stack on `change`.
  `/api/events` and `metrics/live` send `event: dropped` and then end when the
  client falls behind; reconnect. A stack without a compose project has no
  logs: the logs stream sends just `end`.

### What is stored

Every `metrics_interval` (default `10s`) one row per running compose
container is written to the SQLite database in the data directory: CPU,
memory and limit, and cumulative network and block I/O. Rows older than
`metrics_retention` (default `72h`) are pruned hourly. At the default settings
that is about 26,000 rows per container. If the database cannot be written,
`/api/status` reports `metricsDegraded` and live data keeps working.

### Prometheus

`GET /metrics` serves the latest sample of every container
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
