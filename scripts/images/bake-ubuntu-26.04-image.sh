#!/usr/bin/env bash
# Bakes a minimal Ubuntu 26.04 LTS (resolute) rootfs for Firecracker VMs --
# the Ubuntu counterpart to bake-debian-slim-image.sh: SSH access and
# nothing else, no desktop, no build tooling. debian-slim stays the default
# sandbox image (see its header for why); this one is for users who want
# Ubuntu's package set and LTS support window without the XFCE layer that
# desktop/vscode carry (both of which are still on 24.04).
#
# Run ON the Firecracker host (aimax), not locally:
#   sudo bash scripts/images/bake-ubuntu-26.04-image.sh
#
# Output: /opt/fc/images/ubuntu-26.04.ext4 -- selected per-VM via
# boxctl-vms's image=ubuntu-26.04 (see internal/vm/service.go's Create).
set -euo pipefail

T0=$(date +%s)
say() { echo "[bake-ubuntu-26.04 +$(($(date +%s) - T0))s] $*"; }

SUITE="resolute"
IMAGE_DIR="/opt/fc/images"
IMAGE="$IMAGE_DIR/ubuntu-26.04.ext4"
# Built here, not at $IMAGE directly, so a concurrent `onctl create` never
# copies a half-built/mid-truncate rootfs -- same reasoning as
# onctl-runners' bake-fc-image.sh, which this mirrors.
BUILD_IMAGE="$IMAGE.building"
MOUNT="/mnt/fc-rootfs-ubuntu-26.04"
CACHE_DIR="/opt/fc/cache"
mkdir -p "$CACHE_DIR"

say "installing build deps"
export DEBIAN_FRONTEND=noninteractive
export LC_ALL=C.UTF-8 LANG=C.UTF-8
unset LANGUAGE
apt-get update -qq
apt-get install -y -qq debootstrap e2fsprogs >/dev/null

say "creating empty ext4 image (4096M -- same as debian-slim: a minimal Ubuntu base + openssh installs to well under 1G, leaving the rest for what the user installs; medium/large boxes grow it at create time anyway)"
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
# The bake host's debootstrap may predate resolute (the onctl-4 runners and
# aimax are both on 24.04, whose debootstrap only learns new suites via
# updates). Every Ubuntu suite script is a symlink to "gutsy" anyway, so
# name it explicitly when this debootstrap has no script of its own for
# resolute rather than failing with "No such script".
DEBOOTSTRAP_SCRIPT=()
if [ ! -e "/usr/share/debootstrap/scripts/$SUITE" ]; then
  say "this debootstrap has no $SUITE script -- using gutsy (what every Ubuntu suite script links to)"
  DEBOOTSTRAP_SCRIPT=(/usr/share/debootstrap/scripts/gutsy)
fi
# Same mirror-probe as bake-desktop-image.sh: mirror.hetzner.com is only
# reachable from inside Hetzner's own network, and this script also runs
# on aimax (a homelab box on a different network), where it hangs instead
# of erroring -- probe first rather than hardcoding it.
HETZNER_MIRROR="http://mirror.hetzner.com/ubuntu/packages/"
FALLBACK_MIRROR="http://archive.ubuntu.com/ubuntu/"
if curl -fsS --connect-timeout 3 --max-time 5 -o /dev/null "${HETZNER_MIRROR}dists/$SUITE/InRelease" 2>/dev/null; then
  MIRROR="$HETZNER_MIRROR"
else
  say "mirror.hetzner.com unreachable from this host -- falling back to archive.ubuntu.com"
  MIRROR="$FALLBACK_MIRROR"
fi
# The box itself should get updates from the public archive regardless of
# where it was baked -- a box on aimax can't reach Hetzner's mirror either.
BOX_MIRROR="http://archive.ubuntu.com/ubuntu/"
SECURITY_MIRROR="http://security.ubuntu.com/ubuntu/"
DEBOOTSTRAP_CACHE="$CACHE_DIR/debootstrap-$SUITE-minbase.tar"
if [ ! -s "$DEBOOTSTRAP_CACHE" ]; then
  say "debootstrap Ubuntu 26.04 ($SUITE) minbase: fetching package set into cache"
  DEBOOTSTRAP_SCRATCH=$(mktemp -d)
  debootstrap "${DEBOOTSTRAP_ARGS[@]}" --make-tarball="$DEBOOTSTRAP_CACHE" "$SUITE" "$DEBOOTSTRAP_SCRATCH" "$MIRROR" "${DEBOOTSTRAP_SCRIPT[@]}"
  rm -rf "$DEBOOTSTRAP_SCRATCH"
fi
say "debootstrap Ubuntu 26.04 ($SUITE) minbase (from cached package tarball)"
debootstrap "${DEBOOTSTRAP_ARGS[@]}" --unpack-tarball="$DEBOOTSTRAP_CACHE" "$SUITE" "$MOUNT" "$MIRROR" "${DEBOOTSTRAP_SCRIPT[@]}"

say "writing apt sources (deb822, Ubuntu's own format since 24.04; main+universe, plus updates and security so an in-VM apt-get upgrade has patches to pull)"
rm -f "$MOUNT/etc/apt/sources.list"
mkdir -p "$MOUNT/etc/apt/sources.list.d"
cat <<EOF > "$MOUNT/etc/apt/sources.list.d/ubuntu.sources"
Types: deb
URIs: $BOX_MIRROR
Suites: $SUITE $SUITE-updates
Components: main universe
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg

Types: deb
URIs: $SECURITY_MIRROR
Suites: $SUITE-security
Components: main universe
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
EOF

say "resetting baked-in hostname (debootstrap leaks the host's own hostname)"
echo boxctl-ubuntu > "$MOUNT/etc/hostname"
grep -q '^127\.0\.1\.1' "$MOUNT/etc/hosts" 2>/dev/null || echo -e '127.0.1.1\tboxctl-ubuntu' >> "$MOUNT/etc/hosts"

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
# "<sha256> <size> <mtime-nanos>" -- see bake-debian-slim-image.sh for why
# this has to be written here, at the moment the image lands.
IMAGE_SHA=$(sha256sum "$IMAGE" | awk '{print $1}')
printf '%s %s %s\n' "$IMAGE_SHA" "$(stat -c %s "$IMAGE")" "$(date -r "$IMAGE" +%s%N)" > "$IMAGE.sha256sum"
say "sha256: $IMAGE_SHA (boxctl-image publish puts this in the R2 object name)"
# Disarm the EXIT trap now that everything it would try to unmount already
# is -- see onctl-runners' bake-fc-image.sh for why leaving it armed here
# makes the whole script exit non-zero even after a fully successful build.
trap - EXIT

say "done -- minimal Ubuntu 26.04 LTS ($SUITE) baked into $IMAGE"
echo ""
echo "Select it per-VM via boxctl-vms's image=ubuntu-26.04 (internal/vm/service.go),"
echo "or test directly with:"
echo "  onctl create -n ubuntutest -p fc --rootfs-image $IMAGE"
