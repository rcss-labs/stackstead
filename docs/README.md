# Stackstead documentation

The hub for everything under `docs/`. Back to the [project README](../README.md).

## Pick your path

| You are | Start here |
| --- | --- |
| **An operator** running Stackstead on a host | [Installing](install.md), then [Security](security.md) and the [Command line](cli.md) |
| **An API user** scripting or monitoring it | [API and observability](api.md), plus [Contexts and credentials](cli.md#contexts-and-credentials) |
| **A contributor** building or changing it | [Building and development](development.md), then [Releasing](../deploy/release/RELEASING.md) |

## I want to...

| Task | Go to |
| --- | --- |
| Install on Debian, Ubuntu, Fedora or Rocky | [Linux packages](install.md#linux-packages) (or [the install script](install.md#the-install-script)) |
| Install on a Linux box without systemd | [Choose a format](install.md#choose-a-format) |
| Install on macOS | [macOS](install.md#macos) |
| Verify a download | [Verifying a download by hand](install.md#verifying-a-download-by-hand) |
| Finish first-run setup or fix the setup token | [First run](install.md#first-run), [First-run setup](security.md#first-run-setup) |
| Upgrade | [Upgrade](install.md#upgrade); flags and exit codes in [Upgrading](cli.md#upgrading) |
| Back up or restore the database | [Host administration](cli.md#host-administration) (the "Backup and restore" paragraph); back up before an [upgrade](install.md#upgrade) |
| Reset a forgotten admin password | [Host administration](cli.md#host-administration) ("Forgot the admin password") |
| Change the password from the UI | [Password change](security.md#password-change) |
| Configure Stackstead (config keys and environment) | [`deploy/config.example.yaml`](../deploy/config.example.yaml); `stackstead config show` lists every key and its source ([Host administration](cli.md#host-administration)); see also [Quick reference](#quick-reference) |
| Use a private registry | [First run](install.md#first-run) |
| Let stacks live under `/etc`, `/usr` or `/tmp` | [Compose files outside the service's reach](install.md#compose-files-outside-the-services-reach) |
| Use the CLI against a remote server, switch contexts | [Remote commands](cli.md#remote-commands), [Contexts and credentials](cli.md#contexts-and-credentials), [TLS](cli.md#tls) |
| Call the API | [API overview](api.md#api-overview) |
| Follow logs, read metrics history | [Observability](api.md#observability) |
| Scrape Prometheus metrics | [Prometheus](api.md#prometheus), [What is stored](api.md#what-is-stored) |
| Expose it behind a reverse proxy | [Remote access](security.md#remote-access) |
| Understand the threat model and hardening | [The short version](security.md#the-short-version), [Security notes](install.md#security-notes), [Service unit notes](cli.md#service-unit-notes) |
| Turn the update check off | [The short version](security.md#the-short-version) (`update_check: false`) |
| Uninstall or purge | [Uninstall and purge](install.md#uninstall-and-purge) |
| Build from source | [Build](development.md#build), [Make targets](development.md#make-targets), [Manual install (systemd)](install.md#manual-install-systemd) |
| Run the dev loop | [Dev loop](development.md#dev-loop) |
| Run the tests | [Tests](development.md#tests) |
| Cut a release | [`deploy/release/RELEASING.md`](../deploy/release/RELEASING.md) |

## The documents

### [Installing](install.md)

Installers and the host layout: the script, `.deb`/`.rpm`, the macOS `.pkg`, and upgrade/removal.

- [Choose a format](install.md#choose-a-format): which installer fits Linux with or without systemd, and macOS.
- [The install script](install.md#the-install-script): flags (`--version`, `--dry-run`, `--force`, `--uninstall`), always-on checksum verification.
- [Verifying a download by hand](install.md#verifying-a-download-by-hand): what unsigned releases do and do not guarantee.
- [Linux packages](install.md#linux-packages)
- [Where things go](install.md#where-things-go): binary, unit, config, data and stacks paths per installer.
- [First run](install.md#first-run): admin account, setup token, private registries.
- [Security notes](install.md#security-notes) and [Compose files outside the service's reach](install.md#compose-files-outside-the-services-reach).
- [Upgrade](install.md#upgrade), [Uninstall and purge](install.md#uninstall-and-purge), [macOS](install.md#macos), [Manual install (systemd)](install.md#manual-install-systemd).
- [Exit codes](install.md#exit-codes): those of `install.sh`, separate from the CLI's.

### [Command line](cli.md)

The `stackstead` binary as a server, a remote client and a host admin tool.

- [Remote commands](cli.md#remote-commands): `login`, `stacks`, `ops`, `logs`, `metrics`, `events`, `networks`, `settings` and more, with [Contexts and credentials](cli.md#contexts-and-credentials) and [TLS](cli.md#tls).
- [Storage](cli.md#storage): disk usage, cleanup previews, typed confirmation and exit codes.
- [Exit codes](cli.md#exit-codes)
- [Host administration](cli.md#host-administration): `config`, `db` (backup and restore), `admin`, `health`.
- [Upgrading](cli.md#upgrading): `stackstead upgrade` flags, what it runs per install type, database caveats.
- [Service unit notes](cli.md#service-unit-notes)

### [API and observability](api.md)

The JSON API behind the web UI, and how Stackstead observes Docker.

- [API overview](api.md#api-overview): routes, authentication, error format.
- [Storage](api.md#storage): inventory, cleanup plans, build-cache modes and persistent action history.
- [Observability](api.md#observability): event stream, log streaming and resume, metrics history.
- [Dashboard data](api.md#dashboard-data), [Containers](api.md#containers), [Networks](api.md#networks).
- [What is stored](api.md#what-is-stored) and [Prometheus](api.md#prometheus): retention, sampling limits, the `/metrics` endpoint and its token.

### [Security](security.md)

What Stackstead exposes and how to run it safely.

- [The short version](security.md#the-short-version): Docker access is root-equivalent; authentication, CSP, what the API discloses, the update check and its privacy.
- [Storage cleanup](security.md#storage-cleanup): permanent volume deletion, protections and action logging.
- [Remote access](security.md#remote-access): TLS reverse proxy settings and the HTTP/1.1 connection limit.
- [First-run setup](security.md#first-run-setup) and [Password change](security.md#password-change).

### [Building and development](development.md)

For contributors.

- [Build](development.md#build) and [Make targets](development.md#make-targets) (including the UI bundle budgets).
- [Fonts and theme](development.md#fonts-and-theme)
- [Dev loop](development.md#dev-loop): backend plus the Vite dev server.
- [Tests](development.md#tests): unit, integration (needs Docker), `make e2e`, and the lack of CI.
- [Design documents](development.md#design-documents)

### Designs for features not built yet

[`docs/superpowers/specs/`](superpowers/specs/) holds drafts awaiting review, none implemented. Specs for shipped features are removed; find them in git history.

| Design | Summary |
| --- | --- |
| [Richer compose forms](superpowers/specs/2026-10-18-stackstead-compose-forms-design.md) | `depends_on` with conditions, `env_file`, `secrets` and `profiles` in the form editor; support for compose override files. |
| [Configuration history](superpowers/specs/2026-10-18-stackstead-config-history-design.md) | Timestamped revisions of a stack's compose files and `.env`, diffs between them, and restore after a preview (replaces the single `.bak`). |
| [Image updates](superpowers/specs/2026-10-18-stackstead-image-updates-design.md) | Detect newer digests or tags, show what changed, and update one service or a stack, with a way back. |
| [Notifications](superpowers/specs/2026-10-18-stackstead-notifications-design.md) | Telegram or webhook notifications for unhealthy services, restart loops, failed operations and low disk. |
| [Reachability checks](superpowers/specs/2026-10-18-stackstead-reachability-checks-design.md) | Scheduled HTTP, TCP and DNS checks beyond Docker healthchecks, with history and alerts. |
| [Volume and mount diagnostics](superpowers/specs/2026-10-18-stackstead-volume-diagnostics-design.md) | Per-mount diagnostics that flag the mistakes behind "permission denied" and silently empty data. |

## Quick reference

Authoritative sources, not copies:

- **Config keys:** [`deploy/config.example.yaml`](../deploy/config.example.yaml) lists every key; each can also be set as `STACKSTEAD_<KEY>` (for example `STACKSTEAD_UPDATE_CHECK`, see [security.md](security.md#the-short-version)). `stackstead config show` prints the effective values and where each came from ([Host administration](cli.md#host-administration)).
- **CLI exit codes:** [cli.md#exit-codes](cli.md#exit-codes).
- **`install.sh` exit codes:** [install.md#exit-codes](install.md#exit-codes).

## Versioning and releases

- Releases use semantic-version git tags `vX.Y.Z` (release candidates `vX.Y.Z-rc.N`); the artifact version drops the `v`. See "Version strings" in [`RELEASING.md`](../deploy/release/RELEASING.md).
- Each release page carries the binaries, the `.deb`, `.rpm` and macOS `.pkg`, `install.sh` and `SHA256SUMS`. Release notes live on the [GitHub releases page](https://github.com/rcss-labs/stackstead/releases); the latest is [v0.3.0](https://github.com/rcss-labs/stackstead/releases/tag/v0.3.0).
- Releases are **unsigned**. `SHA256SUMS` guards against corrupt or wrong downloads, not a compromised publisher ([details](install.md#verifying-a-download-by-hand)).
- Releases are built and published by hand; there is no CI yet. The runbook is [`deploy/release/RELEASING.md`](../deploy/release/RELEASING.md).
- `stackstead version` and `/api/status` show the running version and commit.
