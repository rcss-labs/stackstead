# API and observability reference

Everything the web UI does goes through this JSON API, so you can script it
too. Back to the [README](../README.md).

## API overview

All routes except health, setup and login need a session cookie. Errors are
`{"error": "..."}`, with `"fields": [{field, message}]` for validation. Errors
from connected servers also carry a `"code"`; see [Connected servers](#connected-servers).

| Method and path | Purpose |
| --- | --- |
| `GET /api/health` | liveness, no authentication: `{"ok": true, "peering": 1}`; `peering` is the connected-servers protocol version, so a server without it predates the feature (used by installers and monitors) |
| `GET /api/stacks` | list stacks with status |
| `POST /api/stacks` | create with `{name, services: [{service, config, extra?, dockerfile?}], extra?}` or legacy `{name, service, config}`; see [Managed Dockerfiles](#managed-dockerfiles) |
| `POST /api/stacks/import` | register an existing compose file without modifying it |
| `GET /api/stacks/{name}` | detail with services and containers |
| `GET`/`PUT /api/stacks/{name}/compose` | raw YAML, saved with the hash from the read (409 if stale) |
| `POST /api/stacks/{name}/services` | add a service with `{service, config, hash, dockerfile?: {content, hash}}`; returns stack detail (200) |
| `POST /api/stacks/{name}/services/preview` | preview an addition with `{service, config, hash}`; returns `{content, hash}` without writing |
| `DELETE /api/stacks/{name}/services/{service}?hash=` | remove a service from the file; returns stack detail (200) |
| `PUT /api/stacks/{name}/services/{service}` | structured service edit, optionally with `dockerfile?: {content, hash}` |
| `GET /api/stacks/{name}/services/{service}/dockerfile` | the service's Dockerfile: `{content, hash, exists, editable, path, reason?}`; see [Managed Dockerfiles](#managed-dockerfiles) |
| `PUT /api/stacks/{name}/services/{service}/dockerfile` | save `{content, hash}` (200 `{hash}`); the hash from the read, `""` for a file that does not exist yet |
| `POST /api/stacks/{name}/services/{service}/actions/up` | rebuild and recreate only this service, `up -d --build --no-deps`; returns an operation (202) with `service` set |
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
| `GET /api/containers/{id}/exec` | WebSocket: a shell or command inside a running container; see [Container exec](#container-exec) |
| `GET /api/networks`, `POST /api/networks` | list Docker networks; create a bridge, macvlan or ipvlan network (201); see [Networks](#networks) |
| `POST /api/networks/{id}/connect`, `POST /api/networks/{id}/disconnect`, `POST /api/networks/prune` | attach or detach a container (204); remove unused networks (200); see [Networks](#networks) |
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

### Managed Dockerfiles

A service that builds from a Dockerfile written in Stackstead uses the layout
`build: {context: ./<service>}`, with the file at `<stack>/<service>/Dockerfile`.
Create and add write that file; update and the Dockerfile route can also
write any editable Dockerfile.

Body and size limits: create accepts up to 32 MiB of JSON, add, update and
the Dockerfile `PUT` up to 8 MiB, and other bodies 1 MiB. Each Dockerfile is at
most 1 MiB, must not be empty (whitespace only counts as empty) and must not
contain NUL bytes. A violation is a 400 with a field error on the service's
`dockerfile` (create, add, update) or `content` (`PUT`).

Hashes:

- A read returns `hash` for the content that was read, and `""` when the file
  does not exist. `PUT` and update send that hash back; a mismatch is 409
  `Dockerfile changed on disk since it was loaded`. An empty `hash` means the
  file must not exist yet.
- Add with `dockerfile` at the managed path `<stack>/<service>/Dockerfile`
  (the service folder is absent or a real directory) may omit the hash, and
  overwrites any file a removed service left there. Anywhere else, add needs the
  hash rule above: `""` means the file must not exist, and an existing file is a 409.
- Update with `dockerfile` always checks the hash.

`GET …/dockerfile` answers `{content, hash, exists, editable, path, reason}`.
`path` is relative to the stack folder, with forward slashes, when the file is
inside it, and the absolute path otherwise. A service that does not build
(or is unknown) is 404. A Dockerfile is `editable` only when its build context
(joined with `dockerfile:`, or `Dockerfile`) resolves, with symlinks followed,
inside the stack folder. Otherwise `editable` is false and `reason` says why.
Stackstead does not guess at a file it cannot locate:

- `uses variables`, `remote build context`, `inline Dockerfile`, `uses extends`
  and `uses include`: the compose file does not name one fixed file.
- `outside the stack folder`: the file is elsewhere. Its content is shown
  read-only when it is a regular file of up to 1 MiB inside the allowed
  directories.
- `outside the allowed directories`: the file is elsewhere and cannot be read.
- `not a regular file` and `larger than 1 MiB`: the file inside the stack
  folder is a directory, link or FIFO, or is too large.

Writing a Dockerfile that is not editable is refused: 422 with the reason, or
403 for a path outside the stack folder. A save that fails validation returns
400, and an unknown or non-building service returns 404.

Create and add refuse build contexts that use variables
(`build context must not use variables`), because the value cannot be confined
before compose reads it. Only create carries `extra`, so only create sees
`additional_contexts` values, and it refuses those that use variables too.
Contexts that come from an `extra` are also refused when they are absolute
symlinks (`build context must not be a symlink; use the real path`); a context
set by the form is resolved instead, as before. On create, each `extra` must be
a single JSON object with no duplicate keys and may not set `services` or
`name`. Where an `extra` and the form both set a key, mappings are merged
recursively; a scalar against a scalar, or a list against a list, keeps the
form's value and drops the extra's; a different kind (for example a list where
the form sets a mapping) is an error.

Rebuilding one service:
`POST /api/stacks/{name}/services/{service}/actions/up` runs
`docker compose up -d --build --no-deps -- <service>`, so only that service is
rebuilt and recreated, and orphans are not removed. The operation keeps
`action: "up"` and sets `service`. It needs a session and a JSON body, which
may be `{}`. Any other action name is a 400 on `action`.

## Connected servers

Servers are connected in Settings → Connected servers. The trust model is in
[security.md](security.md#connected-servers) and the proxy setup is in
[install.md](install.md#connected-servers-behind-a-login-proxy).

Settings routes, with a session cookie:

| Method and path | Purpose |
| --- | --- |
| `GET /api/peers` | connected servers with status, last seen and last error |
| `POST /api/peers` | connect with `{url, token, trust, selfTrust, advertisedUrl}`; `trust` describes the other server's certificate (`caPem`, `pinSha256` or `plaintext`) and `selfTrust` describes this one; 201 |
| `POST /api/peers/probe` | `{url}` reads the certificate an address presents: `fingerprintSha256`, `systemTrusted` (publicly trusted, so no pin is needed) and `plaintextAllowed` |
| `POST /api/peers/tokens` | create a connection token, shown once; 201, `Cache-Control: no-store` |
| `DELETE /api/peers/{id}` | remove the connection; 204. The other server is told when it can be reached |
| `POST /api/peers/{id}/check` | check one server now |
| `GET`/`PUT /api/peers/self` | this server's display name and advertised address; a change is pushed to every peer |

Server-to-server routes. They take `X-Stackstead-Peer: <token or credential>`
and no session cookie:

| Method and path | Purpose |
| --- | --- |
| `POST /api/peer/pair` | pairs with a connection token; the token is used up only on success |
| `GET /api/peer/hello` | instance ID, name, version, advertised URL and stack count |
| `POST /api/peer/update` | the other server's new name or advertised address |
| `POST /api/peer/unpair` | the other server removed the connection |

A credential that is no longer paired gets `401 {"error": "...", "code": "peer_not_paired"}`.
A peer credential is refused with `403 {"code": "peer_forbidden"}` on
`/api/peers`, `/api/settings`, `/api/password`, `/api/logout`, `/api/setup`,
`/api/servers` and `/api/fleet`.

Forwarding, with a session cookie:

| Method and path | Purpose |
| --- | --- |
| `/api/servers/{id}/api/…` | any `/api/` call, forwarded to connected server `{id}` with its credential; event streams are forwarded as they arrive |
| `GET /api/fleet/stacks` | this server's stacks plus each connected server's, asked in parallel with a 3 second timeout per server. A server that does not answer is listed with its error instead of failing the call |
| `GET /api/events/recent` | the last 50 events, for the All stacks screen |

Forwarding turns a peer's answer into an error when it cannot be passed on:
an upstream `401` becomes `502 peer_unpaired`, a redirect becomes `502
peer_proxy`, and a path that would leave `/api/` gets `400 peer_bad_path`.
Other answers pass through unchanged.

Error codes:

| `code` | Meaning |
| --- | --- |
| `peer_unreachable` | the server could not be reached (502) |
| `peer_tls` | the TLS handshake or certificate check failed (502) |
| `peer_unpaired` | the other server no longer accepts the connection (502) |
| `peer_proxy` | the address answered with a redirect or a non-Stackstead page, often a proxy login (502) |
| `peer_version` | the servers' major.minor versions differ, so their screens are disabled (502) |
| `peer_wrong_instance` | the address now answers for a different server; forwarding stops (502) |
| `peer_old` | the other server predates connected servers (502) |
| `peer_unknown` | no connection has that ID (404) |
| `peer_bad_path`, `peer_not_paired`, `peer_forbidden` | see above |

Status checks run every 30 seconds while a browser session on this server
was active in the last 60 seconds, and every 5 minutes otherwise. With no
connected servers, nothing is checked.

## Image library

The Images screen (`/images`, `/images/{slug}`) manages images Stackstead builds
from an uploaded context and keeps on this server. Every image has a `slug`
(derived from its name at creation, never changed) and numbered, tagged
versions. A service uses one by the reference
`stackstead.invalid/<owner>/<slug>:<tag>`, where `<owner>` is the first 12 hex
characters of the owning server's instance ID. Stackstead builds the image
locally; `up` and `pull` make a referenced library image present first, and
the stack list reports `libraryUpdates`, the number of services whose image has
a newer tag. A service detail carries a `library` badge for such a reference.
A service that uses a library image must not set `pull_policy: always`: Compose
would try to pull it from the registry `stackstead.invalid`, so `up` and `pull`
refuse such a stack with a validation error naming the service. `missing` and
`never` work.

All routes need a session. Errors use the shape from [API overview](#api-overview).

| Method and path | Purpose |
| --- | --- |
| `GET /api/images` | the library: `{images, servers}` with each image's owner, versions and latest version |
| `POST /api/images` | create with `{name, source: "upload" \| "github", dockerfilePath, buildArgs, retention?, github?}` (201); a GitHub image needs a token first |
| `GET`/`PUT`/`DELETE /api/images/{slug}` | detail (versions, `revision`, `suggestedTag`, `tagHint`, `arch`), update, delete (409 `image_in_use` while a version runs) |
| `GET /api/images/{slug}/files` | the file manifest and current `revision` |
| `POST /api/images/{slug}/files` | upload `multipart/form-data`, one part per file named `f/<path>`; creates a revision (201) |
| `GET`/`DELETE /api/images/{slug}/files/{path...}` | read a file (up to 1 MiB) or remove it |
| `POST /api/images/{slug}/files/rename` | rename a file |
| `GET`/`PUT /api/images/{slug}/dockerfile` | the Dockerfile, saved with the `revision` from the read (409 `revision_conflict` if stale) |
| `GET /api/images/{slug}/revisions`, `GET .../revisions/{n}` | revision history and one revision |
| `POST /api/images/{slug}/revisions/{n}/restore` | restore a revision as a new one |
| `POST /api/images/{slug}/builds` | build with `{tag, buildArgs, saveArgs, ref}`; 202 `{version, operation}`; `ref` (GitHub images only) is a branch, tag or 40-hex commit, default the branch head |
| `GET /api/images/{slug}/github/head` | a GitHub image's branch head: `{commit, dockerfile, path}`; forwardable to a peer like the other image routes |
| `GET /api/images/{slug}/versions/{id}` | one version with its build log and `usedBy`; keyed by version ID, not tag |
| `DELETE /api/images/{slug}/versions/{id}` | delete a version by ID (409 `image_in_use` if a container uses it) |
| `GET /api/images/registries` | registries for the push-target picker: `[{id, name, kind, host}]` only; reachable with a peer credential |
| `POST /api/images/{slug}/push-targets` | add a push target `{registryId, repository, autoPush}` (201); a registry and repository pair is unique per image |
| `PUT`/`DELETE /api/images/{slug}/push-targets/{id}` | change a target (200, the PushTarget) or remove it (204); a changed registry or repository clears that target's push results |
| `POST /api/images/{slug}/versions/{tag}/push` | push the succeeded version with this tag (by tag, unlike the version routes) with `{target}`; 202 Operation (action `push`) |

A target with `autoPush` is pushed after every successful build, inside the
build's operation; a failed push never fails the build. An auto-push to a
registry that is not configured is recorded as a failed push ("registry not
configured") with a line in the build output. If the operation times out or is
cancelled during the auto-push, the operation ends `timed_out` or `cancelled`
and the output says "build succeeded; push interrupted"; the version's status
is authoritative. Adding, changing and removing push targets is recorded in the
action log as `image.push-target`. Push errors: 404 for an
unknown image, tag (a failed version has none) or target; 409
`registry_unconfigured` when the registry lacks credentials; 409
`build_running` while another operation on the image runs; 502
`registry_auth_failed` when the registry credentials cannot be obtained; 400 `fields` for a bad
repository or a version still building.

### Sharing between connected servers

A server lists, and can use, images owned by the servers it is paired with
(directly paired only; sharing does not reach a peer's peers). `GET
/api/images` merges this server's images with every active peer's: each entry
carries its `owner`, and `servers` reports each peer as `reachable`,
`unreachable` (with an `error`) or `old`. Peer answers are cached for 10
seconds and fetched in parallel, so a hanging peer never holds the list.

Peer routes are authenticated with the peer credential (`X-Stackstead-Peer`),
not a session, and are read-only:

| Method and path | Purpose |
| --- | --- |
| `GET /api/peer/images` | this server's images with their succeeded versions (newest first); no logs, build output, push targets or registry data |
| `GET /api/peer/images/usage` | the library references this server's stacks use: `[{ref, stack, service}]` |
| `GET /api/peer/images/{slug}/versions/{tag}/tar` | the image as a `docker save` archive (`Content-Type: application/x-tar`); the version's image ID is in `X-Stackstead-Image-ID`; 404 for an unknown or failed version |

Fetching copies a peer's version into this server's engine:

| Method and path | Purpose |
| --- | --- |
| `POST /api/images/fetch` | body `{ref}`; **204** when nothing is to do (this server owns the reference, or the image is already present with the owner's recorded ID), **202** with an Operation (action `fetch`) when a transfer started |

Errors: 400 `fields` for a reference that is not a library reference or whose
owner is not a connected server; 409 `image_not_built` when the owner has no
succeeded version with that tag; 409 `arch_mismatch` (see below); 409
`build_running` when another operation on the same image is running (the
operation key is shared with builds, pushes and retention); 502 `peer_unreachable` / `peer_old` /
`peer_proxy` and the other `peer_*` codes when the owner cannot be reached or
answers wrongly.

Transfer behaviour:

- The archive is streamed from the owner into the local engine; it is never
  spooled to disk.
- After loading, the local image ID must equal the ID the owner recorded (and
  sent in `X-Stackstead-Image-ID`); otherwise the image is removed again and
  the fetch fails with "fetched image does not match the owner's record".
- A 60 second idle watchdog cancels a transfer when no data arrives for that
  long ("transfer from X stalled"). The whole fetch is also bounded by
  `image_operation_timeout`; a fetch started by a stack `up` runs inside that
  operation, which then gets the longer of `operation_timeout` and
  `image_operation_timeout`.
- The archive is inspected while it streams: it may only name the requested
  reference. An archive whose `manifest.json`, `repositories` or `index.json`
  names any other tag, hides metadata (not at the archive root, not a regular
  file, over 1 MiB), or is not a valid tar is refused before the engine tags
  anything.
- Concurrent fetches of the same reference (a stack `up` and a manual fetch)
  share one transfer. A caller that gives up does not stop it while another
  still waits.
- `up` and `pull` fetch a missing peer-owned image themselves and print the
  progress in the operation log. Each fetch writes an `image.fetch` action-log
  entry (owner name and bytes; a failure adds the reason).

Architecture rule: a version records the architecture it was built for. A
fetch (or an `up`) on a server with a different engine architecture is
refused with 409 `arch_mismatch` ("built for amd64; this server is arm64")
before any data moves; images are never converted.

Old-peer rule: a paired server that predates image sharing answers the image
routes with a Stackstead JSON 404. It is shown as `old`, cannot own images
that are fetched, and counts as using nothing, so it never blocks a delete.
Any other failure (no answer, a proxy or login page, a plain-text 404) counts
as `unreachable`.

Deleting an owned image or version asks every active peer whether it uses it.
If a peer cannot be asked, the delete is refused with 409 `image_in_use`
rather than guessed: the body lists `usedBy` (each use with `server`,
`serverName`, `stack`, `service`) and `unreachable`, the servers that could not
be asked (`id`, `name`, ...). Delete again once they answer or are unpaired.

### Registries

Registries hold the credentials used to push. The routes are session-only and
on the peer denylist (`/api/settings`); a peer sees only the list under
`/api/images/registries`.

| Method and path | Purpose |
| --- | --- |
| `GET /api/settings/registries` | all registries as `RegistryView` |
| `POST /api/settings/registries` | create `{name, kind, hub?, ecr?}` (201) |
| `PUT /api/settings/registries/{id}` | update; the kind cannot change |
| `DELETE /api/settings/registries/{id}` | delete (204); its push targets are removed with it |
| `POST /api/settings/registries/{id}/test` | check the connection: `{ok, host, message}` |

`kind` is `dockerhub` (`hub: {username, token}`) or `ecr`
(`ecr: {region, mode, accessKeyId, secretAccessKey, sessionToken, profile, accountId?}`).
ECR `mode` is one of:

- `static`: `accessKeyId` and `secretAccessKey` (and an optional `sessionToken`)
  are stored.
- `role`: credentials come from the instance role (IMDS); only `region` is
  needed.
- `profile`: the named profile in the AWS shared credentials files. Only
  static-key profiles work (`aws_access_key_id`, `aws_secret_access_key`,
  optional `aws_session_token`); a profile using `role_arn`, `sso_*` or
  `credential_process` is refused with a clear error.

Secrets are write-only: a view carries `configured` and, for static ECR, the
non-secret `accessKeyId`, never a token or secret key. On update a blank (or
`[redacted]`) secret keeps the stored one. A fresh ECR registry needs no account
or host: the host is discovered from AWS on its first test or push and saved
without overwriting operator-set values. On update, an omitted `accountId`
keeps the stored one (including a discovered one), an empty string clears it
(and the host derived from it), and 12 digits replace it. A failed test answers 200 with
`ok: false` and a scrubbed `message`. Errors: 400 with `fields`, 404 unknown
registry, 409 for a taken name, 409 `registry_unconfigured` when settings are
missing.

### GitHub images

An image with `source: "github"` and `github: {repo, branch, path}` (`repo` is
`owner/name`, `path` a directory in the repository, empty for the root) is
built from a GitHub tarball instead of an upload. A GitHub token is required
for creating, updating, building and reading the head of such an image; without
one the routes answer 409 `github_unconfigured`. The token is the only
credential and applies to every GitHub image on this server.

| Method and path | Purpose |
| --- | --- |
| `GET /api/settings/github` | `{configured, login}`; the token is never returned |
| `PUT /api/settings/github` | set the token with `{token}`, or clear it with an empty token; answers the same `{configured, login}` |
| `POST /api/settings/github/test` | check the stored token: `{ok, login, message}`; a failed check answers 200 with `ok: false` and a scrubbed `message` |

The settings routes are session-only and on the peer denylist
(`/api/settings`). A bad `github.repo`, `github.branch` or `github.path`
answers 400 with that field name; a bad or unknown build `ref` answers 400 with
the field `ref`. GitHub images are read-only here: file, Dockerfile, revision
and rename routes answer 400 `GitHub images are read-only`, and they have no
revisions.

Every build resolves the branch (or `ref`) to a commit and records the full
40-hex commit on the version; building at a 40-hex `ref` makes no lookup. A
GitHub image never auto-pushes: a push target is used only by an explicit push.

### Default image retention

| Method and path | Purpose |
| --- | --- |
| `GET /api/settings/images` | `{retention: {keepLast, keepDays}}`; `{0, 0}` (keep everything) until one is saved |
| `PUT /api/settings/images` | set the default with the same body; answers the stored value |

The default is given to an image created without its own `retention`; changing
it never rewrites existing images. Both values are integers from 0 (rule off):
`keepLast` at most 1000, `keepDays` at most 3650. Anything else answers 400 with
the field `retention`. The same bounds apply to an image's own `retention` on
create and update (400 `retention`); a stored value above a bound still loads.
The route is session-only and on the peer denylist (`/api/settings`).

### Image retention

Each image has `retention: {keepLast, keepDays}`; 0 turns a rule off, and both
0 keeps everything. Keepers are decided as follows:

- `keepLast` ranks succeeded versions only, newest first; the newest
  `keepLast` are kept.
- `keepDays` keeps any version, succeeded or failed, that started within that
  many days.
- A failed version is also kept when it is newer than the oldest succeeded
  version `keepLast` keeps, so a burst of failed builds never pushes good
  versions out.
- The newest succeeded version is always kept, and so is a version that is
  still building.

A version in use is never removed. "In use" covers the owner (containers of
stacks on this server) and its direct peers; a peer running an old Stackstead
without the usage route counts as zero. If any peer cannot be reached, that
image's run is skipped and nothing is removed, so a version a peer needs is
never lost to an outage.

Retention runs after each successful build of the image, and a background sweep
runs 5 minutes after the server starts and then every 6 hours, over every image
with a non-zero policy. A sweep that finds the image busy (a build, push or
fetch) retries on the next run. Each removed version is logged as `image.retain`.

Removal is local only: it untags the version on this server's Docker, deletes
its row and build log, and never touches a registry, so versions already pushed
stay there. Revisions are never removed. Retention only cleans up on the server
that owns the image.

### Limits

| Setting | Default | Environment |
| --- | --- | --- |
| `image_max_file_bytes` | 134217728 (128 MiB) | `STACKSTEAD_IMAGE_MAX_FILE_BYTES` |
| `image_max_context_bytes` | 1073741824 (1 GiB) | `STACKSTEAD_IMAGE_MAX_CONTEXT_BYTES` |
| `image_operation_timeout` | `60m` | `STACKSTEAD_IMAGE_OPERATION_TIMEOUT` |

The byte limits are integers of at least 1048576; an upload over either limit
answers 413 `image_too_large`. `image_operation_timeout` bounds image builds,
pushes and fetches. Stack operations keep `operation_timeout`, except a stack
or service `up` or a stack `pull` of a stack that uses library images: its
operation, which fetches missing images first, is bounded by the longer of
`operation_timeout` and `image_operation_timeout`. Paths are
cleaned and relative, with no `..`, leading `/`, backslash or NUL byte,
segments of at most 255 bytes and 4096 in total. A Dockerfile is non-empty, at
most 1 MiB and has no NUL byte. Uploads are staged under
`DataDir/images/<slug>/tmp-upload-*` while they are checked and removed
afterwards; content blobs live under `DataDir/images/<slug>/blobs/`.

### Error codes

| Status and `code` | Meaning |
| --- | --- |
| 409 `image_in_use` | the image or version is used by a stack service here or on a connected server, or a connected server could not be asked; the body carries `usedBy` and `unreachable` |
| 409 `build_running` | another operation (build, push, fetch or retention) is running for this image; try again shortly |
| 409 `tag_taken` | the tag exists on a non-failed version |
| 409 `revision_conflict` | the file set or Dockerfile changed since the `revision` you sent |
| 409 `image_not_built` | the referenced tag has no built version |
| 409 `arch_mismatch` | the owner built the version for another architecture than this server's engine |
| 409 `registry_unconfigured` | the push target's registry is missing credentials |
| 502 `registry_auth_failed` | the registry's credentials could not be obtained (unreachable, keys rejected, no role); the message is scrubbed of secrets |
| 409 `github_unconfigured` | no GitHub token is stored |
| 409 `github_unauthorized` | GitHub rejected the stored token (never 401, which would end the web session) |
| 429 `github_rate_limited` | GitHub's rate limit was reached; try again later |
| 502 `github_unreachable` | GitHub could not be reached or the download stalled |
| 413 `image_too_large` | a file or the context is over its limit |

### Operations

Builds run as operations in the shared operations list under the key
`image/<owner>/<slug>` (action `build`), so the top bar and toasts label them
`build · image <slug>`. One build runs at a time per image.

## Backups

Backups archive this server's database, settings and stack files, plus the
volumes and images of the stacks you tick. All routes need a session, and none
is under `/api/settings`, so a connected server can forward them.

| Method and path | Purpose |
| --- | --- |
| `GET /api/backups/config` | the schedule, local folder and S3 bucket. `s3.secretKeySet` says whether a secret is stored; the secret itself is never returned. `nextRun` is when the schedule fires next, and is absent when it is off |
| `PUT /api/backups/config` | save the same shape. `400` with the validation messages when it is invalid. An omitted or empty `s3.secretKey` keeps the stored one. Returns the saved configuration |
| `DELETE /api/backups/config/s3-secret` | remove the stored S3 secret; 204 |
| `POST /api/backups/s3/test` | writes and deletes a small object with the `s3` settings in the body, using the stored secret when the body has none; 204, or `502` with the reason |
| `GET /api/backups/stacks` | every stack with `volumes`, `images`, `stopDuring` and its `mounts` (`kind`, `name`, `target`, `bytes`; `bytes` is -1 when unknown, as for bind mounts). A stack never saved has both extras off and `stopDuring` on |
| `PUT /api/backups/stacks/{name}` | `{volumes, images, stopDuring}` for one stack; 204, or 404 for an unknown stack |
| `POST /api/backups/run` | start a backup now with a `{}` body. 202 `{"operation": {...}}`; it runs as operation `backup` on key `host:backup`. 409 `backup_running` when a run is in progress |
| `GET /api/backups/runs?limit=` | recent runs, newest first, with per-item results; `limit` is 1 to 100 and defaults to 20 |
| `GET /api/backups/archives` | archives at every enabled destination from all hosts, newest first: `dest`, `name`, `hostSlug`, `createdAt`, `size`. A destination that cannot be read is left out unless all of them fail |

Scheduled runs use the host's local time zone. A scheduled run that finds a
backup still running is skipped, not queued.

## Setup

These routes work without a session while no admin account exists. They are
allowed from this machine (a direct loopback connection with no forwarding
headers), or from anywhere with the setup token printed in the service log.
Once an admin exists they answer `409`. A connected server can never reach
them.

| Method and path | Purpose |
| --- | --- |
| `GET /api/setup/status` | `{"needsSetup": true}` while no admin exists |
| `POST /api/setup` | create the first admin with `{username, password, setupToken}`; 204 |
| `POST /api/setup/restore/upload` | the body is the archive (`Content-Type: application/gzip` or `application/octet-stream`, setup token in the `X-Stackstead-Setup-Token` header). It is streamed to a temporary file and renamed to `<data_dir>/restore/upload.tar.gz` (mode 0600, no size limit) when complete, replacing an earlier upload. Returns `{"source": {"kind": "upload"}}`. 409 `upload_running` while another upload is in progress, 409 `restore_running` during a restore. Completing setup the normal way (`POST /api/setup`) deletes an unused upload |
| `POST /api/setup/restore/inspect` | `{source, setupToken}`; returns the archive's `manifest`, the `conflicts` it would meet on this host (`kind`, `name`, `action`: `overwrite`, `recreate` or `keep`) and their `conflictsDigest` |
| `POST /api/setup/restore/start` | `{source, startStacks, confirmedDigest, setupToken}`, where `confirmedDigest` is the `conflictsDigest` the user confirmed. It answers once the archive has been checked: 202 `{"restoreToken": "...", "operation": {...}}`. The restore runs as operation `restore` on key `host:restore`, and when it has been staged the server restarts itself to install the restored database |
| `GET /api/setup/restore/progress?token=` | the restore operation's output as server-sent events, in the same format as `/api/operations/{id}/stream`. It needs the `restoreToken` instead of the setup gate, so it keeps working after the gate closes. The stream ends when the server restarts; the client then polls `/api/setup/status` until the server is back, and signs in as the restored admin |

A `source` is `{"kind": "path", "path": "/abs/archive.tar.gz"}`,
`{"kind": "upload"}` (the uploaded archive), or `{"kind": "s3", "s3": {bucket,
key, region, endpoint, pathStyle, accessKeyId, secretKey}}` where `key` is the
full object key. The secret key is used for the request and never stored or
returned.

Errors from inspect and start:

| Status and code | Meaning |
| --- | --- |
| 409 `conflicts_changed` | the host changed since the inspection; inspect again and confirm the new conflicts |
| 409 `restore_running` | a restore is running, or one has been staged and the server is about to restart |
| 409 `restore_pending` | a staged restore is waiting for a restart |
| 409 `upload_running` | the `upload` source is still being uploaded |
| 422 `archive_newer` | the archive was written by a newer Stackstead |
| 422 `archive_damaged` | the archive is truncated, malformed or does not match its manifest |
| 400 | the source cannot be read, such as a wrong path, bucket or key |

On the first start after a restore the server installs the restored database
(the previous one is kept next to it as `stackstead.db.bak-<UTC time>`), pauses the backup schedule
and starts the stacks that were running at backup time when `startStacks` was
set. A stack that fails to start is recorded in the action log as
`backup.restore` by `system`. The pause and the stack starts are recorded in
`<data_dir>/restore/post-apply.json` before the restore marker is removed, so
a pause that fails, or a crash before the stacks were started, is retried at
the next start. Until the pause has succeeded the backup scheduler does not
run. If the staged restore cannot be installed, or that file cannot be read,
the server refuses to start and says why. An upload still in progress when
setup completes is discarded when it ends (409).

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
  recreated container has a new id and starts from `tail`, and log drivers
  with coarse clocks only make the ordinal do more work. Resume
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

### Container exec

`GET /api/containers/{id}/exec` upgrades to a WebSocket and runs a command
inside a running container, on any container (managed or not). `{id}` is a
container id, never a name. Exec is on by default and is turned off with
`exec_enabled: false`.

Query parameters, all read before the upgrade:

| param | meaning | default |
|---|---|---|
| `cmd` | repeated; the argv of the command (at most 64 args, 4 KiB in total, no NUL) | empty: auto shell |
| `user` | `user[:group]` passed to Docker (at most 64 characters, no whitespace, control or NUL characters) | the image default |
| `tty` | `1` or `0` | `1` |
| `cols`, `rows` | initial terminal size (tty only), 1 to 65535 | 80 x 24 |

With no `cmd` the server runs
`/bin/sh -c 'if [ -x /bin/bash ]; then exec /bin/bash; else exec /bin/sh; fi'`.

All checks run for any GET, upgrade or not, in this order: credential (401),
`exec_disabled`, `bad_origin`, container id and existence, running state,
query validation (400), session cap. A request that passes every check but is
not a WebSocket upgrade gets `400 {"error":"websocket upgrade required"}`;
clients use a plain GET to read the reason for a refused handshake. Refused
attempts are not logged.

| status | `code` | meaning |
|---|---|---|
| 401 | | no or invalid session or server credential |
| 403 | `exec_disabled` | `exec_enabled` is false on this server |
| 403 | `bad_origin` | the Origin header's host[:port] is not this server's `Host` (scheme is not compared; a missing Origin is allowed) |
| 404 | `container_not_found` | unknown or ambiguous id; a bare 404 without the code means the server has no exec route (older version) |
| 409 | `not_running` | the container is not running |
| 429 | `exec_limit` | `exec_max_sessions` concurrent sessions are open |

Wire protocol. Binary frames carry data: the first byte is the stream id, the
rest is payload. `0` is stdin (client to server), `1` stdout and `2` stderr
(server to client); with a TTY everything comes back as `1`. Text frames are
JSON control messages:

| direction | message |
|---|---|
| client to server | `{"type":"resize","cols":N,"rows":N}`, `{"type":"eof"}` (half-close stdin), `{"type":"pong"}` |
| server to client | `{"type":"ping"}` every 20 s; the client answers with `pong`, and the server closes if none arrives within 60 s |
| server to client | `{"type":"exit","code":N}` once, when the process ends |
| server to client | `{"type":"error","message":"..."}` for failures after the upgrade, for example `no shell found in this container; pass a command` |
| server to client | `{"type":"idle_warning","seconds":N}`, then `{"type":"closed","reason":"idle"\|"session_expired"\|"shutdown"}` before a close the process did not cause |

Activity for the idle timeout (`exec_idle_timeout`, default 30m, `0s`
disables) is any data frame in either direction. On every ping tick the
server re-validates the credential and closes with `session_expired` if it is
gone. Each session writes a `container.exec` start and end entry to the action
log (see [Security](security.md#container-exec)).

### Networks

`GET /api/networks` returns an array, cached for 2 seconds. Each network has
`id` (12 hexadecimal characters), `name`, `driver`, `scope`, `subnets`,
`gateways`, `ipRange`, `parent`, `ipvlanMode`, `internal`, `builtin`, `project`,
`stack`, `containers`, `fullId` (the full 64-character ID), `attachable`,
`ipv6` and `created` (RFC 3339 UTC, or an empty string when Docker reports
none). The arrays are never null; `gateways[i]` corresponds
to `subnets[i]`. Each container has `name`, `ipv4` (a bare IPv4 address,
or an empty string when unavailable), `id` (12 hexadecimal characters), `mac`
(empty when none), `stack` (the registered stack owning its Compose project,
else the project name, else empty), `state`, `health` and `exitCode` (null
when unknown). `stack`, `state`, `health` and `exitCode` come from the
container list; if that is unavailable the network list still succeeds and
those fields are empty or null.
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

`POST /api/networks/{id}/connect` and `POST /api/networks/{id}/disconnect`
take `{"container": "<name or id>"}` and return 204. The network is resolved
like removal. Connect is refused with 409 for `host` and `null` networks
(`containers can't join the <name> network at runtime`); disconnect is not
forced. An empty `container` is 400, an unknown network is 404, and Docker
refusals return 409 (or 404 when Docker reports a missing container) with
Docker's message.

`POST /api/networks/prune` removes all unused networks and returns 200
`{"removed": [...]}`: the sorted names, never null. All three invalidate the
network and container caches.

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
of 32; Docker holds each stats call for about 2 s, so a tick takes roughly
`ceil(containers / 32) * 2 s` and 10 s fits about 150 running containers.
Each sample carries the time Docker took the reading. Beyond that a tick can
overrun and the next ticks are skipped, so samples get sparser. If
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
