# Command line

Back to the [README](../README.md).

The `stackstead` binary is both the server and a complete command-line tool.
`stackstead help` groups the commands:

- **Server:** `serve`, `version`.
- **Remote:** talk to a running server over its HTTP API from any machine:
  `login`, `logout`, `context`, `whoami`, `status`, `stacks`, `ops`, `logs`,
  `metrics`, `events`, `networks`, `storage`, `images`, `settings`.
- **Host admin:** run on the server's host, next to the files:
  `config`, `db`, `admin`, `health`.

Global flags, before or after the command: `--context NAME`,
`-o table|json` (also `--output`; write `-o json` or `-o=json`, not `-ojson`),
`--no-color`, and `--timeout D` (per request, default 30s). Colour is used only
on a terminal, and never when `NO_COLOR` is set or `TERM=dumb`.

## Remote commands

```bash
stackstead login https://stacks.example.com      # prompts for the password
stackstead status                                 # Docker (and engine version), compose, event feed, metrics
stackstead stacks ls
stackstead stacks up web                          # streams the operation, exits 0 or 9
stackstead stacks down web --detach               # start it and return at once
stackstead stacks rm web --volumes --yes          # files stay on disk
stackstead logs web -f --service app --tail 100
stackstead exec web app                           # a shell in the running `app` container of stack `web`
stackstead metrics web --from 2h --step 5m
stackstead ops ls --running
```

| Command | What it does |
| --- | --- |
| `login <url> [--context N] [--username U] [--password-stdin] [--ca-file F] [--insecure]` | log in and save the session as a context |
| `logout`, `whoami`, `status` | sign out; show the account and server; show Docker (with its engine version), compose and feed status |
| `context ls\|current\|use <name>\|rm <name>` | manage saved servers |
| `stacks ls`, `stacks show <name>` | list stacks; services and containers of one |
| `stacks up\|down\|restart\|pull\|build <name> [--detach]` | lifecycle actions; they wait for the operation unless `--detach` |
| `stacks rm <name> [--volumes] [--yes] [--detach]` | compose down; asks on a terminal, `--yes` is required without one |
| `stacks import <path>` | import an existing compose file (e.g. `/opt/stacks/web/compose.yaml`) inside the server's allowed roots |
| `stacks compose get\|edit\|put <name>` | read, edit (in `$VISUAL`/`$EDITOR`) or replace (`-f FILE --hash H`) the compose file |
| `images ls` | the image library: NAME, SOURCE, OWNER, LATEST, LAST BUILD and PUSH TARGETS, including images owned by paired servers |
| `images build <image> [--tag T] [--build-arg K=V]... [--ref REF] [--detach]` | build a new version; the tag defaults to the suggested next tag |
| `images push <image> <tag> [--target REGISTRY/REPOSITORY] [--detach]` | push a built version to a registry push target |
| `ops ls\|show\|stream\|cancel` | operations and their output |
| `logs <stack> [-f] [--tail N\|all] [--since X] [--service S] [--timestamps] [--raw]` | container logs |
| `exec (<stack> <service> \| --container <name\|id>) [--index N] [--user U] [-T] [-- cmd args...]` | run a command or a shell inside a running container |
| `metrics <stack> [--from T] [--to T] [--step D] [--service S] [--live]` | resource history or a live feed |
| `events` | follow container changes until interrupted |
| `networks ls` | Docker networks |
| `settings metrics-token status\|rotate\|clear`, `settings password` | the `/metrics` token (printed once when created); change the account password |

Details worth knowing:

- **`logs`** strips terminal escape sequences and other control characters from
  container output, so a container cannot drive your terminal. `--raw` prints
  the text unchanged. With `-f`, a dropped connection resumes exactly where it
  stopped, with no gaps or repeats.
- **`exec`** runs a command, or a shell when none is given, inside a running
  container over a WebSocket. A terminal is allocated when both stdin and
  stdout are terminals; `-T` turns that off. On a terminal your keystrokes,
  Ctrl-C included, go to the remote program. The remote command's exit code
  becomes the exit code of `stackstead`, even where it collides with the codes
  in the table below (as with `docker compose exec`). A service with several
  replicas needs `--index N` (the compose replica number, from 1). A command
  with flags of its own needs `--` before it (`stackstead exec web app -- ls -la`),
  otherwise `stackstead` tries to parse them.
  A session with no input or output is closed after the server's idle timeout,
  with a notice shortly before and exit code 7. Without a terminal, stdin is
  sent as is and its end is forwarded, but there is no pseudo-terminal, so
  interactive programs behave as they do under `docker exec` without `-t`.
  ```bash
  stackstead exec web app
  stackstead exec --container web-app-1 --user root -- id
  stackstead exec -T web db -- pg_dump -U app app > dump.sql
  ```
- **`metrics`** shows history. CPU and memory are averages across a service's
  replicas, and the network and block I/O rates are totals across them. With
  `--live` there is one row per container (a `CONTAINER` column, 28 wide; longer names end in `…`), and the
  network and block I/O counters are cumulative totals, not rates.
- **`logs --timestamps`** prefixes each line with its time in local time and a
  numeric UTC offset (`2026-10-14T17:29:30.000000000+05:30`), a fixed width.
- **`stacks compose edit`** splits `$VISUAL` (else `$EDITOR`) on whitespace and
  runs it without a shell: `code --wait` works, quoting and pipes do not.
- **`-o json`** prints the API's JSON. Streaming commands print one JSON object
  per line (`{"event", "id", "data"}`).
- **Interrupt.** Ctrl-C ends a stream with exit code 130 and no error dump.

### Storage

`stackstead storage df [-o table|json]` shows KIND, COUNT, SIZE, RECLAIMABLE
and AS OF for images, container layers, volumes and build cache. Unknown sizes
are `—`; JSON prints the overview API response.

`stackstead storage prune <images|volumes|build-cache> [--preview] [--yes] [--detach]`
prints the proposed items, protected items, warnings and reclaim estimate.
`--preview` exits 0 without deleting anything. Without it, type the displayed
phrase exactly on a terminal; only the trailing input newline is stripped.
Without a terminal, `--yes` is required (exit 2). It confirms that specific
plan. Empty plans print `nothing to clean` and exit 0. `--detach` returns the
operation ID; otherwise output streams until completion. Host operations are
shown as `host` in `ops ls`, `ops show` and operation output.

Images means dangling images only; volumes means unused, unprotected volumes.
Stopped registered stacks' declared volumes remain protected. If declarations
are unknown or a volume has no creation timestamp, safe cleanup is refused.
Build-cache fallback says **not exact**: Docker removes whatever cache is
unused at confirmation time, which may differ from the expected preview.

Storage exits: 5 for conflicts/protected targets, 6 for a wrong typed phrase,
7 for unavailable Docker or size/declaration data, 8 for rate limiting, and 9
for a waited operation that fails, is cancelled or times out. Other codes
follow the table below. See [Storage API](api.md#storage) for cache ages,
identity checks, build-cache modes and action-log byte estimates.

### Images

`stackstead images ls [-o table|json]` lists the library the server sees: its
own images and those of directly paired servers (the OWNER column names the
server that owns each one). NAME is the image slug. Missing values are `-`;
JSON prints the library API response.

```bash
stackstead images ls
stackstead images build web                       # tag = the suggested next tag, streams the build
stackstead images build web --tag v2 --build-arg VERSION=2 --detach
stackstead images build app --ref release/1.4     # GitHub images only
stackstead images push web v2 --target hub/me/web   # REGISTRY/REPOSITORY, or the target ID
```

`images build` uses the server's suggested tag unless `--tag` is given. When
there is no suggestion (the latest tag is not numeric or semver-like) it exits
2 and prints the latest tag; pass `--tag`. `--build-arg` repeats and needs
`KEY=VALUE` (a bare `KEY` or `=VALUE` is exit 2; `KEY=` sets an empty value; a
repeated `--build-arg` key keeps the last value). `--ref` builds a GitHub image at a
branch, tag or commit; the server refuses it for upload images (exit 6). An explicit empty `--tag ""` is exit 2.

`images push` needs `--target` (`REGISTRY/REPOSITORY` or the target ID; a bare registry
name works when it matches one target) when the image
has more than one push target; with one it is used, with none the command exits
2. Both commands stream the operation like `stacks up` (exit 9 if it fails), or
print its ID with `--detach`.

Image exits: 5 for a taken tag, a running build, an image in use, or a missing
registry or GitHub token (`tag_taken`, `build_running`, `image_in_use`,
`registry_unconfigured`, `github_unconfigured`); 8 for GitHub rate limiting;
7 when GitHub or the owning peer is unreachable (502). The server's message is
printed.

### Backups

`stackstead backup run [--detach]` starts a backup now and streams its output
like `stacks up`. It exits 9 when the run failed, and also when it finished
`partial` (some items failed), printing `backup partial: N item(s) failed`.
`--detach` prints the operation ID and returns. A run already in progress is
exit 5.

`stackstead backup list [-o table|json]` lists archives found at the configured
destinations: HOST, TIME, SIZE and DESTINATION (`local` or `s3`). JSON prints
the API response.

```bash
stackstead backup run
stackstead backup list
```

### Contexts and credentials

`login` saves the server URL and the session cookie in
`$XDG_CONFIG_HOME/stackstead/contexts.json` (default
`~/.config/stackstead/contexts.json`), mode 0600 in a 0700 directory. **That
file holds a live session**; treat it like an SSH key. `logout` and an expired
session remove the stored cookie. Passwords are never accepted as flag values;
use the prompt or `--password-stdin`. `STACKSTEAD_CONTEXT` selects a context
for one shell.

`login` refuses to point an existing context name at a different URL; use
`--context <new-name>` or `stackstead context rm <name>` first.

### TLS

System roots are used by default. `--ca-file F` trusts only that CA (stored
with the context; https URLs only, like `--insecure`), and `--insecure` skips verification (stored, and warned
about on every use). Plain `http://` to a non-loopback host works but warns
that the cookie travels in clear text. Redirects to another host are refused.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | success (for a waited operation: it succeeded) |
| 1 | runtime error: 5xx, I/O, unexpected response, config or database problem in `health` |
| 2 | usage: bad flags or arguments, no command, a prompt needed but stdin is not a terminal |
| 3 | authentication or permission: no context, 401 (the stored session is cleared), 403, refused as root; for `upgrade`: it needs root |
| 4 | not found: 404, unknown context, no database |
| 5 | conflict: 409 (busy, stale hash, exists), another server holds the lock, target exists without `--force`; for `upgrade`: a downgrade or reinstall without `--force`, a development build, the dpkg lock held |
| 6 | validation: invalid input, invalid config, weak or mismatched password; for `upgrade`: checksum mismatch, asset missing from `SHA256SUMS`, unreadable `SHA256SUMS` |
| 7 | unavailable: 503, connection refused, Docker or compose down in `health`; for `upgrade`: the release source cannot be reached |
| 8 | rate limited (the message says how long to wait) |
| 9 | the waited operation ended `failed`, `cancelled` or `timed_out` |
| 130 | interrupted |

## Host administration

These commands read `--config P` (default `/etc/stackstead/config.yaml` when it
exists, with `STACKSTEAD_*` overrides) and work on the files directly. Run them
as the service user, for example `sudo -u stackstead stackstead db backup ...`.
Run as root over a database owned by another user, `admin` and `db` commands
refuse (exit 3) and print the `sudo -u` form, so root never leaves a root-owned
file in the data directory. They never create or migrate the database; only
`serve` and `db init` do.

| Command | Server running? | What it does |
| --- | --- | --- |
| `config init --path P [--listen A] [--stacks-dir D] [--data-dir D] [--force]` | any | write the commented config (exit 5 if it exists) |
| `config validate`, `config show` | any | check a config (exit 6 when invalid); show every key and where its value came from |
| `health [-o json]` | any | check config, database, server, Docker and compose; exit 7 when the server, Docker or compose is unreachable |
| `db init` | stopped | create and migrate the database; refuses (exit 5) while a server holds the lock |
| `db check` | any | integrity check |
| `db backup <dest> [--force]` | online-safe | consistent copy, even under writes |
| `db restore <src> [--yes]` | **stopped** | replace the database; exit 5 while a server runs |
| `admin set-password [--username U] [--password-stdin\|--password-file F]` | online-safe | create the admin or reset its password |
| `admin revoke-sessions` | online-safe | sign every session out |
| `admin metrics-token status\|rotate\|clear` | online-safe | manage the `/metrics` token |
| `upgrade [--check] [--version vX.Y.Z] [--force] [--yes] [--base-url URL] [-o json]` | any | download the release, verify it against `SHA256SUMS` and run the native installer; `--check` only reports; see [Upgrading](#upgrading) |

`serve`, `db restore` and `restore` take an exclusive lock on `<data_dir>/stackstead.lock`.
The other admin commands do not take it, so **do not run them while a
`db restore` is in progress**.

**Backup and restore.** `stackstead db backup /backups/stackstead.db` is safe at
any time. To restore: `sudo systemctl stop stackstead`, then
`sudo -u stackstead stackstead db restore /backups/stackstead.db --yes`, then
start the service. The restored database is a self-contained copy (any WAL
content is folded in), and the previous one is kept next to it as
`stackstead.db.bak-<UTC timestamp>`. Restore signs everyone out: all
sessions, connection tokens and pairings still in progress are deleted (paired
servers keep working).

**Whole-host restore.** `stackstead restore <s3://bucket/key | path>` restores a
whole-host backup archive (database, stack files, volumes, images) on a host
where the server is stopped, for example a fresh machine:

```bash
sudo systemctl stop stackstead
export AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=...   # only for an s3:// source
sudo -u stackstead -E stackstead restore s3://my-bucket/host/stackstead-2026-10-26.tar.gz --s3-region eu-west-1
sudo systemctl start stackstead
```

Flags: `--yes` skips the confirmation (required when stdin is not a terminal,
else exit 2), `--no-start` leaves the stacks that were running stopped,
`--s3-endpoint URL`, `--s3-region R` and `--s3-path-style` configure an
S3-compatible store. S3 keys are read only from `AWS_ACCESS_KEY_ID` and
`AWS_SECRET_ACCESS_KEY`, never from flags, and are never printed. The command
prints the archive summary and the existing volumes, networks, containers and
stack directories it would overwrite, recreate, stop or keep, asks once, restores, and
installs the restored database right away (the old one is kept as
`stackstead.db.bak-<UTC timestamp>`). Running containers of the restored
projects whose volumes are replaced are stopped before their volumes are
emptied and refilled (shown as "stopped during restore"); a project whose
container will not stop keeps its volumes, which are reported as skipped. The
stacks start when the server next starts, including those stopped here even
with `--no-start`; if the restore fails, the stopped containers are started
again right away. Everyone is signed out and connection tokens are revoked, as
with `db restore`. The backup schedule is turned off in the restored database, and the UI
says so until you turn it back on. Exit codes: 5 while the server runs (or a
restore is already pending), 6 for an archive from a newer Stackstead or a
damaged one, 9 when some items failed to restore (the database is still
installed; the failed items are listed), 2 for a bad source or missing S3 keys,
3 when run as root and the data directory does not exist (root does not create
it, so the service user is not locked out; create it with
`sudo install -d -o stackstead -m 0700 <data_dir>` and run the restore as the
service user).

Known issue (Docker Desktop on macOS): a bind directory that was deleted and
re-created at the same path may not be visible to Docker for a short while, so
restoring it can fail with "bind source path does not exist" (exit 9). If a
restored stack reports a missing bind source, restart Docker Desktop and restore
the stack again from the UI, or run `docker compose up -d` in its directory.

**Forgot the admin password.**

```bash
sudo -u stackstead stackstead admin set-password --config /etc/stackstead/config.yaml
```

prompts twice on a terminal, or reads one line with `--password-stdin` (at
least 12 characters). It takes effect immediately, on a running server too, and
signs out every session. If the DB does not exist yet, run `db init` or start
the server once.

**Metrics token.** `admin metrics-token rotate -o json` prints the new token
once. A running server picks up a rotate or clear within 5 seconds.

## Upgrading

`stackstead upgrade` needs no config file. It works out how this host was
installed (install script, `.deb`, `.rpm` or macOS `.pkg`), downloads the
matching asset from the latest GitHub release, checks it against that
release's `SHA256SUMS`, and hands it to the native installer. Nothing is
changed before the checksum passes. It prints a backup tip and asks for
confirmation (`--yes` skips the prompt; without a terminal it is required).

| Flag | Effect |
| --- | --- |
| `--check` | only report the installed and latest versions and the command to run; with `-o json` it prints the same object as `GET /api/settings/update`. Exit 0 whether or not an update exists. Cannot be combined with `--force` or `--yes` |
| `--version vX.Y.Z` | install that release instead of the latest |
| `--force` | reinstall the same version, or downgrade (script installs only), or install over a development build |
| `--yes` | do not ask for confirmation |
| `--base-url URL` | release source for mirrors and tests: `https://`, or `http://` to a loopback host. Prints a warning, because it replaces the default `https://github.com/rcss-labs/stackstead` |

What runs, always as one fixed command with no shell:

| Installed with | Command |
| --- | --- |
| script | the downloaded, verified `install.sh --version vX.Y.Z --prefix <prefix>` (the prefix is where `stackstead` lives, normally `/usr/local`) |
| `.deb` | `apt-get install -y <file>.deb` (refuses with exit 5 if another dpkg or apt holds the lock; an edited config file is kept, as the dpkg options `--force-confdef --force-confold` are set) |
| `.rpm` | `dnf install -y <file>.rpm`, or `rpm -U` when `dnf` is not installed |
| `.pkg` | `installer -pkg <file>.pkg -target /` |

Run it as root on Linux and for the macOS `.pkg`. For a script install on macOS
run it as your own user (`/usr/local/bin/stackstead upgrade`); `install.sh`
asks for `sudo` itself. If you run it without the needed rights it exits 3 and
prints the exact command to use.

`upgrade` never restarts anything itself; the installer does. The script and
the packages restart the service only if it was running (a stopped or disabled
service stays that way), and the macOS installers kick the LaunchAgent. On Linux
without systemd, `install.sh` prints a note to restart under your own
supervisor. When you upgrade the host that serves the web UI, the page
reconnects by itself after the restart.

Package downgrades are not done by `upgrade`: it exits 5 and prints the
download, checksum and `apt-get --allow-downgrades` or `rpm -U --oldpackage`
commands for you to run. Script installs downgrade with `--force`.

**Database.** The service migrates its database forward the first time it
starts on a newer version. That cannot be undone by installing the older
binary: a service that finds a schema newer than itself refuses to start until
you restore a backup made before the upgrade (`db restore`, see above). Take a
backup first; `upgrade` prints the command.

Exit codes follow the table above. When it runs `install.sh`, that script's
own codes are mapped (verification 6, source unreachable 7, needs a user
account on macOS 3); a failure of `apt-get`, `dnf`, `rpm` or `installer` is
exit 1 with the tool's name and status.

## Service unit notes

The shipped unit runs `stackstead serve --config /etc/stackstead/config.yaml`,
validates the config first (`ExecStartPre`), and uses `UMask=0007` so files the
service creates are private to its user and group. The unit sets
`ProtectSystem=full`, which makes only `/usr`, `/boot` and `/etc` read-only for
the service; a drop-in (`systemctl edit stackstead`) with
`ReadWritePaths=/path/to/stacks` is needed only for stack folders under those
paths (and `PrivateTmp=no` if stacks live in `/tmp`). Account, group and
cleanup details of packaged installs are described with the installers.

The unit file lives at `deploy/systemd/stackstead.service` and expects the
binary at `/usr/bin/stackstead` (where the `.deb` and `.rpm` put it; the install
script puts it in `/usr/local/bin` and rewrites the unit). Earlier versions of
the unit ran the bare `stackstead --config ...`; that form still works but
prints a deprecation note, so switch to `serve`. Install, upgrade and removal
are covered in [install.md](install.md).
