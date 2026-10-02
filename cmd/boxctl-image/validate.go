package main

import (
	"fmt"
	"os"
	"path/filepath"

	"boxctl-vms/internal/imagecatalog"
)

// validate checks every definition and the rules across them, plus that
// each image CI bakes has the bake script build-fc-images.yml (or, for
// the kernel, build-fc-kernel.yml) will run for it. Run on every PR by
// TestRepoImagesValid.
func validate(imagesDir, scriptsDir string) ([]imagecatalog.Definition, error) {
	defs, err := imagecatalog.LoadDefinitions(imagesDir)
	if err != nil {
		return nil, err
	}
	if err := imagecatalog.ValidateSet(defs); err != nil {
		return nil, err
	}
	for _, d := range defs {
		if d.Source != imagecatalog.SourceBaked {
			continue
		}
		script := filepath.Join(scriptsDir, bakeScript(d))
		if _, err := os.Stat(script); err != nil {
			return nil, fmt.Errorf("%s is baked by CI but has no %s", d.Name, script)
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
