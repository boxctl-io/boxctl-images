# claude-agent plus the Go toolchain: what `boxctl claude` boots for a Go
# project (one with a go.mod or go.work), so `go build` and `go test` work
# on the first command. The CLI picks it by name -- boxctl's
# cmd/imagepick.go matches a project's stack against the image list and
# prefers a name with the agent's in it -- so keep the claude-agent-<lang>
# shape for the next language image, and build it FROM claude-agent so
# everything a run needs (tmux, gh, boxctl-status, Claude Code) is there.
#
# Go from go.dev's own tarball, not Debian's golang package, which lags a
# release or two behind what go.mod files ask for. Pinned with its sha256
# per architecture so a build is reproducible; bumping GO_VERSION (and
# both GO_SHA256_* from https://go.dev/dl/?mode=json) is a change to this
# file, which is what makes build-fc-images.yml rebuild. A go.mod asking
# for a newer Go still works: GOTOOLCHAIN's default (auto) downloads that
# one. TARGETARCH is buildx's: amd64 for the Firecracker rootfs, arm64 too
# for the OCI image publish-oci-images.yml pushes (onctl's ac provider on
# a Mac).
#
# gcc and libc6-dev for cgo and `go test -race`, which need a C compiler.
FROM claude-agent
ARG TARGETARCH
ARG GO_VERSION=1.27.2
ARG GO_SHA256_amd64=ecbadb99091a3f46e31f5f934b068b1864eafa7995211b39eaddf76996045fe5
ARG GO_SHA256_arm64=94f3e30b8e374bc285e7dadc11e0865726b9bc6e85b841ccceaabc0214c6b7c8
RUN apt-get update \
 && apt-get install -y --no-install-recommends gcc libc6-dev \
 && apt-get clean \
 && case "$TARGETARCH" in amd64) sha=$GO_SHA256_amd64;; arm64) sha=$GO_SHA256_arm64;; *) echo "no Go checksum for $TARGETARCH" >&2; exit 1;; esac \
 && curl -fsSL -o /tmp/go.tgz "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
 && echo "${sha}  /tmp/go.tgz" | sha256sum -c - \
 && tar -C /usr/local -xzf /tmp/go.tgz \
 && rm /tmp/go.tgz

# On every shell's PATH by symlink, not ENV: a Dockerfile's ENV doesn't
# survive into the rootfs, and a box's ssh sessions don't read a profile.
# `go install`ed tools land in /usr/local/bin for the same reason (GOBIN
# in root's go env file, /root/.config/go/env).
RUN ln -s /usr/local/go/bin/go /usr/local/go/bin/gofmt /usr/local/bin/ \
 && go env -w GOBIN=/usr/local/bin \
 && go version
