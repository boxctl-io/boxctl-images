#!/usr/bin/env bash
# Downloads + decompresses boxctl-vms's published debian-slim image onto
# this Firecracker host, atomically swapping it into place at
# /opt/fc/images/debian-slim.ext4 -- the fast path once a build has already
# been published via publish-debian-slim-image.sh, as an alternative to
# running bake-debian-slim-image.sh locally. Mirrors pull-desktop-image.sh
# exactly; see that script for why this needs no build tooling beyond the
# one download.
#
# The R2 bucket is private, so DEBIAN_SLIM_ROOTFS_URL below must be a
# short-lived presigned GET URL, not the bucket path itself -- mint one
# with scripts/images/mint-debian-slim-image-link.sh from wherever
# scripts/images/.env.local's R2 credentials live, then run this on aimax:
#   DEBIAN_SLIM_ROOTFS_URL='...' bash scripts/images/pull-debian-slim-image.sh
set -euo pipefail

IMAGE_DIR=/opt/fc/images
IMAGE="$IMAGE_DIR/debian-slim.ext4"

T0=$(date +%s)
say() { echo "[pull-debian-slim-image +$(($(date +%s) - T0))s] $*"; }

: "${DEBIAN_SLIM_ROOTFS_URL:?missing DEBIAN_SLIM_ROOTFS_URL -- mint one with scripts/images/mint-debian-slim-image-link.sh}"

command -v zstd >/dev/null 2>&1 || {
  echo "zstd not found — install with: apt-get install -y zstd" >&2
  exit 1
}

mkdir -p "$IMAGE_DIR"
DLTMP=$(mktemp -d)
trap 'rm -rf "$DLTMP"' EXIT

say "downloading + decompressing debian-slim rootfs"
curl -fsSL "$DEBIAN_SLIM_ROOTFS_URL" | zstd -d -q -o "$DLTMP/debian-slim.ext4"

say "swapping into place at $IMAGE"
mv "$DLTMP/debian-slim.ext4" "$IMAGE"

say "done -- $IMAGE ready. Test with: onctl create -n debiantest -p fc --rootfs-image $IMAGE"
