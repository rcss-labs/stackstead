# Container exec (web terminal and CLI) — design

Date: 2026-10-23
Status: draft, awaiting approval (builder check done)

## Intent

Troubleshooting containers is a daily task. Today it means SSH-ing to the
host and running `docker exec`. Stackstead should open a shell, or run a
command, inside any running container directly: in the browser and from the
`stackstead` CLI. This must also work for containers on connected servers.

## Goals

- Interactive shell (TTY) in any **running** container on this server or a
  connected server, from the web UI.
- `stackstead exec` CLI command, interactive or non-interactive (pipeable),
  against any saved context.
- Works for every running container, including ones Stackstead does not
  manage (`managed: "none"`).
- Closing the browser tab or the CLI ends interactive (TTY) sessions; idle
  sessions are closed automatically.
- Auditable: each session start and end goes into the existing action log.

## Non-goals

- Recording or replaying terminal content (keystrokes and output are never
  stored).
- Multiple terminals docked or kept across navigation. The user picked a
  single terminal tab per container page (option A). A docked or pop-out
  terminal may come later.
- Exec into stopped containers, or `docker run`-style debug containers.
- File upload or download through the terminal.
- Per-user permissions. Stackstead has one admin account, and exec is
  available to whoever is logged in.

## Anti-goals (would make this a failure even if it "works")

- Interactive shells left running in containers after the client is gone.
  (Non-TTY commands that ignore stdin EOF are a documented limitation, see
  section 1.)
- Cross-site WebSocket hijacking: a third-party page that opens a shell
  using the admin's cookie.
- Breaking the existing peer-forward security model: a peer response that
  can set cookies, or a request that escapes the `/api/` prefix.
- A CLI that leaves the user's terminal in raw mode after an error or
  Ctrl-C.

## Decisions made with the user

1. **Transport: WebSocket via `golang.org/x/net/websocket`.** The user chose
   this over `coder/websocket` to keep dependencies within `golang.org/x`.
   The library's known gaps are covered at the application level (see the
   wire protocol): app-level ping/pong, an explicit exit message instead of
   close codes, and a custom handshake that checks Origin.
2. **Both web and CLI.**
3. **Any running container**, managed or not.
4. **Web placement: a "Terminal" tab on the container detail page**
   (`/containers/<name>?tab=terminal`, and
   `/s/<server>/containers/<name>?tab=terminal` for connected servers). This
   was option A in the mockups. Other entry points deep-link to it.
5. **Orphans: best effort, documented** (user picked option (a) after the
   builder check). Stackstead does not kill exec processes itself.
6. **Bundle budget:** raise `TOTAL_BUDGET` in `web/scripts/check-bundle.mjs`
   from 400 kB to 500 kB gz to make room for xterm.js. `ENTRY_BUDGET` stays
   at 120 kB.
7. **Idle timeout:** sessions with no activity are closed automatically
   (see "Idle timeout" in section 2).

## Design

### 1. Engine layer (`internal/engine`)

Stackstead talks to Docker through its own client over the unix socket, not
the Docker SDK. Add exec support in the same style:

- `CreateExec(ctx, containerID string, spec ExecSpec) (execID string, err error)`
  wraps `POST /containers/{id}/exec`. `ExecSpec` has `Cmd []string`,
  `User string`, `Tty bool`, `Env []string` (Env is set to `TERM=xterm-256color`
  when Tty is true), `AttachStdin/Stdout/Stderr = true`.
- `StartExec(ctx, execID string, tty bool) (ExecStream, error)` wraps
  `POST /exec/{id}/start` with `Connection: Upgrade, Upgrade: tcp` and
  hijacks the connection. `ExecStream` exposes:
  - a reader that yields `(stream, []byte)` chunks. With TTY it is a single
    raw stream. Without TTY it is demultiplexed using the same frame format
    as logs. Reuse or generalise `demuxLogs`.
  - `Write([]byte)` for stdin,
  - `CloseWrite()` (half-close) to signal stdin EOF,
  - `Close()`.
- `ResizeExec(ctx, execID string, cols, rows uint16) error` wraps
  `POST /exec/{id}/resize?h=&w=`.
- `InspectExec(ctx, execID string) (ExecInspect, error)` returns
  `Running`, `ExitCode`, `Pid`.
- Error mapping follows the existing `ActionError` style: a 404 container
  becomes not-found, a 409 becomes not-running.

**Ending on disconnect (best effort; user's decision).** Docker has no
"kill exec" API. When the client goes away, the server closes the hijacked
connection. That sends EOF to stdin and SIGHUP for TTY sessions, which ends
interactive shells. Stackstead does **not** try to kill anything further:
the systemd unit runs as `User=stackstead` with an empty
`CapabilityBoundingSet` and `NoNewPrivileges`, and on macOS the process lives
in the Docker Desktop VM, so host-PID signalling cannot work. After closing,
the server calls `InspectExec` once (after 2 s) and logs a warning if the
process is still running. The audit end entry records `Result: "detached"`
in that case. `docs/security.md` and `docs/cli.md` state the limitation:
a non-TTY (`-T`) command that ignores stdin EOF keeps running until it
exits on its own.

### 2. API layer (`internal/api`)

**Route:** `GET /api/containers/{id}/exec`, a WebSocket upgrade. It is
registered with the container action routes (needs `ContainerEngine`) and
only when `exec_enabled` is true. If exec is disabled the route still
exists and answers **403** `{"code":"exec_disabled"}`, so the UI can show a
clear message instead of a 404.

``{id}` is a container ID only (`containerIDPattern`, 12–64 hex), the same
as the existing container routes, and is validated with the same
inspect-prefix check. Clients resolve names to IDs through
`GET /api/containers` first. *(Corrected after the builder check.)*

**Query parameters** (read before the upgrade):

| param | meaning | default |
|---|---|---|
| `cmd` | repeated; argv of the command | empty: auto shell |
| `user` | `user[:group]` passed to Docker | image default |
| `tty` | `1`/`0` | `1` |
| `cols`, `rows` | initial size (tty only) | 80×24 |

**Auto shell:** when `cmd` is empty, run
`/bin/sh -c 'if [ -x /bin/bash ]; then exec /bin/bash; else exec /bin/sh; fi'`.
This takes one exec and no probing. If `/bin/sh` itself is missing
(distroless), Docker fails the start. The server reports that as an `error`
message: "no shell found in this container; pass a command". It never
returns a bare 500.

**Pre-upgrade checks, answered as plain JSON errors with the existing
helpers:**

- Session or peer credential missing or invalid: 401 (from `requireSession`).
- Origin present and its **host[:port]** not equal to the request `Host`:
  **403**. The scheme is deliberately not compared (my call). The documented
  nginx setup does not send `X-Forwarded-Proto`, so a scheme check would
  block every terminal behind TLS. A host check alone still blocks
  cross-site hijacking. A missing
  Origin is allowed: browsers always send Origin on WebSocket upgrades, so
  a request without it is not a browser and still needs a valid credential.
- Container unknown: 404. Not running: **409** `{"code":"not_running"}`.
- Concurrent session cap reached: **429** `{"code":"exec_limit"}`.
- `cmd` or `user` failing validation: 400. Limits: at most 64 args, 4 KiB in
  total, no NUL bytes.

**Wire protocol** (over one WebSocket):

- **Binary frames** carry data. The first byte is the stream ID, the rest is
  payload. `0` = stdin (client→server), `1` = stdout, `2` = stderr
  (server→client). With TTY everything comes back as `1`.
- **Text frames** are JSON control messages:
  - client→server: `{"type":"resize","cols":N,"rows":N}`,
    `{"type":"eof"}` (half-close stdin, needed for `-T` piping),
    `{"type":"pong"}`.
  - server→client: `{"type":"ping"}` every 20 s;
    `{"type":"exit","code":N}` sent once when the process ends, just before
    the server closes; `{"type":"error","message":"…"}` for failures after
    the upgrade.
  - server→client: `{"type":"idle_warning","seconds":N}` and
    `{"type":"closed","reason":"idle"|"session_expired"|"shutdown"}`, sent
    before a close the process didn't cause.
- If no pong arrives within 60 s, the server closes the connection.
- **Session re-check (my call):** on every ping tick (20 s) the server
  re-validates the session cookie (`Auth.Authenticate`). For a forwarded
  session it re-validates the peer credential instead. If the session is no
  longer valid (logout, expiry, password change), it sends
  `closed/session_expired` and closes. No change to `auth.Service` is
  needed.
- On server shutdown (`Closing`), every session gets `closed/shutdown`.
- After a hijack, clear any read and write deadline on the connection
  (`SetDeadline(time.Time{})`) so session length is unbounded. Liveness
  comes from ping/pong only.

**Idle timeout (user's request; details are my call):**

- Config `exec_idle_timeout`, default `30m`. `0s` disables it. Like the
  other durations, it must carry a unit.
- "Activity" means any **data frame in either direction**: stdin from the
  user, or output from the process. A `tail -f` someone is watching
  therefore stays open. Ping/pong and resize don't count.
- One minute before the deadline (or at half the timeout, if that is
  shorter), the server sends `idle_warning` with the seconds left. Any
  activity cancels the countdown.
- At the deadline: send `closed/idle`, close the connection, and record
  audit `Result: "idle"`.
- Web: show a dismissible banner above the terminal ("Closing idle session
  in 60 s, press any key to keep it open"), then `[closed: idle]` with
  Reconnect.
- CLI: print `\r\n[stackstead: idle session closes in 60s]\r\n` to stderr
  on warning, and on close exit with a distinct message and the existing
  "unavailable" exit code.
- The timer is server-side only. A client can't extend it except by real
  activity.

**Concurrency cap:** a server-wide counter. The limit comes from config
`exec_max_sessions` (default 8). Peer-forwarded sessions count against the
**remote** server's cap.

**Audit:** every session writes action-log entries via `actionlog.Store`:

- `Kind: "container.exec"`, `Target`: container name, `Scope`: stack name, or
  `"host"` for containers outside a stack (`actionlog.Store.Append` rejects
  an empty scope), `Username` (local user, or the peer actor string for forwarded
  sessions), `Detail`: the command line (`auto shell` for the default) and
  user.
- One entry at start with `Result: "started"`, and one at end with
  `Result: "exit <code>"`, `"disconnected"`, `"detached"` (still running
  after disconnect), `"idle"` or `"session_expired"`, plus duration in
  Detail.
- Rejected attempts (403/409/429) are not logged.

### 3. Connected servers (peer forward)

The web app calls `/api/servers/{peer}/api/containers/{id}/exec`.
`handleForward` uses `httputil.ReverseProxy`, which already relays
`101 Switching Protocols`. Three changes are needed:

1. **Origin:** the local server validates the browser's Origin, just as it
   does for the local route. It then **deletes `Origin`** in the proxy's
   `Rewrite`, because the remote would otherwise compare it with its own
   Host and reject it. The remote then sees a peer-credential request with
   no Origin, which is allowed.
2. **Deadlines:** `deadlineWriter` re-arms a write deadline per write. The
   proxy hijacks through `ResponseController`, so the hijacked connection
   must have its deadlines cleared after the upgrade. Otherwise a quiet
   shell is cut after `sseWriteTimeout`. The builder confirms the actual
   behaviour with a test and fixes it, for example by skipping
   `deadlineWriter` for upgrade requests.
3. **Older peers:** the exec route's 404 for a missing container carries
   `{"code":"container_not_found"}`. A bare 404 without that code means the
   peer has no exec route, and the UI and CLI show "this server needs a
   newer Stackstead to open a terminal" (my call).
4. **Peer deny list:** check that `/api/containers/{id}/exec` is **not** in
   `isPeerDenied`. Peers are trusted as admin for container actions already,
   so exec follows the same rule. The audit entry records the peer actor.

The CLI targets a connected server the same way it targets any server, by
its context. It never goes through another server's forward route.

### 4. Web UI (`web/src/app/containers`)

- New dependency: `@xterm/xterm` plus `@xterm/addon-fit`, pinned exact
  versions as with the other dependencies. Load them lazily inside the
  Terminal tab so the entry chunk doesn't grow. The total budget is raised
  to 500 kB gz (decision 6).
- The detail page already has the container's row and ID. The tab connects
  with the ID, never the name.
- **ContainerDetailScreen** gets a `terminal` tab in `TABS`. The tab:
  - shows "Container isn't running" plus the existing Start action when the
    container isn't running, and no terminal;
  - shows "Terminal is disabled on this server" on 403 `exec_disabled`;
  - otherwise shows a small option bar (shell: Auto / bash / sh / Custom
    command…; user field, blank by default; Connect / Reconnect) above an
    xterm.js view that fills the tab;
  - remembers the shell and user choice per container name in localStorage,
    wrapped in try/catch;
  - connects when the tab opens, and disconnects when the tab or page is
    left (unmount closes the socket);
  - on `exit` prints `[process exited with code N]` and shows Reconnect; on
    socket loss prints `[connection lost]` and shows Reconnect. It never
    reconnects automatically, since a new connection would start a new
    shell;
  - sends a resize on fit and on window or container resize, debounced;
  - answers pings.
- **Entry points** that deep-link to `?tab=terminal`: a Terminal button in
  the container detail actions group, and an icon button on running rows in
  the Containers list and in the stack ServicePane (for each running
  replica). Remote servers use the `/s/<server>/…` prefix, as existing links
  do.
- A small `lib/exec` module owns the socket protocol (encode and decode
  frames, ping/pong, exit handling). The React tab only wires it to xterm,
  so the protocol is unit-testable without a DOM terminal.

### 5. CLI (`internal/cli`, new `internal/client/execws`)

```
stackstead exec <stack> <service> [--index N] [--user U] [-T] [-- cmd args…]
stackstead exec --container <name|id>     [--user U] [-T] [-- cmd args…]
```

- Target resolution: call `GET /api/containers`, then filter by
  `stack`+`service` (rows carry `stack`, `service`, `name`, `id`) or by
  name/ID, then connect with the ID. `--index N` is 1-based, as in
  `docker compose exec`, and matches the compose container number. Add an
  additive `number int` field (`json:"number,omitempty"`) to
  `apitypes.ContainerRow`, filled from the `com.docker.compose.container-number`
  label (my call). If a server doesn't send it (older peer), fall back to
  replicas sorted by name. Without `--index`, a single running replica
  is used; several replicas are an error that lists them. No match, or a
  match that isn't running, gives an exit code following the existing
  mapping (not found or conflict).
- Interactive by default when stdin **and** stdout are terminals. `-T`
  forces non-TTY: streams go to stdout and stderr separately, and stdin EOF
  is sent as `{"type":"eof"}`. This makes
  `stackstead exec db postgres -T -- pg_dump app > dump.sql` and
  `… -T -- psql < seed.sql` work.
- TTY mode: put stdin in raw mode with `golang.org/x/term` (already a
  dependency). Restore it on every exit path, including signals and panics:
  one deferred restore plus signal handling. Forward SIGWINCH as resize.
  Send the initial size from `term.GetSize`.
- The process exits with the remote command's exit code from the `exit`
  message. On connection loss before `exit`, use the existing "unavailable"
  exit code.
- WebSocket client: `internal/client` is stdlib-only by policy
  (`deps_test.go`), so the dialer lives in a new package
  `internal/client/execws` built on `x/net/websocket.DialConfig`. It reuses
  the client's base URL, prefix, TLS settings (CA file, insecure) and the
  session cookie. `x/net/websocket` requires an Origin, so the CLI sends
  the server's own origin (scheme://host of the base URL), which passes the
  host check. `golang.org/x/net` is a **new** `go.mod` dependency (same
  publisher as the existing `x/crypto`, `x/term`, `x/sys`). The same-host redirect protection
  must hold: no redirects are followed on the upgrade.
- `docs/cli.md` gets a new `exec` section.

### 6. Configuration

Flat keys, matching the existing `config.Config` style:

```yaml
exec_enabled: true        # default true
exec_max_sessions: 8      # default 8, must be ≥ 1
exec_idle_timeout: 30m    # default 30m; 0s disables
```

Both are added to `deploy/config.example.yaml`, the config template, and
`docs/security.md` (a short "Container exec" section: who can use it, the
Origin check, audit, and how to turn it off).

## Testing expectations

Test-first per repository policy:

- **engine:** create, start, resize and inspect against a fake unix-socket
  Docker server (existing test pattern). The fake covers hijack, TTY and
  non-TTY demux, and half-close. Add a real-Docker case under the existing
  integration tag (exec `echo hi` returns `hi` and exit 0; exec `exit 3`
  returns 3).
- **api:** handshake accepted with a cookie and matching Origin; rejected
  with a foreign Origin (403); accepted with no Origin; 409 not running;
  429 at the cap; 403 when disabled; the exit message carries the code; a
  missing pong closes the session; idle timeout sends a warning then
  `closed/idle`, and data in either direction resets it (fake clock); a
  revoked session is closed on the next tick; action-log start and end
  entries are written with scope `host` for unmanaged containers.
- **forward:** an upgrade through `/api/servers/{id}/api/containers/{id}/exec`
  reaches the remote with no Origin and with the peer credential; a session
  idle longer than `sseWriteTimeout` stays alive.
- **cli:** target resolution (stack/service/index, container, ambiguity);
  exit code passthrough; `-T` stream separation and EOF; terminal restored
  on error (through a terminal seam, not a real TTY).
- **web (vitest):** `lib/exec` frame encoding, ping/pong and exit handling;
  the Terminal tab renders the not-running, disabled and connected states
  (xterm mocked); entry-point buttons link to `?tab=terminal`.

## Left to the builder

- Exact Go type and file names, and how `demuxLogs` is shared.
- Visual details of the option bar and icon buttons, following existing
  components and CSS modules.
- The concrete action-log `Detail` format.
