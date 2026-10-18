# Stackstead Plan 12 — Storage inventory

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** this plan specifies *what* to build: files, signatures, behaviour, test cases and acceptance criteria. It deliberately contains no full implementations; implementers write the code test-first. Short snippets show shapes only.

**Goal:** A **Storage** screen (`/storage`, tabs Overview, Images, Volumes, Build cache, and detail pages for one image and one volume). It shows where disk goes: images, container writable layers, volumes and build cache, with what can be freed and what each item belongs to. Every delete is an exact plan, confirmed with a typed phrase, run as a live operation and logged in a persistent `action_log`. Volumes declared by a registered stack (running or stopped) and rollback images can never be deleted here. Two CLI commands, `stackstead storage df` and `stackstead storage prune <kind>`, provide the same from a terminal.

**Architecture:**
- `internal/stale` (new): the panic-safe stale-while-refresh cache, moved out of `internal/host` unchanged as `stale.Value[T]`.
- `internal/engine`: image and volume list, inspect and remove; per-type `DiskUsage`; `PruneBuildCache`; `BuildPruneByID`; `Container.ImageID` and `Container.Mounts`.
- `internal/composecli`: `Runner.ConfigJSON`.
- `internal/actionlog` (new) and one `internal/store` migration: the shared `action_log` table.
- `internal/storage` (new): `Declarations` (compose config per stack, cached by hash), `Classify` (the pure join), `Service` (list caches, df caches, inventory), `Plans` (create, confirm, execute).
- `internal/stacks/ops.go`: host-scoped operations under the reserved key `""`.
- `internal/api/storage.go` (+ `actions.go`): the `/api/storage/*` routes and `GET /api/actions`.
- `internal/client` and `internal/cli/cmd_storage.go`: `storage df` and `storage prune`.
- `web/src/routes/Storage.svelte`, `StorageImage.svelte`, `StorageVolume.svelte`, `web/src/lib/storage/*`: the screen, the detail pages and the cleanup dialog.

**Tech Stack:** Go 1.26 stdlib only, plus the existing `modernc.org/sqlite`. Svelte 5, TypeScript, Vitest (jsdom). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-18-stackstead-storage-inventory-design.md` (the § references below point there). Branch from `main` at `27d5dd2`.

**Independence (user decision, 2026-10-18):** this plan is built on its own. It does not wait for, depend on, or share tasks with the compose-forms, image-updates, volume-diagnostics or any other unbuilt spec, and it builds against `main` as it is today. Every piece the spec borrows from a sibling spec is built here in its simplest form: today's single compose file instead of compose-forms' `Invocation`, the engine fields and calls storage needs, and a rollback provider that returns nothing. When a sibling plan is written later, adapting to this code is part of that plan: moving `ConfigJSON` to `Invocation`, supplying real rollback images, writing `mount.fix` rows to `action_log`. No task here leaves a hook, a placeholder or a TODO for another feature beyond the two interfaces the spec requires (`RollbackImages`, the `action_log` kind pattern).

## Decisions (controller, recorded for the user)

Spec §3 holds the product decisions made with the user. These are the plan-level ones:

- **`ConfigJSON` takes today's single compose file, not an `Invocation`.** Spec §5.3 refers to compose-forms §7.2, which is not built, and this plan does not wait for it (Independence). `composecli.Runner` methods take `file string` and `stacks.Entry` has one `ComposePath`, so `ConfigJSON(ctx, file string) ([]byte, error)` follows the current `Config`/`ProjectName` shape. The declarations cache key is SHA-256 of the compose file plus the `.env` next to it, if there is one.
- **This plan adds `engine.Container.ImageID` and `engine.Container.Mounts` itself.** `Mounts` is decoded from the `/containers/json` list (`Type`, `Name`, `Source`, `Destination`), so a stopped container's volumes are known without an inspect per container. The existing `engine.Mount` type gains a `Name` field.
- **Engine signatures are the spec's §8 list, verbatim**, built here for storage's needs only. `ErrNotFound` keeps its container meaning; `ErrImageNotFound` and `ErrVolumeNotFound` are new and sit beside `ErrNetworkNotFound`.
- **Rollback images:** `storage.NoRollback` is the only provider wired in this plan, so no image is rollback-protected until the image-updates feature supplies one. The interface, the protection logic and the 503-on-provider-error rule are still built and tested here with fakes.
- **`action_log` is created and owned here**, with only storage writing to it. `actionlog` validates kinds by pattern, not by a list, so later features add kinds without changing this code.
- **The `BuildPruneByID` gate starts closed.** `buildPruneByIDMinAPI` starts as `"99.0"`, above `maxAPIVersion`. T3 adds `TestE2EBuildPruneByIDHonoured` under the existing integration build tag. The constant is lowered only in a separate commit, when that test has passed on a real engine, and the commit message names the engine version it ran against. If no builder is available while this plan is carried out, the constant stays closed and the handoff says so.
- **One `storage.Service` owns all storage state.** That covers the 2 s list caches (`flightCache`-shaped, but in `internal/storage` so plans and the API share them), one `stale.Value[DFSnapshot]` per df kind, the declarations cache and the plan store. The API holds an interface over it (`api.StorageAPI`) and does no joining of its own.
- **Plans take a synchronous fresh df for their kind** with a 60 s bound, and that result also refreshes the kind's stale value. Container-layer df is never part of a plan.
- **The dashboard volume tile reads storage's `DFVolume` stale value.** `host.Config` gains `Volumes interface{ VolumeUsage() (engine.VolumeUsage, bool) }`. When it is set, the collector uses it and drops its own 10-minute volume cache. Serve passes the storage service. `docs/api.md` notes the 5-minute age.
- **`action_log` retention runs on the existing hourly tick.** `metrics.Sampler` gains an optional `Pruners []func(ctx context.Context, now time.Time)`, called after its own prune. Serve registers `actionlog.Log.PruneOlderThan(now − 365 d)`. That gives one hourly schedule, not a second ticker.
- **The confirm phrase is compared byte-exact on the server.** The CLI strips only the trailing newline from what was typed. The web field compares the raw value.
- **Plan ids are `p_` plus 16 random bytes in hex.** Plans live only in memory: at most 32 (the oldest is evicted), a 5-minute TTL measured with the injected clock.
- **Host-scoped operations:** `stacks.Ops.Start("", "storage-cleanup", fn)`. No change to `Ops` itself is expected (`byStack[""]` already works). T8 adds the test that pins it. The UI's operations list and the CLI's `ops` output render an empty stack as `host` and link nowhere.
- **DTOs live in `internal/apitypes`**, so the API, the Go client and the CLI share one shape. One key-contract test row is added per DTO.
- **TypeSafe AI skill:** the installed `typesafe-ai` skill is an AI-feature SDK, not a decision scorer. Agents proceed without it and say so in their handoffs (same as Plans 10a, 10b and 11).

## Global Constraints

- Spec §1 success criteria and §11 security apply to every task.
- **Never delete what is protected or used.** A stack-declared or stack-labelled volume, a rollback image, an image used by any container in any state, or a volume in use is refused at plan time (409) **and** re-checked per item at confirm time (skipped). No engine remove call sets `force`. Bulk image plans contain only dangling, unprotected images.
- **Delete only what was previewed.** Confirm deletes by explicit id or name from the plan. Docker prune endpoints are never used for images or volumes. The build-cache fallback (`/build/prune?all=true` without ids) runs only when `BuildPruneByID` is false, and its plan says "not exact".
- **No secrets leave the server.** `compose config` output is decoded into a struct with only the §5.3 fields and is never logged, stored or returned. `ImageInspect` has no `Env`/`Cmd`/`Entrypoint` field. `VolumeInspect` decodes only `Options["type"]`. Tests prove each of these on the JSON body.
- Every new route uses `requireSession`. Mutating routes require JSON bodies (the existing CSRF rule). Errors are `{error}`. Docker unavailable gives 503, except for `GET /api/actions`.
- Opening the screen never waits on `system df`: lists answer from the stale values (`null` sizes before the first df).
- No `{@html}`. Docker messages, tags, labels and names are interpolated as text.
- Exit codes come from `internal/cli/exit.go` constants only (409 → 5, wrong phrase → 6, 503 → 7, 429 → 8, failed operation → 9, missing `--yes` without a terminal → 2).
- Budgets are unchanged: entry JS ≤ 120 kB gz, total ≤ 400 kB gz. The new routes import neither CodeMirror nor uPlot.
- No new npm or Go dependencies. `go test ./...` passes without Node and without Docker. `CGO_ENABLED=0 GOOS=linux GOARCH={amd64,arm64} go build ./...` and `GOOS=darwin go build ./...` pass.
- README: the header, badges and feature table stay unchanged. Only one feature-list line is added.
- Test-first for every behaviour change: write the test, run it, see it fail for the expected reason, implement, pass, commit. No tests for test helpers.
- `make check` and `make ui` pass at the end of every task that touches their inputs.
- Commits are conventional, explain why, and end with exactly:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_015KyLExcq5vhZy8mjMamyBu
  ```

## Review Focus

1. **A stopped stack's volume reached by bypassing the UI.** A crafted `POST /api/storage/plans {"kind":"volume","name":"<stack volume>"}` for a stopped registered stack answers 409 naming the stack. A bulk plan lists it under `protected`, never under `items`. A volume that becomes declared between plan and confirm is skipped. Protection holds through the project label when `compose config` fails. (T5 `TestClassifyVolumeDeclaredByStoppedStackIsUnusedProtected`, `TestClassifyVolumeProtectedByProjectLabelWhenDeclarationsUnknown`; T8 `TestPlanSingleRefusals`, `TestConfirmSkipsVolumeNewlyDeclared`; T13 e2e.)
2. **Nothing outside the preview is touched.** A volume or image created after the plan survives the confirm. A changed fingerprint (new tag, recreated volume with a new `CreatedAt`) is skipped. Remove calls carry no `force`. (T2 `TestRemoveImageByRefNeverForces`; T8 `TestConfirmDeletesByExplicitIDOnly`, `TestConfirmSkipsChangedFingerprint`.)
3. **Rollback images.** A dangling image in the rollback list is protected, excluded from reclaimable, refused singly, and skipped if it joined the list after the plan. A rollback-provider error makes image plans 503. (T5 `TestClassifyRollbackImageProtected`; T8 `TestPlanImagesRollbackError503`.)
4. **Build cache never goes wide unannounced.** With the gate closed, the plan has `exact:false`, `mode:"all-unused"` and the warning, and the request has no `filters`. With the gate open, the request carries exactly the re-checked ids. The capability check never sends a request. (T3 `TestBuildPruneByIDVersionGate`, `TestPruneBuildCacheSendsIDFilter`; T8 `TestPlanBuildCacheFallbackIsLabelledNotExact`.)
5. **Secrets.** The `compose config` fixture carries `environment: {DB_PASSWORD: hunter2}`; that string appears nowhere in `StackDeclarations`, in logs captured by the test logger, or in any route body. The image inspect fixture with `Env` and the volume inspect fixture with `o: "addr=…,password=…"` leak nothing. (T2, T4, T9.)
6. **Cost.** Fifty concurrent `GET /api/storage/images` calls cause one `/images/json`. `GET /api/storage` never calls df on the request path. A manual refresh within 60 s gets 429. The dashboard tile and the Storage screen share one volume df. (T7, T9.)

---

## File Structure

```
internal/stale/stale.go, stale_test.go         Value[T] moved from host/flight.go (T1)
internal/host/flight.go                        removed; collector uses stale.Value (T1)
internal/host/collector.go                     Config.Volumes provider (T7)
internal/engine/client.go, types.go            images, volumes, remove, df, build prune, Container.ImageID/Mounts (T2, T3)
internal/engine/testdata/                      images.json, image_inspect.json, volumes.json, volume_inspect.json, df_*.json (T2, T3)
internal/engine/integration_test.go            TestE2EBuildPruneByIDHonoured (T3)
internal/composecli/runner.go                  ConfigJSON (T4)
internal/storage/declarations.go               Declarations, StackDeclarations, compose config decode, cache (T4)
internal/storage/classify.go, types.go         Inputs, Inventory, Image, Volume, BuildRecord, KindTotals, Classify (T5)
internal/storage/rollback.go                   RollbackImages interface + noRollback (T5)
internal/store/store.go                        action_log migration (T6)
internal/actionlog/actionlog.go                Entry, Filter, Log.Append/Recent/PruneOlderThan (T6)
internal/metrics/sampler.go                    Pruners hook (T6)
internal/storage/service.go                    Service: caches, df stale values, Inventory, Refresh, VolumeUsage (T7)
internal/storage/plans.go, execute.go          Plan store, Create, Confirm, run (T8)
internal/stacks/ops_test.go                    TestOpsHostScopeKey (T8)
internal/apitypes/types.go                     Storage* DTOs, Plan DTOs, ActionEntry (T9)
internal/api/storage.go, actions.go, server.go routes, Options.Storage, Options.Actions (T9)
internal/client/client.go                      Storage, StorageRefresh, CreatePlan, ConfirmPlan, Actions (T9)
internal/cli/serve.go                          wiring (T7, T9)
internal/cli/cmd_storage.go, prompt.go, commands.go, cmd_ops.go   storage df/prune, confirmTyped, host label (T10)
web/src/lib/api/types.ts, client.ts            storage types + calls (T11)
web/src/lib/storage/storage.ts                 chips, filters, sort, reclaimText, controller (T11)
web/src/lib/shell/Rail.svelte, breadcrumb.ts, icons.ts   Storage entry, disk icon (T11)
web/src/App.svelte                             routes /storage, /storage/images/:id, /storage/volumes/:name (T11, T12)
web/src/routes/Storage.svelte                  tabs Overview / Images / Volumes / Build cache (T11)
web/src/lib/stacks/OperationsList.svelte       empty stack → "host" (T11)
web/src/lib/dashboard/tiles.ts, routes/ContainerDetail.svelte   links (T11)
web/src/lib/storage/CleanupDialog.svelte       plan → phrase → OperationPanel (T12)
web/src/routes/StorageImage.svelte, StorageVolume.svelte   detail pages (T12)
cmd/stackstead/e2e_test.go                     storage e2e (T13)
docs/api.md, docs/cli.md, docs/security.md, README.md   (T13)
```

## Execution waves

| Wave | Tasks | Depends on |
|---|---|---|
| 1 | T1 stale, T2 engine lists/remove, T6 action log | — |
| 2 | T3 engine df/build prune, T4 declarations | T2 (shared client helpers) |
| 3 | T5 classify | T2, T3 (types), T4 (`StackDeclarations`) |
| 4 | T7 service + host wiring | T1, T3, T5 |
| 5 | T8 plans + execution | T6, T7 |
| 6 | T9 API + client | T8 |
| 7 | T10 CLI, T11 web shell + Storage page | T9 |
| 8 | T12 detail pages + cleanup dialog | T11 |
| 9 | T13 e2e, docs, visual | all |

Execution is sequential in task order. For every task, Sonnet implements test-first and Opus reviews; Fable does the final whole-branch review. The spec estimated 10–12 tasks. This plan has 13, because it keeps the engine df work (T3) and the action log (T6) separate so each review stays small.

---

### Task 1: `internal/stale` — move the stale cache out of `host`

Spec §4 (second bullet). This is a behaviour-preserving refactor.

**Files:**
- Create: `internal/stale/stale.go`, `internal/stale/stale_test.go`
- Delete: `internal/host/flight.go`; move `internal/host/flight_test.go` to `internal/stale/stale_test.go`
- Modify: `internal/host/collector.go`

**Interfaces:**
- Produces:
  ```go
  type Value[T any] struct{ /* as host.cached[T] */ }
  func New[T any](now func() time.Time) *Value[T]
  func (v *Value[T]) Get(ctx context.Context, maxAge time.Duration, load func(context.Context) (T, error)) (T, error)
  func (v *Value[T]) GetStale(maxAge time.Duration, load func() (T, error)) (T, bool)
  // New for storage: report when the value was loaded and whether a refresh is running.
  func (v *Value[T]) At() (time.Time, bool)
  func (v *Value[T]) Refreshing() bool
  func (v *Value[T]) Err() error // last load error, nil after a success
  func (v *Value[T]) Invalidate()   // next GetStale starts a background load regardless of age
  ```

**Behaviour:** same semantics as `host.cached` today: single-flight, a detached background load, panic-safe, and failures not retried before `maxAge`. `At`, `Refreshing`, `Err` and `Invalidate` are additive.

- [ ] **Step 1: Write the failing tests.** Move the existing flight tests unchanged (package rename only). Add `TestAtAndRefreshing` (zero before the first load; `Refreshing` true while a load blocks; `At` is the injected clock's time after it), `TestErrClearedBySuccess` and `TestInvalidateForcesBackgroundLoad`.
- [ ] **Step 2: Run to verify they fail:** `go test ./internal/stale -v` (the package is missing).
- [ ] **Step 3: Implement.** Switch `host.Collector` to `stale.Value`.
- [ ] **Step 4: Run** `go test -race ./internal/stale ./internal/host ./internal/api`, `go vet ./...`, `gofmt -l .`.
- [ ] **Step 5: Commit** `refactor: move the stale-while-refresh cache to internal/stale` (why: storage needs the same df cache the host collector uses).

---

### Task 2: Engine — images, volumes, inspect, remove, container image id and mounts

Spec §8 and §4. Review Focus 2 and 5.

**Files:**
- Modify: `internal/engine/client.go`, `internal/engine/types.go`
- Create: `internal/engine/testdata/images.json`, `image_inspect.json`, `volumes.json`, `volume_inspect.json`, `containers_mounts.json`
- Test: `internal/engine/client_test.go`, `types_test.go`

**Interfaces:**
- Produces:
  ```go
  var ErrImageNotFound  = errors.New("docker engine: no such image")
  var ErrVolumeNotFound = errors.New("docker engine: no such volume")

  type Image struct { ID string; RepoTags, RepoDigests []string; Created time.Time; Size int64; Labels map[string]string }
  type ImageInspect struct {
      ID string; RepoTags, RepoDigests []string; Created time.Time
      Labels map[string]string; User string   // internal: never returned by a route
      Os, Architecture, Variant string; Layers int
  }                                           // no Env, Cmd, Entrypoint fields
  type ImageDeleteResult struct{ Untagged, Deleted []string }
  type Volume struct { Name, Driver, Scope, Mountpoint string; CreatedAt time.Time; Labels map[string]string }
  type VolumeInspect struct { Volume; OptionType string } // only Options["type"]

  // Container gains:
  ImageID string     // "sha256:…" from the list's ImageID
  Mounts  []Mount    // from the list; Mount gains Name (volume name; empty for binds)

  func (c *Client) Images(ctx context.Context) ([]Image, error)
  func (c *Client) InspectImage(ctx context.Context, id string) (ImageInspect, error)
  func (c *Client) RemoveImage(ctx context.Context, ref string) (ImageDeleteResult, error)
  func (c *Client) Volumes(ctx context.Context) ([]Volume, error)
  func (c *Client) InspectVolume(ctx context.Context, name string) (VolumeInspect, error)
  func (c *Client) RemoveVolume(ctx context.Context, name string) error
  ```

**Behaviour:**
- `Images`: `GET /images/json` (no `all`). `<none>:<none>` and `<none>@<none>` entries are dropped from `RepoTags` and `RepoDigests`, and nil slices become empty slices.
- `Volumes`: `GET /volumes`, decoding `Volumes[]`. A `null` list gives an empty slice. `Options` are not decoded.
- `RemoveImage`: `DELETE /images/{ref}` with the ref path-escaped and **no query parameters**. It decodes `[{Untagged},{Deleted}]`. 404 wraps `ErrImageNotFound`; 409 gives `*ActionError{Status:409}` with Docker's message. It has its own 60 s timeout, bounded by the caller's context.
- `RemoveVolume`: `DELETE /volumes/{name}` with no query and a 60 s timeout. 204 → nil; 404 → `ErrVolumeNotFound`; 409 → `*ActionError`.
- The inspect calls use the short client timeout and map 404 to the matching sentinel.
- Existing decoders and tests still pass. `Container.Mounts` stays empty for containers without mounts.

- [ ] **Step 1: Write the failing tests:** `TestImagesDecode` (tags with `<none>:<none>` dropped; created from unix seconds), `TestInspectImageOmitsEnv` (the fixture has `Config.Env` with `DB_PASSWORD=hunter2`, plus `Cmd` and `Entrypoint`; a reflection walk over the result finds no string containing them), `TestVolumesDecode`, `TestInspectVolumeOmitsOptions` (the fixture's `Options: {type: nfs, o: "addr=10.0.0.1,password=s3cret"}` gives `OptionType == "nfs"` and `s3cret` nowhere), `TestRemoveImageByRefNeverForces` (method DELETE, escaped path `nginx%3A1.25`, `RawQuery == ""`, untagged/deleted decode), `TestRemoveImageMapsStatus` (404/409/500), `TestRemoveVolumeMapsStatus`, `TestContainersDecodesImageIDAndMounts` (a volume mount with its name, a bind mount with an empty name).
- [ ] **Step 2: Run to verify they fail:** `go test ./internal/engine -run 'Image|Volume|Mounts' -v`.
- [ ] **Step 3: Implement.** Add a `delete` helper beside `postJSON` that shares `send` and the error classification.
- [ ] **Step 4: Run** `go test -race ./internal/engine ./internal/state ./internal/api`, `go vet ./...`.
- [ ] **Step 5: Commit** `feat: engine image and volume list, inspect and remove` (why: storage needs exact deletes by id or name, without exposing env or driver options).

---

### Task 3: Engine — per-type disk usage and build cache prune

Spec §4, §6.5, §8. Review Focus 4.

**Files:** Modify `internal/engine/client.go`, `types.go`, `integration_test.go`. Create `testdata/df_image.json`, `df_container.json`, `df_volume.json`, `df_build.json`, `df_full.json`.

**Interfaces:**
- Produces:
  ```go
  type DFKind string
  const ( DFImage DFKind = "image"; DFContainer = "container"; DFVolume = "volume"; DFBuildCache = "build-cache" )
  type DFImage struct{ ID string; Size, SharedSize int64; Containers int64 }
  type DFContainer struct{ ID, Name string; SizeRw int64 }
  type DFVolume struct{ Name string; Size, RefCount int64 }   // -1 when Docker did not compute
  type DFBuildRecord struct{ ID, Type, Description string; Size int64; InUse, Shared bool; LastUsedAt *time.Time; UsageCount int }
  type DFSnapshot struct { Images []DFImage; Containers []DFContainer; Volumes []DFVolume; BuildCache []DFBuildRecord }
  const buildPruneByIDMinAPI = "99.0" // closed until TestE2EBuildPruneByIDHonoured passes on a real engine
  func (c *Client) DiskUsage(ctx context.Context, kind DFKind) (DFSnapshot, error)
  func (c *Client) BuildPruneByID(ctx context.Context) (bool, error)
  func (c *Client) PruneBuildCache(ctx context.Context, ids []string) (reclaimed int64, err error) // ids nil → all unused
  ```

**Behaviour:**
- `DiskUsage`: on API ≥ 1.45 it sends `GET /system/df?type=<kind>` and fills only that kind. On older APIs, or on a 400 from the typed call, it sends one full `/system/df` and fills **all** kinds (the caller may cache all four). It reuses `dfBody`'s version logic.
- `VolumesDiskUsage` is reimplemented on `DiskUsage(DFVolume)` with the same `VolumeUsage` result (existing tests unchanged).
- `BuildPruneByID` compares only the negotiated version with `buildPruneByIDMinAPI`. It never sends a prune.
- `PruneBuildCache`: `POST /build/prune?all=true`, plus `&filters={"id":[…]}` (URL-encoded JSON) when `ids` is non-nil. An empty non-nil `ids` returns 0 without a request. It decodes `SpaceReclaimed` and has a 10-minute timeout, bounded by the caller.
- Integration (existing integration build tag): `TestE2EBuildPruneByIDHonoured` builds two tiny images from different `FROM scratch` contexts with `docker build --no-cache` and skips when no builder is available. It reads the two record ids from `DiskUsage(DFBuildCache)`, prunes one by id, and asserts the other is still there. It logs the engine API version. Lowering the constant is a separate commit, made only when this test passes (Decisions).

- [ ] **Step 1: Write the failing tests:** `TestDiskUsageTypedPerKind` (the fake server sees `type=volume` and the snapshot has only volumes), `TestDiskUsageOldAPIUsesFullDF` (API 1.43: one request, all four kinds filled), `TestDiskUsage400FallsBack`, `TestVolumesDiskUsageUnchanged` (existing expectations), `TestPruneBuildCacheSendsIDFilter` (decoded `filters` equals `{"id":["a","b"]}`, `all=true`, `SpaceReclaimed`), `TestPruneBuildCacheAllUnusedSendsNoFilter`, `TestPruneBuildCacheEmptyIDsNoRequest`, `TestBuildPruneByIDVersionGate` (with a test-only override of the constant: below, at and above; the request counter stays 0).
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.** Expose the constant override only through `export_test.go`.
- [ ] **Step 4: Run** `go test -race ./internal/engine ./internal/host`, `go vet ./...`. Run the integration test where Docker is available and record the outcome in the handoff.
- [ ] **Step 5: Commit** `feat: engine per-type disk usage and build cache prune`.

---

### Task 4: `ConfigJSON` and stack declarations

Spec §5.3. Review Focus 5.

**Files:**
- Modify: `internal/composecli/runner.go` (+ `runner_test.go`)
- Create: `internal/storage/declarations.go`, `declarations_test.go`, `testdata/config_*.json`

**Interfaces:**
- Produces:
  ```go
  // composecli
  func (r *Runner) ConfigJSON(ctx context.Context, file string) ([]byte, error) // compose -f file config --format json
  // storage
  type ImageRef  struct{ Service, Ref string; Built bool }   // Built: build: present and no image:
  type VolumeRef struct{ Key, Name string; External bool }
  type StackDeclarations struct { Stack, Project, State string; Known bool; Images []ImageRef; Volumes []VolumeRef }
  type ConfigRunner interface{ ConfigJSON(ctx context.Context, file string) ([]byte, error) }
  type Declarations interface{ For(ctx context.Context, e stacks.Entry, state string) (StackDeclarations, error) }
  func NewDeclarations(r ConfigRunner, hash func(path string) (string, error)) Declarations
  ```

**Behaviour:**
- `ConfigJSON` uses the existing `capture` with the stack's directory as its working directory. Stdout is returned and never logged. On failure, the error carries only the exit code and compose's **stderr** tail (stdout is never in an error).
- Decoding uses a struct with only `name`, `services.*.image`, `services.*.build` (presence via `json.RawMessage`) and `volumes.*.{name,external}`. A built service without `image:` declares `<project>-<service>`. A volume's name is `name:` when set; otherwise compose already reports the resolved name in `config` output, and when it doesn't, the name is `<project>_<key>`.
- The cache key is SHA-256 of the compose file plus `.env` in the same directory (missing → the empty string). There is one entry per stack name, single-flight per stack, a global concurrency of 2 (a semaphore) and a 10 s timeout per run.
- Errors from a failed parse, a missing file or a missing compose binary give `Known=false` with empty slices. `For` returns the result and **no error** in that case, so the caller always gets a row. The error is kept only for a debug log line that names the stack and the exit code.

- [ ] **Step 1: Write the failing tests:** `TestConfigJSONArgsAndDir` (fake binary records argv and cwd), `TestConfigJSONErrorHasNoStdout`, `TestDeclarationsDecode` (image refs, built default name, named, external and default volumes), `TestDeclarationsCachedByHash` (a second call runs no subprocess; changing `.env` does), `TestDeclarationsFailureMarksUnknown`, `TestDeclarationsIgnoreEnvironment` (the fixture has `environment.DB_PASSWORD=hunter2`; marshal the result and the captured `slog` output, and `hunter2` appears in neither), `TestDeclarationsConcurrencyTwo`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/composecli ./internal/storage`, `go vet ./...`.
- [ ] **Step 5: Commit** `feat: read stack image and volume declarations from compose config`.

---

### Task 5: `storage.Classify` — the pure join

Spec §5.1, §5.2, §5.4. Review Focus 1 and 3.

**Files:** Create `internal/storage/types.go`, `classify.go`, `classify_test.go`, `rollback.go`.

**Interfaces:**
- Produces the spec §5.1 types (`Consumer`, `Declaration`, `Protection`, `Image`, `Volume`, `BuildRecord`, `KindTotals`), plus:
  ```go
  type DFState struct { Snapshot engine.DFSnapshot; Have map[engine.DFKind]bool; At map[engine.DFKind]time.Time }
  type Inputs struct {
      Images []engine.Image; Volumes []engine.Volume; Containers []engine.Container
      DF DFState; Stacks []StackDeclarations; Rollback []RollbackImage
      ProjectToStack map[string]string   // registered projects
  }
  type Inventory struct {
      Images []Image; Volumes []Volume; BuildCache []BuildRecord
      Totals map[engine.DFKind]KindTotals; TopContainers []TopContainer; DeclarationsUnknown []string
  }
  type TopContainer struct{ Name, Stack string; SizeRw int64 }
  type RollbackImages interface{ RetainedImages(ctx context.Context) ([]RollbackImage, error) }
  type RollbackImage struct{ ID, Stack, Service string }
  var NoRollback RollbackImages // returns nil, nil
  func Classify(in Inputs) Inventory
  ```

**Behaviour:** exactly spec §5.1 and §5.2, plus:
- Image size: `Size` comes from df when available, otherwise from the list's `Size`. `SharedSize` comes from df only (`nil` before df).
- Volume `SizesPending` is true until the volume df has loaded. Before then, `Status` is computed from container mounts only.
- `Declaration.StackState` is the stack's state as passed in `StackDeclarations.State`.
- Image matching: a declared ref is normalized (`nginx` → `docker.io/library/nginx:latest`, using the same normalization in both directions) and matched against `RepoTags`.
- Sort order: images by size descending (unknown sizes last), then the first tag. Volumes the same way, then name. Build records by size.
- `Consumer.Stack` is filled from `ProjectToStack`.

- [ ] **Step 1: Write the failing tests:** the spec §13 `internal/storage` classify list (`TestClassifyImageStatus`, `TestClassifyImageMatchesContainersByID`, `TestClassifyBuiltServiceDefaultName`, `TestClassifyRollbackImageProtected`, `TestClassifyVolumeConsumersIncludeStopped`, `TestClassifyVolumeDeclaredByStoppedStackIsUnusedProtected`, `TestClassifyVolumeProtectedByProjectLabelWhenDeclarationsUnknown`, `TestClassifyExternalVolumeDeclaration`, `TestReclaimableTotals`), plus `TestClassifyBeforeDFIsProvisional` (null sizes, `SizesPending`, unused by mounts only) and `TestClassifyRefNormalization`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/storage`, `go vet ./...`.
- [ ] **Step 5: Commit** `feat: classify images and volumes with stack and rollback protection`.

---

### Task 6: `action_log` table and `internal/actionlog`

Spec §5.5.

**Files:**
- Modify: `internal/store/store.go` (+ `store_test.go`), `internal/metrics/sampler.go` (+ test)
- Create: `internal/actionlog/actionlog.go`, `actionlog_test.go`

**Interfaces:**
- Produces:
  ```go
  type Entry struct { ID int64; At time.Time; Username, Kind, Scope, Target, Detail string; Bytes *int64; Result, OperationID string }
  type Filter struct { KindPrefix, Scope string; Limit int }   // Limit clamped to 1..500, 0 → 50
  type Log struct{ /* db, now */ }
  func New(db *sql.DB, now func() time.Time) *Log
  func (l *Log) Append(ctx context.Context, e Entry) error
  func (l *Log) Recent(ctx context.Context, f Filter) ([]Entry, error)   // newest first
  func (l *Log) PruneOlderThan(ctx context.Context, t time.Time) (int64, error)
  var ErrInvalidEntry = errors.New("actionlog: invalid entry")
  // metrics.Config / Sampler gains: Pruners []func(ctx context.Context, now time.Time)
  ```

**Behaviour:**
- The migration is the spec's SQL, appended as one new `migrations` entry. `SupportedSchema()` grows by one. `db restore` and `OpenExisting` keep working (existing tests).
- `Append` validates: `kind` matches `^[a-z]+\.[a-z]+$`; `scope` is `host` or passes `stacks.ValidName`; `result` is non-empty; `username` is non-empty. A bad entry wraps `ErrInvalidEntry`. `At` defaults to `now()`.
- The `KindPrefix` filter uses `kind >= ? AND kind < ?` (a prefix range, so `%` and `_` in the input are never special). The `Scope` filter is exact.
- The sampler calls each pruner after its own prune, recovers panics and logs failures at Warn.

- [ ] **Step 1: Write the failing tests:** `TestMigrationActionLog` (the table and both indexes exist; the schema version went up by one), `TestAppendValidates` (table: bad kind `Storage.delete`, `storage`, `storage.delete.x`; bad scope `../x`; empty result; empty username), `TestRecentFiltersAndOrder` (prefix `storage.` excludes `mount.fix`; scope exact; limit clamp), `TestRecentPrefixIsLiteral` (`stor%` matches nothing), `TestPruneOlderThan`, and `TestSamplerRunsPruners` in metrics.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/store ./internal/actionlog ./internal/metrics ./internal/cli` (the db commands), `go vet ./...`.
- [ ] **Step 5: Commit** `feat: shared action_log table for persistent action records`.

---

### Task 7: `storage.Service` — caches, inventory, refresh, dashboard volume source

Spec §4, §3 "One volume-size source". Review Focus 6.

**Files:**
- Create: `internal/storage/service.go`, `service_test.go`
- Modify: `internal/host/collector.go` (+ test), `internal/cli/serve.go`

**Interfaces:**
- Consumes: `EngineAPI` (the T2/T3 methods plus `Containers`, which `*engine.Client` satisfies), `StacksSource interface{ List(ctx) ([]stacks.Summary, error); Entries(ctx) ([]stacks.Entry, error) }` (adapt to what `stacks.Service` exposes; note the adapter in the handoff), `Declarations`, `RollbackImages`.
- Produces:
  ```go
  type Config struct { Engine EngineAPI; Stacks StacksSource; Declarations Declarations; Rollback RollbackImages; Now func() time.Time; Log *slog.Logger }
  func NewService(cfg Config) *Service
  func (s *Service) Inventory(ctx context.Context) (Inventory, error)              // cheap lists fresh (2 s), df stale
  func (s *Service) Refresh(now time.Time) (startedAt time.Time, retryAfter time.Duration, ok bool) // ≤ 1 per 60 s
  func (s *Service) Invalidate(kind engine.DFKind)                                // after a cleanup
  func (s *Service) FreshDF(ctx context.Context, kind engine.DFKind) (engine.DFSnapshot, error) // for plans
  func (s *Service) VolumeUsage() (engine.VolumeUsage, bool)                      // host.Config.Volumes
  ```

**Behaviour:**
- Lists (images, volumes, containers, stack entries and their declarations) are composed behind one 2 s single-flight cache on a detached context with a 10 s bound. Errors are not cached.
- There are four `stale.Value[engine.DFSnapshot]` values, `maxAge` 5 minutes and load timeout 60 s. When a load falls back to the full df, the result is stored in all four values.
- `Inventory` asks each value with `GetStale` (which starts a background refresh when one is due) and never blocks on df. `KindTotals.SizesAt`, `Refreshing` and `Error` come from `At`/`Refreshing`/`Err`.
- `Refresh` invalidates all four values. A second manual refresh within 60 s returns `ok=false` and the remaining wait.
- `FreshDF` runs a synchronous load through the value (so a concurrent background load is shared) with a 60 s bound and stores the result.
- `host.Collector`: when `cfg.Volumes` is set, it uses it and no longer calls `VolumesDiskUsage`. Existing host tests still pass with `Volumes` nil.
- `serve.go` builds the service with `storage.NoRollback` and passes it to `host.Config.Volumes`.

- [ ] **Step 1: Write the failing tests:** `TestInventoryNeverBlocksOnDF` (a fake df blocks; `Inventory` returns at once with null sizes and `Refreshing` true), `TestInventoryListsCachedFor2s` (50 goroutines → one `Images` call), `TestFullDFFallbackFillsAllKinds`, `TestRefreshRateLimited`, `TestFreshDFSharesInflightLoad`, `TestVolumeUsageFromCache`, and `TestCollectorUsesVolumesProvider` in host.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement** (with the serve wiring).
- [ ] **Step 4: Run** `go test -race ./internal/storage ./internal/host ./internal/cli`, `go vet ./...`.
- [ ] **Step 5: Commit** `feat: storage inventory service with background disk usage`.

---

### Task 8: Plans and execution

Spec §6. Review Focus 1–4.

**Files:**
- Create: `internal/storage/plans.go`, `execute.go`, `plans_test.go`, `execute_test.go`
- Modify: `internal/stacks/ops_test.go`

**Interfaces:**
- Produces:
  ```go
  type PlanRequest struct { Kind, Selection, Ref, Name string }
  type PlanItem struct { Kind, ID, Ref string; Bytes *int64; fingerprint string }
  type Protected struct { Kind, ID, Ref, Reason, Stack, StackState string }
  type Reclaim struct { Min, Max int64; Exact bool }
  type Plan struct {
      ID, Kind, Mode string; CreatedAt, ExpiresAt time.Time   // Mode: "" | "all-unused"
      Items []PlanItem; Protected []Protected; Warnings []string; Reclaim Reclaim; ConfirmPhrase string
  }
  var ( ErrPlanNotFound, ErrPlanEmpty, ErrWrongPhrase, ErrRollbackUnavailable, ErrSizesUnavailable error )
  type RefusedError struct{ Status int; Message string } // 404 unknown item, 409 protected/used
  type OpStarter interface{ Start(stack, action string, fn stacks.OpFunc) (stacks.Operation, error) }
  func (s *Service) CreatePlan(ctx context.Context, req PlanRequest) (Plan, error)
  func (s *Service) Plan(id string) (Plan, error)
  func (s *Service) Confirm(ctx context.Context, id, phrase, username string, ops OpStarter, log ActionAppender) (stacks.Operation, error)
  type ActionAppender interface{ Append(ctx context.Context, e actionlog.Entry) error }
  ```

**Behaviour:**
- Request validation: `kind` is one of `images|volumes|build-cache|image|volume`. Bulk kinds need `selection` (`dangling` for images, `unused` for the others). `image` needs `ref` (a reference, or a 12–64 hex id). `volume` needs `name` matching `[a-zA-Z0-9][a-zA-Z0-9_.-]*`. Anything else is a 400-class validation error.
- `CreatePlan` builds a fresh inventory from fresh lists, `FreshDF(kind)` and a fresh rollback list. A df error gives `ErrSizesUnavailable`; a rollback error on image kinds gives `ErrRollbackUnavailable`.
  - Bulk images: dangling and unprotected only. Protected dangling images go in `Protected`.
  - Bulk volumes: unused and unprotected. Unused protected volumes go in `Protected` with the stack and its state.
  - Single image: refused when protected, or when in use (naming up to 3 containers). If a stopped stack declares it, the warning "declared by stack X; the next Up pulls or builds it again" is added. Deleting a tag of a multi-tag image reclaims 0.
  - Single volume: refused when protected, or when in use (naming up to 3 containers).
  - Build cache: records not in use and not shared. Gate open → `Exact` with ids. Gate closed → `Mode "all-unused"`, `Exact:false`, the spec warning, and items listed as expected.
- Fingerprints are spec §6.2. `ConfirmPhrase` is spec §6.2: singular for 1; build cache is always a count (`delete 1 build cache record`). The short id is 12 hex characters.
- The plan store holds at most 32 plans (oldest evicted) with a 5-minute TTL on the injected clock.
- `Confirm`: unknown or expired → `ErrPlanNotFound`; empty → `ErrPlanEmpty`; phrase not byte-equal → `ErrWrongPhrase`. Otherwise `ops.Start("", "storage-cleanup", run)`, where `ErrBusy` passes through. The plan is removed from the store once started, so it can't be confirmed twice.
- `run`, per item in order, checking `ctx` before each one (cancel stops before the next): the re-read and skip rules of spec §6.3 (for volumes a fresh `FreshDF(DFVolume)` once per run, not per item), then the delete by explicit ref, then one output line and one `actionlog` row (`storage.delete` for single plans, `storage.prune` for bulk plans). A failed log write emits "could not record action" and continues. Build cache, exact path: one `PruneBuildCache(ids)` with the re-checked ids and one row per id; bytes are the plan estimate except Docker's total on the last row (documented in `docs/api.md`). Fallback path: `PruneBuildCache(nil)`, one row `build-cache/all-unused` with `SpaceReclaimed`, and output lines for records missing from a fresh df.
- After the run, the list cache and the kind's df are invalidated. The run returns an error (operation `failed`) when any item failed.

- [ ] **Step 1: Write the failing tests:** the spec §13 plan and execution lists (`TestPlanDanglingImagesExcludesProtectedAndTagged`, `TestPlanUnusedVolumesListsProtectedSeparately`, `TestPlanSingleRefusals`, `TestPlanConfirmPhrase`, `TestPlanExpires`, `TestPlanEviction`, `TestPlanReclaimRange`, `TestPlanBuildCacheFallbackIsLabelledNotExact`, `TestConfirmBuildCacheFallbackLogsOneRow`, `TestConfirmWrongPhrase`, `TestConfirmSkipsItemNowUsed`, `TestConfirmSkipsVolumeNewlyDeclared`, `TestConfirmSkipsChangedFingerprint`, `TestConfirmDeletesByExplicitIDOnly`, `TestConfirmUntagsOneTag`, `TestConfirmContinuesAfterFailure`, `TestConfirmWritesActionRows`, `TestConfirmLogFailureDoesNotStopDeletes`, `TestConfirmCancelStopsBeforeNextItem`), plus `TestPlanImagesRollbackError503`, `TestPlanStoppedStackImageWarning`, `TestPlanRequestValidation`, `TestConfirmTwiceIsNotFound`, `TestConfirmAlreadyGoneIsSkipped`, and `TestOpsHostScopeKey` in stacks (empty key; `ErrBusy` on a second start; listed by `All()`; does not block a stack's operations).
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/storage ./internal/stacks`, `go vet ./...`.
- [ ] **Step 5: Commit** `feat: exact storage cleanup plans with typed confirmation`.

---

### Task 9: API routes, DTOs and Go client

Spec §7, §11, §12. Review Focus 1, 5 and 6.

**Files:**
- Create: `internal/api/storage.go`, `storage_test.go`, `actions.go`, `actions_test.go`
- Modify: `internal/api/server.go` (`Options.Storage StorageAPI`, `Options.Actions ActionsAPI`), `internal/apitypes/types.go` (+ `types_test.go`), `internal/client/client.go` (+ test), `internal/cli/serve.go`, `internal/api/client_contract_test.go`

**Interfaces:**
- `apitypes`: `StorageOverview`, `StorageKindTotals`, `StorageImage`, `StorageImageDetail` (+ `architecture`, `os`, `layers`, `labels`), `StorageVolume`, `StorageVolumeDetail` (+ `mountpoint`, `labels`), `StorageBuildCache`, `StorageList[T]` (`{items, sizesAt, refreshing, error}`), `StoragePlanRequest`, `StoragePlan` (the spec §6.2 JSON exactly, `mode` omitted when empty), `StorageConfirm`, `ActionEntry`. Field names are camelCase as in spec §6.2; nullable sizes are pointers; slices are never null.
- The `Operation` DTO is unchanged; `stack` is `""` for host operations.
- `client`: `Storage(ctx)`, `CreateStoragePlan(ctx, req)`, `ConfirmStoragePlan(ctx, id, phrase)`, `RefreshStorage(ctx)`, `Actions(ctx, kindPrefix, scope string, limit int)`.

**Behaviour:**
- Routes and statuses are exactly the spec §7 table and §6. Plan errors map as: validation → 400 `{error, fields}`; `RefusedError` → its status; `ErrSizesUnavailable` and `ErrRollbackUnavailable` → 503; `ErrPlanNotFound` → 404; `ErrPlanEmpty` and `ErrBusy` → 409; `ErrWrongPhrase` → 400 `{error, fields:[{field:"confirm"}]}`; Docker unavailable → 503. Refresh gives 202 `{startedAt}`, or 429 `{error, retryAfter}` (seconds, also in the `Retry-After` header).
- `{id}` for an image is 12–64 hex or an unambiguous prefix (≥ 12; ambiguous → 409, unknown → 404). `{name}` follows the volume pattern (400 otherwise).
- The image detail is built from `ImageInspect` without `User`. The volume detail never includes options. The confirm handler passes the session's username.
- `GET /api/actions` needs only the store, works without Docker, and parses `kind`, `scope` and `limit` (1–500, else 400).
- `serve.go` wires `storage.Service`, `actionlog.Log` and the existing `Ops`.

- [ ] **Step 1: Write the failing tests:** the spec §13 `internal/api` list (session on every route; overview and list mapping with null sizes, `sizesAt` and `refreshing`; one computation for 50 callers; refresh 202 then 429; plan 201/400/404/409/503; confirm 202/400/404/409; `TestImageDetailBodyHasNoEnvOrUser` (decode into a `map[string]any` and walk for `env`, `cmd`, `entrypoint`, `user` keys and the fixture's secret values); `TestVolumeDetailBodyHasNoOptions`; actions limit bounds; 503 while Docker is down; `GET /api/actions` without Docker, filtered by kind prefix and scope), the apitypes key contract rows, and the client contract test.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/api ./internal/apitypes ./internal/client ./internal/cli`, `go test ./...`, `go vet ./...`.
- [ ] **Step 5: Commit** `feat: storage and action log API routes`.

---

### Task 10: CLI — `storage df`, `storage prune`, host operations in `ops`

Spec §10.

**Files:** Create `internal/cli/cmd_storage.go`, `cmd_storage_test.go`. Modify `internal/cli/prompt.go` (+ test), `commands.go`, `cmd_ops.go` (+ test).

**Interfaces:**
- Produces:
  ```go
  // confirmTyped prints prompt on stderr and reads one line; it returns whether the line,
  // minus its trailing newline, equals phrase. yes returns true without reading.
  // Without a terminal and without yes: ExitUsage "refusing to continue without confirmation: use --yes".
  func confirmTyped(env *Env, prompt, phrase string, yes bool) (bool, error)
  ```

**Behaviour:**
- `storage df [-o table|json]`: columns KIND, COUNT, SIZE, RECLAIMABLE, AS OF (relative age, `—` when unknown; `refreshing` appended when true). JSON prints the overview DTO.
- `storage prune <images|volumes|build-cache> [--preview] [--yes] [--detach] [-o table|json]`: creates the bulk plan; prints the items (ref, size), the protected items with reasons, the warnings, and the reclaim line (range or exact, with "not exact" for fallback build-cache plans). An empty plan prints "nothing to clean" and exits 0 without confirming. `--preview` stops there with exit 0. Otherwise it runs `confirmTyped(... "Type \"<phrase>\" to confirm:" ...)`; a mismatch exits 6 with "confirmation did not match" and sends nothing. `--yes` sends the plan's phrase. Then `startAndFollow` (existing; exit 9 on failure; `--detach` prints the id).
- `ops` and `ops show` print `host` for an empty stack.
- Errors map through the existing `clientErrorCode` (409 → 5, 429 → 8, 503 → 7).

- [ ] **Step 1: Write the failing tests:** the spec §13 `internal/cli` list (`TestStoragePrunePreviewPrintsPlan`, `TestStoragePruneNeedsYesWithoutTTY`, `TestStoragePruneTypedPromptMismatch`, `TestStoragePruneYesSendsPhrase`, `TestStorageDFJSON`), plus `TestStoragePruneEmptyPlan`, `TestStoragePruneFallbackShowsNotExact`, `TestStoragePruneRejectsUnknownKind` (exit 2), `TestConfirmTyped` (table), and `TestOpsShowsHostForEmptyStack`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/cli`, `go vet ./...`.
- [ ] **Step 5: Commit** `feat: stackstead storage df and storage prune`.

---

### Task 11: Web — data layer, shell, Storage page and links

Spec §9.1, §9.2, §9.5.

**Files:**
- Modify: `web/src/lib/api/types.ts`, `client.ts` (+ tests), `web/src/lib/shell/Rail.svelte`, `breadcrumb.ts`, `icons.ts` (+ `shell.test.ts`), `web/src/App.svelte` (+ `App.test.ts`), `web/src/lib/stacks/OperationsList.svelte` (+ test), `web/src/lib/dashboard/tiles.ts` (+ test), `web/src/routes/ContainerDetail.svelte` (+ test)
- Create: `web/src/lib/storage/storage.ts`, `storage.test.ts`, `web/src/routes/Storage.svelte`, `Storage.test.ts`

**Interfaces:**
- Produces:
  ```ts
  export type ImageChip = 'all' | 'in-use' | 'no-containers' | 'dangling' | 'protected';
  export type VolumeChip = 'all' | 'in-use' | 'unused' | 'protected';
  export function filterImages(rows: StorageImage[], chip: ImageChip, q: string): StorageImage[] // q over tags, id, stack
  export function filterVolumes(rows: StorageVolume[], chip: VolumeChip, q: string): StorageVolume[]
  export function imageChipCounts(rows): Record<ImageChip, number>; export function volumeChipCounts(rows): Record<VolumeChip, number>
  export function reclaimText(r: Reclaim): string        // "Frees 1.0 GB" | "Frees between 120 MB and 480 MB" | "Frees about 2.0 GB (not exact)"
  export function deleteDisabledReason(row: StorageImage | StorageVolume): string | undefined
  export function createStorageView(deps): { overview, images, volumes, buildCache, error, refresh(), start(), stop() }
  ```

**Behaviour:**
- Rail: **Storage** after Networks, with a `disk` icon in the existing icon family; current on `/storage` and `/storage/*`. Breadcrumbs and titles follow spec §9.1. Only the `/storage` route is registered in this task; T12 adds the detail routes.
- `Storage.svelte`: `Tabs` with `?tab=overview|images|volumes|build-cache`. Each tab's content follows spec §9.2. The header cleanup buttons and row Delete buttons call an `onCleanup(request)` that opens the T12 dialog. Until T12 they are rendered but `disabled` with the title "Coming in the next step" — **not allowed in a merged state**: T12 must land in the same branch before review (state this in the handoff).
- Refresh follows spec §9.2 (the debounced change helper the Containers list uses; no timers while hidden; a 10 s refetch only while `refreshing` is true and the page is visible; a stale-response generation guard).
- Recalculate is disabled for 60 s after use with the reason in its `title`. A 429 shows the server's message.
- Links: the dashboard Volumes tile goes to `/storage?tab=volumes` (`href` on the tile model). On the container page, volume mounts link to `/storage/volumes/<name>` and the header image links to `/storage/images/<short id>`. `OperationsList` renders an empty stack as `host` with no link.

- [ ] **Step 1: Write the failing tests:** `storage.test.ts` (filters, counts, `reclaimText` table, disabled reasons for in-use, rollback, protected and Docker down, controller timers and visibility), `Storage.test.ts` (tabs and `?tab=`; chips and counts; protected volume row shows the stack note and no Delete; rollback image row disabled with the reason; "calculating…" while `sizesPending`; declarations-unknown banner; Recalculate disabled for 60 s; recent actions list), the shell rail and breadcrumbs tests, the App route and title test, the tile link, the container page links, and OperationsList `host`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `cd web && npm run check && npm test && npm run build` (budgets).
- [ ] **Step 5: Commit** `feat: Storage screen with overview, images, volumes and build cache`.

---

### Task 12: Web — cleanup dialog and detail pages

Spec §9.3, §9.4, §9.5.

**Files:** Create `web/src/lib/storage/CleanupDialog.svelte` (+ test), `web/src/routes/StorageImage.svelte` (+ test), `web/src/routes/StorageVolume.svelte` (+ test). Modify `web/src/App.svelte`, `web/src/routes/Storage.svelte` (enable the buttons).

**Interfaces:**
- `CleanupDialog` props: `{ request: StoragePlanRequest; open: boolean; onclose: () => void; ondone: (op: Operation) => void }`.

**Behaviour:**
- The dialog follows spec §9.4: it creates the plan on open ("Checking current state…"). It shows the 409/503 server message as the dialog body with Close; an empty plan shows "Nothing to clean" with Close. It renders items, reclaim, protected (not selectable), warnings, a countdown to `expiresAt`, and "not exact" beside the phrase for fallback plans.
  - The confirm field has a `<label>` "Type `<phrase>` to confirm" and `aria-describedby`. The danger **Delete** button stays disabled until there is an exact match. Volume plans add "Volumes hold data. This cannot be undone."
  - Confirming shows `OperationPanel` for the returned operation. When the operation ends: a toast, `ondone`, and the lists refresh. A 404 on confirm offers **Re-check**, which creates a new plan. A 400 phrase error shows the field error.
- Detail pages follow spec §9.3. The image `:id` is a short id; a 404 shows `EmptyState` "Image not found" with a link back. Delete opens the dialog with a single-item request; it is disabled with `deleteDisabledReason`. After a successful delete, the page navigates to `/storage?tab=images` (or `volumes`).

- [ ] **Step 1: Write the failing tests:** the spec §13 Vitest dialog and detail items (protected list not selectable, phrase gating, reclaim text exact vs range vs not exact, expired → Re-check, operation output then refresh, detail pages render without env or options keys even when the fixture carries extra keys), plus a 409 refusal shown and not crashing, an empty plan, keyboard focus moving to the field on open, and Escape closing before confirm.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `cd web && npm run check && npm test && npm run build`.
- [ ] **Step 5: Commit** `feat: storage cleanup dialog and image and volume pages`.

---

### Task 13: E2E, docs, final checks, visual pass

Spec §13 (E2E, visual, acceptance), §14.

**Files:** Modify `cmd/stackstead/e2e_test.go`, `docs/api.md`, `docs/cli.md`, `docs/security.md`, `README.md`, `docs/README.md` (hub rows for the new sections).

**Behaviour:**
- E2E (`TestE2ESmoke`, with Docker): exactly the spec §13 E2E paragraph, plus `GET /api/storage` answering before the first df finishes (null sizes allowed), the stack volume's bulk plan listing it under `protected`, `stackstead storage prune volumes --preview` exiting 0, and `GET /api/actions?kind=storage.` returning the row. `t.Cleanup` removes the volume, the tag and any leftovers, including after a failure.
- Docs: spec §14. `docs/api.md` also documents the per-id bytes rule for exact build-cache prunes (T8) and that `"stack": ""` means a host operation. `docs/cli.md` gets the two commands with their exit codes. `docs/security.md` gets the §11 paragraph. README gets one feature-list line.
- Final checks: `make check`; `make ui` (budgets); `env PATH=/usr/bin:/bin:$(go env GOROOT)/bin go test ./...`; `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`; `GOOS=darwin go build ./...`; the e2e; the integration-tagged engine tests where Docker is available (report `TestE2EBuildPruneByIDHonoured`'s outcome and, if it passed, the engine version for the follow-up constant commit).
- Visual pass (headless Chrome, scratchpad config and data, a throwaway compose stack plus one stray volume and one dangling image): `/storage` (each tab), both detail pages and the cleanup dialog (volume plan with a protected item; fallback build-cache plan) at 1280 and 375 px, dark and light. Report issues, don't fix them here. Clean up containers, volumes and images and stop processes.

- [ ] **Step 1: Write the e2e assertions** and run them. If they pass the first time, prove they can fail with a temporary wrong expectation, then restore it.
- [ ] **Step 2: Write the docs.**
- [ ] **Step 3: Final checks and visual pass.**
- [ ] **Step 4: Commit** `test: storage inventory e2e` and `docs: storage screen, storage CLI and action log`.
- [ ] **Step 5: Hand off** for the Fable final review. Any visual findings are fixed before that review (the user's "fix everything first" rule). Then merge to `main`.

## Out of scope

Integration with sibling specs (Independence): real rollback images, `mount.fix` rows, the compose-forms `Invocation` migration, and the volume-diagnostics link target (`?tab=storage` on stack detail; until it exists, "Declared by" links go to the stack page).

Spec §2 Out, unchanged: removing containers, `prune -a` for tagged images, forced removal, pull/tag/push, volume create/backup/browse, mount checks (volume diagnostics), choosing rollback images (image updates), logging non-storage mutations, and single-item deletes from the CLI. Lowering `buildPruneByIDMinAPI` is a follow-up commit gated on the real-engine test (Decisions), not part of any task's acceptance.
