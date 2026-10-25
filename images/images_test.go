package images

import (
	"reflect"
	"testing"

	"github.com/boxctl-io/boxctl-images/imagecatalog"
)

// TestEmbeddedDefinitionsMatchTheDirectory: the server trusts the embedded
// set to be what this module version's images/ holds -- an image added to
// the directory must show up here without anyone listing it twice.
func TestEmbeddedDefinitionsMatchTheDirectory(t *testing.T) {
	embedded, err := Definitions()
	if err != nil {
		t.Fatal(err)
	}
	fromDir, err := imagecatalog.LoadDefinitions(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(embedded) == 0 || !reflect.DeepEqual(embedded, fromDir) {
		t.Fatalf("embedded %d definitions, directory %d; differ", len(embedded), len(fromDir))
	}
}
