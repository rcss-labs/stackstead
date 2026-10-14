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
curl -fsSL https://raw.githubusercontent.com/rcss-labs/stackstead/main/install.sh | sudo sh
# a specific release
curl -fsSL https://raw.githubusercontent.com/rcss-labs/stackstead/main/install.sh | sudo sh -s -- --version v1.2.3
```

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

## Where things go

| | Packages | Install script |
|---|---|---|
| binary | `/usr/bin/stackstead` | `/usr/local/bin/stackstead` |
| service unit | `/usr/lib/systemd/system/stackstead.service` | `/etc/systemd/system/stackstead.service` |
| config | `/etc/stackstead/config.yaml` (0640 root:stackstead, kept on upgrade) | same, written only if absent |
| data (database) | `/var/lib/stackstead` (0700 stackstead) | same |
| default stacks folder | `/opt/stacks` (2775 stackstead:docker, never removed) | same |
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
`sudo -u stackstead stackstead admin set-password` (see [cli.md](cli.md)).

Private registries: the service reads Docker credentials from its own home, so log in as that user:

```bash
sudo -u stackstead -H docker login registry.example.com
```

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

- Script: run it again (or with `--version`). A newer version replaces the binary and unit and restarts
  the service; an older one needs `--force`.
- Packages: install the newer `.deb` / `.rpm`. The config and database are untouched and the service is restarted once.
  A service you disabled stays disabled.

## Uninstall and purge

Nothing here ever removes your compose files (`/opt/stacks` or elsewhere), images, containers, networks
or volumes.

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
curl -fsSL https://raw.githubusercontent.com/rcss-labs/stackstead/main/install.sh | sudo sh
```

or the universal `stackstead_<version>_darwin.pkg` from the release page (one download for Intel and Apple
silicon). Run the script from your own account with `sudo`: it uses `sudo` only for `/usr/local` and puts
everything else in your home, owned by you. A plain root login without `SUDO_USER` is refused (exit 8).

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
| helper files (for uninstall) | `/usr/local/share/stackstead/` |
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
