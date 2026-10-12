# Ubuntu 26.04 LTS (resolute), SSH only -- debian-slim's shape with
# Ubuntu's packages and support window. Docker's Ubuntu image is
# minimized (no man pages or docs); `unminimize` in a box restores them.
FROM ubuntu:26.04@sha256:f144425ff09be612d6d9ad965196e9cdc23dae1f42110a8a11a3e9a8198759f7
RUN --mount=from=common,target=/boxctl sh /boxctl/install.sh
