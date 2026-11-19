# Ubuntu 24.04 (noble) with XFCE, TigerVNC and Firefox, so a box boots
# with the desktop already running. The package set and the vncserver
# unit are the bake script's, which was live-tested: Type=simple with
# `vncserver -fg` (Type=forking plus a PIDFile was flaky), and
# `-localhost no` because the tunnel dials the guest's own address.
# Omitting the flag is not enough: TigerVNC's wrapper defaults to
# localhost-only, and a box built that way refused the tunnel.
#
# Firefox is Mozilla's apt package. Ubuntu's `firefox` is a transitional
# stub that pulls the snap, and snapd's mounts don't belong in this
# rootfs. The pin keeps apt on Mozilla's package after later upgrades.
#
# Recommends stay on for this install. The bake did not pass
# `--no-install-recommends` here, and without them XFCE is missing the
# theme and icon packages that desktop was tested with. `--install-recommends`
# forces that even when the base image's apt config turns recommends off.
#
# SSH host keys are not baked. images/common/install.sh generates them
# on first boot. The bake ran `ssh-keygen -A`, so every box from that
# image shared one set of keys.
FROM ubuntu:24.04@sha256:73b999393d07f2bf65a40373778a44dc9a6d54ad7718d29247d8ff9413c4d6a8
RUN --mount=from=common,target=/boxctl sh /boxctl/install.sh

RUN install -d -m 0755 /etc/apt/keyrings \
 && curl -fsSL https://packages.mozilla.org/apt/repo-signing-key.gpg -o /etc/apt/keyrings/packages.mozilla.org.asc \
 && printf '%s\n' 'deb [signed-by=/etc/apt/keyrings/packages.mozilla.org.asc] https://packages.mozilla.org/apt mozilla main' > /etc/apt/sources.list.d/mozilla.list \
 && printf '%s\n' 'Package: *' 'Pin: origin packages.mozilla.org' 'Pin-Priority: 1000' > /etc/apt/preferences.d/mozilla \
 && apt-get update \
 && apt-get install -y --install-recommends \
      xfce4 xfce4-terminal dbus-x11 tigervnc-standalone-server firefox \
 && apt-get clean

COPY xstartup /root/.vnc/xstartup
COPY vncserver@.service /etc/systemd/system/vncserver@.service
RUN chmod 0755 /root/.vnc/xstartup \
 && systemctl enable vncserver@1.service
