# Package lint image: lintian for the deb, rpmlint (plus rpm itself) for the rpm.
# Base is pinned by the multi-arch index digest (debian:bookworm-slim at 2026-10-14) and
# the tools by exact apt versions; the Makefile tags the built image with a hash of this
# file so editing it rebuilds the image. If Debian retires these versions, bump all three.
FROM debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587

RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      lintian=2.116.3+deb12u1 rpm=4.18.0+dfsg-1+deb12u1 rpmlint=2.4.0+ds1-0.2 \
 && rm -rf /var/lib/apt/lists/*

# nfpm writes absolute paths into the rpm cpio payload. rpmlint extracts with a bare
# `cpio -id`, which then writes into the real filesystem (and finds nothing to inspect).
# Strip the leading slash so the payload lands in rpmlint's temp dir.
RUN printf '#!/bin/sh\nexec /usr/bin/cpio --no-absolute-filenames "$@"\n' > /usr/local/bin/cpio \
 && chmod 755 /usr/local/bin/cpio

WORKDIR /dist
