# Test image for install.sh on Alpine: busybox adduser/addgroup, no systemd.
FROM alpine:3.21
LABEL stackstead.test="image"
RUN apk add --no-cache curl ca-certificates openssh-keygen tar
