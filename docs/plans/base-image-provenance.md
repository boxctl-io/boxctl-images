# Finding an old base image again after a re-bake

## The problem

A backup or export of a box is usually a **diff**, not a whole rootfs:
`internal/vmbundle`'s `Stream` computes `rootfs.diff` against the base
image the VM was created from, and records that base's identity in the
manifest (`baseImagePath`, `baseImageSha256`, `baseImageSizeBytes`).
That's why the 7 backups in the catalog are 66 MB–805 MB against rootfs
images of 4–20 GiB.

Restoring one needs the *same base image, byte for byte*. `Patch` ->
`resolveBaseImage` will use the recorded path only if its sha256 **and**
size still match, and otherwise falls back to `findBaseImageBySHA256`,
which hashes each of `fcImages`' paths looking for that digest.

So re-baking an image -- which `bake-*-image.sh` does with
`mv "$BUILD_IMAGE" "$IMAGE"`, replacing it in place -- makes every
existing diff bundle against that generation unrestorable. Not corrupt:
both lookups compare content, so the new image can't be mistaken for the
old one. You get `no local base image matches sha256 ... -- cannot
reconstruct rootfs from diff`, and the scheduler refuses to place the
restore at all (`image ... differs from the bundle's base image`).
Loud, and unfixable without the old bytes.

## What was *not* the problem

The bytes were never lost. `publish-*-image.sh` uploads each generation
under its own key and only *copies* it to the `images/latest/...`
pointer, so every published generation is still in R2, untouched.

What was missing is **addressability**. Keys were named by publish
timestamp; bundles identify a base by sha256 of the *uncompressed* ext4;
and the stored objects are zstd-compressed, so their own content hash is
something else again. Nothing mapped one to the other. Finding the right
generation meant downloading candidates, decompressing and hashing them,
at 4-20 GiB each.

The `images/latest/` pointer that `internal/vm/images.go`'s
`ImageObjectKey` fetches from doesn't help either: it's name-based, so a
host missing `debian-slim.ext4` gets whatever "latest" is *now* -- the
wrong content for an old bundle, which the hash check then correctly
refuses.

## What landed

**Published objects are named after the image's own sha256**, the same
digest a manifest records: `images/debian-slim.ext4-<sha256>.zst`. A
lookup is now a prefix search instead of a hunt. Re-publishing identical
bytes is idempotent (same key) rather than accumulating a new object per
run, and R2 still carries the publish time on the object itself, so
dropping the timestamp from the key loses nothing.

**`bake-*-image.sh` writes the identity sidecar** (`<image>.sha256sum`,
holding `<sha256> <size> <mtime-nanos>`) the moment the image lands.
This fixes a hazard of its own: `boxctl-vms-agent.sh`'s `host_images`
validates that sidecar by **size alone**, and these images are truncated
to a fixed size, so the previous bake's sidecar would otherwise keep
being reported as the new image's checksum -- and the controller would
schedule a cross-host restore onto a host whose base isn't the one the
bundle needs. (`rootfsdiff.Identity` re-validates on mtime as well, so
the restore would fail rather than patch against the wrong base -- but
only after being placed, with a confusing reason.) Publishing reads that
sidecar, size-checked the same way, instead of re-hashing 4-20 GiB.

**`scripts/images/find-image-by-sha.sh <sha256> [--link]`** turns the
digest from a failed restore into the R2 key, and optionally a presigned
URL plus the command to place that generation on a host *under a name of
its own*, never on top of the current image.

## Migration

Objects published before this are timestamped, so they can't be found by
digest. The images every current backup depends on are still on the
hosts, so the cheap backfill is to **re-publish each current generation
before re-baking anything**: same bytes, new sha-named key.

```sh
# on the host that has the images
sudo bash scripts/images/publish-debian-slim-image.sh
sudo bash scripts/images/publish-vscode-image.sh
sudo bash scripts/images/publish-desktop-image.sh
```

Then a re-bake is safe: the generation the old backups need is
addressable in R2, and the new one gets its own key.

## Still to build: fetch-by-digest on restore

Today the operator runs `find-image-by-sha.sh` and places the file. The
same thing can be automatic, and it's a small step from here because
every piece already exists:

1. `Patch` fails with `no local base image matches sha256 <sha>`. Give
   that failure a typed error carrying the digest rather than a string.
2. The controller constructs the key from the manifest's own
   `baseImagePath` basename plus the digest
   (`images/<basename>-<sha>.zst`) -- a direct GET, no listing -- and
   dispatches the existing `fetch-image.sh` agent job (already built for
   `POST /admin/hosts/{name}/images/sync`, already tolerant of multi-GiB
   downloads at `imageFetchTimeout` 45m).
3. It lands in an archive directory, e.g.
   `/opt/fc/images/archive/<basename>-<sha12>.ext4`, which
   `findBaseImageBySHA256` also scans. Not in `fcImages`: these are
   historical bases for restores, never selectable images for a create.
4. The scheduler's `hostFits` needs to tell "this host can't have it"
   from "this host can fetch it" -- otherwise placement rejects the
   restore before the fetch is ever attempted. This is the only part
   that isn't already built, and the reason it's a separate change.

The archive directory then works as a cache of R2 rather than as the
only copy, which is what makes it safe for a pruning rule to exist: a
pruned entry is re-fetchable, not lost.
