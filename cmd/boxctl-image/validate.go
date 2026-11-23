package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/boxctl-io/boxctl-images/imagecatalog"
)

// validate checks every definition and the rules across them, plus how
// each image is built: a rootfs from images/<name>/Dockerfile, with a
// size_mib that fits the smallest box disk, and the kernel from its bake
// script. Run on every PR by TestRepoImagesValid.
func validate(imagesDir, scriptsDir string) ([]imagecatalog.Definition, error) {
	defs, err := imagecatalog.LoadDefinitions(imagesDir)
	if err != nil {
		return nil, err
	}
	if err := imagecatalog.ValidateSet(defs); err != nil {
		return nil, err
	}
	minDisk := smallestDiskMiB()
	for _, d := range defs {
		if d.Source == imagecatalog.SourceExternal {
			continue // nothing here builds it
		}
		if d.Kind == imagecatalog.KindKernel {
			script := filepath.Join(scriptsDir, kernelScript)
			if _, err := os.Stat(script); err != nil {
				return nil, fmt.Errorf("%s has no %s", d.Name, script)
			}
			continue
		}
		switch {
		case !hasDockerfile(imagesDir, d.Name):
			return nil, fmt.Errorf("%s has no %s", d.Name, filepath.Join(imagesDir, d.Name, dockerfileName))
		case d.SizeMiB <= 0:
			return nil, fmt.Errorf("%s is built from a Dockerfile, so it needs size_mib", d.Name)
		case d.SizeMiB > minDisk:
			return nil, fmt.Errorf("%s: size_mib %d is bigger than the smallest box disk (%d MiB)", d.Name, d.SizeMiB, minDisk)
		}
		if _, err := bakeFile(imagesDir, d.Name); err != nil {
			return nil, fmt.Errorf("%s: %w", d.Name, err)
		}
	}
	return defs, nil
}

// kernelScript is the script under scripts/images/ that builds the
// kernel: not a rootfs, so not a Dockerfile (build-fc-kernel.yml runs it).
const kernelScript = "bake-kernel-image.sh"

// smallestDiskMiB is the smallest disk a box can have; an image bigger
// than it couldn't boot at that size (onctl won't shrink one).
func smallestDiskMiB() int { return imagecatalog.SmallestBoxDiskMiB }
