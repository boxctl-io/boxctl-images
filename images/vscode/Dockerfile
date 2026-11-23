# desktop plus VS Code and Python 3 with pip, for coding over VNC -- with
# the desktop running as a regular user, boxctl (uid 1000, passwordless
# sudo), rather than root: VS Code (Electron) restricts its sandbox under
# root, and XFCE's dbus and polkit pieces misbehave there. Root's SSH
# login is unchanged -- the terminal tunnel always connects as root -- and
# the VNC and SSH tunnels already grant root at the same trust boundary,
# so the sudo rule gives nothing away.
FROM desktop

# Ubuntu marks its Python as externally managed (PEP 668), so a bare `pip
# install` fails. A box is a single-purpose sandbox, not a system whose
# Python apt owns, so pip.conf lifts that rather than every user finding
# --break-system-packages for themselves.
COPY pip.conf /etc/pip.conf
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y python3 python3-pip python3-venv \
 && apt-get clean

# VS Code from Microsoft's apt repository. The debconf answer stops its
# package adding a second copy of the same repository on install.
COPY vscode.list /etc/apt/sources.list.d/
RUN install -d -m 0755 /etc/apt/keyrings \
 && curl -fsSL -o /etc/apt/keyrings/packages.microsoft.asc https://packages.microsoft.com/keys/microsoft.asc \
 && echo 'code code/add-microsoft-repo boolean false' | debconf-set-selections \
 && apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y code \
 && apt-get clean

# No password: passwordless sudo and the ticket-gated tunnels are the
# access control, so there's no login prompt this account ever answers.
# Docker's Ubuntu image has an `ubuntu` user at uid 1000, the uid the
# unit's /run/user/1000 is for, so that user goes first.
COPY --chmod=0440 sudoers /etc/sudoers.d/90-boxctl
RUN if id -u ubuntu >/dev/null 2>&1; then userdel -r ubuntu; fi \
 && useradd -m -u 1000 -s /bin/bash -G sudo boxctl \
 && passwd -l boxctl \
 && install -d -o boxctl -g boxctl /home/boxctl/.vnc \
 && install -o boxctl -g boxctl -m 0755 /root/.vnc/xstartup /home/boxctl/.vnc/xstartup \
 && visudo -cf /etc/sudoers.d/90-boxctl

# desktop's unit, run as boxctl instead of root. Same path, so the
# vncserver@1 link desktop enabled starts this one.
COPY vncserver@.service /etc/systemd/system/
