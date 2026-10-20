# Stackstead Connected Servers: Design

**Date:** 2026-10-20
**Status:** Draft, awaiting review. Lets two or more Stackstead servers connect to each other so every server's web UI can show and control all of them, and adds a combined **All stacks** screen. Implemented by one plan (number assigned when the plan is written).

## 1. Purpose

A homelab operator runs Stackstead on more than one machine (today two on the same LAN). They want to open any one of them and manage all of them from there, without keeping a tab and a login per machine, and to see every stack on every server in one list.

Success criteria:

- On server B, Settings → Connected servers → **Create connection token** shows a token once. On server A, Settings → Connected servers → **Add server** with B's address and that token succeeds, and afterwards **both** A's and B's sidebars list the other server without anything else being done on B.
- From A's UI, every screen that exists today works for B: stack list and detail, logs, live metrics, compose editor, create/import, operations with their progress streams, containers, networks, storage, with the same behaviour as opening B's own UI.
- **All stacks** lists the stacks of every connected server with a Server column, and a row opens that stack under its server.
- When B is down, A's UI still loads instantly. B shows as unreachable in the sidebar with its last-seen time; B's screens say "can't reach B" instead of showing stale data; All stacks shows a banner and the other servers' stacks.
- Removing the connection on either side stops it working in both directions. If the other side is offline, it is cleaned up when it next comes back (§6.4).
- No connection token or peer credential is ever returned by the API after creation, logged, or included in an error message.
- With no connected servers, the UI looks and behaves exactly as today (no server headings, no All stacks entry) and no background work runs for this feature.

## 2. Scope

### In
- A server identity: a stable instance ID and a display name (§5.1).
- Connection tokens (one-time pairing) and peer credentials (long-lived, per connection) (§5).
- Pairing, unpairing and status checks between servers (§6).
- An authenticated pass-through so the local server forwards a signed-in user's API calls and event streams to a connected server (§7).
- An aggregate endpoint for All stacks (§7.4).
- UI: grouped sidebar (option B, accordion), server-scoped routes for every existing screen, the All stacks screen, a **Connected servers** card in Settings (§8).
- Docs: `docs/api.md`, `docs/security.md`, README feature line.

### Out
- Connecting servers that cannot reach each other directly (NAT traversal, relays, cloud brokers). Planned later; nothing here should block it (§10).
- Discovery on the LAN (mDNS etc.). Servers are added by address and token only *(user)*.
- Per-connection permissions (read-only peers, per-stack scopes). A connection is all-or-nothing.
- Chaining: A cannot reach C through B. Every server you want to manage must be connected directly. If A–B and B–C are connected, A does not see C (§6.5).
- The CLI. `stackstead` CLI contexts (`contexts.json`) stay separate and unchanged.
- Cross-server actions (moving a stack between servers, bulk actions across servers from All stacks).
- Combined views for Containers, Networks or Storage. Only Stacks gets a combined view *(user)*.
- Notifications about peers being unreachable.

## 3. Key decisions

The TypeSafe skill could not be run in this session, so decisions are argued in writing like the earlier specs. *(user)* marks decisions the user made.

| Decision | Choice | Reason |
|---|---|---|
| How servers connect *(user)* | Paste the other server's address and a token into Settings | Explicit, works the same on a LAN and later across networks. |
| Which token *(user, after discussion)* | A new, separate connection token. Not the setup token, not the `/metrics` token, not an admin password | The setup token is one-time for first-run and empty afterwards. The metrics token is a read-only scrape credential; widening it would silently make every Prometheus config root-equivalent. Storing an admin password on another machine is worse than a revocable, scoped credential. |
| Direction *(user)* | Pairing is mutual: adding B on A makes B list A automatically | User decision. |
| Transport | The browser only ever talks to its own server, which forwards to the peer | The SPA's CSP is same-origin only and the session cookie is SameSite=Strict on its own host; cross-origin calls from the browser would need CORS and per-server logins. Forwarding keeps one login and one origin. |
| Trust model | A connection is root-equivalent both ways: anyone signed in to A can do on B whatever B's admin can | Matches the user's intent ("control both from each"). Stackstead has one admin per server, so there is no finer identity to map. `docs/security.md` must say this plainly. |
| Network path *(user)* | Through each server's existing reverse proxy (§5.5) | Matches how remote access already works; no built-in TLS needed. |
| HTTPS | Required by default. Plain `http://` only for loopback/private-network addresses (RFC 1918, RFC 4193, link-local) and only after an explicit "I accept sending a root-equivalent credential unencrypted" checkbox. Self-signed peers via CA file or a pinned SHA-256 certificate fingerprint | A peer credential is a root password for the other host. Mirrors the CLI's `caFile`/`insecure` options but prefers pinning over "skip verification". |
| Sidebar *(user)* | Option B: accordion. All stacks on top, then one entry per server; the server being viewed is expanded, others collapse to one line with status dot and stack count | Chosen from the mockups. Scales past two servers. |
| Combined view *(user)* | Exactly today's Stacks screen with combined data plus a Server column | User decision. |
| URLs | Local server keeps today's URLs. Remote screens live under `/s/{server}/…`, e.g. `/s/mini-02/stacks/pihole`. All stacks is `/all` | Existing links and bookmarks keep working; a remote URL is shareable and reload-safe. *(my call)* |
| Settings | Settings in the sidebar is always the local server's. Remote servers' settings are not reachable through the connection | Avoids changing B's password, metrics token or connections from A by accident, and stops a peer from managing peers (§6.5). *(my call)* |

## 4. Terms

- **Local server**: the Stackstead the browser is signed in to.
- **Peer**: another Stackstead connected to the local server.
- **Connection token**: a short-lived, single-use secret created on one server so another server can pair with it.
- **Peer credential**: a long-lived secret one server holds to call the other's API. Each connection has two, one per direction.

## 5. Identity and secrets

### 5.1 Server identity
- Each server gets an **instance ID** (random 128-bit, hex) created once and stored in `settings`. It never changes, including across upgrades and `db backup` restores onto the same machine. (Restoring a backup onto a *different* machine clones the ID; `docs/security.md` should note to re-pair in that case.)
- Each server has a **display name**, defaulting to the hostname from `/api/host`, editable in Settings → Connected servers. Names are what the UI shows and what `/s/{server}` uses, slugified (lowercase, `[a-z0-9-]`, max 40). If two peers would have the same slug the local server appends `-2`, `-3` locally; this does not rename the peer.
- Each server has an **advertised address**: the URL other servers use to reach it. Stored in `settings`. Suggested on first use from the request the user's browser made (scheme + Host, honouring `X-Forwarded-Proto` like `session.go` does) and confirmed by the user when adding a server. Editable later; a change is pushed to all peers (§6.3).

### 5.2 Connection tokens
- Created in Settings by the signed-in admin; shown once.
- Single use, valid 15 minutes, at most 5 outstanding; creating a sixth invalidates the oldest. *(my call)*
- Stored as SHA-256 only, like the metrics token.
- Format carries a recognisable prefix (e.g. `ssc_…`) so it is easy to spot in logs or secret scanners.

### 5.3 Peer credentials
- Minted during pairing (§6.1): the issuer stores only the SHA-256 hash, the holder stores the plaintext in SQLite. Same reasoning as the notifications spec: the database lives in the 0700 state directory and its reader is already root-equivalent; `db backup` includes them and `docs/security.md` says so.
- Sent as `X-Stackstead-Peer: <credential>` on every peer request, not in `Authorization`, so a reverse proxy that uses basic auth or forward-auth/SSO can keep its own `Authorization` handling (§5.5).
- Never returned by any API after creation, never logged, never in error messages.

### 5.4 Storage
One migration adds:
- `peers`: instance ID (PK), display name, slug, advertised URL, TLS settings (CA PEM or pinned fingerprint, plaintext-accepted flag), outbound credential (plaintext), inbound credential hash, peer version, created_at, last_seen_at, last_error, state (`active`, `removing`).
- `connection_tokens`: hash, created_at, expires_at.
- `settings` keys for instance ID, display name and advertised address.

Exact column names and types are left to the builder.

### 5.5 Reaching a peer *(user: through each server's existing reverse proxy)*
- Peers reach each other through each server's existing TLS reverse proxy. Stackstead keeps listening on `127.0.0.1` and gains no built-in TLS.
- The advertised address (§5.1) is the proxy URL, including any path prefix.
- If the proxy has its own login, it must let through requests carrying `X-Stackstead-Peer` to `/api/peer/*` and to every `/api/*` route a peer may call (§6.5). `docs/install.md` gets an nginx example (and a short note for Caddy/Traefik forward-auth) showing that bypass. Stackstead still checks the credential itself, so the bypass doesn't weaken auth.
- A redirect, an HTML login page or a non-Stackstead JSON answer from the peer's address is reported as "mini-02's proxy answered instead of Stackstead (it may require its own login, see docs)". It is never treated as success or as unpaired.

## 6. Pairing protocol

All peer-to-peer endpoints live under `/api/peer/*` and authenticate with the `X-Stackstead-Peer` header (connection token during pairing, peer credential afterwards), never a session cookie.

### 6.1 Add server (A adds B)
1. User on A enters B's address, the connection token from B, and TLS options. A shows its own advertised address for confirmation.
2. A calls `POST {B}/api/peer/pair` with `X-Stackstead-Peer: <connection token>` and body: A's instance ID, display name, advertised URL, version, and a freshly minted credential **for B to use when calling A**. A keeps that credential's hash as A's inbound hash for B, in a pending state.
   The request also carries the trust material B needs to call A: either the CA PEM the user gave for A's address, or the SHA-256 certificate fingerprint A gets by probing its own advertised URL, or the plaintext flag. The plaintext checkbox on A covers both directions, and is only offered when both addresses are private (§3). *(my call)*
3. B checks the token (valid, unused, unexpired), consumes it, rejects pairing with itself (same instance ID) or with an already-connected instance ID, then tries to reach A at the advertised URL (`GET {A}/api/peer/hello` with the credential A gave it). If that fails, B answers with an error naming the problem ("B could not reach A at https://…: connection refused") and stores nothing.
4. B mints a credential for A, stores A as a peer, and answers with B's instance ID, display name, version and the credential **for A to use when calling B**.
5. A stores B as an active peer.

If step 3 fails because B cannot reach A, the user sees why on A and can fix A's advertised address and retry with a new token. The token is consumed only on success. *(my call)*

### 6.2 Status
- Each server checks each peer with `GET /api/peer/hello` every 30 s while at least one browser session is active on it, and every 5 minutes otherwise. *(my call)*
- `hello` returns instance ID, name, version, advertised URL and a stack count. A mismatched instance ID marks the peer as wrong and stops forwarding (protects against a different machine taking over the address).
- The result sets `last_seen_at`, `last_error` and the state shown in the sidebar.

### 6.3 Updates
Changing the display name or advertised address on one server pushes the change to every reachable peer (`POST /api/peer/update`). Unreachable peers get it at the next successful `hello`, when the latest values are exchanged.

### 6.4 Remove
- Either side: Settings → Connected servers → **Remove**. The local side deletes the peer and its credentials immediately and calls `POST {peer}/api/peer/unpair`; the other side deletes its record too.
- If the peer is unreachable, the local side still deletes immediately. The other side's credential for us no longer authenticates, so its next `hello` gets 401 with the machine-readable error code `peer_not_paired` in a JSON body, and it removes the peer. Any other 401, 403, redirect or non-JSON answer (for example a reverse proxy's login page) is treated as "unreachable", never as unpaired, so a proxy can't silently delete a connection, showing "mini-02 removed this connection" once in Settings. *(my call)*

### 6.5 Limits on what a peer credential can do
- A peer credential authenticates the normal `/api/*` routes except: `/api/peers*` (managing connections), `/api/settings/*`, `/api/password`, `/api/logout`, `/api/setup*`, and the forwarding routes in §7. This makes chaining impossible and keeps B's settings with B's own UI.
- Wherever B already writes to its action log (today only storage-plan confirmations), a request made with a peer credential is recorded with actor `peer:<A's name> (<A's username>)`. A asserts the username in an `X-Stackstead-Peer-User` header on forwarded requests, and B trusts it because the credential is root-equivalent anyway. No new log points are added for this feature. *(my call)*
- The restrictions stop *accidental* chaining only. A peer is root-equivalent on B, so through the file browser or compose it could read B's database and B's credentials for C. `docs/security.md` says this.

## 7. Forwarding

### 7.1 Route
`/api/servers/{id}/api/...` on A, session-authenticated, forwards to `{peer}/api/...` with the peer credential. Method, query, body and relevant headers (`Content-Type`, `Last-Event-ID`, `If-Match`/ETags and anything the SPA already relies on such as `X-Stackstead-Checks` on responses) pass through. Cookies, `Authorization` from the browser and hop-by-hop headers do not.

### 7.2 Streams
Server-sent-event routes (operations, logs, metrics, `/api/events`) are forwarded as streams with flush-per-write, honouring `Options.Closing` and the existing write timeouts so graceful shutdown is not held up.

### 7.3 Errors
- Peer unreachable or TLS failure: A answers `502` with a JSON error code `peer_unreachable` / `peer_tls` and a human message naming the server, never the credential.
- Peer answered 401: `peer_unpaired`.
- Otherwise the peer's status and body pass through unchanged, so existing UI error handling (field errors, 409s, etc.) works as is.
- Upstream timeouts match the existing client defaults for unary calls; streams have none beyond the heartbeat logic.

### 7.4 All stacks
`GET /api/fleet/stacks` on A returns the local stack summaries plus each active peer's, fetched in parallel with a short per-peer timeout (my call: 3 s). Each row carries the server's instance ID and name; each unreachable peer is listed separately with its error so the UI can show the banner. Aggregated status totals and merged recent events (tagged by server) come from the same endpoint or a sibling one; the builder decides the exact shape.

### 7.5 Connection budget
Behind an HTTP/1.1 proxy the browser has only 6 connections per origin (`docs/security.md`, Remote access). The UI must not open one `/api/events` stream per peer. A tab keeps at most the local events stream plus the stream of the server currently being viewed plus the active screen's own stream. All stacks refreshes remote data by polling `/api/fleet/stacks` (e.g. every 5 s while visible) rather than streaming. *(my call)*

## 8. UI

### 8.1 Sidebar (option B)
- Shown only when at least one peer exists; otherwise the sidebar is unchanged.
- Order: logo, search, **All stacks** (with the total stack count across reachable servers), then one heading per server: the local server first, labelled "this server", then peers in the order they were added.
- Each heading shows a status dot (reachable / unreachable / wrong instance) and, when collapsed, the stack count or "unreachable".
- The server being viewed is expanded and shows today's entries: Stacks, Containers, Networks, Storage, New stack, Import. Others are collapsed; clicking one expands it and navigates to its Stacks screen.
- Settings and the user block stay at the bottom and belong to the local server. The HostBlock shows the server being viewed.
- The mobile tab bar gains a server switcher. The exact form is the builder's call.

### 8.2 Remote screens
- Every existing screen under `/s/{slug}/…`. The SPA's API client and SSE client take a server base (`''` for local, `/api/servers/{id}` for a peer) so screens are reused, not copied.
- Breadcrumb and page title include the server name.
- Unreachable peer: the screen shows a "Can't reach mini-02 (last seen 3 min ago): <error>" state with a retry action instead of cached data.
- Command palette search covers stacks on all reachable servers, with the server name shown. *(my call)*

### 8.3 All stacks (`/all`)
- The current Stacks screen (status panel, stack list, side column) with combined data: totals across servers, a Server column (chip) on each row, filter chips `All · server1 · server2 …`, and recent events tagged with their server.
- Rows link to `/stacks/{name}` for local stacks and `/s/{slug}/stacks/{name}` for remote ones. Row actions behave as on each server's own Stacks screen.
- Unreachable servers: a banner at the top; their stacks are omitted rather than shown stale.

### 8.4 Settings → Connected servers card
- This server: display name (editable), advertised address (editable, with the suggested value), instance ID (read-only, small).
- **Create connection token**: shows the token once with a copy button and its expiry.
- **Add server**: address, token, TLS options (CA PEM upload/paste, or pinned fingerprint shown after a first probe, or the plaintext checkbox when allowed), confirmation of this server's advertised address. Errors from §6.1 shown inline.
- List of peers: name, address, version, status, last seen, last error, **Remove**.
- Version mismatch between servers is shown as a warning on the peer row (§9).

## 9. Compatibility
- The remote screens are the local SPA talking to the peer's API, so both must speak the same API. `hello` exchanges versions. Forwarding and All stacks rows for a peer are enabled only when the peer's major.minor version equals the local one. Otherwise the peer stays connected, its sidebar entry and row say "mini-02 runs v1.4, this server v1.5. Update to match", and its screens show that state instead of possibly broken data. Patch versions may differ. *(my call: a two-server homelab upgrades both machines together, and a broken screen is worse than a clear "update" message.)*
- A server that predates this feature answers 401 for unknown `/api/` paths (auth runs before the catch-all), not 404. So before pairing, A calls the unauthenticated `GET /api/health`, which gains a `peering` protocol field. If that field is missing, A says "mini-02 runs an older Stackstead without connected servers".
- Peer protocol endpoints carry a protocol version so later releases (e.g. relays, §10) can extend them.

## 10. Forward-looking: servers not on the same LAN
Not built now. The design keeps the door open:
- Peers are identified by instance ID, not address, so an address can later be a relay URL.
- All peer traffic goes through one forwarding layer and one `hello`/`pair` protocol, so a relay transport can slot in behind them.
- HTTPS-by-default with pinning already fits peers on untrusted networks.

## 11. Testing (shape, not a plan)
- Go: pairing happy path and every failure in §6.1 (expired/used token, self-pairing, duplicate, unreachable callback, TLS mismatch); unpair both online and offline; peer-credential route restrictions (§6.5); forwarding of unary, error and SSE responses including shutdown; credentials never appearing in responses or logs; `/api/fleet/stacks` with one peer down.
- An end-to-end test in `cmd/stackstead` running two servers in-process and pairing them.
- Web: sidebar with zero / one / several peers and an unreachable one; server-scoped routing; All stacks rendering, filtering and banner; Connected servers card flows.

## 12. Docs
- `docs/security.md`: the trust model in one paragraph (a connection is root on both machines), HTTPS requirement, plaintext opt-in, where credentials are stored, backups include them, re-pair after restoring onto another machine.
- `docs/api.md`: `/api/peers*` (UI-facing), `/api/peer/*` (server-to-server), `/api/servers/{id}/…` forwarding, `/api/fleet/stacks`.
- README: one feature line.
