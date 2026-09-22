#!/usr/bin/env bash
# Finds the published base image whose *uncompressed* content has a given
# sha256 -- the digest an export bundle's manifest records as
# baseImageSha256, and the one a restore fails on when the host no longer
# has that generation ("no local base image matches sha256 ...", see
# internal/vmbundle's resolveBaseImage).
#
# Since publish-*-image.sh names every object after that digest, this is
# a prefix search rather than a download-and-hash hunt through every
# generation ever published.
#
#   bash scripts/images/find-image-by-sha.sh <sha256>           # print the key
#   bash scripts/images/find-image-by-sha.sh <sha256> --link    # + presigned URL
#
# With --link it prints the ready-to-run pull command for the host, the
# same shape mint-*-image-link.sh emits. Objects published before this
# naming existed are timestamped instead, so they won't be found here --
# re-publish that generation from a host that still has the image, or
# fall back to downloading candidates by hand.
#
# Requires: rclone, and scripts/images/.env.local (gitignored) with:
#   CF_ACCESS_KEY=...
#   CF_SECRET_ACCESS_KEY=...
#   CF_S3_ENDPOINT=https://<account-id>.r2.cloudflarestorage.com
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ENV_FILE="$SCRIPT_DIR/.env.local"
BUCKET="${CF_BUCKET:-boxctl-vms}"

say() { echo "[find-image-by-sha] $*" >&2; }

SHA="${1:-}"
MODE="${2:-}"
if [ -z "$SHA" ]; then
  echo "Usage: $0 <sha256> [--link [expire]]" >&2
  exit 1
fi
# Accept a full digest or a distinctive prefix; reject anything that
# isn't hex, since that silently matches nothing and looks like "the
# image is gone" rather than "you passed the wrong thing".
case "$SHA" in
  *[!0-9a-fA-F]*) echo "not a sha256 (hex expected): $SHA" >&2; exit 1 ;;
esac
SHA=$(echo "$SHA" | tr '[:upper:]' '[:lower:]')
EXPIRE="${3:-1h}"

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

say "searching r2:$BUCKET/images/ for an object named after $SHA"
MATCHES=$(rclone lsf "r2:$BUCKET/images/" | grep -- "-${SHA}[0-9a-f]*\.zst\$" || true)
if [ -z "$MATCHES" ]; then
  echo "no published image matches sha256 $SHA" >&2
  echo "  (objects published before sha-named keys are timestamped instead -- list them with: rclone lsl r2:$BUCKET/images/)" >&2
  exit 1
fi
if [ "$(echo "$MATCHES" | wc -l)" -gt 1 ]; then
  say "warning: more than one object matches that prefix -- pass a longer prefix, or pick one:"
  echo "$MATCHES" >&2
  exit 1
fi

KEY="images/$MATCHES"
echo "$KEY"

if [ "$MODE" = "--link" ]; then
  say "minting presigned URL (expire: $EXPIRE)"
  URL=$(rclone link --expire "$EXPIRE" "r2:$BUCKET/$KEY")
  # The image's own name, not "latest": this is a specific historical
  # generation, and it must not land on top of the host's current image.
  IMAGE_FILE=${MATCHES%%-*}
  say "done -- on the host, this writes the generation to a name of its own:"
  echo ""
  echo "curl -fsSL '$URL' | zstd -d -q -o /opt/fc/images/archive-$IMAGE_FILE-${SHA:0:12}"
fi
