# Package lint image: lintian for the deb, rpmlint (plus rpm itself) for the rpm.
# Base tag is pinned; the Makefile tags the built image with a hash of this file so
# editing it rebuilds the image.
FROM debian:bookworm-slim

RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends lintian rpm rpmlint \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /dist
