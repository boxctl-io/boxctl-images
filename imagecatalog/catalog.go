// Package imagecatalog is the one definition of what a boxctl image is:
// the image.json each image declares under images/<name>/, the manifest
// the pipeline publishes for it to R2 (catalog/<name>.json), and the
// naming rules both sides derive host paths and object keys from. See
// docs/plans/image-catalog.md.
//
// cmd/boxctl-image writes manifests with it; the server reads them. Host
// paths and object keys are always *derived* from an image's name and
// kind here, never read from a manifest -- the name regex is what keeps
// whoever can write to the images bucket from making the server hand an
// arbitrary path to `onctl --rootfs-image`.
package imagecatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// SmallestBoxDiskMiB is the smallest disk a box can have (boxctl-vms's
// internal/vm size table, its "small"): an image bigger than it couldn't
// boot at that size, since onctl won't shrink one, so publish and
// validate refuse such an image. boxctl-vms has a test holding the two in
// step; change them together.
const SmallestBoxDiskMiB = 10 * 1024

// SchemaVersion is the manifest format this package writes and the only
// one it reads. A reader skips a manifest with any other version rather
// than guessing at its fields.
const SchemaVersion = 1

// Kinds.
const (
	KindRootfs = "rootfs"
	KindKernel = "kernel"
)

// Providers, matching onctl's -p values and VM.Provider.
const (
	ProviderFC = "fc"
)

// Statuses. See image-catalog.md's status table for what each one means
// downstream.
const (
	StatusAvailable  = "available"
	StatusHidden     = "hidden"
	StatusDeprecated = "deprecated"
	StatusRetired    = "retired"
)

// Sources.
const (
	// SourceBaked: built and published by CI (build-fc-images.yml or
	// build-fc-kernel.yml).
	SourceBaked = "baked"
)

// Features.
const (
	// FeatureDesktop: boots with a VNC server running.
	FeatureDesktop = "desktop"
)

// FCImageDir is where every host keeps its images, and where
// boxctl-vms-agent.sh's host_images reports them from.
const FCImageDir = "/opt/fc/images"

// ManifestPrefix is where manifests live in the images bucket.
const ManifestPrefix = "catalog/"

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,39}$`)

// ValidName reports whether name can name an image. It becomes a file
// name on every host, so no slashes, nothing leading with a dot, and short.
func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

// Definition is one image's images/<name>/image.json: everything about
// it that a person decides. What the pipeline learns by building it (its
// sha256, size, object key) lives in a Generation instead.
type Definition struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Provider string `json:"provider"`
	Source   string `json:"source"`
	// Label and Description are what boxctl-web and the boxctl CLI show.
	Label       string `json:"label"`
	Description string `json:"description"`
	// OS is the guest's distro family ("debian", "ubuntu"),
	// for an icon -- not a version.
	OS       string   `json:"os"`
	Features []string `json:"features"`
	// SizeMiB is the size of the ext4 a Dockerfile-built rootfs is made
	// into (`boxctl-image build`); a box's own disk is grown from it at
	// create time. Unset for an image whose bake script picks its own.
	SizeMiB int    `json:"size_mib,omitempty"`
	Status  string `json:"status"`
	// Default marks the image a client preselects. Exactly one image has it.
	Default bool `json:"default"`
}

// Validate checks one definition on its own; ValidateSet checks the rules
// that span all of them.
func (d Definition) Validate() error {
	if !ValidName(d.Name) {
		return fmt.Errorf("name %q must match %s", d.Name, namePattern)
	}
	switch d.Kind {
	case KindRootfs, KindKernel:
	default:
		return fmt.Errorf("%s: kind %q must be %q or %q", d.Name, d.Kind, KindRootfs, KindKernel)
	}
	if d.Provider != ProviderFC {
		return fmt.Errorf("%s: provider %q must be %q", d.Name, d.Provider, ProviderFC)
	}
	if d.Source != SourceBaked {
		return fmt.Errorf("%s: source %q must be %q", d.Name, d.Source, SourceBaked)
	}
	switch d.Status {
	case StatusAvailable, StatusHidden, StatusDeprecated, StatusRetired:
	default:
		return fmt.Errorf("%s: unknown status %q", d.Name, d.Status)
	}
	if d.Kind == KindRootfs && (d.Label == "" || d.Description == "" || d.OS == "") {
		return fmt.Errorf("%s: label, description and os are required", d.Name)
	}
	if d.SizeMiB < 0 || (d.SizeMiB > 0 && d.Kind != KindRootfs) {
		return fmt.Errorf("%s: size_mib is only for a rootfs, and must be positive", d.Name)
	}
	for _, f := range d.Features {
		if f != FeatureDesktop {
			return fmt.Errorf("%s: unknown feature %q", d.Name, f)
		}
	}
	if d.Default && (d.Kind != KindRootfs || d.Status != StatusAvailable) {
		return fmt.Errorf("%s: only an available rootfs image can be the default", d.Name)
	}
	return nil
}

// ValidateSet checks every definition plus the rules across them: unique
// names, exactly one default, and exactly one kernel boxes can boot.
func ValidateSet(defs []Definition) error {
	seen := map[string]bool{}
	defaults, kernels := 0, 0
	for _, d := range defs {
		if err := d.Validate(); err != nil {
			return err
		}
		if seen[d.Name] {
			return fmt.Errorf("image %q is defined twice", d.Name)
		}
		seen[d.Name] = true
		if d.Default {
			defaults++
		}
		if d.Kind == KindKernel && d.Status != StatusRetired {
			kernels++
		}
	}
	if defaults != 1 {
		return fmt.Errorf("exactly one image must be the default, found %d", defaults)
	}
	if kernels != 1 {
		return fmt.Errorf("exactly one kernel must be in use, found %d", kernels)
	}
	return nil
}

// HostPath is where every host keeps d's image.
func HostPath(d Definition) string {
	if d.Kind == KindKernel {
		return path.Join(FCImageDir, d.Name+".vmlinux")
	}
	return path.Join(FCImageDir, d.Name+".ext4")
}

// objectPrefix is the R2 prefix d's generations are published under:
// kernels separately from rootfs images, as they always have been.
func objectPrefix(d Definition) string {
	if d.Kind == KindKernel {
		return "kernel/"
	}
	return "images/"
}

// ObjectKey is where the generation of d with this sha256 is published:
// named by the short form of the *uncompressed* image's digest, which is
// what a backup's manifest records as its base (see
// docs/plans/base-image-provenance.md).
func ObjectKey(d Definition, sha256 string) string {
	short := sha256
	if len(short) > 7 {
		short = short[:7]
	}
	return objectPrefix(d) + path.Base(HostPath(d)) + "-" + short + ".zst"
}

// LatestKey is the name-based pointer the server fetches from until it
// reads manifests (images/latest/<file>.zst). Transitional: it goes away
// once nothing fetches by it.
func LatestKey(d Definition) string {
	return objectPrefix(d) + "latest/" + path.Base(HostPath(d)) + ".zst"
}

// ManifestKey is where d's manifest lives in the images bucket.
func ManifestKey(name string) string {
	return ManifestPrefix + name + ".json"
}

// Generation is one published build of an image.
type Generation struct {
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	// Key is the zstd-compressed object in the images bucket.
	Key         string    `json:"key,omitempty"`
	PublishedAt time.Time `json:"published_at"`
	GitSHA      string    `json:"git_sha,omitempty"`
}

// Manifest is catalog/<name>.json: an image's definition plus what has
// been published of it.
type Manifest struct {
	Schema int `json:"schema"`
	Definition
	// Current is what new boxes boot and hosts should hold.
	Current Generation `json:"current"`
	// Previous holds earlier generations, newest first, so a backup taken
	// against one can still find its base.
	Previous []Generation `json:"previous,omitempty"`
}

// maxPrevious bounds Previous. Generations past it are still in R2,
// just no longer listed.
const maxPrevious = 50

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidSHA256 reports whether s is a lowercase hex sha256 digest.
func ValidSHA256(s string) bool {
	return sha256Pattern.MatchString(s)
}

// Advance returns the manifest after publishing d, given the one
// currently published (nil if none). gen is the generation just
// published, or nil for a metadata-only publish, which keeps the current
// generation and only refreshes d's fields.
//
// Publishing the generation that's already current changes nothing about
// the generations: re-running a publish is idempotent.
func Advance(prev *Manifest, d Definition, gen *Generation) (Manifest, error) {
	m := Manifest{Schema: SchemaVersion, Definition: d}
	if prev != nil {
		m.Current = prev.Current
		m.Previous = append([]Generation(nil), prev.Previous...)
	}
	if gen != nil && gen.SHA256 != m.Current.SHA256 {
		if m.Current.SHA256 != "" {
			m.Previous = append([]Generation{m.Current}, m.Previous...)
		}
		m.Current = *gen
		// A sha already in Previous (republishing an old build) is listed
		// once, as current.
		kept := m.Previous[:0]
		for _, g := range m.Previous {
			if g.SHA256 != gen.SHA256 {
				kept = append(kept, g)
			}
		}
		m.Previous = kept
		if len(m.Previous) > maxPrevious {
			m.Previous = m.Previous[:maxPrevious]
		}
	}
	if m.Current.SHA256 == "" {
		return Manifest{}, fmt.Errorf("%s has never been published, so there's nothing to update", d.Name)
	}
	return m, m.Validate()
}

// Validate checks a manifest as a reader must before trusting it.
func (m Manifest) Validate() error {
	if m.Schema != SchemaVersion {
		return fmt.Errorf("manifest schema %d, want %d", m.Schema, SchemaVersion)
	}
	if err := m.Definition.Validate(); err != nil {
		return err
	}
	for _, g := range append([]Generation{m.Current}, m.Previous...) {
		if !ValidSHA256(g.SHA256) {
			return fmt.Errorf("%s: generation sha256 %q isn't a sha256", m.Name, g.SHA256)
		}
		if g.SizeBytes <= 0 {
			return fmt.Errorf("%s: generation %s has no size", m.Name, g.SHA256[:7])
		}
		if g.Key != "" && g.Key != ObjectKey(m.Definition, g.SHA256) {
			return fmt.Errorf("%s: generation key %q isn't where %s publishes it", m.Name, g.Key, g.SHA256[:7])
		}
	}
	if m.Current.Key == "" {
		return fmt.Errorf("%s: the current generation must have an object", m.Name)
	}
	return nil
}

// ParseManifest decodes and validates one manifest.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("decoding manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// DefinitionFile is the name of an image's definition in its directory.
const DefinitionFile = "image.json"

// LoadDefinitions reads every <dir>/<name>/image.json, sorted by name.
// Unknown fields are an error, so a typo can't silently do nothing, and
// each image's name must match its directory.
func LoadDefinitions(dir string) ([]Definition, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var defs []Definition
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		file := filepath.Join(dir, e.Name(), DefinitionFile)
		data, err := os.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		d, err := parseDefinition(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if d.Name != e.Name() {
			return nil, fmt.Errorf("%s: name %q doesn't match its directory", file, d.Name)
		}
		defs = append(defs, d)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs, nil
}

func parseDefinition(data []byte) (Definition, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var d Definition
	if err := dec.Decode(&d); err != nil {
		return Definition{}, err
	}
	if d.Features == nil {
		d.Features = []string{}
	}
	return d, d.Validate()
}

// Find returns the definition named name.
func Find(defs []Definition, name string) (Definition, bool) {
	for _, d := range defs {
		if d.Name == name {
			return d, true
		}
	}
	return Definition{}, false
}
