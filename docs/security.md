# Security

Back to the [README](../README.md).

## The short version

Whoever can use Stackstead can control Docker on that machine, and Docker
access is equivalent to root. Treat the Stackstead login like a root password.

- Stackstead listens on `127.0.0.1` only by default.
- Stackstead writes a service's Dockerfile only inside that stack's own
  folder, after resolving symlinks at write time, and never through a
  symlinked file. Dockerfiles outside the folder are view-only, and reading
  them is limited to the allowed directories (regular files up to 1 MiB). A
  Dockerfile runs commands at build time, just as a compose file does, so the
  trust model is unchanged: the same root-equivalent access Stackstead already
  has.
- There is one admin account. Passwords are hashed with argon2id, sessions use
  an HTTP-only, SameSite=Strict cookie, and login is rate-limited per IP.
- The web UI is served with a strict Content-Security-Policy, `nosniff` on
  every response, and never renders log, compose or error text as HTML. The
  policy allows scripts from the same origin only: the theme script
  (`/theme.js`, which sets the theme before first paint) is a same-origin
  classic script, so no inline script and no third-party origin is needed.
  The browser's `localStorage` holds only the theme and density preferences.
- `GET /api/host`, for signed-in users only, discloses the hostname, CPU core
  count and memory totals, free and total space of the stacks directory's
  filesystem and the name of one network interface. Nothing else from Docker's
  `/info` is exposed: no IP addresses, kernel version, mounts or environment.
- Any signed-in user can read the logs of every container on the host and
  start, stop or restart containers that are not in a registered stack. The
  server refuses those actions on registered stacks' containers, whatever the
  client sends. Container details never include environment variables or the
  command line.
- Macvlan/ipvlan networks put containers directly on the LAN, bypassing
  Docker's port publishing, so host firewall rules do not apply to them.
  `parent` goes to Docker as a driver option only; the interface list reads
  `/proc/net/route` only for routing information.
- The optional Prometheus endpoint has its own bearer token. It is shown once,
  and only its SHA-256 is stored.
- Update check: once a day (once an hour after a failure, and when you press
  Check now in Settings) the server makes one HTTPS request to
  `https://github.com/rcss-labs/stackstead/releases/latest/download/SHA256SUMS`
  to show "update available". It sends only `User-Agent: stackstead/<version>`
  and your IP address as any request does. Redirects are followed only to
  GitHub's own hosts. Set `update_check: false` (or `STACKSTEAD_UPDATE_CHECK=0`)
  on air-gapped hosts; Settings then shows "checks off". Nothing is ever
  downloaded or installed by the server or the browser.
- `stackstead upgrade` runs only from a terminal. It verifies the downloaded
  asset against the release's `SHA256SUMS` before using it, with the same
  limit as the install script: the sums come from the same release page, so
  this catches corruption and wrong files, not a compromised publisher
  (releases are not signed yet). As root it runs the installer by absolute
  path with a fixed `PATH`, never through a shell command line, from a fresh temporary
  directory it removes afterwards. `--base-url` replaces the source (HTTPS or
  loopback HTTP only) and prints a warning.

## Remote access

For access from other machines, put a TLS reverse proxy in front and make it
forward the original Host and client address, for example with nginx:

```nginx
proxy_http_version 1.1;
proxy_set_header Upgrade $http_upgrade;
proxy_set_header Connection $connection_upgrade;   # map $http_upgrade $connection_upgrade { default upgrade; '' close; }
proxy_set_header Host $http_host;
proxy_set_header X-Forwarded-For $remote_addr;
```

Each open browser tab keeps an `/api/events` stream plus the active tab's own
stream (logs, metrics or an operation). HTTP/1.1 allows only 6 connections per
origin, so behind an HTTP/1.1 proxy more than about two tabs can starve regular
requests. Use an HTTP/2 reverse proxy, or keep at most two tabs open.

## Connected servers

Two Stackstead servers can be connected, so each web UI can show and manage
both. On one server, Settings → Connected servers creates a connection token.
Pasting it into the other server's Add server form pairs them.

**A connection is root on both machines.** Anyone signed in to one server can
do on the other whatever that server's admin can. Connect only servers you
would give a root password to.

- Connection tokens start with `ssc_`. A token is shown once, works once and
  expires after 15 minutes. At most five are outstanding; a sixth invalidates
  the oldest. Only its SHA-256 is stored.
- Each direction has its own peer credential, starting with `ssp_`. The server
  that issued a credential keeps only its SHA-256. The server that holds it
  keeps the plaintext in SQLite, in the 0700 state directory. `stackstead db
  backup` includes it, so a backup is as sensitive as the database. No API
  returns a credential after it is created, and none is logged.
- Peers send the credential in an `X-Stackstead-Peer` header, never in
  `Authorization`. A proxy's own login can stay in place; see
  [install.md](install.md#connected-servers-behind-a-login-proxy).
- HTTPS is required. Plain `http://` is allowed only when every resolved
  address of both servers is private (loopback, RFC 1918, `fc00::/7`, or
  link-local) and you tick "I accept sending a root-equivalent credential
  unencrypted". Self-signed certificates work with a CA certificate or a
  pinned SHA-256 fingerprint. A pin is used only when the certificate is not
  publicly trusted. When you add a server, this server tells the other one how
  to verify it: the CA certificate you gave for this server's address, or else
  the fingerprint of the certificate this server presents at its own advertised
  address (found by probing it) unless that certificate is publicly trusted.
- Peer credentials cannot reach `/api/peers`, `/api/settings`, `/api/password`,
  `/api/logout`, `/api/setup`, `/api/servers` or `/api/fleet`. Those answer
  403 `peer_forbidden`. This stops a connected server from chaining further
  or changing this server's settings by accident. **It is not a security
  boundary.** A peer can read this server's database through the file browser
  or compose, and from there reach anything else this server holds. Do not
  rely on it to keep connected servers apart.
- Removing a connection takes effect at once on the server where you remove
  it. The other side is told immediately if it can be reached, otherwise at
  its next status check.
- A backup restored onto a different machine keeps the original instance ID,
  so the copy looks like the original server. Remove the connection on both
  sides and pair again.

## First-run setup

The first visit creates the admin account. From the machine itself no extra
step is needed. From anywhere else, including through a proxy, you also need
the one-time setup token that Stackstead prints in its log
(`journalctl -u stackstead` under systemd). The same setup works from a shell:

```bash
curl -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<12+ chars>","setupToken":"<token or empty on localhost>"}' \
  http://127.0.0.1:8420/api/setup
```

## Password change

`POST /api/password` keeps the session you are using and drops every other
session at once, so a stolen cookie dies with the change. A wrong current
password answers 403 (not 401, which the UI treats as "session gone") and
counts against the same per-IP limiter as logins (429 when exceeded). The UI
never echoes either password.

## Container exec

`GET /api/containers/{id}/exec` opens a shell or command inside any running
container, from the web terminal and `stackstead exec`. Whoever is signed in
can use it, and so can a connected server using its credential; there are no
per-user permissions. A shell in a container is as powerful as Docker access,
so treat it like the rest of the signed-in surface.

- **Origin check.** The Origin header's host[:port] must equal the request's
  `Host` (case-insensitive), otherwise the server answers 403 `bad_origin`.
  The scheme is not compared, because the documented nginx setup does not
  forward `X-Forwarded-Proto`. This blocks cross-site WebSocket hijacking with
  the admin's cookie. A missing Origin is allowed: browsers always send one,
  and any other client still needs a valid credential.
- **Reverse proxy.** The Origin check compares against the `Host` the server
  receives, so the proxy must forward it with its port when not on 80/443
  (nginx: `proxy_set_header Host $http_host;`, not `$host`). It must also pass the WebSocket upgrade (HTTP/1.1 with `Upgrade` and `Connection` headers, as in the snippet above); without that every terminal fails with 400 "websocket upgrade required". An Origin equal to
  Host cannot stop DNS rebinding; the `SameSite=Strict` session cookie is the
  defence there.
- **Audit.** Every session writes a `container.exec` start entry and an end
  entry to the action log: who (the user, or the peer actor for a forwarded
  session), the container, the stack or `host`, the command line (`auto shell`
  for the default) and the user. End results are `exit <code>`,
  `disconnected`, `detached`, `idle`, `session_expired`, `shutdown` or
  `error`. Keystrokes and output are never recorded, but the command line, including its arguments, is stored: do not pass secrets as arguments. Refused attempts
  (403, 409, 429) are not logged.
- **Turning it off.** Set `exec_enabled: false`; the route then answers 403
  `exec_disabled`. `exec_max_sessions` (default 8) caps concurrent sessions.
- **Idle timeout.** `exec_idle_timeout` (default 30m, `0s` disables) closes a
  session with no data in either direction, after a warning. Ping and resize
  do not count as activity. The session is also re-checked every 20 s and ends
  when the login is gone (logout, expiry, password change); for a session forwarded from another server, the remote re-checks the peer credential, not the browser's login.
- **Best effort on disconnect.** Closing the connection sends EOF to stdin and
  SIGHUP for a TTY, which ends interactive shells. Docker has no "kill exec",
  and Stackstead cannot do more: the service runs unprivileged, and on macOS
  the processes live inside the Docker VM. A non-TTY (`-T`) command that
  ignores stdin EOF keeps running until it exits on its own; the audit end
  entry records `detached` when the process is still running after the
  disconnect.

## Image library

Images are built from files uploaded through the signed-in UI or API, and any
signed-in user can create, build and delete them: a Dockerfile runs commands
at build time, so this is the same root-equivalent access Stackstead already
has. Upload contents are stored on disk under `DataDir/images` (directories
0700, files 0600), so they are only as private as the data directory; do not
upload secrets in a build context. Uploads are capped by `image_max_file_bytes`
and `image_max_context_bytes`, paths that escape the context (`..`, absolute,
backslash, NUL) are refused, and builds are bounded by
`image_operation_timeout`. Build logs are capped at 8 MiB and are text only.
Registry credentials (Docker Hub tokens, AWS keys and session tokens) are
stored at rest in the SQLite database under the data directory, which is 0700.
They are write-only through the API (a view carries a single `configured` flag,
plus the non-secret ECR access key id), are never forwarded to peers, and are
redacted from logs, action history, error messages and push output. `stackstead db backup` includes
the registry credentials, so a backup is as sensitive as the database. The
`/api/settings/registries` routes are on the peer denylist. The GitHub token
(for images built from a repository) is stored the same way: in SQLite under
the 0700 data directory, write-only (`GET /api/settings/github` answers only
`{configured, login}`), never forwarded to peers, redacted from logs, action
history and errors, and included in `stackstead db backup`. It is sent only to
the GitHub API; the tarball download follows one redirect to the archive host
and the token is dropped on a redirect to another host, so it is never sent
to the archive host. `/api/settings/github` is on the peer
denylist. Other image data is the uploaded build context, its
revisions, build args (not for secrets; they are saved in plain text and can
appear in build output) and build logs.

A connected server's peer credential can reach `/api/images/...`: `/api/images`
is not among the denied prefixes listed under
[Connected servers](#connected-servers), so a peer can create, upload to, build
and delete images here through forwarding, exactly as it can manage stacks.
That matches stacks and is not a security boundary; see the caveat there.
See [Image library](api.md#image-library).

### Sharing images with connected servers

The peer image surface is read-only: `GET /api/peer/images`, `GET
/api/peer/images/usage` and `GET /api/peer/images/{slug}/versions/{tag}/tar`.
They accept only the peer credential (a session cookie does not reach them),
answer 401 `peer_not_paired` otherwise, and expose no build logs, build
output, push targets or registry credentials; failed versions are not served.
Sharing reaches directly paired servers only.

What a peer credential can do with images: read the owner's library and
usage, download any succeeded version's archive (so a paired server can read
every image's contents, including files baked into it), and, through
forwarding to `/api/images/...`, create, build, push and delete images as an
operator would (see above). It cannot read registry or GitHub settings
(`/api/settings/...` stays denied) and cannot write anything through the peer
image routes.

A fetched archive cannot create other tags. The archive is inspected as it
streams into the local engine and is refused if its `manifest.json`,
`repositories` or `index.json` names any tag other than the requested
reference, if metadata files are hidden (not at the archive root, symlinks,
oversized) or the tar is malformed, so a compromised peer cannot plant
`nginx:latest` or another tag on this server. The loaded image's ID must also
match the one the owner recorded. A nested OCI index blob that names another
tag is not parsed; a real Docker (containerd store) does not tag from it, and
`TestIntegrationLoadNeverCreatesForeignTags` (integration build tag) guards
that assumption. A stalled transfer is cut after 60 seconds.

## Storage cleanup

Signed-in users can delete unused images, volumes and build cache. Deleting a
volume permanently destroys its data. Typed confirmation prevents mistakes;
it does not restrict a signed-in user, whose Docker access is root-equivalent.
Every cleanup requires an authenticated JSON plan and confirmation. The server
rechecks ownership, use and identity before removing each exact item without
force. Stack-declared and registered-project-labelled volumes stay protected
when stopped; images used by any container and volumes in use are refused.
Unknown stack declarations block otherwise unprotected volume cleanup, and a
volume without a creation timestamp is refused. The build-cache fallback is
explicitly **not exact**, because Docker can prune all currently unused cache.

The persistent action log records who deleted or skipped which item, the time,
result and estimated bytes (actual aggregate bytes for fallback cache pruning).
Compose interpolation output is never stored, returned or logged; image env,
command and entrypoint and volume driver options are omitted. Names, labels,
tags and Docker messages are rendered as text. See [Storage](api.md#storage).
