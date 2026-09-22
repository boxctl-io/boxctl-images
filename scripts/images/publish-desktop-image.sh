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

# The object is named after the image's own sha256 -- the digest of the
# *uncompressed* ext4, which is exactly what an export bundle's manifest
# records as baseImageSha256 (see internal/vmbundle's Manifest and
# rootfsdiff.Identity). A timestamp told you when a generation was
# published but not which one a given backup needs, so restoring a
# diff-mode bundle after a re-bake meant downloading and hashing
# candidates at 4-20 GiB a go. Named this way the lookup is a prefix
# search (scripts/images/find-image-by-sha.sh), or a direct GET when the
# caller already knows the image name. R2 keeps the publish time on the
# object itself, so nothing is lost by dropping it from the key, and
# re-publishing identical bytes is now idempotent rather than a new
# object each time.
IMAGE_SHA=""
if [ -r "$IMAGE.sha256sum" ]; then
  read -r sidecar_sha sidecar_size _ <"$IMAGE.sha256sum" || true
  # Size-checked like boxctl-vms-agent.sh's own host_images does: a
  # sidecar left over from a previous bake must never name this object.
  [ "${sidecar_size:-0}" = "$(stat -c %s "$IMAGE")" ] && IMAGE_SHA="$sidecar_sha"
fi
if [ -z "$IMAGE_SHA" ]; then
  say "hashing $IMAGE (no usable sidecar -- baked before bake-*-image.sh wrote one)"
  IMAGE_SHA=$(sha256sum "$IMAGE" | awk '{print $1}')
fi
say "image sha256: $IMAGE_SHA"
DEST_KEY="images/desktop.ext4-$IMAGE_SHA.zst"
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
