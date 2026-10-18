# Stackstead configuration history — Design

**Date:** 2026-10-18
**Status:** Draft, awaiting review. Records timestamped revisions of each stack's compose file set (base, override and extra files) and `.env`, shows diffs between them, and restores an earlier revision after a preview. It replaces the single `compose.yaml.bak` copy. Builds on `2026-10-14-stackstead-v1-design.md` (§5.3 edit, §6 file integrity), `2026-10-14-stackstead-cli-design.md`, `2026-10-15-stackstead-networks-multiservice-design.md` and `2026-10-18-stackstead-compose-forms-design.md` (§7.1 file set, §9 env files).

## 1. Purpose

Today a save keeps exactly one previous version, as `compose.yaml.bak` next to the file. A second bad save, an edit made in a shell, a change to an override file, or a change to `.env` (an image tag, `COMPOSE_PROJECT_NAME`) leaves no record, so "what changed since it last worked?" has no answer.

In this spec, a stack's **configuration** is:

- its compose file set, exactly as `Service.fileSet(e)` returns it (compose-forms §7.1): the base file, the default override or the explicit extra files, in order;
- the stored `ExtraFiles` list (`[]` means compose's default set);
- the `.env` in the base file's folder.

Success criteria:

- Every change Stackstead makes to a stack's configuration is recorded as a revision with its time, source and actor. This covers file contents and file-set changes. A configuration identical to the latest revision records nothing.
- Changes made outside Stackstead are recorded as `external` revisions before compose acts on them and before Stackstead overwrites them.
- Any two revisions, or a revision and the current files, can be compared as a unified diff in the UI and the CLI.
- A revision can be restored after a preview that shows the diff against the current files and the result of the same validation a save runs. A restore brings back every file of the set, `.env` and the file list together. It never writes anything when validation fails, and never overwrites a file that changed since the preview. "Restore and Up" applies it in one step.
- `.env` values are never stored in clear in the database and are masked in the API unless explicitly revealed.
- History survives stack removal and can be purged explicitly.
- Saves no longer write `compose.yaml.bak`. The previous version is always in History.

## 2. Scope

### In
- Backend: package `internal/history` (store, `.env` encryption and masking, retention); one schema migration; the change hooks in `stacks` (§5.6), called from the save, create, import, restore, file-set and env-file paths and before lifecycle actions; candidate-set validation with a candidate `.env`; history routes; two config keys.
- `composefile.Save` stops writing `.bak`.
- Frontend: **History** tab on stack detail with diff and restore dialog; read-only history of deleted stacks with purge; save toast and editor text point at History.
- CLI: `stackstead stacks history ls|show|diff|restore|purge`.
- Tests, docs (`docs/api.md`, `docs/security.md`, `docs/cli.md`, README).

### Out
- **Git export** (local or remote repositories, push, credentials). Deferred to a later spec. Revisions are append-only and ordered per stack, so an exporter can replay them later.
- Files outside the configuration: `env_file:` targets other than the stack's own `.env`, `configs:`/`secrets:` files, Dockerfiles and build contexts (see §14).
- Profiles (`Entry.Profiles`, compose-forms §8). They select which services run; they are not file content. Restore leaves them as they are.
- A `.env` editor of its own. `.env` is written by restore here, and by compose-forms' env-file editor when a service lists `.env` as an `env_file` (§5.6).
- Notes or messages on revisions (user decision).
- Attribution beyond the single admin account.
- Scheduled or automatic rollback.

## 3. Key decisions

The TypeSafe skill named in the global rules cannot run here (the installed `typesafe-ai` skill is an AI-feature SDK, not a decision scorer). Decisions marked *user* were put to the user; the others follow from existing code and conventions and are argued here.

| Decision | Choice | Reason |
|---|---|---|
| What is versioned (*user*) | The whole compose file set (base, default override or explicit extra files), the stored `ExtraFiles` list, and `.env` | These are everything compose reads to build the project, so a revision describes exactly what ran. Each file is small, and a set has at most 9 files (compose-forms caps extra files at 8). |
| `.env` secrets (*user*) | Encrypted with AES-256-GCM using `<data_dir>/history.key` (0600); masked in the API unless `reveal=true` | A copied database alone leaks no `.env` value, and full restore still works. |
| Storage (*user*) | SQLite: revisions, file rows and blobs deduplicated by sha256 | Fits "no external database", is included in `db backup`, and retention runs in one transaction. |
| Git export (*user*) | Deferred to a later spec | |
| Retention (*user*) | By age with a per-stack floor: `history_retention` (default `2160h`, 90 days) and `history_min_revisions` (default 20). The newest and the last-deployed revision are never pruned | Recent history stays in full, and a quiet stack never loses its last good configurations. |
| External edit detection (*user*) | Lazy hash checks: at startup, before every save, restore and lifecycle action, and when stack detail or history is read | No new dependency and no background work. Every change is caught before compose runs it or Stackstead overwrites it. |
| Stack removal (*user*) | History is kept. Purge is explicit. A stack registered again under the same name continues the same history | Removal never deletes files either. The record of what ran stays available. |
| UI and CLI (*user*) | History tab on stack detail, plus `stacks history` CLI commands | Matches the existing tab layout and the `stacks compose get\|edit\|put` commands. |
| `compose.yaml.bak` (*user*) | No longer written. Existing `.bak` files are left on disk untouched | History holds every previous version. Deleting files the user may rely on is not Stackstead's call. |
| Revision notes (*user*) | None | |
| Revision identity | Per-stack sequence `seq` (1, 2, …). A file version is the sha256 of its plaintext | `composefile.Hash` is already the version identity in stale-file checks, so a revision's compose hash is directly comparable with a save's `hash`. |
| No-op changes | Recorded only when at least one file's hash differs from the latest revision | Repeated identical saves would otherwise fill history with empty diffs. |
| Recording point | Under the stack lock `commit` already holds: an external check before the write, then a `save` revision after it | The file on disk is the source of truth (v1 design). The lock keeps revision order equal to write order. |
| History store failure (*user*) | The pre-write check failing refuses the change with 503. Recording after a successful write logs only | Without `.bak`, the pre-write snapshot is the only copy of what is about to be overwritten. After the write, the new content is on disk and the next check records it, with an `external` attribution. |
| Writers outside `commit` (*user*) | Compose-forms' env-file save (when its target is the stack's `.env`) and `SetFiles` call the same two hooks as `commit` (§5.6) | One rule for every writer: snapshot before, record after, both under the stack lock. |
| Restore | Validates the whole candidate configuration, then writes every file and the file list as one change, writing a **new** revision (`source: restore`, `restoredFrom`) | Restore is exactly as safe as a raw save and uses the same checks. History stays append-only. |
| Candidate `.env` during validation | `compose config --env-file <temp copy>` | Compose then reads the candidate `.env` without the real file being written first, so a failed validation writes nothing. |
| Files that leave the set on restore | An explicit extra file that is not in the revision's list is dropped from the list and left on disk. A default override that is not in the revision is deleted, because compose would otherwise still pick it up | Same stance as stack removal (files stay) except where leaving the file would change what runs. The deleted override's content is in history. |
| Up after restore | Only with "Restore and Up" (`up: true`), as a normal operation after the write | Matches "file first, explicit Up applies". |
| `.env` masking in diffs | Each value is replaced by `‹redacted:xxxxxxxx›`, the first 8 hex digits of HMAC-SHA256(maskKey, value) | A unified diff still shows which values changed and which are equal, without showing them. The mask key is derived from the history key, so tags cannot be brute-forced from the database alone. |
| Secrets inside `compose.yaml` | Stored in plaintext, like the file | `GET /api/stacks/{name}/compose` already returns the file in full to the same user. Encrypting a copy adds nothing. |
| Purge of registered stacks | Refused. Purge applies only to stacks no longer registered | Live stacks are bounded by retention. Purging one would remove the pre-write snapshots the dropped `.bak` relied on. |

## 4. Data model

### 4.1 Migration

One entry appended to `migrations` in `internal/store/store.go`:

- `history_blobs(hash TEXT PRIMARY KEY, size INTEGER NOT NULL, encrypted INTEGER NOT NULL, content BLOB NOT NULL)`. `hash` is the sha256 of the plaintext. For `.env` blobs `content` is `nonce || ciphertext`. For compose blobs it is the plaintext.
- `history_revisions(id INTEGER PRIMARY KEY, stack TEXT NOT NULL, seq INTEGER NOT NULL, created_at INTEGER NOT NULL, source TEXT NOT NULL, actor TEXT NOT NULL, restored_from INTEGER, deployed_at INTEGER, extra_files TEXT NOT NULL DEFAULT '[]', UNIQUE(stack, seq))`, plus an index on `(stack, created_at)`. `extra_files` is the stored `ExtraFiles` list as JSON, exactly as the registry holds it (compose-forms §7.1).
- `history_files(revision_id INTEGER NOT NULL REFERENCES history_revisions(id) ON DELETE CASCADE, position INTEGER NOT NULL, path TEXT NOT NULL, role TEXT NOT NULL, blob_hash TEXT REFERENCES history_blobs(hash), present INTEGER NOT NULL, PRIMARY KEY(revision_id, position))`.

Rows of `history_files`:

- `role` is `base`, `override` (the default override compose discovers), `extra` (an `ExtraFiles` entry) or `env`.
- `position` is the file's place in the set: 0 for the base, then the set order. `.env` is always position -1.
- `path` is the base's file name for the base; the path as written in `ExtraFiles` for extra files (relative to the base's folder, or absolute); the file name for the default override; and `.env`.

Every revision has a row for every file of its set and for `.env`, changed or not, so a revision can be read without walking back. An absent `.env` is a row with `present = 0` and a NULL `blob_hash`. An `ExtraFiles` entry whose file is missing is a row with `present = 0`. Deleting either by hand is a recorded change. `history_revisions.stack` is the stack name, with no foreign key to `stacks`, so history outlives the registry row (§5.5).

Before compose-forms ships, `fileSet(e)` is `[base]` and `ExtraFiles` is `[]`, so a revision is the base plus `.env`. Nothing changes in the schema when it ships.

### 4.2 Go types (`internal/history`)

```go
type Source string // "create" | "import" | "save" | "external" | "restore"

type Role string // "base" | "override" | "extra" | "env"

type FileState struct {
    Position int    // 0 = base, set order; -1 = .env
    Path     string // as described in §4.1
    Role     Role
    Present  bool
    Hash     string // sha256 of plaintext; "" when absent
    Changed  bool   // content differs from the file with the same path in the previous revision, or it is new
}

type Revision struct {
    Stack        string
    Seq          int
    CreatedAt    time.Time
    Source       Source
    Actor        string // admin username; "" for external
    RestoredFrom int    // 0 when not a restore
    DeployedAt   *time.Time
    ExtraFiles   []string // as stored in the registry
    SetChanged   bool     // ExtraFiles or the set's paths differ from the previous revision
    Files        []FileState
}

type FileContent struct{ Position int; Path string; Role Role; Present bool; Data []byte }

type Snapshot struct {
    ExtraFiles []string
    Files      []FileContent // the set in order, then .env
}

type Store struct{ /* *sql.DB, AEAD, mask key, clock */ }

func Open(db *sql.DB, keyPath string, now func() time.Time) (*Store, error)
func (s *Store) Record(ctx context.Context, stack string, src Source, actor string, snap Snapshot, restoredFrom int) (rev Revision, recorded bool, err error)
func (s *Store) List(ctx context.Context, stack string, beforeSeq, limit int) ([]Revision, error)
func (s *Store) Get(ctx context.Context, stack string, seq int) (Revision, error)
func (s *Store) Snapshot(ctx context.Context, stack string, seq int) (Snapshot, error) // plaintext
func (s *Store) Content(ctx context.Context, stack string, seq int, position int) (FileContent, error) // plaintext
func (s *Store) Latest(ctx context.Context, stack string) (Revision, error)
func (s *Store) MarkDeployed(ctx context.Context, stack string, at time.Time) error // latest revision
func (s *Store) Stacks(ctx context.Context) ([]StackHistory, error) // every stack name with history
func (s *Store) Purge(ctx context.Context, stack string) (int, error)
func (s *Store) Prune(ctx context.Context, maxAge time.Duration, minKeep int) (int, error)
```

`Record` compares the whole snapshot with the latest revision: `ExtraFiles`, then each file by position, path, presence and hash. Any difference records a revision. `StackHistory{Stack string; Revisions int; LatestAt time.Time}`. The store encrypts `.env` on write and decrypts on read. Callers never see ciphertext. `Mask(data []byte) []byte` lives in the same package and is used by the API and CLI paths that return `.env` text.

### 4.3 Key file

- `<data_dir>/history.key`: 32 random bytes. Created by `serve` on first start with `O_EXCL`, mode 0600. A key file readable by group or others is refused at start, with a message naming the file.
- The encryption key and the mask key are derived with HKDF-SHA256 (`info` `stackstead history enc v1` and `stackstead history mask v1`).
- `db backup` copies the database only. Without the key, `.env` revisions cannot be read or restored. `compose.yaml` revisions are unaffected. `docs/cli.md` and `docs/security.md` tell operators to back up the key with the database.
- If the key is missing while encrypted blobs exist, `serve` refuses to start and explains how to restore the key or purge `.env` history (`stackstead db history-reset-env`, a host admin command that deletes encrypted blobs and marks those file rows unreadable). A new key is never generated silently over existing ciphertext.
- `db` and `admin` host commands never create the key.

### 4.4 Retention

`Prune` runs at start and then hourly, the same cadence as the metrics prune. For each stack, registered or not, it deletes revisions older than `history_retention` except:

- the newest `history_min_revisions` revisions,
- the newest revision,
- the newest revision with `deployed_at` set.

Unreferenced blobs are deleted in the same transaction. Config keys in `internal/config/config.go` and the commented template: `history_retention` (duration, default `2160h`, minimum `24h`) and `history_min_revisions` (int, default 20, minimum 1). `STACKSTEAD_HISTORY_RETENTION` and `STACKSTEAD_HISTORY_MIN_REVISIONS` override them, as for every key. `config validate` rejects values below the minimums.

## 5. When a revision is recorded

### 5.1 Writes by Stackstead

| Path | Source |
|---|---|
| `Service.commit` (raw save of any file in the set, structured edit, add service, remove service, secret definition changes from compose-forms) | `save` |
| `SetFiles` (compose-forms §7.5) | `save` |
| Env-file save whose target is the stack's `.env` (compose-forms §9.3) | `save` |
| `Create` | `create` |
| `Import` | `import` |
| Restore (§6) | `restore` |

All of them go through the hooks in §5.6. A snapshot is always the whole configuration: the current `fileSet(e)`, `ExtraFiles` and `.env`, read after the change.

### 5.2 Changes made outside Stackstead

The check (`beforeChange`, §5.6) builds the current snapshot and records an `external` revision with an empty actor when it differs from the latest revision. That catches a hand edit of any file in the set or of `.env`, and an override file added or removed by hand (the default set changes). It runs under the stack lock and costs one read per file (at most 10) and one indexed query. It records nothing when the base file is missing or the set cannot be resolved (the stack is already `unavailable`), or when the stack has no history yet and the check is not at startup.

Check points:

- **Startup**, after `Sync`, for every registered stack. A stack with no history gets an `import` revision as its baseline. This is the upgrade path: existing stacks get their first revision on the first start of the new version.
- **Before every change**: in every path in §5.1. A failure refuses the change with 503 "configuration history is unavailable; the current configuration could not be recorded" (§3).
- **Before lifecycle actions**: in `Service.Act` and `Service.Remove`, before the operation starts. A failure here is logged and does not block the action (nothing is overwritten).
- **On read**: in `Service.Get` (stack detail) and in the history list handler, so the History tab is current when opened. A failure is logged only.

### 5.3 Who made the change

There is one admin account, so `actor` is the username of the request's session. The API layer puts it in the context (`history.WithActor(ctx, username)`) and the recorder reads it from there. Requests from the CLI carry the same session and are attributed the same way. `external` revisions have an empty actor and are shown as "outside Stackstead".

### 5.4 Deployed marker

When an `up` operation succeeds, `MarkDeployed` sets `deployed_at` on the stack's latest revision. The Act check (§5.2) has just made that revision match the files compose ran. The UI marks it **deployed**, so the operator can see which revision is running. Other actions do not set it.

### 5.5 Stack removal and re-registration

- `Remove` leaves history untouched. The stack then appears in `GET /api/history` with `registered: false`.
- Stackstead has no rename, so history is keyed by stack name. Creating or importing a stack under a name that has history continues its sequence. The first revision of the new registration is `create` or `import`, and the diff against the previous revision shows what changed.
- `DELETE /api/history/{stack}` purges the history of a stack that is not registered (409 when it is).

### 5.6 Change hooks (the interface other writers call)

Two unexported methods on `*stacks.Service`, in `internal/stacks/history.go`. Every code path that changes a stack's configuration calls them, including compose-forms' env-file save and `SetFiles`, which live in the same package:

```go
// beforeChange records the configuration on disk as an "external" revision
// when it differs from the latest one. The caller holds the stack lock.
// A non-nil error means the caller must refuse the change (API: 503).
func (s *Service) beforeChange(ctx context.Context, e Entry) error

// afterChange records the configuration on disk with src and the actor
// from ctx. The caller still holds the stack lock. It returns the new seq,
// or 0 when nothing changed or recording failed (logged, never returned).
func (s *Service) afterChange(ctx context.Context, e Entry, src history.Source) int

// snapshot reads fileSet(e), e.ExtraFiles and the .env next to the base.
func (s *Service) snapshot(e Entry) (history.Snapshot, error)
```

Rules for callers:

1. Take the stack lock, then call `beforeChange`, before reading the hash that the stale-file check compares, and before writing anything.
2. Write.
3. On success, call `afterChange` before releasing the lock. On failure, do not call it.

Compose-forms' env-file save follows these rules when the target's real path (after `fsguard.Resolve`) equals the stack's `.env` real path. Saves of other env files do not call the hooks, because they are not part of the configuration. `SetFiles` calls the hooks even though it writes no file, because the set and `ExtraFiles` change. The env-file save response and the `SetFiles` response carry `revision` like the save response (§7). A stack with no `Service.history` (tests, or a nil store) gets no-op hooks.

## 6. Restore

### 6.1 Steps

1. **Preview** (`Service.PreviewRestore(ctx, stack string, seq int, reveal bool) (RestorePreview, error)`): runs `beforeChange`, builds the candidate configuration from `Store.Snapshot`, and validates it without writing (§6.2). The result holds:
   - per file, the current and candidate contents, paired by path (`.env` masked unless revealed);
   - the current and candidate `ExtraFiles` lists;
   - the files that would be deleted (§6.3);
   - the validation result (`ok`, `error`, `fields`, `checksPartial`);
   - `sharedWith` (§6.3);
   - `configHash`, the sha256 over the current snapshot's `ExtraFiles` and every file's path, presence and hash.
2. **Restore** (`Service.Restore(ctx, stack string, seq int, configHash string, confirmShared []string, up bool) (RestoreResult, error)`): takes the stack lock and runs `beforeChange`. It returns 409 when the current `configHash` differs from the one sent, then validates again (§6.2). It writes the files (§6.4), stores `ExtraFiles`, claims a changed compose project the way `commit` does, and calls `afterChange` with `restore`. With `up`, it starts an `up` operation and returns it.

`RestorePreview` and `RestoreResult` are wire types in `apitypes`. One `configHash` replaces per-file hashes because the set itself can differ between preview and restore.

### 6.2 Validation of the candidate

`commit`'s checks are split out so save and restore share them:

```go
type CandidateSet struct {
    ExtraFiles []string
    Files      []CandidateFile // set order; Data nil = file must not exist
    Env        *[]byte         // nil = no .env
}
func (s *Service) checkCandidate(ctx context.Context, e Entry, c CandidateSet) (checkResult, error)
```

- The paths in the candidate's `ExtraFiles` must pass compose-forms' §7.1/§7.5 rules again (inside the stack folder or `allowed_roots`, at most 8, `.yaml`/`.yml`). Allowed roots may have changed since the revision was recorded.
- Ports, IPs and the other `commit` checks run on the candidate set as compose-forms' `commit` runs them on a file set.
- `compose config` and `ProjectName` run on the candidate set. Every candidate file is written as a `.stackstead-check-*.yaml` temp file next to the file it replaces, generalising `validateCandidate` from one swapped position to many. The candidate `.env` is passed as `--env-file` with a temp copy (mode 0600, removed after). `composecli.Invocation` (compose-forms §7.2, the argument `Compose.Config` and `Compose.ProjectName` already take) gains `EnvFile string`, passed as `--env-file` when set.
- Nothing is written to a real file before validation passes.

### 6.3 Files leaving the set, and shared files

- **Explicit extra files** in the current list but not in the revision's are dropped from `ExtraFiles` and left on disk.
- **A default override** that exists now but is not in the revision's set (the revision used the default set without one) is deleted, because compose would otherwise still merge it. The preview lists it under "will be deleted".
- **Files present now but absent in the revision**, such as a missing `.env` or a missing explicit extra file, are deleted, and the preview lists them the same way. No `.bak` copies are written, consistent with dropping `.bak` and with compose-forms' rule against extra plaintext copies of secrets. Their content is in history.
- **Shared files.** An extra file outside the stack folder may also be in another registered stack's set. `sharedWith` lists those stacks, and `confirmShared` must equal that list (409 otherwise, the same pattern as compose-forms' shared env files §9.2).

### 6.4 Writing

- **Order:** `.env` first, then the files of the set from last to first, with the base last. Then `ExtraFiles` is saved in the registry.
- **Rollback:** before writing, the current bytes (or absence) of every file to be touched are read under the lock. If any write, delete or the registry update fails, every file already changed is put back from those bytes, the registry is left as it was, and the error is returned. All writes are atomic (`writeAtomic`), and files keep their modes. New files are created 0644, and `.env` 0600.
- **No change:** restoring a revision equal to the current configuration returns 409 "already the current configuration".
- **Validation failure:** 400 with compose's error text or field errors, the same as a raw save. Nothing is written.
- **Unregistered stack:** a stack with history but no registry entry cannot be restored (404 from the stack lookup). The UI offers to view its history only.
- **Profiles:** active profiles are unchanged (§2 Out). If a profile named in `Entry.Profiles` is no longer declared by the restored set, the preview shows a warning, and the profile is dropped from the active set on restore, as compose-forms' `SetProfiles` rules require.

## 7. `.bak` removal

- `composefile.Save` no longer writes `path + ".bak"`. Its doc comment and `disk_test.go` change accordingly (a test asserts no `.bak` is created).
- Existing `.bak` files are never read, moved or deleted. The README and `docs/api.md` say they are left over from earlier versions and can be deleted by hand.
- `ComposeEditor.svelte`: the success toast becomes "Saved — revision #N is in History" with a link to the History tab, and the hint under the editor becomes "Every save is kept in History. Comments and key order are preserved." The `composePath`-based backup name goes away. `ComposeEditor.test.ts` cases that assert `.bak` text change to the History text.
- `EditResult` gains `Revision int` (0 when recording failed after the write), and `hashResponse` gains `revision`. The toast falls back to "Saved" when it is 0.

## 8. API

All routes require a session. `POST` routes require `Content-Type: application/json` and a body (existing rule). Error bodies are `{error}`, and validation errors are `{error, fields}`. Wire types go in `internal/apitypes`.

| Method and path | Purpose |
|---|---|
| `GET /api/history` | every stack with history: `[{stack, registered, revisions, latestAt}]`, sorted by name |
| `GET /api/stacks/{name}/history?before=&limit=` | revisions newest first. `limit` defaults to 50, at most 200. `before` is a `seq`. Works for unregistered stacks |
| `GET /api/stacks/{name}/history/{seq}` | one revision |
| `GET /api/stacks/{name}/history/{seq}/files/{ref}?reveal=` | one file's content. `ref` is `env` or a set position (`0` = base). `.env` is masked unless `reveal=true` |
| `GET /api/stacks/{name}/history/diff?from=&to=&reveal=` | both sides of each file that differs, plus both `extraFiles` lists. `from` is a `seq`, `to` a `seq` or `current`. `to` defaults to the revision after `from` |
| `POST /api/stacks/{name}/history/{seq}/restore/preview` | §6.1 step 1. Body `{reveal?}` |
| `POST /api/stacks/{name}/history/{seq}/restore` | §6.1 step 2. Body `{configHash, confirmShared, up}`. Returns 200 `{revision, hash}` (`hash` is the base file's new hash), or 202 `{revision, hash, operation}` with `up` |
| `DELETE /api/history/{stack}` | purge a stack that is not registered. 204, or 409 when registered |

Revision object:

```json
{"seq": 14, "createdAt": "2026-10-18T09:12:44Z", "source": "save", "actor": "admin",
 "restoredFrom": 0, "deployedAt": "2026-10-18T09:13:02Z",
 "extraFiles": ["compose.prod.yaml"], "setChanged": false,
 "files": [{"position": 0, "role": "base", "path": "compose.yaml", "present": true, "hash": "9f…", "changed": true},
           {"position": 1, "role": "extra", "path": "compose.prod.yaml", "present": true, "hash": "c2…", "changed": false},
           {"position": -1, "role": "env", "path": ".env", "present": true, "hash": "41…", "changed": false}]}
```

Diff and preview responses carry `files: [{path, role, original, modified, originalPresent, modifiedPresent}]`, paired by `path`, and `extraFiles: {original, modified}`. The preview adds `deletes: [path]`, `sharedWith`, `warnings`, `validation` and `configHash`. The UI passes `original` and `modified` to `DiffView`. A masked `.env` line reads `DB_PASSWORD=‹redacted:3f9a1c07›`. Comments, blank lines, `export` prefixes and quoting are kept. A line that does not parse as `KEY=VALUE` is masked whole as `‹redacted line:xxxxxxxx›`.

Responses with `reveal=true` carry `Cache-Control: no-store`. A stack with no history returns 404, as does an unknown `seq`. `.env` content of a revision whose blob is unreadable (§4.3) returns 410. Startup, save and restore errors from §5.2 return 503.

## 9. Frontend

### 9.1 History tab (`web/src/lib/stacks/HistoryPanel.svelte`)

- `StackDetail.svelte` `TabId` adds `history`, after `compose`. Its URL is `?tab=history&rev=<seq>`, so a revision is bookmarkable.
- **Left: the revision list**, newest first, loaded 50 at a time with "Load older". Each row shows `#seq`, `RelativeTime`, a source chip (`saved`, `outside Stackstead`, `restored from #n`, `created`, `imported`), the actor, a chip for each changed file, and a **deployed** badge on the last-deployed revision.
- **Right: the selected revision.** "Compare with" is a segmented control: **Previous** (default) or **Current files**. When the `ExtraFiles` lists differ, a line above the diff shows "Compose files: a, b → a, c". A file switcher lists, with a role badge (`base`, `override`, `extra`, `.env`), only files that differ, with "No differences" when none do. The diff is `DiffView`. A file that was added or removed shows as a full add or delete.
- `.env` diffs are masked. **Reveal values** refetches with `reveal=true` and holds the result in component state only. Values are dropped when the selection changes or the tab closes.
- **Restore this revision…** is shown on every revision except the one equal to the current files. Its tooltip says why it is hidden there.
- Refresh: the list reloads on mount, after a restore, after a save in the Compose tab, and on `/api/events` `change` for the stack's project (debounced, the existing helper).

### 9.2 Restore dialog (`web/src/lib/stacks/RestoreDialog.svelte`)

- Opens with a call to the preview. It shows a spinner, then:
  - the file-list change, if any;
  - the diff per file (current → candidate);
  - "Will be deleted" files;
  - the validation result: a success line, or the compose error in `<pre>` plus any field errors;
  - a `checksPartial` warning when Docker was unreachable, and profile warnings.
- When `sharedWith` is not empty, a checkbox "Also changes stacks: x, y" must be ticked before the buttons enable. It sends `confirmShared`.
- Buttons: **Restore** and **Restore and Up**, both disabled when validation failed, and **Cancel**.
- A 409 for changed files shows the existing stale-file banner text with **Refresh preview**. Success shows a toast ("Restored #n as revision #m"). With Up, the dialog navigates to the operation in the Operations tab, as other actions do.
- A dirty Compose tab draft blocks opening the dialog, with the reason "Save or discard your compose edits first" (the existing `dirty` guard).

### 9.3 History of removed stacks

- Settings gets a **History** section listing `GET /api/history` entries with `registered: false`. Each has **View** (route `/history/:name`, a read-only `HistoryPanel` without restore) and **Purge** (`ConfirmDialog`: "Delete all N revisions of <name>? This cannot be undone."). Purge is never offered for registered stacks.
- The breadcrumb is "Settings › History › <name>".

### 9.4 API client and accessibility

- `lib/api/client.ts` adds `history`, `historyRevision`, `historyFile`, `historyDiff`, `restorePreview`, `restore`, `historyStacks` and `purgeHistory`. Types go in `types.ts`.
- The revision list is a `listbox` with `aria-selected`. The segmented control uses the existing `Tabs` pattern. Reveal is a toggle button with `aria-pressed`. Below the existing breakpoint the list stacks above the diff.

## 10. CLI

New subcommands under `stacks` in `internal/cli/cmd_stacks.go`, with remote API calls and `-o json` printing the API's JSON:

| Command | What it does |
| --- | --- |
| `stacks history ls <name> [--limit N] [--before SEQ]` | table: SEQ, TIME, SOURCE, ACTOR, CHANGED, DEPLOYED |
| `stacks history show <name> <seq> [--file base\|env\|PATH] [--reveal]` | print one file of a revision (default `base`); without `--file` and with `-o table`, the file list first |
| `stacks history diff <name> <from> [<to>\|current] [--file base\|env\|PATH] [--reveal]` | unified diff (`---`/`+++`/`@@`) of every differing file, or of one, plus the file-list change; coloured on a terminal |
| `stacks history restore <name> <seq> [--up] [--yes] [--confirm-shared] [--detach] [--reveal]` | print the preview (file list, diffs, deletes, validation), ask on a terminal (`--yes` required without one), restore. Shared files need `--confirm-shared` (exit 5 otherwise, naming the stacks). With `--up` it follows the operation like `stacks up` |
| `stacks history purge <name> [--yes]` | purge the history of a stack that is not registered |

- Unified diffs are computed client-side from the API's two sides by a small line-diff function (`internal/textdiff`, Myers algorithm with 3 lines of context, standard library only). This avoids a new dependency.
- Exit codes follow `docs/cli.md`: 4 for unknown stack or seq, 5 for a 409 (stale, already current, purge of a registered stack), 6 for failed validation, 7 for a 503, 9 when a waited `up` fails.
- `--reveal` output is never written to the contexts file or logs. `show --file env` without `--reveal` prints the masked text.
- `db history-reset-env` (host admin, server stopped, as for `db restore`) implements the key-loss recovery in §4.3.

## 11. Security

`docs/security.md` adds:

- History keeps every recorded compose file of the set in plaintext and every `.env` encrypted with `<data_dir>/history.key`. The key is not in database backups. Back it up with the database.
- Any signed-in user can reveal `.env` values from history. This grants nothing new (the same user controls Docker), but revealed values are sent with `no-store` and never logged.
- `.env` plaintext never appears in logs, error messages or operation output.

Other rules:

- Restore writes only the base, the `.env` beside it, the default override next to the base, and `ExtraFiles` paths that pass compose-forms' confinement rules at restore time (§6.2). File `ref`s in URLs are `env` or a position, never a path, so history routes cannot read arbitrary files.
- The candidate `.env` temp file used for validation is 0600 and removed in a `defer`.
- The purge and restore routes use the existing session and JSON CSRF rules. Purge is refused for registered stacks.

## 12. Testing

Test-first for each behaviour change (global rules).

- **history store** (`internal/history/store_test.go`): `TestRecordSkipsUnchanged`, `TestRecordDedupesBlobs` (two stacks with the same compose share one blob), `TestEnvStoredEncrypted` (raw blob bytes do not contain the plaintext, and `Content` returns it), `TestEnvAbsentIsRecorded` (absent → present → absent gives three revisions), `TestSeqPerStackContinuesAcrossReregistration`, `TestPurgeRemovesRevisionsAndOrphanBlobs`, `TestMissingKeyRefusesOverExistingCiphertext`, `TestKeyFileWithWidePermissionsRefused`.
- **retention** (`internal/history/prune_test.go`), as a table: old revisions pruned, the newest `min` kept whatever their age, the newest kept, the last-deployed kept even when older than `min`, unregistered stacks pruned by the same rule, and shared blobs kept while referenced.
- **masking** (`internal/history/mask_test.go`): equal values give equal tags, changed values give different tags, comments, blank lines, `export`, quoted and empty values keep their shape, an unparseable line is masked whole, and tags differ for different keys.
- **stacks** (`internal/stacks/history_test.go`):
  - Every `commit` path records `save` with the actor from the context.
  - A hand edit before a save records `external` first, then `save`.
  - A pre-write check failure refuses the save with 503 and leaves the file unchanged.
  - A record failure after the write still returns success with `Revision: 0`.
  - Failed validation records nothing.
  - `Act` records `external` before starting the operation.
  - A successful `up` sets `deployedAt`, and a failed one does not.
  - Startup gives every registered stack without history an `import` baseline.
  - `Remove` keeps history.
  - Re-importing the same name continues `seq`.
- **file set in history** (`internal/stacks/history_set_test.go`):
  - `TestSnapshotCoversWholeSet`: base, the default override, and explicit extras in order, plus `.env`.
  - `TestHandAddedOverrideRecordsExternal`: adding `compose.override.yaml` by hand changes the default set and records `external`.
  - `TestRawSaveOfOverrideRecordsSave`: a raw save of an extra file records `save`, and only that file is `changed`.
  - `TestSetFilesRecordsSetChange`: `setChanged` is true, and no file content changed.
  - `TestMissingExtraFileRecordedAbsent`.
- **change hooks** (`internal/stacks/history_hooks_test.go`):
  - `TestEnvFileSaveOfStackDotEnvCallsHooks`: an unrecorded hand edit is recorded `external` first, then `save`.
  - `TestEnvFileSaveOfOtherFileRecordsNothing`.
  - `TestEnvFileSaveRefusedWhenSnapshotFails`: 503, and the file is unchanged.
  - `TestSetFilesRefusedWhenSnapshotFails`.
  - `TestHooksNoopWithoutStore`.
- **restore** (`internal/stacks/restore_test.go`):
  - Preview returns diffs, the list change, deletes and validation, and writes nothing (every file byte-identical, `ExtraFiles` unchanged).
  - A changed `configHash` returns 409: a hand edit of any file, an added override, or a `SetFiles` call between preview and restore. Restoring the current configuration also returns 409.
  - Validation failure writes nothing.
  - The candidate `.env` is used for validation through `--env-file`: a set needing a variable from its own revision's `.env` validates, while the real `.env` still holds the old value.
  - `TestRestoreBringsBackFileList`: from `[a]` to `[b]` and back, the list and both files' contents match the revision.
  - `TestRestoreDropsExtraButKeepsFile`.
  - `TestRestoreDeletesDefaultOverrideNotInRevision`.
  - `TestRestoreRejectsExtraOutsideRootsNow`: the allowed roots shrank since the revision.
  - `TestRestoreRollsBackOnMidWriteFailure`: a failure injected on the third write leaves every file and the registry as before.
  - An absent `.env` in the revision deletes the file and writes no `.env.bak`.
  - Shared extra file: a missing or stale `confirmShared` returns 409, an exact one returns 200.
  - A profile no longer declared is warned in the preview and dropped on restore.
  - `up: true` returns an operation.
  - The new revision has `source: restore` and `restoredFrom`, and `afterChange` sees the restored list.
- **composefile**: `TestSaveWritesNoBak`, and an existing `.bak` is left byte-identical.
- **api**:
  - Every route needs a session.
  - `reveal` is off by default and sets `no-store` when on.
  - A file `ref` that is not `env` or a position of that revision returns 404.
  - The env-file save and `SetFiles` responses include `revision`.
  - Paging works with `before`/`limit`.
  - `to=current` diffs against the files on disk.
  - Purge of a registered stack returns 409.
  - Unregistered stacks' history is readable, and restoring one returns 404.
  - The save response includes `revision`.
- **config**: defaults, env overrides, and `validate` rejecting values below the minimums.
- **cli**: `TestStacksHistoryLsTable`, `TestHistoryDiffUnifiedOutput` (the `textdiff` golden cases: insert, delete, change, no-op, empty file), `TestHistoryRestoreNeedsYesWithoutTerminal`, and exit code mapping for 409, 400 and 503.
- **integration (Docker)**:
  1. Create a stack and save twice.
  2. Change `.env` and the image tag by hand, then Up. This records `external`, marked deployed.
  3. Add `compose.override.yaml` by hand to change a port, then Up.
  4. Restore revision 1 with `up: true`. The override is deleted, and the container runs the original tag, the original port and revision 1's environment.
  5. Remove the stack. Its history is still listed.
  6. Re-import it. `seq` continues.

  Also, `TestRestoreValidatesWithEnvFileFlag` checks that the installed compose honours `--env-file` for `config` and `config --format json` project names.
- **web (vitest)**:
  - `HistoryPanel.test.ts`: list rendering and chips, the deployed badge, previous/current compare switching, the file switcher hiding unchanged files, reveal fetching once and clearing on selection change, and paging.
  - `RestoreDialog.test.ts`: the file-list change and deletes shown, the shared-files checkbox gating the buttons and sending `confirmShared`, buttons disabled on failed validation, the stale 409 refresh flow, Restore and Up navigating to the operation, and blocking while the compose draft is dirty.
  - `ComposeEditor.test.ts`: the toast names the revision and links to History.
  - Settings history section: only unregistered stacks listed, and purge confirmation.
- **E2E smoke**: extend `TestE2ESmoke` with save → history list → diff → restore preview → restore.

## 13. Documentation

`docs/api.md` (§8, and the `.bak` note), `docs/security.md` (§11), `docs/cli.md` (§10, the config keys and key backup next to database backup), README feature line ("Configuration history: every change to a stack's compose files and .env is recorded, compared and restorable") and its edit paragraph (replace the `.bak` sentence).

## 14. Integration points with sibling specs

- **Richer compose forms** (`2026-10-18-stackstead-compose-forms-design.md`):
  - This spec depends on its `fileSet(e)`, `Entry.ExtraFiles`, `SetFiles`, `composecli.Invocation` and file-set `validateCandidate` (compose-forms §7.3, generalised in §6.2). Before it ships, the set is the base alone (§4.1).
  - Its env-file save (when the target is the stack's `.env`) and `SetFiles` must call `beforeChange`/`afterChange` (§5.6). Structured and raw edits through `commit` are covered already.
  - `env_file:` targets other than `.env` and secret files stay out of history. Neither keeps a `.bak` copy (compose-forms §3).
- **Image updates**: a tag bump written through `commit` is recorded like any save and can be undone with a restore.
- **Reachability checks**: check create, update and delete save through `commit`, so each records a `save` revision. Restore calls the checks engine's `Reload(stack)` after writing, as a compose save does, so restored checks take effect.
- **Notifications**: an `external` revision is a natural event to notify on. This spec only records it.
- **Git export** (future): replays revisions per stack in `seq` order. `.env` needs its own redaction rules there.
