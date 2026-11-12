# Alpine, SSH only: the smallest image a box boots (about 30 MB of files),
# for trying out a host or an onctl fc setup without a 4 GiB download's
# worth of Debian. Not apt-based and not systemd, so images/common/install.sh
# doesn't apply -- install.sh here meets the same contract with OpenRC,
# sharing only the chrony drop-in.
FROM alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f
RUN --mount=target=/boxctl --mount=from=common,target=/boxctl-common sh /boxctl/install.sh
