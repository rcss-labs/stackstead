# Stackstead Storage inventory — Design

**Date:** 2026-10-18
**Status:** Draft, awaiting review. Adds a **Storage** screen (images, volumes, build cache, container writable layers), exact cleanup plans with typed confirmation, a persistent storage action log, and two CLI commands. Builds on `2026-10-15-stackstead-ui-v2-design.md` (the `/api/host` volumes tile), `2026-10-15-stackstead-containers-design.md` and `2026-10-15-stackstead-networks-multiservice-design.md` (list/detail pattern, the "owned by a registered stack is not removable here" rule).

## 1. Purpose

Today the dashboard's Volumes tile shows a count, a total size and a dangling count, and nothing else: an operator whose disk is filling up has to drop to `docker system df -v` and `docker image prune` on the host, and nothing stops them from deleting the volume of a stack that is merely stopped. The Storage screen answers "where did my disk go, and what can I safely delete?" from the browser, and makes every delete an exact, previewed, typed-confirmed, logged action.

Success criteria:

- `/storage` shows disk used by images, container writable layers, volumes and build cache, each with its reclaimable share and the age of the size data.
- Every image lists the containers (any state) using it and the registered stacks declaring it; every volume lists its containers (including stopped ones) and the registered stacks declaring it, with each stack's state.
- Before any delete, the operator sees the exact list of items and the bytes they free (exact for volumes and build cache, a min–max range for images), and must type a confirmation phrase. The one exception, a build-cache cleanup on an engine that ignores the `id` filter, is labelled "not exact" in the preview (§6.5).
- A volume declared by a registered stack, running or stopped, can never be deleted from this screen (server-enforced); it is shown as "unused · protected" when Docker counts it unused.
- Images retained for rollback by the image-updates feature are never deleted from this screen.
- Every delete runs as an operation with live output and leaves a row in a persistent log that survives restarts.
- Opening the screen never blocks on `system df`; ten open tabs cost the same as one.

## 2. Scope

### In
- Backend: new package `internal/storage` (inventory join, classification, protections, cleanup plans, action log); `engine.Client` image list/inspect/remove, volume list/inspect/remove, per-type `system df`, build cache prune; `composecli.Runner.ConfigJSON`; host-scoped operations in `stacks.Ops`; one SQLite migration (the shared `action_log` table, §5.5, also used by volume diagnostics); routes under `/api/storage`.
- Frontend: rail entry and route `/storage` (tabs Overview, Images, Volumes, Build cache), `/storage/images/:id`, `/storage/volumes/:name`, cleanup dialog, links from the dashboard and container details.
- CLI: `stackstead storage df`, `stackstead storage prune <images|volumes|build-cache> [--preview] [--yes] [--detach]`.
- Tests, e2e smoke, docs (`docs/api.md`, `docs/cli.md`, `docs/security.md`, README feature line).

### Out
- Removing containers (the Containers spec keeps remove out); container writable layers are shown, not cleaned.
- Bulk deletion of tagged images that no container uses (`docker image prune -a` semantics): single delete only.
- Forced image removal (`force=true`), pulling, tagging, pushing, registry browsing.
- Creating volumes, volume backup/restore, browsing volume contents.
- Mount-level volume checks (permissions, bind paths, free space at the mountpoint): owned by the **volume diagnostics** spec, which adds a per-stack **Storage** tab on stack detail (`?tab=storage`). A stack link in a volume's "Declared by" here goes to that tab; diagnostics never removes volumes, and this screen never checks mounts.
- Choosing which images to keep for rollback: owned by the **image updates** spec. This spec only consumes its list (§5.4).
- Logging every existing Stackstead mutation (stack actions, edits) to `action_log`: the table is shared and owned here, but only storage and mount fixes write to it for now.
- Single-item deletes from the CLI.

## 3. Key decisions

The TypeSafe skill named in the global rules cannot run here (the installed `typesafe-ai` skill is an AI-feature SDK, not a decision scorer), so the decisions are argued in writing. All choices marked "User" were put to the user as options with trade-offs on 2026-10-18; the rest follow from existing code or spec conventions.

| Decision | Choice | Reason |
|---|---|---|
| Meaning of "unused" | Docker's own: an image is unused when it is **dangling** (untagged, no container); a volume is unused when Docker's `RefCount` is 0 | User. Simple and matches what operators already know from `docker system df`. Protections (below) cover the cases where Docker's definition is unsafe. |
| Volumes declared by a registered stack | Never deletable here, running or stopped: 409 "declared by stack X; remove it with the stack (Remove → also delete volumes)". When `RefCount` is 0 they are listed as **unused · protected**, are not selectable, and a confirm skips them | User (Q2) and the reconciliation with "unused". Same ownership rule as networks: compose owns them, and a stopped stack's volume is the typical data-loss case. |
| Rollback images | Listed, tagged **protected · rollback for stack X**, never selectable, 409 on delete | User (F1). A rollback image is often untagged, so Docker calls it dangling; without the guard the default cleanup would destroy rollback. |
| Tagged images with no container | Tagged "no containers"; single delete only, never in a bulk plan | User (F2). Avoids wiping base images the operator still wants. |
| Bulk delete mechanism | Two steps: **plan** (exact items, `planId`, short TTL) then **confirm** (server re-checks each item against fresh Docker state, deletes by explicit id/name, skips items that changed); build cache via `/build/prune` with an `id` filter, or a clearly labelled non-exact fallback when the engine ignores it (§6.5) | User (Q3, and the build-cache follow-up). The preview is exactly what may be deleted; nothing created after the preview is touched. Docker's prune endpoints cannot promise that. |
| Single deletes | A plan of one item through the same endpoints | Follows from Q3 and Q7: one code path for re-check, typed confirmation, operation and log; no second, weaker delete route. |
| Confirmation | Typed confirmation for **every** delete, images and build cache included; the phrase is returned with the plan and checked by the server | User (Q7). Server-side check, like every other Stackstead guard: the client's view is not trusted. |
| Images used by containers; multi-tag images | No force. An image used by any container (any state) is refused with 409 naming up to 3 containers. A tagged image is deleted by **reference**, which untags that one tag; the image is removed only with its last tag | User (Q8). Never removes something a stopped container needs to start again. |
| Records | Every confirm runs as an **Operation** (host scope, live output via the existing stream) and appends one row per item to the shared SQLite table `action_log` | User (Q4, then one shared table). Live progress for slow build-cache prunes, plus history that survives restarts; one table for storage and mount fixes instead of a log per feature. |
| Page structure | One rail entry `/storage` with tabs Overview, Images, Volumes, Build cache; detail pages `/storage/images/:id`, `/storage/volumes/:name` | User (Q5). Keeps the rail short; mirrors list → detail elsewhere. |
| Caching | Cheap lists (`/images/json`, `/volumes`, containers) fresh behind a 2 s cache; sizes from **per-type** `system df` (`type=image`, `container`, `volume`, `build-cache`) refreshed in the background every 5 min, served stale with their age; **Recalculate** at most once a minute | User (Q6). A slow volume size walk never blocks the image list, and sizes cost nothing while nobody looks. |
| One volume-size source | The dashboard's Volumes tile reads storage's `type=volume` cache instead of running its own df | Follows from caching: avoids two directory walks of every volume. `host.Collector` already takes a `VolumesDiskUsage` provider. |
| What a stack declares | `docker compose config --format json` per registered stack, cached by compose file hash; on failure, fall back to compose labels on existing objects and mark the stack's declarations unknown | User (Q10). Accurate (interpolation, resolved volume names, external volumes); the subprocess runs only when a file changes. |
| Images declared by a stopped stack | Single delete allowed, with a plan warning "declared by stack X; the next Up pulls or builds it again" | Follows from F2 and Q8: no data is lost, the image is re-creatable. Confirmed by the user. |
| CLI | `storage df` and `storage prune <kind>` with `--preview`; without it, prune confirms the plan: typed phrase on a terminal, `--yes` required without one | User (Q9) for the commands; confirming follows the existing `stacks rm --volumes --yes` convention (`confirm` in `internal/cli/prompt.go`, exit 2 without a terminal). |
| Route prefix | Everything under `/api/storage/…` | One namespace for this screen; it cannot collide with the volume diagnostics routes (`/api/stacks/{name}/mounts…`, `/api/containers/{id}/mounts`) or the image updates routes (`/api/updates…`). |

## 4. Data sources and cost

| Data | Engine call | Cost | Cache |
|---|---|---|---|
| Images | `GET /images/json` | cheap (metadata only) | 2 s flight cache |
| Image detail | `GET /images/{id}/json` | one call per open detail | none |
| Volumes | `GET /volumes` | cheap, no sizes | 2 s flight cache |
| Volume detail | `GET /volumes/{name}` | one call | none |
| Containers | the existing containers list computation (`computeContainerList`) | already cached 2 s | shared |
| Image sizes incl. `SharedSize` | `GET /system/df?type=image` | seconds on large hosts | background, 5 min |
| Container writable layers (`SizeRw`) | `GET /system/df?type=container` | slow (walks each container's upper dir) | background, 5 min |
| Volume sizes and `RefCount` | `GET /system/df?type=volume` | slowest (walks every local volume) | background, 5 min |
| Build cache records | `GET /system/df?type=build-cache` | moderate | background, 5 min |
| Stack declarations | `docker compose config --format json` per registered stack | one subprocess per changed file | keyed by compose file hash |
| Rollback images | `RollbackImages` provider (§5.4) | in-process | provider's own |

- Typed df needs API 1.45. On older engines (or a 400 for the typed call) the four kinds are filled from **one** full `/system/df`, as `dfBody` already does for volumes.
- df refreshes use the detached, panic-safe stale cache that `/api/host` uses today (`host.cached[T].GetStale`), moved to a shared internal package `internal/stale` as `stale.Value[T]` (behavior-preserving refactor covered by the existing host tests). Each kind has its own value, refresh timeout 60 s, and failures are not retried before the interval (existing rule).
- `RefCount` and size come only from df. Until a kind's first df finishes, its sizes are `null` and its "unused" state is computed from the cheap data: a volume with no container in any state is `unused` provisionally, flagged `sizesPending`. Plans always take a **fresh** typed df for their kind before answering (§6.3), so a provisional state never reaches a delete.

## 5. Data model (`internal/storage`)

### 5.1 Types (shape only)

```go
type Consumer struct { ContainerID, Name, State, Project, Service, Stack string }
type Declaration struct { Stack, Service, Ref string; StackState string } // StackState: running|partial|stopped|not-deployed
type Protection struct { Reason string; Stack string }                      // Reason: "stack" | "rollback"

type Image struct {
    ID string; Tags, Digests []string; Created time.Time
    Size *int64; SharedSize *int64           // nil until df
    Containers []Consumer; DeclaredBy []Declaration
    Dangling bool; Protection *Protection
    Status string                            // in-use | no-containers | dangling
}
type Volume struct {
    Name, Driver, Scope, Project string; CreatedAt time.Time; Labels map[string]string
    Size *int64; RefCount *int64; SizesPending bool
    Containers []Consumer; DeclaredBy []Declaration; Protection *Protection
    Status string                            // in-use | unused
}
type BuildRecord struct { ID, Type, Description string; Size int64; InUse, Shared bool; LastUsedAt *time.Time; UsageCount int }
type KindTotals struct { Count int; Size, Reclaimable *int64; SizesAt *time.Time; Refreshing bool; Error string }
```

- Image `Status`: `in-use` when any container (any state) uses it; `dangling` when it has no tags and no containers; else `no-containers`.
- Volume `Status`: `in-use` when `RefCount > 0` or any container lists it in its mounts; else `unused`. A volume with `Status=unused` and a `Protection` is shown as **unused · protected**.
- `Reclaimable` per kind: images = sum of `Size − SharedSize` over dangling, unprotected images; volumes = sum of `Size` over unused, unprotected volumes; build cache = sum of `Size` over records not `InUse` and not `Shared`; container layers = `null` (nothing to reclaim here).

### 5.2 Joining (pure functions, the bulk of the unit tests)

```go
func Classify(in Inputs) Inventory
type Inputs struct {
    Images []engine.Image; Volumes []engine.Volume; Containers []engine.Container
    DF DFSnapshot; Stacks []StackDeclarations; Rollback []RollbackImage
}
```

- Image → containers: by the container's image ID (not its image string, which can be a stale tag).
- Volume → containers: from container mounts of type `volume` by name (stopped containers included).
- Declarations: an image declared by a stack matches by resolved reference (after `config`), or by `<project>-<service>` for services with `build:` and no `image:` (compose's default name). A volume declared by a stack matches by its resolved name (`<project>_<key>`, or `name:`, or the external name).
- Protection `stack`: the volume is declared by a registered stack, **or** carries the `com.docker.compose.project` label of a registered stack's project (this keeps protection even when the stack's declarations are unknown).
- Protection `rollback`: the image ID is in the rollback list.

### 5.3 Declarations source

```go
type Declarations interface { For(ctx context.Context, e stacks.Entry) (StackDeclarations, error) }
type StackDeclarations struct {
    Stack, Project, State string; Known bool
    Images []ImageRef           // {Service, Ref}
    Volumes []VolumeRef         // {Key, Name, External bool}
}
```

- `composecli.Runner.ConfigJSON(ctx, inv Invocation) ([]byte, error)`, run on the stack's whole compose file set (`Invocation`, compose-forms §7.2); `ProjectName` is rewritten on top of it (behavior unchanged).
- Only `name`, `services.*.image`, `services.*.build` presence and `volumes.*.{name,external}` are decoded; the rest of the output (which may contain interpolated secrets) is discarded unread and never logged, stored or returned.
- Cached per stack by the SHA-256 hashes of every file in the set plus `.env` (the key compose-forms §7.4 uses for `/effective`). Concurrency 2, timeout 10 s per run, single-flight per stack.
- On error (parse error, an unavailable file set, compose missing): `Known=false`, `Images`/`Volumes` empty. The UI shows "declarations unknown for stack X" on the Volumes tab; protection still applies through the project label (§5.2).

### 5.4 Rollback images (contract with the image updates spec)

```go
type RollbackImages interface { RetainedImages(ctx context.Context) ([]RollbackImage, error) }
type RollbackImage struct { ID, Stack, Service string }
```

Wired in `serve.go`. Until the image updates feature exists, the provider returns nothing. If the provider errors, plans for images are refused with 503 ("cannot verify rollback images") rather than risk deleting one.

### 5.5 Shared action log (`action_log`, migration)

One table for every persistent action record in Stackstead. This spec owns its definition; the volume diagnostics spec writes `mount.fix` rows into it (scope = the stack name), and later features add kinds without a migration. Appended to `internal/store/store.go` `migrations`:

```sql
CREATE TABLE action_log (
  id INTEGER PRIMARY KEY,
  ts INTEGER NOT NULL,                 -- unix seconds
  username TEXT NOT NULL,
  kind TEXT NOT NULL,                  -- storage.delete | storage.prune | mount.fix | ...
  scope TEXT NOT NULL,                 -- 'host' or a stack name
  target TEXT NOT NULL,                -- e.g. 'volume/old_data', 'image/nginx:1.25', 'build-cache/<id>'
  detail TEXT NOT NULL DEFAULT '',     -- command, reason or Docker's message
  bytes INTEGER,                       -- NULL when not applicable or unknown
  result TEXT NOT NULL,                -- per kind; storage: deleted | untagged | skipped | failed
  operation_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX action_log_ts ON action_log(ts);
CREATE INDEX action_log_kind_ts ON action_log(kind, ts);
```

- No CHECK on `kind` or `result`, so features add values without a migration; the shared package `internal/actionlog` validates them: `kind` matches `^[a-z]+\.[a-z]+$`, `scope` is `host` or a valid stack name, `result` is non-empty. API: `Append(ctx, Entry) error`, `Recent(ctx, Filter) ([]Entry, error)` with `Filter{KindPrefix, Scope string; Limit int}`.
- Storage writes one row per item when its result is known: `kind` `storage.delete` for single-item plans, `storage.prune` for bulk plans; `scope` `host`; `bytes` the plan estimate (or Docker's `SpaceReclaimed` for a build-cache prune); `detail` the skip reason or Docker's message.
- Rows older than 365 days are deleted by the existing hourly prune.
- A failed log write does not stop the deletes; the operation output says "could not record action".

## 6. Cleanup plans

### 6.1 Plan request

`POST /api/storage/plans` with one of:

```json
{"kind": "images", "selection": "dangling"}
{"kind": "volumes", "selection": "unused"}
{"kind": "build-cache", "selection": "unused"}
{"kind": "image", "ref": "nginx:1.25"}           // or a 12–64 hex id for an untagged image
{"kind": "volume", "name": "media_cache"}
```

### 6.2 Plan response (201)

```json
{"id": "p_7f3a…", "kind": "volumes", "createdAt": "…", "expiresAt": "…",
 "items": [{"kind": "volume", "id": "old_data", "ref": "old_data", "bytes": 1073741824}],
 "protected": [{"kind": "volume", "id": "media_db", "ref": "media_db",
                "reason": "stack", "stack": "media", "stackState": "stopped"}],
 "warnings": [], "reclaim": {"min": 1073741824, "max": 1073741824, "exact": true},
 "confirmPhrase": "delete 1 volume"}
```

- `confirmPhrase`: for one item, its `ref` (volume name, image reference, or short id for an untagged image); for a bulk plan, `delete <n> <images|volumes|build cache records>` (singular for 1). Build cache bulk is always phrased as a count.
- `reclaim`: volumes and build cache are exact (`min == max`); images use `min = Σ(Size − SharedSize)`, `max = Σ Size`, `exact: false` (Docker reports no per-layer sizes). Untagging a tag of a multi-tag image reclaims 0, shown as such.
- An empty bulk plan is returned (201, no items) so the UI can say "nothing to clean"; confirming it is 409.
- Single-item refusals answer at plan time: 409 when the item is protected (message names the stack or "rollback image for X"), when an image is used by containers (names up to 3), or when a volume is in use (names up to 3 containers). 404 for an unknown item.
- Plans live in memory (at most 32, oldest evicted), expire after **5 minutes**, and are lost on restart. A plan records a fingerprint per item: image id + tag set; volume name + `CreatedAt`; build record id.

### 6.3 Confirm

`POST /api/storage/plans/{id}/confirm` with `{"confirm": "<phrase>"}` → 202 with the operation (the existing `Operation` DTO). Errors: 400 `{error, fields:[{field:"confirm"}]}` for a wrong phrase; 404 unknown or expired plan; 409 empty plan, or another storage operation running (`ErrBusy`); 503 Docker unavailable.

The operation function, per item, in plan order:

1. Re-read fresh state for that item (image inspect / volume inspect + containers list + a fresh typed df for volumes so `RefCount` is current + declarations + rollback list).
2. Skip with a reason when the fingerprint changed, the item became used, protected or declared, or it no longer exists ("already gone" is `skipped`, not `failed`).
3. Delete by explicit id/name: `DELETE /images/{ref}` (tag) or `DELETE /images/{id}` (untagged), never `force`; `DELETE /volumes/{name}` without `force`; build cache: see §6.5.
4. Emit one output line (`deleted volume old_data (1.0 GB)`, `untagged nginx:1.25`, `skipped volume x: now used by container y`) and append the `action_log` row.

After the run the operation invalidates the 2 s list caches and triggers a background df refresh for the kind. The operation succeeds when every item is `deleted`, `untagged` or `skipped`; it fails when any item failed (Docker refusal), and continues past failures.

### 6.4 Host-scoped operations

`stacks.Ops.Start` already keys by stack; host-scoped operations use the reserved stack key `""` (stack names can never be empty) with action `storage-cleanup`. `GET /api/operations` lists them with `"stack": ""`; `GET /api/operations/{id}`, `/stream` and `/cancel` work unchanged. Cancel stops before the next item. One storage cleanup runs at a time (`ErrBusy`).

### 6.5 Build cache: exact and fallback paths

- **Capability check, never a trial prune.** A probe that sends a prune and watches what happens is unsafe: an engine that ignores the filter would delete all unused build cache during the probe. The capability is therefore decided by the negotiated API version: `engine.Client.BuildPruneByID(ctx) (bool, error)` returns true when the version is at least the constant `buildPruneByIDMinAPI`. The first engine task sets that constant from a real-engine test, `TestE2EBuildPruneByIDHonoured`. The test creates two small build cache records with `docker build` (it is skipped when no builder is available), prunes one by id, and checks that the other is still there. Until that test has run and the constant is set, the constant is above `maxAPIVersion`, so every engine takes the fallback path. That default is safe and only less precise.
- **Exact path** (`true`): the plan lists the selected records (`exact: true`); confirm sends one `POST /build/prune?all=true&filters={"id":[...]}` with the remaining records after the re-check, and reports Docker's `SpaceReclaimed`.
- **Fallback path** (`false`): the plan has `"exact": false, "mode": "all-unused"`, lists the records not in use **at preview time** as "expected", and carries the warning "This engine cannot delete build cache records by id. Docker will delete all build cache that is not in use at the moment you confirm, which may differ from this list." The reclaim figure is labelled an estimate. Confirm sends `POST /build/prune?all=true` (no `id` filter; Docker skips in-use records itself), logs one `storage.prune` row with target `build-cache/all-unused` and Docker's `SpaceReclaimed`, and the operation output lists what the fresh df no longer shows. The UI dialog and the CLI print the "not exact" label next to the phrase; the phrase is the same count phrase.
- Rollback and stack protections do not apply to build cache, so the fallback cannot touch a protected object.

## 7. API

All routes require a session; mutating routes require `Content-Type: application/json` and a body (the existing CSRF rule); errors are `{error}`; 503 while Docker is unavailable (except `GET /api/actions`).

| Method and path | Purpose |
|---|---|
| `GET /api/storage` | Overview: `images`, `containers`, `volumes`, `buildCache` as `KindTotals`; `topContainers` (five largest writable layers `{name, stack, sizeRw}`); `declarationsUnknown` (stack names) |
| `GET /api/storage/images` | `Image` rows, sorted by size, largest first (unknown sizes last) |
| `GET /api/storage/images/{id}` | row plus inspect fields: `architecture`, `os`, `layers` (count), `labels`. Never `Config.Env`, `Cmd` or `Entrypoint` |
| `GET /api/storage/volumes` | `Volume` rows, sorted by size |
| `GET /api/storage/volumes/{name}` | row plus `mountpoint`, `labels`. Never driver `Options` (they can hold NFS/CIFS credentials) |
| `GET /api/storage/build-cache` | `BuildRecord` rows plus totals |
| `POST /api/storage/refresh` | start a background df refresh of all four kinds; 202 `{startedAt}`; 429 `{error, retryAfter}` within 60 s of the last manual refresh |
| `POST /api/storage/plans` | create a plan (§6.1–6.2) |
| `GET /api/storage/plans/{id}` | read a plan; 404 when expired |
| `POST /api/storage/plans/{id}/confirm` | run it (§6.3); 202 operation |
| `GET /api/actions?kind=storage.&scope=&limit=50` | newest `action_log` rows; `kind` is a prefix filter, `scope` exact; limit 1–500. Shared route, also used by volume diagnostics |

- `{id}` for images is a 12–64 hex id or unambiguous prefix (as for containers); references with `/` or `:` go in plan bodies, never in paths. `{name}` is a Docker volume name (`[a-zA-Z0-9][a-zA-Z0-9_.-]*`).
- Every list carries `sizesAt` and `refreshing` per kind so the UI can say "sizes from 3 min ago".
- Lists use `flightCache` (2 s, detached, single-flight) like containers and networks; the join itself is `storage.Classify`.

## 8. Engine (`internal/engine`)

```go
func (c *Client) Images(ctx context.Context) ([]Image, error)
func (c *Client) InspectImage(ctx context.Context, id string) (ImageInspect, error)
func (c *Client) RemoveImage(ctx context.Context, ref string) (ImageDeleteResult, error) // {Untagged, Deleted []string}
func (c *Client) Volumes(ctx context.Context) ([]Volume, error)
func (c *Client) InspectVolume(ctx context.Context, name string) (VolumeInspect, error)
func (c *Client) RemoveVolume(ctx context.Context, name string) error
func (c *Client) DiskUsage(ctx context.Context, kind DFKind) (DFSnapshot, error)        // typed, with the full-df fallback
func (c *Client) PruneBuildCache(ctx context.Context, ids []string) (reclaimed int64, err error)
```

- These are the only definitions of these calls; the image updates and volume diagnostics specs reuse them, and whichever plan lands first adds them with these signatures. `ImageInspect` carries `RepoDigests`, `Created`, `Config.Labels`, `Config.User` (internal, never returned by any route), `Os`, `Architecture`, `Variant` and the layer count, and has no field for `Env`, `Cmd` or `Entrypoint`. `VolumeInspect` carries `Driver`, `Mountpoint`, `Labels`, `CreatedAt`, `Scope` and only the `type` entry of the driver options (`OptionType`, e.g. `nfs`, `cifs`); the other options are never decoded. The image-to-container join uses `engine.Container.ImageID` (image updates §8).
- `VolumesDiskUsage` stays (the host collector's interface) and is implemented on top of `DiskUsage(DFVolume)`; the collector is wired to storage's volume cache (§3).
- New sentinel errors `ErrImageNotFound`, `ErrVolumeNotFound`; Docker 409 maps to `*ActionError{Status:409}` with Docker's message; removals never set `force`.
- Delete and prune calls use their own timeouts: 60 s per image or volume, 10 min for build prune (the operation's own context bounds it).

## 9. Frontend

### 9.1 Shell
- Rail item **Storage** after Networks (`/storage`, current on `/storage` and `/storage/*`), new `disk` icon in `web/src/lib/shell/icons.ts`. Breadcrumbs `Storage`, `Storage / Images / <ref>`, `Storage / Volumes / <name>`; titles `Storage · Stackstead`.
- Dashboard Volumes tile links to `/storage?tab=volumes`. Container details mounts of type `volume` link to `/storage/volumes/<name>`; the image in the container header links to `/storage/images/<id>`.

### 9.2 `web/src/routes/Storage.svelte` (tabs via `Tabs`, `?tab=overview|images|volumes|build-cache`)
- **Overview:** four `HostTile`-style tiles (Images, Container layers, Volumes, Build cache) with size and reclaimable. A muted line "Sizes from 3 min ago · **Recalculate**" (disabled for 60 s after use, with the reason). Top 5 container writable layers (link to `/containers/<name>`). "Declarations unknown for: …" banner when needed. **Recent storage actions**: the last 20 `action_log` rows with kind prefix `storage.` (time, user, kind, ref, result, bytes).
- **Images:** chips All / In use / No containers / Dangling / Protected (mono counts, `aria-pressed`), search over tags, id, stack. Columns: Image (tags as mono chips, `<none>` + short id when dangling), Size (`formatBytes`, "unique ≈ X" in `title`), Created (`RelativeTime`), Containers (count; names link), Declared by (stack links + state), Status tag. Header button **Clean up dangling images** (count and `~min–max`). Row **Delete** on `no-containers` and `dangling` unprotected rows; disabled with the reason elsewhere ("used by 2 containers", "rollback image for media").
- **Volumes:** chips All / In use / Unused / Protected. Columns: Name, Driver, Size ("calculating…" while `sizesPending`), Containers (names + state; stopped shown muted), Declared by (stack link + `running` / `stopped` / `not deployed`), Status (**in use**, **unused**, **unused · protected**: "declared by stack media (stopped) — remove via the stack" linking to the stack). Header **Clean up unused volumes**. Row **Delete** only on unused unprotected rows.
- **Build cache:** totals, records table (Type, Description, Size, In use, Shared, Last used), **Clean up build cache**.
- Refresh: on mount, after an operation ends, on `/api/events` `change`/resync (the debounced helper the Containers list uses); no polling while hidden. While `refreshing` is true, the lists refetch every 10 s while visible to pick up new sizes.

### 9.3 Detail pages
- `web/src/routes/StorageImage.svelte`: header (mono first tag or short id, status tag, `CopyButton` id), `<dl>` (tags, digests, created, size, architecture/os, layers), Containers and Declared by lists, labels in `<details>`, **Delete** (same rules as the row).
- `web/src/routes/StorageVolume.svelte`: header, `<dl>` (driver, scope, created, size, mountpoint, project), Containers, Declared by, labels, **Delete** or the protected note. A placeholder section is not added for volume diagnostics; that spec adds its own section.

### 9.4 Cleanup dialog (`web/src/lib/storage/CleanupDialog.svelte`)
- Opens by creating the plan; shows a spinner "Checking current state…" (plans take a fresh df).
- Shows: the item list (mono refs, bytes), the reclaim figure ("Frees 1.0 GB" or "Frees between 120 MB and 480 MB"), the **protected** list (not selectable, each with its reason and stack link), warnings, and the expiry countdown.
- A text field labelled "Type `<phrase>` to confirm"; the danger button **Delete** is disabled until the text matches exactly. Volume plans add the line "Volumes hold data. This cannot be undone."
- On confirm, the dialog shows the operation output with the existing `OperationPanel`; on end, a toast and the lists refresh. An expired plan (404) offers **Re-check**.
- Plan and helpers live in `web/src/lib/storage/storage.ts` (types, chips/filters, sort, `reclaimText(reclaim)`).

### 9.5 Accessibility and responsive
As the Containers spec §5.5: icon controls with `aria-label`/`title`; status by text, not colour; focus to `h1` after navigation; tables become cards under 720 px; the confirm field has an associated label and `aria-describedby` for the phrase.

## 10. CLI (`internal/cli/cmd_storage.go`)

- `stackstead storage df [-o table|json]`: the overview as a table (KIND, COUNT, SIZE, RECLAIMABLE, AS OF); `-o json` prints `GET /api/storage`.
- `stackstead storage prune <images|volumes|build-cache> [--preview] [--yes] [--detach]`:
  - creates the bulk plan and prints items, protected items with reasons, and the reclaim figure;
  - `--preview`: stop, exit 0;
  - otherwise on a terminal, prompt `Type "<phrase>" to confirm:` (new helper `confirmTyped` next to `confirm` in `prompt.go`); `--yes` sends the plan's phrase without prompting and is required without a terminal (exit 2 otherwise, as `stacks rm`);
  - then waits for the operation like `stacks up` (exit 9 on a failed operation) unless `--detach`.
- Exit codes follow `docs/cli.md`: 409 → 5, 429 → 8, 503 → 7, wrong phrase → 6.

## 11. Security

- Session and JSON CSRF rule on every route; deletes only via plan + confirm.
- Server-enforced, re-checked at confirm time: stack-declared and stack-labelled volumes, rollback images, images used by any container, volumes in use. The typed phrase guards against mistakes, not against an attacker (any signed-in user is already root-equivalent; `docs/security.md` says so).
- `compose config` output is parsed for image refs and volume names only and never returned, stored or logged (it can contain interpolated secrets). Image inspect never returns env/cmd; volume inspect never returns driver options.
- `action_log` stores the username, item names/ids, bytes and Docker's message: no secrets.
- Docker error messages, labels, tags and names are rendered as text only (no `{@html}`).
- `docs/security.md` gains a paragraph: signed-in users can delete unused images, volumes (permanent data loss) and build cache; what is protected; that `action_log` records who deleted what.

## 12. Error handling

| Condition | Behaviour |
|---|---|
| Docker unreachable | lists/plans/confirm 503; UI keeps the last data with the global banner; Delete buttons disabled "Docker is unreachable" |
| df slow or failing | lists answer at once with `null` sizes and the kind's `error`; plans for that kind 503 "sizes unavailable, try again" |
| Plan expired | 404; dialog offers Re-check |
| Item changed between plan and confirm | skipped with the reason, logged; operation still succeeds |
| Docker refuses a delete (for example a container was created in between) | item `failed` with Docker's message; operation `failed`; later items still run |
| Compose config fails for a stack | declarations unknown; volumes still protected by project label |
| Rollback provider error | image plans 503 |
| `action_log` write fails | delete proceeds; output line "could not record action" |

## 13. Testing

Test-first for every behavior (global rules). Existing host collector tests cover the `internal/stale` extraction.

**Go — `internal/engine`** (fake socket server as in `client_test.go`):
- `TestImagesDecode`, `TestInspectImageOmitsEnv` (fixture has `Env`, result type has no field for it).
- `TestVolumesDecode`, `TestInspectVolumeOmitsOptions` (only the `type` option is decoded).
- `TestRemoveImageByRefNeverForces` (path, no `force` query, untagged/deleted decode), `TestRemoveImageMapsStatus` (404 → `ErrImageNotFound`, 409 → `ActionError`).
- `TestRemoveVolumeMapsStatus`.
- `TestDiskUsageTypedPerKind`, `TestDiskUsageOldAPIUsesFullDF` (one call fills all kinds).
- `TestPruneBuildCacheSendsIDFilter` (`filters` JSON, `all=true`, `SpaceReclaimed` decode), `TestPruneBuildCacheAllUnusedSendsNoFilter`, `TestBuildPruneByIDVersionGate` (below, at and above `buildPruneByIDMinAPI`; never sends a request).

**Go — `internal/storage`** (pure, table-driven):
- `TestClassifyImageStatus`: in use by a running container; by an exited container only (still `in-use`); tagged with none (`no-containers`); untagged with none (`dangling`).
- `TestClassifyImageMatchesContainersByID` (container's image string is a stale tag).
- `TestClassifyBuiltServiceDefaultName` (`<project>-<service>` declaration).
- `TestClassifyRollbackImageProtected` (dangling and protected; excluded from reclaimable).
- `TestClassifyVolumeConsumersIncludeStopped`.
- `TestClassifyVolumeDeclaredByStoppedStackIsUnusedProtected` (RefCount 0, stack stopped → `unused` + `Protection{stack}`; not in reclaimable).
- `TestClassifyVolumeProtectedByProjectLabelWhenDeclarationsUnknown`.
- `TestClassifyExternalVolumeDeclaration` (resolved external name).
- `TestReclaimableTotals` (images uses unique size; build cache skips in-use and shared).
- `TestDeclarationsCachedByHash` (second call with the same hash runs no subprocess; a new hash does), `TestDeclarationsFailureMarksUnknown`, `TestDeclarationsIgnoreEnvironment` (fixture with an environment secret; the secret appears nowhere in the result).
- Plans: `TestPlanDanglingImagesExcludesProtectedAndTagged`, `TestPlanUnusedVolumesListsProtectedSeparately`, `TestPlanSingleRefusals` (protected, used, in-use volume, unknown), `TestPlanConfirmPhrase` (single ref, bulk count, singular), `TestPlanExpires`, `TestPlanEviction`, `TestPlanReclaimRange`, `TestPlanBuildCacheFallbackIsLabelledNotExact` (mode `all-unused`, `exact:false`, warning present), `TestConfirmBuildCacheFallbackLogsOneRow`.
- Execution (fake engine): `TestConfirmWrongPhrase`, `TestConfirmSkipsItemNowUsed`, `TestConfirmSkipsVolumeNewlyDeclared`, `TestConfirmSkipsChangedFingerprint`, `TestConfirmDeletesByExplicitIDOnly` (a volume created after the plan is untouched), `TestConfirmUntagsOneTag`, `TestConfirmContinuesAfterFailure`, `TestConfirmWritesActionRows`, `TestConfirmLogFailureDoesNotStopDeletes`, `TestConfirmCancelStopsBeforeNextItem`.

**Go — `internal/store` / `internal/actionlog`:** `TestMigrationActionLog` (table and indexes exist); `Append` rejects a `kind` that does not match the pattern, a bad `scope` and an empty `result`; prune removes rows older than 365 days.

**Go — `internal/stacks`:** `TestOpsHostScopeKey` (empty stack key, `ErrBusy` for a second storage cleanup, listed by `All()`).

**Go — `internal/api`:** session required on every route; overview and list mapping (null sizes, `sizesAt`, `refreshing`); 2 s cache shared across callers; refresh 202 then 429 within 60 s; plan 201/404/409/503; confirm 202/400/404/409; image detail has no env key; volume detail has no options key; actions limit bounds; 503 while Docker is down; `GET /api/actions` works without Docker and filters by kind prefix and scope.

**Go — `internal/cli`:** `TestStoragePrunePreviewPrintsPlan` (exit 0, no confirm call); `TestStoragePruneNeedsYesWithoutTTY` (exit 2); `TestStoragePruneTypedPromptMismatch` (no confirm sent); `TestStoragePruneYesSendsPhrase`; `TestStorageDFJSON`.

**Vitest:** Storage tabs and `?tab=`; chips and counts; protected volume row shows the stack note and no Delete; rollback image row disabled with the reason; cleanup dialog: protected list not selectable, phrase gating (button disabled until exact match), reclaim text exact vs range, expired plan → Re-check, operation output then refresh; Recalculate disabled for 60 s; detail pages render without env/options; rail item, breadcrumbs, dashboard tile link.

**E2E (`cmd/stackstead/e2e_test.go`, `TestE2ESmoke`, with Docker):** create a labelled throwaway volume (`docker volume create`), find it `unused` in `/api/storage/volumes`; plan `{kind:"volume"}`, wrong phrase → 400, right phrase → 202, operation succeeds, volume gone, one `action_log` row with kind `storage.delete`; the smoke stack's named volume after `down` → shown `unused` + protected, single plan → 409; `docker tag busybox stackstead-e2e:tmp`, plan `{kind:"image", ref}`, confirm → `untagged`, busybox still present; `t.Cleanup` removes leftovers. Build cache: `TestE2EBuildPruneByIDHonoured` (§6.5) runs when a builder is available and is skipped otherwise; unit tests cover both request shapes.

**Visual:** headless-Chrome screenshots of `/storage` (each tab), the two detail pages and the cleanup dialog at 1280 and 375 px, dark and light.

**Acceptance:** all of the above pass; `go test ./...`, `go vet`, the web test suite and the e2e smoke pass; a protected volume cannot be deleted through any route; no route returns env, cmd or volume options.

## 14. Documentation

- `docs/api.md`: a **Storage** section (the build-cache exact and fallback paths, routes, fields, statuses, protections, plan/confirm, phrases, caching and `sizesAt`, host-scoped operations with `"stack": ""`, the shared `action_log` and `GET /api/actions`); the `/api/host` `volumes` note that it now shares storage's df cache (5 min instead of 10).
- `docs/cli.md`: `storage df`, `storage prune`.
- `docs/security.md`: §11 paragraph.
- `README.md`: one feature line ("Storage: see what uses disk and clean up safely, with previews").

## 15. Delivery

One plan (number assigned when the plan is written, as for the sibling specs), about 10–12 tasks: `internal/stale` extraction; engine calls; `ConfigJSON` and declarations; `storage.Classify`; migration and log; plans and execution with host-scoped ops; API routes; CLI; frontend shell + Storage page; detail pages + cleanup dialog; e2e, docs, visual pass.

## 16. Open questions

None open. Resolved with the user on 2026-10-18: build cache uses the exact path when the engine honours the `id` filter and the labelled fallback otherwise (§6.5); images declared by a stopped stack are single-deletable with a warning; plan TTL 5 min, `action_log` retention 365 days, df refresh 5 min; one shared `action_log` table owned by this spec; "Storage" names both this rail screen and the volume diagnostics stack-detail tab.
