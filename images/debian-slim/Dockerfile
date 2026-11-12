# Debian 13 (trixie), SSH only -- the same default AI-agent sandbox
# providers (E2B, Modal, Daytona) use. images/common/install.sh is all of
# it; see docs/plans/dockerfile-rootfs.md for how this becomes an ext4.
FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2
RUN --mount=from=common,target=/boxctl sh /boxctl/install.sh
RUN apt-get update && apt-get install -y --no-install-recommends iptables && rm -rf /var/lib/apt/lists/*
