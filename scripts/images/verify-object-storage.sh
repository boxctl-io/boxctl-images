#!/usr/bin/env bash
# Verifies the Cloudflare R2 bucket used by boxctl-vms's own image pipeline
# is actually reachable, authenticated, and read/write-capable -- not just
# that it exists. Does a real authenticated round-trip via the
# S3-compatible API. Mirrors onctl-runners' fc-host/scripts/
# verify-object-storage.sh exactly (same rclone quirks apply here too --
# see that script's comments for why `aws`/`mc` aren't used, and why the
# account-root ListBuckets call is deliberately not the auth test).
#
# Run locally from the repo root:
#   scripts/images/verify-object-storage.sh
#
# Requires: rclone (`brew install rclone`), and
# scripts/images/.env.local (gitignored) with:
#   CF_ACCESS_KEY=...
#   CF_SECRET_ACCESS_KEY=...
#   CF_S3_ENDPOINT=https://<account-id>.r2.cloudflarestorage.com
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ENV_FILE="$SCRIPT_DIR/.env.local"
BUCKET="${CF_BUCKET:-boxctl-vms}"

say() { echo "[verify-object-storage] $*"; }

command -v rclone >/dev/null 2>&1 || {
  echo "rclone not found — install with: brew install rclone" >&2
  exit 1
}

[ -f "$ENV_FILE" ] || {
  echo "missing $ENV_FILE — expected CF_ACCESS_KEY/CF_SECRET_ACCESS_KEY/CF_S3_ENDPOINT" >&2
  exit 1
}
# shellcheck source=/dev/null
source "$ENV_FILE"
: "${CF_ACCESS_KEY:?missing CF_ACCESS_KEY in $ENV_FILE}"
: "${CF_SECRET_ACCESS_KEY:?missing CF_SECRET_ACCESS_KEY in $ENV_FILE}"
: "${CF_S3_ENDPOINT:?missing CF_S3_ENDPOINT in $ENV_FILE}"

export RCLONE_CONFIG=/dev/null
export RCLONE_CONFIG_R2_TYPE=s3
export RCLONE_CONFIG_R2_PROVIDER=Cloudflare
export RCLONE_CONFIG_R2_ACCESS_KEY_ID="$CF_ACCESS_KEY"
export RCLONE_CONFIG_R2_SECRET_ACCESS_KEY="$CF_SECRET_ACCESS_KEY"
export RCLONE_CONFIG_R2_ENDPOINT="$CF_S3_ENDPOINT"
export RCLONE_S3_NO_CHECK_BUCKET=true

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT
TEST_KEY="_verify/verify-object-storage-$(date +%s).txt"

say "listing objects in $BUCKET (confirms the key authenticates and has access to this bucket -- a bucket-scoped token can't ListBuckets at the account root, so that isn't tested here)"
rclone lsf "r2:$BUCKET/" || {
  echo "FAIL: listing $BUCKET was denied — check the R2 API token's" >&2
  echo "permission scope in the Cloudflare dashboard (needs Object Read" >&2
  echo "& Write on this bucket)." >&2
  exit 1
}

say "uploading a test object"
echo "boxctl-vms image pipeline verification $(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$WORKDIR/upload.txt"
rclone copyto "$WORKDIR/upload.txt" "r2:$BUCKET/$TEST_KEY" || {
  echo "FAIL: upload to $BUCKET denied" >&2
  exit 1
}

say "downloading it back"
rclone copyto "r2:$BUCKET/$TEST_KEY" "$WORKDIR/download.txt" || {
  echo "FAIL: download from $BUCKET denied" >&2
  exit 1
}

say "comparing upload vs download"
if ! diff -q "$WORKDIR/upload.txt" "$WORKDIR/download.txt" >/dev/null; then
  echo "FAIL: downloaded content doesn't match what was uploaded" >&2
  exit 1
fi

say "cleaning up test object"
rclone deletefile "r2:$BUCKET/$TEST_KEY" || {
  echo "WARN: cleanup delete failed — $TEST_KEY may be left behind in $BUCKET" >&2
}

say "PASS — $BUCKET (Cloudflare R2) is reachable, authenticated, and read/write-capable"
