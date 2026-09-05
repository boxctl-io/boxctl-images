#!/usr/bin/env bash
# Bakes a clean, minimal Firecracker rootfs with an XFCE desktop + TigerVNC
# server baked in, for browser-based GUI access to a boxctl VM -- see
# docs/plans/gui-desktop-vnc-access.md. Deliberately does NOT start from
# onctl-runners' runner-base-docker.ext4 (the image boxctl VMs currently
# share with that project): a GUI desktop VM has no use for the GH Actions
# runner/Docker tooling baked into that image, so this builds a lean image
# from scratch instead, matching onctl-runners' own fc-host/scripts/
# bake-fc-image.sh in approach but not in output.
#
# The VNC server + systemd unit are baked directly into the image, not
# applied post-boot via onctl-templates' desktop/xfce-vnc.sh -- that
# apply-file script's ~300-package XFCE install takes several minutes on
# every single VM create, which is fine for testing the recipe once but
# not something an interactive "give me a desktop" user should ever wait
# through. This script bakes the exact same, already-live-tested package
# set and systemd unit (Type=simple + vncserver -fg, not Type=forking +
# PIDFile -- that combination was flaky under systemd, confirmed live: see
# that script's git history) once, here, so every VM created from the
# resulting image boots with the desktop already running.
#
# Run ON the Firecracker host (aimax), not locally:
#   sudo bash scripts/images/bake-desktop-image.sh
#
# Output: /opt/fc/images/desktop.ext4 -- selected per-VM via boxctl-vms's
# image=desktop (see internal/vm/service.go's Create), not aimax's default
# fc.rootfsImage (which stays runner-base-docker.ext4 for everyone who
# doesn't ask for a desktop).
set -euo pipefail

T0=$(date +%s)
say() { echo "[bake-desktop +$(($(date +%s) - T0))s] $*"; }

IMAGE_DIR="/opt/fc/images"
IMAGE="$IMAGE_DIR/desktop.ext4"
# Built here, not at $IMAGE directly, so a concurrent `onctl create` never
# copies a half-built/mid-truncate rootfs -- same reasoning as
# onctl-runners' bake-fc-image.sh, which this mirrors.
BUILD_IMAGE="$IMAGE.building"
MOUNT="/mnt/fc-rootfs-desktop"
CACHE_DIR="/opt/fc/cache"
mkdir -p "$CACHE_DIR"

say "installing build deps"
export DEBIAN_FRONTEND=noninteractive
export LC_ALL=C.UTF-8 LANG=C.UTF-8
unset LANGUAGE
apt-get update -qq
apt-get install -y -qq debootstrap e2fsprogs >/dev/null

say "creating empty ext4 image (8000M -- Ubuntu 24.04 + XFCE + TigerVNC installs to well under half that; this image has no build-tool cache workload, unlike onctl-runners' 20000M runner images)"
mkdir -p "$IMAGE_DIR"
truncate -s 8000M "$BUILD_IMAGE"
mkfs.ext4 -q -F "$BUILD_IMAGE"

say "mounting image"
mkdir -p "$MOUNT"
mount -o loop "$BUILD_IMAGE" "$MOUNT"
trap 'umount "$MOUNT" 2>/dev/null || true' EXIT

DEBOOTSTRAP_ARGS=(
  --variant=minbase
  --include=systemd,systemd-sysv,udev,openssh-server,curl,ca-certificates,iproute2,iputils-ping,sudo,netbase
)
# Same mirror-probe as onctl-runners' bake-fc-image.sh: mirror.hetzner.com
# is only reachable from inside Hetzner's own network, and this script
# also runs on aimax (a homelab box on a different network), where it
# hangs instead of erroring -- probe first rather than hardcoding it.
HETZNER_MIRROR="http://mirror.hetzner.com/ubuntu/packages/"
FALLBACK_MIRROR="http://archive.ubuntu.com/ubuntu/"
if curl -fsS --connect-timeout 3 --max-time 5 -o /dev/null "${HETZNER_MIRROR}dists/noble/InRelease" 2>/dev/null; then
  MIRROR="$HETZNER_MIRROR"
else
  say "mirror.hetzner.com unreachable from this host -- falling back to archive.ubuntu.com"
  MIRROR="$FALLBACK_MIRROR"
fi
# A separate cache key from onctl-runners' debootstrap-noble.tar -- that
# tarball was fetched with a different --include list (jq, git added for
# runner use), and debootstrap's --unpack-tarball expects the include list
# it's given to match what --make-tarball actually fetched. Safer to keep
# a dedicated cache than assume the two lists stay interchangeable.
DEBOOTSTRAP_CACHE="$CACHE_DIR/debootstrap-noble-desktop.tar"
if [ ! -s "$DEBOOTSTRAP_CACHE" ]; then
  say "debootstrap Ubuntu 24.04 (noble) minbase: fetching package set into cache"
  DEBOOTSTRAP_SCRATCH=$(mktemp -d)
  debootstrap "${DEBOOTSTRAP_ARGS[@]}" --make-tarball="$DEBOOTSTRAP_CACHE" noble "$DEBOOTSTRAP_SCRATCH" "$MIRROR"
  rm -rf "$DEBOOTSTRAP_SCRATCH"
fi
say "debootstrap Ubuntu 24.04 (noble) minbase (from cached package tarball)"
debootstrap "${DEBOOTSTRAP_ARGS[@]}" --unpack-tarball="$DEBOOTSTRAP_CACHE" noble "$MOUNT" "$MIRROR"

say "enabling universe (xfce4/tigervnc-standalone-server live there; debootstrap only enables main by default)"
# Found live: passing --components=main,universe straight to debootstrap
# silently produced an image with NO /etc/apt/sources.list at all (every
# apt-get install then failed with "no installation candidate", even for
# apt-utils) -- writing it by hand after the fact, on top of the exact
# same plain debootstrap invocation onctl-runners' bake-fc-image.sh
# already proves works, sidesteps whatever that was.
cat <<EOF > "$MOUNT/etc/apt/sources.list"
deb $MIRROR noble main universe
deb $MIRROR noble-updates main universe
EOF

say "resetting baked-in hostname (debootstrap leaks the host's own hostname)"
echo boxctl-desktop > "$MOUNT/etc/hostname"
grep -q '^127\.0\.1\.1' "$MOUNT/etc/hosts" 2>/dev/null || echo -e '127.0.1.1\tboxctl-desktop' >> "$MOUNT/etc/hosts"

say "fixing DNS (mask systemd-resolved, use static nameservers)"
rm -f "$MOUNT/etc/resolv.conf"
printf 'nameserver 8.8.8.8\nnameserver 1.1.1.1\n' > "$MOUNT/etc/resolv.conf"
ln -sf /dev/null "$MOUNT/etc/systemd/system/systemd-resolved.service"

mount -t proc proc "$MOUNT/proc"
mount --bind /dev "$MOUNT/dev"
mount -t devpts devpts "$MOUNT/dev/pts"
mkdir -p "$CACHE_DIR/apt-archives" "$MOUNT/var/cache/apt/archives"
mount --bind "$CACHE_DIR/apt-archives" "$MOUNT/var/cache/apt/archives"
trap 'umount "$MOUNT/dev/pts" 2>/dev/null; umount "$MOUNT/dev" 2>/dev/null; umount "$MOUNT/proc" 2>/dev/null; umount "$MOUNT/var/cache/apt/archives" 2>/dev/null; umount "$MOUNT" 2>/dev/null || true' EXIT

say "installing XFCE desktop + TigerVNC (same package set as onctl-templates' desktop/xfce-vnc.sh)"
chroot "$MOUNT" apt-get update -qq
chroot "$MOUNT" apt-get install -y -qq --no-install-recommends apt-utils >/dev/null
chroot "$MOUNT" apt-get install -y -qq xfce4 xfce4-terminal dbus-x11 tigervnc-standalone-server >/dev/null

say "enabling SSH"
chroot "$MOUNT" systemctl enable ssh
# Pre-generate host keys so sshd starts immediately at boot instead of
# blocking on kernel entropy for fresh key generation.
chroot "$MOUNT" ssh-keygen -A -q

say "configuring VNC (baked in -- see this script's header comment)"
mkdir -p "$MOUNT/root/.vnc"
cat <<'XSTARTUP' > "$MOUNT/root/.vnc/xstartup"
#!/bin/bash
unset SESSION_MANAGER
unset DBUS_SESSION_BUS_ADDRESS
exec startxfce4
XSTARTUP
chmod +x "$MOUNT/root/.vnc/xstartup"

# -localhost yes + -SecurityTypes None: the VNC port is not reachable from
# outside the VM's own loopback -- an SSH port-forward (today) or
# boxctl-vms's ticket-gated tunnel (once built) is the actual access
# control, not a second password prompt in the VNC client.
cat <<'UNIT' > "$MOUNT/etc/systemd/system/vncserver@.service"
[Unit]
Description=TigerVNC server on display %i
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/root
ExecStart=/usr/bin/vncserver -fg -localhost yes -SecurityTypes None :%i
ExecStop=/usr/bin/vncserver -kill :%i
Restart=on-failure

[Install]
WantedBy=multi-user.target
UNIT
chroot "$MOUNT" systemctl enable vncserver@1.service

say "cleaning apt cache"
umount "$MOUNT/var/cache/apt/archives"
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
# Disarm the EXIT trap now that everything it would try to unmount already
# is -- see onctl-runners' bake-fc-image.sh for why leaving it armed here
# makes the whole script exit non-zero even after a fully successful build.
trap - EXIT

say "done -- XFCE desktop + TigerVNC baked into $IMAGE"
echo ""
echo "Test directly (before boxctl-vms's image= plumbing exists) with:"
echo "  onctl create -n desktoptest -p fc --rootfs-image $IMAGE"
