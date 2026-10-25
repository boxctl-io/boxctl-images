# boxctl-images

The images a boxctl box boots, and the tool that builds and publishes
them. Moved out of boxctl-vms (the server) so the images have one home
for everything that consumes them: Firecracker boxes (ext4 rootfs
images published to R2), the boxctl-vms server (which imports
`imagecatalog` to read the published catalog), and onctl's `ac` provider
(the `claude-agent` OCI image on ghcr).

- `images/<name>/`: one directory per image -- `image.json` (the catalog
  definition) and its `Dockerfile`, or a `scripts/images/bake-<name>-image.sh`
  for the ones not moved to a Dockerfile yet. `images/common/install.sh`
  is what every box rootfs needs; every Dockerfile runs it first.
- `cmd/boxctl-image`: `validate`, `matrix`, `build`, `convert`,
  `publish`. See README.md and docs/plans/.
- `imagecatalog`: the definitions and the published manifests' schema.
  boxctl-vms depends on this package; keep its exported API stable and
  bump the dependency there when it changes. `SmallestBoxDiskMiB` mirrors
  the server's smallest box size, checked by a test in boxctl-vms.
- `objectstore`: the S3/R2 client `publish` uses (boxctl-vms uses it too).

CI: `build.yml`/`lint.yml` on every PR; `build-fc-images.yml` and
`build-fc-kernel.yml` bake and publish to R2 on a push to main that
changes an image; `publish-oci-images.yml` pushes `claude-agent` to
`ghcr.io/boxctl-io/claude-agent` for amd64 and arm64. All run on the
`onctl-2` self-hosted pool (the onctl-runners GitHub App must be
installed on this repository). Publishing needs the `CF_ACCESS_KEY`,
`CF_SECRET_ACCESS_KEY` and `CF_S3_ENDPOINT` repository secrets.

Conventions: comments say why, not what; tests encode intent; one PR per
change; match the existing style.
