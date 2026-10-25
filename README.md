# boxctl-images

The images a [boxctl](https://boxctl.io) box boots, and `boxctl-image`,
the tool that builds and publishes them. One home for everything that
consumes them:

- **Firecracker boxes** boot the ext4 rootfs images CI publishes to R2
  (`build-fc-images.yml`), and boxctl's own guest kernel
  (`build-fc-kernel.yml`).
- **The boxctl-vms server** imports [`imagecatalog`](imagecatalog) to read
  the published catalog, and [`objectstore`](objectstore) to reach R2.
- **onctl's `ac` provider** (Apple containers on a Mac) runs
  `claude-agent` as an OCI image, `ghcr.io/boxctl-io/claude-agent`, built
  for amd64 and arm64 by `publish-oci-images.yml` from the same Dockerfile.

This used to be part of [boxctl-vms](https://github.com/cdalar/boxctl-vms)
(`images/`, `cmd/boxctl-image`, `internal/imagecatalog`); the history
came along.

## Box images

Every image a box can boot is defined once, in
`images/<name>/image.json`: its label and description, OS, provider
(always `fc`), features (`desktop`), status
(`available`/`hidden`/`deprecated`/`retired`) and whether it's the
default. `go test ./cmd/boxctl-image` validates all of them on every PR
(names, exactly one default, exactly one kernel, and how each image is
built).

An image is built from `images/<name>/Dockerfile`
([`docs/plans/dockerfile-rootfs.md`](docs/plans/dockerfile-rootfs.md)) --
`debian-slim`, `ubuntu-26.04`, `claude-agent` (`FROM debian-slim`,
plus git, rsync, jq, ripgrep, make, gh, tmux and Claude Code for coding
agents such as boxctl's Claude Code plugin, and the box side of `boxctl
claude`'s GitHub credential forwarding), `claude-agent-go` (`FROM
claude-agent`, plus Go and gcc -- what `boxctl claude` picks for a project
with a `go.mod`; a language image is named `claude-agent-<lang>` so the
CLI's image picker finds it) and `openai-agents` (`FROM debian-slim`,
plus Node.js, the Codex CLI and the `codex-set-key`/`codex-connect`
helpers -- the self-hosted environment of an OpenAI Agents API session)
so far -- or, until it moves over, by `scripts/images/bake-<name>-image.sh`
(`desktop`, `vscode`, the kernel). `boxctl-image build <name>` does
either. For a Dockerfile it runs `docker buildx bake` (from a bake file it
generates from the Dockerfiles' `FROM`/`--from` references), exports the
filesystem as a tar, unpacks it, fixes up what Docker manages during a
build (`/etc/hostname`, `/etc/hosts`, `/etc/resolv.conf`,
`/etc/machine-id`, `/.dockerenv`, Docker's apt tweaks), checks the tree
boots as a box (systemd init, sshd enabled, no baked SSH host keys), and
makes a `size_mib` ext4 from it with `mkfs.ext4 -d` -- no loop mount, no
chroot. Every Dockerfile starts with `images/common/install.sh`: the
packages and units every box needs, including generating SSH host keys on
each box's first boot rather than baking one set into the image. Base
images are pinned by digest, and Dependabot bumps them.

`build-fc-images.yml` reads `images/` rather than a list of its own. A
push that changes an image's sources (its bake script, or its Dockerfile
directory, anything it builds from, or the builder) rebuilds it; one that
changes only `image.json` republishes its manifest without building.
Either way `boxctl-image publish` uploads the image to
`images/<file>-<sha7>.zst` (named by its uncompressed sha256, which also
rides along as `x-amz-meta-sha256`) and writes `catalog/<name>.json`: the
definition plus the current generation (sha256, size, object key, commit)
and the earlier ones. See
[`docs/plans/image-catalog.md`](docs/plans/image-catalog.md).

The boxctl-vms server reads those manifests (`internal/vm/catalog.go`
there) every 5 minutes, overlaid on the definitions it ships: a manifest
wins, and an image defined but not yet published stays bootable on hosts
that already have it. Image sync fetches each image a host lacks from its
current generation's own object, and `fetch-image.sh` refuses anything
that doesn't decompress to that generation's sha256. A host already
holding an *older* generation keeps it -- replacing it would strand
backups taken against it.

An image can't be bigger than the smallest box disk
(`imagecatalog.SmallestBoxDiskMiB`, 10 GiB): onctl won't shrink one.
`validate` and `publish` refuse such an image, and a test in boxctl-vms
keeps the constant equal to its size table.

**Adding an image:** add `images/<name>/image.json` (with `size_mib`)
and `images/<name>/Dockerfile` -- `FROM` a pinned base image (or another
image in `images/`), then `RUN --mount=from=common,target=/boxctl sh
/boxctl/install.sh` -- and merge. To try it first:

```bash
go run ./cmd/boxctl-image build <name> -tar-only /tmp/<name>.tar   # needs Docker
sudo ./boxctl-image build <name> -from-tar /tmp/<name>.tar -out /tmp/<name>.ext4   # Linux, needs e2fsprogs
onctl create -n test -p fc --rootfs-image /tmp/<name>.ext4 --kernel-image /opt/fc/images/boxctl-kernel.vmlinux   # on a host
```

CI publishes it, the server picks it up on its next refresh, and image
sync fetches it onto hosts -- no server change or deploy. The one
exception is restoring a diff backup through `boxctl-tunnel-agent`, which
still has its own list of base images.

**The build graph, for a build of your own:** `boxctl-image build <name>
-bake-only /tmp/bake.json` writes the `docker buildx bake` file `build`
generates and stops. `publish-oci-images.yml` uses it to build
`claude-agent` for `linux/amd64` and `linux/arm64` and push it to
`ghcr.io/boxctl-io/claude-agent` -- the same Dockerfile as the box image,
as a container image for runtimes that take one (onctl's `ac` provider
runs it as an Apple container, with sshd, tmux, git, gh and Claude Code
ready instead of installed on every new sandbox).

**Converting a container image:** `boxctl-image convert <ref>` makes a
box image out of any image Docker can pull -- Docker Hub, GHCR, ECR,
Artifactory, or a private registry `docker login` already knows:

```bash
go run ./cmd/boxctl-image convert ghcr.io/acme/api:1.4 -tar-only /tmp/api.tar   # needs Docker
sudo ./boxctl-image convert ghcr.io/acme/api:1.4 -from-tar /tmp/api.tar -out /tmp/api.ext4   # Linux, needs e2fsprogs
```

(or both halves in one run, as root on Linux with Docker). It's the same
pipeline as `build`, for a Dockerfile it writes itself: `FROM <ref>`,
then `images/common/install.sh`. So the same limit applies -- the image
has to be Debian- or Ubuntu-based; Alpine, distroless and `scratch`
images fail with a message saying so -- and it's built for `linux/amd64`
whatever the registry's default is. `-size-mib` (default 4096) is the
ext4's size.

A container image is a filesystem plus a config saying what to run in
it, and a box boots systemd, which reads none of that config. So convert
reads it from the registry and carries it over
(`cmd/boxctl-image/convert.go`):

- `ENTRYPOINT` + `CMD` become `boxctl-container.service`, with the
  image's `ENV`, `USER`, `WORKDIR` and `STOPSIGNAL`, restarted on
  failure. Its output is in `journalctl -u boxctl-container`. An image
  whose command is only a shell (a base image) gets no unit;
  `-no-service` skips it for any image.
- `ENV` also goes to `/etc/profile.d/boxctl-container-env.sh`, so an SSH
  session has the `PATH` and variables `docker exec` would.
- Units the image's packages had enabled are disabled when a container
  unit is installed. They never ran in the container, and in a box they
  would start next to the command that replaces them (the `nginx`
  image's packaged `nginx.service` and its `nginx -g 'daemon off;'`,
  both wanting port 80).
- `EXPOSE` is only reported: a box publishes nothing by itself.
  `VOLUME` and `HEALTHCHECK` are ignored.

The result isn't a catalog image -- nothing publishes or syncs it; it's
a file to `onctl create --rootfs-image`.

To publish by hand (CI normally does it), with `CF_ACCESS_KEY`,
`CF_SECRET_ACCESS_KEY` and `CF_S3_ENDPOINT` set:

```bash
go build -o boxctl-image ./cmd/boxctl-image
sudo --preserve-env=CF_ACCESS_KEY,CF_SECRET_ACCESS_KEY,CF_S3_ENDPOINT \
  ./boxctl-image publish debian-slim
```

## The box kernel

Every Firecracker box boots boxctl's own guest kernel,
`/opt/fc/images/boxctl-kernel.vmlinux` (`onctl create --kernel-image`),
not the kernel the host's `onctl.yaml` names. That one is onctl-runners'
CI-runner kernel, which lacks the iptables `statistic` match: kube-proxy
needs it for any Service with two or more endpoints, so a k3s box broke as
soon as a second node joined. It also lacks ipset and the nftables pieces
kube-proxy's nftables mode and k3s's NetworkPolicy controller use.

[`scripts/images/bake-kernel-image.sh`](scripts/images/bake-kernel-image.sh)
builds it (Firecracker's microvm config plus those symbols, all verified
`=y` since boxes have no `/lib/modules`; `CONFIG_ONLY=1` checks just the
config), `boxctl-image publish boxctl-kernel` uploads it to R2 under its
own `kernel/` prefix (`kernel/latest/boxctl-kernel.vmlinux.zst`), and
`build-fc-kernel.yml` runs both on a change -- separately from the rootfs
images, since Firecracker boots kernel and rootfs as independent inputs.
From there it travels like a rootfs image: the agent reports it, the
server lists it as missing for a host that lacks it, and placement won't
send a new box to such a host.

## CI

Everything runs on the `onctl-2` self-hosted pool (the onctl-runners
GitHub App, installed on this repository). `build.yml` and `lint.yml` run
on every PR. Publishing needs the `CF_ACCESS_KEY`, `CF_SECRET_ACCESS_KEY`
and `CF_S3_ENDPOINT` repository secrets; `publish-oci-images.yml` pushes
to ghcr with the workflow's own token.
