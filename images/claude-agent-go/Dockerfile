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
ARG GO_VERSION=1.27.4
ARG GO_SHA256_amd64=7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840
ARG GO_SHA256_arm64=3392582988a273a87ab5043ee1b2c9c17a2c3c00580bf9326c47a2e0aaaeac92
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

# The checks a Go repository's CI runs beyond the toolchain's own:
# golangci-lint and govulncheck. Every unattended run on a Go repository
# installed both before it could open a pull request that passes CI, and
# `go install ...golangci-lint@latest` gets v1, which rejects a v2 config
# -- the first thing each of them tripped over. golangci-lint from its
# release tarball, pinned by sha256 per architecture like Go above (its
# own docs advise against `go install`); govulncheck by `go install` at a
# pinned version, into GOBIN (/usr/local/bin). Its vulnerability database
# is fetched when it runs, so a pinned binary still reports new
# advisories -- which is also why GO_VERSION above wants bumping when one
# lands in the standard library: a run would otherwise see findings CI's
# newer Go doesn't have.
ARG GOLANGCI_LINT_VERSION=2.15.0
ARG GOLANGCI_LINT_SHA256_amd64=9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea
ARG GOLANGCI_LINT_SHA256_arm64=b4d8f17068b35ee6060cc2f69a16fb48740d706fbba81661c318649c114a6998
ARG GOVULNCHECK_VERSION=v1.9.0
RUN case "$TARGETARCH" in amd64) sha=$GOLANGCI_LINT_SHA256_amd64;; arm64) sha=$GOLANGCI_LINT_SHA256_arm64;; *) echo "no golangci-lint checksum for $TARGETARCH" >&2; exit 1;; esac \
 && name="golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${TARGETARCH}" \
 && curl -fsSL -o /tmp/gcl.tgz "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT_VERSION}/${name}.tar.gz" \
 && echo "${sha}  /tmp/gcl.tgz" | sha256sum -c - \
 && tar -C /tmp -xzf /tmp/gcl.tgz "${name}/golangci-lint" \
 && install -m 0755 "/tmp/${name}/golangci-lint" /usr/local/bin/golangci-lint \
 && rm -rf /tmp/gcl.tgz "/tmp/${name}" \
 && go install "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" \
 && go clean -cache -modcache \
 && golangci-lint --version && govulncheck -version
