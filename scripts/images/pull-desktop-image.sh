#!/usr/bin/env bash
# Downloads + decompresses boxctl-vms's published desktop image onto this
# Firecracker host, atomically swapping it into place at
# /opt/fc/images/desktop.ext4 -- the fast path once a build has already
# been published via publish-desktop-image.sh, as an alternative to
# running bake-desktop-image.sh locally (~a few minutes either way, but
# this needs no build tooling/network access beyond the one download).
#
# Unlike onctl-runners' install.sh, this does NOT set up firecracker,
# onctl, NAT, or bridge isolation -- aimax already has all of that from
# onctl-runners' own onboarding. This script only ever needs to run again
# to pick up a newer desktop image (or to restore it after e.g. an /opt/fc
# disk rebuild).
#
# The R2 bucket is private, so DESKTOP_ROOTFS_URL below must be a
# short-lived presigned GET URL, not the bucket path itself -- mint one
# with scripts/images/mint-image-link.sh from wherever
# scripts/images/.env.local's R2 credentials live, then run this on aimax:
#   DESKTOP_ROOTFS_URL='...' bash scripts/images/pull-desktop-image.sh
set -euo pipefail

IMAGE_DIR=/opt/fc/images
IMAGE="$IMAGE_DIR/desktop.ext4"

T0=$(date +%s)
say() { echo "[pull-desktop-image +$(($(date +%s) - T0))s] $*"; }

: "${DESKTOP_ROOTFS_URL:?missing DESKTOP_ROOTFS_URL -- mint one with scripts/images/mint-image-link.sh}"

command -v zstd >/dev/null 2>&1 || {
  echo "zstd not found — install with: apt-get install -y zstd" >&2
  exit 1
}

mkdir -p "$IMAGE_DIR"
DLTMP=$(mktemp -d)
trap 'rm -rf "$DLTMP"' EXIT

say "downloading + decompressing desktop rootfs"
curl -fsSL "$DESKTOP_ROOTFS_URL" | zstd -d -q -o "$DLTMP/desktop.ext4"

say "swapping into place at $IMAGE"
mv "$DLTMP/desktop.ext4" "$IMAGE"

say "done -- $IMAGE ready. Test with: onctl create -n desktoptest -p fc --rootfs-image $IMAGE"
