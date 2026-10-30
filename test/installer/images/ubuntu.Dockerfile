# Test image for install.sh on Ubuntu: nothing of Stackstead is baked in.
FROM ubuntu:24.04@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852
LABEL stackstead.test="image"
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      curl ca-certificates openssh-client tar systemd procps util-linux passwd \
 && rm -rf /var/lib/apt/lists/*
