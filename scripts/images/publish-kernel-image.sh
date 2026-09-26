#!/usr/bin/env bash
# Compresses and uploads the box kernel (bake-kernel-image.sh's output) to the
# same R2 bucket and layout as the rootfs images -- see
# publish-debian-slim-image.sh for the bucket, naming and stable-pointer
# rationale, all reused here unchanged:
#   images/boxctl-kernel.vmlinux-<sha7>.zst   this build, full sha256 as metadata
#   images/latest/boxctl-kernel.vmlinux.zst   what hosts fetch (internal/vm
#                                             ImageObjectKey), via the
#                                             controller's image sync and
#                                             scripts/agent/fetch-image.sh
#
# Run ON the host that has the kernel, as root:
#   sudo bash scripts/images/publish-kernel-image.sh
#
# Requires zstd, rclone and CF_ACCESS_KEY/CF_SECRET_ACCESS_KEY/CF_S3_ENDPOINT
# (env vars, or scripts/images/.env.local).
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ENV_FILE="$SCRIPT_DIR/.env.local"
IMAGE="${PUBLISH_IMAGE:-/opt/fc/images/boxctl-kernel.vmlinux}"
BUCKET="${CF_BUCKET:-boxctl-vms}"
NAME=$(basename "$IMAGE")

say() { echo "[publish-kernel-image] $*"; }

[ -s "$IMAGE" ] || {
  echo "missing $IMAGE — run scripts/images/bake-kernel-image.sh first" >&2
  exit 1
}
command -v zstd >/dev/null 2>&1 || { echo "zstd not found — apt-get install -y zstd" >&2; exit 1; }
command -v rclone >/dev/null 2>&1 || { echo "rclone not found — curl https://rclone.org/install.sh | sudo bash" >&2; exit 1; }

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
export RCLONE_S3_NO_CHECK_BUCKET=true  # bucket-scoped token; see publish-debian-slim-image.sh

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT

IMAGE_SHA=""
if [ -r "$IMAGE.sha256sum" ]; then
  read -r sidecar_sha sidecar_size _ <"$IMAGE.sha256sum" || true
  [ "${sidecar_size:-0}" = "$(stat -c %s "$IMAGE")" ] && IMAGE_SHA="$sidecar_sha"
fi
[ -n "$IMAGE_SHA" ] || IMAGE_SHA=$(sha256sum "$IMAGE" | awk '{print $1}')
say "kernel sha256: $IMAGE_SHA"

COMPRESSED="$WORKDIR/$NAME.zst"
zstd -T0 -19 -q "$IMAGE" -o "$COMPRESSED"
say "compressed size: $(du -h "$COMPRESSED" | cut -f1)"

DEST_KEY="images/$NAME-${IMAGE_SHA:0:7}.zst"
STABLE_KEY="images/latest/$NAME.zst"

say "uploading to r2:$BUCKET/$DEST_KEY"
rclone copyto --header-upload "X-Amz-Meta-Sha256: $IMAGE_SHA" "$COMPRESSED" "r2:$BUCKET/$DEST_KEY"
rclone lsf "r2:$BUCKET/images/" | grep -F "$(basename "$DEST_KEY")" >/dev/null || {
  echo "FAIL: uploaded file not found when listing r2:$BUCKET/images/" >&2
  exit 1
}

LATEST_SHA=$(rclone lsjson --metadata "r2:$BUCKET/$STABLE_KEY" 2>/dev/null \
  | tr ',' '\n' \
  | sed -n 's/.*"[Ss]ha256"[[:space:]]*:[[:space:]]*"\([0-9a-f]\{64\}\)".*/\1/p' \
  | head -1)
if [ "$LATEST_SHA" = "$IMAGE_SHA" ]; then
  say "stable pointer already holds this kernel -- skipping the copy"
else
  say "updating stable pointer r2:$BUCKET/$STABLE_KEY"
  rclone copyto --header-upload "X-Amz-Meta-Sha256: $IMAGE_SHA" "r2:$BUCKET/$DEST_KEY" "r2:$BUCKET/$STABLE_KEY"
fi

say "PASS — published to r2:$BUCKET/$DEST_KEY (and r2:$BUCKET/$STABLE_KEY)"
