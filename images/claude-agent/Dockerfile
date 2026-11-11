# debian-slim plus the tools a coding agent reaches for on its first
# command -- what boxctl's Claude Code plugin (boxctl/claude-plugin)
# boots, so `git clone`, `rg` and `jq` work without an apt-get first --
# and what `boxctl claude` runs Claude Code itself in
# (docs/plans/claude-on-the-box.md): gh and tmux, and the box side of
# GitHub credential forwarding.
#
# Not a project's language toolchain (build-essential, a JDK, a pinned
# Node): those vary per project, and `apt-get install` on a box is quick.
# What is here beyond the basics is what the agent itself uses on every
# run, whatever the project -- see "Evidence tools" below.
#
# Claude Code is baked in (Anthropic's installer, below) so a new box
# doesn't spend its first minute downloading it; `boxctl claude` skips
# its own install when `claude` is already there, and the installed copy
# keeps itself updated.
#
# Also published as an OCI image, ghcr.io/boxctl-io/claude-agent (amd64 and
# arm64; .github/workflows/publish-oci-images.yml), for onctl's ac
# provider, which runs it as an Apple container.
FROM debian-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      git openssh-client rsync jq ripgrep make \
      procps less file unzip xz-utils \
      gh tmux curl ca-certificates socat \
 && apt-get clean

# Evidence tools: what an unattended run (`boxctl claude --goal`) needs to
# show its result, not just describe it -- screenshots and a video of a
# web page (Playwright driving headless Chromium), a terminal recording
# (asciinema, turned into a GIF by agg) -- plus python3, which agents
# reach for as a scripting tool so often that its absence was the first
# thing every run tripped over. Baked in because each run otherwise
# installs all of it again: about three minutes and 950 MB per box.
#
# Node and npm are Debian's (Playwright needs a Node; a project that pins
# its own still installs its own). Playwright is pinned, with only
# Chromium's headless shell: the browser build must match the package's
# version, so a script should use this copy rather than `npm i playwright`
# (a newer one would download its own browser). /root/node_modules points
# at the global modules so `import "playwright"` resolves from anywhere
# under /root, ESM included -- NODE_PATH only helps require(). Playwright
# brings its own ffmpeg for video; Debian's (another 420 MB) is left out.
#
# agg from its release binary, pinned by sha256 per architecture like Go
# in claude-agent-go (TARGETARCH is buildx's). asciinema refuses to run
# without a UTF-8 locale, and a Dockerfile's ENV doesn't survive into the
# rootfs, so /etc/default/locale sets one for sessions that read it;
# anything else passes LC_ALL=C.UTF-8 itself.
ARG TARGETARCH
ARG PLAYWRIGHT_VERSION=1.65.0
ARG AGG_VERSION=1.9.0
ARG AGG_SHA256_amd64=f111e315cd71056b116302342553dd765b7297579ed511f111d0cedb442aeda6
ARG AGG_SHA256_arm64=2b4be407b97e00e1c313a41d154ced8fa3d02c560c8f47a0db4950a2576444c9
RUN apt-get update \
 && apt-get install -y --no-install-recommends python3 asciinema nodejs npm \
 && case "$TARGETARCH" in amd64) agg=x86_64 sha=$AGG_SHA256_amd64;; arm64) agg=aarch64 sha=$AGG_SHA256_arm64;; *) echo "no agg checksum for $TARGETARCH" >&2; exit 1;; esac \
 && curl -fsSL -o /usr/local/bin/agg "https://github.com/asciinema/agg/releases/download/v${AGG_VERSION}/agg-${agg}-unknown-linux-gnu" \
 && echo "${sha}  /usr/local/bin/agg" | sha256sum -c - \
 && chmod 0755 /usr/local/bin/agg \
 && npm install -g "playwright@${PLAYWRIGHT_VERSION}" \
 && playwright install --with-deps --only-shell chromium \
 && ln -s "$(npm root -g)" /root/node_modules \
 && echo 'LANG=C.UTF-8' > /etc/default/locale \
 && npm cache clean --force \
 && apt-get clean \
 && agg --version && python3 --version && LC_ALL=C.UTF-8 asciinema --version \
 && node -e 'require("/root/node_modules/playwright").chromium.launch().then(b => b.close())'

# Claude Code, in /root/.local/bin -- where boxctl claude puts /root/.local/bin
# on PATH. Not on the image's own PATH: only boxctl's sessions need it.
RUN curl -fsSL https://claude.ai/install.sh | bash \
 && /root/.local/bin/claude --version

# GitHub credentials forwarded from the attached machine
# (git-credential-boxctl, gh). git asks the helper for github.com over
# https; ssh-style GitHub remotes -- what a repo copied from a laptop
# usually has -- are rewritten to https so they go through it too, since
# the box has no SSH key for GitHub.
COPY --chmod=0755 git-credential-boxctl gh /usr/local/bin/
# boxctl-status: a run's reports to the control plane, over the box's
# vsock device (docs/plans/run-status.md) -- hence socat, which speaks
# AF_VSOCK where the shell can't.
COPY --chmod=0755 boxctl-status /usr/local/bin/
COPY boxctl-forwarding.conf /etc/ssh/sshd_config.d/
RUN mkdir -p -m 0700 /root/.boxctl \
 && git config --system credential.https://github.com.helper boxctl \
 && git config --system url.https://github.com/.insteadOf git@github.com: \
 && git config --system --add url.https://github.com/.insteadOf ssh://git@github.com/
