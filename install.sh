#!/bin/sh
# Stackstead installer (Linux with systemd, macOS with launchd).
#
#   Linux: curl -fsSL https://github.com/rcss-labs/stackstead/releases/latest/download/install.sh | sudo sh
#   macOS: curl -fsSL https://github.com/rcss-labs/stackstead/releases/latest/download/install.sh | sh
#
# Downloads a release tarball, verifies it against SHA256SUMS (always), installs
# the binary and starts the service: on Linux a systemd unit run by a dedicated
# user, on macOS a per-user LaunchAgent (sudo is only used for /usr/local).
# Re-running upgrades in place; config, data and stacks are never touched.
set -eu

# Root on macOS must not run anything from a user-writable directory: `sudo sh` passes the
# invoking user's PATH on, and Homebrew's bin directories (user-owned, ahead of /usr/bin) would
# supply sed, tar, awk, shasum... to a root process. So the very first thing is a system-only
# PATH, found with absolute-path id and uname. Linux is deliberately left alone: sudo applies
# secure_path there, and a root login shell with a writable PATH is the administrator's own
# business. safe_path (below) stays as a second layer for the per-user directories.
if [ "$(/usr/bin/id -u 2>/dev/null)" = 0 ] && [ "$(/usr/bin/uname -s 2>/dev/null)" = Darwin ]; then
	PATH=/usr/bin:/bin:/usr/sbin:/sbin
	export PATH
fi

DEFAULT_BASE_URL=https://github.com/rcss-labs/stackstead
SERVICE_USER=stackstead
SERVICE_NAME=stackstead
DEFAULT_LABEL=io.github.rcss-labs.stackstead
DOCKER_URL=https://docs.docker.com/engine/install/

usage() {
	cat <<'EOF'
Usage: install.sh [options]

Install or upgrade Stackstead from a verified GitHub release.

Options:
  --version vX.Y.Z    pin a release (default: latest); env STACKSTEAD_VERSION
  --prefix DIR        install under DIR (default /usr/local): DIR/bin, DIR/share/stackstead
  --no-start          install and enable the service but do not start or restart it
  --user NAME         macOS: the account to install the LaunchAgent for (default: $SUDO_USER,
                      or the user running the script)
  --signers FILE      also verify SHA256SUMS.sig with ssh-keygen -Y against FILE
                      (off by default; no key is shipped yet); env STACKSTEAD_INSTALL_SIGNERS_FILE
  --force             reinstall the same version or an older one
  --base-url URL      release repository URL (default https://github.com/rcss-labs/stackstead);
                      env STACKSTEAD_BASE_URL
  --from-pkg          macOS: finish a .pkg installation (per-user setup only; the .pkg runs it)
  --root DIR          stage into DIR (DESTDIR style); no root needed; service and account
                      commands are printed, not run
  --yes               macOS: confirm --purge without asking (it removes your stacks)
  --dry-run           download and verify, print the plan, change nothing
  --uninstall         remove the binary, unit (macOS: LaunchAgent) and helper (keeps config,
                      data, stacks, user)
  --purge             with --uninstall: also remove /etc/stackstead, /var/lib/stackstead and
                      the stackstead user (never /opt/stacks or anything in Docker); on macOS
                      ~/Library/Application Support/Stackstead (stacks included, needs --yes)
                      and ~/Library/Logs/Stackstead
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
  8  refused (not root, a package-managed install is present, or on macOS root without
     SUDO_USER or --user)
  130  interrupted (INT, TERM or HUP)
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

VERSION_ARG=
BASE_URL=
SIGNERS=
PREFIX=/usr/local
ROOT=
NO_START=
FORCE=
DRY_RUN=
UNINSTALL=
PURGE=
PRINT_PATHS=
USER_ARG=
YES=
FROM_PKG=
PAYLOAD_DIR=
LABEL=$DEFAULT_LABEL

need_value() { # option remaining-arg-count
	[ "$2" -ge 2 ] || usage_error "$1 needs a value"
}

parse_args() {
	VERSION_ARG=${STACKSTEAD_VERSION:-}
	BASE_URL=${STACKSTEAD_BASE_URL:-$DEFAULT_BASE_URL}
	SIGNERS=${STACKSTEAD_INSTALL_SIGNERS_FILE:-}
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
		--user) need_value "$1" $#; USER_ARG=$2; shift 2 ;;
		--user=*) USER_ARG=${1#*=}; shift ;;
		--from-pkg) FROM_PKG=1; shift ;;
		--payload-dir) need_value "$1" $#; PAYLOAD_DIR=$2; shift 2 ;;
		--label) need_value "$1" $#; LABEL=$2; shift 2 ;;
		--label=*) LABEL=${1#*=}; shift ;;
		--yes | -y) YES=1; shift ;;
		-h | --help) usage; exit 0 ;;
		*) usage_error "unknown option: $1" ;;
		esac
	done
}

# ---------------------------------------------------------------- paths

BIN_DIR=
BIN=
HELPER_DIR=
HELPER=
UNIT=/etc/systemd/system/$SERVICE_NAME.service
CONFIG_DIR=/etc/stackstead
CONFIG=$CONFIG_DIR/config.yaml
DATA_DIR=/var/lib/stackstead
STACKS_DIR=/opt/stacks
DEFAULT_LISTEN=127.0.0.1:8420

# rp PATH: where an absolute PATH lands (prefixed with --root when staging).
rp() { printf '%s%s' "$ROOT" "$1"; }

setup_paths() {
	[ -z "$PURGE" ] || [ -n "$UNINSTALL" ] || usage_error "--purge only makes sense with --uninstall"
	case $PREFIX in
	/*) ;;
	*) usage_error "--prefix must be an absolute path: $PREFIX" ;;
	esac
	case $PREFIX in
	*[!A-Za-z0-9/._+-]*) usage_error "--prefix may only contain letters, digits and / . _ + -: $PREFIX" ;;
	esac
	PREFIX=${PREFIX%/}
	if [ -n "$FROM_PKG" ]; then
		[ -z "$UNINSTALL" ] || usage_error "--from-pkg cannot be combined with --uninstall"
		[ -z "$VERSION_ARG" ] || usage_error "--from-pkg installs the files the package already placed; --version does not apply"
	fi
	if [ -n "$PAYLOAD_DIR" ]; then
		[ -n "$FROM_PKG" ] || usage_error "--payload-dir only makes sense with --from-pkg"
		case $PAYLOAD_DIR in
		/*) ;;
		*) usage_error "--payload-dir must be an absolute path: $PAYLOAD_DIR" ;;
		esac
	fi
	case $LABEL in
	"" | *[!A-Za-z0-9._-]*) usage_error "--label may only contain letters, digits and . _ -: $LABEL" ;;
	esac
	case $ROOT in
	"" | /*) ;;
	*) usage_error "--root must be an absolute path: $ROOT" ;;
	esac
	case $ROOT in
	*[!/]*) ;;
	"") ;;
	*) usage_error "--root / is the real system; omit --root to install there" ;;
	esac
	ROOT=${ROOT%/}
	BASE_URL=${BASE_URL%/}
	BIN_DIR=$PREFIX/bin
	BIN=$BIN_DIR/stackstead
	HELPER_DIR=$PREFIX/share/stackstead
	HELPER=$HELPER_DIR/install.sh
}

print_paths() {
	if [ "$OS" = darwin ]; then
		printf 'PREFIX=%s\nBIN=%s\nHELPER=%s\nPLIST=%s\nCONFIG=%s\nDATA_DIR=%s\nSTACKS_DIR=%s\nLOG=%s\nUSER=%s\nROOT=%s\n' \
			"$(rp "$PREFIX")" "$(rp "$BIN")" "$(rp "$HELPER")" "$(rp "$PLIST")" "$(rp "$CONFIG")" \
			"$(rp "$DATA_DIR")" "$(rp "$STACKS_DIR")" "$(rp "$LOG")" "$TARGET_USER" "$ROOT"
		return 0
	fi
	printf 'PREFIX=%s\nBIN=%s\nHELPER=%s\nUNIT=%s\nCONFIG=%s\nDATA_DIR=%s\nSTACKS_DIR=%s\nSERVICE_USER=%s\nROOT=%s\n' \
		"$(rp "$PREFIX")" "$(rp "$BIN")" "$(rp "$HELPER")" "$(rp "$UNIT")" "$(rp "$CONFIG")" \
		"$(rp "$DATA_DIR")" "$(rp "$STACKS_DIR")" "$SERVICE_USER" "$ROOT"
}

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
		case $1 in
		https://*) curl -fsSL --proto '=https' --proto-redir '=https' --retry 2 --connect-timeout 15 --max-time 300 -o "$2" "$1" ;;
		*) curl -fsSL --retry 2 --connect-timeout 15 --max-time 300 -o "$2" "$1" ;;
		esac
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

# check_base_url: warn about a non-default source; plain http only for loopback.
check_base_url() {
	case $BASE_URL in
	*@*) usage_error "--base-url must not contain @ (a userinfo part can disguise the real host): $BASE_URL" ;;
	esac
	case $BASE_URL in
	https://*) ;;
	http://127.0.0.1 | http://127.0.0.1[:/]* | http://localhost | http://localhost[:/]* | http://\[::1\] | http://\[::1\][:/]*) ;;
	*) usage_error "--base-url must be https (plain http is only accepted for 127.0.0.1, localhost and [::1]): $BASE_URL" ;;
	esac
	if [ "$BASE_URL" != "$DEFAULT_BASE_URL" ]; then
		warn "using a non-default release source: $BASE_URL"
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
	Darwin) OS=darwin ;;
	*) die 3 "unsupported operating system: $(uname -s)" ;;
	esac
	case $(uname -m) in
	x86_64 | amd64) ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	*) die 3 "unsupported architecture: $(uname -m)" ;;
	esac
}

require_root() {
	[ "$OS" = linux ] || return 0
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

# valid_version V: vX.Y.Z with an optional suffix, no whitespace or newlines.
valid_version() {
	vv=${1#v}
	case $vv in
	"" | *[!0-9A-Za-z.+~-]*) return 1 ;;
	esac
	vcore=${vv%%[-+~]*}
	case $vcore in
	*[!0-9.]* | .* | *. | *..*) return 1 ;;
	*.*.*.*) return 1 ;;
	[0-9]*.[0-9]*.[0-9]*) return 0 ;;
	esac
	return 1
}

# ver_cmp A B: print -1, 0 or 1. Semver precedence, except that a build made
# after a tag (N-g<sha>) is newer than the tag itself.
ver_cmp() {
	awk -v a="$(plain_version "$1")" -v b="$(plain_version "$2")" '
	function parse(s, o,    i, core, pre, n, parts) {
		sub(/\+.*/, "", s)
		i = index(s, "-")
		if (i) { core = substr(s, 1, i - 1); pre = substr(s, i + 1) } else { core = s; pre = "" }
		split(core, parts, ".")
		for (i = 1; i <= 3; i++) o[i] = parts[i] + 0
		o["post"] = 0
		if (pre ~ /^[0-9]+-g[0-9a-f]+/) { o["post"] = 1; pre = substr(pre, 1, index(pre, "-") - 1) + 0 }
		o["pre"] = pre
	}
	function idcmp(x, y) {
		if (x ~ /^[0-9]+$/ && y ~ /^[0-9]+$/) return (x + 0 < y + 0) ? -1 : ((x + 0 > y + 0) ? 1 : 0)
		if (x ~ /^[0-9]+$/) return -1
		if (y ~ /^[0-9]+$/) return 1
		return (x < y) ? -1 : ((x > y) ? 1 : 0)
	}
	function precmp(x, y,    nx, ny, xs, ys, i, c) {
		nx = split(x, xs, "."); ny = split(y, ys, ".")
		for (i = 1; i <= nx && i <= ny; i++) { c = idcmp(xs[i], ys[i]); if (c) return c }
		return (nx < ny) ? -1 : ((nx > ny) ? 1 : 0)
	}
	BEGIN {
		parse(a, A); parse(b, B)
		for (i = 1; i <= 3; i++) {
			if (A[i] < B[i]) { print -1; exit }
			if (A[i] > B[i]) { print 1; exit }
		}
		# rank: pre-release < release < build after the tag
		ra = A["post"] ? 2 : (A["pre"] == "" ? 1 : 0)
		rb = B["post"] ? 2 : (B["pre"] == "" ? 1 : 0)
		if (ra != rb) { print (ra < rb) ? -1 : 1; exit }
		if (ra == 1) { print 0; exit }
		if (ra == 2) { print (A["pre"] < B["pre"]) ? -1 : ((A["pre"] > B["pre"]) ? 1 : 0); exit }
		print precmp(A["pre"], B["pre"])
	}'
}

# limited SECONDS OUTFILE CMD...: run CMD with no stdin, output to OUTFILE and a
# time limit; timeout(1) when present, otherwise poll and kill. Returns CMD's
# status (non-zero when it had to be killed).
limited() {
	lim_secs=$1
	lim_out=$2
	shift 2
	if have timeout; then
		timeout "$lim_secs" "$@" </dev/null >"$lim_out" 2>&1
		return
	fi
	"$@" </dev/null >"$lim_out" 2>&1 &
	lim_pid=$!
	lim_i=0
	while kill -0 "$lim_pid" 2>/dev/null; do
		if [ "$lim_i" -ge "$lim_secs" ]; then
			kill "$lim_pid" 2>/dev/null || true
			sleep 1
			kill -9 "$lim_pid" 2>/dev/null || true
			wait "$lim_pid" 2>/dev/null || true
			return 124
		fi
		lim_i=$((lim_i + 1))
		sleep 1
	done
	lim_rc=0
	wait "$lim_pid" || lim_rc=$?
	return "$lim_rc"
}

# installed_version: print the installed version, or nothing when unknown.
# Older binaries ignore unknown arguments and start the server, so nothing but
# --help (which never serves) is run until the help text shows a `version`
# command; then `version --short`, then `version -o json`.
# A macOS root run probes as the target user: the prefix binary may be
# user-writable (Intel Homebrew owns /usr/local), so root never executes it.
installed_version() {
	bin=$(rp "$BIN")
	[ -x "$bin" ] || return 0
	probe=$TMP/probe.out
	limited_as_user 5 "$probe" "$bin" --help || true
	grep -qw version "$probe" 2>/dev/null || return 0
	limited_as_user 5 "$probe" "$bin" version --short || true
	v=$(head -n 1 "$probe")
	if valid_version "$v"; then
		printf '%s' "$v"
		return 0
	fi
	limited_as_user 5 "$probe" "$bin" version -o json || true
	v=$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$probe" | head -n 1)
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
			{ n = $2; sub(/^\*/, "", n) }
			index(n, "stackstead_") == 1 && length(n) > length(suffix) &&
			substr(n, length(n) - length(suffix) + 1) == suffix { print n; exit }' "$TMP/latest-sums")
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
	if [ -x "$(rp "$BIN")" ]; then
		UPGRADE=1
	fi
	if [ -z "$cur" ]; then
		if [ -x "$(rp "$BIN")" ]; then
			note "could not determine the installed version; reinstalling"
		fi
		return 0
	fi
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
	want=$(awk -v n="$ASSET" '{ f = $2; sub(/^\*/, "", f) } f == n { print $1; exit }' "$TMP/SHA256SUMS")
	[ -n "$want" ] || die 6 "$ASSET is not listed in SHA256SUMS"
	fetch "$RELEASE_URL/$ASSET" "$TMP/$ASSET"
	got=$(sha256_of "$TMP/$ASSET")
	if [ "$got" != "$want" ]; then
		die 6 "checksum mismatch for $ASSET (expected $want, got $got); nothing was installed"
	fi
	note "checksum OK"
	# Only regular files, only known names, nothing that could escape the stage dir.
	tar -tvzf "$TMP/$ASSET" >"$TMP/tar.long" 2>/dev/null || die 6 "cannot read $ASSET"
	tar -tzf "$TMP/$ASSET" >"$TMP/tar.names" 2>/dev/null || die 6 "cannot read $ASSET"
	if awk '{ c = substr($1, 1, 1); if (c != "-" && c != "d") bad = 1 } END { exit !bad }' "$TMP/tar.long"; then
		die 6 "$ASSET contains links or special files; nothing was installed"
	fi
	if grep -Eq '(^/|(^|/)\.\.(/|$))' "$TMP/tar.names"; then
		die 6 "$ASSET contains unsafe paths; nothing was installed"
	fi
	members=
	for f in stackstead stackstead.service launchagent.plist.in config.example.yaml install.sh LICENSE; do
		if grep -Fxq "$f" "$TMP/tar.names"; then
			members="$members $f"
		elif grep -Fxq "./$f" "$TMP/tar.names"; then
			members="$members ./$f"
		fi
	done
	noown=
	# bsdtar accepts --no-same-owner without listing it in --help
	if tar --help 2>&1 | grep -q -- --no-same-owner || tar --no-same-owner --version >/dev/null 2>&1; then
		noown=--no-same-owner
	fi
	# shellcheck disable=SC2086
	tar -xzf "$TMP/$ASSET" -C "$STAGE" $noown $members || die 6 "cannot unpack $ASSET"
	required="stackstead config.example.yaml"
	if [ "$OS" = darwin ]; then
		required="$required launchagent.plist.in"
	else
		required="$required stackstead.service"
	fi
	for f in $required; do
		{ [ -f "$STAGE/$f" ] && [ ! -L "$STAGE/$f" ]; } || die 6 "$ASSET does not contain $f"
	done
}

# ---------------------------------------------------------------- plan

print_plan() {
	if [ "$OS" = darwin ]; then
		print_plan_darwin
		return 0
	fi
	say "Plan (nothing is changed in dry-run mode)"
	note "create/replace $(rp "$BIN")"
	if have systemctl; then
		note "create/replace $(rp "$UNIT")"
	fi
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
	if [ "$OS" = darwin ]; then
		print_uninstall_plan_darwin
		return 0
	fi
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
		# the unit also grants the group, but the packages put the user in /etc/group
		if ! id -nG "$SERVICE_USER" 2>/dev/null | tr ' ' '\n' | grep -Fxq docker; then
			if have usermod; then
				acct usermod -aG docker "$SERVICE_USER"
			elif have addgroup; then
				acct addgroup "$SERVICE_USER" docker
			else
				warn "cannot add $SERVICE_USER to the docker group: no usermod or addgroup"
			fi
		fi
		return 0
	fi
	shell=$(nologin_shell)
	if have useradd; then
		acct useradd --system --user-group -G docker --home-dir "$DATA_DIR" --no-create-home --shell "$shell" "$SERVICE_USER"
	elif have adduser; then
		if ! group_exists "$SERVICE_USER"; then
			acct addgroup -S "$SERVICE_USER"
		fi
		acct adduser -S -D -H -h "$DATA_DIR" -s "$shell" -G "$SERVICE_USER" "$SERVICE_USER"
		acct addgroup "$SERVICE_USER" docker
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

# put_file SRC DEST MODE: copy via an unpredictable temp name in the same
# directory (mktemp creates it exclusively, so nothing planted there is
# followed), then rename.
put_file() {
	pf_dir=$(dirname "$2")
	mkdir -p "$pf_dir"
	pf_tmp=$(mktemp "$pf_dir/.stackstead.XXXXXX") || die 7 "cannot create a temporary file in $pf_dir"
	if ! cp "$1" "$pf_tmp" || ! chmod "$3" "$pf_tmp" || ! mv -f "$pf_tmp" "$2"; then
		rm -f "$pf_tmp" || true
		die 7 "cannot write $2"
	fi
}

# check_stacks_dir: an existing stacks dir is never chown'ed or chmod'ed; warn
# when neither the service user nor the docker group (nor anyone) can write.
check_stacks_dir() {
	sd_line=$(ls -ld "$(rp "$STACKS_DIR")")
	# shellcheck disable=SC2086
	set -- $sd_line
	sd_mode=$1
	sd_owner=$3
	sd_group=$4
	sd_ok=
	case $sd_mode in ????????w*) sd_ok=1 ;; esac
	case $sd_mode in
	??w*) [ "$sd_owner" != "$SERVICE_USER" ] || sd_ok=1 ;;
	esac
	case $sd_mode in
	?????w*)
		if [ "$sd_group" = docker ] || id -nG "$SERVICE_USER" 2>/dev/null | tr ' ' '\n' | grep -Fxq "$sd_group"; then
			sd_ok=1
		fi
		;;
	esac
	if [ -z "$sd_ok" ]; then
		warn "$STACKS_DIR exists but $SERVICE_USER cannot write to it; to let the service create stacks run: chgrp docker $STACKS_DIR && chmod 2775 $STACKS_DIR"
	fi
}

install_files() {
	make_dir "$CONFIG_DIR" 0755 ""
	make_dir "$DATA_DIR" 0700 "$SERVICE_USER"
	if [ -d "$(rp "$STACKS_DIR")" ]; then
		check_stacks_dir
	else
		make_dir "$STACKS_DIR" 2775 "$SERVICE_USER:docker"
	fi

	if [ ! -e "$(rp "$CONFIG")" ]; then
		put_file "$STAGE/config.example.yaml" "$(rp "$CONFIG")" 0640
		own "root:$SERVICE_USER" "$(rp "$CONFIG")"
		say "Wrote $(rp "$CONFIG")"
		CONFIG_WRITTEN=1
	else
		note "keeping existing $(rp "$CONFIG")"
	fi

	# a unit is only useful where systemd exists (not on Alpine, say)
	if have systemctl; then
		sed "s|^ExecStart=.*|ExecStart=$BIN serve --config $CONFIG|; s|^ExecStartPre=.*stackstead.* config validate.*|ExecStartPre=$BIN config validate --config $CONFIG|" \
			"$STAGE/stackstead.service" >"$STAGE/unit.rewritten"
		put_file "$STAGE/unit.rewritten" "$(rp "$UNIT")" 0644
	fi

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
	if ! limited_as_user 10 "$TMP/validate.out" "$BIN" config validate --config "$CONFIG"; then
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
		# never re-enable a service the admin disabled
		if [ -n "$was_active" ]; then
			svc systemctl restart "$SERVICE_NAME"
			STARTED=1
		fi
	else
		note_port_taken
		svc systemctl enable --now "$SERVICE_NAME"
		STARTED=1
	fi
}

# config_value KEY DEFAULT. On macOS the config lives in the user's home, so
# a root run reads it as the user (as_fs), never with its own privileges.
config_value() {
	v=$(as_fs sed -n "s/^$1:[[:space:]]*//p" "$(rp "$CONFIG")" 2>/dev/null | head -n 1 | sed "s/[[:space:]]*#.*\$//; s/^[\"']//; s/[\"']\$//") || v=
	printf '%s' "${v:-$2}"
}

# runuser_limited SECONDS OUTFILE CMD...: limited(), as the service user.
runuser_limited() {
	rl_secs=$1
	rl_out=$2
	shift 2
	limited "$rl_secs" "$rl_out" runuser -u "$SERVICE_USER" -- "$@"
}

# health_summary RUNNER: print `stackstead health` for humans; never fatal.
health_summary() {
	[ -x "$BIN" ] || return 0
	note "stackstead health (summary only):"
	hrc=0
	"$1" 10 "$TMP/health.out" "$BIN" health --config "$CONFIG" || hrc=$?
	sed 's/^/      /' "$TMP/health.out"
	note "stackstead health exit code: $hrc (informational)"
}

# listen_addr: where to probe the service (a wildcard address becomes loopback).
listen_addr() {
	la=$(config_value listen "$DEFAULT_LISTEN")
	case $la in
	:* | 0.0.0.0:* | \[::\]:*) la=127.0.0.1:${la##*:} ;;
	esac
	printf '%s' "$la"
}

# note_port_taken: call right before a fresh start. Something already answering on the listen
# address (another Stackstead, say) would make the readiness probe succeed for the wrong
# process, so remember it, warn, and stop claiming the service is running.
PORT_TAKEN=
note_port_taken() {
	[ -z "$ROOT" ] || return 0
	npt=$(listen_addr)
	if http_ok "http://$npt/api/health"; then
		PORT_TAKEN=$npt
		warn "something is already answering on $npt (another Stackstead?); the new service cannot listen there until it is stopped, or change listen: in $(rp "$CONFIG")"
	fi
}

wait_ready() {
	[ -n "$STARTED" ] && [ -z "$ROOT" ] || return 0
	listen=$(listen_addr)
	say "Waiting for the service on http://$listen"
	i=0
	while [ "$i" -lt 15 ]; do
		if http_ok "http://$listen/api/health" || http_ok "http://$listen/api/setup/status"; then
			if [ -n "$PORT_TAKEN" ]; then
				warn "the answer on $listen may come from the process that already held it, not from the new service; check it with: $(if [ "$OS" = darwin ]; then echo "launchctl print gui/$TARGET_UID/$LABEL"; else echo "systemctl status $SERVICE_NAME"; fi)"
				return 0
			fi
			if [ "$OS" = darwin ]; then
				health_summary limited_as_user
				say "Stackstead is running"
				note "Open http://$listen/ from this Mac to create the admin account"
				note "Logs: $LOG"
				note "Setup token (only needed when not on this Mac): grep setup_token $LOG"
			else
				if have runuser; then
					health_summary runuser_limited
				fi
				say "Stackstead is running"
				note "Open http://$listen/ from this machine to create the admin account"
				note "Remote? use the setup token: journalctl -u $SERVICE_NAME | grep setup_token"
			fi
			return 0
		fi
		i=$((i + 1))
		sleep 1
	done
	err "the service did not answer on http://$listen within 15 s"
	if [ "$OS" = darwin ]; then
		note "launchctl print gui/$TARGET_UID/$LABEL"
		note "tail -n 50 $LOG"
	else
		note "systemctl status $SERVICE_NAME"
		note "journalctl -u $SERVICE_NAME -n 50"
	fi
	DIED=1
	exit 7
}

docker_check() {
	[ -z "$ROOT" ] || return 0
	if [ "$OS" = darwin ]; then
		docker_check_darwin
		return 0
	fi
	if ! have docker; then
		warn "Docker is not installed (see $DOCKER_URL); Stackstead starts degraded until it is"
		return 0
	fi
	docker compose version >/dev/null 2>&1 || warn "the docker compose plugin is missing (see $DOCKER_URL)"
	sock=$(config_value docker_socket /var/run/docker.sock)
	[ -S "$sock" ] || warn "Docker socket $sock not found; is the Docker daemon running?"
}

do_install() {
	if [ "$OS" = darwin ]; then
		do_install_darwin
		return 0
	fi
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

# ---------------------------------------------------------------- macOS

AS_ROOT=
TARGET_USER=
TARGET_UID=
TARGET_HOME=
SUPPORT_DIR=
LOG_DIR=
LOG=
AGENT_DIR=
PLIST=
DOCKER_SOCKET=
PLIST_CHANGED=
USER_PATH=

# resolve_target_user: the account the LaunchAgent belongs to. Under sudo that
# is $SUDO_USER, never root; a plain root login must say who with --user.
resolve_target_user() {
	cur_uid=$(id -u) || die 1 "cannot determine the current user"
	if [ -n "$USER_ARG" ]; then
		case $USER_ARG in
		-* | *[!A-Za-z0-9._-]*) usage_error "--user: not a valid account name: $USER_ARG" ;;
		esac
	fi
	if [ "$cur_uid" = 0 ]; then
		AS_ROOT=1
		TARGET_USER=${USER_ARG:-${SUDO_USER:-}}
		case $TARGET_USER in
		"" | root) die 8 "running as root: run this as your own user (sudo is requested only for $PREFIX), or use 'sudo sh install.sh' from your account so SUDO_USER is set, or pass --user NAME" ;;
		esac
	else
		cur_user=$(id -un) || die 1 "cannot determine the current user"
		TARGET_USER=${USER_ARG:-$cur_user}
		[ "$TARGET_USER" = "$cur_user" ] || die 8 "only root can install for another user ($TARGET_USER); run as that user or with sudo"
	fi
	case $TARGET_USER in
	-* | *[!A-Za-z0-9._-]*) die 8 "not a valid account name: $TARGET_USER" ;;
	esac
	TARGET_UID=$(id -u "$TARGET_USER" 2>/dev/null) || die 8 "no such user: $TARGET_USER"
	[ "$TARGET_UID" != 0 ] || die 8 "refusing to install a LaunchAgent for uid 0 ($TARGET_USER); use a regular account"
	# parsed with the shell, not sed/head: this runs before safe_path can drop the
	# user's directories from a root run's PATH
	TARGET_HOME=
	dscl_out=$(dscl . -read "/Users/$TARGET_USER" NFSHomeDirectory 2>/dev/null) || dscl_out=
	dscl_next=
	while read -r dscl_key dscl_val; do
		if [ -n "$dscl_next" ]; then
			# the value of a home with a space sits alone on the next line
			TARGET_HOME=$dscl_key${dscl_val:+ $dscl_val}
			break
		fi
		if [ "$dscl_key" = "NFSHomeDirectory:" ]; then
			if [ -n "$dscl_val" ]; then
				TARGET_HOME=$dscl_val
				break
			fi
			dscl_next=1
		fi
	done <<EOF
$dscl_out
EOF
	if [ -z "$TARGET_HOME" ]; then
		TARGET_HOME=$(eval "echo ~$TARGET_USER" 2>/dev/null) || TARGET_HOME=
	fi
	case $TARGET_HOME in
	/?*) ;;
	*) die 7 "cannot determine the home directory of $TARGET_USER" ;;
	esac
	TARGET_HOME=${TARGET_HOME%/}
}

darwin_paths() {
	SUPPORT_DIR="$TARGET_HOME/Library/Application Support/Stackstead"
	CONFIG_DIR=$SUPPORT_DIR
	CONFIG=$SUPPORT_DIR/config.yaml
	DATA_DIR=$SUPPORT_DIR/data
	STACKS_DIR=$SUPPORT_DIR/stacks
	LOG_DIR=$TARGET_HOME/Library/Logs/Stackstead
	LOG=$LOG_DIR/stackstead.log
	AGENT_DIR=$TARGET_HOME/Library/LaunchAgents
	PLIST=$AGENT_DIR/$LABEL.plist
	# Root's own PATH must never gain user-writable directories: only the
	# commands run as the user get Docker's usual locations.
	if [ -n "$AS_ROOT" ]; then
		safe_path
	fi
	USER_PATH=$PATH:/usr/local/bin:/opt/homebrew/bin:$TARGET_HOME/.docker/bin
}

# safe_path: drop empty, relative and home-directory entries from PATH. Only a
# root run needs it. The first lines of the script already replace PATH with
# the system directories for root on macOS; this is the second layer, kept for
# --user runs and anything that re-extends PATH, and it needs TARGET_HOME so it
# runs after the home lookup (which therefore avoids PATH tools beyond id and
# dscl). When the invoker is the target user nothing runs as anyone else, so
# their own PATH is theirs to control and is left alone.
safe_path() {
	sp_out=
	sp_rest=$PATH:
	while [ -n "$sp_rest" ]; do
		sp_e=${sp_rest%%:*}
		sp_rest=${sp_rest#*:}
		case $sp_e in
		"$TARGET_HOME" | "$TARGET_HOME"/*) continue ;;
		/*) sp_out=${sp_out:+$sp_out:}$sp_e ;;
		esac
	done
	PATH=$sp_out
}

platform_setup() {
	[ "$OS" = darwin ] || return 0
	resolve_target_user
	darwin_paths
}

# priv CMD...: a write under the prefix. Root and staged runs write directly;
# otherwise sudo is used, and only for these commands. A staged run's note
# goes to stderr: priv's stdout may be captured (priv mktemp).
priv() {
	if [ -n "$ROOT" ]; then
		[ -n "$AS_ROOT" ] || note "would use sudo: $*" >&2
		"$@"
	elif [ -n "$AS_ROOT" ]; then
		"$@"
	else
		sudo "$@"
	fi
}

# priv_svc CMD...: like svc, but privileged (printed under --root).
priv_svc() {
	if [ -n "$ROOT" ]; then
		if [ -n "$AS_ROOT" ]; then
			say "would run: $*"
		else
			say "would run: sudo $*"
		fi
	else
		priv "$@"
	fi
}

# priv_put_file SRC DEST MODE: put_file under priv.
priv_put_file() {
	ppf_dir=$(dirname "$2")
	priv mkdir -p "$ppf_dir"
	ppf_tmp=$(priv mktemp "$ppf_dir/.stackstead.XXXXXX") || die 7 "cannot create a temporary file in $ppf_dir"
	if ! priv cp "$1" "$ppf_tmp" || ! priv chmod "$3" "$ppf_tmp" || ! priv mv -f "$ppf_tmp" "$2"; then
		priv rm -f "$ppf_tmp" || true
		die 7 "cannot write $2"
	fi
}

# as_user CMD...: run as the target user (a sudo run is root); a shell function
# so it is only for commands that need no time limit.
as_user() {
	if [ -n "$AS_ROOT" ] && [ -z "$ROOT" ]; then
		sudo -H -u "$TARGET_USER" env "PATH=$USER_PATH" "$@"
	else
		env "PATH=$USER_PATH" "$@"
	fi
}

# as_fs CMD...: a file operation inside the user's home. Root never touches
# those files itself (the user may have planted symlinks there): it runs the
# operation as the user, so everything created is theirs and nothing can be
# redirected with root's privileges.
as_fs() {
	if [ -n "$AS_ROOT" ] && [ -z "$ROOT" ]; then
		sudo -H -u "$TARGET_USER" "$@"
	else
		"$@"
	fi
}

# agent_cmd CMD...: a launchctl call in the user's session (printed under --root).
agent_cmd() {
	if [ -n "$AS_ROOT" ]; then
		svc sudo -H -u "$TARGET_USER" "$@"
	else
		svc "$@"
	fi
}

# limited_as_user SECONDS OUTFILE CMD...: limited(), as the target user (like
# as_user, a staged --root run acts directly on its own tree).
limited_as_user() {
	lau_secs=$1
	lau_out=$2
	shift 2
	if [ "$OS" = darwin ] && [ -n "$AS_ROOT" ] && [ -z "$ROOT" ]; then
		limited "$lau_secs" "$lau_out" sudo -H -u "$TARGET_USER" env "PATH=$USER_PATH" "$@"
	else
		limited "$lau_secs" "$lau_out" "$@"
	fi
}

# user_make_dir PATH MODE: create (as the user) with mode only when missing.
user_make_dir() {
	d=$(rp "$1")
	[ ! -d "$d" ] || return 0
	as_fs mkdir -p "$d" || die 7 "cannot create $d"
	as_fs chmod "$2" "$d" || die 7 "cannot set the mode of $d"
}

# user_put_file SRC DEST MODE: write DEST as the user through an unpredictable
# temp name in its directory, then rename over it. SRC is read by us and
# handed over on stdin (the staging dir is not readable by the user).
user_put_file() {
	up_dir=$(dirname "$2")
	as_fs mkdir -p "$up_dir" || die 7 "cannot create $up_dir"
	up_tmp=$(as_fs mktemp "$up_dir/.stackstead.XXXXXX") || die 7 "cannot create a temporary file in $up_dir"
	if ! as_fs sh -c "cat >\"\$1\"" sh "$up_tmp" <"$1" || ! as_fs chmod "$3" "$up_tmp" || ! as_fs mv -f "$up_tmp" "$2"; then
		as_fs rm -f "$up_tmp" || true
		die 7 "cannot write $2"
	fi
}

# yaml_scalar VALUE: plain when that is safe, otherwise single-quoted.
yaml_scalar() {
	case $1 in
	*[!\ A-Za-z0-9/._+@~=,-]*) printf "'%s'" "$(printf '%s' "$1" | sed "s/'/''/g")" ;;
	*) printf '%s' "$1" ;;
	esac
}

xml_escape() { printf '%s' "$1" | sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g'; }

# detect_docker_socket: ask the target user's Docker CLI which socket it uses.
detect_docker_socket() {
	ds_ep=$(as_user docker context inspect --format '{{(index .Endpoints "docker").Host}}' 2>/dev/null | head -n 1) || ds_ep=
	DOCKER_SOCKET=
	case $ds_ep in
	unix://?*) DOCKER_SOCKET=${ds_ep#unix://} ;;
	esac
	if [ -z "$DOCKER_SOCKET" ]; then
		DOCKER_SOCKET=/var/run/docker.sock
		warn "could not detect a local Docker socket (docker missing, or its context is not a unix socket); using $DOCKER_SOCKET: set docker_socket in $CONFIG if Docker listens elsewhere"
	fi
}

generate_config() { # DEST
	detect_docker_socket
	SS_STACKS=$(yaml_scalar "$STACKS_DIR") SS_DATA=$(yaml_scalar "$DATA_DIR") SS_SOCK=$(yaml_scalar "$DOCKER_SOCKET") awk '
		/^stacks_dir:/ { print "stacks_dir: " ENVIRON["SS_STACKS"]; next }
		/^data_dir:/ { print "data_dir: " ENVIRON["SS_DATA"]; next }
		/^# docker_socket:/ { print "docker_socket: " ENVIRON["SS_SOCK"]; seen = 1; next }
		{ print }
		END { if (!seen) print "docker_socket: " ENVIRON["SS_SOCK"] }' "$STAGE/config.example.yaml" >"$1"
}

# render_plist DEST: the template with absolute paths (launchd expands neither
# a tilde nor variables). Values are XML-escaped and substituted literally.
render_plist() {
	PL_LABEL=$(xml_escape "$LABEL") PL_BIN=$(xml_escape "$BIN") PL_CONFIG=$(xml_escape "$CONFIG") \
		PL_LOG=$(xml_escape "$LOG") PL_HOME=$(xml_escape "$TARGET_HOME") awk '
		function subst(s,    out, i, j, tok) {
			out = ""
			while ((i = index(s, "@")) > 0) {
				out = out substr(s, 1, i - 1)
				s = substr(s, i)
				j = index(substr(s, 2), "@")
				if (j > 0) {
					tok = substr(s, 1, j + 1)
					if (tok in V) { out = out V[tok]; s = substr(s, j + 2); continue }
				}
				out = out "@"
				s = substr(s, 2)
			}
			return out s
		}
		BEGIN {
			V["@LABEL@"] = ENVIRON["PL_LABEL"]; V["@BIN@"] = ENVIRON["PL_BIN"]; V["@CONFIG@"] = ENVIRON["PL_CONFIG"]
			V["@LOG@"] = ENVIRON["PL_LOG"]; V["@HOME@"] = ENVIRON["PL_HOME"]
		}
		{ print subst($0) }' "$STAGE/launchagent.plist.in" >"$1"
}

install_files_darwin() {
	user_make_dir "$SUPPORT_DIR" 0755
	user_make_dir "$DATA_DIR" 0700
	user_make_dir "$STACKS_DIR" 0755
	user_make_dir "$LOG_DIR" 0755
	user_make_dir "$AGENT_DIR" 0755

	if ! as_fs test -e "$(rp "$CONFIG")"; then
		generate_config "$STAGE/config.generated"
		user_put_file "$STAGE/config.generated" "$(rp "$CONFIG")" 0600
		say "Wrote $(rp "$CONFIG")"
		CONFIG_WRITTEN=1
	else
		note "keeping existing $(rp "$CONFIG")"
	fi

	render_plist "$STAGE/plist.rendered"
	PLIST_CHANGED=1
	# the installed plist is read as the user; the rendered one arrives on stdin
	if as_fs test -f "$(rp "$PLIST")" && have cmp && as_fs cmp -s - "$(rp "$PLIST")" <"$STAGE/plist.rendered"; then
		PLIST_CHANGED=
	fi
	user_put_file "$STAGE/plist.rendered" "$(rp "$PLIST")" 0644

	if [ -n "$FROM_PKG" ]; then
		# the package payload already holds the binary and the helper files
		say "Using $(rp "$BIN") from the package"
		return 0
	fi
	priv_put_file "$STAGE/launchagent.plist.in" "$(rp "$HELPER_DIR")/launchagent.plist.in" 0644
	if [ -f "$STAGE/install.sh" ]; then
		priv_put_file "$STAGE/install.sh" "$(rp "$HELPER")" 0755
	else
		warn "the release has no install.sh; --uninstall will need a download"
	fi
	if [ -f "$STAGE/LICENSE" ]; then
		priv_put_file "$STAGE/LICENSE" "$(rp "$HELPER_DIR")/LICENSE" 0644
	fi
	priv_put_file "$STAGE/stackstead" "$(rp "$BIN")" 0755
	say "Installed $(rp "$BIN")"
}

# stage_from_pkg: --from-pkg (used by the .pkg postinstall; shown in --help only as
# "finish a .pkg installation"). No download: the binary is already under the prefix.
# The two templates come from one of two places:
#  - --payload-dir DIR: the pkg's own scripts directory, which installer extracts into
#    a root-only temp dir; root reads them itself (regular files only, no symlinks).
#  - otherwise (an admin re-running it from /usr/local/share/stackstead): that
#    directory may be controlled by a non-root user (Homebrew on Intel Macs), so the
#    files are read AS the target user, never by root: a swapped directory or a hard
#    link to a root-only file yields nothing root-specific.
# The per-user steps then run exactly as in the normal flow. An existing plist marks an
# upgrade (the binary is the new one already, so its version says nothing).
stage_from_pkg() {
	TMP=$(mktemp -d "${TMPDIR:-/tmp}/stackstead-install.XXXXXX") || die 1 "cannot create a temporary directory"
	STAGE=$TMP/stage
	mkdir -p "$STAGE"
	{ [ -f "$(rp "$BIN")" ] && [ -x "$(rp "$BIN")" ] && [ ! -L "$(rp "$BIN")" ]; } || die 7 "the package payload is incomplete: no executable $(rp "$BIN")"
	for sf_f in launchagent.plist.in config.example.yaml; do
		if [ -n "$PAYLOAD_DIR" ]; then
			{ [ -f "$PAYLOAD_DIR/$sf_f" ] && [ ! -L "$PAYLOAD_DIR/$sf_f" ]; } || die 7 "the package scripts are incomplete or unsafe: $PAYLOAD_DIR/$sf_f"
			cp "$PAYLOAD_DIR/$sf_f" "$STAGE/$sf_f" || die 7 "cannot read $PAYLOAD_DIR/$sf_f"
		else
			sf_src=$(rp "$HELPER_DIR")/$sf_f
			as_fs cat "$sf_src" >"$STAGE/$sf_f" || die 7 "cannot read $sf_src as $TARGET_USER"
			[ -s "$STAGE/$sf_f" ] || die 7 "$sf_src is empty"
		fi
	done
	if as_fs test -e "$(rp "$PLIST")"; then
		UPGRADE=1
	fi
}

agent_loaded() {
	[ -z "$ROOT" ] || return 0
	agent_cmd launchctl print "gui/$TARGET_UID/$LABEL" >/dev/null 2>&1
}

configure_service_darwin() {
	gui=gui/$TARGET_UID
	[ -z "$NO_START" ] || return 0
	if [ -n "${UPGRADE:-}" ]; then
		# never start an agent that is not loaded: the user may have removed it
		if ! agent_loaded; then
			note "the agent is not loaded; not starting it (launchctl bootstrap $gui $PLIST)"
			return 0
		fi
		if [ -n "$PLIST_CHANGED" ]; then
			agent_cmd launchctl bootout "$gui/$LABEL" || true
			agent_cmd launchctl bootstrap "$gui" "$PLIST" || die 7 "launchctl bootstrap failed"
		else
			agent_cmd launchctl kickstart -k "$gui/$LABEL" || die 7 "launchctl kickstart failed"
		fi
		STARTED=1
		return 0
	fi
	agent_cmd launchctl bootout "$gui/$LABEL" || true
	note_port_taken
	if agent_cmd launchctl bootstrap "$gui" "$PLIST"; then
		STARTED=1
	else
		warn "launchctl could not load the agent (no GUI session for $TARGET_USER, for example over ssh); it starts at next login, or run: launchctl bootstrap $gui $PLIST"
	fi
}

docker_check_darwin() {
	if ! as_user docker --version >/dev/null 2>&1; then
		warn "Docker is not installed; Docker Desktop, OrbStack or Colima must be installed and running for stacks to work"
		return 0
	fi
	as_user docker compose version >/dev/null 2>&1 || warn "the docker compose plugin is missing"
	ds=$(config_value docker_socket /var/run/docker.sock)
	[ -S "$ds" ] || warn "Docker socket $ds not found; start Docker Desktop, OrbStack or Colima"
}

do_install_darwin() {
	PHASE=install
	say "Installing for $TARGET_USER"
	if [ -z "$ROOT" ] && [ -z "$AS_ROOT" ] && [ -z "$FROM_PKG" ]; then
		have sudo || die 4 "need sudo to write to $PREFIX (or run as root)"
		note "sudo is used only for $PREFIX; you may be asked for your password"
	fi
	install_files_darwin
	validate_config
	configure_service_darwin
	PHASE=
	wait_ready
	docker_check
}

print_plan_darwin() {
	say "Plan (nothing is changed in dry-run mode)"
	note "install for user $TARGET_USER (home $TARGET_HOME)"
	note "create/replace $(rp "$BIN") and $(rp "$HELPER") (sudo only for $PREFIX, unless root)"
	note "create if missing $(rp "$CONFIG") (0600), $(rp "$DATA_DIR"), $(rp "$STACKS_DIR"), $(rp "$LOG_DIR")"
	note "create/replace $(rp "$PLIST")"
	if [ -n "$NO_START" ]; then
		note "the agent is not started (--no-start)"
	elif [ -n "${UPGRADE:-}" ]; then
		note "would run: launchctl kickstart -k gui/$TARGET_UID/$LABEL (bootout + bootstrap when the plist changed)"
	else
		note "would run: launchctl bootout gui/$TARGET_UID/$LABEL; launchctl bootstrap gui/$TARGET_UID $PLIST"
	fi
}

print_uninstall_plan_darwin() {
	say "Plan (nothing is changed in dry-run mode)"
	note "would run: launchctl bootout gui/$TARGET_UID/$LABEL"
	note "remove $(rp "$PLIST"), $(rp "$BIN"), $(rp "$HELPER_DIR")"
	note "would run: pkgutil --forget $LABEL (when that receipt exists)"
	if [ -n "$PURGE" ]; then
		note "remove $(rp "$SUPPORT_DIR") (including the stacks) and $(rp "$LOG_DIR")"
	fi
	note "keep everything in Docker"
}

# confirm_purge: --purge removes the stacks, so it needs --yes or an answer.
confirm_purge() {
	[ -n "$PURGE" ] || return 0
	[ -z "$YES" ] || return 0
	cp_msg="--purge removes $SUPPORT_DIR, including your stacks in $STACKS_DIR, and $LOG_DIR"
	if [ -t 0 ]; then
		printf '%s. Continue? [y/N] ' "$cp_msg" >&2
		cp_ans=
		read -r cp_ans || cp_ans=
		case $cp_ans in
		y | Y | yes | YES) return 0 ;;
		esac
		die 2 "not confirmed; nothing was removed"
	fi
	die 2 "$cp_msg; re-run with --yes to confirm"
}

do_uninstall_darwin() {
	PHASE=install
	agent_cmd launchctl bootout "gui/$TARGET_UID/$LABEL" || true
	as_fs rm -f "$(rp "$PLIST")"
	priv rm -f "$(rp "$BIN")" "$(rp "$BIN").new"
	priv rm -rf "$(rp "$HELPER_DIR")"
	if have pkgutil && pkgutil --pkg-info "$LABEL" >/dev/null 2>&1; then
		priv_svc pkgutil --forget "$LABEL" || warn "could not forget the $LABEL package receipt"
	fi
	say "Removed $(rp "$BIN"), $(rp "$PLIST") and $(rp "$HELPER_DIR")"
	if [ -n "$PURGE" ]; then
		as_fs rm -rf "$(rp "$SUPPORT_DIR")" "$(rp "$LOG_DIR")"
		say "Purged $(rp "$SUPPORT_DIR") and $(rp "$LOG_DIR")"
	else
		note "kept $(rp "$SUPPORT_DIR") and $(rp "$LOG_DIR") (use --purge --yes to remove them, stacks included)"
	fi
	note "kept everything in Docker"
	PHASE=
}

# ---------------------------------------------------------------- uninstall

do_uninstall() {
	if [ "$OS" = darwin ]; then
		do_uninstall_darwin
		return 0
	fi
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
	parse_args "$@"
	setup_paths
	detect_platform
	platform_setup
	if [ -n "$PRINT_PATHS" ]; then
		print_paths
		exit 0
	fi
	[ -n "$UNINSTALL" ] || [ -n "$FROM_PKG" ] || check_base_url
	if [ -n "$FROM_PKG" ] && [ "$OS" != darwin ]; then
		usage_error "--from-pkg is only for the macOS package"
	fi
	require_root
	refuse_package_install

	if [ -n "$UNINSTALL" ]; then
		if [ -n "$DRY_RUN" ]; then
			print_uninstall_plan
			exit 0
		fi
		[ "$OS" != darwin ] || confirm_purge
		do_uninstall
		exit 0
	fi

	if [ -n "$FROM_PKG" ]; then
		stage_from_pkg
	else
		check_tools
		resolve_and_download
		check_installed
		download_and_verify
	fi
	if [ -n "$DRY_RUN" ]; then
		print_plan
		exit 0
	fi
	do_install
}

main "$@"
