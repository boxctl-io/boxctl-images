package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/boxctl-io/boxctl-images/imagecatalog"
)

// validate checks every definition and the rules across them, plus how
// each image is built: from images/<name>/Dockerfile (with a size_mib
// that fits the smallest box disk) or from its bake script, never both
// and never neither. Run on every PR by TestRepoImagesValid.
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
		script := filepath.Join(scriptsDir, bakeScript(d))
		_, err := os.Stat(script)
		hasScript := err == nil
		if d.Kind == imagecatalog.KindKernel {
			if !hasScript {
				return nil, fmt.Errorf("%s has no %s", d.Name, script)
			}
			continue
		}
		switch df := hasDockerfile(imagesDir, d.Name); {
		case df && hasScript:
			return nil, fmt.Errorf("%s has both a Dockerfile and %s; delete the script once the Dockerfile replaces it", d.Name, script)
		case !df && !hasScript:
			return nil, fmt.Errorf("%s has neither a Dockerfile nor %s", d.Name, script)
		case df && d.SizeMiB <= 0:
			return nil, fmt.Errorf("%s is built from a Dockerfile, so it needs size_mib", d.Name)
		case df:
			if d.SizeMiB > minDisk {
				return nil, fmt.Errorf("%s: size_mib %d is bigger than the smallest box disk (%d MiB)", d.Name, d.SizeMiB, minDisk)
			}
			if _, err := bakeFile(imagesDir, d.Name); err != nil {
				return nil, fmt.Errorf("%s: %w", d.Name, err)
			}
		}
	}
	return defs, nil
}

// bakeScript is the script under scripts/images/ that builds d.
func bakeScript(d imagecatalog.Definition) string {
	if d.Kind == imagecatalog.KindKernel {
		return "bake-kernel-image.sh"
	}
	return "bake-" + d.Name + "-image.sh"
}

// smallestDiskMiB is the smallest disk a box can have; an image bigger
// than it couldn't boot at that size (onctl won't shrink one).
func smallestDiskMiB() int { return imagecatalog.SmallestBoxDiskMiB }
