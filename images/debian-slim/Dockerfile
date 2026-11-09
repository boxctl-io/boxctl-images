# Debian 13 (trixie), SSH only -- the same default AI-agent sandbox
# providers (E2B, Modal, Daytona) use. images/common/install.sh plus
# iptables; see docs/plans/dockerfile-rootfs.md for how this becomes an ext4.
FROM debian:trixie-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad
RUN --mount=from=common,target=/boxctl sh /boxctl/install.sh
# iptables: Kubernetes on a box needs it on the host -- RKE2's CNI
# portmap plugin runs it from PATH, and without it every pod with a
# hostPort (RKE2's Traefik) sticks in FailedCreatePodSandBox, which is what
# a Rancher downstream node on a fresh box ran into. k3s ships its own, so
# it never noticed. trixie's default is the nft backend, which the box
# kernel supports.
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends iptables \
 && apt-get clean
