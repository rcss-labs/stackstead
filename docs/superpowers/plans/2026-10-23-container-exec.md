# Container Exec Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Interactive and non-interactive `docker exec` into any running container: from a Terminal tab in the web UI, and from `stackstead exec` in the CLI. It also works through connected servers.

**Architecture:** The engine client gains exec primitives that hijack the Docker socket connection. A WebSocket route `GET /api/containers/{id}/exec` (`golang.org/x/net/websocket`) bridges the browser or CLI to the exec stream. It uses a small framed protocol: binary frames with a stream-ID byte, plus JSON control frames for ping, resize, eof, exit, idle and closed. Connected servers reach the route through the existing reverse-proxy forward. The web tab uses xterm.js, loaded lazily. The CLI uses a new `internal/client/execws` package.

**Tech Stack:** Go 1.26 (stdlib, `golang.org/x/net/websocket`, `golang.org/x/term`), React 19 + Vite + vitest, `@xterm/xterm`, `@xterm/addon-fit`.

**Spec:** `docs/superpowers/specs/2026-10-23-container-exec-design.md`. Read it before starting any task.

## Global Constraints

- Server WebSocket library: `golang.org/x/net/websocket` only (new `go.mod` dependency). No gorilla or coder.
- `internal/client` stays stdlib + `internal/apitypes` only (`internal/client/deps_test.go`). The WebSocket dialer lives in `internal/client/execws`.
- Route `{id}` is a container ID only: `containerIDPattern` `^[0-9a-f]{12,64}$` plus the inspect-prefix check (`inspectContainer`). Names are never accepted.
- Origin check: compare Origin **host[:port]** to request `Host`. Never compare the scheme. A missing Origin is allowed.
- Config keys (flat, with env overrides `STACKSTEAD_EXEC_ENABLED`, `STACKSTEAD_EXEC_MAX_SESSIONS`, `STACKSTEAD_EXEC_IDLE_TIMEOUT`): `exec_enabled: true`, `exec_max_sessions: 8` (must be ≥ 1), `exec_idle_timeout: 30m` (`0s` disables; durations need units).
- Protocol timings: ping every **20 s**; close if no pong for **60 s**; session re-check on every ping tick; idle warning **60 s** before the deadline, or at half the timeout if that is shorter; post-disconnect `InspectExec` after **2 s**.
- Auto shell argv: `["/bin/sh","-c","if [ -x /bin/bash ]; then exec /bin/bash; else exec /bin/sh; fi"]`.
- TTY execs set `Env: ["TERM=xterm-256color"]`.
- Validation: `cmd` ≤ 64 args, ≤ 4 KiB in total, no NUL; `user` ≤ 64 chars, no NUL or whitespace.
- Action log: `Kind: "container.exec"`; `Scope` = stack name, or `"host"` when the container belongs to no stack; end `Result` ∈ `exit <code>`, `disconnected`, `detached`, `idle`, `session_expired`, `shutdown`.
- Error codes (JSON `code`): `exec_disabled` (403), `bad_origin` (403), `not_running` (409), `exec_limit` (429), `container_not_found` (404).
- Web bundle: `TOTAL_BUDGET` 400 kB → **500 kB** gz; `ENTRY_BUDGET` stays 120 kB. xterm must not be in the entry chunk.
- Pin exact npm versions (no `^`/`~`), matching `web/package.json` style.
- Test-first for every behaviour task. Run `go vet ./... && gofmt -l .` (empty) before each Go commit, and `cd web && npm run check && npx vitest run` before each web commit.
- Conventional commits explaining why, ending with:
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` and
  `Claude-Session: https://claude.ai/code/session_01Y7wbvmmyu4RHW7arKEJyvi`.

## Review Focus

1. **Quiet shell through a connected server**: a session idle on output longer than `sseWriteTimeout` must stay open (the `deadlineWriter` deadline must not survive the hijack). The test lives in Task 6.
2. **Terminal left in raw mode**: a CLI error, Ctrl-C or connection drop mid-session must restore the terminal. The test lives in Task 8 (restore called on every return path through a terminal seam).
3. **Distroless image (no `/bin/sh`)**: the user should get an `error` frame saying "no shell found in this container; pass a command", not a dropped socket or a 500. The test lives in Task 4.
4. **Large paste or burst output**: stdin chunks up to the frame cap and fast output (`yes | head -c 5M`) must not deadlock or drop bytes. `MaxPayloadBytes` is 1 MiB on both ends. The test lives in Task 3 (5 MiB echo round-trip through the session loop with fakes).
5. **Reconnect storm hitting the cap**: closing a tab must release the session slot immediately, so a quick Reconnect never gets `exec_limit`. The test lives in Task 4 (open cap sessions, close one, the next open succeeds).

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/engine/exec.go` (new) | Exec create, start (hijack), resize, inspect; `ExecStream` |
| `internal/engine/client.go` (modify) | Store a `dial` func so `exec.go` can open a raw socket connection |
| `internal/engine/stream.go` (modify) | Export-neutral reuse of `demuxLogs` for exec non-TTY output |
| `internal/config/config.go`, `template.go`, `template.yaml`, `deploy/config.example.yaml` | Three exec keys |
| `internal/api/execproto.go` (new) | Frame codec and control message types (shared shape with web and CLI) |
| `internal/api/execsession.go` (new) | Session loop: pumps, ping/pong, idle, re-check, exit, end reason |
| `internal/api/exec.go` (new) | Route, pre-upgrade checks, Origin handshake, cap, audit |
| `internal/api/forward.go` (modify) | Strip Origin after validation; no deadline on upgraded connections |
| `internal/apitypes/types.go`, `internal/api/containers.go` (modify) | `ContainerRow.Number` |
| `internal/client/client.go` (modify) | `Containers()`, `Config()` accessor |
| `internal/client/execws/` (new) | CLI-side WebSocket dial and protocol |
| `internal/cli/cmd_exec.go` (new), `commands.go` (modify) | `stackstead exec` |
| `internal/cli/serve.go` (modify) | Wire config and engine into `api.Options` |
| `web/src/lib/exec/` (new) | Browser protocol client |
| `web/src/app/containers/TerminalTab.tsx` (+ css, new) | xterm tab |
| `web/src/app/containers/ContainerDetailScreen.tsx`, `ContainerRow.tsx`, `web/src/app/detail/ServiceOverview.tsx` (modify) | Tab and entry points |
| `web/scripts/check-bundle.mjs` (modify) | Budget |
| `docs/cli.md`, `docs/security.md`, `docs/api.md` (modify) | Docs |

Suggested models (per CLAUDE.md): Task 1, 3, 4, 6, 8 → **Sonnet** (hijacked I/O, concurrency, security); Tasks 2, 5, 7, 9, 10, 11 → **Haiku** unless a decision comes up, in which case escalate.

---

### Task 1: Engine exec primitives

**Files:**
- Create: `internal/engine/exec.go`, `internal/engine/exec_test.go`
- Modify: `internal/engine/client.go` (`Client` struct + `New`), `internal/engine/integration_test.go`

**Interfaces:**
- Produces:
  ```go
  type ExecSpec struct { Cmd []string; User string; Tty bool; Env []string }
  type ExecInspect struct { Running bool; ExitCode int; Pid int }
  type ExecStream interface {
      Read(p []byte) (int, error)         // TTY: raw bytes. Non-TTY: use ReadFrame.
      ReadFrame() (stream byte, p []byte, err error) // 1=stdout, 2=stderr; TTY always 1
      Write(p []byte) (int, error)        // stdin
      CloseWrite() error                  // stdin EOF (half-close the unix conn)
      Close() error
  }
  func (c *Client) CreateExec(ctx context.Context, containerID string, spec ExecSpec) (string, error)
  func (c *Client) StartExec(ctx context.Context, execID string, tty bool) (ExecStream, error)
  func (c *Client) ResizeExec(ctx context.Context, execID string, cols, rows uint16) error
  func (c *Client) InspectExec(ctx context.Context, execID string) (ExecInspect, error)
  var ErrNotRunning = errors.New("docker engine: container is not running")
  ```
  Callers use `ReadFrame` only (drop `Read` from the interface if it's not needed).

- [ ] **Step 1: Write failing tests** in `exec_test.go` against the existing unix-socket `fake` (`serveVer`):
  - `TestCreateExecSendsSpec`: asserts POST `/v1.41/containers/abc/exec` with JSON body `AttachStdin/Stdout/Stderr:true`, `Tty`, `Cmd`, `User`, `Env`; returns ID `"e1"`.
  - `TestCreateExecMapsErrors`: 404 → `errors.Is(err, ErrNotFound)`; 409 → `errors.Is(err, ErrNotRunning)`.
  - `TestStartExecTTYRoundTrip`: the fake hijacks on `POST /v1.41/exec/e1/start` (asserts `Upgrade: tcp`, `Connection: Upgrade`, body `{"Detach":false,"Tty":true}`), answers `101 UPGRADED`, and echoes stdin. Writing `"hi"` reads back `ReadFrame() == (1, "hi")`.
  - `TestStartExecNonTTYDemux`: the fake writes multiplexed frames (stdout `"out"`, stderr `"err"`). `ReadFrame` yields `(1,"out")`, then `(2,"err")`, then `io.EOF`.
  - `TestStartExecCloseWrite`: after `CloseWrite()`, the fake's read returns EOF while the stream can still read the fake's final output.
  - `TestResizeExecQuery`: POST `/v1.41/exec/e1/resize?h=40&w=120`.
  - `TestInspectExec`: decodes `{"Running":false,"ExitCode":3,"Pid":0}`.
- [ ] **Step 2: Run** `go test ./internal/engine -run 'Exec' -v`. Expected: FAIL (undefined: `CreateExec`…).
- [ ] **Step 3: Implement.** Add `dial func(ctx context.Context) (net.Conn, error)` to `Client`, set in `New` from the same dialer. `StartExec` dials, writes the request with `req.Write(conn)`, reads it with `http.ReadResponse(bufio.Reader)`, and requires a 101 (otherwise maps through `actionResult` and adds `ErrNotRunning` on 409). It keeps the `bufio.Reader` for reads (buffered bytes!) and the `*net.UnixConn` for writes and `CloseWrite`. The connection is bound by the caller's ctx: close it when ctx is done. Header-phase timeout = `c.timeout`. Non-TTY framing reuses the `demuxLogs` frame parser: refactor it into a frame reader both callers use, without changing logs behaviour (existing `stream_test.go` must still pass). Versioned paths use `apiVersion` like `ContainerAction`.
- [ ] **Step 4: Run** `go test ./internal/engine/... -v`. Expected: PASS, including the existing logs tests.
- [ ] **Step 5: Integration test** (`//go:build integration`): `TestExecIntegration` runs `busybox` (pinned tag, as other integration tests do) and checks that exec `echo hi` → stdout `"hi\n"`, `InspectExec.ExitCode == 0`, and exec `sh -c 'exit 3'` → `ExitCode == 3`. Run: `go test -tags integration ./internal/engine -run ExecIntegration -v` (needs Docker; report it as skipped if Docker is unavailable).
- [ ] **Step 6: Commit** `feat: add Docker exec primitives to the engine client`.

---

### Task 2: Exec configuration keys

**Files:**
- Modify: `internal/config/config.go`, `internal/config/template.go`, `internal/config/template.yaml`, `deploy/config.example.yaml`
- Test: `internal/config/config_test.go`, `internal/config/template_test.go`

**Interfaces:**
- Produces: `Config.ExecEnabled bool` (`yaml:"exec_enabled"`), `Config.ExecMaxSessions int` (`yaml:"exec_max_sessions"`), `Config.ExecIdleTimeout time.Duration` (`yaml:"exec_idle_timeout"`). Defaults are `true`, `8`, `30*time.Minute`.

- [ ] **Step 1: Failing tests:**
  - `TestExecDefaults`: `Defaults()` has the three values above.
  - `TestExecFromFileAndEnv`: the file sets `exec_enabled: false`, `exec_idle_timeout: 0s`, and env `STACKSTEAD_EXEC_MAX_SESSIONS=3` wins.
  - `TestExecMaxSessionsInvalid`: `exec_max_sessions: 0` → load error mentioning `exec_max_sessions`.
  - `TestExecIdleTimeoutNeedsUnit`: `exec_idle_timeout: 30` → error (existing `rejectBareDurations` path).
  - Template test: the rendered template contains the three keys and round-trips through `Load`.
- [ ] **Step 2: Run** `go test ./internal/config -run Exec -v`. Expected: FAIL.
- [ ] **Step 3: Implement**, following the existing field, env and validation patterns. Template comments: one line each, plus a note that terminal access is root-equivalent on the container.
- [ ] **Step 4: Run** `go test ./internal/config/... -v`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add exec config keys`.

---

### Task 3: Exec protocol and session loop (no HTTP)

**Files:**
- Create: `internal/api/execproto.go`, `internal/api/execsession.go`, `internal/api/execsession_internal_test.go`

**Interfaces:**
- Consumes: `engine.ExecStream`, `engine.ExecInspect` (Task 1).
- Produces:
  ```go
  // Frame stream IDs.
  const (streamStdin byte = 0; streamStdout byte = 1; streamStderr byte = 2)
  type controlMsg struct { Type string `json:"type"`; Cols, Rows int `json:",omitempty"`; Code *int `json:"code,omitempty"`; Seconds int `json:"seconds,omitempty"`; Reason, Message string `json:",omitempty"` }
  // Lowercase JSON names: cols, rows, code, seconds, reason, message.

  type wsConn interface {          // implemented by *websocket.Conn via frameCodec; faked in tests
      ReadFrame() (text bool, data []byte, err error)
      WriteBinary(p []byte) error
      WriteText(v controlMsg) error
      Close() error
  }
  type execBackend interface {
      Resize(ctx context.Context, cols, rows uint16) error
      Inspect(ctx context.Context) (engine.ExecInspect, error)
  }
  type sessionConfig struct {
      Ping, PongTimeout, IdleTimeout, DetachCheck time.Duration
      Now func() time.Time
      After func(d time.Duration) <-chan time.Time // fake clock seam
      StillAuthorized func(ctx context.Context) bool
      Closing <-chan struct{}
  }
  // runSession pumps until the end and returns the audit Result string.
  func runSession(ctx context.Context, ws wsConn, stream engine.ExecStream, be execBackend, cfg sessionConfig) string
  ```
  The `frameCodec` for x/net (`websocket.Codec` whose Unmarshal captures `payloadType`) also lives in `execproto.go`, along with `newWSConn(*websocket.Conn) wsConn`, which sets `MaxPayloadBytes = 1 << 20`.

- [ ] **Step 1: Failing tests** with a fake `wsConn` (channels) and a fake `ExecStream` (`io.Pipe`s). Each asserts the returned result string and the frames sent:
  - `TestSessionPumpsStdinStdout`: binary `[0,'l','s']` reaches stream stdin; stream `(1,"out")` → binary `[1,'o','u','t']`.
  - `TestSessionStderrFrame`: `(2,"e")` → `[2,'e']`.
  - `TestSessionResize`: text `{"type":"resize","cols":120,"rows":40}` → `be.Resize(120,40)`.
  - `TestSessionEOF`: text `{"type":"eof"}` → `stream.CloseWrite()` called once.
  - `TestSessionExitSendsCode`: the stream ends and `Inspect` returns `{Running:false, ExitCode:3}` → text `{"type":"exit","code":3}`, then close; result `"exit 3"`.
  - `TestSessionClientGoneDisconnected`: ws read error → stream closed; after `DetachCheck`, `Inspect` says not running → `"disconnected"`; if still running → `"detached"`.
  - `TestSessionPingPongTimeout`: the fake clock passes 60 s with no pong → result `"disconnected"`, connection closed; a pong within the window keeps it open.
  - `TestSessionIdleWarningThenClose`: `IdleTimeout=5m`; at 4 m with no data, `{"type":"idle_warning","seconds":60}`; at 5 m `{"type":"closed","reason":"idle"}` → `"idle"`.
  - `TestSessionIdleResetByOutput`: stdout data at 4m30s cancels the countdown (no close at 5 m).
  - `TestSessionIdleShortTimeoutWarnsAtHalf`: `IdleTimeout=90s` → warning at 45 s with `seconds:45`.
  - `TestSessionIdleDisabled`: `IdleTimeout=0` → no idle frames over 2 h of fake time.
  - `TestSessionRecheckExpired`: `StillAuthorized` returns false on the next tick → `closed/session_expired` → `"session_expired"`.
  - `TestSessionShutdown`: `Closing` closed → `closed/shutdown` → `"shutdown"`.
  - `TestSessionLargeTransfer` (Review Focus 4): 5 MiB echoed through stdin→stdout arrives intact, in order, within 5 s.
  - `TestSessionUnknownControlIgnored`: `{"type":"bogus"}` doesn't end the session.
- [ ] **Step 2: Run** `go test ./internal/api -run 'TestSession' -v`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement.** Approach: one goroutine reads ws → stdin/control, one reads stream → ws, and a main select over ticker, idle timer, closing and done channels. Activity resets on any binary frame in either direction only. All ws writes go through one mutex (x/net `Conn` writes are not safe to interleave). Exactly one terminal frame is sent (exit or closed), and only when the cause isn't the client disappearing.
- [ ] **Step 4: Run** `go test -race ./internal/api -run 'TestSession' -v`. Expected: PASS, race-clean.
- [ ] **Step 5: Commit** `feat: add exec session protocol loop`.

---

### Task 4: Exec HTTP route, checks and audit

**Files:**
- Create: `internal/api/exec.go`, `internal/api/exec_test.go`
- Modify: `internal/api/server.go` (`Options` + registration), `internal/api/containers.go` (`ContainerEngineAPI` gains exec methods; `container_not_found` code on 404s from `inspectContainer`), `internal/cli/serve.go` (wire options), `docs/api.md`, `docs/security.md`

**Interfaces:**
- Consumes: Task 1 engine methods, Task 2 config fields, Task 3 `runSession`, `newWSConn`.
- Produces:
  ```go
  // in Options
  Exec ExecOptions
  type ExecOptions struct { Enabled bool; MaxSessions int; IdleTimeout time.Duration }
  // ContainerEngineAPI additions
  CreateExec(ctx, containerID string, spec engine.ExecSpec) (string, error)
  StartExec(ctx, execID string, tty bool) (engine.ExecStream, error)
  ResizeExec(ctx, execID string, cols, rows uint16) error
  InspectExec(ctx, execID string) (engine.ExecInspect, error)
  ```
  Route: `GET /api/containers/{id}/exec`, registered in `registerContainerActionRoutes` (always registered there; answers 403 `exec_disabled` when `!Enabled`).

- [ ] **Step 1: Failing tests** (`httptest.Server` + `websocket.DialConfig` from x/net; extend `fakeContainerEngine` with exec fakes; reuse `actionsEnv`):
  - `TestExecDisabled`: 403 `{"code":"exec_disabled"}`.
  - `TestExecRequiresSession`: no cookie → 401.
  - `TestExecBadID`: `/api/containers/web/exec` (a name) → 404 `container_not_found`.
  - `TestExecNotRunning`: inspect state `exited` → 409 `not_running`.
  - `TestExecForeignOrigin`: `Origin: https://evil.example` → 403 `bad_origin`.
  - `TestExecSameHostDifferentScheme`: `Origin: https://<host>` against an http test server → upgrade succeeds.
  - `TestExecNoOrigin`: raw upgrade without Origin (hand-built handshake request) → 101.
  - `TestExecValidation`: 65 `cmd` params → 400; a NUL in `user` → 400.
  - `TestExecAutoShellArgv`: no `cmd` → `CreateExec` gets the Global Constraints auto-shell argv, `Tty:true`, `Env:["TERM=xterm-256color"]`; `cols=100&rows=30` → `ResizeExec(100,30)` after start.
  - `TestExecNoShell` (Review Focus 3): `StartExec` → error containing `no such file` / `executable file not found` → text frame `{"type":"error","message":"no shell found in this container; pass a command"}`, then close.
  - `TestExecLimit`: with `MaxSessions=2`, the third concurrent → 429 `exec_limit`.
  - `TestExecSlotReleasedOnClose` (Review Focus 5): open 2 (cap 2), close one client-side, then a new open succeeds within 1 s.
  - `TestExecAuditStack` / `TestExecAuditHost`: after an exit, the action log has a `container.exec` start and end (`Result` `"started"` → `"exit 0"`); `Scope` is the stack name for a stack container and `"host"` for an unmanaged one; `Username` is the session user.
  - `TestExecPeerCredentialAllowed`: a peer-credential request (no cookie, no Origin) reaches 101; the audit `Username` is the peer actor.
- [ ] **Step 2: Run** `go test ./internal/api -run 'TestExec' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.** All checks happen before calling `websocket.Server{Handshake: ..., Handler: ...}.ServeHTTP`. The `Handshake` is a no-op that only accepts what the pre-checks already passed (avoids x/net's default `checkOrigin`). Cap = `chan struct{}` semaphore with a non-blocking acquire; release in `defer` as soon as `runSession` returns. `StillAuthorized`: re-run `Auth.Authenticate(cookie)` for a cookie session, or `Peers.Authenticate(header)` for a peer one. Server-side `IdleTimeout` from `Options.Exec`, `Closing` from `Options.Closing`. The hijacked connection must not keep server deadlines: the first thing the `Handler` does is `ws.SetDeadline(time.Time{})` (`*websocket.Conn` implements it). Add `TestExecOutlivesReadTimeout`: on a server with `ReadTimeout: 200ms`, a session quiet for 1 s still echoes. In `serve.go`, pass `api.ExecOptions{Enabled: cfg.ExecEnabled, MaxSessions: cfg.ExecMaxSessions, IdleTimeout: cfg.ExecIdleTimeout}`.
- [ ] **Step 4: Run** `go test -race ./internal/api/... ./internal/cli/... -v`. Expected: PASS.
- [ ] **Step 5: Docs.** `docs/api.md`: the route, query parameters, wire protocol and error codes (copy the spec tables). `docs/security.md`: a "Container exec" section covering who can use it, the host-only Origin check, audit (no content recorded), `exec_enabled: false`, the idle timeout, and the best-effort limitation (non-TTY commands ignoring EOF keep running; Stackstead can't kill them because the service runs unprivileged and macOS processes live in the Docker VM).
- [ ] **Step 6: Commit** `feat: add container exec WebSocket route`.

---

### Task 5: Container number in the list

**Files:**
- Modify: `internal/apitypes/types.go` (`ContainerRow`), `internal/api/containers.go` (both `ContainerRow{` literals), `web/src/lib/api/types.ts`
- Test: `internal/api/containers_test.go`, `internal/apitypes/types_test.go` if it pins JSON

**Interfaces:**
- Produces: `ContainerRow.Number int` `json:"number,omitempty"` from label `com.docker.compose.container-number` (0 when absent or non-numeric); TS `number?: number`.

- [ ] **Step 1: Failing test** `TestContainerListNumber`: a container labelled `container-number=2` → `"number":2`; an unlabelled one → the key is absent.
- [ ] **Step 2: Run** `go test ./internal/api -run TestContainerListNumber -v`. Expected: FAIL.
- [ ] **Step 3: Implement** (parse with `strconv.Atoi`).
- [ ] **Step 4: Run** `go test ./internal/api/... ./internal/apitypes/... && cd web && npm run check`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: expose compose container number in the container list`.

---

### Task 6: Exec through connected servers

**Files:**
- Modify: `internal/api/forward.go`
- Test: `internal/api/forward_test.go` (or `peers_integration_test.go` if it already runs two real servers)

**Interfaces:**
- Consumes: Task 4 route on the "remote" test server.

- [ ] **Step 1: Failing tests:**
  - `TestForwardExecStripsOrigin`: a browser-style upgrade with a same-host Origin to `/api/servers/{peer}/api/containers/{id}/exec` reaches the remote with **no** Origin and with the peer credential; 101 is relayed and an echo round-trip works.
  - `TestForwardExecForeignOrigin`: a foreign Origin on the local forward → 403 `bad_origin` and the remote is never called.
  - `TestForwardExecSurvivesQuiet` (Review Focus 1): with `sseWriteTimeout` set to 200 ms in the test, a session with no traffic for 1 s still echoes afterwards.
  - `TestForwardExecOldPeer`: the remote answers a bare JSON 404 without `code` → relayed unchanged (the client side interprets it; see Tasks 8 and 10).
- [ ] **Step 2: Run** `go test ./internal/api -run 'TestForwardExec' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.** In `handleForward`: when the request is a WebSocket upgrade (`Connection` contains `upgrade`, `Upgrade: websocket`, case-insensitive), validate Origin with the same helper as Task 4 (extract it as `originAllowed(r *http.Request) bool`), delete `Origin` in `Rewrite`, and skip the `deadlineWriter` wrapper (pass `w` directly) so the hijacked connection carries no write deadline.
- [ ] **Step 4: Run** `go test -race ./internal/api/... -v`. Expected: PASS.
- [ ] **Step 5: Commit** `fix: let exec sessions pass through connected-server forwarding`.

---

### Task 7: Go client support

**Files:**
- Modify: `internal/client/client.go`
- Create: `internal/client/execws/execws.go`, `internal/client/execws/execws_test.go`
- Modify: `go.mod`/`go.sum` (`golang.org/x/net`, if Task 4 didn't already add it)

**Interfaces:**
- Produces:
  ```go
  // client
  func (c *Client) Containers(ctx context.Context) ([]apitypes.ContainerRow, error) // GET /api/containers
  func (c *Client) Config() Config
  // execws
  type Options struct { Cmd []string; User string; TTY bool; Cols, Rows int }
  type Session struct { /* … */ }
  func Dial(ctx context.Context, cfg client.Config, containerID string, o Options) (*Session, error)
  func (s *Session) Stdin(p []byte) error
  func (s *Session) CloseStdin() error                 // sends {"type":"eof"}
  func (s *Session) Resize(cols, rows int) error
  // Run copies stdout/stderr frames to the writers, answers pings, and reports how the session ended.
  func (s *Session) Run(stdout, stderr io.Writer, onIdleWarning func(seconds int)) (End, error)
  type End struct { ExitCode *int; Reason string } // Reason: "", "idle", "session_expired", "shutdown", "error: <msg>"
  var ErrOldServer = errors.New("this server needs a newer Stackstead to open a terminal")
  ```
  Pre-upgrade HTTP errors come back as `*client.Error` (so the CLI's `apiFailure` mapping applies). A 404 whose body has no `code` becomes `ErrOldServer`.

- [ ] **Step 1: Failing tests** (`execws_test.go` against a stub server built on x/net `websocket.Server`; `client` test for `Containers`):
  - `TestDialSendsCookieAndSameOrigin`: the server sees `Cookie: stackstead_session=…` and `Origin` == base scheme://host; `ws`/`wss` derived from http/https; the base path prefix is kept.
  - `TestDialQuery`: `cmd` repeated in order, `user`, `tty=0`, `cols`/`rows`.
  - `TestDialHTTPErrorMapped`: 409 `{"code":"not_running"}` → `*client.Error{Status:409}`; bare 404 → `ErrOldServer`.
  - `TestRunExitCode`: frames `[1,"a"]`, `[2,"b"]`, `{"type":"exit","code":7}` → stdout `"a"`, stderr `"b"`, `End.ExitCode==7`.
  - `TestRunAnswersPing`: `{"type":"ping"}` → the client sends `{"type":"pong"}`.
  - `TestRunIdleWarningCallback` and `TestRunClosedReason` (`idle`).
  - `TestDialNoRedirect`: a 302 → error, location not followed.
  - `TestContainersList` in the client package.
- [ ] **Step 2: Run** `go test ./internal/client/... -v`. Expected: FAIL.
- [ ] **Step 3: Implement** with `websocket.NewConfig` + `DialConfig`. TLS from `cfg.CAFile`/`cfg.Insecure`, mirroring `client.New`. Header `Cookie` set through `config.Header`. `deps_test.go` must still pass (the `client` package itself gains no imports).
- [ ] **Step 4: Run** `go test -race ./internal/client/... -v`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: add exec WebSocket client for the CLI`.

---

### Task 8: `stackstead exec` command

**Files:**
- Create: `internal/cli/cmd_exec.go`, `internal/cli/cmd_exec_test.go`
- Modify: `internal/cli/commands.go` (add `execCommand()` to "Remote", after `logsCommand()`), `internal/cli/command.go` (`Env` gains a terminal seam), `docs/cli.md`

**Interfaces:**
- Consumes: Task 7 (`client.Containers`, `execws.Dial/Run`, `ErrOldServer`).
- Produces:
  ```go
  // Env additions (OSEnv fills with x/term + os/signal; tests inject fakes)
  MakeRaw   func(fd uintptr) (restore func() error, err error)
  TermSize  func(fd uintptr) (cols, rows int, err error)
  Resizes   func(ctx context.Context) <-chan struct{} // SIGWINCH; nil-safe on non-unix
  // resolution, pure and unit-tested
  func resolveExecTarget(rows []apitypes.ContainerRow, stack, service, container string, index int) (apitypes.ContainerRow, error)
  ```
  Usage line: `exec (<stack> <service> | --container <name|id>) [--index N] [--user U] [-T] [-- cmd args...]`.

- [ ] **Step 1: Failing tests:**
  - `resolveExecTarget` table: single running replica → it; two replicas without `--index` → `ExitUsage` error listing both names; `--index 2` uses `Number==2`; `Number` absent (old server) → name-sorted, index 2 = the second; `--container` by name and by 12-char ID prefix; no match → `ExitNotFound`; match not running → `ExitConflict` "container is not running"; both stack/service and `--container` → `ExitUsage`.
  - `TestExecExitCodePassthrough`: a fake server exits with 7 → the command returns an exit error with code 7, and code 0 → nil.
  - `TestExecNonTTYStreams`: `-T`, stdin `"in"` → the server receives stdin `"in"` then eof; server stdout/stderr go to `env.Stdout`/`env.Stderr` separately.
  - `TestExecRestoresTerminal` (Review Focus 2): with a TTY fake, `restore` is called exactly once for each of: normal exit, server close mid-session, ctx cancel (Ctrl-C), and `Run` returning an error.
  - `TestExecIdleNotice`: an idle warning prints `\r\n[stackstead: idle session closes in 60s]\r\n` to stderr; closed `idle` → exits `ExitUnavailable` with message "session closed after being idle".
  - `TestExecOldServer`: `ErrOldServer` → `ExitUnavailable`, message matches.
  - `TestExecTTYAuto`: TTY only when both stdin and stdout are terminals and `-T` is absent.
- [ ] **Step 2: Run** `go test ./internal/cli -run 'Exec' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.** Use `withAPI` for auth and context handling. Raw mode is entered after `Dial` succeeds, and `restore` is deferred immediately (plus called on panic by the defer). Resizes go to `Session.Resize`. Stdin is copied with `Session.Stdin` until EOF, then `CloseStdin` (non-TTY only; in TTY mode EOF is just a byte). Exit with the remote exit code via the existing exit-error type.
- [ ] **Step 4: Run** `go test -race ./internal/cli/... -v`. Expected: PASS.
- [ ] **Step 5: Docs.** In `docs/cli.md`, add the command table row and an `exec` section with examples (`stackstead exec web app`, `--container`, `-T -- pg_dump … > dump.sql`), the exit code passthrough, the idle timeout and the non-TTY limitation.
- [ ] **Step 6: Commit** `feat: add stackstead exec command`.

---

### Task 9: Web exec protocol client

**Files:**
- Create: `web/src/lib/exec/exec.ts`, `web/src/lib/exec/exec.test.ts`

**Interfaces:**
- Produces:
  ```ts
  export type ExecEnd = { kind: 'exit'; code: number } | { kind: 'closed'; reason: 'idle' | 'session_expired' | 'shutdown' } | { kind: 'error'; message: string } | { kind: 'lost' };
  export interface ExecHandlers { onData(bytes: Uint8Array): void; onIdleWarning(seconds: number): void; onEnd(end: ExecEnd): void; }
  export interface ExecOptions { cmd?: string[]; user?: string; cols: number; rows: number }
  export function execUrl(base: string, containerId: string, o: ExecOptions, loc?: Location): string // ws(s)://host + base + /api/containers/{id}/exec?...
  export function openExec(url: string, h: ExecHandlers, socketCtor?: typeof WebSocket): { send(text: string | Uint8Array): void; resize(cols: number, rows: number): void; close(): void };
  ```
  `base` is `''` locally and `/api/servers/{id}` for a peer (the same base `createApi` uses).

- [ ] **Step 1: Failing tests** (fake WebSocket class):
  - `execUrl` with https → `wss:`; peer base kept; `cmd` repeated; `user` omitted when blank; `tty` absent (server default 1).
  - `send('ls')` → binary frame `[0, …utf8('ls')]`.
  - A binary `[1, …]` → `onData` gets the payload without the ID byte.
  - Text `ping` → replies `{"type":"pong"}`.
  - `exit` code 3 → `onEnd({kind:'exit',code:3})` and no `lost` afterwards.
  - `closed` idle → `onEnd` closed/idle; `idle_warning` → `onIdleWarning(60)`.
  - Socket close without a terminal frame → `{kind:'lost'}`; close before open with HTTP 403 isn't observable in browsers, so a pre-open close → `lost`.
  - `close()` sends nothing further and fires no `onEnd`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/lib/exec`. Expected: FAIL.
- [ ] **Step 3: Implement** (`binaryType = 'arraybuffer'`).
- [ ] **Step 4: Run** the same command. Expected: PASS. Also run `npm run check`.
- [ ] **Step 5: Commit** `feat: add browser exec protocol client`.

---

### Task 10: Terminal tab and entry points

**Files:**
- Create: `web/src/app/containers/TerminalTab.tsx`, `TerminalTab.module.css`, `terminalTab.test.tsx`
- Modify: `web/package.json` + lockfile (`@xterm/xterm`, `@xterm/addon-fit`, exact latest stable versions at least 2 weeks old), `ContainerDetailScreen.tsx` (`TABS` + render branch + Terminal button in the actions group), `ContainerRow.tsx` (terminal icon button on running rows), `web/src/app/detail/ServiceOverview.tsx` (per running replica link), `web/scripts/check-bundle.mjs` (`TOTAL_BUDGET = 500 * 1024`)

**Interfaces:**
- Consumes: Task 9 `execUrl`/`openExec`; `useServer()` (`local`, `id`, `href`); Task 5 not needed.
- Produces: `export function TerminalTab(props: { containerId: string; containerName: string; running: boolean; onStart(): void })`, default-exported lazily via `lazy(() => import('./TerminalTab'))` from `ContainerDetailScreen`.

- [ ] **Step 1: Failing tests** (`terminalTab.test.tsx`, xterm mocked with `vi.mock('@xterm/xterm')`, fake WebSocket):
  - Not running → text "Container isn't running" and a Start button calling `onStart`; no socket opened.
  - Running → a socket opened at `execUrl('', id, …)`; on a peer scope, the base is `/api/servers/<id>`.
  - Shell picker "sh" → `cmd=/bin/sh`; Custom command `"python -i"` → `cmd=python&cmd=-i` (split on whitespace); user field → `user=`.
  - Choices persisted per container name in localStorage, and a throwing localStorage doesn't break rendering.
  - `exit` → the terminal writes `[process exited with code 3]` and a Reconnect button appears; Reconnect opens a new socket.
  - `lost` → `[connection lost]` + Reconnect; no automatic reconnect (one socket only after 5 s of fake timers).
  - `idle_warning` → a banner "Closing idle session in 60 s, press any key to keep it open"; terminal input hides it.
  - An `error` frame with "no shell found…" is shown in a danger banner.
  - Unmount closes the socket.
  - `containerDetail.test.tsx`: the `?tab=terminal` tab exists; the actions group has a Terminal button linking to `?tab=terminal`; `containers.test.tsx`: running rows have a "Open terminal for <name>" link and stopped rows don't; a ServiceOverview replica link exists for running replicas.
  - Peer scope: if the socket ends `lost` **before it ever opened** (browsers hide the HTTP status, so a missing route looks the same as a refusal), show "Could not open a terminal. This server may need a newer Stackstead." On the local scope, the same case shows "Could not open a terminal." 
- [ ] **Step 2: Run** `cd web && npx vitest run src/app/containers`. Expected: FAIL.
- [ ] **Step 3: Implement.** xterm + FitAddon are imported only inside `TerminalTab.tsx` (lazy chunk). `ResizeObserver` → `fit()` → debounced (150 ms, existing `debounce`) `resize`. Styling uses existing CSS-module tokens; the terminal theme follows the light and dark tokens.
- [ ] **Step 4: Run** `cd web && npm run check && npx vitest run && npm run build`. Expected: PASS; the build prints the entry under 120 kB and the total under 500 kB; xterm isn't in the entry chunk (check the `check-bundle` output).
- [ ] **Step 5: Commit** `feat: add terminal tab for running containers`.

---

### Task 11: Full verification

- [ ] **Step 1:** `make check` (vet, gofmt, Go tests, UI check and tests). Expected: all pass.
- [ ] **Step 2:** `go test -race ./internal/...`. Expected: PASS.
- [ ] **Step 3:** If Docker is available: `go test -tags integration ./internal/engine -run ExecIntegration`. Otherwise record it as not run.
- [ ] **Step 4:** Manual smoke with the `run` skill: `make build`, start the server, open a running container → Terminal → `ls`, resize the window, type `exit 3` → "[process exited with code 3]"; `stackstead exec --container <name> -- sh -c 'exit 5'; echo $?` → 5. Record what was and wasn't checked.
- [ ] **Step 5:** Final whole-branch review (Fable per CLAUDE.md). It checks that tests came first, the full suite passes and the spec is covered.
