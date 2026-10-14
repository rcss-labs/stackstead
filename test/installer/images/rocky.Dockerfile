# Test image for install.sh on Rocky Linux (rpm family): nothing of Stackstead is baked in.
# rockylinux:9-minimal ships curl-minimal, which satisfies the script.
FROM rockylinux:9-minimal
LABEL stackstead.test="image"
RUN microdnf -y install openssh-clients tar systemd procps-ng util-linux shadow-utils findutils \
 && (command -v curl >/dev/null || microdnf -y install curl) \
 && microdnf clean all
