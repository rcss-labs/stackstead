# Security

Back to the [README](../README.md).

## The short version

Whoever can use Stackstead can control Docker on that machine, and Docker
access is equivalent to root. Treat the Stackstead login like a root password.

- Stackstead listens on `127.0.0.1` only by default.
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
proxy_set_header Host $host;
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
