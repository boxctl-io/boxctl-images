# debian-slim plus the tools a coding agent reaches for on its first
# command -- what boxctl's Claude Code plugin (boxctl/claude-plugin)
# boots, so `git clone`, `rg` and `jq` work without an apt-get first.
# Deliberately not language toolchains (python3, build-essential, node):
# those vary per project, and `apt-get install` on a box is quick.
FROM debian-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      git openssh-client rsync jq ripgrep make \
      procps less file unzip xz-utils \
 && apt-get clean
