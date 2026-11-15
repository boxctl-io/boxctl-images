# Alpine, SSH only: the smallest image a box boots (about 30 MB of files),
# for trying out a host or an onctl fc setup without a 4 GiB download's
# worth of Debian. Not apt-based and not systemd, so images/common/install.sh
# doesn't apply -- install.sh here meets the same contract with OpenRC,
# sharing only the chrony drop-in.
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN --mount=target=/boxctl --mount=from=common,target=/boxctl-common sh /boxctl/install.sh
