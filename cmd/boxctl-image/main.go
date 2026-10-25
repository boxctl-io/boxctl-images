// Command boxctl-image is the image pipeline's side of the image catalog
// (docs/plans/image-catalog.md): it validates images/*/image.json,
// decides which images a CI run builds, and publishes a built image --
// the compressed object plus its catalog/<name>.json manifest -- to the
// images bucket.
//
//	boxctl-image validate
//	boxctl-image matrix [-since <git-ref>] [-images a,b|all]
//	boxctl-image build <name> [-out <path>] [-from-tar <tar> | -tar-only <tar> | -bake-only <json>]
//	boxctl-image convert <ref> [-name <name>] [-out <path>] [-size-mib <n>]
//	boxctl-image publish <name> [-image <path>] [-metadata-only] [-git-sha <sha>]
//
// build makes the image at its host path with a <path>.sha256sum sidecar
// beside it: from images/<name>/Dockerfile (build.go), or by running its
// scripts/images/bake-<name>-image.sh for an image not moved over yet.
// convert makes one from a container image in a registry instead
// (convert.go); it isn't a catalog image, so nothing publishes it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/boxctl-io/boxctl-images/imagecatalog"
	"github.com/boxctl-io/boxctl-images/objectstore"
)

const usage = `usage:
  boxctl-image validate [-images dir] [-scripts dir]
  boxctl-image matrix [-images dir] [-since ref] [-only a,b|all]
  boxctl-image build <name> [-images dir] [-scripts dir] [-out path] [-from-tar tar | -tar-only tar | -bake-only json]
  boxctl-image convert <ref> [-images dir] [-name name] [-out path] [-size-mib n] [-no-service] [-from-tar tar | -tar-only tar]
  boxctl-image publish <name> [-images dir] [-image path] [-metadata-only] [-git-sha sha]

publish reads CF_ACCESS_KEY, CF_SECRET_ACCESS_KEY and CF_S3_ENDPOINT, and
CF_BUCKET (default boxctl-vms). For a store other than R2, CF_S3_REGION sets the signing
region (default auto) and CF_S3_PATH_STYLE=true addresses the bucket by path.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "validate":
		err = runValidate(os.Args[2:])
	case "matrix":
		err = runMatrix(os.Args[2:])
	case "build":
		err = runBuild(os.Args[2:])
	case "convert":
		err = runConvert(os.Args[2:])
	case "publish":
		err = runPublish(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "boxctl-image %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}

func runValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	imagesDir := fs.String("images", "images", "directory of image definitions")
	scriptsDir := fs.String("scripts", "scripts/images", "directory of bake scripts")
	_ = fs.Parse(args)

	defs, err := validate(*imagesDir, *scriptsDir)
	if err != nil {
		return err
	}
	fmt.Printf("%d images OK\n", len(defs))
	return nil
}

func runMatrix(args []string) error {
	fs := flag.NewFlagSet("matrix", flag.ExitOnError)
	imagesDir := fs.String("images", "images", "directory of image definitions")
	since := fs.String("since", "", "git ref to diff HEAD against; empty rebuilds everything")
	only := fs.String("only", "", "comma-separated images to rebuild, or 'all'; overrides -since")
	_ = fs.Parse(args)

	defs, err := imagecatalog.LoadDefinitions(*imagesDir)
	if err != nil {
		return err
	}
	var entries []matrixEntry
	if *only != "" && *only != "all" {
		entries, err = matrixFor(defs, strings.Split(*only, ","))
		if err != nil {
			return err
		}
	} else {
		var changed []string
		if *only == "" {
			changed = changedFiles(*since)
		}
		entries = planMatrix(*imagesDir, defs, changed)
	}
	out, err := githubOutput(entries)
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

func runBuild(args []string) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("an image name is required\n%s", usage)
	}
	name := args[0]
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	imagesDir := fs.String("images", "images", "directory of image definitions")
	scriptsDir := fs.String("scripts", "scripts/images", "directory of bake scripts")
	out := fs.String("out", "", "where the image lands (default: its host path)")
	fromTar := fs.String("from-tar", "", "skip the Docker build: make the image from this filesystem tar")
	tarOnly := fs.String("tar-only", "", "stop after the Docker build, leaving the filesystem tar here")
	bakeOnly := fs.String("bake-only", "", "stop before the Docker build, writing its buildx bake file here (for a docker buildx bake of your own: another platform, a registry push)")
	_ = fs.Parse(args[1:])

	defs, err := imagecatalog.LoadDefinitions(*imagesDir)
	if err != nil {
		return err
	}
	def, ok := imagecatalog.Find(defs, name)
	if !ok {
		return fmt.Errorf("no image %q in %s", name, *imagesDir)
	}
	if *out == "" {
		*out = imagecatalog.HostPath(def)
	}
	return newBuilder().build(buildRequest{
		def: def, imagesDir: *imagesDir, scriptsDir: *scriptsDir,
		out: *out, fromTar: *fromTar, tarOnly: *tarOnly, bakeOnly: *bakeOnly,
	})
}

func runPublish(args []string) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("an image name is required\n%s", usage)
	}
	name := args[0]
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	imagesDir := fs.String("images", "images", "directory of image definitions")
	imagePath := fs.String("image", "", "the built image (default: its host path)")
	metadataOnly := fs.Bool("metadata-only", false, "republish the manifest from image.json without uploading a new generation")
	gitSHA := fs.String("git-sha", "", "commit the image was built from, recorded in the manifest")
	_ = fs.Parse(args[1:])

	defs, err := imagecatalog.LoadDefinitions(*imagesDir)
	if err != nil {
		return err
	}
	def, ok := imagecatalog.Find(defs, name)
	if !ok {
		return fmt.Errorf("no image %q in %s", name, *imagesDir)
	}

	store, err := storeFromEnv()
	if err != nil {
		return err
	}

	path := *imagePath
	if path == "" {
		path = imagecatalog.HostPath(def)
	}
	p := newPublisher(store)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()
	m, err := p.publish(ctx, publishRequest{
		def:          def,
		imagePath:    path,
		gitSHA:       *gitSHA,
		metadataOnly: *metadataOnly,
	})
	if err != nil {
		return err
	}
	fmt.Printf("published %s: %s (%s)\n", imagecatalog.ManifestKey(m.Name), m.Current.SHA256, m.Status)
	return nil
}

// storeFromEnv is the images bucket, from publish's CF_* environment.
func storeFromEnv() (*objectstore.Store, error) {
	bucket := os.Getenv("CF_BUCKET")
	if bucket == "" {
		bucket = "boxctl-vms"
	}
	pathStyle := false
	if v := os.Getenv("CF_S3_PATH_STYLE"); v != "" {
		var err error
		if pathStyle, err = strconv.ParseBool(v); err != nil {
			return nil, fmt.Errorf("invalid CF_S3_PATH_STYLE %q: want true or false", v)
		}
	}
	return objectstore.Open(objectstore.Config{
		Endpoint:        os.Getenv("CF_S3_ENDPOINT"),
		Region:          os.Getenv("CF_S3_REGION"),
		Bucket:          bucket,
		PathStyle:       pathStyle,
		AccessKeyID:     os.Getenv("CF_ACCESS_KEY"),
		SecretAccessKey: os.Getenv("CF_SECRET_ACCESS_KEY"),
	})
}
