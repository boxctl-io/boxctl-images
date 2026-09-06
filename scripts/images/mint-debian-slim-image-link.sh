#!/usr/bin/env bash
# Mints a short-lived presigned GET URL for boxctl-vms's published
# debian-slim image, for a host to pull without ever holding the
# write-capable R2 token itself. Mirrors mint-image-link.sh (the desktop
# image's own version) exactly, just pointed at the debian-slim object --
# see that script for why this is a presigned URL rather than a standing
# read token.
#
# Usage:
#   bash scripts/images/mint-debian-slim-image-link.sh [expire]   # expire default: 1h
#
# Requires: rclone, and scripts/images/.env.local (same file
# publish-debian-slim-image.sh and verify-object-storage.sh read) with:
#   CF_ACCESS_KEY=...
#   CF_SECRET_ACCESS_KEY=...
#   CF_S3_ENDPOINT=https://<account-id>.r2.cloudflarestorage.com
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ENV_FILE="$SCRIPT_DIR/.env.local"
BUCKET="${CF_BUCKET:-boxctl-vms}"
EXPIRE="${1:-1h}"

say() { echo "[mint-debian-slim-image-link] $*" >&2; }

command -v rclone >/dev/null 2>&1 || {
  echo "rclone not found — install with: curl https://rclone.org/install.sh | sudo bash" >&2
  exit 1
}

if [ -z "${CF_ACCESS_KEY:-}" ] || [ -z "${CF_SECRET_ACCESS_KEY:-}" ] || [ -z "${CF_S3_ENDPOINT:-}" ]; then
  [ -f "$ENV_FILE" ] || {
    echo "missing $ENV_FILE — expected CF_ACCESS_KEY/CF_SECRET_ACCESS_KEY/CF_S3_ENDPOINT (or set them as env vars directly)" >&2
    exit 1
  }
  # shellcheck source=/dev/null
  source "$ENV_FILE"
fi
: "${CF_ACCESS_KEY:?missing CF_ACCESS_KEY (env var or $ENV_FILE)}"
: "${CF_SECRET_ACCESS_KEY:?missing CF_SECRET_ACCESS_KEY (env var or $ENV_FILE)}"
: "${CF_S3_ENDPOINT:?missing CF_S3_ENDPOINT (env var or $ENV_FILE)}"

export RCLONE_CONFIG=/dev/null
export RCLONE_CONFIG_R2_TYPE=s3
export RCLONE_CONFIG_R2_PROVIDER=Cloudflare
export RCLONE_CONFIG_R2_ACCESS_KEY_ID="$CF_ACCESS_KEY"
export RCLONE_CONFIG_R2_SECRET_ACCESS_KEY="$CF_SECRET_ACCESS_KEY"
export RCLONE_CONFIG_R2_ENDPOINT="$CF_S3_ENDPOINT"
export RCLONE_S3_NO_CHECK_BUCKET=true

say "minting presigned URL (expire: $EXPIRE)"
DEBIAN_SLIM_URL=$(rclone link --expire "$EXPIRE" "r2:$BUCKET/images/latest/debian-slim.ext4.zst")

say "done -- paste this into the pull-debian-slim-image.sh invocation on aimax:"
echo ""
echo "DEBIAN_SLIM_ROOTFS_URL='$DEBIAN_SLIM_URL' bash scripts/images/pull-debian-slim-image.sh"
