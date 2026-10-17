# Stackstead Self-Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add manually approved stable-release updates to Settings for every existing installer, preserving configuration, data, native package ownership and recoverability.

**Architecture:** An independent root-owned bootstrap selects a pinned recovery worker; an authenticated Unix socket connects the unprivileged application to the bounded updater. A durable transaction verifies current and target releases, quiesces applications, snapshots SQLite, installs through the original format, and admits writes only after version/identity probation succeeds. Installer-specific adapters preserve lifecycle behavior; the release builder ships mandatory compatibility metadata and helper payloads together.

**Tech Stack:** Existing Go 1.26.0, standard library, existing golang.org/x/sys and modernc.org/sqlite, Svelte 5/TypeScript, existing nfpm/systemd/launchd/OpenRC tooling; no runtime AI dependency.

**Spec:** `docs/superpowers/specs/2026-10-17-stackstead-self-update-design.md`, including “Builder-check resolutions”.

## Global Constraints

- “Initially expose stable versions only, explicit Check for updates and Update buttons, no scheduled automatic install, no arbitrary downgrade or channel selection.”
- “The web server retains its current identity and hardening.”
- “No browser sudo password, setuid workaround, arbitrary URL, arbitrary path, arbitrary command, or Docker-based host escalation.”
- “Default to the project's existing distribution trust contract: HTTPS to the fixed `rcss-labs/stackstead` GitHub release source plus strict SHA256SUMS verification.”
- “A failed transaction may reinstall its one recorded previous package as bounded recovery.”
- “The first release providing this feature is the baseline for managed web updates.”
- “All later web updates require both a compatible target manifest and a compatible previous updater-enabled release with its exact verified rollback artifact.”
- “The release builder derives the database schema from compiled source, never from a manually maintained independent constant.”
- “No two update jobs, no overlap with uninstall, and no concurrent binary writer.”
- “Write/fsync journal and parent directory before irreversible steps”; root trust state is separate from app data/user homes.
- “`--root` must only stage helper/config artifacts and print service operations; never start a host helper.”
- “`--no-start` must not accidentally start the application”; preserve disabled/masked/unloaded state, configs, data, stacks and Docker state.
- “A shared macOS prefix means updating the binary affects every user”; coordinate all registered instances, refuse unknown running instances.
- “After this point no automatic rollback to the pre-update snapshot”: durable success precedes write admission.
- User testing policy controls: focused meaningful tests first for changed behavior; existing checks suffice for pure refactors/docs/mechanical changes. Record actual red/green evidence; never invent chronology or claim skipped native checks passed.
- TypeSafe scorer unavailable: disclose that limitation; no fabricated verdicts/scores. Do not add TypeSafe to application code.
- Execution is **serial subagent-driven development**: Sol medium worker, fresh Sol high task review, next task only after review passes. Planning is Sol high. The planning override does not change the normal final-review tier: Astra high if available, otherwise Sol high. No parallel shared-file edits.
- Source/plan work is authorized; privileged installation on the user's host is outside this build. Show exact operational mutation commands and obtain required approval before live resource changes. Never inspect/edit `.git/` directly or commit secrets.
- Commit only with already established authorization; use the task's conventional message and explicit file paths. Do not add an invented Claude/coauthor trailer.

## Review Focus

1. Metadata/artifact changes after confirmation: immutable candidate binding must prevent installing a different release (Tasks 1–2).
2. Package-owned helper disappears during a crash: root-owned bootstrap must recover with the pinned worker, without journal-controlled executable paths (Tasks 3, 13, 19).
3. Same-schema writes during probation: rollback must restore the final pre-stop snapshot without discarding admitted user writes (Tasks 4–7).
4. Non-console/macOS users sharing a prefix: update every registered active instance under its UID and refuse unknown instances (Tasks 11–12, 16).
5. Administrator-disabled or custom-supervised applications: do not accidentally enable/start them, and report setup-required until supervision is registered (Tasks 8, 14–16).

---

## File Map and Shared Contracts

Keep updater logic separate from HTTP/CLI. Create focused files under `internal/update/` for types, manifest, release, verification, journal, bootstrap and transaction; `internal/updatehost/` for adapters/composition; `internal/updateipc/` for peer-authenticated transport; `internal/maintenance/` for write admission; `internal/dbmaint/` for reused DB restore/snapshot. Entry points are `cmd/stackstead-updater/main.go` and `cmd/stackstead-updater-bootstrap/main.go`. Existing app wiring stays in `internal/cli/serve.go`; handlers/types/clients and Settings files follow existing conventions. Exact files owned by each task are listed below.

In abbreviated signatures below, `ctx` always means `ctx context.Context`; installation/instance/job IDs are `string`; named DTO parameters use the shown Go DTO type; no `any` payloads are allowed. Task 1 defines these contracts, including JSON tags using the lowerCamelCase names shown; all later tasks consume these names. No production adapter is registered until Task 13 composes it. Package names in the contracts qualify their local Go types when crossing packages (for example `update.Plan` in updatehost). Signature-only, fail-closed scaffolding may be added after writing a test if needed to compile it; rerun until the red failure is the named behavioral assertion, not an import/build/fixture error. Never count a compiler failure as behavioral red evidence.

**`internal/update/types.go`:**

- `ReleaseIdentity { Version, Commit string }`; versions normalized without leading `v`, release commit full hexadecimal source commit. This is platform-neutral release identity.
- `Identity { Version, Commit, OS, Arch string }`; selected executable identity, with concrete runtime OS/arch.
- `Manifest { SchemaVersion int; Identity ReleaseIdentity; HelperMin, HelperMax, BootstrapProtocol, JournalProtocol, SourceSchemaMin, TargetSchema int; Assets []AssetSpec }`; one manifest describes the whole release, never the builder host's OS/arch.
- `AssetSpec { OS, Arch string; Installer InstallerKind; Name string }`; `InstallerKind` values `script`, `deb`, `rpm`, `pkg`; PKG has `Arch="universal"` and resolves for both Darwin architectures.
- `VerifiedArtifact { Name, Path, SHA256 string; Size int64 }`; paths are helper-private outputs, never client inputs.
- `Candidate { ID string; Identity Identity; Manifest Manifest; MetadataSHA256, ChecksumsSHA256, NotesURL string; Asset AssetSpec; ExpiresAt time.Time }`; identity combines manifest version/commit with the registered executable platform. Universal PKG assets retain `Arch="universal"`, while candidate identity uses concrete `amd64`/`arm64`.
- `SupervisorSpec { Kind, Name, AdapterPath string }`; kinds `systemd`, `launchd`, `custom`; custom adapter path only from root registration.
- `Instance { ID string; UID, GID uint32; ConfigPath, DataDir, ControlSocket string; Supervisor SupervisorSpec }`.
- `Registration { SchemaVersion int; ID string; Installer InstallerKind; Prefix, Binary string; Identity Identity; Instances []Instance; UpdateEnabled bool }`; metadata/helper root inferred from platform constants, never a writable field.
- `ServiceState { InstanceID string; Running, Enabled, Masked, Loaded bool; PID int }`.
- `DBSnapshot { InstanceID, Path, SHA256, ConfigSHA256 string; Schema int }`.
- `Plan { Registration Registration; Current, Target Candidate; PreviousArtifact, TargetArtifact VerifiedArtifact; States []ServiceState }`.
- `Probation { JobID, Nonce string; Expected Identity }`; `Readiness { InstanceID, JobID, Nonce, ConfigSHA256 string; Identity Identity; PID, Schema int; Ready bool }`.
- `Phase string`: `accepted`, `resolving`, `downloading`, `verified`, `preflighted`, `quiescing`, `stopped`, `snapshotted`, `installing`, `starting-probation`, `verifying`, `succeeded`, `rolling-back`, `rolled-back`, `recovery-required`, `cancelled`.
- `Job { ID, InstallationID, IdempotencyKey string; Protocol int; Phase Phase; Plan Plan; WorkerSHA256 string; Probation Probation; Snapshots []DBSnapshot; ErrorCode, ErrorMessage string; UpdatedAt time.Time }`. Private full job is never an HTTP response.
- Typed sentinel errors: `ErrUnavailable`, `ErrSetupRequired`, `ErrDisabled`, `ErrBusy`, `ErrIncompatible`, `ErrCandidateExpired`, `ErrUnauthorized`, `ErrRecoveryRequired`; wrap with `%w`, map at API boundary.

**Service boundaries (defined with their owning implementation task):**

```go
// update, Task 2
NewReleaseClient(client *http.Client) *ReleaseClient
(*ReleaseClient).Check(context.Context, Registration) (Candidate, error)
(*ReleaseClient).Prepare(context.Context, Registration, Candidate) (Plan, error)
// update, Task 3
OpenJournal(root string) (*Journal, error)
(*Journal).Save(Job) error
(*Journal).Load(installationID string) (Job, error)
PinWorker(root, source string) (string, error) // returns sha256
SelectWorker(root string, job *Job) (string, error) // validated digest-derived path
// update, Task 7
NewCoordinator(releases *ReleaseClient, journal *Journal, runtime Runtime) *Coordinator
(*Coordinator).Status(context.Context, string) (UpdateStatus, error)
(*Coordinator).Check(context.Context, string) (UpdateStatus, error)
(*Coordinator).Begin(context.Context, string, BeginRequest) (JobView, error)
(*Coordinator).Job(context.Context, string, string) (JobView, error)
(*Coordinator).Cancel(context.Context, string, string) (JobView, error)
(*Coordinator).Recover(context.Context, string) error
(*Coordinator).Startup(context.Context, string, string) (*Probation, error)
```

Journal reads for authenticated Startup/Status/Job use bounded consistent durable snapshots without acquiring the installation mutation lock or starting repair. `PinWorker` source paths and registration/locator write APIs are root-internal, never IPC arguments. A normal successful Check with no newer stable release returns status without a candidate; source/network failure remains an error. Public error conversion strips private paths/nonces; `installed-pending-start` is a message for originally stopped instances, not a readiness proof or additional phase.

Task 1 also defines public DTOs in `internal/update/public.go`: `BeginRequest { CandidateID, IdempotencyKey string }`; `JobView { ID string; Phase Phase; CurrentVersion, TargetVersion, ErrorCode, Message string; UpdatedAt time.Time }`; `UpdateStatus { Current Identity; Installer InstallerKind; Capability, Reason, SetupCommand string; Candidate *CandidateView; Job *JobView }`; `CandidateView { ID, Version, Commit, NotesURL string; ExpiresAt time.Time }`. Capabilities exactly `ready`, `setup-required`, `disabled`, `busy`, `recovery-required`. Public types contain no paths except sanitized installer-generated `SetupCommand`; no DB/artifact paths, nonces or private journal.

## Task 1: Compatibility Contract and Build Identity

**Files:** Create `internal/update/{types,public,manifest}.go`, `internal/update/manifest_test.go`; modify `internal/version/version.go`, `internal/version/version_test.go`, `Makefile` (full commit stamping only).

**Interfaces:** Produce shared contracts above; `ParseManifest([]byte) (Manifest,error)`, `ValidateCompatibility(current, target Manifest, schema int) error`, `CandidateID(metadataSHA256, sumsSHA256, assetName string) string`. Define protocol constants `HelperProtocol=1`, `BootstrapProtocol=1`, `JournalProtocol=1`; release staging root is the platform-private root under `staging/`. `version.Get()` retains existing output fields; release `Commit` is full `git rev-parse HEAD`. Schema descriptor later comes from `store.SupportedSchema()`, not a new constant.

- [ ] Write `TestManifestRejectsUnknownOrMissingProtocols`, `TestCompatibilityRequiresBothUpdaterEnabledReleases`, `TestCandidateIDBindsAssetAndMetadata`, `TestManifestResolvesAllPlatforms`, `TestUniversalPkgMatchesBothDarwinArchitectures`; assert malformed/unknown fields, missing asset, installed identity inconsistent with current metadata, or source schema outside `[sourceSchemaMin,targetSchema]` return `ErrIncompatible`; current v0.2 metadata absent is rejected. Assert one platform-neutral manifest resolves all four executable platforms and universal PKG selection retains concrete runtime arch, changing either digest/asset changes ID, and full stamped commit survives `version.Get()`.
- [ ] Red: `go test ./internal/update ./internal/version -run 'Manifest|Compatibility|CandidateID|UniversalPkg|Version' -count=1`; confirm each named assertion fails as intended.
- [ ] Implement strict schema 1, protocol 1, contiguous helper-range validation, bounded semver comparison (stable candidates only), duplicate/conflicting asset rejection and public DTOs. Set types/interfaces without enabling new behavior elsewhere.
- [ ] Green: same command, then `go test ./internal/update ./internal/version`; require PASS.
- [ ] Review and commit explicit task files: `feat: define updater compatibility before allowing managed installs`.

## Task 2: Fixed-Source Release Verification

**Files:** Create `internal/update/{release,verify,extract}.go`, corresponding `_test.go` files.

**Interfaces:** Produce `ReleaseClient` contract above. Its production constructor fixes `https://api.github.com/repos/rcss-labs/stackstead/releases/latest` and official versioned release downloads; tests inject an HTTP RoundTripper, never a production base-URL override. `Prepare` fetches/checks both current and target metadata, sums and native artifact into private staging, returning `Plan` without service calls.

Compare current metadata version/full commit to the installed current executable and target metadata to the staged target executable, with selected OS/arch checked separately. Different releases need not share a commit. The helper revalidates these identities and both manifest protocol ranges against its actual pinned worker before stop.

- [ ] Write `TestReleasePinsConfirmedCandidate`, `TestReleaseOutageIsNotUpToDate`, `TestPrepareRejectsMissingPreviousMetadata`, `TestVerifyRejectsWrongAssetChecksum`, `TestExtractRejectsTraversalLinksAndSpecialFiles`. Assert later `latest` changes cannot alter candidate, incompatible current/target yields no artifact execution, all escaped/link members are refused before writes outside staging; test same/older/dev/prerelease, 429, timeout and cancellation.
- [ ] Red: `go test ./internal/update -run 'Release|Prepare|Verify|Extract' -count=1`.
- [ ] Implement immutable digest binding and revalidation at Begin; 15-minute candidate TTL, 5-minute check cache, 30-second request timeout, metadata/sums each <=1 MiB, artifact/extracted bytes <=512 MiB. Allow HTTPS redirects only to `github.com`, `release-assets.githubusercontent.com`, `objects.githubusercontent.com`; never forward credentials. Confirm actual official redirect behavior in release rehearsal; fail closed on another host. Extract only expected regular payload files and directories.
- [ ] Green: repeat focused command, then `go test ./internal/update`.
- [ ] Review and commit task files: `feat: verify exact official release artifacts before stopping services`.

## Task 3: Root Trust Store, Journal and Pinned Recovery Selection

**Files:** Create `internal/update/{paths,journal,bootstrap}.go`, `paths_linux.go`, `paths_darwin.go`, associated tests.

**Interfaces:** Produce journal/pinning contract above; `ValidateRegistration(Registration) error`, `ReadRegistration(root,id string) (Registration,error)`, `WriteRegistration(root string, reg Registration) error`. `ClientLocator { InstallationID, InstanceID, Socket, ConfigPath string; Enabled bool }`, `ReadClientLocator(configPath string,uid uint32)(ClientLocator,error)` and `WriteClientLocator(ClientLocator,uid uint32) error` expose only root-owned nonsensitive app discovery, outside the private trust root. Linux private root `/var/lib/stackstead-updater`, macOS `/Library/Application Support/StacksteadUpdater`; `bootstrap/v1/`, `workers/<sha256>/`, `registrations/<id>.json`, `jobs/<installationID>.json`, and one non-deleted flock file per installation. Public root-owned locator directories are `/etc/stackstead-updater/clients/<uid>/` and `/Library/Application Support/StacksteadUpdaterClients/<uid>/`; filenames are instance IDs, directory traversal is allowed but writes are root-only. Locator files carry no secrets and are 0644; root registration remains 0600/private. The application matches only its canonical config path and UID, rejects ambiguous locators, and never accepts a locator from app-writable config.

- [ ] Write `TestBootstrapRecoversWithoutPackageHelper`, `TestJournalRejectsUntrustedExecutablePath`, `TestTrustStoreRejectsWritableOrSymlinkParent`, `TestJournalCrashKeepsLastDurableState`. Assert unfinished job selects its digest-pinned worker after package payload deletion, unknown protocol or bad digest refuses execution, and interrupted write preserves prior parseable state. Use ownership-injection seams without root and native checks later.
- [ ] Red: `go test ./internal/update -run 'Bootstrap|Journal|TrustStore' -count=1`.
- [ ] Implement ownership/no-follow path traversal using dirfd-based operations, exclusive temp files, fsync+rename+directory fsync, bounded checksum envelope, digest-derived worker selection and lock lifetime. Bootstrap binary/protocol v1 is never overwritten by an in-flight job; persist active idle worker digest separately. Handle disk full and fsync failures before acknowledging transition.
- [ ] Green: focused command, then `go test ./internal/update`.
- [ ] Review and commit task files: `feat: retain trusted recovery workers across package replacement`.

## Task 4: Reusable SQLite Snapshot and Restore

**Files:** Create `internal/dbmaint/{snapshot,restore}.go`; modify `internal/cli/cmd_db.go`, `internal/cli/local.go`; extend `internal/cli/cmd_db_test.go`, create `internal/dbmaint/maintenance_test.go` only for newly distinct transaction behavior.

**Interfaces:** `Snapshot(ctx context.Context, dataDir, dest string) (schema int, sha256 string, err error)` and `Restore(ctx context.Context, dataDir, src, failedDir string) error`. Caller must run as registered app UID; routines hold existing `stackstead.lock` during stopped-server work, preserve ownership, validate quick_check/schema and retain failed DB/WAL/SHM.

- [ ] Run existing `go test ./internal/cli -run 'DBBackup|Restore' -count=1` before refactor. Extract existing verified snapshot/swap logic without new behavior; keep CLI output/prompts/error codes unchanged.
- [ ] Write `TestStoppedSnapshotIncludesFinalWalCommit` and `TestRestorePreservesFailedWalAndOwnership` only for behavior not already pinned by existing tests; assert final WAL-only row survives snapshot, lock holder prevents restore, failed state is archived and restored DB has pre-migration rows. Red: `go test ./internal/dbmaint -count=1`.
- [ ] Implement stopped-server snapshot/restore entry points without root ownership bypass or copying live SQLite files.
- [ ] Green: `go test ./internal/dbmaint ./internal/store ./internal/cli -run 'Snapshot|Restore|DBBackup|Ownership|Backup' -count=1`.
- [ ] Review and commit task files: `refactor: share verified database recovery with the updater`.

## Task 5: Maintenance Admission and Startup Probation

**Files:** Create `internal/maintenance/gate.go`, tests; modify `internal/api/server.go`, mutation routes in `stacks.go`, `containers.go`, `networks.go`, `handlers_password.go`, `handlers_auth.go`, `observe.go`; modify `internal/auth/service.go` and focused auth tests, `internal/stacks/ops.go`, `internal/cli/serve.go`, add focused API/serve tests.

**Interfaces:** `maintenance.New() *Gate`; `(*Gate).Enter() (release func(),err error)`, `Freeze() error`, `Thaw()`, `Frozen() bool`. `Freeze` atomically refuses if active mutations remain, otherwise blocks new entries. Async stack ops retain their admission until completion. `api.Options.Maintenance *maintenance.Gate`. Define an injectable serve startup provider `StartupProbation(ctx context.Context)(*update.Probation,error)` before `store.Open`; nil means ordinary startup. Task 5 tests the seam with a fake and does not import not-yet-created Task 6 transport packages. Task 6 wires the trusted provider. Ordinary startup remains unchanged until an enabled trusted locator is installed.

- [ ] Write `TestFreezeRefusesActiveOperation`, `TestProbationRejectsAllMutators`, `TestProbationDefersBackgroundWrites`, `TestProbationDefersStartupStackSync`, `TestProbationExpiredSessionReadDoesNotWrite`. Assert every registered POST/PUT/PATCH/DELETE mutator except updater control returns 503 while frozen, including login/logout/setup/password/token/session writes; GET reads remain available; an expired-session GET still returns 401 but leaves session rows unchanged; async admission cannot disappear after HTTP 202. Migration may write in probation, samplers/auth user writes and startup stack-registry sync may not. Observable stack-registry state must stay unchanged before release and synchronize after release. The same-schema rollback test belongs to Task 7, after transport and recovery exist.
- [ ] Red: `go test ./internal/maintenance ./internal/api ./internal/auth ./internal/cli ./internal/stacks -run 'Freeze|Probation|Admission' -count=1`.
- [ ] Implement gate before startup migration/collectors/HTTP admission; app remains frozen after restart until helper's durable commit release. Defer both background writers and startup `stackSvc.Sync(ctx)` until release; include other lazy request-triggered writers in admission. `Authenticate` must defer expired-session deletion while frozen rather than writing during GET authentication. Existing nonprobation startup/shutdown and expired-session cleanup remain unchanged. Public `/api/health` remains version-free; Docker degraded state does not fail app readiness.
- [ ] Green: focused command, then `go test ./internal/maintenance ./internal/api ./internal/auth ./internal/cli ./internal/stacks`.
- [ ] Review and commit task files: `feat: prevent writes until an update passes startup probation`.

## Task 6: Authenticated Local Transport and App Control

**Files:** Create `internal/updateipc/{server,client,peer_linux,peer_darwin}.go`, peer/transport tests; create `internal/updatecontrol/{server,client,ticket}.go`, tests; modify `internal/cli/serve.go`.

**Interfaces:** `updateipc.Backend` has `Startup(ctx,installationID,instanceID)(*update.Probation,error)`, `Status(ctx,installationID)`, `Check(ctx,installationID)`, `Begin(ctx,installationID,BeginRequest)`, `Job(ctx,installationID,jobID)`, `Cancel(ctx,installationID,jobID)` with Task 1 return types. `Serve(ctx, listener net.Listener, registrations []update.Registration, backend Backend) error`; `NewClient(socket,installationID string) *Client` implements the same operations without installationID arguments; `Startup(ctx,instanceID)` is helper/app-only and is never forwarded through the public API. IPC startup response is nullable (nil means ordinary boot), with a persisted active ticket returning the exact JobID/Nonce/Expected identity. `updatecontrol.Client` methods `Freeze(ctx,Instance) error`, `Prepare(ctx,Instance,Probation) error`, `Ready(ctx,Instance) (Readiness,error)`, `Release(ctx,Instance,jobID string) error`; server authenticates root peer, helper authenticates app UID.

- [ ] Write `TestPeerCannotOperateAnotherInstance`, `TestIPCRejectsUnknownFieldsAndOversizedRequest`, `TestControlRejectsOldBootNonce`, `TestStartupRestoresProbationTicket`. Assert unauthorized UID/root impersonation rejected, no arbitrary path/command fields accepted, both sides validate peer UID, stale health/PID alone cannot satisfy readiness, and startup returns the exact persisted current boot ticket rather than regenerating one. Use a fake Backend here; the real coordinator arrives in Task 7.
- [ ] Red: `go test ./internal/updateipc ./internal/updatecontrol ./internal/cli -run 'Peer|IPC|Control|ProbationTicket' -count=1`.
- [ ] Implement Unix HTTP transport with one-MiB max requests, 10-second ordinary control deadline, strict decoder and root-owned short socket paths (`/run/stackstead-updater` Linux, `/var/run/stackstead-updater` Darwin). Use SO_PEERCRED Linux and verified Darwin peer credentials. Root-private per-instance startup ticket is served by `Startup` over the authenticated helper socket to the registered app UID before migration. The coordinator persists `Job.Probation` before start; app-control `Prepare` only applies that existing ticket to a live app and never writes the root journal. Only the helper creates tickets. An enabled trusted client locator requires successful helper startup negotiation; unavailable helper fails startup closed rather than bypassing probation. Missing locator or explicit disabled locator retains ordinary legacy/opt-out startup. `Release` is job-specific and occurs only after durable commit; committed jobs answer ordinary startup after restart so an app cannot remain frozen indefinitely. Coordinator implements `Startup` from a bounded durable snapshot and instance membership, without acquiring the installation mutation lock or starting recovery. No request can supply helper root or supervisor path.
- [ ] Green: focused command and affected package suites; cross-compile Darwin peer code on Linux or Linux peer code on Darwin; native peer assertions must run on matching OS during Task 19.
- [ ] Review and commit task files: `feat: authenticate updater and probation control on local sockets`.

## Task 7: Durable Transaction Coordinator

**Files:** Create `internal/update/{transaction,recovery,coordinator}.go`, transaction/recovery tests; create `internal/updatehost/{interfaces,runtime}.go` with exact composition contract below and fail-closed unconfigured adapters.

**Interfaces:** Produce Coordinator above; Task 1 DTO names remain authoritative. `update.Runtime` methods `Preflight(ctx,Plan)([]ServiceState,error)`, `Quiesce(ctx,Plan,Probation) error`, `Stop(ctx,Plan) error`, `Snapshot(ctx,Plan)([]DBSnapshot,error)`, `Install(ctx,Plan,VerifiedArtifact) error`, `StartProbation(ctx,Plan,Probation) error`, `Ready(ctx,Plan,Probation)([]Readiness,error)`, `Release(ctx,Plan,Probation) error`, `Restore(ctx,Plan,[]DBSnapshot) error`, `Inspect(ctx,Job)(RecoveryAction,error)`. `Quiesce` consumes the coordinator's persisted current-identity ticket and calls Freeze then Prepare for each originally running instance. `Release` passes the persisted probation JobID to app control, including after helper restart; it never depends on in-memory StartProbation state. Before installation it also reconciles a durable keep-old recovery decision using the pre-quiesce ticket as described below. `RecoveryAction` values `keep-old`, `rollback`, `complete`, `repair-required`; inspect never authorizes unrelated package repair. `updatehost.NewRuntime(adapters map[update.InstallerKind]PackageAdapter, supervisors map[string]Supervisor, control *updatecontrol.Client) update.Runtime`. Define `PackageAdapter`, `Supervisor`, `CommandRunner` interfaces in `interfaces.go` now, with the signatures listed in Task 8, so composition compiles before concrete factories land. Coordinator also implements the Task 6 `Startup` method; nil control/unregistered adapters return `ErrUnavailable`.

- [ ] Write `TestTransactionPersistsBeforeEachMutation`, `TestTransactionDuplicateBeginIsIdempotent`, `TestRecoveryAtEveryPhase`, `TestSuccessNeverRestoresSnapshot`, `TestRollbackFailureRequiresRecovery`, `TestProbationRollbackRestoresSameSchemaData`, `TestRecoveryServesStartupWhileHoldingInstallLock`. Assert expected phase order, zero stop/install on preflight failure, final DB snapshot after stop, durable success before success Release, retry uses same job, and each injected crash resumes from journal rather than repeats destructive steps. Extend the existing persistence/recovery tests with partial-quiesce cases: first instance frozen and prepared, later Freeze fails; Freeze applied but its response is lost; Prepare fails before or after applying; helper crashes between Freeze and Prepare or during cleanup; Release applies but its response is lost. Assert current-identity ticket and original running-instance set were durable before the first Freeze, zero stop/install/DB restore on these pre-install failures, all possibly frozen old apps eventually admit writes after confirmed job-specific cleanup, and unresolved/conflicting peers retain recovery-required rather than claim cleanup. Retry uses the same pre-quiesce job/nonce; target and rollback boots each use a different freshly persisted nonce. Use real dbmaint for same-schema probation changes restored from the old final snapshot, with fake supervisor/runtime edges and real maintenance/control State where practical. Cover persisted job identity after restart and post-commit Release failure retried without snapshot restore. Recover can hold the installation lock while the restarted app successfully negotiates Startup/status and reaches readiness; Begin stays busy. Cancellation only through `verified`; later cancel returns conflict.
- [ ] Red: `go test ./internal/update -run 'Transaction|Recovery|Rollback|Success' -count=1`.
- [ ] Implement asynchronous helper-owned jobs independent of HTTP request cancellation, one installation lock, verified pinned worker before stop, current/target compatibility, disk budget for both artifacts+DB and 20% margin, 120-second readiness deadline per active instance. Startup/Status/Job read durable snapshots without waiting on that mutation lock. Before the first Freeze, durably save the quiescing phase, original `Plan.States`, and `Job.Probation` with the job ID, fresh nonce and Expected=Plan.Current.Identity. Treat every originally running instance as possibly frozen; a Freeze/Prepare transport error never proves no mutation occurred. Do not stop any app unless all required Freeze/Prepare calls succeeded. On partial quiesce failure or recovery while still pre-stop, durably choose keep-old cleanup and reconcile every possibly frozen instance with the same persisted ticket: retry Freeze, Prepare, then job-specific Release; retrying the full sequence also handles a lost Release acknowledgement. Continue cleanup for other instances after an error. Preserve the ticket and unfinished cleanup identity until every required peer is confirmed released; unavailable/conflicting peers leave recovery-required and retry, with no install, snapshot restore, unauthenticated thaw or claim that all apps were released. Do not expose terminal ordinary Startup until this cleanup is durably complete. Only after confirmed old stop replace the pre-quiesce ticket with a freshly persisted target ticket with Expected=Plan.Target.Identity before target boot; rollback gets another persisted fresh nonce and Expected=Plan.Current.Identity before old boot. Durable success never restores the snapshot, even if Release fails; retry/reconcile release using the durable job identity. Post-install failure restores native artifact then each DB before original service state. Failed rollback stays recovery-required; original stopped instances remain stopped and report installed-pending-start via message without inventing success readiness. Snapshot/Restore invoke a bounded trusted helper child under the registered app UID/GID; never call dbmaint directly in the root coordinator. Child executable/staging and input/output handles must be usable after dropping credentials: do not assume the app UID can traverse root-0700 storage. Use app-accessible validated staging or bounded inherited handles/streams to transfer private backup data. Task 13 supplies the fixed internal child role; unavailable wiring fails closed until then.
- [ ] Green: focused command, then `go test ./internal/update ./internal/updatehost`.
- [ ] Review and commit task files: `feat: recover update transactions before admitting application writes`.

## Task 8: Linux Script and Supervisor Runtime

**Files:** Create `internal/updatehost/{command,systemd,custom,script_linux}.go`, corresponding tests; create `deploy/openrc/stackstead-updater`, `deploy/openrc/stackstead-update-adapter`.

**Interfaces:** Consume interfaces declared in Task 7: `PackageAdapter { Validate(ctx,Plan,VerifiedArtifact) error; Install(ctx,Plan,VerifiedArtifact) error; Inspect(ctx,Plan)(Identity,error) }`; `Supervisor { Probe(ctx,Instance)(ServiceState,error); Stop(ctx,Instance) error; Start(ctx,Instance,ServiceState) error }`. `NewSystemd(runner CommandRunner) Supervisor`, `NewCustom(runner CommandRunner) Supervisor`, `NewScript(runner CommandRunner) PackageAdapter`. `CommandRunner.Run(ctx context.Context,uid,gid uint32,path string,args []string,stdin []byte)(stdout []byte,stderr []byte,err error)`; output bounded/sanitized, fixed environment, no shell. UID 0 allowed only for known native tools/root helpers; config/DB/app binaries execute as instance UID.

- [ ] Write `TestSystemdPreservesMaskedDisabledState`, `TestCustomSupervisorRequiresTrustedRegistration`, `TestScriptInstallPreservesConfigAndStacks`, `TestRuntimeUnknownSupervisorMakesNoChanges`. Assert custom executable and every parent root-owned; verbs exactly `probe`, `stop`, `start` with registered instance ID, JSON ServiceState output, no caller command; Alpine missing adapter gives setup-required.
- [ ] Red: `go test ./internal/updatehost -run 'Systemd|Custom|Script|UnknownSupervisor' -count=1`.
- [ ] Implement dirfd-anchored atomic payload replacement, package-ownership refusal, app identity probe as UID, fixed systemctl argv; stopped/masked app not started. Complete OpenRC helper init + bounded adapter example uses `set -euo pipefail` for bash or `set -eu` for POSIX sh, usage text and explicit errors; no eval. Custom helper itself must be independently supervised before capability ready.
- [ ] Green: focused command, then `go test ./internal/updatehost`; shell parse/check examples with available tools.
- [ ] Review and commit task files: `feat: preserve Linux supervision while updating script installations`.

## Task 9: DEB Native Adapter

**Files:** Create `internal/updatehost/deb_linux.go`, `deb_linux_test.go`; extend `runtime.go` composition.

**Interfaces:** `NewDeb(runner CommandRunner) PackageAdapter`; same Task 8 interface, no additional public DTO.

- [ ] Write `TestDebUsesExactVerifiedPackage`, `TestDebBusyDoesNotStopService`, `TestDebRollbackRestoresPackageBookkeeping`, `TestDebPartialStateRequiresBoundedRepair`. Assert query name `stackstead`, architecture and exact version before installation; mismatches stop nothing, no raw `/usr/bin` writes, previous recorded package used for rollback, no force/nodeps/delete-lock commands.
- [ ] Red: `go test ./internal/updatehost -run Deb -count=1`.
- [ ] Implement fixed `/usr/bin/dpkg-query`, `/usr/bin/dpkg-deb`, `/usr/bin/dpkg` commands, native lock/state preflight, noninteractive conffile preservation with verified flags, test/dry-run before install where supported. Diagnose only this package's state; do not run global `dpkg --configure -a` or general dependency repair. Coordinate with Task 15's trusted managed context; unavailable context fails closed.
- [ ] Green: focused command, then `go test ./internal/updatehost`.
- [ ] Review and commit task files: `feat: retain Debian package ownership during managed updates`.

## Task 10: RPM Native Adapter

**Files:** Create `internal/updatehost/rpm_linux.go`, `rpm_linux_test.go`; extend runtime composition.

**Interfaces:** `NewRPM(runner CommandRunner) PackageAdapter`.

- [ ] Write `TestRPMVerifiesNameArchVersion`, `TestRPMTestsBeforeUpgrade`, `TestRPMOldpackageOnlyForRecordedRollback`, `TestRPMBusyDoesNotStopService`. Assert `rpm --test` succeeds before real transaction; general requests cannot downgrade; only exact recorded previous artifact enables `--oldpackage`; no `--force`, `--nodeps` or manual package DB edits.
- [ ] Red: `go test ./internal/updatehost -run RPM -count=1`.
- [ ] Implement fixed RPM tool path selected from validated system paths, native metadata/state/lock checks and same interface. Preserve noreplace config, package scripts, original supervision and recovery-required for unrepairable partial state.
- [ ] Green: focused command, then `go test ./internal/updatehost`.
- [ ] Review and commit task files: `feat: preserve RPM transactions and bounded rollback`.

## Task 11: macOS Agents and Script Payload Runtime

**Files:** Create `internal/updatehost/{launchd_darwin,script_darwin,instances_darwin}.go`, Darwin tests.

**Interfaces:** `NewLaunchd(runner CommandRunner) Supervisor`; `NewDarwinScript(runner CommandRunner) PackageAdapter`; `RegisteredInstances(ctx context.Context,reg update.Registration)([]update.Instance,error)` validates all agents sharing the recorded binary.

- [ ] Write `TestLaunchdPreservesUnloadedAgents`, `TestSharedPrefixCoordinatesRegisteredUIDs`, `TestUnknownAgentBlocksBeforeStop`, `TestDarwinPrefixNeverExecutesRootCode`, `TestNoGuiSessionDoesNotSelectConsoleUser`. Assert all active registered UIDs quiesce/snapshot, unloaded instances stay unloaded, unknown running instance prevents any install, and user-home DB/config actions execute under that UID.
- [ ] Red on Darwin: `go test ./internal/updatehost -run 'Launchd|SharedPrefix|UnknownAgent|DarwinPrefix|NoGui' -count=1`; other hosts cross-compile and report native red unrun.
- [ ] Implement user-domain launchctl calls with identity from root registration, independent helper LaunchDaemon, anchored prefix writes, root-private staging and fixed credential drop. Enumerate shared-prefix running agents conservatively; cannot prove membership means setup-required. Never trust console user, inherited HOME/PATH, user prefix helper, or root writes in homes.
- [ ] Green native focused command and `go test ./internal/updatehost`; cross-build is supplemental only.
- [ ] Review and commit task files: `feat: coordinate macOS agent updates under their registered users`.

## Task 12: macOS PKG Native Adapter

**Files:** Create `internal/updatehost/pkg_darwin.go`, `pkg_darwin_test.go`.

**Interfaces:** `NewPKG(runner CommandRunner) PackageAdapter`.

- [ ] Write `TestPKGUsesUniversalVerifiedPayload`, `TestPKGChecksReceiptAndIdentity`, `TestPKGRollbackRetainsRecoveryWorker`, `TestPKGNonConsoleInitiatorRetained`. Assert receipt identifier `io.github.rcss-labs.stackstead`, exact package version and full staged app identity; matching prior updater-enabled PKG required; no tar fallback or receipt-only “success”.
- [ ] Red on Darwin: `go test ./internal/updatehost -run PKG -count=1`.
- [ ] Implement fixed `/usr/sbin/installer`, `/usr/sbin/pkgutil` and `/usr/bin/pkgutil` only where actually present/validated (choose one canonical detected system path, never caller PATH), inspect package/payload before install, target boot volume `/`, user setup suppressed/registered by trusted context. Verify downgrade receipts on isolated native test in Task 19; if native installer refuses, return recovery-required with exact local repair instructions, never raw overwrite plus stale receipt.
- [ ] Green native focused command and affected package suite; record remaining native validation explicitly.
- [ ] Review and commit task files: `feat: update macOS packages without bypassing installer receipts`.

## Task 13: Helper Entrypoints and Complete Release Payload

**Files:** Create `internal/updatehost/compose_linux.go`, `internal/updatehost/compose_darwin.go`, `cmd/stackstead-updater/{main,main_test}.go`, `cmd/stackstead-updater-bootstrap/{main,main_test}.go`, `tools/mkupdate/main.go`, tests; modify `tools/mkrelease/main.go`, tests, `Makefile`, `deploy/nfpm.yaml`; create `deploy/systemd/stackstead-updater.service`, `deploy/macos/updater.plist.in`.

**Interfaces:** `stackstead-updater version -o json`, `serve`, `worker --installation-id ID`, `status --installation-id ID`, `recover --installation-id ID`, `register --registration FILE`, `unregister --installation-id ID` are local host operations; validate root for writes. Bootstrap supports `serve`, `version`, and bounded `lifecycle --event EVENT --package NAME --version V`, resolves trusted active/pinned worker from private store; never takes executable path. Task 15 adds worker lifecycle logic; bootstrap preserves the same fixed argv verb while selecting pinned worker, so package scripts never depend on the replaceable package-owned executable. `tools/mkupdate -version V -commit FULL -dist DIR` imports `store.SupportedSchema()` and emits schema-1 `stackstead-update.json`; protocol constants from update package, source minimum 0 for current contiguous migrations (change only when a future migration removes older compatibility).

The fixed internal DB child role accepts only typed snapshot/restore work from the root runtime, with paths/identity derived from trusted registration and the recorded job, never from public IPC. It drops to registered UID/GID before any app config/data/home access and calls dbmaint there; any transfer to/from private backup storage uses validated bounded handles/streams, not root path operations inside user homes. No caller-selected executable or user-prefix application binary runs as root. Define its private request/response types with this implementation; no generic command/argv dispatcher.

- [ ] Write `TestReleaseRequiresHelperAndManifestForEveryTarget`, `TestManifestSchemaComesFromStore`, `TestBootstrapEntrypointIgnoresCallerEnvironment`, `TestReleaseRefusesMissingNativeAsset`, `TestWorkerSurvivesPayloadReplacement`, `TestWorkerDBMaintenanceUsesRegisteredUID`. Assert full matrix assets and checksum listing, platform-neutral release identity/full commit versus every staged platform executable, tar helper files and universal PKG helpers; subset/dev rehearsals explicitly nonpublishable. Assert the fixed DB child uses the registered credentials and accepts no app-supplied operation paths.
- [ ] Red: `go test ./cmd/stackstead-updater/... ./cmd/stackstead-updater-bootstrap/... ./tools/... -run 'Release|Manifest|Bootstrap|Worker' -count=1`.
- [ ] Compose concrete adapters, runtime, coordinator and IPC. Bind and serve authenticated IPC before asynchronous startup recovery: Startup/Status/Job remain available from bounded durable snapshots while recovery holds its installation lock and waits for app readiness; Begin/new work remains busy or recovery-required until recovery is safe. Exercise Task 7's recovery/startup test against real entrypoint wiring. Task 13 implements startup negotiation and fixed app-UID DB child end to end; `register` creates Task 3 client locator, `unregister` removes it only idle (or writes disabled locator for explicit opt-out). Build all three binaries per OS/arch with Make variables `UPDATER_BIN` and `BOOTSTRAP_BIN` passed to nfpm contents; universalize/ad-hoc-sign helpers in PKG. Add the platform-neutral manifest only after all archives/native packages exist, then SHA256SUMS over everything; never stamp builder-host OS/arch into its release identity. Full release requires every declared asset; partial local builds cannot claim a complete managed release. Supervisor launches bootstrap-private copy, not replaceable package helper. Promote active worker only idle after durable success/rollback; retain prior needed worker.
- [ ] Green focused command, `go test ./tools/... ./cmd/stackstead-updater/... ./cmd/stackstead-updater-bootstrap/...`; build Linux/Darwin targets into scratch output. Real release rehearsal deferred Task 19; no host daemon installation.
- [ ] Review and commit task files: `feat: ship updater binaries with mandatory compatibility metadata`.

## Task 14: Linux Script Bootstrap and Custom Supervisor Lifecycle

**Files:** Modify `install.sh`; extend `test/installer/{script,container,systemd}_test.go`; modify OpenRC artifacts from Task 8.

**Interfaces:** Existing installer flags preserved; add `--no-updater` to deliberately skip helper activation, plus host helper `register` contract from Task 13. Script install records canonical prefix/instance/root trust layout. Set client locator Enabled only after independent helper supervision is registered and reachable; unconfigured non-systemd installs keep ordinary app startup and advertise setup-required, never an enabled locator pointing to a missing helper. `--no-start` affects app only; independent helper starts by default unless `--no-updater`.

- [ ] Write `TestScriptBootstrapPrivateWorker`, `TestScriptUpdaterNoStartAndRootStage`, `TestScriptCustomSupervisorSetupRequired`, `TestScriptUninstallRefusesActiveJob`. Extend existing scenarios rather than duplicate layout tests. Assert `--root` makes zero live account/service calls; `--no-start` and stopped/disabled states preserved; old v0.2 manual bootstrap installs feature; active uninstall fails before any deletion.
- [ ] Red: `go test -tags installer -count=1 -run 'ScriptBootstrap|ScriptUpdater|ScriptCustom|ScriptUninstall|Alpine|NoStart' ./test/installer/` (inspect skips).
- [ ] Implement trusted bootstrap provisioning, hash-pinned initial idle worker, registration/unit/OpenRC instructions, helper opt-out, idempotent reinstall and idle-only known-artifact cleanup. Script must still refuse package-owned installs and preserve config/data/stacks. Dry-run prints helper and supervisor operations; do not start unconfigured custom supervisor.
- [ ] Green same focused installer scenarios, `STACKSTEAD_TEST_SHELL=dash go test -tags installer -count=1 -run 'ScriptBootstrap|ScriptUpdater|ScriptCustom|ScriptUninstall' ./test/installer/` where dash exists; affected systemd isolated paths reserved Task 19 if not authorized yet.
- [ ] Review and commit task files: `feat: bootstrap recoverable updates from Linux script installs`.

## Task 15: DEB/RPM Managed Lifecycle and Removal Guards

**Files:** Modify `cmd/stackstead-updater/main.go`, `internal/updatehost/command.go`, `internal/updatehost/runtime.go`, `deploy/pkg/deb/{preinst,postinst,prerm,postrm}`, `deploy/pkg/rpm/{pre,post,preun,postun}`, `deploy/pkg/scripts_test.go`, `deploy/nfpm.yaml`; extend `test/installer/package_test.go`.

**Interfaces:** Root-private `managed-context.json` contains `protocol`, `jobId`, `installationId`, `packageName`, `expectedVersion`, `operation` (`install`/`rollback`), `workerPID`, `phase`. It is written durably by runtime before spawning the package tool and records the already-running pinned worker PID, validated by the pinned worker through the fixed bootstrap lifecycle command `lifecycle --event EVENT --package NAME --version V`; scripts never honor an environment flag alone. External removal/upgrade while an unrelated job is active fails before payload deletion; a matching helper-owned transaction skips app/helper restart and worker removal.

For a matching managed transaction, lifecycle is a read-only bounded probe: it never takes the installation mutation lock or native package lock, starts recovery, changes journal/worker selection, or deletes state. The parent already holds the installation lock while awaiting the package tool. External idle operations instead use a separate nonblocking helper-owned reservation, acquired before payload changes and retained until the validated native package-tool process completes/exits; Begin checks that reservation under its installation lock. Reservation creation/removal is not part of the managed read-only probe. An external guard already inside a native package transaction must fail busy rather than wait for the installation lock, preventing opposite lock order. Corrupt active context fails closed. Test reservation cleanup after tool failure/exit without deleting unrelated state.

- [ ] Write `TestManagedPackageScriptsDoNotRestartAppOrHelper`, `TestExternalRemovalRefusedBeforeFilesDeleted`, `TestManagedPackageContextCannotBeSpoofed`, `TestPackageLockOrderingAvoidsDeadlock`. Assert owner/mode/live worker identity/descendant package-tool ancestry/expected version checked (a marker alone is insufficient); run a lifecycle child while the parent holds its installation lock and waits for the fake package tool, and prove it completes without either lock reentry. Unrelated external operations fail before deletion, external reservations block Begin and release on tool exit, external idle upgrade retains try-restart policy, Debian remove/reinstall/purge and RPM retain-data policy remain unchanged.
- [ ] Red: `go test -tags installer ./deploy/pkg -run 'Managed|ExternalRemoval|PackageLock|Maintainer|Remove|Purge' -count=1`.
- [ ] Implement worker lifecycle command in `cmd/stackstead-updater/main.go`, invoked by package scripts through the private bootstrap path rather than package-owned executable, with read-only context validation and no managed-path lock acquisition; declare it and the separate bounded external reservation operations in local CLI help. External preinstall guards protect script→package transitions, initial manual bootstrap allowed only idle. Install helper/registration/bootstrap from root-owned package contents; skip live setup in chroot. Cleanup only idle private artifacts, never active pinned worker, application stacks or unrelated paths.
- [ ] Green focused command, `go test -tags installer ./deploy/pkg ./internal/updatehost -count=1`; affected package installer scenarios with `-count=1`. Verify the named tests ran: script tests have the installer build tag, and no-tests-to-run/build errors/skips are not behavioral passes.
- [ ] Review and commit task files: `feat: preserve recovery state through native Linux package lifecycle`.

## Task 16: macOS Script/PKG Bootstrap and Shared Registration

**Files:** Modify `install.sh`, `deploy/macos/scripts/postinstall`, `deploy/macos/updater.plist.in`; extend `test/installer/{script_darwin,pkg_darwin,homeguard}_test.go`; adjust PKG assembly in `Makefile` only for lifecycle payload.

**Interfaces:** Use Task 13 helper registration and Task 15 managed context/read-only managed lifecycle probe plus separate external reservation guard; never reacquire the parent's installation lock from a managed PKG script. Fresh PKG may retain current console-user setup, managed upgrade exclusively uses all registered instances. Last-registration uninstall removes shared helper only idle. Nonboot-volume PKG stages files but registers/starts nothing.

- [ ] Write `TestDarwinBootstrapOutsideWritablePrefix`, `TestManagedPkgPreservesRegisteredUsers`, `TestSharedHelperRemovedOnlyAfterLastRegistration`, `TestDarwinRootStageAndNoStartDoNotLaunch`, `TestDarwinUpdaterUninstallRetainsHomes`. Assert original user-home safety and incoming PATH protections still pass, no root-owned file created inside home, no console user substitution, active transaction prevents uninstall.
- [ ] Red Darwin: `go test -tags installer -count=1 -run 'DarwinBootstrap|ManagedPkg|SharedHelper|DarwinRootStage|DarwinUpdater|RootNeverActs' ./test/installer/`.
- [ ] Implement trusted root-private bootstrap deployment, daemon registration separate from agents, user work through existing `as_user`, staged/no-start/disabled/opt-out behavior and exact helper recovery hints. Never globally chown `/usr/local`; verify anchored writes and root helper trust separately.
- [ ] Green same native focused tests, affected Darwin installer suite. Native sudo installer tests execute only on approved isolated VM/test volume in Task 19, never automatically on user's boot volume.
- [ ] Review and commit task files: `feat: provision macOS updater without elevating user agents`.

## Task 17: Authenticated Update API and Local Recovery CLI

**Files:** Create `internal/api/update.go`, `update_test.go`; modify `internal/api/server.go`, `internal/apitypes/types.go`, `internal/client/client.go`, client contract tests; create `internal/cli/cmd_update.go`, tests; modify `internal/cli/{commands,serve}.go`.

**Interfaces:** `api.UpdateAPI` exactly `Status(ctx)(UpdateStatus,error)`, `Check(ctx)(UpdateStatus,error)`, `Begin(ctx,BeginRequest)(JobView,error)`, `Job(ctx,id string)(JobView,error)`, `Cancel(ctx,id string)(JobView,error)`; updateipc.Client satisfies it. Add `Options.Updates UpdateAPI`; apitypes aliases Task 1 public DTOs to avoid divergence. Routes `GET /api/settings/update`, `POST /api/settings/update/check`, `POST /api/settings/update`, `GET /api/settings/update/jobs/{id}`, `POST .../jobs/{id}/cancel`. Go client methods `UpdateStatus`, `CheckUpdate`, `BeginUpdate`, `UpdateJob`, `CancelUpdate` with corresponding context/DTO args. Host CLI `update status|recover --installation-id ID`; the initial application CLI adds only local status/recover, leaving remote check/apply out of scope; local recovery calls helper socket/root command without OS password in HTTP.

- [ ] Write `TestUpdateRoutesRequireSessionAndSameOrigin`, `TestUpdateWorksWithoutDockerObserve`, `TestUpdateBeginReturnsDurable202`, `TestUpdateErrorsAreSanitized`, `TestUpdateCLIRecoveryWithoutWeb`. Assert malicious/cross-origin or missing-origin browser mutation rejected; nonbrowser clients require `X-Stackstead-Request: 1` and JSON (cross-origin fetch preflight disallowed), Origin if present must match effective host/scheme. Existing source has no trusted-proxy configuration; do not assume one. Support direct TLS/HTTP and the documented loopback TLS reverse-proxy deployment: use request Host (never forwarded host), honor a single `X-Forwarded-Proto: https` only from a loopback proxy peer, and reject ambiguous scheme/header forms. Test direct HTTP plus that documented TLS proxy case and a nonloopback spoof. Assert 409 busy/expired, 503 unavailable/setup/recovery, 401 session expiry; no private nonce/path leakage.
- [ ] Red: `go test ./internal/api ./internal/client ./internal/cli -run 'Update' -count=1`.
- [ ] Implement routes independent of Stacks/Observe, body bounds/strict JSON, setup-required without helper and exact installer hints. Mutation gate exempts bounded updater control while preserving auth/CSRF. Wire serve to helper using `ReadClientLocator` and canonical config-path/UID matching; no user-supplied installation IDs for server requests. Add client/CLI JSON/text help and sanitized errors.
- [ ] Green focused command, then `go test ./internal/api ./internal/client ./internal/cli`.
- [ ] Review and commit task files: `feat: expose controlled update jobs and local recovery`.

## Task 18: Settings Update Card and Reconnect State

**Files:** Create `web/src/lib/settings/{UpdateCard.svelte,UpdateCard.test.ts}`, `web/src/lib/settings/updateState.ts` only if state logic merits extraction; modify `web/src/routes/{Settings.svelte,Settings.test.ts}`, `web/src/lib/api/{types.ts,client.ts,client.test.ts}`.

**Interfaces:** TS types mirror Task 1 JSON fields exactly. API mutations set `X-Stackstead-Request: 1` and JSON content type, matching Task 17; read-only polling stays GET. Methods `updateStatus():Promise<UpdateStatus>`, `checkUpdate():Promise<UpdateStatus>`, `beginUpdate(body:BeginRequest):Promise<JobView>`, `updateJob(id:string):Promise<JobView>`, `cancelUpdate(id:string):Promise<JobView>`. Component has no install parameters beyond issued candidate and `crypto.randomUUID()` idempotency key; persisted navigation hint is only job ID, never credentials.

- [ ] Write `showsUpdateAndRequiresConfirmation`, `reconnectsUntilMatchingDurableJobSucceeds`, `reloadFindsActiveJob`, `doesNotReportNetworkFailureAsCurrent`, `showsRollbackAndLocalRecovery`, `expiresSessionThroughExistingLoginHandler`. Assert exact current→target displayed, one Begin for double click, polling outage is reconnecting, 401 uses existing handler, wrong-version status never triggers success toast, setup/disabled/busy states disable update and preserve explanation; release notes ordinary validated link.
- [ ] Red: `cd web && npm test -- src/lib/settings/UpdateCard.test.ts src/routes/Settings.test.ts src/lib/api/client.test.ts`.
- [ ] Implement accessible card/confirmation using existing components and design tokens; text explains brief UI downtime and running stacks retained. Poll 1/2/4/8/10-second capped backoff, reset on response; request cleanup on unmount. Reload queries status/current job; local job ID merely resume hint. Cancel only phases allowed by helper, distinguish installed-pending-start, rolled-back and recovery-required; no success based solely on HTTP return or health 200.
- [ ] Green focused command, then `cd web && npm run check` and `cd web && npm test` (entire affected web suite once).
- [ ] Review and commit task files: `feat: show verified update progress and recovery in Settings`.

## Task 19: Isolated Native Acceptance, Release Rehearsal and Documentation

**Files:** Create `test/installer/update_test.go`, `update_darwin_test.go`, `update_systemd_test.go`, `test/installer/testdata/update/` fixtures; extend existing `cmd/stackstead/e2e_test.go`; modify `docs/{install,security,api,cli,development}.md`, `deploy/release/RELEASING.md`, README update pointer.

**Interfaces:** Test opt-ins `STACKSTEAD_UPDATE_SYSTEMD_TEST=1` and `STACKSTEAD_UPDATE_MACOS_TEST=1`; existing `STACKSTEAD_PKG_TEST_VOLUME` must be an approved isolated volume, never `/`. Test helpers use unique names/labels and cleanup only owned resources. Fixture releases v0.3.0→v0.3.1 are explicitly updater-enabled test identities; manual v0.2.0→baseline is a separate bootstrap fixture, not managed rollback.

- [ ] Add meaningful end-to-end tests `TestUpdateSystemdScript`, `TestUpdateSystemdDeb`, `TestUpdateSystemdRPM`, `TestUpdateOpenRC`, `TestUpdateCustomSupervisor`, `TestUpdateDarwinScript`, `TestUpdateDarwinPKG`, `TestUpdateCrashAfterHelperPayloadReplacement`, `TestManualLegacyBootstrap`. Assert real package ownership/receipts, helper/app privilege separation, reboot/job persistence, restored old schema/identity on migration failure, all shared UIDs, unchanged config/stacks, disabled/no-start/staged behavior, and absence/incompatibility metadata yields zero stops/install calls. Reuse fixtures, no tests for test helpers.
- [ ] Run each new test's red against the isolated failing behavior before its fix. If implementation already satisfies an integration test, retain the coverage but report first-run green truthfully; do not engineer fake failures. Resolve concrete failures through focused regression tests, then rerun affected integration case.
- [ ] Update documentation with one-time legacy bootstrap, exact custom supervisor/OpenRC registration, helper privilege/paths, unsigned trust limitation, mandatory metadata/release order, status/recovery CLI, inactive agents, opt-out and remove/purge behavior. No extra product questions or signing prerequisite.
- [ ] Final ordinary gates once: `make check`, `make build`, `make e2e`, `make installer-test`. Inspect all skips; ordinary e2e must pass real built UI/app. If dependencies are missing, follow established permission policy rather than install silently.
- [ ] Full release rehearsal into scratch directories: `make dist VERSION=v0.3.1 DIST=/tmp/stackstead-update-dist BIN=/tmp/stackstead-update-bin`, then `make verify-release DIST=/tmp/stackstead-update-dist` and `make package-lint DIST=/tmp/stackstead-update-dist`. This creates local artifacts/containers only with established command authorization. A Linux run cannot validate/build the official universal PKG: run that portion on an approved Mac. Ensure metadata schema and stamped full commit match built app and every format/helper is listed in sums.
- [ ] Approved isolated Linux gate: `STACKSTEAD_UPDATE_SYSTEMD_TEST=1 go test -tags installer -count=1 -run 'TestUpdate(Systemd|OpenRC|CustomSupervisor|Crash)|TestManualLegacyBootstrap' -timeout 1800s ./test/installer/`; restore/boot tests must prove independent bootstrap after package-owned executable replacement. Confirm every intended test was selected; inspect build exclusions, runtime skips and no-tests-to-run output explicitly.
- [ ] Approved isolated macOS gate: `STACKSTEAD_UPDATE_MACOS_TEST=1 STACKSTEAD_PKG_TEST_VOLUME=/Volumes/stackstead-update-test go test -tags installer -count=1 -run 'TestUpdateDarwin|TestUpdateCrash|TestManualLegacy' -timeout 1800s ./test/installer/`. Run Intel/Apple silicon where available; report architecture gaps. Real root/launchd update and native rollback receipt validation cannot be replaced by mocks, cross-compile or test-volume staging alone; use disposable VM for running-agent/boot-volume behavior.
- [ ] Fresh final reviewer at normal final tier checks spec coverage, behavioral red/green evidence, package/DB failure recovery and actual native results. TypeSafe unavailable remains disclosed. If any required native gate unavailable, report implementation complete only with named unverified acceptance limitation; do not claim all-installers validation.
- [ ] Commit explicit test/docs files: `test: verify installer updates recover without losing managed state`. Re-run only failures/new changes and affected checks after fixes; no repeated full gate without concrete reason.

## Self-Review and Handoff

- Spec coverage maps to Tasks 1–3 (identity/trust/metadata/recovery), 4–7 (DB/admission/IPC/transactions), 8–12 (every runtime format/supervisor), 13–16 (all distributions/bootstrap/uninstall), 17–18 (Settings/admin/local recovery), 19 (real native lifecycle/docs).
- Builder resolutions are explicit: bootstrap path separate from package payload; worker digest/journal protocol pinned before stop; metadata+release payload introduced together; current and target must both be updater-enabled. No v0.2 managed rollback.
- Type names, public fields, adapter factories and coordinator/client boundaries above are the single contract. Platform-specific factories compile only on their OS; Task 13 creates `internal/updatehost/compose_linux.go` and `compose_darwin.go` for build-tagged composition, not references to unavailable constructors.
- All tasks land buildable. Unconfigured adapters fail closed, and no installer advertises capability until helper, metadata, runtime and supervision are present. Final release acceptance includes every format.
- Review Focus classes have named assertions in owning tasks. Verification stays focused until integration; document/mechanical/refactor changes use existing checks. No checks were run while writing this plan.
- Remaining operational evidence: isolated native Linux/macOS lifecycles and supported-macOS PKG downgrade/receipt behavior. These are validation requirements, not reasons to broaden product scope or silently declare unsupported formats.
- Finite preflight corrections recorded before Task 1: neutral release/selected-platform identities; buildable T5 provider seam with rollback tests owned by T7; recovery serves lock-free Startup/status; managed package lifecycle probes avoid lock reentry and external operations reserve independently; tagged package checks/native selector corrected; auth GET expiry cleanup and startup stack sync deferred during probation; explicit durable ticket ownership and Runtime.Release probation argument. Full task/interface evidence is retained in `.superpowers/sdd/2026-10-17-stackstead-self-update/preflight-sol.md`.

Execution handoff: use serial SDD with the already authorized scope and session preference against repeated confirmation. Read this plan and spec together, track checkboxes and red/green evidence, then implement Task 1. Code completion does not authorize installing the helper on the user's host.
