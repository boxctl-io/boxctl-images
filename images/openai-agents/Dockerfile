# debian-slim with Node.js, the Codex CLI and the codex-set-key/
# codex-connect helpers already installed: ready to be the self-hosted
# environment of an OpenAI Agents API session as soon as it boots. A box
# from it skips the minute-long install the onctl template would otherwise
# do at create time, and doesn't depend on npm being reachable then.
#
# The Codex part isn't a second copy of that setup: the build runs the
# published onctl template itself (openai-agents/codex-executor.sh from
# templates.onctl.com), so a box from this image and a debian-slim box
# with the template applied end up identical, except that the image pins
# the Codex CLI where the template follows the moving @alpha tag. Pinned
# so a build is reproducible, and because @alpha can briefly point at a
# release whose Linux build isn't on npm yet -- npm then installs the
# wrapper without its binary and says nothing. Bumping the pin is a change
# to this file, which is what makes build-fc-images.yml rebuild.
FROM debian-slim
ARG CODEX_VERSION=0.161.0-alpha.5
# `codex --version` is a real check, not the template's closing echo: the
# wrapper installs fine without its platform binary, and only running it
# shows that.
RUN curl -fsSL https://templates.onctl.com/openai-agents/codex-executor.sh -o /tmp/codex-executor.sh \
 && CODEX_VERSION="$CODEX_VERSION" bash /tmp/codex-executor.sh \
 && rm /tmp/codex-executor.sh \
 && codex --version \
 && npm cache clean --force \
 && apt-get clean
