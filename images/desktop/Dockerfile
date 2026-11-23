# Ubuntu 24.04 with an XFCE desktop, TigerVNC and Firefox, the desktop
# already running at first boot -- the dashboard's VNC tunnel connects to
# it straight away, with no post-boot install to wait through (the XFCE
# install is about 300 packages and several minutes).
#
# 24.04, as the debootstrap bake this replaces was: moving the base to
# 26.04 is its own change, with the VNC path tested again on it.
FROM ubuntu:24.04@sha256:9c7f200c07e3d4a03e7249d2c16bf66a51120fb876aa2242cf31451412543289
RUN --mount=from=common,target=/boxctl sh /boxctl/install.sh

# Docker's Ubuntu image is minimized (no man pages or docs), as
# ubuntu-26.04 is; `unminimize` in a box restores them. Not run here:
# it reinstalls every package, and what that would change in the image
# hasn't been tested.

# With recommends, unlike install.sh's packages: the package set the
# debootstrap bake installed and the desktop was tested with.
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y \
      xfce4 xfce4-terminal dbus-x11 tigervnc-standalone-server \
 && apt-get clean

# Firefox from Mozilla's apt repository: Ubuntu's own firefox package is a
# stub that installs the snap, and snapd's squashfs mounts and confinement
# don't work in a box. The pin keeps apt preferring Mozilla's package over
# Ubuntu's on every later upgrade, not just this install
# (https://support.mozilla.org/kb/install-firefox-linux).
COPY mozilla.list /etc/apt/sources.list.d/
COPY mozilla.pref /etc/apt/preferences.d/mozilla
RUN install -d -m 0755 /etc/apt/keyrings \
 && curl -fsSL -o /etc/apt/keyrings/packages.mozilla.org.asc https://packages.mozilla.org/apt/repo-signing-key.gpg \
 && apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y firefox \
 && apt-get clean

# The desktop runs as root, on display :1 (port 5901). See the unit for
# why VNC listens on every address with no password.
COPY --chmod=0755 xstartup /root/.vnc/xstartup
COPY vncserver@.service /etc/systemd/system/
RUN systemctl enable vncserver@1.service

# lightdm comes in with xfce4's recommends. It's a login screen for a
# local display, and a box has none: it fails at every boot and leaves
# the system degraded. VNC starts the session itself, so nothing needs
# it. Masked rather than purged, so apt doesn't pull in another display
# manager to satisfy the recommend.
RUN systemctl mask lightdm
