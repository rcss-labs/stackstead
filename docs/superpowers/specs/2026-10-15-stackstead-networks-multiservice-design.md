# Stackstead Networks and multi-service stacks — Design

**Date:** 2026-10-15
**Status:** Draft, awaiting review. Adds Docker network management (a **Networks** screen) and makes stacks multi-service in the UI: create with many services, add and remove services on an existing stack. Implemented by Plans 10a (Networks) and 10b (Multi-service). Builds on `2026-10-14-stackstead-v1-design.md`, `2026-10-15-stackstead-ui-v2-design.md` and `2026-10-15-stackstead-containers-design.md`.

## 1. Purpose

Two gaps block common homelab setups such as "a `dns` stack with Pi-hole on the LAN at `192.168.0.30`":

1. **No way to create a network.** A service can be given a static IPv4 only on a network that already exists in Docker (`NetworkPicker` lists `GET /api/networks`; `CheckStaticIP` rejects unknown networks). A LAN address needs a macvlan network, which today must be created with `docker network create` on the host.
2. **Create is single-service.** `POST /api/stacks` takes exactly one `service` + `config`, and existing stacks can only gain or lose a service through the raw YAML editor. A compose stack is normally several services.

Success criteria:

- From the browser alone, an operator can create a macvlan network on a host interface (subnet and gateway pre-filled from that interface), then create stack `dns` with service `pihole` at static IP `192.168.0.30` on it, and start it.
- `/networks` lists every Docker network with driver, subnet, gateway, parent interface, attached containers and owner; unmanaged, unused, non-built-in networks can be removed.
- New stack accepts 1–20 services in one submit; validation errors point at the right service card and field.
- Stack detail can add a service and remove a service, with the same validation and stale-file protection as structured edits; the change applies on the next **Up**.
- Old API clients sending the single-service create body keep working.

## 2. Scope

### In
- Backend: `engine.Client` network create, remove and richer list; `GET /api/networks` (extended, backward compatible), `POST /api/networks`, `DELETE /api/networks/{id}`; `GET /api/host/interfaces`; multi-service `POST /api/stacks`; `POST /api/stacks/{name}/services`; `DELETE /api/stacks/{name}/services/{service}`; `composefile` multi-service `New`, `AddService`, `RemoveService`.
- Frontend: rail entry and route `/networks`; Create network dialog (shared); `NetworkPicker` "Create network…" option; New stack service cards; stack detail Add service / Remove service.
- Tests, E2E smoke, docs (`docs/api.md`, `docs/security.md`, README feature list).

### Out
- Host ↔ macvlan shim interface (needs root; Stackstead runs as a non-root service user). The UI explains the limitation instead.
- IPv6 subnets, overlay / swarm networks, network labels and driver options beyond those in §4.2.
- Editing a network after creation (Docker does not support it), connecting / disconnecting running containers.
- Renaming a service; reordering services; editing top-level compose keys other than the external network declarations named here.
- Removing top-level network declarations when a service is removed.
- CLI commands for networks or service add/remove.

## 3. Key decisions

The TypeSafe skill named in the global rules is a placeholder (the installed `typesafe-ai` skill is an AI-feature SDK, not a decision scorer); these were decided with the user in brainstorming.

| Decision | Choice | Reason |
|---|---|---|
| Network scope | Full Networks screen (list, create bridge/macvlan/ipvlan, remove) plus "Create network…" in the picker | User decision. Alternatives: inline create only; compose-declared networks (would tie a LAN network to one stack). |
| Engine access | Raw Docker API over the existing socket client (`POST /networks/create`, `DELETE /networks/{id}`) | Same as Plan 9 container actions; no `docker` CLI dependency for engine calls. |
| Network ownership | Networks with a compose project label of a registered stack are owned by that stack: not removable here | Same rule as containers in Plan 9; compose owns them. |
| Host interfaces | `GET /api/host/interfaces` from `net.Interfaces` + `/proc/net/route`, Linux only, `[]` elsewhere | Pre-fills subnet/gateway so the user does not have to know them; stdlib only. |
| Create body | `services: [...]`, legacy `service`/`config` still accepted, both together is 400 | Bug fix without breaking existing clients. |
| Create form | Collapsible service cards, one open at a time | User decision (vs tabs, vs create-then-add). |
| Existing stacks | Add and remove service on stack detail | User decision. |
| Remove semantics | Edit the file only; container goes on the next `up` (already `--remove-orphans`) | Matches every other structured edit: file first, explicit Up applies. |
| Plan split | 10a Networks, 10b Multi-service, parallel worktrees | Only overlap is `NetworkPicker`; 10a's change there is additive. |

## 4. API

All routes require a session; mutating routes require `Content-Type: application/json` (existing CSRF rule); error bodies are `{error}`; validation errors are `{error, fields: [{field, message}]}` with 400, as today.

### 4.1 `GET /api/networks` (extended)

Existing fields (`name`, `subnets` as CIDR strings) keep their meaning; new fields are added:

```json
[{"id": "9f1c2e3a4b5d", "name": "lan", "driver": "macvlan", "scope": "local",
  "subnets": ["192.168.0.0/24"], "gateways": ["192.168.0.1"], "ipRange": "192.168.0.224/27",
  "parent": "enp3s0", "ipvlanMode": "", "internal": false,
  "builtin": false, "project": "", "stack": "",
  "containers": [{"name": "dns-pihole-1", "ipv4": "192.168.0.30"}]}]
```

- `gateways[i]` pairs with `subnets[i]` (`""` when Docker has none). `ipRange` is the first IPAM config's range, `""` if unset.
- `parent` and `ipvlanMode` come from the driver options (`parent`, `ipvlan_mode`), `""` for other drivers.
- `builtin` is true for `bridge`, `host`, `none` (by name, driver `bridge`/`host`/`null`).
- `project` is the `com.docker.compose.project` label; `stack` is the registered stack name for that project, `""` if none.
- `containers` comes from the network inspect (`GET /networks/{id}`), one call per network, run with bounded concurrency (4) and cached for 2 s like the containers list.

### 4.2 `POST /api/networks`

```json
{"name": "lan", "driver": "macvlan", "subnet": "192.168.0.0/24", "gateway": "192.168.0.1",
 "ipRange": "192.168.0.224/27", "parent": "enp3s0", "ipvlanMode": "l2", "internal": false}
```

Validation (400 with field errors), before calling Docker:

- `name`: compose network name rules (`[a-zA-Z0-9][a-zA-Z0-9_.-]*`, at most 63 chars); not an existing network name (409 if it exists).
- `driver`: `bridge`, `macvlan` or `ipvlan`.
- `subnet`: optional for bridge, required for macvlan/ipvlan; IPv4 CIDR, masked form; must not overlap any existing network's IPv4 subnet.
- `gateway`: optional; IPv4 inside `subnet`, not its network or broadcast address. Requires `subnet`.
- `ipRange`: optional; IPv4 CIDR inside `subnet`. Requires `subnet`.
- `parent`: required for macvlan/ipvlan, forbidden for bridge; an interface name (`[a-zA-Z0-9_.:-]{1,15}`). Not checked against the host list (Docker validates it; the host list may be empty on non-Linux).
- `ipvlanMode`: ipvlan only, `l2` (default) or `l3`.
- `internal`: bool, default false.

Docker call: `POST /networks/create` with `CheckDuplicate: true`, `IPAM.Config` from subnet/gateway/ipRange, `Options` `parent` / `ipvlan_mode`, label `dev.stackstead.created=true`. Docker errors map to 400 (bad request, e.g. unknown parent interface) or 409 (conflict), message passed through. Response 201 with the §4.1 object.

### 4.3 `DELETE /api/networks/{id}`

`id` is the network id (12-hex or full) or exact name. Refusals (409): `builtin`; owned by a registered stack (`stack` non-empty) — message names the stack; any containers attached — message names up to 3. Unknown network 404. Otherwise `DELETE /networks/{id}`; 204.

### 4.4 `GET /api/host/interfaces`

```json
[{"name": "enp3s0", "ipv4": "192.168.0.10/24", "subnet": "192.168.0.0/24", "gateway": "192.168.0.1", "default": true}]
```

Linux only (`runtime.GOOS == "linux"`), `[]` elsewhere. Interfaces that are up, not loopback, have an IPv4 address, and whose name does not start with `docker`, `br-`, `veth`, `virbr`, `cni`, `flannel`, `cali`, `tailscale`, `wg`. `gateway` is the default route's gateway when it leaves through this interface (from `/proc/net/route`), else `""`; `default` marks that interface. Sorted: default first, then by name. Note: when Stackstead runs in a container this shows the container's interfaces; documented, not handled.

### 4.5 `POST /api/stacks` (multi-service)

```json
{"name": "dns", "services": [
  {"service": "pihole", "config": {"image": "pihole/pihole:2025.08.0", "networks": [{"name": "lan", "ipv4Address": "192.168.0.30"}]}},
  {"service": "unbound", "config": {"image": "mvance/unbound:1.21.1"}}]}
```

- `services`: 1–20 entries. Legacy `{name, service, config}` is converted to a one-entry list (service defaults to `name`, as today). Both `services` and `service`/`config` present: 400.
- Service names: valid compose service names, unique in the request (400 `services[i].service`). An empty name on entry 0 defaults to the stack name; on any other entry it is an error.
- Each config goes through the existing `prepareConfig` and `ValidateConfig`.
- `checkChanges` receives all services. New: ports published twice within the request, and the same static IP on the same network twice within the request, are errors on the later entry.
- Field paths are prefixed `services[i].` (e.g. `services[1].ports[0]`, `services[0].networks.lan.ipv4_address`). Legacy-form requests keep the unprefixed paths so old clients still map errors.
- `composefile.New(services []NamedService)` writes all services in request order, with one top-level `networks:` block declaring every non-local network used by any service as `external: true` (deduplicated, sorted).
- Everything else is unchanged: compose validation, atomic folder claim, cleanup on failure, optional start (`up --build`).

### 4.6 `POST /api/stacks/{name}/services`

Body `{service, config, hash}`. `hash` is the file hash from the last read (as for `PUT .../services/{service}`); stale: 409. Service exists: 409. Same validation as create for one service (field paths unprefixed). Writes with `composefile.AddService`, then compose validation, as for structured edits. Response: the stack detail. Also `POST /api/stacks/{name}/services/preview` with the same body returns `{content, hash}` without writing (the UI shows the diff the same way the edit preview does).

### 4.7 `DELETE /api/stacks/{name}/services/{service}?hash=`

Stale hash 409; unknown service 404. Refused with 409: it is the last service ("delete the stack instead"); another service lists it in `depends_on` (short or long form) — message names them. Writes with `composefile.RemoveService`. Response: the stack detail. No compose action runs.

## 5. composefile

- `NamedService{Name string; Config ServiceConfig}`.
- `New([]NamedService) ([]byte, error)` replaces `New(name, cfg)`; the old call sites pass a one-item slice.
- `(*File).AddService(name, cfg) (*File, error)`: appends the service node at the end of `services:`; adds `external: true` declarations for networks not yet declared at top level (creating `networks:` if missing). Comments and order of everything else are preserved. Errors: name exists, invalid name, invalid config.
- `(*File).RemoveService(name) (*File, error)`: deletes the service key and its node, including head/line comments attached to that key. Leaves top-level `networks:`, `volumes:` and others as they are.
- `(*File).DependentsOf(name) []string`: services whose `depends_on` names it.

## 6. Frontend

### 6.1 Networks screen (`web/src/routes/NetworkList.svelte`)

- Rail entry **Networks** (after Containers), route `/networks`, breadcrumb "Networks".
- Table: Name, Driver, Subnet, Gateway, Parent, Containers (count; row expands to `name → IP`, names link to `/containers/<name>`), Owner chip (`built-in` / `stack <name>` linking to the stack / `compose <project>` / `unmanaged`).
- Header button **Create network** opens the dialog (§6.2). Row action **Remove**: enabled only when not built-in, not stack-owned and no containers; otherwise disabled with a tooltip giving the reason. Confirm dialog, then `DELETE`; toast; list refreshes.
- Refresh: on mount, after create/remove, and on `/api/events` change/resync (debounced, same helper as the Containers list). No polling while hidden.

### 6.2 Create network dialog (`web/src/lib/networks/CreateNetworkDialog.svelte`)

- Fields: Name; Driver (segmented: Bridge / Macvlan / IPvlan); for macvlan/ipvlan, **Host interface** select from `GET /api/host/interfaces` (plus "Other…" free text; free text only when the list is empty); Subnet; Gateway; IP range (optional); IPvlan mode (ipvlan only); Internal (checkbox, under "Advanced").
- Picking an interface fills Subnet and Gateway when they are empty or still hold the previous auto-filled values.
- Macvlan/ipvlan show a persistent note: "The Docker host itself cannot reach containers on this network. Pick addresses outside your router's DHCP pool, or set an IP range outside it."
- Server field errors map to fields; success calls `oncreated(network)`.

### 6.3 `NetworkPicker`

The select gets a last option **Create network…**. Choosing it opens the dialog; on success the picker reloads networks and selects the new one; on cancel the previous value is restored.

### 6.4 New stack (`StackCreate.svelte`)

- Stack name, then a list of service cards. Each card is the existing `ServiceForm` (with `showName`) in a collapsible card; header shows service name and image (or build context) and a ✕ remove button (hidden when only one card). One card open at a time; **+ Add service** appends and opens a new card.
- Card 0's service name defaults to the stack name until edited (today's behavior).
- Submit sends `services`. Field errors `services[i].…` are routed to card `i`; the first erroring card opens and focuses its first error (existing `focusFirstError`). Cards with errors show an error marker in the header while collapsed.
- Client-side check for duplicate service names before submit (server is authoritative).

### 6.5 Stack detail

- **Add service** button on the services section: opens a panel with one `ServiceForm` card + `NetworkPicker`, Preview (diff), Save. Uses the current file hash; on 409 stale, the existing stale-file banner flow (reload / merge).
- Each service row: **Remove service** action, confirm dialog "Remove <service> from compose.yaml? Its container is removed on the next Up." Disabled with a reason when it is the last service. A 409 `depends_on` message is shown in the dialog.
- After either, the detail shows the existing "changes not applied — Up" state.

### 6.6 Accessibility and responsive

Cards are `<section>` with a heading button (`aria-expanded`, `aria-controls`); remove buttons have `aria-label="Remove service <name>"`. Dialog follows the existing `Dialog` component (focus trap, Esc). Networks table collapses to cards under the same breakpoint as the Containers list.

## 7. Error handling

- Docker unreachable: `GET /api/networks` 503 (picker falls back to free text as today); create/delete 503; interfaces endpoint still works (does not need Docker).
- Network create races (name or subnet appears between check and create): Docker's 409 is passed through.
- Remove network while a container attaches in between: Docker's 409 "has active endpoints" is passed through.
- Multi-service create failure after the folder is created: existing cleanup removes exactly what this call created.
- Add/remove service with a stale hash: 409 and the existing stale flow; never overwrite.

## 8. Security

- New mutations (network create/delete, service add/remove) use session + JSON CSRF rule; no new privileges (the service user already has Docker access, which is root-equivalent; `docs/security.md` already says so).
- `docs/security.md` adds: macvlan/ipvlan networks put containers directly on the LAN, bypassing Docker's port publishing; firewall rules on the host do not apply to them.
- `parent` is passed to Docker only as a driver option, never to a shell. Interface listing reads `/proc/net/route` only.

## 9. Testing

Test-first for every behavior change (global rules).

- **engine**: network create/delete/inspect request bodies and error mapping against the fake socket server already used by engine tests.
- **api**: network create validation table (driver/subnet/gateway/ipRange/parent combos, overlap), delete refusals (builtin, stack-owned, attached), list fields; interfaces endpoint with a fake `/proc/net/route` and interface source (injected); stacks create legacy vs `services` form, both-present 400, duplicate names, cross-entry port and IP clashes, prefixed field paths; add/remove service: stale hash, exists, last service, `depends_on` refusal.
- **composefile**: multi `New` (order, external network dedupe), `AddService` (comments preserved, networks block created or extended), `RemoveService` (comments of neighbours preserved, top-level blocks untouched), `DependentsOf` (short and long `depends_on`).
- **integration (Docker)**: create bridge network with a subnet via API; create a two-service stack with a static IP on it; `up`; container has that IP; remove a service; `up`; container gone; network delete refused while attached, allowed after `down`. Macvlan is not exercised in CI-less local runs (needs a real parent NIC); covered by unit tests of the request body.
- **web (vitest)**: Networks list rendering and remove enablement; dialog auto-fill and driver-dependent fields; picker "Create network…" selects the new network; create cards add/remove/collapse, error routing to card `i`, duplicate-name check; detail add/remove flows.
- **E2E smoke**: Go HTTP smoke extended with network create/delete and a two-service create.

## 10. Documentation

`docs/api.md` (all routes in §4), `docs/security.md` (§8), README feature list ("Networks: create LAN (macvlan/ipvlan) and bridge networks, give services static IPs"; "Multi-service stacks from the UI").

## 11. Delivery

- **Plan 10a — Networks**: engine, `/api/networks*`, `/api/host/interfaces`, Networks screen, dialog, picker option, docs for those.
- **Plan 10b — Multi-service**: composefile, create API, add/remove endpoints, create cards, detail add/remove, docs for those.
- Independent; run in parallel worktrees, merge 10a first (10b's cards render `NetworkPicker` unchanged either way).
