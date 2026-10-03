#!/bin/sh
# What every box rootfs needs, whatever else its image adds -- run by each
# images/<name>/Dockerfile as its first step, with this directory mounted
# at /boxctl:
#
#   RUN --mount=from=common,target=/boxctl sh /boxctl/install.sh
#
# The contract it meets is onctl's fc provider's and boxctl's (see
# docs/plans/dockerfile-rootfs.md): systemd as init, sshd enabled for
# root key logins (onctl writes /root/.ssh/authorized_keys into each box's
# copy), chrony against /dev/ptp0 so a resumed box's clock catches up, and
# no network config of its own -- the kernel command line brings up eth0.
# The files Docker manages during a build (/etc/hostname, /etc/hosts,
# /etc/resolv.conf) are written after export by `boxctl-image build`,
# since anything written to them here wouldn't survive.
#
# Debian and Ubuntu only: both are apt-based, and both are what the old
# debootstrap bakes installed (bake-debian-slim-image.sh's package list).
set -eu

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends \
  systemd systemd-sysv udev openssh-server chrony sudo \
  curl ca-certificates iproute2 iputils-ping netbase

# docs/plans/guest-clock-sync.md
install -D -m 0644 /boxctl/boxctl-resume.conf /etc/chrony/conf.d/boxctl-resume.conf
# Stock chrony.conf on Debian/Ubuntu reads conf.d already; don't assume it.
grep -q '^confdir /etc/chrony/conf.d' /etc/chrony/chrony.conf \
  || echo 'confdir /etc/chrony/conf.d' >>/etc/chrony/chrony.conf

# SSH host keys are made on each box's first boot, never baked: keys baked
# into an image are shared by every box booted from it, so any one box
# could impersonate all the others to a client that had seen it.
install -m 0644 /boxctl/boxctl-ssh-host-keys.service /etc/systemd/system/
rm -f /etc/ssh/ssh_host_*

systemctl enable ssh chrony boxctl-ssh-host-keys
# Static nameservers instead (written after export); resolved would
# replace them with a stub nothing configures.
systemctl mask systemd-resolved

apt-get clean
