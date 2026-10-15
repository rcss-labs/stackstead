# Stackstead Containers screen — Design

**Date:** 2026-10-15
**Status:** Draft, awaiting review. Adds a host-wide **Containers** screen to the v2 UI. Implemented by Plan 9. Builds on `2026-10-14-stackstead-v1-design.md` and `2026-10-15-stackstead-ui-v2-design.md` (8a/8b, released in v0.1.0).

## 1. Purpose

Today containers are only visible inside a stack's detail page; containers outside registered stacks appear only indirectly (alerts, top consumers, the events feed). The Containers screen is a **host-wide inventory for triage**: every container on the host in one table — compose or not, registered stack or not — so the operator can find the broken or resource-hungry one fast and, within one click, read its logs or restart it. If it belongs to a registered stack, the screen sends the operator to that stack instead of acting on it.

Success criteria:

- `/containers` lists every container the Docker daemon reports, with state, health, image, ports, CPU, memory and 24 h restarts, problems sorted first.
- Any container's logs are one click away (`/containers/<name>?tab=logs`).
- Containers **not** in a registered stack can be started, stopped and restarted from the list and from their page; containers in a registered stack cannot (server-enforced), and link to their stack.
- CPU and memory are shown for every running container, including plain `docker run` ones.
- Nothing polls while the tab is hidden; the list costs one cached server computation per 2 s regardless of how many tabs are open.

## 2. Scope

### In
- Backend: `GET /api/containers`, `GET /api/containers/{id}`, `GET /api/containers/{id}/logs` (SSE), `POST /api/containers/{id}/{start|stop|restart}`; `engine.Client` POST support and container inspect; the sampler samples every running container (non-compose samples are live-only, not stored).
- Frontend: rail entry and route `/containers` (list), route `/containers/:name` (Details and Logs tabs), breadcrumb, links into these pages from the Stacks dashboard, the events feed, top consumers, alerts and stack detail.
- E2E, docs (`docs/api.md`, `docs/security.md`, README feature line).

### Out
- Remove, kill, pause, exec/shell, attach, rename, create, image management, volume/network management.
- Actions on containers in a registered stack (compose stays the source of truth; use the stack's actions).
- Environment variables, command lines and other inspect fields beyond §4.2's list.
- Metrics history for non-compose containers (live values only).
- An "adopt / import as stack" flow for unregistered compose projects (the row only hints that the project is unregistered).

## 3. Key decisions

The TypeSafe skill is used for scored decisions where available; these were decided with the user in brainstorming.

| Decision | Choice | Reason |
|---|---|---|
| Screen purpose | Host-wide inventory + triage | User decision. |
| Page structure | List page `/containers` + container page `/containers/:name` (Details, Logs tabs) | User chose approach A: mirrors stacks (list → detail), bookmarkable, room for logs, reuses `LogsViewer`, `Tabs`, detail header patterns. Alternatives (drawer; tab on Stacks) rejected. |
| Container addressing in the UI | By **name** | Names survive recreation; ids do not. The API is addressed by id (12-hex or full); the page resolves name → id from the list. |
| Actions | Logs for every container; start/stop/restart only for containers not in a registered stack | User decision. Compose owns stack containers; a raw Docker stop/start would drift from what compose expects. |
| Which containers are "in a registered stack" | Compose project label equals a registered stack's project | Unregistered compose projects and plain containers are actionable. Server checks the registry; the client's view is not trusted. |
| Action execution | Synchronous Docker API call (`POST /containers/{id}/start|stop|restart`), stop/restart timeout 10 s; no Stackstead operation record | These are not compose operations; a toast reports the outcome. Keeps the operations model compose-only. |
| CPU/memory for plain containers | Sampler samples every running container; only compose samples are stored | User decision. Same cost model per container; history stays per compose project; `Latest()` (and thus Top consumers) now includes non-compose containers. |
| List freshness | Refetch on `/api/events` `change`/resync (debounced like the Stacks list); CPU/mem/restarts refreshed every 10 s while visible | Matches the 8b refresh policy; no timers while hidden. |
| Details source | `GET /api/containers/{id}` = one Docker inspect per open details view | The list never inspects; inspect only when a person looks. |
| Secrets | Details never return `Env`, command, entrypoint, `HostConfig` beyond mounts/restart policy | Env often holds secrets; the screen is for triage, not configuration. |

## 4. API

All routes require a session (`requireSession`); unknown `/api/*` keeps answering a JSON 404 after auth; error bodies are `{error}`. Mutating routes require `Content-Type: application/json` (existing CSRF rule) and accept `{}`.

### 4.1 `GET /api/containers`

```json
[{"id": "4e344ea0e904", "name": "media-jellyfin-1", "image": "jellyfin/jellyfin:10.9",
  "state": "running", "status": "Up 3 days (healthy)", "health": "healthy", "exitCode": null,
  "createdAt": "2026-10-12T08:01:22Z",
  "project": "media", "service": "jellyfin", "stack": "media", "managed": "stack",
  "ports": ["0.0.0.0:8096->8096/tcp"], "networks": {"media_default": "172.20.0.3"},
  "cpu": 3.1, "mem": 1174405120, "memLimit": 4294967296, "restarts24h": 0}]
```

- One row per container (all states) from a fresh Docker container listing, not the state cache snapshot, so Docker's human `status` ("Up 3 minutes") is current (ruling R4). Sorted by name (the UI applies its own order).
- `health` per `engine.Container.Health()` (`healthy|unhealthy|starting|none`); `exitCode` from `ExitCode()` when exited, else `null`.
- `managed`: `stack` when the compose project matches a registered stack's project (then `stack` = that stack's name); `compose` when the container has a compose project label that no registered stack has; `none` otherwise. `project`/`service`/`stack` are empty strings when not applicable.
- `ports`: published bindings as `hostIP:hostPort->containerPort/proto`, deduplicated (IPv4/IPv6 duplicates collapsed), sorted by host port.
- `cpu`/`mem`/`memLimit`: from `Sampler.Latest()` by container name; `null` when the container has no current sample (stopped, or not sampled yet).
- `restarts24h`: from the host collector's `RestartCounts()` by container id; `0` when absent; the whole field is `0` for every row before the first restart scan completes.
- Cached 2 s, single-flight (detached context, bounded timeout, panic-safe like the 8b flights). 503 `{error}` while Docker is unavailable.

### 4.2 `GET /api/containers/{id}`

```json
{"id": "4e344ea0e904…", "name": "media-jellyfin-1", "image": "jellyfin/jellyfin:10.9",
 "state": "running", "status": "Up 3 days (healthy)", "health": "healthy",
 "startedAt": "2026-10-12T08:01:25Z", "finishedAt": null, "exitCode": null,
 "restartPolicy": "unless-stopped", "memoryLimit": 4294967296, "restarts24h": 0,
 "project": "media", "service": "jellyfin", "stack": "media", "managed": "stack",
 "ports": ["0.0.0.0:8096->8096/tcp"], "networks": {"media_default": "172.20.0.3"},
 "mounts": [{"type": "bind", "source": "/srv/media", "destination": "/media", "readOnly": true}],
 "labels": {"com.docker.compose.project": "media"}}
```

- `memoryLimit` is the configured limit from `HostConfig.Memory` in bytes; `null` when 0 (unlimited).
- `{id}` is a container id or unambiguous id prefix (≥ 12 hex) — never a name (names are resolved by the client from the list).
- One `GET /containers/{id}/json` per request, short engine timeout. 404 for an unknown id, 503 when Docker is unavailable.
- Never includes `Env`, `Cmd`, `Entrypoint`, `Config.User`, secrets, or `HostConfig` fields beyond `RestartPolicy.Name`, `Memory` and the mounts list.

### 4.3 `GET /api/containers/{id}/logs`

SSE with exactly the stack logs contract (same query options `tail`, `follow`, `since`; same `log`/`error`/`end` events; same `id:` cursor and `Last-Event-ID` resume) for one container. The streaming core of the stack `handleLogs` is extracted into a shared helper used by both routes. 404 for an unknown id.

### 4.4 `POST /api/containers/{id}/{start|stop|restart}`

- 409 `{error: "<name> belongs to stack <stack>; use the stack's actions"}` when the container's project is a registered stack's project (checked against the registry on every request).
- 404 unknown id; 503 Docker unavailable; 502 `{error: "<Docker's message>"}` when Docker refuses (e.g. port already allocated).
- `stop` and `restart` use a 10 s stop timeout (`?t=10`). The request waits for Docker's answer (bounded by 30 s).
- 200 with the container's row as in §4.1 after the action (fresh state from Docker, not the cache).
- An unknown action → 404 via the route pattern.

### 4.5 Engine and sampler

- `engine.Client` gains a small POST helper (versioned path, empty body, explicit timeout), `ContainerAction(ctx, id, action string, stopTimeout int) error`, and `InspectContainer(ctx, id) (ContainerInspect, error)` decoding only the fields §4.2 needs.
- `metrics.Sampler.tick` samples every running container. Samples with an empty compose project are kept in `Latest()` and fan-out but skipped by `InsertBatch` (history stays per project). `SubscribeSamples(project)` is unchanged.

## 5. Frontend

### 5.1 Shell

- Rail: new item **Containers** after Stacks (`/containers`, current on `/containers` and `/containers/:name`), cube/box icon from the mock-up's icon style, `aria-label`/`title` "Containers".
- Breadcrumb: `/containers` → `Containers`; `/containers/:name` → `Containers / <name>` (mono). Titles: "Containers · Stackstead", "<name> · Stackstead".

### 5.2 List page (`web/src/routes/ContainerList.svelte`)

- Heading row: `h1` "Containers", muted counts (`<total> · <running> running`).
- Chips: All / Running / Stopped / Problems / Unmanaged with mono counts and `aria-pressed`. Problems = unhealthy, restarting, dead, or exited with a non-zero code. Unmanaged = `managed !== 'stack'`. Search (`aria-label="Search containers"`) over name, image, stack/project and port text.
- Table (`overflow-x:auto` card, explicit table roles, `data-label`s for ≤ 720 px cards):
  - **Container**: status dot (tone by state/health), mono name, faint short id.
  - **Stack**: link to `/stacks/<stack>` (managed `stack`); `<project>` + an "unregistered" tag (managed `compose`); `—` (none).
  - **Image**: mono, ellipsis, full text in `title`.
  - **State**: `StatusBadge` + health dot when not `none`.
  - **Status**: Docker's text, muted.
  - **Ports**: first two as chips, then `+N` (`title` lists all).
  - **CPU** / **Memory**: mono values (`formatPercent`, `formatBytes`), `—` when null.
  - **Restarts 24h**: mono; `--warning` when ≥ 3.
  - **Actions**: `Logs` link (to `/containers/<name>?tab=logs`) on every row; for `managed !== 'stack'`: Start (when not running) or Stop (when running), and Restart, as 26×26 icon buttons with `aria-label="Stop <name>"` etc.; for `managed === 'stack'`: a "Manage in stack →" link to the stack.
- Order: problems first, then running, then stopped; name breaks ties.
- Stop and Restart open a `ConfirmDialog` naming the container. Buttons are soft-disabled with a reason while Docker is unreachable or an action on that row is in flight. Results: success toast; failure toast with the server message (409 explains the stack ownership).
- Refresh: list on `/api/events` `change`/resync (debounced 300 ms, max 1 s; resync immediate); the list again every 10 s while the tab is visible (CPU/mem/restarts), paused while hidden, immediate fetch on visible. No other timers.
- States: loading spinner in the card; error banner + Retry when nothing loaded; Docker-down keeps the last list (global banner explains); empty: "No containers on this host."

### 5.3 Container page (`web/src/routes/ContainerDetail.svelte`)

- Resolves `:name` against the list (`GET /api/containers`); not found → `EmptyState` "Container not found" with a link to Containers. If it disappears while open (destroy event): "This container no longer exists" with a Back link.
- Header row 1: mono `h1` name, `StatusBadge`, health, muted meta `image · stack <link>` / `project <p> (unregistered)` / `not in a stack`; right side `role="group"` actions: non-stack → Start/Stop + Restart (same confirm and disabled rules as the list); stack → secondary **Open stack** button and the muted note "Managed by stack <name>; use the stack's actions." Row 2: full id in mono + `CopyButton` ("Copy id").
- Tabs (`?tab=`; Details default): **Details** card `<dl>`: State & status, Started / Finished + exit code, Restart policy, Restarts 24h, Ports (chips), Networks (`name → IPv4`), Mounts (`type source → destination (ro)`), Labels (collapsed `<details>`, compose labels first); plus a **Resources · now** box (CPU %, memory used / limit, from the list's latest values; `—` when null). Data from `GET /api/containers/{id}`, refetched on a `change` for this container's project or on resync. **Logs**: `LogsViewer` with a new `source` prop (`{kind:'stack', name} | {kind:'container', id}`); the container source hides the service selector; everything else unchanged.
- The page opens no SSE except the logs stream while the Logs tab is active; no metrics live stream.

### 5.4 Links from existing screens

- Stacks dashboard: the Containers host tile links to `/containers`.
- Docker events feed rows, Top consumers rows, and alerts with a container and no stack link to `/containers/<name>`.
- Stack detail: container names in the aside and in service cards link to `/containers/<name>`.

### 5.5 Accessibility and responsive

As the v2 spec §7 and §9: icon-only controls carry `aria-label` and `title`; status never by colour alone (badge text, health label); focus moves to `h1` after navigation; no horizontal page scroll at 375 px (table scrolls inside its card above 720 px, cards below).

## 6. Error handling

| Condition | Behaviour |
|---|---|
| Docker unreachable | List 503 (UI keeps the last list, global banner); actions 503 and buttons disabled with "Docker is unreachable"; details 503 → page shows the error banner with Retry |
| Unknown container id | 404 |
| Action on a stack container | 409 with the stack name; UI never offers it, the server refuses it anyway |
| Docker refuses an action | 502 with Docker's message, shown verbatim (as text) in the toast |
| Container removed while its page is open | "This container no longer exists" + Back; a logs stream ends with the existing `ended` state |
| Sampler has no value yet | CPU/memory `—` |

## 7. Security

- Session required on every route; actions are JSON-only POSTs (existing CSRF model).
- The stack-ownership check runs server-side on every action, against the registry.
- Details exclude environment, command lines and host-config internals; `docs/security.md` gains a paragraph: authenticated users can read any container's logs and start/stop/restart containers that are not in a registered stack.
- Untrusted strings (names, images, labels, Docker error messages, log lines) are only interpolated as text; no `{@html}`.

## 8. Testing

Test-first throughout (user rules).

**Go:**
- `engine`: POST helper and `ContainerAction` against the fake socket (path, `t=10`, 204/304/404/409/500 mapping); `InspectContainer` decoding from a fixture (env present in the fixture, absent from the result).
- `metrics`: sampler samples a non-compose running container into `Latest()` but `InsertBatch` stores only compose samples.
- `api`: list join (`managed` stack/compose/none, stack name mapping, Latest and restarts merged, ports formatting/dedup, nulls); 2 s cache + single-flight; 503 while disconnected; details mapping (no env key in the body), 404; action guard (409 for a stack container, 200 + fresh row otherwise, 502 on Docker refusal, 503 Docker down, 404 unknown); container logs streams one container with resume (shared helper; existing stack logs tests unchanged); session required on every route.

**Vitest:**
- List: chips and counts, search fields, problems-first order, per-row actions by `managed` (stack rows show the link, not buttons), confirm dialogs, disabled reasons, success and 409/502 toasts, refetch on change, visibility pause.
- Container page: not found, header and meta per `managed`, actions vs "Open stack", tabs and `?tab=`, details rendering (no env), "no longer exists", `LogsViewer` container source hides the service selector and opens `/api/containers/{id}/logs`.
- Shell: rail item and current state, breadcrumbs, titles. Links: dashboard tile, events feed, top consumers, alerts, stack detail container names.

**E2E (`cmd/stackstead/e2e_test.go`, with Docker):** start a throwaway `busybox` container with `docker run -d` (labelled for cleanup); `/api/containers` lists it with `managed:"none"`; stop then start it via the API (200); the same action on the smoke stack's container returns 409; its logs SSE delivers a line; clean up the container in `t.Cleanup`.

**Visual:** headless-Chrome screenshots of `/containers` and `/containers/<name>` (both tabs) at 1280 and 375 px, dark and light.

## 9. Documentation

- `docs/api.md`: the five routes, fields, `managed` semantics, caching, error codes, the sampler change (Top consumers now includes non-compose containers).
- `docs/security.md`: §7 paragraph.
- `README.md`: one feature-list line (host-wide Containers screen); the user's header, badges and table stay unchanged.

## 10. Delivery

One implementation plan, **Plan 9** (`docs/superpowers/plans/2026-10-15-stackstead-09-containers.md`), about 8–10 tasks: backend first (engine POST + inspect, sampler, list endpoint, details, actions, container logs), then frontend (shell + routes, list page, container page, links), then e2e, docs and the visual pass.
