# Stackstead Plan 10a — Networks

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** this plan specifies *what* to build: files, signatures, behaviour, test cases and acceptance criteria. It deliberately contains no full implementations; implementers write the code test-first. Short snippets show shapes only.

**Goal:** Docker network management from the browser: `/networks` lists every network on the host (driver, subnet, gateway, parent interface, attached containers, owner) and lets the operator create bridge, macvlan and ipvlan networks (subnet and gateway pre-filled from a host interface) and remove networks that are not built-in, not owned by a registered stack and not in use. The stack form's `NetworkPicker` gains a last option **Create network…** so a LAN network can be created in the flow that needs it.

**Architecture:**
- Backend: `engine.Client` gains a JSON-body POST helper, a richer `Networks()` decode (scope, internal, options, labels, IP range), `InspectNetwork`, `CreateNetwork` and `RemoveNetwork`; `internal/host` gains a Linux interface lister (`net.Interfaces` + `/proc/net/route`, injectable source); `internal/api/networks.go` serves `GET /api/networks` (extended, backward compatible, 2 s single-flight cache shared with the containers list through a small generic cache), `POST /api/networks` (validated before Docker is called) and `DELETE /api/networks/{id}` (server-side refusals); `internal/api/dashboard.go` serves `GET /api/host/interfaces`.
- Frontend: rail entry + route `/networks`; `NetworkList.svelte` (table, expandable container rows, owner chip, Remove with confirm, events-driven refresh through a shared refreshing-list controller extracted from the Containers list); `CreateNetworkDialog.svelte` (driver-dependent fields, interface auto-fill, server field errors); `NetworkPicker` "Create network…" option.

**Tech Stack:** Go 1.26 stdlib only; Svelte 5, TypeScript, Vitest (jsdom). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-15-stackstead-networks-multiservice-design.md` §1, §2, §3, §4.1–4.4, §6.1–6.3, §6.6 (networks parts), §7–§10 (networks parts), §11 Plan 10a. Plan 10b (multi-service stacks) runs in a parallel worktree and is **not** part of this plan; the only shared file is `web/src/lib/stacks/NetworkPicker.svelte`, where 10a's change is additive. Builds on Plan 9 (`main` at `826f5bd`).

## Decisions (controller, recorded for the user)

The spec's §3 table holds the product decisions (made with the user). Plan-level ones, each grounded in the current code:

- **Validation errors are 400, not 422.** The spec says 422 for field errors, but every existing validation failure (`stacks.ValidationError` in `writeStackError`, `types.ts` "400 body for validation failures", the UI's error routing) is `400 {error, fields}`. Networks follow the code, not the spec text: field errors 400, state conflicts 409. Docker refusals on create map per spec (409 for Docker's 409, 400 with Docker's message otherwise), not to Plan 9's 502: a create failure is a bad request (unknown parent NIC) or a conflict, and the message is what the user needs.
- **`GET /api/networks` moves to `internal/api/networks.go` and reads the engine directly.** Today it is registered in `registerStackRoutes` and served from `StacksAPI.Networks`; the rich list needs `InspectNetwork` (containers) which only the engine has. New `api.Options.NetworkEngine NetworkEngineAPI` (satisfied by `*engine.Client`, wired in `internal/cli/serve.go`); the three network routes are registered only when `Stacks` and `NetworkEngine` are set (`Stacks` supplies the project → stack map). `StacksAPI.Networks` stays in the interface (the router no longer calls it; removing it would touch `StacksAPI`, which 10b edits in parallel — follow-up after both merge). `fakeStacks.Networks` therefore keeps compiling; the networks assertion in `TestNetworksAndStatus` and the `c.Networks` call in `client_contract_test.go` get a fake network engine.
- **One generic single-flight cache.** The containers list's `containerCache`/`containerFlight`/`invalidateContainerList` (Plan 9) is extracted, behaviour-preserving, into `flightCache[T]` (`internal/api/flightcache.go`) and reused for networks. Existing `TestContainerList*` tests stay green and pin the extraction.
- **Docker is called with a JSON body for the first time.** `engine.request` takes no body today. T1 splits it into request building + `send` and adds `postJSON`; existing call sites and their tests are unchanged.
- **Host interfaces need no build tags.** The `statfs_unix.go`/`statfs_other.go` split exists because `syscall.Statfs` is not portable; `net.Interfaces` and parsing a text file are. `host.InterfaceLister` compiles everywhere, gates on an injected `GOOS` (like `host.Config.GOOS`) and takes an injectable `Source` so tests never touch the real host. Its `Interface` struct carries the JSON tags and is the wire body directly, as `host.Snapshot` is for `/api/host` (no `apitypes` entry).
- **Gateway validation reuses `stacks.CheckStaticIP`** with a synthetic one-subnet `engine.Network`: it already rejects non-IPv4, out-of-subnet, network and broadcast addresses with good messages, so nothing is exported or duplicated. Subnet overlap uses `netip.Prefix.Overlaps`.
- **`DELETE /api/networks/{id}` has no body**, like `DELETE /api/stacks/{name}` and `DELETE /api/settings/metrics-token`; the existing CSRF rule (JSON content type) applies to bodies, and the SameSite=Strict cookie covers body-less methods. `{id}` is resolved against a fresh `Networks()` listing: exact id or exact name first, then a unique 12–64 hex prefix; anything else is 404.
- **List DTO ids are short** (`shortID`, 12 hex) like `ContainerRow.ID`; the spec example shows 12 hex.
- **Network list refresh reuses the Containers controller.** `createContainerList` (Plan 9) becomes a thin wrapper over a generic `createRefreshList<T>` in `web/src/lib/stores/refreshList.ts`; `list.test.ts` stays green and pins the extraction.
- **The dialog never shows a toast itself**: it calls `oncreated(network)`; the Networks page toasts and refreshes, the picker reloads and selects. Validation of the macvlan parent against `GET /api/host/interfaces` is not done (spec §4.2: Docker validates; the list is empty off Linux).

## Global Constraints

- Spec §1 success criteria: from the browser alone an operator can create a macvlan network on a host interface with subnet/gateway pre-filled; `/networks` shows driver, subnet, gateway, parent, containers and owner; unmanaged, unused, non-built-in networks can be removed — **server-enforced** refusals (409) for built-in, stack-owned and in-use networks whatever the client sends.
- Spec §4: every new route `requireSession`; unknown `/api/*` JSON 404 after auth; errors `{error}`; `POST /api/networks` JSON-only (existing `decodeJSON` rule, unknown fields rejected); list and mutations 503 while Docker is unavailable; `GET /api/host/interfaces` works without Docker.
- Spec §4.1: existing fields `name`, `driver`, `subnets` keep their meaning and position in the JSON; arrays are never `null` (`subnets: []`, `gateways: []`, `containers: []`); `gateways[i]` pairs with `subnets[i]`.
- Spec §4.2: all field validation runs **before** Docker is called; `parent` reaches Docker only as the `parent` driver option, never a shell; the created network carries label `dev.stackstead.created=true`.
- Spec §6.6 / Plan 9 a11y rules: icon-only controls have `aria-label` and `title`; soft-disabled buttons carry the reason (`Button.disabledReason`); expand buttons `aria-expanded` + `aria-controls`; dialog is the existing `Dialog` (focus trap, Esc); no horizontal scroll at 375 px; explicit table roles + `data-label`s for the ≤ 720 px card layout; status never by colour alone.
- No `{@html}`; Docker error messages, network, container and interface names are interpolated as text.
- Budgets unchanged: entry JS ≤ 120 kB gz, total ≤ 400 kB gz, fonts ≤ 200 kB; the new pages import neither CodeMirror nor uPlot.
- No new npm or Go dependencies. `go test ./...` passes without Node; `CGO_ENABLED=0 GOOS=linux GOARCH={amd64,arm64} go build ./...` and `GOOS=darwin go build ./...` pass.
- README: the user's centred header, badges and feature table stay unchanged; only one feature-list line is added.
- Test-first for every behaviour change: write the test, run it, see it fail for the expected reason, implement, pass, commit.
- `make check` and `make ui` pass at the end of every task that touches their inputs.
- The global rules' "TypeSafe AI skill" is a placeholder: the installed `typesafe-ai` skill is an AI-feature SDK, not a decision scorer. Agents proceed without it and say so in their reports.
- Commits: conventional, explain why, and end with exactly one trailer line:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  ```

## Review Focus

1. **Bad input must never reach Docker.** A `POST /api/networks` with an overlapping subnet, a gateway outside the subnet (or equal to the network/broadcast address), an `ipRange` outside the subnet, a `parent` on a bridge, a missing `parent` on macvlan, `ipvlanMode` on a non-ipvlan driver, or a bad name answers `400 {error, fields}` naming the right field, and the fake engine records **no** create call; a name that already exists is 409. (T4 `TestNetworkCreateValidationTable`, T4 `TestNetworkCreateExistingNameIs409`, T4 `TestNetworkCreateFieldErrorsCallNoDocker`)
2. **Removal refusals are server-side and fresh.** `DELETE` of `bridge`/`host`/`none` is 409 "built-in"; of a network whose compose project is a registered stack is 409 naming the stack — including a stack registered moments ago; of a network with attached containers is 409 naming up to three of them; the engine records no remove call in any of these. A network removed between list and click is 404. (T4 `TestNetworkDeleteRefusals`, T4 `TestNetworkDeleteReadsOwnershipFresh`, T4 `TestNetworkDeleteUnknownIs404`)
3. **Docker says no.** A create that races another client (Docker 409) answers 409 with Docker's text verbatim; a create Docker rejects for an unknown parent interface answers 400 with Docker's text; a remove that races an attaching container (Docker 409 "has active endpoints") answers 409 with Docker's text; Docker down is 503 everywhere except `/api/host/interfaces`. (T1 `TestCreateNetworkMapsErrors`, T4 `TestNetworkCreateDockerErrorsPassThrough`, T4 `TestNetworkDeleteDockerConflictIs409`, T5 `TestHostInterfacesWorksWithoutDocker`)
4. **Host interfaces are right and harmless.** Only up, non-loopback, IPv4-bearing, non-virtual interfaces are listed; the default route's gateway is attributed to its interface and that interface sorts first; a missing or malformed `/proc/net/route` still lists interfaces (no gateway, no default); non-Linux answers `[]`; nothing but `/proc/net/route` is read. (T2 `TestInterfacesFiltersAndSorts`, T2 `TestDefaultRouteParsing`, T2 `TestInterfacesWithoutRouteFile`, T2 `TestInterfacesNonLinuxIsEmpty`, T5 `TestHostInterfacesBody`)
5. **A host with many networks and an open stack form.** 50 concurrent `GET /api/networks` share one computation; per-network inspects never exceed 4 in flight; the list is fresh right after a create or remove; the picker's "Create network…" option selects the new network and restores the old value on cancel. (T3 `TestNetworkListSingleFlight50`, T3 `TestNetworkListInspectConcurrencyBounded`, T4 `TestNetworkListFreshAfterCreateAndDelete`, T7 `selects the created network` / `restores the previous value on cancel`)

---

## File Structure

```
internal/engine/client.go            postJSON/send split; Networks() richer decode; InspectNetwork, CreateNetwork, RemoveNetwork (T1)
internal/engine/types.go             Network (+Scope, Internal, Options, Labels, Containers), Subnet.IPRange, NetworkEndpoint, NetworkSpec, ErrNetworkNotFound (T1)
internal/engine/testdata/            networks.json (extended), inspect_network.json (T1)
internal/host/ifaces.go              Interface, RawInterface, InterfaceLister, OSInterfaces, readDefaultRoute (T2)
internal/host/ifaces_test.go, testdata/proc/{a,b}/net/route (T2)
internal/apitypes/types.go           Network (extended), NetworkContainer, NetworkCreateRequest (+ types_test.go key contract) (T3, T4)
internal/api/flightcache.go          flightCache[T] extracted from containers.go (T3)
internal/api/containers.go           uses flightCache (T3)
internal/api/networks.go             NetworkEngineAPI; GET list (T3); POST, DELETE (T4)
internal/api/networks_validate.go    validateNetworkCreate, toNetworkSpec (T4)
internal/api/networks_test.go, networks_internal_test.go (T3, T4)
internal/api/stacks.go               GET /api/networks route removed from registerStackRoutes (T3)
internal/api/stacks_test.go, client_contract_test.go  fake network engine for the moved route (T3)
internal/api/server.go               Options.NetworkEngine (T3), Options.Interfaces (T5)
internal/api/dashboard.go            GET /api/host/interfaces (T5)
internal/cli/serve.go                wiring (T3, T5)
web/src/lib/api/types.ts, client.ts  Network (extended), NetworkContainer, CreateNetworkRequest, HostInterface; api.createNetwork/deleteNetwork/hostInterfaces (T6)
web/src/lib/stores/refreshList.ts    createRefreshList<T> extracted from containers/list.ts (T6)
web/src/lib/containers/list.ts       createContainerList wraps createRefreshList (T6)
web/src/lib/networks/networks.ts     owner/removal/sort/autofill helpers; createNetworkList (T6)
web/src/lib/networks/CreateNetworkDialog.svelte (+ test) (T7)
web/src/lib/stacks/NetworkPicker.svelte (+ test)  "Create network…" option (T7)
web/src/lib/shell/Rail.svelte, breadcrumb.ts, icons.ts (+ shell.test.ts)  Networks entry (T8)
web/src/App.svelte (+ App.test.ts)   route /networks, title (T8)
web/src/routes/NetworkList.svelte (+ test)  list page (T8)
cmd/stackstead/e2e_test.go           networks e2e (T9)
docs/api.md, docs/security.md, README.md (T9)
```

## Execution waves

| Wave | Tasks | Depends on | Parallel? |
|---|---|---|---|
| 1 | T1 engine, T2 host interfaces, T6 web data + controllers | — | T1, T2, T6 are file-disjoint: run in three worktrees |
| 2 | T3 network list endpoint, T5 interfaces endpoint, T7 dialog + picker | T3 ← T1; T5 ← T2; T7 ← T6 | T3 and T5 both add one `Options` field and one `serve.go` line (trivial merge); T7 is disjoint from both |
| 3 | T4 create/delete endpoints, T8 Networks page + shell | T4 ← T3; T8 ← T6, T7 | T4 (Go) and T8 (web) are disjoint |
| 4 | T9 e2e + docs + final checks | all | — |

Every task: Sonnet implements test-first; Opus reviews; Fable does the final whole-branch review. Merge 10a before 10b (spec §11).

---

### Task 1: Engine — JSON POST helper, richer networks, inspect/create/remove

Spec §3 (engine access), §4.1 (source fields), §4.2 (Docker call), §4.3, §9 (engine tests).

**Files:**
- Modify: `internal/engine/client.go`, `internal/engine/types.go`, `internal/engine/testdata/networks.json`
- Create: `internal/engine/testdata/inspect_network.json`
- Test: `internal/engine/client_test.go`

**Interfaces:**
- Produces:
  ```go
  var ErrNetworkNotFound = errors.New("docker engine: no such network") // wrapped for Docker 404s on /networks/*
  type Subnet struct{ Prefix netip.Prefix; Gateway netip.Addr; IPRange netip.Prefix } // IPRange zero when unset
  type Network struct {
      ID, Name, Driver, Scope string
      Internal        bool
      Subnets         []Subnet
      Options, Labels map[string]string // driver options and labels; never nil
      Containers      []NetworkEndpoint // only InspectNetwork fills this; Networks() leaves it nil
  }
  func (n Network) Project() string    // Labels["com.docker.compose.project"]
  func (n Network) Parent() string     // Options["parent"]
  func (n Network) IPvlanMode() string // Options["ipvlan_mode"]
  type NetworkEndpoint struct{ ID, Name, IPv4 string } // IPv4 without the "/nn" Docker appends; "" when none
  type NetworkSpec struct{ Name, Driver, Subnet, Gateway, IPRange, Parent, IPvlanMode string; Internal bool }
  func (c *Client) InspectNetwork(ctx context.Context, id string) (Network, error)
  func (c *Client) CreateNetwork(ctx context.Context, spec NetworkSpec) (id string, err error)
  func (c *Client) RemoveNetwork(ctx context.Context, id string) error
  const labelCreated = "dev.stackstead.created"
  ```

**Behaviour:**
- `request` is split into building the `*http.Request` and `send(caller, ctx, req)` (reads, caps, classifies, maps non-2xx to `*ActionError` exactly as today); a new `postJSON(caller, ctx, path string, payload any) (*response, error)` marshals `payload`, sets `Content-Type: application/json` and goes through `send`. GET and body-less POST call sites are untouched.
- `Networks()` additionally decodes `Scope`, `Internal`, `Options`, `Labels` and each IPAM config's `IPRange` (parsed like `Subnet`; a malformed or IPv6 range is dropped, the subnet kept). IPv6 and malformed subnets are still skipped. Maps are never nil.
- `InspectNetwork`: `GET /networks/<id>` with the client timeout; same decode as one list entry plus `Containers` from the inspect's `Containers` map (key = container id; `Name`; `IPv4Address` with its prefix length stripped), sorted by name; Docker 404 → wraps `ErrNetworkNotFound`; Docker 400 (ambiguous) → `*ActionError` unchanged.
- `CreateNetwork`: `POST /networks/create` through `postJSON`, bounded by `callerBound(ctx)` like `ContainerAction`. Body: `Name`, `CheckDuplicate: true`, `Driver`, `Internal`, `IPAM.Driver: "default"`, `IPAM.Config` with one entry carrying only the non-empty `Subnet`/`Gateway`/`IPRange` (no `IPAM.Config` at all when `Subnet` is empty), `Options` with `parent` and/or `ipvlan_mode` only when set, `Labels` `{dev.stackstead.created: "true"}`. Returns Docker's `Id`. Non-2xx → `*ActionError` (status and message preserved; no 409/404 remapping here, the API layer decides).
- `RemoveNetwork`: `DELETE /networks/<id>`; 204 → nil; 404 → wraps `ErrNetworkNotFound`; other non-2xx (403 built-in, 409 active endpoints) → `*ActionError`.
- Driver and name validation is **not** the engine's job (T4).

- [ ] **Step 1: Write the failing tests** — extend `TestNetworks` (fixture gains a 6th entry `lan`: macvlan, `Scope: local`, `Options {parent: enp3s0}`, `Labels {com.docker.compose.project: dns}`, IPAM config with `IPRange`; assert `Scope`, `Internal`, `Parent()`, `Project()`, `Subnets[0].IPRange`, and that `bridge` has empty but non-nil maps and a zero `IPRange`), `TestInspectNetworkDecodes` (fixture with two containers, `IPv4Address: "192.168.0.30/24"` → `IPv4 "192.168.0.30"`, one container without an address → `""`, sorted by name), `TestInspectNetworkNotFound` (`errors.Is(ErrNetworkNotFound)`), `TestCreateNetworkBody` (fake socket captures method, path `/v1.41/networks/create`, `Content-Type`, and the decoded JSON: `CheckDuplicate` true, IPAM config entry with subnet/gateway/ipRange, `Options.parent`, `Options.ipvlan_mode` only for the ipvlan case, label present; bridge without subnet sends no `IPAM.Config` and no `Options`; returns the `Id`), `TestCreateNetworkMapsErrors` (409 `{"message":"network with name lan already exists"}` → `*ActionError{409, …}`; 400 → `*ActionError{400}`; socket missing → `ErrUnavailable`), `TestRemoveNetworkMapsStatus` (204 → nil; 404 → `ErrNetworkNotFound`; 409 `has active endpoints` → `*ActionError` with that message; asserts method DELETE and path), and that existing `TestContainerActionMapsStatus`/`TestInspectContainer*` still pass after the `request` split.
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/engine -run 'Network' -v` (compile errors on missing names; `TestNetworks` fails on count 5 ≠ 6).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/engine ./internal/state ./internal/stacks`, `go vet ./...`, `gofmt -l .`.
- [ ] **Step 5: Commit** — `feat: engine network inspect, create and remove` (why: the Networks screen needs attached containers, driver options and a way to create and remove networks without the docker CLI).

---

### Task 2: Host — interface listing with default route

Spec §3 (host interfaces), §4.4, §8 (reads `/proc/net/route` only), §9; Review Focus 4.

**Files:**
- Create: `internal/host/ifaces.go`, `internal/host/ifaces_test.go`, `internal/host/testdata/proc/a/net/route` (default via `eth0` to `192.168.0.1`, plus a non-default line and an `RTF_UP`-only line), `internal/host/testdata/proc/b/net/route` (two default routes with different metrics; the lower metric wins)

**Interfaces:**
- Produces:
  ```go
  type Interface struct {
      Name string `json:"name"`; IPv4 string `json:"ipv4"` /* "192.168.0.10/24" */; Subnet string `json:"subnet"` /* masked */
      Gateway string `json:"gateway"` /* "" when not the default route's interface */; Default bool `json:"default"`
  }
  type RawInterface struct{ Name string; Up, Loopback bool; Addrs []netip.Prefix } // what a Source yields
  type InterfaceLister struct{ ProcRoot, GOOS string; Source func() ([]RawInterface, error) } // zero values: "/proc", runtime.GOOS, OSInterfaces
  func (l InterfaceLister) Interfaces() ([]Interface, error) // never returns a nil slice
  func OSInterfaces() ([]RawInterface, error)                // net.Interfaces + (*net.Interface).Addrs, IPv4 prefixes only
  func readDefaultRoute(procRoot string) (iface string, gateway netip.Addr, ok bool, err error)
  var hiddenInterfacePrefixes = []string{"docker", "br-", "veth", "virbr", "cni", "flannel", "cali", "tailscale", "wg"}
  ```

**Behaviour:**
- `GOOS != "linux"` → `[]Interface{}`, nil, without calling `Source` or reading any file.
- Keep interfaces that are up, not loopback, have at least one IPv4 address (the first one is reported) and whose name has none of the hidden prefixes. `Subnet` is the masked prefix.
- `readDefaultRoute` parses `/proc/net/route` (header line; fields `Iface Destination Gateway Flags … Metric …`; little-endian hex addresses): a default route has `Destination == 00000000` and flags `RTF_UP|RTF_GATEWAY` (`0x3`); with several, the lowest `Metric` wins; `ok=false` when none. A missing or malformed file is **not** an error for `Interfaces()`: every entry gets `Gateway ""`, `Default false`.
- `Default`/`Gateway` are set on the interface named by the default route (only if it survived the filter). Sort: default first, then by name.
- `Source` errors propagate (the API answers 500).

- [ ] **Step 1: Write the failing tests** — `TestDefaultRouteParsing` (fixture `a` → `eth0`, `192.168.0.1`; fixture `b` → the lower-metric interface; a route file with only a non-gateway default line → `ok=false`), `TestInterfacesFiltersAndSorts` (fake source: `lo` loopback, `eth0` up `192.168.0.10/24`, `wlan0` down, `eth1` up IPv6-only, `docker0`, `br-1a2b`, `veth9f1`, `tailscale0`, `wg0`, `enp5s0` up `10.0.0.5/16` → exactly `eth0` (default, gateway) then `enp5s0` (no gateway); `Subnet` masked), `TestInterfacesWithoutRouteFile` (proc root without `net/route` → both listed, no gateway, no default, nil error), `TestInterfacesNonLinuxIsEmpty` (`GOOS: "darwin"`, a Source that fails the test if called → `[]`, nil), `TestInterfacesSourceError` (error propagates), `TestOSInterfacesRuns` (smoke: `OSInterfaces()` returns without error on the test host; no assertions on content).
- [ ] **Step 2: Run to verify they fail** — `go test ./internal/host -run 'Interfaces|DefaultRoute' -v`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/host`, `go vet ./...`, `GOOS=darwin go build ./...`, `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`.
- [ ] **Step 5: Commit** — `feat: host interface listing from net.Interfaces and /proc/net/route` (why: the create-network dialog pre-fills subnet and gateway so operators do not have to know them).

---

### Task 3: `GET /api/networks` — rich, cached list

Spec §4.1, §7 (503), §9; Review Focus 5.

**Files:**
- Create: `internal/api/flightcache.go`, `internal/api/networks.go`, `internal/api/networks_test.go`, `internal/api/networks_internal_test.go`
- Modify: `internal/api/containers.go` (use `flightCache`), `internal/api/stacks.go` (drop the `GET /api/networks` registration and `handleNetworks`), `internal/api/stacks_dto.go` (drop `toNetworks`), `internal/api/server.go` (Options, registration), `internal/apitypes/types.go` (+ `types_test.go`), `internal/api/stacks_test.go` + `client_contract_test.go` (fake network engine), `internal/cli/serve.go` (wiring)

**Interfaces:**
- Consumes: `engine.Network` (T1), `s.projectStacks(ctx)` (Plan 9), `shortID`.
- Produces:
  ```go
  // api
  type flightCache[T any] struct{ /* mu, at, body, ok, inflight, gen — as containerCache today */ }
  func (c *flightCache[T]) get(ctx context.Context, now time.Time, ttl, timeout time.Duration, compute func(context.Context) (T, error)) (T, error)
  func (c *flightCache[T]) invalidate()
  type NetworkEngineAPI interface {
      Networks(ctx context.Context) ([]engine.Network, error)
      InspectNetwork(ctx context.Context, id string) (engine.Network, error)
      CreateNetwork(ctx context.Context, spec engine.NetworkSpec) (string, error)
      RemoveNetwork(ctx context.Context, id string) error
  }
  // Options.NetworkEngine NetworkEngineAPI  — routes registered when Stacks != nil && NetworkEngine != nil
  const networkListTTL = 2 * time.Second; networkListTimeout = 10 * time.Second; networkInspectConcurrency = 4
  func (s *server) registerNetworkRoutes(mux *http.ServeMux)          // GET /api/networks (T3); POST, DELETE (T4)
  func (s *server) computeNetworkList(ctx context.Context) ([]apitypes.Network, error)
  func networkRow(n engine.Network, projectToStack map[string]string) apitypes.Network
  func isBuiltinNetwork(n engine.Network) bool // name bridge|host|none AND driver bridge|host|null
  // apitypes
  type Network struct {
      ID string `json:"id"`; Name string `json:"name"`; Driver string `json:"driver"`; Scope string `json:"scope"`
      Subnets []string `json:"subnets"`; Gateways []string `json:"gateways"`; IPRange string `json:"ipRange"`
      Parent string `json:"parent"`; IPvlanMode string `json:"ipvlanMode"`; Internal bool `json:"internal"`
      Builtin bool `json:"builtin"`; Project string `json:"project"`; Stack string `json:"stack"`
      Containers []NetworkContainer `json:"containers"`
  }
  type NetworkContainer struct{ Name string `json:"name"`; IPv4 string `json:"ipv4"` }
  ```

**Behaviour:**
- **Refactor first (checkbox Step 1, own commit):** move the cache/flight/invalidate logic out of `containers.go` into `flightCache[T]` with identical semantics (TTL check under the lock, detached context with timeout, panic → error, errors not cached, generation guard, each waiter on its own ctx). `server.containerCache` becomes `flightCache[[]apitypes.ContainerRow]`; `invalidateContainerList` calls `invalidate()`. All existing `TestContainerList*` and `TestListIsFreshAfterAction` pass unchanged.
- `server` gains `networkCache flightCache[[]apitypes.Network]`. `GET /api/networks`: `s.networkCache.get(…, networkListTTL, networkListTimeout, s.computeNetworkList)`. Compute: `Networks()` → `projectStacks()` → inspect every network with at most `networkInspectConcurrency` in flight (semaphore + WaitGroup) to get `Containers`; `ErrNetworkNotFound` for one network (removed meanwhile) drops that network; any other inspect error fails the computation (not cached). Rows sorted by name; `containers` sorted by name.
- `networkRow`: `ID` short; `subnets[i]`/`gateways[i]` from `Subnets` (`""` gateway when zero); `ipRange` from `Subnets[0].IPRange` or `""`; `parent`, `ipvlanMode` from the engine helpers; `builtin` per `isBuiltinNetwork`; `project` = `n.Project()`; `stack` from the map or `""`; empty slices never nil.
- Errors: `engine.ErrUnavailable` → 503 `{error: "docker engine is unavailable"}`; `context.DeadlineExceeded` → 503 "Docker did not answer in time"; `Stacks.List` error → 500; client gone → no write (reuse `writeEngineError`'s shape; a network-specific not-found text is only needed in T4).
- `stacks.go` no longer registers `GET /api/networks`; `TestNetworksAndStatus` keeps only the status assertion, and the networks assertion moves to `networks_test.go`; `client_contract_test.go` and `newObserveEnvWith` (or its caller) set a fake `NetworkEngine` whose list has one network `lan` with subnet `10.0.0.0/24`, so `c.Networks` still decodes one entry. The CLI client (`internal/client.Networks`, `networks ls`) decodes the extended type without change.
- `serve.go` passes `NetworkEngine: dockerEngine`.

- [ ] **Step 1 (refactor): extract `flightCache`**, run `go test -race ./internal/api -run 'ContainerList|ListIsFresh'` green, commit `refactor: generic single-flight list cache` (why: the networks list needs the same 2 s single-flight behaviour as the containers list).
- [ ] **Step 2: Write the failing tests** — `networks_internal_test.go`: `TestIsBuiltinNetwork` table (name+driver pairs incl. a user network named `bridge2` and a macvlan named `host` → false), `TestNetworkRowShapes` (nil subnets → `[]`, gateway pairing with `""`, ipRange, parent/ipvlanMode, project→stack). `networks_test.go` (fake `NetworkEngineAPI` recording calls, with per-id inspect fixtures and a configurable max-in-flight counter): `TestNetworkListJoinsSources` (exact JSON for three networks: built-in `bridge`, compose `dns_default` owned by registered stack `dns` with two containers, macvlan `lan` unmanaged with one container; key order per spec example; `containers: []` for bridge), `TestNetworkListExistingFieldsUnchanged` (decode into the old `{name, driver, subnets}` shape still works), `TestNetworkListSingleFlight50`, `TestNetworkListCachedFor2s`, `TestNetworkListInspectConcurrencyBounded` (12 networks, a gated fake inspect → max in flight ≤ 4; all 12 listed), `TestNetworkListDropsNetworkRemovedDuringInspect`, `TestNetworkListUnavailableIs503`, `TestNetworkListRequiresSession`, `TestNetworkRoutesAbsentWithoutEngine` (no `NetworkEngine` → 404 JSON after auth), and the moved `lan` assertion from `TestNetworksAndStatus`; `apitypes` key contract rows for `Network` (`builtin,containers,driver,gateways,id,internal,ipRange,ipvlanMode,name,parent,project,scope,stack,subnets`) and `NetworkContainer` (`ipv4,name`).
- [ ] **Step 3: Run to verify they fail** — `go test ./internal/api -run 'Network' -v`; `go test ./internal/apitypes`.
- [ ] **Step 4: Implement** (+ `serve.go` wiring; remove the stacks-side route and DTO).
- [ ] **Step 5: Run** `go test -race ./internal/api ./internal/apitypes ./internal/client ./internal/cli`, `go test ./...`, `go vet ./...`.
- [ ] **Step 6: Commit** — `feat: rich cached network list endpoint` (why: the Networks screen and removal rules need containers, owner and driver details; one computation per 2 s keeps many tabs cheap).

---

### Task 4: `POST /api/networks` and `DELETE /api/networks/{id}`

Spec §4.2, §4.3, §7, §8; Review Focus 1–3, 5.

**Files:**
- Create: `internal/api/networks_validate.go`
- Modify: `internal/api/networks.go` (+ `networks_test.go`, `networks_internal_test.go`), `internal/apitypes/types.go` (+ `types_test.go`)

**Interfaces:**
- Consumes: `stacks.CheckStaticIP` (gateway checks via a synthetic one-subnet `engine.Network`), `netip.Prefix.Overlaps`, `decodeJSON`, `fieldErrorBody`, `s.projectStacks`, `s.networkCache.invalidate()`.
- Produces:
  ```go
  // apitypes
  type NetworkCreateRequest struct {
      Name string `json:"name"`; Driver string `json:"driver"`; Subnet string `json:"subnet"`; Gateway string `json:"gateway"`
      IPRange string `json:"ipRange"`; Parent string `json:"parent"`; IPvlanMode string `json:"ipvlanMode"`; Internal bool `json:"internal"`
  }
  // api
  var networkNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
  var interfaceNameRe = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,15}$`)
  func validateNetworkCreate(req apitypes.NetworkCreateRequest, existing []engine.Network) []apitypes.FieldError // pure; never calls Docker
  func toNetworkSpec(req apitypes.NetworkCreateRequest) engine.NetworkSpec // ipvlanMode defaults to "l2" for ipvlan
  func (s *server) resolveNetwork(ctx context.Context, ref string) (engine.Network, bool, error) // exact id/name, then unique 12–64 hex prefix
  ```

**Behaviour:**
- Validation (field names are the JSON names; one error per field, first failure wins; all fields checked so the UI can show several): `name` regex; `driver` ∈ bridge|macvlan|ipvlan; `subnet` required for macvlan/ipvlan, optional for bridge, IPv4 CIDR in masked form (`10.0.0.0/24`, not `10.0.0.5/24`), must not `Overlaps` any existing network's IPv4 subnet (message names that network); `gateway` requires `subnet`, then `CheckStaticIP("gateway", name, gateway, synthetic, nil)` messages pass through; `ipRange` requires `subnet`, IPv4 CIDR, inside `subnet`; `parent` required for macvlan/ipvlan, forbidden for bridge, `interfaceNameRe`; `ipvlanMode` only for ipvlan, `l2`|`l3`|`""`; `internal` any bool.
- `POST /api/networks`: `decodeJSON` → `Networks()` fresh (503 if unavailable) → field errors → `400 fieldErrorBody` → existing name (exact match) → `409 {error: "network <name> already exists"}` → `CreateNetwork(ctx bounded by 30 s, toNetworkSpec(req))` → `*ActionError` 409 → 409 with Docker's message, other `*ActionError` → 400 with Docker's message, unavailable → 503 → `invalidate()` → `InspectNetwork(id)` + `projectStacks` → `201` with the §4.1 row. Log Info `network created` with name, driver, parent.
- `DELETE /api/networks/{id}`: `resolveNetwork` (404 "network not found" when none or an ambiguous prefix; 503 unavailable) → `InspectNetwork` fresh (404 on `ErrNetworkNotFound`) → refusals in this order, each 409: `isBuiltinNetwork` → "`<name>` is a built-in network"; fresh `projectStacks` says the project is registered → "`<name>` belongs to stack `<stack>`; remove it with the stack"; `len(Containers) > 0` → "`<name>` is in use by `a`, `b`, `c` (+N more)" (first three by name) → `RemoveNetwork` → `ErrNetworkNotFound` 404; `*ActionError` (Docker 403/409) → 409 with Docker's message; unavailable 503 → `invalidate()` → 204. Log Info `network removed` with name; refusals logged at Info with the reason.
- Both mutations also `invalidateContainerList()` — container `networks` maps change when a network goes away.

- [ ] **Step 1: Write the failing tests** — `networks_internal_test.go`: `TestNetworkCreateValidationTable` (≥ 14 rows: valid bridge without subnet; valid macvlan; bad name `-x`, 64-char name; unknown driver; macvlan without subnet; unmasked subnet; IPv6 subnet; overlap with `172.17.0.0/16` names `bridge`; gateway without subnet; gateway outside; gateway = network address; gateway = broadcast; ipRange outside; parent on bridge; missing parent on ipvlan; parent `eth0!`; `ipvlanMode` on macvlan; `l9` on ipvlan — each asserting the `field` and a message fragment), `TestToNetworkSpecDefaults` (ipvlan mode default `l2`; bridge sends no parent). `networks_test.go`: `TestNetworkCreateHappyPath` (201 body equals the inspect-derived row incl. `builtin:false`, `stack:""`; the fake engine recorded exactly one create with the expected `NetworkSpec`), `TestNetworkCreateFieldErrorsCallNoDocker` (400 `{error, fields}`, zero create calls), `TestNetworkCreateExistingNameIs409`, `TestNetworkCreateDockerErrorsPassThrough` (fake create returns `*ActionError{409,"…already exists"}` → 409 verbatim; `*ActionError{400,"failed to create … no such device"}` → 400 verbatim; `ErrUnavailable` → 503), `TestNetworkCreateRequiresJSON` (form post → 415), `TestNetworkCreateRejectsUnknownFields`, `TestNetworkListFreshAfterCreateAndDelete` (list, create → list shows it within the same 2 s window; delete → gone), `TestNetworkDeleteRefusals` (table: `bridge` built-in; `dns_default` stack-owned names `dns`; `lan` with 4 containers names 3 + "+1 more"; each 409 and zero remove calls), `TestNetworkDeleteReadsOwnershipFresh` (first delete of an unregistered compose network proceeds; registering its project in the fake stacks between requests → second is 409), `TestNetworkDeleteByIdNameAndPrefix` (full id, 12-hex prefix, exact name all resolve; ambiguous prefix 404), `TestNetworkDeleteUnknownIs404`, `TestNetworkDeleteDockerConflictIs409` (fake remove returns `*ActionError{409,"has active endpoints"}` → 409 verbatim), `TestNetworkDeleteSuccessIs204AndInvalidates`, `TestNetworkMutationsRequireSession`; `apitypes` key contract row for `NetworkCreateRequest` (`driver,gateway,internal,ipRange,ipvlanMode,name,parent,subnet`).
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/api ./internal/apitypes`, `go test ./...`, `go vet ./...`.
- [ ] **Step 5: Commit** — `feat: network create and remove endpoints with server-side refusals` (why: a LAN network for static IPs must be creatable from the UI, and removal must be safe whatever the client sends).

---

### Task 5: `GET /api/host/interfaces`

Spec §4.4, §7 (works without Docker); Review Focus 3, 4.

**Files:**
- Modify: `internal/api/dashboard.go` (+ `dashboard_test.go`), `internal/api/server.go` (Options), `internal/cli/serve.go` (wiring)

**Interfaces:**
- Consumes: `host.Interface`, `host.InterfaceLister` (T2).
- Produces:
  ```go
  type InterfacesAPI interface{ Interfaces() ([]host.Interface, error) } // satisfied by host.InterfaceLister
  // Options.Interfaces InterfacesAPI — route registered only when set
  ```

**Behaviour:** `GET /api/host/interfaces` with `requireSession`, registered in `registerDashboardRoutes` when `opts.Interfaces != nil`; body is the lister's slice (never `null`); lister error → 500 `{error: "internal error"}` (logged). It needs no Docker and no `Observe`/`Stacks`. `serve.go` passes `Interfaces: host.InterfaceLister{ProcRoot: "/proc", GOOS: runtime.GOOS}`.

- [ ] **Step 1: Write the failing tests** — `TestHostInterfacesBody` (fake lister with two entries → exact JSON `[{"name":"enp3s0","ipv4":"192.168.0.10/24","subnet":"192.168.0.0/24","gateway":"192.168.0.1","default":true}, …]`), `TestHostInterfacesEmptyIsArray` (`[]`, not `null`), `TestHostInterfacesWorksWithoutDocker` (env with `Interfaces` set but no `Stacks`/`Observe`/`NetworkEngine` → 200), `TestHostInterfacesErrorIs500`, `TestHostInterfacesRequiresSession`, `TestHostInterfacesAbsentWithoutSource`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement** (+ wiring).
- [ ] **Step 4: Run** `go test -race ./internal/api`, `go test ./...`, `go vet ./...`, `GOOS=darwin go build ./...`.
- [ ] **Step 5: Commit** — `feat: host interfaces endpoint` (why: the create-network dialog pre-fills subnet and gateway from the host's interfaces).

---

### Task 6: Web data layer, shared refreshing-list controller, network helpers

Spec §4 (wire shapes), §6.1 (refresh), §6.2 (auto-fill rule, removal reasons).

**Files:**
- Create: `web/src/lib/stores/refreshList.ts` (+ `refreshList.test.ts`), `web/src/lib/networks/networks.ts` (+ `networks.test.ts`)
- Modify: `web/src/lib/api/types.ts`, `client.ts` (+ `client.test.ts`), `web/src/lib/containers/list.ts` (+ `list.test.ts` unchanged)

**Interfaces:**
- Produces:
  ```ts
  // types.ts — mirrors internal/apitypes (authoritative) and host.Interface
  export interface NetworkContainer { name: string; ipv4: string }
  export interface Network { id; name; driver; scope; subnets: string[]; gateways: string[]; ipRange; parent; ipvlanMode;
    internal: boolean; builtin: boolean; project; stack; containers: NetworkContainer[] }
  export type NetworkDriver = 'bridge' | 'macvlan' | 'ipvlan';
  export interface CreateNetworkRequest { name: string; driver: NetworkDriver; subnet?; gateway?; ipRange?; parent?; ipvlanMode?: 'l2' | 'l3'; internal?: boolean }
  export interface HostInterface { name: string; ipv4: string; subnet: string; gateway: string; default: boolean }
  // client.ts
  api.networks(): Promise<Network[]>                       // unchanged path, richer type
  api.createNetwork(body: CreateNetworkRequest): Promise<Network>   // POST /api/networks
  api.deleteNetwork(id: string): Promise<void>             // DELETE /api/networks/<encoded id>, no body
  api.hostInterfaces(): Promise<HostInterface[]>           // GET /api/host/interfaces
  // stores/refreshList.ts
  export interface RefreshListDeps<T> { list: () => Promise<T[]>; changes: Readable<LiveChange | null>; key: (row: T) => string; doc?: Document }
  export function createRefreshList<T>(deps: RefreshListDeps<T>):
    { rows: Readable<T[] | undefined>; error: Readable<string | undefined>; patch(fresh: T): void; refresh(): void; start(): void; stop(): void }
  // containers/list.ts — createContainerList(deps) = createRefreshList<ContainerRow>({ ...deps, key: (r) => r.id }); same return type as today
  // networks/networks.ts
  export type NetworkOwner = { kind: 'builtin' } | { kind: 'stack'; stack: string } | { kind: 'compose'; project: string } | { kind: 'unmanaged' };
  export function networkOwner(n: Network): NetworkOwner
  export function removeDisabledReason(n: Network): string | undefined // undefined = removable
  export function sortNetworks(rows: Network[]): Network[]            // by name
  export function needsParent(driver: NetworkDriver): boolean
  export function autofill(iface: HostInterface, current: { subnet: string; gateway: string }, lastAuto: { subnet: string; gateway: string }): { subnet: string; gateway: string }
  export function createNetworkList(deps: { list: () => Promise<Network[]>; changes: Readable<LiveChange | null>; doc?: Document }) // createRefreshList keyed by id
  ```

**Behaviour:**
- `createRefreshList` is the Plan 9 controller verbatim with `ContainerRow` replaced by `T` and `patch` matching on `key`; `createContainerList` keeps its signature so `ContainerList.svelte`/`ContainerDetail.svelte` and `list.test.ts` are untouched.
- `removeDisabledReason`: built-in → "Built-in networks cannot be removed"; stack-owned → "Owned by stack <name>"; compose project without a stack is removable only when no containers; containers → "In use by N container(s)".
- `autofill`: sets `subnet`/`gateway` from the interface when the current value is empty or equals the last auto-filled value (the user's own edits are never overwritten); the interface's `gateway` may be `""` (then gateway is left alone).

- [ ] **Step 1: Write the failing tests** — `client.test.ts`: create POSTs the body with JSON content type and returns the row; delete sends DELETE to the encoded path with no body and resolves on 204; hostInterfaces GETs the path. `refreshList.test.ts`: the Plan 9 controller cases re-run against a generic row type (fetch on start, change debounce 300 ms / max 1 s, resync immediate, 10 s interval visible only, pause/resume, stale response ignored, in-flight skip, `patch` by key); `list.test.ts` unchanged and green. `networks.test.ts`: owner table (builtin/stack/compose/unmanaged), removal reasons incl. the plural, sort, `needsParent`, `autofill` (fills empty; keeps user edits; replaces values equal to the last auto-fill; leaves gateway alone when the interface has none).
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `cd web && npm run check && npm test && npm run build`.
- [ ] **Step 5: Commit** — two commits: `refactor: shared refreshing-list controller` (why: the Networks list needs the containers list's refresh rules) and `feat: network API client and list helpers`.

---

### Task 7: Create network dialog and the picker option

Spec §6.2, §6.3, §6.6, §7; Review Focus 5.

**Files:**
- Create: `web/src/lib/networks/CreateNetworkDialog.svelte` (+ `CreateNetworkDialog.test.ts`)
- Modify: `web/src/lib/stacks/NetworkPicker.svelte` (+ `NetworkPicker.test.ts`)

**Interfaces:**
- Produces:
  ```ts
  // CreateNetworkDialog props
  { open: boolean; onclose: () => void; oncreated: (network: Network) => void }
  // NetworkPicker: unchanged props; the select gets a last <option value="__create__">Create network…</option>
  ```

**Behaviour (dialog):**
- Built on `Dialog` (title "Create network"); form with `Field`s and stable ids `net-name`, `net-driver`, `net-parent`, `net-subnet`, `net-gateway`, `net-iprange`, `net-ipvlan-mode`, `net-internal`. Driver is a segmented control: a `fieldset` with `role="radiogroup"` + `aria-label="Driver"`, three radios Bridge / Macvlan / IPvlan (default Bridge).
- On open: reset the form and fetch `api.hostInterfaces()` once; failure or `[]` → no select, a plain **Parent interface** text input. With interfaces: **Host interface** `<select>` with options "`<name>` — `<ipv4>`" (" (default)" suffix on the default one, preselected) plus a last option "Other…" that reveals the text input; the select and text input are rendered only for macvlan/ipvlan.
- Picking an interface applies `autofill` (T6) to Subnet and Gateway and remembers the auto-filled pair.
- Subnet, Gateway, IP range (optional) text inputs with `mono`; Subnet required hint for macvlan/ipvlan; IPvlan mode select (`l2`/`l3`, ipvlan only); "Advanced" `<details>` with an Internal checkbox.
- Macvlan/ipvlan show the persistent note (role `note`, text exactly): "The Docker host itself cannot reach containers on this network. Pick addresses outside your router's DHCP pool, or set an IP range outside it."
- Submit: `Button` primary "Create network" with `loading`; the body omits empty optional fields; `ApiError.fields` map by `field` to the `Field.error` props, unknown fields and non-field errors (409, 503, 400 without fields) go to a `Banner kind="danger"` inside the dialog; on success `oncreated(network)` then `onclose()`. Cancel/Esc → `onclose()`; nothing is sent.

**Behaviour (picker):**
- The "Create network…" option is appended last (not shown in the free-text fallback, disabled when `readonly`). Choosing it does **not** call `set`; it records the row index and opens one shared dialog instance. On `oncreated`: reload `api.networks()`, then `set(rename(item, created.name))` for that row. On close without creation: the select shows the row's previous `item.name` again (force the `<select>` value back; the parent's value never changed).

- [ ] **Step 1: Write the failing tests** — `CreateNetworkDialog.test.ts`: renders bridge fields only by default and no parent/mode controls; switching to Macvlan shows the interface select (fetched once), the note, the required subnet hint; switching to IPvlan adds the mode select; choosing `enp3s0` fills subnet `192.168.0.0/24` and gateway `192.168.0.1`; a typed subnet survives a later interface change; "Other…" reveals the text input; empty interface list renders the text input only; submit posts the exact body for macvlan (`{name, driver, subnet, gateway, parent}`; no `ipRange`/`ipvlanMode`/`internal` keys when unset) and calls `oncreated` with the response then `onclose`; a 400 with fields `subnet` and `parent` puts the messages on those fields and nothing in the banner; a 409 shows the banner text; Esc calls `onclose` and sends nothing; the submit button is busy while the request is in flight (double submit sends one request). `NetworkPicker.test.ts` (added, existing cases unchanged): `offers Create network… last`; `selects the created network` (choose the option → dialog visible → fake `POST /api/networks` 201 `lan` and the refreshed `GET /api/networks` includes `lan` → `onchange` called with `{name: 'lan', ipv4Address: ''}` and the option list includes it; `GET /api/networks` was fetched exactly twice); `restores the previous value on cancel` (`onchange` never called; select value back to `frontend`); no create option in the free-text fallback; option disabled when `readonly`.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm run check && npm test && npm run build`.
- [ ] **Step 5: Commit** — `feat: create network dialog and picker option` (why: a static IP needs a network that exists; creating it where it is needed removes a trip to the host shell).

---

### Task 8: Shell entry and the Networks page

Spec §6.1, §6.6, §7; Global Constraints (refresh, a11y, responsive).

**Files:**
- Create: `web/src/routes/NetworkList.svelte` (+ `NetworkList.test.ts`)
- Modify: `web/src/lib/shell/Rail.svelte`, `breadcrumb.ts`, `icons.ts` (+ `shell.test.ts`), `web/src/App.svelte` (+ `App.test.ts`)

**Interfaces:**
- Consumes: `createNetworkList`, `networkOwner`, `removeDisabledReason`, `sortNetworks` (T6), `CreateNetworkDialog` (T7), `ConfirmDialog`, `Button`, `Banner`, `EmptyState`, `Spinner`, `toasts`, `changes`.
- Produces: `ICONS.network` (stroke icon in the existing family: three nodes joined by lines); Rail item `{ href: '/networks', label: 'Networks', icon: 'network', match: p => p === '/networks' }` after Containers; `breadcrumb('/networks')` → `[{ label: 'Networks' }]`; App view `'networks'` → `<NetworkList />`, title "Networks · Stackstead".

**Behaviour:**
- Page: `h1#networks-heading` "Networks", count "N networks"; header `Button` primary "Create network" opening `CreateNetworkDialog`; `oncreated` → toast success "Network `<name>` created" + `list.refresh()`.
- Table (`role="table"` + explicit roles, `data-label`s, same ≤ 720 px card rules as `ContainerList`): Name (mono; when `containers.length > 0` an expand `button` with `aria-expanded`/`aria-controls` pointing at the detail row, `aria-label="Show containers on <name>"`), Driver, Subnet (first + `+N`, `—` when none), Gateway (first, `—`), Parent (`—`), Containers (count), Owner chip: `built-in` / `stack <name>` as a link to `/stacks/<name>` / `compose <project>` / `unmanaged`, Actions.
- Expanded detail row: one line per container "`<name>` → `<ipv4>`", name linking to `/containers/<encoded name>`, `—` when the IP is empty.
- Remove: `Button` danger `size="sm"`, soft-disabled with `disabledReason` from `removeDisabledReason`; `ConfirmDialog` title "Remove network `<name>`?", message "Docker deletes the network. Containers can only attach again once it is recreated.", `confirmLabel="Remove"`, `danger`; on confirm `api.deleteNetwork(id)`; success → toast "Network `<name>` removed" + refresh; error → toast danger with the server message + refresh (the row's reason updates from the fresh list); one request per click.
- Refresh through `createNetworkList({ list: api.networks, changes })`: start/stop with the page; no timers while hidden (inherited).
- Loading, empty ("No networks" cannot really happen; keep the state for a Docker that lists none), error + Retry states as in `ContainerList`.

- [ ] **Step 1: Write the failing tests** — `shell.test.ts`: Rail has a Networks item right after Containers, current only on `/networks`; breadcrumb. `App.test.ts`: `/networks` renders the page heading and sets the title. `NetworkList.test.ts` (fake fetch for `GET /api/networks`, `DELETE`, `POST`; `live` mocked as in `ContainerList.test.ts`): rows sorted by name with driver/subnet/gateway/parent/count; owner chips (built-in, stack link href, compose project, unmanaged); expand button toggles the container rows with links and `aria-expanded`; Remove soft-disabled with the right reason for built-in, stack-owned and in-use rows; removable row → confirm dialog → DELETE sent once → success toast and refetch; DELETE 409 → danger toast with the server text; Create network button opens the dialog and a created network toasts and refetches; error banner + Retry; no horizontal overflow assumptions beyond `data-label`s present on every cell.
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm run check && npm test && npm run build` (budgets).
- [ ] **Step 5: Commit** — `feat: Networks page` (why: operators need to see which networks exist, who owns them and what is attached before creating or removing one).

---

### Task 9: E2E, docs, final checks, visual pass

Spec §9 (integration, E2E), §10.

**Files:** Modify `cmd/stackstead/e2e_test.go`, `docs/api.md`, `docs/security.md`, `README.md`.

**Behaviour:**
- E2E (inside `dockerStack`, a new `t.Run("networks", …)` calling `networkEndpoints(t, c)`): `GET /api/host/interfaces` → 200 and a JSON array (content not asserted; on Linux additionally at most one `default: true`); `GET /api/networks` lists `bridge` with `builtin: true` and the smoke stack's `<name>_default` with `stack: <name>` and one container; `DELETE /api/networks/bridge` → 409 "built-in"; `DELETE /api/networks/<name>_default` → 409 naming the stack; `POST /api/networks` bridge `e2e-net-<rand>` with subnet `10.213.<r>.0/24` and gateway `.1` (retry with another `<r>` on a 400 overlap, three attempts) → 201 with `builtin:false`, `stack:""`, `containers: []`, `subnets`/`gateways` echoed; `POST` again with the same name → 409; `docker run -d --rm --network e2e-net-<rand> --name ss-e2e-net busybox sleep 300` (cleanup in `t.Cleanup`) → `GET` shows one container with an IP in the subnet → `DELETE` → 409 naming `ss-e2e-net` → `docker rm -f` → `DELETE` → 204 → `GET` no longer lists it; anonymous `GET /api/networks`, `POST /api/networks`, `DELETE /api/networks/x`, `GET /api/host/interfaces` → 401. Cleanup also `docker network rm` the test network if it survived.
- Docs: `docs/api.md` overview table gets rows for `GET`/`POST /api/networks`, `DELETE /api/networks/{id}` and `GET /api/host/interfaces` (the fs/status row keeps only `fs` and `status`), plus a "### Networks" section after "### Containers" describing the §4.1 fields, `builtin`/`project`/`stack`, 2 s caching, the §4.2 body and validation rules, the §4.3 refusals, status codes (400 fields, 404, 409, 503) and the interfaces body with the Linux-only and in-container notes. `docs/security.md` "The short version" gets a bullet: macvlan/ipvlan networks put containers directly on the LAN, bypassing Docker's port publishing, so host firewall rules do not apply to them; `parent` goes to Docker as a driver option only, the interface list reads `/proc/net/route` only. README "What you get" gets one line: "**Networks.** Create LAN (macvlan/ipvlan) and bridge networks from the UI, give services static IPs on them, and see which containers use each network." — header, badges and table untouched.
- Final checks: `make check`; `make ui` (budgets); `env PATH=/usr/bin:/bin:$(go env GOROOT)/bin go test ./...`; `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...`; `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`; `GOOS=darwin go build ./...`; the e2e with Docker.
- Visual pass (headless Chrome as in Plan 9, scratchpad config/data, the throwaway compose stack plus one user bridge network): `/networks` (collapsed and expanded row, Remove reasons) and the Create network dialog on Bridge and Macvlan at 1280 and 375, dark and light; report issues, fix before the final review; clean up networks, containers and processes.

- [ ] **Step 1: Write the e2e assertions**, run them (prove they can fail by a temporary wrong expectation if they pass first time, then restore).
- [ ] **Step 2: Write the docs.**
- [ ] **Step 3: Final checks and visual pass.**
- [ ] **Step 4: Commit** — `test: networks e2e` and `docs: networks API and security notes`.
- [ ] **Step 5: Hand off** for the Fable final review (any visual findings fixed first), then merge to `main` before Plan 10b.

---

## Spec coverage (self-review)

| Spec item | Task |
|---|---|
| §2 engine create/remove/richer list | T1 |
| §2 / §4.1 `GET /api/networks` extended, backward compatible, 2 s cache, inspect concurrency 4 | T3 |
| §2 / §4.2 `POST /api/networks` validation, Docker call, labels, 409/400 mapping | T4 |
| §2 / §4.3 `DELETE /api/networks/{id}` refusals, id or name, 404 | T4 |
| §2 / §4.4 `GET /api/host/interfaces` Linux-only, filters, default route, sort | T2 (lister), T5 (route) |
| §3 engine access over the socket, ownership rule, interfaces source | T1, T3/T4, T2 |
| §6.1 rail, route, breadcrumb, table, owner chip, Remove, refresh | T8 (+ T6 controller) |
| §6.2 dialog fields, auto-fill, note, field errors, `oncreated` | T7 (+ T6 `autofill`) |
| §6.3 picker "Create network…" | T7 |
| §6.6 dialog a11y, responsive table | T7, T8 |
| §7 Docker unreachable 503, races passed through, interfaces without Docker | T3, T4, T5 |
| §8 session + JSON rule, `parent` never shelled, security.md note | T4, T2, T9 |
| §9 engine/api/host/web/E2E tests | T1–T8 steps, T9 |
| §10 api.md, security.md, README line | T9 |
| §11 Plan 10a scope, merge before 10b | this plan |

Deferred, recorded for the user: removing `StacksAPI.Networks` (unused by the router after T3) waits until 10b has merged; the UI does not validate `parent` against the interface list (spec §4.2); IPv6, overlay, labels/options beyond `parent`/`ipvlan_mode`, connect/disconnect and the host↔macvlan shim are out of scope (spec §2).
