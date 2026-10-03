# debian-slim plus the tools a coding agent reaches for on its first
# command -- what boxctl's Claude Code plugin (boxctl/claude-plugin)
# boots, so `git clone`, `rg` and `jq` work without an apt-get first --
# and what `boxctl claude` runs Claude Code itself in
# (docs/plans/claude-on-the-box.md): gh and tmux, and the box side of
# GitHub credential forwarding.
#
# Deliberately not language toolchains (python3, build-essential, node):
# those vary per project, and `apt-get install` on a box is quick. Nor
# Claude Code: `boxctl claude` installs it with Anthropic's installer at
# first use, rather than this image redistributing it.
FROM debian-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      git openssh-client rsync jq ripgrep make \
      procps less file unzip xz-utils \
      gh tmux \
 && apt-get clean

# GitHub credentials forwarded from the attached machine
# (git-credential-boxctl, gh). git asks the helper for github.com over
# https; ssh-style GitHub remotes -- what a repo copied from a laptop
# usually has -- are rewritten to https so they go through it too, since
# the box has no SSH key for GitHub.
COPY --chmod=0755 git-credential-boxctl gh /usr/local/bin/
COPY boxctl-forwarding.conf /etc/ssh/sshd_config.d/
RUN mkdir -p -m 0700 /root/.boxctl \
 && git config --system credential.https://github.com.helper boxctl \
 && git config --system url.https://github.com/.insteadOf git@github.com: \
 && git config --system --add url.https://github.com/.insteadOf ssh://git@github.com/
