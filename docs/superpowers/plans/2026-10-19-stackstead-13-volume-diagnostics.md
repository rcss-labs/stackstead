# Stackstead Plan 13 — Volume and mount diagnostics (V1)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** tasks, names, signatures, test cases and examples only; no code bodies. Implementers write everything test-first (failing test, confirm the reason, implement, pass, commit).

**Goal:** A **Storage** tab on every stack and mount badges on every container page that show what is mounted where, who owns it, whether the container user can use it, and a finding (with severity, evidence and a copy-paste remedy) for the common mount mistakes; a periodic unprivileged scan that turns danger findings of every container on the host into `mount_problem` alerts; and an on-demand root **Deep check** through a pinned busybox helper.

**Architecture:** `composecli.Runner.Model` decodes the volume subset of `docker compose config --format json`; `engine` grows read-only inspect fields, `ContainerTop`, daemon security options and a helper-container lifecycle; the new `internal/mounts` package collects declared + runtime mounts and host metadata, runs a pure `Analyze` into a `Report`, caches reports per stack, scans in the background and feeds `alerts.Engine`; `internal/api/mounts.go` exposes four read routes; the web adds `StoragePanel`, container badges, an operation hint and alert links. Fixes are Plan 14.

**Tech Stack:** Go 1.26 standard library, `golang.org/x/sys/unix` (promoted from indirect to direct), existing SQLite driver; Svelte 5, TypeScript, Vitest. No other new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-18-stackstead-volume-diagnostics-design.md` (§ references below). Branch from `main` at `f7271d9`. Plan 14 (`2026-10-19-stackstead-14-volume-fixes.md`) depends on this plan.

## Spec divergences (the spec was written before the storage inventory shipped)

1. **`Invocation` does not exist.** `composecli.Runner` methods take a compose file path (`ConfigJSON(ctx, file)`); a stack has exactly one compose file (`stacks/service.go:196` refuses overrides). `Runner.Model(ctx, file string)` follows that shape. The "compose file set" is the file plus its `.env`.
2. **`engine.InspectVolume` and `InspectImage` already exist** (`engine/client.go:613`, `:643`; `VolumeInspect = Volume{Name, Driver, Scope, Mountpoint, CreatedAt, Labels, OptionType}`, `ImageInspect.User`, `ErrVolumeNotFound`, `ErrImageNotFound`). Nothing to add.
3. **The `action_log` table and `actionlog.Store` exist** (`store/store.go:65`, `actionlog/store.go`: `Append`, `Recent(Filter{KindPrefix, Scope, Limit})`). Every "create the table if that plan has not landed" branch is dropped (used by Plan 14 only).
4. **The compose-hash cache key** is `storage.declarationHash` (`storage/declarations.go:218`, unexported, hashes path + content + `.env`). Task 1 moves it to `composecli.FileSetHash` and makes `storage` call it (behaviour-preserving, covered by the existing storage tests).
5. **Top-level volume `driver_opts`** are not decoded whole (they can carry NFS/CIFS credentials; `engine.Volume` deliberately keeps only `OptionType`). `Model` keeps `driver_opts.type` only, as `VolumeModel.DriverType`.
6. **`golangci-lint` is not configured** in this repository (no `.golangci.yml`; `make check` runs `go vet`, `gofmt -l`, `make test`, `ui-check`, `ui-test`). Verification uses those.
7. **Alerts source wiring:** `alerts.NewEngine` takes positional sources and has many callers in tests; the new optional source is attached with `(*Engine).SetMountProblems(src)` instead of a seventh constructor argument.
8. **Scanner entry point** is `Run(ctx)` (not `Start`) so `cli/serve.go:170 startBackground` can run it like `cache.Run` and `sampler.Run`.
9. **"After a stack operation finishes"** has no hook today; Task 12 adds `(*stacks.Ops).OnEnd(fn func(Operation))` and `(*stacks.Ops).Running(stack string) bool`.
10. **Deep-check 502 and parse-error 409:** a stack whose compose does not parse is reported by `stacks.Service.Get` as `Detail.ParseError`; the collector returns an error wrapping `stacks.ErrStackUnavailable`, which `api/stacks_errors.go:51` already maps to 409 with the detail text.

## Global Constraints

- Test-first for every behaviour change: write the failing test, run it and confirm it fails for the expected reason, implement, run again, commit. No new tests for pure refactors already covered (Task 1's hash move) or docs.
- Conventional commits (`feat:`, `fix:`, `refactor:`, `docs:`, `chore:`) whose body says why. Each task ends with a commit.
- Verification commands: `go test -race ./internal/<pkg>/` (expected `ok`), `go vet ./...` and `test -z "$(gofmt -l .)"` (no output), `make ui-test` and `make ui-check` (0 errors). Integration tests: `go test -tags integration -race ./internal/mounts/`. E2E: `make e2e`.
- Never return, log or cache file contents, `environment` values, or the raw container `User` string (§11). Clients never supply a host path. Reads are metadata only (`Lstat`, `Statfs`, xattr, one-entry `ReadDir`); never `Open` a regular file.
- The scanner never calls `RunHelper` or `PullImage`. The helper runs only from Deep check (this plan) and fixes (Plan 14).
- Helper container (§8.2): `NetworkMode: none`, `ReadonlyRootfs: true`, `CapDrop: ["ALL"]`, `SecurityOpt: ["no-new-privileges", "label=disable"]`, `Memory` 32 MiB, `PidsLimit` 16, `User: "0:0"`, labels `dev.stackstead.helper=true`, `dev.stackstead.stack=<name>`, `dev.stackstead.operation=<id>`; `Cmd` is an argv array with `--` before every path; never a shell except Plan 14's count.
- Config defaults (§6.4): `mount_scan_interval` `15m` (0 disables, else ≥ `1m`); `low_space_warning_percent` `10`, `low_space_warning_free` `2GiB`, `low_space_danger_percent` `3`, `low_space_danger_free` `500MiB` (percent 0–100, danger ≤ warning); `userns_remap_user` `""`.
- Scan limits (§6.2): concurrency 2, per-unit timeout 10 s, pass budget 120 s, caps 300 containers / 3000 mounts / 200 `/top` / 20 `compose config`, jitter ±10 %, no work in the first 60 s, debounce 5 s.
- Report cache 10 s per stack, single flight, `refresh=1` bypasses; Deep-check results kept 10 min; Deep check timeout 15 s.
- UI (§10.6): severity and verdicts always have text, icon-only controls carry `aria-label`, per-service table becomes cards under 720 px, no `{@html}`.
- The TypeSafe skill could not be used as a decision scorer for the spec (§3); decisions in this plan are argued in writing too.

## Review Focus

Five inputs the spec implies but names no test for; each has its test in the task named.

1. `mount_scan_interval: 0` in YAML. `config.rejectBareDurations` (`config/config.go:84`) rejects a bare integer for every duration key; `0` must be accepted for this key and still rejected for the others. (Task 4.)
2. A bind source that is a dangling symlink, or a symlink whose target is outside every allowed root: `symlink_source` plus `bind_source_missing`, both link and target shown, no panic, no `ErrOutsideRoots` leak. (Task 8.)
3. `/top` fails for a paused or restarting container (Docker 409/500): the user falls back to `Config.User` → image → unknown with `source` set accordingly, and only one `user_unknown` finding per service, not one per mount. (Task 6.)
4. A registered stack whose compose file disappears between the scanner's listing and its unit: the unit is skipped, its previous problems are dropped on that pass (the `parse_error` alert covers it), and the pass continues. (Task 12.)
5. Two services bind the same host directory with different users: each service gets its own verdict and finding ids, `Statfs` runs once for that device, and `low_space` is emitted once per filesystem, not per mount. (Task 8 and Task 9.)

---

## File structure

| Area | Files | Tasks |
|---|---|---|
| Compose model | `internal/composecli/model.go` (new), `runner.go`, `internal/storage/declarations.go` | T1 |
| Engine reads | `internal/engine/types.go`, `client.go`, `top.go` (new), new fixtures under `testdata/` | T2 |
| Engine helper | `internal/engine/helper.go` (new), `client.go` | T3 |
| Config | `internal/config/config.go`, `bytesize.go` (new), `template.yaml`, `template.go` | T4 |
| Mounts: pure core | `internal/mounts/types.go`, `idmap.go`, `verdict.go`, `user.go`, `join.go`, `findings_bind.go`, `findings_volume.go`, `findings_runtime.go`, `findings_space.go`, `analyze.go` | T5, T6, T8, T9 |
| Mounts: host access | `internal/mounts/hostfs.go`, `hostfs_linux.go`, `hostfs_darwin.go`, `hostfs_other.go`, `pathmapper.go`, `hostenv.go` | T7 |
| Mounts: collection | `internal/mounts/collector.go`, `modelcache.go`, `reportcache.go`, `deepcheck.go`, `helper_image.go`, `helper_pull.go` | T10, T11 |
| Scan + alerts | `internal/mounts/scanner.go`, `units.go`; `internal/alerts/alerts.go`, `rules.go`, `engine.go`; `internal/stacks/ops.go` | T12 |
| API | `internal/api/mounts.go` (new), `server.go`, `internal/apitypes/types.go`, `internal/cli/serve.go` | T13 |
| Web | `web/src/lib/api/types.ts`, `client.ts`, `web/src/lib/stacks/storage.ts`, `StoragePanel.svelte` (new), `web/src/routes/StackDetail.svelte`, `ContainerDetail.svelte`, `web/src/lib/containers/MountBadges.svelte` (new), `web/src/lib/stacks/OperationPanel.svelte`, `web/src/lib/dashboard/AlertsCard.svelte` | T14, T15 |
| Release, docs, e2e | `Makefile`, `docs/api.md`, `docs/security.md`, `docs/development.md`, `README.md`, `internal/mounts/integration_test.go`, `cmd/stackstead/e2e_test.go` | T16, T17 |

## Execution order

T1, T2, T3, T4 are independent (wave 1). T5 → T6 → T7 → T8 → T9 (pure core, then host access, then analysis). T10 needs T1–T9; T11 needs T3, T10; T12 needs T10; T13 needs T11, T12; T14, T15 need T13; T16 needs T11; T17 needs everything. Within each task: Sonnet implements test-first, Opus reviews the task, Fable reviews the whole branch at the end.

---

### Task 1: `composecli.Model` and `FileSetHash`

Spec §4.1 (declared mounts), §6.2 (cache key), §11 (secrets).

**Files:**
- Create: `internal/composecli/model.go`, `internal/composecli/testdata/config-volumes.json` (fixture: one service with long-syntax bind (`create_host_path: false`, `selinux: z`, `propagation: rprivate`), one short-syntax bind normalised by compose, one named volume with `subpath`, one anonymous volume, a `tmpfs:` shorthand entry and a long tmpfs with `size`/`mode`, `user: "1000:1000"`, `group_add: ["44"]`, `read_only: true`, an `environment` with `DB_PASSWORD: hunter2`; top-level volumes: one `external: true`, one with `driver_opts: {type: nfs, o: "addr=10.0.0.1,password=s3cret"}`)
- Modify: `internal/composecli/runner.go:88-96` (reuse `ConfigJSON`), `internal/storage/declarations.go:218-235` (replace `declarationHash` with the exported function), `internal/composecli/runner_test.go:17-95` (fake docker gains a `FAKE_MODE=volumes` branch that prints the fixture)
- Test: `internal/composecli/model_test.go`

**Interfaces:**
- Produces: `type ComposeModel struct { Name string; Services map[string]ServiceModel; Volumes map[string]VolumeModel }`; `type ServiceModel struct { User string; GroupAdd []string; ReadOnly bool; Volumes []ServiceVolume }`; `type ServiceVolume struct { Type, Source, Target string; ReadOnly bool; Bind *BindOptions; Volume *VolumeOptions; Tmpfs *TmpfsOptions }`; `type BindOptions struct { CreateHostPath bool; SELinux, Propagation string }`; `type VolumeOptions struct { NoCopy bool; Subpath string }`; `type TmpfsOptions struct { Size int64; Mode uint32 }`; `type VolumeModel struct { Name string; External bool; Driver, DriverType string }`; `func (r *Runner) Model(ctx context.Context, file string) (ComposeModel, error)`; `func FileSetHash(composePath string) ([32]byte, error)`.

- [ ] **Step 1: Write the failing tests.** `TestModel_DecodesVolumes` (every field above from the fixture; `tmpfs:` shorthand becomes a `ServiceVolume{Type: "tmpfs"}`; the anonymous volume has empty `Source`; `DriverType == "nfs"`), `TestModel_NeverRetainsEnvironment` (marshal the decoded struct to JSON with `reflect`/`json` and assert `hunter2` and `s3cret` are absent), `TestModel_Error` (fake exits 17 with stderr `bad config`: error is `*ExitError` carrying `bad config` and not stdout), `TestFileSetHash_ChangesWithEnv` (same compose, `.env` added → different hash; `.env` absent is not an error; missing compose file is).
- [ ] **Step 2: Run** `go test -race ./internal/composecli/ -run 'TestModel|TestFileSetHash'` — expected FAIL: `undefined: (*Runner).Model`, `undefined: FileSetHash`.
- [ ] **Step 3: Implement** `Model` over `ConfigJSON` (decode only the fields above with a struct that has no `environment` member; `volumes[]` entries that are strings after normalisation do not occur, compose emits long form) and `FileSetHash` as the moved `declarationHash`; make `storage.DeclarationCache.Get` call `composecli.FileSetHash`.
- [ ] **Step 4: Run** `go test -race ./internal/composecli/ ./internal/storage/` — expected `ok` for both; `go vet ./...` clean.
- [ ] **Step 5: Commit** `feat(composecli): decode the compose volume model and share the file-set hash` (why: the mount diagnostics need compose's normalised mounts, and one hash definition keeps the storage and mount caches in step).

---

### Task 2: Engine read additions — inspect fields, `Info` security options, `ContainerTop`

Spec §4.1 rows "Runtime mounts", "Process user", "Daemon facts"; §4.7 mode detection input.

**Files:**
- Modify: `internal/engine/types.go:72-91` (`Mount`, `ContainerInspect`), `:190-215` (`apiInspect`: add `State.Error`, `Created`, `Config.User`, `HostConfig.GroupAdd`), `:301-312` (`apiMount`: `Driver`, `Mode`, `Propagation`), `internal/engine/client.go:107-124` (`Info`)
- Create: `internal/engine/top.go`, fixtures `testdata/top.json` (titles `PID EUID EGID SUPGID COMM`, rows for `tini` pid 1 uid 0 and `jellyfin` uid 1000 gid 1000 supgid `44,1000`), `testdata/top-busybox.json` (titles `PID USER COMMAND` only), `testdata/info-rootless.json` (`SecurityOptions: ["name=seccomp,profile=builtin","name=rootless"]`, `DockerRootDir`, `OperatingSystem`); extend `testdata/inspect_container.json` with `State.Error`, `Created`, `Config.User: "1000:1000"`, `HostConfig.GroupAdd: ["44"]`, mount `Driver`/`Mode`/`Propagation`
- Test: `internal/engine/top_test.go`, additions to `client_test.go` (`TestInfoDecodes` at `:619`), `types_test.go`

**Interfaces:**
- Produces: `Mount` gains `Driver, Mode, Propagation string`; `ContainerInspect` gains `StateError string`, `Created time.Time`, `GroupAdd []string`, `User string` (doc comment: internal, never copied into a DTO); `Info` gains `SecurityOptions []string`, `DockerRootDir, OperatingSystem string`; `type Process struct { PID int; EUID, EGID uint32; SupGIDs []uint32; Comm string }`; `var ErrTopColumns = errors.New(...)`; `func (c *Client) ContainerTop(ctx context.Context, id, psArgs string) ([]Process, error)`.

- [ ] **Step 1: Write the failing tests.** `TestInspectContainerExtendedFields` (new fields decoded; `User == "1000:1000"`; `Created` parsed; zero `Created` normalised like `StartedAt`), `TestContainerDetailsNeverCarryUser` in `internal/api/containers_test.go` (the `/api/containers/{id}` body has no `user` key even when the fixture has one), `TestInfoDecodesSecurityOptions` (three new fields), `TestContainerTop` (request path is `/v1.41/containers/<id>/top` with query `ps_args=-o pid,euid,egid,supgid,comm` URL-encoded; two processes decoded with `SupGIDs == [44, 1000]`), `TestContainerTopMissingColumns` (busybox titles → `ErrTopColumns`), `TestContainerTopNotFound` (404 → `ErrNotFound`).
- [ ] **Step 2: Run** `go test -race ./internal/engine/ -run 'TestInspectContainerExtended|TestInfoDecodesSecurity|TestContainerTop'` — expected FAIL: `unknown field StateError`, `undefined: (*Client).ContainerTop`.
- [ ] **Step 3: Implement** the type and decoder additions, and `ContainerTop` over `get` (titles are matched by name, case-insensitive; a missing title → `ErrTopColumns`; `SUPGID` is comma-separated, `-` means none).
- [ ] **Step 4: Run** `go test -race ./internal/engine/ ./internal/api/` — `ok`; existing inspect tests unchanged except the additive fields.
- [ ] **Step 5: Commit** `feat(engine): read mount options, start error, process ids and daemon security options` (why: the verdicts need the container's real user and mount flags, not the compose file's).

---

### Task 3: Engine helper lifecycle — `RunHelper`, `PullImage`, `ImagePresent`, `RemoveHelpers`

Spec §8.2 (container shape), §7.3 (pull), §4.4 (Deep check uses it). Plan 14 reuses it unchanged, so the write-capable options (`CapAdd`, rw binds, `UsernsMode`) are built here.

**Files:**
- Create: `internal/engine/helper.go`; fixtures `testdata/helper-create.json` (`{"Id":"h1"}`), `testdata/helper-inspect.json` (one mount with `Source` `/srv/x`), `testdata/helper-wait.json` (`{"StatusCode": 0}`), `testdata/images-create.ndjson` (progress lines then `{"status":"Digest: sha256:…"}`)
- Test: `internal/engine/helper_test.go`

**Interfaces:**
- Produces: `type HelperBind struct { Source, Target string; ReadOnly bool }`; `type HelperSpec struct { Image string; Cmd []string; Binds []HelperBind; CapAdd []string; UsernsMode string; Labels map[string]string }`; `type HelperResult struct { ExitCode int; Stdout, Stderr string }`; `var ErrHelperMountMismatch`; `func (c *Client) RunHelper(ctx context.Context, spec HelperSpec, out func(stream, line string)) (HelperResult, error)` (`out` may be nil; bounded by `ctx`); `func (c *Client) PullImage(ctx context.Context, ref string, out func(line string)) error` (ref `repo:tag@sha256:…`; verifies the pulled `RepoDigests` contains the digest, else error); `func (c *Client) ImagePresent(ctx context.Context, ref string) (bool, error)`; `func (c *Client) RemoveHelpers(ctx context.Context) (removed int, err error)` (exited containers with label `dev.stackstead.helper=true`); const `HelperLabel = "dev.stackstead.helper"`.

- [ ] **Step 1: Write the failing tests.** `TestRunHelperSequenceAndHostConfig` (fake records `POST /containers/create` body: `HostConfig.NetworkMode == "none"`, `ReadonlyRootfs`, `CapDrop == ["ALL"]`, `CapAdd` as given, `SecurityOpt == ["no-new-privileges","label=disable"]`, `Memory == 33554432`, `PidsLimit == 16`, `User == "0:0"`, `Binds == ["/srv/x:/t/0:ro"]`, labels; then `GET …/json`, `POST …/start`, `POST …/wait`, `GET …/logs?stdout=1&stderr=1`, `DELETE …?force=1` in that order; result exit 0 and demuxed stdout/stderr), `TestRunHelperMountMismatchRemovesContainer` (inspect `Source` differs → `ErrHelperMountMismatch`, no `start`, one `DELETE`), `TestRunHelperCancelForceRemoves` (ctx cancelled during `wait` → `DELETE …?force=1` still sent, error wraps `context.Canceled`), `TestRunHelperNonZeroExit` (wait returns 2 → `ExitCode 2`, `err == nil`), `TestPullImageStreamsAndChecksDigest` (query `fromImage=busybox&tag=1.37.0@sha256:…` — confirm the exact form Docker accepts in the integration test of Task 17 and adjust; lines passed to `out`; mismatching digest → error naming both), `TestImagePresent` (200 → true, 404 → false, 500 → error), `TestRemoveHelpersByLabel` (list filtered by label and `status=exited`, each removed, count returned).
- [ ] **Step 2: Run** `go test -race ./internal/engine/ -run 'TestRunHelper|TestPullImage|TestImagePresent|TestRemoveHelpers'` — FAIL: `undefined: HelperSpec`.
- [ ] **Step 3: Implement** in `helper.go` using `postJSON`/`request` and the existing `demuxLogs` for the logs body; a 60 s own bound like `RemoveImage` when `ctx` has no deadline; `defer` the force-remove.
- [ ] **Step 4: Run** `go test -race ./internal/engine/` — `ok`.
- [ ] **Step 5: Commit** `feat(engine): run a locked-down helper container and pull an image by digest` (why: Deep check and fixes need root reads and writes without a shell on the host, and the safety settings belong in one place that tests can pin).

---

### Task 4: Configuration keys and `ByteSize`

Spec §6.4. Review focus 1.

**Files:**
- Create: `internal/config/bytesize.go`
- Modify: `internal/config/config.go:18-43` (fields, defaults), `:80-95` (`durationKeys` and the `0` exception), `:97-138` (env), `:152-177` (validate), `internal/config/template.yaml:20` (new commented block after `operation_timeout`), `internal/config/template.go` (render the new lines through `optional`)
- Test: `internal/config/bytesize_test.go`, additions to `config_test.go`, `template_test.go`

**Interfaces:**
- Produces: `type ByteSize int64` with `func ParseByteSize(s string) (ByteSize, error)` (`KiB MiB GiB TiB` binary, `KB MB GB TB` decimal, bare bytes, case-insensitive, no negatives), `func (b ByteSize) String() string`, `UnmarshalYAML`; `Config` gains `MountScanInterval time.Duration`, `LowSpaceWarningPercent, LowSpaceDangerPercent int`, `LowSpaceWarningFree, LowSpaceDangerFree ByteSize`, `UsernsRemapUser string` (yaml keys as §6.4; env `STACKSTEAD_MOUNT_SCAN_INTERVAL` etc.).

- [ ] **Step 1: Write the failing tests.** `TestParseByteSize` table (`2GiB`→2147483648, `500MiB`, `1GB`→1000000000, `1024`, `1.5GiB` rejected or accepted — pick rejected, `-1` rejected, `2 GiB` with a space rejected), `TestMountKeysDefaults` (the six defaults), `TestMountKeysFromYAMLAndEnv` (file sets `mount_scan_interval: 30m`, env overrides to `0`; byte sizes from env), `TestMountScanIntervalZeroAccepted` (YAML `mount_scan_interval: 0` loads as disabled while `operation_timeout: 900` is still rejected), `TestMountKeysValidation` (`30s` rejected with "at least 1m", percent 101, danger percent 20 > warning 10, danger free > warning free, `userns_remap_user: "no spaces here"` rejected), `TestTemplateRendersMountKeys` (defaults appear commented, a changed value appears uncommented).
- [ ] **Step 2: Run** `go test -race ./internal/config/` — FAIL: `undefined: ParseByteSize`, unknown fields.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/config/` — `ok`.
- [ ] **Step 5: Commit** `feat(config): mount scan interval, low-space thresholds and userns remap user` (why: SD cards and data disks need different low-space numbers, and rootful userns daemons do not expose their remap user).

---

### Task 5: `internal/mounts` core types and ID mapping

Spec §4.7, §9. Pure code, no I/O.

**Files:**
- Create: `internal/mounts/types.go`, `internal/mounts/idmap.go`
- Test: `internal/mounts/idmap_test.go`

**Interfaces:**
- Produces (`types.go`, used by every later task): `type Kind string` (`KindBind`, `KindVolume`, `KindTmpfs`, `KindNpipe`, `KindImage`, `KindCluster`); `type Verdict string` (`VerdictYes = "yes"`, `VerdictNo`, `VerdictUnknown`); `type Access struct { Read, Write Verdict }`; `DeclaredMount` exactly as spec §9 plus `DriverType string` (not `DriverOpts`); `type RuntimeMount struct { Service, Container, Target, Source, Name string; Kind Kind; ReadOnly bool; Driver, Mode, Propagation string }`; `type FSInfo struct { Free, Total uint64; ReadOnly, NoExec bool }`; `HostInfo` as spec §9; `EffectiveUser` as spec §9 (`Source` one of `process|config|image|unknown`); `type Remedy struct { Text, Command string; Fixes []string }`; `Finding` as spec §9 (`Evidence map[string]any`); `type VolumeInfo struct { Name, Driver, DriverType, Project string; Exists, External, Anonymous bool }`; `type TmpfsInfo struct { Size int64; Mode uint32 }`; `type MountEntry struct { Kind Kind; Target, Source string; ReadOnly, Declared, Runtime bool; SELinuxOption, Propagation string; Host *HostInfo; Access Access; Volume *VolumeInfo; Tmpfs *TmpfsInfo; FindingIDs []string }`; `type ServiceMounts struct { Name string; Containers []string; User EffectiveUser; Mounts []MountEntry }`; `type HostFacts struct { OS, PathChecks string; SELinux, AppArmor bool; IDMapping IDMapping; HelperImage string }`; `Report` as spec §9; `type Thresholds struct { WarningPercent, DangerPercent int; WarningFree, DangerFree uint64 }`.
- Produces (`idmap.go`): `IDMapping`, `IDRange` as spec §9; `type IDSource interface { ReadFile(path string) ([]byte, error); SocketOwner(path string) (uid uint32, err error); LookupUser(nameOrUID string) (name string, uid, gid uint32, err error) }`; `func ParseSubIDs(r io.Reader, user string, uid uint32) (ranges []IDRange, malformed int)`; `func DetectIDMapping(info engine.Info, socketPath string, src IDSource, remapUser string) IDMapping`; `func (m IDMapping) ToHostUID(c uint32) (uint32, bool)`; `func (m IDMapping) ToHostGID(c uint32) (uint32, bool)`; `const ModeNone, ModeRootless, ModeUserns`, `StatusExact, StatusUnavailable`.

- [ ] **Step 1: Write the failing tests** (spec §12 names them): `TestParseSubIDs` (name and uid forms, two ranges kept in file order, other users ignored, comment/blank/malformed lines skipped and counted, `start+count` overflowing uint32 rejected and counted), `TestDetectIDMapping` (table: `name=rootless` + socket owner 1000 under `/run/user/1000/docker.sock` → rootless/exact with user `alice`; root-owned socket → unavailable "socket is owned by root"; `/run/user/1001/…` owned by 1000 → unavailable; `name=userns` with `dockremap` present / `remapUser` set / neither → exact, exact, unavailable "set userns_remap_user"; no option → `ModeNone`; subuid missing / EACCES / no entry → unavailable with the matching reason text), `TestIDMappingToHost` (rootless: 0 → daemon uid, 1 → `start₁`, last of range 1 → `start₁+count₁−1`, first of range 2 → `start₂`, beyond → `false`; userns: 0 → `start₁`; gid independent of uid ranges).
- [ ] **Step 2: Run** `go test -race ./internal/mounts/` — FAIL: package has no `ParseSubIDs`.
- [ ] **Step 3: Implement** (`Reason` strings are the ones the tests assert; `/run/user/<n>` is matched with a regexp on the cleaned socket path).
- [ ] **Step 4: Run** — `ok`.
- [ ] **Step 5: Commit** `feat(mounts): report types and subuid/subgid id mapping` (why: without mapping every rootless verdict would be wrong or unknown).

---

### Task 6: Access verdict and effective user

Spec §4.3 (verdict), §3 "Container user", §4.7 "Which ids need mapping". Review focus 3.

**Files:**
- Create: `internal/mounts/verdict.go`, `internal/mounts/user.go`
- Test: `internal/mounts/verdict_test.go`, `internal/mounts/user_test.go`

**Interfaces:**
- Produces: `func AccessFor(info HostInfo, ancestors []HostInfo, user EffectiveUser, readOnly bool, goos string) Access` (unknown whenever `user.HostUID == nil`, which covers unknown users and unmapped ids); `type ContainerUserInput struct { Top []engine.Process; TopErr error; ConfigUser, ImageUser string; GroupAdd []string }`; `func ResolveUser(in ContainerUserInput, m IDMapping) EffectiveUser` (init names `tini`, `dumb-init`, `s6-svscan`, `docker-init` are skipped when other processes exist); `EffectiveUser` gains `AltUIDs []uint32` (every distinct non-init uid seen in `/top`; `UID` is the first) so Task 8 evaluates each and keeps the most permissive verdict; `func MostPermissive(a, b Access) Access`.

- [ ] **Step 1: Write the failing tests.** `TestAccessVerdict` table: owner bits only (uid matches → `r yes w yes`), group via primary gid, group via supplementary gid, other bits, `0755` root-owned dir with uid 1000 → `w no`, a read-only mount → `Write == ""` (not evaluated), ACL present → both unknown, `HostUID == nil` → unknown, uid 0 → yes/yes, uid 0 on `FS.ReadOnly` → `w no`, directory without `x` → `r no`, ancestor without `x` for the user → `r no` and `w no`, `goos == "darwin"` → unknown. `TestEffectiveUser`: top with `tini` + `jellyfin` → uid 1000 from `process` with groups `[44,1000]`; two app uids → both in `AltUIDs`; `TopErr` set → `ConfigUser "1234"` → `config`; `ConfigUser "1234:100"` splits; `ConfigUser "appuser"` (a name) → falls to `ImageUser`; `ImageUser "1000"` → `image`; everything empty → `unknown` with nil ids; `GroupAdd ["44","video"]` → numeric only kept. `TestEffectiveUser_Mapping`: rootless maps `/top` ids and `ConfigUser`; userns maps `ConfigUser` but not `/top`; unmapped id → `HostUID == nil`.
- [ ] **Step 2: Run** — FAIL: `undefined: AccessFor`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/mounts/` — `ok`.
- [ ] **Step 5: Commit** `feat(mounts): POSIX access verdicts for the container's effective user` (why: "permission denied" is the most common mount failure and only the process uid, not the compose `user`, tells the truth).

---

### Task 7: `HostFS`, `PathMapper` and container detection

Spec §4.3 steps 1–8, §4.5, §4.6.

**Files:**
- Create: `internal/mounts/hostfs.go` (interface, `FakeHostFS` for tests lives in `hostfs_fake_test.go`), `hostfs_linux.go`, `hostfs_darwin.go`, `hostfs_other.go` (returns "unsupported" errors), `pathmapper.go`, `hostenv.go`
- Modify: `go.mod` (`golang.org/x/sys` becomes a direct requirement; `go mod tidy`)
- Test: `internal/mounts/hostfs_test.go` (runs on linux and darwin against `t.TempDir()`), `hostenv_test.go`

**Interfaces:**
- Produces: `type HostFS interface { Lstat(path string) (HostInfo, error); Statfs(path string) (FSInfo, error); Label(path string) (string, error); HasACL(path string) (bool, error); IsEmptyDir(path string) (bool, error) }` (`Lstat` fills `Type`, `UID`, `GID`, `Owner`/`Group` via `os/user` best effort, `Mode`, `Ctime`, `Dev`, `LinkTarget` for symlinks; it never follows the link); `func NewOSHostFS() HostFS`; `type PathMapper interface { ToLocal(hostPath string) (string, bool) }`; `type IdentityMapper struct{}`; `type UnavailableMapper struct{ Reason string }`; `func InContainer(stat func(string) error, readFile func(string) ([]byte, error)) bool` (`/.dockerenv` exists, or `/proc/1/cgroup` contains `docker`, `containerd`, `kubepods`, `libpod`).

- [ ] **Step 1: Write the failing tests.** `TestOSHostFS_LstatDirFileSymlink` (temp dir with `0750` dir, a file, a symlink to the file and a dangling symlink: type, mode, uid equals `os.Getuid()`, `LinkTarget`, dangling → `Exists true, Type "symlink"`), `TestOSHostFS_IsEmptyDir` (empty vs one entry; a file → error), `TestOSHostFS_Statfs` (`Total > 0`, `Free <= Total`), `TestOSHostFS_HasACLAndLabelWithoutXattrs` (temp dir → `false`, `""`, no error; `ENOTSUP`/`ENODATA` treated as absent), `TestOSHostFS_PermissionDenied` (skip as root; `0000` dir → `Lstat` of a child returns an error wrapping `fs.ErrPermission`), `TestInContainer` (table over the two probes).
- [ ] **Step 2: Run** — FAIL: `undefined: NewOSHostFS`.
- [ ] **Step 3: Implement** with `syscall.Stat_t` fields (`Ctim`/`Ctimespec` differ per OS, hence the two files), `unix.Statfs` (`ST_RDONLY`, `ST_NOEXEC` flags on Linux), `unix.Lgetxattr` for `security.selinux` and `system.posix_acl_access`.
- [ ] **Step 4: Run** `go test -race ./internal/mounts/`, `go vet ./...`, `GOOS=windows go vet ./internal/mounts/` (the `_other` file must build) — all clean.
- [ ] **Step 5: Commit** `feat(mounts): metadata-only host filesystem reads and container detection` (why: the diagnostics must never open files, and a containerised Stackstead sees different paths).

---

### Task 8: `Analyze` — join, bind findings and finding identity

Spec §4.2 bind, §5 rows `bind_source_missing` … `symlink_source`, `docker_socket`, `not_inspectable`, `user_unknown`, `id_mapping_unavailable`; ordering; §7.1 `remedy.fixes` lists only fixes the server would accept now (roots, Linux, path checks). Review focus 2 and 5.

**Files:**
- Create: `internal/mounts/join.go`, `internal/mounts/findings_bind.go`, `internal/mounts/analyze.go`
- Test: `internal/mounts/analyze_bind_test.go`, `internal/mounts/analyze_test.go` (shared builders: `svc(...)`, `bind(...)`, `probe(...)`)

**Interfaces:**
- Produces: `type HostProbe struct { Info HostInfo; Ancestors []HostInfo; Err error }` (`Err` wrapping `fs.ErrPermission` → not readable); `type VolumeState struct { Inspect engine.VolumeInspect; Exists bool }`; `type ContainerInput struct { ID, Name string; Created, StartedAt time.Time; Running bool; ExitCode, RestartCount int; StateError string; Mounts []RuntimeMount; User ContainerUserInput }`; `type ServiceInput struct { Name string; Declared []DeclaredMount; Containers []ContainerInput; User string; GroupAdd []string; ImageUser string }`; `type Input struct { Stack string; Services []ServiceInput; Volumes map[string]VolumeState; Probes map[string]HostProbe; Host HostFacts; Mapping IDMapping; Mapper PathMapper; AllowedRoots []string; DockerRootDir string; Now time.Time }`; `func Analyze(in Input, th Thresholds) Report` (pure; `GeneratedAt = in.Now`); `func FindingID(code, service, target string) string` (16 hex chars of sha256); `func joinMounts(s ServiceInput) []MountEntry` (keyed by target; `Declared`/`Runtime` flags); finding constructors in `findings_bind.go` return `[]Finding`; `fixesAllowed(in Input, path string) []string` (Plan 14 reuses: `mkdir|chown|chmod` only when `path` is strictly below an allowed root, `Host.OS == "linux"`, `PathChecks == "available"`).

- [ ] **Step 1: Write the failing tests.** `TestAnalyze_BindSourceMissing` (warning by default, `danger` with `CreateHostPath: false`; remedy `Fixes == ["mkdir"]` inside roots, empty outside), `TestAnalyze_BindAutocreated` (empty `0:0` `0755` dir, ctime within 60 s of `Created`, uid 1000 → warning with `Evidence["likely"] == true` and `Command == "sudo chown 1000:1000 /srv/media/config"`; not flagged when non-empty, 2 min older, or user root), `TestAnalyze_FileDirMismatch` (three triggers: `StateError` "not a directory", empty dir named `nginx.conf`, missing source with target `/etc/app.conf`; no fixes; command contains `rmdir`), `TestAnalyze_NotWritableSeverity` (rw mount `w no` → warning while running, danger when the container exited or `RestartCount > 0`), `TestAnalyze_NotReadable`, `TestAnalyze_ParentNotTraversable` (ancestor `0700` root → fix `chmod` only when the ancestor is inside roots), `TestAnalyze_WorldWritable` (writable only via `o+w` → info), `TestAnalyze_SymlinkSource` (link + target both in evidence; dangling link → also `bind_source_missing`; target outside roots → no `Fixes`), `TestAnalyze_DockerSocket` (`/var/run/docker.sock` bind → info), `TestAnalyze_NotInspectable` (probe `Err` permission → `Host.Readable false`, verdict unknown, one `not_inspectable` per mount, no `not_writable`), `TestAnalyze_UserUnknownOnce` (unknown user and three mounts → one `user_unknown`), `TestAnalyze_IDMappingUnavailableOnce`, `TestAnalyze_SharedSourceTwoServices` (same dir, uid 1000 and uid 0: one `not_writable` for the first only; ids differ by service), `TestFindingIDStable` (two runs → identical ids; different service → different id), `TestAnalyze_Ordering` (danger before warning before info, then service, then target).
- [ ] **Step 2: Run** — FAIL: `undefined: Analyze`.
- [ ] **Step 3: Implement** `join.go`, `findings_bind.go`, `analyze.go` (volume/runtime/space findings arrive in Task 9; `analyze.go` calls stubs that return nil until then). Message texts come from §5; the file-extension list is a package-level slice.
- [ ] **Step 4: Run** `go test -race ./internal/mounts/` — `ok`.
- [ ] **Step 5: Commit** `feat(mounts): analyse bind mounts into findings with stable ids` (why: the operator needs one sentence, the evidence and the next step, not a raw mount list).

---

### Task 9: `Analyze` — volumes, drift, tmpfs, space, SELinux, unavailable modes

Spec §4.2 volume/tmpfs, §4.5, §4.6, §5 rows `volume_*`, `anonymous_volume`, `mount_drift`, `readonly_fs`, `low_space`, `selinux_label`, `tmpfs_unbounded`. Review focus 5 (space once per filesystem).

**Files:**
- Create: `internal/mounts/findings_volume.go`, `internal/mounts/findings_runtime.go` (drift, tmpfs, start error), `internal/mounts/findings_space.go` (low space, readonly fs, SELinux)
- Modify: `internal/mounts/analyze.go` (wire the three)
- Test: `internal/mounts/analyze_volume_test.go`, `analyze_runtime_test.go`, `analyze_space_test.go`, `analyze_unavailable_test.go`

**Interfaces:**
- Consumes: Task 8 types. Produces: nothing new beyond the finding codes; `Thresholds` semantics: a filesystem trips when `free*100/total < percent` **or** `free < bytes`, danger evaluated before warning.

- [ ] **Step 1: Write the failing tests.** `TestAnalyze_Volumes` (external missing → danger with `docker volume create media_db`; label `com.docker.compose.project=other` → `volume_foreign_project`; `OptionType nfs` or driver not `local` → `volume_remote` and no owner/space checks; anonymous → info; `Volume.Exists` filled), `TestAnalyze_Drift` (source changed, ro changed, declared target absent at runtime, extra runtime target → one `mount_drift` each; equal → none; a stack never started → none), `TestAnalyze_StartError` (Docker "mount a directory onto a file" text → `file_dir_mismatch` — overlaps Task 8; here: unrelated `StateError` → no finding), `TestAnalyze_Tmpfs` (no size → `tmpfs_unbounded`; `Tmpfs` info filled), `TestAnalyze_LowSpace` (percent trips first on a 1 TiB disk at 5 % free; bytes trip first on a 16 GiB card with 1 GiB free; danger thresholds; custom thresholds; two mounts on one `Dev` → one finding), `TestAnalyze_ReadonlyFS` (rw bind on `FS.ReadOnly` → warning), `TestAnalyze_SELinux` (on + no `z` + `unconfined_u:object_r:default_t:s0` → warning with command "add `:z`"; `container_file_t` or `svirt_sandbox_file_t` or `:z`/`:Z` → none; SELinux off → never), `TestAnalyze_Unavailable` (`PathChecks "unavailable"`: no host findings, volume/drift/tmpfs still present, every `Remedy.Fixes` empty; `OS darwin`: ownership verdicts unknown, no fixes, volume rows carry evidence "inside the Docker VM").
- [ ] **Step 2: Run** — FAIL on the new codes (findings slice empty).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/mounts/` — `ok`.
- [ ] **Step 5: Commit** `feat(mounts): volume, drift, space and SELinux findings; container and macOS modes` (why: cases 4–7 of the spec, and wrong verdicts are worse than none where the host is not visible).

---

### Task 10: `Collector` — stack and container reports with caches

Spec §4.1 (sources), §6.2 (caches), §7.1 (10 s single flight, `refresh=1`, 409 on parse error, `helperImage`), §7.4.

**Files:**
- Create: `internal/mounts/collector.go`, `internal/mounts/modelcache.go`, `internal/mounts/reportcache.go` (per-stack single flight modelled on `internal/api/flightcache.go:11-70`), `internal/mounts/fakes_test.go` (`fakeEngine` implementing `Engine` with call counters, `fakeStacks`, `fakeCompose`)
- Test: `internal/mounts/collector_test.go`

**Interfaces:**
- Produces: `type Engine interface { InspectContainer(ctx, id string) (engine.ContainerInspect, error); InspectVolume(ctx, name string) (engine.VolumeInspect, error); InspectImage(ctx, ref string) (engine.ImageInspect, error); ContainerTop(ctx, id, psArgs string) ([]engine.Process, error); Info(ctx) (engine.Info, error); RunHelper(ctx, engine.HelperSpec, func(string, string)) (engine.HelperResult, error); PullImage(ctx, ref string, out func(string)) error; ImagePresent(ctx, ref string) (bool, error) }` (satisfied by `*engine.Client`); `type ComposeModeler interface { Model(ctx, file string) (composecli.ComposeModel, error) }`; `type StackSource interface { List(ctx) ([]stacks.Summary, error); Get(ctx, name string) (stacks.Detail, error) }`; `type ContainerSource interface { Containers(ctx) ([]engine.Container, error) }`; `type CollectorConfig struct { Engine Engine; Compose ComposeModeler; Stacks StackSource; Containers ContainerSource; HostFS HostFS; Mapper PathMapper; IDs IDSource; SocketPath, RemapUser string; AllowedRoots []string; GOOS string; Thresholds Thresholds; Now func() time.Time; Logger *slog.Logger }`; `func NewCollector(cfg CollectorConfig) *Collector`; `type CollectOptions struct { Refresh bool }`; `func (c *Collector) Stack(ctx, name string, opts CollectOptions) (Report, error)` (404: `stacks.ErrStackNotFound`; parse error: wraps `stacks.ErrStackUnavailable`); `func (c *Collector) Container(ctx, id string) (Report, error)` (one service named after the container, `Stack` the registered stack name or `""`); `func (c *Collector) Invalidate(stack string)`; `func (c *Collector) Mapping() IDMapping` (recomputed on `Info` change); `func (c *Collector) HelperImagePresent(ctx) string` (`present|absent|unknown`, cached one minute).
- Caches: compose model by `composecli.FileSetHash` (no `Model` call when unchanged); inspect + `/top` by container id + `StartedAt`; volume inspects for `CollectorConfig.VolumeTTL time.Duration` (default 15 m; `serve.go` passes `cfg.MountScanInterval` when it is > 0); `Statfs` deduplicated per `Dev` within one report; ancestor probes per directory within one report.

- [ ] **Step 1: Write the failing tests.** `TestCollector_StackReportJoinsSources` (fake compose model + two containers + probes via `FakeHostFS` → report with `Declared && Runtime` entries, `User.Source "process"`, `Host.HelperImage "present"`), `TestCollector_CacheAndRefresh` (two calls within 10 s → one compose and one inspect; `Refresh: true` → recomputed; `Invalidate` → recomputed), `TestCollector_SingleFlight` (20 concurrent calls → one computation), `TestCollector_ModelCachedByHash` (file unchanged across `Invalidate` → `Model` not called again; edit the file → called), `TestCollector_TopOncePerStart` (same `StartedAt` → one `ContainerTop`; new `StartedAt` → another), `TestCollector_ParseErrorIs409` (`Detail.ParseError` set → `errors.Is(err, stacks.ErrStackUnavailable)`), `TestCollector_UnknownStack`, `TestCollector_ContainerReport` (unregistered container: `Declared false` everywhere, `Stack == ""`), `TestCollector_NeverReturnsUserString` (marshal the report; the raw `"appuser"` config string is absent), `TestCollector_ContainerModeDisablesPathChecks` (`UnavailableMapper` → `PathChecks "unavailable"`, no probes requested).
- [ ] **Step 2: Run** — FAIL: `undefined: NewCollector`.
- [ ] **Step 3: Implement.** Probing a source: `Mapper.ToLocal`, then `Lstat`, type-dependent `IsEmptyDir`, `Statfs` once per `Dev`, `Label` when `Host.SELinux`, `HasACL`, ancestors up to `/`; a permission error at any step → `HostProbe.Err`. `Host.SELinux/AppArmor` from `Info.SecurityOptions`.
- [ ] **Step 4: Run** `go test -race ./internal/mounts/` — `ok`.
- [ ] **Step 5: Commit** `feat(mounts): collect declared and runtime mounts into cached stack and container reports` (why: a 10-service report must stay under a second on a Pi, so engine results are reused across calls).

---

### Task 11: Deep check, helper image pin and pull operation

Spec §4.4, §7.2, §7.3, §8.2 image paragraph. Review: the scanner never reaches this code (Task 12 asserts).

**Files:**
- Create: `internal/mounts/helper_image.go` (`const HelperImage = "busybox:1.37.0@sha256:<digest>"` — fill the digest by running Task 16's target once, before this task's tests run), `internal/mounts/deepcheck.go`, `internal/mounts/helper_pull.go`
- Test: `internal/mounts/helper_image_test.go`, `deepcheck_test.go`, `helper_pull_test.go`

**Interfaces:**
- Produces: `var ErrHelperImageAbsent`, `var ErrHelperFailed` (message = Docker's error or the helper's stderr); `func (c *Collector) DeepCheck(ctx, stack string) (Report, error)` (one `RunHelper` with `CapAdd ["DAC_READ_SEARCH"]`, read-only binds of each unreadable bind source and each `local` volume `Mountpoint` at `/t/<n>`, `Cmd` = `stat -c '%F|%u|%g|%a|%W|%Z' -- /t/0 /t/1 …` followed by one `find /t/<n> -maxdepth 0 -empty` per path, 15 s timeout; results stored per stack for 10 min and merged into the next `Stack()` report as `Host.Readable true` entries with `DeepCheckedAt`); unexported `type deepCheckResult struct { Paths map[string]HostInfo; At time.Time }`; `func (c *Collector) PullHelperImage(ctx) (stacks.Operation, error)` needs `CollectorConfig.Ops *stacks.Ops`: `Ops.Start("", "pull-helper", fn)`; on failure the output ends with `docker pull busybox@sha256:…`; `func ParseHelperImage(ref string) (repo, tag, digest string, err error)`.

- [ ] **Step 1: Write the failing tests.** `TestHelperImagePinned` (`HelperImage` parses, has a 64-hex digest and a numeric tag), `TestDeepCheck_SpecAndMerge` (fake engine records the `HelperSpec`: caps, ro binds in order, argv with `--`, labels; stdout fixture → the next report shows owner `0:0`, mode `0700`, `Empty true` for the volume root and `Readable true`; `DeepCheckedAt` set; after 10 min clock advance → gone), `TestDeepCheck_ImageAbsent` (→ `ErrHelperImageAbsent`, no `RunHelper`), `TestDeepCheck_HelperFailure` (exit 1 with stderr → `ErrHelperFailed` containing the stderr), `TestDeepCheck_RootlessVolumeRootUnreadable` (mapping rootless → report note that root-owned paths cannot be read), `TestPullHelperImage` (operation `Action "pull-helper"`, `Stack ""`; lines streamed; registry error → `OpFailed` with the `docker pull` line last).
- [ ] **Step 2: Run** — FAIL: `undefined: HelperImage`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/mounts/` — `ok`.
- [ ] **Step 5: Commit** `feat(mounts): deep check through a pinned busybox helper and a consent-gated pull` (why: Stackstead runs unprivileged; full coverage only on an explicit click, with no silent downloads).

---

### Task 12: Scanner, `Ops.OnEnd`, and `mount_problem` alerts

Spec §6.1–§6.3, §9 `Scanner`. Review focus 4.

**Files:**
- Create: `internal/mounts/scanner.go`, `internal/mounts/units.go`
- Modify: `internal/stacks/ops.go:81-97` (hook slice), `:173-204` (call hooks after unlock), new `Running`; `internal/alerts/alerts.go:14-23` (`KindMountProblem`), `:56` (`MountProblem`, `MountProblems`); `internal/alerts/engine.go:18-31`, `:104-146` (source field, setter, items from problems); `internal/alerts/rules.go:37-69`
- Test: `internal/mounts/scanner_test.go`, `scanner_bench_test.go`, `internal/stacks/ops_test.go`, `internal/alerts/engine_test.go`

**Interfaces:**
- Produces: `func (o *Ops) OnEnd(fn func(Operation))`; `func (o *Ops) Running(stack string) bool`; `type UnitKey struct { Stack, ContainerID string }`; `type ScannerConfig struct { Collector *Collector; Stacks StackSource; Containers ContainerSource; Connected func() bool; Events func() (<-chan engine.Event, func()); OpsRunning func(stack string) bool; Interval time.Duration; Now func() time.Time; Sleep func(context.Context, time.Duration) error; Logger *slog.Logger }` (`Sleep` injectable for the fake clock); `func NewScanner(cfg ScannerConfig) *Scanner`; `func (s *Scanner) Run(ctx context.Context)`; `func (s *Scanner) Trigger(k UnitKey)`; `func (s *Scanner) Problems() []alerts.MountProblem`; `func (s *Scanner) LastScanned(k UnitKey) (time.Time, bool)`. In `alerts`: `const KindMountProblem = "mount_problem"`; `type MountProblem struct { Stack, Service, Container, Code, Target, Subject, Message string }` (`Subject` = finding id); `type MountProblems interface { Problems() []MountProblem }`; `func (e *Engine) SetMountProblems(src MountProblems)`; `Evaluate` gains a `problems []MountProblem` parameter.

- [ ] **Step 1: Write the failing tests.** `TestOpsOnEndAndRunning` (hook called once with the final `Operation` after the state is visible to `Get`; `Running` true during, false after). `TestScanner_Units` (registered stack → one stack unit; unregistered compose project and plain container → container units; containers labelled `dev.stackstead.helper` skipped; a removed container's problems gone after the next full pass). `TestScanner_Triggers` (fake clock: first pass no earlier than 60 s; interval with jitter within ±10 %; `Trigger` and a `start` event each run that unit once after 5 s even when fired five times; a full pass does not start while one runs). `TestScanner_Cost` (unchanged stack → 0 `Model` calls on the second pass; `/top` once per container id + `StartedAt`; per-unit timeout 10 s cuts a hanging unit and the pass continues; a budget of 120 s with 300 slow units → the rest run first next pass (round-robin); caps roll over with one Warn; disconnected → nothing; `OpsRunning` → stack unit skipped; unavailable stack → skipped and its problems dropped; `RunHelper`/`PullImage` never called — assert zero on the fake). `TestScanner_ProblemsExcludeLowSpaceAndNonDanger`. `BenchmarkScannerSteadyState` (100 containers / 600 mounts: second pass makes 0 `Model`, 0 `ContainerTop`, 1 `Containers` call). In `alerts`: `TestMountProblemAlerts` (problems → `mount_problem` danger alerts; stack problems carry `Stack`+`Service`, container problems only `Container`; `Since` stable across computations; nil source → none; message e.g. `media/jellyfin: /config is not writable by uid 1000`).
- [ ] **Step 2: Run** `go test -race ./internal/mounts/ ./internal/stacks/ ./internal/alerts/` — FAIL: `undefined: NewScanner`, `OnEnd`, `KindMountProblem`.
- [ ] **Step 3: Implement.** Scanner state: `map[UnitKey][]Finding` plus `map[UnitKey]time.Time`; the stack unit calls `Collector.Stack(ctx, name, CollectOptions{})`, the container unit `Collector.Container`. Hooks in `Ops.finish` run after the mutex is released.
- [ ] **Step 4: Run** the three packages with `-race`, plus `go test -bench BenchmarkScannerSteadyState -run ^$ ./internal/mounts/` — `ok`, bench prints the counters.
- [ ] **Step 5: Commit** `feat(mounts,alerts): background mount scan feeding mount_problem alerts` (why: problems in containers nobody has opened must reach the dashboard and notifications within one interval).

---

### Task 13: Read API routes, DTOs and `serve.go` wiring

Spec §7 preamble, §7.1–§7.4, §7.8; §12 `api` tests.

**Files:**
- Create: `internal/api/mounts.go`
- Modify: `internal/api/server.go:18-74` (`Mounts MountsAPI` option), `:138-143` (register when `Stacks`, `Observe`, `ContainerEngine` and `Mounts` are set), `internal/apitypes/types.go` (after `ContainerDetail`, `:560`), `internal/cli/serve.go:136-199` (build `mounts.NewCollector`, `mounts.NewScanner`, `alertsEngine.SetMountProblems`, `dockerEngine.RemoveHelpers` at startup with a Warn on error), `:170` (add `scanner.Run` to `startBackground`), `:208-235` (`Mounts: collector`)
- Test: `internal/api/mounts_test.go` (`fakeMounts`), `internal/cli/serve_test.go` (wiring compiles; existing style)

**Interfaces:**
- Produces: `type MountsAPI interface { StackReport(ctx, name string, refresh bool) (mounts.Report, error); ContainerReport(ctx, id string) (mounts.Report, error); DeepCheck(ctx, name string) (mounts.Report, error); PullHelperImage(ctx) (stacks.Operation, error) }` — `*mounts.Collector` satisfies it via thin adapters (`StackReport` calls `Stack(ctx, name, CollectOptions{Refresh: refresh})`). Routes: `GET /api/stacks/{name}/mounts`, `POST /api/stacks/{name}/mounts/deep-check`, `POST /api/helper-image/pull`, `GET /api/containers/{id}/mounts`. DTOs: `MountReport{Stack, GeneratedAt, Host MountHost, Services []MountService, Findings []MountFinding, DeepCheckedAt *time.Time}`, `MountHost{OS, PathChecks string; SELinux, AppArmor bool; IDMapping MountIDMapping; HelperImage string}`, `MountIDMapping{Mode, Status, User, Reason}`, `MountService{Name string; Containers []string; User MountUser; Mounts []MountEntry}`, `MountUser{UID, GID, HostUID, HostGID *uint32; Groups, HostGroups []uint32; Source string}`, `MountEntry{Kind, Target, Source string; ReadOnly, Declared, Runtime bool; SELinuxOption, Propagation string; Host *MountHostInfo; Access MountAccess; Volume *MountVolume; Tmpfs *MountTmpfs; FindingIDs []string}`, `MountHostInfo{Exists bool; Type string; UID, GID uint32; Owner, Group, Mode string; Empty, Readable, ACL bool; Label string; FS *MountFS}`, `MountFS{Free, Total uint64; ReadOnly bool}`, `MountAccess{Read, Write string}`, `MountVolume`, `MountTmpfs`, `MountFinding{ID, Code, Severity, Service, Container, Target, Source, Message string; Evidence map[string]any; Remedy MountRemedy}`, `MountRemedy{Text, Command string; Fixes []string}`, `ContainerMountReport{MountService; Host MountHost; Findings []MountFinding}`; JSON keys exactly as the §7.1 example (`generatedAt`, `pathChecks`, `idMapping`, `helperImage`, `readOnly`, `findingIds`, `deepCheckedAt`). Error mapping: `writeStackError` for stack errors (404/409/503), `ErrHelperImageAbsent` → 409 `{error, code: "helper_image_absent"}`, `ErrHelperFailed` → 502, container id handling as `inspectContainer` (`containers.go:233`).

- [ ] **Step 1: Write the failing tests.** `TestMountRoutesRequireSessionAndJSON` (401 without a cookie on all four; 415/400 without `Content-Type: application/json` on the POSTs, as the existing storage test checks), `TestStackMountsReportShape` (golden JSON compared field by field with the §7.1 example; arrays never `null`), `TestStackMountsRefreshQuery` (`?refresh=1` passes `refresh=true` to the fake), `TestStackMountsErrors` (404 unknown stack, 409 parse error with the detail text, 503 `engine.ErrUnavailable`), `TestDeepCheckStatuses` (200 report with `deepCheckedAt`; 409 `helper_image_absent`; 502 helper failure text), `TestHelperImagePullReturnsOperation` (202, `action == "pull-helper"`, `stack == ""`), `TestContainerMountsRoute` (shape; bad id → 404), `TestMountRoutesIgnoreClientPath` (a request body `{"path": "/etc"}` on deep-check is accepted and the fake receives no path), `TestMountRoutesNotRegisteredWithoutSources` (`Options.Mounts == nil` → 404 JSON), `TestNoResponseContainsRawUser` (fake report built from an inspect with `User "appuser"` → the string never appears in any body).
- [ ] **Step 2: Run** `go test -race ./internal/api/ -run 'Mount|DeepCheck|HelperImage'` — FAIL: `unknown field Mounts in api.Options`.
- [ ] **Step 3: Implement** handlers, DTO conversion in `mounts.go` (`toMountReport(mounts.Report) apitypes.MountReport`), wiring in `serve.go` (collector config from `cfg`: `SocketPath: cfg.DockerSocket`, `RemapUser: cfg.UsernsRemapUser`, `AllowedRoots`, thresholds from the four keys, `Mapper` = `UnavailableMapper` when `mounts.InContainer(...)`, `Interval: cfg.MountScanInterval`).
- [ ] **Step 4: Run** `go test -race ./internal/api/ ./internal/cli/`, `go vet ./...`, `gofmt -l .` — clean.
- [ ] **Step 5: Commit** `feat(api): mount diagnostics read routes and server wiring` (why: the UI and a later CLI share one report shape; routes exist only when every source is wired, like the container routes).

---

### Task 14: Web — types, client, `storage.ts` helpers and the Storage tab

Spec §10.1, §10.6, §12 web.

**Files:**
- Create: `web/src/lib/stacks/storage.ts`, `web/src/lib/stacks/storage.test.ts`, `web/src/lib/stacks/StoragePanel.svelte`, `web/src/lib/stacks/StoragePanel.test.ts`
- Modify: `web/src/lib/api/types.ts` (after `ContainerMount`, `:63-70`: `MountReport`, `MountHost`, `MountService`, `MountUser`, `MountEntry`, `MountHostInfo`, `MountAccess`, `MountVolume`, `MountTmpfs`, `MountFinding`, `MountRemedy`, `ContainerMountReport`, `Severity = 'danger' | 'warning' | 'info'`, `Verdict = 'yes' | 'no' | 'unknown'`), `web/src/lib/api/client.ts:168-171` (`stackMounts(name, refresh?)`, `deepCheck(name)`, `pullHelperImage()`, `containerMounts(id)`), `web/src/routes/StackDetail.svelte:38-45` (`TabId` gains `'storage'`, tab `{ id: 'storage', label: 'Storage' }` after Services), `:64-72` (badge = danger + warning count from the last report the panel reported up via an `onfindings` callback, hidden in parse-error mode like Services), `:495-586` (render `StoragePanel` for `tab === 'storage'`), `web/src/routes/StackDetail.test.ts:106-116` (the tab list now has six entries)

**Interfaces:**
- Produces (`storage.ts`): `mountErrorHint(lines: string[]): boolean` (patterns §10.4: `not a directory`, `mount a directory onto a file`, `permission denied` together with `mount`, `no such file or directory` together with `bind source`, `external volume … not found`, case-insensitive), `severityRank(s: Severity): number`, `countActionable(findings: MountFinding[]): number` (danger + warning), `accessLabel(v: Verdict | ''): string` (`yes` → "yes", `no` → "no", `unknown` → "unknown", `''` → "—"), `spaceTone(free, total, thresholds?)`, `findingsByTarget(report): Map<string, MountFinding[]>`, `hostSummary(h: MountHostInfo | null): string` (`dir · root:root · 0755`). `StoragePanel` props: `{ name: string; onfindings?: (count: number) => void; onopenoperation?: (id: string) => void }`; it fetches on mount, on `changes` for the stack's project (debounced via `$lib/stores/debounce`), after Deep check and after pull; header buttons **Refresh** (`refresh=1`) and **Deep check** (only when a `not_inspectable` finding exists); pull consent uses `ConfirmDialog` with the §10.1 copy, then renders `OperationPanel` for the pull operation, then runs the Deep check; banners for `pathChecks === 'unavailable'` (fixed copy from §4.6: "Stackstead runs in a container; host paths are not visible at the same locations") and `os === 'darwin'` ("ownership verdicts are unknown behind the file-sharing layer"); findings list with `<details>` evidence and `CopyButton` for `remedy.command`; per-service table (Target, Kind, Source, Host, Access, Space, Flags) that becomes cards under 720 px; a `data-finding` anchor per finding so table markers can link up. The **Fix…** button and fix history are Plan 14; render nothing for them here.

- [ ] **Step 1: Write the failing tests.** `storage.test.ts`: `mountErrorHint` true for each pattern and false for `pulling image`, `permission denied` alone; `countActionable`; `accessLabel`; `hostSummary`. `StoragePanel.test.ts` (with `installFakeFetch`): renders one section per service with the user chip `uid 1000:1000 · from process` and `unknown`; table rows show type/owner/mode, `r yes` / `w no` as text, free/total; findings ordered by severity with text labels and a Copy button whose text is the command; `onfindings(2)` for one danger and one warning and an info; Deep check button absent without `not_inspectable`, present with it; clicking it with `helperImage: 'absent'` opens the consent dialog, confirming posts `/api/helper-image/pull`, shows the operation panel, and after the operation finishes posts `…/deep-check` and re-renders with `deepCheckedAt`; `helperImage: 'present'` posts deep-check directly; a 409 `helper_image_absent` from deep-check also opens the consent; Refresh posts with `?refresh=1`; the unavailable banner and the macOS banner; a `change` for the project refetches once per burst; 409 parse error shows the message; narrow viewport (matchMedia mocked) renders cards. `StackDetail.test.ts`: six tabs with `Storage` second; badge from `onfindings`; hidden in parse-error mode; `?tab=storage` mounts the panel.
- [ ] **Step 2: Run** `cd web && npm run test -- storage StoragePanel StackDetail` — FAIL: module not found / tab list mismatch.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make ui-test` and `make ui-check` — all pass, 0 errors.
- [ ] **Step 5: Commit** `feat(web): Storage tab with mount findings, access verdicts and Deep check` (why: the operator should not need `ls -ln`, `stat` and `df` over SSH).

---

### Task 15: Web — container Mounts badges, operation hint, alert links

Spec §10.3, §10.4, §10.5.

**Files:**
- Create: `web/src/lib/containers/MountBadges.svelte`, `web/src/lib/containers/MountBadges.test.ts`
- Modify: `web/src/routes/ContainerDetail.svelte:49-63` (lazy `containerMounts(id)` when the Details tab is shown, same `seq` guard), `:211-214` (each mount line gets `<MountBadges>` for its destination and a **Diagnose** link: `/stacks/<stack>?tab=storage` when `d.managed === 'stack'`, else an inline toggle that lists that mount's findings with copyable commands), `web/src/lib/stacks/OperationPanel.svelte:9-19` (new optional prop `storageHref?: string`), `:99-117` (when `$op.action === 'up'`, state `failed` and `mountErrorHint($lines.map(l => l.text))` → a **Diagnose mounts** link to `storageHref`), `web/src/routes/StackDetail.svelte:577-594` (pass `storageHref={stackUrl({ tab: 'storage' })}`), `web/src/lib/dashboard/AlertsCard.svelte:22-23` (`kind === 'mount_problem' && a.stack` → `/stacks/<stack>?tab=storage`; container-only alerts keep `/containers/<name>`)
- Test: `web/src/routes/ContainerDetail.test.ts`, `web/src/lib/stacks/OperationPanel.test.ts`, `web/src/lib/dashboard/dashboard.test.ts`

- [ ] **Step 1: Write the failing tests.** `MountBadges`: renders one badge per finding severity with text (`danger`), none when empty. `ContainerDetail`: Details tab fetches `/api/containers/<id>/mounts` once and not on the Logs tab; a finding on `/config` shows its badge on that line only; `managed: 'stack'` → Diagnose is a link to `/stacks/web?tab=storage`; `managed: 'none'` → Diagnose expands the inline list with the command and a Copy button; a 503 from the mounts route leaves the mount lines intact without badges. `OperationPanel`: failed `up` with a line `mount a directory onto a file` → link with the given href; succeeded `up` or failed `pull` → no link; failed `up` without matching lines → no link. `dashboard.test.ts`: `mount_problem` with stack → href ends in `?tab=storage`; `mount_problem` with container only → `/containers/<name>`; other kinds unchanged.
- [ ] **Step 2: Run** `cd web && npm run test -- MountBadges ContainerDetail OperationPanel dashboard` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make ui-test`, `make ui-check` — pass.
- [ ] **Step 5: Commit** `feat(web): mount badges on container pages, Diagnose links and failed-Up hint` (why: the finding should meet the operator where they already look — the failed operation, the container page, the alert).

---

### Task 16: `make helper-image-update` and documentation

Spec §8.2 (release step), §13.

**Files:**
- Modify: `Makefile` (`.PHONY` list and a new target after `e2e`: `HELPER_BUSYBOX_VERSION ?= 1.37.0`; `helper-image-update` runs `docker buildx imagetools inspect busybox:$(HELPER_BUSYBOX_VERSION) --format '{{json .Manifest.Digest}}'` and rewrites the `HelperImage` constant in `internal/mounts/helper_image.go` with `go run ./tools/helperimage -version … -digest …`; fails clearly without Docker), create `tools/helperimage/main.go` (tiny rewriter; `-check` mode exits 1 when the constant does not match the given digest) with `tools/helperimage/main_test.go`
- Docs: `docs/api.md:138-160` (new section **Mounts** after Storage: four routes, report fields, `refresh`, 409/502 codes; `:321-345` alert table gains `mount_problem`; operations list gains `pull-helper`), `docs/security.md:87` (new section **Mount diagnostics and fixes** before Storage cleanup: §11 points — metadata-only reads, no client paths, helper runs as root with the listed capabilities only from Deep check and fixes, Deep check can reveal ownership of named-volume roots, `/etc/subuid` only parsed; the fixes paragraph is completed by Plan 14), `docs/development.md:18-28` (target table) and a **Release** subsection: "Run `make helper-image-update`, commit the digest bump (`chore: refresh helper image digest`)", `README.md:66-107` (feature line "Storage diagnostics: missing bind mounts, ownership and permission problems, SELinux labels, low disk, with guarded fixes") and `docs/install.md:83-103` or `:103` (configuration keys §6.4 and the rootless/userns note: set `userns_remap_user` when the daemon uses a non-default remap user).

- [ ] **Step 1: Write the failing test** `TestHelperImageRewrite` in `tools/helperimage/main_test.go` (rewrites a temp copy of the Go file; `-check` fails on mismatch, passes on match).
- [ ] **Step 2: Run** `go test ./tools/helperimage/` — FAIL: no such package.
- [ ] **Step 3: Implement** the tool and the Makefile target; run `make helper-image-update` once and verify `internal/mounts/helper_image.go` now carries the resolved digest (the Task 11 test `TestHelperImagePinned` passes); write the docs.
- [ ] **Step 4: Run** `go test ./tools/... ./internal/mounts/`, `make help` shows the target, `go vet ./...`.
- [ ] **Step 5: Commit** in two commits: `chore: add make helper-image-update and pin the busybox helper digest` and `docs: mount diagnostics routes, security notes, config keys and release step`.

---

### Task 17: Integration tests, E2E smoke and visual check

Spec §12 Integration, E2E, Visual.

**Files:**
- Create: `internal/mounts/integration_test.go` (`//go:build integration`, Linux only, real Docker through `DOCKER_HOST` or the default socket; temp dir under `t.TempDir()` added to `AllowedRoots`), `internal/mounts/integration_rootless_test.go` (`//go:build integration && rootless`)
- Modify: `cmd/stackstead/e2e_test.go:204-470` (new subtests after `containerEndpoints`), `:491-530` helpers reused; `docs/development.md:63-66` (the rootless job line)

- [ ] **Step 1: Write the failing integration tests.** `TestIntegration_ReportAfterUp`: compose with (a) `./missing:/data`, (b) `./app.conf:/etc/app.conf` where `app.conf` does not exist, (c) `user: "1234"` with a dir created by the test user and `chmod 0555` (the owner is not 1234, so `w no`); before `up`: `bind_source_missing` and `file_dir_mismatch` (target extension); after `docker compose up -d`: `bind_autocreated` (or no finding when the image runs as root), `file_dir_mismatch` from `StateError`, `not_writable` with `User.Source == "process"` and `UID == 1234`. `TestIntegration_DeepCheckVolumeRoot`: a named volume → after `DeepCheck`, the volume entry's `Host` is readable with owner `0:0`. `TestIntegration_PullHelperImage` (skipped offline). `TestIntegration_RootlessIDs` (rootless daemon: container `user: "1000"` writing to a bind dir owned by the mapped host id → `Write == "yes"`; `/top` euid is container-side, pinning §4.7; this pins the assumption named in §15).
- [ ] **Step 2: Run** `go test -tags integration -race -run TestIntegration_ -v ./internal/mounts/` — FAIL (no tests yet compile? they compile but fail on the missing wiring pieces found while running against Docker); fix whatever the real daemon disagrees with (notably the `PullImage` query form from Task 3).
- [ ] **Step 3: Extend the E2E smoke** (`TestE2ESmoke`): a stack whose compose has a bind to `<stacksdir>/<name>/missing` → `GET …/mounts` lists `bind_source_missing`; after `up` → `bind_autocreated` or none; a stack with `volumes: {ext: {external: true}}` that was never created, where another service of the same stack starts fine: the container `start` event triggers the stack unit within 5 s, so poll `GET /api/alerts` for a `mount_problem` with code `volume_missing_external` for up to 20 s (no scan-trigger route exists and the interval is too slow for a smoke test). Plain container: `docker run -d -u 1234 -v <root-owned 0755 temp dir>:/data --label io.stackstead.e2e=1 busybox sleep 300` → container-only `mount_problem` with `stack == ""`. Visual: screenshots of `/stacks/<name>?tab=storage` at 1280×800 and 375×812, dark and light, using the existing `findChrome` flow.
- [ ] **Step 4: Run** `make e2e` with Docker — PASS (skips cleanly without Docker or Chrome); `go test -tags integration -race ./internal/mounts/` — `ok`.
- [ ] **Step 5: Commit** `test: integration and e2e coverage for mount diagnostics` (why: the verdict logic depends on real daemon behaviour — `/top` id namespaces, autocreated directory ownership — that fakes cannot prove).

---

## Self-review notes

- Spec coverage: §1–§2 (Tasks 8–15), §3 every row has a task; §4.1 T1/T2/T7, §4.2–§4.3 T8–T9, §4.4 T11, §4.5–§4.6 T7/T9/T10, §4.7 T5–T6; §5 T8–T9; §6 T12 (+T4 config); §7.1–§7.4, §7.8 T13; §7.5–§7.7 and §8.1/§8.3–§8.5 are Plan 14; §8.2 T3/T11/T16; §9 T5–T12; §10.1 T14, §10.2 Plan 14, §10.3–§10.6 T15/T14; §11 T2 (no `User` in DTOs), T7, T10, T13 (no client path), T16 docs; §12 all tasks, integration/e2e T17; §13 T16; §14 this split; §15 pinned by T17's rootless job.
- Existing tests change only additively (inspect fixture fields) plus the StackDetail "five tabs" assertion, which the spec's acceptance line did not foresee; noted in Task 14.
- Names used across tasks: `Report`, `Finding`, `MountEntry`, `EffectiveUser`, `IDMapping`, `HostFS`, `HostProbe`, `Collector.Stack/Container/DeepCheck/PullHelperImage/Invalidate`, `Scanner.Run/Trigger/Problems`, `alerts.MountProblem`, `Ops.OnEnd/Running`, `engine.HelperSpec/RunHelper/PullImage/ImagePresent`, `composecli.Model/FileSetHash`. Plan 14 uses exactly these.
