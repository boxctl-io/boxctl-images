# Ubuntu 26.04 LTS (resolute), SSH only -- debian-slim's shape with
# Ubuntu's packages and support window. Docker's Ubuntu image is
# minimized (no man pages or docs); `unminimize` in a box restores them.
FROM ubuntu:26.04@sha256:6a06111c3b0a8600feadb270aec771dc239bf407b25625f93f44ab00e11f4d3c
RUN --mount=from=common,target=/boxctl sh /boxctl/install.sh
