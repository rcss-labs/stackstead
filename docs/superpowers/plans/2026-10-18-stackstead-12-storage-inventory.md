# Stackstead Plan 12 — Storage inventory

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** this plan contains tasks and examples only, with no code. Each task names the files to touch, the behaviour to build, example cases and the tests that prove it. Implementers choose the names of types and functions not given here and write the code test-first.

**Goal:** A **Storage** screen (`/storage`, tabs Overview, Images, Volumes, Build cache, plus a page for one image and one volume). It shows where disk goes: images, container writable layers, volumes and build cache, with what can be freed and what each item belongs to. Every delete is an exact plan, confirmed with a typed phrase, run as a live operation and recorded in a persistent action log. Volumes declared by a registered stack, running or stopped, can never be deleted here. Two CLI commands, `stackstead storage df` and `stackstead storage prune <kind>`, provide the same from a terminal.

**Spec:** `docs/superpowers/specs/2026-10-18-stackstead-storage-inventory-design.md` (the § references below point there). Branch from `main` at `27d5dd2`.

**Depends only on `main`.** This plan uses nothing from an unbuilt spec, and every task builds against the code on `main` today. Where the storage spec refers to another unbuilt spec, this plan either builds the needed piece itself in its simplest form or leaves the feature out. The full list is under **Departures from the spec**.

**Tech stack:** Go 1.26 standard library plus the existing SQLite driver; Svelte 5, TypeScript, Vitest. No new dependencies.

## Departures from the spec (user decision, 2026-10-18: no dependencies on unbuilt specs)

- **No rollback-image protection.** Spec §1, §3 (F1), §5.4 and §12 depend on the image updates feature, which is not built. This plan has no rollback provider, no "protected · rollback" status and no 503 for a provider error. Images are protected only by being in use, and a tagged image with no containers is never in a bulk plan. The image updates plan adds rollback protection when it is written.
- **Stack declarations come from the single compose file each stack has today.** Spec §5.3 refers to compose-forms' multi-file invocation, which is not built. The declarations cache key is the compose file plus the `.env` next to it.
- **The action log belongs to storage alone.** The table and package are built for storage's rows. Nothing in this plan mentions or prepares for other writers. Kinds are still validated by pattern (spec §5.5), so a later feature can add its own kind.
- **No links to the volume diagnostics tab.** "Declared by" links go to the stack page (`/stacks/<name>`).
- **The engine calls are built for storage only.** Spec §8 says other specs reuse them; this plan does not plan for that.

## Decisions (controller, recorded for the user)

Spec §3 holds the product decisions made with the user. These are the plan-level ones:

- **Container image id and container mounts come from the container list.** The engine's container list also reads each container's image id and its mounts (type, volume name, source, destination). Stopped containers' volumes are then known without inspecting each container.
- **The build-cache "by id" capability starts switched off.** Every engine takes the labelled fallback path (§6.5) until a real-engine test proves the id filter is honoured. Turning it on is a separate commit that names the engine version the test passed on. It is not part of any task's acceptance.
- **One storage service owns all storage state:** the 2-second list cache, the four per-kind disk-usage caches, the declarations cache and the plan store. The API asks the service and does no joining itself.
- **Plans take a fresh disk-usage reading for their kind** (60-second bound), and that reading also refreshes the cached one. Container layers are never part of a plan.
- **The dashboard Volumes tile reads storage's volume disk-usage cache** and drops its own 10-minute cache. Its sizes are then at most 5 minutes old (documented).
- **Action-log retention runs on the existing hourly metrics prune.** The sampler gets an optional list of extra prune steps; serve registers "delete action rows older than 365 days".
- **The confirm phrase is compared byte-exact on the server.** The CLI strips only the trailing newline from what was typed.
- **Plan ids** look like `p_` followed by 32 hex characters. There are at most 32 plans (the oldest is evicted), they expire after 5 minutes on the injected clock, and they are kept in memory only.
- **Host-scoped operations** use the stack key `""` with the action `storage-cleanup`. The web operations list and the CLI's `ops` output show such an operation as `host`, with no link.
- **Response shapes live in the shared API types package**, so the API, the Go client and the CLI agree. One key-contract test row is added per shape.
- **The TypeSafe skill** is an AI-feature SDK, not a decision scorer. Agents proceed without it and say so in handoffs, as in Plans 10a, 10b and 11.

## Global constraints

- Spec §1 success criteria (minus rollback, see Departures) and §11 security apply to every task.
- **Never delete what is protected or used.** These are refused when the plan is made (409) and checked again per item at confirm time (skipped): a stack-declared or stack-labelled volume, an image used by any container in any state, a volume in use. No remove call ever sets `force`.
- **Delete only what was previewed.** Confirm deletes by the exact id or name in the plan. Docker's prune endpoints are never used for images or volumes. The build-cache fallback runs only when the by-id capability is off, and its plan says "not exact".
- **No secrets leave the server.** From `compose config` output, only image references, build presence and volume names are read; the rest is never logged, stored or returned. Image details never include env, command or entrypoint. Volume details never include driver options except the option type (for example `nfs`).
- Every new route needs a session. Mutating routes need JSON bodies (the existing CSRF rule). Errors are `{error}`. Docker unavailable gives 503, except on the action-log route.
- Opening the screen never waits on disk usage: lists answer at once, with empty sizes before the first reading.
- No `{@html}`. Docker messages, tags, labels and names are shown as text.
- CLI exit codes: conflict 5, wrong phrase 6, unavailable 7, rate limited 8, failed operation 9, missing `--yes` without a terminal 2.
- Budgets are unchanged (entry JS ≤ 120 kB gz, total ≤ 400 kB gz). The new pages load neither CodeMirror nor uPlot.
- No new npm or Go dependencies. `go test ./...` passes without Node and without Docker. Cross-builds for linux amd64/arm64 and darwin pass.
- README: header, badges and feature table unchanged; one feature-list line added.
- Test-first for every behaviour change: write the test, run it, see it fail for the expected reason, implement, pass, commit. No tests for test helpers.
- `make check` and `make ui` pass at the end of every task that touches their inputs.
- Commits are conventional, explain why, and end with the session's `Co-Authored-By` and `Claude-Session` trailer lines.

## Review focus

1. **A stopped stack's volume reached by bypassing the UI.** A crafted single-volume plan for a stopped registered stack's volume answers 409 naming the stack. A bulk plan lists it under "protected", never under "items". A volume that becomes declared between plan and confirm is skipped. Protection holds through the compose project label when `compose config` fails. (T5, T8, T13.)
2. **Nothing outside the preview is touched.** A volume or image created after the plan survives the confirm. A changed item is skipped, for example an image that gained a tag or a volume recreated under the same name. Remove requests carry no `force`. (T2, T8.)
3. **Build cache never goes wide unannounced.** With the capability off, the plan is labelled not exact and the request has no id filter. With it on, the request carries exactly the re-checked ids. The capability check never sends a prune. (T3, T8.)
4. **Secrets.** A compose file with `DB_PASSWORD: hunter2` in `environment`, an image whose env holds a password, and an NFS volume whose options hold a password: none of those values appears in declarations, in logs, or in any route body. (T2, T4, T9.)
5. **Cost.** Fifty concurrent image-list requests cause one Docker image list. The overview never runs disk usage on the request path. A second manual refresh within 60 s gets 429. The dashboard tile and the Storage screen share one volume disk-usage reading. (T7, T9.)

---

## File structure

| Area | Files | Tasks |
|---|---|---|
| Stale cache | new `internal/stale/`; `internal/host/flight.go` removed | T1 |
| Engine | `internal/engine/client.go`, `types.go`, new fixtures in `testdata/`, `integration_test.go` | T2, T3 |
| Compose | `internal/composecli/runner.go` | T4 |
| Storage | new `internal/storage/` (declarations, classify, service, plans, execute) | T4, T5, T7, T8 |
| Action log | `internal/store/store.go` (migration), new `internal/actionlog/`, `internal/metrics/sampler.go` | T6 |
| Host tile | `internal/host/collector.go` | T7 |
| Operations | `internal/stacks/ops_test.go` | T8 |
| API | new `internal/api/storage.go`, `actions.go`; `server.go`; `internal/apitypes/types.go`; `internal/client/client.go`; `internal/cli/serve.go` | T7, T9 |
| CLI | new `internal/cli/cmd_storage.go`; `prompt.go`, `commands.go`, `cmd_ops.go` | T10 |
| Web | `web/src/lib/api/*`, new `web/src/lib/storage/*`, `web/src/lib/shell/*`, `App.svelte`, new `routes/Storage*.svelte`, `OperationsList.svelte`, dashboard tiles, `ContainerDetail.svelte` | T11, T12 |
| E2E and docs | `cmd/stackstead/e2e_test.go`, `docs/api.md`, `docs/cli.md`, `docs/security.md`, `docs/README.md`, `README.md` | T13 |

## Execution waves

| Wave | Tasks | Depends on |
|---|---|---|
| 1 | T1 stale cache, T2 engine lists/remove, T6 action log | — |
| 2 | T3 engine disk usage/build prune, T4 declarations | T2 |
| 3 | T5 classify | T2, T3, T4 |
| 4 | T7 service + host tile | T1, T3, T5 |
| 5 | T8 plans + execution | T6, T7 |
| 6 | T9 API + client | T8 |
| 7 | T10 CLI, T11 Storage page | T9 |
| 8 | T12 dialog + detail pages | T11 |
| 9 | T13 e2e, docs, visual | all |

Execution is sequential in task order. For each task, Sonnet implements test-first and Opus reviews; Fable does the final whole-branch review.

---

### Task 1: Move the stale cache to `internal/stale`

Spec §4. A behaviour-preserving refactor.

**Files:** create `internal/stale/` (moved from `internal/host/flight.go` and its test); update `internal/host/collector.go`.

**Behaviour:**
- Same semantics as the host cache today: one load at a time, background loads detached from the caller, panics recovered, and failures not retried before the maximum age.
- New for storage: ask when the value was last loaded, whether a load is running now, and the last load error (cleared by a success); and invalidate, so the next stale read starts a background load whatever its age.

**Examples:** before the first load there is no load time and nothing is refreshing; while a slow load runs, "refreshing" is true; after it finishes, the load time equals the test clock.

- [ ] **Step 1: Write the failing tests.** Move the existing flight tests (package rename only). Add tests for load time and refreshing, error cleared by a success, and invalidate forcing a background load.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement**, and switch the host collector over.
- [ ] **Step 4: Run** the race tests for `stale`, `host` and `api`, `go vet`, `gofmt`.
- [ ] **Step 5: Commit** `refactor: move the stale-while-refresh cache to internal/stale`.

---

### Task 2: Engine — images, volumes, inspect, remove, container image id and mounts

Spec §8, §4. Review focus 2 and 4.

**Files:** `internal/engine/client.go`, `types.go`; new fixtures for the image list, image inspect, volume list, volume inspect, and a container list with mounts.

**Behaviour:**
- **Image list** from `/images/json`: id, tags, digests, created time, size, labels. Placeholder tags such as `<none>:<none>` are dropped.
- **Image inspect:** id, tags, digests, created, labels, user (internal only), OS, architecture, variant, layer count. There are no fields for env, command or entrypoint.
- **Volume list** from `/volumes`, and **volume inspect:** name, driver, scope, mountpoint, created time, labels, and only the `type` driver option.
- **Remove image** by reference or id: a DELETE with the reference path-escaped and **no query string**. It reports which tags were untagged and which images were deleted.
- **Remove volume** by name: a DELETE with no query string.
- Both removes have their own 60-second limit, bounded by the caller.
- New errors for "no such image" and "no such volume" (404). A Docker 409 becomes the existing action error carrying Docker's message.
- **The container list** also reads the image id and the mounts (with the volume name for volume mounts).

**Examples:**
- Removing `nginx:1.25` sends `DELETE …/images/nginx%3A1.25` with an empty query and reports `untagged nginx:1.25`.
- An image fixture whose env holds `DB_PASSWORD=hunter2` gives a result where that string appears nowhere.
- A volume with options `type=nfs, o=addr=10.0.0.1,password=s3cret` gives option type `nfs`, and `s3cret` appears nowhere.
- A bind mount has an empty volume name.

- [ ] **Step 1: Write the failing tests:** image list decode; image inspect without env (a reflection walk finds no fixture secret); volume list decode; volume inspect without options; remove image never forces (method, escaped path, empty query, result decode); status mapping for both removes (404, 409, 500); container list decodes the image id and mounts.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement**, with a DELETE helper that shares the existing send and error classification.
- [ ] **Step 4: Run** the race tests for `engine`, `state` and `api`, and `go vet`.
- [ ] **Step 5: Commit** `feat: engine image and volume list, inspect and remove`.

---

### Task 3: Engine — disk usage per kind and build-cache prune

Spec §4, §6.5. Review focus 3.

**Files:** `internal/engine/client.go`, `types.go`, `integration_test.go`; disk-usage fixtures for each kind and for the full response.

**Behaviour:**
- **Disk usage for one kind** (images with size and shared size; containers with writable-layer size; volumes with size and ref count; build-cache records with type, description, size, in use, shared, last used, usage count).
  - On API 1.45 and later it asks `/system/df?type=<kind>`.
  - On older APIs, or when the typed call answers 400, it makes **one** full call and fills all four kinds.
- **The existing volume summary** (used by the dashboard) is rebuilt on top of the volume kind, with the same results.
- **Capability check "prune by id":** compares only the negotiated API version with a minimum version constant. It never sends a request. The constant starts above the highest supported version, so the capability is off.
- **Build-cache prune:** `POST /build/prune?all=true`, adding an id filter when ids are given. An empty id list makes no request. It reports Docker's reclaimed bytes and has a 10-minute limit, bounded by the caller.
- **Real-engine test** (existing integration tag): build two tiny images with no cache, read their build records, prune one by id, check that the other remains, and log the engine version. It is skipped when no builder exists.

**Examples:**
- API 1.47 and kind volume: one request with `type=volume`, only volumes filled.
- API 1.43: one request with no type, all four kinds filled.
- Prune with ids `a, b`: the filter decodes to `{"id":["a","b"]}`.
- Prune with no ids: the query has `all=true` and no filter.

- [ ] **Step 1: Write the failing tests:** typed per kind; old API uses the full call; 400 falls back; the volume summary is unchanged; prune sends the id filter; prune of all unused sends no filter; empty ids send no request; the capability check below, at and above the constant (overridden in tests) never sends a request.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the race tests for `engine` and `host`, and `go vet`. Run the integration test where Docker is available and record the result in the handoff.
- [ ] **Step 5: Commit** `feat: engine per-kind disk usage and build cache prune`.

---

### Task 4: Stack declarations from `compose config`

Spec §5.3. Review focus 4.

**Files:** `internal/composecli/runner.go`; new `internal/storage/` declarations file and `compose config` fixtures.

**Behaviour:**
- **The compose runner** gains "config as JSON" for a stack's compose file. It runs in the stack's directory. Errors carry the exit code and compose's error output only, never its standard output.
- **Declarations per stack:** stack name, project, stack state, whether they are known, the image references (service, reference, whether built locally) and the volumes (key, resolved name, external or not).
- **Only these fields are decoded:** the project name, each service's image and whether it has a build section, and each volume's name and external flag.
- **Default names:**
  - A built service without an image declares `<project>-<service>`.
  - A volume without a name resolves to `<project>_<key>`.
- **Caching:** by a hash of the compose file plus the `.env` beside it. At most 2 runs at once, one at a time per stack, 10 seconds per run.
- **On failure** (bad file, missing file, missing compose), the stack is marked "declarations unknown" with empty lists. Only a debug line naming the stack and the exit code is logged.

**Examples:**
- Project `media`, service `web` with `build: .` and no image: declares `media-web`.
- Volume key `db` without a name: `media_db`. `external: true, name: shared_data`: `shared_data`, external.
- A second request with the same file and `.env` runs no subprocess. Editing `.env` runs one.

- [ ] **Step 1: Write the failing tests:** arguments and working directory (a fake binary records them); errors contain no standard output; decode (image refs, the default build name, named, external and default volumes); cached by hash; failure marks unknown; environment ignored (`hunter2` appears in neither the result nor the captured log); concurrency limited to two.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the race tests for `composecli` and `storage`, and `go vet`.
- [ ] **Step 5: Commit** `feat: read stack image and volume declarations from compose config`.

---

### Task 5: Classify images and volumes

Spec §5.1, §5.2 (minus rollback). Review focus 1.

**Files:** new classify and types files in `internal/storage/`.

**Behaviour:** a pure function. Input: the image, volume and container lists, the disk-usage readings (with which kinds are loaded and when), the stack declarations, and the registered project → stack map. Output: classified images, volumes and build records, totals per kind, the five largest container writable layers, and the stacks whose declarations are unknown.
- **Image status:** `in-use` if any container uses it, matched by image id (never by the container's image string), in any state. Otherwise `dangling` if it has no tags, else `no-containers`.
- **Volume status:** `in-use` if its ref count is above 0 or any container mounts it, stopped containers included; otherwise `unused`.
- **Volume protection "stack":** the volume is declared by a registered stack, or it carries the compose project label of a registered stack's project.
- **Image sizes:** from disk usage when loaded, otherwise the list size. Shared size is empty until disk usage loads.
- **Volumes before the first volume reading:** size empty, marked "sizes pending", status from container mounts only.
- **Reclaimable:**
  - images: size minus shared size over dangling images;
  - volumes: size over unused, unprotected volumes;
  - build cache: size over records neither in use nor shared;
  - container layers: none.
- **Reference matching:** `nginx` and `docker.io/library/nginx:latest` match each other.
- **Sorting:** largest first, unknown sizes last, then by name.

**Examples:**
- An image used only by an exited container is `in-use`.
- A container whose image string is a tag that has since moved still matches its image by id.
- Stack `media` is stopped and its volume `media_db` has ref count 0: `unused`, protected by stack `media`, and not reclaimable.
- Declarations for `media` are unknown, but `media_db` has the project label `media`: still protected.
- An external volume `shared_data` declared by a stack is protected.

- [ ] **Step 1: Write the failing tests** (table-driven): image status; matching by id; default build name; volume consumers include stopped containers; stopped stack's volume is unused and protected; protection by project label when declarations are unknown; external volume declaration; reclaimable totals; provisional state before disk usage; reference normalization; sort order.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/storage` and `go vet`.
- [ ] **Step 5: Commit** `feat: classify images and volumes with stack protection`.

---

### Task 6: Action log

Spec §5.5 (storage only).

**Files:** `internal/store/store.go` (one appended migration); new `internal/actionlog/`; `internal/metrics/sampler.go`.

**Behaviour:**
- **The migration** creates the `action_log` table and its two indexes as specified in spec §5.5. The supported schema version goes up by one, and database backup and restore keep working.
- **Append** checks the row:
  - the kind looks like `word.word`, in lower case;
  - the scope is `host` or a valid stack name;
  - the result and the username are not empty.
  - The time defaults to now.
- **Recent** returns rows newest first, filtered by a kind prefix (taken literally: `%` and `_` are not wildcards) and an exact scope. The limit is 1–500, default 50.
- **Prune** deletes rows older than a given time.
- **The metrics sampler** runs optional extra prune steps after its own hourly prune. A failing step is logged as a warning; a panicking one is recovered.

**Examples:**
- Accepted kinds: `storage.delete`, `storage.prune`.
- Rejected kinds: `Storage.delete`, `storage`, `storage.delete.x`. Rejected scope: `../x`.
- A prefix of `storage.` finds both storage kinds. A prefix of `stor%` finds nothing.

- [ ] **Step 1: Write the failing tests:** the migration creates the table and indexes and bumps the version; append validation (table of the examples); recent filters, order and limit clamp; the prefix is literal; prune by age; the sampler runs extra prune steps.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the race tests for `store`, `actionlog`, `metrics` and `cli` (the database commands), and `go vet`.
- [ ] **Step 5: Commit** `feat: persistent action log for storage cleanups`.

---

### Task 7: Storage service and the dashboard Volumes tile

Spec §4, §3 "One volume-size source". Review focus 5.

**Files:** new service file in `internal/storage/`; `internal/host/collector.go`; `internal/cli/serve.go`.

**Behaviour:**
- **Cheap lists** (images, volumes, containers, registered stacks and their declarations) are combined behind one 2-second shared cache, on a context detached from the caller, with a 10-second bound. Errors are not cached.
- **One stale cache per disk-usage kind:** a 5-minute maximum age and a 60-second load limit. When a load fell back to the full call, all four kinds are stored.
- **The inventory** reads the disk-usage caches without waiting, which starts a background load when one is due. Each kind reports its load time, whether it is refreshing, and its last error.
- **Manual refresh** invalidates all four kinds. A second request within 60 s is refused with the remaining wait.
- **A fresh reading for plans** goes through the same cache, so a load already running is shared, and the result is stored.
- **A volume summary for the dashboard** comes from the volume cache. The host collector uses it when it is given one and no longer runs its own volume disk usage. With none given, the existing behaviour is unchanged.
- **Serve** builds the service and hands its volume summary to the host collector.

**Examples:**
- Disk usage hangs: the inventory still answers at once, with empty sizes and "refreshing".
- 50 simultaneous inventory requests cause one image list.
- Refresh at 12:00:00 is accepted. Refresh at 12:00:30 is refused with "retry in 30 s".

- [ ] **Step 1: Write the failing tests:** the inventory never blocks on disk usage; lists cached for 2 s across 50 callers; the full-call fallback fills all kinds; refresh rate limit; a fresh reading shares the load in flight; the volume summary comes from the cache; the collector uses the given volume source.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement**, including the serve wiring.
- [ ] **Step 4: Run** the race tests for `storage`, `host` and `cli`, and `go vet`.
- [ ] **Step 5: Commit** `feat: storage inventory service with background disk usage`.

---

### Task 8: Cleanup plans and execution

Spec §6. Review focus 1–3.

**Files:** new plans and execute files in `internal/storage/`; `internal/stacks/ops_test.go`.

**Behaviour:**
- **Plan requests:**
  - bulk: images with selection `dangling`; volumes or build cache with selection `unused`;
  - single image by reference or a 12–64 hex id;
  - single volume by name (letters, digits, `_`, `.`, `-`, starting with a letter or digit).
  - Anything else is a validation error.
- **Making a plan** uses fresh lists and a fresh disk-usage reading for its kind; a reading failure means "sizes unavailable".
  - Bulk images: dangling images only.
  - Bulk volumes: unused, unprotected volumes as items; unused protected ones listed separately with the stack and its state.
  - Single image: refused when used by containers, naming up to 3. A warning when a stopped stack declares it. Untagging one of several tags frees 0.
  - Single volume: refused when protected (naming the stack) or in use (naming up to 3 containers). Unknown item: not found.
  - Build cache:
    - capability on: the records neither in use nor shared, exact;
    - capability off: mode "all unused", not exact, the spec §6.5 warning, records listed as "expected".
- **Each item records a fingerprint:** image id plus tag set; volume name plus created time; build record id.
- **The confirm phrase** (spec §6.2) is the item's reference for one item, or a count phrase for a bulk plan (singular for one). Build cache always uses the count phrase. An untagged image is named by its 12-character short id.
- **Confirm:**
  - unknown or expired → not found; empty → conflict; phrase not exactly equal → wrong phrase;
  - otherwise it starts a host-scoped operation (one at a time; busy → conflict) and removes the plan, so it can't be confirmed twice.
- **The operation, per item in order:**
  1. Stop if cancelled.
  2. Re-read the item (volumes get one fresh volume reading per run).
  3. Skip it if it is gone ("already gone"), its fingerprint changed, it became used, or it became declared.
  4. Delete it by exact reference.
  5. Print one line and write one action-log row: kind `storage.delete` for single plans, `storage.prune` for bulk plans; scope `host`. If the row cannot be written, print "could not record action" and continue.
- **Build cache:**
  - exact path: one prune with the re-checked ids and one row per id;
  - fallback path: one prune of all unused cache, one row with target `build-cache/all-unused` and Docker's reclaimed bytes, and output lines for records missing from a fresh reading.
- **Afterwards:** invalidate the list cache and the kind's disk-usage cache. The operation fails if any item failed; later items still run.

**Examples:**
- Two unused volumes `old_data` and `test_tmp`, plus the stopped stack's `media_db`: items are the first two, `media_db` is listed as protected (stack `media`, stopped), and the phrase is `delete 2 volumes`.
- One volume: the phrase is `old_data`.
- A volume `late` created after the plan: still there after the confirm.
- `old_data` mounted by a new container after the plan: skipped, "now used by container x".
- Image tagged `nginx:1.25` and `nginx:latest`, plan for `nginx:1.25`: output `untagged nginx:1.25`, and the image remains.
- Docker refuses the first of two items: the first is `failed`, the second is `deleted`, and the operation is failed.

- [ ] **Step 1: Write the failing tests** (fake engine): each request validation case; dangling-images plan; unused-volumes plan with protected listed separately; each single refusal; phrase rules; expiry; eviction; image reclaim range; stopped-stack image warning; build-cache fallback is labelled not exact; wrong phrase; confirm twice is not found; skip when now used; skip when newly declared; skip when the fingerprint changed; delete by explicit id only; untag one tag; already gone is skipped; continue after failure; action rows written; a log failure does not stop deletes; cancel stops before the next item; build-cache fallback writes one row. In stacks: the host-scope key (empty key, busy on a second start, listed with all operations, does not block a stack's operations).
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the race tests for `storage` and `stacks`, and `go vet`.
- [ ] **Step 5: Commit** `feat: exact storage cleanup plans with typed confirmation`.

---

### Task 9: API routes and Go client

Spec §7, §11, §12. Review focus 1, 4 and 5.

**Files:** new `internal/api/storage.go` and `actions.go` with tests; `internal/api/server.go`; `internal/apitypes/types.go`; `internal/client/client.go`; `internal/cli/serve.go`.

**Behaviour:**
- **Routes:** exactly the spec §7 table.
- **Response shapes** match spec §6.2: camelCase; sizes may be null; lists are never null; the plan's `mode` appears only on fallback plans.
- **Status mapping:**
  - validation error → 400 with field errors; refused item → 409 or 404; sizes unavailable → 503;
  - unknown or expired plan → 404; empty plan or busy → 409; wrong phrase → 400 with field `confirm`;
  - Docker down → 503;
  - refresh → 202 with its start time, or 429 with the seconds to wait (also in `Retry-After`).
- **Path parameters:**
  - an image id in a path is 12–64 hex characters or an unambiguous prefix of at least 12 (ambiguous → 409, unknown → 404);
  - a volume name in a path follows the volume-name rule (400 otherwise).
- **Details:** image details never include env, command, entrypoint or user. Volume details never include options.
- **Confirm** records the signed-in username.
- **The action-log route** works without Docker and checks kind, scope and limit (1–500, else 400).
- **The Go client** gains: storage overview, create plan, confirm plan, refresh, recent actions.

**Examples:**
- `POST /api/storage/plans` with `{"kind":"volume","name":"media_db"}` on a stopped stack: 409 "declared by stack media; remove it with the stack (Remove → also delete volumes)".
- A wrong phrase gives 400 with field `confirm`.
- `GET /api/actions?kind=storage.&limit=600` gives 400.

- [ ] **Step 1: Write the failing tests:** session required on every route; overview and list mapping (null sizes, load time, refreshing); one computation for 50 callers; refresh 202 then 429; plan 201/400/404/409/503; confirm 202/400/404/409; the image details body has no env, command, entrypoint or user key and no fixture secret; the volume details body has no options; action limit bounds; 503 while Docker is down; the action route works without Docker and filters; the key-contract rows; the client contract.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement**, including the serve wiring.
- [ ] **Step 4: Run** the race tests for `api`, `apitypes`, `client` and `cli`, then `go test ./...` and `go vet`.
- [ ] **Step 5: Commit** `feat: storage and action log API routes`.

---

### Task 10: CLI — `storage df`, `storage prune`, host operations in `ops`

Spec §10.

**Files:** new `internal/cli/cmd_storage.go` with tests; `prompt.go`, `commands.go`, `cmd_ops.go`.

**Behaviour:**
- **`storage df`** prints a table: KIND, COUNT, SIZE, RECLAIMABLE, AS OF. The age is relative, `—` when unknown, with "refreshing" added while a load runs. `-o json` prints the overview.
- **`storage prune <images|volumes|build-cache>`:**
  1. Make the bulk plan.
  2. Print the items, the protected items with reasons, the warnings, and the reclaim line ("not exact" for fallback build-cache plans).
  3. An empty plan prints "nothing to clean" and exits 0.
  4. `--preview` stops after printing, exit 0.
  5. Otherwise, on a terminal, ask `Type "<phrase>" to confirm:`. A mismatch exits 6 and sends nothing.
  6. `--yes` sends the plan's phrase without asking. Without a terminal, `--yes` is required (exit 2).
  7. Then follow the operation like `stacks up` (exit 9 on failure), or print its id with `--detach`.
- **A typed-confirmation helper** sits next to the existing yes/no helper.
- **`ops` and `ops show`** print `host` for an empty stack.

**Examples:**
- `stackstead storage prune volumes --preview` prints two items and one protected volume, then exits 0.
- `echo | stackstead storage prune images` without a terminal exits 2: "refusing to continue without confirmation: use --yes".
- Typing `delete 2 volume` (missing the s) exits 6 and nothing is deleted.

- [ ] **Step 1: Write the failing tests:** preview prints the plan and makes no confirm call; `--yes` is needed without a terminal; a typed mismatch sends nothing; `--yes` sends the phrase; df JSON; an empty plan; fallback shows "not exact"; an unknown kind exits 2; the typed-confirmation helper table; `ops` shows `host`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the race tests for `cli`, and `go vet`.
- [ ] **Step 5: Commit** `feat: stackstead storage df and storage prune`.

---

### Task 11: Web — data layer, shell, Storage page and links

Spec §9.1, §9.2, §9.5 (minus rollback).

**Files:** `web/src/lib/api/` types and client; new `web/src/lib/storage/` helpers; `web/src/lib/shell/` rail, breadcrumbs and icons; `App.svelte`; new `routes/Storage.svelte`; `OperationsList.svelte`; dashboard tiles; `ContainerDetail.svelte`. Tests beside each.

**Behaviour:**
- **Rail:** a **Storage** entry after Networks with a new disk icon, current on `/storage` and below. Breadcrumbs and titles follow spec §9.1.
- **Tabs:** Overview, Images, Volumes, Build cache, selected by `?tab=`.
  - Overview: four tiles, sizes age with **Recalculate**, top five container layers, the "declarations unknown" banner, the last 20 storage actions.
  - Images: chips All / In use / No containers / Dangling.
  - Volumes: chips All / In use / Unused / Protected, with "unused · protected" and the stack note linking to `/stacks/<name>`.
  - Build cache: totals and records.
- **Helpers:** filter and count per chip; search; reclaim text; the reason a Delete button is disabled; a refresh controller.
- **Refresh:** on mount, after an operation ends, and on live change events (the debounced helper the Containers list uses). Nothing runs while hidden. Every 10 s only while a kind is refreshing and the page is visible. Out-of-date responses are ignored.
- **Recalculate** is disabled for 60 s after use, with the reason in its tooltip. A 429 shows the server's message.
- **Links:**
  - the dashboard Volumes tile → `/storage?tab=volumes`;
  - container page volume mounts → `/storage/volumes/<name>`;
  - the container header image → `/storage/images/<short id>`.
- **The operations list** shows `host` for an empty stack, with no link.
- **The cleanup and Delete buttons** open the T12 dialog. Until T12 is built they are disabled. T12 lands on the same branch before review.

**Examples:**
- Reclaim text: "Frees 1.0 GB" when exact; "Frees between 120 MB and 480 MB" for a range; "Frees about 2.0 GB (not exact)" for the fallback.
- A volume row for `media_db` (stack media, stopped) shows "unused · protected — declared by stack media (stopped), remove via the stack" and has no Delete button.
- An image used by 2 containers has Delete disabled with "used by 2 containers".

- [ ] **Step 1: Write the failing tests:** filters, counts, reclaim text table and disabled reasons (in use, protected, Docker down); controller timers and visibility; tabs and `?tab=`; chips and counts; the protected volume row; "calculating…" while sizes are pending; the declarations-unknown banner; Recalculate disabled for 60 s; recent actions; rail and breadcrumbs; route and title; the tile link; container page links; `host` in the operations list.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm run check`, `npm test`, `npm run build` (budgets).
- [ ] **Step 5: Commit** `feat: Storage screen with overview, images, volumes and build cache`.

---

### Task 12: Web — cleanup dialog and detail pages

Spec §9.3, §9.4, §9.5.

**Files:** new `web/src/lib/storage/CleanupDialog.svelte`, `routes/StorageImage.svelte`, `routes/StorageVolume.svelte` with tests; `App.svelte`; `routes/Storage.svelte` (enable the buttons).

**Behaviour:**
- **Opening the dialog** creates the plan and shows "Checking current state…" meanwhile.
  - A 409 or 503 shows the server's message with Close.
  - An empty plan shows "Nothing to clean".
- **Otherwise it shows:** items with sizes, the reclaim text, protected items (not selectable, each with its reason and stack link), warnings, a countdown to expiry, and "not exact" beside the phrase for fallback plans.
- **The confirm field** is labelled "Type `<phrase>` to confirm" and described by the phrase. The danger **Delete** button stays disabled until the text matches exactly. Volume plans add "Volumes hold data. This cannot be undone."
- **On confirm** it shows the existing operation panel. When the operation ends: a toast, then the lists refresh.
  - An expired plan offers **Re-check**, which makes a new plan.
  - A wrong phrase shows the field error.
- **Focus** moves to the field on open. Escape closes the dialog before confirm.
- **Detail pages** follow spec §9.3. An unknown image or volume shows "not found" with a link back. Delete opens the dialog for that one item, or is disabled with the reason. After a delete, the page returns to the matching tab.

**Examples:**
- Plan phrase `delete 2 volumes`: typing `delete 2 volumes ` (trailing space) keeps Delete disabled.
- After 5 minutes the countdown reaches 0 and Delete becomes **Re-check**.

- [ ] **Step 1: Write the failing tests:** protected items not selectable; phrase gating; reclaim text exact, range and not exact; expired → Re-check; operation output then refresh; a 409 shown; empty plan; focus on open; Escape closes; detail pages render no env or options even when the fixture has extra keys; not found; Delete disabled with the reason.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm run check`, `npm test`, `npm run build`.
- [ ] **Step 5: Commit** `feat: storage cleanup dialog and image and volume pages`.

---

### Task 13: E2E, docs, final checks, visual pass

Spec §13 (e2e, visual, acceptance), §14.

**Files:** `cmd/stackstead/e2e_test.go`, `docs/api.md`, `docs/cli.md`, `docs/security.md`, `docs/README.md`, `README.md`.

**Behaviour:**
- **E2E** (the existing smoke test, with Docker), with cleanup that runs even on failure:
  - A labelled throwaway volume shows as unused. A single plan with the wrong phrase gives 400; the right phrase gives 202. The operation succeeds, the volume is gone, and one `storage.delete` row exists.
  - The smoke stack's volume after `down` is unused and protected: a single plan gives 409, and the bulk plan lists it as protected.
  - `docker tag busybox stackstead-e2e:tmp`, then plan and confirm: untagged, and busybox remains.
  - The overview answers before the first disk-usage reading.
  - `storage prune volumes --preview` exits 0.
  - The action route returns the row.
- **Docs:**
  - `docs/api.md`: the Storage section, including the build-cache paths, the per-id bytes rule, `"stack": ""` for host operations, and the Volumes tile's new 5-minute age.
  - `docs/cli.md`: the two commands and their exit codes.
  - `docs/security.md`: the §11 paragraph, without rollback.
  - `docs/README.md`: hub rows.
  - `README.md`: one feature line.
- **Final checks:** `make check`; `make ui`; `go test ./...` without Node on the PATH; the cross-builds; the e2e; the integration engine tests where Docker is available (report the build-cache test result and engine version).
- **Visual pass** (headless Chrome, scratch config and data, a throwaway stack, one stray volume, one dangling image): every tab, both detail pages and the dialog (a volume plan with a protected item; a fallback build-cache plan), at 1280 and 375 px, dark and light. Report issues without fixing them in this step. Clean up containers, volumes and images and stop processes.

- [ ] **Step 1: Write the e2e assertions** and run them. If they pass the first time, prove they can fail with a temporary wrong expectation, then restore it.
- [ ] **Step 2: Write the docs.**
- [ ] **Step 3: Final checks and visual pass.**
- [ ] **Step 4: Commit** `test: storage inventory e2e` and `docs: storage screen, storage CLI and action log`.
- [ ] **Step 5: Hand off** for the Fable final review. Visual findings are fixed before that review. Then merge to `main`.

## Out of scope

- Everything under **Departures from the spec**: rollback protection, multi-file compose stacks, other writers of the action log, the volume diagnostics link.
- Spec §2 Out: removing containers, bulk deletion of tagged unused images, forced removal, pull/tag/push, creating, backing up or browsing volumes, mount checks, logging non-storage actions, single-item deletes from the CLI.
- Switching on the build-cache by-id capability: a follow-up commit, made only after the real-engine test passes.
