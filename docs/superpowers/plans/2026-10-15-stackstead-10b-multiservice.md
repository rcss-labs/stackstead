# Stackstead Plan 10b — Multi-service stacks

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** this plan specifies *what* to build: files, signatures, behaviour, test cases and acceptance criteria. It deliberately contains no full implementations; implementers write the code test-first. Short snippets show shapes only.

**Goal:** Stacks become multi-service in the UI and the API: `POST /api/stacks` accepts 1–20 services in one submit (the legacy single-service body keeps working), an existing stack can gain a service (`POST /api/stacks/{name}/services`, with a preview) and lose one (`DELETE /api/stacks/{name}/services/{service}`), both with the same validation and stale-file protection as structured edits; the New stack page is a list of collapsible service cards, and the stack detail page has Add service and Remove service.

**Architecture:**
- Backend: `composefile` gains `NamedService`, a multi-service `New`, `AddService`, `RemoveService` and `DependentsOf` (text-preserving edits through the existing `applyEdits`/`verifyEdits` pipeline); `stacks.Service.Create` takes a service list and checks clashes *between* the entries (ports, static IPs, names) with `services[i].` field paths; `stacks.Service` gains `AddService`, `PreviewAddService` and `RemoveService` sharing the existing lock → hash check → edit → `commit` pipeline; `internal/api` adds the three routes, the dual-form create body and the 409 mappings.
- Frontend: `StackCreate.svelte` renders service cards (one open at a time) and routes `services[i].…` errors to card `i`; `StackDetail.svelte` gets an Add service panel (new `AddServicePanel.svelte` + `serviceAdd.ts` state machine, lazy-loaded like the service editor) and a Remove service confirm flow on `ServiceCard`. `NetworkPicker.svelte` is used unchanged (Plan 10a extends it in parallel).

**Tech Stack:** Go 1.26 stdlib + `go.yaml.in/yaml/v3` (already a dependency); Svelte 5, TypeScript, Vitest (jsdom). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-15-stackstead-networks-multiservice-design.md` §1, §2, §3, §4.5–4.7, §5, §6.4–6.6, §7–§10 (multi-service parts), §11 Plan 10b. Plan 10a (Networks screen, `POST`/`DELETE /api/networks`, `GET /api/host/interfaces`, the picker's "Create network…" option) is written and executed in parallel and is **not** part of this plan. Builds on Plan 9 (`main` at `826f5bd`).

## Decisions (controller, recorded for the user)

The spec's §3 table holds the product decisions (made with the user). Plan-level ones, each grounded in the current code:

- **Field errors are HTTP 400** (spec §4, aligned with the code on 2026-10-15): `writeStackError` answers `{error, fields}` with 400 for `*stacks.ValidationError` and the UI (`StackCreate`, `serviceEdit.ts`) maps fields on 400. 422 stays what it is today: compose `config` failures and `composefile.ErrInvalidConfig`. "Both `services` and `service`/`config` present" is a plain 400 `{error}` with `fields: []`.
- **One domain shape for create.** `stacks.CreateRequest` becomes `{Name string; Services []composefile.NamedService; IndexedFields bool}`. The API converts the legacy body to a one-entry list with `IndexedFields: false` (unprefixed field paths, as today) and the `services` body to `IndexedFields: true` (`services[i].` paths). Existing Go tests that build `CreateRequest{Name, Config}` are updated mechanically to the one-entry form.
- **Service-name rule = the project-name rule.** The server validates service names with `ValidateName`'s regex (`^[a-z0-9][a-z0-9_-]{0,62}$`), which is what the UI's `NAME_RE` already enforces on the service name; compose allows more, but a stricter, shared rule keeps client and server messages identical. Legacy requests get the error on field `service`; the services form on `services[i].service`.
- **Build-image default per service.** `prepareConfig` today defaults a build service's image to `stackstead/<stack>:latest`; with several build services that tag would collide. Rule: `stackstead/<stack>:latest` when the service name equals the stack name (today's behaviour, legacy-compatible), otherwise `stackstead/<stack>-<service>:latest`. Applies to create and to add.
- **Sibling clashes are reported once, on the later entry.** In `checkChanges`, a sibling's ports and static IPs count as "in use" for a job when the sibling is unchanged (has an `old` config) **or** comes earlier in `cs.services`. Create reports a duplicate port/IP on the later entry only; an edit of service A that clashes with untouched service B still errors on A wherever B sits. Static IPs of siblings become `IPUse{Owner: "service <name>"}` on the resolved network.
- **Refusals for remove live in `composefile`.** `RemoveService` returns `ErrLastService` for the only service and `*DependentsError{Service, Dependents}` when another service's `depends_on` names it (short list or long map form). `stacks` passes them through; the API maps both to 409. `DependentsOf` is exported per spec §5 and backs the check.
- **Head comments of a removed service go with it.** `RemoveService` deletes the key's lines (key line through the end of its block, as `deleteEntry` does) plus the contiguous `#` lines directly above the key at the key's own indentation (yaml.v3's `HeadComment` of that key). A comment block separated from the key by a blank line is not attached and stays. Implemented as a separate helper used only by `RemoveService`, so `UpdateService`'s golden tests are untouched.
- **Remove runs the structured-edit pipeline minus any compose action.** "No compose action runs" (spec §4.7) means no `up`; the candidate file still goes through `commit` (compose `config` validation, project claim, hash-guarded `composefile.Save`). A removed service referenced by `network_mode: service:x` or `volumes_from` is caught by compose, not by us.
- **No "changes not applied" state exists on stack detail**, and none is added. After a successful add or remove the page reloads the detail, toasts "… Run Up to apply." and the existing `ActionsBar` Up is the apply. The add panel also offers Apply in its own "Saved" step only via the toast, not a new section.
- **Add service has its own small state machine.** `createServiceEdit` is keyed by an existing service and rebases the form on the file's new version; an add has no file counterpart to rebase onto. `serviceAdd.ts` implements validate → preview (re-reading the compose hash first) → save with the previewed hash → 409 conflict with Reload/Discard, reusing `validateForm`, `fromForm`, `mapServerErrors` and `DiffView`.
- **Create cards mount one `ServiceForm` at a time.** `ServiceForm` uses fixed control ids (`svc-name`, `svc-image`, `svc-source` radio group, `svc-networks` picker prefix); mounting several would duplicate ids. The page keeps every card's state (`name`, `form`, `errors`) itself; a collapsed card renders only its header (name, image or build context, error marker) and the open card renders the form. Opening a card then calling `focusFirstError` (after `tick`) is how errors are focused.
- **Page-level form error on create.** `_form` errors (both-forms 400, 403, 422 compose text, network failure) render in a page-level `role="alert"` above the cards instead of inside a card, since the erroring card may not be the open one.
- **Stale 409 on remove reloads inside the dialog.** The confirm dialog stays open, the detail is refetched, and the dialog says "The compose file changed and was reloaded. Confirm again to remove." A `depends_on` / last-service 409 shows its message in the dialog with Confirm soft-disabled.
- **`docs/security.md` is not touched by 10b.** Plan 10a edits it (spec §8 macvlan paragraph); the "service add/remove use the session + JSON rule" sentence goes into `docs/api.md`'s notes instead, avoiding a merge conflict.
- **`New` sorts the external network block; `AddService` appends.** A new file declares every non-local network once, sorted by name. `AddService` adds `external: true` declarations only for networks not yet declared at top level (local or external), appended in the service's attachment order; existing declarations are never reordered or rewritten.
- **`GET /api/networks`, `NetworkPicker` and `apitypes.Network` are untouched** (10a owns them).

## Global Constraints

- Spec §1 success criteria: 1–20 services in one submit with errors pointing at the right card and field; add/remove on detail with the same validation and stale-file protection as edits; old API clients sending `{name, service, config}` keep working.
- Spec §4: every new route `requireSession`; mutating routes JSON-only (existing `decodeJSON` rule, unknown fields rejected); errors `{error}`; validation `{error, fields}` (400, see Decisions); stale hash 409 and **never** an overwrite; unknown stack/service 404; compose unavailable 503.
- Spec §7: a multi-service create that fails after the folder is created removes exactly the file and folder this call created (existing `Create` cleanup, unchanged).
- Spec §6.6: cards are `<section>` with a heading `<button aria-expanded aria-controls>`; remove buttons have `aria-label="Remove service <name>"`; dialogs use the existing `Dialog`/`ConfirmDialog` (focus trap, Esc); no horizontal page scroll at 375 px.
- No `{@html}`; server messages, service names and images are interpolated as text.
- Budgets unchanged: entry JS ≤ 120 kB gz, total ≤ 400 kB gz; `AddServicePanel` (it imports `DiffView` → CodeMirror) is loaded lazily like `ServiceEditor`; `StackCreate` imports no CodeMirror.
- No new npm or Go dependencies. `go test ./...` passes without Node; `CGO_ENABLED=0 GOOS=linux GOARCH={amd64,arm64} go build ./...` and `GOOS=darwin go build ./...` pass.
- Files owned by Plan 10a are not modified here: `web/src/lib/stacks/NetworkPicker.svelte`, `web/src/lib/networks/*`, `web/src/routes/NetworkList.svelte`, `internal/api/networks*.go`, `internal/engine/*`, `docs/security.md`, rail/breadcrumb/App routes. `apitypes.Network` and `toNetworks` stay as they are. 10a moves `GET /api/networks` into `internal/api/networks.go` but keeps `StacksAPI.Networks` in the interface: 10b neither removes nor renames it (`fakeStacks.Networks` stays). 10a also introduces a generic `flightCache[T]` in `internal/api` and `createRefreshList<T>` in `web/src/lib`; 10b creates no helpers with those names or that purpose (it needs neither).
- README: the user's centred header, badges and feature table stay unchanged; only one feature-list line is added under "What you get".
- Test-first for every behaviour change: write the test, run it, see it fail for the expected reason, implement, pass, commit.
- `make check` and `make ui` pass at the end of every task that touches their inputs.
- **TypeSafe AI skill:** the global rules name a "TypeSafe AI skill" for structured decisions; the installed `typesafe-ai` skill is an AI-feature SDK, not a decision scorer. Agents proceed without it and say so in their handoffs (the spec §3 records the same).
- Commits: conventional, explain why, and end with exactly one trailer line:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  ```

## Review Focus

1. **Old clients keep working.** `{name, service, config}` still creates a one-service stack and field errors keep their unprefixed paths (`ports[0]`, `networks.lan.ipv4_address`, `build.context`); `{name}` alone still means "service named after the stack"; a body carrying both `services` and `service`/`config` is 400 with a message naming both forms; `services: []` and 21 entries are field errors on `services`. (T3 `TestCreateLegacyFieldPathsStayUnprefixed`, T3 `TestCreateRejectsServiceCountBounds`, T5 `TestCreateStackLegacyBodyUnprefixedPaths`, T5 `TestCreateStackBothFormsIs400`, T5 `TestCreateStackEmptyServicesIsForwarded`)
2. **Clashes inside one request.** Two entries named `web`; entry 0 and entry 2 both publishing `8080:80`; two entries with `192.168.0.30` on `lan`: each is one error on the later entry with a `services[i].` path, and the earlier entry carries no error. An entry whose own ports clash with a *running container* still reports that too. (T3 `TestCreateMultiRejectsDuplicateServiceNames`, T3 `TestCreateMultiPortClashAcrossEntriesReportedOnLaterEntry`, T3 `TestCreateMultiStaticIPClashAcrossEntries`, T3 `TestCreateMultiPrefixesEngineConflicts`)
3. **The file after add and remove.** `AddService` on `labCompose` leaves every existing byte in place (the `# My home lab` header, `# pinned` trailing comment, blank lines, `x-custom`), appends the service last and declares only networks not already in the top-level block; `RemoveService` on the middle of three services keeps the neighbours byte-identical and leaves `networks:`/`volumes:` alone; the last service and a depended-on service are refused (short list and long map `depends_on`). (T2 `TestAddServicePreservesCommentsAndOrder`, T2 `TestAddServiceDeclaresOnlyUndeclaredNetworks`, T2 `TestRemoveServiceKeepsNeighboursAndTopLevelBlocks`, T2 `TestRemoveServiceRefusesLastService`, T2 `TestRemoveServiceRefusesDependedOn`, T1 `TestNewDedupesExternalNetworks`)
4. **A stale hash never writes.** Add, preview-add and remove with an old hash answer `ErrConflict` → 409 and the file bytes (and `.bak`) are unchanged; concurrent add and remove with the same hash let exactly one win. (T4 `TestAddServiceStaleHashIsConflictAndWritesNothing`, T4 `TestRemoveServiceStaleHashIsConflictAndWritesNothing`, T4 `TestConcurrentAddAndRemoveWithSameHashExactlyOneWins`, T5 `TestAddAndRemoveServiceStale409`, T8 `remove: a stale 409 reloads the detail and asks again`)
5. **Errors reach the right card.** A 400 with `services[1].ports[0]` and `services[1].service` opens card 2 (collapsing card 1), focuses Port 1, shows the duplicate-name message under that card's Service name, and card 1 shows no error; a collapsed card with errors shows a marker in its header; the client duplicate-name check blocks the request. (T6 `groupServiceErrors`, T7 `routes services[i] errors to card i, opens it and focuses the field`, T7 `marks collapsed cards that have errors`, T7 `blocks duplicate service names before the request`)

---

## File Structure

```
internal/composefile/model.go            NamedService (T1)
internal/composefile/build.go            New([]NamedService); networksBlock helper (T1)
internal/composefile/build_test.go       updated callers + new tests (T1)
internal/composefile/services.go         AddService, RemoveService, DependentsOf, ErrServiceExists, ErrLastService, DependentsError (T2)
internal/composefile/services_test.go    (T2)
internal/composefile/edit.go             deleteEntryWithHead (T2)
internal/stacks/service_create.go        CreateRequest{Name, Services, IndexedFields}; per-entry validation; prepareConfig(stack, service, …) (T3)
internal/stacks/validate.go              ValidateServiceName, prefixFields (T3)
internal/stacks/service_edit.go          changeSet.indexed; sibling rule; sibling static IPs (T3); stagedAdd, AddService, PreviewAddService, RemoveService (T4)
internal/stacks/service_test.go          updated CreateRequest callers; new tests (T3, T4)
internal/stacks/integration_test.go      two-service create + remove + up (T4)
internal/apitypes/types.go               CreateStackRequest (dual form), CreateStackService, AddServiceRequest (T5)
internal/apitypes/types_test.go          key contract rows (T5)
internal/api/stacks.go                   dual-form create; three routes (T5)
internal/api/stacks_dto.go               request → domain conversions (T5)
internal/api/stacks_errors.go            409 for ErrServiceExists, ErrLastService, *DependentsError (T5)
internal/api/stacks_test.go              fakeStacks gains AddService/PreviewAddService/RemoveService; new tests (T5)
web/src/lib/api/types.ts, client.ts      CreateStackService, AddServiceRequest; api.addService/previewAddService/removeService (T6)
web/src/lib/compose/form.ts              groupServiceErrors; mapPath handles `service` (T6)
web/src/routes/StackCreate.svelte        service cards (T7)
web/src/routes/StackCreate.test.ts       (T7)
web/src/lib/stacks/serviceAdd.ts         createServiceAdd state machine (T8)
web/src/lib/stacks/AddServicePanel.svelte  add panel (lazy) (T8)
web/src/lib/stacks/ServiceCard.svelte    Remove action (T8)
web/src/routes/StackDetail.svelte        Add service button/panel; remove confirm flow (T8)
cmd/stackstead/e2e_test.go               two-service create, add/remove, legacy body (T9)
docs/api.md, README.md                   (T9)
```

Parallelism (file-disjoint tasks that can run in separate worktrees at the same time):

| Wave | Tasks | Depends on |
|---|---|---|
| 1 | T1 composefile New ∥ T2 composefile services ∥ T6 web data layer | — (T1 and T2 are the same Go package but different files; T6 is frontend-only) |
| 2 | T3 stacks create ∥ T4 stacks add/remove | T3 ← T1; T4 ← T2 (both edit `service_edit.go`/`service_test.go` in different regions — run T3 then T4 in one worktree, or merge carefully) |
| 3 | T5 API | T3, T4 |
| 4 | T7 create cards ∥ T8 detail add/remove | T6 (T7 and T8 touch disjoint files) |
| 5 | T9 e2e + docs + final checks | all |

Recommended: run T1, T2, T6 in parallel; then T3 and T4 sequentially in one worktree (shared files); then T5; then T7 ∥ T8; then T9. Every task: Sonnet implements test-first; Opus reviews; Fable does the final whole-branch review.

---

### Task 1: composefile — `NamedService` and multi-service `New`

Spec §5 (`NamedService`, `New([]NamedService)`), §4.5 (one top-level `networks:` block, external, deduplicated, sorted).

**Files:**
- Modify: `internal/composefile/model.go`, `internal/composefile/build.go`
- Test: `internal/composefile/build_test.go`

**Interfaces:**
- Produces:
  ```go
  type NamedService struct{ Name string; Config ServiceConfig }
  var ErrServiceExists = errors.New("service already exists")   // also used by T2
  func New(services []NamedService) ([]byte, error)             // replaces New(name string, cfg ServiceConfig)
  ```
- Consumes: `serviceNode`, `normalize`, `ValidateConfig`, `mapNode`, `strNode`, `encode` (all existing, unchanged).

**Behaviour:**
- Services are rendered in slice order under `services:`, each with `serviceNode` (canonical key order, unchanged).
- Errors: empty slice → `ErrInvalidConfig` ("no services"); any empty name → `ErrInvalidServiceName`; a repeated name → `ErrServiceExists` (wrapped with the name); an invalid config → `ErrInvalidConfig` (wrapped with the service name, e.g. `service db: …`).
- One top-level `networks:` block follows `services:` when any service has attachments: every distinct network name across all services, sorted ascending, each as `external: true`; no block when no service has attachments (same bytes as today for one service with one network — `TestNewRendersCanonicalFile` keeps its golden).
- The only external caller is `stacks/service_create.go` (updated in T3); test callers in `build_test.go` switch to the one-item slice form in this task.

- [ ] **Step 1: Write the failing tests** — `TestNewRendersServicesInRequestOrder` (three services `web`, `db`, `cache` in that order; asserts `Services()` returns the same order and the exact YAML has `web:` before `db:` before `cache:`), `TestNewDedupesExternalNetworks` (`web` on `lan` + `backend`, `db` on `backend` with a static IP → exactly one `networks:` block with `backend` then `lan`, each `external: true`; asserts the exact bytes of the block and that `yaml.Unmarshal` of the whole file has 2 network keys), `TestNewRejectsDuplicateServiceName` (`errors.Is(err, ErrServiceExists)`), `TestNewRejectsEmptyList` (`ErrInvalidConfig`), existing `TestNewRejectsEmptyName`/`TestNewRejectsInvalidConfig`/round-trip tests updated to the slice form and kept green.
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/composefile -run 'TestNew' -v` (compile errors on `NamedService`/the new signature).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/composefile`, `go vet ./...`, `gofmt -l .` (the `stacks` package will not compile until T3; that is expected in a T1 worktree — run `go build ./internal/composefile/...` and the package tests only, and note it in the handoff).
- [ ] **Step 5: Commit** — `feat: compose files with several services from one request` (why: the create API needs one file holding every service, with each external network declared once).

---

### Task 2: composefile — `AddService`, `RemoveService`, `DependentsOf`

Spec §5, §4.6–4.7 refusals; Review Focus 3.

**Files:**
- Create: `internal/composefile/services.go`, `internal/composefile/services_test.go`
- Modify: `internal/composefile/edit.go` (one helper, see below)

**Interfaces:**
- Produces:
  ```go
  var ErrLastService = errors.New("the last service cannot be removed; delete the stack instead")
  type DependentsError struct{ Service string; Dependents []string } // Error(): "service db is required by web, worker (depends_on)"
  func (f *File) AddService(name string, cfg ServiceConfig) (*File, error)
  func (f *File) RemoveService(name string) (*File, error)
  func (f *File) DependentsOf(name string) []string   // file order; nil when none
  // edit.go
  func deleteEntryWithHead(l lines, key, val *yaml.Node) ([]byte, error) // deleteEntry + attached head-comment lines
  ```
- Consumes: `edit`, `applyEdits`, `serviceNode`, `normalize`, `ValidateConfig`, `mappingValue`, `isExternal`, `Services`, `serviceNode(name)`, `Parse`, `ErrServiceNotFound`, `ErrServiceExists` (T1; if T2 runs before T1 merges, declare it in `services.go` and let the merge keep one definition).

**Behaviour:**
- `AddService`: `name == ""` → `ErrInvalidServiceName`; `ValidateConfig(normalize(cfg))` error → as is; name already a service → `ErrServiceExists`. Edits, in order: `{["services", name], serviceNode(cfg)}` then, for each attachment whose name is **not** a key of the top-level `networks:` mapping (local or external alike), `{["networks", <net>], {external: true}}`; applied with `applyEdits` (so anchors, flow roots and verification failures surface as `ErrEditRefused`), result re-parsed. `services:` absent or null and `networks:` absent or null are created by the existing insert/null paths. With `network_mode` set there are no attachments, so no network edits.
- `RemoveService`: unknown → `ErrServiceNotFound`; the only service → `ErrLastService`; `DependentsOf(name)` non-empty → `*DependentsError`; otherwise the edit `{["services", name], nil}` applied through `applyEdits`, except that the final-segment delete uses `deleteEntryWithHead` for this call (a `removeHead bool` on `edit`, false everywhere else, keeps `UpdateService` byte-for-byte as today). Top-level `networks:`, `volumes:` and anything else stay untouched (spec: no declaration pruning).
- `deleteEntryWithHead`: the deletion start moves up from the key line over contiguous lines that are (a) non-blank, (b) indented exactly at the key's column and (c) start with `#`; it stops at the first blank line or non-comment line. Everything else is `deleteEntry`.
- `DependentsOf`: for each other service (file order) whose `depends_on` value is a sequence of scalars (names) or a mapping (keys), return the names of those services that list `name`. Aliases/anchors or other shapes are skipped (best effort; compose would reject them anyway).

- [ ] **Step 1: Write the failing tests** — `TestAddServicePreservesCommentsAndOrder` (`labCompose` + service `cache` with image, ports and `networks: [lan]` → the result is `labCompose` with the `cache` block inserted after the `db` block and before the blank line that precedes `networks:`; the `networks:` block is byte-identical because `lan` is already declared; re-parsed `Services()` = `web, db, cache`), `TestAddServiceDeclaresOnlyUndeclaredNetworks` (file declaring `lan` external and `internal` local; new service on `lan`, `internal`, `backend` → exactly one new declaration `backend: {external: true}` appended to the block, both existing entries untouched), `TestAddServiceCreatesNetworksBlock` (file without `networks:` → the block is created after the services block, `external: true`), `TestAddServiceRejectsExistingName` (`ErrServiceExists`), `TestAddServiceRejectsInvalidNameAndConfig`, `TestAddServiceRefusesAnchoredServices` (`errors.Is(err, ErrEditRefused)`), `TestRemoveServiceKeepsNeighboursAndTopLevelBlocks` (three services with comments: a `# db` head comment on `db`, a trailing `# pinned` on `web`'s image, `volumes:` and `networks:` at top level → removing `db` yields the same text minus the `# db` line and the `db` block; also removing `web` (the first service) keeps `# My home lab`-style file header because it is separated by `services:`), `TestRemoveServiceKeepsCommentSeparatedByBlankLine` (a comment, a blank line, then the key → the comment stays), `TestRemoveServiceRefusesLastService` (`ErrLastService`, file unchanged), `TestRemoveServiceRefusesDependedOn` (table: short form `depends_on: [db]`, long form `depends_on: {db: {condition: service_healthy}}`; `errors.As` to `*DependentsError`, `Dependents` in file order, message names them), `TestRemoveServiceUnknown` (`ErrServiceNotFound`), `TestDependentsOf` (short and long forms, none, and a service depending on itself-named key elsewhere not counted).
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/composefile -run 'AddService|RemoveService|DependentsOf' -v`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/composefile` (all golden `TestApplyEditsGolden` and `TestUpdate*` cases unchanged), `go vet ./...`, `gofmt -l .`.
- [ ] **Step 5: Commit** — `feat: add and remove services in a compose file without touching the rest` (why: stack detail needs structured add/remove with the same comment-preserving guarantees as edits).

---

### Task 3: stacks — multi-service `Create` with cross-entry checks

Spec §4.5; Review Focus 1, 2.

**Files:**
- Modify: `internal/stacks/service_create.go`, `internal/stacks/validate.go`, `internal/stacks/service_edit.go` (`changeSet`/`checkChanges` only)
- Test: `internal/stacks/service_test.go`, `internal/stacks/validate_test.go`

**Interfaces:**
- Produces:
  ```go
  type CreateRequest struct {
      Name     string
      Services []composefile.NamedService // 1–20; Services[0].Name defaults to Name
      // IndexedFields prefixes field paths with "services[i]." (the services form of
      // POST /api/stacks); false keeps the legacy unprefixed paths of a one-service body.
      IndexedFields bool
  }
  func ValidateServiceName(name string) error        // same regex as ValidateName; field "service"
  func prefixFields(err error, prefix string) error  // rewrites *ValidationError field paths; other errors unchanged
  func (s *Service) prepareConfig(stack, service, stackDir string, cfg composefile.ServiceConfig) (composefile.ServiceConfig, error)
  // changeSet gains: indexed bool  // prefix "services[i]." (i = index in services) instead of "services.<name>."
  ```
- Consumes: `composefile.New([]NamedService)` (T1), `checkChanges`, `PortConflicts`, `CheckStaticIP`, `declaredBy`, `resolveNetwork`.

**Behaviour:**
- Validation order (before any filesystem write): `ValidateName`; registry/folder existence (as today); `len(Services)` outside 1–20 → `ValidationError{Field: "services", Message: "a stack needs between 1 and 20 services"}`; per entry `i`: empty name on entry 0 → stack name, empty name elsewhere → `services[i].service` "service name is required"; `ValidateServiceName` (error field `service`, prefixed when indexed); duplicate of an earlier entry → `services[i].service` "duplicate service name <x>" (on the later entry); `prepareConfig` (its `build.context` error prefixed via `prefixFields`); `composefile.ValidateConfig` → `ErrInvalidConfig` wrapped as `services[i]: …` when indexed. All field errors of all entries are collected into one `ValidationError` (the UI opens the first erroring card). Then `composefile.New`, then `checkChanges(changeSet{services, indexed: req.IndexedFields})`, then the unchanged claim/write/config/register/cleanup sequence.
- `prepareConfig` image default: `stackstead/<stack>:latest` when `service == stack`, else `stackstead/<stack>-<service>:latest`. Context confinement unchanged. `Import` is untouched.
- `checkChanges` prefix: `""` (default), `services.<name>.` (`qualify`, raw saves — unchanged), or `services[i].` (`indexed`).
- Sibling rule (Decisions): for job `j`, a sibling `s` contributes its ports (`declaredBy("service "+s.Name, …)`) and its static IPs (`IPUse{Owner: "service " + s.Name}` under `resolveNetwork(nets, cs.project, n.Name)`) when `s` has an `old` entry in `cs.old` **or** `index(s) < index(j)`. Engine-side checks are unchanged; `partial` still skips IP checks entirely.

- [ ] **Step 1: Write the failing tests** — `TestCreateMultiWritesAllServicesInOrder` (two entries → file has both, `Services()` order, summary `Services == 2`, registry entry as before), `TestCreateDefaultsFirstServiceNameToStackName` (`Services[0].Name == ""` → service named after the stack; an empty name on entry 1 → `services[1].service`), `TestCreateMultiRejectsDuplicateServiceNames` (`web`, `db`, `web` → exactly one field error `services[2].service`), `TestCreateMultiPortClashAcrossEntriesReportedOnLaterEntry` (entry 0 and entry 2 publish `8080:80`; entry 1 publishes `9090:90` → exactly one error, field `services[2].ports[0]`, message names `service web`), `TestCreateMultiStaticIPClashAcrossEntries` (engine network `lan` 10.0.0.0/24; entries 0 and 1 both `10.0.0.5` on `lan` → one error `services[1].networks.lan.ipv4_address` naming `service <entry0>`), `TestCreateMultiPrefixesEngineConflicts` (a running container on 7070 and a build context outside the roots on entry 1 → `services[0].ports[0]` and `services[1].build.context`), `TestCreateLegacyFieldPathsStayUnprefixed` (`IndexedFields: false`, one entry → `ports[0]`, `networks.lan.ipv4_address`, `build.context` exactly as the existing tests expect), `TestCreateRejectsServiceCountBounds` (0 and 21 → field `services`), `TestCreateBuildImageDefaultPerService` (two build services → `stackstead/<stack>:latest` for the one named like the stack, `stackstead/<stack>-<svc>:latest` for the other), `TestValidateServiceName` table in `validate_test.go`, `TestUpdateServicePortClashWithLaterSiblingStillReported` (edit service `a` (index 0) to publish the port untouched `b` (index 1) already declares → error on `services`-less path `ports[0]` naming `service b`; pins the sibling rule for edits), `TestCreateMultiFailureLeavesNoTrace` (compose `config` fails on a two-service create → no folder). Existing `Create*` tests are updated to the one-entry form (`h.create` helper included) and stay green; `TestSaveRaw`'s `services.web.ports[0]` expectation is unchanged.
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/stacks -run 'TestCreate|TestValidateServiceName|TestUpdateServicePortClash' -v` (compile errors on the new `CreateRequest` shape first; fix callers, then real failures).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/stacks ./internal/composefile`, `go vet ./...`, `gofmt -l .`. (`internal/api` fails to compile until T5 adapts the handler; note it.)
- [ ] **Step 5: Commit** — `feat: create stacks with several services and check clashes between them` (why: a compose stack is normally several services; errors must point at the entry that caused them).

---

### Task 4: stacks — `AddService`, `PreviewAddService`, `RemoveService`

Spec §4.6, §4.7, §7; Review Focus 4.

**Files:**
- Modify: `internal/stacks/service_edit.go`, `internal/stacks/service_test.go`, `internal/stacks/integration_test.go`

**Interfaces:**
- Produces:
  ```go
  func (s *Service) AddService(ctx context.Context, stack, service string, cfg composefile.ServiceConfig, hash string) (EditResult, error)
  func (s *Service) PreviewAddService(ctx context.Context, stack, service string, cfg composefile.ServiceConfig, hash string) ([]byte, error)
  func (s *Service) RemoveService(ctx context.Context, stack, service, hash string) (EditResult, error)
  // private: stagedAdd(ctx, stack, service, cfg, hash) (Entry, old, edited *composefile.File, unlock func(), err error)
  ```
- Consumes: `lockedEntry`, `loadFile`, `parseFile`, `refuseOverrides`, `commit`, `prepareConfig` (T3), `composefile.(*File).AddService`/`RemoveService` (T2), `composefile.ErrConflict`.

**Behaviour:**
- `stagedAdd` mirrors `stagedEdit`: lock → `loadFile` → `current != hash` → `ErrConflict` → `parseFile` → `ValidateServiceName` (field `service`) → `prepareConfig(stack, service, filepath.Dir(e.ComposePath), cfg)` (field paths unprefixed) → `old.AddService(service, cfg)` (`ErrServiceExists`, `ErrInvalidConfig`, `ErrEditRefused` pass through) → `refuseOverrides`.
- `AddService` = `stagedAdd` + `commit(ctx, e, old, edited, hash, false)`: the new service has no `old` config so its ports and IPs are checked (unprefixed paths); existing services are unchanged siblings and count against it; compose `config` validation, project claim and hash-guarded save as for edits. `PreviewAddService` = `stagedAdd` + `edited.Bytes()`, no compose, no write.
- `RemoveService`: lock → `loadFile` → hash check → `parseFile` → `refuseOverrides` → `old.RemoveService(service)` (`ErrServiceNotFound`, `ErrLastService`, `*DependentsError` pass through) → `commit(ctx, e, old, edited, hash, false)` (no jobs in `checkChanges`; compose `config` runs; save under the hash). No compose action is started.
- Logging: one Info line per successful add/remove with stack and service.

- [ ] **Step 1: Write the failing tests** — `TestAddServiceWritesAndReturnsNewHash` (setup `web`, add `cache` → file has it, hash differs, `.bak` kept, registry project unchanged), `TestAddServiceStaleHashIsConflictAndWritesNothing` (`ErrConflict`; bytes identical; no `.bak`), `TestAddServiceExistingNameIsErrServiceExists`, `TestAddServicePortClashWithExistingServiceAndContainer` (existing `web` on 8080 + container on 7070; add with both → `ports[0]`/`ports[1]`, unprefixed, owners `service web` and `container …`), `TestAddServiceStaticIPCheckedAndImageDefaulted` (build service → `stackstead/<stack>-<svc>:latest` in the file), `TestAddServiceConfigFailureLeavesFileUnchanged` (compose `configErr` → bytes identical, `checkTempFiles`), `TestPreviewAddServiceDoesNotWriteOrCallCompose`, `TestPreviewAddAndAddUseSameHash` (preview then add with the preview's hash succeeds; add with the stale hash after an intervening save conflicts), `TestRemoveServiceWritesFile` (three services, remove the middle one → `Services()` has two, hash new), `TestRemoveServiceStaleHashIsConflictAndWritesNothing`, `TestRemoveServiceUnknownIs404Error` (`ErrServiceNotFound`), `TestRemoveServiceRefusesLastServiceAndDependents` (`ErrLastService`; `*DependentsError` with names), `TestRemoveServiceRunsNoComposeAction` (fake compose records `Config` once and no `Run`), `TestConcurrentAddAndRemoveWithSameHashExactlyOneWins` (same shape as `TestConcurrentUpdatesWithSameHashExactlyOneWins`), `TestAddAndRemoveRefuseOverrideFiles`, `TestAddAndRemoveMissingFileIsErrStackUnavailable`. Integration (`-tags integration`, Docker): `TestTwoServiceCreateRemoveAndUp` — create a two-service stack (two tiny images, one with a static IP on a bridge network created with `docker network create --subnet` in the test and removed in cleanup), `up`, both containers exist and the IP matches; remove one service with the current hash, `up` again, that container is gone (`--remove-orphans`), the other remains; cleanup.
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/stacks -run 'AddService|RemoveService|PreviewAdd|ConcurrentAddAndRemove' -v`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/stacks`, `go test -tags integration -race -run TwoService ./internal/stacks/` (skips without Docker; run it where Docker exists and report), `go vet ./...`.
- [ ] **Step 5: Commit** — `feat: add and remove services on an existing stack under the file hash` (why: detail-page add/remove must be as safe as structured edits: locked, hash-checked, compose-validated, never overwriting).

---

### Task 5: API — dual-form create, add/preview/remove routes, 409 mappings

Spec §4.5–4.7, §7; Review Focus 1, 4.

**Files:**
- Modify: `internal/apitypes/types.go` (+ `types_test.go`), `internal/api/stacks.go`, `internal/api/stacks_dto.go`, `internal/api/stacks_errors.go`, `internal/api/stacks_test.go`

**Interfaces:**
- Produces:
  ```go
  // apitypes
  type CreateStackService struct { Service string `json:"service"`; Config ServiceConfig `json:"config"` }
  type CreateStackRequest struct {
      Name     string               `json:"name"`
      Service  string               `json:"service,omitempty"`  // legacy form
      Config   *ServiceConfig       `json:"config,omitempty"`   // legacy form; nil when absent
      Services []CreateStackService `json:"services,omitempty"` // 1–20
  }
  type AddServiceRequest struct { Service string `json:"service"`; Config ServiceConfig `json:"config"`; Hash string `json:"hash"` }
  // api.StacksAPI gains
  AddService(ctx, stack, service string, cfg composefile.ServiceConfig, hash string) (stacks.EditResult, error)
  PreviewAddService(ctx, stack, service string, cfg composefile.ServiceConfig, hash string) ([]byte, error)
  RemoveService(ctx, stack, service, hash string) (stacks.EditResult, error)
  ```
- Consumes: `decodeJSON`, `writeStackError`, `markPartial`, `toDetail`, `serviceConfigToDomain`, `stacks.CreateRequest` (T3).

**Behaviour:**
- `POST /api/stacks`: `legacy := req.Services == nil`; `!legacy && (req.Config != nil || req.Service != "")` → 400 `{error: "send either services or service and config, not both", fields: []}`. Legacy → `CreateRequest{Name, Services: [{req.Service, cfg}], IndexedFields: false}` (a nil `Config` means an empty config, as `{name}` alone does today). Services form → one `NamedService` per entry, `IndexedFields: true`. Count bounds and everything else are the domain's job (field `services`).
- Routes (all `requireSession`, registered in `registerStackRoutes`): `POST /api/stacks/{name}/services` → `AddService` then `Get` → 200 `StackDetail` + `markPartial`; `POST /api/stacks/{name}/services/preview` → `PreviewAddService` → 200 `PreviewResponse{Content, Hash: req.Hash}`; `DELETE /api/stacks/{name}/services/{service}` → `hash` query required (missing → 400 "hash is required"), `RemoveService` then `Get` → 200 `StackDetail`. The literal `/services/preview` and the pattern `/services/{service}/preview` have different segment counts, so the mux keeps both; a service literally named `preview` previews at `/services/preview/preview`.
- Error mapping additions in `writeStackError`: `composefile.ErrServiceExists`, `composefile.ErrLastService`, `*composefile.DependentsError` → 409 with the error text. Everything else already maps (`ErrConflict` 409, `ErrServiceNotFound` 404, `ErrStackNotFound` 404, `ValidationError` 400, compose exit 422, `ErrComposeUnavailable` 503).
- Key contract rows: `CreateStackRequest` → `config,name,service,services`; `CreateStackService` → `config,service`; `AddServiceRequest` → `config,hash,service`.

- [ ] **Step 1: Write the failing tests** — `TestCreateStackLegacyBodyUnprefixedPaths` (fake returns a `ValidationError` with `ports[0]` → body `fields[0].field == "ports[0]"`; fake `gotCreate.IndexedFields == false`, one service with the given name), `TestCreateStackServicesBody` (two entries → `gotCreate.Services` in order, `IndexedFields == true`, 201), `TestCreateStackBothFormsIs400`, `TestCreateStackNameOnlyIsLegacy` (`{name}` → one entry, empty name, `IndexedFields == false`), `TestCreateStackEmptyServicesIsForwarded` (`services: []` reaches the fake as an empty, non-nil `Services` with `IndexedFields == true`, so the domain's count rule (T3) answers the `services` field error; the handler adds no count check of its own), `TestAddServiceReturnsDetailAndPartialHeader`, `TestPreviewAddServiceReturnsCandidateAndHash`, `TestRemoveServiceRequiresHashAndReturnsDetail`, `TestAddAndRemoveServiceStale409` (fake err `composefile.ErrConflict` → 409 on all three routes), `TestServiceAddRemove409Mappings` (table: `ErrServiceExists`, `ErrLastService`, `&DependentsError{…}` → 409 with the message), `TestServiceRoutesRequireSession` and `TestServiceRoutesRequireJSON` (POSTs without JSON → 415; `DELETE` takes no body), `TestJSONTagsMatchContract` rows updated. `fakeStacks` records `gotService`, `gotCfg`, `gotHash` for the new methods and returns `Get`'s detail.
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/api ./internal/apitypes -run 'CreateStack|AddService|RemoveService|PreviewAdd|ServiceRoutes|JSONTags' -v`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/api ./internal/apitypes ./internal/stacks`, `go test ./...`, `go vet ./...`, `gofmt -l .`.
- [ ] **Step 5: Commit** — `feat: multi-service create body and service add, preview and remove routes` (why: the UI needs them, and old clients must keep the single-service body).

---

### Task 6: Web data layer — types, client, error grouping

Spec §4.5–4.7 (wire shapes), §6.4 (error routing).

**Files:** Modify `web/src/lib/api/types.ts`, `web/src/lib/api/client.ts` (+ `client.test.ts`), `web/src/lib/compose/form.ts` (+ `form.test.ts`).

**Interfaces:**
- Produces:
  ```ts
  export interface CreateStackService { service: string; config: ServiceConfig }
  export interface CreateStackRequest { name: string; services: CreateStackService[] }   // the UI never sends the legacy form
  export interface AddServiceRequest { service: string; config: ServiceConfig; hash: string }
  api.addService(name, service, config, hash): Promise<{ data: StackDetail; checksPartial: boolean }>   // POST /api/stacks/<name>/services
  api.previewAddService(name, service, config, hash): Promise<PreviewResponse>                            // POST /api/stacks/<name>/services/preview
  api.removeService(name, service, hash): Promise<StackDetail>                                            // DELETE /api/stacks/<name>/services/<service>?hash=…
  // form.ts
  export function groupServiceErrors(fields: FieldError[]): { cards: Map<number, FieldError[]>; rest: FieldError[] }
  // mapPath: 'service' → 'name' (so a card's `services[i].service` lands on its Service name field)
  ```

**Behaviour:** paths are encoded with the existing `seg`; `hash` goes in the query for the delete and is URL-encoded; `groupServiceErrors` strips `services[i].` and buckets by `i` (`services[1].ports[0]` → card 1 `ports[0]`; `services[0].service` → card 0 `service`); `name`, `services` and anything unprefixed go to `rest`. `mapServerErrors` keeps its existing `services.<name>.` handling for the edit flow.

- [ ] **Step 1: Write the failing tests** — `client.test.ts`: paths, methods and bodies of the three calls, hash in the delete query (encoded), `checksPartial` surfaced for add; `form.test.ts`: `groupServiceErrors` buckets and rest, `mapServerErrors` maps `service` to `name` and still strips `services.<name>.`.
- [ ] **Step 2: Run to verify they fail** — `cd web && npm test -- client form`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `cd web && npm run check && npm test && npm run build`.
- [ ] **Step 5: Commit** — `feat: service add/remove API client and per-card error grouping`.

---

### Task 7: New stack — service cards

Spec §6.4, §6.6; Review Focus 5.

**Files:** Modify `web/src/routes/StackCreate.svelte` (+ `StackCreate.test.ts`).

**Interfaces:**
- Page state (not exported):
  ```ts
  type Card = { id: number; name: string; autoName: boolean; form: ServiceForm; errors: FormErrors };
  let cards = $state<Card[]>([…one…]); let openId = $state(cards[0].id); let formError = $state('');
  ```
- Consumes: `ServiceForm` (unchanged), `NetworkPicker` (unchanged), `validateForm`, `fromForm`, `groupServiceErrors`, `mapServerErrors` (T6), `api.createStack` (T6 shape), `Button`, `Field`, `Banner`.

**Behaviour:**
- Stack name field stays first (no longer inside the `lead` snippet — it moves above the cards so it exists when any card is open). Then the cards: each a `<section aria-labelledby>` whose header is a `<button aria-expanded aria-controls>` showing the service name (or "Service N" while empty), a summary (image, or `build: <context>`), an error marker (`N errors`, text + icon, when collapsed and `errors` has keys other than `_form`) and a ✕ `Button` `icon` with `aria-label="Remove service <name>"` (not rendered when only one card). Exactly one card is open; only the open card mounts `ServiceForm` (`showName`, `bind:name`, `bind:form`, `errors`, `onbrowse`, `networkPicker` snippet with `idPrefix="svc-networks"`); collapsed cards keep their state.
- Card 0's name follows the stack name until edited (today's `autoServiceName` rule, now `autoName`); other cards start empty. **+ Add service** appends, opens it and focuses its Service name input; soft-disabled with reason at 20 cards. Removing the open card opens the previous one.
- Submit: `stackNameError` from `validateForm` as today; each card `validateForm(card.form, { name: card.name, requireName: true, original: null })`; client duplicate names → `name: "duplicate service name <x>"` on the later card; any error → open the first erroring card, `tick`, `focusFirstError`. Body `{ name, services: cards.map(c => ({ service: c.name, config: fromForm(c.form, null) })) }`. Responses: as today (toast, optional `up`, navigate). Errors: 400 → `groupServiceErrors`; per card `mapServerErrors(bucket, card.form)`; `rest` → `name` to `stackNameError`, others joined into `formError`; open the first card with errors and focus; 409 → stack name; 403/422/network → `formError`; 503 → unavailable banner. `formError` renders as a page-level `<div role="alert" tabindex="-1">` above the cards and is focused when it is the only error.
- The stale-build-error cleanup effect applies per card. Lead text: "One or more services. Stackstead writes `compose.yaml` into a folder named after the stack in its stacks directory."

- [ ] **Step 1: Write the failing tests** — update the existing `StackCreate` tests for the new body shape (`post().body.services[0].service`, `.config`) and keep their intent; add `adds a card, opens it and focuses its name; removes a card and reopens the previous one`, `only one card is expanded and only it renders the form` (one `Image` field in the DOM), `card 0 follows the stack name until edited; card 1 does not`, `sends every card in order`, `blocks duplicate service names before the request` (no POST; error under the second card's Service name), `routes services[i] errors to card i, opens it and focuses the field` (400 with `services[1].ports[0]` and `services[1].service`), `marks collapsed cards that have errors` (header text `1 error` on the collapsed card after validation), `a page-level error (both forms 400 text) is announced above the cards`, `Add service is soft-disabled at 20 cards`, `remove buttons carry aria-labels and are hidden with one card`.
- [ ] **Step 2: Run to verify they fail** — `cd web && npm test -- StackCreate`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `cd web && npm run check && npm test && npm run build` (budgets).
- [ ] **Step 5: Commit** — `feat: New stack with several service cards` (why: a stack is normally several services; errors must land on the right card).

---

### Task 8: Stack detail — Add service and Remove service

Spec §6.5, §6.6, §7; Review Focus 4.

**Files:**
- Create: `web/src/lib/stacks/serviceAdd.ts` (+ `serviceAdd.test.ts`), `web/src/lib/stacks/AddServicePanel.svelte` (+ `AddServicePanel.test.ts`)
- Modify: `web/src/lib/stacks/ServiceCard.svelte` (+ test), `web/src/routes/StackDetail.svelte` (+ test)

**Interfaces:**
- Produces:
  ```ts
  // serviceAdd.ts
  export type AddPhase = 'editing' | 'busy' | 'previewing' | 'saving' | 'conflict';
  export interface ServiceAddDeps {
    preview: (service: string, config: ServiceConfig, hash: string) => Promise<{ content: string; hash: string }>;
    add: (service: string, config: ServiceConfig, hash: string) => Promise<{ detail: StackDetail; checksPartial: boolean }>;
    compose: () => Promise<RawCompose>;
    reloadDetail: () => Promise<StackDetail>;
  }
  export function createServiceAdd(initialHash: string, deps: ServiceAddDeps): {
    form: Writable<ServiceForm>; name: Writable<string>; errors; phase; notice; conflictMessage; hash; previewText; dirty;
    preview(): Promise<void>; save(): Promise<StackDetail | undefined>; cancelPreview(): void; reload(detail: StackDetail): void; discard(detail: StackDetail): void;
  }
  // AddServicePanel.svelte props
  { stack: StackDetail; onadded: (detail: StackDetail, checksPartial: boolean) => void; oncancel: () => void; onreload: () => Promise<StackDetail>; dirty?: boolean (bindable); ondirtychange?: (d: boolean) => void }
  // ServiceCard.svelte new props
  { onremove?: () => void; removeDisabledReason?: string }   // renders Button "Remove" with aria-label "Remove service <name>"
  ```
- Consumes: `api.addService`, `api.previewAddService`, `api.removeService`, `api.compose`, `api.stack` (T6), `validateForm`, `fromForm`, `mapServerErrors`, `ServiceForm`, `NetworkPicker` (unchanged), `DiffView`, `Banner`, `Button`, `ConfirmDialog`, `toasts`, `refreshStacks`, `refreshStatus`.

**Behaviour:**
- `createServiceAdd`: `preview()` validates (`requireName`, `original: null`); `compose()` → if its hash differs from the machine's, `reloadDetail()` and take the new hash (the user's form is kept as is, nothing to rebase), then `preview(name, fromForm(form, null), hash)`; `previewText = { original: compose.content, modified }`; `save()` sends exactly the previewed config and hash, returns the detail; `fail` mirrors `serviceEdit.ts` (409 → `conflict`; 400 fields → `mapServerErrors(fields, form)` with `service` → `name`; 422 → notice; else notice). `reload(detail)` takes the new hash and returns to `editing`; `discard(detail)` also clears the form and name.
- `AddServicePanel`: `ServiceForm` with `showName`, `bind:name`, `networkPicker` snippet (`idPrefix="svc-networks"`), Preview changes → heading "Review the service to add" + `DiffView` + Back / Save; conflict banner "Save refused" with Reload / Discard my changes (as `ServiceEditor`); a Cancel button (`oncancel`, guarded by the page's dirty flow); `dirty` = name or form non-empty; `beforeunload` guard while dirty. 503 → `refreshStatus()`.
- `StackDetail`: services section gets a header row with **Add service** (`Button secondary`, hidden while a card is being edited or the panel is open, and when `parseError`). Opening is `guarded`; it closes any open editor. The panel is lazy-loaded (`import('$lib/stacks/AddServicePanel.svelte')`) under `{#key d.name}`. `onadded(detail, partial)` → `detail = detail`, close the panel, toast success "Added <svc> to compose.yaml. Run Up to start it." (+ the partial-checks warning toast as today), `refreshStacks()`. `dirty` now also covers `addingService && addDirty`; `discardAndLeave` closes the panel.
- Remove: each `ServiceCard` gets `onremove` (opens the confirm) and `removeDisabledReason` = "The last service cannot be removed; delete the stack instead." when `d.services.length === 1`. State `removing = { service, error?, pending }`. `ConfirmDialog` title "Remove service?", message "Remove <service> from compose.yaml? Its container is removed on the next Up.", `confirmLabel="Remove service"`, `danger`; `children` renders `error` as `<p role="alert">` when set. Confirm → `api.removeService(name, service, d.hash)`; success → `detail = result`, close, toast "Removed <svc> from compose.yaml. Run Up to apply.", `refreshStacks()`; 409 whose message matches `/changed on disk/` → `reloadDetail()` then `error = "The compose file changed and was reloaded. Confirm again to remove."` (dialog stays open, confirm enabled again); other 409 (depends_on, last service) → `error = message`, confirm soft-disabled with that reason; 404 → close + toast + `load()`; 503 → `refreshStatus()` + toast; double confirm ignored while pending.

- [ ] **Step 1: Write the failing tests** — `serviceAdd.test.ts`: `validation errors block the preview without any request`, `preview re-reads the compose hash and previews with the new one when the file moved`, `save sends exactly the previewed config and hash and returns the detail`, `409 enters conflict; reload takes the new hash and keeps the form`, `400 field errors map service to name`, `a second preview while one is in flight starts no second chain`. `AddServicePanel.test.ts`: renders the name field and form, Preview shows the diff and Save/Back, Cancel calls `oncancel`, conflict banner with Reload/Discard, `onadded` receives the detail. `ServiceCard.test.ts`: `Remove has the aria-label and is soft-disabled with the reason`. `StackDetail.test.ts`: `Add service opens the panel, closes the editor, and a successful add replaces the detail and toasts`, `Add service is hidden on parse error`, `remove asks for confirmation with the next-Up message`, `remove: a stale 409 reloads the detail and asks again`, `remove: a depends_on 409 shows the message in the dialog and disables confirm`, `remove: the last service's button is disabled with the reason`, `leaving with a dirty add panel asks first`.
- [ ] **Step 2: Run to verify they fail** — `cd web && npm test -- serviceAdd AddServicePanel ServiceCard StackDetail`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `cd web && npm run check && npm test && npm run build` (entry chunk unchanged by the lazy panel).
- [ ] **Step 5: Commit** — `feat: add and remove services from the stack page` (why: existing stacks could only change their service set through raw YAML).

---

### Task 9: E2E, docs, final checks, visual pass

Spec §9 (E2E), §10 (docs).

**Files:** Modify `cmd/stackstead/e2e_test.go`, `docs/api.md`, `README.md`.

**Behaviour:**
- E2E (Docker part of `TestE2ESmoke`, inside `dockerStack`): the smoke stack is created with `services: [{service: "web", nginx:alpine, memory 64m}, {service: "web2", nginx:alpine}]` → 201 and `GET /api/stacks/<name>` has `serviceCount == 2`; after `up` both containers exist (the existing log/metrics/containers checks keep working with `web`); then `DELETE …/services/web2?hash=<detail.hash>` → 200 with `serviceCount == 1`; `POST …/services/preview` with `{service: "web2", config, hash}` → 200 with `content` containing `web2:`; `POST …/services` with the stale pre-delete hash → 409; with the current hash → 200 and `serviceCount == 2`; `PUT …/compose` with the current content plus `depends_on: [web]` under `web2` (raw save, current hash) → 200, then `DELETE …/services/web?hash=` → 409 naming `web2`; remove `web2` → 200, then remove `web` → 409 (last service). A second, short check that the legacy body `{name, service, config}` still answers 201 for a throwaway stack that is immediately removed (no `up`). Every new route without a session → 401. Cleanup unchanged (project containers and `<name>_default`).
- `docs/api.md`: overview rows for `POST /api/stacks` (both bodies), `POST /api/stacks/{name}/services` + `…/services/preview`, `DELETE /api/stacks/{name}/services/{service}?hash=`; a "Multi-service stacks" note under "Notes on the stacks API": the two body forms, 1–20, both-present 400, `services[i].` field paths vs unprefixed legacy paths, cross-entry port/IP/name checks reported on the later entry, one external `networks:` block; "Adding and removing services": hash from the last read, 409 cases (stale, exists, last service, `depends_on`), the detail as response, the container going on the next `up`, no compose action, and that these mutations use the same session + JSON rule.
- README: one line under "What you get", after "Forms instead of YAML": `- **Multi-service stacks.** Create a stack with several services in one go, and add or remove services on an existing stack from the same forms, with the same checks and the same diff before anything is written.` Header, badges and feature table untouched.
- Final checks: `make check`; `make ui` (budgets); `env PATH=/usr/bin:/bin:$(go env GOROOT)/bin go test ./...` (no Node on PATH); `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...`; `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`; `GOOS=darwin go build ./...`; `make e2e` (with Docker); `go test -tags integration -race -run TwoService ./internal/stacks/`.
- Visual pass (headless Chrome, scratchpad config/data, a throwaway two-service stack): `/stacks/new` with two cards (one collapsed with an error marker), the stack page's Add service panel in editing and previewing states, the Remove service dialog, at 1280 and 375, dark and light; fix findings before the final review; clean up containers and stop processes.

- [ ] **Step 1: Write the e2e assertions**, run them (prove they can fail by a temporary wrong expectation if they pass first time, then restore).
- [ ] **Step 2: Write the docs.**
- [ ] **Step 3: Final checks and visual pass.**
- [ ] **Step 4: Commit** — `test: multi-service create, add and remove in the e2e smoke` and `docs: multi-service stacks API and README line`.
- [ ] **Step 5: Hand off** for the Fable final review (visual findings fixed first), then merge to `main` after Plan 10a (spec §11: 10a merges first; 10b's cards render `NetworkPicker` unchanged either way).

---

## Spec coverage

| Spec item | Task |
|---|---|
| §4.5 `services` body, 1–20, legacy conversion, both forms 400 | T3 (bounds, defaults), T5 (body forms) |
| §4.5 service-name validity and uniqueness, entry-0 default, empty name elsewhere | T3 |
| §4.5 `prepareConfig` + `ValidateConfig` per entry | T3 |
| §4.5 `checkChanges` with all services; cross-entry ports and IPs on the later entry | T3 |
| §4.5 `services[i].` vs unprefixed legacy paths | T3, T5 |
| §4.5 `composefile.New([]NamedService)`, one external `networks:` block, deduplicated, sorted | T1 |
| §4.5 compose validation, atomic claim, cleanup, optional start | unchanged `Create`; T3 `TestCreateMultiFailureLeavesNoTrace` |
| §4.6 `POST …/services` `{service, config, hash}`, stale 409, exists 409, validation, `AddService`, compose validation, detail response, `…/services/preview` | T4, T5 |
| §4.7 `DELETE …/services/{service}?hash=`, stale 409, 404, last-service 409, `depends_on` 409 (short and long), `RemoveService`, detail response, no compose action | T2 (refusals), T4, T5 |
| §5 `NamedService`, `New`, `AddService` (append, declare missing networks, preserve), `RemoveService` (key + head comments, top-level untouched), `DependentsOf` | T1, T2 |
| §6.4 cards, one open, + Add service, card 0 name default, ✕ hidden with one card, error routing + focus, collapsed markers, client duplicate check | T6 (grouping), T7 |
| §6.5 Add service panel with form + picker + preview + save, hash, 409 stale flow; Remove with confirm text, last-service disabled, `depends_on` message in dialog | T8 |
| §6.5 "changes not applied — Up" | adapted: detail reload + toast + existing Up (Decisions) |
| §6.6 `<section>` + heading button `aria-expanded`/`aria-controls`, remove `aria-label`, `Dialog` focus trap/Esc | T7, T8 |
| §7 multi-service create cleanup; add/remove stale never overwrites | T3, T4, T5, T8 |
| §8 session + JSON rule for add/remove (documented in api.md; security.md left to 10a) | T5 tests, T9 docs |
| §9 composefile, api, stacks, integration, vitest, E2E tests | T1–T9 |
| §10 `docs/api.md`, README line | T9 |
| §11 parallel worktrees, merge after 10a | File Structure table, T9 step 5 |
