# Image Library Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A top-level Images library: named images built from uploaded Dockerfiles (with file revisions) or a GitHub path into tagged versions, usable by any service on any paired server, pushable to Docker Hub and ECR, shared over the peer link, and pruned by retention policy.

**Architecture:** A new domain package `internal/images` owns image metadata (SQLite), a content-addressed blob store and revision manifests under `DataDir/images/<slug>/`, and runs builds, pushes and fetches as operations in the existing `stacks.Ops` manager (key `image/<owner>/<slug>`), so the existing `/api/operations/{id}/stream`, web `OperationPanel` and CLI streaming carry build output unchanged. `internal/registry` (with a hand-rolled `awsauth` for SigV4, IMDSv2 and INI profiles) turns a registry connection into Docker `X-Registry-Auth` credentials. The engine client gains build, push, tag, save and load. Library references use `stackstead.invalid/<owner-id>/<slug>:<tag>` (`internal/imageref`); stacks resolve them before `up`/`pull`, and peers expose a read-only `/api/peer/images` surface plus a streaming `docker save` transfer. The web app gets `/images` screens (per-server routes, so the existing `ServerScope` forwards actions on a peer-owned image to its owner) and a "from library" image source in the service form.

**Tech Stack:** Go 1.26 stdlib + existing modules only (`modernc.org/sqlite`, `go.yaml.in/yaml/v3`, `golang.org/x/*`); React 19 + Vite + vitest; CodeMirror (already present) for the Dockerfile editor and revision diffs.

**Spec:** `docs/superpowers/specs/2026-10-23-image-library-design.md`. Read it before starting any task. The companion spec and plan for style are `docs/superpowers/specs/2026-10-23-container-exec-design.md` and `docs/superpowers/plans/2026-10-23-container-exec.md`.

## Decisions taken while planning (TypeSafe verdicts)

`typesafe:typesafe-ai` (model `jev-1.14.0`) was asked one Choice per open point, with the spec's constraints as state. Verdicts (probability of the chosen option):

| Decision | Choice | p |
| --- | --- | --- |
| Revision storage | **content-addressed blobs + manifest rows** (not full snapshots, not Dockerfile-only) | 0.98 |
| Build log storage | **one log file per version** under `DataDir/images/<slug>/logs/`, removed with the version (not a SQLite column) | 0.98 |
| Build/push/fetch execution | **reuse `stacks.Ops`** under key `image/<owner>/<slug>`; existing SSE route and panels work unchanged | 1.00 |
| Peer transfer | **single streaming GET** piped into `docker load`, verified by image ID, 60 s idle watchdog (not owner-push, not ranged download) | 0.99 |
| Retention timing | **after each successful build + a sweep every 6 h** (first 5 min after start) | 0.97 |
| Web routing | **per-server routes** (`/images`, `/images/:slug`, also under `/s/:server/`) using the existing `ServerScope` | 0.95 |
| Milestone/wave split risk | Score 1.05/2 ("moderate: workable, some collisions on shared files or interface drift") | conf 0.71 |

Mitigation for the moderate split risk: every milestone starts with a sequential foundation wave that owns the shared files (migration list, config, `apitypes`, web API module), the full SQL schema for all milestones ships in one migration in M1, and each task's Interfaces block below is the contract parallel implementers code against. ECR SigV4 is hand-rolled (user constraint, no SDK), the AWS profile source is static keys only.

Minor calls made here (marked **[call]** where they appear): image slugs are fixed at creation; every file mutation (Dockerfile save, upload, rename, delete, restore) creates a revision; failed build attempts are subject to the same retention rule as versions; the registry alias tag used for a push is removed after the push; builds, pushes and fetches use their own `image_operation_timeout` (default 60 min, chosen by the user); the Images nav entry is desktop sidebar only (the mobile tab bar keeps its five entries).

## Global Constraints

- No new Go modules. Docker only through `internal/engine`. AWS auth hand-rolled in `internal/registry/awsauth`. AWS profiles mean static keys only (`aws_access_key_id`, `aws_secret_access_key`, optional `aws_session_token`); `role_arn`, `sso_*`, `credential_process` profiles are refused with a clear error.
- Library reference: `stackstead.invalid/<owner-id>/<slug>:<tag>`; `<owner-id>` is the first 12 hex characters of the owning server's instance ID.
- Image slug: `^[a-z0-9]+(?:[-_][a-z0-9]+)*$`, at most 40 characters, derived from the name at creation and never changed (it is part of every reference). Name: 1–64 printable characters. Tag: `^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`, unique among non-failed versions of an image.
- Tag suggestion: latest tag empty → `1`; integer `n` → `n+1`; semver-like `^v?\d+(\.\d+)*$` → last numeric part +1 (`v1.4` → `v1.5`, `1.4.0` → `1.4.1`); otherwise `""` with the latest tag as the hint.
- Storage: `DataDir/images/<slug>/blobs/<sha256>` (0600 files, 0700 dirs), logs at `DataDir/images/<slug>/logs/<versionID>.log` (cap 8 MiB, then a `…(truncated)` line), temp build dirs at `DataDir/images/<slug>/tmp-<id>` removed after each build.
- Upload limits (config, bytes, integers): `image_max_file_bytes` default `134217728` (128 MiB), `image_max_context_bytes` default `1073741824` (1 GiB); env `STACKSTEAD_IMAGE_MAX_FILE_BYTES`, `STACKSTEAD_IMAGE_MAX_CONTEXT_BYTES`; each at least `1048576`. Paths in uploads, renames and GitHub tarballs: cleaned, relative, no `..`, no leading `/`, no `\`, no NUL, segments ≤ 255 bytes, total ≤ 4096; symlinks and hard links in tarballs are skipped. A Dockerfile is at most 1 MiB, non-empty, no NUL (reuse `stacks.ValidateDockerfile` rules).
- Operations: key `image/<owner-id>/<slug>` in the shared `stacks.Ops`; actions `build`, `push`, `fetch`. One build at a time per image: a second build answers 409 `build_running`. Builds, pushes and fetches are bounded by `image_operation_timeout` (config, duration, default `60m`, env `STACKSTEAD_IMAGE_OPERATION_TIMEOUT`; user decision), started via `Ops.StartTimeout`; stack operations keep `operation_timeout`.
- Action log kinds (`^[a-z]+\.[a-z]+$`): `image.create`, `image.update`, `image.delete`, `image.source` (revision), `image.build`, `image.push`, `image.fetch`, `image.retain` (one entry per removed version), `image.version` (manual version delete), `registry.create`, `registry.update`, `registry.delete`, `github.update`. Scope is the image slug for image kinds and `host` otherwise. Detail never contains a token, key or password.
- Secrets: `internal/secret.Secret` (same redaction as `peers.Secret`, which becomes an alias). Registry tokens, keys and the GitHub token are stored in SQLite, never returned by any API, never logged, and never appear in error messages or build/push output.
- Error codes (JSON `code`): `image_in_use` (409, body also carries `usedBy`), `build_running` (409), `tag_taken` (409), `revision_conflict` (409), `registry_unconfigured` (409), `arch_mismatch` (409), `image_not_built` (409), `image_too_large` (413), plus the existing `peer_*` codes.
- Peer surface for images is read-only: `GET /api/peer/images`, `GET /api/peer/images/usage`, `GET /api/peer/images/{slug}/versions/{tag}/tar`. Sharing reaches directly paired servers only. Operator routes `/api/images/...` are not on the peer denylist (forwarding manages a peer's images on the owner); `/api/settings/...` stays denied.
- Web: pin exact npm versions; `ENTRY_BUDGET` 120 kB and `TOTAL_BUDGET` 500 kB gz unchanged (the image screens are lazy chunks). Checks before each web commit: `cd web && npm run check && npx vitest run`.
- Go checks before each Go commit: `gofmt -l cmd internal tools` (empty), `go vet ./...`, `go test ./...` (scope to the touched packages while developing).
- Test-first for every behaviour task; tests assert observable behaviour (HTTP answers, rows, files, output), not mocks.
- Conventional commits explaining why, ending with
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` and
  `Claude-Session: https://claude.ai/code/session_01Y7wbvmmyu4RHW7arKEJyvi`.

## Review Focus

1. **Hostile upload paths and sizes**: `../x`, `/etc/x`, `a\b`, a NUL byte, a 300-byte segment, a file over `image_max_file_bytes`, a batch over `image_max_context_bytes` → 400/413 with the offending path and nothing written outside `DataDir/images/<slug>/`. Tests in Task 6 (`TestUploadRejectsPaths`, `TestUploadEnforcesCaps`) and Task 11 (`TestUploadRouteTraversal`).
2. **Tag reuse rules**: a succeeded tag → 409 `tag_taken`; a failed attempt frees the tag; a concurrent build → 409 `build_running`; a server restart mid-build must mark the `building` version failed (`interrupted by server restart`) so the tag frees and the image is not stuck. Tests in Task 10 (`TestBuildTagTaken`, `TestBuildFailedTagReusable`, `TestBuildBusy`, `TestRecoverInterruptedBuilds`).
3. **Secret leakage**: a failing registry test, push or GitHub call must not put the token in the HTTP answer, the operation output, the action log or the server log. Tests in Task 14 (`TestHubTestFailureRedacts`), Task 16 (`TestECRErrorRedacts`), Task 17 (`TestPushOutputRedacted`), Task 19 (`TestGitHubErrorRedacts`).
4. **Peer transfer integrity and stalls**: a loaded image whose ID differs from the owner's record is removed and the fetch fails; a transfer with no bytes for 60 s is cancelled; the service never runs a version built for another architecture. Tests in Task 25 (`TestFetchVerifiesImageID`, `TestFetchIdleWatchdog`, `TestFetchRefusesArchMismatch`).
5. **Retention never guesses**: with one peer unreachable, no version of any image is removed on that run and the result says which server blocked it; a manual delete of an in-use version is refused with the using services. Tests in Task 27 (`TestRetentionSkipsWhenPeerUnreachable`, `TestDeleteVersionInUseLists`).

---

## File Structure

| File | Responsibility |
| --- | --- |
| `internal/imageref/ref.go` (new) | Parse/format `stackstead.invalid/...` references; `OwnerID`; `Usage` type shared by stacks and images |
| `internal/secret/secret.go` (new), `internal/peers/secret.go`, `store.go` (modify) | Redacting `Secret`; `peers.Secret` becomes an alias |
| `internal/store/store.go` (modify) | Migration 8: all image-library tables |
| `internal/config/*` , `deploy/config.example.yaml` (modify) | Two upload-limit keys |
| `internal/apitypes/images.go` (new), `types.go` (modify: two fields) | Wire types for images, registries, GitHub, defaults |
| `internal/engine/images_ops.go`, `jsonstream.go` (new) | Build, push, tag, save, load, arch; Docker JSON message stream |
| `internal/images/model.go`, `store.go`, `validate.go`, `tags.go` | Domain types, SQL, slug/tag/path validation, tag suggestion |
| `internal/images/blobs.go`, `manifest.go`, `source.go`, `dockerignore.go`, `context.go` | Blob store, manifests, revisions, `.dockerignore`, build-context tar |
| `internal/images/service.go`, `build.go`, `logs.go`, `usage.go` | Service, build operation, log files, used-by |
| `internal/images/push.go` (M2), `github.go` (M3), `peerlib.go` + `fetch.go` (M4), `retention.go` (M5) | Later milestones |
| `internal/registry/*.go`, `internal/registry/awsauth/*.go` (M2) | Registry connections, Docker Hub, ECR, SigV4/IMDSv2/INI |
| `internal/stacks/library.go` (new), `service.go` (modify) | Library refs in stacks: usage, ensure-local before up, pull skipping |
| `internal/api/images.go`, `images_dto.go`, `images_settings.go` (M2/M3/M5), `images_peer.go` (M4) (new); `server.go`, `stacks.go`, `stacks_dto.go` (modify) | Routes, DTO mapping, badge enrichment |
| `internal/client/images.go`, `internal/cli/cmd_images.go` (M5) | CLI |
| `web/src/lib/api/imageTypes.ts`, `images.ts` (new); `client.ts` (modify: one spread) | Web API types and members |
| `web/src/lib/images/*.ts` (new) | Pure helpers: refs, tags, retention text, upload paths, library grouping |
| `web/src/app/images/*` (new) | Images list, image detail tabs, build dialog, file tree, library picker |
| `web/src/app/settings/RegistriesCard.tsx` (M2), `GitHubCard.tsx` (M3), `ImageDefaultsCard.tsx` (M5); `SettingsScreen.tsx` (modify) | Settings cards |
| `web/src/app/routes.tsx`, `shell/nav.ts`, `lib/shell/icons.ts`, `lib/shell/breadcrumb.ts` (modify) | Routes, nav, breadcrumbs |
| `docs/api.md`, `docs/cli.md`, `docs/security.md` (modify) | Docs |

## Execution Waves

Tasks in one wave touch disjoint files and may run concurrently in separate worktrees. A wave starts when every task it depends on is merged. Milestones are sequential and each leaves `main` shippable. Models follow CLAUDE.md tiers: **haiku** for mechanical work, **sonnet** for implementation with judgement; escalate on two failures.

**M1 — uploaded images, builds, versions, use in stacks (local only)**

| Wave | Tasks (model) | Depends on |
| --- | --- | --- |
| 1 | T1 imageref + secret (haiku) · T2 migration + config (haiku) · T3 apitypes + web API module (haiku) | — |
| 2 | T4 engine image ops (sonnet) · T5 images store/model/validation/tags (sonnet) · T6 blobs/manifest/source/dockerignore/context (sonnet) · T7 stacks library hooks (sonnet) · T8 web Images screens (sonnet) · T9 web library picker + badges (sonnet) | T1–T3 |
| 3 | T10 images.Service (sonnet) | T4, T5, T6 |
| 4 | T11 API routes + DTOs (sonnet) | T7, T10 |
| 5 | T12 M1 integration: wiring, routes, nav, docs, smoke (sonnet) | T8, T9, T11 |

**M2 — registries and push**

| Wave | Tasks | Depends on |
| --- | --- | --- |
| 1 | T13 awsauth (sonnet) · T14 registry store + Docker Hub provider (sonnet) · T15 web registries card + push UI (sonnet) | M1 |
| 2 | T16 ECR provider (sonnet) · T17 images push targets, push, auto-push (sonnet) | T13, T14 |
| 3 | T18 API routes + integration + docs (sonnet) | T15, T16, T17 |

**M3 — GitHub source**

| Wave | Tasks | Depends on |
| --- | --- | --- |
| 1 | T19 GitHub client + tarball extraction (sonnet) · T20 web GitHub card + GitHub fields (sonnet) | M2 |
| 2 | T21 images GitHub source: head view, build from tarball (sonnet) | T19 |
| 3 | T22 API routes + integration + docs (sonnet) | T20, T21 |

**M4 — sharing across peers**

| Wave | Tasks | Depends on |
| --- | --- | --- |
| 1 | T23 peer routes + merged library + usage (sonnet) · T24 web owner-aware library (sonnet) | M3 |
| 2 | T25 fetch over the peer link + ensure-local + `/api/images/fetch` (sonnet) | T23 |
| 3 | T26 two-server integration test + docs (sonnet) | T24, T25 |

**M5 — retention and CLI**

| Wave | Tasks | Depends on |
| --- | --- | --- |
| 1 | T27 used-by + retention + sweep (sonnet) · T28 default retention setting (haiku) · T29 client + CLI `images` commands (sonnet) | M4 |
| 2 | T30 integration, docs, final verification (sonnet; final review by Fable/Opus per CLAUDE.md) | T27–T29 |

---

## Milestone 1: uploaded images, builds, versions, use in stacks

### Task 1: `imageref` and `secret` packages

**Files:**
- Create: `internal/imageref/ref.go`, `internal/imageref/ref_test.go`, `internal/secret/secret.go`, `internal/secret/secret_test.go`
- Modify: `internal/peers/secret.go` (methods move to `secret`), `internal/peers/store.go` (`type Secret = secret.Secret`)

**Interfaces:**
- Produces:
  ```go
  // internal/imageref
  const Domain = "stackstead.invalid"
  type Ref struct{ Owner, Slug, Tag string }   // Owner: 12 lowercase hex chars
  func OwnerID(instanceID string) string        // first 12 chars; "" for shorter input
  func Format(owner, slug, tag string) string   // "stackstead.invalid/<owner>/<slug>:<tag>"
  func (r Ref) String() string
  func Parse(s string) (Ref, bool)              // false unless exactly domain/owner/slug:tag with a valid owner, slug and tag
  func IsLibrary(s string) bool                 // strings.HasPrefix(s, Domain+"/")
  type Usage struct{ Stack, Service string; Ref Ref }
  // internal/secret
  type Secret string   // String/GoString/LogValue/MarshalJSON return "[redacted]"; Reveal() string
  ```
  `peers.Secret` is `= secret.Secret`; `peers.NewCredential` and `HashSecret` keep their signatures.

- [ ] **Step 1: Failing tests**
  - `TestParseFormatRoundTrip`: `Format("0123456789ab","web-app","1.2")` parses back to the same `Ref`; `Parse` rejects `nginx:1`, `stackstead.invalid/x/y` (no tag), `stackstead.invalid/0123456789ab/Bad_Slug:1`, `stackstead.invalid/short/web:1`, and `stackstead/0123456789ab/web:1`.
  - `TestOwnerID`: a 32-hex instance ID → its first 12 chars; `"abc"` → `""`.
  - `TestSecretRedacts`: `fmt.Sprint`, `%#v`, `slog` text output and `json.Marshal` of `Secret("ssp_x")` never contain `ssp_x`; `Reveal()` does.
  - Existing `internal/peers` tests keep passing with the alias.
- [ ] **Step 2: Run** `go test ./internal/imageref ./internal/secret ./internal/peers`. Expected: FAIL (undefined packages).
- [ ] **Step 3: Implement.** Slug/tag rules from Global Constraints live here as unexported regexps so `Parse` and `images.ValidateSlug/ValidateTag` (Task 5) agree: export `SlugPattern` and `TagPattern` (`*regexp.Regexp`).
- [ ] **Step 4: Run** the same tests. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add library image references and a shared redacting secret type`.

---

### Task 2: Schema migration, upload-limit and image-timeout config

**Files:**
- Modify: `internal/stacks/ops.go`, `internal/stacks/ops_test.go` (or the existing ops test file), `internal/store/store.go` (append migration 8), `internal/store/store_test.go`, `internal/config/config.go`, `config_test.go`, `template.go`, `template.yaml`, `template_test.go`, `deploy/config.example.yaml`

**Interfaces:**
- Produces migration 8 (one string, append-only), tables:
  ```sql
  images(id TEXT PK, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL, source TEXT NOT NULL CHECK (source IN ('upload','github')),
         dockerfile_path TEXT NOT NULL DEFAULT 'Dockerfile', github_repo TEXT NOT NULL DEFAULT '', github_branch TEXT NOT NULL DEFAULT '',
         github_path TEXT NOT NULL DEFAULT '', build_args TEXT NOT NULL DEFAULT '{}', retention_keep_last INTEGER NOT NULL DEFAULT 0,
         retention_keep_days INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)
  image_revisions(image_id TEXT NOT NULL REFERENCES images(id) ON DELETE CASCADE, number INTEGER NOT NULL, created_at INTEGER NOT NULL,
         summary TEXT NOT NULL DEFAULT '', manifest TEXT NOT NULL, PRIMARY KEY (image_id, number))
  image_versions(id TEXT PK, image_id TEXT NOT NULL REFERENCES images(id) ON DELETE CASCADE, tag TEXT NOT NULL,
         status TEXT NOT NULL CHECK (status IN ('building','succeeded','failed')), revision INTEGER NOT NULL DEFAULT 0,
         commit_sha TEXT NOT NULL DEFAULT '', build_args TEXT NOT NULL DEFAULT '{}', arch TEXT NOT NULL DEFAULT '',
         image_digest TEXT NOT NULL DEFAULT '', size INTEGER NOT NULL DEFAULT 0, started_at INTEGER NOT NULL, ended_at INTEGER,
         error TEXT NOT NULL DEFAULT '', operation_id TEXT NOT NULL DEFAULT '')
  CREATE UNIQUE INDEX image_versions_tag_live ON image_versions(image_id, tag) WHERE status != 'failed';
  CREATE INDEX image_versions_image_started ON image_versions(image_id, started_at);
  registries(id TEXT PK, name TEXT NOT NULL UNIQUE, kind TEXT NOT NULL CHECK (kind IN ('dockerhub','ecr')), host TEXT NOT NULL DEFAULT '',
         config TEXT NOT NULL DEFAULT '{}', secrets TEXT NOT NULL DEFAULT '{}', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
         last_test_at INTEGER, last_test_ok INTEGER NOT NULL DEFAULT 0, last_test_message TEXT NOT NULL DEFAULT '')
  push_targets(id TEXT PK, image_id TEXT NOT NULL REFERENCES images(id) ON DELETE CASCADE,
         registry_id TEXT NOT NULL REFERENCES registries(id) ON DELETE CASCADE, repository TEXT NOT NULL, auto_push INTEGER NOT NULL DEFAULT 0)
  image_pushes(version_id TEXT NOT NULL REFERENCES image_versions(id) ON DELETE CASCADE,
         target_id TEXT NOT NULL REFERENCES push_targets(id) ON DELETE CASCADE,
         status TEXT NOT NULL CHECK (status IN ('pending','pushing','pushed','failed')), digest TEXT NOT NULL DEFAULT '',
         error TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL, PRIMARY KEY (version_id, target_id))
  ```
  Settings keys (existing `settings` table): `github_token`, `images_default_retention` (JSON `{"keepLast":0,"keepDays":0}`).
- Produces `Config.ImageMaxFileBytes int64` (`yaml:"image_max_file_bytes"`), `Config.ImageMaxContextBytes int64` (`yaml:"image_max_context_bytes"`) with the Global Constraints defaults and env names.
- Produces `Config.ImageOperationTimeout time.Duration` (`yaml:"image_operation_timeout"`, default `60 * time.Minute`, env `STACKSTEAD_IMAGE_OPERATION_TIMEOUT`, added to `durationKeys`).
- Produces `func (o *Ops) StartTimeout(key, action string, timeout time.Duration, fn OpFunc) (Operation, error)` in `internal/stacks/ops.go`: same as `Start` but bounded by `timeout` instead of the manager's default; `Start` keeps its behaviour.

- [ ] **Step 1: Failing tests**
  - `TestMigration8Tables` (store): after `Open`, inserting an image, a revision, a building version, then a second building version with the same tag fails with a unique violation; after updating the first to `failed`, the second insert succeeds; deleting the image cascades the revisions and versions.
  - `TestSupportedSchemaIs8`.
  - Config: `TestImageLimitDefaults`, `TestImageLimitsFromFileAndEnv` (file sets file bytes, env `STACKSTEAD_IMAGE_MAX_CONTEXT_BYTES=2097152` wins), `TestImageLimitTooSmall` (`image_max_file_bytes: 1024` → error naming the key); `TestImageOperationTimeoutDefault` (60m) and bare `image_operation_timeout: 900` rejected like `operation_timeout`; ops: `TestStartTimeoutOverridesDefault` (manager default 1h, `StartTimeout` with 50ms and a blocking fn → operation ends failed/timed out within ~1s, while `Start` with the same fn is still running); template renders both keys and round-trips through `Load`.
- [ ] **Step 2: Run** `go test ./internal/store ./internal/config ./internal/stacks -run 'Migration|Schema|Image|StartTimeout'`. Expected: FAIL.
- [ ] **Step 3: Implement.** Follow the existing env/validation patterns (integers parse with `strconv.ParseInt`). Template comments: one line each ("Largest single file in an uploaded image context (bytes)" / "Largest uploaded image context in total (bytes)").
- [ ] **Step 4: Run** `go test ./internal/store ./internal/config ./internal/cli ./internal/stacks`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add image library schema and upload limits`.

---

### Task 3: Wire types (Go and TypeScript)

**Files:**
- Create: `internal/apitypes/images.go`, `web/src/lib/api/imageTypes.ts`, `web/src/lib/api/images.ts`, `web/src/lib/api/images.test.ts`, `web/src/lib/images/ref.ts`, `tags.ts`, `retention.ts`, `upload.ts`, `library.ts`, `ops.ts` (+ a `.test.ts` each)
- Modify: `internal/apitypes/types.go` (`StackSummary.LibraryUpdates`, `ServiceDetail.Library`, `Status.Arch`), `web/src/lib/api/types.ts` (same three fields), `web/src/lib/api/client.ts` (spread `...createImagesApi(base)` into `createApi`, `...createLocalImagesApi()` into `createLocalOnlyApi`)

**Interfaces:**
- Produces (Go, JSON names camelCase, mirrored 1:1 in `imageTypes.ts`):
  ```go
  type Retention struct{ KeepLast, KeepDays int }                         // 0 = unlimited
  type GitHubSource struct{ Repo, Branch, Path string }                   // Repo "owner/name"; Path "" = repo root
  type LibraryServer struct{ ID, Name, Slug string; Local bool; Status string; Error string `json:",omitempty"` } // Status: reachable|unreachable|old
  type PushTarget struct{ ID, Registry, RegistryName, Kind, Repository string; AutoPush bool }
  type PushState struct{ Target, Registry, Repository, Status, Digest, Error string; UpdatedAt time.Time }
  type LibraryVersion struct{ ID, Tag, Status string; Revision int; Commit string; BuildArgs map[string]string; Arch, ImageID string;
                              Size int64; StartedAt time.Time; EndedAt *time.Time; Error string; OperationID string; Ref string; Pushes []PushState }
  type LibraryImage struct{ Slug, Name, Source, DockerfilePath string; Owner LibraryServer; GitHub *GitHubSource `json:",omitempty"`;
                            BuildArgs map[string]string; Retention Retention; LatestVersion *LibraryVersion `json:",omitempty"`;
                            LastBuild *LibraryVersion `json:",omitempty"`; PushTargets []PushTarget; CreatedAt, UpdatedAt time.Time }
  type Library struct{ Images []LibraryImage; Servers []LibraryServer }
  type LibraryImageDetail struct{ LibraryImage; Versions []LibraryVersion; SuggestedTag, TagHint, Arch string; Revision int }
  type LibraryUse struct{ Server, ServerName, Stack, Service string }
  type LibraryVersionDetail struct{ LibraryVersion; Log string; UsedBy []LibraryUse }
  type LibraryUsage struct{ Ref, Stack, Service string }
  type ImageFile struct{ Path string; Size int64 }
  type ImageFiles struct{ Revision int; Files []ImageFile }
  type ImageFileContent struct{ Path, Content string; Revision int }
  type ImageDockerfile struct{ Content string; Revision int }                 // PUT body and GET answer
  type ImageRevision struct{ Number int; CreatedAt time.Time; Summary string; DockerfileChanged bool }
  type ImageRevisionDetail struct{ ImageRevision; Dockerfile, Previous string; Files []ImageFile }
  type CreateImageRequest struct{ Name, Source, DockerfilePath string; GitHub *GitHubSource; BuildArgs map[string]string; Retention *Retention }
  type UpdateImageRequest struct{ Name, DockerfilePath string; GitHub *GitHubSource; BuildArgs map[string]string; Retention Retention }
  type RenameFileRequest struct{ From, To string }
  type BuildRequest struct{ Tag string; BuildArgs map[string]string; SaveArgs bool; Ref string }
  type BuildStarted struct{ Version LibraryVersion; Operation Operation }
  type PushRequest struct{ Target string }
  type PushTargetRequest struct{ RegistryID, Repository string; AutoPush bool }
  type FetchRequest struct{ Ref string }
  type ServiceLibrary struct{ Ref, Owner, OwnerName, Image, Tag, LatestTag, Arch string; Newer, Known bool }
  type GitHubHead struct{ Commit, Dockerfile, Path string }
  type GitHubSettings struct{ Configured bool; Login string }
  type GitHubTokenRequest struct{ Token string }
  type GitHubTest struct{ OK bool; Login, Message string }
  type RegistryHub struct{ Username, Token string }                           // Token write-only: never in answers
  type RegistryECR struct{ Region, Mode, AccessKeyID, SecretAccessKey, SessionToken, Profile string } // secrets write-only
  type RegistryRequest struct{ Name, Kind string; Hub *RegistryHub; ECR *RegistryECR }
  type RegistryView struct{ ID, Name, Kind, Host string; Username, Region, Mode, Profile, AccountID string; Configured bool;
                            LastTestAt *time.Time; LastTestOK bool; LastTestMessage string }
  type RegistryTest struct{ OK bool; Host, Message string }
  type ImageDefaults struct{ Retention Retention }
  // types.go additions
  StackSummary.LibraryUpdates int     `json:"libraryUpdates"`
  ServiceDetail.Library *ServiceLibrary `json:"library,omitempty"`
  Status.Arch string                  `json:"arch,omitempty"`   // this server's Docker architecture (amd64, arm64)
  ```
- Produces (web, pure helpers in `web/src/lib/images`, consumed by Tasks 8, 9, 12, 24):
  ```ts
  export function parseLibraryRef(s: string): { owner: string; slug: string; tag: string } | null      // ref.ts
  export function formatLibraryRef(owner: string, slug: string, tag: string): string
  export function isNewerAvailable(current: string, latest: string | undefined): boolean            // tags.ts: latest non-empty and != current
  export function retentionText(r: { keepLast: number; keepDays: number }): string                 // retention.ts: "Keep forever" | "Keep the last 5" | "Keep 30 days" | "Keep the last 5 and anything from the last 30 days"
  export function relativeUploadPaths(files: File[], base?: string): { path: string; blob: File }[] // upload.ts: uses webkitRelativePath when set; prefixes base
  export function groupByOwner(images: LibraryImage[]): { owner: LibraryServer; images: LibraryImage[] }[]  // library.ts: local first, then by owner name; images by name
  export function ownerIsViewed(image: LibraryImage, viewedServerId: string): boolean               // library.ts: image.owner.id === viewedServerId ('local' maps to the local owner)
  export function imageHref(image: LibraryImage, scope: { id: string; href(p: string): string }): string // library.ts: scope.href(`/images/${slug}`) when owned by the viewed server, else `/s/${owner.slug}/images/${slug}`
  export function operationSubject(op: { stack: string }): { label: string; href: string }          // ops.ts: 'image/<owner>/<slug>' → { label: 'image <slug>', href: '/images/<slug>' }; else { label: stack, href: '/stacks/<stack>' }
  ```
- Produces (web): `createImagesApi(base)` with members `images()`, `createImage`, `image(slug)`, `updateImage`, `deleteImage`, `imageFiles`, `imageFile(slug, path)`, `uploadImageFiles(slug, files: {path: string; blob: Blob}[])` (multipart, part name `f/<path>`), `renameImageFile`, `deleteImageFile`, `saveImageDockerfile(slug, content, revision)`, `imageRevisions`, `imageRevision(slug, n)`, `restoreImageRevision`, `buildImage(slug, req)`, `imageVersion(slug, tag)`, `deleteImageVersion`, `pushImageVersion(slug, tag, target)`, `addPushTarget`, `updatePushTarget`, `deletePushTarget`, `imageRegistries()`, `githubHead(slug)`, `fetchImage(ref)`; `createLocalImagesApi()` with `registries()`, `createRegistry`, `updateRegistry`, `deleteRegistry`, `testRegistry`, `githubSettings()`, `setGitHubToken`, `testGitHub`, `imageDefaults()`, `setImageDefaults`. Routes exactly as in Tasks 11, 18, 22, 25, 28.

- [ ] **Step 1: Failing tests** `images.test.ts` (fake fetch from `vitest-setup`): `uploadImageFiles` sends `multipart/form-data` with parts named `f/src/app.py`; `imageFile` encodes each path segment but keeps `/`; `createImagesApi('/api/servers/p1')` prefixes every path; `apitypes` Go test `TestImagesJSONNames` pins `libraryUpdates`, `latestVersion`, `pushTargets`, `suggestedTag`, `arch`. Helper tests: ref parse/format round trip and the rejects from Task 1's list; `isNewerAvailable`; `retentionText` four cases; `relativeUploadPaths` with and without `webkitRelativePath`; `groupByOwner` order; `imageHref` for a viewed-owner and a peer-owned image; `operationSubject` for both key shapes.
- [ ] **Step 2: Run** `cd web && npx vitest run src/lib/api/images.test.ts src/lib/images` and `go test ./internal/apitypes`. Expected: FAIL.
- [ ] **Step 3: Implement.** The JSON tags follow the file's existing camelCase convention. The multipart body is built with `FormData` and `fetch` without `Content-Type` (the browser sets the boundary); add an `upload` helper beside `request` in `images.ts` that reuses `ApiError` mapping.
- [ ] **Step 4: Run** `go test ./internal/apitypes` and `cd web && npm run check && npx vitest run src/lib/api src/lib/images`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add image library wire types, web API members and helpers`.

---

### Task 4: Engine image operations

**Files:**
- Create: `internal/engine/images_ops.go`, `internal/engine/jsonstream.go`, `internal/engine/images_ops_test.go`
- Modify: `internal/engine/integration_test.go` (one build+save+load case)

**Interfaces:**
- Produces:
  ```go
  type BuildOptions struct{ Tags []string; Dockerfile string; BuildArgs, Labels map[string]string }
  type BuildResult struct{ ImageID string }   // from the final aux {"ID": "sha256:..."}
  type BuildError struct{ Message string }     // Docker's {"error": ...}; Error() = "build failed: " + Message
  type RegistryAuth struct{ Username, Password, ServerAddress string }
  func (c *Client) Build(ctx context.Context, contextTar io.Reader, opts BuildOptions, out func(line string)) (BuildResult, error)
  func (c *Client) Push(ctx context.Context, ref string, auth RegistryAuth, out func(line string)) (digest string, err error)
  func (c *Client) TagImage(ctx context.Context, source, repo, tag string) error
  func (c *Client) SaveImage(ctx context.Context, ref string) (io.ReadCloser, error)   // body bound by ctx only
  func (c *Client) LoadImage(ctx context.Context, tar io.Reader) error
  func (c *Client) Arch(ctx context.Context) (string, error)                            // /version "Arch": amd64|arm64|...
  ```
  Paths: `POST /v{v}/build?t=<tag>&t=...&dockerfile=<path>&buildargs=<json>&labels=<json>&rm=1&forcerm=1` (body `application/x-tar`); `POST /v{v}/images/{ref}/push?tag=<tag>` with `X-Registry-Auth: base64url(JSON)`; `POST /v{v}/images/{source}/tag?repo=&tag=`; `GET /v{v}/images/{ref}/get`; `POST /v{v}/images/load?quiet=1`. Header wait uses the client timeout; bodies stream under the caller's ctx. `out` receives `stream` text split on newlines (trailing newline stripped) and, for push, `status` lines as `<id>: <status> <progress>`; an `error` message ends the call with `*BuildError` (build) or an error containing the message (push/load). 404 maps to `ErrImageNotFound`.

- [ ] **Step 1: Failing tests** against the package's unix-socket fake (`serveVer` in `client_test.go`):
  - `TestBuildSendsContextAndOptions`: asserts method, path and query (`t`, `dockerfile`, `buildargs` JSON, `labels` JSON, `rm=1`, `forcerm=1`), `Content-Type: application/x-tar`, the body bytes equal the tar passed in; the fake answers `{"stream":"Step 1/2 : FROM x\n"}{"stream":"ok\n"}{"aux":{"ID":"sha256:abc"}}` → `out` got `Step 1/2 : FROM x`, `ok`; `ImageID == "sha256:abc"`.
  - `TestBuildErrorMessage`: `{"errorDetail":{"message":"boom"},"error":"boom"}` → `*BuildError{Message:"boom"}`; earlier stream lines were still delivered.
  - `TestPushAuthHeaderAndDigest`: header decodes to `{"username":"u","password":"p","serveraddress":"h"}`; `{"status":"Pushing","id":"l1"}{"aux":{"Digest":"sha256:d"}}` → digest `sha256:d`, `out` got `l1: Pushing`.
  - `TestPushErrorNoSecretInError`: `{"error":"denied"}` → error text is `push failed: denied` and does not contain `p`'s value.
  - `TestTagImageQuery`, `TestSaveImageStreamsBody` (body is read lazily and ends when the fake closes), `TestLoadImageSendsTarAndErrors`, `TestArch` (`{"Arch":"arm64"}` → `arm64`), `TestImageOps404` → `ErrImageNotFound`.
- [ ] **Step 2: Run** `go test ./internal/engine -run 'Build|Push|TagImage|SaveImage|LoadImage|Arch|ImageOps' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.** Add a `postStream(caller, path string, body io.Reader, contentType string, headers http.Header) (*http.Response, context.CancelFunc, error)` beside `openStream` (same header-timeout and non-2xx handling). `jsonstream.go`: `decodeJSONStream(r io.Reader, fn func(msg jsonMessage) error) error` with `jsonMessage{Stream, Status, ID, Progress, Error string; ErrorDetail struct{Message string}; Aux json.RawMessage}`; a line longer than 1 MiB is an error.
- [ ] **Step 4: Run** `go test -race ./internal/engine/...`. Expected: PASS. Integration (`-tags integration`, skipped without Docker): `TestImageOpsIntegration` builds `FROM busybox` with a label, saves it, removes it, loads the save, and inspects the same ID.
- [ ] **Step 5: Commit** `feat: add build, push, save and load to the engine client`.

---

### Task 5: Images store, model, validation and tag suggestion

**Files:**
- Create: `internal/images/model.go`, `store.go`, `validate.go`, `tags.go`, `store_test.go`, `validate_test.go`, `tags_test.go`

**Interfaces:**
- Consumes: Task 2 schema, `imageref.SlugPattern`/`TagPattern`.
- Produces:
  ```go
  type Source string; const ( SourceUpload Source = "upload"; SourceGitHub Source = "github" )
  type Retention struct{ KeepLast, KeepDays int }
  type GitHubSource struct{ Repo, Branch, Path string }
  type Image struct{ ID, Slug, Name string; Source Source; DockerfilePath string; GitHub GitHubSource; BuildArgs map[string]string;
                     Retention Retention; CreatedAt, UpdatedAt time.Time }
  type VersionStatus string; const ( VersionBuilding VersionStatus = "building"; VersionSucceeded = "succeeded"; VersionFailed = "failed" )
  type Version struct{ ID, ImageID, Tag string; Status VersionStatus; Revision int; Commit string; BuildArgs map[string]string;
                       Arch, ImageDigest string; Size int64; StartedAt time.Time; EndedAt *time.Time; Error, OperationID string }
  type VersionResult struct{ Status VersionStatus; Arch, ImageDigest string; Size int64; Error string }
  type Revision struct{ ImageID string; Number int; CreatedAt time.Time; Summary string; ManifestJSON string }
  type PushTarget struct{ ID, ImageID, RegistryID, Repository string; AutoPush bool }
  type PushStatus string; const ( PushPending PushStatus = "pending"; PushPushing = "pushing"; PushPushed = "pushed"; PushFailed = "failed" )
  type Push struct{ VersionID, TargetID string; Status PushStatus; Digest, Error string; UpdatedAt time.Time }
  var ErrImageExists, ErrImageNotFound, ErrVersionNotFound, ErrTagTaken, ErrBuildRunning, ErrRevisionNotFound, ErrTargetNotFound error
  type ValidationError = stacks.ValidationError  // reuse (field errors → 400)
  func ValidateName(name string) error; func Slugify(name string) string; func ValidateSlug(slug string) error
  func ValidateTag(tag string) error
  func ValidateBuildArgs(args map[string]string) error   // keys ^[A-Za-z_][A-Za-z0-9_]*$, ≤ 64 keys, values ≤ 4 KiB, no NUL
  func SuggestTag(latest string) (suggest, hint string)
  type Store struct{ /* db, now */ }; func NewStore(db *sql.DB, now func() time.Time) *Store
  func (s *Store) CreateImage(ctx, img Image) (Image, error)        // fills ID (16 random hex), timestamps; ErrImageExists on slug
  func (s *Store) Image(ctx, slug string) (Image, error); ImageByID(ctx, id string) (Image, error); Images(ctx) ([]Image, error) // by name
  func (s *Store) UpdateImage(ctx, img Image) error; DeleteImage(ctx, id string) error
  func (s *Store) AddRevision(ctx, imageID, summary, manifestJSON string) (Revision, error)   // number = max+1
  func (s *Store) Revisions(ctx, imageID string) ([]Revision, error)   // newest first
  func (s *Store) Revision(ctx, imageID string, n int) (Revision, error); LatestRevision(ctx, imageID string) (Revision, bool, error)
  func (s *Store) StartVersion(ctx, v Version) (Version, error)   // ErrTagTaken (unique index), ErrBuildRunning (a building row exists)
  func (s *Store) FinishVersion(ctx, id string, r VersionResult) error
  func (s *Store) Versions(ctx, imageID string) ([]Version, error)  // newest first
  func (s *Store) Version(ctx, imageID, tag string) (Version, error)  // the non-failed row with that tag
  func (s *Store) VersionByID(ctx, id string) (Version, error); DeleteVersion(ctx, id string) error
  func (s *Store) LatestSucceeded(ctx, imageID string) (Version, bool, error)
  func (s *Store) BuildingVersions(ctx) ([]Version, error)           // across images, for restart recovery
  func (s *Store) AddPushTarget(ctx, t PushTarget) (PushTarget, error); PushTargets(ctx, imageID string) ([]PushTarget, error)
  func (s *Store) UpdatePushTarget(ctx, t PushTarget) error; DeletePushTarget(ctx, id string) error
  func (s *Store) SetPush(ctx, p Push) error; Pushes(ctx, versionIDs []string) (map[string][]Push, error)
  ```

- [ ] **Step 1: Failing tests** (`store.Open` on a temp file, as `internal/peers/store_test.go` does):
  - `TestCreateImageSlugUnique` (`ErrImageExists`), `TestImageRoundTrip` (all fields including build args and retention survive).
  - `TestRevisionsNumberFromOne` and newest-first order.
  - `TestStartVersionTagTaken`: succeeded `1.0` → start `1.0` → `ErrTagTaken`; `TestStartVersionBusy` (a building row) → `ErrBuildRunning`; `TestFailedTagReusable`.
  - `TestLatestSucceededSkipsFailedAndBuilding`.
  - `TestDeleteImageCascades` (revisions, versions, targets, pushes gone).
  - `validate_test.go` table: names, slugs (`"My App!"` → `my-app`), tags, build args.
  - `tags_test.go` table per Global Constraints: `""`→`1`; `7`→`8`; `1.4.0`→`1.4.1`; `v1.4`→`v1.5`; `2026.10`→`2026.11`; `latest`→`""` with hint `latest`.
- [ ] **Step 2: Run** `go test ./internal/images`. Expected: FAIL.
- [ ] **Step 3: Implement.** Map the unique-index violation on `image_versions_tag_live` to `ErrTagTaken` with the same `isUniqueViolation` approach as `stacks/registry.go`.
- [ ] **Step 4: Run** `go test ./internal/images`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add image library store, validation and tag suggestion`.

---

### Task 6: Blobs, manifests, revisions on disk, `.dockerignore`, build context

**Files:**
- Create: `internal/images/blobs.go`, `manifest.go`, `source.go`, `dockerignore.go`, `context.go`, and `_test.go` files for each

**Interfaces:**
- Produces:
  ```go
  func ValidatePath(p string) (clean string, err error)           // Global Constraints path rules; ErrBadPath names the path
  type FileEntry struct{ Hash string; Size int64; Mode uint32 }   // Mode: 0644 or 0755 only
  type Manifest map[string]FileEntry                              // key: clean slash path
  func ParseManifest(s string) (Manifest, error); func (m Manifest) JSON() string   // sorted keys
  func (m Manifest) Files() []ImageFileInfo                        // sorted; ImageFileInfo{Path string; Size int64}
  type Blobs struct{ /* dir */ }; func OpenBlobs(dir string) (*Blobs, error)  // mkdir 0700
  func (b *Blobs) Put(r io.Reader, maxBytes int64) (hash string, size int64, err error)  // ErrFileTooLarge when r exceeds maxBytes; writes tmp then renames; existing hash is a no-op
  func (b *Blobs) Open(hash string) (*os.File, error); func (b *Blobs) Remove(hash string) error
  func (b *Blobs) GC(keep map[string]bool) (removed int, err error)
  type Limits struct{ MaxFileBytes, MaxContextBytes int64 }
  var ErrFileTooLarge, ErrContextTooLarge, ErrBadPath error        // wrap with the path in the message
  type UploadFile struct{ Path string; Content io.Reader }
  // Pure manifest edits; every call returns the new manifest and a one-line summary ("uploaded 3 files", "renamed a → b", "deleted x", "edited Dockerfile", "restored revision 4")
  func ApplyUpload(m Manifest, b *Blobs, files []UploadFile, lim Limits) (Manifest, string, error)
  func ApplyRename(m Manifest, from, to string) (Manifest, string, error)     // from may be a directory prefix; ErrBadPath / fs.ErrNotExist / fs.ErrExist
  func ApplyDelete(m Manifest, path string) (Manifest, string, error)
  func ApplyDockerfile(m Manifest, b *Blobs, path string, content []byte) (Manifest, string, error)
  func ParseDockerignore(src []byte) (*Ignore, error); func (i *Ignore) Excluded(path string) bool   // Docker semantics: Go filepath.Match per segment, `**`, `!` negation, last match wins, Dockerfile/.dockerignore never excluded
  func Materialize(dir string, m Manifest, b *Blobs) error         // hard-links (copies on EPERM/ENOTSUP) each blob to dir/<path>; dir must be empty
  func WriteContext(w io.Writer, dir string) error                 // tar of dir honouring dir/.dockerignore; regular files only (symlinks skipped); mtime epoch 0; stable order
  const DefaultDockerfile = "FROM alpine:3.20\n# Add build steps below, for example:\n# RUN apk add --no-cache curl\n"
  ```
  Paths in `Manifest` keys never start with `./` or `/`.

- [ ] **Step 1: Failing tests**
  - `TestBlobsPutDedupAndCap`: same content twice → one file, same hash; 11 bytes with cap 10 → `ErrFileTooLarge` and no file left; file mode is 0600 and the dir 0700.
  - `TestValidatePath` table: every Review Focus 1 case rejected, `./a/b` → `a/b`, `a//b` → `a/b`.
  - `TestUploadRejectsPaths` (Review Focus 1): each hostile path → `ErrBadPath` naming the path; manifest unchanged; nothing under `dir` besides existing blobs.
  - `TestUploadEnforcesCaps`: total over `MaxContextBytes` → `ErrContextTooLarge`, no partial manifest change.
  - `TestRenameDirectoryPrefix`: `src/` → `lib/` renames every child; renaming onto an existing path → `fs.ErrExist`.
  - `TestDockerignore` table: `*.log`, `!keep.log`, `node_modules`, `**/tmp`, `a/*/b`, a pattern excluding `Dockerfile` has no effect.
  - `TestMaterializeAndWriteContext`: a manifest with `Dockerfile`, `src/app.py`, `.dockerignore` (`*.md`), `README.md` → tar has `Dockerfile`, `.dockerignore`, `src/app.py` only, in sorted order, modes preserved, no symlink entries; `Materialize` into a non-empty dir fails.
- [ ] **Step 2: Run** `go test ./internal/images -run 'Blobs|Upload|Rename|Delete|Dockerignore|Materialize|Context'`. Expected: FAIL.
- [ ] **Step 3: Implement.** `Materialize` and `WriteContext` go through `os.OpenRoot(dir)` so a link swapped in between cannot escape (same reasoning as `stacks/dockerfile.go`).
- [ ] **Step 4: Run** `go test -race ./internal/images`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add content-addressed image sources and build contexts`.

---

### Task 7: Library references in stacks

**Files:**
- Create: `internal/stacks/library.go`, `internal/stacks/library_test.go`
- Modify: `internal/stacks/service.go` (`Deps.Library`, `Summary.LibraryRefs`, `Act` pull/up handling, `ActService`), `internal/stacks/export_test.go` if needed

**Interfaces:**
- Consumes: `imageref` (Task 1).
- Produces:
  ```go
  // library.go
  type LibraryFetcher interface{ EnsureLocal(ctx context.Context, ref imageref.Ref, out func(string)) error }
  // Deps gains: Library LibraryFetcher   // nil: library refs are left to compose (as before)
  // Summary gains: LibraryRefs []string  // every library reference used by the stack's services (sorted, unique); filled by describe
  func (s *Service) LibraryUsage(ctx context.Context) ([]imageref.Usage, error)   // every service's image: that parses as a library ref, all stacks; unreadable stacks are skipped
  ```
  Behaviour: `Act(up)` and `ActService(up)` run `Library.EnsureLocal` for each library ref of the affected services inside the operation (output `ensuring <ref>` then the fetcher's lines) before `compose`; an error ends the operation as failed. `Act(pull)` runs `compose pull -- <services whose image is not a library ref>` (no `pull` at all when there are none) after ensuring the library ones; `restart`, `down`, `build` are unchanged.

- [ ] **Step 1: Failing tests** (fake compose/engine as in `service_test.go`, a recording fake fetcher):
  - `TestLibraryUsageAcrossStacks`: two stacks, one library ref each plus `nginx` → two usages with stack/service/ref.
  - `TestSummaryLibraryRefs`.
  - `TestUpEnsuresLibraryImages`: fetcher called with the parsed ref before `compose up`; output contains `ensuring stackstead.invalid/...`; fetcher error → operation `failed` with the message and compose never ran.
  - `TestPullSkipsLibraryServices`: services `web` (library) and `db` (nginx) → compose args `pull -- db`; both library → no compose call, operation succeeds.
  - `TestNilLibraryLeavesComposeAlone`.
- [ ] **Step 2: Run** `go test ./internal/stacks -run 'Library|Pull'`. Expected: FAIL.
- [ ] **Step 3: Implement.** `composeArgs["pull"]` stays as the default; `Act` computes the pull args from the parsed file when `s.library != nil`.
- [ ] **Step 4: Run** `go test -race ./internal/stacks/...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: resolve library image references before stack actions`.

---

### Task 8: Web Images screens

**Files:**
- Create: `web/src/app/images/ImagesScreen.tsx`, `ImageDetailScreen.tsx`, `SourceTab.tsx`, `FileTree.tsx`, `RevisionsTab.tsx`, `VersionsTab.tsx`, `BuildDialog.tsx`, `ImageSettingsTab.tsx`, `NewImageDialog.tsx`, module CSS files, `imagesTestKit.tsx`, `images.test.tsx`, `imageDetail.test.tsx`, `sourceTab.test.tsx`, `versionsTab.test.tsx`; `web/src/app/routes/Images.tsx`, `web/src/app/routes/ImageDetail.tsx`

**Interfaces:**
- Consumes: Task 3 types, `createImagesApi` and the `lib/images` helpers; `useServer()` (`api`, `href`, `id`, `trackOperation`, `operationsRefresh`); `OperationPanel`; `DiffView` (`web/src/app/stacks/DiffView.tsx`) and `createEditor` from `lib/stacks/cm.ts` for the Dockerfile editor.
- Produces: screens as default exports from `web/src/app/routes/Images.tsx` and `ImageDetail.tsx` (wrappers like `routes/Settings.tsx`); `ImageDetailScreen` reads `:slug` and `?tab=source|revisions|versions|settings`; `export function BuildDialog(props: { image: LibraryImageDetail; onStarted(started: BuildStarted): void; onClose(): void })`; `export function FileTree(props: { files: ImageFile[]; onUpload(files: {path: string; blob: File}[]): void; onRename(from: string, to: string): void; onDelete(path: string): void })`.

- [ ] **Step 1: Failing tests** (`installFakeFetch`, `mountAt` from the existing test kits):
  - `images.test.tsx`: list renders name, source, owner, latest tag, last build status (badge from `operationStateLook`), push target count; "New image" opens the dialog; creating posts `{name, source:'upload', dockerfilePath:'Dockerfile'}` and navigates to the detail; a peer-owned row links to `/s/<ownerSlug>/images/<slug>` (`imageHref`).
  - `imageDetail.test.tsx`: tabs from `?tab=`; header shows the full reference of the latest version with a copy button; Build opens the dialog prefilled with `suggestedTag` and build arg defaults; submitting posts `BuildRequest` and shows an `OperationPanel` for the returned operation id; a 409 `build_running` shows the message inline.
  - `sourceTab.test.tsx`: Dockerfile editor loads `GET .../dockerfile` content and saves `{content, revision}`; a 409 `revision_conflict` shows "changed since you loaded it" with a Reload action; file tree lists files, upload (file and folder inputs) posts multipart with relative paths; rename and delete call their routes and refresh.
  - Revisions: list with time and summary; selecting one shows a diff of `previous` → `dockerfile`; Restore posts and refreshes.
  - `versionsTab.test.tsx`: rows show tag, revision/commit, arch, build args (plain text, with the "not secrets" note), status, used-by count; opening a version shows the log; Delete asks for confirmation; a 409 `image_in_use` lists the services from `usedBy`.
  - Settings tab: build arg defaults editor (`ListEditor`), retention fields, Save sends `UpdateImageRequest`; Delete image with `ConfirmDialog`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/images`. Expected: FAIL.
- [ ] **Step 3: Implement** with the existing components (`Tabs`, `Dialog`, `ConfirmDialog`, `Field`, `ListEditor`, `StatusBadge`, `Tag`, `EmptyState`). The Dockerfile editor is a lazy chunk like `DockerfilePanel`; nothing from CodeMirror lands in the entry chunk.
- [ ] **Step 4: Run** `cd web && npm run check && npx vitest run`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add the Images screens`.

---

### Task 9: Web "from library" service source and badges

**Files:**
- Create: `web/src/app/stacks/LibraryPicker.tsx`, `LibraryPicker.module.css`, `libraryPicker.test.tsx`
- Modify: `web/src/lib/compose/form.ts` (+ `form.test.ts`), `web/src/app/stacks/ServiceForm.tsx` (+ `serviceForm.test.tsx`), `web/src/app/detail/ServiceOverview.tsx`, `web/src/app/overview/StackRow.tsx`, `web/src/lib/stacks/summary.ts` (+ tests)

**Interfaces:**
- Consumes: Task 3 types (`ServiceLibrary`, `StackSummary.libraryUpdates`, `Status.arch`) and helpers (`parseLibraryRef`, `formatLibraryRef`, `groupByOwner`).
- Produces:
  ```ts
  // form.ts
  ServiceForm.source: 'image' | 'build' | 'dockerfile' | 'library'
  ServiceForm.library: { ref: string } | null        // chosen reference
  // toForm: an image that parses as a library ref → source 'library'; fromForm: source 'library' writes config.image = library.ref
  // ServiceForm.tsx: SOURCES gains ['library', 'From library']; the group renders <LibraryPicker>
  export interface LibraryPickerProps { value: string; onChange(ref: string): void; load(): Promise<Library>; localArch?: string }
  // StackRow: `libraryUpdates > 0` → a Tag "n update(s)" ; ServiceOverview: `service.library` → "<image>:<tag> (owner)" and a "new version available: <latestTag>" badge when `newer`
  ```
  The picker lists images grouped by owner, then versions (succeeded only); a version whose `arch` differs from `localArch` is shown disabled with "built for arm64; this server is amd64".

- [ ] **Step 1: Failing tests**
  - `form.test.ts`: `toForm({image:'stackstead.invalid/0123456789ab/web:1'})` → `source 'library'`; `fromForm` writes that image back and never sets `build`; `validateForm` requires a chosen reference for `'library'`.
  - `libraryPicker.test.tsx`: loads the library, groups by owner, selecting a version calls `onChange` with the formatted ref; a mismatched-arch version is disabled with the note; an empty library shows "No images yet" with a link to `/images`.
  - `serviceForm.test.tsx`: the new source option renders the picker; switching back to Image keeps the ref as the image text.
  - `StackRow`/`ServiceOverview` tests: the badge appears only when `libraryUpdates > 0` / `library.newer`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/lib/compose src/app/stacks src/app/detail src/app/overview`. Expected: FAIL.
- [ ] **Step 3: Implement.** `localArch` comes from `GET /api/status` (`arch`, Task 3 type; served from Task 11) via the existing status store.
- [ ] **Step 4: Run** `cd web && npm run check && npx vitest run`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: pick library images in the service form and flag newer versions`.

---

### Task 10: `images.Service` (create, source, revisions, build, versions)

**Files:**
- Create: `internal/images/service.go`, `build.go`, `logs.go`, `usage.go`, `service_test.go`, `build_test.go`

**Interfaces:**
- Consumes: Tasks 4–6, `stacks.Ops`, `actionlog.Entry`, `imageref`.
- Produces:
  ```go
  type Engine interface {
      Build(ctx context.Context, contextTar io.Reader, opts engine.BuildOptions, out func(string)) (engine.BuildResult, error)
      Push(ctx context.Context, ref string, auth engine.RegistryAuth, out func(string)) (string, error)
      TagImage(ctx context.Context, source, repo, tag string) error
      SaveImage(ctx context.Context, ref string) (io.ReadCloser, error)
      LoadImage(ctx context.Context, tar io.Reader) error
      InspectImage(ctx context.Context, ref string) (engine.ImageInspect, error)
      RemoveImage(ctx context.Context, ref string) (engine.ImageDeleteResult, error)
      Arch(ctx context.Context) (string, error)
  }
  type Self struct{ InstanceID, Name, Slug string }
  type ActionLog interface{ Append(context.Context, actionlog.Entry) error }
  type UsageSource interface{ LibraryUsage(ctx context.Context) ([]imageref.Usage, error) }   // *stacks.Service
  type Deps struct{ Store *Store; Ops *stacks.Ops; Engine Engine; Dir string; Limits Limits; OpTimeout time.Duration; Actions ActionLog;
                    Self func(ctx context.Context) (Self, error); Usage UsageSource; Now func() time.Time; Logger *slog.Logger
                    // later milestones add: Registries (T17), GitHub + Settings (T21), Peers (T23) }
  func NewService(d Deps) *Service
  func (s *Service) SetUsage(u UsageSource)              // late binding: stacks needs the service as its LibraryFetcher, so serve constructs images first
  func (s *Service) Start(ctx context.Context) error     // marks leftover 'building' versions failed ("interrupted by server restart"); called by serve
  func OpKey(owner, slug string) string                   // "image/<owner>/<slug>"
  type CreateRequest struct{ Name string; Source Source; DockerfilePath string; GitHub GitHubSource; BuildArgs map[string]string; Retention *Retention }
  type UpdateRequest struct{ Name, DockerfilePath string; GitHub GitHubSource; BuildArgs map[string]string; Retention Retention }
  func (s *Service) Create(ctx, user string, req CreateRequest) (Image, error)   // upload source: revision 1 = DefaultDockerfile at DockerfilePath; Retention nil → 0/0 (M5 replaces this with the Settings default)
  func (s *Service) Get(ctx, slug string) (Image, error)
  func (s *Service) Update(ctx, user, slug string, req UpdateRequest) (Image, error); Delete(ctx, user, slug string) error  // *InUseError when any version is in use
  type ServerInfo struct{ ID, Name, Slug string; Local bool; Status, Error string }   // Status reachable|unreachable|old
  type LibraryEntry struct{ Image; Owner ServerInfo; Versions []Version; PushTargets []PushTarget; Pushes map[string][]Push }
  type Library struct{ Images []LibraryEntry; Servers []ServerInfo }
  func (s *Service) Library(ctx context.Context) (Library, error)   // M1: own images only, Servers = [self]; M4 merges peers
  type Detail struct{ Image; Versions []Version; Pushes map[string][]Push; PushTargets []PushTarget; Revision int; SuggestedTag, TagHint, Arch string }
  func (s *Service) Detail(ctx, slug string) (Detail, error)
  func (s *Service) Files(ctx, slug string) (Manifest, int, error); ReadFile(ctx, slug, path string) ([]byte, error)   // ErrFileTooLarge over 1 MiB
  func (s *Service) Dockerfile(ctx, slug string) ([]byte, int, error)
  func (s *Service) SaveDockerfile(ctx, user, slug string, content []byte, revision int) (Revision, error)   // ErrRevisionConflict unless revision == latest
  func (s *Service) Upload(ctx, user, slug string, files []UploadFile) (Revision, error)
  func (s *Service) Rename(ctx, user, slug, from, to string) (Revision, error); DeleteFile(ctx, user, slug, path string) (Revision, error)
  func (s *Service) Revisions(ctx, slug string) ([]Revision, error); RevisionDockerfile(ctx, slug string, n int) ([]byte, error)
  func (s *Service) Restore(ctx, user, slug string, n int) (Revision, error)
  type BuildRequest struct{ Tag string; BuildArgs map[string]string; SaveArgs bool; Ref string }
  func (s *Service) Build(ctx, user, slug string, req BuildRequest) (Version, stacks.Operation, error)
  type UsedBy struct{ Server, ServerName, Stack, Service string }
  type InUseError struct{ UsedBy []UsedBy; Unreachable []ServerInfo }; func (e *InUseError) Error() string   // "in use by <stack>/<service> on <server>, ..." or "cannot confirm use: <server> could not be reached" (M4)
  var ErrRevisionConflict error
  type VersionDetail struct{ Version; Log string; UsedBy []UsedBy }
  func (s *Service) VersionDetail(ctx, slug, tag string) (VersionDetail, error)
  func (s *Service) DeleteVersion(ctx, user, slug, tag string) error
  func (s *Service) Ref(owner, slug, tag string) string   // imageref.Format
  func (s *Service) EnsureLocal(ctx context.Context, ref imageref.Ref, out func(string)) error   // M1: owner must be self and the version succeeded and present in Docker, else error "version <tag> of <slug> is not built on this server" (code image_not_built); M4 adds peer fetch
  type ResolvedRef struct{ Known bool; Owner, OwnerName, Image, Tag, LatestTag, Arch string; Newer bool }
  func (s *Service) Resolve(ctx context.Context, refs []string) (map[string]ResolvedRef, error)   // M1: local images only
  ```
  Build operation (`Ops.StartTimeout(OpKey, "build", deps.OpTimeout, fn)`): `StartVersion(building)` first (so `ErrTagTaken`/`ErrBuildRunning` answer before any operation); fn materializes the latest revision into `tmp-<versionID>`, writes the context tar through an `io.Pipe` into `Engine.Build` with tags `[ref]`, `Dockerfile` = `DockerfilePath`, build args = defaults overridden by the request, labels `stackstead.image=<slug>`, `stackstead.version=<id>`; then `InspectImage(ref)` for ID, size and arch; `FinishVersion`; writes the log file from the lines it emitted; appends `image.build` with `Result` `succeeded`/`failed`. `SaveArgs` updates the image defaults before the build. Version `OperationID` is stored at start. Auto-push (M2) and retention (M5) hooks run after a success.

- [ ] **Step 1: Failing tests** (real SQLite, temp `Dir`, fake `Engine` that records calls and can fail, `stacks.NewOps(time.Minute, now)`):
  - `TestCreateUploadImageHasTemplateRevision`: revision 1 manifest has `Dockerfile` with `DefaultDockerfile`; `image.create` logged; `Create` with an existing slug → `ErrImageExists`; a name slugifying to `""` → `ValidationError` on `name`.
  - `TestUpdateKeepsSlugAndLogs`: renaming changes `Name` only; `image.update` logged; `TestLibraryLocalOnly`: `Library()` lists own images with `Owner.Local` and `Servers` = `[self]`.
  - `TestSaveDockerfileCreatesRevisionAndConflicts`: revision 2 after save; saving with revision 1 again → `ErrRevisionConflict`.
  - `TestUploadRenameDeleteRestore`: four revisions with the expected summaries; `Restore(2)` produces revision 5 whose manifest equals revision 2's.
  - `TestBuildSucceeds`: the fake engine receives a tar containing `Dockerfile` and the uploaded file, tags `[stackstead.invalid/<owner>/<slug>:1]`, build args merged; version ends `succeeded` with arch/digest/size from the fake inspect; log file exists and contains the engine's lines; `image.build` logged with `Result: succeeded`; `Detail().SuggestedTag == "2"`.
  - `TestBuildTagTaken`, `TestBuildBusy` (second `Build` while the first blocks → `ErrBuildRunning`, no second version row), `TestBuildFailedTagReusable` (engine `BuildError` → version `failed` with the message, tag `1` buildable again) (Review Focus 2).
  - `TestRecoverInterruptedBuilds` (Review Focus 2): a `building` row before `Start` → `failed`, error `interrupted by server restart`.
  - `TestDeleteVersionRemovesTagAndLog`; `TestDeleteVersionInUse`: `Usage` reports a stack using the ref → `*InUseError` listing `stack/service`, nothing removed.
  - `TestDeleteImageRefusedWhenInUse` and `TestDeleteImageRemovesEverything` (blobs dir gone, docker tags removed).
  - `TestEnsureLocalLocalOnly`: present → nil without output; missing in Docker → error mentioning `not built on this server`; foreign owner → same error (until M4).
  - `TestResolveLocal`: `Newer` true when a later succeeded version exists.
- [ ] **Step 2: Run** `go test ./internal/images -run 'Service|Build|Delete|Ensure|Resolve|Recover'`. Expected: FAIL.
- [ ] **Step 3: Implement.** Serialize source edits per image with a `sync.Map` of mutexes (as `stacks.Service.lockStack`). Log lines are appended to the file as they arrive (so a crash keeps a partial log) with the 8 MiB cap.
- [ ] **Step 4: Run** `go test -race ./internal/images`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add the image library service with builds and revisions`.

---

### Task 11: Image API routes and badge enrichment

**Files:**
- Create: `internal/api/images.go`, `internal/api/images_dto.go`, `internal/api/images_test.go`
- Modify: `internal/api/server.go` (`Options.Images ImagesAPI`, `Options.Arch func(context.Context) (string, error)`; register when `Images` is set), `internal/api/stacks.go` (`handleListStacks`, `handleGetStack` enrich via `s.libraryEnrich*`; `handleStatus` fills `Status.Arch` when `Options.Arch` is set, empty on error)

**Interfaces:**
- Consumes: Task 10 service, Task 3 types.
- Produces `ImagesAPI` interface (satisfied by `*images.Service`) with exactly the Task 10 methods used here, and routes (session or forwarded peer credential; `{path...}` is a wildcard):
  ```
  GET    /api/images                              → Library (from Service.Library; M1: own images only, Servers = [self])
  POST   /api/images                              → 201 LibraryImage
  GET    /api/images/{slug}                       → LibraryImageDetail
  PUT    /api/images/{slug}                       → LibraryImage
  DELETE /api/images/{slug}                       → 204 | 409 image_in_use {error, code, usedBy}
  GET    /api/images/{slug}/files                 → ImageFiles
  GET    /api/images/{slug}/files/{path...}       → ImageFileContent (text; 413 over 1 MiB)
  POST   /api/images/{slug}/files                 → multipart parts named f/<path>; 201 ImageRevision; 413 image_too_large
  POST   /api/images/{slug}/files/rename          → ImageRevision
  DELETE /api/images/{slug}/files/{path...}       → ImageRevision
  GET    /api/images/{slug}/dockerfile            → ImageDockerfile
  PUT    /api/images/{slug}/dockerfile            → ImageRevision | 409 revision_conflict
  GET    /api/images/{slug}/revisions             → []ImageRevision
  GET    /api/images/{slug}/revisions/{n}         → ImageRevisionDetail
  POST   /api/images/{slug}/revisions/{n}/restore → ImageRevision
  POST   /api/images/{slug}/builds                → 202 BuildStarted | 409 tag_taken | 409 build_running | 400 fields
  GET    /api/images/{slug}/versions/{id}         → LibraryVersionDetail
  DELETE /api/images/{slug}/versions/{id}         → 204 | 409 image_in_use
  ```
  `writeImagesError(w, r, err)` maps: `ValidationError` → 400 with fields; `ErrImageNotFound`/`ErrVersionNotFound`/`ErrRevisionNotFound`/`fs.ErrNotExist` → 404; `ErrImageExists` → 409; `*InUseError` → 409 `image_in_use` + `usedBy`; `ErrTagTaken` → 409 `tag_taken`; `ErrBuildRunning` → 409 `build_running`; `ErrRevisionConflict` → 409 `revision_conflict`; `ErrFileTooLarge`/`ErrContextTooLarge` → 413 `image_too_large`; `ErrBadPath` → 400; `engine.ErrUnavailable` → 503; else 500. Multipart body is bounded by `MaxBytesReader(limits.MaxContextBytes + 1 MiB)`.
  Enrichment: `GET /api/stacks` sets `libraryUpdates` per stack from `Summary.LibraryRefs` through `Images.Resolve`; `GET /api/stacks/{name}` sets `services[].library` for services whose image parses as a library ref. A `Resolve` failure leaves the fields zero and logs at warn (stacks must list even when the library is broken).

- [ ] **Step 1: Failing tests** (real `images.Service` on a temp dir with the fake engine from Task 10's tests exported through an `export_test.go`, or a `fakeImages` implementing `ImagesAPI`; the upload and traversal tests must hit the real service):
  - `TestImagesRoutesNeedSession` (one route → 401), `TestCreateImageAndList`, `TestImageDetailSuggestedTag`.
  - `TestUploadRouteTraversal` (Review Focus 1): parts `f/../etc/x` and `f//abs` → 400 naming the path, no revision added, no file outside the image dir; a part over `MaxFileBytes` → 413 `image_too_large`.
  - `TestDockerfileConflict409`, `TestBuildConflicts` (`tag_taken`, `build_running` codes), `TestDeleteVersionInUse409Body` (`usedBy` present).
  - `TestStacksLibraryBadges`: a stack whose service uses `stackstead.invalid/<self>/web:1` while version `2` exists → `libraryUpdates: 1` and `services[0].library.newer == true`, `latestTag == "2"`; an unknown ref → `known: false`.
  - `TestStatusArch`.
  - `TestPeerCredentialCanManageImages`: a peer credential (no cookie) reaches `POST /api/images/{slug}/builds` (forwarding path) and the action log `Username` is the peer actor.
- [ ] **Step 2: Run** `go test ./internal/api -run 'Images|Upload|Dockerfile|Build|Badges|StatusArch'`. Expected: FAIL.
- [ ] **Step 3: Implement.** Registration in `server.New`: `if opts.Images != nil { s.registerImageRoutes(mux) }`. The multipart reader streams each part straight into `Upload` (no buffering of whole files in memory).
- [ ] **Step 4: Run** `go test -race ./internal/api/...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add image library API routes`.

---

### Task 12: M1 integration, wiring and docs

**Files:**
- Modify: `internal/cli/serve.go` (construct `images.NewService`, `Start`, pass `Images`, `Arch`, `Library` into `stacks.Deps`), `web/src/app/routes.tsx` (`/images`, `/images/:slug`; both in `SERVER_ROUTES`), `web/src/app/shell/nav.ts` (entry `Images`, icon `image`, group `main`, after Storage; not in `tabBarNav`), `web/src/lib/shell/icons.ts` (`image` path), `web/src/lib/shell/breadcrumb.ts` (`/images`, `/images/:slug`), `web/src/app/shell/palette.ts` (Images entry if the palette lists screens), `web/src/app/shell/TopBar.tsx` (running text uses `operationSubject(op).label`), `docs/api.md` (new "Image library" section: routes table, limits, error codes, references, operations key), `docs/security.md` (data at rest, upload limits, who can manage images)
- Test: `web/src/app/routes` tests (`findRoute('/images/web')`), `shell.test.tsx` nav entry and top bar label, `internal/api/client_contract_test.go` unchanged (CLI comes in M5)

- [ ] **Step 1: Failing tests**: `findRoute('/images')`/`('/s/x/images/web')` resolve to the image screens; the sidebar shows "Images"; breadcrumb for `/images/web` is `Images › web`; a running `build` on `image/0123456789ab/web` shows `build · image web` in the top bar.
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/routes src/app/shell src/lib/shell`. Expected: FAIL.
- [ ] **Step 3: Implement** wiring, in this order: `imagesSvc := images.NewService(images.Deps{Store, Ops: ops, Engine: dockerEngine, Dir: filepath.Join(cfg.DataDir, "images"), Limits: {cfg.ImageMaxFileBytes, cfg.ImageMaxContextBytes}, OpTimeout: cfg.ImageOperationTimeout, Actions: actions, Self: adapter over peersSvc identity (InstanceID, Name, Slug), Now, Logger})`; `stackSvc` with `Library: imagesSvc`; `imagesSvc.SetUsage(stackSvc)`; `imagesSvc.Start(ctx)` after `stackSvc.Sync`; `api.Options{Images: imagesSvc, Arch: dockerEngine.Arch}`.
- [ ] **Step 4: Run** `make check` (vet, gofmt, Go tests, UI check and tests) and `cd web && npm run build` (budgets hold; the image screens are lazy chunks). Expected: PASS.
- [ ] **Step 5: Smoke** with the `run` skill: create an image, upload a file, build `1`, pick it in a service, `up` the stack, build `2`, see the badge. Record what was and was not checked.
- [ ] **Step 6: Commit** `feat: wire the image library into the server and web app` (docs in the same commit).

---

## Milestone 2: registries and push

### Task 13: `awsauth`: SigV4, IMDSv2, shared config profiles

**Files:**
- Create: `internal/registry/awsauth/sigv4.go`, `imds.go`, `ini.go`, `profile.go`, `sigv4_test.go`, `imds_test.go`, `ini_test.go`, `profile_test.go`

**Interfaces:**
- Produces:
  ```go
  type Credentials struct{ AccessKeyID, SecretAccessKey, SessionToken string }
  func SignV4(req *http.Request, body []byte, c Credentials, region, service string, now time.Time) error  // sets X-Amz-Date, X-Amz-Security-Token (when set), Authorization; host from req.URL
  type IMDS struct{ Base string; Client *http.Client }   // Base default "http://169.254.169.254"; Client default 2 s timeout
  func (i *IMDS) RoleCredentials(ctx context.Context) (Credentials, error)   // PUT /latest/api/token (X-aws-ec2-metadata-token-ttl-seconds: 21600) → GET /latest/meta-data/iam/security-credentials/ → GET .../<role> ; ErrNoRole when the listing is empty or 404
  func ParseINI(r io.Reader) (map[string]map[string]string, error)           // sections, key = value, # and ; comments, case-sensitive keys lowercased
  func LoadProfile(name, credentialsPath, configPath string) (Credentials, region string, err error) // credentials file [name] first, then config [profile name] (or [default]); ErrProfileNotFound; ErrProfileUnsupported for role_arn/sso_*/credential_process/source_profile
  func DefaultPaths(getenv func(string) string, home string) (credentialsPath, configPath string)  // AWS_SHARED_CREDENTIALS_FILE / AWS_CONFIG_FILE else ~/.aws/{credentials,config}
  var ErrNoRole, ErrProfileNotFound, ErrProfileUnsupported error
  ```

- [ ] **Step 1: Failing tests**
  - `TestSignV4KnownVector`: the AWS documentation example (GET `https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08`, header `Content-Type: application/x-www-form-urlencoded; charset=utf-8`, `X-Amz-Date: 20150830T123600Z`, key `AKIDEXAMPLE` / `wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY`, region `us-east-1`, service `iam`) → `Authorization` ends with `Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7` and `SignedHeaders=content-type;host;x-amz-date` (verify the vector against the current AWS "Signature Version 4 signing process" example before relying on it).
  - `TestSignV4SessionTokenHeader`, `TestSignV4PayloadHash` (POST body hash in `x-amz-content-sha256` is not required for ECR; the canonical request still hashes the body).
  - `TestIMDSRoleCredentials` (httptest server asserts the PUT token flow and that the token header is sent on the GETs), `TestIMDSNoRole`.
  - `TestParseINI` (comments, whitespace, `[profile x]`), `TestLoadProfileStaticKeys`, `TestLoadProfileUnsupported` (`role_arn` → `ErrProfileUnsupported` naming the key), `TestLoadProfileMissing`.
- [ ] **Step 2: Run** `go test ./internal/registry/awsauth`. Expected: FAIL.
- [ ] **Step 3: Implement** (stdlib `crypto/hmac`, `sha256`; canonical query sorted and RFC 3986 encoded; header names lowercased and trimmed).
- [ ] **Step 4: Run** `go test ./internal/registry/awsauth`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add hand-rolled AWS SigV4, IMDSv2 and profile credentials`.

---

### Task 14: Registry store and Docker Hub provider

**Files:**
- Create: `internal/registry/model.go`, `store.go`, `service.go`, `dockerhub.go`, `store_test.go`, `service_test.go`, `dockerhub_test.go`

**Interfaces:**
- Consumes: Task 2 `registries` table, `secret.Secret`, `engine.RegistryAuth`.
- Produces:
  ```go
  type Kind string; const ( KindDockerHub Kind = "dockerhub"; KindECR Kind = "ecr" )
  type ECRMode string; const ( ECRStatic ECRMode = "static"; ECRRole = "role"; ECRProfile = "profile" )
  type Hub struct{ Username string; Token secret.Secret }
  type ECR struct{ Region string; Mode ECRMode; AccessKeyID string; SecretAccessKey, SessionToken secret.Secret; Profile, AccountID string }
  type Registry struct{ ID, Name string; Kind Kind; Host string; Hub Hub; ECR ECR; CreatedAt, UpdatedAt time.Time; LastTestAt *time.Time; LastTestOK bool; LastTestMessage string }
  type Store struct{ /* db, now */ }; func NewStore(db *sql.DB, now func() time.Time) *Store
  func (s *Store) Create(ctx, r Registry) (Registry, error) // ErrNameTaken
  func (s *Store) Get(ctx, id string) (Registry, error); List(ctx) ([]Registry, error); Update(ctx, r Registry) error; Delete(ctx, id string) error
  func (s *Store) RecordTest(ctx, id string, ok bool, message string) error
  var ErrNotFound, ErrNameTaken, ErrUnconfigured error
  type TestResult struct{ OK bool; Host, Message string }
  type Provider interface{ Test(ctx context.Context, r Registry) (TestResult, error); Credentials(ctx context.Context, r Registry) (engine.RegistryAuth, error) }
  type Service struct{ /* store, providers map[Kind]Provider, actions, logger */ }
  func NewService(store *Store, providers map[Kind]Provider, actions ActionLog, logger *slog.Logger) *Service
  type Request struct{ Name string; Kind Kind; Hub *Hub; ECR *ECR }    // on update, an empty secret keeps the stored one
  func (s *Service) Create(ctx, user string, req Request) (Registry, error); Update(ctx, user, id string, req Request) (Registry, error); Delete(ctx, user, id string) error
  func (s *Service) List(ctx) ([]Registry, error); Get(ctx, id string) (Registry, error)
  func (s *Service) Test(ctx, id string) (TestResult, error)      // records the result
  func (s *Service) Credentials(ctx, id string) (engine.RegistryAuth, host string, err error)   // ErrUnconfigured when the kind's required fields are missing
  func PushRef(r Registry, repository, tag string) string   // hub: "docker.io/<repository>:<tag>" ("<username>/<repository>" when repository has no slash); ecr: "<host>/<repository>:<tag>"
  type HubProvider struct{ Base string; Client *http.Client }  // Base default "https://hub.docker.com"
  ```
  Docker Hub test: `POST {Base}/v2/users/login` JSON `{username, password}` → 200 means OK (message `Signed in as <username>`), 401 → OK false with `Docker Hub rejected the username or token`; other → error. Credentials: `{Username, Password: token, ServerAddress: "https://index.docker.io/v1/"}`, host `docker.io`.

- [ ] **Step 1: Failing tests**
  - `TestStoreSecretsNeverInConfigColumn`: after `Create`, `SELECT config FROM registries` contains no token text and `secrets` does; `Get` round-trips.
  - `TestUpdateKeepsSecretWhenBlank`, `TestNameTaken`, `TestDeleteCascadesPushTargets` (needs an image + target row).
  - `TestHubTestOKAndRejected` (httptest), `TestHubTestFailureRedacts` (Review Focus 3: a 500 with the token echoed in the body → the returned error and recorded message do not contain the token).
  - `TestPushRef` table; `TestCredentialsUnconfigured`; `TestActionLogOnCreate` (`registry.create`, scope `host`, detail = name and kind only).
- [ ] **Step 2: Run** `go test ./internal/registry`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/registry/...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add registry connections with a Docker Hub provider`.

---

### Task 15: Web registries settings card and push UI

**Files:**
- Create: `web/src/app/settings/RegistriesCard.tsx`, `RegistriesCard.module.css`, `registriesCard.test.tsx`, `web/src/app/images/PushTargetsCard.tsx`, `pushTargets.test.tsx`
- Modify: `web/src/app/images/ImageSettingsTab.tsx` (renders `PushTargetsCard`), `VersionsTab.tsx` (push status per target, Push action per target with an `OperationPanel`), `web/src/app/settings/SettingsScreen.tsx` (section `settings-registries`, "Registries", after Connected servers)

**Interfaces:**
- Consumes: Task 3 `RegistryView`, `RegistryRequest`, `PushTarget`, `PushState`; `createLocalImagesApi().registries/...`; `imageRegistries()` (names for the picker on any scope).
- Produces: `RegistriesCard` lists registries (name, kind, host, last test), add/edit dialog with kind-specific fields (Hub: username, token masked; ECR: region, mode segmented `Access key / IAM role / AWS profile`, the fields of the chosen mode), "Test connection" with the result inline, delete with confirm. `PushTargetsCard` lists targets with registry name, repository and auto-push toggle; add/edit/remove.

- [ ] **Step 1: Failing tests**: add Hub registry posts `{name, kind:'dockerhub', hub:{username, token}}`; editing shows the token field empty with "leave blank to keep"; ECR mode switch shows only that mode's fields; test button shows `message`; delete confirms; `PushTargetsCard` posts `{registryId, repository, autoPush}`; the Versions tab shows a `pushed`/`failed` chip per target and Push posts `{target}` then shows the operation.
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/settings src/app/images`. Expected: FAIL.
- [ ] **Step 3: Implement** with `SettingsCard`, `Dialog`, `Field`, `Segmented`.
- [ ] **Step 4: Run** `cd web && npm run check && npx vitest run`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add registry settings and push targets to the web app`.

---

### Task 16: ECR provider

**Files:**
- Create: `internal/registry/ecr.go`, `ecr_test.go`

**Interfaces:**
- Consumes: Task 13, Task 14 `Provider`.
- Produces:
  ```go
  type ECRProvider struct{ Endpoint func(region string) string /* default https://api.ecr.<region>.amazonaws.com */; Client *http.Client; IMDS *awsauth.IMDS;
                           Paths func() (credentialsPath, configPath string); Now func() time.Time }
  func (p *ECRProvider) Test(ctx, r Registry) (TestResult, error)                 // GetAuthorizationToken; OK message "Registry <host>"; Host filled from proxyEndpoint; records AccountID
  func (p *ECRProvider) Credentials(ctx, r Registry) (engine.RegistryAuth, error) // Username "AWS", Password from the token, ServerAddress = host
  ```
  Request: `POST <endpoint>/` with `Content-Type: application/x-amz-json-1.1`, `X-Amz-Target: AmazonEC2ContainerRegistry_V20150921.GetAuthorizationToken`, body `{}`, signed for service `ecr`. Response `authorizationData[0].authorizationToken` (base64 `AWS:<password>`) and `proxyEndpoint`. Credentials by mode: static from the record; role via `IMDS.RoleCredentials`; profile via `awsauth.LoadProfile` (region from the profile when the record has none).

- [ ] **Step 1: Failing tests** (httptest ECR endpoint that checks the `Authorization` header shape, `X-Amz-Target`, and answers a token; httptest IMDS):
  - `TestECRStaticCredentials`, `TestECRRoleViaIMDS`, `TestECRProfile` (temp credentials file), `TestECRProfileUnsupportedMessage` (`role_arn` → `OK:false` message `profile "x" uses role_arn, which is not supported; use static keys`).
  - `TestECRHostFromProxyEndpoint` (`https://123456789012.dkr.ecr.eu-west-1.amazonaws.com` → host without scheme, AccountID `123456789012`).
  - `TestECRErrorRedacts` (Review Focus 3): a 400 whose body echoes the request → the error contains neither the secret key nor the session token.
- [ ] **Step 2: Run** `go test ./internal/registry -run ECR`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/registry/...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add ECR registry authentication`.

---

### Task 17: Push targets, push operation, auto-push

**Files:**
- Create: `internal/images/push.go`, `push_test.go`
- Modify: `internal/images/service.go` (`Deps.Registries`), `build.go` (auto-push hook after success)

**Interfaces:**
- Consumes: Task 14 `registry.Service` (`Credentials`, `Get`, `PushRef`), Task 4 `Engine.Push/TagImage/RemoveImage`.
- Produces:
  ```go
  type RegistryAuth interface{ Credentials(ctx context.Context, id string) (engine.RegistryAuth, string, error); Get(ctx context.Context, id string) (registry.Registry, error) }
  // Deps gains: Registries RegistryAuth   // nil: push routes answer registry_unconfigured
  type PushTargetRequest struct{ RegistryID, Repository string; AutoPush bool }   // Repository: ^[a-z0-9]+(?:[._-/][a-z0-9]+)*$ ≤ 255
  func (s *Service) AddPushTarget(ctx, user, slug string, req PushTargetRequest) (PushTarget, error)
  func (s *Service) UpdatePushTarget(ctx, user, slug, id string, req PushTargetRequest) (PushTarget, error)
  func (s *Service) DeletePushTarget(ctx, user, slug, id string) error
  func (s *Service) Push(ctx, user, slug, tag, targetID string) (stacks.Operation, error)   // ErrVersionNotFound (or not succeeded → ValidationError), ErrTargetNotFound, registry.ErrUnconfigured
  var ErrRegistryUnconfigured = registry.ErrUnconfigured
  ```
  Push operation (`Ops.StartTimeout(OpKey, "push", deps.OpTimeout, fn)`): `SetPush(pushing)`; `TagImage(ref → PushRef)`; `Engine.Push(pushRef, auth, out)`; `RemoveImage(pushRef)` (untag only) **[call]**; `SetPush(pushed, digest)` or `(failed, error)`; action log `image.push` with detail `<registry name> <repository>:<tag>` and result. Auto-push: after a successful build of an **upload** image, every target with `AutoPush` and a configured registry is pushed sequentially inside the same build operation (output `pushing to <name>…`); a push failure does not fail the build (version stays succeeded; push state `failed`). GitHub images never auto-push.

- [ ] **Step 1: Failing tests** (fake `RegistryAuth`):
  - `TestPushTagsPushesUntags`: engine calls in order `TagImage(ref, docker.io/u/app, 1)`, `Push("docker.io/u/app:1", auth)`, `RemoveImage("docker.io/u/app:1")`; push row `pushed` with the digest; `image.push` logged.
  - `TestPushOutputRedacted` (Review Focus 3): the fake engine's push error echoes the password → operation error and push row error do not contain it.
  - `TestPushUnconfiguredRegistry409`, `TestPushFailedVersionRefused`.
  - `TestAutoPushAfterBuild`: upload image with two targets, one auto → exactly that target pushed inside the build operation; a failing auto-push leaves the version `succeeded` and the push `failed`; `TestGitHubImageNeverAutoPushes`.
- [ ] **Step 2: Run** `go test ./internal/images -run Push`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/images`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: push image versions to registries, automatically for uploaded images`.

---

### Task 18: M2 API routes, wiring, docs

**Files:**
- Create: `internal/api/images_settings.go`, `internal/api/images_settings_test.go`
- Modify: `internal/api/images.go` (push target and push routes, `GET /api/images/registries`), `internal/api/server.go` (`Options.Registries RegistriesAPI`), `internal/api/images_dto.go` (`toRegistryView` never copies secrets), `internal/cli/serve.go` (construct `registry.NewStore/NewService` with `HubProvider` and `ECRProvider`; `images.Deps.Registries`), `docs/api.md`, `docs/security.md` (registry credentials at rest, redaction)

**Interfaces:**
- Produces routes:
  ```
  GET    /api/settings/registries            → []RegistryView          POST → 201 RegistryView
  PUT    /api/settings/registries/{id}       → RegistryView            DELETE → 204 (push targets that used it are removed by cascade)
  POST   /api/settings/registries/{id}/test  → RegistryTest
  GET    /api/images/registries              → []RegistryView (id, name, kind, host only; forwardable)
  POST   /api/images/{slug}/push-targets     → 201 PushTarget   PUT/DELETE /api/images/{slug}/push-targets/{id}
  POST   /api/images/{slug}/versions/{tag}/push → 202 Operation | 409 registry_unconfigured
  ```
- [ ] **Step 1: Failing tests**: `TestRegistryViewNeverContainsSecrets` (create with token, list/get bodies lack it), `TestRegistrySettingsDeniedToPeer` (403 `peer_forbidden`), `TestImageRegistriesListForwardable` (peer credential → 200 with names only), `TestPushRouteCodes`, `TestRegistryTestRecordsResult`.
- [ ] **Step 2: Run** `go test ./internal/api -run 'Registry|PushRoute'`. Expected: FAIL.
- [ ] **Step 3: Implement**, then docs: a "Registries" subsection (routes, kinds, ECR modes and the static-keys-only profile rule) and the push routes in the image library section.
- [ ] **Step 4: Run** `make check`. Expected: PASS. Smoke: add a Docker Hub registry, test it, add a target with auto-push, build, see `pushed`.
- [ ] **Step 5: Commit** `feat: add registry settings and push routes`.

---

## Milestone 3: GitHub source

### Task 19: GitHub client and tarball extraction

**Files:**
- Create: `internal/images/github/client.go`, `extract.go`, `client_test.go`, `extract_test.go`

**Interfaces:**
- Produces:
  ```go
  package github
  type Client struct{ Base string /* default https://api.github.com */; HTTP *http.Client }
  func (c *Client) User(ctx, token secret.Secret) (login string, err error)                       // GET /user
  func (c *Client) Head(ctx, token secret.Secret, repo, branch string) (commit string, err error) // GET /repos/{repo}/commits/{branch} → sha
  func (c *Client) File(ctx, token secret.Secret, repo, ref, path string) ([]byte, error)        // GET /repos/{repo}/contents/{path}?ref=; base64 decode; ErrNotFound; ErrTooLarge over 1 MiB
  func (c *Client) Tarball(ctx, token secret.Secret, repo, ref string) (io.ReadCloser, error)     // GET /repos/{repo}/tarball/{ref}, follows the one redirect GitHub sends (to a non-api host, without the token)
  var ErrUnauthorized, ErrNotFound, ErrTooLarge error
  func ValidateRepo(repo string) error   // ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ; ValidateBranch: no "..", no leading "-", ≤ 255
  // extract.go
  func ExtractContext(tarGz io.Reader, subdir, dst string, maxFile, maxTotal int64) (files int, err error)
  // strips the top-level "<owner>-<repo>-<sha>/" component, keeps only entries under subdir, skips symlinks/hardlinks/devices,
  // rejects entries whose cleaned path escapes (ErrBadPath), enforces caps; dst must be an empty directory; writes through os.OpenRoot
  ```
  Headers: `Authorization: Bearer <token>`, `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`, `User-Agent: stackstead`.

- [ ] **Step 1: Failing tests** (httptest GitHub): `TestUserAndErrors` (401 → `ErrUnauthorized`), `TestHeadSHA`, `TestFileDecodes` and `TestFileTooLarge`, `TestTarballRedirectDropsToken` (the redirect target records no `Authorization` header), `TestGitHubErrorRedacts` (Review Focus 3: an error body echoing the token → error text without it), `TestExtractSubdirAndTraversal` (a tar.gz built in the test with `repo-abc/`, `repo-abc/svc/Dockerfile`, `repo-abc/svc/../../etc/x`, a symlink, a 2-byte file over `maxFile`), `TestExtractCaps`.
- [ ] **Step 2: Run** `go test ./internal/images/github`. Expected: FAIL.
- [ ] **Step 3: Implement** (`archive/tar`, `compress/gzip`).
- [ ] **Step 4: Run** `go test -race ./internal/images/github`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add a GitHub client and safe tarball extraction for image sources`.

---

### Task 20: Web GitHub settings card and GitHub image fields

**Files:**
- Create: `web/src/app/settings/GitHubCard.tsx`, `githubCard.test.tsx`
- Modify: `web/src/app/images/NewImageDialog.tsx` (source choice Upload / GitHub; repo, branch, path, Dockerfile path fields), `ImageSettingsTab.tsx` (GitHub fields editable), `SourceTab.tsx` (GitHub: settings summary + read-only Dockerfile at branch head from `githubHead`, "Open on GitHub" link, no editor/tree/revisions), `ImageDetailScreen.tsx` (hide the Revisions tab for GitHub images), `BuildDialog.tsx` (GitHub: "Branch head" / "Commit" segmented with a commit input → `ref`), `web/src/app/settings/SettingsScreen.tsx` (section `settings-github`, "GitHub")

- [ ] **Step 1: Failing tests**: GitHub card shows "Not configured" / "Signed in as <login>", saving posts `{token}`, test shows the message, token field masked and never prefilled; new image dialog with GitHub posts `{source:'github', github:{repo, branch, path}, dockerfilePath}`; the detail hides Revisions and shows the read-only Dockerfile with the commit; the build dialog sends `ref` only when Commit is chosen.
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/settings src/app/images`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `cd web && npm run check && npx vitest run`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add GitHub-sourced images to the web app`.

---

### Task 21: GitHub-sourced images in the service

**Files:**
- Create: `internal/images/github_source.go`, `github_source_test.go`
- Modify: `internal/images/service.go` (`Deps.GitHub`, `Deps.GitHubToken`), `build.go` (context from a tarball for GitHub images), `settings.go` (new: GitHub token setting)

**Interfaces:**
- Consumes: Task 19.
- Produces:
  ```go
  type GitHubAPI interface{ User(...); Head(...); File(...); Tarball(...) }   // the Task 19 methods
  // Deps gains: GitHub GitHubAPI; Settings *SettingsStore (reads/writes settings keys github_token and images_default_retention)
  type SettingsStore struct{ /* db */ }; func NewSettingsStore(db *sql.DB) *SettingsStore
  func (s *SettingsStore) GitHubToken(ctx) (secret.Secret, error); SetGitHubToken(ctx, t secret.Secret) error   // "" clears
  type GitHubStatus struct{ Configured bool; Login string }
  func (s *Service) GitHubStatus(ctx) (GitHubStatus, error)                    // Login from the last successful test (settings key github_login)
  func (s *Service) SetGitHubToken(ctx, user string, token secret.Secret) error // logs github.update (result "set"/"cleared")
  func (s *Service) TestGitHub(ctx) (login string, err error)
  type GitHubHead struct{ Commit, Dockerfile, Path string }
  func (s *Service) GitHubHead(ctx, slug string) (GitHubHead, error)           // ErrGitHubUnconfigured, github.ErrNotFound → ValidationError on "github.path"
  var ErrGitHubUnconfigured error
  ```
  Build of a GitHub image: `req.Ref` empty → `Head(branch)` gives the commit; else the commit is `req.Ref` (40 hex or a tag/branch name; stored as returned by `Head` when it was a branch); `Tarball(commit)` → `ExtractContext` into `tmp-<versionID>` → `WriteContext` → `Engine.Build` with `Dockerfile` = `DockerfilePath`; `Version.Commit` set, `Revision` 0. Create/Update of a GitHub image validate repo/branch/path and require a configured token.

- [ ] **Step 1: Failing tests** (fake `GitHubAPI` returning a tar.gz built in the test): `TestGitHubBuildRecordsCommit` (context tar contains `Dockerfile` from the subdir only; version `Commit` = head sha), `TestGitHubBuildAtCommit` (`Ref` passed through), `TestGitHubHeadView`, `TestGitHubImageNeedsToken` (`ErrGitHubUnconfigured` on create), `TestGitHubImageHasNoRevisions` (`Files`/`SaveDockerfile` → `ValidationError` "GitHub images are read-only"), `TestSetGitHubTokenLogsWithoutValue`.
- [ ] **Step 2: Run** `go test ./internal/images -run GitHub`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/images/...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: build images from a GitHub repository path`.

---

### Task 22: M3 API routes, wiring, docs

**Files:**
- Modify: `internal/api/images_settings.go` (`GET/PUT /api/settings/github` → `GitHubSettings`; `POST /api/settings/github/test` → `GitHubTest`), `internal/api/images.go` (`GET /api/images/{slug}/github/head` → `GitHubHead`; 409 `github_unconfigured`), `internal/cli/serve.go` (`github.Client`, `SettingsStore`), `docs/api.md`, `docs/security.md` (token at rest; the tarball redirect never carries it)

- [ ] **Step 1: Failing tests**: `TestGitHubSettingsNeverReturnToken`, `TestGitHubSettingsDeniedToPeer`, `TestGitHubHeadRoute`, `TestCreateGitHubImageValidation` (bad repo → 400 field `github.repo`).
- [ ] **Step 2: Run** `go test ./internal/api -run GitHub`. Expected: FAIL.
- [ ] **Step 3: Implement**; docs.
- [ ] **Step 4: Run** `make check`. Expected: PASS. Smoke: set a token, create a GitHub image on a public repo, build at head, see the commit.
- [ ] **Step 5: Commit** `feat: add GitHub settings and head routes`.

---

## Milestone 4: sharing across peers

### Task 23: Peer routes, merged library and usage

**Files:**
- Create: `internal/images/peerlib.go`, `peerlib_test.go`, `internal/api/images_peer.go`, `images_peer_test.go`
- Modify: `internal/images/service.go` (`Deps.Peers`; `List`→`Library`), `usage.go` (peer usage in used-by)

**Interfaces:**
- Consumes: `peers.Service` (`Views`, `Usable`), `peers.NewHTTPClient`, `peers.Classify`, `peers.PeerHeader`.
- Produces:
  ```go
  type PeerSource interface{ Views(ctx context.Context) ([]apitypes.PeerView, error); Usable(id string) (peers.Peer, error) }   // *peers.Service
  // Deps gains: Peers PeerSource (nil: no peers); PeerTimeout time.Duration (default 3 s for list/usage calls)
  // Library (Task 10) now returns local + every active peer in parallel (GET /api/peer/images), cached 10 s; a peer answering 404 is "old", one that fails is "unreachable" with Error set
  func (s *Service) OwnImages(ctx context.Context) ([]LibraryEntry, error)   // what /api/peer/images serves: owner = self, succeeded versions only, no logs
  func (s *Service) OwnUsage(ctx context.Context) ([]imageref.Usage, error)  // what /api/peer/images/usage serves (local stacks)
  func (s *Service) OpenVersionTar(ctx context.Context, slug, tag string) (rc io.ReadCloser, imageID string, err error)   // succeeded only; Engine.SaveImage(ref)
  func (s *Service) FindOwner(ctx context.Context, owner string) (ServerInfo, bool, error)   // by imageref.OwnerID over self and active peers
  // Resolve (Task 10) now consults Library() so peer-owned refs are Known with LatestTag/Arch
  // usage.go: usedBy(version) = local usage ∪ each active peer's usage; peers that fail are returned in `unreachable []ServerInfo`
  func (s *Service) usedBy(ctx context.Context, ref imageref.Ref) (uses []UsedBy, unreachable []ServerInfo, err error)
  ```
  Peer routes (`requirePeer(false, ...)`, in `registerPeerRoutes` when `Options.Images != nil`):
  ```
  GET /api/peer/images                              → []LibraryImage (owner = this server)
  GET /api/peer/images/usage                        → []LibraryUsage
  GET /api/peer/images/{slug}/versions/{tag}/tar    → application/x-tar stream, header X-Stackstead-Image-ID; 404 for unknown or non-succeeded
  ```
  `forwardTail` already refuses `/api/peer/*` through forwarding; nothing to change there. `DeleteVersion`/`Delete` refuse (409 with `unreachable` servers named) when any peer could not be asked.

- [ ] **Step 1: Failing tests** (two real servers via `newPeerEnv`-style pairing as in `peers_integration_test.go`, or an httptest peer answering the three routes with the credential check):
  - `TestPeerImagesListRequiresCredential` (no header → 401 `peer_not_paired`), `TestPeerImagesListShape` (succeeded versions only, no `log`, owner is the serving server).
  - `TestPeerTarStreamsAndHeader` (bytes equal the fake engine's save output; header is the version's digest; a failed version → 404).
  - `TestLibraryMergesPeers` (local + peer images, servers list with statuses; a 404 peer → `old`; an unreachable peer → `unreachable` and the call still succeeds within the timeout).
  - `TestUsedByIncludesPeerUsage`, `TestDeleteVersionRefusedWhenPeerUnreachable` (error names the server; nothing deleted).
- [ ] **Step 2: Run** `go test ./internal/images -run 'Library|UsedBy|PeerTar' && go test ./internal/api -run PeerImages`. Expected: FAIL.
- [ ] **Step 3: Implement.** The tar handler copies with a 10 s per-write deadline (reuse the `deadlineWriter` pattern from `forward.go`) and flushes; it ends when the engine stream ends or the request context is cancelled.
- [ ] **Step 4: Run** `go test -race ./internal/images/... ./internal/api/...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: expose the image library to paired servers`.

---

### Task 24: Web owner-aware library

**Files:**
- Modify: `web/src/app/images/ImagesScreen.tsx` (owner column/chips, server status banner for unreachable/old servers, peer-owned rows read-only here and linked through `imageHref`), `ImageDetailScreen.tsx` (a 404 for a slug that the library lists under another owner redirects to `imageHref`), `web/src/app/stacks/LibraryPicker.tsx` (owner grouping already exists; add the "fetched on save" note), `web/src/app/detail/ServiceEditor.tsx` (after a successful save of a service whose library ref's owner is not the viewed server (`ownerIsViewed` false), call `fetchImage(ref)` on the stack's scope and track the operation), tests for each

- [ ] **Step 1: Failing tests**: list shows an owner chip per peer-owned image and a banner "mini-02 could not be reached" from `servers[]`; a peer-owned row links to `/s/mini-02/images/web`; saving a service with a peer-owned ref posts `/api/images/fetch {ref}` on the stack's scope and shows the operation; a local-owned ref posts no fetch.
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/images src/app/detail src/lib/images`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `cd web && npm run check && npx vitest run`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: show image owners and fetch peer-owned versions after a service picks them`.

---

### Task 25: Fetch over the peer link and ensure-local

**Files:**
- Create: `internal/images/fetch.go`, `fetch_test.go`
- Modify: `internal/images/service.go` (`EnsureLocal` peer path), `internal/api/images.go` (`POST /api/images/fetch` → 202 Operation; 409 `arch_mismatch`, 409 `image_not_built`, 502 `peer_*`)

**Interfaces:**
- Consumes: Task 23 `FindOwner`, peer tar route.
- Produces:
  ```go
  func (s *Service) Fetch(ctx, user string, ref imageref.Ref) (stacks.Operation, error)   // Ops key OpKey(owner, slug), action "fetch", started with `Ops.StartTimeout(..., OpTimeout, ...)`; refuses when the owner is unknown (ValidationError on "ref"), when the version is unknown or not succeeded (image_not_built), or when its arch differs from Engine.Arch (ErrArchMismatch with both arches)
  func (s *Service) EnsureLocal(ctx, ref imageref.Ref, out func(string)) error    // present locally with the recorded ID → nil; owner is self → image_not_built error; else fetch inline (same checks), writing progress to out
  type ArchMismatchError struct{ Built, Local string }   // "built for arm64; this server is amd64"
  const fetchIdleTimeout = 60 * time.Second
  ```
  Transfer: `GET /api/peer/images/{slug}/versions/{tag}/tar` through `peers.NewHTTPClient(base, peer.TLS, 30s)` with the peer credential and `X-Stackstead-Peer-User: <user>`; the body is wrapped in an idle-watchdog reader (cancels the request when no `Read` completes for 60 s) and piped into `Engine.LoadImage`; afterwards `InspectImage(ref).ID` must equal the `X-Stackstead-Image-ID` header, else `RemoveImage(ref)` and error `fetched image does not match the owner's record`. Output lines: `fetching <ref> from <owner name> (<size so far>)` every 5 s and `loaded <ref>`. Action log `image.fetch` (scope slug, detail owner name, bytes).

- [ ] **Step 1: Failing tests** (httptest owner serving the tar route; fake engine load/inspect):
  - `TestFetchLoadsAndVerifies` (engine received the exact bytes; log has `image.fetch` with bytes).
  - `TestFetchVerifiesImageID` (Review Focus 4): inspect returns a different ID → `RemoveImage(ref)` called, operation failed with the mismatch message.
  - `TestFetchIdleWatchdog` (Review Focus 4): the owner stalls after 10 bytes → the operation fails within ~61 s of fake-clock time (inject the watchdog interval through an unexported seam so the test runs in milliseconds).
  - `TestFetchRefusesArchMismatch` (Review Focus 4): `*ArchMismatchError` with `Built: "arm64", Local: "amd64"`; no request sent.
  - `TestEnsureLocalSkipsWhenPresent`, `TestEnsureLocalFetchesFromOwner` (stack `up` on the consumer prints the fetch lines before compose).
  - API: `TestFetchRouteCodes`.
- [ ] **Step 2: Run** `go test ./internal/images -run 'Fetch|EnsureLocal' && go test ./internal/api -run FetchRoute`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/images/... ./internal/api/... ./internal/stacks/...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: fetch library versions from their owning server`.

---

### Task 26: Two-server integration and docs

**Files:**
- Create: `internal/api/images_integration_test.go`
- Modify: `docs/api.md` (peer routes, fetch route, transfer behaviour, arch rule), `docs/security.md` (peer surface is read-only; what a peer credential can do with images)

- [ ] **Step 1: Failing test** `TestImageSharedAcrossPeers`: servers A and B paired (as `peers_integration_test.go` does) with fake engines; A builds `web:1`; B's `GET /api/images` lists it with owner A; B's `POST /api/images/fetch` loads it into B's engine with the matching ID; `DELETE` on A answers 409 `image_in_use` once a stack on B uses it; `/api/stacks` on B flags `libraryUpdates` after A builds `web:2`.
- [ ] **Step 2: Run** `go test ./internal/api -run TestImageSharedAcrossPeers -v`. Expected: FAIL until wiring is complete, then PASS.
- [ ] **Step 3: Docs.**
- [ ] **Step 4: Run** `make check`. Expected: PASS. Smoke with two local servers if feasible; otherwise record it as not run.
- [ ] **Step 5: Commit** `test: cover image sharing between paired servers` (docs included).

---

## Milestone 5: retention and CLI

### Task 27: Retention policy and sweep

**Files:**
- Create: `internal/images/retention.go`, `retention_test.go`
- Modify: `internal/images/build.go` (apply after a successful build)

**Interfaces:**
- Produces:
  ```go
  type RetentionResult struct{ Removed []Version; Kept int; Skipped string }   // Skipped names the unreachable server that blocked the run, "" otherwise
  func Keep(policy Retention, versions []Version, now time.Time) (keep map[string]bool)   // pure: succeeded and failed rows ranked by StartedAt desc; kept when rank <= KeepLast or age < KeepDays*24h; both 0 → keep all; building rows always kept
  func (s *Service) ApplyRetention(ctx, user, slug string) (RetentionResult, error)   // computes Keep, subtracts in-use (usedBy); if any peer is unreachable → Skipped set, nothing removed; otherwise removes tag, log, row and logs image.retain per removal (user "system" from the sweep)
  type Sweeper struct{ Service *Service; After func(time.Duration) <-chan time.Time; Logger *slog.Logger }   // After nil → time.After
  func (w *Sweeper) Run(ctx context.Context)   // first sweep after 5 min, then every 6 h: ApplyRetention for every image with a non-zero policy; failures logged at warn
  ```

- [ ] **Step 1: Failing tests**
  - `TestKeepTable`: policies `{3,0}`, `{0,30}`, `{3,30}`, `{0,0}` against seven versions with known ages → exact kept sets; the building row survives every policy.
  - `TestRetentionAfterBuildRemovesOldest` (policy keep-last 2; three builds → the first tag is untagged, its log file gone, `image.retain` logged with user and tag).
  - `TestRetentionKeepsInUse`: a used version older than the window survives.
  - `TestRetentionSkipsWhenPeerUnreachable` (Review Focus 5): `Skipped` names the peer; nothing removed; the sweep logs at warn.
  - `TestDeleteVersionInUseLists` (Review Focus 5): `*InUseError` lists `stack/service on server`.
  - `TestRetentionNeverTouchesRevisions` (revision count unchanged after a run).
  - `TestSweepSchedule` (`Sweeper` with a fake `After`: first call 5 m, then 6 h; a ctx cancel ends `Run`).
- [ ] **Step 2: Run** `go test ./internal/images -run 'Keep|Retention|Sweep'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/images`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: prune image versions by retention policy without touching versions in use`.

---

### Task 28: Default retention setting

**Files:**
- Create: `web/src/app/settings/ImageDefaultsCard.tsx`, `imageDefaultsCard.test.tsx`
- Modify: `internal/images/settings.go` (`DefaultRetention(ctx) (Retention, error)`, `SetDefaultRetention(ctx, r Retention) error` on `SettingsStore`), `internal/images/service.go` (`Create` uses `Deps.Settings.DefaultRetention` when `req.Retention == nil`; the only M5 task that edits this file), `internal/api/images_settings.go` (`GET/PUT /api/settings/images` → `ImageDefaults`), `web/src/app/settings/SettingsScreen.tsx` (section `settings-images`, "Images"), `docs/api.md`

- [ ] **Step 1: Failing tests**: Go `TestDefaultRetentionAppliesToNewImagesOnly` (change the default after creating image A; A keeps its policy, new image B gets the new one); API `TestImageDefaultsRoundTrip` and validation (negative → 400); web card loads and saves `{retention:{keepLast, keepDays}}` and shows `retentionText`.
- [ ] **Step 2: Run** `go test ./internal/images -run Default && go test ./internal/api -run ImageDefaults && cd web && npx vitest run src/app/settings`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same plus `npm run check`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add a default retention policy for new images`.

---

### Task 29: Go client and `stackstead images` commands

**Files:**
- Create: `internal/client/images.go`, `internal/client/images_test.go`, `internal/cli/cmd_images.go`, `internal/cli/cmd_images_test.go`
- Modify: `internal/cli/commands.go` (add `imagesCommand()` to "Remote" after `storageCommand()`), `docs/cli.md` (command table rows, an "Images" section with examples), `internal/api/client_contract_test.go` (one images call against the real router)

**Interfaces:**
- Produces:
  ```go
  // client
  func (c *Client) Images(ctx) (apitypes.Library, error)
  func (c *Client) Image(ctx, slug string) (apitypes.LibraryImageDetail, error)
  func (c *Client) BuildImage(ctx, slug string, req apitypes.BuildRequest) (apitypes.BuildStarted, error)
  func (c *Client) PushImageVersion(ctx, slug, tag, target string) (apitypes.Operation, error)
  // cli: usage lines
  images ls                                   // table: NAME SOURCE OWNER LATEST LAST BUILD PUSH TARGETS; -o json prints the Library
  images build <image> [--tag T] [--build-arg K=V]... [--detach]   // tag defaults to the detail's suggestedTag; errors when the suggestion is empty and --tag is absent (exit 2 with the hint)
  images push <image> <tag> [--target NAME] [--detach]              // --target by push target registry name; required when the image has more than one target
  ```
  409 `tag_taken`/`build_running`/`image_in_use`/`registry_unconfigured` map to `ExitConflict` with the server message; the operation streams through `startAndFollow`.

- [ ] **Step 1: Failing tests** (fake server as the other `cmd_*_test.go` files do): `TestImagesLsTable` (columns and `-` for missing), `TestImagesBuildUsesSuggestedTag`, `TestImagesBuildNoSuggestionIsUsageError`, `TestImagesBuildArgsParsed` (`K=V` repeated; `K` without `=` → usage error), `TestImagesBuildConflictExit`, `TestImagesPushTargetRequiredWhenAmbiguous`, `TestImagesPushStreams`; client tests pin the paths.
- [ ] **Step 2: Run** `go test ./internal/client ./internal/cli -run Images`. Expected: FAIL.
- [ ] **Step 3: Implement**; docs.
- [ ] **Step 4: Run** `go test -race ./internal/client/... ./internal/cli/... ./internal/api/...`. Expected: PASS (`deps_test.go` still passes: `client` gains no imports).
- [ ] **Step 5: Commit** `feat: add stackstead images commands`.

---

### Task 30: M5 integration, docs, final verification

**Files:**
- Modify: `internal/cli/serve.go` (`(&images.Sweeper{Service: imagesSvc, Logger: logger}).Run` added to `startBackground`), `docs/api.md` (retention semantics: rule, in-use definition, unreachable-peer skip, sweep timing), `docs/security.md` (retention never deletes in registries), `README.md` feature list line

- [ ] **Step 1:** `make check`. Expected: all pass.
- [ ] **Step 2:** `go test -race ./internal/...`. Expected: PASS.
- [ ] **Step 3:** If Docker is available: `go test -tags integration ./internal/engine -run ImageOpsIntegration`. Otherwise record it as not run.
- [ ] **Step 4:** Manual smoke with the `run` skill: set a retention of keep-last 1, build twice, see the first version pruned; `stackstead images ls`, `stackstead images build web`, `stackstead images push web 3 --target hub`. Record what was and was not checked.
- [ ] **Step 5:** Final whole-branch review (Fable per CLAUDE.md, Opus if unavailable): tests came first, the full suite passes, every spec section maps to a task (use the coverage list below), secrets never surface.
- [ ] **Step 6: Commit** `docs: describe image library retention and sharing`.

---

## Spec coverage

| Spec item | Tasks |
| --- | --- |
| Uploaded source, revisions, restore, diff | 5, 6, 8, 10, 11 |
| GitHub source, read-only Dockerfile, commit per version, branch-head-or-commit | 19, 20, 21, 22 |
| Versions with tag, revision/commit, build args, arch, time, result, log | 4, 5, 10, 11 |
| Tag suggestion and validation | 5, 8, 29 |
| Registry connections (Hub, ECR × 3 modes), push targets, auto-push, manual push | 13–18 |
| Service picks image+version, pinned, "new version available" | 7, 9, 11 |
| Shared across peers, read-only peer surface, transfer with ID check, arch rule | 23–26 |
| Retention per image, default in Settings, local only, in-use rule, unreachable peer | 27, 28, 30 |
| Build args defaults, per-build override, save-as-defaults, plain text note | 5, 8, 10 |
| Live build output, one build per image, failed attempt frees the tag | 4, 10, 11 |
| Local reference format and owner id, compose stays plain-docker-compatible | 1, 7 |
| Stack pull skips library services and fetches missing ones | 7, 25 |
| Forwarding manages a peer's images on the owner | 11 (`TestPeerCredentialCanManageImages`), 24 |
| Upload limits, path traversal, symlinks, `.dockerignore` | 2, 6, 11, 19 |
| Action log for build, push, fetch, retention, credential changes | 10, 14, 17, 21, 25, 27 |
| Secrets at rest and redaction | 1, 14, 16, 18, 19, 22 |
| CLI `images list/build/push` | 29 |
| Existing per-service Dockerfile builds unchanged | 7 (`TestNilLibraryLeavesComposeAlone`), existing suites |
