# Stackstead Plan 24 — Connected servers

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Plan format:** tasks, names, signatures, test cases and examples only; no code bodies. Implementers write everything test-first.

**Goal:** Let two or more Stackstead servers pair with each other so any server's web UI shows and controls all of them through an authenticated pass-through, and add a combined **All stacks** screen. With no peers, nothing changes.

**Architecture:** A new `internal/peers` package owns identity, connection tokens, peer records, the peer HTTP transport (TLS by CA, pin or private-network plaintext), the pair/hello/update/unpair protocol and the status checker. `internal/api` gains the server-to-server routes (`/api/peer/*`), the UI routes (`/api/peers*`), a peer-credential branch in `requireSession` with a route denylist, a `httputil.ReverseProxy` forwarder at `/api/servers/{id}/api/…`, and `/api/fleet/stacks`. The SPA gets `createApi(base)`, a base-prefixing SSE opener and per-server store factories behind a `ServerContext`; every existing screen is reused under `/s/{slug}/…`, the sidebar becomes an accordion, and Settings gets a Connected servers card.

**Tech Stack:** Go 1.26 standard library (`net/http/httputil`, `crypto/tls`, `crypto/sha256`, `modernc.org/sqlite` already in use); React 19, TypeScript, React Router, Vitest + Testing Library. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-20-stackstead-connected-servers-design.md` (all sections).

## Spec divergences and decisions the spec left open

1. **Peer states.** §5.4 lists `active`, `removing`; §6.1 needs a *pending* inbound credential and §6.4 deletes immediately, so `removing` has no use. States are `pending` (A's half during a pair call; authenticates only `GET /api/peer/hello`; deleted when the pair call fails and ignored after 2 min), `active`, and `removed` (the other side unpaired us: credentials wiped, row kept only to show "mini-02 removed this connection" until the user dismisses it with **Remove**).
2. **A peer's 401 never reaches the browser as 401.** The SPA treats any 401 as "session lost" (`web/src/lib/api/client.ts` `notifyUnauthorized`). The forwarder maps an upstream 401 to **502** `peer_unpaired`; §7.3 named the code but not the status.
3. **Machine-readable codes** need a field: `apitypes.ErrorBody` gains `Code string \`json:"code,omitempty"\``. `TestJSONTagsMatchContract` changes to `"code,error,fields"`; `TestHealthIsPublic` changes to the new body `{"ok":true,"peering":1}`.
4. **Recent events for All stacks.** No REST source of recent Docker events exists today (they only flow over `/api/events`). Each server gains `GET /api/events/recent` (last 50 forwarded container events from a ring in `state.Cache`), and `/api/fleet/stacks` merges them, tagged by server. This is the "sibling" shape §7.4 allows.
5. **End-to-end test.** The existing e2e harness (`cmd/stackstead/e2e_test.go`, `//go:build e2e`) runs the real binary as subprocesses. The plan adds both an in-process two-server test that runs in `make test` (`internal/api/peers_integration_test.go`) and a subprocess `TestE2EConnectedServers` in the e2e harness.
6. **Version compatibility for non-release builds.** `internal/version` reports `dev` or `git describe` output. Rule: parse a leading `vMAJOR.MINOR` (optional `v`); when either side does not parse, compatible only if the strings are equal.
7. **Plaintext on hostnames.** "Private address" is checked on the resolved IPs: every resolved address must be loopback, RFC 1918, RFC 4193 (`fc00::/7`) or link-local, and the plaintext dialer re-checks the dialed IP (DNS rebinding).
8. **Pinning vs. renewal.** A pin is sent/stored only when the certificate does not verify against the system roots; a publicly trusted certificate uses system verification, so Let's Encrypt renewals don't break connections.
9. **"Browser session active"** (§6.2 cadence) = a session-cookie request authenticated within the last 60 s.
10. **Zero-peer UI work.** §1 says no background work runs without peers. The server arms no timer without peers; the SPA still polls `GET /api/peers` every 30 s while visible (one tiny request) so B's sidebar shows A after A pairs, without a reload. Flagged for the user.
11. **TypeSafe skill.** The `typesafe-ai` skill in this environment is an SDK for building AI features against a hosted API, not a local decision tool, so decisions here are argued in writing, as in the spec.
12. **No golangci-lint** is configured; verification is `go vet ./...`, `gofmt -l .`, `make test`, `make ui-check`, `make ui-test`, and `make e2e` for Task 8.

## Global Constraints

- Header for every peer request: `X-Stackstead-Peer: <connection token | peer credential>`; forwarded user: `X-Stackstead-Peer-User: <username>`. Never in `Authorization`.
- Connection token: prefix `ssc_`, 32 random bytes base64url; single use; valid 15 minutes; at most 5 outstanding (a sixth evicts the oldest); stored as SHA-256 hex only; consumed only on a successful pair.
- Peer credential: prefix `ssp_`, 32 random bytes base64url; issuer stores SHA-256 hex only, holder stores plaintext. No token or credential is ever returned after creation, logged, or put in an error message.
- Instance ID: 128 random bits, lowercase hex (32 chars), created once.
- Slug: lowercase `[a-z0-9-]`, max 40, collisions get `-2`, `-3` locally.
- HTTPS by default; `http://` only when both addresses are private (see divergence 7) and the user ticked "I accept sending a root-equivalent credential unencrypted".
- Status checks: every 30 s while a browser session is active, every 5 min otherwise; no timer with zero peers.
- Fleet: 3 s per-peer timeout; UI polls `/api/fleet/stacks` every 5 s while visible.
- Peer error codes (all `502` except `peer_unknown` = `404`): `peer_unreachable`, `peer_tls`, `peer_unpaired`, `peer_proxy`, `peer_version`, `peer_wrong_instance`. Inbound unpaired answer: `401 {"error":…, "code":"peer_not_paired"}`.
- Peer-credential denylist (§6.5): `/api/peers`, `/api/peers/…`, `/api/settings/…`, `/api/password`, `/api/logout`, `/api/setup…`, `/api/servers/…`, `/api/fleet/…`.
- Action-log actor for peer requests: `peer:<peer name> (<peer username>)`.
- Health: `/api/health` gains `peering: 1` (protocol version) and still reveals no software version.
- Browser connection budget (§7.5): at most the local `/api/events`, the viewed peer's `/api/events`, and the active screen's own stream.
- UI copy, verbatim: "this server"; "Can't reach {name} (last seen {ago}): {error}"; "{name} runs v{X.Y}, this server v{X.Y}. Update to match"; "{name}'s proxy answered instead of Stackstead (it may require its own login, see docs)"; "{name} runs an older Stackstead without connected servers"; "{name} removed this connection".
- Commits: conventional, explain why, end with the two trailer lines `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` and `Claude-Session: https://claude.ai/code/session_011L1bX7AfHUJ8cqtidt9JUF`.

## Review Focus

1. **A reverse proxy's login page answers a peer call** (302 to SSO, or 200 HTML, or 401 HTML). Expected: classified `peer_proxy` / unreachable, never "unpaired", never deletes the connection. (Task 2 classification test, Task 5 checker test.)
2. **Credential leakage through logs and errors.** A TLS or dial error message, a `slog` line, or a 502 body must not contain the credential or token. (Task 2 `Secret` test, Task 6 and Task 8 scans.)
3. **Peer credential calling a denied route**, including path tricks (`/api//settings/update`, `/api/peers/`, `/api/servers/x/api/stacks`). Expected 403 for every variant; normal routes still work. (Task 3.)
4. **Graceful shutdown with a forwarded SSE stream open.** Expected: `Closing` ends the forwarded stream and `srv.Shutdown` returns within the grace. (Task 6.)
5. **Session expiry while viewing a peer.** A local 401 on `/api/servers/{id}/…` must still send the user to /login; a peer's 401 must not. (Task 6 Go test, Task 9 web test.)

---

## File structure

| Area | Files | Task |
|---|---|---|
| Schema | `internal/store/store.go` (migration 8) | T1 |
| Identity, tokens, peer records | `internal/peers/identity.go`, `store.go`, `tokens.go` | T1 |
| Transport, TLS, address policy, response classes, secrets | `internal/peers/transport.go`, `address.go`, `classify.go`, `secret.go` | T2 |
| Wire types | `internal/apitypes/types.go` (peer + fleet types, `ErrorBody.Code`, `Health.Peering`) | T3 |
| Server-to-server routes, peer auth | `internal/api/peer_protocol.go` (new), `internal/api/session.go`, `internal/api/handlers_auth.go`, `internal/api/server.go` | T3 |
| Pair/remove/update flows, UI routes | `internal/peers/service.go`, `internal/api/peers.go` (new) | T4 |
| Status checker, compatibility | `internal/peers/checker.go`, `compat.go` | T5 |
| Forwarding | `internal/api/forward.go` (new) | T6 |
| Fleet, recent events | `internal/api/fleet.go` (new), `internal/state/cache.go`, `internal/api/observe.go` | T7 |
| Wiring, integration, e2e | `internal/cli/serve.go`, `internal/api/peers_integration_test.go`, `cmd/stackstead/e2e_test.go` | T8 |
| Scoped clients, store factories | `web/src/lib/api/client.ts`, `types.ts`, `web/src/lib/sse/client.ts`, `web/src/lib/stores/{live,stacks,host,runningOps,operationTracker}.ts` | T9 |
| Server scope, routes, peers store | `web/src/lib/servers/peers.ts`, `web/src/lib/router.ts`, `web/src/app/servers/ServerContext.tsx`, `RemoteServer.tsx`, `web/src/app/routes.tsx`, `App.tsx`, `web/src/lib/shell/breadcrumb.ts`, `web/src/app/hooks/useDocumentTitle.ts` | T10 |
| Screen migration | listed per task | T11, T12 |
| Shell | `web/src/app/shell/{Sidebar,ServerNav,TabBar,MobileTopBar,HostBlock,palette,CommandPalette}.tsx/ts`, `nav.ts` | T13 |
| All stacks | `web/src/lib/servers/fleet.ts`, `web/src/app/routes/AllStacks.tsx`, `web/src/app/overview/StacksList.tsx` | T14 |
| Settings card | `web/src/app/settings/ConnectedServersCard.tsx`, `SettingsScreen.tsx` | T15 |
| Docs | `docs/api.md`, `docs/security.md`, `docs/install.md`, `README.md` | T16 |

Order: T1 → T2 → T3 → T4 → T5 → T6 → T7 → T8 (backend), then T9 → T10 → T11 ∥ T12 → T13 → T14 → T15, then T16. Web tasks T9–T15 need only the wire types from T3/T4/T7 and can start once T7 is merged. Opus reviews each task; Fable (or Opus) reviews the branch.

---

### Task 1: Schema, identity and connection tokens

**Model:** Sonnet — token expiry/eviction/single-use rules and an append-only migration need care.

**Files:**
- Modify: `.gitignore` (add `.superpowers/` under "Editor/IDE")
- Modify: `internal/store/store.go` (append migration 8; never edit shipped entries)
- Create: `internal/peers/identity.go`, `internal/peers/store.go`, `internal/peers/tokens.go`
- Test: `internal/peers/store_test.go`, `internal/peers/identity_test.go`, `internal/store/store_test.go` (extend)

**Interfaces:**
- Consumes: `store.Open(path) (*sql.DB, error)`; `settings(key, value)` table.
- Produces:
  - Migration 8: `peers(instance_id TEXT PK, name TEXT NOT NULL, slug TEXT NOT NULL UNIQUE, url TEXT NOT NULL, ca_pem TEXT NOT NULL DEFAULT '', pin_sha256 TEXT NOT NULL DEFAULT '', plaintext INTEGER NOT NULL DEFAULT 0, out_credential TEXT NOT NULL DEFAULT '', in_hash TEXT NOT NULL DEFAULT '', version TEXT NOT NULL DEFAULT '', stack_count INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, last_seen_at INTEGER, last_error TEXT NOT NULL DEFAULT '', state TEXT NOT NULL CHECK (state IN ('pending','active','removed')))`, unique partial index on `in_hash WHERE in_hash != ''`; `connection_tokens(hash TEXT PK, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL)`.
  - Settings keys: `instance_id`, `server_name`, `advertised_url`.
  - `type Identity struct { InstanceID, Name, Slug, AdvertisedURL string }`
  - `func Slugify(name string) string`
  - `type TLSOptions struct { CAPEM string; PinSHA256 string; Plaintext bool }`
  - `type Peer struct { InstanceID, Name, Slug, URL, Version, LastError string; TLS TLSOptions; OutCredential Secret; StackCount int; State PeerState; CreatedAt time.Time; LastSeenAt *time.Time }` (`Secret` from Task 2 is declared here as `type Secret string` and gains its methods in Task 2)
  - `type PeerState string` with `StatePending`, `StateActive`, `StateRemoved`
  - `type Store struct{…}`; `func NewStore(db *sql.DB, now func() time.Time) *Store`
  - `(*Store) Identity(ctx, fallbackName string) (Identity, error)` — creates `instance_id` on first call; name defaults to `fallbackName`
  - `(*Store) SetIdentity(ctx, name, advertisedURL string) (Identity, error)`
  - `(*Store) CreateToken(ctx) (plain string, expiresAt time.Time, err error)`
  - `(*Store) ConsumeToken(ctx, plain string, consume func() error) error` — validates, runs `consume` (the pair attempt), deletes the row only when `consume` returns nil; `ErrTokenInvalid` otherwise
  - `(*Store) Peers(ctx) ([]Peer, error)` ordered by `created_at, rowid`; `(*Store) Peer(ctx, id string) (Peer, error)`; `(*Store) PeerByInboundHash(ctx, hash string) (Peer, error)`
  - `(*Store) InsertPeer(ctx, p Peer, inHash string) error` (assigns a unique slug); `(*Store) UpdatePeer(ctx, id string, fn func(*Peer) error) error`; `(*Store) SetInboundHash(ctx, id, hash string) error`; `(*Store) DeletePeer(ctx, id string) error`; `(*Store) CountPeers(ctx) (int, error)`
  - Errors: `ErrTokenInvalid`, `ErrPeerNotFound`, `ErrAlreadyPaired`

- [ ] **Step 1: Write the failing tests**
  - `TestMigrationAddsPeerTables` (store_test.go): fresh `store.Open` → `SupportedSchema() == 8`; `peers` and `connection_tokens` exist; an `INSERT` with `state='removing'` fails the CHECK.
  - `TestSlugify` table: `"Mini 02" → "mini-02"`, `"  My_Host!! " → "my-host"` (every run of characters outside `[a-z0-9]` becomes one `-`, no leading/trailing `-`), 60-char name → 40 chars, `"" → "server"`.
  - `TestIdentityIsCreatedOnceAndStable`: two calls return the same 32-hex `InstanceID`; name defaults to the fallback; `SetIdentity` changes name and URL but never the ID.
  - `TestConnectionTokenRules`: token starts with `ssc_`; DB holds only `sha256(token)`; expired at `now+15m+1s` → `ErrTokenInvalid`; a sixth token evicts the oldest (oldest → invalid, newest five valid); `ConsumeToken` whose callback fails leaves the token usable; a successful consume makes a second use fail.
  - `TestInsertPeerAssignsUniqueSlug`: three peers named `mini` get `mini`, `mini-2`, `mini-3`; `Peers` returns them in insertion order.
- [ ] **Step 2: Run** `go test -race ./internal/peers/ ./internal/store/` — FAIL: `undefined: NewStore` / schema version 7.
- [ ] **Step 3: Implement** the migration and the store. Token hashing reuses the `metrics.HashToken` approach (hex SHA-256) but lives in `internal/peers/tokens.go` as `func HashSecret(plain string) string` so `peers` does not import `metrics`. Token eviction and consume run in one transaction.
- [ ] **Step 4: Run** the same command — `ok`; also `go test ./internal/store/...` for the existing migration tests.
- [ ] **Step 5: Commit**
```bash
git add .gitignore internal/store/store.go internal/store/store_test.go internal/peers/
git commit -m "feat(peers): store server identity, peers and one-time connection tokens

Pairing needs a stable instance ID, hashed single-use tokens and per-peer
records before any protocol can run; .superpowers/ is local tool state.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_011L1bX7AfHUJ8cqtidt9JUF"
```

---

### Task 2: Peer transport, address policy and response classification

**Model:** Sonnet — TLS pinning, private-address checks and the proxy-vs-unpaired distinction are security-critical.

**Files:**
- Create: `internal/peers/secret.go`, `internal/peers/address.go`, `internal/peers/transport.go`, `internal/peers/classify.go`
- Test: `internal/peers/transport_test.go`, `internal/peers/address_test.go`, `internal/peers/classify_test.go`

**Interfaces:**
- Consumes: `TLSOptions`, `Peer` (Task 1).
- Produces:
  - `type Secret string` methods: `String() string` → `"[redacted]"`, `LogValue() slog.Value` → `"[redacted]"`, `GoString() string` → `"[redacted]"`, `Reveal() string`; `func NewCredential() (Secret, string /*hash*/, error)` (`ssp_` prefix)
  - `func ValidateAddress(raw string, plaintextAccepted bool, resolve func(ctx context.Context, host string) ([]netip.Addr, error)) (*url.URL, error)` — https required; http only with `plaintextAccepted` and all resolved IPs private; strips trailing `/`; keeps a path prefix; rejects userinfo, query, fragment
  - `func IsPrivate(addr netip.Addr) bool`
  - `func NewHTTPClient(base *url.URL, tls TLSOptions, timeout time.Duration) (*http.Client, error)` — `CheckRedirect` returns `ErrProxyAnswered`; pin checked in `tls.Config.VerifyConnection` against the leaf's SHA-256 (hex, case-insensitive, colons allowed); CA PEM replaces roots; plaintext dialer refuses a non-private dialed IP; `ResponseHeaderTimeout = timeout`, no overall `Timeout` (streams)
  - `func Probe(ctx, base *url.URL) (ProbeResult, error)`; `type ProbeResult struct { Peering int; FingerprintSHA256 string; SystemTrusted bool }` — reads `GET /api/health` with verification off only to read the leaf certificate, then reports whether the system roots verify it
  - `type PeerError struct { Code string; Server string; Msg string; cause error }` with `Error()` = `Msg`; codes per Global Constraints
  - `func Classify(server string, resp *http.Response, err error) error` — nil for 2xx JSON; `peer_not_paired` body on 401 → `ErrNotPaired`; any other 401/403, 3xx, or non-JSON `Content-Type` → `PeerError{Code: "peer_proxy"}`; `x509`/`tls` errors → `peer_tls`; dial/timeout → `peer_unreachable`; messages name the server, never the URL query or credential

- [ ] **Step 1: Write the failing tests**
  - `TestSecretNeverPrints`: `fmt.Sprintf("%v %s %+v %#v", s, s, struct{S Secret}{s}, s)` and a `slog` JSON handler line contain no `ssp_`.
  - `TestValidateAddress` table: `https://mini-02.lan/stackstead/` → kept prefix `/stackstead`; `http://192.168.1.5:8420` without flag → error "plain http needs…"; with flag → ok; `http://example.com` resolving to `93.184.216.34` with flag → error; `http://mini.lan` resolving to `[10.0.0.2, 8.8.8.8]` → error; `http://[fd00::1]` and `http://169.254.1.1` and `http://127.0.0.1:9` with flag → ok; `ftp://x`, `https://u:p@x`, `https://x/?a=1` → error.
  - `TestPinnedCertificate`: `httptest.NewTLSServer`; client with the server cert's SHA-256 pin → 200; wrong pin → `peer_tls`; CA PEM of the test server → 200; no options → `peer_tls`.
  - `TestRedirectIsProxyAnswer`: server answering 302 to `/login` → `PeerError` code `peer_proxy`.
  - `TestClassify` table: 401 JSON `{"code":"peer_not_paired"}` → `errors.Is(err, ErrNotPaired)`; 401 HTML → `peer_proxy`; 200 `text/html` → `peer_proxy`; 403 JSON without code → `peer_proxy`; dial refused → `peer_unreachable`; every message contains the server name and no `ssp_`.
  - `TestProbeReportsFingerprint`: self-signed `httptest.NewTLSServer` with health `{"ok":true,"peering":1}` → `Peering == 1`, fingerprint matches, `SystemTrusted == false`; health without `peering` → `Peering == 0`.
- [ ] **Step 2: Run** `go test -race ./internal/peers/ -run 'Secret|Address|Pinned|Redirect|Classify|Probe'` — FAIL: `undefined: ValidateAddress`.
- [ ] **Step 3: Implement** the four files. The plaintext dialer wraps `net.Dialer.DialContext` and checks the remote `netip.Addr` after resolution.
- [ ] **Step 4: Run** — `ok`.
- [ ] **Step 5: Commit** `feat(peers): add the peer transport with pinning and private-only plaintext` (why: a peer credential is root on the other host, so TLS is required and a proxy's answer must never look like "unpaired"), with the trailer.

---

### Task 3: Server-to-server routes and peer-credential authentication

**Model:** Sonnet — changes the central auth middleware; the denylist is a security boundary.

**Files:**
- Modify: `internal/apitypes/types.go`, `internal/apitypes/types_test.go`
- Create: `internal/api/peer_protocol.go`
- Modify: `internal/api/session.go`, `internal/api/handlers_auth.go` (`handleHealth`), `internal/api/server.go` (`Options.Peers *peers.Service`, route registration), `internal/api/health_test.go`
- Create: `internal/peers/inbound.go` (pair/hello/update/unpair as seen by the receiving server)
- Test: `internal/api/peer_protocol_test.go`, `internal/peers/inbound_test.go`

**Interfaces:**
- Consumes: Task 1 store, Task 2 `NewCredential`, `HashSecret`, `ValidateAddress`, `NewHTTPClient`.
- Produces (apitypes; add each to `TestJSONTagsMatchContract`):
  - `Health{OK bool "ok"; Peering int "peering"}`; `ErrorBody{Error, Fields, Code string "code,omitempty"}`
  - `PeerTrust{CAPEM string "caPem"; PinSHA256 string "pinSha256"; Plaintext bool "plaintext"}`
  - `PeerPairRequest{Protocol int "protocol"; InstanceID "instanceId"; Name "name"; URL "url"; Version "version"; Credential "credential"; Trust PeerTrust "trust"}`
  - `PeerPairResponse{Protocol, InstanceID, Name, Version, Credential}`
  - `PeerHello{Protocol int; InstanceID, Name, Version, URL string; StackCount int "stackCount"}`
  - `PeerUpdate{Name "name"; URL "url"}`
- Produces (peers):
  - `type Service struct{…}`; `func NewService(cfg ServiceConfig) *Service`; `type ServiceConfig struct { Store *Store; Version string; Hostname func() (string, error); Now func() time.Time; Logger *slog.Logger; Resolve func(ctx context.Context, host string) ([]netip.Addr, error); Timeout time.Duration /*unary, default 30s*/ }`
  - `(*Service) Authenticate(ctx, credential string, helloOnly bool) (Peer, error)` — `ErrNotPaired` for unknown/removed; pending rows only when `helloOnly`
  - `(*Service) AcceptPair(ctx, token string, req apitypes.PeerPairRequest) (apitypes.PeerPairResponse, error)` — §6.1 steps 3–4: consume token via `ConsumeToken`, refuse own instance ID (`ErrSelfPair`) and already-connected IDs (`ErrAlreadyPaired`), call `GET {req.URL}/api/peer/hello` with `req.Credential` and `req.Trust`, require the hello's instance ID to equal `req.InstanceID`, then mint our credential and insert an `active` peer
  - `(*Service) ApplyUpdate(ctx, from Peer, u apitypes.PeerUpdate) error`; `(*Service) AcceptUnpair(ctx, from Peer) error`
  - `(*Service) NoteSessionActivity()`
  - `const Protocol = 1`
- Produces (api):
  - Routes: `POST /api/peer/pair` (token in header), `GET /api/peer/hello`, `POST /api/peer/update`, `POST /api/peer/unpair` — registered only when `Options.Peers != nil`; auth by `X-Stackstead-Peer`, never cookies; failure → `401 {"error":"not paired","code":"peer_not_paired"}`
  - `requireSession`: cookie path unchanged plus `Peers.NoteSessionActivity()`; when there is no cookie, `Options.Peers != nil` and `X-Stackstead-Peer` is set: deny (403 `{"code":"peer_forbidden"}`) when `isPeerDenied(r.URL.Path)`, else authenticate and put `auth.User{ID: 0, Username: "peer:<name> (<user>)"}` in the context. `<user>` is `X-Stackstead-Peer-User` trimmed, printable ASCII, max 64, `"unknown"` when empty
  - `func isPeerDenied(path string) bool` — matches on `path.Clean` of the path (handles `//`, `/./`, trailing `/`)
  - `/api/health` → always `{"ok":true,"peering":1}`: the field states the binary's protocol version, independent of `Options.Peers`.

- [ ] **Step 1: Write the failing tests** (in `internal/api/peer_protocol_test.go`, using `newEnvWith` plus a real `peers.Service` on the env's DB and an `httptest.Server` that plays the pairing side)
  - `TestHealthAdvertisesPeeringProtocol`: body `{"ok":true,"peering":1}`; update `TestHealthIsPublic` to the same body.
  - `TestPairHappyPath`: a fake A server answering `/api/peer/hello` with matching instance ID; POST pair with a fresh token → 200 with `ssp_` credential and B's instance ID; B lists A as `active`; the token is consumed (a second pair → 401 `peer_not_paired`).
  - `TestPairFailures` table, each asserting status, message substring, and "nothing stored" (`CountPeers == 0`) and "token still usable" where applicable: expired token → 401; used token → 401; own instance ID → 409 "cannot pair with itself"; already-connected ID → 409; A unreachable → 502 containing "could not reach" and A's URL, token still usable; A answers hello with another instance ID → 502 `peer_wrong_instance`; A's TLS pin mismatch → 502 `peer_tls`; no message contains `ssp_` or `ssc_`.
  - `TestPeerCredentialAuthenticatesNormalRoutes`: with a stored inbound hash, `GET /api/stacks` with the header → 200; `GET /api/me` → username `peer:mini-01 (pavel)` when `X-Stackstead-Peer-User: pavel`.
  - `TestPeerCredentialDenylist` table → 403 for `GET /api/peers`, `POST /api/peers/tokens`, `GET /api/settings/update`, `POST /api/settings/metrics-token`, `POST /api/password`, `POST /api/logout`, `POST /api/setup`, `GET /api/servers/x/api/stacks`, `GET /api/fleet/stacks`, `GET /api//settings/update`, `GET /api/peers/`; and a bogus credential on `/api/stacks` → 401 `peer_not_paired`.
  - `TestPendingCredentialOnlyAnswersHello`: pending row → `GET /api/peer/hello` 200, `GET /api/stacks` 401.
  - `TestPeerConfirmIsLoggedWithPeerActor`: with `Options.Storage` + `Actions` set as in `storage_test.go`, a storage plan confirmed with a peer credential writes an `action_log` row whose `username` is `peer:mini-01 (pavel)`.
  - `TestUnpairDeletesRecord` and `TestUpdateRenamesPeer` (slug recomputed, still unique).
- [ ] **Step 2: Run** `go test -race ./internal/api/ ./internal/peers/ ./internal/apitypes/ -run 'Health|Pair|PeerCredential|Pending|PeerConfirm|Unpair|UpdateRenames|JSONTags'` — FAIL: `undefined: apitypes.PeerPairRequest`, health body mismatch.
- [ ] **Step 3: Implement** the types, `inbound.go`, `peer_protocol.go` and the `requireSession` branch. The pair handler is the only route that reads a connection token; the other three read a credential.
- [ ] **Step 4: Run** the same command, then `make test` — all `ok`.
- [ ] **Step 5: Commit** `feat(api): authenticate peers and accept pairing from another server` (why: B must be able to verify A and accept A's calls on normal routes while keeping settings and peer management out of reach), with the trailer.

---

### Task 4: Pairing, removal and updates from the local UI

**Model:** Sonnet — the two-sided protocol with pending state and partial failures.

**Files:**
- Create: `internal/peers/service.go` (outbound flows), `internal/api/peers.go` (UI routes)
- Modify: `internal/apitypes/types.go` (+ contract test), `internal/api/server.go`
- Test: `internal/peers/service_test.go`, `internal/api/peers_test.go`

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces (apitypes):
  - `ServerSelf{InstanceID "instanceId"; Name "name"; Slug "slug"; AdvertisedURL "advertisedUrl"; SuggestedURL "suggestedUrl"; Version "version"}`
  - `ServerSelfUpdate{Name "name"; AdvertisedURL "advertisedUrl"}`
  - `ConnectionToken{Token "token"; ExpiresAt time.Time "expiresAt"}`
  - `AddPeerRequest{URL "url"; Token "token"; Trust PeerTrust "trust"; SelfTrust PeerTrust "selfTrust"; AdvertisedURL "advertisedUrl"}` (`selfTrust` = how B should trust A; `Plaintext` must be equal in both)
  - `PeerProbeRequest{URL "url"}`; `PeerProbe{Peering int "peering"; FingerprintSHA256 "fingerprintSha256"; SystemTrusted bool "systemTrusted"; PlaintextAllowed bool "plaintextAllowed"}`
  - `PeerView{ID "id"; Name "name"; Slug "slug"; URL "url"; Version "version"; Status "status" /*reachable|unreachable|wrong_instance|incompatible|removed|pending*/; StackCount int "stackCount"; LastSeenAt *time.Time "lastSeenAt"; LastError "lastError"; Compatible bool "compatible"; Plaintext bool "plaintext"}`
- Produces (peers):
  - `(*Service) Self(ctx, r *http.Request) (apitypes.ServerSelf, error)` — `SuggestedURL` from scheme (`isHTTPS` rule: TLS or `X-Forwarded-Proto: https`) + `Host`
  - `(*Service) SetSelf(ctx, u apitypes.ServerSelfUpdate) (apitypes.ServerSelf, error)` — validates URL with `ValidateAddress(raw, true, …)` shape-only, then pushes `POST /api/peer/update` to every active peer in parallel; push failures are ignored (next hello carries it)
  - `(*Service) CreateConnectionToken(ctx) (apitypes.ConnectionToken, error)`
  - `(*Service) ProbeAddress(ctx, raw string) (apitypes.PeerProbe, error)`
  - `(*Service) AddServer(ctx, req apitypes.AddPeerRequest) (apitypes.PeerView, error)` — §6.1: probe `/api/health` (missing `peering` → `PeerError{Code:"peer_old"}` with the "older Stackstead" copy); persist `advertisedUrl` if given; mint the B→A credential, insert A's half as `pending` keyed by a temporary ID, call `POST {B}/api/peer/pair`, then on success replace the pending row with B's real instance ID as `active`; on any failure delete the pending row
  - `(*Service) Remove(ctx, id string) error` — delete locally first, then best-effort `POST /api/peer/unpair` with a 5 s timeout
  - `(*Service) Views(ctx) ([]apitypes.PeerView, error)`
- Produces (api routes, session-only, all in the peer denylist):
  `GET /api/peers`, `POST /api/peers` (201), `DELETE /api/peers/{id}` (204), `POST /api/peers/probe`, `POST /api/peers/tokens` (`Cache-Control: no-store`), `GET /api/peers/self`, `PUT /api/peers/self`, `POST /api/peers/{id}/check` (→ `CheckNow` from Task 5; register it in Task 5 when that method exists). Field errors use the existing `fieldErrorBody` shape (`url`, `token`, `trust.pinSha256`, `advertisedUrl`).

- [ ] **Step 1: Write the failing tests** (two real nodes: two `api.New` handlers on `httptest.NewServer` at 127.0.0.1, each with its own `store.Open` DB, auth service and `peers.Service`; helper `newPeerNode(t, name string) *peerNode` in `internal/api/peers_test.go` with `login`, `do`, and `url` methods)
  - `TestAddServerPairsBothSides`: B creates a token; A `POST /api/peers` with B's URL, the token, `trust.plaintext=true`, `selfTrust.plaintext=true` → 201; `GET /api/peers` on A lists B `active`, on B lists A `active`, both `reachable`.
  - `TestAddServerFailuresStoreNothing` table: wrong token (B answers 401) → A answers 400 with field error on `token`; B cannot reach A (A's advertised URL points at a closed port) → 502 with "could not reach", A has no pending row, B's token still valid; target without `peering` in health → "runs an older Stackstead without connected servers"; plaintext unticked for http → 400 field `trust.plaintext`.
  - `TestCreateTokenResponseIsOnlyPlaintextCopy`: response has `token` and `expiresAt`; `GET /api/peers` and `GET /api/peers/self` bodies never contain it.
  - `TestRemoveOnlineUnpairsBothSides`: A deletes B → 204; B's list is empty within the call.
  - `TestRemoveOfflineDeletesLocally`: B stopped; A deletes → 204 and A's list empty.
  - `TestSetSelfPushesUpdate`: A renames itself to `Mini Alpha` → B lists A with name `Mini Alpha`, slug `mini-alpha`.
  - `TestSuggestedURLHonoursForwardedProto`: `GET /api/peers/self` with `Host: mini.lan` and `X-Forwarded-Proto: https` → `suggestedUrl == "https://mini.lan"`.
- [ ] **Step 2: Run** `go test -race ./internal/api/ ./internal/peers/ -run 'AddServer|CreateToken|Remove|SetSelf|SuggestedURL'` — FAIL: `404` on `/api/peers` / `undefined: AddServer`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — `ok`; then `make test`.
- [ ] **Step 5: Commit** `feat(peers): add, remove and rename connected servers from Settings` (why: pairing is mutual, so A drives both halves and must leave nothing behind when B rejects it), with the trailer.

---

### Task 5: Status checker and version compatibility

**Model:** Sonnet — timing, state transitions and the "never delete on a proxy answer" rule.

**Files:**
- Create: `internal/peers/checker.go`, `internal/peers/compat.go`
- Modify: `internal/api/peers.go` (register `POST /api/peers/{id}/check` → `CheckNow`, returns `PeerView`)
- Test: `internal/peers/checker_test.go`, `internal/peers/compat_test.go`, `internal/api/peers_test.go` (`TestCheckRouteReturnsFreshStatus`: B stopped → `POST /api/peers/{B}/check` returns `status: unreachable` with a `lastError`)

**Interfaces:**
- Consumes: Tasks 1–4; `Classify`, `ErrNotPaired`.
- Produces:
  - `func Compatible(local, remote string) bool` (divergence 6)
  - `(*Service) Run(ctx context.Context)` — the loop; blocks without a timer while there are no active peers; woken by `Kick()` (called by `AddServer`, `AcceptPair`)
  - `(*Service) Kick()`
  - `(*Service) CheckNow(ctx, id string) (apitypes.PeerView, error)` (used by the UI retry)
  - `ServiceConfig` gains `After func(d time.Duration) <-chan time.Time` (test seam; default `time.After`)
  - Runtime status per peer (in memory, mirrored into `last_seen_at`, `last_error`, `version`, `stack_count`, `name`, `url`): `reachable`, `unreachable`, `wrong_instance`, `incompatible`; a hello with `ErrNotPaired` sets the row to `removed`, wipes `out_credential` and `in_hash`, sets `last_error` = "{name} removed this connection"
  - `(*Service) Usable(id string) (Peer, error)` — returns `PeerError` `peer_unknown` / `peer_unpaired` / `peer_wrong_instance` / `peer_version` / (last known `unreachable` is still attempted)

- [ ] **Step 1: Write the failing tests**
  - `TestCompatible` table: `v1.5.0`/`v1.5.3` → true; `v1.4.9`/`v1.5.0` → false; `1.5.0`/`v1.5.1` → true; `dev`/`dev` → true; `dev`/`v1.5.0` → false; `v0.3.0-5-gabc`/`v0.3.1` → true.
  - `TestCheckerIdleWithoutPeers`: fake `After` that records requested durations; run for a moment with zero peers → no durations requested.
  - `TestCheckerCadence`: one peer; `NoteSessionActivity` just now → next wait 30 s; activity 61 s ago → 5 min.
  - `TestHelloUpdatesRecord`: hello returns new name, version, URL, stack count 7 → `Views` shows them and `LastSeenAt` = fake now.
  - `TestWrongInstanceStopsForwarding`: hello with another instance ID → status `wrong_instance`; `Usable` returns `peer_wrong_instance`.
  - `TestNotPairedRemovesConnection`: hello answered 401 `peer_not_paired` → state `removed`, credentials wiped, `last_error` "mini-02 removed this connection".
  - `TestProxyAnswerNeverRemoves` table: 302 to `/login`, 200 HTML, 401 HTML, 403 JSON → state stays `active`, status `unreachable`, `last_error` mentions "proxy answered instead of Stackstead".
  - `TestIncompatibleVersion`: hello version `v9.0.0` vs local `v1.5.0` → status `incompatible`, `Usable` → `peer_version` with copy "mini-02 runs v9.0, this server v1.5. Update to match".
- [ ] **Step 2: Run** `go test -race ./internal/peers/ -run 'Compatible|Checker|Hello|WrongInstance|NotPaired|ProxyAnswer|Incompatible'` — FAIL: `undefined: Compatible`.
- [ ] **Step 3: Implement.** Hellos to several peers run in parallel, each bounded by the unary timeout.
- [ ] **Step 4: Run** — `ok`.
- [ ] **Step 5: Commit** `feat(peers): check connected servers and drop connections the other side removed` (why: the sidebar needs live status, and only a Stackstead `peer_not_paired` answer — never a proxy page — may end a connection), with the trailer.

---

### Task 6: Forwarding `/api/servers/{id}/api/…`

**Model:** Sonnet — reverse proxying with SSE, shutdown and header hygiene.

**Files:**
- Create: `internal/api/forward.go`
- Modify: `internal/api/server.go`
- Test: `internal/api/forward_test.go`

**Interfaces:**
- Consumes: `(*peers.Service) Usable`, `NewHTTPClient` transport, `Options.Closing`, `sseWriteTimeout()`.
- Produces:
  - Route `/api/servers/{id}/api/{rest...}` (all methods) behind `requireSession` (cookie only — it is in the peer denylist)
  - `func (s *server) handleForward(w http.ResponseWriter, r *http.Request)` using `httputil.ReverseProxy{Rewrite, ModifyResponse, ErrorHandler, FlushInterval: -1, Transport: peer transport}`
  - Request: target = peer URL + prefix + `/api/` + `rest`, raw query kept; removes `Cookie`, `Authorization`, `X-Stackstead-Peer*`, `Forwarded`, `X-Forwarded-*`; sets `X-Stackstead-Peer` (revealed `Secret`) and `X-Stackstead-Peer-User` (local username); passes `Content-Type`, `Accept`, `Last-Event-ID`, `If-Match`, `If-None-Match`
  - Response: removes `Set-Cookie`; 401 → 502 `peer_unpaired`; 3xx → 502 `peer_proxy`; everything else (including `X-Stackstead-Checks`, 400 field errors, 409) passes unchanged
  - Errors: `ErrorHandler` writes 502 JSON `{"error":"Can't reach mini-02: connection refused","code":"peer_unreachable"}` / `peer_tls`; `Usable` errors map to their codes; unknown id → 404 `peer_unknown`
  - Shutdown: the outbound request context is cancelled when `Options.Closing` closes; writes go through a `ResponseWriter` wrapper that sets a fresh write deadline (`sseWriteTimeout()`) per `Write` via `http.ResponseController`, ignoring `http.ErrNotSupported`

- [ ] **Step 1: Write the failing tests** (`newPeerNode` pair from Task 4, already paired)
  - `TestForwardUnary`: `GET A/api/servers/{B}/api/stacks` → B's stack list; `PUT …/api/stacks/web/compose` with a stale hash → B's 409 body unchanged; a partial-checks response keeps `X-Stackstead-Checks: partial`.
  - `TestForwardStripsBrowserCredentials`: a recording upstream sees no `Cookie`, no `Authorization`, the peer header present, `X-Stackstead-Peer-User` = the local username; the browser response has no `Set-Cookie`.
  - `TestForwardMapsPeer401`: upstream 401 → A answers 502 with `code: peer_unpaired` (never 401). Session expiry on A → 401 (unchanged).
  - `TestForwardErrorsNeverLeakCredential`: B stopped → 502 `peer_unreachable`, message names B, body and captured `slog` output contain no `ssp_`; pin mismatch → `peer_tls`.
  - `TestForwardSSE`: upstream SSE handler writes an event, waits, writes another; the client receives the first before the second is written (flush-per-write); `Last-Event-ID` reaches upstream.
  - `TestForwardStreamEndsOnClosing`: open a forwarded stream, close `Options.Closing` → the client's body read ends within 1 s.
  - `TestForwardIncompatibleOrWrongInstance`: marked peers → 502 `peer_version` / `peer_wrong_instance`, no upstream request made.
- [ ] **Step 2: Run** `go test -race ./internal/api/ -run Forward` — FAIL: 404 on `/api/servers/…`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — `ok`; then `make test`.
- [ ] **Step 5: Commit** `feat(api): forward a signed-in user's calls and streams to a connected server` (why: the SPA is same-origin with a SameSite=Strict cookie, so the local server must carry every remote call), with the trailer.

---

### Task 7: All-stacks aggregate and recent events

**Model:** Sonnet — parallel fan-out with timeouts and partial results.

**Files:**
- Modify: `internal/state/cache.go` (ring of the last 50 forwarded events), `internal/api/observe.go` (`GET /api/events/recent`)
- Create: `internal/api/fleet.go`
- Modify: `internal/apitypes/types.go` (+ contract test)
- Test: `internal/state/cache_test.go` (extend), `internal/api/fleet_test.go`

**Interfaces:**
- Consumes: `Options.Stacks.List`, `toSummary`, `(*peers.Service) Usable`/`Views`, peer HTTP client.
- Produces:
  - `(*state.Cache) RecentEvents() []engine.Event` (newest first, max 50); api checks it through an optional interface `type recentEventsSource interface{ RecentEvents() []engine.Event }` on `Options.Observe`, so existing fakes need no change
  - `GET /api/events/recent` → `[]apitypes.DockerEvent` (reuse the event DTO the `/api/events` stream sends)
  - apitypes: `FleetServer{ID "id"; Name "name"; Slug "slug"; Local bool "local"; Status "status"; Error "error"; LastSeenAt *time.Time "lastSeenAt"}`; `FleetStack{Server string "server"; StackSummary (embedded, flattened)}`; `FleetEvent{Server string "server"; DockerEvent (embedded)}`; `FleetStacks{Servers []FleetServer "servers"; Stacks []FleetStack "stacks"; Events []FleetEvent "events"}`
  - `GET /api/fleet/stacks` (session only): local first, then active peers in added order; per peer `GET /api/stacks` and `GET /api/events/recent` in parallel with a 3 s timeout; failed/unusable peers appear in `servers` with `status` and `error` and contribute no stacks; events merged newest first, max 50

- [ ] **Step 1: Write the failing tests**
  - `TestCacheKeepsRecentEvents`: 60 forwarded events → `RecentEvents()` has 50, newest first.
  - `TestFleetStacksMergesServers`: A with stacks `web`, B with `pihole` → `stacks` has both with `server` = each instance ID; `servers[0].local == true`; events tagged.
  - `TestFleetStacksWithPeerDown`: B stopped → response 200 within 3.5 s (fake slow upstream that sleeps 10 s for the timeout case), B in `servers` with `status: unreachable` and an error, no B stacks.
  - `TestFleetStacksSkipsIncompatiblePeer`: status `incompatible`, no request sent to it.
  - `TestFleetWithoutPeers`: only local stacks; no outbound requests.
- [ ] **Step 2: Run** `go test -race ./internal/state/ ./internal/api/ -run 'RecentEvents|Fleet'` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — `ok`.
- [ ] **Step 5: Commit** `feat(api): list every connected server's stacks in one call` (why: All stacks must render instantly when a peer is down, so peers are fetched in parallel with a short timeout and reported separately), with the trailer.

---

### Task 8: Wiring, in-process two-server test, e2e

**Model:** Sonnet — real-process orchestration and secret scans.

**Files:**
- Modify: `internal/cli/serve.go` (build `peers.NewService` from `db`, `version.Get().Version`, `os.Hostname`; pass `Options.Peers`; start `peersSvc.Run` in `startBackground`)
- Create: `internal/api/peers_integration_test.go`
- Modify: `cmd/stackstead/e2e_test.go`
- Test: `internal/cli/serve_test.go` (extend if it asserts the options/background list)

**Interfaces:**
- Consumes: everything above.
- Produces: nothing new.

- [ ] **Step 1: Write the failing tests**
  - `TestConnectedServersInProcess` (`internal/api/peers_integration_test.go`, runs in `make test`): two `newPeerNode`s with `peers.Run` started; pair A→B; both list each other; forwarded `GET /api/stacks`; forwarded SSE `/api/events` delivers `status`; `/api/fleet/stacks` has both; B removes A → A's next `CheckNow` shows `removed`; a capture of both servers' `slog` output and every response body contains neither the token nor either credential (search for `ssc_` and `ssp_`).
  - `TestE2EConnectedServers` (`//go:build e2e`): `startServer` twice; set up both admins; on B `POST /api/peers/tokens`; on A `PUT /api/peers/self` with `http://127.0.0.1:<portA>`, then `POST /api/peers` with plaintext both ways → 201; B's `GET /api/peers` lists A; `GET A/api/servers/{B}/api/stacks` → 200; A removes B → B's list empty; `redact(stderr)` of both processes contains no `ssc_`/`ssp_`.
- [ ] **Step 2: Run** `go test -race ./internal/api/ -run TestConnectedServersInProcess` — FAIL until wiring helpers exist; `make e2e` — FAIL: `/api/peers/tokens` 404 (not wired in `serve.go`).
- [ ] **Step 3: Implement** the wiring.
- [ ] **Step 4: Run** `make test` and `make e2e` (needs a built UI: `make ui` first) — `ok` / `PASS`. Then `go vet ./...` and `test -z "$(gofmt -l .)"`.
- [ ] **Step 5: Commit** `feat(cli): run connected servers in stackstead serve` (why: the service and its checker must live with the HTTP server so shutdown stops them in order), with the trailer.

---

### Task 9: Scoped API and SSE clients, per-server store factories

**Model:** Sonnet — refactors shared singletons without changing local behaviour.

**Files:**
- Modify: `web/src/lib/api/client.ts`, `web/src/lib/api/types.ts`, `web/src/lib/sse/client.ts`, `web/src/lib/stores/live.ts`, `stacks.ts`, `host.ts`, `runningOps.ts`, `operationTracker.ts`
- Test: `web/src/lib/api/client.test.ts`, `web/src/lib/sse/client.test.ts`, `web/src/lib/stores/live.test.ts`, `stacks.test.ts` (extend)

**Interfaces:**
- Consumes: wire types from T3, T4, T7 (mirror them in `types.ts`: `ServerSelf`, `ConnectionToken`, `AddPeerRequest`, `PeerTrust`, `PeerProbe`, `PeerView`, `FleetServer`, `FleetStack`, `FleetEvent`, `FleetStacks`).
- Produces:
  - `export type Api = ReturnType<typeof createApi>`; `export function createApi(base: string): …` (same members as today's `api`, every path prefixed with `base`); `export const api = createApi('')`; new local-only members on `api`: `peers()`, `addPeer(req)`, `removePeer(id)`, `probePeer(url)`, `createConnectionToken()`, `serverSelf()`, `updateServerSelf(body)`, `checkPeer(id)` (`POST /api/peers/{id}/check`), `fleetStacks()`
  - `ApiError.code: string | undefined` (from the body's `code`)
  - `export function scopedOpen(base: string): typeof openSSE` (prefixes `url`)
  - Factories returning the same members the modules export today: `createLiveStore()`, `createStacksStore()`, `createHostStore()`, `createRunningOpsStore()`, `createOperationTracker()`; module-level exports become one local instance each, so existing imports and tests keep working

- [ ] **Step 1: Write the failing tests**
  - `createApi prefixes every path`: with the fake fetch, `createApi('/api/servers/b1').stacks()` requests `GET /api/servers/b1/api/stacks`; `.stack('a b')` → `/api/servers/b1/api/stacks/a%20b`.
  - `ApiError exposes the code`: 502 `{"error":"Can't reach mini-02","code":"peer_unreachable"}` → `err.code === 'peer_unreachable'`, `err.status === 502`.
  - `a 502 peer error does not trigger the unauthorized handler`, and `a 401 under /api/servers/ does` (Review Focus 5).
  - `scopedOpen prefixes the stream URL`: fake `fetchImpl` sees `/api/servers/b1/api/events`.
  - `two live stores are independent`: messages fed to one do not change the other's `events`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/lib/api src/lib/sse src/lib/stores` — FAIL: `createApi is not exported`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same, then `make ui-check` and `make ui-test` — all pass (existing tests unchanged).
- [ ] **Step 5: Commit** `refactor(web): let the API client, SSE opener and live stores target any server` (why: remote screens reuse every local screen, so the data layer must take a server base instead of a hard-coded origin), with the trailer.

---

### Task 10: Server scope, `/s/{slug}` routes and the peers store

**Model:** Sonnet — routing and lifecycle design every later web task builds on.

**Files:**
- Create: `web/src/lib/servers/peers.ts`, `web/src/app/servers/ServerContext.tsx`, `web/src/app/servers/RemoteServer.tsx`, `web/src/app/servers/RemoteServer.module.css`
- Modify: `web/src/lib/router.ts`, `web/src/app/routes.tsx`, `web/src/app/App.tsx`, `web/src/lib/shell/breadcrumb.ts`, `web/src/app/hooks/useDocumentTitle.ts`
- Test: `web/src/lib/servers/peers.test.ts`, `web/src/lib/router.test.ts`, `web/src/lib/shell/breadcrumb.test.ts`, `web/src/app/servers/remoteServer.test.tsx`

**Interfaces:**
- Consumes: T9 factories, `createApi`, `scopedOpen`.
- Produces:
  - `peers.ts`: `export const peers: Readable<{ items: PeerView[]; loaded: boolean }>`; `startPeers(deps: { list: () => Promise<PeerView[]>; doc?: Document }): () => void` (fetch at start, then every 30 s while visible); `refreshPeers(): Promise<void>`
  - `router.ts`: `splitServerPath(path: string): { slug: string | null; inner: string }` (`/s/mini-02/stacks/x` → `{slug:'mini-02', inner:'/stacks/x'}`; `/s/mini-02` → inner `/`)
  - `ServerContext.tsx`: `interface ServerScope { local: boolean; id: string; slug: string; name: string; api: Api; open: typeof openSSE; live; changes; events; stacks; refreshStacks; host; hostHistory; runningOps; trackOperation; href(path: string): string }`; `ServerContext` whose **default value is the local scope** built from the module singletons (so screens outside a provider, and every existing test, behave as today); `useServer(): ServerScope`
  - `RemoteServer.tsx`: route element for `/s/:server/*`; resolves the slug from `peers`; unknown slug → NotFound; while mounted creates one scope (`createApi('/api/servers/'+id)`, `scopedOpen(...)`, fresh live/stacks/host/runningOps/tracker) and stops it on unmount or slug change; renders nested `<Routes>` from `SERVER_ROUTES`; state screens with the Global Constraints copy for `unreachable` (with **Retry** → `api.checkPeer`), `incompatible`, `wrong_instance`, `removed`
  - `routes.tsx`: `SERVER_ROUTES` = `ROUTES` minus `/settings`, `/login`, `/setup`, `*`, with leading `/` stripped (`/` → index); add `{ path: '/all', title: 'All stacks', screen: lazy(AllStacks placeholder until T14) }` and `{ path: '/s/:server/*', screen: RemoteServer }`; `findRoute` uses `splitServerPath`
  - `breadcrumb(path, serverName?)`: when `serverName` is given the first crumb is `{ label: serverName, href: '/s/{slug}' }`
  - `useDocumentTitle(route, authenticated, serverName?)` → `"{title} · {serverName} · Stackstead"` for peers

- [ ] **Step 1: Write the failing tests**
  - `splitServerPath` table (above cases, plus `/stacks/x` → `slug: null`, `/s/` → `slug: null`).
  - `peers store polls every 30 s while visible and not while hidden` (fake timers, fake `doc.visibilityState`).
  - `remote Overview loads from the peer`: fake fetch with `/api/peers` listing `mini-02` (id `b1`, reachable) and `/api/servers/b1/api/stacks` → mounting `/s/mini-02` shows the peer's stack and makes no request to local `/api/stacks` for the page content; exactly one `/api/servers/b1/api/events` stream is opened.
  - `leaving the peer closes its events stream` (navigate to `/` → the remote stream's abort signal fires).
  - `unreachable peer shows the can't-reach state` with "Can't reach mini-02 (last seen" and a **Retry** button that calls `POST /api/peers/b1/check`.
  - `incompatible peer shows the update message` with the exact copy.
  - `/s/mini-02/settings is not found`.
  - `breadcrumb prefixes the server` and `document title includes the server name`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/lib/servers src/lib/router.test.ts src/lib/shell src/app/servers` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same, then `make ui-check && make ui-test`.
- [ ] **Step 5: Commit** `feat(web): open any screen on a connected server under /s/{server}` (why: remote screens are the local ones with another data source, so they share code and keep shareable, reload-safe URLs), with the trailer.

---

### Task 11: Migrate stack, detail, create and overview screens to the server scope

**Model:** Haiku — mechanical: replace singleton imports with `useServer()` members and absolute links with `href()`; no decisions.

**Files (modify):** `web/src/app/routes/Overview.tsx`, `web/src/app/hooks/useDashboard.ts`, `web/src/app/overview/{useStackAction.ts,StatusPanel.tsx,HostStats.tsx,TopConsumers.tsx,StackTowers.tsx,ActivityFeed.tsx}`, `web/src/app/detail/{StackDetailScreen.tsx,useStackDetail.ts,MetricsTab.tsx,ServiceEditor.tsx,AddServicePanel.tsx,ComposeEditor.tsx,ServiceOverview.tsx,useLogStream.ts}`, `web/src/app/create/{StackCreateScreen.tsx,StackImportScreen.tsx,useCreateForm.ts}`, `web/src/app/stacks/{FileBrowser.tsx,NetworkPicker.tsx}`, `web/src/lib/stacks/{operation.ts,detail.ts,lifecycle.ts}`, `web/src/lib/dashboard/{controller.ts,tiles.ts}`, `web/src/app/routes/NotFound.tsx`
- Test: `web/src/app/servers/remoteScreens.test.tsx` (new)

**Interfaces:**
- Consumes: `useServer()` (T10). Rules: `import { api }` → `const { api } = useServer()` in components/hooks; `lib/` modules take what they need as a parameter (`deps.get`, `deps.open`, `deps.changes`) — never import `useServer`; `<Link to="/stacks/…">` and `navigate('/…')` → `href('/…')`; SSE stores get `open` from the scope.
- Produces: no new names.

- [ ] **Step 1: Write the failing test** `remoteScreens.test.tsx`, table over paths, each asserting every API/SSE request goes to `/api/servers/b1/api/…` and every in-page link starts with `/s/mini-02/`: `/s/mini-02` (Overview), `/s/mini-02/stacks/web` (detail: `GET …/stacks/web`, logs stream `…/stacks/web/logs`), `/s/mini-02/stacks/web?tab=compose` (compose load), `/s/mini-02/stacks/new` (submit posts to `…/api/stacks` and navigates to `/s/mini-02/stacks/web`), `/s/mini-02/stacks/import` (file browser hits `…/api/fs`).
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/servers/remoteScreens.test.tsx` — FAIL: requests go to `/api/stacks`.
- [ ] **Step 3: Implement** the replacements listed above.
- [ ] **Step 4: Run** the test, then `make ui-check && make ui-test` (all existing local tests must still pass unchanged).
- [ ] **Step 5: Commit** `refactor(web): read stacks, details and create screens from the viewed server` (why: the same screens now serve local and remote servers), with the trailer.

---

### Task 12: Migrate containers, networks and storage screens

**Model:** Haiku — the same mechanical rules as Task 11.

**Files (modify):** `web/src/app/containers/{ContainersScreen.tsx,ContainerDetailScreen.tsx,ContainerActions.tsx,ContainerPeek.tsx,ContainerRow.tsx,useContainerAction.ts,useContainerList.ts}`, `web/src/lib/containers/list.ts`, `web/src/app/networks/{NetworksScreen.tsx,NetworkCard.tsx,CreateNetworkDialog.tsx,useNetworkList.ts}`, `web/src/lib/networks/networks.ts`, `web/src/app/storage/{StorageScreen.tsx,StorageImageScreen.tsx,StorageVolumeScreen.tsx,CleanupDialog.tsx,DetailSections.tsx,VolumeRows.tsx,UsedBy.tsx,useRecalculate.ts,useStorageRefresh.ts}`, `web/src/lib/storage/controller.ts`
- Test: extend `web/src/app/servers/remoteScreens.test.tsx`

**Interfaces:** as Task 11.

- [ ] **Step 1: Write the failing test cases** in the same table: `/s/mini-02/containers` (`…/api/containers`, row link `/s/mini-02/containers/{name}`), `/s/mini-02/containers/web-1`, `/s/mini-02/networks` (create posts to `…/api/networks`), `/s/mini-02/storage` (`…/api/storage`), `/s/mini-02/storage/volumes/data` and the cleanup confirm posting to `…/api/storage/plans/{id}/confirm`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/servers/remoteScreens.test.tsx` — FAIL on the new cases.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the test, then `make ui-check && make ui-test`.
- [ ] **Step 5: Commit** `refactor(web): read containers, networks and storage from the viewed server`, with the trailer.

---

### Task 13: Accordion sidebar, mobile server switcher, host block and palette

**Model:** Sonnet — layout, accessibility and the zero-peer invariant.

**Files:**
- Create: `web/src/app/shell/ServerNav.tsx`, `ServerNav.module.css`, `web/src/app/shell/ServerSwitcher.tsx` (mobile)
- Modify: `web/src/app/shell/{Sidebar.tsx,Shell.tsx,TabBar.tsx,MobileTopBar.tsx,HostBlock.tsx,TopBar.tsx,nav.ts,palette.ts,CommandPalette.tsx}`
- Test: `web/src/app/shell/shell.test.tsx` (extend), `web/src/app/shell/palette.test.ts` (extend)

**Interfaces:**
- Consumes: `peers` store, `useServer()`, `api.fleetStacks()`.
- Produces:
  - `nav.ts`: `serverNav(slug: string | null): NavEntry[]` (the six entries with `href` prefixed for peers and `match` against the inner path)
  - `ServerNav` props `{ current: string; localName: string; localStackCount: number; peers: PeerView[]; attention: boolean }`: "All stacks" (`/all`, count = local + reachable peers' `stackCount`), then a heading per server ("this server" label on the local one) with a status `Led` (reachable/unreachable/wrong instance) and, when collapsed, the count or "unreachable"; the viewed server is expanded (`aria-expanded`); clicking a collapsed heading navigates to its Stacks screen
  - `Sidebar`: renders today's markup when `peers.items.length === 0`, `ServerNav` otherwise; Settings and the user block unchanged; `HostBlock` shows `useServer().host`
  - `ServerSwitcher`: a `<select aria-label="Server">` in `MobileTopBar` shown only with peers; `TabBar` links use `serverNav(slug)`
  - `palette.ts`: `paletteEntries` gains `remote: FleetStack[]` and `servers: FleetServer[]`; remote stacks navigate to `/s/{slug}/stacks/{name}` and show the server name as detail; `CommandPalette` fetches `api.fleetStacks()` once per open when peers exist

- [ ] **Step 1: Write the failing tests**
  - `sidebar without peers is unchanged`: no "All stacks", no "this server" (existing shell tests stay green).
  - `sidebar with one peer` shows All stacks with the summed count, "this server" expanded with Stacks/Containers/Networks/Storage/New stack/Import, `mini-02` collapsed with its count.
  - `viewing a peer expands it and collapses this server`; `clicking a collapsed server opens its Stacks`.
  - `unreachable peer shows "unreachable" and a danger dot`; `several peers keep added order`.
  - `mobile switcher appears only with peers and navigates to /s/{slug}`.
  - `palette lists remote stacks with the server name` and selecting one navigates to `/s/mini-02/stacks/pihole`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/shell` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same, then `make ui-check && make ui-test` (bundle budget check runs in `make ui-test`'s build: entry ≤ 120 kB gz).
- [ ] **Step 5: Commit** `feat(web): group the sidebar by server and switch servers on mobile`, with the trailer.

---

### Task 14: All stacks screen

**Model:** Sonnet — combines data from several servers with filtering and partial failure.

**Files:**
- Create: `web/src/lib/servers/fleet.ts`, `web/src/app/routes/AllStacks.tsx`, `AllStacks.module.css`
- Modify: `web/src/app/overview/StacksList.tsx`, `StackRow.tsx` (optional `server` chip column and per-row `href`/`onAct`), `web/src/app/overview/ActivityFeed.tsx` (optional server tag), `web/src/app/routes.tsx` (real screen)
- Test: `web/src/lib/servers/fleet.test.ts`, `web/src/app/routes/allStacks.test.tsx`

**Interfaces:**
- Consumes: `api.fleetStacks()`, `createApi` (row actions call the row's server), `useStackAction` behaviour.
- Produces:
  - `createFleet(deps: { load: () => Promise<FleetStacks>; doc?: Document }): { state: Readable<FleetState>; start(): void; stop(): void; refresh(): Promise<void> }`, polling every 5 s while visible
  - `StacksList` props gain `serverOf?: (s) => { name: string; slug: string | null }` and `hrefOf?: (s) => string`
  - `AllStacks`: `StatusPanel` with combined items and `host={undefined}`, filter chips `All · {server}…` (`aria-pressed`), Server chip column, side column events tagged with server name, danger banner "Can't reach {name}" per unreachable/incompatible server

- [ ] **Step 1: Write the failing tests**
  - `fleet polls every 5 s only while visible`.
  - `All stacks lists both servers' stacks with a Server column`; local row links to `/stacks/web`, remote to `/s/mini-02/stacks/pihole`.
  - `filter chip shows only that server's stacks`.
  - `unreachable server shows a banner and omits its stacks`.
  - `row action on a remote stack posts to /api/servers/b1/api/stacks/pihole/actions/restart`.
  - `/all opens no extra event stream` (stream count unchanged from the local one).
- [ ] **Step 2: Run** `cd web && npx vitest run src/lib/servers/fleet.test.ts src/app/routes/allStacks.test.tsx` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same, then `make ui-check && make ui-test`.
- [ ] **Step 5: Commit** `feat(web): add the All stacks screen across connected servers`, with the trailer.

---

### Task 15: Settings → Connected servers card

**Model:** Sonnet — multi-step form with one-time secrets and TLS choices; follows `MetricsTokenCard.tsx`.

**Files:**
- Create: `web/src/app/settings/ConnectedServersCard.tsx`, `ConnectedServersCard.module.css`
- Modify: `web/src/app/settings/SettingsScreen.tsx` (section `settings-servers`, label "Connected servers", after Prometheus)
- Test: `web/src/app/settings/connectedServers.test.tsx`

**Interfaces:**
- Consumes: `api.serverSelf/updateServerSelf/createConnectionToken/probePeer/addPeer/removePeer/peers`, `refreshPeers()`, `CopyButton`, `ConfirmDialog`, `Field`, `SettingsCard`.
- Produces: `ConnectedServersCard()`; the token lives only in component state (as in `MetricsTokenCard`).
- Behaviour: **This server** (name editable, advertised address editable with the suggested value as placeholder and a "Use suggested" button, instance ID read-only small); **Create connection token** shows token + copy button + "Expires at {time}"; **Add server** form: address, token, **Check address** (probe) → shows fingerprint with "Trust this certificate" when not system-trusted, CA PEM textarea, plaintext checkbox (only when `plaintextAllowed`) labelled "I accept sending a root-equivalent credential unencrypted", confirmation line "Mini-02 will reach this server at {advertised URL}"; field errors inline; peer list rows (name, address, version, status, last seen, last error, version warning, **Remove** with `ConfirmDialog`); removed peers show "{name} removed this connection" with **Dismiss** (= Remove).

- [ ] **Step 1: Write the failing tests**
  - `creates a token and shows it once`: after Create, the token and expiry show; remount → token gone; `GET /api/peers` never contains it.
  - `probe shows the fingerprint and pins it on add` (request body `trust.pinSha256`).
  - `plaintext checkbox appears only when allowed` and its value reaches `trust.plaintext` and `selfTrust.plaintext`.
  - `add-server error is shown inline on the token field` (401 field error) and `older server copy is shown`.
  - `remove asks for confirmation then deletes and refreshes peers`.
  - `version mismatch shows a warning on the row`.
  - `renaming this server sends PUT /api/peers/self`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/settings/connectedServers.test.tsx` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same, then `make ui-check && make ui-test`.
- [ ] **Step 5: Commit** `feat(web): manage connected servers in Settings`, with the trailer.

---

### Task 16: Documentation

**Model:** Haiku — prose from the spec and the routes above; no decisions.

**Files:** `docs/security.md` (new section "Connected servers"), `docs/api.md` (`/api/peers*`, `/api/peer/*`, `/api/servers/{id}/…`, `/api/fleet/stacks`, `/api/events/recent`, `peering` in health, `code` in error bodies), `docs/install.md` (nginx `location` letting `X-Stackstead-Peer` requests to `/api/` bypass the proxy's own auth, plus a Caddy/Traefik forward-auth note), `README.md` (one feature line).

- [ ] **Step 1:** Write the sections. `security.md` must state: a connection is root on both machines; HTTPS required, plaintext opt-in only for private addresses; credentials stored in SQLite in the 0700 state dir and included in `db backup`; re-pair after restoring a backup onto a different machine (cloned instance ID); peers can read each other's databases through the file browser or compose, so the denylist stops accidental chaining only.
- [ ] **Step 2: Verify** every route in `docs/api.md` exists: `grep -n '"/api/peer\|/api/peers\|/api/servers\|/api/fleet' internal/api/*.go` matches each documented route; `make check` passes.
- [ ] **Step 3: Commit** `docs: describe connected servers, their trust model and proxy setup`, with the trailer.

---

## Self-review

1. **Spec coverage.** §5.1 identity/slug/advertised address → T1, T4; §5.2 tokens → T1; §5.3 credentials, header, never returned → T2, T3, T4, T8 scans; §5.4 storage → T1 (divergence 1); §5.5 proxy answers → T2, T5, docs T16; §6.1 → T3 (B side), T4 (A side); §6.2 → T5; §6.3 → T4 (push), T5 (catch-up via hello); §6.4 → T4, T5; §6.5 denylist and actor → T3; §7.1–7.3 → T6; §7.4 → T7; §7.5 → T10, T14 tests; §8.1 → T13; §8.2 → T10–T12, palette T13; §8.3 → T14; §8.4 → T15; §9 compatibility → T5, T4 (`peer_old`), health T3; §10 nothing to build (identity by instance ID and one transport layer keep the door open); §11 tests → spread as listed; §12 docs → T16. Success criteria "no peers = unchanged UI and no server background work" → T5 `TestCheckerIdleWithoutPeers`, T13 "sidebar without peers is unchanged".
2. **Step scan.** Every code step names files, signatures and pinned values; no function bodies. The one judgement left open on purpose is CSS/visual layout of the accordion and switcher (spec: "builder's call").
3. **Type consistency.** `Secret`, `TLSOptions`, `Peer`, `PeerState`, `Service`, `Usable`, `CheckNow`, `Kick`, `NoteSessionActivity`, `PeerView.status` values, error codes, `createApi`/`Api`/`scopedOpen`/`useServer`/`ServerScope.href`, `FleetStacks` are spelled the same in every task. `checkPeer` (T9) relies on `POST /api/peers/{id}/check`, listed in T4 and registered in T5.
4. **Review Focus.** Five lines above, each with its test in the owning task.
5. **Proportion.** Plan is roughly three times the spec's length for 16 tasks across Go and web, with no code bodies.
