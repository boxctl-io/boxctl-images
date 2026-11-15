# Ubuntu 26.04 LTS (resolute), SSH only -- debian-slim's shape with
# Ubuntu's packages and support window. Docker's Ubuntu image is
# minimized (no man pages or docs); `unminimize` in a box restores them.
FROM ubuntu:26.04@sha256:065aa8626841f16057b78b027b464cd59c4fac36c204c72dcbf48d66f5fa8a96
RUN --mount=from=common,target=/boxctl sh /boxctl/install.sh
