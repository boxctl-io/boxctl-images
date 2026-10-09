# Building box images from Dockerfiles

> **Status:** `debian-slim` and `ubuntu-26.04` are built this way;
> `desktop` and `vscode` still use their bake scripts. The layout and
> pipeline below describe what's built (`cmd/boxctl-image/build.go`),
> not the first draft of this plan.

Companion to `image-catalog.md`. That plan has two halves: the
**catalog** (what images exist, published as manifests the server reads)
and the **builder** (how an ext4 gets made). This plan replaces the
builder half -- the `boxctl-image build` + `images/steps/*.sh` design --
with Dockerfiles converted to ext4, which is how most of the Firecracker
ecosystem builds rootfs images (E2B templates, Fly.io, Lambda's container
images). The catalog half is unchanged, and ships first; see
"Sequencing".

## Why

- **Layering is free.** `vscode` is `desktop` plus two layers, written
  as `FROM`, instead of a copy of `bake-desktop-image.sh` with additions.
  A change to the shared base rebuilds everything above it through
  ordinary Docker caching.
- **No root, loop devices or chroot mounts to orchestrate.** Today's
  bake scripts mount an ext4 over loop, bind-mount `/proc`/`/dev`/
  `/dev/pts` into it, and juggle an `EXIT` trap that has to unmount all
  of it in the right order (and has bitten us -- see the "disarm the
  trap" comment in every bake script). A Docker build needs none of it.
- **It's a format people already write.** It's the path to letting
  users bring their own images later, E2B-style (out of scope here; see
  the end).
- **Supply chain tooling works on it out of the box**: digest-pinned
  bases that Dependabot bumps, syft/trivy against the image, registry
  build cache.

## What a box rootfs must satisfy

The contract today's images meet, which a Docker-built one must too.
Read from onctl's `internal/providerfc` (`DebugfsRootfsPreparer.Prepare`,
`injectSSHKey`, `injectHostname`, `growRootfs`) and the bake scripts:

1. **ext4**, because onctl edits each box's copy with `debugfs` and
   grows it with `e2fsck -f` + `resize2fs`.
2. **`/etc/hostname` and `/etc/hosts` are regular files**, not
   symlinks: onctl rewrites both with debugfs, which doesn't follow
   links (onctl's own comment on this found it the hard way).
3. **`/root/.ssh` creatable, `sshd` enabled**: onctl writes
   `authorized_keys` there and the dashboard/terminal SSH in as root.
4. **systemd as `/sbin/init`**. The kernel runs it; nothing else does.
5. **Networking comes from the kernel command line** (`fc.kernelArgs`),
   so the image needs no network config of its own -- but it must not
   bring up anything that fights it (no `systemd-resolved`; static
   `/etc/resolv.conf`, as the bake scripts do now).
6. **chrony with the `/dev/ptp0` refclock config** from
   `guest-clock-sync.md`, or a resumed box keeps a stale clock.
7. **`/opt/.baked`** marker -- the existing static smoke test checks it.
8. **Fits the smallest box disk** (10 GiB) -- `TestSizeDisksFitEveryImage`
   today, `boxctl-image validate` in the catalog plan.

## Layout

```
images/
  common/                    what every image gets; a named build context, not an image
    install.sh               the packages and units the contract above needs
    boxctl-resume.conf       chrony drop-in (guest-clock-sync.md)
    boxctl-ssh-host-keys.service
  debian-slim/image.json     catalog fields + size_mib
  debian-slim/Dockerfile     FROM debian:trixie-slim@sha256:... + common/install.sh
  ubuntu-26.04/Dockerfile    FROM ubuntu:26.04@sha256:...      + common/install.sh
  desktop/Dockerfile         (next) FROM ubuntu:24.04@... + install.sh + XFCE/TigerVNC/Firefox
  vscode/Dockerfile          (next) FROM desktop + VS Code + Python + desktop user
```

Every Dockerfile starts the same way:

```dockerfile
FROM debian:trixie-slim@sha256:<pinned>
RUN --mount=from=common,target=/boxctl sh /boxctl/install.sh
```

The first draft had a `base-*` image per distro family. One shared
script does the same job with less: the contract is apt-based for every
image we build, so it doesn't need a Dockerfile per distro, and an image
picks its distro by its own `FROM`.

There's no hand-written `docker-bake.hcl` either. `boxctl-image build`
generates the bake file from the Dockerfiles: a `FROM x` or
`--from=x` / `--mount=from=x` naming a directory under `images/` becomes
a named context -- `target:x` if that directory has a Dockerfile (so
`FROM desktop` builds the `desktop` target in the same run, not
something from Docker Hub), or the directory itself otherwise
(`images/common`). The same references drive the CI matrix, so the
build graph can't drift from the Dockerfiles. (Bake target names can't
contain dots, so `ubuntu-26.04` is the target `ubuntu-26_04`.)

`image.json` keeps only the catalog's fields plus `size_mib`, the ext4's
size. The build recipe is the Dockerfile. `validate` requires an image to
have a Dockerfile or a bake script, never both, and a Dockerfile image a
`size_mib` no bigger than the smallest box disk.

**SSH host keys are generated on first boot, not baked.** The bake
scripts ran `ssh-keygen -A` at bake time, so every box from an image
shared one set of host keys -- any box could impersonate any other to a
client that had seen one. `boxctl-ssh-host-keys.service` is a oneshot
ordered before `ssh.service` (not `ssh.socket`: that is an ordering
cycle, which on Ubuntu costs the box its SSH listener) with
`ConditionPathExists=!/etc/ssh/ssh_host_ed25519_key`, running
`ssh-keygen -A`; `install.sh` deletes any keys the package made. Same for
`/etc/machine-id`: left empty, so systemd generates a unique one per
box. (Debian 13 and Ubuntu 26.04's openssh also ship a `sshd-keygen`
unit of their own; ours makes it not matter whether a distro does.)

## Build pipeline

`boxctl-image build <name>`:

1. **Build.** `docker buildx bake` with the generated bake file and
   `output=type=tar`: the image's filesystem, every layer merged, written
   straight to a tar -- nothing is loaded into the Docker daemon, and no
   container is created to export. Works with both the plain `docker`
   driver and a `docker-container` builder. `BUILDX_BAKE_ENTITLEMENTS_FS=0`,
   since buildx 0.19+ otherwise refuses a bake file that writes outside
   its own directory, and the bake file is ours.
2. **Unpack** as root into scratch space next to the output (`tar -xpf
   --numeric-owner`), keeping ownership and modes -- `sudo` stays setuid
   root. (e2fsprogs >= 1.47.1 can read the tar directly with `mkfs.ext4
   -d rootfs.tar`; onctl-4 is on 24.04's 1.47.0.)
3. **Fix-ups** -- see below.
4. **Check** the tree boots as a box (`checkRootfs`): `/sbin/init`
   resolves to systemd (following links the way the box will, absolute
   targets inside the tree), sshd is enabled, no SSH host keys,
   `/etc/hostname` and `/etc/hosts` are regular files, `/root` exists.
   Checked rather than assumed, since a Dockerfile can undo any of it.
5. **Make the filesystem.** A `size_mib` sparse file +
   `mkfs.ext4 -F -d <dir>` -- populated at creation, never mounted --
   then `e2fsck -fn`.
6. **Move into place** at `/opt/fc/images/<name>.ext4` and write the
   `<sha256> <size> <mtime-nanos>` sidecar, exactly as bake did, so
   `publish` and everything after it is unchanged.

In CI the Docker build runs as the runner user (in the docker group;
buildx may be a per-user plugin root lacks) with `-tar-only`, and the rest
under `sudo` with `-from-tar`. The same two flags split a local try-out
between a Mac with Docker and a Linux box with e2fsprogs.

### Fix-ups after export

A Docker build manages some files itself rather than taking them from
the image, so no Dockerfile can set them; `fixupRootfs` writes them after
export:

| Path | Fix |
|---|---|
| `/etc/hostname` | placeholder `boxctl-<name>`; onctl writes each box's own |
| `/etc/hosts` | localhost lines + `127.0.1.1 boxctl-<name>`; onctl rewrites the 127.0.1.1 line |
| `/etc/resolv.conf` | static nameservers, as the bake scripts wrote (replacing a symlink if the image had one) |
| `/etc/machine-id` | empty, so each box generates its own |
| `/.dockerenv` | deleted -- software that checks it would think it's in a container |
| `/usr/sbin/policy-rc.d` | deleted -- in a box, apt upgrades must restart what they update |
| `/etc/apt/apt.conf.d/docker-*` | deleted -- Docker images' layer-size tweaks, not for a machine people use |
| `/opt/.baked` | created -- the marker the workflow's smoke test checks |

### What a VM ignores

`ENTRYPOINT`, `CMD`, `ENV`, `USER`, `WORKDIR`, `EXPOSE`, `HEALTHCHECK`
are image config, not filesystem; a VM boots init and never reads them.
Our Dockerfiles don't use them. (Not enforced yet; worth a check in
`validate` once anyone other than us writes these.)

## Parity with today's images

Each image switches only after its Docker-built version passes:

- `dpkg -l` diff against the current bake's output: same packages
  modulo deliberate changes. Slim Docker base images are *minimized*
  (Ubuntu strips man pages and docs via `/etc/dpkg/dpkg.cfg.d/excludes`);
  decide per image whether to `unminimize` -- desktop and vscode
  probably should, debian-slim/ubuntu-26.04 probably shouldn't.
- Boot on aimax: `onctl create -p fc --rootfs-image <file>
  --kernel-image /opt/fc/images/boxctl-kernel.vmlinux`; SSH in from the
  dashboard terminal; pause/resume and check the clock catches up
  (chrony); for desktop/vscode, open VNC.
- Size: the ext4 at its `size_mib`, compressed size comparable to
  today's publish (135 MB debian-slim, 751 MB desktop, 1.13 GB vscode).
- A new image means a new sha, which is true of any rebake today -- old
  diff backups keep restoring from their own generation per
  `base-image-provenance.md`.

Order: debian-slim and ubuntu-26.04 first (smallest, no bespoke steps),
then desktop, then vscode (non-root desktop user, the most bespoke).
Delete each `bake-<name>-image.sh` once its image has switched.

## What stays as it is

- **Kernel**: `bake-kernel-image.sh` / `build-fc-kernel.yml`. Not a
  rootfs; a Dockerfile buys nothing there.
- **Publish, R2 layout, manifests, server, host sync**: all catalog
  plan, untouched by how the ext4 was made.

## Workflow

`build-fc-images.yml`:

- `plan` job: `boxctl-image matrix` -- a Dockerfile image rebuilds when
  anything in its directory but `image.json` changes, or in a directory
  it builds from (transitively), or `cmd/boxctl-image/build.go` (the
  fix-ups are part of every image). A bake-script image rebuilds when its
  script changes.
- `bake-and-publish`: the build step runs `boxctl-image build` for
  either kind; smoke test and publish are unchanged.
- Dependabot's `docker` ecosystem over `images/*` bumps the pinned base
  digests; the bump is a change to the image's directory, so merging it
  rebuilds and republishes.

Not done yet: a registry build cache (every onctl-4 job starts cold --
fine at ~1 minute for the slim images, worth it for desktop/vscode), and a
weekly rebuild for distro security updates that change no file.

## Later: user images

Not part of this plan; recorded so the design doesn't close it off.
A user Dockerfile would go through the same build, with differences that
each need their own design:

- **Build isolation.** A user's `RUN` is arbitrary code. It can't run on
  `onctl-4` (shared CI) or a box host. Build inside a throwaway box
  instead -- we already run isolated Firecracker VMs.
- **Boot contract.** Either require `FROM ghcr.io/cdalar/boxctl-base-*`
  (works with the above as-is), or inject our own init + sshd into any
  image, as Fly does, so arbitrary Docker Hub images boot.
- **Catalog.** Owner-namespaced names (`<owner>/<name>`), quotas and
  size limits, and fetched to a host on first placement rather than
  synced to every host.
- **Snapshots** (E2B's fast start: boot once, snapshot, resume per
  sandbox) are separate again: snapshots are tied to host CPU and
  Firecracker version, and every resumed box needs a fresh identity
  (hostname, host keys, machine-id, IP).

## Sequencing

The catalog shipped first (#117, #118) on the bake scripts; this plan
swaps builders behind the same `publish`, image by image. Done:
`debian-slim`, `ubuntu-26.04`. Next: `desktop`, then `vscode`
(`FROM desktop`, with its non-root desktop user) -- each deleting its
`bake-<name>-image.sh` once its Docker-built image boots on aimax.
