# Stackstead Plan 25 — YAML create mode and managed Dockerfiles

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** tasks, names, signatures, test cases and examples only; no code bodies. Implementers write everything test-first.

**Goal:** Let New stack be written as one editable compose YAML document that converts both ways with the service cards without losing anything that affects how the stack runs, and let a service's Dockerfile be written in Stackstead, stored in the stack's own folder, and edited, saved and rebuilt from the stack page.

**Architecture:** The SPA converts YAML ↔ cards client-side with the `yaml` library (lazy, create screen only); each card carries the compose keys the form cannot model as an opaque `extra`, and so does the stack. The server renders form fields, deep-merges the extras into the YAML node tree (`composefile.NewStack`), then re-reads the merged document (`composefile.EffectiveService`) so build confinement and port/IP checks also see long-syntax values. Dockerfiles live at `<stack>/<service>/Dockerfile` (or wherever an existing build already points inside the stack folder); one resolver in `internal/stacks/dockerfile.go` decides editability and every read and write goes through it, writing atomically after symlink resolution. The stack page gains a Dockerfile tab with hash-checked save and a service-scoped `up --build`.

**Tech Stack:** Go 1.26 standard library, `go.yaml.in/yaml/v3` (already used); React 19, TypeScript, Vitest + Testing Library, CodeMirror 6. New web dependencies: `yaml@2.9.1` (parser/serializer for YAML mode), `@codemirror/legacy-modes@6.5.4` (Dockerfile highlighting; 6.5.5 is under two weeks old).

**Spec:** `docs/superpowers/specs/2026-10-20-stackstead-yaml-create-and-dockerfiles-design.md` (all sections).

## Spec divergences and decisions the spec left open

1. **No YAML parser is bundled today.** `previewYaml` only *writes* text (`serviceConfigYaml`), and `@codemirror/lang-yaml` gives a syntax tree, not values. The plan adds `yaml@2.9.1` (eemeli/yaml: no dependencies, ISC, maintained, gives `linePos` on errors and expands merge keys with `merge: true`). It is imported only from `web/src/lib/compose/stackYaml.ts`, which only the lazy New stack screen imports. Measured today: entry 107.4 kB gz, total 331.2 kB gz of a 400 kB budget; `yaml` adds roughly 30 kB gz to the lazy total.
2. **`extra` only on create.** §4.4a says create/add/update gain `extra`; but Add/Edit service have no YAML mode (§2 Out), and `UpdateService` already keeps every unmodelled key through minimal text edits. Only `POST /api/stacks` gains `extra` (per service and per stack). YAGNI.
3. **Dockerfile content travels per service, not as a `dockerfiles` map.** Create entries, add and update each gain `dockerfile: {content, hash}`. Create field errors are already indexed (`services[i].…`), so a per-entry field keeps errors on the right card and avoids name/index mismatches (the first card's name defaults to the stack name server-side).
4. **Hash semantics for Dockerfile writes.** Create: folder is new, hash ignored. Add: overwrite (the user is writing that file now; §5.5 leaves old folders behind, so a re-added service must not hit a 409), hash ignored. Update and `PUT …/dockerfile`: hash-checked; `""` means "must not exist yet"; mismatch → 409 `Dockerfile changed on disk since it was loaded`.
5. **Request body limits.** `decodeJSON` caps bodies at 1 MiB (`internal/api/respond.go`), less than one 1 MiB Dockerfile after JSON escaping. Create gets a 32 MiB cap (fits 20 full 1 MiB Dockerfiles plus JSON escaping); add, update and `PUT …/dockerfile` get 8 MiB. The 1 MiB cap applies to the decoded Dockerfile bytes. Controller ruling (the user asked for 1 MiB Dockerfiles; the body cap must not refuse a valid maximal create).
6. **Save & rebuild is one operation.** §3 says "build, then up"; §5.4 says `up --build <service>` only; §7 says "triggers build then up". The plan follows §5.4: one operation running `docker compose up -d --build --no-deps <service>`. `--no-deps` is how "other services are not rebuilt or recreated" holds; `--remove-orphans` is left out because it acts on the whole project. The operation keeps `action: "up"` and gains `service: "<name>"` (new optional field on `Operation`).
7. **Reading Dockerfiles outside the stack folder** is limited to the configured allowed roots (`fsguard.Resolve`), regular files, at most 1 MiB. Otherwise the view says "outside the allowed directories" with no content. Without this rule a compose file could show any host file (e.g. `dockerfile: /etc/shadow`) to the browser.
8. **Write safety.** The parent folder is created with mode 0755 if missing, then symlink-resolved and required to be inside the resolved stack folder. A final path component that is a symlink is refused (never followed, even when the link stays inside). The write is temp file plus rename in the verified parent. Dockerfiles get no `.bak` (it would land in the build context). A new file is 0644; an existing file keeps its mode.
9. **Top-level `name:` and `services:` in the stack `extra` are refused by the server** (field `extra`). The client never sends them (§4.1 feeds `name:` into the stack name field), and the folder name stays the project name.
10. **Long-syntax port and IP checks run on the create path only.** `EffectiveService` feeds `checkChanges` in `Create`. `SaveRaw`, `AddService` and `UpdateService` keep today's checks, so "with neither feature used … exactly as today" holds, including raw saves.
11. **Image tag on Edit service.** `UpdateService` applies no build defaults (only create/add call `prepareConfig`). When the user switches an existing service to Write a Dockerfile, the form prefills Image tag with `stackstead/<stack>[-<svc>]:latest` when the field is empty or held the previous `image:` source. This stops a build from re-tagging a pulled image such as `nginx:1.27`.
12. **Cards keep groups that live in `extra` read-only.** A group whose YAML the form cannot represent (e.g. long-syntax `ports`) moves to the card's `extra`, and the group is shown read-only with the hint "Set in YAML. Switch to YAML to change it.". Typing into an empty form field would otherwise silently replace the YAML value at merge time.
13. **Per-key round-trip rule (YAML→Form).** A service key goes into the form only when rendering the resulting form back (`joinService`) reproduces the parsed value exactly, with numbers and booleans in `ports`, `volumes` and `environment` compared as strings. Anything else moves to `extra` as a whole key. Exception: `deploy`, where only `resources.limits.{cpus,memory}` goes to the form and the rest stays in `extra.deploy`. This turns the spec's "never lose information" into one mechanical test.
14. **Dockerfile texts on New stack are kept by service name.** The text is part of the card's form in Form mode. At each Form→YAML switch the screen stores a `name → text` map. YAML→Form gives a service whose build is exactly `{context: ./<name>}` its text from that map. Renaming in YAML and back restores the text; until then the card shows Existing build `./<old>`.
15. **TypeSafe skill.** As in plan 24 and the spec, `typesafe-ai` is an SDK for a hosted judgement API, not a local decision tool; decisions are argued in writing here.
16. **No golangci-lint** is configured; verification is `go vet ./...`, `gofmt -l .`, `make test`, `make ui-check`, `make ui-test`, and `make e2e` for Task 15.

## Global Constraints

- Wire shapes (Task 1 pins them; every other task mirrors them exactly):
  - `DockerfileContent` = `{"content": string, "hash": string}`. `hash` may be omitted on create and add.
  - `POST /api/stacks` = `{"name", "services": [{"service", "config", "extra"?: object, "dockerfile"?: DockerfileContent}], "extra"?: object}`. Legacy `{name, service, config}` is unchanged and has no `extra`/`dockerfile`.
  - `POST /api/stacks/{name}/services` = `{"service", "config", "hash", "dockerfile"?}`; `PUT /api/stacks/{name}/services/{service}` = `{"config", "hash", "dockerfile"?}`. Preview bodies are the same types; the server ignores `dockerfile` in previews and the client never sends it there.
  - `GET /api/stacks/{name}/services/{service}/dockerfile` → 200 `{"content", "hash", "exists": bool, "editable": bool, "path", "reason"?}`. `path` is relative to the stack folder (slash form) when inside, absolute otherwise. `hash` is `""` when `exists` is false.
  - `PUT /api/stacks/{name}/services/{service}/dockerfile` with `{"content", "hash"}` → 200 `{"hash"}`.
  - `POST /api/stacks/{name}/services/{service}/actions/up` with `{}` → 202 `Operation`; `Operation` gains `"service"?: string` (omitted for stack-wide operations).
- Statuses: Dockerfile not editable (variables, remote, inline, extends, include) → 422 with the reason. Outside the stack folder on PUT → 403 `this Dockerfile is outside the stack's folder and can't be edited here`. Service has no build → 404 `service does not build an image`. Dockerfile hash mismatch → 409 `Dockerfile changed on disk since it was loaded`. Content too large or with NUL → 400 field error (`dockerfile` on add/update, `services[i].dockerfile` on create, `content` on PUT).
- Limits: 1 MiB (`1 << 20` bytes) per Dockerfile; no NUL bytes; create body ≤ 32 MiB; add/update/PUT-dockerfile body ≤ 8 MiB; at most 20 services (`MAX_SERVICES`, server bound unchanged).
- Managed layout: `<stack>/<service>/Dockerfile`, compose `build: ./<service>` (a scalar; no `dockerfile:` key), folder 0755, file 0644.
- Not-editable reasons, verbatim: `uses variables`, `remote build context`, `inline Dockerfile`, `uses extends`, `uses include`, `outside the stack folder`, `outside the allowed directories`.
- Field messages, verbatim: `Dockerfile is required`, `Dockerfile is larger than 1 MiB`, `Dockerfile must not contain NUL bytes`, `a Dockerfile can only be written inside the stack's folder`, `extra must be a JSON object`, `extra may not set services or name`.
- UI copy, verbatim: source radios with accessible names `From Image`, `From Existing build`, `Write a Dockerfile`; mode radiogroup `Editor mode` with `Form` and `YAML`; `A stack needs a services: section`; `A stack can have at most 20 services.`; YAML error line `Line {n}: {message}`; card note `1 more setting from YAML` / `{n} more settings from YAML`; read-only hint `Set in YAML. Switch to YAML to change it.`; Create disabled reason `Fix the YAML error first`; outside note `This Dockerfile is outside the stack's folder and can't be edited here.`; buttons `Save`, `Save & rebuild`; editor aria-label `Dockerfile for {service}`.
- Template (exact text, trailing newline): `FROM alpine:3.20\n# Add build steps below, for example:\n# RUN apk add --no-cache curl\n`.
- Lazy loading: CodeMirror (with the Dockerfile mode) stays in the `cm` chunk; `yaml` and `stackYaml.ts` ship only in lazy chunks; eager entry stays ≤ 110 kB gz (existing ratchet).
- Commits: conventional, explain why, end with the two trailer lines `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` and `Claude-Session: https://claude.ai/code/session_011L1bX7AfHUJ8cqtidt9JUF`.

## Review Focus

1. **Dockerfile write through a symlink** in the stack folder: `<stack>/web` → `/tmp/outside`, or `<stack>/web/Dockerfile` → `/etc/passwd`, or a context `../other`. Expected: refused, nothing is written outside the stack folder, and the error never echoes the target path. (Task 4 `TestSaveDockerfileRefusesSymlinkEscape`, `TestSaveDockerfileRefusesSymlinkFile`, `TestDockerfileTargetDotDot`; Task 6 `TestUpdateServiceDockerfileSymlinkedFolderRefused`.)
2. **Round trip drops a value the form "almost" supports**: healthcheck test `["CMD", "curl", "-f", "http://a b"]`, env `KEY:` (null = pass-through), `ports: [{target: 80, published: 8080}]`, `networks: {lan: {ipv4_address: x, aliases: [a]}}`, and `deploy` with both limits and `placement`. Expected: Form→YAML→Form→YAML deep-equals the original document. (Task 11 `round trip keeps values the form cannot hold`.)
3. **YAML mode bypasses a server rule**: a long-form `build: {context: /etc, args: …}`, a long-syntax published port already used by another stack, or a static IP from an extra `networks` mapping. Expected: the same field errors as the form path. (Task 5 `TestCreateExtraBuildConfined`, `TestCreateExtraLongPortsChecked`, `TestCreateExtraStaticIPChecked`.)
4. **Failed create leaves service folders behind.** `compose config` fails after Dockerfiles were written. Expected: neither `<stack>/` nor `<stack>/web/` exists afterwards. (Task 5 `TestCreateDockerfileRollbackOnComposeConfigFailure`.)
5. **Rebuild touches other services.** Expected: the scoped operation's compose args are exactly `up -d --build --no-deps <service>` and the stack-wide `up` is unchanged. (Task 7 `TestActServiceRunsScopedUp`, `TestActUpUnchanged`.)

---

## File structure

| Area | Files | Task |
|---|---|---|
| Wire contract (Go + TS) | `internal/apitypes/types.go`, `types_test.go`; `web/src/lib/api/types.ts`, `client.ts`, `client.test.ts` | T1 |
| Effective service read (long syntax, build ref) | `internal/composefile/effective.go` (new), `effective_test.go` (new) | T2 |
| Extras and merged render | `internal/composefile/extra.go` (new), `model.go`, `build.go`, `extra_test.go` (new) | T3 |
| Dockerfile resolver, safe write, view/save | `internal/stacks/dockerfile.go` (new), `dockerfile_test.go` (new), `internal/composefile/disk.go` (export `WriteAtomic`), `internal/api/stacks_errors.go`, `internal/api/respond.go` | T4 |
| Create with extras and Dockerfiles | `internal/stacks/service_create.go`, `create_extra_test.go` (new); `internal/api/stacks.go` (`handleCreateStack`), `stacks_dto.go` (`createServiceToDomain`), `stacks_create_test.go` (new) | T5 |
| Add/update with Dockerfile | `internal/stacks/service_edit.go`, `export_test.go`, `service_dockerfile_test.go` (new), existing callers in `internal/stacks/*_test.go`; `internal/api/stacks.go` (`StacksAPI`, add/update handlers), `stacks_test.go` (fake signatures) | T6 |
| Dockerfile routes, scoped up | `internal/stacks/service.go` (`ActService`), `ops.go` (`StartService`, `Operation.Service`); `internal/api/dockerfiles.go` (new), `dockerfiles_test.go` (new), `stacks.go` (one route-registration line), `stacks_dto.go` (`toOperation`) | T7 |
| Form model | `web/src/lib/compose/form.ts`, `form.test.ts` | T8 |
| Editor language | `web/package.json`, `package-lock.json`, `web/src/lib/stacks/cm.ts`, `cm.test.ts`, `web/src/app/stacks/YamlEditor.tsx`, `DiffView.tsx`, `yamlEditor.test.tsx`, `web/src/bundle.test.ts` | T9 |
| Service form source | `web/src/app/stacks/ServiceForm.tsx`, `ServiceForm.module.css`, `serviceForm.test.tsx`; label updates in `web/src/app/create/stackCreate.test.tsx`, `stackCreate.live.test.tsx` | T10 |
| YAML ↔ cards | `web/src/lib/compose/stackYaml.ts` (new), `stackYaml.test.ts` (new), `web/package.json`, `package-lock.json`, `web/src/bundle.test.ts` | T11 |
| New stack screen | `web/src/app/create/{useCreateForm.ts,StackCreateScreen.tsx,StackCreateScreen.module.css,ServiceCard.tsx,ComposePreview.tsx,YamlMode.tsx (new),stackCreate.yaml.test.tsx (new),stackCreate.test.tsx}`, `web/src/lib/stacks/createPreview.ts`, `createPreview.test.ts` | T12 |
| Add/Edit service Dockerfile | `web/src/lib/stacks/serviceAdd.ts`, `serviceEdit.ts`, their tests; `web/src/app/detail/AddServicePanel.tsx`, `ServiceEditor.tsx`, `addServicePanel.test.tsx`, `serviceEditor.test.tsx` | T13 |
| Stack page Dockerfile tab | `web/src/app/detail/DockerfilePanel.tsx` (new), `DockerfilePanel.module.css` (new), `dockerfilePanel.test.tsx` (new), `ServicePane.tsx`, `StackDetailScreen.tsx`, `OperationsList.tsx`, `OperationPanel.tsx`, `web/src/lib/stacks/detailLocation.ts`, `detailLocation.test.ts` | T14 |
| e2e | `cmd/stackstead/e2e_test.go` | T15 |
| Docs | `docs/api.md`, `docs/security.md`, `README.md` | T16 |

### Lanes and dependency graph

```
Wave 1 (all parallel):  T1 contract | T2 composefile read | T3 composefile write | T8 form model | T9 editor language
Wave 2:                 T4 (needs T2)  |  T10 (needs T8, T9)  |  T11 (needs T8)  |  T14 (needs T1, T9)
Wave 3:                 T5 (needs T1, T2, T3, T4) | T6 (needs T1, T4) | T7 (needs T1, T4) | T12 (needs T1, T10, T11) | T13 (needs T1, T10)
Wave 4:                 T15 e2e (needs T5, T6, T7) | T16 docs (needs T5, T6, T7)
```

- **Backend lane:** T2 ∥ T3 → T4 → T5 ∥ T6 ∥ T7. **Web lane:** T8 ∥ T9 → T10 ∥ T11 ∥ T14 → T12 ∥ T13. T1 is in neither lane and is small; merge it first so both lanes compile against the pinned shapes. Web tasks only need T1's TypeScript, never a running backend.
- Files two parallel tasks both touch (give each subagent its own worktree, merge in task order, hunks do not overlap):
  - T2 and T3: both in `internal/composefile`, separate files. T3 alone edits `model.go` and `build.go`.
  - T5, T6, T7 share `internal/api/stacks.go`: T5 edits `handleCreateStack`; T6 edits the `StacksAPI` interface plus `handleAddService` and `handleUpdateService`; T7 adds one call in `registerStackRoutes`.
  - T5 and T7 share `internal/api/stacks_dto.go`: T5 adds `createServiceToDomain`, T7 edits `toOperation`.
  - T9 and T11 share `web/package.json`, `web/package-lock.json` and `web/src/bundle.test.ts` (separate `it` blocks). After merging, run `cd web && npm install` to regenerate the lockfile; never hand-merge it.
  - T10 and T12 both edit `stackCreate.test.tsx` (T10 changes the radio label only). T12 depends on T10, so they never run together.
- Opus reviews each task; Fable (or Opus) reviews the branch.

---

### Task 1: Wire contract (Go apitypes and TypeScript)

**Model:** Haiku — mechanical: every name, tag and path is pinned in Global Constraints.

**Files:**
- Modify: `internal/apitypes/types.go`, `internal/apitypes/types_test.go`
- Modify: `web/src/lib/api/types.ts`, `web/src/lib/api/client.ts`, `web/src/lib/api/client.test.ts`

**Interfaces:**
- Consumes: nothing.
- Produces (Go):
  - `type DockerfileContent struct { Content string \`json:"content"\`; Hash string \`json:"hash"\` }`
  - `CreateStackService` gains `Extra json.RawMessage \`json:"extra,omitempty"\`` and `Dockerfile *DockerfileContent \`json:"dockerfile,omitempty"\``
  - `CreateStackRequest` gains `Extra json.RawMessage \`json:"extra,omitempty"\`` (its strict `UnmarshalJSON` keeps working because the field is in the struct)
  - `AddServiceRequest`, `UpdateServiceRequest` gain `Dockerfile *DockerfileContent \`json:"dockerfile,omitempty"\``
  - `type DockerfileView struct { Content, Hash string; Exists, Editable bool; Path string; Reason string \`json:"reason,omitempty"\` }` with tags `content,hash,exists,editable,path,reason`
  - `Operation` gains `Service string \`json:"service,omitempty"\``
- Produces (TS, `types.ts`): `DockerfileContent { content: string; hash?: string }`; `CreateStackService` + `extra?: Record<string, unknown>; dockerfile?: DockerfileContent`; `CreateStackRequest` + `extra?: Record<string, unknown>`; `AddServiceRequest` becomes explicit `{ service; config; hash; dockerfile?: DockerfileContent }` (it no longer extends `CreateStackService`, so `extra` cannot be sent to a route that rejects it); `UpdateServiceRequest` + `dockerfile?`; `DockerfileView { content; hash; exists; editable; path; reason?: string }`; `Operation.service?: string`.
- Produces (TS, `client.ts` members of `createApi`):
  - `addService(name, service, config, hash, dockerfile?: DockerfileContent)` and `updateService(name, service, config, hash, dockerfile?: DockerfileContent)`: `dockerfile` is included in the body only when given; previews unchanged.
  - `dockerfile(name: string, service: string): Promise<DockerfileView>` → `GET /api/stacks/{n}/services/{s}/dockerfile`
  - `saveDockerfile(name, service, content: string, hash: string): Promise<HashResponse>` → `PUT …/dockerfile`
  - `upService(name: string, service: string): Promise<Operation>` → `POST /api/stacks/{n}/services/{s}/actions/up` with `{}`

- [ ] **Step 1: Write the failing tests**
  - `TestJSONTagsMatchContract` gains rows: `DockerfileContent` → `"content,hash"`; `DockerfileView` → `"content,editable,exists,hash,path,reason"`; `CreateStackService` → `"config,dockerfile,extra,service"`; `CreateStackRequest` → `"config,extra,name,service,services"`; `AddServiceRequest` → `"config,dockerfile,hash,service"`; `UpdateServiceRequest` → `"config,dockerfile,hash"`; `Operation` → add `service`. `fill` gains a `json.RawMessage` case (set to `{"x":1}`; a zero byte would fail `json.Marshal`).
  - `TestCreateStackRequestAcceptsExtraAndDockerfile`: strict unmarshal of `{"name":"a","services":[{"service":"web","config":{"limits":{}},"extra":{"labels":{"a":"b"}},"dockerfile":{"content":"FROM x"}}],"extra":{"volumes":{"d":{}}}}` succeeds and `UsesServices()` is true; an unknown key `"dockerfiles"` still fails.
  - `client.test.ts`: `addService sends dockerfile only when given` (body keys with and without); `updateService sends dockerfile with hash`; `dockerfile() GETs the service dockerfile URL` (`/api/stacks/a%20b/services/web/dockerfile`); `saveDockerfile PUTs {content, hash}`; `upService POSTs {} to …/services/web/actions/up`; `createApi('/api/servers/b1').upService` is prefixed.
- [ ] **Step 2: Run** `go test -race ./internal/apitypes/ -run 'JSONTags|CreateStackRequestAccepts'` → FAIL `undefined: apitypes.DockerfileContent`; `cd web && npx vitest run src/lib/api/client.test.ts` → FAIL `api.upService is not a function`.
- [ ] **Step 3: Implement** the types and client members. Run `make ui-check`: callers of the old `AddServiceRequest` shape still type-check because their bodies already list `service, config, hash`.
- [ ] **Step 4: Run** both commands → `ok` / all pass; then `make ui-check`.
- [ ] **Step 5: Commit** `feat(api): define the wire shapes for compose extras, Dockerfiles and scoped up` (why: backend and web lanes build in parallel against one pinned contract), with the trailer.

---

### Task 2: Effective service read (long syntax and build reference)

**Model:** Sonnet — YAML node walking with anchors, merge keys and long-syntax port forms; feeds security checks.

**Files:**
- Create: `internal/composefile/effective.go`, `internal/composefile/effective_test.go`

**Interfaces:**
- Consumes: `(*File).Service`, `serviceNode`, `mappingValue`, `isScalar` (package-internal).
- Produces:
  - `type BuildRef struct { Context string /* as written; "" = compose default "." */; Dockerfile string /* "" = "Dockerfile" */; Inline bool /* dockerfile_inline set */ }`
  - `func (f *File) BuildOf(service string) (ref BuildRef, ok bool, err error)`: reads the service decoded with aliases and merge keys resolved (`node.Decode` into `map[string]any`). `ok` is false without `build`. The scalar form is the context. In the mapping form, a non-scalar `context` or `dockerfile` is an `ErrNotMapping`-wrapped error. `ErrServiceNotFound` for an unknown service.
  - `func (f *File) UsesExtends(service string) bool`; `func (f *File) UsesInclude() bool` (top-level `include`)
  - `func (f *File) EffectiveService(name string) (ServiceConfig, error)`: `Service(name).Config` plus, only for groups that `Service` marked read-only, values from any syntax. `Ports` gets one spec per list entry, indices preserved: short entries as written; long entries rendered `[host_ip:]published:target[/protocol]` (IPv6 `host_ip` in brackets, `/tcp` omitted) or bare `target` when there is no `published`. `Networks` gets a name plus `ipv4_address` from mappings with any other keys. `Build` gets `{Context, Dockerfile}` from `BuildOf`.

- [ ] **Step 1: Write the failing tests** (`effective_test.go`)
  - `TestBuildOf` table: `build: ./web` → `{Context:"./web"}`, ok; `build: {context: ., dockerfile: Dockerfile.dev, args: {A: 1}}` → `{".", "Dockerfile.dev", false}`; `build: {dockerfile_inline: "FROM x"}` → `Inline: true`; merge key `<<: *b` supplying `build` → resolved; no build → `ok == false`; `build: {context: [a]}` → error; unknown service → `ErrServiceNotFound`.
  - `TestEffectiveServiceLongPorts`: `ports: ["8080:80", {target: 80, published: 9090, host_ip: 127.0.0.1, protocol: udp}, {target: 81}, {target: 82, published: "7000-7001", host_ip: "::1"}]` → `["8080:80", "127.0.0.1:9090:80/udp", "81", "[::1]:7000-7001:82"]`; `Service()` still reports `ports` read-only and `Config.Ports == nil`.
  - `TestEffectiveServiceNetworksWithAliases`: `networks: {lan: {ipv4_address: 192.168.1.5, aliases: [a]}, b: null}` → `[{lan 192.168.1.5} {b ""}]`.
  - `TestEffectiveServiceLongBuild`: `build: {context: /srv/app, args: {X: 1}}` → `Build == &Build{Context: "/srv/app"}`.
  - `TestEffectiveServiceMatchesServiceForPlainFiles`: for every fixture in `read_test.go`'s simple cases, `EffectiveService == Service().Config`.
  - `TestUsesExtendsAndInclude`: `extends: {service: base}` → true; top-level `include: [x.yaml]` → true; neither → false.
- [ ] **Step 2: Run** `go test -race ./internal/composefile/ -run 'BuildOf|EffectiveService|UsesExtends'` → FAIL `f.BuildOf undefined`.
- [ ] **Step 3: Implement** `effective.go`. Long port entries are decoded per entry into a small struct (`Target`, `Published`, `HostIP`, `Protocol` as `any`/string) so `published: 8080` and `"8080"` both work.
- [ ] **Step 4: Run** the same command, then `go test -race ./internal/composefile/` → `ok`.
- [ ] **Step 5: Commit** `feat(composefile): read build, ports and networks in any compose syntax` (why: YAML mode can carry long-syntax values the form model drops, and the server's confinement and conflict checks must still see them), with the trailer.

---

### Task 3: Compose extras and the merged render

**Model:** Sonnet — node-tree deep merge with precedence rules and key order.

**Files:**
- Create: `internal/composefile/extra.go`, `internal/composefile/extra_test.go`
- Modify: `internal/composefile/model.go` (`NamedService.Extra`), `internal/composefile/build.go` (`New` delegates to `NewStack`)

**Interfaces:**
- Consumes: `serviceNode`, `mapNode`, `strNode`, `encode`, `blockCopy` (render.go).
- Produces:
  - `var ErrInvalidExtra = errors.New("extra must be a JSON object")`
  - `type Extra struct { node *yaml.Node }`; `func ParseExtra(raw []byte) (Extra, error)` — `nil`, empty or `null` → zero `Extra`; a JSON object → parsed with `yaml.Unmarshal` (keeps key order) and converted to block style; anything else → `ErrInvalidExtra`
  - `func (e Extra) IsZero() bool`; `func (e Extra) Keys() []string` (in order)
  - `NamedService` gains `Extra Extra`
  - `func NewStack(services []NamedService, top Extra) ([]byte, error)`; `New(services)` becomes `NewStack(services, Extra{})` and keeps producing identical bytes
  - Merge rules: service node = `serviceNode(cfg)` deep-merged with the service's extra. Mapping + mapping recurses; at any other collision the form's value wins. Keys only in the extra are appended after the form keys, in the extra's order. Top level = `services`, then the automatic `networks: {<n>: {external: true}}` for attached networks *not* declared in `top.networks`, deep-merged with `top`. A `top` with key `services` or `name` → `ErrInvalidExtra` wrapped with "extra may not set services or name".

- [ ] **Step 1: Write the failing tests** (`extra_test.go`)
  - `TestParseExtra` table: `{"labels":{"a":"b"}}` → `Keys() == ["labels"]`; `null`, `""`, `nil` → `IsZero()`; `[]`, `"x"`, `1` → `ErrInvalidExtra`; key order of `{"z":1,"a":2}` → `["z","a"]`.
  - `TestNewStackWithoutExtrasMatchesNew`: for the fixtures in `build_test.go`, `NewStack(s, Extra{})` bytes equal `New(s)`.
  - `TestNewStackMergesServiceExtra`: form limits `cpus 0.5` + extra `{"deploy":{"placement":{"constraints":["node.role==manager"]},"resources":{"limits":{"cpus":"2","pids":100}}},"labels":{"a":"b"}}` → parsed result has `deploy.resources.limits.cpus == "0.5"` (form wins), `pids == 100`, `placement` kept, `labels` after `deploy`; `Parse` + `Service("web")` reads the form fields back.
  - `TestNewStackKeepsUserNetworkDefinition`: services attach `lan` and `ext`; top `{"networks":{"lan":{"driver":"bridge"}}}` → `lan` has `driver: bridge` and no `external`; `ext` is `external: true`.
  - `TestNewStackTopExtraOrderAndKeys`: top `{"volumes":{"data":{}},"x-common":{"a":1},"secrets":{"s":{"file":"./s"}}}` → those keys follow `services`/`networks` in that order.
  - `TestNewStackRejectsReservedTopKeys`: top with `services` or `name` → `errors.Is(err, ErrInvalidExtra)`.
- [ ] **Step 2: Run** `go test -race ./internal/composefile/ -run 'ParseExtra|NewStack'` → FAIL `undefined: ParseExtra`.
- [ ] **Step 3: Implement** `extra.go` and the `model.go`/`build.go` changes.
- [ ] **Step 4: Run** the same, then `go test -race ./internal/composefile/` → `ok`.
- [ ] **Step 5: Commit** `feat(composefile): render a stack with compose keys the forms do not model` (why: YAML mode must keep labels, logging, top-level volumes and the rest without widening the form model), with the trailer.

---

### Task 4: Dockerfile resolver, safe write, view and save

**Model:** Sonnet — path confinement under symlinks is the security boundary of this plan.

**Files:**
- Create: `internal/stacks/dockerfile.go`, `internal/stacks/dockerfile_test.go`
- Modify: `internal/composefile/disk.go` (rename `writeAtomic` → exported `WriteAtomic(path string, data []byte, mode fs.FileMode) error`; update its callers)
- Modify: `internal/api/stacks_errors.go` (+ test in `internal/api/stacks_test.go` error-mapping table, or a new `stacks_errors_test.go` if none exists), `internal/api/respond.go`

**Interfaces:**
- Consumes: T2 `BuildOf`, `UsesExtends`, `UsesInclude`; `lockedEntry`, `readFile`, `fsguard.Resolve`, `s.roots`.
- Produces (stacks):
  - `const MaxDockerfileBytes = 1 << 20`
  - `var ErrOutsideStack`, `ErrNoBuild`, `ErrDockerfileConflict` with the Global Constraints messages
  - `type NotEditableError struct{ Reason string }` — `Error()` = `"Dockerfile can't be edited here: " + Reason`
  - `func ValidateDockerfile(field string, content []byte) error` — `*ValidationError` on `field` with the size/NUL messages
  - `type DockerfileView struct { Content []byte; Hash string; Exists, Editable bool; Path, Reason string }`
  - `func dockerfileTarget(stackDir string, f *composefile.File, service string) (path string, inside bool, err error)`. Errors: `ErrNoBuild`; `*NotEditableError` when the context or dockerfile contains `${` or `$` + letter, the context is remote (`://`, `git@`, a `github.com/` prefix), the Dockerfile is inline, or the service uses extends or the file uses include. Otherwise `path` is the cleaned absolute path (relative contexts join `stackDir`; an absolute `dockerfile` stands alone). `inside` is true when the symlink-resolved longest existing prefix of `path` plus the remainder lies inside the resolved `stackDir`.
  - `func writeDockerfile(stackDir, path string, content []byte, expectedHash *string) (hash string, rollback func(), err error)` — decision 8. `expectedHash == nil` skips the check. `rollback` restores the previous bytes or removes the new file and every folder this call created.
  - `func (s *Service) Dockerfile(ctx context.Context, stack, service string) (DockerfileView, error)` — inside: content read with no-follow on the final component, `Path` relative, `Editable` true (also when `Exists` is false). Outside: decision 7. Not editable: `Reason` from the error and no content.
  - `func (s *Service) SaveDockerfile(ctx context.Context, stack, service string, content []byte, hash string) (string, error)` — under the stack lock; `ValidateDockerfile("content", …)`; outside → `ErrOutsideStack`; hash checked
- Produces (api):
  - `writeStackError`: `ErrOutsideStack` → 403, `ErrNoBuild` → 404, `ErrDockerfileConflict` → 409, `*NotEditableError` → 422 with its message
  - `func decodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool` (`decodeJSON` = `decodeJSONLimit(…, maxBodyBytes)`); `const createBodyBytes = 32 << 20`, `dockerfileBodyBytes = 8 << 20`

- [ ] **Step 1: Write the failing tests** (`dockerfile_test.go`, using `newHarness` and stacks written to disk directly)
  - `TestDockerfileTargetLayouts` table: `build: ./web` → `<stack>/web/Dockerfile`, inside; `build: {context: ., dockerfile: Dockerfile.dev}` → `<stack>/Dockerfile.dev`, inside; absolute context inside the stack → inside; `build: /srv/elsewhere` → outside; no build → `ErrNoBuild`.
  - `TestDockerfileTargetDotDot`: `build: ../other` → outside (never inside after `Clean`).
  - `TestDockerfileTargetNotEditable` table → reasons `uses variables` (`./${APP}`), `remote build context` (`https://github.com/a/b.git`, `git@github.com:a/b.git`), `inline Dockerfile`, `uses extends`, `uses include`.
  - `TestDockerfileViewInside`: existing file → content, `hash == composefile.Hash(content)`, `path == "web/Dockerfile"`, editable; missing file → `exists == false`, `hash == ""`, editable.
  - `TestDockerfileViewOutside`: file under an allowed root outside the stack → content shown, `editable == false`, `reason == "outside the stack folder"`, absolute path; file outside every root → no content, reason `outside the allowed directories`.
  - `TestSaveDockerfileHashConflict`: wrong hash → `ErrDockerfileConflict`, file unchanged; `""` on an existing file → conflict; `""` on a missing file → created 0644, parent 0755.
  - `TestSaveDockerfileRefusesSymlinkEscape`: `<stack>/web` is a symlink to a temp dir outside → `ErrOutsideStack`, the outside dir stays empty.
  - `TestSaveDockerfileRefusesSymlinkFile`: `<stack>/web/Dockerfile` symlinks to a file inside the stack → refused; the target is unchanged.
  - `TestSaveDockerfileLimits`: `1<<20 + 1` bytes → field `content` "Dockerfile is larger than 1 MiB"; `"FROM x\x00"` → NUL message; exactly `1<<20` bytes → ok.
  - `TestWriteDockerfileRollback`: new nested path → rollback removes the file and created folders; existing file → rollback restores the old bytes and mode.
  - api: `TestStackErrorMapsDockerfileErrors` table → 403, 404, 409, 422 with messages.
- [ ] **Step 2: Run** `go test -race ./internal/stacks/ -run 'Dockerfile' && go test -race ./internal/api/ -run StackErrorMapsDockerfile` → FAIL `undefined: stacks.ErrOutsideStack`.
- [ ] **Step 3: Implement.** Use `os.Lstat` on the final component; resolve the parent with `filepath.EvalSymlinks` after `MkdirAll`, and compare with the resolved stack folder using the same whole-component prefix test as `fsguard` (`within`). Read inside files via `os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)`.
- [ ] **Step 4: Run** the same → `ok`; then `make test`.
- [ ] **Step 5: Commit** `feat(stacks): resolve, read and write a service's Dockerfile inside the stack folder only` (why: every Dockerfile route must share one confinement rule that holds under symlinks at write time), with the trailer.

---

### Task 5: Create with extras and written Dockerfiles

**Model:** Sonnet — extends the create rollback sequence and runs every existing check on the merged document.

**Files:**
- Modify: `internal/stacks/service_create.go`
- Create: `internal/stacks/create_extra_test.go`
- Modify: `internal/api/stacks.go` (`handleCreateStack`), `internal/api/stacks_dto.go` (`createServiceToDomain`)
- Create: `internal/api/stacks_create_test.go`

**Interfaces:**
- Consumes: T1 `apitypes.CreateStackService.Extra/Dockerfile`, `CreateStackRequest.Extra`; T2 `EffectiveService`, `BuildOf`; T3 `ParseExtra`, `NewStack`, `NamedService.Extra`; T4 `ValidateDockerfile`, `dockerfileTarget`, `writeDockerfile`, `decodeJSONLimit`, `createBodyBytes`.
- Produces:
  - `CreateRequest` gains `Extra composefile.Extra` and `Dockerfiles map[int][]byte` (keyed by `Services` index)
  - `func (s *Service) confineBuild(stackDir string, b composefile.Build) (composefile.Build, error)` — the context-confinement half of `prepareConfig`, extracted unchanged; `prepareConfig` calls it
  - Create sequence: validate entries as today; `ValidateDockerfile(fmt.Sprintf("services[%d].dockerfile", i), …)`; render `NewStack`; `Parse`; per service `EffectiveService`. When the build came from the extra, run `confineBuild` (field `services[i].build.context`). For a Dockerfile entry, `dockerfileTarget` against the lexical stack dir must be inside (field `services[i].dockerfile`, message `a Dockerfile can only be written inside the stack's folder`). `checkChanges` gets the effective configs plus `localNetworks: parsed.LocalNetworks()`. Then claim the dir, `Save` compose, `writeDockerfile(…, nil)` for each entry, `compose config` and register. The deferred cleanup runs every Dockerfile rollback before removing `compose.yaml` and the dir.
  - API: `handleCreateStack` uses `decodeJSONLimit(…, createBodyBytes)`; `createServiceToDomain(i int, e apitypes.CreateStackService) (composefile.NamedService, []byte /*dockerfile*/, error)`. A bad service extra → 400 field `services[i].extra`; a bad top extra, including reserved keys → 400 field `extra`.

- [ ] **Step 1: Write the failing tests** (`create_extra_test.go` with `newHarness`)
  - `TestCreateWritesManagedDockerfile`: service `web`, build `./web`, Dockerfile `"FROM busybox\n"` → `<stack>/web/Dockerfile` holds it, file 0644, folder 0755; compose has `build: ./web` and `image: stackstead/<stack>:latest`.
  - `TestCreateDockerfileRollbackOnComposeConfigFailure`: `fakeCompose` config error → `os.Stat(<stack>)` is `ErrNotExist`.
  - `TestCreateDockerfileLimits`: oversize or NUL → `ValidationError` field `services[0].dockerfile` with the exact messages; stack dir absent.
  - `TestCreateDockerfileRequiresBuildInsideStack` table: image-only service, `build: ../x`, absolute outside context → field `services[i].dockerfile` with the inside-only message.
  - `TestCreateMergesExtras`: service extra `labels`, `logging`; top `volumes`, `secrets`, `x-common` → present in `compose.yaml`; `fakeCompose` config saw that file.
  - `TestCreateExtraBuildConfined`: service extra `{"build":{"context":"/etc","args":{"A":"1"}}}` and an image-less form → field `services[0].build.context` "build context must be inside an allowed directory".
  - `TestCreateExtraLongPortsChecked`: another stack declares `8080:80`; extra `{"ports":[{"target":80,"published":8080}]}` → field `services[0].ports[0]` naming that stack.
  - `TestCreateExtraStaticIPChecked`: engine network `lan` with a container on `192.168.1.5`; extra `{"networks":{"lan":{"ipv4_address":"192.168.1.5","aliases":["a"]}}}` → field `services[0].networks.lan.ipv4_address`.
  - `TestCreateWithoutExtrasWritesSameBytes`: a multi-service request without extras or Dockerfiles writes the bytes `composefile.New` gives (guards success criterion "behave exactly as today").
  - api `stacks_create_test.go`: `TestCreateStackPassesExtrasAndDockerfiles` (the fake records `CreateRequest`: `Extra.Keys()`, `Dockerfiles[0]`); `TestCreateStackRejectsNonObjectExtra` → 400 field `services[0].extra`; `TestCreateStackRejectsReservedTopExtra` → 400 field `extra`; `TestCreateStackBodyLimit`: a 3 MiB Dockerfile body is decoded (reaches the fake), 9 MiB → 400.
- [ ] **Step 2: Run** `go test -race ./internal/stacks/ -run 'TestCreate(Writes|Dockerfile|Merges|Extra|WithoutExtras)' && go test -race ./internal/api/ -run TestCreateStack` → FAIL `unknown field Dockerfiles`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same, then `make test` (the `service_multi_test.go` create tests must pass unchanged).
- [ ] **Step 5: Commit** `feat(stacks): create stacks from compose extras and written Dockerfiles` (why: YAML mode and Write a Dockerfile both end in Create, which must keep its all-or-nothing rollback and every check the form path has), with the trailer.

---

### Task 6: Add and update a service with a Dockerfile

**Model:** Sonnet — threads a write and its rollback through the shared commit pipeline.

**Files:**
- Modify: `internal/stacks/service_edit.go`, `internal/stacks/export_test.go` (seam)
- Create: `internal/stacks/service_dockerfile_test.go`
- Modify: callers of `AddService`/`UpdateService` in `internal/stacks/*_test.go`
- Modify: `internal/api/stacks.go` (`StacksAPI`, `handleAddService`, `handleUpdateService`), `internal/api/stacks_test.go` (fake signatures, plus `TestAddServicePassesDockerfile`, `TestUpdateServicePassesDockerfile`)

**Interfaces:**
- Consumes: T1 request fields; T2 `EffectiveService`; T4 `ValidateDockerfile`, `dockerfileTarget`, `writeDockerfile`, `ErrDockerfileConflict`, `decodeJSONLimit`, `dockerfileBodyBytes`.
- Produces:
  - `type DockerfileWrite struct { Content []byte; Hash string; CheckHash bool }`
  - `func (s *Service) AddService(ctx context.Context, stack, service string, cfg composefile.ServiceConfig, hash string, df *DockerfileWrite) (EditResult, error)`
  - `func (s *Service) UpdateService(ctx context.Context, stack, service string, cfg composefile.ServiceConfig, hash string, df *DockerfileWrite) (EditResult, error)`
  - `StacksAPI` signatures change the same way; previews unchanged.
  - `commit(ctx, e, old, edited, hash, qualify, preSave func() (rollback func(), err error))` — runs `preSave` after `validateCandidate`, before `composefile.Save`; calls `rollback` when that save (or the project claim) fails. `SaveRaw`/`RemoveService` pass nil.
  - Before commit: `ValidateDockerfile("dockerfile", …)` and `dockerfileTarget` on the **edited** file. Not inside → `ValidationError` field `dockerfile` with the inside-only message; not editable → `*NotEditableError`.
  - API: add → `&DockerfileWrite{CheckHash: false}`; update → `&DockerfileWrite{Hash: req.Dockerfile.Hash, CheckHash: true}`; both decode with `dockerfileBodyBytes`.
  - Seam: `func SetBeforeComposeSave(s *Service, fn func())` in `export_test.go`, called just before `composefile.Save` in `commit`.

- [ ] **Step 1: Write the failing tests** (`service_dockerfile_test.go`)
  - `TestAddServiceWritesManagedDockerfile`: add `api` with build `./api` + content → file written; compose has the service with image `stackstead/<stack>-api:latest`.
  - `TestAddServiceOverwritesLeftoverDockerfile`: `<stack>/api/Dockerfile` exists from a removed service → replaced, no conflict.
  - `TestAddServiceNoWriteWhenComposeInvalid`: `fakeCompose` config error → no `<stack>/api/`.
  - `TestAddServiceDockerfileRolledBackWhenComposeSaveFails`: seam rewrites `compose.yaml` → `ErrConflict`; `<stack>/api/` absent afterwards.
  - `TestUpdateServiceWritesInPlaceDockerfile`: existing `build: {context: ., dockerfile: Dockerfile.dev}` and file; correct hash → written in place, compose unchanged; wrong hash → `ErrDockerfileConflict`, neither file changed.
  - `TestUpdateServiceSwitchImageToManaged`: image service → build `./web` + content, hash `""` → `<stack>/web/Dockerfile` created; any other file untouched.
  - `TestUpdateServiceDockerfileOutsideRefused`: build `/srv/elsewhere` → `ValidationError` field `dockerfile`; nothing written.
  - `TestUpdateServiceDockerfileSymlinkedFolderRefused`: `<stack>/web` → outside dir → refused; outside dir unchanged.
  - `TestUpdateWithoutDockerfileUnchanged`: `df == nil` behaves exactly as before (existing update tests also pass with the new argument).
  - api: `TestAddServicePassesDockerfile` and `TestUpdateServicePassesDockerfile` (fake records `df`; update has `CheckHash`); `TestUpdateServiceDockerfileConflictIs409`.
- [ ] **Step 2: Run** `go test -race ./internal/stacks/ -run 'TestAddService|TestUpdateService' && go test -race ./internal/api/ -run 'AddService|UpdateService'` → FAIL: too many arguments in call.
- [ ] **Step 3: Implement**; update all existing callers to pass `nil`.
- [ ] **Step 4: Run** the same, then `make test`.
- [ ] **Step 5: Commit** `feat(stacks): write a service's Dockerfile when it is added or edited` (why: the Dockerfile must land only after compose accepts the change and must not outlive a compose write that failed), with the trailer.

---

### Task 7: Dockerfile routes and the service-scoped `up`

**Model:** Sonnet — new operation variant plus routes and status mapping.

**Files:**
- Modify: `internal/stacks/ops.go`, `internal/stacks/service.go`, `internal/stacks/ops_test.go`, `internal/stacks/service_test.go`
- Create: `internal/api/dockerfiles.go`, `internal/api/dockerfiles_test.go`
- Modify: `internal/api/stacks.go` (one line in `registerStackRoutes`), `internal/api/stacks_dto.go` (`toOperation` copies `Service`)

**Interfaces:**
- Consumes: T1 `apitypes.DockerfileView`, `DockerfileContent`, `Operation.Service`; T4 `Dockerfile`, `SaveDockerfile`, error mapping, `decodeJSONLimit`.
- Produces:
  - `Operation` (stacks) gains `Service string`; `func (o *Ops) StartService(stack, action, service string, fn OpFunc) (Operation, error)`; `Start(stack, action, fn)` = `StartService(stack, action, "", fn)`; busy rule unchanged (one running op per stack)
  - `func (s *Service) ActService(ctx context.Context, stack, service, action string) (Operation, error)` — only `"up"` (else `ValidationError` field `action`); unknown service → `composefile.ErrServiceNotFound`; same lock/refresh sequence as `Act`; args `["up", "-d", "--build", "--no-deps", service]`
  - api optional interface (so existing fakes need no change): `type dockerfileAPI interface { Dockerfile(ctx, stack, service string) (stacks.DockerfileView, error); SaveDockerfile(ctx, stack, service string, content []byte, hash string) (string, error); ActService(ctx, stack, service, action string) (stacks.Operation, error) }` with `var _ dockerfileAPI = (*stacks.Service)(nil)`; `func (s *server) registerDockerfileRoutes(mux *http.ServeMux)` registers `GET`/`PUT /api/stacks/{name}/services/{service}/dockerfile` and `POST /api/stacks/{name}/services/{service}/actions/{action}` behind `requireSession`, only when `s.opts.Stacks` implements it

- [ ] **Step 1: Write the failing tests**
  - stacks: `TestActServiceRunsScopedUp`: `fakeCompose.lastRun().args == [up -d --build --no-deps web]`, op `Action == "up"`, `Service == "web"`; `TestActUpUnchanged`: stack `Act(…, "up")` args unchanged and `Service == ""`; `TestActServiceUnknownService` → `ErrServiceNotFound`; `TestActServiceRejectsOtherActions` (`"down"`) → field `action`; `TestActServiceBusy` → `ErrBusy` while a stack op runs.
  - api (`dockerfiles_test.go`, a fake implementing `StacksAPI` by embedding plus `dockerfileAPI`): `TestGetDockerfile` body keys exactly `content,editable,exists,hash,path` (+`reason` when set); `TestPutDockerfile` → 200 `{"hash"}` and the fake got the bytes; stale hash → 409; outside → 403; not editable → 422 with reason; no build → 404; oversize → 400 field `content`; `TestServiceUpRoute` → 202 with `"service":"web"`; `TestDockerfileRoutesNeedSession` → 401 without cookie; `TestDockerfileRoutesAbsentWithoutSupport` → 404 when the stacks fake lacks `dockerfileAPI`.
- [ ] **Step 2: Run** `go test -race ./internal/stacks/ -run 'ActService|ActUpUnchanged' && go test -race ./internal/api/ -run 'Dockerfile|ServiceUpRoute'` → FAIL `undefined: ActService` / 404.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same, then `make test`.
- [ ] **Step 5: Commit** `feat(api): view, save and rebuild one service's Dockerfile` (why: the stack page edits Dockerfiles after creation and Save & rebuild must not recreate the other services), with the trailer.

---

### Task 8: Form model for "Write a Dockerfile"

**Model:** Sonnet — `fromForm`/`validateForm` are shared by create, add and edit; a wrong default breaks all three.

**Files:**
- Modify: `web/src/lib/compose/form.ts`, `web/src/lib/compose/form.test.ts`

**Interfaces:**
- Consumes: `ServiceConfig` (unchanged).
- Produces:
  - `ServiceForm.source: 'image' | 'build' | 'dockerfile'`; new fields `dockerfileText: string`, `dockerfileAt: { context: string; dockerfile: string } | null` (null = managed layout); `emptyForm()` and `toForm()` set `''` / `null`
  - `export const DOCKERFILE_TEMPLATE` (Global Constraints text); `export const MAX_DOCKERFILE_BYTES = 1 << 20`
  - `export function fromForm(f: ServiceForm, original: ServiceConfig | null, service?: string): ServiceConfig`. For `source === 'dockerfile'`: `build` = `dockerfileAt` (with `dockerfile` only when non-empty), else `{ context: './' + service }`. Throws when managed and `service` is missing or empty. `image` kept when set.
  - `export function defaultImageTag(stack: string, service: string): string` → `stackstead/<stack>:latest` when equal, else `stackstead/<stack>-<service>:latest`
  - `validateForm`: for `dockerfile` source, errors on `dockerfileText` (`Dockerfile is required` when blank after trim; byte length via `TextEncoder` > 1 MiB; NUL); no `image`/`build.context` requirement for that source
  - `mapServerErrors`/`mapPath`: `dockerfile` → `dockerfileText` (also with the `services.<x>.` prefix); `fieldOf('dockerfileText') === 'build'`

- [ ] **Step 1: Write the failing tests** (`form.test.ts`)
  - `fromForm writes the managed build for a written Dockerfile` (`{context: './web'}`, image kept).
  - `fromForm keeps an in-place Dockerfile location` (`dockerfileAt {context: '.', dockerfile: 'Dockerfile.dev'}` → same build).
  - `fromForm throws for a managed Dockerfile without a service name`.
  - `validateForm requires Dockerfile text`, `rejects a Dockerfile over 1 MiB counting UTF-8 bytes` (`'é'.repeat(524289)`), `rejects NUL`, `does not require image for a written Dockerfile`.
  - `mapServerErrors maps dockerfile to dockerfileText` (plain and `services.web.dockerfile` with service `web`).
  - `defaultImageTag` both cases.
  - `toForm and emptyForm start without Dockerfile text`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/lib/compose/form.test.ts` → FAIL `DOCKERFILE_TEMPLATE is not exported`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same, then `make ui-check` (the new union member must type-check in `ServiceForm.tsx` and `ServiceCard.tsx`; add exhaustive branches only where tsc requires them, rendering `dockerfile` like `build` until T10).
- [ ] **Step 5: Commit** `feat(web): model a written Dockerfile as a service source` (why: create, add and edit share one form model, so the managed build layout is decided in one place), with the trailer.

---

### Task 9: Dockerfile highlighting in the lazy editor

**Model:** Haiku — small, fully specified change in one module plus a bundle assertion.

**Files:**
- Modify: `web/package.json` (`"@codemirror/legacy-modes": "6.5.4"`), `web/package-lock.json` (via `npm install`)
- Modify: `web/src/lib/stacks/cm.ts`, `web/src/lib/stacks/cm.test.ts`, `web/src/app/stacks/YamlEditor.tsx`, `web/src/app/stacks/DiffView.tsx`, `web/src/app/stacks/yamlEditor.test.tsx`, `web/src/bundle.test.ts`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `EditorOptions.language?: 'yaml' | 'dockerfile'` and `DiffOptions.language?` (default `'yaml'`); `'dockerfile'` uses `StreamLanguage.define(dockerFile)` from `@codemirror/legacy-modes/mode/dockerfile`, imported **only** in `cm.ts`; highlight style gains `tags.keyword` → `var(--info)`
  - `YamlEditorProps.language?` and `DiffView` prop `language?` passed through

- [ ] **Step 1: Write the failing tests**
  - `cm.test.ts`: `createEditor with language dockerfile tags the content as dockerfile` (`.cm-content` has `data-language="dockerfile"`); default stays `yaml`.
  - `yamlEditor.test.tsx`: `passes the language to CodeMirror` (after the lazy load, `data-language` is `dockerfile`).
  - `bundle.test.ts` (build-shape block): `keeps the Dockerfile mode in the lazy cm chunk`: the string `stopsignal` (from the mode's instruction list) appears in the `cm` lazy chunk and in no eager file.
- [ ] **Step 2: Run** `cd web && npx vitest run src/lib/stacks/cm.test.ts src/app/stacks/yamlEditor.test.tsx` → FAIL (`data-language` is `yaml`).
- [ ] **Step 3: Implement**; `npm install` after editing `package.json`.
- [ ] **Step 4: Run** the same; then `make ui-test` (builds first, so the bundle test runs) and `make ui-check`.
- [ ] **Step 5: Commit** `feat(web): highlight Dockerfiles in the lazily loaded editor` (why: the Dockerfile editor reuses CodeMirror without adding to the eager bundle), with the trailer.

---

### Task 10: "Write a Dockerfile" in the shared service form

**Model:** Sonnet — UI on the shared form used by three screens, with accessibility names other tests depend on.

**Files:**
- Modify: `web/src/app/stacks/ServiceForm.tsx`, `ServiceForm.module.css`, `serviceForm.test.tsx`
- Modify (label only): `web/src/app/create/stackCreate.test.tsx`, `web/src/app/create/stackCreate.live.test.tsx` (`From Dockerfile` → `From Existing build`)

**Interfaces:**
- Consumes: T8 form fields, `DOCKERFILE_TEMPLATE`; T9 `YamlEditor` `language`.
- Produces:
  - Source radiogroup with three options, accessible names `From Image`, `From Existing build`, `Write a Dockerfile`
  - Selecting `Write a Dockerfile` patches `{ source: 'dockerfile', dockerfileText: text || DOCKERFILE_TEMPLATE }` and keeps `dockerfileAt` as is. The section shows `YamlEditor` (`language="dockerfile"`, aria-label `Dockerfile for {name or "this service"}`, error `shown.dockerfileText`) and the Image tag field. No context or Dockerfile path fields.
  - New props: `onSourceChange?: (source: ServiceFormValue['source']) => ServiceFormValue | void` (lets Edit prefill the image tag, decision 11); `readOnlyHint?: string` (replaces `RO_HINT` text; create passes the Global Constraints hint)
  - `export function fieldLabel(path: string, source: ServiceFormValue['source']): string` (today's `labelOf`, exported; `dockerfileText` → `Dockerfile`; `image` → `Image tag` for both build sources)

- [ ] **Step 1: Write the failing tests** (`serviceForm.test.tsx`)
  - `offers three sources` (the three radio names).
  - `Write a Dockerfile shows the editor prefilled with the template` (`onChange` receives `source: 'dockerfile'` and the template; the textbox `Dockerfile for web` is present).
  - `keeps typed Dockerfile text when switching away and back`.
  - `shows a Dockerfile error and counts it in the summary` (`errors.dockerfileText` → summary names `Dockerfile`).
  - `uses the readOnlyHint for read-only groups`.
  - `fieldLabel names dockerfileText`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/stacks/serviceForm.test.tsx` → FAIL (no `Write a Dockerfile` radio).
- [ ] **Step 3: Implement**; update the label in the two create test files.
- [ ] **Step 4: Run** `cd web && npx vitest run src/app/stacks src/app/create src/app/detail`, then `make ui-check`.
- [ ] **Step 5: Commit** `feat(web): let any service form write its own Dockerfile` (why: a one-off image should not need a file written outside Stackstead), with the trailer.

---

### Task 11: YAML ↔ cards conversion

**Model:** Sonnet — the no-information-loss rules are the core of the YAML feature.

**Files:**
- Modify: `web/package.json` (`"yaml": "2.9.1"`), `web/package-lock.json`
- Create: `web/src/lib/compose/stackYaml.ts`, `web/src/lib/compose/stackYaml.test.ts`
- Modify: `web/src/bundle.test.ts`

**Interfaces:**
- Consumes: T8 `ServiceForm`, `emptyForm`, `fromForm`, `MAX_SERVICES` value 20 (pass as a parameter or duplicate as a constant; do not import from `app/`).
- Produces:
  - `export type Extra = Record<string, unknown>`
  - `export interface StackCard { name: string; form: ServiceForm; extra: Extra }`
  - `export function splitService(name: string, value: unknown, dockerfiles: Record<string, string>): { form: ServiceForm; extra: Extra }`: decision 13 per key; groups moved to `extra` are added to `form.readOnly` (`image`, `build`, `ports`, `volumes`, `environment`, `networks`, `network_mode`, `limits`, `restart`, `healthcheck`); `build` exactly `{context: './<name>'}` or `'./<name>'` with `dockerfiles[name]` set → `source: 'dockerfile'`, `dockerfileText` from the map
  - `export function joinService(card: StackCard): Record<string, unknown>`: `fromForm(card.form, {limits: {}}, card.name)` converted to compose keys in the server's order (`image, build, restart, ports, volumes, environment, networks, network_mode, deploy, healthcheck`; env as a mapping; networks as a list unless an IP is set; build as a scalar unless `dockerfile` is set), then deep-merged with `extra` (form wins at leaves, extra-only keys appended)
  - `export function stackToYaml(cards: StackCard[], extra: Extra): string`: `services:` first, then `extra` keys in order; `yaml.stringify` with `indent: 2`, `lineWidth: 0`; port strings double-quoted; an empty service renders `{}`
  - `export type ParseResult = { ok: true; name?: string; cards: StackCard[]; extra: Extra } | { ok: false; message: string; line?: number }`
  - `export function yamlToStack(text: string, dockerfiles: Record<string, string>, maxServices: number): ParseResult`: `parseDocument(text, { merge: true, uniqueKeys: true })`. The first error gives `message` and `line` (`linePos[0].line`). A non-mapping root or missing/non-mapping `services` → `A stack needs a services: section`. More than `maxServices` → `A stack can have at most 20 services.`. `name` is returned separately and never put in `extra`. A `null` service value counts as `{}`.
  - `export function extraCount(extra: Extra): number` (top-level keys)

- [ ] **Step 1: Write the failing tests** (`stackYaml.test.ts`)
  - `round trip keeps every form field`: a card with image, ports, volumes, env, networks with an IP, network mode, cpus/memory, restart and a healthcheck → `yamlToStack(stackToYaml(…))` cards deep-equal (form and empty extra).
  - `round trip keeps unknown keys per service and top-level`: `labels`, `logging`, `command`, `depends_on`; top `networks`, `volumes`, `secrets`, `configs`, `x-common` → second `stackToYaml` output parses deep-equal to the first.
  - `round trip keeps values the form cannot hold` (Review Focus 2): the five inputs listed there each land in `extra` (or `deploy` split) and the parsed documents are deep-equal; `form.readOnly` has the matching group.
  - `deploy splits limits from the rest`.
  - `numeric ports and env values become strings` (`ports: [80]` → `['80']`; `PORT: 8080` → `'8080'`).
  - `anchors and merge keys are expanded` (`x-base: &b {restart: always}`, `<<: *b`) → form restart `always`.
  - `syntax error reports message and line` (bad indent on line 3 → `line === 3`).
  - `missing services is refused` (`{}`, `services: []`, a scalar root).
  - `more than 20 services is refused`.
  - `name feeds the result, not extra`.
  - `a managed build with Dockerfile text becomes Write a Dockerfile` and `without text becomes Existing build`.
  - `extraCount counts top-level keys`.
  - `bundle.test.ts`: `keeps the YAML library out of the eager entry`: `Implicit map keys need to be followed by map values` and `A stack needs a services: section` occur in lazy chunks only.
- [ ] **Step 2: Run** `cd web && npx vitest run src/lib/compose/stackYaml.test.ts` → FAIL `Cannot find module './stackYaml'`.
- [ ] **Step 3: Implement**; `npm install` after editing `package.json`.
- [ ] **Step 4: Run** the same; `make ui-test` (bundle checks run after the build); `make ui-check`.
- [ ] **Step 5: Commit** `feat(web): convert a whole compose document to service cards and back` (why: YAML mode must never drop a setting that changes how the stack runs, so unmodelled keys ride along as data), with the trailer.

---

### Task 12: New stack screen — Form / YAML switch and Dockerfiles on create

**Model:** Sonnet — state machine across two modes with async submit and error routing.

**Files:**
- Modify: `web/src/app/create/useCreateForm.ts`, `StackCreateScreen.tsx`, `StackCreateScreen.module.css`, `ServiceCard.tsx`, `ComposePreview.tsx`
- Create: `web/src/app/create/YamlMode.tsx`, `web/src/app/create/stackCreate.yaml.test.tsx`
- Modify: `web/src/app/create/stackCreate.test.tsx`, `web/src/lib/stacks/createPreview.ts`, `createPreview.test.ts`

**Interfaces:**
- Consumes: T1 `CreateStackRequest.extra`, `CreateStackService.extra/dockerfile`; T10 `fieldLabel`, `readOnlyHint`; T11 `stackToYaml`, `yamlToStack`, `extraCount`, `StackCard`.
- Produces:
  - `Card` gains `extra: Extra`; `CreateForm` gains `mode: 'form' | 'yaml'`, `setMode(mode): void`, `yamlText: string`, `setYamlText(text): void`, `yamlError: { message: string; line?: number } | null`, `yamlFieldErrors: string[]`, `stackExtra: Extra`
  - Form→YAML: when no form edit happened since the last YAML→Form, the stored text is restored as is (comments kept). Otherwise `stackToYaml(cards, stackExtra)`. Either way the `name → dockerfileText` map is captured (decision 14).
  - YAML→Form: `yamlToStack`; failure → stay in YAML and set `yamlError`. Success → cards rebuilt in YAML order, reusing the `id` of a card with the same name; the stack name field is filled from `name` only when empty; `stackExtra` replaced.
  - Create in YAML mode parses first and then sends the same body as Form mode. Create is disabled with reason `Fix the YAML error first` while the text does not parse (checked on each edit, debounced 150 ms).
  - Body: per card `{service, config: fromForm(form, readOnly.size ? {limits: {}} : null, name), extra?, dockerfile?: {content: dockerfileText}}`; `extra` included only when non-empty, top `extra` likewise.
  - Errors after create: Form mode as today; YAML mode → `yamlFieldErrors` lines `"{service}: {fieldLabel(path)}: {message}"`, shown as a list above the editor (`role="alert"`).
  - `ServiceCard` shows `extraCount` as `1 more setting from YAML` / `{n} more settings from YAML` next to the summary; summary for `dockerfile` source is `Dockerfile`.
  - `ComposePreview` (Form mode side panel) shows `stackToYaml(cards, stackExtra)`; `previewYaml` and its tests are removed; `isCreateDirty` also counts non-empty `stackExtra`, any card `extra`, and edited YAML text.
  - `YamlMode.tsx`: the YAML editor (`YamlEditor`, aria-label `Stack compose file`), the error line `Line {n}: {message}` (`data-testid="yaml-error"`; Go to line moves the caret), and the field-error list.

- [ ] **Step 1: Write the failing tests** (`stackCreate.yaml.test.tsx`, fake fetch as in `createTestKit.tsx`)
  - `switching to YAML shows the cards as one document` (`Editor mode` → `YAML`; textbox `Stack compose file` contains `services:` and `image: nginx:1.27`).
  - `YAML to Form makes one card per service in order` (type a 3-service document → three cards named in order; removing one in YAML removes its card).
  - `a syntax error blocks switching and Create and shows the line` (`Line 2:`; `Form` radio stays unchecked; Create shows the disabled reason).
  - `missing services and 21 services are refused with the copy`.
  - `unknown keys show a settings note and reach the request` (`labels` + `logging` → `2 more settings from YAML`; the POST body has `services[0].extra.labels` and top `extra.volumes`).
  - `returning to YAML without form edits keeps comments`; `a form edit regenerates the YAML without comments`.
  - `a long-syntax ports group is read-only on the card with the YAML hint`.
  - `server field errors in YAML mode are listed by service and field` (400 `services[1].ports[0]` → `db: Port 1: …`).
  - `stackCreate.test.tsx`: `Write a Dockerfile sends the managed build and the content` (body `config.build = {context: './web'}`, `dockerfile.content` = typed text); `renaming the service moves its Dockerfile` (rename to `api` → `./api`); `a Dockerfile error from the server shows on the card` (400 `services[0].dockerfile`).
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/create` → FAIL (no `Editor mode` radiogroup).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same; `make ui-check`; `make ui-test` (the `StackCreate` bundle markers must still pass).
- [ ] **Step 5: Commit** `feat(web): write a new stack as YAML or as cards and switch freely` (why: people paste whole compose files and still want the form's checks and pickers), with the trailer.

---

### Task 13: Add service and Edit service with a Dockerfile

**Model:** Sonnet — the edit store's conflict, preview and changed-group logic must learn a second file.

**Files:**
- Modify: `web/src/lib/stacks/serviceAdd.ts`, `serviceAdd.test.ts`, `web/src/lib/stacks/serviceEdit.ts`, `serviceEdit.test.ts`
- Modify: `web/src/app/detail/AddServicePanel.tsx`, `addServicePanel.test.tsx`, `ServiceEditor.tsx`, `serviceEditor.test.tsx`

**Interfaces:**
- Consumes: T1 `addService(…, dockerfile?)`, `updateService(…, dockerfile?)`, `dockerfile()`; T8 `fromForm(…, service)`, `defaultImageTag`; T10 `onSourceChange`.
- Produces:
  - `ServiceAddDeps.add(service, config, hash, dockerfile?: DockerfileContent)`; the add store sends `{content: dockerfileText}` when `source === 'dockerfile'`; preview never carries it
  - `ServiceEditDeps.update(config, hash, dockerfile?: DockerfileContent)`; `ServiceEditDeps.loadDockerfile?: () => Promise<DockerfileView>`
  - Edit start: when the service has `config.build` (or `readOnly` has `build`) the store loads the view. If it is editable and `exists`: form `source: 'dockerfile'`, `dockerfileText` = content, `dockerfileAt` = the current build, and the loaded hash is kept. Not editable, or the load failed: today's form. The editor keeps working without the view.
  - `GROUPS` gains `['dockerfileText']` labelled `Dockerfile`, field group `build`. Save sends `dockerfile: {content, hash: loadedHash}` when that group changed or the source became `dockerfile`; hash `''` when switching to the managed layout.
  - 409 `Dockerfile changed on disk since it was loaded` takes the store's existing conflict path; Reload re-fetches the view too.
  - Edit switching to `dockerfile` from `image`: `onSourceChange` prefills Image tag with `defaultImageTag(stack, service)` when it was empty or equal to the previous image (decision 11).

- [ ] **Step 1: Write the failing tests**
  - `serviceAdd.test.ts`: `sends the Dockerfile content for a written Dockerfile` and `never sends it to preview`.
  - `serviceEdit.test.ts`: `loads an editable Dockerfile as the source` (view `{editable: true, exists: true, path: 'Dockerfile.dev'}` → form source `dockerfile`, `dockerfileAt` from config); `keeps an outside Dockerfile as Existing build`; `saving a Dockerfile change sends content and the loaded hash`; `switching an image service to a written Dockerfile sends hash ''`; `a Dockerfile conflict enters the conflict phase`.
  - `serviceEditor.test.tsx`: `prefills the image tag when switching to Write a Dockerfile` (`stackstead/web:latest` for stack `web`, service `web`).
  - `addServicePanel.test.tsx`: `adds a service with a written Dockerfile` (POST body `dockerfile.content`, `config.build.context === './api'`).
- [ ] **Step 2: Run** `cd web && npx vitest run src/lib/stacks/serviceAdd.test.ts src/lib/stacks/serviceEdit.test.ts src/app/detail/serviceEditor.test.tsx src/app/detail/addServicePanel.test.tsx` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same; `make ui-check`.
- [ ] **Step 5: Commit** `feat(web): write or edit a Dockerfile when adding or editing a service` (why: the same source choice must exist wherever a service is defined, and edits must not overwrite a file changed on disk), with the trailer.

---

### Task 14: Stack page — Dockerfile tab with Save and Save & rebuild

**Model:** Sonnet — reuses the compose editor store for a second file and chains a save into a tracked operation.

**Files:**
- Create: `web/src/app/detail/DockerfilePanel.tsx`, `DockerfilePanel.module.css`, `dockerfilePanel.test.tsx`
- Modify: `web/src/lib/stacks/detailLocation.ts`, `detailLocation.test.ts` (`ServiceTab` gains `'dockerfile'`, URL `tab=dockerfile`)
- Modify: `web/src/app/detail/ServicePane.tsx` (tab `Dockerfile` shown when `service.config.build` or `service.readOnly` includes `build`), `StackDetailScreen.tsx` (renders the panel for that tab), `OperationsList.tsx`, `OperationPanel.tsx` (show `docker compose up --build {service}` when `op.service` is set)

**Interfaces:**
- Consumes: T1 `dockerfile()`, `saveDockerfile()`, `upService()`, `Operation.service`; T9 `YamlEditor`/`DiffView` `language`; `createComposeEditor` from `web/src/lib/stacks/compose.ts` (reused with `load = api.dockerfile → {content, hash}` and `save = saveDockerfile`); `useServer()` (`trackOperation`, `refreshRunningOps`).
- Produces: `DockerfilePanel({ stackName, service }: { stackName: string; service: string })`:
  - Editable: toolbar with `path`, `Preview changes` (diff with `language="dockerfile"`), `Save`, `Save & rebuild`, and ⌘S/Ctrl+S. 409 shows the compose editor's Reload / Discard pair.
  - `Save & rebuild` saves, then `api.upService(stackName, service)` → `trackOperation(op)`, `refreshRunningOps()`, and navigates to the operations tab with `op` selected, as `useCreateForm` does after Create.
  - Not editable: read-only editor with the content (when present), the outside note (verbatim) or `This Dockerfile can't be edited here: {reason}`, and no Save buttons.
  - `exists === false` and editable: empty editor prefilled with `DOCKERFILE_TEMPLATE`, Save creates the file (hash `''`).

- [ ] **Step 1: Write the failing tests** (`dockerfilePanel.test.tsx`, fake fetch)
  - `shows the Dockerfile tab only for services with a build` (ServicePane with image-only vs build).
  - `loads and saves with the loaded hash` (PUT body `{content, hash}`).
  - `Save & rebuild saves then starts the scoped up` (PUT, then POST `…/services/web/actions/up` in that order; tracked operation id appears in the URL).
  - `a conflict offers Reload and Discard` (409).
  - `an outside Dockerfile is read-only with the note` (no `Save` button; note text verbatim).
  - `a not-editable Dockerfile names the reason` (`uses variables`).
  - `detailLocation round-trips tab=dockerfile`.
  - `operations list names the rebuilt service` (`service: 'web'` → `docker compose up --build web`).
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/detail/dockerfilePanel.test.tsx src/lib/stacks/detailLocation.test.ts` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same, then `cd web && npx vitest run src/app/detail`; `make ui-check`.
- [ ] **Step 5: Commit** `feat(web): edit, save and rebuild a service's Dockerfile from the stack page` (why: an image written in Stackstead must stay maintainable there after the stack exists), with the trailer.

---

### Task 15: End-to-end with a real build

**Model:** Sonnet — real Docker orchestration with cleanup.

**Files:**
- Modify: `cmd/stackstead/e2e_test.go`

**Interfaces:**
- Consumes: everything above; existing helpers `c.expect`, `c.do`, `projectContainers`, `dockerOutput`, and the `wait` pattern from `dockerStack`.
- Produces: `func dockerfileStack(t *testing.T, c *client)` run as `t.Run("dockerfile stack", …)` next to `dockerStack` when Docker is available.

- [ ] **Step 1: Write the failing test** `dockerfileStack`
  - Unique name `e2e-df-<hex>`; cleanup registered first (API delete with volumes, then remove containers, network and the image `stackstead/<name>:latest`).
  - `POST /api/stacks`: service `app`, `config: {build: {context: "./app"}, limits: {}}`, `dockerfile: {content: "FROM busybox:1.36\nCMD [\"sleep\", \"3600\"]\n"}`, plus `extra: {labels: {"e2e": "1"}}` → 201.
  - `GET …/services/app/dockerfile` → `editable: true`, `path: "app/Dockerfile"`, content as sent.
  - `POST …/actions/up` → wait succeeded → the container is running and has label `e2e=1` (`docker inspect`).
  - `PUT …/services/app/dockerfile` with `CMD ["sleep", "3601"]` and the hash → 200; a second PUT with the old hash → 409.
  - `POST …/services/app/actions/up` → 202 with `"service":"app"` → wait succeeded → the container's `Config.Cmd` contains `3601`.
- [ ] **Step 2: Run** `make ui && make e2e` → FAIL before T5–T7 are merged (`unknown field "dockerfile"` / 404).
- [ ] **Step 3: Implement** the test function only; the features already exist.
- [ ] **Step 4: Run** `make e2e` → PASS (or the documented skip without Docker). Then `go vet ./...` and `test -z "$(gofmt -l .)"`.
- [ ] **Step 5: Commit** `test(e2e): build, edit and rebuild a stack from a written Dockerfile` (why: only a real compose build proves the managed layout and the scoped up work together), with the trailer.

---

### Task 16: Documentation

**Model:** Haiku — prose from the spec and the routes above; no decisions.

**Files:** `docs/api.md`, `docs/security.md`, `README.md`

- [ ] **Step 1: Write**
  - `docs/api.md`: the overview table's create row becomes `{name, services: [{service, config, extra?, dockerfile?}], extra?}` (extras are compose keys the form does not model; `services`/`name` refused). The add and update rows gain `dockerfile?: {content, hash}` with decision 4's hash rules. Add rows for `GET`/`PUT …/services/{service}/dockerfile` (response fields, `path` rule, statuses) and `POST …/services/{service}/actions/up`. The operation description gains `service`. Note the 1 MiB Dockerfile cap and the 32/8 MiB body caps.
  - `docs/security.md`: one paragraph under "The short version" or a new "Dockerfiles" section. Stackstead writes Dockerfiles only inside a stack's own folder, checked after resolving symlinks at write time, and never through a symlinked file. Dockerfiles elsewhere are read-only and shown only inside the allowed directories. A Dockerfile runs commands at build time just as a compose file does, so the trust model is unchanged.
  - `README.md`: the "Forms instead of YAML" / "Dockerfile first" lines gain: write a new stack as YAML or as cards and switch both ways; write a service's Dockerfile in Stackstead and Save & rebuild it from the stack page.
- [ ] **Step 2: Verify** every documented route exists: `grep -n 'dockerfile\|services/{service}/actions' internal/api/*.go` matches each new row; `make check` passes.
- [ ] **Step 3: Commit** `docs: describe YAML create mode, managed Dockerfiles and their folder rule`, with the trailer.

---

## Self-review

1. **Spec coverage.**
   - §1 success criteria: Form/YAML switch → T12. Form→YAML → T11, T12. YAML→Form order and add/remove → T11, T12. Nothing lost → T3, T5 server side; T11 client side. "N more settings" → T12. Invalid YAML blocks the switch and Create → T11, T12. Three sources → T10. Write on create → T5. Edit/save/rebuild on the stack page → T7, T14. Outside is view-only → T4, T14. Unchanged without the features → T5 `TestCreateWithoutExtrasWritesSameBytes`, T6 `TestUpdateWithoutDockerfileUnchanged`, T7 `TestActUpUnchanged`.
   - §4.1–4.3 → T11, T12. §4.4 → T12. §4.4a → T2, T3, T5 (divergence 2 for add/update). §4.5 → T12.
   - §5.1 → T8, T10, T12, T13. §5.2 → T4, T5, T6 (divergences 3, 4, 5). §5.3 → T4, T6, T13. §5.4 → T7, T14 (divergence 6). §5.5 → T6 `TestAddServiceOverwritesLeftoverDockerfile` (the folder survives removal; nothing deletes it).
   - §6 security → T4, T16; connected servers need no work because the routes are plain `/api/stacks/…`. §7 tests → spread as listed; e2e → T15.
2. **Step scan.** Every code step names files, signatures, pinned values and copy. No function bodies. Builder's calls the spec left open are settled here: route names, the template text, component structure (`YamlMode`, `DockerfilePanel`) and the parser choice. CSS stays the implementer's call.
3. **Type consistency.**
   - Go: `DockerfileContent`, `DockerfileView`, `Operation.Service`, `Extra`/`ParseExtra`/`NewStack`, `BuildOf`/`EffectiveService`, `ErrOutsideStack`/`ErrNoBuild`/`ErrDockerfileConflict`/`NotEditableError`, `DockerfileWrite`, `ActService`/`StartService`, `decodeJSONLimit` are spelled identically in every task.
   - TS: `dockerfileText`/`dockerfileAt`/`DOCKERFILE_TEMPLATE`/`defaultImageTag`/`fieldLabel`, `upService`/`saveDockerfile`/`dockerfile`, and `stackToYaml`/`yamlToStack`/`extraCount`/`StackCard` likewise.
   - Field paths: `services[i].dockerfile` (create), `dockerfile` (add/update), `content` (PUT) are all mapped by T8's `mapPath` (`dockerfile` → `dockerfileText`). `content` only appears on the stack-page panel, which shows it inline.
4. **Review Focus.** Five lines above, each with its test named in the owning task.
5. **Proportion.** The plan is several times the spec's length for 16 tasks across two lanes and contains no code bodies. The bulk is the pinned wire contract and the test lists that let lanes run in parallel.
