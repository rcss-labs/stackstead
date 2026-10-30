# Test image for install.sh on Alpine: busybox adduser/addgroup, no systemd.
FROM alpine:3.21@sha256:ee16d92f96db08d9d2d07da48555a27f3a69d8f0759021dbc601195ed0d2b5bd
LABEL stackstead.test="image"
RUN apk add --no-cache curl ca-certificates openssh-keygen tar
