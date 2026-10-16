#!/usr/bin/env bash
# Builds the Firecracker guest kernel every boxctl box boots
# (/opt/fc/images/boxctl-kernel.vmlinux, passed as `onctl create
# --kernel-image` by internal/vm/service.go's Create).
#
# Until this existed, boxes booted whatever kernel the host's own onctl.yaml
# named -- onctl-runners' fc-host/scripts/build-fc-kernel.sh output
# (vmlinux-overlay2), which is built for CI runner VMs. That kernel has the
# netfilter/VXLAN/eBPF symbols a *single-node* k3s needs, but not the
# iptables `statistic` match: kube-proxy (iptables mode) writes
# `-m statistic --mode random` for any Service with two or more endpoints,
# so the first such Service makes every iptables-restore on every node fail
# ("iptables-restore: line N failed"). A one-node k3s box never trips it;
# adding a second node does straight away, because Rancher then scales
# cattle-cluster-agent to two replicas. The node that joined after that
# point never gets any Service rules at all (no ClusterIP, no DNS), while
# the older node silently keeps its last good rules -- nothing shows up in
# node or pod status. Owning the kernel here also keeps a box-driven change
# like this off the CI runners' kernel.
#
# Starts from the same base as build-fc-kernel.sh (Firecracker's
# microvm-kernel-ci config for $KERNEL_SERIES, latest point release) and
# enables the same symbols, plus what multi-node Kubernetes needs:
#   - kube-proxy iptables mode: NETFILTER_XT_MATCH_STATISTIC (the fix above),
#     NETFILTER_XT_MATCH_ADDRTYPE/CONNTRACK (already built in upstream, now
#     asserted so a future base config can't drop them silently)
#   - kube-proxy nftables mode: NFT_NUMGEN (random endpoint pick), NFT_FIB_*
#     (local-address checks), NFT_CT/NAT/MASQ/REJECT
#   - k3s's embedded NetworkPolicy controller (kube-router): ipset
#     (IP_SET + the hash types it creates, NETFILTER_XT_SET), -m physdev,
#     -m limit and the NFLOG target
#
# No /lib/modules ships in any box image, so everything must be =y; the
# verification step below fails the bake if olddefconfig downgraded any of
# it to =m (see build-fc-kernel.sh's comments for how that happens).
#
# Run on any x86_64 Linux host with root (no KVM needed -- it only
# compiles), e.g. the build-fc-kernel workflow's onctl-4 runner, or aimax:
#   sudo bash scripts/images/bake-kernel-image.sh
# CONFIG_ONLY=1 stops after the config verification (a minute, not 15).
# Then `boxctl-image publish boxctl-kernel` uploads it; hosts pick it up through the
# controller's image sync (it's in MissingImages like the rootfs images).
# Build time: ~10-15 min on 12 cores.
set -euo pipefail

KERNEL_SERIES="${KERNEL_SERIES:-6.1}"
OUTPUT_DIR="${OUTPUT_DIR:-/opt/fc/images}"
OUTPUT="$OUTPUT_DIR/boxctl-kernel.vmlinux"
BUILD_DIR=$(mktemp -d /tmp/boxctl-kernel-build.XXXXXX)
trap 'rm -rf "$BUILD_DIR"' EXIT

T0=$(date +%s)
say() { echo "[bake-kernel-image +$(($(date +%s) - T0))s] $*"; }

say "installing build deps"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq \
  build-essential flex bison libssl-dev libelf-dev bc wget xz-utils jq curl >/dev/null

say "resolving latest $KERNEL_SERIES.x from kernel.org"
KERNEL_VERSION=$(curl -fsSL https://www.kernel.org/releases.json \
  | jq -r '[.releases[] | select(.version | startswith("'"$KERNEL_SERIES"'."))] | first | .version')
[ -n "$KERNEL_VERSION" ] && [ "$KERNEL_VERSION" != null ] || {
  echo "no $KERNEL_SERIES.x release listed on kernel.org" >&2
  exit 1
}
say "kernel version: $KERNEL_VERSION"

cd "$BUILD_DIR"
say "downloading linux-$KERNEL_VERSION"
wget -q "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-${KERNEL_VERSION}.tar.xz"
tar xf "linux-${KERNEL_VERSION}.tar.xz"
cd "linux-${KERNEL_VERSION}"

say "fetching Firecracker microvm kernel config for $KERNEL_SERIES"
curl -fsSL \
  "https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/resources/guest_configs/microvm-kernel-ci-x86_64-${KERNEL_SERIES}.config" \
  -o .config

# Same as onctl-runners' build-fc-kernel.sh (see its comments for each):
# overlayfs for Docker, the iptables chain forced to =y end to end, NAT,
# comment/mark/multiport, br_netfilter, VXLAN, eBPF + its MODULES dependency.
BASE_SYMBOLS="OVERLAY_FS
  NETFILTER NETFILTER_ADVANCED NETFILTER_XTABLES IP_NF_IPTABLES IP_NF_RAW IP6_NF_IPTABLES IP6_NF_RAW
  IP_NF_FILTER IP6_NF_FILTER NF_CONNTRACK NF_NAT IP_NF_NAT IP6_NF_NAT
  NETFILTER_XT_NAT NETFILTER_XT_TARGET_MASQUERADE NETFILTER_XT_MATCH_COMMENT NETFILTER_XT_MARK
  NETFILTER_XT_MATCH_MULTIPORT BRIDGE BRIDGE_NETFILTER VXLAN NET_UDP_TUNNEL
  MODULES BPF_SYSCALL BPF_JIT BPF_JIT_ALWAYS_ON CGROUP_BPF NET_CLS_ACT NET_CLS_BPF NET_SCH_INGRESS"

# Multi-node Kubernetes additions (see the header).
K8S_SYMBOLS="NETFILTER_XT_MATCH_STATISTIC NETFILTER_XT_MATCH_ADDRTYPE NETFILTER_XT_MATCH_CONNTRACK
  NF_TABLES NF_TABLES_INET NF_TABLES_IPV4 NF_TABLES_IPV6
  NFT_NUMGEN NFT_FIB NFT_FIB_IPV4 NFT_FIB_IPV6 NFT_FIB_INET NFT_CT NFT_NAT NFT_MASQ NFT_REJECT
  IP_SET IP_SET_HASH_IP IP_SET_HASH_IPPORT IP_SET_HASH_IPPORTIP IP_SET_HASH_IPPORTNET IP_SET_HASH_NET
  NETFILTER_XT_SET NETFILTER_XT_MATCH_PHYSDEV NETFILTER_XT_MATCH_LIMIT NETFILTER_XT_TARGET_NFLOG
  NETFILTER_NETLINK_LOG"

# virtio-vsock, the guest end of a box's run-status channel
# (docs/plans/run-status.md). Built in upstream; asserted so a future base
# config can't drop it silently.
VSOCK_SYMBOLS="VSOCKETS VIRTIO_VSOCKETS"

say "enabling base + Kubernetes netfilter + vsock symbols"
for sym in $BASE_SYMBOLS $K8S_SYMBOLS $VSOCK_SYMBOLS; do
  ./scripts/config --enable "$sym"
done
make olddefconfig

say "verifying every symbol landed as built-in (not =m)"
failed=0
for sym in $BASE_SYMBOLS $K8S_SYMBOLS $VSOCK_SYMBOLS; do
  val=$(./scripts/config --state "$sym")
  if [ "$val" != "y" ]; then
    echo "ERROR: CONFIG_${sym}=${val} (expected y) -- boxes have no /lib/modules, so =m is as good as =n" >&2
    failed=1
  fi
done
[ "$failed" = 0 ] || exit 1

if [ "${CONFIG_ONLY:-0}" = 1 ]; then
  say "CONFIG_ONLY=1: config verified for linux $KERNEL_VERSION, skipping the build"
  exit 0
fi

say "building vmlinux -- $(nproc) jobs"
make vmlinux -j"$(nproc)"

say "installing to $OUTPUT"
mkdir -p "$OUTPUT_DIR"
TMP=$(mktemp "$OUTPUT.new.XXXXXX")
cp vmlinux "$TMP"
chmod 644 "$TMP"
mv -f "$TMP" "$OUTPUT"

say "recording the identity sidecar ($OUTPUT.sha256sum)"
# Same "<sha256> <size> <mtime-nanos>" format the rootfs bakes write and
# boxctl-vms-agent.sh's host_images reports from.
KERNEL_SHA=$(sha256sum "$OUTPUT" | awk '{print $1}')
printf '%s %s %s\n' "$KERNEL_SHA" "$(stat -c %s "$OUTPUT")" "$(date -r "$OUTPUT" +%s%N)" > "$OUTPUT.sha256sum"

say "done -- $OUTPUT (linux $KERNEL_VERSION, $(du -sh "$OUTPUT" | cut -f1), sha256 $KERNEL_SHA)"
say "test directly with: onctl create -n kerneltest -p fc --kernel-image $OUTPUT --rootfs-image /opt/fc/images/debian-slim.ext4 -a k3s/k3s-server.sh"
