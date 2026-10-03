# Debian 13 (trixie), SSH only -- the same default AI-agent sandbox
# providers (E2B, Modal, Daytona) use. images/common/install.sh is all of
# it; see docs/plans/dockerfile-rootfs.md for how this becomes an ext4.
FROM debian:trixie-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a
RUN --mount=from=common,target=/boxctl sh /boxctl/install.sh
