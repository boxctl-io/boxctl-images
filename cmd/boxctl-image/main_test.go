package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"boxctl-vms/internal/imagecatalog"
	"boxctl-vms/internal/objectstore"
)

// TestRepoImagesValid is the gate on images/: every definition in the
// repo must validate, as a set, with a bake script for each baked image.
func TestRepoImagesValid(t *testing.T) {
	if _, err := validate("../../images", "../../scripts/images"); err != nil {
		t.Fatal(err)
	}
}

func defs(t *testing.T) []imagecatalog.Definition {
	t.Helper()
	d, err := imagecatalog.LoadDefinitions("../../images")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func names(entries []matrixEntry) string {
	var s []string
	for _, e := range entries {
		s = append(s, fmt.Sprintf("%s:%v", e.Image, e.Rebuild))
	}
	return strings.Join(s, ",")
}

func TestPlanMatrix(t *testing.T) {
	d := defs(t)
	cases := map[string]struct {
		changed []string
		want    string
	}{
		"everything":                           {nil, "debian-slim:true,desktop:true,ubuntu-26.04:true,vscode:true"},
		"workflow":                             {[]string{workflowFile}, "debian-slim:true,desktop:true,ubuntu-26.04:true,vscode:true"},
		"one bake script":                      {[]string{"scripts/images/bake-desktop-image.sh"}, "desktop:true"},
		"one definition":                       {[]string{"images/vscode/image.json"}, "vscode:false"},
		"both":                                 {[]string{"images/vscode/image.json", "scripts/images/bake-vscode-image.sh"}, "vscode:true"},
		"kernel isn't built":                   {[]string{"scripts/images/bake-kernel-image.sh", "images/boxctl-kernel/image.json"}, ""},
		"unrelated":                            {[]string{"README.md"}, ""},
		"a Dockerfile":                         {[]string{"images/debian-slim/Dockerfile"}, "debian-slim:true"},
		"the builder":                          {[]string{"cmd/boxctl-image/build.go"}, "debian-slim:true,ubuntu-26.04:true"},
		"the shared rootfs layer":              {[]string{"images/common/install.sh"}, "debian-slim:true,ubuntu-26.04:true"},
		"a Dockerfile image's definition only": {[]string{"images/ubuntu-26.04/image.json"}, "ubuntu-26.04:false"},
		"nothing changed at all":               {[]string{}, ""},
	}
	for name, c := range cases {
		if got := names(planMatrix("../../images", d, c.changed)); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

func TestPlanMatrixRetired(t *testing.T) {
	d := defs(t)
	for i := range d {
		if d[i].Name == "desktop" {
			d[i].Status = imagecatalog.StatusRetired
		}
	}
	if got := names(planMatrix("../../images", d, nil)); strings.Contains(got, "desktop") {
		t.Errorf("a retired image was rebuilt: %s", got)
	}
	if got := names(planMatrix("../../images", d, []string{"images/desktop/image.json"})); got != "desktop:false" {
		t.Errorf("retiring an image didn't republish its manifest: %s", got)
	}
	if _, err := matrixFor(d, []string{"desktop"}); err == nil {
		t.Error("a manual run rebuilt a retired image")
	}
}

func TestMatrixFor(t *testing.T) {
	d := defs(t)
	got, err := matrixFor(d, []string{"vscode", " debian-slim "})
	if err != nil {
		t.Fatal(err)
	}
	if names(got) != "vscode:true,debian-slim:true" {
		t.Fatalf("got %s", names(got))
	}
	for _, bad := range []string{"nope", "boxctl-kernel"} {
		if _, err := matrixFor(d, []string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestChangedFilesWithoutABase(t *testing.T) {
	for _, since := range []string{"", "0000000000000000000000000000000000000000"} {
		if changedFiles(since) != nil {
			t.Errorf("%q: want nil (everything)", since)
		}
	}
}

func TestGithubOutput(t *testing.T) {
	out, err := githubOutput([]matrixEntry{})
	if err != nil {
		t.Fatal(err)
	}
	if out != "matrix={\"include\":[]}\ncount=0\n" {
		t.Fatalf("got %q", out)
	}
}

// memStore is an in-memory bucket.
type memStore struct {
	objects map[string][]byte
	meta    map[string]map[string]string
	puts    []string
	copies  []string
}

func newMemStore() *memStore {
	return &memStore{objects: map[string][]byte{}, meta: map[string]map[string]string{}}
}

func (m *memStore) Head(_ context.Context, key string) (objectstore.Object, error) {
	data, ok := m.objects[key]
	if !ok {
		return objectstore.Object{}, objectstore.ErrNotFound
	}
	return objectstore.Object{Size: int64(len(data)), Metadata: m.meta[key]}, nil
}

func (m *memStore) Get(_ context.Context, key string) ([]byte, error) {
	data, ok := m.objects[key]
	if !ok {
		return nil, objectstore.ErrNotFound
	}
	return data, nil
}

func (m *memStore) Put(_ context.Context, key string, body io.ReadSeeker, _ int64, _ string, meta map[string]string) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	m.objects[key], m.meta[key] = data, meta
	m.puts = append(m.puts, key)
	return nil
}

func (m *memStore) Copy(_ context.Context, src, dst string, meta map[string]string) error {
	data, ok := m.objects[src]
	if !ok {
		return errors.New("no such source")
	}
	m.objects[dst], m.meta[dst] = data, meta
	m.copies = append(m.copies, dst)
	return nil
}

func testPublisher(s *memStore) *publisher {
	return &publisher{
		store: s,
		compress: func(src, dst string, _ int) error {
			data, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			return os.WriteFile(dst, append([]byte("zstd:"), data...), 0o644)
		},
		now:          func() time.Time { return time.Unix(1_700_000_000, 0) },
		minDiskBytes: 1 << 20,
	}
}

func find(t *testing.T, name string) imagecatalog.Definition {
	t.Helper()
	d, ok := imagecatalog.Find(defs(t), name)
	if !ok {
		t.Fatalf("no %s", name)
	}
	return d
}

func writeImage(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "image.ext4")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPublish(t *testing.T) {
	s := newMemStore()
	p := testPublisher(s)
	def := find(t, "ubuntu-26.04")
	ctx := context.Background()

	img := writeImage(t, "generation one")
	m, err := p.publish(ctx, publishRequest{def: def, imagePath: img, gitSHA: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	key := imagecatalog.ObjectKey(def, m.Current.SHA256)
	if m.Current.Key != key || m.Current.SizeBytes != int64(len("generation one")) || m.Current.GitSHA != "abc123" {
		t.Fatalf("manifest: %+v", m.Current)
	}
	if string(s.objects[key]) != "zstd:generation one" || s.meta[key]["sha256"] != m.Current.SHA256 {
		t.Fatal("object wasn't uploaded compressed with its sha256 metadata")
	}
	if !bytes.Equal(s.objects[imagecatalog.LatestKey(def)], s.objects[key]) {
		t.Fatal("latest pointer wasn't updated")
	}
	if _, err := imagecatalog.ParseManifest(s.objects[imagecatalog.ManifestKey(def.Name)]); err != nil {
		t.Fatalf("published manifest doesn't parse: %v", err)
	}
	// The object must land before the manifest that names it.
	if s.puts[len(s.puts)-1] != imagecatalog.ManifestKey(def.Name) {
		t.Fatalf("manifest wasn't written last: %v", s.puts)
	}

	// Same image again: no upload, no copy, same generation.
	s.puts, s.copies = nil, nil
	m2, err := p.publish(ctx, publishRequest{def: def, imagePath: img})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.puts) != 1 || len(s.copies) != 0 || m2.Current != m.Current {
		t.Fatalf("re-publish wasn't idempotent: puts %v copies %v", s.puts, s.copies)
	}

	// A new build moves the old one into history.
	m3, err := p.publish(ctx, publishRequest{def: def, imagePath: writeImage(t, "generation two")})
	if err != nil {
		t.Fatal(err)
	}
	if len(m3.Previous) != 1 || m3.Previous[0].SHA256 != m.Current.SHA256 {
		t.Fatalf("history: %+v", m3.Previous)
	}

	// Metadata-only: status changes, generations don't, nothing uploads.
	s.puts = nil
	def.Status = imagecatalog.StatusDeprecated
	m4, err := p.publish(ctx, publishRequest{def: def, metadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if m4.Status != imagecatalog.StatusDeprecated || m4.Current != m3.Current || len(s.puts) != 1 {
		t.Fatalf("metadata-only: %+v, puts %v", m4, s.puts)
	}
}

func TestPublishUsesSidecarOnlyWhenItMatches(t *testing.T) {
	img := writeImage(t, "content")
	fake := strings.Repeat("f", 64)
	if err := os.WriteFile(img+".sha256sum", []byte(fake+" 7 123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, _, err := imageIdentity(img)
	if err != nil || sha != fake {
		t.Fatalf("matching sidecar ignored: %s %v", sha, err)
	}
	// Stale: recorded size differs from the file's.
	if err := os.WriteFile(img+".sha256sum", []byte(fake+" 8 123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, _, err = imageIdentity(img)
	if err != nil || sha == fake {
		t.Fatalf("stale sidecar trusted: %s %v", sha, err)
	}
}

func TestPublishRefuses(t *testing.T) {
	ctx := context.Background()
	p := testPublisher(newMemStore())

	if _, err := p.publish(ctx, publishRequest{def: find(t, "debian-slim"), metadataOnly: true}); err == nil {
		t.Error("metadata-only publish of a never-published image succeeded")
	}
	big := writeImage(t, strings.Repeat("x", 2<<20))
	if _, err := p.publish(ctx, publishRequest{def: find(t, "debian-slim"), imagePath: big}); err == nil {
		t.Error("an image bigger than the smallest box disk was published")
	}

	s := newMemStore()
	s.objects[imagecatalog.ManifestKey("debian-slim")] = []byte("{not json")
	if _, err := testPublisher(s).publish(ctx, publishRequest{def: find(t, "debian-slim"), imagePath: writeImage(t, "x")}); err == nil {
		t.Error("overwrote a manifest that didn't parse")
	}
}
