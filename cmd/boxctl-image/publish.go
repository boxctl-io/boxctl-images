package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/boxctl-io/boxctl-images/imagecatalog"
	"github.com/boxctl-io/boxctl-images/objectstore"
)

// store is the slice of objectstore.Store publishing needs, so tests can
// run it against an in-memory bucket.
type store interface {
	Head(ctx context.Context, key string) (objectstore.Object, error)
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string, metadata map[string]string) error
	Copy(ctx context.Context, src, dst string, metadata map[string]string) error
}

// maxObjectBytes is the most one PUT can upload (S3's and R2's
// single-request limit). The biggest image today compresses to ~1.1 GB;
// anything past this needs multipart upload, which this doesn't do.
const maxObjectBytes = 5 << 30

// metaSHA256 is the user metadata key every published object carries
// its uncompressed image's full digest under (x-amz-meta-sha256), so a
// 7-hex key name is never trusted on its own -- see
// docs/plans/base-image-provenance.md.
const metaSHA256 = "sha256"

type publisher struct {
	store store
	// compress writes src zstd-compressed to dst.
	compress func(src, dst string, level int) error
	now      func() time.Time
	// minDiskBytes is the smallest box disk; a Firecracker rootfs bigger
	// than it couldn't boot at that size (onctl won't shrink an image).
	minDiskBytes int64
}

func newPublisher(s store) *publisher {
	return &publisher{store: s, compress: zstdCompress, now: time.Now, minDiskBytes: int64(imagecatalog.SmallestBoxDiskMiB) << 20}
}

type publishRequest struct {
	def          imagecatalog.Definition
	imagePath    string
	gitSHA       string
	metadataOnly bool
}

// publish makes the bucket hold req's image and a manifest describing it:
//
//  1. Unless metadata-only, upload the image to its
//     sha-named key -- skipped when that key already holds this exact
//     image, so a re-run is cheap -- and refresh the name-based latest
//     pointer the server still fetches from.
//  2. Write catalog/<name>.json from image.json and that generation,
//     keeping the previous current generation in the history.
//
// The object always lands before the manifest naming it, so a reader
// never sees a manifest pointing at something that isn't there.
func (p *publisher) publish(ctx context.Context, req publishRequest) (imagecatalog.Manifest, error) {
	def := req.def
	prev, err := p.currentManifest(ctx, def.Name)
	if err != nil {
		return imagecatalog.Manifest{}, err
	}

	var gen *imagecatalog.Generation
	if !req.metadataOnly {
		gen, err = p.upload(ctx, def, req.imagePath, req.gitSHA)
		if err != nil {
			return imagecatalog.Manifest{}, err
		}
	}

	m, err := imagecatalog.Advance(prev, def, gen)
	if err != nil {
		return imagecatalog.Manifest{}, err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return imagecatalog.Manifest{}, err
	}
	data = append(data, '\n')
	key := imagecatalog.ManifestKey(def.Name)
	if err := p.store.Put(ctx, key, strings.NewReader(string(data)), int64(len(data)), "application/json", nil); err != nil {
		return imagecatalog.Manifest{}, err
	}
	return m, nil
}

// currentManifest returns the published manifest for name, or nil if
// there isn't one yet. One that exists but doesn't parse is an error, not
// "none": overwriting it would drop its generation history.
func (p *publisher) currentManifest(ctx context.Context, name string) (*imagecatalog.Manifest, error) {
	data, err := p.store.Get(ctx, imagecatalog.ManifestKey(name))
	if errors.Is(err, objectstore.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m, err := imagecatalog.ParseManifest(data)
	if err != nil {
		return nil, fmt.Errorf("the published %s doesn't parse, so publishing would lose its history: %w", imagecatalog.ManifestKey(name), err)
	}
	return &m, nil
}

func (p *publisher) upload(ctx context.Context, def imagecatalog.Definition, imagePath, gitSHA string) (*imagecatalog.Generation, error) {
	sha, size, err := imageIdentity(imagePath)
	if err != nil {
		return nil, err
	}
	log.Printf("%s: sha256 %s, %d bytes", imagePath, sha, size)
	if def.Kind == imagecatalog.KindRootfs && size > p.minDiskBytes {
		return nil, fmt.Errorf("%s is %d MiB, bigger than the smallest box disk (%d MiB), so no box of that size could boot it",
			def.Name, size>>20, p.minDiskBytes>>20)
	}

	key := imagecatalog.ObjectKey(def, sha)
	meta := map[string]string{metaSHA256: sha}
	obj, err := p.store.Head(ctx, key)
	switch {
	case err == nil && obj.Metadata[metaSHA256] == sha:
		log.Printf("%s already holds this image; not uploading it again", key)
	case err == nil || errors.Is(err, objectstore.ErrNotFound):
		if err := p.uploadCompressed(ctx, def, imagePath, key, meta); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}

	// Transitional: the server fetches images/latest/<file>.zst by name
	// until it reads manifests. Skip the copy (R2 duplicates the bytes at
	// ~18 MiB/s) when the pointer already holds this image.
	latest := imagecatalog.LatestKey(def)
	if obj, err := p.store.Head(ctx, latest); err == nil && obj.Metadata[metaSHA256] == sha {
		log.Printf("%s already holds this image", latest)
	} else if err != nil && !errors.Is(err, objectstore.ErrNotFound) {
		return nil, err
	} else {
		log.Printf("updating %s", latest)
		if err := p.store.Copy(ctx, key, latest, meta); err != nil {
			return nil, err
		}
	}

	return &imagecatalog.Generation{SHA256: sha, SizeBytes: size, Key: key, PublishedAt: p.now().UTC(), GitSHA: gitSHA}, nil
}

func (p *publisher) uploadCompressed(ctx context.Context, def imagecatalog.Definition, imagePath, key string, meta map[string]string) error {
	dir, err := os.MkdirTemp("", "boxctl-image-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// The kernel is small, so it gets the slow, tight level; rootfs
	// images are 4-8 GiB, where -19 takes far longer for little gain.
	level := 9
	if def.Kind == imagecatalog.KindKernel {
		level = 19
	}
	compressed := filepath.Join(dir, filepath.Base(imagePath)+".zst")
	log.Printf("compressing %s (zstd -%d)", imagePath, level)
	if err := p.compress(imagePath, compressed, level); err != nil {
		return err
	}
	f, err := os.Open(compressed)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() > maxObjectBytes {
		return fmt.Errorf("%s compresses to %d bytes, over the %d a single upload allows", def.Name, st.Size(), int64(maxObjectBytes))
	}
	log.Printf("uploading %s (%d MiB)", key, st.Size()>>20)
	return p.store.Put(ctx, key, f, st.Size(), "application/zstd", meta)
}

// imageIdentity returns path's sha256 and size, from the
// <path>.sha256sum sidecar bake (and fetch-image.sh) writes beside it
// when that sidecar's recorded size still matches the file -- the same
// check boxctl-vms-agent.sh's host_images makes -- and by hashing the file
// otherwise.
func imageIdentity(path string) (string, int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", 0, err
	}
	if st.Size() == 0 {
		return "", 0, fmt.Errorf("%s is empty", path)
	}
	if sha, ok := readSidecar(path+".sha256sum", st.Size()); ok {
		return sha, st.Size(), nil
	}
	log.Printf("no usable sidecar for %s; hashing it", path)
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, bufio.NewReaderSize(f, 1<<20)); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), st.Size(), nil
}

// readSidecar parses "<sha256> <size> [...]" and accepts it only for a
// file of exactly that size: a sidecar left over from an earlier bake
// must never name a new image.
func readSidecar(path string, size int64) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 || !imagecatalog.ValidSHA256(fields[0]) {
		return "", false
	}
	recorded, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || recorded != size {
		return "", false
	}
	return fields[0], true
}

func zstdCompress(src, dst string, level int) error {
	cmd := exec.Command("zstd", "-T0", "-"+strconv.Itoa(level), "-q", "-f", src, "-o", dst)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("zstd %s: %w", src, err)
	}
	return nil
}
