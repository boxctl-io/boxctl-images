# Debian 13 (trixie), SSH only -- the same default AI-agent sandbox
# providers (E2B, Modal, Daytona) use. images/common/install.sh is all of
# it; see docs/plans/dockerfile-rootfs.md for how this becomes an ext4.
FROM debian:trixie-slim@sha256:f6b08b09a05725f9431502d3b5714f5c4576e82c1a84fa1debe0c24a7e0c3ec4
RUN --mount=from=common,target=/boxctl sh /boxctl/install.sh
