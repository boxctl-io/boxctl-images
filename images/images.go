// Package images is the image definitions, embedded: images/<name>/image.json
// for every image, so a consumer that has this module but not a checkout --
// the boxctl-vms server, which falls back to the definitions when the
// images bucket's catalog can't be read -- gets the set its module version
// was built with, with nothing to copy into its container.
package images

import (
	"embed"

	"github.com/boxctl-io/boxctl-images/imagecatalog"
)

// FS holds <name>/image.json for every image in this directory.
//
//go:embed */image.json
var FS embed.FS

// Definitions is every image's definition, as imagecatalog.LoadDefinitions
// reads them from the directory.
func Definitions() ([]imagecatalog.Definition, error) {
	return imagecatalog.LoadDefinitionsFS(FS)
}
