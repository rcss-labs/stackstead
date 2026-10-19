# Stackstead Plan 14 — Mount fixes (V2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** tasks, names, signatures, test cases and examples only; no code bodies. Implementers write everything test-first.

**Goal:** Guarded fixes for the findings Plan 13 produces: `mkdir`, `chown` and `chmod` (optionally recursive, counted and capped) on bind sources strictly inside `allowed_roots`, run as root through the pinned helper container, with an exact command preview, a typed confirmation phrase, a `fix` operation with live output and cancel, and a persistent `mount.fix` row in the shared `action_log`.

**Architecture:** `internal/mounts` gains a `Fixer` (validation, command building, in-memory plans bound to the signed-in user, re-validation at apply, the `fix` operation, the action-log row and cache invalidation); `internal/api/mounts_fix.go` adds the three fix routes; the web adds `FixDialog`, a `confirmPhrase` prop on `ConfirmDialog`, **Fix…** buttons and a fix history on the Storage tab. The helper lifecycle (`engine.RunHelper`) and all reads come from Plan 13 unchanged.

**Tech Stack:** Go 1.26 standard library; Svelte 5, TypeScript, Vitest. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-18-stackstead-volume-diagnostics-design.md` §7.5–§7.7, §8, §10.2, §11, §12 (fixer, store, api, integration, web). **Depends on Plan 13** (`2026-10-19-stackstead-13-volume-diagnostics.md`) being merged: it uses `mounts.Collector`, `Report`, `Finding`, `MountEntry`, `IDMapping`, `HostFS`, `fixesAllowed`, `Scanner.Trigger`, `engine.RunHelper`/`HelperSpec`, `stacks.Ops.OnEnd` and the `MountsAPI` option.

## Spec divergences

1. **`action_log` already exists** (`internal/store/store.go:65-78`, `internal/actionlog/store.go`). No migration, no store work; the row is written through `actionlog.Store.Append` and read through `Recent(Filter{KindPrefix: "mount.fix", Scope: name, Limit: 100})`. `Append` already validates `kind` (`^[a-z]+\.[a-z]+$`), `scope` (a stack name) and requires `username` and `result`, which is exactly §8.5's mapping. `TestFixHistory_FiltersMountFixByStack` therefore lives in `internal/mounts` against a real SQLite store, not in `store`.
2. **"Bound to the session that created them"**: handlers only see `auth.User{ID, Username}` (`api/session.go:16`); the session token never reaches domain code. Plans are bound to the user id, and a plan created by another user is 409 "preview again". If a per-session bind is wanted, the handler would have to hash the cookie — raised as an open question in the report.
3. **`golangci-lint` is not configured**; verification is `go vet`, `gofmt -l`, `make test`, `make ui-check`, `make ui-test` (as in Plan 13).

## Global Constraints

- Everything in Plan 13's Global Constraints, plus:
- Fix safety (§8.3, §11): the target path comes from the server's current report, never the client; it is resolved with `fsguard.Resolve` (existing path) or the nearest existing ancestor (mkdir), must be strictly below one of `allowed_roots`, never the stack's compose directory or an ancestor of it, never the stacks dir, never under `DockerRootDir`, never on a read-only filesystem, never on macOS, never when path checks are unavailable, and only a fix kind the finding lists.
- uid/gid: integers `0 … 2147483646`, default to the finding's effective host ids, names refused; uid 0 allowed with the warning "root ownership is rarely what a container needs". Mode: `^0?[0-7]{3,4}$`, setuid refused, `o+w` warns "world-writable", a directory mode without owner `x` refused.
- Recursive: `chown`/`chmod` on directories only; `find -xdev` with `-h` (chown) or `! -type l` (chmod); counted first with the read-only helper, refused above 100 000 entries; the count goes into the confirm phrase; apply refuses when the count grew by more than 1 % or the device changed.
- Plans: in memory, 5 min, bound to the user; confirm phrase must match exactly; apply re-checks `Lstat` type, owner, mode, resolved path, device and count.
- Helper for fixes: `CapAdd ["CHOWN","FOWNER","DAC_OVERRIDE"]`, the nearest existing ancestor of the target bound read-write at `/t/0`, `UsernsMode: "host"` in `userns` mode; mount source re-verified after create (Plan 13's `ErrHelperMountMismatch`).
- Every applied fix writes one `mount.fix` row whatever the outcome; a refusal before start writes none; one `slog` Info line per fix.
- No `{@html}`; the confirm input has a visible label and `aria-describedby` pointing at the command block.

## Review Focus

1. The target directory is replaced by a symlink (or its owner changes) between preview and apply: 409 "the path changed since the preview; preview again", nothing runs, no row. (Task 4.)
2. A `chown` preview for a finding whose effective user is unknown and a request without `uid`: 400 with `fields: [{field: "uid"}]`, not a chown to 0. (Task 1.)
3. The recursive count boundary: exactly 100 000 entries is allowed, 100 001 is refused with the copy-paste command; the counting helper is itself capped (`head -n 100001`). (Task 3.)
4. Apply while an `up` runs on the same stack: 409 `ErrBusy`, plan kept (not consumed), no row. (Task 4.)
5. Mode spellings: `755`, `0755`, `2775`, `1777` accepted; `4755` refused (setuid); `0644` on a directory refused (no owner `x`); `777` accepted with the world-writable warning. (Task 1.)

---

## File structure

| Area | Files | Tasks |
|---|---|---|
| Validation | `internal/mounts/fix_validate.go` | T1 |
| Commands and helper specs | `internal/mounts/fix_commands.go` | T2 |
| Plans and preview | `internal/mounts/fixer.go`, `fix_plan.go` | T3 |
| Apply, recording, history | `internal/mounts/fix_apply.go`, `fix_history.go` | T4 |
| API | `internal/api/mounts_fix.go` (new), `internal/api/server.go`, `internal/apitypes/types.go`, `internal/cli/serve.go` | T5 |
| Web | `web/src/lib/components/ConfirmDialog.svelte`, `web/src/lib/stacks/FixDialog.svelte` (new), `FixHistory.svelte` (new), `StoragePanel.svelte`, `web/src/lib/api/types.ts`, `client.ts` | T6 |
| Docs, integration | `docs/api.md`, `docs/security.md`, `internal/mounts/integration_test.go`, `integration_rootless_test.go` | T7 |

Order: T1 → T2 → T3 → T4 → T5 → T6 → T7. Sonnet implements test-first, Opus reviews each task, Fable reviews the branch.

---

### Task 1: Fix validation

Spec §8.3. Review focus 2 and 5.

**Files:**
- Create: `internal/mounts/fix_validate.go`
- Test: `internal/mounts/fix_validate_test.go` (temp dirs as roots; `fsguard.Resolve` is real)

**Interfaces:**
- Consumes (Plan 13): `Finding`, `MountEntry`, `EffectiveUser`, `HostFS`, `HostInfo`, `fixesAllowed`.
- Produces: `type FixKind string` (`FixMkdir = "mkdir"`, `FixChown`, `FixChmod`); `type FixRequest struct { FindingID string; Fix FixKind; UID, GID *uint32; Mode string; Recursive bool }`; `type FixEnv struct { AllowedRoots []string; StacksDir, ComposeDir, DockerRootDir, GOOS, PathChecks string; FS HostFS }`; `type FixTarget struct { HostPath, Resolved, BindSource, Rel string; IsDir, Exists bool; Snapshot TargetSnapshot }` (`BindSource` is the nearest existing ancestor for mkdir, else `Resolved`; `Rel` is the path below `/t/0`); `type TargetSnapshot struct { Type string; UID, GID uint32; Mode fs.FileMode; Dev uint64; Resolved string; Entries int }`; `var ErrFixValidation` (400; carries `FieldError{Field, Message}` like `storage.FieldError`), `var ErrFixRefused` (409 with the reason); `func ValidateFix(req FixRequest, f Finding, entry MountEntry, user EffectiveUser, env FixEnv) (FixTarget, []string /*warnings*/, error)`; `func ParseMode(s string, isDir bool) (uint32, []string, error)`; `const MaxUID = 2147483646`.

- [ ] **Step 1: Write the failing test** `TestFixer_Validation` as a table: finding does not list the kind → refused; a root itself → refused "is an allowed root"; outside roots → refused; the compose dir and its parent → refused; the stacks dir → refused; a path under `DockerRootDir` → refused; `GOOS darwin` → refused; `PathChecks "unavailable"` → refused; `FS.ReadOnly` filesystem → refused; uid `2147483647` → validation `uid`; uid absent and `user.HostUID == nil` → validation `uid`; uid 0 → warning text; `Mode` table (focus 5); `Recursive` on a file → validation `recursive`; `Recursive` with `mkdir` → validation; a target that is a symlink → `Resolved` is the link target and must itself pass the roots check; mkdir of `/srv/a/b/c` where only `/srv/a` exists → `BindSource == "/srv/a"`, `Rel == "b/c"`.
- [ ] **Step 2: Run** `go test -race ./internal/mounts/ -run TestFixer_Validation` — FAIL: `undefined: ValidateFix`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — `ok`.
- [ ] **Step 5: Commit** `feat(mounts): validate fix requests against the allowed roots and the finding` (why: a fix must be refused before any plan exists, with a reason the operator can act on).

---

### Task 2: Commands and helper specs

Spec §8.1, §8.2, §8.4, §4.7 "Fixes (rootless/userns)".

**Files:**
- Create: `internal/mounts/fix_commands.go`
- Test: `internal/mounts/fix_commands_test.go`

**Interfaces:**
- Produces: `type FixCommands struct { Host []string; Helper [][]string }` (`Host` are the preview strings with the host path, `Helper` the argv arrays with `/t/0/<rel>`); `func BuildFixCommands(kind FixKind, t FixTarget, uid, gid uint32, mode string, recursive bool, m IDMapping) FixCommands` (host strings use host ids, helper argv uses container ids in `ModeRootless`, host ids otherwise; mkdir: `mkdir -p -- /t/0/<rel>` then `chown -h uid:gid -- <each created component>` and `chmod mode -- …` when given; chown recursive: `find /t/0/<rel> -xdev -exec chown -h uid:gid -- {} +`; chmod recursive: `find /t/0/<rel> -xdev ! -type l -exec chmod mode -- {} +`); `func FixHelperSpec(t FixTarget, cmds FixCommands, stack, opID string, m IDMapping) engine.HelperSpec` (image `HelperImage`, rw bind `t.BindSource → /t/0`, `CapAdd ["CHOWN","FOWNER","DAC_OVERRIDE"]`, `UsernsMode "host"` when `m.Mode == ModeUserns`, labels); `func CountHelperSpec(t FixTarget, stack string) engine.HelperSpec` (ro bind, `CapAdd ["DAC_READ_SEARCH"]`, `Cmd = ["sh","-c","find \"$1\" -xdev | head -n 100001 | wc -l","sh","/t/0/<rel>"]`); `func ConfirmPhrase(kind FixKind, hostPath string, recursive bool, entries int) string` (`chown /srv/media/config`, `chown -R /srv/media/config (12431 entries)`); `func RootlessOwnerWarning(t FixTarget, m IDMapping, daemonUID uint32) string` (non-empty when the target's owner is outside the daemon user's uid and ranges).

- [ ] **Step 1: Write the failing test** `TestFixer_HelperSpec`: every argv has `--` before each path; recursive uses `find -xdev` and `-h` / `! -type l`; chmod never follows symlinks; `HostConfig` fields asserted through the `HelperSpec` (caps, rw bind, labels, `UsernsMode`); count spec passes the path as `$1` and never interpolates it (a path with a space and a `'` stays one argv element); rootless: host string `sudo chown 100999:100999 /srv/x` while helper argv has `1000:1000`; `ConfirmPhrase` forms; `RootlessOwnerWarning` empty when the owner is inside the ranges.
- [ ] **Step 2: Run** — FAIL: `undefined: BuildFixCommands`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/mounts/` — `ok`.
- [ ] **Step 5: Commit** `feat(mounts): build fix commands and the helper specs that run them` (why: the operator sees the exact command that will run, and the argv shape is what keeps symlinks and other filesystems out of reach).

---

### Task 3: `Fixer.Preview` and plan storage

Spec §7.5, §8.4, §8.3 planId rule.

**Files:**
- Create: `internal/mounts/fixer.go`, `internal/mounts/fix_plan.go`
- Test: `internal/mounts/fixer_preview_test.go` (fake engine from Plan 13's `fakes_test.go`)

**Interfaces:**
- Produces: `type Plan struct { ID, Stack, FindingID, FindingCode string; Fix FixKind; Target FixTarget; UID, GID uint32; Mode string; Recursive bool; Entries int; Commands FixCommands; ConfirmPhrase string; Warnings []string; CreatedAt, ExpiresAt time.Time; UserID int64 }`; `type ActionWriter interface { Append(ctx, actionlog.Entry) error; Recent(ctx, actionlog.Filter) ([]actionlog.Entry, error) }`; `type FixerConfig struct { Collector *Collector; Engine Engine; Ops *stacks.Ops; Actions ActionWriter; Scanner interface{ Trigger(UnitKey) }; AllowedRoots []string; StacksDir string; GOOS string; FS HostFS; Now func() time.Time; Logger *slog.Logger }`; `func NewFixer(cfg FixerConfig) *Fixer`; `var ErrPlanNotFound` (409 "preview again"), `var ErrTooManyEntries` (409 with the copy-paste command in the message); `func (f *Fixer) Preview(ctx, userID int64, stack string, req FixRequest) (Plan, error)` (reads the current report through `Collector.Stack`, finds the finding and its entry, validates, counts when recursive, stores the plan, returns a copy).

- [ ] **Step 1: Write the failing tests.** `TestFixer_PreviewBuildsPlan` (finding `bind_autocreated` → plan with host command `chown -h 1000:1000 -- /srv/media/config`, `Entries 1`, phrase `chown /srv/media/config`, `ExpiresAt == now+5m`, `UserID`), `TestFixer_PreviewUnknownFinding` (→ `ErrFixValidation` field `findingId`), `TestFixer_PreviewRecursiveCounts` (fake helper stdout `12431` → `Entries 12431`, phrase with the count, the count spec was the one run; stdout `100001` → `ErrTooManyEntries` and the message contains `find … -exec chown`; exactly `100000` → allowed), `TestFixer_PreviewRootless` (host ids in `Commands.Host`, container ids in `Commands.Helper`, the owner warning when applicable), `TestFixer_PreviewDefaultsIDs` (no uid/gid → the finding's host ids), `TestFixer_PlanExpiry` (after 5 min the plan is gone), `TestFixer_PreviewRefusals` (each §8.3 refusal reaches the caller as `ErrFixRefused`; nothing stored).
- [ ] **Step 2: Run** — FAIL: `undefined: NewFixer`.
- [ ] **Step 3: Implement** (plan ids: 16 random bytes hex; at most 50 plans kept, oldest evicted).
- [ ] **Step 4: Run** `go test -race ./internal/mounts/` — `ok`.
- [ ] **Step 5: Commit** `feat(mounts): preview a fix as an expiring plan with a counted recursive walk` (why: the blast radius must be visible — the entry count — before anything can be confirmed).

---

### Task 4: `Fixer.Apply`, recording and history

Spec §7.6, §7.7, §8.5, §4.7 EPERM text. Review focus 1 and 4.

**Files:**
- Create: `internal/mounts/fix_apply.go`, `internal/mounts/fix_history.go`
- Test: `internal/mounts/fixer_apply_test.go` (real `actionlog.Store` on a temp `store.Open` database; fake engine; real `stacks.NewOps`)

**Interfaces:**
- Produces: `var ErrWrongPhrase` (400, field `confirm`), `var ErrPathChanged` (409 "the path changed since the preview; preview again"); `func (f *Fixer) Apply(ctx, userID int64, username, stack, planID, confirm string) (stacks.Operation, error)` (`Ops.Start(stack, "fix", fn)`; output: previewed host commands, then helper stdout/stderr lines, then `exit <code>`; on `ErrBusy` the plan is kept; the plan is consumed once the operation starts; cancel kills the helper through the operation context; a helper exit with `EPERM`/`Operation not permitted` in `ModeRootless` fails with "a rootless daemon cannot change ownership of paths owned by other host users; run the shown command with sudo"); `type FixDetail struct { Fix FixKind; Command string; UID, GID uint32; Mode string; Recursive bool; Entries int; FindingCode string; ExitCode int; Summary string }` (JSON keys `fix, command, uid, gid, mode, recursive, entries, findingCode, exitCode, summary`); `type FixHistoryEntry struct { ID int64; At time.Time; User, Target string; Detail FixDetail; Result, OperationID string }`; `func (f *Fixer) History(ctx, stack string) ([]FixHistoryEntry, error)`.
- Row (§8.5): `Entry{TS: op end, Username, Kind: "mount.fix", Scope: stack, Target: host path, Detail: FixDetail JSON, Bytes: nil, Result: succeeded|failed|cancelled|timed_out, OperationID}`; written from an `Ops.OnEnd` hook registered by `NewFixer` (matched by operation id), followed by `Collector.Invalidate(stack)` and `Scanner.Trigger(UnitKey{Stack: stack})`.

- [ ] **Step 1: Write the failing tests.** `TestFixer_PreviewApply` (happy path: operation `Action "fix"`, lines in the stated order, state succeeded, one row with every column of the §8.5 mapping, `Detail` round-trips, cache invalidated and `Trigger` called once), `TestFixer_ApplyWrongPhrase` (400, plan kept), `TestFixer_ApplyOtherUserOrExpired` (409 `ErrPlanNotFound`), `TestFixer_ApplyPathChanged` (owner changed / symlink swapped / device changed / count grew 2 % → `ErrPathChanged`, no operation, no row; count grew 0.5 % → runs), `TestFixer_ApplyBusy` (an `up` running → `stacks.ErrBusy`, plan kept, no row), `TestFixer_ApplyFailureAndCancelStillRecord` (helper exit 1 → `failed` row with `exitCode 1`; cancel → `cancelled` row; mount mismatch → failed with the error text), `TestFixer_RootlessEPERM` (the sudo message), `TestFixHistory_FiltersMountFixByStack` (rows `storage.delete`/host and `mount.fix`/other stack excluded; newest first; 101 rows → 100).
- [ ] **Step 2: Run** — FAIL: `undefined: (*Fixer).Apply`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/mounts/ ./internal/stacks/` — `ok`.
- [ ] **Step 5: Commit** `feat(mounts): apply a previewed fix as an operation and record it in the action log` (why: operations are in-memory and capped; the audit row survives restarts and sits in the same history as storage cleanups).

---

### Task 5: Fix API routes and wiring

Spec §7.5–§7.7, §7.8, §12 api.

**Files:**
- Create: `internal/api/mounts_fix.go`
- Modify: `internal/api/server.go:18-74` (`Fixes FixesAPI` option), `:138-143` (register the three routes when `Mounts` and `Fixes` are set), `internal/apitypes/types.go` (after the Plan 13 mount DTOs), `internal/cli/serve.go` (build `mounts.NewFixer` after the scanner; `Fixes: fixer`)
- Test: `internal/api/mounts_fix_test.go`

**Interfaces:**
- Produces: `type FixesAPI interface { PreviewFix(ctx, userID int64, stack string, req mounts.FixRequest) (mounts.Plan, error); ApplyFix(ctx, userID int64, username, stack, planID, confirm string) (stacks.Operation, error); FixHistory(ctx, stack string) ([]mounts.FixHistoryEntry, error) }`; routes `POST /api/stacks/{name}/mounts/fixes/preview`, `POST /api/stacks/{name}/mounts/fixes`, `GET /api/stacks/{name}/mounts/fixes`; DTOs `FixPreviewRequest{FindingID string; Fix string; UID, GID *uint32; Mode string; Recursive bool}` (`findingId`, `fix`, `uid`, `gid`, `mode`, `recursive`), `FixPlan{PlanID, Target string; Commands []string; Entries int; Recursive bool; ConfirmPhrase string; Warnings []string; ExpiresAt time.Time}` (`planId`, …, `expiresAt`), `FixApplyRequest{PlanID, Confirm string}`, `FixHistoryEntry{ID int64; At time.Time; User, Target, Fix, Command string; Recursive bool; Entries int; Result string; ExitCode int; OperationID, Summary string}` (`operationId`). Status mapping: `ErrFixValidation`/`ErrWrongPhrase` → 400 `{error, fields}`; `ErrFixRefused`, `ErrPlanNotFound`, `ErrPathChanged`, `ErrTooManyEntries`, `stacks.ErrBusy` → 409; unknown stack 404; `engine.ErrUnavailable` 503; the apply handler passes `userFrom(r.Context()).ID` and `.Username`.

- [ ] **Step 1: Write the failing tests.** `TestFixRoutesRequireSessionAndJSON`, `TestFixPreviewShape` (the §7.5 response JSON), `TestFixPreviewStatuses` (400 with `fields[0].field == "mode"`, 409 refused text, 409 too many entries), `TestFixApplyStatuses` (202 operation with `action == "fix"`; 400 confirm; 409 path changed; 409 busy; 404 stack), `TestFixApplyIgnoresClientPath` (body with an extra `path` field is accepted and the fake never sees it), `TestFixHistoryShape` (fields; empty list is `[]`), `TestFixRoutesNotRegisteredWithoutFixes`.
- [ ] **Step 2: Run** `go test -race ./internal/api/ -run Fix` — FAIL: `unknown field Fixes`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/api/ ./internal/cli/`, `go vet ./...`, `gofmt -l .` — clean.
- [ ] **Step 5: Commit** `feat(api): mount fix preview, apply and history routes` (why: the client names a finding and a kind; the server owns the path, the phrase and the plan).

---

### Task 6: Web — `ConfirmDialog.confirmPhrase`, `FixDialog`, Fix buttons and history

Spec §10.1 (Fix…, fix history), §10.2, §10.6.

**Files:**
- Modify: `web/src/lib/components/ConfirmDialog.svelte:6-42` (optional `confirmPhrase?: string`; when set, a labelled text input "Type `<phrase>` to confirm" with `aria-describedby` pointing at the message/command block; the confirm button stays disabled until the value equals the phrase exactly; the existing `confirmDisabledReason` still applies), `web/src/lib/components/components.test.ts:187-230`, `web/src/lib/api/types.ts` (`FixPreviewRequest`, `FixPlan`, `FixHistoryEntry`), `web/src/lib/api/client.ts` (`previewFix(name, body)`, `applyFix(name, planId, confirm)`, `fixHistory(name)`), `web/src/lib/stacks/StoragePanel.svelte` (**Fix…** button on findings whose `remedy.fixes` is non-empty; opens `FixDialog`; refreshes the report when the operation finishes; collapsed `<details>` **Fix history** at the bottom rendering `FixHistory`)
- Create: `web/src/lib/stacks/FixDialog.svelte`, `FixDialog.test.ts`, `web/src/lib/stacks/FixHistory.svelte`, `FixHistory.test.ts`

**Interfaces:**
- `FixDialog` props: `{ stack: string; finding: MountFinding; user: MountUser; open: boolean; onclose: () => void; onapplied: (op: Operation) => void }`. Step 1 (options): fix kind select when `finding.remedy.fixes.length > 1`; `uid`/`gid` number inputs prefilled from `user.hostUid`/`hostGid`; mode input for chmod/mkdir; **Apply to everything inside (recursive)** checkbox for chown/chmod on directories with the help text "Counts entries first; never follows symlinks or crosses filesystems"; **Preview** → `previewFix`; `fields[]` errors map to inputs via `Field`. Step 2 (preview): target, commands in a mono block (`id` referenced by the input's `aria-describedby`), entries for recursive, warnings, the typed phrase input (reusing the `ConfirmDialog` pattern or the same markup), **Run fix** → `applyFix` → `OperationPanel`; on finish calls `onapplied`. A 409 whose message starts with "the path changed" returns to step 1 with the message in a `Banner`; other 409s show the reason and keep step 1. `FixHistory` props: `{ stack: string }`; rows `at · user · fix · target · result` with a link to `/stacks/<stack>?tab=operations&op=<operationId>` while `operationId` is set.

- [ ] **Step 1: Write the failing tests.** `components.test.ts`: `ConfirmDialog` with `confirmPhrase: 'chown /srv/x'` renders the labelled input, the button is disabled until the exact phrase is typed (case-sensitive, trailing space does not match), `aria-describedby` points at the message. `FixDialog.test.ts`: prefilled ids from the user; preview posts the §7.5 body (`findingId`, `fix: 'chown'`, `uid: 1000`, `gid: 1000`, `recursive: false`); a `fields` error on `mode` marks the mode input invalid; preview shows the commands and the phrase; Run fix stays disabled until the phrase matches, then posts `{planId, confirm}` and renders the operation panel; a 409 "the path changed…" returns to step 1 with the banner; recursive checkbox only for `chown`/`chmod` on `type === 'dir'`; the `Type … to confirm` label is visible text. `FixHistory.test.ts`: renders rows newest first and links the operation. `StoragePanel.test.ts` (extend): Fix… appears only when `fixes` is non-empty; after `onapplied` the report is refetched; the history details element fetches `…/mounts/fixes` when opened.
- [ ] **Step 2: Run** `cd web && npm run test -- components FixDialog FixHistory StoragePanel` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make ui-test`, `make ui-check` — pass, 0 errors.
- [ ] **Step 5: Commit** `feat(web): fix dialog with command preview, typed confirmation and fix history` (why: the operator must read the exact command and type the phrase — including the entry count — before root touches the host).

---

### Task 7: Documentation and integration tests

Spec §13, §12 Integration (fix parts), §15.

**Files:**
- Modify: `docs/api.md` (the Plan 13 **Mounts** section: §7.5–§7.7 routes, request and response fields, status codes; `fix` in the operations list), `docs/security.md` (complete the Plan 13 section with §11's fix points: writes only strictly below `allowed_roots`, never the compose dir/stacks dir/Docker root, `fsguard` resolution, mount re-verification, symlinks never followed, no filesystem crossing, typed phrase with the count, plan bound to the user and expiring, re-validation at apply, per-stack lock, audit row), `README.md` (feature line already mentions "guarded fixes"; add one sentence under "Good to know" that fixes run as root in a locked-down helper and only inside `allowed_roots`), `internal/mounts/integration_test.go`, `internal/mounts/integration_rootless_test.go`

- [ ] **Step 1: Write the failing integration tests** (`//go:build integration`, real Docker, Linux): `TestIntegration_ChownFix` (from Plan 13's `not_writable` case: preview → apply with the phrase → operation succeeds → one `mount.fix` row with `result succeeded` → the next report has no `not_writable`), `TestIntegration_RecursiveCount` (create 1 234 files and dirs plus a symlink to `/etc` and a nested bind mount of another temp dir; `Entries` equals the walk without crossing into the nested mount; after a recursive chown the symlink target's owner is unchanged), `TestIntegration_RefusedOutsideRoots` (a finding whose source is outside roots → preview 409 and no helper container was created, checked by listing containers with the helper label), `TestIntegration_MkdirFix` (missing source → mkdir + chown → directory exists with the requested owner). Extend `TestIntegration_RootlessIDs` (`integration,rootless`): a chown fix through the helper produces the expected host owner; a target owned by host root → the operation fails with the sudo message.
- [ ] **Step 2: Run** `go test -tags integration -race -run 'TestIntegration_(ChownFix|RecursiveCount|RefusedOutsideRoots|MkdirFix)' -v ./internal/mounts/` — FAIL, then fix what the real daemon disagrees with.
- [ ] **Step 3: Write the docs.**
- [ ] **Step 4: Run** the integration tests again — `ok`; `make check` (vet, gofmt, Go tests, UI checks and tests) — clean; `make e2e` still passes (the smoke does not apply fixes, §12).
- [ ] **Step 5: Commit** in two commits: `test: integration coverage for mount fixes` and `docs: mount fix routes and the fix safety model`.

---

## Self-review notes

- Spec coverage: §7.5 T3/T5, §7.6 T4/T5, §7.7 T4/T5, §8.1 T2, §8.2 fix settings T2 (lifecycle from Plan 13), §8.3 T1, §8.4 T2/T3, §8.5 T4, §10.1 Fix…/history T6, §10.2 T6, §10.6 T6, §11 fix points T1–T4 and T7 docs, §12 fixer/store/api/integration/web T1–T7, §13 T7, §14 this plan, §15 T7's rootless job.
- Type consistency with Plan 13: `Finding`, `MountEntry`, `EffectiveUser` (`HostUID`), `IDMapping` (`ModeRootless`, `ModeUserns`), `HostFS`, `HostInfo`, `Collector.Stack/Invalidate`, `Scanner.Trigger(UnitKey)`, `engine.HelperSpec`, `HelperImage`, `fixesAllowed`, `Ops.OnEnd`, `MountsAPI` — all as defined there; new names introduced here (`FixKind`, `FixRequest`, `FixEnv`, `FixTarget`, `TargetSnapshot`, `FixCommands`, `Plan`, `Fixer`, `FixDetail`, `FixHistoryEntry`, `FixesAPI`) are used with the same spelling in every task.
- Existing tests change only by the additive `ConfirmDialog` prop (the spec's acceptance line allows it).
