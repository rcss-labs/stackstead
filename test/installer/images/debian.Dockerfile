# Test image for install.sh on Debian: nothing of Stackstead is baked in.
# systemctl exists (systemd package) but systemd is not booted.
FROM debian:bookworm-slim
LABEL stackstead.test="image"
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      curl ca-certificates openssh-client tar systemd procps util-linux passwd devscripts \
 && rm -rf /var/lib/apt/lists/*
