# Debian 13 (trixie), SSH only -- the same default AI-agent sandbox
# providers (E2B, Modal, Daytona) use. images/common/install.sh is all of
# it; see docs/plans/dockerfile-rootfs.md for how this becomes an ext4.
FROM debian:trixie-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad
RUN --mount=from=common,target=/boxctl sh /boxctl/install.sh
