# Public image downloads

Status: proposal. Nothing here is built yet.

## The problem

The images CI publishes can only be downloaded with R2 credentials: they
sit in `boxctl-vms`, a private bucket, and the server hands hosts
presigned links. Someone who wants to try `onctl create -p fc` on a host
of their own has no image to give it, short of building one.

Nothing in the images is private. Their definitions and Dockerfiles are
in this public repo, `claude-agent` and `claude-agent-go` are already
public on ghcr, and no image bakes in a key, token or password (SSH host
keys are made on first boot).

The bucket is another matter. `boxctl-vms` also holds what the server
writes for customers -- `backups/`, `archives/` and `imports/`
(boxctl-vms `internal/bucketrules`) -- and the host agent bundle
(`agent/`). So the bucket can't simply be made public.

## The proposal

A second bucket, `boxctl-images`, public for reads through a custom
domain, holding only what this repo publishes:

```
https://images.boxctl.io/catalog/<name>.json             the manifest
https://images.boxctl.io/images/<file>-<sha7>.zst        one generation
https://images.boxctl.io/images/latest/<file>.zst        the current one, by name
```

The keys are the ones `boxctl-image publish` writes today, so manifests
stay valid as they are: a generation's `key` is relative to the bucket.

```bash
curl -fsSL https://images.boxctl.io/images/latest/debian-slim.ext4.zst | zstd -d -o debian-slim.ext4
curl -fsSL https://images.boxctl.io/images/latest/boxctl-kernel.vmlinux.zst | zstd -d -o vmlinux
sudo onctl create -n box -p fc --rootfs-image $PWD/debian-slim.ext4 --kernel-image $PWD/vmlinux
```

A separate bucket rather than a Worker or rule in front of `boxctl-vms`
that lets `images/*` and `catalog/*` through: with a rule, one mistake in
it publishes customer backups. With two buckets there is no
configuration in which that happens.

## What changes

### Cloudflare (by hand, once)

1. Create the R2 bucket `boxctl-images`.
2. Attach the custom domain `images.boxctl.io` to it. That serves
   objects by key and nothing else -- a custom domain doesn't list a
   bucket. Leave the `r2.dev` URL off.
3. Two API tokens scoped to this bucket: read-write for CI, read-only
   for the server.

### This repository

- `build-fc-images.yml` and `build-fc-kernel.yml` publish to the new
  bucket: `CF_BUCKET` from a repository variable, added to the `sudo
  --preserve-env` list (it isn't passed through today, so `publish`
  falls back to `boxctl-vms`), and the `CF_*` secrets replaced with the
  new token's. `boxctl-image` itself needs no change -- it already reads
  `CF_BUCKET`.
- `publish` also writes `catalog/index.json`, the names of every
  published image. The server lists `catalog/` with its key, but a
  public reader can't list, and without an index would have to know an
  image's name already. (`image-catalog.md` left this as an open
  question; a public reader is the reason to answer it.)
- README: the download commands above, and that sha256 in a manifest is
  what to check a download against.

### boxctl-vms

- Server configuration only for images: `BOXCTL_R2_IMAGES_BUCKET=
  boxctl-images` with the read-only token in `BOXCTL_R2_IMAGES_*`. The
  catalog refresh and image sync already go through that store, and
  hosts keep getting presigned links, so `fetch-image.sh` is untouched.
- One code change: the server reads the agent release
  (`agent/latest.json`) through the same store, and `publish-agent`
  writes it to `boxctl-vms`. The agent bundle shouldn't move to a public
  bucket without its own review, so the server needs a store for the
  agent release that stays on `boxctl-vms` (a `BOXCTL_R2_AGENT_*` key
  pair, defaulting to today's behaviour when unset).

## Rollout

Each step leaves a working system, and nothing is deleted until the end.

1. Create the bucket, domain and tokens.
2. Copy `images/` and `catalog/` from `boxctl-vms` to `boxctl-images`
   (`rclone copy`, which keeps the `x-amz-meta-sha256` metadata
   `find-image-by-sha.sh` and `publish` rely on). Old generations come
   along: a diff backup can only be restored against the exact base it
   was taken from.
3. Ship the boxctl-vms change that separates the agent release's store.
4. Point CI at the new bucket and run `build-fc-images` for `all` and
   `build-fc-kernel` once, to prove publishing works there.
5. Point the server at the new bucket. Check the catalog refresh log
   line and that a host lacking an image (`alpine` is 8 MiB) syncs it.
6. After a week without a rollback, delete `images/` and `catalog/`
   from `boxctl-vms`.

Rollback at any point before step 6 is setting the bucket variable and
key back, on CI or the server.

Between steps 4 and 5 the server still reads the old bucket, so an image
published in that window isn't seen until step 5. Do both the same day.

## What becoming public means

- **`hidden` and `retired` stop meaning unreachable.** They still mean
  "not offered" and "not bootable" on boxctl, but anyone can download
  the objects. Don't publish an image that must not be seen; today
  nothing is.
- **`windows-server-2025` is unaffected.** It is `external`: `publish`
  refuses it, so it was never in the bucket, and must stay that way --
  a Windows disk isn't ours to redistribute.
- **Third-party software in the images** (Claude Code, the Codex CLI,
  VS Code, Chromium) is already distributed publicly in the ghcr images
  or by its vendor's installer; check each one's terms allow
  redistribution in an image before step 5, `vscode` in particular.
- **Cost.** R2 doesn't charge for egress, only per request, and the
  custom domain puts Cloudflare's cache in front. Sha-named objects
  never change and can be cached forever; `images/latest/*` and
  `catalog/*` need a short TTL.
- **Integrity.** A public URL is also a URL someone might be tricked
  into replacing. The write token is CI-only, and a manifest's sha256 is
  the check; the README should say to use it.

## Not in this plan

- onctl fetching an image by name (`--rootfs-image debian-slim`
  resolving through the public catalog). The natural next step, and a
  change in onctl, once the URLs exist.
- Mirroring to ghcr or anywhere else.
- Moving the agent bundle.

## Open questions

- **The domain.** `images.boxctl.io` is assumed; any name on a zone in
  the same Cloudflare account works.
- **Should `desktop` and `vscode` be public from day one**, or only the
  Dockerfile-built images until the two bake scripts have had the same
  look for baked-in state as the Dockerfiles?
