#!/usr/bin/env bash
# Bakes a minimal Debian rootfs for Firecracker VMs -- a lean "give me a box"
# default with no desktop, no build tooling, nothing beyond SSH access.
# Debian (not Ubuntu) is a deliberate choice here, not just a name: E2B,
# Modal, and Daytona -- the AI-agent-sandbox providers surveyed at
# https://rywalker.com/research/ai-agent-sandboxes -- all default their
# sandbox images to Debian slim (Modal's own base image function is
# literally named debian_slim()), so this matches the shape users of those
# tools already expect from a "default" sandbox box. onctl-runners' own
# bake-fc-image.sh/bake-desktop-image.sh both use Ubuntu (noble) instead --
# that's fine for a CI runner or a GUI desktop, where Ubuntu's broader
# package/driver support pays for itself, but it's not the industry-default
# choice for a bare sandbox, so this image doesn't inherit it.
#
# Run ON the Firecracker host (aimax), not locally:
#   sudo bash scripts/images/bake-debian-slim-image.sh
#
# Output: /opt/fc/images/debian-slim.ext4 -- selected per-VM via boxctl-vms's
# image=debian-slim (see internal/vm/service.go's Create), not aimax's
# default fc.rootfsImage (which stays runner-base-docker.ext4 for everyone
# who doesn't ask for this image).
set -euo pipefail

T0=$(date +%s)
say() { echo "[bake-debian-slim +$(($(date +%s) - T0))s] $*"; }

IMAGE_DIR="/opt/fc/images"
IMAGE="$IMAGE_DIR/debian-slim.ext4"
# Built here, not at $IMAGE directly, so a concurrent `onctl create` never
# copies a half-built/mid-truncate rootfs -- same reasoning as
# onctl-runners' bake-fc-image.sh, which this mirrors.
BUILD_IMAGE="$IMAGE.building"
MOUNT="/mnt/fc-rootfs-debian-slim"
CACHE_DIR="/opt/fc/cache"
mkdir -p "$CACHE_DIR"

say "installing build deps"
export DEBIAN_FRONTEND=noninteractive
export LC_ALL=C.UTF-8 LANG=C.UTF-8
unset LANGUAGE
apt-get update -qq
apt-get install -y -qq debootstrap e2fsprogs >/dev/null

say "creating empty ext4 image (4096M -- a minimal Debian base + openssh installs to well under 1G; this leaves headroom comparable to Daytona's 3GiB default sandbox disk for whatever the user installs at runtime, without the multi-gig overhead a desktop or build-tooling image needs)"
mkdir -p "$IMAGE_DIR"
truncate -s 4096M "$BUILD_IMAGE"
mkfs.ext4 -q -F "$BUILD_IMAGE"

say "mounting image"
mkdir -p "$MOUNT"
mount -o loop "$BUILD_IMAGE" "$MOUNT"
trap 'umount "$MOUNT" 2>/dev/null || true' EXIT

DEBOOTSTRAP_ARGS=(
  --variant=minbase
  --include=systemd,systemd-sysv,udev,openssh-server,curl,ca-certificates,iproute2,iputils-ping,sudo,netbase
)
# deb.debian.org is a globally load-balanced CDN (unlike Ubuntu's
# archive.ubuntu.com/mirror.hetzner.com split) -- no reachability probe
# needed here regardless of which network this host is on.
MIRROR="http://deb.debian.org/debian/"
SECURITY_MIRROR="http://deb.debian.org/debian-security/"
DEBOOTSTRAP_CACHE="$CACHE_DIR/debootstrap-trixie-slim.tar"
if [ ! -s "$DEBOOTSTRAP_CACHE" ]; then
  say "debootstrap Debian 13 (trixie) minbase: fetching package set into cache"
  DEBOOTSTRAP_SCRATCH=$(mktemp -d)
  debootstrap "${DEBOOTSTRAP_ARGS[@]}" --make-tarball="$DEBOOTSTRAP_CACHE" trixie "$DEBOOTSTRAP_SCRATCH" "$MIRROR"
  rm -rf "$DEBOOTSTRAP_SCRATCH"
fi
say "debootstrap Debian 13 (trixie) minbase (from cached package tarball)"
debootstrap "${DEBOOTSTRAP_ARGS[@]}" --unpack-tarball="$DEBOOTSTRAP_CACHE" trixie "$MOUNT" "$MIRROR"

say "writing sources.list (security repo included even though nothing extra is installed here, so an in-VM apt-get update/upgrade later actually has somewhere to pull patches from)"
cat <<EOF > "$MOUNT/etc/apt/sources.list"
deb $MIRROR trixie main
deb $MIRROR trixie-updates main
deb $SECURITY_MIRROR trixie-security main
EOF

say "resetting baked-in hostname (debootstrap leaks the host's own hostname)"
echo boxctl-debian-slim > "$MOUNT/etc/hostname"
grep -q '^127\.0\.1\.1' "$MOUNT/etc/hosts" 2>/dev/null || echo -e '127.0.1.1\tboxctl-debian-slim' >> "$MOUNT/etc/hosts"

say "fixing DNS (mask systemd-resolved, use static nameservers)"
rm -f "$MOUNT/etc/resolv.conf"
printf 'nameserver 8.8.8.8\nnameserver 1.1.1.1\n' > "$MOUNT/etc/resolv.conf"
ln -sf /dev/null "$MOUNT/etc/systemd/system/systemd-resolved.service"

mount -t proc proc "$MOUNT/proc"
mount --bind /dev "$MOUNT/dev"
mount -t devpts devpts "$MOUNT/dev/pts"
trap 'umount "$MOUNT/dev/pts" 2>/dev/null; umount "$MOUNT/dev" 2>/dev/null; umount "$MOUNT/proc" 2>/dev/null; umount "$MOUNT" 2>/dev/null || true' EXIT

say "installing chrony (what puts a resumed box's clock back -- see docs/plans/guest-clock-sync.md)"
chroot "$MOUNT" apt-get update -qq
chroot "$MOUNT" apt-get install -y -qq chrony >/dev/null
mkdir -p "$MOUNT/etc/chrony/conf.d"
cat <<'EOF' > "$MOUNT/etc/chrony/conf.d/boxctl-resume.conf"
# Firecracker emulates no RTC: a guest is handed wall-clock time once, at
# boot, by kvm-clock. `onctl pause` snapshots memory *and* device state,
# so a resumed box carries on with the clock it had when it was paused --
# behind by exactly how long it sat paused. With idle auto-pause that's
# the normal lifecycle of a box, not an edge case.

# /dev/ptp0 is the KVM paravirtual clock (the ptp_kvm driver, "KVM
# virtual PTP"). Reading it is a hypercall that returns the *host's* real
# time, so it needs no network, no NTP server and no DNS -- each of which
# a badly wrong clock can break by itself, which is the whole reason not
# to depend on them here. poll 0 reads it every second, so a resumed box
# is right again about a second later.
refclock PHC /dev/ptp0 poll 0 dpoll -2

# The line this hinges on. chrony's default is to *slew* a large offset
# (run the clock fast until it catches up) and to step only during its
# first few updates after startup. A post-resume offset always arrives
# long after startup, so the default path is slewing -- capped near 8%,
# which takes about a week to absorb a 12-hour gap. -1 means "step
# whenever the offset exceeds a second, at any time, however long this
# daemon has been running".
makestep 1.0 -1
EOF
# Stock chrony.conf on Debian/Ubuntu reads conf.d already; don't assume it.
grep -q '^confdir /etc/chrony/conf.d' "$MOUNT/etc/chrony/chrony.conf" \
  || echo 'confdir /etc/chrony/conf.d' >> "$MOUNT/etc/chrony/chrony.conf"
chroot "$MOUNT" systemctl enable chrony

say "enabling SSH"
chroot "$MOUNT" systemctl enable ssh
# Pre-generate host keys so sshd starts immediately at boot instead of
# blocking on kernel entropy for fresh key generation.
chroot "$MOUNT" ssh-keygen -A -q

say "cleaning apt cache"
chroot "$MOUNT" apt-get clean

say "writing baked marker"
touch "$MOUNT/opt/.baked"

say "flushing writes"
sync

say "unmounting and swapping into place at $IMAGE"
umount "$MOUNT/dev/pts" 2>/dev/null || true
umount "$MOUNT/dev" 2>/dev/null || true
umount "$MOUNT/proc" 2>/dev/null || true
umount "$MOUNT"
mv "$BUILD_IMAGE" "$IMAGE"

say "recording the identity sidecar ($IMAGE.sha256sum)"
# "<sha256> <size> <mtime-nanos>", exactly the format
# internal/rootfsdiff.Identity caches and boxctl-vms-agent.sh's
# host_images reports from. Written here, at the moment the image lands,
# because host_images validates a sidecar by *size* alone: this image is
# always truncated to the same fixed size, so the previous bake's sidecar
# would otherwise keep being served as the new image's checksum, and the
# controller would place a cross-host restore on a host whose base image
# isn't the one the bundle needs. Identity itself re-validates on mtime
# too, so the restore would then fail rather than patch against the wrong
# base -- but it would fail after being scheduled, with a confusing
# reason.
IMAGE_SHA=$(sha256sum "$IMAGE" | awk '{print $1}')
printf '%s %s %s\n' "$IMAGE_SHA" "$(stat -c %s "$IMAGE")" "$(date -r "$IMAGE" +%s%N)" > "$IMAGE.sha256sum"
say "sha256: $IMAGE_SHA (boxctl-image publish puts this in the R2 object name)"
# Disarm the EXIT trap now that everything it would try to unmount already
# is -- see onctl-runners' bake-fc-image.sh for why leaving it armed here
# makes the whole script exit non-zero even after a fully successful build.
trap - EXIT

say "done -- minimal Debian 13 (trixie) baked into $IMAGE"
echo ""
echo "Select it per-VM via boxctl-vms's image=debian-slim (internal/vm/service.go),"
echo "or test directly with:"
echo "  onctl create -n debiantest -p fc --rootfs-image $IMAGE"
