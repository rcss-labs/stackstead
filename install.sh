#!/bin/sh
# Stackstead installer (Linux; macOS arrives in a later task).
#
#   curl -fsSL https://github.com/rcss-labs/stackstead/releases/latest/download/install.sh | sudo sh
#
# Downloads a release tarball, verifies it against SHA256SUMS (always), installs
# the binary, service user, directories and systemd unit, and starts the service.
# Re-running upgrades in place; config, data and stacks are never touched.
set -eu

DEFAULT_BASE_URL=https://github.com/rcss-labs/stackstead
SERVICE_USER=stackstead
SERVICE_NAME=stackstead
DOCKER_URL=https://docs.docker.com/engine/install/

usage() {
	cat <<'EOF'
Usage: install.sh [options]

Install or upgrade Stackstead from a verified GitHub release.

Options:
  --version vX.Y.Z    pin a release (default: latest); env STACKSTEAD_VERSION
  --prefix DIR        install under DIR (default /usr/local): DIR/bin, DIR/share/stackstead
  --no-start          install and enable the service but do not start or restart it
  --signers FILE      also verify SHA256SUMS.sig with ssh-keygen -Y against FILE
                      (off by default; no key is shipped yet); env STACKSTEAD_INSTALL_SIGNERS_FILE
  --force             reinstall the same version or an older one
  --base-url URL      release repository URL (default https://github.com/rcss-labs/stackstead);
                      env STACKSTEAD_BASE_URL
  --root DIR          stage into DIR (DESTDIR style); no root needed; service and account
                      commands are printed, not run
  --dry-run           download and verify, print the plan, change nothing
  --uninstall         remove the binary, unit and helper (keeps config, data, stacks, user)
  --purge             with --uninstall: also remove /etc/stackstead, /var/lib/stackstead and
                      the stackstead user (never /opt/stacks or anything in Docker)
  --print-paths       print the resolved paths as KEY=VALUE and exit
  -h, --help          show this help

The tarball checksum is always verified; there is no option to skip it.

Environment:
  STACKSTEAD_VERSION, STACKSTEAD_BASE_URL, STACKSTEAD_INSTALL_SIGNERS_FILE, NO_COLOR

Exit codes:
  0  success, or nothing to do
  1  unexpected failure
  2  usage error
  3  unsupported OS or architecture
  4  a required tool is missing
  5  download failure
  6  verification failure (checksum or signature)
  7  an install step failed (user, directories, unit, service)
  8  refused (not root, or a package-managed install is present)
EOF
}

# ---------------------------------------------------------------- output

say() { printf '==> %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
err() { printf 'error: %s\n' "$*" >&2; }

DIED=
PHASE=
TMP=

# die CODE MESSAGE...: report and exit with a documented code.
die() {
	code=$1
	shift
	DIED=1
	err "$*"
	exit "$code"
}

usage_error() {
	DIED=1
	err "$*"
	usage >&2
	exit 2
}

cleanup() {
	status=$?
	if [ -n "$TMP" ]; then
		rm -rf "$TMP"
	fi
	if [ "$status" -ne 0 ] && [ -z "$DIED" ]; then
		# set -e or a signal ended the script: map to the documented codes.
		if [ "$PHASE" = install ]; then
			err "install step failed (see the message above); the system may be partially installed, re-run to retry"
			exit 7
		fi
		err "unexpected failure (exit status $status)"
		exit 1
	fi
}
trap cleanup EXIT
trap 'DIED=1; exit 130' INT TERM HUP

# ---------------------------------------------------------------- options

VERSION_ARG=${STACKSTEAD_VERSION:-}
BASE_URL=${STACKSTEAD_BASE_URL:-$DEFAULT_BASE_URL}
SIGNERS=${STACKSTEAD_INSTALL_SIGNERS_FILE:-}
PREFIX=/usr/local
ROOT=
NO_START=
FORCE=
DRY_RUN=
UNINSTALL=
PURGE=
PRINT_PATHS=

need_value() { # option remaining-arg-count
	[ "$2" -ge 2 ] || usage_error "$1 needs a value"
}

while [ $# -gt 0 ]; do
	case $1 in
	--version) need_value "$1" $#; VERSION_ARG=$2; shift 2 ;;
	--version=*) VERSION_ARG=${1#*=}; shift ;;
	--prefix) need_value "$1" $#; PREFIX=$2; shift 2 ;;
	--prefix=*) PREFIX=${1#*=}; shift ;;
	--signers) need_value "$1" $#; SIGNERS=$2; shift 2 ;;
	--signers=*) SIGNERS=${1#*=}; shift ;;
	--base-url) need_value "$1" $#; BASE_URL=$2; shift 2 ;;
	--base-url=*) BASE_URL=${1#*=}; shift ;;
	--root) need_value "$1" $#; ROOT=$2; shift 2 ;;
	--root=*) ROOT=${1#*=}; shift ;;
	--no-start) NO_START=1; shift ;;
	--force) FORCE=1; shift ;;
	--dry-run) DRY_RUN=1; shift ;;
	--uninstall) UNINSTALL=1; shift ;;
	--purge) PURGE=1; shift ;;
	--print-paths) PRINT_PATHS=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*) usage_error "unknown option: $1" ;;
	esac
done

[ -z "$PURGE" ] || [ -n "$UNINSTALL" ] || usage_error "--purge only makes sense with --uninstall"
case $PREFIX in
/*) ;;
*) usage_error "--prefix must be an absolute path: $PREFIX" ;;
esac
case $PREFIX in
*[!A-Za-z0-9/._+-]*) usage_error "--prefix may only contain letters, digits and / . _ + -: $PREFIX" ;;
esac
PREFIX=${PREFIX%/}
[ -n "$PREFIX" ] || PREFIX=/
case $ROOT in
"" | /*) ;;
*) usage_error "--root must be an absolute path: $ROOT" ;;
esac
ROOT=${ROOT%/}
BASE_URL=${BASE_URL%/}

# ---------------------------------------------------------------- paths

# rp PATH: where an absolute PATH lands (prefixed with --root when staging).
rp() { printf '%s%s' "$ROOT" "$1"; }

BIN_DIR=$PREFIX/bin
BIN=$BIN_DIR/stackstead
HELPER_DIR=$PREFIX/share/stackstead
HELPER=$HELPER_DIR/install.sh
UNIT=/etc/systemd/system/$SERVICE_NAME.service
CONFIG_DIR=/etc/stackstead
CONFIG=$CONFIG_DIR/config.yaml
DATA_DIR=/var/lib/stackstead
STACKS_DIR=/opt/stacks
DEFAULT_LISTEN=127.0.0.1:8420

if [ -n "$PRINT_PATHS" ]; then
	printf 'PREFIX=%s\nBIN=%s\nHELPER=%s\nUNIT=%s\nCONFIG=%s\nDATA_DIR=%s\nSTACKS_DIR=%s\nSERVICE_USER=%s\nROOT=%s\n' \
		"$(rp "$PREFIX")" "$(rp "$BIN")" "$(rp "$HELPER")" "$(rp "$UNIT")" "$(rp "$CONFIG")" \
		"$(rp "$DATA_DIR")" "$(rp "$STACKS_DIR")" "$SERVICE_USER" "$ROOT"
	exit 0
fi

# ---------------------------------------------------------------- helpers

have() { command -v "$1" >/dev/null 2>&1; }

# svc / acct: run a service or account command, or print it when staging.
svc() {
	if [ -n "$ROOT" ]; then
		say "would run: $*"
	else
		"$@"
	fi
}
acct() { svc "$@"; }

# own USER[:GROUP] PATH: change ownership, only where it can work.
own() {
	if [ -n "$ROOT" ] && { [ "$(id -u)" -ne 0 ] || ! id "$SERVICE_USER" >/dev/null 2>&1; }; then
		return 0
	fi
	if [ "$(id -u)" -ne 0 ]; then
		return 0
	fi
	chown "$1" "$2"
}

user_exists() { id "$SERVICE_USER" >/dev/null 2>&1; }

group_exists() { # NAME
	if have getent; then
		getent group "$1" >/dev/null 2>&1
	else
		grep -q "^$1:" /etc/group 2>/dev/null
	fi
}

nologin_shell() {
	for s in /usr/sbin/nologin /sbin/nologin /usr/bin/nologin /bin/false; do
		if [ -x "$s" ]; then
			printf '%s' "$s"
			return 0
		fi
	done
	printf '/bin/false'
}

# ---------------------------------------------------------------- download tools

DOWNLOADER=

detect_downloader() {
	if have curl; then
		DOWNLOADER=curl
	elif have wget; then
		DOWNLOADER=wget
	else
		die 4 "need curl or wget (apt install curl | dnf install curl | apk add curl)"
	fi
}

# try_fetch URL DEST: download; the status says whether it worked.
try_fetch() {
	if [ "$DOWNLOADER" = curl ]; then
		curl -fsSL --retry 2 --connect-timeout 15 -o "$2" "$1"
	else
		wget -q -O "$2" -T 30 "$1"
	fi
}

# fetch URL DEST: download or exit 5.
fetch() {
	try_fetch "$1" "$2" || die 5 "download failed: $1"
}

# http_ok URL: quiet probe, true on HTTP 200.
http_ok() {
	if [ "$DOWNLOADER" = curl ]; then
		curl -fsS -o /dev/null -m 2 "$1" >/dev/null 2>&1
	else
		wget -q -O /dev/null -T 2 "$1" >/dev/null 2>&1
	fi
}

# sha256_of FILE: print the hex digest.
sha256_of() {
	if have sha256sum; then
		sha256sum "$1" | awk '{print $1}'
	elif have shasum; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		openssl dgst -sha256 "$1" | awk '{print $NF}'
	fi
}

check_tools() {
	detect_downloader
	have tar || die 4 "need tar (apt install tar | dnf install tar | apk add tar)"
	if ! have sha256sum && ! have shasum && ! have openssl; then
		die 4 "need a SHA-256 tool: sha256sum (coreutils), shasum or openssl"
	fi
	have awk || die 4 "need awk"
	have sed || die 4 "need sed"
	have mktemp || die 4 "need mktemp"
	if [ -n "$SIGNERS" ]; then
		if ! have ssh-keygen || ! ssh-keygen -Y 2>&1 | grep -q verify; then
			die 4 "--signers needs an ssh-keygen that supports -Y (apt install openssh-client | dnf install openssh-clients | apk add openssh-keygen)"
		fi
		[ -r "$SIGNERS" ] || die 2 "signers file not readable: $SIGNERS"
	fi
}

# ---------------------------------------------------------------- platform

OS=
ARCH=

detect_platform() {
	case $(uname -s) in
	Linux) OS=linux ;;
	Darwin) die 3 "macOS support arrives in a later task" ;;
	*) die 3 "unsupported operating system: $(uname -s)" ;;
	esac
	case $(uname -m) in
	x86_64 | amd64) ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	*) die 3 "unsupported architecture: $(uname -m)" ;;
	esac
}

require_root() {
	[ -n "$ROOT" ] && return 0
	[ -n "$DRY_RUN" ] && return 0
	if [ "$(id -u)" -ne 0 ]; then
		die 8 "this needs root: re-run with sudo (or use --root DIR to stage, --dry-run to preview)"
	fi
}

refuse_package_install() {
	if have dpkg && dpkg -S /usr/bin/stackstead >/dev/null 2>&1; then
		die 8 "stackstead is installed by a package (dpkg); use apt to upgrade or remove it"
	fi
	if have rpm && rpm -qf /usr/bin/stackstead >/dev/null 2>&1; then
		die 8 "stackstead is installed by a package (rpm); use dnf to upgrade or remove it"
	fi
}

# ---------------------------------------------------------------- versions

# plain_version V: strip a leading v.
plain_version() { printf '%s' "${1#v}"; }

valid_version() {
	printf '%s' "$1" | grep -Eq '^v?[0-9]+\.[0-9]+\.[0-9]+([-+.~][0-9A-Za-z.+~-]*)?$'
}

# ver_cmp A B: print -1, 0 or 1 (semver precedence; no prerelease beats a prerelease).
ver_cmp() {
	awk -v a="$(plain_version "$1")" -v b="$(plain_version "$2")" '
	function split_ver(s, out,    core, pre, i) {
		sub(/\+.*/, "", s)
		i = index(s, "-")
		if (i) { core = substr(s, 1, i - 1); pre = substr(s, i + 1) } else { core = s; pre = "" }
		out["pre"] = pre
		out["n"] = split(core, parts, ".")
		for (i = 1; i <= 3; i++) out[i] = (i <= out["n"]) ? parts[i] + 0 : 0
	}
	BEGIN {
		split_ver(a, A); split_ver(b, B)
		for (i = 1; i <= 3; i++) {
			if (A[i] < B[i]) { print -1; exit }
			if (A[i] > B[i]) { print 1; exit }
		}
		if (A["pre"] == B["pre"]) { print 0; exit }
		if (A["pre"] == "") { print 1; exit }
		if (B["pre"] == "") { print -1; exit }
		print (A["pre"] < B["pre"]) ? -1 : 1
	}'
}

# bounded CMD...: run with a time limit when timeout(1) exists.
bounded() {
	if have timeout; then
		timeout 5 "$@"
	else
		"$@"
	fi
}

# installed_version: print the installed version, or nothing when unknown.
# Wraps whichever form the CLI offers: `version --short`, `version -o json`,
# then the second field of `version`.
installed_version() {
	bin=$(rp "$BIN")
	[ -x "$bin" ] || return 0
	v=$(bounded "$bin" version --short 2>/dev/null | head -n 1) || v=
	if valid_version "$v"; then
		printf '%s' "$v"
		return 0
	fi
	v=$(bounded "$bin" version -o json 2>/dev/null | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1) || v=
	if valid_version "$v"; then
		printf '%s' "$v"
		return 0
	fi
	v=$(bounded "$bin" version 2>/dev/null | head -n 1 | awk '{print $2}') || v=
	if valid_version "$v"; then
		printf '%s' "$v"
	fi
	return 0
}

# ---------------------------------------------------------------- release

VERSION=
ASSET=
RELEASE_URL=
STAGE=

resolve_and_download() {
	TMP=$(mktemp -d "${TMPDIR:-/tmp}/stackstead-install.XXXXXX") || die 1 "cannot create a temporary directory"
	STAGE=$TMP/stage
	mkdir -p "$STAGE"

	if [ -n "$VERSION_ARG" ]; then
		valid_version "$VERSION_ARG" || usage_error "not a version: $VERSION_ARG (expected vX.Y.Z)"
		VERSION=$(plain_version "$VERSION_ARG")
	else
		say "Resolving the latest release"
		fetch "$BASE_URL/releases/latest/download/SHA256SUMS" "$TMP/latest-sums"
		name=$(awk -v suffix="_${OS}_${ARCH}.tar.gz" '
			index($2, "stackstead_") == 1 && length($2) > length(suffix) &&
			substr($2, length($2) - length(suffix) + 1) == suffix { print $2; exit }' "$TMP/latest-sums")
		[ -n "$name" ] || die 6 "the latest release lists no stackstead tarball for ${OS}/${ARCH}"
		name=${name#stackstead_}
		VERSION=${name%_"${OS}_${ARCH}.tar.gz"}
		valid_version "$VERSION" || die 6 "cannot read a version from the latest release asset names"
	fi
	ASSET=stackstead_${VERSION}_${OS}_${ARCH}.tar.gz
	RELEASE_URL=$BASE_URL/releases/download/v$VERSION
}

# already_current: decide whether there is anything to do. Prints a message and
# exits 0 for "same version" and "newer installed" unless --force.
check_installed() {
	cur=$(installed_version)
	if [ -z "$cur" ]; then
		if [ -x "$(rp "$BIN")" ]; then
			note "could not determine the installed version; reinstalling"
		fi
		return 0
	fi
	UPGRADE=1
	rel=$(ver_cmp "$cur" "$VERSION")
	if [ -z "$FORCE" ]; then
		if [ "$rel" = 0 ]; then
			say "stackstead v$(plain_version "$cur") already installed"
			exit 0
		elif [ "$rel" = 1 ]; then
			say "installed v$(plain_version "$cur") is newer than requested v$VERSION; use --force to downgrade"
			exit 0
		fi
	fi
	note "installed: v$(plain_version "$cur")"
}

download_and_verify() {
	say "Downloading stackstead v$VERSION (${OS}/${ARCH})"
	fetch "$RELEASE_URL/SHA256SUMS" "$TMP/SHA256SUMS"
	if [ -n "$SIGNERS" ]; then
		try_fetch "$RELEASE_URL/SHA256SUMS.sig" "$TMP/SHA256SUMS.sig" 2>/dev/null ||
			die 6 "no signature found at $RELEASE_URL/SHA256SUMS.sig (--signers was given)"
		if ! ssh-keygen -Y verify -n stackstead-release -I release@stackstead -f "$SIGNERS" \
			-s "$TMP/SHA256SUMS.sig" <"$TMP/SHA256SUMS" >/dev/null 2>&1; then
			die 6 "signature verification of SHA256SUMS failed"
		fi
		note "SHA256SUMS signature OK"
	fi
	want=$(awk -v n="$ASSET" '$2 == n || $2 == "*" n { print $1; exit }' "$TMP/SHA256SUMS")
	[ -n "$want" ] || die 6 "$ASSET is not listed in SHA256SUMS"
	fetch "$RELEASE_URL/$ASSET" "$TMP/$ASSET"
	got=$(sha256_of "$TMP/$ASSET")
	if [ "$got" != "$want" ]; then
		die 6 "checksum mismatch for $ASSET (expected $want, got $got); nothing was installed"
	fi
	note "checksum OK"
	if tar -tzf "$TMP/$ASSET" 2>/dev/null | grep -Eq '(^/|(^|/)\.\.(/|$))'; then
		die 6 "$ASSET contains unsafe paths; nothing was installed"
	fi
	tar -xzf "$TMP/$ASSET" -C "$STAGE" || die 6 "cannot unpack $ASSET"
	for f in stackstead stackstead.service config.example.yaml; do
		[ -f "$STAGE/$f" ] || die 6 "$ASSET does not contain $f"
	done
}

# ---------------------------------------------------------------- plan

print_plan() {
	say "Plan (nothing is changed in dry-run mode)"
	note "create/replace $(rp "$BIN")"
	note "create/replace $(rp "$UNIT")"
	note "create/replace $(rp "$HELPER")"
	note "create if missing $(rp "$CONFIG") (0640 root:$SERVICE_USER)"
	note "create if missing $(rp "$CONFIG_DIR") (0755), $(rp "$DATA_DIR") (0700 $SERVICE_USER), $(rp "$STACKS_DIR") (2775 $SERVICE_USER:docker)"
	note "would run: groupadd docker (if missing), create user $SERVICE_USER (if missing)"
	if [ -n "$NO_START" ]; then
		note "would run: systemctl daemon-reload; systemctl enable $SERVICE_NAME"
	elif [ -n "${UPGRADE:-}" ]; then
		note "would run: systemctl daemon-reload; systemctl restart $SERVICE_NAME (if it is running)"
	else
		note "would run: systemctl daemon-reload; systemctl enable --now $SERVICE_NAME"
	fi
}

print_uninstall_plan() {
	say "Plan (nothing is changed in dry-run mode)"
	note "would run: systemctl disable --now $SERVICE_NAME"
	note "remove $(rp "$UNIT"), $(rp "$BIN"), $(rp "$HELPER_DIR")"
	if [ -n "$PURGE" ]; then
		note "remove $(rp "$CONFIG_DIR"), $(rp "$DATA_DIR") and user $SERVICE_USER"
	fi
	note "keep $(rp "$STACKS_DIR") and everything in Docker"
}

# ---------------------------------------------------------------- install steps

ensure_account() {
	if ! group_exists docker; then
		if have groupadd; then
			acct groupadd --system docker
		elif have addgroup; then
			acct addgroup -S docker
		else
			die 7 "cannot create the docker group: no groupadd or addgroup"
		fi
	fi
	if user_exists; then
		return 0
	fi
	shell=$(nologin_shell)
	if have useradd; then
		acct useradd --system --user-group --home-dir "$DATA_DIR" --no-create-home --shell "$shell" "$SERVICE_USER"
	elif have adduser; then
		if ! group_exists "$SERVICE_USER"; then
			acct addgroup -S "$SERVICE_USER"
		fi
		acct adduser -S -D -H -h "$DATA_DIR" -s "$shell" -G "$SERVICE_USER" "$SERVICE_USER"
	else
		die 7 "cannot create the $SERVICE_USER user: no useradd or adduser"
	fi
}

# make_dir PATH MODE OWNER: create with mode and owner only when missing.
make_dir() {
	d=$(rp "$1")
	if [ -d "$d" ]; then
		return 0
	fi
	mkdir -p "$d"
	chmod "$2" "$d"
	[ -z "$3" ] || own "$3" "$d"
}

# put_file SRC DEST MODE: copy via a temp name in the same directory, then rename.
put_file() {
	mkdir -p "$(dirname "$2")"
	cp "$1" "$2.new"
	chmod "$3" "$2.new"
	mv -f "$2.new" "$2"
}

install_files() {
	make_dir "$CONFIG_DIR" 0755 ""
	make_dir "$DATA_DIR" 0700 "$SERVICE_USER"
	make_dir "$STACKS_DIR" 2775 "$SERVICE_USER:docker"

	if [ ! -e "$(rp "$CONFIG")" ]; then
		put_file "$STAGE/config.example.yaml" "$(rp "$CONFIG")" 0640
		own "root:$SERVICE_USER" "$(rp "$CONFIG")"
		say "Wrote $(rp "$CONFIG")"
		CONFIG_WRITTEN=1
	else
		note "keeping existing $(rp "$CONFIG")"
	fi

	sed "s|^ExecStart=.*|ExecStart=$BIN serve --config $CONFIG|; s|^ExecStartPre=.*stackstead.* config validate.*|ExecStartPre=$BIN config validate --config $CONFIG|" \
		"$STAGE/stackstead.service" >"$STAGE/unit.rewritten"
	put_file "$STAGE/unit.rewritten" "$(rp "$UNIT")" 0644

	if [ -f "$STAGE/install.sh" ]; then
		put_file "$STAGE/install.sh" "$(rp "$HELPER")" 0755
	else
		warn "the release has no install.sh; --uninstall will need a download"
	fi
	put_file "$STAGE/stackstead" "$(rp "$BIN")" 0755
	say "Installed $(rp "$BIN")"
}

validate_config() {
	[ -n "${CONFIG_WRITTEN:-}" ] && [ -z "$ROOT" ] || return 0
	if ! bounded "$BIN" config validate --config "$CONFIG" >/dev/null 2>&1; then
		warn "config validate did not pass for $CONFIG; the service may refuse to start (older binaries lack this command)"
	fi
	return 0
}

systemd_booted() { [ -d "$(rp /run/systemd/system)" ]; }

STARTED=

configure_service() {
	if ! have systemctl; then
		warn "no systemd on this host: start it under your own supervisor: $BIN serve --config $CONFIG"
		return 0
	fi
	if ! systemd_booted; then
		warn "systemd is installed but not booted (container or chroot): enabling the unit only, nothing was started"
		svc systemctl enable "$SERVICE_NAME"
		return 0
	fi
	was_active=
	if [ -n "${UPGRADE:-}" ]; then
		if [ -n "$ROOT" ] || systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
			was_active=1
		fi
	fi
	svc systemctl daemon-reload
	if [ -n "$NO_START" ]; then
		svc systemctl enable "$SERVICE_NAME"
		return 0
	fi
	if [ -n "${UPGRADE:-}" ]; then
		svc systemctl enable "$SERVICE_NAME"
		if [ -n "$was_active" ]; then
			svc systemctl restart "$SERVICE_NAME"
			STARTED=1
		fi
	else
		svc systemctl enable --now "$SERVICE_NAME"
		STARTED=1
	fi
}

# config_value KEY DEFAULT
config_value() {
	v=$(sed -n "s/^$1:[[:space:]]*//p" "$(rp "$CONFIG")" 2>/dev/null | head -n 1 | sed "s/[[:space:]]*#.*\$//; s/^[\"']//; s/[\"']\$//") || v=
	printf '%s' "${v:-$2}"
}

wait_ready() {
	[ -n "$STARTED" ] && [ -z "$ROOT" ] || return 0
	listen=$(config_value listen "$DEFAULT_LISTEN")
	case $listen in
	:* | 0.0.0.0:* | \[::\]:*) listen=127.0.0.1:${listen##*:} ;;
	esac
	say "Waiting for the service on http://$listen"
	i=0
	while [ "$i" -lt 15 ]; do
		if http_ok "http://$listen/api/health" || http_ok "http://$listen/api/setup/status"; then
			if have runuser && [ -x "$BIN" ]; then
				note "stackstead health (summary only):"
				runuser -u "$SERVICE_USER" -- "$BIN" health --config "$CONFIG" 2>&1 | sed 's/^/      /' || true
			fi
			say "Stackstead is running"
			note "Open http://$listen/ from this machine to create the admin account"
			note "Remote? use the setup token: journalctl -u $SERVICE_NAME | grep setup_token"
			return 0
		fi
		i=$((i + 1))
		sleep 1
	done
	err "the service did not answer on http://$listen within 15 s"
	note "systemctl status $SERVICE_NAME"
	note "journalctl -u $SERVICE_NAME -n 50"
	DIED=1
	exit 7
}

docker_check() {
	[ -z "$ROOT" ] || return 0
	if ! have docker; then
		warn "Docker is not installed (see $DOCKER_URL); Stackstead starts degraded until it is"
		return 0
	fi
	docker compose version >/dev/null 2>&1 || warn "the docker compose plugin is missing (see $DOCKER_URL)"
	sock=$(config_value docker_socket /var/run/docker.sock)
	[ -S "$sock" ] || warn "Docker socket $sock not found; is the Docker daemon running?"
}

do_install() {
	PHASE=install
	say "Installing"
	if [ -z "$ROOT" ] || [ -z "$UPGRADE" ]; then
		# when staging an upgrade the host's accounts say nothing about the target
		ensure_account
	fi
	install_files
	validate_config
	configure_service
	PHASE=
	wait_ready
	docker_check
}

# ---------------------------------------------------------------- uninstall

do_uninstall() {
	PHASE=install
	if [ -e "$(rp "$UNIT")" ] && have systemctl; then
		if systemd_booted; then
			svc systemctl disable --now "$SERVICE_NAME" || warn "could not stop $SERVICE_NAME"
		else
			svc systemctl disable "$SERVICE_NAME" || warn "could not disable $SERVICE_NAME"
		fi
	fi
	rm -f "$(rp "$UNIT")" "$(rp "$BIN")" "$(rp "$BIN").new"
	rm -rf "$(rp "$HELPER_DIR")"
	if [ -e "$(rp "$UNIT")" ] || ! have systemctl || ! systemd_booted; then
		:
	else
		svc systemctl daemon-reload
	fi
	say "Removed $(rp "$BIN"), $(rp "$UNIT") and $(rp "$HELPER_DIR")"
	if [ -n "$PURGE" ]; then
		rm -rf "$(rp "$CONFIG_DIR")" "$(rp "$DATA_DIR")"
		if user_exists || [ -n "$ROOT" ]; then
			if have userdel; then
				acct userdel "$SERVICE_USER"
			elif have deluser; then
				acct deluser "$SERVICE_USER"
			else
				warn "cannot remove the $SERVICE_USER user: no userdel or deluser"
			fi
		fi
		say "Purged $(rp "$CONFIG_DIR"), $(rp "$DATA_DIR") and the $SERVICE_USER user"
	else
		note "kept $(rp "$CONFIG_DIR"), $(rp "$DATA_DIR") and the $SERVICE_USER user (use --purge to remove them)"
	fi
	note "kept $(rp "$STACKS_DIR") and everything in Docker"
	PHASE=
}

# ---------------------------------------------------------------- main

UPGRADE=
CONFIG_WRITTEN=

main() {
	detect_platform
	require_root
	refuse_package_install

	if [ -n "$UNINSTALL" ]; then
		if [ -n "$DRY_RUN" ]; then
			print_uninstall_plan
			exit 0
		fi
		do_uninstall
		exit 0
	fi

	check_tools
	resolve_and_download
	check_installed
	download_and_verify
	if [ -n "$DRY_RUN" ]; then
		print_plan
		exit 0
	fi
	do_install
}

main
