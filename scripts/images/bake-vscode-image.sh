#!/usr/bin/env bash
# Bakes a clean Firecracker rootfs with an XFCE desktop + TigerVNC server +
# Firefox + VS Code + Python3/pip, for browser-based GUI coding sessions on
# a boxctl VM -- see docs/plans/gui-desktop-vnc-access.md for the VNC
# tunnel this rides on. Borrows bake-desktop-image.sh's base recipe
# (debootstrap Ubuntu 24.04 + XFCE + TigerVNC + Firefox, baked in and
# enabled so the desktop is already running at first boot) -- see that
# script's header for why this is baked once rather than applied
# post-boot.
#
# Deliberately does NOT run the desktop (VNC) or VS Code as root, unlike
# bake-desktop-image.sh's root-only setup: VS Code (an Electron app) warns
# about / restricts running as root's sandbox, and several XFCE session
# components (dbus, polkit) misbehave under root too. Instead this creates
# a regular sudo-capable user ("boxctl", uid 1000, NOPASSWD sudo -- the VNC
# tunnel and SSH already grant root-equivalent access at the same trust
# boundary, so this isn't a downgrade) and runs the VNC server, and
# everything launched from the desktop including VS Code, as that user.
# Root SSH stays exactly as every other boxctl image has it (untouched) --
# the terminal tunnel (cmd/boxctl-tunnel-agent) always connects as
# root@<vm-ip> regardless of image, so this doesn't touch sshd_config or
# root's own access at all.
#
# Run ON the Firecracker host (aimax), not locally:
#   sudo bash scripts/images/bake-vscode-image.sh
#
# Output: /opt/fc/images/vscode.ext4 -- selected per-VM via boxctl-vms's
# image=vscode (see internal/vm/service.go's Create), not aimax's default
# fc.rootfsImage.
set -euo pipefail

T0=$(date +%s)
say() { echo "[bake-vscode +$(($(date +%s) - T0))s] $*"; }

IMAGE_DIR="/opt/fc/images"
IMAGE="$IMAGE_DIR/vscode.ext4"
# Built here, not at $IMAGE directly, so a concurrent `onctl create` never
# copies a half-built/mid-truncate rootfs -- same reasoning as
# bake-desktop-image.sh, which this mirrors.
BUILD_IMAGE="$IMAGE.building"
MOUNT="/mnt/fc-rootfs-vscode"
CACHE_DIR="/opt/fc/cache"
BOXCTL_USER="boxctl"
BOXCTL_UID=1000
mkdir -p "$CACHE_DIR"

say "installing build deps"
export DEBIAN_FRONTEND=noninteractive
export LC_ALL=C.UTF-8 LANG=C.UTF-8
unset LANGUAGE
apt-get update -qq
apt-get install -y -qq debootstrap e2fsprogs >/dev/null

say "creating empty ext4 image (8000M -- Ubuntu 24.04 + XFCE + TigerVNC + VS Code + Python installs to well under half that)"
mkdir -p "$IMAGE_DIR"
truncate -s 8000M "$BUILD_IMAGE"
mkfs.ext4 -q -F "$BUILD_IMAGE"

say "mounting image"
mkdir -p "$MOUNT"
mount -o loop "$BUILD_IMAGE" "$MOUNT"
trap 'umount "$MOUNT" 2>/dev/null || true' EXIT

DEBOOTSTRAP_ARGS=(
  --variant=minbase
  --include=systemd,systemd-sysv,udev,openssh-server,curl,ca-certificates,iproute2,iputils-ping,sudo,netbase,tmux
)
# Same mirror-probe as bake-desktop-image.sh: mirror.hetzner.com is only
# reachable from inside Hetzner's own network, and this script also runs
# on aimax (a homelab box on a different network), where it hangs instead
# of erroring -- probe first rather than hardcoding it.
HETZNER_MIRROR="http://mirror.hetzner.com/ubuntu/packages/"
FALLBACK_MIRROR="http://archive.ubuntu.com/ubuntu/"
if curl -fsS --connect-timeout 3 --max-time 5 -o /dev/null "${HETZNER_MIRROR}dists/noble/InRelease" 2>/dev/null; then
  MIRROR="$HETZNER_MIRROR"
else
  say "mirror.hetzner.com unreachable from this host -- falling back to archive.ubuntu.com"
  MIRROR="$FALLBACK_MIRROR"
fi
# A dedicated cache key, not shared with bake-desktop-image.sh's
# debootstrap-noble-desktop.tar -- see that script's own comment on why a
# tarball's --include list must match the script reusing it; keeping a
# separate cache per bake script avoids assuming the two never diverge.
DEBOOTSTRAP_CACHE="$CACHE_DIR/debootstrap-noble-vscode.tar"
if [ ! -s "$DEBOOTSTRAP_CACHE" ]; then
  say "debootstrap Ubuntu 24.04 (noble) minbase: fetching package set into cache"
  DEBOOTSTRAP_SCRATCH=$(mktemp -d)
  debootstrap "${DEBOOTSTRAP_ARGS[@]}" --make-tarball="$DEBOOTSTRAP_CACHE" noble "$DEBOOTSTRAP_SCRATCH" "$MIRROR"
  rm -rf "$DEBOOTSTRAP_SCRATCH"
fi
say "debootstrap Ubuntu 24.04 (noble) minbase (from cached package tarball)"
debootstrap "${DEBOOTSTRAP_ARGS[@]}" --unpack-tarball="$DEBOOTSTRAP_CACHE" noble "$MOUNT" "$MIRROR"

say "enabling universe (xfce4/tigervnc-standalone-server live there; debootstrap only enables main by default)"
cat <<EOF > "$MOUNT/etc/apt/sources.list"
deb $MIRROR noble main universe
deb $MIRROR noble-updates main universe
EOF

say "resetting baked-in hostname (debootstrap leaks the host's own hostname)"
echo boxctl-vscode > "$MOUNT/etc/hostname"
grep -q '^127\.0\.1\.1' "$MOUNT/etc/hosts" 2>/dev/null || echo -e '127.0.1.1\tboxctl-vscode' >> "$MOUNT/etc/hosts"

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

say "installing XFCE desktop + TigerVNC (same package set as bake-desktop-image.sh)"
chroot "$MOUNT" apt-get update -qq
chroot "$MOUNT" apt-get install -y -qq --no-install-recommends apt-utils >/dev/null
chroot "$MOUNT" apt-get install -y -qq xfce4 xfce4-terminal dbus-x11 tigervnc-standalone-server >/dev/null

# Ubuntu 24.04's own "firefox" package is a transitional stub that pulls
# the snap -- snapd's squashfs-mount + confinement machinery is a poor
# fit for a minimal debootstrap rootfs booted as a Firecracker microVM,
# so this installs real Firefox from Mozilla's own apt repo instead (same
# as bake-desktop-image.sh -- see that script's comment for the rationale
# and the officially documented snap-free install path,
# https://support.mozilla.org/kb/install-firefox-linux). The pin file
# guarantees apt keeps preferring this package over Ubuntu's snap
# wrapper on every future `apt upgrade`, not just this one install.
say "installing Firefox (Mozilla's own apt repo, not Ubuntu's snap-wrapper package)"
mkdir -p "$MOUNT/etc/apt/keyrings"
curl -fsSL https://packages.mozilla.org/apt/repo-signing-key.gpg -o "$MOUNT/etc/apt/keyrings/packages.mozilla.org.asc"
cat <<'EOF' > "$MOUNT/etc/apt/sources.list.d/mozilla.list"
deb [signed-by=/etc/apt/keyrings/packages.mozilla.org.asc] https://packages.mozilla.org/apt mozilla main
EOF
cat <<'EOF' > "$MOUNT/etc/apt/preferences.d/mozilla"
Package: *
Pin: origin packages.mozilla.org
Pin-Priority: 1000
EOF
chroot "$MOUNT" apt-get update -qq
chroot "$MOUNT" apt-get install -y -qq firefox >/dev/null

say "installing Python 3 + pip"
chroot "$MOUNT" apt-get install -y -qq python3 python3-pip python3-venv >/dev/null
# Ubuntu 24.04 marks the system Python as "externally managed" (PEP 668),
# so a bare `pip install` fails with "error: externally-managed-environment"
# by default. This is a single-purpose sandbox VM, not a shared system
# whose Python apt owns, so that protection doesn't buy anything here --
# lift it globally rather than making every user discover
# --break-system-packages (or set up a venv) themselves.
cat <<'EOF' > "$MOUNT/etc/pip.conf"
[global]
break-system-packages = true
EOF

say "installing VS Code (Microsoft's own apt repo)"
mkdir -p "$MOUNT/etc/apt/keyrings"
curl -fsSL https://packages.microsoft.com/keys/microsoft.asc -o "$MOUNT/etc/apt/keyrings/packages.microsoft.asc"
cat <<'EOF' > "$MOUNT/etc/apt/sources.list.d/vscode.list"
deb [arch=amd64 signed-by=/etc/apt/keyrings/packages.microsoft.asc] https://packages.microsoft.com/repos/code stable main
EOF
chroot "$MOUNT" apt-get update -qq
chroot "$MOUNT" apt-get install -y -qq code >/dev/null

say "creating regular desktop user ($BOXCTL_USER, uid $BOXCTL_UID, NOPASSWD sudo)"
chroot "$MOUNT" useradd -m -u "$BOXCTL_UID" -s /bin/bash -G sudo "$BOXCTL_USER"
# No password set -- NOPASSWD sudo below plus the ticket-gated VNC/SSH
# tunnels are the actual access control here (same trust boundary root SSH
# already has on every other boxctl image), so there's no login prompt
# this account ever needs to answer.
chroot "$MOUNT" passwd -l "$BOXCTL_USER" >/dev/null
cat <<EOF > "$MOUNT/etc/sudoers.d/90-$BOXCTL_USER"
$BOXCTL_USER ALL=(ALL) NOPASSWD:ALL
EOF
chmod 0440 "$MOUNT/etc/sudoers.d/90-$BOXCTL_USER"

say "installing chrony (what puts a resumed box's clock back -- see docs/plans/guest-clock-sync.md)"
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
# is right again a few seconds later.
#
# trust and prefer are what make that true -- found live, pausing a box
# for 90s and resuming it on the host: without them it was still 91s
# behind half a minute later. After a resume every NTP source is still
# holding samples from *before* the pause, which agree with the stale
# clock, so they outvote the one source that knows the real time and
# chrony rejects PHC0 as a falseticker. trust stops a non-trusted source
# from outvoting it. prefer makes chrony steer by it at all on Ubuntu,
# whose own pools (sources.d/ubuntu-ntp-pools.sources) are marked prefer
# and would otherwise always win selection over it.
refclock PHC /dev/ptp0 poll 0 dpoll -2 prefer trust

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

say "configuring VNC for $BOXCTL_USER, not root (see this script's header)"
mkdir -p "$MOUNT/home/$BOXCTL_USER/.vnc"
cat <<'XSTARTUP' > "$MOUNT/home/$BOXCTL_USER/.vnc/xstartup"
#!/bin/bash
unset SESSION_MANAGER
unset DBUS_SESSION_BUS_ADDRESS
exec startxfce4
XSTARTUP
chmod +x "$MOUNT/home/$BOXCTL_USER/.vnc/xstartup"
chroot "$MOUNT" chown -R "$BOXCTL_USER:$BOXCTL_USER" "/home/$BOXCTL_USER/.vnc"

# Same -localhost no / -SecurityTypes None reasoning as
# bake-desktop-image.sh's unit (see that script's header comment for the
# live-tested findings behind both flags) -- just running as $BOXCTL_USER
# instead of root. There's no display manager / PAM login session here
# (systemd launches vncserver directly), so XDG_RUNTIME_DIR is created
# explicitly before startxfce4's dbus/polkit need it -- the two
# ExecStartPre lines need their leading "+" (run as root, overriding this
# unit's own User=) because /run/user is root-owned/non-writable by other
# users; without it, systemd runs ExecStartPre as $BOXCTL_USER same as the
# main process, and `mkdir /run/user/$BOXCTL_UID` fails with "Permission
# denied" -- confirmed live (crash-looped: "Start request repeated too
# quickly").
cat <<UNIT > "$MOUNT/etc/systemd/system/vncserver@.service"
[Unit]
Description=TigerVNC server on display %i
After=network.target

[Service]
Type=simple
User=$BOXCTL_USER
Group=$BOXCTL_USER
WorkingDirectory=/home/$BOXCTL_USER
Environment=HOME=/home/$BOXCTL_USER
Environment=USER=$BOXCTL_USER
Environment=XDG_RUNTIME_DIR=/run/user/$BOXCTL_UID
ExecStartPre=+/bin/mkdir -p /run/user/$BOXCTL_UID
ExecStartPre=+/bin/chown $BOXCTL_USER:$BOXCTL_USER /run/user/$BOXCTL_UID
ExecStart=/usr/bin/vncserver -fg -localhost no -SecurityTypes None --I-KNOW-THIS-IS-INSECURE :%i
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
# is -- see bake-desktop-image.sh for why leaving it armed here makes the
# whole script exit non-zero even after a fully successful build.
trap - EXIT

say "done -- XFCE desktop + TigerVNC (as $BOXCTL_USER) + Firefox + VS Code + Python3/pip baked into $IMAGE"
echo ""
echo "Test directly (before boxctl-vms's image= plumbing exists) with:"
echo "  onctl create -n vscodetest -p fc --rootfs-image $IMAGE"
