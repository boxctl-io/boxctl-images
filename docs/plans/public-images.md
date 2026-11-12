# Public image downloads

Status: proposal. Decided so far: the domain is `images.boxctl.io`, and
only the Dockerfile-built images (and the kernel) are public to begin
with.

## The problem

The images CI publishes can only be downloaded with R2 credentials: they
sit in `boxctl-vms`, a private bucket, and the server hands hosts
presigned links. Someone who wants to try `onctl create -p fc` on a host
of their own has no image to give it, short of building one.

Nothing in the Dockerfile-built images is private. Their definitions and
Dockerfiles are in this public repo, `claude-agent` and `claude-agent-go`
are already public on ghcr, and none bakes in a key, token or password
(SSH host keys are made on first boot).

The bucket is another matter. `boxctl-vms` also holds what the server
writes for customers -- `backups/`, `archives/` and `imports/`
(boxctl-vms `internal/bucketrules`) -- and the host agent bundle
(`agent/`). So the bucket can't simply be made public.

## The proposal

A second bucket, `boxctl-images`, public for reads through a custom
domain. CI publishes the public images to it **as well as** to
`boxctl-vms`, with the same `boxctl-image publish` and so the same keys:

```
https://images.boxctl.io/catalog/<name>.json             the manifest
https://images.boxctl.io/images/<file>-<sha7>.zst        one generation
https://images.boxctl.io/images/latest/<file>.zst        the current one, by name
https://images.boxctl.io/kernel/...                      the kernel, laid out the same way
```

```bash
curl -fsSL https://images.boxctl.io/images/latest/debian-slim.ext4.zst | zstd -d -o debian-slim.ext4
curl -fsSL https://images.boxctl.io/kernel/latest/boxctl-kernel.vmlinux.zst | zstd -d -o vmlinux
sudo onctl create -n box -p fc --rootfs-image $PWD/debian-slim.ext4 --kernel-image $PWD/vmlinux
```

**Public:** every image built from `images/<name>/Dockerfile` (today
`alpine`, `debian-slim`, `ubuntu-26.04`, `claude-agent`,
`claude-agent-go`, `openai-agents`) and `boxctl-kernel`, without which
none of them boots. **Not public:** `desktop` and `vscode`, until their
bake scripts and VS Code's redistribution terms have been looked at --
moving one to a Dockerfile makes it public, so that review belongs in
that PR. `windows-server-2025` is `external` and never published
anywhere.

### Why a mirror, and not a move

The first draft moved image publishing to the new bucket and pointed the
server at it. With only some images public that no longer works: the
server reads its whole catalog from one bucket, and `desktop` and
`vscode` would be in another. Publishing to both instead means:

- **boxctl-vms doesn't change at all** -- no configuration, no code. It
  keeps reading `boxctl-vms`, where every image still is. (The draft's
  one code change, a separate store for the agent bundle, goes away
  too.)
- **Nothing is migrated.** The public bucket starts empty and fills as
  images are published. Old generations stay private, where the backups
  that need them are.
- **Turning it off is deleting a variable.** The private bucket never
  stopped being the source of truth.

The cost is storing the public images twice: about 1.7 GiB compressed
for the current set, plus up to `maxPrevious` older generations of each.

A separate bucket rather than a Worker or rule in front of `boxctl-vms`
that lets some keys through: with a rule, one mistake in it publishes
customer backups. With two buckets there is no configuration in which
that happens.

## What changes

### Cloudflare (by hand, once)

1. Create the R2 bucket `boxctl-images`.
2. Attach the custom domain `images.boxctl.io` to it. That serves
   objects by key and nothing else -- a custom domain doesn't list a
   bucket. Leave the `r2.dev` URL off.
3. One API token, read-write on this bucket only, for CI.

### This repository

- `build-fc-images.yml`: after publishing an image, publish it again to
  the public bucket when it has a Dockerfile. `build-fc-kernel.yml`: the
  same for the kernel. Both steps are skipped while the
  `PUBLIC_IMAGES_BUCKET` repository variable is unset, and take the
  token from the `PUBLIC_IMAGES_ACCESS_KEY` /
  `PUBLIC_IMAGES_SECRET_ACCESS_KEY` secrets (the endpoint is the
  account's, `CF_S3_ENDPOINT`, as now). `boxctl-image` needs no change:
  it already reads `CF_BUCKET`.
- README, once step 3 of the rollout has passed: the download commands
  above, and that a manifest's sha256 is what to check a download
  against.

## Rollout

1. Create the bucket, domain and token; set the variable and the two
   secrets.
2. Run `build-fc-images` for `all` and `build-fc-kernel` once. A first
   publish to the public bucket has to be a full one -- a manifest-only
   republish of an image the bucket has never held fails, by design
   (`imagecatalog.Advance`).
3. Download `alpine` and the kernel from `images.boxctl.io` and boot
   them with onctl on a host.

Rollback is deleting the `PUBLIC_IMAGES_BUCKET` variable, and the bucket
if the images should stop being reachable.

## What becoming public means

- **`hidden` and `retired` stop meaning unreachable** for public images.
  They still mean "not offered" and "not bootable" on boxctl, but anyone
  can download the objects. `alpine` is hidden today and is exactly the
  image an outside reader wants.
- **Third-party software in the images** (Claude Code, the Codex CLI,
  Chromium) is already distributed publicly in the ghcr images or by its
  vendor's installer; check each one's terms allow redistribution in an
  image before step 2.
- **Cost.** R2 doesn't charge for egress, only per request, and the
  custom domain puts Cloudflare's cache in front. Sha-named objects
  never change and can be cached forever; `images/latest/*` and
  `catalog/*` need a short TTL.
- **Integrity.** The write token is CI-only, and a manifest's sha256 is
  the check on a download; the README should say to use it.

## Not in this plan

- onctl fetching an image by name (`--rootfs-image debian-slim`
  resolving through the public catalog). The natural next step, and a
  change in onctl, once the URLs exist.
- `catalog/index.json`, a list of what is published (a public reader
  can't list the bucket). Worth adding with the first consumer that
  needs to discover images rather than name them.
- `desktop` and `vscode`, and the agent bundle.
