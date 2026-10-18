# Stackstead Image updates — Design

**Date:** 2026-10-18
**Status:** Draft, awaiting review. Adds an **image update center**. Stackstead detects when the image behind a stack service has a newer build (new digest for the same tag) or a newer version tag, shows what it can honestly say about the change, and updates one service or a whole stack: pull, recreate, wait for the healthcheck, and roll back to the previous image on failure. Builds on `2026-10-14-stackstead-v1-design.md` (operations, compose as the source of truth, host `docker login`), `2026-10-15-stackstead-ui-v2-design.md` (shell, Operations tab, `/api/events`) and `2026-10-15-stackstead-containers-design.md` (compose owns stack containers). Integrates with the sibling specs `2026-10-18-stackstead-storage-inventory-design.md` (rollback images are protected there), `2026-10-18-stackstead-notifications-design.md` and `2026-10-18-stackstead-config-history-design.md`.

## 1. Purpose

Today the only way to update an image is the stack's **Pull** then **Up**. That pulls every service, recreates whatever changed, gives no hint beforehand that anything changed, and leaves no way back if the new image is broken. A broken `:latest` on a homelab box usually means a manual hunt for the old digest, which `docker image prune` may already have deleted.

Success criteria:

- Within one check interval (default 6 h) of a publisher pushing a new build of the tag a service uses, `/updates` and the stack's service card show **Update available**. This holds for Docker Hub, GHCR and private registries the `stackstead` user is logged in to.
- For a service on a version tag (`1.27`, `v2.3.1`, `1.27-alpine`), newer versions with the same shape are listed as patch / minor / major.
- For an available update the UI shows the current and new digest, both creation dates, and the OCI version and revision labels when the image has them. It also gives a link to the source or release notes built from `org.opencontainers.image.source`, and says plainly when there is nothing more to show.
- **Update** on a service runs as a stack operation with live output: it pulls, recreates only that service and waits until its healthcheck reports `healthy`. If the container turns `unhealthy`, exits or times out, it is recreated from the exact previous image, without network access, even after `docker image prune`.
- A whole-stack update recreates services one at a time in `depends_on` order. If any service fails, every service updated in that run is rolled back.
- A service with no healthcheck is clearly labelled "No healthcheck: only checks that the container starts" before and after the update.
- Checks stay within Docker Hub's limits: a check uses manifest `HEAD` requests, which Docker documents as not counting against the pull limit. A host with 30 services on 20 distinct images makes at most about 20 `HEAD` requests plus tag-list pages per interval.
- Nothing is updated without an operator clicking **Update**.

## 2. Scope

### In
- Backend:
  - new package `internal/registry` (stdlib-only Docker Registry HTTP API v2 client: reference parsing, auth challenges, credential lookup incl. credential helpers, manifest `HEAD`/`GET`, tag list, config blob);
  - new package `internal/updates` (checker, scheduler, store, updater with health gate and rollback, `RollbackImages` provider);
  - `engine.Client`: image ID on the container list, `TagImage`, healthcheck presence in `InspectContainer`, registry config from `/info` (reuses the storage spec's `Images`/`InspectImage`/`RemoveImage`);
  - one SQLite migration; routes under `/api/updates` and `/api/stacks/{name}/...`; an `updates` message on `/api/events`.
- Frontend: rail entry and route `/updates`, badges on the rail and stack list, chips and actions on stack detail service cards, update dialog, changes panel, **Settings › Updates**.
- Tests (fake registry), e2e smoke with a local registry, docs (`docs/api.md`, `docs/security.md`, `docs/install.md` credentials note, README feature line).

### Out
- **Automatic updates.** No policy applies an update unattended (User Q3). The health gate and rollback are built so an auto policy could be added later.
- Containers outside registered stacks. Compose owns stack containers (Containers spec), and plain `docker run` containers have no file to recreate from.
- Services whose compose `image:` is pinned to a digest (`repo:tag@sha256:…`). They show as **pinned, not checked** (User Q8).
- Services with only `build:` (no registry image), and images with no repository digest (built or loaded locally). Both are shown as not checkable.
- Fetching release-note text from GitHub or any other API (User Q13): link only.
- Vulnerability scanning, SBOMs, signature verification (cosign / Notary).
- Registry mirrors (future work). Checks ignore the daemon's mirrors and go to the image's own registry (§4.6). A host that reaches Docker Hub only through a mirror gets `network` errors for Hub images. A `registry_mirrors` setting can be added later.
- Registry credential management in the UI: credentials stay the host's `docker login` (v1 §2).
- CLI commands for updates. They would follow the CLI spec's patterns in a later plan.
- Dashboard alerts for available updates (User Q15). Alerts are for problems; failed update operations already notify through `op_failed` (notifications spec).

## 3. Key decisions

The TypeSafe skill named in the global rules cannot run here (the installed `typesafe-ai` skill is an AI-feature SDK, not a decision scorer), so the decisions are argued in writing. Choices marked "User (Qn)" were put to the user as options with trade-offs on 2026-10-18; the rest follow from existing code or spec conventions.

| Decision | Choice | Reason |
|---|---|---|
| Registry access | Own stdlib client in `internal/registry` | User (Q1). Same approach as `internal/engine` (raw API, only the fields we read); no new dependency, small binary. Rejected: `go-containerregistry` (large dependency tree), daemon `/distribution` endpoint (splits logic, tag lists still need a client). |
| What counts as an update | Same-tag digest change (one-click update), plus newer semver-shaped tags (update edits the compose file) | User (Q2). Covers both `:latest`-style tags and pinned-version tags. |
| Policies | Per service: `off` (not checked), `notify` (shown, no Update button), `manual` (shown with Update button). Default `manual`. No auto | User (Q3). |
| Policy storage | SQLite, keyed by stack and service | User (Q11). Never writes Stackstead settings into the user's compose file. |
| Previous image | Before pulling, tag the current image `stackstead-rollback/<project>/<service>:previous` and record its image ID and repository digest; one per service | User (Q4). The tag keeps the image from `docker image prune` (dangling-only). If `prune -a` or a manual delete removes it anyway, rollback pulls `repo@sha256:<recorded digest>`. One image per service bounds disk use. |
| Rollback mechanism | Same-tag update: re-tag the retained image ID as the compose tag, then recreate with `--pull never`. Tag-change update: restore the previous `image:` value through `commit` (hash-checked), then recreate | Follows from Q4 and the v1 rule that the file is the truth: a same-tag rollback leaves the file untouched; a tag change is a file edit, so its undo is one too (and is recorded by config history). |
| Health gate | Wait for the container's healthcheck to report `healthy`. Fail on `unhealthy`, exit, restart or the gate timeout. **No healthcheck: no gate**, success once the container is running | User (Q5). Says plainly in the dialog, the service card and the operation output. |
| Rollback trigger | Automatic on gate failure by default; an **Auto-rollback** checkbox in the update dialog (API `autoRollback`, default `true`) turns it off for that run | User (Q6). |
| Stack-wide update | One service at a time in `depends_on` order. On the first failure (with auto-rollback on), every service updated in this run is rolled back in reverse order | User (Q7). The stack returns to a known previous state instead of a mix. |
| Digest-pinned compose refs | Not checked, shown as **pinned, not checked** | User (Q8). A pin is a deliberate choice. Bumping pins is out of scope. |
| Credentials | `$DOCKER_CONFIG/config.json` (`auths`, `credHelpers`, `credsStore`), with credential helpers run directly (no shell) | User (Q9). Without helpers, Docker Desktop on macOS (keychain) and `pass`/`secretservice` setups could not check private images. |
| Schedule | Every 6 h with ±10 % jitter; **Check now**, reusing a result checked in the last 5 min; interval configurable, `0` = off | User (Q10). |
| UI placement | `/updates` page, count badges on the rail item and stack rows, chips and actions on service cards, **Settings › Updates** | User (Q12). |
| "Show changes" | Digests, created dates, OCI labels (`version`, `revision`, `source`, `url`, `documentation`), and one link built from `source` | User (Q13). Honest about the limits (§5.5). |
| Skip this version | Per service, one skipped digest and/or tag in SQLite. A newer digest or tag shows again | User (Q14). |
| Dashboard alerts | None for available updates | User (Q15). |
| Execution | Updates and rollbacks are stack **operations** (`Ops.Start` actions `update`, `rollback`) | Follows from v1: one operation per stack (409 if busy), live output, cancel, Operations tab, `op_failed` notifications. |
| Recreate command | `docker compose up -d --no-deps --no-build --pull never <service>` | Follows from the Containers spec (compose owns containers): only the named service is touched. No `--remove-orphans`, unlike Up, so an update never removes other containers. |
| Compose version floor | Updates and rollbacks need Compose **v2.8.0** or newer. Older versions are refused with "Image updates need Docker Compose v2.8.0 or newer (found vX.Y.Z)" | User. `up --pull` was added in docker/compose#9474, merged 2022-07-27. It first shipped in v2.8.0: absent from v2.7.0's `docs/reference/docker_compose_up.yaml`, present in v2.8.0's with `"always"\|"missing"\|"never"`. Without `--pull never`, a rollback of a service with `pull_policy: always` would pull the new image again. Checks still run on older Compose. |
| Scaled services | The gate waits for every replica; the oldest replica's image counts as current | User. |
| Update history | Newest 50 runs per stack | User. |
| Push to the UI | `updates` message on the existing `/api/events` stream | Follows from the v2 rule of one SSE per tab. |
| Resolved image refs | From the storage spec's `composecli.Runner.ConfigJSON` on the stack's compose file set (cached by the set's file hashes and `.env`, storage §5.3) | Follows from storage §5.3: interpolation (`${TAG}`) is resolved exactly as compose does. No second compose parser. |
| Check scope | Registered stacks only; one check per distinct normalized reference and platform | Follows from the Containers spec and the rate limits. |

## 4. Detection (`internal/registry`, `internal/updates`)

### 4.1 Reference handling

```go
type Reference struct {
    Registry   string // "docker.io", "ghcr.io", "registry.example.com:5000"
    Repository string // "library/nginx", "linuxserver/jellyfin"
    Tag        string // "latest" when omitted
    Digest     string // "" unless pinned
}
func ParseReference(s string) (Reference, error)
func (r Reference) String() string     // normalized, e.g. "docker.io/library/nginx:1.27"
func (r Reference) APIHost() string    // "registry-1.docker.io" for docker.io
```

Normalization follows Docker's rules: no registry → `docker.io`; a single-path Docker Hub name gets `library/`; the first path element is a registry only if it contains `.` or `:` or is `localhost`.

Per service the checker reads the **resolved** `image` from `ConfigJSON`. It also reads the `image:` text in the base file through `composefile` to decide whether a tag change can be written: only when that text is a literal reference, not `${…}`, and no override or extra file sets `image:` for the service (compose-forms §7.4).

| Situation | Status |
|---|---|
| Policy `off` | `off` (no registry call) |
| Resolved ref has `@sha256:` | `pinned` (“pinned, not checked”) |
| Service has only `build:` | `build_only` |
| Service has no container yet | `not_deployed` (compares against the image the next Up would use, if present locally; else only newer tags) |
| Local image has no `RepoDigests` for this repository | `local_only` |
| Registry answered, digests match | `up_to_date` (plus `newerTags` if any) |
| Digest differs | `update_available` |
| Digest or tag equals the skipped one | `skipped` (with the skipped value) |
| Registry error | `error` with `errorKind`: `auth`, `not_found`, `rate_limited`, `network`, `protocol` |

### 4.2 Same-tag digest comparison

- **Local:** the container's image ID (new `engine.Container.ImageID`) → `InspectImage` → the `RepoDigests` entries whose repository matches the reference (normalized). This works with both the classic and the containerd image store: both record the digest the image was pulled by.
- **Remote:** `HEAD /v2/<repo>/manifests/<tag>` with `Accept` listing OCI index, Docker manifest list, OCI manifest and Docker v2 manifest. The digest comes from `Docker-Content-Digest`. If a registry omits that header on `HEAD`, the client falls back to `GET` and hashes the body (SHA-256).
- **Equal** when the remote digest is one of the local repository digests. If the remote is an index and no local digest matches, the checker `GET`s the index and compares the manifest for the host platform (`os/arch/variant` from engine `/info`, cached) as well. This covers images pulled by platform digest. Only then is it `update_available`.
- Multiple containers of one service (`scale`): the oldest image among them is the "current" one. Containers of one service never legitimately differ for long.

### 4.3 Newer version tags

```go
type TagShape struct { VPrefix bool; Parts int; Suffix string } // "v1.27.3-alpine" → {true,3,"alpine"}
type Version struct { Shape TagShape; Nums [3]int }
func ParseVersionTag(tag string) (Version, bool)
func NewerTags(current Version, tags []string) NewerTags
type NewerTags struct { Patch, Minor, Major string } // highest strictly greater per class; "" if none
```

- A tag is version-shaped when it matches optional `v`, 1–3 dot-separated integers, and optionally `-` plus a suffix. Candidates must have the **same shape**: same `v` prefix, same number of parts, identical suffix. So `1.27-alpine` only considers `1.x-alpine`. A prerelease such as `1.28-rc1` has a different suffix and is therefore never offered, and neither is a suffixed tag for an unsuffixed current tag.
- Classes: **patch** = same major and minor (3-part tags only), **minor** = same major, **major** = any. Each shows the highest candidate. A 1-part tag (`16`) only has `major`.
- Tags that are not version-shaped (`latest`, `stable`, `bookworm`) get no newer-tag search.
- The tag list is fetched with `GET /v2/<repo>/tags/list?n=1000`, following `Link` headers, capped at 20 pages. A cap hit is reported as `newerTagsTruncated: true` (Docker Hub's `library/node` has thousands of tags). It is fetched once per repository per check run, only when some service uses a version-shaped tag of it.
- A newer tag is offered for update only when the file's `image:` is a literal (§4.1). Otherwise the UI says "set through a variable; change it in `.env`".

### 4.4 Change details (only for an available update)

When a check first finds a new digest, the checker fetches the remote manifest for the host platform and its config blob (`GET /v2/<repo>/blobs/<digest>`, ≤ 1 MiB). From the blob it reads `created` and `config.Labels`. The local side comes from `InspectImage` (`Created`, `Config.Labels`). Results are stored with the check (§6), so the details are fetched once per new digest, not on every page view. For a newer tag, the same is fetched lazily when the operator opens its changes panel (`GET …/changes?tag=`), then cached.

Labels read: `org.opencontainers.image.{version,revision,source,url,documentation,created,title}`, falling back to `org.label-schema.{version,vcs-ref,vcs-url,url}`.

### 4.5 Authentication

```go
type Credentials struct { Username, Password, IdentityToken string } // never logged, never serialized
type CredentialStore interface { Get(ctx context.Context, registryHost string) (Credentials, bool, error) }
func NewDockerConfigStore(configDir string, exec HelperRunner) CredentialStore
type HelperRunner func(ctx context.Context, helper, serverURL string) ([]byte, error)
```

- **Config location:** `$DOCKER_CONFIG/config.json`, else `~/.docker/config.json`. The systemd unit already sets `DOCKER_CONFIG=/var/lib/stackstead/.docker`, and `docs/install.md` already tells users to run `sudo -u stackstead -H docker login`. The file is re-read at the start of every check run, so a new login works without a restart.
- **Lookup order** (Docker's own): `credHelpers[host]`, then `credsStore`, then `auths[host]` (`auth` = base64 `user:pass`, or `identitytoken`). Docker Hub's key is `https://index.docker.io/v1/`.
- **Helpers:** run `docker-credential-<name> get` with the server URL on stdin. The binary is resolved via `PATH` and the name must match `^[a-z0-9][a-z0-9_-]*$`. There is no shell, a 10 s timeout and a 64 KiB stdout cap. Exit status non-zero with "credentials not found" means anonymous; other failures are `errorKind: auth` with the helper's stderr first line (never stdout).
- **Challenges:** a request that returns 401 is answered by its `WWW-Authenticate` header. For `Bearer realm=…,service=…,scope=…` the client gets a token from the realm: `GET` with Basic credentials if any, anonymous otherwise, or `POST` with `grant_type=refresh_token` when only an identity token exists. For `Basic`, it sends Basic. Tokens are cached in memory per registry and scope until `expires_in` (default 60 s) minus 10 s.
- **Realm safety:** credentials go to a realm only over HTTPS. Plain HTTP is allowed only when the registry itself is an allowed insecure registry (§4.6). Bearer tokens are only sent to the registry host that issued the challenge. Blob redirects to a CDN are followed without `Authorization` (Go's client drops it across hosts; the client also strips it explicitly).

### 4.6 Transport, limits and politeness

- HTTPS by default. Plain HTTP only for `localhost`, `127.0.0.0/8`, `::1`, and registries the daemon lists as insecure (`/info` `RegistryConfig.IndexConfigs[*].Secure == false`). This mirrors what `docker pull` would accept.
- `HTTPS_PROXY`/`NO_PROXY` are honoured (`http.ProxyFromEnvironment`).
- Daemon registry mirrors are **not** used for checks. Docker Hub checks go to `registry-1.docker.io` directly, which is documented. A host without direct internet access gets `errorKind: network`. Mirror support is future work (§2 Out).
- 15 s per request; at most 4 requests in flight overall and 2 per registry host.
- Body caps: manifest 4 MiB, tag list page 1 MiB, config blob 1 MiB, token response 64 KiB.
- **Rate limits:** checks use `HEAD` for the digest. Docker documents that `HEAD` manifest requests do not count as pulls. `GET`s happen only for the platform fallback and change details. `ratelimit-remaining` is recorded when present. A `429` (any registry) marks that registry `rate_limited` until `Retry-After`, or 1 h without one. The rest of the run skips it, and those services keep their previous result with `errorKind: rate_limited`. Logging in raises Docker Hub's limits, and the Settings page says so.
- User agent: `stackstead/<version>`.

### 4.7 Schedule and caching

```go
type Scheduler struct { /* … */ }
func (s *Scheduler) Run(ctx context.Context)                 // added to startBackground in cli/serve.go
func (s *Scheduler) CheckNow(stack string) (started bool)     // "" = all stacks; joins a running check
```

- **Interval:** setting `updates.interval_seconds`, default 21600 (6 h), allowed 3600–604800 or `0` (off). Each wait is the interval ±10 % uniform jitter.
- **No burst on restart:** the next run is computed from `updates.last_run_at` in `settings`. The first run after start is never earlier than 2 min after start.
- **One run at a time.** A run collects all registered stacks' services, groups them by normalized reference and platform, and checks each group once. Each group's result is written to `image_checks` as soon as it arrives.
- **Check now** (all or one stack) starts a run immediately, or joins the running one. Within it, a reference checked in the last 5 min reuses its stored result.
- Reads never call a registry. `GET /api/updates` joins the stored checks with live local state (containers, `InspectImage`, policies, rollback records) behind a 2 s `flightCache`, like the containers list.
- An `updates` event `{checking, seq}` is published on `/api/events` when a run starts, when it ends and when an update or rollback operation finishes. Clients then refetch.

## 5. Update flow (`internal/updates`)

### 5.1 Request

```go
type UpdateRequest struct {
    Stack        string
    Services     []ServiceTarget // empty = every service with status update_available and policy manual
    AutoRollback bool            // API default true
    Hash         string          // required when any target changes the tag
}
type ServiceTarget struct { Service, Tag string } // Tag "" = same tag, new digest
func (u *Updater) Start(ctx context.Context, req UpdateRequest) (stacks.Operation, error)
func (u *Updater) Rollback(ctx context.Context, stack, service string) (stacks.Operation, error)
```

**Refusals before the operation starts:**
- 404 for an unknown stack or service.
- 409 when the policy is `off` or `notify` ("policy is notify: change it to manual to update here").
- 409 for a `pinned`, `build_only` or `local_only` service.
- 409 with `ErrBusy` when another operation is running on the stack.
- 409 for a stale `hash`.
- 400 for a `Tag` that is not one of the offered newer tags, or a tag change on a variable `image:`.
- 503 when Docker or compose is unavailable (`requireLifecycle`).
- 409 when Compose is older than v2.8.0, which has no `up --pull`: "Image updates need Docker Compose v2.8.0 or newer (found vX.Y.Z)". The version comes from the existing cached `Compose.Version`. Both `v2.8.0` and `2.8.0` forms are parsed, and any v3+ (for example v5.1.1) passes.

Compose commands run with the stack's compose file set and active profiles, as every lifecycle action does (compose-forms §7, §8). A tag change is refused with 400, as for a variable `image:`, when the service's `image:` is set in an override or extra file, because form edits write only the base file (compose-forms §3); the UI says "set in <file>; change it in the Compose tab".

### 5.2 Steps (one operation `update`, output lines prefixed `[<service>]`)

1. **Order:** the selected services sorted topologically by `depends_on` (dependencies first; ties by name). A cycle is reported by compose config anyway; it is refused with 400.
2. **For each service, in order:**
   1. **Record the previous image.** Record the current image ID and its repository digest. Tag it `stackstead-rollback/<project>/<service>:previous` (`engine.TagImage`). Upsert the `rollback_images` row.
   2. **Tag change only:** write the new `image:` through `stacks.Service.UpdateService` with the request hash (or the hash returned by the previous service's edit). This is the normal `commit` path, so validation runs and config history records a revision (the config history spec replaces the old single-copy backup with revisions).
   3. **Pull:** `docker compose pull <service>`, output streamed. If the resulting image ID equals the previous one, the result is `unchanged`: the rollback tag is moved back to where it was and the service is not recreated.
   4. **Recreate:** `docker compose up -d --no-deps --no-build --pull never <service>`.
   5. **Health gate (§5.3).**
3. **Failure:** if a gate fails and `AutoRollback` is on, every service already updated in this run (including the failing one) is rolled back in reverse order (§5.4), each through its own gate. The operation ends `failed` with the error "<service>: <gate reason>; rolled back N services". With `AutoRollback` off, the run stops at the failing service, nothing is rolled back, and the error says "rollback available".
4. Each service's outcome is recorded in `update_items`: `updated`, `unchanged`, `failed`, `rolled_back` or `rollback_failed`.
5. **Cancel and timeout** (existing operation semantics) stop after the current step. Cancel during a gate counts as a gate failure, so the rollback rules apply. Rollback steps themselves are not cancellable: they run with a detached context bounded by 5 min per service, so the stack is never left half rolled back.

### 5.3 Health gate

```go
type GateResult struct { OK bool; Reason string; HasHealthcheck bool; Waited time.Duration }
func (g *Gate) Wait(ctx context.Context, project, service string, hc *engine.HealthConfig) GateResult
```

- **Which healthcheck:** the effective one is read from `InspectContainer` of a new container (`Config.Healthcheck`, from the compose file or the image). `Test` empty or `["NONE"]` means no healthcheck. `InspectContainer` gains `HealthConfig{Interval, Timeout, StartPeriod time.Duration; Retries int}`. The test command itself is not exposed in the API (it can embed secrets).
- **With a healthcheck:** wait until every container of the service is `healthy`. Fail at once on `unhealthy`, or on an exited, dead or restarting container (or a restart count above the one seen right after `up`).
  - Backstop timeout = `start_period + (interval + timeout) × (retries + 1) + 30 s`, using Docker's defaults for unset values (30 s, 30 s, 3, 0). This is the longest Docker can stay in `starting` before declaring the container unhealthy.
  - The timeout is bounded by the operation's remaining time.
  - Progress line every 10 s: `[web] waiting for healthy (starting, 40s / 3m)`.
- **Without a healthcheck:** no gate. One inspect after `up`: if every container is `running`, the service succeeds and the output says `[web] no healthcheck: only checked that the container is running`. A container that exits after that moment is not detected. The dialog and the service card say this beforehand.
- **Wake-ups:** the gate wakes on `state.Cache` changes for the project (Docker `health_status` and `die` events), with a 2 s fallback poll. It never busy-waits.

### 5.4 Rollback

- **Same-tag update:** `engine.TagImage(rollbackImageID, <compose repo>, <compose tag>)`. If the retained image is gone, first `docker pull <repo>@<recorded digest>` (streamed), then tag that. Then the same recreate command (`--pull never`, so a moved tag on the registry cannot sneak back in), then the gate.
- **Tag-change update:** restore the previous `image:` text through `UpdateService` with the hash the update's own edit produced. If the file changed since (hash mismatch), the rollback still re-tags and recreates from the retained image, but the operation reports `rollback_failed: compose file changed since the update; image restored, file not reverted`. The retained image is tagged with the old tag (still present locally, normally), then recreated and gated.
- **Manual Roll back** (service card, `/updates`, API) runs the same steps as operation action `rollback`, for one service, from the `rollback_images` row. It is available while a row exists and the service's current image differs from it.
- After a successful rollback the service shows `update_available` again (the registry still has the newer digest), with a note "rolled back from <digest> at <time>" until the next successful update. **Skip this version** is offered next to it.
- **Retention:** one `rollback_images` row and one tag per service. The next update moves the tag, and the image it leaves may become dangling (storage then treats it as ordinary). When a stack is removed, its rows are deleted and its rollback tags are removed (`RemoveImage` by reference, never forced; an in-use image just loses the tag).

### 5.5 What "changes" can and cannot show

Shown when available:
- current → new digest (first 12 hex characters, with a copy button);
- created dates (relative and absolute);
- `version` and `revision` label old → new;
- `title`;
- links to `url` and `documentation`.

**One release link built from `source`:**
- `source` is `https://github.com/<owner>/<repo>`:
  - with a version label: `…/releases/tag/<version>`;
  - else with both revisions: `…/compare/<old>...<new>`;
  - else: `…/releases`.
- Any other `https` source: the source URL itself.
- Non-`http(s)` URLs are never rendered as links.

**Stated plainly in the panel:**
- "Labels are set by the image publisher; many images have none."
- "A new digest can be only a base-image or dependency rebuild with no app change."
- "Created dates can be wrong (some reproducible builds report 1970)."
- "Stackstead cannot tell what changed inside the image."

## 6. Data model

One migration appended to `internal/store/store.go` `migrations`. The number is assigned at merge, because sibling specs add migrations too.

```sql
CREATE TABLE image_checks (
  ref TEXT NOT NULL, platform TEXT NOT NULL,          -- normalized reference; "linux/arm64/v8"
  checked_at INTEGER NOT NULL, remote_digest TEXT NOT NULL DEFAULT '',
  newer_tags TEXT NOT NULL DEFAULT '{}',              -- JSON NewerTags + truncated flag
  details TEXT NOT NULL DEFAULT '{}',                 -- JSON: created, labels of remote_digest; per-tag details
  error_kind TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (ref, platform));
CREATE TABLE update_policies (
  stack TEXT NOT NULL, service TEXT NOT NULL,
  policy TEXT NOT NULL CHECK (policy IN ('off','notify','manual')),
  skip_digest TEXT NOT NULL DEFAULT '', skip_tag TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (stack, service));
CREATE TABLE rollback_images (
  stack TEXT NOT NULL, service TEXT NOT NULL, image_id TEXT NOT NULL,
  repo_digest TEXT NOT NULL, ref TEXT NOT NULL, tag TEXT NOT NULL, created_at INTEGER NOT NULL,
  PRIMARY KEY (stack, service));
CREATE TABLE update_runs (
  id INTEGER PRIMARY KEY, op_id TEXT NOT NULL, stack TEXT NOT NULL, action TEXT NOT NULL,
  started_at INTEGER NOT NULL, ended_at INTEGER, result TEXT NOT NULL, auto_rollback INTEGER NOT NULL);
CREATE TABLE update_items (
  run_id INTEGER NOT NULL REFERENCES update_runs(id) ON DELETE CASCADE, service TEXT NOT NULL,
  from_ref TEXT NOT NULL, from_image TEXT NOT NULL, from_digest TEXT NOT NULL,
  to_ref TEXT NOT NULL, to_image TEXT NOT NULL DEFAULT '', to_digest TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL, message TEXT NOT NULL DEFAULT '');
CREATE INDEX update_runs_stack ON update_runs(stack, started_at);
```

- A service with no `update_policies` row has policy `manual`. Rows are written only on change and deleted with the stack (`stacks.Service.Remove` calls an `OnRemove` hook).
- `image_checks` rows not referenced by any service for 7 days are pruned by the scheduler.
- `update_runs` keeps the newest 50 per stack.
- Settings keys in `settings`: `updates.interval_seconds`, `updates.last_run_at`.
- No secret is stored. Tokens live in memory only.

**Storage inventory contract** (storage spec §5.4), implemented by `*updates.Service`:

```go
func (s *Service) RetainedImages(ctx context.Context) ([]storage.RollbackImage, error) // {ID, Stack, Service}
```

It returns the `rollback_images` rows. A database error is returned as an error, never as an empty list, so storage refuses image plans with 503. It is wired in `cli/serve.go` in place of storage's empty provider.

## 7. API

All routes require a session. Mutating routes require `Content-Type: application/json` and a body (`{}` allowed). Errors are `{error}` and validation errors `{error, fields}`, as today.

### 7.1 `GET /api/updates`

```json
{"checking": false, "lastRunAt": "2026-10-18T06:00:12Z", "nextRunAt": "2026-10-18T12:07:40Z",
 "intervalSeconds": 21600,
 "services": [{"stack": "media", "service": "jellyfin", "image": "docker.io/jellyfin/jellyfin:10.9",
   "policy": "manual", "status": "update_available", "hasHealthcheck": true,
   "current": {"digest": "sha256:9b1c…", "created": "2026-09-30T10:00:00Z", "version": "10.9.10", "revision": "a1b2c3d"},
   "available": {"digest": "sha256:4f0e…", "created": "2026-10-16T08:12:00Z", "version": "10.9.11", "revision": "d4e5f6a"},
   "releaseUrl": "https://github.com/jellyfin/jellyfin/releases/tag/10.9.11",
   "newerTags": {"patch": "", "minor": "10.10", "major": ""}, "newerTagsTruncated": false, "tagEditable": true,
   "skipped": {"digest": "", "tag": ""}, "checkedAt": "2026-10-18T06:00:15Z", "errorKind": "", "error": "",
   "rollback": {"available": true, "digest": "sha256:77aa…", "createdAt": "2026-10-01T09:00:00Z"},
   "lastRun": {"result": "updated", "at": "2026-10-01T09:00:40Z", "opId": "3f2a…"}}]}
```

Sorted by stack then service. `available` is `null` unless the status is `update_available`. `?stack=` filters. 503 is returned only when the database is unavailable. With Docker down, the stored results are still returned and the local fields are `null`.

### 7.2 Other routes

| Route | Behaviour |
|---|---|
| `GET /api/stacks/{name}/updates` | The §7.1 items for one stack, plus `history` (newest 20 runs with items) |
| `GET /api/stacks/{name}/services/{service}/changes?tag=` | Details for a newer tag (lazy fetch, §4.4); 400 if `tag` is not offered |
| `POST /api/updates/check` | `{stack?}`; 202 `{checking: true}`; joins a running check |
| `POST /api/stacks/{name}/updates` | `{services?: [{service, tag?}], autoRollback?: true, hash?}`; 202 Operation; refusals per §5.1 |
| `POST /api/stacks/{name}/updates/preview` | Same body; `{content, hash}` for tag changes (wraps `PreviewService`), 400 when no tag changes |
| `POST /api/stacks/{name}/services/{service}/rollback` | `{}`; 202 Operation; 409 when no rollback row or the current image is the retained one |
| `PUT /api/stacks/{name}/services/{service}/update-policy` | `{policy}`; 200 with the item |
| `PUT /api/stacks/{name}/services/{service}/update-skip` | `{digest?, tag?}` (must equal the currently available ones); 200 with the item |
| `DELETE /api/stacks/{name}/services/{service}/update-skip` | Clears the skip; 200 with the item |
| `GET`/`PUT /api/settings/updates` | `{intervalSeconds}` (0 or 3600–604800, else 400); `PUT` reschedules |
| `GET /api/events` | New message `updates` `{checking, seq}` |

Operations appear in `GET /api/stacks/{name}/operations` with action `update` or `rollback`. Stream, cancel and recent lists are unchanged.

## 8. Engine additions (`internal/engine`)

```go
// Container gains:
ImageID string // "sha256:…" from the container list's ImageID
// ContainerInspect gains:
Healthcheck *HealthConfig                 // nil = none (Test empty or ["NONE"])
type HealthConfig struct { Interval, Timeout, StartPeriod time.Duration; Retries int }
func (c *Client) TagImage(ctx context.Context, imageID, repo, tag string) error // POST /images/{id}/tag
func (c *Client) RegistryConfig(ctx context.Context) (RegistryConfig, error)     // from /info, cached 5 min
type RegistryConfig struct { InsecureHosts []string; InsecureCIDRs []netip.Prefix; Platform string }
```

`Images`, `InspectImage` (needs `RepoDigests`, `Created`, `Config.Labels`, `Os`, `Architecture`, `Variant`) and `RemoveImage` come from the storage spec §8. Whichever plan lands first adds them, with the same signatures.

## 9. Frontend

### 9.1 Shell
- Rail item **Updates** after Networks (after Storage once that ships), route `/updates`, icon `arrow-up-circle` in `web/src/lib/shell/icons.ts`. A count badge (mono, `--info`) shows services with `update_available` and policy `notify` or `manual`. The badge has `aria-label="Updates, 3 available"`. Breadcrumb `Updates`, title `Updates · Stackstead`.
- Stack list: a small `↑ n` chip after the origin tag on rows with available updates (`title` lists services).

### 9.2 `/updates` (`web/src/routes/Updates.svelte`)
- Heading row: `h1` "Updates". Muted `checked <RelativeTime> · next in 4h`, or `checking…` with a spinner while `checking`. Primary **Check now** (disabled while checking).
- Chips: All / Available / Newer tags / Errors / Not checked (pinned, build only, local only, off), with counts and `aria-pressed`. Search over stack, service and image.
- Table grouped by stack. Each group header has the stack link and **Update all (n)** (manual services with `update_available`).
- Columns:
  - Service (mono);
  - Image (mono, ellipsis);
  - Status (tone chip: `up to date`, `update available`, `newer: 10.10`, `pinned, not checked`, `build only`, `local image`, `off`, `skipped`, error kind with the message in `title`);
  - Current → Available (short digests, created dates);
  - Policy (`select`: Off / Notify / Manual, `aria-label="Update policy for <service>"`);
  - actions: **Changes** (expands the panel), **Update** (manual only), **Skip**, **Roll back** (when available).
- Changes panel (`web/src/lib/updates/ChangesPanel.svelte`): the §5.5 fields, the release link (`target="_blank" rel="noopener noreferrer"`), and the honesty notes. The newer-tag rows each have **Show changes** (lazy) and **Update to <tag>**.
- Refresh: on mount, on `updates` and resync events from `/api/events`, and after any action. No timers.
- Docker down: stored results are shown, local columns show `—`, and actions are disabled with "Docker is unreachable".

### 9.3 Update dialog (`web/src/lib/updates/UpdateDialog.svelte`)
- Lists the services in execution order. For each: current → target, and the gate line: **"Waits for healthcheck (up to 3m)"** or **"No healthcheck: only checks that the container starts"** (warning tone).
- Tag changes show the compose diff (existing `DiffView`, from `/updates/preview`).
- **Auto-rollback** checkbox, checked, with hint "If a service fails its healthcheck, every service updated in this run goes back to its previous image."
- **Update** posts. On 202 the UI navigates to `/stacks/<name>?tab=operations&op=<id>` (existing pattern). A 409 (stale hash, busy, policy) is shown in the dialog.

### 9.4 Stack detail
- Service card header: a status chip as in §9.2. Actions: **Update** (manual, available), **Roll back** (when available), and a `⋯` menu with Policy and Skip. **Roll back** and the stack-level **Update all** use `ConfirmDialog`.
- Below the `<dl>`: an `Updates` row (`checked 2h ago · healthcheck: yes|no`) and the note "rolled back from …" when applicable.
- Operations tab: `update` and `rollback` operations render with the existing `OperationPanel`. Lines with `rolled back` use `--warning`, display only.

### 9.5 Settings › Updates (`web/src/lib/settings/UpdatesSettings.svelte`)
- Check interval segmented control: Off / 1h / 6h / 12h / 24h.
- Last and next run, **Check now**.
- Credentials note with `sudo -u stackstead -H docker login <registry>` (`CopyButton`) and "Logging in to Docker Hub raises its rate limits."
- A line per registry currently `rate_limited` or `auth`-failing, with its message.

### 9.6 Accessibility and responsive
As the v2 spec §7/§9: icon buttons carry `aria-label` and `title`; status is never shown by colour alone; the table becomes cards at ≤ 720 px; the dialog uses `Dialog` (focus trap, Esc). The badge count is in the link's accessible name, not a live region.

## 10. Security

- **New outbound traffic:** Stackstead now contacts the registries of the images in registered stacks, and `docs/security.md` says so. With the interval set to `0` and **Check now** unused, Stackstead makes no outbound connections.
- **Credentials:**
  - read from the service user's Docker config and helpers;
  - held in memory only for the request;
  - never logged, never in operation output, never in an API response, never in SQLite;
  - error messages from registries are passed through, but `Authorization` and token values are never echoed.
- **Credential helpers:** run as the service user with no shell and a validated name. Only `PATH` binaries named `docker-credential-*` can run. This is the same trust `docker` itself places in them.
- **Who chooses registries:** the registry host comes from compose files the admin registered. Registry requests are therefore admin-directed, not user-input SSRF. The client still refuses non-HTTPS except for loopback and daemon-insecure registries, and caps all bodies.
- **Untrusted metadata:** labels, tag names and registry error text are untrusted. They are rendered as text only (no `{@html}`), and URLs become links only for `http(s)` schemes.
- **Docker access:** rollback tags and recreation use the existing Docker access (root-equivalent, already documented). Updates and rollbacks of stack containers stay compose operations; Containers-spec ownership rules are unchanged.

## 11. Error handling

| Condition | Behaviour |
|---|---|
| Registry unreachable / DNS / TLS | `errorKind: network`, previous digest kept, next run retries |
| 401 after auth, helper failure | `errorKind: auth`, message "log in as the stackstead user: sudo -u stackstead -H docker login <host>" |
| 404 manifest | `errorKind: not_found` (tag deleted upstream or private without login) |
| 429 | `errorKind: rate_limited`, registry skipped until reset (§4.6) |
| Docker down during check | Local side unknown; remote results still stored; statuses computed on next read |
| Pull fails | Operation fails at that service; earlier services in the run are rolled back if auto-rollback is on; rollback tag moved back |
| Gate fails, rollback succeeds | Operation `failed`, items `rolled_back`, card note |
| Rollback fails (image gone and registry no longer has the digest, file changed, compose error) | Item `rollback_failed`, operation error names the service and the manual fix; `danger` banner on the service card until the next successful update or rollback |
| Stackstead stops mid-update | `Ops.Shutdown` cancels: the gate fails and the detached rollback continues within the shutdown grace; anything left is visible as `rollback_failed` / current image differing from the rollback row on next start |
| Concurrent Up/Pull on the stack | 409 busy (one operation per stack) |

## 12. Testing

Test-first for every behaviour change (user rules). All registry tests use a **fake registry**: `internal/registry/registrytest`, an `httptest.Server` with programmable repositories, tags → manifests (index and single), blobs, tag pagination, Bearer and Basic challenges, token endpoint, 429 with `Retry-After`, missing `Docker-Content-Digest`, and a request log for asserting call counts and methods.

**`internal/registry`:**
- `TestParseReferenceNormalizes`: `nginx`, `nginx:1.27`, `library/nginx`, `ghcr.io/o/r:v1`, `localhost:5000/x`, `registry:5000/x@sha256:…`, invalid refs.
- `TestHeadDigestUsesHeadAndAcceptHeaders`: one `HEAD`, all four media types in `Accept`.
- `TestHeadDigestFallsBackToGetAndHashes`.
- `TestBearerChallengeAnonymousAndWithBasicCredentials`; `TestIdentityTokenUsesRefreshGrant`; `TestTokenCachedUntilExpiry`.
- `TestCredentialsNotSentToHTTPRealm`; `TestAuthorizationDroppedOnCrossHostRedirect`.
- `TestTagListFollowsLinkAndCapsPages`; `TestBodyCapsEnforced`.
- `TestRateLimitedRegistrySkippedUntilRetryAfter`.
- `TestDockerConfigLookupOrder` (credHelpers > credsStore > auths; Docker Hub key) with a fake `HelperRunner`.
- `TestHelperNameValidatedAndNoShell`; `TestHelperNotFoundMeansAnonymous`.
- `TestParseVersionTag` and `TestNewerTags` (table: same shape only, `-alpine` suffix, `v` prefix, rc excluded, 1-/2-/3-part classes, none newer).

**`internal/updates`:**
- `TestCheckerStatuses` (table over §4.1 statuses with a fake engine and fake registry).
- `TestCheckerDedupesByReferenceAndPlatform` (two stacks, one image → one `HEAD`).
- `TestCheckerPlatformFallbackMatchesPlatformDigest`.
- `TestDetailsFetchedOncePerNewDigest`.
- `TestSkipHidesOnlyThatDigestOrTag`.
- `TestSchedulerNoBurstAfterRestart`, `TestSchedulerJitterBounds`, `TestCheckNowReusesFreshResults` (fake clock).
- `TestUpdateOrderFollowsDependsOn`.
- `TestUpdateTagsPreviousImageBeforePull`; `TestUnchangedPullSkipsRecreate`.
- Gate: `TestGateHealthyPasses`, `TestGateUnhealthyFails`, `TestGateTimeoutDerivedFromHealthcheck`, `TestGateNoHealthcheckRunningIsSuccess`, `TestGateExitedFails`.
- `TestAutoRollbackRestoresAllServicesInReverseOrder`; `TestAutoRollbackOffStopsWithoutRollback`.
- `TestRollbackRepullsByDigestWhenTagPruned`.
- `TestTagChangeRollbackRevertsFileThroughCommit`; `TestTagChangeRollbackReportsChangedFile`.
- `TestRollbackNotCancellable`; `TestRefusalsBeforeStart` (policy notify/off, pinned, busy, stale hash, unknown tag, variable image).
- `TestComposeVersionFloor` (table: `v2.7.3` refused with the message naming v2.8.0 and the found version; `2.8.0`, `v2.40.1` and `v5.1.1` accepted; an unparseable version refused). `TestGateWaitsForEveryReplica`. `TestHistoryKeepsNewest50PerStack`.
- `TestRetainedImagesErrorsOnDBFailure`; `TestStackRemovalDeletesRowsAndTags`.

These use a fake `Compose` that records args (asserts `--no-deps --no-build --pull never <svc>`, never `--remove-orphans`) and a fake engine with scripted health transitions.

**`internal/engine`:** `TestContainersDecodesImageID`, `TestInspectHealthcheckNoneAndDurations`, `TestTagImageRequest` (path, `repo`/`tag` query, 201/404/409 mapping), `TestRegistryConfigInsecure`.

**`internal/store`:** `TestMigrationUpdatesTables` (tables, policy CHECK, cascade).

**`internal/api`:** session on every route; `GET /api/updates` shape and `?stack=`; check 202 and join; update 202 and each §5.1 refusal code; preview; rollback 409 cases; policy and skip validation; settings bounds; `updates` event published on run start/end and on operation finish.

**Vitest:**
- Updates page: chips and counts, group **Update all** count, status chips including "pinned, not checked", policy select calls PUT, changes panel (links only for http(s), honesty notes, lazy tag changes), Check now disabled while checking, refetch on `updates` event.
- Update dialog: order, gate lines including the no-healthcheck warning, auto-rollback default checked, diff for tag change, navigation to the operation, 409 shown.
- Service card: chips, Update/Roll back visibility by policy and status, rolled-back note.
- Settings section; rail badge accessible name; stack list chip.

**E2E (`cmd/stackstead/e2e_test.go`, with Docker):**
1. Start `registry:2.8.3` on `127.0.0.1:<random>` (labelled for cleanup); push `busybox` as `<reg>/e2e/app:1` and `:1.1`.
2. Create a stack using `<reg>/e2e/app:1` with a healthcheck; Up.
3. Push a different build (an added layer) to `:1`. `POST /api/updates/check`: the service shows `update_available` and `newerTags.minor == "1.1"`.
4. Update: the operation succeeds and the container runs the new image ID. The rollback tag exists and `RetainedImages` lists it.
5. Push a build to `:1` whose healthcheck always fails; update. The gate fails, auto-rollback runs and the container is back on the previous image ID.
6. `docker image prune -f` leaves the rollback image in place.
7. Clean up in `t.Cleanup`.

**Visual:** headless-Chrome screenshots of `/updates` (with an available update, an error and a pinned row), the dialog and a service card at 1280 and 375 px, dark and light.

## 13. Documentation

- `docs/api.md`: an **Updates** section covering:
  - routes, statuses, policies and skip;
  - the gate rules, including "no healthcheck → only checks the container starts";
  - auto-rollback, retention and the rollback tag name;
  - the `updates` event and operation actions `update`/`rollback`.
- `docs/security.md`: §10 (outbound registry traffic, credentials and helpers, untrusted labels).
- `docs/install.md`: extend the private-registry note with credential helpers (they must be on the service's `PATH`) and the Docker Hub rate-limit advice.
- `README.md`: one feature line ("Image updates: detect new builds and versions, update with health checks and automatic rollback").

## 14. Delivery

One implementation plan, about 12 tasks, numbered with the sibling plans:
1. `internal/registry` reference and version parsing;
2. registry client and fake registry;
3. credentials and helpers;
4. engine additions;
5. migration and store;
6. checker;
7. scheduler and `updates` event;
8. updater (steps, gate, rollback) and `RetainedImages` wiring;
9. API routes;
10. frontend: Updates page, rail and stack list badges;
11. dialog, changes panel, service card, Settings;
12. e2e, docs, visual pass.

Tasks 1–3 can run in parallel with the storage plan. Task 4 coordinates on the shared engine image calls.

## 15. Open questions

None. The four raised in review were decided by the user on 2026-10-18 (§3: Compose version floor, scaled services, update history; §2 Out: registry mirrors).
