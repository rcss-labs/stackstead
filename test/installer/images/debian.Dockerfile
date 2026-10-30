# Test image for install.sh on Debian: nothing of Stackstead is baked in.
# systemctl exists (systemd package) but systemd is not booted.
FROM debian:bookworm-slim@sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3
LABEL stackstead.test="image"
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      curl ca-certificates openssh-client tar systemd procps util-linux passwd devscripts \
 && rm -rf /var/lib/apt/lists/*
