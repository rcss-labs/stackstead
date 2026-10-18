# Stackstead Richer Compose forms — Design

**Date:** 2026-10-18
**Status:** Draft, awaiting review. Extends the structured service editor with `depends_on` (with conditions), `env_file`, `secrets` and `profiles`, and lifts the v1 refusal of compose override files. Builds on `2026-10-14-stackstead-v1-design.md` (§4 composefile, §5.3 edit, §6 file integrity), Plan 2 (`2026-10-14-stackstead-02-composefile.md`) and `2026-10-15-stackstead-networks-multiservice-design.md` (multi-service create, add/remove service, `DependentsOf`).

## 1. Purpose

The form editor covers the single-service basics (image/build, ports, volumes, environment, networks, limits, restart, healthcheck). Real multi-service stacks, which Plan 10b made creatable from the UI, also need start order and readiness (`depends_on`), shared configuration files (`env_file`), credentials that do not belong in `environment` (`secrets`), optional services (`profiles`), and per-host overrides (`compose.override.yaml`, extra `-f` files). Today all of them are raw-YAML-only, and a stack with an override file is refused outright (`internal/stacks/service.go` `refuseOverrides`; `docs/api.md` "Override files are not supported yet"; alert kind `override`).

Why v1 refused overrides: Stackstead always runs `docker compose -f <file>`. Passing `-f` turns off compose's automatic merge of `compose.override.{yaml,yml}` / `docker-compose.override.*`, so Stackstead would silently run a different project than `docker compose up` in the same folder, and `up --remove-orphans` would delete the containers of services defined only in the override. The splice editor is also single-file: it locates nodes in one `yaml.Node` tree and verifies an edit against that one file. Refusing was the only safe option until both the compose invocation and the editor knew about the file set. This spec fixes the invocation first (§7) and keeps the editor single-file (§3).

### Success criteria

- In the New stack form, the operator can create `app` + `db` where `app` waits for `db` to be `service_healthy`, gets `env_file: ./app.env` and the secret `db_password`, and an `adminer` service exists only under the profile `debug`. They do this without writing any YAML, and `docker compose up` by hand in that folder behaves the same.
- A hand-written file that already uses these keys opens in the form. Every key the form can represent is editable. Anything else (long-syntax secrets with `uid`/`mode`, `env_file` with `format`, anchors) is read-only and left byte-identical when other fields are saved.
- Saving refuses (field errors on the right card and row): a dependency on an unknown service, a self-dependency, a dependency cycle (the message names the cycle), a required `env_file` that does not exist or lies outside the allowed directories, and a secret reference that has no top-level definition.
- A secret's value is never returned by any API response, log line, operation output or preview diff.
- A stack with `compose.override.yaml` can be imported, edited and started. Stackstead runs exactly the file set `docker compose` would use in that folder, and the `override` alert fires only for ambiguous override sets.
- Every existing stack keeps working unchanged after upgrade, with no file rewritten.

## 2. Scope

### In
- `composefile`: model, read, write and validation for service `depends_on`, `env_file`, `secrets` (references), `profiles`; top-level `secrets:` definitions; read-only detection; style-preserving writes; cycle detection.
- `stacks`: dependency / env_file / secret validation in the save pipeline; secret value storage; file-set resolution for overrides; profile selection for lifecycle actions; removal of the override refusal.
- `composecli`: invocation with several `-f` files and `--profile` flags.
- `envfile` (new package): comment-preserving reader and writer for `env_file` targets, with masked reads and explicit reveal.
- API: extended `ServiceConfig`, new secret and profile routes, `files` and `profiles` on stack detail; `docs/api.md`, `docs/security.md`.
- Web: four new form groups in `ServiceForm`, an env-file contents editor, a Secrets panel, a profile selector next to the lifecycle actions, an override/file indicator on stack detail; New stack sibling-aware dependency picker.
- Tests, integration and E2E smoke extensions.

### Out
- `configs:` (top-level and per service), `extends`, `include:` (top-level). These stay raw-only. A service using `extends` is fully read-only, the same as an anchored service.
- Long-syntax secret targets (`target`, `uid`, `gid`, `mode`) in forms: read-only, raw-editable.
- `env_file` `format: raw`: read-only.
- Swarm-only secret sources (`external`, `name`): read-only, raw-editable.
- Renaming services (unchanged from 10b), editing `x-*` extension blocks.
- Stack-level `.env` (interpolation variables) editing. Untouched, and it still applies through compose.
- CLI commands for secrets or profiles.

## 3. Key decisions

The TypeSafe skill named in the global rules cannot score decisions in this environment (the installed `typesafe-ai` skill is an AI-feature SDK, not a decision scorer), so decisions are argued here. Rows marked *user* were put to the user with options; the rest follow from existing code or spec conventions.

| Decision | Choice | Reason |
|---|---|---|
| Editing technique | Same splice editor + semantic verification as Plan 2; new fields are new `fieldReader`s and `UpdateService` field rows | Follows from Plan 2: preserves comments, order, unknown keys; every edit is verified by `verifyEdits`. |
| Unrepresentable shapes | Read-only per field (`Service.ReadOnly`), never partially edited | Existing rule (v1 §4): a field the model cannot hold keeps its zero value in `Config` and is raw-only. |
| Write style | Keep the file's existing shape (short list vs long map for `depends_on`, scalar vs list for `env_file`); new keys use the shortest form that holds the value | Same as `environment` list/map and `networks` list/map today (`asList`, `asMap` in `update.go`). |
| Validation placement | Pure checks in `composefile` (`ErrInvalidConfig`); cross-service and filesystem checks in `stacks` (`ValidationError` with field paths) | Same split as `ValidateConfig` vs `checkChanges`/`prepareConfig` today. |
| Path confinement (`env_file`, secret `file`) | Relative paths resolve against the stack folder; anything that resolves outside it must be inside `allowed_roots` (`fsguard.Resolve`) | Same rule as build contexts (`prepareConfig`). |
| Final authority | `docker compose config` on the candidate (with the full file set) still runs before every save | v1 §5.3; Stackstead's checks exist to give field-level errors, not to replace compose. |
| Which file the form writes (*user*) | Always the base compose file. A field that any override/extra file also sets for that service is read-only in the form, labelled with the file that sets it, and editable in that file's raw editor | User decision (vs per-file forms; vs write-to-winning-file). Keeps the splice editor single-file and every save's target obvious. |
| Choosing the file set (*user*) | An ordered per-stack list of extra files in the registry. An empty list means compose's own default (base + `compose.override.{yaml,yml}` when the base has a default name) | User decision (vs `COMPOSE_FILE` in `.env`; vs auto-override only). Explicit, and it covers the `-f compose.prod.yaml` layouts. With the default set, hand-run `docker compose` matches exactly. |
| Secret values (*user*) | A write-only API writes `<stack dir>/secrets/<name>` (dir 0700, file 0600) and compose references it as `file: ./secrets/<name>`. The value is never returned | User decision (vs reference-only; vs encrypted in DB). No lock-in: the stack still runs by hand. Plaintext on disk is what compose itself requires. |
| Active profiles (*user*) | A per-stack set in the registry, passed as `--profile` to up, pull, build and restart. `down` (and stack removal) uses `--profile '*'` | User decision (vs per-action dialog; vs `COMPOSE_PROFILES` in `.env`). Repeatable from the UI, the CLI and the list's quick actions. Down must never leave profiled containers running. |
| `service_healthy` without a compose healthcheck (*user*) | Non-blocking warning on the dependency row; the save is allowed | User decision (vs blocking unless the image has a HEALTHCHECK; vs always blocking). An image's own `HEALTHCHECK` makes the dependency valid, and blocking would wrongly refuse working stacks. |
| `env_file` coverage (*user*) | Paths with a `required` toggle, **plus** a key/value editor for the file's contents: values masked by default, explicit reveal, never logged, and a writer that preserves comments | User decision (vs paths only; vs a read-only key list). Removes the last shell step for typical stacks. The writer follows the same rules as the compose editor: splice, then verify (§9). |
| Writable env files (*user*) | Any env file inside the stack folder or `allowed_roots`. When other registered stacks reference the same file, they are listed and confirmation is required before the save (server-enforced) | User decision (vs stack folder only). No shell is needed for shared env files. The confirmation makes the cross-stack effect explicit. |
| Secrets UI (*user*) | A stack-level Secrets panel for top-level definitions, plus a per-service checklist of the secrets the service receives | User decision (vs per-service only with implicit top-level side effects). Mirrors compose's structure, and a shared secret is defined once. |
| API compatibility for new fields (*user*) | Presence semantics for `dependsOn`, `envFile`, `secrets`, `profiles` in structured edits: a missing key keeps the file's value, while `[]`/`null` clears it. Existing fields keep replace semantics | User decision (vs replace-everything; vs a schema version field). An old script or cached UI bundle cannot silently delete the new keys. |
| Recreate after a secret change (*user*) | A new `recreate` lifecycle action (`up -d --force-recreate --no-deps [services]`), offered from the Secrets panel as "Recreate services using this secret" | User decision (vs a note only). Compose never recreates a container for a changed secret file, so without this action a replaced value silently does not apply. |
| `COMPOSE_FILE` / `COMPOSE_PROFILES` in `.env` (*user*) | On import, seed `extra_files` and the active profiles from them, then warn that Stackstead's settings take precedence from now on. At run time Stackstead's own settings always win | User decision (vs ignoring them; vs a warning only). An imported stack keeps running the way it did by hand, and there is one source of truth afterwards. |
| Env-file backups (*user*) | No `.bak` for env-file saves | User decision. Avoids extra plaintext copies of secret values. The atomic write still protects against a failed save. |
| Secret value backups | None either. Switching a secret to an environment source, or removing it, deletes the managed value file after the commit succeeds, and the confirm dialog says so | Follows from the env-file decision (no extra plaintext copies of secrets). Compose `.bak` copies are also gone (config history replaces them), so there is no `.bak` pattern left to follow. |
| Ambiguous overrides | Two default override files present (e.g. `compose.override.yaml` and `compose.override.yml`): the stack is unavailable with a clear message until one is removed or an explicit file list is set | Compose's choice between them is an implementation detail. Guessing would recreate the v1 risk (running a different project than the user expects). |
| Effective values of overridden fields | Shown read-only from `docker compose config --format json` (the authoritative merge), computed on demand | Reimplementing compose's merge rules (ports append, volumes by target, env by key) in Go would drift from compose. |

## 4. Data model (`internal/composefile`)

### 4.1 New fields

`model.go` adds field constants and types. The `Field` vocabulary is also the API's `readOnly` vocabulary, so the strings are the compose keys:

```go
const (
    FieldDependsOn Field = "depends_on"
    FieldEnvFile   Field = "env_file"
    FieldSecrets   Field = "secrets"
    FieldProfiles  Field = "profiles"
)

type ServiceConfig struct {
    // ...existing fields...
    DependsOn []Dependency // file order
    EnvFiles  []EnvFile
    Secrets   []string     // short syntax: names of top-level secrets
    Profiles  []string
}

type Dependency struct {
    Service   string
    Condition string // "service_started" (default), "service_healthy", "service_completed_successfully"
    Restart   bool   // restart: true
    Optional  bool   // required: false
}

type EnvFile struct {
    Path     string
    Optional bool // required: false
}
```

`normalize` maps empty slices to nil and `Condition: ""` to `service_started`, so a form config compares equal to a file config.

### 4.2 Reading (`read.go`)

New `fieldReader`s, in this order after `healthcheck`: `depends_on`, `env_file`, `secrets`, `profiles`.

| Key | Representable (editable) | Read-only when |
|---|---|---|
| `depends_on` | list of scalar names (each `service_started`); mapping of name → mapping with only `condition`, `restart`, `required` scalar keys (missing `condition` = `service_started`) | any other key, a non-scalar value, an unknown condition string, anchors/aliases, merge-key inheritance |
| `env_file` | scalar path; list of scalars; list items that are mappings with only `path` and `required` | `format` or any other key; anchors |
| `secrets` | list of scalar names | any long-syntax item (`source`/`target`/`uid`/`gid`/`mode`); anchors |
| `profiles` | list of scalars | non-list; anchors |

Anchored services, merge-key inheritance and `extends` make every field read-only (existing rule; `extends` is added to the service-level check).

Top-level secrets get a separate typed read:

```go
type SecretDef struct {
    Name        string
    Source      SecretSource // SecretFile | SecretEnvironment
    File        string       // as written (relative to the project dir or absolute)
    Environment string       // variable name
    ReadOnly    bool         // external, name, or any other key; anchors
}
func (f *File) Secrets() ([]SecretDef, error)          // file order
func (f *File) DeclaredProfiles() []string             // union over services, sorted
```

### 4.3 Writing (`update.go`, `services.go`, `build.go`)

- `UpdateService` gets one row per new field in its field table (changed → validate → refuse if read-only → edits), the same as the existing ones.
- `depends_on`: if the current node is a list and every new entry is `service_started`, not `Restart` and not `Optional`, rewrite as a list. Otherwise write the long mapping, with keys in the order `condition`, `restart` (only when true), `required` (only when false). New keys use the list form when possible.
- `env_file`: one required entry with no current node, or a current scalar → scalar. Otherwise a list, where only `Optional` entries use the `{path, required: false}` form.
- `secrets`, `profiles`: `listEdit` with `strNode`.
- `serviceNode` (canonical order for `New`/`AddService`): `image, build, restart, depends_on, ports, volumes, env_file, environment, secrets, networks, network_mode, deploy, healthcheck, profiles`.
- Top-level secrets: `(*File).SetSecret(def SecretDef) (*File, error)` inserts or replaces one entry under `secrets:` (creating the block after `services:` or after `networks:` when present). `(*File).RemoveSecret(name string) (*File, error)` refuses with `*SecretInUseError{Secret, Services}` when any service references it (short or long syntax), and refuses read-only entries with `ErrEditRefused`.
- `New([]NamedService)` also takes the top-level secret definitions. The signature becomes `New(services []NamedService, secrets []SecretDef)`; existing call sites pass `nil`.
- `RemoveService` keeps its `depends_on` refusal (10b) unchanged; it now uses the typed reader for representable shapes and keeps the raw-node scan for the rest.

### 4.4 Pure validation (`build.go` `ValidateConfig`, extended)

Wrapped in `ErrInvalidConfig`, as for environment/networks:

- `DependsOn`: non-empty service names matching the compose service-name rule; no duplicates; condition in the three allowed values; not the service itself (needs the service name, so `ValidateServiceConfig(name string, cfg ServiceConfig) error` is added, and `ValidateConfig` keeps its signature for existing callers).
- `EnvFiles`: non-empty paths, no NUL or newline, no duplicates.
- `Secrets`: names matching `^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`, no duplicates.
- `Profiles`: compose's profile rule `^[a-zA-Z0-9][a-zA-Z0-9_.-]+$`, no duplicates.
- `SecretDef`: exactly one of `File` / `Environment`; environment name matches `^[A-Za-z_][A-Za-z0-9_]*$`.

Cross-service checks (§5.2) live in `stacks`, because they need all services of the resulting file:

```go
func DependencyCycle(deps map[string][]string) []string // nil, or the cycle as a closed path, e.g. [app worker app]
```

`DependencyCycle` is exported from `composefile` (pure graph code, deterministic: services visited in file order).

### 4.5 Reading a file set

`composefile` stays single-file. The file set is assembled in `stacks` from one parsed `*File` per file. `composefile` adds only the helper the read-only marking needs:

```go
// ServiceFields returns the Fields whose keys a file sets for a service (nil if the
// file does not mention the service).
func (f *File) ServiceFields(name string) ([]Field, error)
```

`deploy` maps to `FieldLimits` (only when it touches `resources.limits`, `cpus` or `mem_limit`). Unknown keys map to nothing. A service key holding an anchor/alias in an override marks every field read-only (it cannot be analysed safely).

## 5. Save pipeline (`internal/stacks`)

### 5.1 Where the checks run

`stagedEdit`, `stagedAdd`, `Create` and `SaveRaw` all reach `commit`. `checkChanges` gains a `checkDependencies(cs changeSet) []FieldError` step and `prepareConfig` gains path checks, so preview and save share them exactly as today (preview still skips compose and engine checks).

### 5.2 Rules and field paths

Field paths follow the existing conventions: unprefixed for single-service edits, `services[i].` for multi-service create, `services.<name>.` for raw saves (`qualify`).

| Check | Where | Error (field → message) |
|---|---|---|
| Unknown dependency target | `checkDependencies` | `depends_on[i].service` → `no service "x" in this stack` |
| Self-dependency | `ValidateServiceConfig` | `depends_on[i].service` → `a service cannot depend on itself` |
| Cycle | `checkDependencies` on the resulting file | `depends_on[i].service` on the edited service's edge that closes the cycle → `dependency cycle: app → worker → app` |
| `service_healthy` target without a compose healthcheck | `checkDependencies` | **warning**, not an error: `depends_on[i].condition` → `db has no healthcheck in compose; Up fails unless its image defines one`. The save goes ahead |
| Required `env_file` missing | `prepareConfig` | `env_file[i].path` → `file does not exist: ./app.env` |
| `env_file` outside allowed dirs | `prepareConfig` | `env_file[i].path` → `env file must be inside the stack folder or an allowed directory` |
| Optional `env_file` missing | — | allowed. The UI shows the hint "missing; compose skips it". |
| Service references an undefined secret | `checkDependencies` | `secrets[i]` → `no secret "x" is defined in this stack` |
| Secret `file` source missing | secret routes (§7.3) and `checkDependencies` | `secrets[i]` → `secret "x" has no value yet` |
| Removing a service that others depend on | `RemoveService` (10b) | unchanged: 409 naming dependents |
| Removing a secret in use | `RemoveSecret` | 409 naming the services |

The existing `ValidationError` collect-all behaviour holds: every row above is reported in one response, not only the first.

### 5.3 Paths are never read for contents

`env_file` and secret files are `os.Stat`ed (after `fsguard.Resolve`), never opened by these checks. `compose config` reads them as it does today. The candidate check file already sits next to the real file (`validateCandidate`), so relative paths resolve the same way.

## 6. Secrets

### 6.1 Storage

- **Managed secret files** live at `<stack dir>/secrets/<name>`, where `<stack dir>` is the directory of the base compose file (managed and external stacks alike). Stackstead creates `secrets/` with mode 0700 and a `secrets/.gitignore` containing `*`, so a stack folder kept in git does not commit values. Each value file is mode 0600, owned by the service user, and written atomically (temp file in the same dir → fsync → rename, the `writeAtomic` pattern from `composefile/disk.go`).
- The secret name is the file name. The name regex (§4.4) excludes `/`, and `.`/`..` are rejected, so the path cannot escape `secrets/`. The resolved path is still checked with `fsguard`-style containment against `<stack dir>/secrets` after symlink resolution, and a symlinked `secrets/` directory is refused.
- A top-level definition written by Stackstead is `<name>: {file: ./secrets/<name>}`. A hand-written `file:` source pointing anywhere else is shown with its path and existence (`set`, `size`, `modifiedAt` from `os.Stat`), but its value cannot be written through Stackstead (`managed: false`). Its path can be changed in the raw editor. This keeps Stackstead's write surface to one directory per stack.
- **Environment sources** (`environment: VAR`) store nothing. Compose reads the variable from its own process environment, which is Stackstead's service environment plus the stack's `.env`. The UI says so. Stackstead does not check that the variable is set (it cannot see what systemd passes compose any differently than compose does). `compose config` reports what it reports.
- Value size: 1–65536 bytes, arbitrary bytes allowed (sent base64 in JSON, §10.5). An empty value is refused (it is almost always a mistake, and compose would mount an empty file).

### 6.2 Flows (in `stacks`, under the stack lock)

```go
func (s *Service) Secrets(ctx context.Context, stack string) ([]SecretInfo, error)
func (s *Service) SetSecret(ctx context.Context, stack string, req SetSecretRequest) (EditResult, error)
func (s *Service) RemoveSecret(ctx context.Context, stack, secret, hash string) (EditResult, error)

type SecretInfo struct {
    Name, Source, File, Environment string
    Managed, Set, ReadOnly          bool
    Size                            int64
    ModifiedAt                      time.Time
    UsedBy                          []string // services, file order
}
type SetSecretRequest struct {
    Name, Source, Environment, Hash string
    Value                           []byte // nil = keep the current value (definition-only change)
}
```

- **Create, or switch the source to file:** validate → write the value file → `SetSecret` on the base file → `commit` (compose config with the full file set). If the commit fails and the value file did not exist before, it is removed. A replaced value is restored from the temp copy kept for the duration of the call.
- **Replace the value only** (definition unchanged): write the value file. The compose file is not touched, its hash does not change, and no compose validation is needed. The `hash` is still checked, so a definition removed in another tab is detected.
- **Switch the source to environment:** the definition changes through `commit`, and the old managed value file is then deleted. No `secrets/<name>.bak` is kept: the same rule as env files, so no extra plaintext copies of secret values. The confirm dialog says the stored value will be deleted.
- **Remove:** refused with 409 when in use (§5.2). Otherwise the definition is removed through `commit`, and the managed value file is deleted after the commit succeeds (if the commit fails, the file stays). The file is not backed up.
- Running containers only see a changed value after they are recreated, and compose does not recreate a container because a secret file's content changed. After a value is replaced, the panel offers **Recreate services using this secret**, which runs the `recreate` action (§8.1) for the secret's `UsedBy` services that are active.

### 6.3 Never echoing values

- No response type has a value field. `SecretInfo` has no value, and the set request's `Value` is decoded into a `[]byte` that is zeroed after the write (best effort).
- The access log and `slog` calls in these handlers never include the request body. A test asserts that the value bytes appear in no log output, no response body and no operation output.
- Previews and the raw editor show only the definition (`file: ./secrets/db`), never the file. The file browser (`GET /api/fs`) refuses to list any `secrets/` directory that contains a `.gitignore` written by Stackstead (marker comment `# stackstead-managed`), and refuses regular files under it. This is defence in depth: it lists names only today, but a future file viewer must not reach them.
- Responses of the secret routes carry `Cache-Control: no-store`.

## 7. Override and extra compose files

### 7.1 The file set

```go
type Entry struct { /* existing */ ; ExtraFiles []string; Profiles []string }

// FileSet returns the files passed to compose, base first.
func (s *Service) fileSet(e Entry) ([]string, error)
```

- `ExtraFiles` empty: `[base]`, plus the default override when the base's file name is one compose finds by default (`compose.yaml`, `compose.yml`, `docker-compose.yaml`, `docker-compose.yml`) and exactly one of `compose.override.yaml`, `compose.override.yml`, `docker-compose.override.yaml`, `docker-compose.override.yml` exists next to it. More than one present → `ErrStackUnavailable` ("several override files: …; remove one or choose the files explicitly"). A base with a non-default name gets no automatic override, because compose would not merge one either: by hand it is run with `-f`.
- `ExtraFiles` non-empty: exactly `[base] + ExtraFiles`, in order, with no automatic override (this is what `docker compose -f a -f b` does). Entries are stored as written: relative to the base's directory, or absolute.
- Every listed file must exist, be a regular file, and resolve inside the stack folder or `allowed_roots`. A file that later disappears makes the stack unavailable with a message naming it (the same state as a missing base file today).
- The exact default-override rules are pinned by an integration test against the installed compose (`TestDefaultFileSetMatchesComposeDiscovery`), so a compose behaviour change shows up as a test failure rather than a silent drift.

### 7.2 Invocation (`internal/composecli`)

```go
type Invocation struct {
    Files    []string // base first; working directory = dir of Files[0]
    Profiles []string // each becomes --profile <p>; "*" allowed
}
func (r *Runner) Run(ctx context.Context, inv Invocation, args []string, out func(string)) error
func (r *Runner) Config(ctx context.Context, inv Invocation) error
func (r *Runner) ProjectName(ctx context.Context, inv Invocation) (string, error)
```

`composeArgs` becomes `compose -f f1 -f f2 … [--profile p …] <args>`, and the subprocess environment gets `COMPOSE_PROFILES` (§8.2). `firstArg` skips `--profile` values the way it skips `-f` values. The `stacks.Compose` interface changes the same way. Fakes in tests record the `Invocation`.

### 7.3 Validation with a file set

`validateCandidate(ctx, e, position int, data []byte)` writes the candidate next to the file it replaces (`.stackstead-check-*.yaml`) and runs `Config`/`ProjectName` on the file set with that one file swapped. `renameInExitError` maps the temp name back. Form edits always replace position 0 (the base). Raw saves replace the edited file's position (§10.2).

### 7.4 Merged view and read-only marking

- `describe` parses every file in the set. For each service of the base, the fields that any later file sets (`ServiceFields`) are added to `ReadOnly`, and recorded in a new `Overridden map[Field]string` (field → the last file that sets it, relative path).
- A service defined only in a later file is listed with `DefinedIn: "<file>"` and every field read-only. Remove service is refused for it with 409 ("defined in compose.override.yaml; edit that file").
- Removing a base service that a later file also defines is refused with 409 ("also defined in compose.override.yaml; remove it there first"). Otherwise the override would be left defining an incomplete service.
- Add service refuses a name that any file in the set defines (`ErrServiceExists`).
- Dependency, secret and cycle checks (§5.2) run over the union: service names from every file. A service's `depends_on` is the union of every file's representable `depends_on` for it. If a later file has a read-only `depends_on` shape, the cycle check is skipped for that service and `compose config` remains the authority.
- **Effective values:** `GET /api/stacks/{name}/effective` runs `docker compose config --format json` on the file set and returns, per service, the effective JSON of each field listed in `Overridden` (environment values included, exactly as compose resolves them; secrets are only ever paths). It is cached per (set of file hashes, `.env` hash) for 30 s. The UI calls it only when a stack has overrides and the operator opens a service.
- Hashes: `hash` on the detail stays the base file's hash (so existing clients keep working). A new `files: [{path, hash, role: "base"|"override"|"extra", exists}]` lists the set. Structured edits check only the base hash, because they only write the base. Read-only marking is recomputed from the current overrides at save time, so a field that became overridden after the form loaded is refused with `*ReadOnlyFieldError` rather than written.

### 7.5 Choosing extra files

`SetFiles(ctx, stack string, files []string) (Detail, error)` validates (§7.1 rules; at most 8 extra files; no duplicates; not the base; `.yaml`/`.yml` only), runs `Config`/`ProjectName` with the new set under the stack lock, claims a changed project exactly like `commit` does, and then saves `ExtraFiles`. Setting the list never writes a compose file. Sending `[]` returns to the default set.

## 8. Profiles and lifecycle

### 8.1 The `recreate` action

- `composeArgs` gains `"recreate": {"up", "-d", "--force-recreate", "--no-deps"}`. `--no-deps` limits recreation to the named services, so dependencies (for example a database) are not restarted. It does not use `--build` or `--remove-orphans`: recreate only replaces containers, and never changes the set of services.
- `POST /api/stacks/{name}/actions/recreate` takes an optional `{services: [...]}`. Each name must be a service of the file set whose profiles are active (400 `services[i]`: `no service "x"` / `x is in inactive profile debug`). A missing or empty list means every active service. The names are appended after the args. Other actions keep rejecting a `services` key (unknown fields stay rejected).
- `Act` becomes `Act(ctx, stack, action string, services []string)`. Locking, the parse check, `requireLifecycle`, `refreshProject` and active profiles are the same as `up`. It returns an operation (202), streamed like every action, and holds the per-stack lock, so it gets 409 while another operation runs.
- `ACTIONS` in the web client stays the five toolbar actions. `recreate` is not a toolbar button: it is offered where a change needs it (the Secrets panel). Its confirm dialog reads "Recreate app, worker? Their containers are replaced; dependencies are left running." On confirm it opens the existing operation panel.
- The CLI gets no new command (CLI work is out of scope). The API is documented in `docs/api.md`.

### 8.2 Active profiles


- `Entry.Profiles` is the active set (sorted, deduplicated). `SetProfiles(ctx, stack string, profiles []string) (Detail, error)` accepts only names that some service in the file set declares (400 `profiles[i]` otherwise) and runs no compose action.
- `Act`: `up`, `pull`, `build` and `restart` run with `Profiles: e.Profiles`, after dropping names no longer declared (each dropped name is logged once at Info). `down` and `Remove` run with `Profiles: ["*"]`. When the installed compose is older than the version that supports `--profile '*'` (checked against the cached `Version` in `versioncache.go`), the fallback is every declared profile listed explicitly.
- Turning a profile off does not stop its running services: the next `up` leaves them alone (compose treats disabled services as part of the model, not as orphans). The UI says "Turning a profile off doesn't stop its services; use Down or stop them first." This is pinned by `TestUpWithProfileOffLeavesItsContainerRunning`.
- Status: a service whose profiles are all inactive is `inactive`, not "down". It is excluded from the running/expected comparison that makes a stack `partial`, so an inactive `debug` service does not make the stack look broken. Port and static-IP checks still include inactive services, because enabling a profile must not produce a clash.
- The CLI (`stackstead stacks up`) goes through the same API, so it uses the saved profiles with no new flags.
- **Stackstead's settings win over `.env`.** Compose reads `COMPOSE_PROFILES` from the project's `.env` whenever no `--profile` flag is given, so an empty active set would let `.env` enable profiles behind Stackstead's back. Every invocation therefore sets `COMPOSE_PROFILES` in the subprocess environment to the active set (comma-joined, empty when none). The process environment takes precedence over `.env`. `COMPOSE_FILE` needs nothing extra, because compose ignores it when `-f` is given. Pinned by `TestDotEnvComposeProfilesDoesNotLeak`.

### 8.3 Import seeding from `.env`

- `Import` reads `.env` next to the imported file with `envfile.Parse`, read-only. It never writes it.
- `COMPOSE_FILE` is split on `COMPOSE_PATH_SEPARATOR` when `.env` sets it, else on `:` (the Linux default). Paths resolve against the `.env` directory. When the first entry resolves to the imported file, the remaining entries become `extra_files`, validated by the §7.1/§7.5 rules. When the imported file is not the first entry, or any entry fails validation, nothing is seeded and the warning says why ("COMPOSE_FILE does not start with compose.yaml; using compose defaults").
- `COMPOSE_PROFILES` is split on `,`. Only names that the resulting file set declares are seeded. Unknown names are listed in the warning.
- Seeded settings go through the same checks as `SetFiles`, including compose config and the project-name derivation. They are stored with the registry row in the same transaction as the import.
- When either variable was present, the import response carries `warnings: [{field: "env", message: "Seeded files and profiles from .env (COMPOSE_FILE, COMPOSE_PROFILES). Stackstead's settings now take precedence; later changes to these variables in .env have no effect here."}]`. The stack detail shows the same note (`dotenvSeeded: true`) until the operator dismisses it, which clears the registry column `dotenv_seeded`.
- Startup sync of managed folders does not seed. Managed stacks are created by Stackstead, which never writes these variables.

## 9. Env file contents

### 9.1 Package `internal/envfile`

This is a new, stdlib-only package with the same discipline as `composefile`: parse only to locate, edit by splicing lines, and verify every edit semantically.

```go
type File struct { /* src []byte; lines []line */ }
type Entry struct {
    Key      string
    Value    string // decoded value; never logged
    Line     int    // 1-based
    Editable bool
    Reason   string // why not editable, e.g. "multi-line value", "uses ${...} interpolation"
}
func Parse(src []byte) (*File, error)
func (f *File) Entries() []Entry                 // file order
func (f *File) Apply(ops []Op) (*File, error)    // all-or-nothing
func (f *File) Bytes() []byte
type Op struct { Key string; Value *string }     // nil Value = delete
var ErrNotEditable, ErrInvalidKey, ErrUnrepresentable error
```

- **Syntax read** (compose's `env_file` dotenv dialect): `KEY=VALUE`, optional `export ` prefix, blank lines, `#` full-line comments, unquoted values with ` #` inline comments, single-quoted (literal) and double-quoted (escapes `\\ \" \n \t \$`) values. Keys match `^[A-Za-z_][A-Za-z0-9_.-]*$`.
- **Not editable (kept byte-for-byte, shown read-only with `Reason`):** multi-line quoted values, values containing `${` or `$VAR` interpolation, keys that appear more than once (all occurrences), and lines that do not parse (the rest of the file stays editable, and the line is shown as "unparsed line N"). CRLF files: the whole file is read-only (the same stance as `composefile`).
- **Writing a value** replaces only the value span on its line. The `export ` prefix, the spacing around `=`, the inline comment and the existing quote style are kept whenever the new value can be represented in that style. Otherwise the shortest safe form is used: plain when the value matches `^[A-Za-z0-9_./:@%+,=-]*$`, else single quotes when it has no `'`, else double quotes with escapes. Values with a newline or NUL are refused (`ErrUnrepresentable`, field error "use a shell or the raw file for multi-line values").
- **New keys** are appended at the end of the file (after a trailing comment block only if separated by a blank line), with plain `KEY=value` style. A file without a final newline gets one first.
- **Delete** removes the line, plus any immediately preceding comment lines that are attached to it (no blank line in between), the same rule as `RemoveService`.
- **Verification:** after `Apply`, the result is re-parsed. `Entries()` of the result must equal the original entries with exactly `ops` applied, and every non-entry line must be byte-identical. Otherwise the edit is refused (`ErrEditRefused`-equivalent `envfile.ErrVerify`). An integration test pins the dialect against compose itself (§14).

### 9.2 Which files may be edited

User decision (vs only files inside the stack's own folder): any env file that resolves (after symlinks, via `fsguard.Resolve`) inside the stack folder or `allowed_roots` is writable. That includes shared files such as `/opt/shared/common.env`. Files outside both are read-only, with the key list shown and values masked, and the reason "outside the allowed directories". A missing file can be created anywhere the same rule allows.

Shared files are guarded, not forbidden:

- `EnvFileView.SharedWith []string` lists every *other* registered stack whose file set has a service `env_file` that resolves to the same real path. It is computed on each GET by parsing the registered stacks' file sets, and is cheap at homelab scale (tens of stacks).
- Before the first save in an editor session, the UI shows a warning and lists those stacks: "Also used by: media, paperless. Their containers keep the old values until each stack is brought Up." The operator confirms to continue.
- The server enforces the warning: `EnvFileSave.ConfirmShared []string` must equal the current `SharedWith` set. If it differs (the file became shared, or another stack started using it after the view loaded), the save is refused with 409 `{error, sharedWith}` and nothing is written, so the UI can show the new list and ask again.
- A write to a shared file is serialized by a process-wide lock keyed by the resolved path (taken after the stack lock, the same order as `projectMu`). Together with the hash check, this prevents two stacks' editors from overwriting each other.
- After a save, the other stacks are not touched. Their detail shows nothing new (Stackstead does not track env-file hashes per stack), which is why the warning names them.

### 9.3 Flows (in `stacks`, under the stack lock)

```go
func (s *Service) EnvFile(ctx context.Context, stack, path string, reveal bool) (EnvFileView, error)
func (s *Service) SaveEnvFile(ctx context.Context, stack string, req EnvFileSave) (EditResult, error)
type EnvFileView struct { Path string; Exists, Writable bool; Hash string; Entries []EnvEntryView; ReadOnlyReason string; SharedWith []string }
type EnvEntryView struct { Key string; Value *string; Length int; Editable bool; Reason string } // Value nil unless reveal
type EnvFileSave struct { Path, Hash string; Create bool; ConfirmShared []string; Ops []envfile.Op }
```

- `path` must be an `env_file` path of some service in the file set (as written, resolved against the base's directory), or, with `Create`, a new path that passes §9.2. Anything else is 404 ("not an env file of this stack"), so the routes cannot be used to read arbitrary files.
- The save checks `Hash` (stale → `ErrConflict`, 409), applies `ops`, and writes atomically, preserving the file's mode (a created file is 0600). No `.bak` is written (user decision): a backup would be another plaintext copy of secret values, outside every confinement and masking rule. The write is still atomic, so a failed save leaves the old file intact. **When the path resolves to the stack's own `.env`** (the file config history versions), the save also runs config history's pre-write hook `beforeChange(ctx, e)` (config-history §5.6) under the stack lock before writing. A failure refuses the save with 503 and writes nothing. After a successful write, and before releasing the lock, it calls `afterChange(ctx, e, history.Source("save"))`, which records a `save` revision with the session's actor from the context, so the change shows as a History revision with masked values. It runs no compose validation: the compose file did not change. The stack detail then shows the existing "changes not applied — Up" state. Compose recreates the affected containers on the next Up, because the resolved environment is part of its config hash.
- `Ops` reference keys. Values that the client did not change are not sent at all, so masked values never round-trip through the browser.

### 9.4 Exposure rules

- Values are masked by default. A GET without `reveal=true` returns `value: null` and `length` only. `reveal=true` returns every value of that one file with `Cache-Control: no-store`. This is the same convention as the config-history spec (`reveal=true`). The UI requests it only when the operator clicks Reveal, holds the values in component state, and drops them when the editor closes.
- Values never appear in logs (the handlers log path, key count and outcome only), in operation output, in alerts, or in error messages (key names may appear; values never do).
- Previews of an env-file save list keys with `added`, `changed` or `removed`, never values.

## 10. API

All new routes require a session. Mutating routes require `Content-Type: application/json` (the existing CSRF rule). Errors are `{error}`, or `{error, fields}` with 400, and unknown JSON fields are rejected, all as today.

### 10.1 `ServiceConfig` (create, add, update, detail)

```json
"dependsOn": [{"service": "db", "condition": "service_healthy", "restart": false, "required": true}],
"envFile":   [{"path": "./app.env", "required": true}],
"secrets":   ["db_password"],
"profiles":  ["debug"]
```

- `condition` is optional and defaults to `service_started`. `restart` defaults to false and `required` to true.
- **Presence semantics** (user decision): in `PUT …/services/{service}`, a missing key keeps the file's current value, and `[]` or `null` clears it. Implemented with an `apitypes.Present[T]` wrapper whose `UnmarshalJSON` records presence (it is also called for `null`). `stacks.UpdateService` gains `keep []composefile.Field` and copies the current values of kept fields into the config before `UpdateService` runs, inside `stagedEdit`, so preview and save agree. In create and add, missing means "not set".
- Responses always include the four keys (`[]` when empty), so a client that echoes a detail back never relies on absence.
- `ServiceDetail` adds `overridden: {"<field>": "<file>"}`, `definedIn: "<file>"` (`""` for the base), `profiles` (the same as `config.profiles`, for list views) and `active` (false when every one of its profiles is inactive).
- Edit, add and create responses add `warnings: [{field, message}]`. This is the first non-blocking result. It is only used by the `service_healthy` check today, and its paths are prefixed the same way as errors.

### 10.2 Raw files

`GET /api/stacks/{name}/compose?file=<path>` and `PUT` with `{content, hash, file}`. `file` defaults to the base and must be a member of the current file set (400 otherwise). The response adds `file`. A raw save of an override goes through `commit` with that file's position (§7.3).

### 10.3 Stack detail additions

`files: [{path, role, hash, exists}]`, `extraFiles: [...]` (as stored; `[]` = default set), `profiles: [...]` (active), `declaredProfiles: [...]`. The stacks list (`StackSummary`) adds only `profiles` (active), for the list's quick actions.

### 10.4 New routes

| Method and path | Body | Result |
|---|---|---|
| `PUT /api/stacks/{name}/files` | `{files: [...]}` | 200 detail; 400 field errors `files[i]`; 409 stack busy, project taken; 422 compose config failure (output passed through, as for edits) |
| `PUT /api/stacks/{name}/profiles` | `{profiles: [...]}` | 200 detail; 400 `profiles[i]` unknown |
| `POST /api/stacks/{name}/actions/recreate` | `{services?: [...]}` | 202 operation; 400 `services[i]`; 409 busy (§8.1) |
| `POST /api/stacks/{name}/dotenv-note/dismiss` | `{}` | 204; clears `dotenvSeeded` (§8.3) |
| `GET /api/stacks/{name}/effective?service=` | — | 200 `{service, fields: {"<field>": <json>}}`; 422 compose config failure |
| `GET /api/stacks/{name}/secrets` | — | 200 `{hash, secrets: [SecretInfo]}`, where `hash` is the base hash |
| `PUT /api/stacks/{name}/secrets/{secret}` | `{source: "file"\|"environment", value?: "<base64>", environment?, hash}` | 200 `{hash, secret}`; 400 fields `name`, `value`, `environment`; 409 stale or read-only |
| `DELETE /api/stacks/{name}/secrets/{secret}?hash=` | — | 200 `{hash}`; 409 in use (names services) or stale |
| `GET /api/stacks/{name}/envfiles?path=&reveal=` | — | 200 `EnvFileView`; 404 not an env file of the stack |
| `PUT /api/stacks/{name}/envfiles` | `{path, hash, create?, confirmShared: [...], set: [{key, value}], delete: [key]}` | 200 `{hash}`; 400 fields `set[i].key`, `set[i].value`, `delete[i]`; 409 stale, or `{error, sharedWith}` when `confirmShared` does not match |
| `POST /api/stacks/{name}/envfiles/preview` | same as PUT | 200 `{changes: [{key, change}]}` |

### 10.5 Create

`POST /api/stacks` (services form only) adds optional top-level `secrets: [{name, source, value?, environment?}]` and `envFiles: [{path, set: [{key, value}]}]`. Paths must be relative and inside the new folder. They are written after the folder claim and before compose validation, and the existing failure cleanup removes them with the folder. The legacy body gets neither.

### 10.6 Docs

`docs/api.md`: the overview rows above; replace "Override files are not supported yet" with a "Compose files and overrides" note (the default set, explicit lists, the ambiguity refusal, the base-only form edits); presence semantics; secret and env-file exposure rules; profiles and `down --profile '*'`. Alerts table: `override` now means "several override files" (§12).

## 11. Frontend

### 11.1 Form model (`web/src/lib/compose/form.ts`)

`ServiceForm` adds:

```ts
dependsOn: { service: string; condition: Condition; restart: boolean; required: boolean }[];
envFiles: { path: string; required: boolean }[];
secrets: string[];
profiles: string[];
```

The `Field` type in `lib/api/types.ts` adds `depends_on`, `env_file`, `secrets` and `profiles`. `toForm` and `fromForm` follow the existing read-only rule: a read-only group is taken from `original`. `fromForm` always sends all four keys in edits, so the UI never relies on presence semantics. `validateForm` adds client-side mirrors of §4.4 (duplicates, self-dependency, name rules) and needs `opts.siblings: string[]` for unknown-target checks. `mapServerErrors`/`fieldOf` learn the new paths (`depends_on[0].service`, `env_file[1].path`, `secrets[0]`, `profiles[0]`), and `warnings` map to row hints in the same way.

### 11.2 `ServiceForm.svelte` groups

These are added in canonical order after Healthcheck and before Restart/Resources:

- **Dependencies:** a `ListEditor` row with a service `<select>` of sibling names (prop `siblings`; in New stack, the other cards' current names), a condition `<select>` (Started / Healthy / Completed successfully), and Restart and Required checkboxes. A healthy condition on a sibling with no healthcheck shows the warning text inline as you type (client mirror), and the server warning shows after save.
- **Env files:** a `ListEditor` row with a path input plus a FileBrowser pick button, a Required checkbox, a missing-file hint ("missing; compose skips it" when optional), and an **Edit contents** button. The button is disabled with a reason when the file is not writable (§9.2) or the form is unsaved for a new path that is not yet created.
- **Secrets:** a checklist of the stack's top-level secrets, with a "Manage secrets" link to the panel. Long-syntax references make the group read-only with the existing read-only note.
- **Profiles:** a chip input. Suggestions are the stack's `declaredProfiles`, and the rule is enforced as you type.
- **Overridden fields:** the existing read-only note reads "Set in compose.override.yaml. Edit it in the Compose tab." and has a **Show effective value** disclosure that lazily calls `/effective` and renders the JSON as YAML, read-only.

### 11.3 New components

- `lib/stacks/EnvFileEditor.svelte` (dialog): a key/value table. When `sharedWith` is non-empty, a warning banner names those stacks (as links) and the first Save opens a confirm that sends `confirmShared`. A `sharedWith` 409 refreshes the list and asks again. Values show as `••••••••` with their length. **Reveal** fetches `reveal=true` once and shows all values, and **Hide** clears them from state. Non-editable rows show `Reason`, and there is an add row and per-row delete. Save → preview (a changed-keys list) → PUT. A 409 uses the existing stale-reload pattern.
- `lib/stacks/SecretsPanel.svelte`: a section on the Services tab below the service cards. Rows show name, source, `set · 23 B · modified 2d ago` or `missing`, used-by chips, and a managed or "managed outside Stackstead (path)" badge. Actions: **Add secret** and **Replace value** (a dialog with a password-type textarea and a "from file" upload; the value is never prefilled), **Switch source**, and **Remove** (confirm: "Remove secret db_password? Its stored value is deleted."; disabled with the reason when in use).
- `lib/stacks/ProfileSelector.svelte`: toggle chips next to `ActionsBar`, shown only when `declaredProfiles` is non-empty. A change calls `PUT …/profiles` and toasts "Profiles saved. Run Up to start newly enabled services." The off-toggle tooltip carries the §8 note.
- `lib/stacks/ComposeFilesDialog.svelte`: opened from `StackAside`'s new **Files** row (which lists the set with roles). It is an ordered list with add (FileBrowser), remove, up/down, and "Use compose defaults" (sends `[]`). A compose config failure is shown verbatim.
- Compose tab: a file switcher (`<select>` of `files`) above `ComposeEditor`. Each file keeps its own dirty state and hash.

### 11.4 Lists and status

Service cards show `inactive · profile debug` for inactive services and `defined in compose.override.yaml` for override-only services (their Edit and Remove are disabled with the reason).

### 11.5 New stack (`StackCreate.svelte`)

Cards pass `siblings`. A stack-level **Secrets** section (the same row editor as the panel, values included in the submit) and per-card env-file rows with **Create file** (an inline key/value table) map to §10.5. Errors `secrets[i].*` and `envFiles[i].*` route to those sections.

### 11.6 Accessibility

Chip toggles are `role="switch"` with `aria-checked`. Masked values have `aria-label="value hidden"` and Reveal is a real button with `aria-pressed`. Dialogs use the existing `Dialog` (focus trap, Esc).

## 12. Migration of existing stacks

- **DB:** one appended migration: `ALTER TABLE stacks ADD COLUMN extra_files TEXT NOT NULL DEFAULT '[]'` `ADD COLUMN profiles TEXT NOT NULL DEFAULT '[]'` (JSON arrays) and `ADD COLUMN dotenv_seeded INTEGER NOT NULL DEFAULT 0`. Existing rows get the default file set and no active profiles.
- **Files:** nothing is rewritten on upgrade. New fields are read lazily, and keys that the form now models but that use unrepresentable shapes become read-only. Before, they were simply unmodelled, so nothing the user could edit before becomes uneditable.
- **Behaviour changes, called out in release notes and `docs/api.md`:**
  - Stacks whose folder has exactly one default override file are adopted by startup sync and accepted by import. Their next Up now includes the override, which is what `docker compose up` by hand always did.
  - `down` and stack removal now also stop services in profiles (`--profile '*'`). Before, profiled containers started by hand were left running.
  - The `override` alert now fires only for ambiguous override sets ("several override files: compose.override.yaml, docker-compose.override.yml"). `stacks.Detail.Overrides` is replaced by `Files`, and `alerts/rules.go` is updated with it.
  - Profiled services were never started by Stackstead before (no `--profile` was passed), and with an empty active set they still are not.
- `.stackstead-check-*` temp files keep their existing cleanup.

## 13. Security

- No new privilege: the service user can already read and write the stack folders and talks to Docker (root-equivalent, per `docs/security.md`).
- Secret and env-file values: write-only for secrets and masked by default for env files (§6.3, §9.4). `no-store` on every response that could carry a value. Never logged. Excluded from previews, alerts and operation output.
- On-disk: managed secret files 0600 in a 0700 `secrets/` with a `.gitignore`. Created env files are 0600, and env-file saves write no `.bak` copy. `docs/security.md` adds that compose secrets and env files are plaintext on disk by design, and that a stack folder backup contains them.
- Path confinement: every user-supplied path (`env_file`, extra compose files, env-file edits) goes through `fsguard.Resolve` against the stack folder and `allowed_roots`. The env-file routes only serve paths the stack references (§9.3). Writes to an env file shared by several stacks need an explicit, server-checked confirmation that names them (§9.2). `docs/security.md` notes that editing a shared env file changes every stack that uses it. Secret value writes only target `<stack dir>/secrets/<name>`.
- Profiles and file names reach compose only as separate `exec` arguments, never through a shell, as today.
- Integration note: config history versions the compose file set and `.env` (encrypted), but not `env_file:` targets other than `.env`, and not secret files. Those keep no previous version: env-file saves and secret removals write no `.bak`.

## 14. Testing

Test-first for every behaviour change (global rules). Tests assert observable behaviour (bytes in → bytes out, HTTP in → HTTP out).

**composefile** (`read_test.go`, `update_test.go`, `services_test.go`, `build_test.go`):
- `TestServiceReadsDependsOnShortAndLong` (list → `service_started`; map with condition/restart/required).
- `TestServiceMarksUnrepresentableNewFieldsReadOnly`, a table: unknown condition, extra key under a dependency, `env_file` with `format`, long-syntax secret, anchored `profiles`, service with `extends` (all fields).
- `TestUpdateDependsOnKeepsListStyle` (adding a started dependency to a list stays a list, byte-exact golden).
- `TestUpdateDependsOnSwitchesToLongFormForCondition` (golden; comments on neighbouring keys kept).
- `TestUpdateEnvFileScalarStaysScalar`, `TestUpdateEnvFileOptionalUsesMapping`.
- `TestUpdateSecretsAndProfilesChangeOnlyThoseLines`.
- `TestUpdateLeavesReadOnlyNewFieldsByteIdentical` (an edit of `image` on a service with long-syntax secrets).
- `TestSetSecretCreatesBlockAfterServices`, `TestSetSecretReplacesEntryInPlace`, `TestRemoveSecretRefusesInUse` (short and long references), `TestRemoveSecretRefusesReadOnlyEntry`.
- `TestNewRendersNewFieldsInCanonicalOrder`, `TestNewWritesTopLevelSecrets`.
- `TestDependencyCycle`, a table: none, self-loop excluded (handled by validation), 2-cycle, 3-cycle path, deterministic first cycle in file order.
- `TestValidateServiceConfigRejects`, a table: duplicate dependency, bad condition, self-dependency, empty env path, bad secret/profile names, a secret def with both sources.
- `TestServiceFieldsMapsKeysToFields` (`deploy.resources.limits` → limits, `x-foo` → nothing, anchor → all).

**envfile** (`envfile_test.go`):
- `TestParseDialect`, a table: export, quotes, inline comments, escapes, blank and comment lines.
- `TestEntriesMarksNotEditable`: multi-line, interpolation, duplicate keys, unparsed line, CRLF.
- `TestApplySetKeepsCommentsAndStyle` (golden: inline comment and quote style preserved).
- `TestApplyChoosesSafeQuoting` (values with spaces, `'`, `"`, `#`, `$`).
- `TestApplyRefusesMultilineValue`, `TestApplyAppendsNewKeyAndFinalNewline`, `TestApplyDeleteRemovesAttachedComment`, `TestApplyDeleteKeepsCommentSeparatedByBlankLine`, `TestApplyIsAllOrNothing`, `TestApplyVerifyCatchesMismatch` (an injected splice bug is refused).

**stacks** (`service_compose_forms_test.go`, fake compose recording `Invocation`):
- `TestUpdateServiceUnknownDependencyIsFieldError`, `TestCycleErrorNamesPath`, `TestServiceHealthyWithoutHealthcheckIsWarningNotError`.
- `TestEnvFileMissingRequiredIsFieldError`, `TestEnvFileOutsideRootsIsFieldError`, `TestEnvFileOptionalMissingIsAccepted`.
- `TestUndefinedSecretReferenceIsFieldError`.
- `TestPresenceSemanticsKeepsOmittedNewFields` (PUT without `dependsOn` leaves it; with `[]` removes it; preview agrees).
- `TestFileSetDefault` (table: no override; one; two → unavailable; non-default base name ignores the override), `TestFileSetExplicitList`, `TestSetFilesValidatesAndClaimsProject`, `TestSetFilesRejectsOutsideRoots`.
- `TestOverriddenFieldsAreReadOnlyWithFile`, `TestOverrideOnlyServiceCannotBeRemoved`, `TestRemoveBaseServiceAlsoInOverrideIsRefused`, `TestFormEditRefusedWhenFieldBecameOverriddenAfterLoad`.
- `TestRawSaveOfOverrideValidatesWithFullSet`.
- `TestRecreateArgsAndServiceValidation` (named services → `up -d --force-recreate --no-deps a b` with profiles; inactive or unknown → 400; empty → all active), `TestRecreateBusyIs409`, `TestImportSeedsFilesAndProfilesFromDotenv` (table: both set; custom `COMPOSE_PATH_SEPARATOR`; imported file not first → no seed plus warning; unknown profile dropped plus warning; invalid extra file → no seed plus warning), `TestImportNeverWritesDotenv`, `TestInvocationSetsComposeProfilesEnv` (empty active set → `COMPOSE_PROFILES=` in the subprocess env), `TestActPassesActiveProfiles`, `TestDownAndRemoveUseAllProfiles`, `TestDownFallsBackToDeclaredProfilesOnOldCompose`, `TestSetProfilesRejectsUndeclared`, `TestInactiveServiceDoesNotMakeStackPartial`.
- Secrets: `TestSetSecretWritesManagedFile0600AndGitignore`, `TestSetSecretFailedCommitRemovesNewFile`, `TestReplaceSecretValueDoesNotTouchComposeFile`, `TestRemoveSecretDeletesValueFileOnlyAfterCommit`, `TestSecretNameCannotEscapeDir` (`..`, symlinked `secrets/`).
- Env files: `TestEnvFileSharedWithListsOtherStacks` (two stacks referencing one file via different relative paths/symlink), `TestSaveSharedEnvFileRequiresMatchingConfirm` (missing → 409 with the list, stale list → 409, exact → 200), `TestSaveEnvFileOutsideRootsIsReadOnly`, `TestConcurrentSavesToSharedEnvFileOneWins`, `TestEnvFileViewMasksUnlessReveal`, `TestEnvFileRoutesRefuseUnreferencedPath`, `TestSaveDotEnvRunsHistoryCheckAndRecords` (check failure → 503 and the file is unchanged; success → one revision recorded), `TestSaveEnvFileStaleHashConflicts`, `TestSaveEnvFileKeepsModeAndWritesNoBak`.
- `TestImportAndSyncAcceptSingleOverride` (replaces the old refusal tests).

**composecli**: `TestComposeArgsWithFilesAndProfiles` (order: every `-f`, then `--profile`, then the args), and `TestFirstArgSkipsProfileValues`.

**api** (`httptest`, real router): JSON shapes of §10 (presence: missing vs `null` vs `[]`), `warnings` on responses, auth on every new route (401), JSON content-type rule on mutations, `no-store` headers, and `TestSecretValueNeverInAnyResponseOrLog` (sets a value with a unique marker, then greps every route's response body and the captured `slog` output).

**alerts**: `override` fires only for ambiguous sets; a single override produces no alert.

**Integration** (`-tags integration`, Docker):
- `TestDependsOnHealthyOrdersStartup` (busybox `db` with a healthcheck; `app` starts after healthy).
- `TestDefaultFileSetMatchesComposeDiscovery` (for each layout, `docker compose config` without `-f` equals Stackstead's set).
- `TestOverrideAppliedOnUp` (an override changes an env var; the container sees it).
- `TestUpWithProfileOffLeavesItsContainerRunning`, `TestDownStopsProfiledServices`, `TestDotEnvComposeProfilesDoesNotLeak` (`.env` has `COMPOSE_PROFILES=debug`, active set empty → `up` does not start `debug` services), `TestRecreatePicksUpNewSecretValue` (replace the value, recreate → the container reads the new value; its dependency's container id is unchanged).
- `TestSecretMountedFromManagedFile` (the container reads `/run/secrets/x`).
- `TestEnvWriterMatchesComposeParsing` (a corpus of tricky values written by `envfile`, then `compose config --format json` shows the same values).

**web (vitest):** `form.test.ts`: round trip of the new fields, read-only groups taken from `original`, sibling validation, new error paths and warnings. `ServiceForm.test.ts`: the dependency select lists siblings, the healthy warning, the missing env hint, the overridden note and effective-value disclosure. `EnvFileEditor.test.ts`: the shared warning lists stacks and the first save confirms with `confirmShared`, a `sharedWith` 409 re-asks, masked by default, reveal fetches once with `reveal=true`, close clears values, save sends only changed keys, stale 409 flow. `SecretsPanel.test.ts`: after a replace, the Recreate button confirms with the service list and starts the `recreate` action with only the active `UsedBy` services, no value is ever prefilled, remove disabled when in use, replace dialog. `ProfileSelector.test.ts`: hidden without declared profiles, toggling PUTs and toasts. `ComposeFilesDialog.test.ts`: reorder, defaults, error display. `StackCreate.test.ts`: secrets and env files in the body, error routing.

**E2E smoke:** extend `TestE2ESmoke`: create a two-service stack with `depends_on` healthy, a secret and an env file; `up`; `GET …/secrets` has no value; add `compose.override.yaml` by hand → the detail lists it and the overridden field is read-only; set profile `debug` → `up` starts the profiled service; `down` stops all.

### Acceptance criteria

All §1 success criteria demonstrated by the E2E smoke and the integration tests above. `go test ./...` and `go test -tags integration ./...` green. `npm test` green. `golangci-lint` clean. No test asserts on implementation details (mock call counts are used only where the call *is* the behaviour, such as "no compose action ran").

## 15. Integration points with sibling specs

- **Config history:** it versions the whole compose file set (base, override and extra files) and the per-stack file list (`extra_files`), plus `.env`. Every write in this spec that changes one of those goes through its hooks: `commit` (form, raw, add/remove service, secret definitions) and `SetFiles` call its `beforeChange` hook before writing (a failure refuses with 503) and `afterChange` after (config-history §5.6). Env-file editor writes to the stack's own `.env` do the same (§9.3). Secret value files and other `env_file:` targets stay out of history, and keep no `.bak`.
- **Notifications:** the `override` alert's meaning changes (ambiguous overrides only). Any notification rule keyed on alert kinds sees fewer `override` alerts.
- **Image updates:** image references can come from override files. The image-update check should read the effective image (`/effective` or `compose config`), not the base file's `image:`.
- **Reachability checks and volume diagnostics:** ports and volumes may be set in overrides. Use the effective config, and treat services in inactive profiles as not expected to be running.
- **Images/storage inventory:** no interaction beyond the effective config for image names.

## 16. Open questions

None. All decisions were settled with the user (rows marked *user* in §3).
