# Ubuntu 26.04 LTS (resolute), SSH only -- debian-slim's shape with
# Ubuntu's packages and support window. Docker's Ubuntu image is
# minimized (no man pages or docs); `unminimize` in a box restores them.
FROM ubuntu:26.04@sha256:3595d7fc4286a33fad0fd853a4063e654287a9c3787437d7937c94ca3f7a804e
RUN --mount=from=common,target=/boxctl sh /boxctl/install.sh
