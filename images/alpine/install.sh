#!/bin/sh
# images/common/install.sh for Alpine: the same contract (onctl's fc
# provider's and boxctl's, see docs/plans/dockerfile-rootfs.md) met with
# busybox init and OpenRC instead of systemd. Run by the Dockerfile with
# this directory at /boxctl and images/common at /boxctl-common.
set -eu

# e2fsprogs-extra is where Alpine keeps resize2fs, which growing a box's
# disk uses. bash because onctl's apply scripts (`onctl create -a`) are
# bash scripts.
apk add --no-cache openrc openssh-server chrony e2fsprogs-extra bash ca-certificates

# Alpine's own inittab puts gettys on tty1-6, which a microVM doesn't
# have, and none on the serial console, which is the only one it does.
install -m 0644 /boxctl/inittab /etc/inittab

# docs/plans/guest-clock-sync.md. Alpine's stock chrony.conf has no
# confdir, an initstepslew that waits on DNS at boot, and rtcsync for an
# RTC Firecracker doesn't emulate.
cat >/etc/chrony/chrony.conf <<'CONF'
pool pool.ntp.org iburst
driftfile /var/lib/chrony/chrony.drift
cmdport 0
confdir /etc/chrony/conf.d
CONF
install -D -m 0644 /boxctl-common/boxctl-resume.conf /etc/chrony/conf.d/boxctl-resume.conf
# chronyd's init script needs "net", which would start the networking
# service to configure interfaces. The kernel command line already
# brought eth0 up, and there is no /etc/network/interfaces for it to read.
echo 'rc_need="!net"' >>/etc/conf.d/chronyd

# devfs mounts /dev/pts, which sshd needs for a terminal; hostname applies
# the /etc/hostname onctl writes into each box's copy; loopback brings up
# lo, which the kernel command line doesn't.
rc-update add devfs sysinit
rc-update add dmesg sysinit
rc-update add hostname boot
rc-update add loopback boot
rc-update add bootmisc boot
rc-update add sshd default
rc-update add chronyd default
rc-update add killprocs shutdown
rc-update add mount-ro shutdown

# No SSH host keys baked in (images/common/install.sh says why): Alpine's
# sshd init script generates the missing ones each time it starts, so a
# box makes its own on first boot.
rm -f /etc/ssh/ssh_host_*
