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
- The optional Prometheus endpoint has its own bearer token. It is shown once,
  and only its SHA-256 is stored.

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
