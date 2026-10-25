package imagecatalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func rootfs(name string) Definition {
	return Definition{
		Name: name, Kind: KindRootfs, Provider: ProviderFC, Source: SourceBaked,
		Label: name, Description: name + " image", OS: "debian",
		Features: []string{}, Status: StatusAvailable,
	}
}

func kernel() Definition {
	return Definition{Name: "boxctl-kernel", Kind: KindKernel, Provider: ProviderFC, Source: SourceBaked, Features: []string{}, Status: StatusAvailable}
}

func sha(c byte) string { return strings.Repeat(string(c), 64) }

func TestValidName(t *testing.T) {
	for _, ok := range []string{"debian-slim", "ubuntu-26.04", "a", "vscode"} {
		if !ValidName(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", ".hidden", "-x", "a/b", "../etc", "Debian", "a b", strings.Repeat("a", 41)} {
		if ValidName(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestPathsAndKeysMatchWhatHostsAndR2AlreadyHold(t *testing.T) {
	// These are the paths every host has and the keys every existing
	// object was published under (by the publish-*-image.sh scripts this
	// replaced), so
	// changing any of them strands what's already out there.
	cases := []struct {
		def                   Definition
		path, object, latestK string
	}{
		{rootfs("debian-slim"), "/opt/fc/images/debian-slim.ext4", "images/debian-slim.ext4-aaaaaaa.zst", "images/latest/debian-slim.ext4.zst"},
		{rootfs("ubuntu-26.04"), "/opt/fc/images/ubuntu-26.04.ext4", "images/ubuntu-26.04.ext4-aaaaaaa.zst", "images/latest/ubuntu-26.04.ext4.zst"},
		{kernel(), "/opt/fc/images/boxctl-kernel.vmlinux", "kernel/boxctl-kernel.vmlinux-aaaaaaa.zst", "kernel/latest/boxctl-kernel.vmlinux.zst"},
	}
	for _, c := range cases {
		if got := HostPath(c.def); got != c.path {
			t.Errorf("HostPath(%s) = %q, want %q", c.def.Name, got, c.path)
		}
		if got := ObjectKey(c.def, sha('a')); got != c.object {
			t.Errorf("ObjectKey(%s) = %q, want %q", c.def.Name, got, c.object)
		}
		if got := LatestKey(c.def); got != c.latestK {
			t.Errorf("LatestKey(%s) = %q, want %q", c.def.Name, got, c.latestK)
		}
	}
}

func TestValidateSet(t *testing.T) {
	def := rootfs("debian-slim")
	def.Default = true
	good := []Definition{def, rootfs("desktop"), kernel()}
	if err := ValidateSet(good); err != nil {
		t.Fatalf("valid set rejected: %v", err)
	}

	noDefault := []Definition{rootfs("debian-slim"), kernel()}
	if err := ValidateSet(noDefault); err == nil {
		t.Error("a set with no default was accepted")
	}
	twoDefaults := []Definition{def, def, kernel()}
	twoDefaults[1].Name = "other"
	if err := ValidateSet(twoDefaults); err == nil {
		t.Error("a set with two defaults was accepted")
	}
	noKernel := []Definition{def}
	if err := ValidateSet(noKernel); err == nil {
		t.Error("a set with no kernel was accepted")
	}
	dup := []Definition{def, rootfs("debian-slim"), kernel()}
	if err := ValidateSet(dup); err == nil {
		t.Error("a duplicate name was accepted")
	}
}

func TestDefinitionValidate(t *testing.T) {
	mutations := map[string]func(*Definition){
		"bad name":            func(d *Definition) { d.Name = "../x" },
		"unknown kind":        func(d *Definition) { d.Kind = "disk" },
		"unknown provider":    func(d *Definition) { d.Provider = "qemu" },
		"unknown status":      func(d *Definition) { d.Status = "beta" },
		"unknown feature":     func(d *Definition) { d.Features = []string{"gpu"} },
		"missing label":       func(d *Definition) { d.Label = "" },
		"hidden default":      func(d *Definition) { d.Default, d.Status = true, StatusHidden },
		"ch provider":         func(d *Definition) { d.Provider = "ch" },
		"unknown source":      func(d *Definition) { d.Source = "manual" },
		"external source":     func(d *Definition) { d.Source = "external" },
		"retired default too": func(d *Definition) { d.Default, d.Status = true, StatusRetired },
	}
	for name, mutate := range mutations {
		d := rootfs("x")
		mutate(&d)
		if err := d.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func gen(c byte, d Definition) *Generation {
	return &Generation{SHA256: sha(c), SizeBytes: 4 << 30, Key: ObjectKey(d, sha(c)), PublishedAt: time.Unix(int64(c), 0).UTC()}
}

func TestAdvance(t *testing.T) {
	d := rootfs("debian-slim")

	if _, err := Advance(nil, d, nil); err == nil {
		t.Fatal("metadata-only publish of a never-published image succeeded")
	}

	m1, err := Advance(nil, d, gen('a', d))
	if err != nil {
		t.Fatal(err)
	}
	if m1.Current.SHA256 != sha('a') || len(m1.Previous) != 0 {
		t.Fatalf("first publish: %+v", m1)
	}

	m2, err := Advance(&m1, d, gen('b', d))
	if err != nil {
		t.Fatal(err)
	}
	if m2.Current.SHA256 != sha('b') || len(m2.Previous) != 1 || m2.Previous[0].SHA256 != sha('a') {
		t.Fatalf("second publish didn't move a into history: %+v", m2)
	}

	// Re-publishing the current generation changes nothing, including
	// when it was first published.
	again := gen('b', d)
	again.PublishedAt = time.Unix(999, 0)
	m3, err := Advance(&m2, d, again)
	if err != nil {
		t.Fatal(err)
	}
	if !m3.Current.PublishedAt.Equal(m2.Current.PublishedAt) || len(m3.Previous) != 1 {
		t.Fatalf("idempotent re-publish changed generations: %+v", m3)
	}

	// Republishing an old build makes it current and drops it from history.
	m4, err := Advance(&m3, d, gen('a', d))
	if err != nil {
		t.Fatal(err)
	}
	if m4.Current.SHA256 != sha('a') || len(m4.Previous) != 1 || m4.Previous[0].SHA256 != sha('b') {
		t.Fatalf("rollback: %+v", m4)
	}

	// Metadata-only: new fields, same generations.
	d.Status = StatusDeprecated
	m5, err := Advance(&m4, d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m5.Status != StatusDeprecated || m5.Current.SHA256 != sha('a') || len(m5.Previous) != 1 {
		t.Fatalf("metadata-only: %+v", m5)
	}
}

func TestAdvanceCapsHistory(t *testing.T) {
	d := rootfs("debian-slim")
	var m *Manifest
	for i := 0; i < maxPrevious+5; i++ {
		g := &Generation{SHA256: strings.Repeat("0", 56) + strings.Repeat("0", 4) + hex4(i), SizeBytes: 1, PublishedAt: time.Unix(int64(i), 0)}
		g.Key = ObjectKey(d, g.SHA256)
		next, err := Advance(m, d, g)
		if err != nil {
			t.Fatal(err)
		}
		m = &next
	}
	if len(m.Previous) != maxPrevious {
		t.Fatalf("history has %d generations, want %d", len(m.Previous), maxPrevious)
	}
}

func hex4(i int) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[i>>12&15], digits[i>>8&15], digits[i>>4&15], digits[i&15]})
}

func TestParseManifestRejects(t *testing.T) {
	d := rootfs("debian-slim")
	good, err := Advance(nil, d, gen('a', d))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Manifest){
		"future schema":         func(m *Manifest) { m.Schema = 2 },
		"path-like name":        func(m *Manifest) { m.Name = "../../etc/passwd" },
		"bad sha":               func(m *Manifest) { m.Current.SHA256 = "nope" },
		"key not where it goes": func(m *Manifest) { m.Current.Key = "images/other.ext4-aaaaaaa.zst" },
		"without object":        func(m *Manifest) { m.Current.Key = "" },
		"no size":               func(m *Manifest) { m.Current.SizeBytes = 0 },
	}
	for name, mutate := range cases {
		m := good
		mutate(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// The Windows image's manifest, as `publish -register` once wrote it:
	// still in the bucket, but no longer something a reader accepts, so
	// it's skipped rather than offered.
	win := `{"schema":1,"name":"windows-server-2025","kind":"rootfs","provider":"ch","source":"external",` +
		`"label":"W","description":"w","os":"windows","features":[],"status":"available","default":false,` +
		`"current":{"sha256":"` + sha('c') + `","size_bytes":1,"published_at":"2026-01-01T00:00:00Z"}}`
	if _, err := ParseManifest([]byte(win)); err == nil {
		t.Error("a ch/external manifest was accepted")
	}
}

func TestLoadDefinitions(t *testing.T) {
	dir := t.TempDir()
	write := func(sub, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sub, DefinitionFile), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("debian-slim", `{"name":"debian-slim","kind":"rootfs","provider":"fc","source":"baked","label":"D","description":"d","os":"debian","status":"available","default":true}`)
	if err := os.MkdirAll(filepath.Join(dir, "no-definition"), 0o755); err != nil {
		t.Fatal(err)
	}
	defs, err := LoadDefinitions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Features == nil {
		t.Fatalf("got %+v", defs)
	}

	write("typo", `{"name":"typo","kind":"rootfs","provider":"fc","source":"baked","label":"T","description":"t","os":"debian","status":"available","defualt":true}`)
	if _, err := LoadDefinitions(dir); err == nil {
		t.Fatal("an unknown field was accepted")
	}
	write("typo", `{"name":"other","kind":"rootfs","provider":"fc","source":"baked","label":"T","description":"t","os":"debian","status":"available"}`)
	if _, err := LoadDefinitions(dir); err == nil {
		t.Fatal("a name that doesn't match its directory was accepted")
	}
}
