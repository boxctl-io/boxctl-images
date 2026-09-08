#!/usr/bin/env bash
# Downloads + decompresses boxctl-vms's published vscode image onto this
# Firecracker host, atomically swapping it into place at
# /opt/fc/images/vscode.ext4 -- the fast path once a build has already
# been published via publish-vscode-image.sh, as an alternative to running
# bake-vscode-image.sh locally. Mirrors pull-desktop-image.sh exactly; see
# that script for why this needs no build tooling beyond the one download.
#
# The R2 bucket is private, so VSCODE_ROOTFS_URL below must be a
# short-lived presigned GET URL, not the bucket path itself -- mint one
# with scripts/images/mint-vscode-image-link.sh from wherever
# scripts/images/.env.local's R2 credentials live, then run this on aimax:
#   VSCODE_ROOTFS_URL='...' bash scripts/images/pull-vscode-image.sh
set -euo pipefail

IMAGE_DIR=/opt/fc/images
IMAGE="$IMAGE_DIR/vscode.ext4"

T0=$(date +%s)
say() { echo "[pull-vscode-image +$(($(date +%s) - T0))s] $*"; }

: "${VSCODE_ROOTFS_URL:?missing VSCODE_ROOTFS_URL -- mint one with scripts/images/mint-vscode-image-link.sh}"

command -v zstd >/dev/null 2>&1 || {
  echo "zstd not found — install with: apt-get install -y zstd" >&2
  exit 1
}

mkdir -p "$IMAGE_DIR"
DLTMP=$(mktemp -d)
trap 'rm -rf "$DLTMP"' EXIT

say "downloading + decompressing vscode rootfs"
curl -fsSL "$VSCODE_ROOTFS_URL" | zstd -d -q -o "$DLTMP/vscode.ext4"

say "swapping into place at $IMAGE"
mv "$DLTMP/vscode.ext4" "$IMAGE"

say "done -- $IMAGE ready. Test with: onctl create -n vscodetest -p fc --rootfs-image $IMAGE"
