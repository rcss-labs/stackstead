# Installing Stackstead

Reference for the installers. The short version is in the [README](../README.md#install).
Everything here is built from [`install.sh`](../install.sh), [`deploy/`](../deploy) and the
[release runbook](../deploy/release/RELEASING.md).

## Choose a format

| You have | Use | Result |
|---|---|---|
| Linux with systemd (Debian, Ubuntu, Fedora, Rocky, ...) | the install script, or the `.deb` / `.rpm` | a `stackstead` systemd service, enabled and started |
| Linux without systemd (Alpine, ...) | the install script | binary, user, directories and config; start it under your own supervisor |
| macOS (Intel or Apple silicon) | the `.pkg`, or the install script | a per-user LaunchAgent that runs while you are logged in |

amd64 and arm64 only. Nothing installs Docker: install Docker Engine (or Docker Desktop, OrbStack,
Colima on macOS) and the Compose v2 plugin yourself. Every installer checks for them and warns.

## The install script

```bash
curl -fsSL https://github.com/rcss-labs/stackstead/releases/latest/download/install.sh | sudo sh
# a specific release
curl -fsSL https://github.com/rcss-labs/stackstead/releases/latest/download/install.sh | sudo sh -s -- --version v1.2.3
```

This is the `install.sh` attached to the latest release (it is listed in that release's `SHA256SUMS`), not
the development copy on `main`.

Prefer to read it first: download `install.sh`, read it, run `sudo sh install.sh`. `sh install.sh --help`
lists every flag, environment variable and exit code.

It resolves the release, downloads `SHA256SUMS` and the tarball for your OS and architecture, and
**always** verifies the tarball against `SHA256SUMS` before installing anything (a mismatch, or an
asset missing from the file, stops with exit code 6; there is no flag to skip it). Then it creates
the account and directories, writes the config if there is none, installs the binary and the service,
waits for `/api/health` and prints the URL to finish setup in.

Useful flags: `--version vX.Y.Z`, `--dry-run` (download, verify, print the plan, change nothing),
`--no-start`, `--force` (reinstall the same or an older version), `--prefix DIR` (default `/usr/local`),
`--uninstall [--purge]`. Running it again is a no-op for the same version and an in-place upgrade
for a newer one; it never overwrites `config.yaml` and never touches data, stacks or Docker. It
refuses (exit 8) to run over a `.deb`/`.rpm` installation: use `apt` or `dnf` for those.

### Verifying a download by hand

Releases are **not signed yet**: `SHA256SUMS` catches truncated, corrupted and wrong-architecture
downloads, but it does not authenticate the publisher (anyone who can edit the release page can edit
the sums too). Pin a version and compare `SHA256SUMS` with the copy in the release notes if that matters to you.

```bash
sha256sum -c SHA256SUMS --ignore-missing        # Linux
shasum -a 256 -c SHA256SUMS --ignore-missing    # macOS
```

The script already has the signature path built in for later: with `--signers FILE` it also verifies
`SHA256SUMS.sig` using `ssh-keygen -Y verify`. It is off by default and no key ships yet.

## Linux packages

```bash
sudo apt install ./stackstead_1.2.3_amd64.deb
sudo dnf install ./stackstead-1.2.3-1.x86_64.rpm
```

Download them from the release page and check them against `SHA256SUMS` first. Both enable and
start the service on a fresh install, restart it on an upgrade, and recommend (never require) Docker.

**Moving from the install script to a package.** The script's unit in `/etc/systemd/system` would
shadow the packaged one and keep the old `/usr/local/bin` binary running, so the package refuses to
install over a script install and changes nothing. Remove the script install first, without
`--purge`, which keeps `/etc/stackstead`, `/var/lib/stackstead`, the `stackstead` user and your stacks:

```bash
sudo /usr/local/share/stackstead/install.sh --uninstall
sudo apt install ./stackstead_1.2.3_amd64.deb
```

Your own copy of the packaged unit or a drop-in under `/etc/systemd/system` does not trigger the
refusal. The script refuses to run over a package install in the same way (exit 8).

## Where things go

| | Packages | Install script |
|---|---|---|
| binary | `/usr/bin/stackstead` | `/usr/local/bin/stackstead` |
| service unit | `/usr/lib/systemd/system/stackstead.service` | `/etc/systemd/system/stackstead.service` |
| config | `/etc/stackstead/config.yaml` (0640 root:stackstead, kept on upgrade) | same, written only if absent |
| data (database) | `/var/lib/stackstead` (0700 stackstead) | same |
| default stacks folder | `/opt/stacks` (created 2775 stackstead:docker; an existing one is never changed or removed) | same |
| uninstall helper | your package manager | `/usr/local/share/stackstead/install.sh --uninstall` |

The shipped unit expects the binary at `/usr/bin/stackstead`; the script rewrites `ExecStart` for its
prefix. Set `docker_socket` in the config for rootless Docker or a non-default socket, and restart
the service after editing it.

## First run

The first visit creates the admin account. On the host itself that needs nothing more: open
`http://127.0.0.1:8420`. From another machine you also need the one-time setup token printed in the service log:

```bash
journalctl -u stackstead | grep setup_token
```

Installers never create the admin account or ask for a password. If you lose the password later:
`sudo -u stackstead /usr/bin/stackstead admin set-password --config /etc/stackstead/config.yaml` (script installs:
`/usr/local/bin/stackstead`, which RHEL and Rocky leave out of sudo's `secure_path`, hence the full path). On macOS:
`stackstead admin set-password --config ~/Library/Application\ Support/Stackstead/config.yaml`. See [cli.md](cli.md).

Private registries: the service reads Docker credentials from its own home, so log in as that user:

```bash
sudo -u stackstead -H docker login registry.example.com
```

## Connected servers behind a login proxy

If the reverse proxy in front of Stackstead asks for its own login (basic
auth, forward-auth or SSO), it would also stop calls from connected servers.
Let requests that carry `X-Stackstead-Peer` through to Stackstead. Be aware
that this lets any request carrying that header skip the proxy's own login, so
Stackstead's login page and setup page become reachable without it. Stackstead
still requires its own login or a valid peer credential for everything else.

nginx, with basic auth. The `map` goes in the `http` block:

```nginx
map $http_x_stackstead_peer $stackstead_auth {
    ""      "Stackstead";
    default off;
}

# Needed for the web terminal (WebSocket upgrade).
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    # your existing TLS settings
    location / {
        auth_basic           $stackstead_auth;
        auth_basic_user_file /etc/nginx/stackstead.htpasswd;
        proxy_pass           http://127.0.0.1:8420;
        proxy_http_version   1.1;
        proxy_set_header     Upgrade $http_upgrade;
        proxy_set_header     Connection $connection_upgrade;
        proxy_set_header     Host $http_host;
        proxy_set_header     X-Forwarded-For $remote_addr;
        proxy_buffering      off;   # keeps event streams live
    }
}
```

Set `trusted_proxies` (for example `[127.0.0.1]`) in the Stackstead config so
the login rate limit tells the clients behind this proxy apart; see
[security.md](security.md#remote-access).

`auth_basic` accepts a variable, so `off` skips the login for peer calls only.
`auth_request` has no such switch. With it, have the auth service return
success when `X-Stackstead-Peer` is present.

Caddy and Traefik use forward-auth. Send requests that carry
`X-Stackstead-Peer` past the forward-auth step. Caddy (the first matching
`handle` wins, so the peer block comes first):

```caddyfile
stacks.example.com {
    @peer header X-Stackstead-Peer *
    handle @peer {
        reverse_proxy 127.0.0.1:8420
    }
    handle {
        forward_auth authelia:9091 {
            # your existing forward_auth settings
        }
        reverse_proxy 127.0.0.1:8420
    }
}
```

Traefik v3 (file provider, v3 matcher syntax): a peer router with a higher
priority and no forward-auth middleware:

```yaml
http:
  routers:
    stackstead-peer:
      rule: "Host(`stacks.example.com`) && HeaderRegexp(`X-Stackstead-Peer`, `.+`)"
      priority: 100
      service: stackstead
      tls: {}
    stackstead:
      rule: "Host(`stacks.example.com`)"
      middlewares: [sso]   # your forward-auth middleware
      service: stackstead
      tls: {}
```

If a peer's address answers with a login page or another non-Stackstead
response, the other server shows "{name}'s proxy answered instead of
Stackstead". The status checker keeps retrying and shows the server as
unreachable with that message; it does not remove the connection, so it
recovers once the proxy lets Stackstead's peer requests through.

## Security notes

- The `stackstead` user is in the `docker` group. Docker access is root-equivalent on the host, so anyone
  who controls the web UI controls the machine. The unit's hardening (`NoNewPrivileges`, `ProtectSystem=full`,
  `PrivateTmp`, kernel and cgroup protections, an empty capability set) limits accidents in Stackstead's own
  process; it is defense in depth, not a security boundary.
- It listens on `127.0.0.1` by default. For remote access use an authenticating TLS reverse proxy
  ([security.md](security.md)).
- On macOS the service runs as your own user: the UI is as powerful as your shell.
- `UMask=0007`: files the service creates are readable and writable by the `stackstead` user and group only.
  Add admins who should edit stacks by hand to the `stackstead` group (or `docker`, which owns `/opt/stacks`).

## Compose files outside the service's reach

The unit sets `ProtectSystem=full` (`/usr`, `/boot` and `/etc` are read-only for the service) and
`PrivateTmp=true` (the service sees its own empty `/tmp`). Stacks under your home or `/opt` need nothing.
Stacks under `/etc` or `/usr`, or in `/tmp`, need a drop-in:

```bash
sudo systemctl edit stackstead
```

```ini
[Service]
ReadWritePaths=/etc/myapp/stacks
# only if stacks live in /tmp:
PrivateTmp=no
```

Then `sudo systemctl restart stackstead`. Edit unit settings only through drop-ins; upgrades overwrite
the unit file itself.

## Upgrade

Releases after 0.2.0 include `stackstead upgrade`, which does it for every install type. Settings shows when a newer release
exists and the exact command for this machine.

| Installed with | Run |
|---|---|
| script, Linux | `sudo /usr/local/bin/stackstead upgrade` |
| script, macOS | `/usr/local/bin/stackstead upgrade` (as your user, no `sudo`; `install.sh` asks for it when needed) |
| `.deb` | `sudo stackstead upgrade` (runs `apt-get install`) |
| `.rpm` | `sudo stackstead upgrade` (runs `dnf install`, or `rpm -U` without dnf) |
| `.pkg` | `sudo /usr/local/bin/stackstead upgrade` (runs `installer`) |

`stackstead upgrade --check` only reports the latest version. `--version vX.Y.Z` picks a release. Before
anything is installed the asset is verified against the release's `SHA256SUMS`, which has the same limits as
the install script (see [Verifying a download by hand](#verifying-a-download-by-hand)). See
[cli.md](cli.md#upgrading) for flags and exit codes.

- **Back up first.** `sudo -u stackstead stackstead db backup /var/backups/stackstead-$(date +%Y%m%d).db`
  (macOS: `stackstead db backup ~/stackstead.db`). The service migrates its database forward on first start,
  and installing an older binary afterwards does not undo that.
- **Restart.** The installer restarts the service, once, and only if it was running; a service you disabled
  stays disabled. Without systemd (Alpine) nothing is started: restart it under your own supervisor.
  The web UI reconnects on its own.
- **Config.** `config.yaml` is kept. An edited config is never replaced. When a new package changes the packaged config, the `.deb`
  keeps yours and writes the new one next to it as `config.yaml.dpkg-dist`; the `.rpm` does the same with `.rpmnew`.
- **Downgrades.** The script needs `--force` (`sudo stackstead upgrade --version vX.Y.Z --force`). `.deb` and
  `.rpm` downgrades are manual: `upgrade` prints the commands (`apt-get install --allow-downgrades ./file.deb`,
  `rpm -U --oldpackage file.rpm`). A downgrade across a database change leaves a service that refuses to
  start until you restore the backup (`db restore`).
- **Without `upgrade`.** 0.2.0 and earlier do not have the command: run the install script (or install
  the newer `.deb` / `.rpm` / `.pkg`) once, the old way. Running the script again, or installing a newer
  package, still works and does the same thing.
- **Turning the check off.** Set `update_check: false` (see [security.md](security.md)).

## Uninstall and purge

On Linux nothing here ever removes your compose files (`/opt/stacks` or elsewhere), images, containers,
networks or volumes. On macOS, `--purge` deletes `~/Library/Application Support/Stackstead`, which includes
its default stacks folder (it asks for `--yes`); stacks you registered elsewhere are never touched.

| Installed with | Remove (keeps config, data, user) | Purge |
|---|---|---|
| script | `sudo /usr/local/share/stackstead/install.sh --uninstall` | add `--purge`: also removes `/etc/stackstead`, `/var/lib/stackstead` and the `stackstead` user |
| `.deb` | `sudo apt remove stackstead` | `sudo apt purge stackstead` |
| `.rpm` | `sudo dnf remove stackstead` | none: rpm has no purge, do it by hand (below) |

Manual purge after removing an rpm:

```bash
sudo rm -rf /var/lib/stackstead /etc/stackstead
sudo userdel stackstead
```

## macOS

Two ways, same result:

```bash
curl -fsSL https://github.com/rcss-labs/stackstead/releases/latest/download/install.sh | sh
```

or the universal `stackstead_<version>_darwin.pkg` from the release page (one download for Intel and Apple
silicon). Run the script from your own account (`| sh`, or `| sudo sh`): root is used only for `/usr/local`, and
everything in your home is written as you. A plain root login without `SUDO_USER` is refused (exit 8).

**Unsigned package.** The `.pkg` is not signed with an Apple Developer ID yet, so macOS Gatekeeper will say
it "cannot be opened because it is from an unidentified developer" (`spctl --assess --type install` reports
"rejected"; that is expected). To open it, either:

- right-click (or Control-click) the package and choose **Open**, then confirm; or, if macOS offers no Open
  choice, double-click it once, then open **System Settings, Privacy & Security** and choose **Open Anyway**
  next to the message about the package; or
- install it from a terminal, which bypasses the prompt: `sudo installer -pkg stackstead_<version>_darwin.pkg -target /`

The binary itself runs normally: it is ad-hoc signed and neither `installer` nor `curl` marks it as quarantined.

| | Path |
|---|---|
| binary | `/usr/local/bin/stackstead` |
| helper files (for uninstall) | `/usr/local/share/stackstead/`: `install.sh`, `launchagent.plist.in`, `LICENSE`; the `.pkg` also ships `config.example.yaml` there (the documented reference config) |
| config | `~/Library/Application Support/Stackstead/config.yaml` |
| data | `~/Library/Application Support/Stackstead/data/` |
| default stacks folder | `~/Library/Application Support/Stackstead/stacks/` |
| log (includes the setup token) | `~/Library/Logs/Stackstead/stackstead.log` |
| LaunchAgent | `~/Library/LaunchAgents/io.github.rcss-labs.stackstead.plist` |

Stackstead is a **LaunchAgent**, not a daemon: it runs while you are logged in, because Docker Desktop,
OrbStack and Colima expose their socket to your session. It starts at login and is restarted if it exits. The
installer detects the socket with `docker context inspect` and writes it to `docker_socket` in the config.

```bash
grep setup_token ~/Library/Logs/Stackstead/stackstead.log      # only needed from another machine
launchctl kickstart -k gui/$(id -u)/io.github.rcss-labs.stackstead   # restart
launchctl bootout gui/$(id -u)/io.github.rcss-labs.stackstead        # stop until next login
```

Uninstall (also what to run after installing the `.pkg`):

```bash
sudo /usr/local/share/stackstead/install.sh --uninstall           # keeps config, data and stacks
sudo /usr/local/share/stackstead/install.sh --uninstall --purge --yes   # also deletes them, stacks included
```

## Manual install (systemd)

If you build from source and want no installer:

```bash
sudo groupadd -f docker
sudo useradd --system --user-group --home-dir /var/lib/stackstead --no-create-home --shell /usr/sbin/nologin --groups docker stackstead
sudo install -m 0755 bin/stackstead /usr/bin/stackstead
sudo install -d -m 0755 /etc/stackstead
sudo install -m 0640 -g stackstead deploy/config.example.yaml /etc/stackstead/config.yaml
sudo install -d -o stackstead -g docker -m 2775 /opt/stacks
sudo install -m 0644 deploy/systemd/stackstead.service /etc/systemd/system/stackstead.service
sudo systemctl daemon-reload && sudo systemctl enable --now stackstead
```

`StateDirectory=` creates `/var/lib/stackstead` for you. The unit expects `/usr/bin/stackstead`.

## Exit codes

`install.sh` has its own exit codes; they are unrelated to the [`stackstead` command's](cli.md#exit-codes).

| Code | Meaning |
|---|---|
| 0 | success, or nothing to do |
| 1 | unexpected failure |
| 2 | usage error |
| 3 | unsupported OS or architecture |
| 4 | a required tool is missing |
| 5 | download failure |
| 6 | verification failure (checksum or signature) |
| 7 | an install step failed (user, directories, unit, service) |
| 8 | refused: not root, a package-managed install is present, or on macOS root without `SUDO_USER` or `--user` |
| 130 | interrupted (INT, TERM or HUP) |
