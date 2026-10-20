# Stackstead YAML create mode and managed Dockerfiles: Design

**Date:** 2026-10-20
**Status:** Draft, awaiting review. Adds (A) a whole-stack YAML mode to the New stack screen that converts both ways with the service cards, and (B) Dockerfiles written in Stackstead, stored in the stack's own folder, and editable and rebuildable after the stack exists. Builds on the create screen (`web/src/app/create/*`), the shared service form (`web/src/app/stacks/ServiceForm.tsx`, `web/src/lib/compose/form.ts`), the compose editor (`web/src/app/detail/ComposeEditor.tsx`, `web/src/app/stacks/YamlEditor.tsx`) and the stacks service (`internal/stacks`, `internal/composefile`). Implemented by one plan (number assigned when the plan is written).

## 1. Purpose

Today a new stack can only be described through form cards; the YAML beside them is a read-only preview. People who already have a compose file in their head (or in their clipboard) want to type or paste it, and still be able to switch to the cards to adjust it. Separately, a service that builds its own image can only point at a build context and Dockerfile that already exist on the host, so a one-off image means leaving Stackstead to write a Dockerfile by hand. The user wants to write that Dockerfile inside Stackstead, and to change and rebuild it later.

### Success criteria
- On New stack, a **Form / YAML** switch shows the whole stack either as cards or as one editable compose YAML document.
- Form → YAML shows the YAML generated from the cards (what the preview shows today), now editable.
- YAML → Form turns every service under `services:` into its own card, in YAML order, with every field the form supports filled in. Services added or removed in YAML appear or disappear as cards.
- Nothing that affects how the stack runs is lost by switching modes: compose keys the form has no field for (per service, e.g. `labels`, `deploy`, `logging`; and top-level, e.g. `networks:`, `volumes:`, `secrets:`, `configs:`, `x-*`) are carried through and come back in YAML and in the created stack. Only comments, anchors/aliases (expanded), key order outside the form's own order, and formatting may be lost. *(user)*
- A card carrying settings the form can't show says so, e.g. "3 more settings from YAML".
- Invalid YAML blocks switching to Form and blocks Create, with the parser's message and line shown in the editor.
- A service's Source can be **Image**, **Existing build** (today's context + Dockerfile pickers) or **Write a Dockerfile**, which opens a Dockerfile text editor on the card. *(user: focus on the Dockerfile itself)*
- Creating the stack writes each written Dockerfile into the new stack's folder and points that service's `build:` at it; `up` builds it (it already runs `up --build`).
- After creation, the stack page lets the user open, edit, save, and **Save & rebuild** any Dockerfile that lives inside the stack's own folder. *(user)*
- Dockerfiles outside the stack's folder can never be written through Stackstead; they are view-only. *(user)*
- With neither feature used, create, add service and edit service behave exactly as today.

## 2. Scope

### In
- YAML mode on the New stack screen with two-way conversion (§4).
- "Write a Dockerfile" source in the shared service form — therefore on New stack, Add service and Edit service (§5.1).
- Backend: create/add/update accept Dockerfile contents for services whose build uses a managed Dockerfile, written safely inside the stack folder (§5.2–5.3).
- Stack page: per-service Dockerfile view/edit/save/save-and-rebuild (§5.4).
- API, docs (`docs/api.md`, `docs/security.md` one paragraph, README feature line).

### Out
- Other files in the build context (scripts, app code, config files). The context is the service's folder; the user can only write the Dockerfile. *(user: "focus mainly on the Docker file itself")*
- A raw-YAML mode for Add service / Edit service on an existing stack (the stack page already has the whole-file compose editor).
- Build args, target, cache options in the form (they are carried as "more settings" if written in YAML).
- Version history for Dockerfiles (the config-history spec explicitly excludes Dockerfiles; not implemented anyway).
- Deleting a service's folder/Dockerfile when the service is removed (§5.5).
- Editing Dockerfiles outside the stack folder.

## 3. Key decisions

The TypeSafe skill is an SDK for a hosted judgment API and cannot score decisions locally, so they are argued in writing as in earlier specs. *(user)* marks decisions the user made.

| Decision | Choice | Reason |
|---|---|---|
| YAML granularity *(user)* | Whole stack, one document | "write basically the whole new stack". |
| What may be lost on switching *(user)* | Only comments, anchors, formatting, cosmetic key order | Anything that changes how the stack runs must survive. |
| Unknown compose keys | Carried per service and per stack as opaque YAML, round-tripped | Required by the row above; keeps the form simple. |
| Where conversion runs | Client-side in the SPA, using the YAML library already used by `previewYaml` (or one added for parsing if none parses) | Switching must be instant and work before the stack exists; the server still validates on Create with `compose config`. *(my call)* |
| Dockerfile location | `<stack>/<service>/Dockerfile`; `build: { context: ./<service> }` (no `dockerfile:` key needed) | Each service gets its own context and file; two services never collide; the context is a real directory compose understands. *(my call)* |
| Editable files | Only Dockerfiles whose resolved path is inside the stack folder (symlinks resolved; must stay inside) | *(user)* "Only the Docker file inside the stack's own folder can be edited." |
| Dockerfile syntax highlighting | `@codemirror/legacy-modes` dockerfile mode, lazy-loaded with the rest of CodeMirror | Same project as the editor already in use; small; no new vendor. *(my call — new dependency)* |
| Size cap *(user)* | 1 MiB per Dockerfile | User raised it from 256 KiB; still bounds memory. |
| Rebuild | "Save & rebuild" = save, then the existing `build` action for that service, then `up` for that service | Reuses existing operations and their progress streams. *(my call)* |

## 4. YAML mode on New stack

### 4.1 Model
The create screen's state gains a mode (`form` | `yaml`) and a stack-level "extra" bag:
- Per card: today's `ServiceForm` plus `extra` — the service's compose keys the form does not model, kept as plain data (parsed YAML values).
- Per stack: `extra` — every top-level key other than `services:` (and other than `name:` if present, which feeds the stack name field when empty).

### 4.2 Form → YAML
Generate the document from cards + extras: each service = form fields (as `fromForm` does today) merged with that card's `extra`; then top-level extras. The editor opens with this text. If the user returns to YAML without having edited in form mode since the last YAML→Form, the previous YAML text is shown unchanged (comments preserved in that case). *(my call)*

### 4.3 YAML → Form
1. Parse. On a syntax error: stay in YAML, show the message and line (as `ComposeEditor` does), block the switch.
2. Require a mapping with a `services:` mapping; otherwise show "A stack needs a services: section" and block the switch.
3. For each service in order: map known keys into `ServiceForm` (the inverse of `fromForm`); everything else → the card's `extra`. Values the form can't represent in its own field shape (e.g. a long-syntax port the form only supports in short form) go to `extra` for that key rather than being reshaped. *(my call; the builder decides the exact per-field rules, but must never lose information)*
4. More than the create screen's 20-service cap → stay in YAML with a message.
5. A service whose `build.context` is `./<service>` and that had a written Dockerfile in form mode keeps its Dockerfile text (§5.1). A service newly typed in YAML with such a context gets Source "Existing build" unless it already has Dockerfile text.

### 4.4 Create
Create submits the same request in both modes (cards + extras), so the backend path, validation and `compose config` verification are shared. Invalid documents can't be submitted from YAML mode (Create disabled with the parse error shown).

### 4.4a How extras reach the server, and which checks apply *(my call)*
- The create/add/update requests gain optional `extra` per service and `extra` for the stack, as JSON values (the parsed YAML). The server renders the form fields, then merges the extras into the YAML node tree.
- **Collisions:** for a key both sides set, maps are deep-merged and the form's value wins at each leaf the form models (e.g. form CPU/memory under `deploy.resources.limits`, everything else under `deploy` from the extra). Top-level `networks:`/`volumes:` merge by name; a definition the user wrote wins over one the server would add automatically.
- **Every existing check runs on the merged result, not only on the form fields.** The server reads the merged document back into its model before the checks: build contexts (including a long-form `build:` from extras, e.g. with `args:`) go through the same allowed-roots / stack-folder confinement as today; port and IP conflict checks include long-syntax ports from extras; then `compose config` as today. YAML mode must never weaken a rule the form enforces.

### 4.5 Errors after Create
Server errors that today map to card fields (field errors) are shown on the card in form mode; in YAML mode they are shown as a list above the editor naming the service and field. *(my call)*

## 5. Managed Dockerfiles

### 5.1 Form
Source gains "Write a Dockerfile". Choosing it:
- shows a Dockerfile editor (CodeMirror, Dockerfile highlighting) prefilled with a minimal template (`FROM alpine:3.20` + a comment), *(my call)*
- sets the service's build to the managed layout (`context: ./<service>`), hidden from the user,
- keeps today's image-name default (`stackstead/<stack>[-<svc>]:latest`).
Renaming a service on New stack moves its Dockerfile with it. On Add service / Edit service the same choice is offered; on Edit service of a service that already has a managed Dockerfile, the editor loads the current file.

### 5.2 Writing at create / add / update
The create, add-service and update-service requests gain an optional `dockerfiles: { <service>: <content> }`. The backend:
- accepts content only for a service whose Dockerfile is editable under §5.3 (or the new managed path `<stack>/<service>/Dockerfile`),
- rejects content larger than 1 MiB or containing NUL bytes (field error on that service's Dockerfile),
- writes `<stack>/<service>/Dockerfile` atomically, creating the folder 0755 if missing, never following a symlink out of the stack folder (open with no-follow, verify the parent resolves inside the stack folder),
- on create, does this inside the same "claim directory → write → `compose config` → roll back on failure" sequence that `Create` uses today, so a failed create leaves nothing behind,
- on add/update, writes the Dockerfile only after the compose change is verified, and rolls back the Dockerfile if the compose write fails. *(my call)*

### 5.3 Which Dockerfiles are editable, and path safety *(my call)*
- **Editable** = the service's resolved Dockerfile (build context joined with `dockerfile:` or `Dockerfile`) is inside the stack folder after symlink resolution. That includes existing layouts such as context `.` with `Dockerfile`, or a custom `dockerfile:` name — not only `./<service>`.
- Resolution reads the stack's compose file with the existing `composefile` reader, no `docker compose` call. If the context or `dockerfile` value contains `${…}` interpolation, comes from `extends`/`include`, is a remote context (git/URL), or the service uses `dockerfile_inline`, the Dockerfile is shown as **not editable** with a reason ("uses variables", "remote build context", "inline Dockerfile"); it is never guessed.
- Edit service on a service whose Dockerfile is editable shows Source "Write a Dockerfile" with the current file loaded, in place (its context is not moved). Switching an image service, or a build service whose Dockerfile is outside the folder, to "Write a Dockerfile" uses the managed layout `./<service>`; any old file is left untouched.
- The `dockerfiles` request field may write only to a service's editable Dockerfile path (as resolved after the compose change) or, for a new managed service, `<stack>/<service>/Dockerfile`.

A new helper (in `internal/stacks` or `internal/fsguard`) resolves "the Dockerfile of service X in stack S": it reads the service's effective build context and `dockerfile` from the compose model, resolves symlinks, and returns the path only if it is inside the stack folder; otherwise `ErrOutsideStack`. Every read and write route uses it. Imported (external) stacks follow the same rule: their own folder is the stack folder.

### 5.4 Stack page
- The service detail (or the service list) shows a **Dockerfile** entry for every service with a build. Inside the stack folder: an editor with the compose editor's behaviour (diff view of unsaved changes, hash-checked save → 409 on conflict). Outside: read-only view with the text "This Dockerfile is outside the stack's folder and can't be edited here."
- **Save** writes the file. **Save & rebuild** saves, then runs `docker compose up -d --build <service>` for that one service as a new service-scoped operation (with the usual progress stream); other services are not rebuilt or recreated. *(my call: `up` already builds, so no separate build step; adds a service-scoped variant of the existing `up` action — API route, operation plumbing; CLI support is out of scope.)*
- Routes (shape; names are the builder's call): `GET /api/stacks/{name}/services/{service}/dockerfile` → `{content, hash, editable, path}` (path relative to the stack folder when inside, absolute otherwise); `PUT …/dockerfile` with `{content, hash}`; rebuild reuses existing action routes.

### 5.5 Removing a service
Removing a service leaves its folder and Dockerfile on disk. *(my call: nothing is deleted without an explicit request; a later feature can offer cleanup.)*

## 6. Security
- Writes are confined to the stack folder after symlink resolution; the check runs at write time, not only at request validation.
- A Dockerfile can run arbitrary commands at build time, but so can any compose file Stackstead already runs; the trust model is unchanged (Stackstead access is root-equivalent). `docs/security.md` gets one paragraph saying Stackstead now writes Dockerfiles only inside stack folders.
- Connected servers: Dockerfile routes are normal `/api/stacks/…` routes and work through forwarding on a peer, subject to the peer's own folder rule.

## 7. Testing (shape)
- Go: managed-Dockerfile create (file written, `build` points at it, rollback on `compose config` failure leaves no folder); add/update write and rollback; size/NUL limits; path safety (symlinked service folder pointing outside → refused; `../` contexts → refused; external Dockerfile → read-only); GET/PUT with hash conflict.
- Web: Form→YAML→Form round trip keeps every field and every unknown key (per service and top-level); comments lost only after a form edit; YAML syntax error blocks switching with the line; services added/removed in YAML become cards; 20-service cap; "N more settings" note; Write-a-Dockerfile flow on create, add and edit; stack-page editor read-only for outside paths; Save & rebuild triggers build then up.
- e2e: create a stack with a written Dockerfile and `up` it (`FROM busybox`), edit, Save & rebuild.

## 8. Left to the builder
Exact per-field mapping rules in YAML→Form (as long as nothing is lost), component structure, where the Dockerfile entry sits on the stack page, route names, the template text, and whether the YAML parser on the client is the one already bundled.
