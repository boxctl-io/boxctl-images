# An image catalog the bake pipeline owns

> **Status:** rollout steps 1-3 are done -- definitions, `boxctl-image`
> and manifests (#117), the server reading the catalog (#118) -- and the
> builder swap is under way (`dockerfile-rootfs.md`, #123). Windows was
> dropped afterwards (#121), so everything below about `ch`,
> `windows-server-2025` and external images is history. Still open:
> step 4 (the tunnel agent's own image list), step 5 (boxctl-web and the
> CLI reading `/api/images`) and step 6 (older generations on hosts).

## The problem

Adding `ubuntu-26.04` (#110) touched seven files, and only two of them
were about Ubuntu:

| Where | What it hardcodes |
|---|---|
| `scripts/images/bake-ubuntu-26.04-image.sh` | a 188-line copy of `bake-debian-slim-image.sh` with the suite and mirror changed |
| `scripts/images/publish-ubuntu-26.04-image.sh` | a 142-line copy of `publish-debian-slim-image.sh` with **only the name** changed |
| `.github/workflows/build-fc-images.yml` | the matrix entry, and two `paths:` lines |
| `internal/vm/service.go` | `fcImages` (name -> path), `imageDescriptions`; `chImages` for Windows |
| `cmd/boxctl-tunnel-agent/main.go` | a second copy of `fcImages`, "keep in sync" |
| `README.md` | the list again |
| `boxctl-web/lib/images.ts`, `app/OsIcon.tsx` | a third copy, with label, `hasDesktop`, OS icon |

Plus the things that depend on that list without naming it: the kernel
(`fcKernelImage`, its own workflow and scripts), `TestSizeDisksFitEveryImage`
(the smallest box disk must hold the biggest image), the default image
(`debian-slim`, hardcoded in both `boxctl-web` and the `boxctl` CLI), and
the `mint-*`/`pull-*` scripts that predate `imagesync.go`.

So the set of images is decided in five places, by hand, in three repos,
and the server only learns about a new image when someone edits Go and
redeploys. The pipeline that actually produces the image -- and is the
only thing that knows it exists, what it's called, its size and its
sha256 -- tells nobody.

There's also a real bug hiding in it: `MissingImages` checks presence
by **path**, not content. When `build-fc-images.yml` publishes a new
generation of `debian-slim`, a host that already has an older
`debian-slim.ext4` reports it present and never gets the new one.
Hosts drift apart silently, and `fetch-image.sh`'s `EXPECT_SHA256` check
is never used because the server doesn't know what sha to expect.

## The shape of the fix

Invert the ownership. **An image is defined once, as data, in the repo;
the pipeline builds it with one generic builder and publishes a manifest
alongside it; the server reads the manifests at runtime.** Nothing else
names an image.

```
images/<name>/image.json  ──>  boxctl-image build  ──>  ext4 + sha
  (definition, in git)          (generic builder)          │
                                                           ▼
                               boxctl-image publish  ──>  R2: images/<name>.ext4-<sha7>.zst
                                                          R2: catalog/<name>.json   (manifest)
                                                           │
            server: catalog refresh (every few min) <──────┘
              ├─ GET /api/images        (boxctl-web, boxctl CLI render it)
              ├─ Create validation      (name -> host path, provider, min disk)
              ├─ imagesync              (fetch missing *or stale* by sha)
              └─ placement              (host must have the catalog's sha)
```

Adding an image becomes: add `images/<name>/`, merge. Retiring one is a
one-line status change, merged. No Go edit, no deploy, no web change.

## 1. Image definitions

> **Builder half superseded.** The build recipe in sections 1-2 (the
> `rootfs`/`steps` fields and `boxctl-image build` with chroot steps) is
> replaced by Dockerfiles -- see `dockerfile-rootfs.md`. Everything else
> here (catalog fields in `image.json`, `validate`, `publish`,
> manifests, server, workflow matrix) stands, and ships first on the
> existing bake scripts; see Rollout.

One directory per image under `images/` (top-level, not
`scripts/images/` -- these are definitions, not scripts):

```
images/
  debian-slim/image.json
  ubuntu-26.04/image.json
  desktop/image.json
  vscode/image.json
  steps/                 # shared, composable provisioning steps
    base-debian.sh       # sources.list, hostname, DNS, ...
    base-ubuntu.sh       # same, plus the hetzner/archive mirror probe and gutsy fallback
    chrony.sh            # docs/plans/guest-clock-sync.md
    ssh.sh               # enable sshd, pre-generate host keys
    xfce-vnc.sh
    firefox.sh
    vscode.sh
    python.sh
    desktop-user.sh      # the non-root sudo user vscode needs
```

```json
{
  "name": "ubuntu-26.04",
  "label": "Ubuntu 26.04 LTS",
  "description": "Minimal Ubuntu 26.04 LTS base, SSH-only",
  "provider": "fc",
  "os": "ubuntu",
  "status": "available",
  "features": [],
  "rootfs": {
    "size_mib": 4096,
    "distro": "ubuntu",
    "suite": "resolute",
    "variant": "minbase",
    "include": ["systemd", "systemd-sysv", "udev", "openssh-server", "curl",
                "ca-certificates", "iproute2", "iputils-ping", "sudo", "netbase"]
  },
  "steps": ["base-ubuntu", "chrony", "ssh"]
}
```

`vscode` is then `"steps": ["base-ubuntu", "universe", "xfce-vnc",
"firefox", "python", "vscode", "desktop-user", "chrony", "ssh"]` and
`"features": ["desktop"]`. The `features` list is what replaces
`boxctl-web`'s `hasDesktop` -- the "vscode was left out of the
`=== "desktop"` check" bug from that file's comment can't recur when the
image declares it itself.

The five existing bake scripts are, today, ~80% the same skeleton
(truncate, mkfs, loop-mount, debootstrap from a cached tarball, chroot
mounts, the steps, `apt-get clean`, `/opt/.baked`, unmount, atomic `mv`,
sha256 sidecar) wrapped around a few distinct install blocks. The
skeleton moves into the builder once; the distinct blocks become
`steps/`.

JSON rather than YAML because the builder and the server are both Go and
this repo keeps its dependency list to the standard library plus two.
Steps stay shell: they are `apt-get install` and file writes into a
chroot, which is what shell is for -- the builder runs each one as
`chroot $MOUNT /bin/bash -s < steps/<step>.sh` with a fixed, documented
environment (`BOXCTL_IMAGE_NAME`, `BOXCTL_SUITE`, `BOXCTL_MIRROR`, ...),
so a step can't depend on which image called it except through those.

### Non-baked images

`windows-server-2025` (`chImages`) is prepared out-of-band, and the
kernel has its own build. Both still go through the catalog, so there is
one list:

- **Kernel**: `images/boxctl-kernel/image.json` with `"kind": "kernel"`
  and a `build` that runs the existing `bake-kernel-image.sh` logic.
  `build-fc-kernel.yml` keeps its own trigger (kernel builds are slow and
  rare) but publishes through the same `boxctl-image publish`. The
  server's `fcKernelImage` constant goes away; the catalog has exactly
  one `kind: kernel, status: available` entry, and Create uses it.
- **Windows**: `images/windows-server-2025/image.json` with
  `"provider": "ch"` and `"source": "external"` -- the builder doesn't
  build it, but `boxctl-image publish --from <file>` uploads one an
  operator prepared, writing the same manifest. That also fixes the
  "never published, so its fetch 404s every time" note in `images.go`:
  until someone publishes it, it has no manifest, so it isn't in the
  catalog and nothing tries to fetch it.

## 2. One builder: `cmd/boxctl-image`

A Go command in this repo, replacing every `bake-*`, `publish-*`,
`mint-*` and `pull-*` script:

```
boxctl-image validate                 # every images/*/image.json; CI on every PR
boxctl-image matrix                   # JSON list of images whose inputs changed, for the workflow
boxctl-image build <name>             # -> /opt/fc/images/<name>.ext4 + .sha256sum
boxctl-image check <name>             # static smoke test (today's /opt/.baked check, plus size/fs checks)
boxctl-image publish <name> [--from f]# upload by sha, write catalog/<name>.json
```

Go for the orchestration because it's testable without a host (the
repo's hard rule): definition parsing, validation, the matrix diff, key
naming and manifest writing all get unit tests with fixtures. Only the
`build` subcommand shells out (debootstrap, mkfs, mount, chroot, zstd),
and it does so through one small runner interface so its sequencing is
testable too.

`publish` keeps the current R2 layout exactly -- `images/<file>-<sha7>.zst`
with `x-amz-meta-sha256`, so `find-image-by-sha.sh`,
`base-image-provenance.md`'s lookups and every existing object keep
working. What it drops is the `images/latest/` copy: the manifest
replaces the stable pointer (and its 18 MiB/s server-side copy on every
publish).

### `validate` -- the gate that replaces review-by-grep

Enforced on every PR (`lint.yml`), so a bad definition can't merge:

- `name` matches `^[a-z0-9][a-z0-9.-]{0,39}$` and equals the directory.
  The server derives the host path from the name and never takes a path
  from a manifest, so this regex is the security boundary -- see 4.
- Every step exists; `provider`/`kind`/`status` are known values.
- `rootfs.size_mib` fits the **smallest** box disk (today 8 GiB) --
  `TestSizeDisksFitEveryImage` moves here, checked against `size.go`'s
  `fcSizes` (the builder imports `internal/vm` for this, so the two can't
  disagree).
- Exactly one image has `"default": true`.

## 3. The manifest: `catalog/<name>.json` in R2

Written by `publish` to the images bucket, one object per image, so two
matrix jobs publishing different images never race on one file:

```json
{
  "schema": 1,
  "name": "ubuntu-26.04",
  "kind": "rootfs",
  "provider": "fc",
  "label": "Ubuntu 26.04 LTS",
  "description": "Minimal Ubuntu 26.04 LTS base, SSH-only",
  "os": "ubuntu",
  "features": [],
  "status": "available",
  "default": false,
  "current": {
    "sha256": "<64 hex>",
    "size_bytes": 4294967296,
    "key": "images/ubuntu-26.04.ext4-1a2b3c4.zst",
    "published_at": "2026-10-02T10:00:00Z",
    "git_sha": "ad302e0"
  },
  "previous": [
    { "sha256": "...", "key": "images/ubuntu-26.04.ext4-9f8e7d6.zst", "published_at": "..." }
  ]
}
```

`previous` is the generation history -- what makes an old diff bundle's
base findable by sha without listing the bucket (`base-image-provenance.md`
already put the bytes there; this makes them addressable from the
server).

**Status** drives everything downstream:

| status | listed in `/api/images` | new boxes | synced to hosts | restores |
|---|---|---|---|---|
| `available` | yes | yes | yes | yes |
| `hidden` | no | yes, by name | yes | yes |
| `deprecated` | yes, marked | yes | yes | yes |
| `retired` | no | no | no | yes, from `previous`/`current` |

`hidden` is how a new image lands: published and synced to hosts, but
not offered to users until someone boots it on real hardware -- the
boot smoke test `build-fc-images.yml` can't run on `onctl-4` (no
`/dev/kvm`). Flipping it to `available` is a one-line PR that only
republishes the manifest (`publish --metadata-only`), no rebake.

Changing only labels/status/description republishes the manifest and
nothing else; the matrix diff (`boxctl-image matrix`) tells the two
apart by hashing the definition's build-relevant fields separately from
its display fields.

## 4. Server: `internal/vm/catalog.go`

`fcImages`, `chImages`, `imageDescriptions` and `fcKernelImage` are
deleted. In their place:

- **`Catalog`**: loaded at startup and refreshed every 5 minutes by
  listing `catalog/` and reading each manifest, with the images key the
  server already has (`BOXCTL_R2_IMAGES_*`, read-only -- it needs
  `ListBucket` on the `catalog/` prefix as well as `GetObject`).
- **Last known good on disk**: `data/images.json`, written after every
  successful refresh, read at startup if R2 is unreachable. A restart
  during an R2 outage keeps every image it had; an unparseable or
  schema-unknown manifest is skipped with a log line, never allowed to
  empty the catalog. If there's no cache and no R2 either (local/dev),
  the catalog is empty and `Create` returns `ErrInvalidImage` --
  explicit, rather than a compiled-in fallback list that would be the
  hardcoded list again under another name.
- **Host paths are derived, never read**: `/opt/fc/images/<name>.ext4`
  for fc rootfs, `/opt/fc/images/<name>.vmlinux` for the kernel,
  `/opt/win-images/<name>.raw` for ch. The name already passed the
  regex at publish time and is re-checked on load. Whoever can write to
  the images bucket can add an image -- that's already true of the
  bytes today -- but can't make the server pass an arbitrary path to
  `onctl --rootfs-image`.
- **`Images()`** returns the catalog's listable entries, now with
  `label`, `os`, `features`, `status`, `default`, `min_disk_mib`. Same
  route, additive fields, so old clients keep working.
- **Only offer what can be placed**: an `available` image no placeable
  host has yet is listed with `"ready": false` (the web disables it),
  instead of letting a create fail at placement for the first few
  minutes after a publish.
- **`MissingImages` compares sha, not path**: a host whose
  `facts.Images[path]` sha differs from `current.sha256` is *stale* and
  gets the new generation through the same one-at-a-time sync, now with
  `EXPECT_SHA256` set so `fetch-image.sh` verifies what it installed. A
  host reporting an empty sha (no sidecar) is treated as stale too --
  that's the "present, checksum unknown" case, and re-fetching once
  fixes it for good. `fetch-image.sh` keeps its atomic `mv`; existing
  boxes are unaffected because `onctl create` copies the base.
- **Placement prefers current**: a host with an older generation can
  still take the box (it's a valid image, just older), but scores below
  one with the current sha, so drift doesn't block creates while sync
  catches up.
- **`retired`** images are removed from `MissingImages` and listing, but
  stay resolvable for restore/import.
- **Size**: `Create` rejects a size whose `DiskMiB` is below the image's
  `size_bytes` (validate already prevents this at publish; this is the
  runtime guard that replaces the test).

### Restore and the tunnel agent's copy

`cmd/boxctl-tunnel-agent`'s duplicated `fcImages` exists only for
`findBaseImageBySHA256`. It goes away: the server already knows every
host's images and their sha (`HostFacts.Images`), so it resolves the
base path itself and puts it in the `tunnelRequest` (`BaseImagePath`).
The agent verifies the sha it was told (it already re-hashes via
`rootfsdiff.Identity`) and never needs to know image names. A
`TunnelKind` doesn't change, but the request gains a field -- old agents
ignore it and fall back to their own list, so the server can deploy
first.

`internal/vm/import.go`'s local-mode copy iterates the catalog instead.

When no host has the bundle's base sha, the server can now look it up
in the catalog's `previous` list and fetch *that* generation (to a
sha-suffixed path, `/opt/fc/images/<name>.ext4@<sha7>`) before
restoring -- the "unfixable without the old bytes" case from
`base-image-provenance.md`, closed. This is a follow-up, not part of
the first cut.

## 5. The workflow

`build-fc-images.yml` stops listing images:

```yaml
on:
  push:
    branches: [main]
    paths: ["images/**", "cmd/boxctl-image/**", ".github/workflows/build-fc-images.yml"]
  workflow_dispatch:
    inputs: { images: { description: "comma-separated, or 'all'" } }

jobs:
  plan:
    runs-on: ubuntu-latest
    outputs: { matrix: ${{ steps.m.outputs.matrix }} }
    steps:
      - uses: actions/checkout@v7
        with: { fetch-depth: 2 }
      - id: m
        run: echo "matrix=$(go run ./cmd/boxctl-image matrix --since HEAD~1)" >> "$GITHUB_OUTPUT"
  bake-and-publish:
    needs: plan
    if: needs.plan.outputs.matrix != '[]'
    strategy: { fail-fast: false, matrix: { image: ${{ fromJSON(needs.plan.outputs.matrix) }} } }
    runs-on: onctl-4
    steps:
      - build, check, publish (one generic command each)
```

`matrix` rebuilds an image when its `image.json`, any step it uses, or
the builder itself changed -- so editing `steps/chrony.sh` rebuilds
every image that uses it, which today means editing five scripts. A
scheduled weekly run (`all`) picks up distro security updates.

## What clients change (separate PRs, after the server)

- **boxctl-web**: `lib/images.ts`'s `BOOT_IMAGES` and `DEFAULT_IMAGE`
  are replaced by `GET /api/images`;
  `hasDesktop` -> `features.includes("desktop")`, `fixedSize` ->
  `provider === "ch"`, `OsIcon`'s map -> the `os` field.
- **boxctl CLI**: `defaultImage` comes from the catalog's `default`
  entry. `-i` with no value still means "the default", so nothing breaks
  for users.

Both keep working against an old server until then, because names don't
change.

## Rollout

Each step ships and is verified on its own; no step needs another repo
to move at the same time.

1. **Definitions + manifests, on the existing bake scripts.** Add
   `images/<name>/image.json` (catalog fields only) and
   `cmd/boxctl-image validate|matrix|publish`. One `publish` replaces
   the four `publish-*-image.sh` and writes `catalog/<name>.json`; the
   kernel's publish writes one too. Backfill manifests for the four
   current images from their existing objects (sha already in
   metadata), the kernel, and -- once someone uploads it -- Windows.
   The workflow's matrix comes from `images/*/image.json` but still
   runs `bake-<name>-image.sh`. Delete `publish-*`/`mint-*`/`pull-*`.
2. **Builder swap** -- `dockerfile-rootfs.md`, image by image, any time
   after step 1. Independent of steps 3-6.
3. **Server reads the catalog.** Deploy with the catalog loader, sha-
   based `MissingImages`, `EXPECT_SHA256`, and `/api/images` extensions.
   Check `GET /hosts` before and after: every host should list the same
   images, and any stale ones should resync. This is the step that
   deletes `fcImages`/`chImages`/`imageDescriptions`/`fcKernelImage`.
4. **Tunnel agent** stops carrying its own list (server sends
   `BaseImagePath`). Server first, then agents.
5. **Clients** read `/api/images` (boxctl-web, then the CLI).
6. **Generations on hosts** for restoring against `previous` -- only if
   restores of re-baked bases are actually being hit.

## Open questions

- **ListBucket on the images key.** If the existing read-only key can't
  list, either widen it to the `catalog/` prefix or have `publish` also
  maintain a small `catalog/index.json` (one name per line, written
  under a workflow `concurrency:` group so jobs can't race on it).
- **Who flips `hidden` -> `available`?** A PR is the proposal above. A
  boot-test job on aimax (through the agent, not SSH) could do it
  automatically later, but that's a new capability, not part of this.
- **Retiring removes the file from hosts?** Proposed: no -- retired
  images stop syncing but aren't deleted, because a restore may still
  need them. Disk pressure is handled by the existing disk-low skip; a
  `prune` is a later, explicit operation.
