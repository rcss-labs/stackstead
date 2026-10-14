# Command line

Back to the [README](../README.md).

The `stackstead` binary is both the server and a complete command-line tool.
`stackstead help` groups the commands:

- **Server:** `serve`, `version`.
- **Remote:** talk to a running server over its HTTP API from any machine:
  `login`, `logout`, `context`, `whoami`, `status`, `stacks`, `ops`, `logs`,
  `metrics`, `events`, `networks`, `settings`.
- **Host admin:** run on the server's host, next to the files:
  `config`, `db`, `admin`, `health`.

Global flags, before or after the command: `--context NAME`,
`-o table|json` (also `--output`; write `-o json` or `-o=json`, not `-ojson`),
`--no-color`, and `--timeout D` (per request, default 30s). Colour is used only
on a terminal, and never when `NO_COLOR` is set or `TERM=dumb`.

## Remote commands

```bash
stackstead login https://stacks.example.com      # prompts for the password
stackstead status                                 # Docker, compose, event feed, metrics
stackstead stacks ls
stackstead stacks up web                          # streams the operation, exits 0 or 9
stackstead stacks down web --detach               # start it and return at once
stackstead stacks rm web --volumes --yes          # files stay on disk
stackstead logs web -f --service app --tail 100
stackstead metrics web --from 2h --step 5m
stackstead ops ls --running
```

| Command | What it does |
| --- | --- |
| `login <url> [--context N] [--username U] [--password-stdin] [--ca-file F] [--insecure]` | log in and save the session as a context |
| `logout`, `whoami`, `status` | sign out; show the account and server; show Docker, compose and feed status |
| `context ls\|current\|use <name>\|rm <name>` | manage saved servers |
| `stacks ls`, `stacks show <name>` | list stacks; services and containers of one |
| `stacks up\|down\|restart\|pull\|build <name> [--detach]` | lifecycle actions; they wait for the operation unless `--detach` |
| `stacks rm <name> [--volumes] [--yes]` | compose down; asks on a terminal, `--yes` is required without one |
| `stacks import <path>` | import an existing compose directory |
| `stacks compose get\|edit\|put <name>` | read, edit (in `$VISUAL`/`$EDITOR`) or replace (`-f FILE --hash H`) the compose file |
| `ops ls\|show\|stream\|cancel` | operations and their output |
| `logs <stack> [-f] [--tail N\|all] [--since X] [--service S] [--timestamps] [--raw]` | container logs |
| `metrics <stack> [--from T] [--to T] [--step D] [--service S] [--live]` | resource history or a live feed |
| `events` | follow container changes until interrupted |
| `networks ls` | Docker networks |
| `settings metrics-token status\|rotate\|clear`, `settings password` | the `/metrics` token (printed once when created); change the account password |

Details worth knowing:

- **`logs`** strips terminal escape sequences and other control characters from
  container output, so a container cannot drive your terminal. `--raw` prints
  the text unchanged. With `-f`, a dropped connection resumes exactly where it
  stopped, with no gaps or repeats.
- **`metrics`** shows history. CPU and memory are averages across a service's
  replicas, and the network and block I/O rates are totals across them. With
  `--live` there is one row per container (a `CONTAINER` column), and the
  network and block I/O counters are cumulative totals, not rates.
- **`stacks compose edit`** splits `$VISUAL` (else `$EDITOR`) on whitespace and
  runs it without a shell: `code --wait` works, quoting and pipes do not.
- **`-o json`** prints the API's JSON. Streaming commands print one JSON object
  per line (`{"event", "id", "data"}`).
- **Interrupt.** Ctrl-C ends a stream with exit code 130 and no error dump.

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
with the context), and `--insecure` skips verification (stored, and warned
about on every use). Plain `http://` to a non-loopback host works but warns
that the cookie travels in clear text. Redirects to another host are refused.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | success (for a waited operation: it succeeded) |
| 1 | runtime error: 5xx, I/O, unexpected response, config or database problem in `health` |
| 2 | usage: bad flags or arguments, no command, a prompt needed but stdin is not a terminal |
| 3 | authentication or permission: no context, 401 (the stored session is cleared), 403, refused as root |
| 4 | not found: 404, unknown context, no database |
| 5 | conflict: 409 (busy, stale hash, exists), another server holds the lock, target exists without `--force` |
| 6 | validation: invalid input, invalid config, weak or mismatched password |
| 7 | unavailable: 503, connection refused, Docker or compose down in `health` |
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
| `db init`, `db check` | any | create and migrate the database; integrity check |
| `db backup <dest> [--force]` | online-safe | consistent copy, even under writes |
| `db restore <src> [--yes]` | **stopped** | replace the database; exit 5 while a server runs |
| `admin set-password [--username U] [--password-stdin\|--password-file F]` | online-safe | create the admin or reset its password |
| `admin revoke-sessions` | online-safe | sign every session out |
| `admin metrics-token status\|rotate\|clear` | online-safe | manage the `/metrics` token |

`serve` and `db restore` take an exclusive lock on `<data_dir>/stackstead.lock`.
The other admin commands do not take it, so **do not run them while a
`db restore` is in progress**.

**Backup and restore.** `stackstead db backup /backups/stackstead.db` is safe at
any time. To restore: `sudo systemctl stop stackstead`, then
`sudo -u stackstead stackstead db restore /backups/stackstead.db --yes`, then
start the service. The restored database is a self-contained copy (any WAL
content is folded in), and the previous one is kept next to it as
`stackstead.db.bak-<UTC timestamp>`. Sessions are whatever the backup holds.

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

## Service unit notes

The shipped unit runs `stackstead serve --config /etc/stackstead/config.yaml`,
validates the config first (`ExecStartPre`), and uses `UMask=0007` so files the
service creates are private to its user and group. The `stackstead` user must
be in the `docker` group (`SupplementaryGroups=docker` in the unit, and a
membership in `/etc/group` for the CLI). The unit sets `ProtectSystem=full`;
to let the service write stack folders outside `/opt/stacks`, add a drop-in
(`systemctl edit stackstead`) with `ReadWritePaths=/path/to/stacks`. After a
manual rpm uninstall, remove leftovers with
`sudo rm -rf /var/lib/stackstead && sudo userdel stackstead`.

The unit file lives at `deploy/stackstead.service` in this tree; the installers
move it to `deploy/systemd/stackstead.service`. Earlier versions of the unit
ran the bare `stackstead --config ...`; that form still works but prints a
deprecation note, so switch to `serve`.
