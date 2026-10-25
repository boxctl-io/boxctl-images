# Ubuntu 26.04 LTS (resolute), SSH only -- debian-slim's shape with
# Ubuntu's packages and support window. Docker's Ubuntu image is
# minimized (no man pages or docs); `unminimize` in a box restores them.
FROM ubuntu:26.04@sha256:c098726dd9b9733f3e885f8de0ef375206a311b0a317eadc40d347b1115fb944
RUN --mount=from=common,target=/boxctl sh /boxctl/install.sh
