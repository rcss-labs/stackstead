# Stackstead Volume and mount diagnostics — Design

**Date:** 2026-10-18
**Status:** Draft, awaiting review. Adds mount diagnostics: for every bind mount, named volume and tmpfs of a stack's services (and of any container), show what is mounted where, flag the mistakes that make containers fail with "permission denied", "not a directory" or silently empty data, and offer guarded fixes (mkdir, chown, chmod). Implemented by Plan 13 (`docs/superpowers/plans/2026-10-19-stackstead-13-volume-diagnostics.md`, diagnostics) and Plan 14 (`docs/superpowers/plans/2026-10-19-stackstead-14-volume-fixes.md`, fixes, depends on Plan 13). The storage inventory spec this document cites has shipped (storage screen, `action_log`, `engine.InspectVolume`/`InspectImage`); the plans list where its references diverge from the shipped code. Builds on `2026-10-14-stackstead-v1-design.md`, `2026-10-15-stackstead-containers-design.md` and `2026-10-15-stackstead-networks-multiservice-design.md`.

## 1. Purpose

Mount problems are the most common reason a homelab container will not start or cannot write its data. Today Stackstead shows only the raw mount list on the container page (`type source → destination (ro)`, `ContainerDetail.svelte`) and the short-syntax `volumes` strings in the service form; the operator still has to SSH in and run `ls -ln`, `stat`, `id` and `df`. Typical cases:

1. A bind source did not exist: Docker created it as an empty `root:root` directory, the app (running as uid 1000) cannot write to it, and the data the operator meant to mount is elsewhere.
2. A file mount (`./nginx.conf:/etc/nginx/nginx.conf`) whose source was missing: Docker created a **directory** `nginx.conf` and the container fails with "are you trying to mount a directory onto a file".
3. The directory exists but belongs to another uid, or is `0755` while the container runs as non-root: "permission denied" at runtime.
4. A volume declared `external: true` does not exist, so `up` fails.
5. The compose file was edited but the container still runs with the old mounts.
6. On SELinux hosts, a bind mount without `:z` is unreadable even with mode `0777`.
7. The filesystem behind a data mount is nearly full.

Success criteria:

- The stack's **Storage** tab lists every mount of every service with host path type, owner, group, mode, read-only flag, free space and the effective uid/gid of the container process, without a shell on the host.
- Cases 1–7 each produce a finding with a severity, a one-sentence explanation, the evidence, and the next step: a fix button where §8 allows it, otherwise a copy-paste command.
- Diagnostics work for a stack that has never been started (declared mounts only) and for any container on the host (runtime mounts only).
- Danger findings (except `low_space`, §6.3) of every container on the host (registered stacks, unregistered compose projects and plain containers) appear in `GET /api/alerts` as `mount_problem` within one scan interval, without anyone opening a page.
- Diagnostics never return file contents and never accept a host path from the client; fixes never act outside `allowed_roots` and always show the exact command and require typed confirmation.
- A report for a 10-service stack (no Deep check) is computed in under 1 s on a Raspberry Pi 4; a full background scan of 20 stacks plus 80 other containers stays within its 120 s budget, an unchanged host is rescanned in under 10 s, and the scan never runs a container.

## 2. Scope

### In
- Backend: new package `internal/mounts` (collect, analyse, scan, fix planning); `composecli.Runner.Model`; `engine` additions (inspect fields, volume and image inspect, container top, Info security options, helper container lifecycle, image pull); routes in §7; `mount_problem` alert kind; `fix` operation kind; `mount.fix` rows in the shared `action_log` table (storage inventory spec); config keys in §6.4.
- Frontend: Storage tab on stack detail; container page Mounts badges and Diagnose link; Deep check; fix dialog with command preview and typed confirmation; fix history on the Storage tab; "Diagnose mounts" link on failed Up operations.
- Rootless and userns-remap id mapping through `/etc/subuid` / `/etc/subgid` (§4.7); `make helper-image-update` and its release step.
- Tests, E2E smoke, docs (`docs/api.md`, `docs/security.md`, `docs/development.md`, README configuration section and feature line).

### Out
- A host-wide volumes inventory screen, volume sizes, volume prune/remove: the sibling images/storage inventory spec owns them. That screen may link to a stack's Storage tab.
- Path translation when Stackstead itself runs in a container (detected and reported, §4.6).
- Editing mounts from the Storage tab (adding `:z`, `create_host_path`, long syntax): the richer compose forms spec; findings link to the service editor or the Compose tab.
- Fixes for named-volume contents, NFS/CIFS or plugin volumes, files outside `allowed_roots`, ACL editing, SELinux relabelling (`chcon`): copy-paste commands only.
- Running the helper container from the background scan.
- CLI commands (a later `stackstead stacks check` can call the same route).

## 3. Key decisions

The TypeSafe skill named in the global rules cannot run here (the installed `typesafe-ai` skill is an AI-feature SDK, not a decision scorer), so decisions are argued in writing. Those marked *user* were put to the user with options and answered; the rest follow from existing code and conventions.

| Decision | Choice | Reason |
|---|---|---|
| Declared mounts source | `docker compose config --format json` (normalised), not `composefile` | `composefile.ServiceConfig.Volumes` is short syntax only (long syntax makes the field read-only, `read.go`), does no `${VAR}` interpolation and does not resolve `./data` against the project dir. Compose's normalised model is exactly what Docker will be asked to mount, and Stackstead already runs this command (`Runner.ProjectName`). |
| Runtime mounts source | Docker inspect (existing `InspectContainer`, extended) | Same engine path as the Containers screen; no CLI. |
| Unreadable paths | Report "not readable by Stackstead"; explicit **Deep check** runs a pinned busybox helper as root (*user*) | Stackstead runs as the non-root `stackstead` user with an empty capability set, so `/var/lib/docker/volumes` and root-only dirs are invisible to it. Full coverage only when asked; Docker access is already root-equivalent and documented. |
| Fixes | mkdir, chown, chmod through the root helper, targets inside `allowed_roots` only, exact command preview, typed confirmation (*user*) | Covers the real fixes (cases 1–3) without SSH. The safety model is §8. |
| Recursive chown/chmod | Opt-in only; counted, capped walk (100 000 entries); `-h`/`-P`, `-xdev`; count in the typed phrase (*user*) | Fixes "restored backup owned by root" while keeping the blast radius visible. |
| Recording fixes | A `fix` operation (live output, cancel, per-stack lock) **and** a persistent `mount.fix` row in the shared `action_log` table defined by the storage inventory spec (*user*) | Operations are in memory only (last 20 per stack, `stacks/ops.go`); the action log survives restarts, and one shared table instead of a per-feature log keeps a single audit history across storage actions. The per-stack lock means a fix never runs during an Up. |
| Helper image | `busybox` pinned by digest in code; pulled only after the operator agrees (*user*) | No silent download; offline hosts get the `docker pull` command. |
| Which host paths may be stat-ed | Any bind source declared by a registered stack's compose or reported by a container's inspect; server-derived, metadata only (*user*) | Real bind mounts live in `/srv`, `/mnt`, `/home`; restricting reads to `allowed_roots` would leave most unchecked. Clients never supply a path. |
| Container user | Running: process uid/gid from `GET /containers/{id}/top`; otherwise numeric `Config.User`, else the image's `User`; names → unknown (*user*) | `/top` sees privilege drops (gosu, LinuxServer `PUID`). Only numeric ids are exposed, never the raw `User` string (containers spec keeps it out of details). |
| Rootless and userns-remap ids | Map container ids to host ids through `/etc/subuid` / `/etc/subgid` of the daemon's user, so verdicts are exact; daemon user from the socket owner (§4.7) (*user*) | Without mapping every rootless verdict would be wrong or `unknown`; the files are world-readable on standard distributions and parsing them is a few lines. |
| UI placement | Storage tab on stack detail; badges + Diagnose link on the container page Mounts list (*user*) | Reuses `Tabs`/`?tab=`; host-wide list belongs to the inventory spec. |
| When checks run | On demand, plus a periodic unprivileged scan feeding `mount_problem` alerts; never the helper automatically (*user*) | Problems show on the dashboard and reach notifications. Scan cost bounds are §6. |
| Scan scope | Every container on the host: registered stacks (declared + runtime), unregistered compose projects and plain containers (runtime only) (*user*) | Problems in containers started outside Stackstead are just as common; alerts already support a container without a stack. |
| Scan interval and cost | `mount_scan_interval` default `15m` (0 disables), plus a targeted scan 5 s after a stack operation finishes or a container starts; incremental (unchanged units reuse cached engine results), concurrency 2, 120 s budget, caps in §6.2 | Mounts change only when files or containers change. Caching by compose hash and by container id + start time keeps a steady-state rescan to stat calls only, which a Pi handles for hundreds of mounts. |
| SELinux/AppArmor | Read daemon `SecurityOptions`; with SELinux, read each bind source's `security.selinux` xattr and warn when not a container type and no `z`/`Z`; AppArmor: show profile only (*user*) | Catches the Fedora/RHEL "denied despite 0777" with one cheap xattr read. |
| Stackstead in a container | Out of scope: detect, mark host-path checks unavailable, identity `PathMapper` seam (*user*) | v1 deploys natively ("no path-mapping issues"); wrong verdicts are worse than none. |
| Low-space thresholds | Configurable: warning `10%` / `2GiB`, danger `3%` / `500MiB` (*user*) | Big data disks and SD cards need different numbers. |
| Low space and alerts | `low_space` findings appear on the Storage tab and the dashboard but are never raised as `mount_problem` alerts; phone alerts for low disk come only from the notifications spec's `disk_low` (*user*) | Avoids two notifications for the same full disk. |
| Report caching | 10 s per stack (single flight); Refresh bypasses | Same flight-cache pattern as `/api/containers` (`flightcache.go`). |

## 4. What is inspected

### 4.1 Sources

| Source | How | Gives |
|---|---|---|
| Declared mounts | new `composecli.Runner.Model(ctx, inv Invocation) (ComposeModel, error)` running `config --format json` on the stack's compose file set (`Invocation`, compose-forms §7.2) through the existing `capture` | Per service: `volumes[]` in long form (`type` bind/volume/tmpfs/npipe/image, `source` already absolute, `target`, `read_only`, `bind.create_host_path`, `bind.selinux`, `bind.propagation`, `volume.nocopy`, `volume.subpath`, `tmpfs.size`, `tmpfs.mode`), `tmpfs:` shorthand, `user`, `group_add`, `read_only` (rootfs). Top level `volumes` (`name`, `external`, `driver`, `driver_opts`). Only these fields are decoded; the rest (notably interpolated `environment`) is discarded during decoding, never logged or returned. |
| Runtime mounts | `engine.InspectContainer` gains `Mount.Name`, `Driver`, `Mode`, `Propagation`; `ContainerInspect.StateError` (`State.Error`), `Created`, `GroupAdd`, and an internal `User` | What the container actually has, and Docker's start error text. `User` is used only by `internal/mounts` and is not added to the container details DTO. |
| Volumes | `engine.InspectVolume(ctx, name) (VolumeInspect, error)`, defined by the storage inventory spec §8 | `Driver`, `Mountpoint`, `Labels` (compose project/volume), `OptionType` (the driver's `type` option only), `CreatedAt`; `ErrVolumeNotFound` → missing. |
| Image user | `engine.InspectImage(ctx, ref) (ImageInspect, error)`, defined by the storage inventory spec §8; only `Config.User` is read here | Default user when the service sets none. |
| Process user | new `engine.ContainerTop(ctx, id, psArgs) ([]Process, error)` with `ps_args=-o pid,euid,egid,supgid,comm` | Effective uid, gid and supplementary groups of each process, host view. The check uses the processes other than an init (`tini`, `dumb-init`, `s6-svscan`) when present, else PID 1; several distinct uids → each is evaluated, verdict is the most permissive. `ps` without these columns (busybox hosts) → fall back as §3. |
| Daemon facts | `engine.Info` gains `SecurityOptions`, `DockerRootDir`, `OperatingSystem` | `selinux`, `apparmor`, `rootless`, `userns` flags. |
| Host paths | new `mounts.HostFS` interface (real implementation on `os` and `golang.org/x/sys/unix`, promoted from indirect to direct dependency) | See §4.3. |

Declared and runtime are joined by service + target. Declared answers "what was asked"; runtime answers "what the container has".

### 4.2 Per mount kind

- **bind**: host checks (§4.3) on the source, read-only flag, SELinux option, propagation.
- **volume** (named): existence, driver (`local`, or `local` with `driver_opts.type` nfs/cifs, or a plugin), owning project label vs this stack, `external`. Owner and mode of the volume's root directory only with Deep check (the mountpoint is under `DockerRootDir`, root-only). Anonymous volumes are listed with an info finding.
- **tmpfs**: target, size, mode; no host checks.
- **npipe / image / cluster**: listed, not checked.

### 4.3 Host path checks (bind sources)

Read as the `stackstead` user, metadata only:

1. `Lstat`: exists; symlink (then `Stat` the target, show both).
2. Type: `dir`, `file`, `socket`, `other`.
3. Owner `uid:gid` with names (`os/user`, best effort), mode octal and symbolic.
4. ACL presence (`system.posix_acl_access` xattr exists) → write verdicts become `unknown`.
5. Empty-directory flag (`ReadDir` of at most 1 entry), and `ctime`.
6. Filesystem free/total bytes and `ro`/`noexec` flags (`Statfs`), deduplicated per device id.
7. SELinux label (`security.selinux` xattr) when the daemon reports SELinux.
8. Each ancestor's `x` bit for the effective uid/gid (traversal), stopping at `/`.

A permission error at any step yields `readable: false` for that step and the report says "not readable by Stackstead — run Deep check". The reader never opens regular files.

**Access verdict** for the effective user (`uid`, `gid`, supplementary groups from `/top` or `group_add`): standard POSIX owner/group/other evaluation for `r`, `w` (rw mounts only) and `x` on directories. `uid 0` → `yes` (except SELinux/read-only fs). The evaluation always uses **host** ids: on rootless and userns-remap daemons the container ids are first mapped (§4.7). Result `yes | no | unknown`; `unknown` when the uid is unknown, ACLs exist, the id mapping is unavailable or the id is outside the mapped range (§4.7), or on macOS.

### 4.4 Deep check

On request only (`POST .../mounts/deep-check`). One helper container (§8.2) per request mounts each unreadable bind source and each local named volume's mountpoint read-only, under `/t/<n>`, with only `CAP_DAC_READ_SEARCH` added, and runs a single `stat -c '%F|%u|%g|%a|%W|%Z'` over them plus `find -maxdepth 0 -empty`. Results replace the "not readable" entries in the next report for 10 minutes (kept in memory with the report cache). Timeout 15 s.

### 4.5 Platforms

- **Linux native** (the server target): everything above.
- **macOS** (LaunchAgent + Docker Desktop/OrbStack/Colima): bind sources are checked for existence and type; ownership verdicts are always `unknown` because the file-sharing layer remaps ownership; named-volume roots and Deep check of volumes are "inside the Docker VM — not inspectable from this host". Fixes are disabled on macOS (chown through the VM's sharing layer does not do what the operator expects).

### 4.6 Stackstead running in a container

Detected at startup (`/.dockerenv` exists, or `/proc/1/cgroup` names a container runtime). Then `report.host.pathChecks = "unavailable"` with reason "Stackstead runs in a container; host paths are not visible at the same locations", host-path findings are omitted, fixes are disabled, and volume, runtime-drift, start-error and tmpfs findings still work. All host path reads go through `PathMapper` (`ToLocal(hostPath string) (string, bool)`), identity today, so translation can be added later without touching the analysis.

### 4.7 ID mapping (rootless and userns-remap)

**Mode detection** (once at startup and after every Docker reconnect, from `engine.Info().SecurityOptions`):

| SecurityOptions | Mode |
|---|---|
| contains `name=rootless` | `rootless` |
| contains `name=userns` (rootful daemon with `userns-remap`) | `userns` |
| neither | `none` (identity) |

**Daemon user (rootless):** the owner uid of the Docker socket Stackstead is configured with (`docker_socket`, `Stat` of the resolved path). A rootless daemon creates its socket as its own user (`$XDG_RUNTIME_DIR/docker.sock`). Two cross-checks; if either fails the mapping is `unavailable` with a reason rather than a guess:
- the socket owner is not uid 0 (a root-owned socket with `name=rootless` means a proxy or bind-mounted socket; the real owner is unknown);
- when the socket path has the form `/run/user/<n>/…`, `<n>` equals the socket owner.

The user's name comes from `os/user.LookupId` (both name and uid forms are accepted in the files, as `newuidmap` does).

**Remap user (userns):** Docker's API does not report the `userns-remap` user. Stackstead uses `dockremap` (Docker's default for `userns-remap: default`) when that user exists, or the optional config key `userns_remap_user` when set; otherwise the mapping is `unavailable` ("set `userns_remap_user`").

**Reading the ranges:** `/etc/subuid` and `/etc/subgid`, lines `name-or-uid:start:count`. Every line for the user is used, concatenated in file order, as `newuidmap` and rootlesskit apply them. Malformed lines are skipped and counted (a Warn is logged once). Files are re-read when their mtime changes, at most once per report.

**Mapping a container id `c` to a host id:**
- `rootless`: `c = 0` → the daemon user's uid (gid: the user's primary gid); `1 ≤ c ≤ total range` → `start_i + (c − 1 − offset_i)` in the range that covers it; beyond the ranges → `unmapped`.
- `userns`: `0 ≤ c < total range` → `start_i + (c − offset_i)`; beyond → `unmapped`.
- Supplementary groups map the same way through `/etc/subgid`.

**Which ids need mapping:** `Config.User` and the image's `User` are always container ids. `/top` output depends on where the daemon runs `ps`. In `userns` mode the rootful daemon runs it in the host namespace, so the ids are already host ids and are not mapped. In `rootless` mode the daemon runs inside rootlesskit's namespace, so the ids are container-side ids and are mapped. The integration tests (§12) pin this behaviour, so a Docker change shows up as a failing test rather than wrong verdicts.

**When mapping is unavailable:** the files are missing or unreadable, the user has no entry, the daemon user cannot be determined, or the id is outside the ranges. Then verdicts that need the host id become `unknown`, and each affected service gets one `id_mapping_unavailable` info finding with the reason, e.g. "`/etc/subuid` has no entry for `alice`" or "uid 70000 is outside alice's mapped range; the container sees it as `nobody`". Nothing fails hard. `report.host.idMapping` carries `{mode, status: "exact" | "unavailable", user, reason}`.

**Effects elsewhere:**
- **Report.** `user` shows both id pairs: `{uid, gid, hostUid, hostGid, groups, hostGroups, source}`. Findings and copy-paste commands use host ids (`sudo chown 100999:100999 /srv/x`).
- **Fixes (rootless).** The helper's root is the daemon user on the host, so its `chown` takes **container** ids, and the kernel maps them. The preview shows both, the host command and the helper argv. The helper cannot change paths outside the daemon user's own ids and ranges, such as host-root-owned directories. The preview warns about this, and an `EPERM` fails the operation with "a rootless daemon cannot change ownership of paths owned by other host users; run the shown command with sudo". The same limit applies to Deep check, which cannot read such paths in rootless mode, and the report says so.
- **Fixes (userns).** The helper runs remapped too, unless the daemon allows `--userns=host` for it. Stackstead sets `UsernsMode: "host"` on the helper in `userns` mode so the fix runs with real root and uses host ids.

## 5. Findings

Each finding: `id` (stable hash of code + service + target), `code`, `severity` (`danger | warning | info`, the alert severities), `service`, `container` (optional), `target`, `source`, `message` (one sentence), `evidence` (observed values, `likely: true` for heuristics), `remedy` (`text`, optional `command` to copy, optional `fixes []FixKind`).

| Code | Severity | Condition | Remedy / fix |
|---|---|---|---|
| `bind_source_missing` | danger if `create_host_path: false`, else warning | Declared bind source does not exist | "Docker will create an empty root-owned directory." Fix: mkdir (+ chown to the effective user) |
| `bind_autocreated` | warning | Source is an empty dir owned `0:0`, `ctime` within 60 s of the container's `Created`, effective user not root | "Docker created this directory because it was missing; the app cannot write to it and your data is not here." Fix: chown |
| `file_dir_mismatch` | danger | `StateError` matches Docker's "not a directory" / "mount a directory onto a file"; or source is an empty dir whose name has a file extension (`.conf .yml .yaml .json .ini .toml .env .xml .txt .pem .key .crt .sh .lua .js`); or source missing and target has such an extension | "Docker created a directory where a file was expected." Command only (`rmdir` + create the file); no fix, because removing paths is out of the fix set |
| `not_writable` | danger when the container exited or restarted since start, else warning | rw mount, verdict `w = no` | Fix: chown to `uid:gid`, or chmod |
| `not_readable` | danger | verdict `r = no` (or `x = no` on a dir) | Fix: chown / chmod |
| `parent_not_traversable` | warning | An ancestor lacks `x` for the effective user | Fix: chmod `+x` on that ancestor (only if inside `allowed_roots`) |
| `world_writable` | info | Writable only through other bits | Fix: chmod to drop `o+w` |
| `symlink_source` | info | Source is a symlink (resolved at container create) | — |
| `volume_missing_external` | danger | `external: true` volume does not exist | Command `docker volume create <name>` |
| `volume_foreign_project` | info | Named volume labelled for another compose project | — |
| `volume_remote` | info | NFS/CIFS/plugin volume: owner and space checks skipped | — |
| `anonymous_volume` | info | Anonymous volume on a target | "Data is lost when the container is removed; name the volume." |
| `mount_drift` | warning | Declared and runtime differ (source, type, ro, missing or extra target) | "Changes not applied — Up." |
| `readonly_fs` | warning | rw bind on a filesystem mounted `ro` | — |
| `low_space` | warning / danger | Free below the §6.4 thresholds (percent **or** bytes, whichever trips first) | — |
| `selinux_label` | warning | SELinux on, bind without `z`/`Z`, label type not `container_file_t`/`svirt_sandbox_file_t` | Command: add `:z` to the mount |
| `tmpfs_unbounded` | info | tmpfs without `size` | — |
| `docker_socket` | info | Docker socket bind-mounted | "This container controls Docker (root-equivalent)." |
| `user_unknown` | info | Effective user could not be determined | — |
| `id_mapping_unavailable` | info | Rootless/userns daemon and the container ids cannot be mapped to host ids (§4.7) | The reason, e.g. "add `alice:100000:65536` to `/etc/subuid`" or "set `userns_remap_user`" |
| `not_inspectable` | info | Path unreadable by Stackstead and no Deep check result | Button: Deep check |

Ordering: severity, then service, then target.

## 6. Background scan

### 6.1 Behaviour

`mounts.Scanner` covers every container Docker reports, in **scan units**:

- **Stack unit**: one per registered stack; the same report as `GET /api/stacks/{name}/mounts` (declared + runtime).
- **Container unit**: one per container that is not in a registered stack (unregistered compose projects and plain containers); the same report as `GET /api/containers/{id}/mounts` (runtime only; no compose file is read for unregistered projects). Stackstead's own helper containers (label `dev.stackstead.helper`) are skipped.

Triggers: every `mount_scan_interval` (±10 % jitter) a full pass; 5 s after a stack operation finishes, a pass of that stack unit; 5 s after a Docker `start` event (from the existing events subscription), a pass of that container's unit (debounced per unit). Every pass is unprivileged: the `stackstead` user's own stat calls and Docker read APIs, **never** Deep check or the helper. Danger findings are kept in memory per unit (`map[unitKey][]Finding` plus scan time); units that disappear (container removed, stack unregistered) are dropped on the next full pass. Nothing is persisted.

### 6.2 Cost limits

Sized for a host with about 100 containers on a Raspberry Pi 4:

- **Incremental**: `Runner.Model` is cached by the content hashes of the stack's compose file set plus `.env` (the same key storage inventory §5.3 uses), so an unchanged stack costs no `compose` subprocess. Container inspect results and the effective user from `/top` are cached by container id + `StartedAt`; `/top` (which forks `ps` on the host) therefore runs once per container start, not per pass. Volume inspects are cached for one interval. A steady-state pass is stat calls plus one container listing.
- **Concurrency** 2 units at a time; **per-unit timeout** 10 s; **pass budget** 120 s. Units not reached are scanned first in the next pass (round-robin), so a slow host still covers everything over a few intervals; the scan time per unit is kept so the UI can say "last checked".
- **Caps** per pass: 300 containers, 3000 mounts, 200 `/top` calls, 20 `compose config` runs. Above a cap the remaining units roll over to the next pass; a `slog` Warn says so once per pass.
- `Statfs` deduplicated per device id within a pass; ancestor traversal checks cached per directory within a pass.
- Skipped entirely while Docker is disconnected. Stack units are skipped while the stack is `unavailable` or has a parse error (those already have alerts) and while one of its operations is running (checks the op lock, no waiting).
- The scanner runs at low priority: no work in the first 60 s after startup, and a full pass never starts while the previous one is still running.

### 6.3 Alerts

`alerts.Engine` gets a new optional source `MountProblems interface{ Problems() []MountProblem }` (`MountProblem{Stack, Service, Container, Code, Target, Message}`) and emits one alert per problem (the scanner never reports `low_space` findings as problems; they stay on the Storage tab and the dashboard, and low-disk phone alerts come from notifications' `disk_low`): kind `mount_problem` (new constant `KindMountProblem`), severity `danger`, `subject` = finding id, message e.g. "media/jellyfin: /config is not writable by uid 1000" or "plex: /config is not writable by uid 1000". Stack-unit alerts carry `Stack` and link to `/stacks/<name>?tab=storage`; container-unit alerts carry only `Container` (and `Stack` empty), so the dashboard links them to `/containers/<name>` like other container alerts, where the Mounts badges show the finding. "Since" follows the engine's existing first-seen tracking. The notifications spec consumes alerts generically; nothing extra is needed here.

### 6.4 Configuration

New flat keys in `internal/config/config.go` (each with `STACKSTEAD_<KEY>` env override, validated in `validate()`, rendered in `template.yaml`):

| Key | Default | Validation |
|---|---|---|
| `mount_scan_interval` | `15m` | `0` (disabled) or ≥ `1m` |
| `low_space_warning_percent` | `10` | 0–100 |
| `low_space_warning_free` | `2GiB` | byte size (`KiB/MiB/GiB/TiB`, `KB/MB/GB/TB`), parsed by a new `config.ByteSize` type |
| `low_space_danger_percent` | `3` | 0–100, ≤ warning |
| `low_space_danger_free` | `500MiB` | byte size, ≤ warning |
| `userns_remap_user` | `""` (use `dockremap` if it exists) | a user name or numeric uid; only used when the daemon reports `name=userns` |

## 7. API

All routes require a session; POSTs require `Content-Type: application/json` (existing CSRF rule); errors are `{error}`; Docker unavailable → 503; unknown stack/container → 404. Routes are registered only when the stacks, observe and container engine sources are set (like the container routes).

### 7.1 `GET /api/stacks/{name}/mounts?refresh=1`

```json
{"stack": "media", "generatedAt": "2026-10-18T09:12:00Z",
 "host": {"os": "linux", "pathChecks": "available", "selinux": false, "apparmor": true,
          "idMapping": {"mode": "none", "status": "exact", "user": "", "reason": ""}, "helperImage": "present"},
 "services": [{"name": "jellyfin", "containers": ["media-jellyfin-1"],
   "user": {"uid": 1000, "gid": 1000, "groups": [44], "hostUid": 1000, "hostGid": 1000, "hostGroups": [44], "source": "process"},
   "mounts": [{"kind": "bind", "target": "/config", "source": "/srv/media/config",
     "readOnly": false, "declared": true, "runtime": true, "selinuxOption": "", "propagation": "rprivate",
     "host": {"exists": true, "type": "dir", "uid": 0, "gid": 0, "owner": "root", "group": "root",
              "mode": "0755", "empty": true, "readable": true, "acl": false, "label": "",
              "fs": {"free": 9663676416, "total": 125829120000, "readOnly": false}},
     "access": {"read": "yes", "write": "no"},
     "volume": null, "tmpfs": null, "findingIds": ["b41c…"]}]}],
 "findings": [{"id": "b41c…", "code": "bind_autocreated", "severity": "warning", "service": "jellyfin",
   "container": "media-jellyfin-1", "target": "/config", "source": "/srv/media/config",
   "message": "Docker created /srv/media/config because it was missing; uid 1000 cannot write to it.",
   "evidence": {"owner": "0:0", "mode": "0755", "empty": true, "likely": true},
   "remedy": {"text": "Give it to the container user.", "command": "sudo chown 1000:1000 /srv/media/config",
              "fixes": ["chown"]}}],
 "deepCheckedAt": null}
```

- `user.source`: `process | config | image | unknown`; `uid`/`gid` are container-side ids, `hostUid`/`hostGid`/`hostGroups` the mapped host ids (equal in mode `none`); each `null` when unknown or unmapped. Never the raw `User` string. `host.idMapping` is §4.7.
- `host.helperImage`: `present | absent | unknown`.
- `remedy.fixes` lists only fixes the server would accept now (inside `allowed_roots`, Linux, path checks available).
- Cached 10 s per stack, single flight; `refresh=1` bypasses. A stack whose compose file does not parse → 409 `{error}` with the parse error (the Storage tab is hidden in that state anyway).

### 7.2 `POST /api/stacks/{name}/mounts/deep-check`

Body `{}`. 409 `{error, code: "helper_image_absent"}` when the helper image is missing (UI offers the pull). Otherwise runs §4.4 synchronously (≤ 15 s) and returns the §7.1 report with `deepCheckedAt`. 502 when the helper fails (Docker's message or the helper's stderr, as text).

### 7.3 `POST /api/helper-image/pull`

Body `{}`. Pulls the pinned image through `POST /images/create?fromImage=busybox&tag=<version>@<digest>` and checks the resulting digest. Returns an operation (202, `Stack` = `""`, kind `pull-helper`) whose output streams over the existing operation SSE. Offline/registry errors → the operation fails with the message and the copy-paste `docker pull busybox@sha256:…` line.

### 7.4 `GET /api/containers/{id}/mounts`

Same shape as one `services[]` entry plus `findings` (runtime mounts only, `declared` false), for any container. Deep check and fixes are not offered for containers outside a registered stack (stack-scoped operations and audit); the remedy shows commands.

### 7.5 `POST /api/stacks/{name}/mounts/fixes/preview`

```json
{"findingId": "b41c…", "fix": "chown", "uid": 1000, "gid": 1000, "mode": "", "recursive": false}
```

The client names a finding and a fix kind, never a path; the target path is taken from the server's current report. Response:

```json
{"planId": "9a7e…", "target": "/srv/media/config", "commands": ["chown -h 1000:1000 -- /srv/media/config"],
 "entries": 1, "recursive": false, "confirmPhrase": "chown /srv/media/config",
 "warnings": [], "expiresAt": "2026-10-18T09:17:00Z"}
```

Validation (400 with `fields`) and refusals (409) are §8.3. For `recursive: true` the preview runs the counting helper (§8.4) first; `entries` is the count and `confirmPhrase` includes it (`chown -R /srv/media/config (12431 entries)`). Plans live in memory for 5 minutes, bound to the session that created them.

### 7.6 `POST /api/stacks/{name}/mounts/fixes`

Body `{planId, confirm}`. `confirm` must equal `confirmPhrase` exactly (400 otherwise). The server recomputes the target's `Lstat` and resolution; if anything changed since the preview (type, owner, mode, resolved path, device, entry count drift > 1 %) → 409 "the path changed since the preview; preview again". Otherwise it starts a `fix` operation (202, the operation object) on the stack's operation lock (409 `ErrBusy` when another operation runs) and writes an `action_log` row when it ends (§8.5).

### 7.7 `GET /api/stacks/{name}/mounts/fixes`

The stack's fixes: `action_log` rows with kind `mount.fix` and scope `{name}`, newest first, at most 100, through `actionlog.Recent` (storage inventory §5.5) with `Filter{KindPrefix: "mount.fix", Scope: name, Limit: 100}`. Each item: `{id, at, user, target, fix, command, recursive, entries, result, exitCode, operationId, summary}` (`operationId` comes from the row's `operation_id`, and `fix`, `command`, `recursive`, `entries`, `exitCode` and `summary` from its `detail`).

### 7.8 Existing routes

- `GET /api/alerts`: may contain `mount_problem` alerts (§6.3).
- `GET /api/operations…`: operations with `action` `fix` or `pull-helper` appear in lists and streams like any other.

## 8. Fixes and the helper container

### 8.1 Fix kinds

| Kind | Command (argv, run in the helper) | Applies to |
|---|---|---|
| `mkdir` | `mkdir -p -- /t/0/<rel>` then optional `chown -h uid:gid` and `chmod mode` on each created component | `bind_source_missing` |
| `chown` | `chown -h uid:gid -- /t/0/<rel>`; recursive: `find /t/0/<rel> -xdev -exec chown -h uid:gid -- {} +` | `bind_autocreated`, `not_writable`, `not_readable` |
| `chmod` | `chmod mode -- /t/0/<rel>`; recursive: `find /t/0/<rel> -xdev ! -type l -exec chmod mode -- {} +` (symlinks never chmod-ed) | `not_writable`, `not_readable`, `parent_not_traversable`, `world_writable` |

The preview's `commands` show the same commands with `/t/0/<rel>` replaced by the host path, so the operator sees exactly what will happen on the host. Recursion uses `find -xdev` rather than `chown -R` so it never crosses into other filesystems (e.g. a nested mount); `-h` and `! -type l` mean symlinks are never followed or modified through.

### 8.2 Helper container

- Created via the raw Docker API (`POST /containers/create`, `start`, `wait`, `logs`, `DELETE ?force=1`), new `engine` methods `RunHelper(ctx, HelperSpec) (HelperResult, error)` and `PullImage(ctx, ref, out func(string)) error`. Never the docker CLI, never a shell: `Cmd` is an argv array with `--` before every path.
- Image `busybox:<version>@sha256:<digest>` (constant `mounts.HelperImage`; a unit test checks that the constant parses and carries a digest). **Refreshed every release** as a routine step (*user*): a new opt-in Makefile target `helper-image-update` resolves the current `busybox:<latest stable version>` multi-arch index digest with `docker buildx imagetools inspect` and rewrites the constant, and the release steps in `docs/development.md` gain the line "Run `make helper-image-update`, commit the digest bump (`chore: refresh helper image digest`)". The bump is a normal reviewed commit; nothing updates the pin at runtime. Hosts that already pulled the previous digest are offered the pull again after upgrading (§7.3).
- `HostConfig`: `Binds` only from server-derived, already-resolved host paths (Deep check: read-only; fix: the nearest existing ancestor of the target, read-write, at `/t/0`); `NetworkMode: none`; `ReadonlyRootfs: true`; `CapDrop: ["ALL"]`; `CapAdd`: `DAC_READ_SEARCH` (Deep check), `CHOWN, FOWNER, DAC_OVERRIDE` (fixes; mkdir needs `DAC_OVERRIDE` in root-owned parents); `SecurityOpt: ["no-new-privileges", "label=disable"]` (so the helper can read unlabelled paths on SELinux hosts without relabelling them); `Memory` 32 MiB; `PidsLimit` 16; `User: "0:0"`.
- Labels `dev.stackstead.helper=true`, `dev.stackstead.stack=<name>`, `dev.stackstead.operation=<id>`; the containers list shows them as `managed: none` like any other, and startup removes leftover exited helpers by label.
- Before start, the created container is inspected and its `Mounts[0].Source` must equal the resolved host path; otherwise it is removed and the fix fails (guards against a symlink swapped in between resolution and create).
- Inside the helper, the bind is the only host filesystem: an absolute or `..`-escaping symlink below `/t/0` resolves into the helper's read-only busybox rootfs, not the host, so a link planted inside the target cannot redirect the fix elsewhere on the host.

### 8.3 Validation and refusals

- **Target**: from the finding (server report), never the client. Resolved with `fsguard.Resolve` (existing target) or, for mkdir, the nearest existing ancestor; must be **strictly below** one of `allowed_roots` (a root itself is refused). Refused (409, with reason) when: outside roots; under `DockerRootDir`; the target is the stack's compose directory or an ancestor of it (Stackstead must keep write access to `compose.yaml`), or the stacks dir itself; a filesystem mounted `ro`; macOS; path checks unavailable (§4.6); the finding does not list that fix kind.
- **uid/gid**: integers `0 … 2147483646`; default to the finding's effective uid/gid. Names are not accepted (no ambiguity between host and container name spaces). `uid 0` allowed with warning "root ownership is rarely what a container needs".
- **mode**: octal string `^0?[0-7]{3,4}$`; setuid (`04000`) refused; setgid and sticky allowed (stack dirs use `2775`); a mode granting `o+w` returns warning "world-writable"; for directories, a mode without owner `x` is refused ("the directory would become untraversable").
- **recursive**: allowed only for `chown` and `chmod`, only on directories; refused above 100 000 entries, with the copy-paste command instead.
- **planId**: unknown, expired, other session → 409 "preview again".

### 8.4 Recursive count

The preview of a recursive fix runs a read-only helper (`DAC_READ_SEARCH`) with `find /t/0/<rel> -xdev | head -n 100001 | wc -l` (as argv to `sh -c` with the path passed as `$1`, never interpolated). The count and the target's device id are stored in the plan; apply re-checks the device and that the count did not grow by more than 1 %.

### 8.5 Recording

- **Operation**: `Ops.Start(stack, "fix", fn)`; output lines are the previewed commands, then the helper's stdout/stderr, then `exit <code>`. Cancel kills the helper (`DELETE ?force=1`). Timeout `operation_timeout`. Shows in the Operations tab.
- **Action log**: one row per applied fix in the shared `action_log` table, which is defined (schema, migration, retention, store API) by `2026-10-18-stackstead-storage-inventory-design.md`. Whichever of Plan V2 and the storage inventory plan lands first creates the table; this spec adds no table of its own. Row mapping:

| `action_log` column | Value for a fix |
|---|---|
| `ts` | when the operation ended |
| `username` | the signed-in user who applied the plan |
| `kind` | `mount.fix` |
| `scope` | the stack name (the inventory spec's scope is `host` or a stack name) |
| `target` | the host path |
| `detail` | JSON `{fix: "mkdir\|chown\|chmod", command, uid, gid, mode, recursive, entries, findingCode, exitCode, summary}`; `command` is the exact previewed command(s), `summary` a one-line summary |
| `bytes` | `NULL` (not applicable) |
| `result` | `succeeded`, `failed`, `cancelled` or `timed_out` |
| `operation_id` | the `fix` operation's id |

  A row is written when the operation ends, whatever its state; a fix refused before it starts (validation, stale plan) writes none. A `slog` line at Info is also written per fix (stack, target, fix kind, result).
- The stack's report cache is invalidated when a fix ends, and a scan of that stack is queued (§6.1).

## 9. Package and type outline

`internal/mounts` (new):

- `type Kind string` (`bind`, `volume`, `tmpfs`, `npipe`, `image`, `cluster`).
- `type DeclaredMount struct{ Service, Target, Source string; Kind Kind; ReadOnly, CreateHostPath bool; SELinux, Propagation, VolumeName string; External bool; Driver string; DriverOpts map[string]string; TmpfsSize int64; TmpfsMode uint32 }`.
- `type HostInfo struct{ Exists bool; Type string; UID, GID uint32; Owner, Group string; Mode fs.FileMode; Empty, Readable, ACL bool; Label string; Ctime time.Time; Dev uint64; FS FSInfo; LinkTarget string }`.
- `type EffectiveUser struct{ UID, GID, HostUID, HostGID *uint32; Groups, HostGroups []uint32; Source string }`.
- `type IDMapping struct{ Mode string /* none|rootless|userns */; Status string /* exact|unavailable */; User string; Reason string; UIDRanges, GIDRanges []IDRange }`, `type IDRange struct{ Start, Count uint32 }`.
- `func DetectIDMapping(info engine.Info, socketPath string, fs IDSource, remapUser string) IDMapping` and `func (m IDMapping) ToHostUID(c uint32) (uint32, bool)` / `ToHostGID` — pure; `IDSource` reads `/etc/subuid`, `/etc/subgid`, socket stat and user lookup (fake in tests).
- `func ParseSubIDs(r io.Reader, user string, uid uint32) (ranges []IDRange, malformed int)`.
- `type Finding struct{ ID, Code, Severity, Service, Container, Target, Source, Message string; Evidence map[string]any; Remedy Remedy }`.
- `type Report struct{ Stack string; GeneratedAt time.Time; Host HostFacts; Services []ServiceMounts; Findings []Finding; DeepCheckedAt *time.Time }`.
- `type HostFS interface{ Lstat(string) (HostInfo, error); Statfs(string) (FSInfo, error); Label(string) (string, error); HasACL(string) (bool, error); IsEmptyDir(string) (bool, error) }` — real `osHostFS`, fake in tests.
- `type PathMapper interface{ ToLocal(host string) (string, bool) }` — `IdentityMapper`, `UnavailableMapper`.
- `type Engine interface{ InspectContainer; InspectVolume; InspectImage; ContainerTop; Info; RunHelper; PullImage; ImagePresent }` (narrow, satisfied by `*engine.Client`).
- `func Analyze(in Input, th Thresholds) Report` — pure: declared + runtime + host facts → mounts, verdicts and findings. Almost all logic and tests live here.
- `type Collector struct{…}`; `func (c *Collector) Stack(ctx, name string, opts CollectOptions) (Report, error)`, `Container(ctx, id string)`.
- `type UnitKey struct{ Stack, ContainerID string }` (exactly one set).
- `type Scanner struct{…}`; `Start(ctx)`, `Problems() []alerts.MountProblem`, `Trigger(UnitKey)`, `LastScanned(UnitKey) time.Time`.
- `type Fixer struct{…}`; `Preview(ctx, session, stack string, req FixRequest) (Plan, error)`, `Apply(ctx, session, stack, planID, confirm string) (stacks.Operation, error)`.

Other packages: `composecli.ComposeModel` (decoded subset only); `engine` additions listed in §4.1 and §8.2; `apitypes` DTOs `MountReport`, `MountFinding`, `FixPlan`, `FixHistoryEntry`; `api/mounts.go` handlers; `alerts.KindMountProblem`; `store`: the shared `action_log` migration and its insert/query functions as named in the storage inventory spec (created here only if that plan has not landed); `cli/serve.go` wiring of collector, scanner (started after the state cache), fixer.

## 10. Frontend

### 10.1 Storage tab (`web/src/lib/stacks/StoragePanel.svelte`)

- New tab `storage` ("Storage") in `StackDetail.svelte` `ALL_TABS` after Services; badge = number of danger + warning findings; hidden in parse-error mode like Services.
- Top: findings list (severity icon + text, never colour alone), each with message, evidence (`<details>`), remedy text, **Copy command** (`CopyButton`), and **Fix…** when `remedy.fixes` is non-empty. A banner when `host.pathChecks` is unavailable (§4.6 reason) or on macOS (ownership unknown).
- Then one section per service: heading with service name and the effective user chip (`uid 1000:1000 · from process` / `unknown`); a table: Target, Kind, Source (mono, ellipsis, `title`), Host (type · owner:group · mode), Access (`r w` badges: yes/no/unknown), Space (free / total, tone by threshold), Flags (`ro`, `z`, `drift`), and finding markers linking up to the finding.
- Header actions: **Refresh** (`refresh=1`), **Deep check** (shown when any `not_inspectable` exists; when the helper image is absent it opens the pull consent dialog: "Stackstead needs to pull busybox (≈2 MB, pinned digest) once to inspect paths as root." → `POST /api/helper-image/pull`, shows the operation panel, then runs the Deep check).
- **Fix history**: collapsed `<details>` at the bottom from `GET …/mounts/fixes`; each row links to its operation while it is still retained.
- Refresh policy: fetch on tab open, after a fix or Deep check, and on `/api/events` `change` for this stack (debounced like other lists). No timers.

### 10.2 Fix dialog (`web/src/lib/stacks/FixDialog.svelte`)

- Step 1, options: fix kind (when several), `uid`/`gid` number inputs prefilled from the finding, mode input (chmod/mkdir), **Apply to everything inside (recursive)** checkbox (chown/chmod on dirs) with the help text "Counts entries first; never follows symlinks or crosses filesystems". **Preview** → `fixes/preview`; field errors map to inputs.
- Step 2, preview: target path, the exact command(s) in a mono block, entry count for recursive, warnings, and a text input labelled "Type `<confirmPhrase>` to confirm" (the `ConfirmDialog` gains an optional `confirmPhrase` prop; the confirm button stays disabled until it matches exactly). **Run fix** → `fixes` → the existing `OperationPanel` streams the operation; on success the report refreshes.
- 409 "path changed" returns to step 1 with the message.

### 10.3 Container page

`ContainerDetail.svelte` Details tab, Mounts row: each mount line gets a severity badge when `GET /api/containers/{id}/mounts` has findings for its destination (fetched lazily when the Details tab is shown), and a **Diagnose** link: to `/stacks/<stack>?tab=storage` for stack containers, else it expands an inline findings list (commands only).

### 10.4 Operation panel

When a finished `up` operation failed and its output matches mount error patterns (`not a directory`, `mount a directory onto a file`, `permission denied` with `mount`, `no such file or directory` with `bind source`, `external volume … not found`), `OperationPanel` shows a **Diagnose mounts** link to `?tab=storage`. Patterns live in one helper `mountErrorHint(lines: string[]): boolean` in `web/src/lib/stacks/storage.ts`.

### 10.5 Alerts

Dashboard alert rows of kind `mount_problem` link to `/stacks/<stack>?tab=storage`.

### 10.6 Accessibility and responsive

As the containers spec §5.5: icon-only controls carry `aria-label`/`title`; severity and access verdicts have text; the per-service table becomes cards under 720 px; the confirm input has a visible label and `aria-describedby` pointing at the command block. All paths, labels, Docker and helper messages render as text only (no `{@html}`).

## 11. Security

- **No client paths.** Every path read or changed comes from the compose model or Docker inspect of a registered stack/container. Fix requests carry a finding id and a fix kind; the server takes the path from its own report.
- **Reads are metadata only**: `Lstat`, `Statfs`, xattrs, one-entry `ReadDir`; never `Open` on a regular file. Unreadable paths are reported, not retried with privilege. Any signed-in user can therefore learn the existence, owner and mode of paths that stacks bind-mount — documented in `docs/security.md`; it is less than the Docker access they already have.
- **Privilege only on explicit action**: the helper runs only from Deep check, the recursive-count preview, or an applied fix, each from a click; never from the scanner. It has no network, a read-only rootfs, all caps dropped except the named ones, `no-new-privileges`, small memory/pid limits, and a pinned image digest.
- **Writes only below `allowed_roots`** (strictly), never the stack's own compose directory, the stacks dir, or Docker's root dir; resolution with `fsguard` (symlink-safe, fails closed), mount source re-verified after create, symlinks never followed by the commands, no filesystem crossing.
- **Confirmation**: exact command preview, typed phrase (including the entry count for recursive), plan bound to session and expiring in 5 minutes, re-validation at apply, per-stack operation lock.
- **Audit**: `fix` operation output plus a persistent `action_log` row (`mount.fix`) with the user.
- **ID files**: `/etc/subuid` and `/etc/subgid` are only read and parsed, never written; their contents are not returned, only the derived mapping status and reason.
- **Secrets**: the compose model decoder keeps only volume fields, `user` and `group_add`; container `User` strings are reduced to numeric ids; nothing from `environment` is read into memory structures that are logged or returned.
- `docs/security.md` gains a section "Mount diagnostics and fixes" with the points above and: the helper container runs as root with the listed capabilities, and Deep check can reveal ownership of named-volume roots.
- No command string is ever built for a shell except §8.4's `sh -c` with the path as a positional argument.

## 12. Testing

Test-first for every behaviour change (global rules); tests assert observable behaviour.

**`internal/mounts` (unit, fake HostFS and Engine):**
- `TestAnalyze_BindSourceMissing` — warning by default, danger with `create_host_path: false`; remedy offers mkdir only inside roots.
- `TestAnalyze_BindAutocreated` — empty root-owned dir with ctime near container create and uid 1000 → warning `likely`; not flagged when non-empty, older, or user root.
- `TestAnalyze_FileDirMismatch` — each trigger: StateError text, empty dir with `.conf` name, missing source with file-like target; no fix offered.
- `TestAccessVerdict` — table: owner/group/other bits × uid/gid/supplementary groups × ro mount × ACL present (unknown) × mapping unavailable (unknown) × uid 0; evaluated with host ids after mapping.
- `TestParseSubIDs` — name and uid forms, several ranges kept in file order, other users ignored, comments/blank/malformed lines skipped and counted, overflow `start+count` rejected.
- `TestDetectIDMapping` — `name=rootless` + socket owned by 1000 under `/run/user/1000` → rootless/exact; root-owned socket → unavailable; `/run/user/<n>` mismatch → unavailable; `name=userns` with `dockremap` present / `userns_remap_user` set / neither → exact, exact, unavailable; no option → none; subuid file missing, unreadable (EACCES), no entry for the user → unavailable with the matching reason.
- `TestIDMappingToHost` — rootless: 0 → daemon uid, 1 → first range start, last id of range 1 → end, first id of range 2 → second start, beyond → unmapped; userns: 0 → first start; gid mapping via subgid independent of uid ranges.
- `TestEffectiveUser_Mapping` — rootless maps `/top` and `Config.User` ids; userns maps `Config.User` but not `/top`; unmapped id → `hostUid` null + `id_mapping_unavailable` finding (one per service).
- `TestFixer_Rootless` — preview shows host command with host ids and helper argv with container ids, plus the "other host users" warning when the target's owner is outside the daemon user's ids; helper `EPERM` → failed op with the sudo message; userns mode sets `UsernsMode: host` on the helper.
- `TestAnalyze_ParentNotTraversable`, `TestAnalyze_WorldWritable`, `TestAnalyze_SymlinkSource`.
- `TestAnalyze_Volumes` — missing external (danger), foreign project label, NFS `driver_opts`, anonymous.
- `TestAnalyze_Drift` — source changed, ro changed, target added/removed; none when equal.
- `TestAnalyze_LowSpace` — percent vs bytes thresholds, whichever trips first; custom thresholds.
- `TestAnalyze_SELinux` — enforcing + no `z` + `default_t` → warning; `container_file_t` or `:z` → none; SELinux off → never.
- `TestAnalyze_Unavailable` — container mode: no host findings, volume/drift still present; macOS: ownership unknown, no fixes.
- `TestEffectiveUser` — top with init + app process, multiple uids, top error → Config.User numeric → image User → `name` → unknown.
- `TestFindingIDStable` — same input, same ids across runs.
- `TestScanner_Units` — registered stack → stack unit; unregistered compose and plain containers → container units; helper containers skipped; removed container's problems dropped on the next full pass.
- `TestScanner_Triggers` — interval with fake clock and jitter bounds; post-operation and container `start` triggers debounced per unit; no work in the first 60 s; no overlapping full passes.
- `TestScanner_Cost` — model cache by hash (no `Model` call when unchanged); `/top` called once per container id + StartedAt across passes; per-unit timeout; 120 s budget and caps roll remaining units over round-robin; skipped while Docker disconnected or a stack op runs; never calls `RunHelper` or `PullImage`.
- `TestScanner_Benchmark` (`go test -bench`) — 100 containers / 600 mounts against fakes; steady-state pass makes 0 `Model`, 0 `/top` and 1 list call.
- `TestFixer_Validation` — table for §8.3 (root itself, outside roots, compose dir ancestor, DockerRootDir, setuid, untraversable dir mode, bad uid, recursive on file, >100 000 entries, macOS, unknown finding/kind).
- `TestFixer_PreviewApply` — commands text with host path; confirm mismatch 400; plan expiry and other session 409; changed owner/device/count between preview and apply 409; success starts `fix` op, writes one `mount.fix` action_log row with the §8.5 mapping (also on failure and cancel; none on refusal), invalidates cache.
- `TestFixer_HelperSpec` — exact `HostConfig` (caps, readonly rootfs, network none, binds), argv with `--`, recursive uses `find -xdev` and `-h`/`! -type l`; mount source mismatch after create → removed and failed.

**`composecli`:** `TestModel_DecodesVolumes` from a fixture JSON (long and short normalised forms, tmpfs shorthand, top-level volumes) and asserts `environment` values are absent from the decoded struct; `TestModel_Error` passes stderr.

**`engine`** (fake socket server): `InspectVolume` 200/404; `InspectImage` user; `ContainerTop` request `ps_args` and parsing; extended inspect fields; `RunHelper` create/start/wait/logs/remove sequence, cleanup on cancel and on error; `PullImage` streaming and digest check.

**`alerts`:** `TestMountProblemAlerts` — problems become `mount_problem` danger alerts with stable keys and since; stack problems carry the stack, container problems only the container; none when the source is nil.

**`store`:** `action_log` tests belong to whichever plan creates the table (storage inventory spec). This feature adds `TestFixHistory_FiltersMountFixByStack`: rows of other kinds and other stacks are excluded, newest first, limit 100.

**`config`:** new keys parse, env overrides, byte sizes, validation (interval < 1m, danger > warning).

**`api`:** session on every route; report JSON shape and 10 s cache/refresh; 404/409/503 mapping; deep-check 409 `helper_image_absent`; container mounts route; fix preview/apply status codes; no route accepts a `path` parameter (request with `path` field is ignored — asserted by target in the plan); `User` string never in any response body.

**Integration (`//go:build integration`, real Docker, Linux):** temp dir added to `allowed_roots`; stack with (a) a missing bind source, (b) `./app.conf:/etc/app.conf` missing, (c) `user: "1234"` on a root-owned `0755` dir; `up`; report shows `bind_autocreated`, `file_dir_mismatch`, `not_writable` with uid from `/top`; chown fix via the API → operation succeeds, `mount.fix` action_log row, next report has no `not_writable`; recursive chown count matches files created; refusal for a path outside roots; Deep check of a named volume returns its root owner. A separate opt-in job `TestIntegration_RootlessIDs` (tag `integration,rootless`, run where a rootless daemon is available via `DOCKER_HOST`) runs a container with `user: "1000"` writing to a bind dir owned by the mapped host id, and asserts the verdict is `write: yes`, that `/top` ids are container-side (pinning §4.7's assumption), and that a chown fix through the helper produces the expected host owner.

**Web (vitest):** Storage tab rendering per kind, badges, unavailable/macOS banners, copy command, Deep check flow including pull consent; FixDialog options → preview → typed phrase gating → operation panel, 409 path-changed returns to step 1, field errors; `ConfirmDialog` `confirmPhrase`; container Mounts badges and Diagnose link per `managed`; `mountErrorHint` patterns; alert link to the Storage tab.

**E2E smoke (`cmd/stackstead/e2e_test.go`):** create a stack with a missing bind source under the test stacks dir; `GET …/mounts` lists `bind_source_missing`; after `up`, `bind_autocreated` (or no finding when the image runs as root); `GET /api/alerts` after a triggered scan contains `mount_problem` for a danger case (missing external volume); a plain `docker run -d -u 1234 -v <root-owned 0755 temp dir>:/data busybox` container (labelled for cleanup) produces a container-only `mount_problem` alert after its start-triggered scan. Fix apply is covered by integration, not the smoke (needs the helper image pull).

**Visual:** headless-Chrome screenshots of the Storage tab and the fix dialog at 1280 and 375 px, dark and light.

Acceptance: all of the above pass; `go vet`, `golangci-lint`, `make ui-check`; no change to existing tests' expectations except the additive inspect fields and the `ConfirmDialog` prop.

## 13. Documentation

- `docs/api.md`: §7 routes and fields, `mount_problem` alerts, `fix` and `pull-helper` operations.
- `docs/security.md`: §11 section.
- `docs/development.md`: release step "Run `make helper-image-update` and commit the digest bump" (§8.2).
- README: configuration keys (§6.4), rootless/userns notes (§4.7); feature line "Storage diagnostics: missing bind mounts, ownership and permission problems, SELinux labels, low disk, with guarded fixes".

## 14. Delivery

- **Plan V1 — Diagnostics**: compose model, engine reads, `internal/mounts` analyse/collect, Deep check + helper + pull, scanner + alerts, config keys, API read routes, Storage tab, container badges, operation hint, docs.
- **Plan V2 — Fixes**: fixer, helper write spec, fix routes, `fix` operation, `action_log` writes (and the table's migration if the storage inventory plan has not landed yet), FixDialog, `ConfirmDialog` phrase, fix history, docs. Depends on V1.

## 15. Open questions

None outstanding. Every judgment call was settled with the user (marked *user* in §3). Implementation should confirm one assumption against real daemons, and §12's rootless integration job pins it: where `/top` ids live in rootless and userns-remap modes (§4.7).
