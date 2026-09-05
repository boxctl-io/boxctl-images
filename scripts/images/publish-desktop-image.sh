#!/usr/bin/env bash
# Compresses and uploads the baked desktop rootfs (scripts/images/
# bake-desktop-image.sh's output) to the Cloudflare R2 bucket dedicated to
# boxctl-vms's own images -- deliberately a separate bucket from
# onctl-runners' own (see docs/plans/gui-desktop-vnc-access.md), so a
# bucket-scoped R2 API token here can never touch that project's images.
# Mirrors onctl-runners' fc-host/scripts/publish-image.sh almost exactly;
# see that script's own comments for the RCLONE_S3_NO_CHECK_BUCKET
# workaround and the dated-key + stable-"latest"-pointer upload shape,
# both reused here unchanged.
#
# Run ON the host that has the image (aimax), as root:
#   sudo bash scripts/images/publish-desktop-image.sh
#
# Requires: rclone (`curl https://rclone.org/install.sh | sudo bash`), and
# scripts/images/.env.local (gitignored) with:
#   CF_ACCESS_KEY=...
#   CF_SECRET_ACCESS_KEY=...
#   CF_S3_ENDPOINT=https://<account-id>.r2.cloudflarestorage.com
# (CI passes these as env vars instead -- see
# .github/workflows/build-desktop-image.yml.)
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ENV_FILE="$SCRIPT_DIR/.env.local"
IMAGE="${PUBLISH_IMAGE:-/opt/fc/images/desktop.ext4}"
BUCKET="${CF_BUCKET:-boxctl-vms}"

say() { echo "[publish-desktop-image] $*"; }

[ -s "$IMAGE" ] || {
  echo "missing $IMAGE — run scripts/images/bake-desktop-image.sh first" >&2
  exit 1
}

command -v zstd >/dev/null 2>&1 || {
  echo "zstd not found — install with: apt-get install -y zstd" >&2
  exit 1
}
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
# rclone's S3 backend checks the destination bucket exists (falling back
# to CreateBucket) before every write by default -- both denied for a
# deliberately bucket-scoped token. See onctl-runners' publish-image.sh
# for the full root-cause writeup of the misleading error this avoids.
export RCLONE_S3_NO_CHECK_BUCKET=true

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT

say "compressing $IMAGE"
COMPRESSED="$WORKDIR/desktop.ext4.zst"
zstd -T0 -9 -q "$IMAGE" -o "$COMPRESSED"
say "compressed size: $(du -h "$COMPRESSED" | cut -f1)"

STAMP=$(date -u +%Y%m%d-%H%M%S)
DEST_KEY="images/desktop.ext4-$STAMP.zst"
STABLE_KEY="images/latest/desktop.ext4.zst"

say "uploading to r2:$BUCKET/$DEST_KEY"
rclone copyto "$COMPRESSED" "r2:$BUCKET/$DEST_KEY"

say "verifying upload"
rclone lsf "r2:$BUCKET/images/" | grep -F "$(basename "$DEST_KEY")" >/dev/null || {
  echo "FAIL: uploaded file not found when listing r2:$BUCKET/images/" >&2
  exit 1
}

say "updating stable pointer r2:$BUCKET/$STABLE_KEY"
rclone copyto "r2:$BUCKET/$DEST_KEY" "r2:$BUCKET/$STABLE_KEY"

say "PASS — published to r2:$BUCKET/$DEST_KEY (and r2:$BUCKET/$STABLE_KEY)"
