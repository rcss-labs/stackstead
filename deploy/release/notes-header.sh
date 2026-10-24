#!/usr/bin/env bash
# Prints the fixed part of the release notes (install, macOS Gatekeeper note, checksums) for the
# release workflow; the Changes section is appended by `gh release create --generate-notes`.
# Keep it in step with the template in RELEASING.md.
set -euo pipefail

if [ $# -ne 2 ]; then
	echo "usage: $0 vX.Y.Z path/to/SHA256SUMS" >&2
	exit 2
fi
version=${1#v}
sums=$2
[ -s "$sums" ] || { echo "$sums is missing or empty" >&2; exit 1; }

cat <<EOF
## Install

    curl -fsSL https://github.com/rcss-labs/stackstead/releases/latest/download/install.sh | sudo sh

Or use the \`.deb\`, \`.rpm\` or macOS \`.pkg\` below. Details: docs/install.md.

## macOS package

The \`.pkg\` is not signed with an Apple Developer ID. Double-clicking it shows "cannot be opened because it is
from an unidentified developer"; right-click the package and choose Open, or run
\`sudo installer -pkg stackstead_${version}_darwin.pkg -target /\` from a terminal. \`spctl --assess --type install\`
reports "rejected" for an unsigned package; that is expected. The installed binary runs normally (it is ad-hoc
signed, and neither installer nor curl sets a quarantine attribute).

## Checksums (SHA256SUMS)

Releases are not signed yet: verify downloads with \`sha256sum -c SHA256SUMS --ignore-missing\`
(\`shasum -a 256 -c ...\` on macOS).

EOF
sed 's/^/    /' "$sums"
