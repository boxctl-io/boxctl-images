package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"boxctl-vms/internal/imagecatalog"
)

// Building an image: from images/<name>/Dockerfile when it has one (see
// docs/plans/dockerfile-rootfs.md), and otherwise by running its
// scripts/images/bake-<name>-image.sh as before. Either way the result
// is the image at its host path plus the <path>.sha256sum sidecar
// publish reads -- the seam the catalog depends on, so images move from
// one build to the other one at a time.
//
// A Dockerfile build:
//
//  1. docker buildx bake, exporting the image's filesystem as a tar --
//     every layer merged, nothing loaded into the daemon.
//  2. Unpack it as root, ownership intact.
//  3. Fix up what Docker manages during a build rather than the image
//     declaring it (fixupRootfs).
//  4. Check the result boots as a box (checkRootfs).
//  5. mkfs.ext4 -d: a filesystem populated from the directory as it's
//     created -- no loop mount, no chroot.
//  6. Hash it, move it into place, write the sidecar.

// dockerfileName is an image's recipe in its directory.
const dockerfileName = "Dockerfile"

// buildPlatform is what boxes run on.
const buildPlatform = "linux/amd64"

// hasDockerfile reports whether name is built from a Dockerfile.
func hasDockerfile(imagesDir, name string) bool {
	_, err := os.Stat(filepath.Join(imagesDir, name, dockerfileName))
	return err == nil
}

var (
	fromLine    = regexp.MustCompile(`(?im)^\s*FROM\s+(?:--\S+\s+)*(\S+)(?:\s+AS\s+(\S+))?`)
	fromFlag    = regexp.MustCompile(`--from=(\S+)`)
	mountFrom   = regexp.MustCompile(`--mount=\S*?\bfrom=([^,\s]+)`)
	contextName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
)

// localRefs returns the names a Dockerfile builds from that aren't
// registry images or its own stages: `FROM x`, `COPY --from=x` and
// `RUN --mount=from=x` where x is a plain name. Which of them are directories under images/ is up to
// the caller.
func localRefs(dockerfile []byte) []string {
	stages := map[string]bool{}
	for _, m := range fromLine.FindAllSubmatch(dockerfile, -1) {
		if len(m[2]) > 0 {
			stages[strings.ToLower(string(m[2]))] = true
		}
	}
	seen := map[string]bool{}
	var refs []string
	add := func(r string) {
		if contextName.MatchString(r) && !stages[r] && !seen[r] {
			seen[r] = true
			refs = append(refs, r)
		}
	}
	for _, m := range fromLine.FindAllSubmatch(dockerfile, -1) {
		add(string(m[1]))
	}
	for _, re := range []*regexp.Regexp{fromFlag, mountFrom} {
		for _, m := range re.FindAllSubmatch(dockerfile, -1) {
			add(string(m[1]))
		}
	}
	return refs
}

// bakeTarget is one target of a generated bake file.
type bakeTarget struct {
	Context    string            `json:"context"`
	Dockerfile string            `json:"dockerfile"`
	Platforms  []string          `json:"platforms"`
	Contexts   map[string]string `json:"contexts,omitempty"`
}

// imageDeps returns every directory under imagesDir that name builds
// from, transitively, mapped to whether it's itself a Dockerfile image
// (built as a target) or a plain directory (a named build context, like
// images/common).
func imageDeps(imagesDir, name string) (map[string]bool, error) {
	deps := map[string]bool{}
	var walk func(n string, stack []string) error
	walk = func(n string, stack []string) error {
		for _, s := range stack {
			if s == n {
				return fmt.Errorf("%s builds from itself: %s", n, strings.Join(append(stack, n), " -> "))
			}
		}
		data, err := os.ReadFile(filepath.Join(imagesDir, n, dockerfileName))
		if err != nil {
			return err
		}
		for _, ref := range localRefs(data) {
			st, err := os.Stat(filepath.Join(imagesDir, ref))
			if err != nil || !st.IsDir() {
				continue // a registry image
			}
			isImage := hasDockerfile(imagesDir, ref)
			deps[ref] = isImage
			if isImage {
				if err := walk(ref, append(stack, n)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return deps, walk(name, nil)
}

// bakeTargetName is name as a bake target, which may not contain the
// dots image names can ("ubuntu-26.04" -> "ubuntu-26_04").
func bakeTargetName(name string) string {
	return strings.ReplaceAll(name, ".", "_")
}

// bakeFile is a `docker buildx bake` definition building name, with each
// image it builds FROM as a target of its own and each plain directory it
// mounts as a named context. Generated, so there's no hand-kept build
// graph to drift from the Dockerfiles.
func bakeFile(imagesDir, name string) ([]byte, error) {
	abs, err := filepath.Abs(imagesDir)
	if err != nil {
		return nil, err
	}
	targets := map[string]bakeTarget{}
	images := map[string]string{} // target name -> image name, to catch two names mapping to one
	var add func(n string) error
	add = func(n string) error {
		tn := bakeTargetName(n)
		if other, done := images[tn]; done {
			if other != n {
				return fmt.Errorf("%s and %s are the same bake target name", other, n)
			}
			return nil
		}
		images[tn] = n
		deps, err := imageDeps(imagesDir, n)
		if err != nil {
			return err
		}
		t := bakeTarget{Context: filepath.Join(abs, n), Dockerfile: dockerfileName, Platforms: []string{buildPlatform}}
		data, _ := os.ReadFile(filepath.Join(imagesDir, n, dockerfileName))
		for _, ref := range localRefs(data) {
			isImage, ok := deps[ref]
			if !ok {
				continue
			}
			if t.Contexts == nil {
				t.Contexts = map[string]string{}
			}
			if isImage {
				t.Contexts[ref] = "target:" + bakeTargetName(ref)
				if err := add(ref); err != nil {
					return err
				}
			} else {
				t.Contexts[ref] = filepath.Join(abs, ref)
			}
		}
		targets[tn] = t
		return nil
	}
	if err := add(name); err != nil {
		return nil, err
	}
	return json.MarshalIndent(map[string]any{"target": targets}, "", "  ")
}

// buildRequest is one `boxctl-image build`.
type buildRequest struct {
	def        imagecatalog.Definition
	imagesDir  string
	scriptsDir string
	out        string // where the image lands; its sidecar goes beside it
	// fromTar skips the Docker build and starts from this filesystem tar.
	fromTar string
	// tarOnly stops after the Docker build, leaving the filesystem tar here.
	tarOnly string
}

type builder struct {
	// run runs a command with its output passed through.
	run func(name string, args ...string) error
}

func newBuilder() *builder {
	return &builder{run: func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		// buildx (0.19+) refuses a bake file that reads or writes outside
		// its own directory without an --allow=fs.* grant. Ours is
		// generated here, with exactly the paths this build uses, so the
		// check guards nothing; turning it off by env rather than flag
		// keeps older buildx, which doesn't know the flag, working.
		cmd.Env = append(os.Environ(), "BUILDX_BAKE_ENTITLEMENTS_FS=0")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}}
}

func (b *builder) build(req buildRequest) error {
	d := req.def
	if !hasDockerfile(req.imagesDir, d.Name) {
		if req.fromTar != "" || req.tarOnly != "" {
			return fmt.Errorf("%s is built by its bake script, not a Dockerfile", d.Name)
		}
		return b.run("bash", filepath.Join(req.scriptsDir, bakeScript(d)))
	}
	if d.Kind != imagecatalog.KindRootfs || d.SizeMiB <= 0 {
		return fmt.Errorf("%s: a Dockerfile image must be a rootfs with size_mib set", d.Name)
	}

	// Scratch space next to the output, so the final move is a rename on
	// one filesystem and the unpacked tree never lands in a small /tmp --
	// except for -tar-only, which only needs room for the bake file and
	// runs as a user who can't write to the image directory (CI runs the
	// Docker build unprivileged and only the rest under sudo).
	workParent := filepath.Dir(req.out)
	if req.tarOnly != "" {
		workParent = ""
	} else if err := os.MkdirAll(workParent, 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp(workParent, "."+d.Name+".build-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()

	tarPath := req.fromTar
	if tarPath == "" {
		tarPath = filepath.Join(work, "rootfs.tar")
		if req.tarOnly != "" {
			tarPath = req.tarOnly
		}
		bake, err := bakeFile(req.imagesDir, d.Name)
		if err != nil {
			return err
		}
		bakePath := filepath.Join(work, "bake.json")
		if err := os.WriteFile(bakePath, bake, 0o644); err != nil {
			return err
		}
		log.Printf("building %s from %s", d.Name, filepath.Join(req.imagesDir, d.Name, dockerfileName))
		target := bakeTargetName(d.Name)
		if err := b.run("docker", "buildx", "bake", "-f", bakePath, target,
			"--set", target+".output=type=tar,dest="+tarPath); err != nil {
			return err
		}
		if req.tarOnly != "" {
			log.Printf("filesystem of %s written to %s", d.Name, tarPath)
			return nil
		}
	}

	root := filepath.Join(work, "rootfs")
	if err := os.Mkdir(root, 0o755); err != nil {
		return err
	}
	log.Printf("unpacking %s", tarPath)
	if err := b.run("tar", "-xpf", tarPath, "--numeric-owner", "-C", root); err != nil {
		return err
	}
	if err := fixupRootfs(root, d.Name); err != nil {
		return err
	}
	if err := checkRootfs(root); err != nil {
		return fmt.Errorf("%s doesn't look bootable: %w", d.Name, err)
	}

	img := filepath.Join(work, d.Name+".ext4")
	log.Printf("making a %d MiB ext4 from it", d.SizeMiB)
	f, err := os.Create(img)
	if err != nil {
		return err
	}
	if err := f.Truncate(int64(d.SizeMiB) << 20); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := b.run("mkfs.ext4", "-q", "-F", "-d", root, img); err != nil {
		return fmt.Errorf("%w (does everything fit in %d MiB?)", err, d.SizeMiB)
	}
	if err := b.run("e2fsck", "-fn", img); err != nil {
		return err
	}
	return install(img, req.out)
}

// install moves img to out and writes out's sidecar -- "<sha256> <size>
// <mtime-nanos>", what bake-*-image.sh write and internal/rootfsdiff.
// Identity caches -- after it lands: a sidecar must never describe a file
// other than the one at its path.
func install(img, out string) error {
	f, err := os.Open(img)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(h, bufio.NewReaderSize(f, 1<<20))
	_ = f.Close()
	if err != nil {
		return err
	}
	sha := hex.EncodeToString(h.Sum(nil))
	if err := os.Rename(img, out); err != nil {
		return err
	}
	st, err := os.Stat(out)
	if err != nil {
		return err
	}
	sidecar := fmt.Sprintf("%s %d %d\n", sha, st.Size(), st.ModTime().UnixNano())
	if err := os.WriteFile(out+".sha256sum", []byte(sidecar), 0o644); err != nil {
		return err
	}
	log.Printf("installed %s (sha256 %s)", out, sha)
	return nil
}

// fixupRootfs corrects what a Docker build leaves behind that a VM must
// not boot with. Each of these comes from Docker managing a file during
// the build rather than the image declaring it, so no Dockerfile can fix
// it, and every image needs it.
func fixupRootfs(root, name string) error {
	hostname := "boxctl-" + name
	files := map[string]string{
		// Placeholders: onctl writes each box's own hostname into both
		// (injectHostname), and needs them to be regular files to do it.
		"etc/hostname": hostname + "\n",
		"etc/hosts": "127.0.0.1\tlocalhost\n" +
			"::1\tlocalhost ip6-localhost ip6-loopback\n" +
			"ff02::1\tip6-allnodes\n" +
			"ff02::2\tip6-allrouters\n" +
			"127.0.1.1\t" + hostname + "\n",
		// Static, as the bake scripts did: nothing in a box runs a
		// resolver that would write one.
		"etc/resolv.conf": "nameserver 8.8.8.8\nnameserver 1.1.1.1\n",
		// Empty, so systemd gives each box a machine-id of its own on
		// first boot instead of every box sharing the image's.
		"etc/machine-id": "",
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}

	remove := []string{
		// Tells software it's in a container.
		".dockerenv",
		// Some base images forbid starting services from package scripts;
		// in a box, apt upgrades must restart what they update.
		"usr/sbin/policy-rc.d",
	}
	// Docker images' apt tweaks (no caches, no translations) are for
	// keeping layers small, not for a machine people use.
	tweaks, _ := filepath.Glob(filepath.Join(root, "etc/apt/apt.conf.d/docker-*"))
	for _, t := range tweaks {
		rel, _ := filepath.Rel(root, t)
		remove = append(remove, rel)
	}
	for _, rel := range remove {
		if err := os.Remove(filepath.Join(root, rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}

	// The marker build-fc-images.yml's smoke test looks for.
	if err := os.MkdirAll(filepath.Join(root, "opt"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "opt/.baked"), nil, 0o644)
}

// checkRootfs fails a tree that wouldn't boot as a box, or would boot
// insecurely -- the contract images/common/install.sh meets, checked
// rather than assumed, since a Dockerfile can undo any of it.
func checkRootfs(root string) error {
	var problems []string

	if init, err := resolveIn(root, "/sbin/init"); err != nil {
		problems = append(problems, "no /sbin/init: "+err.Error())
	} else if filepath.Base(init) != "systemd" {
		problems = append(problems, "/sbin/init is "+init+", not systemd")
	}

	enabled := false
	for _, unit := range []string{
		"etc/systemd/system/multi-user.target.wants/ssh.service",
		"etc/systemd/system/sockets.target.wants/ssh.socket",
	} {
		if _, err := os.Lstat(filepath.Join(root, unit)); err == nil {
			enabled = true
		}
	}
	if !enabled {
		problems = append(problems, "sshd isn't enabled")
	}

	if keys, _ := filepath.Glob(filepath.Join(root, "etc/ssh/ssh_host_*")); len(keys) > 0 {
		problems = append(problems, fmt.Sprintf("%d SSH host keys are baked in; every box would share them", len(keys)))
	}

	for _, rel := range []string{"etc/hostname", "etc/hosts"} {
		st, err := os.Lstat(filepath.Join(root, rel))
		if err != nil || !st.Mode().IsRegular() {
			problems = append(problems, "/"+rel+" isn't a regular file (onctl rewrites it with debugfs, which won't follow a link)")
		}
	}

	if st, err := os.Stat(filepath.Join(root, "root")); err != nil || !st.IsDir() {
		problems = append(problems, "no /root, where onctl puts the box's authorized_keys")
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// resolveIn follows p's symlinks the way the box will see them: absolute
// targets relative to root, not to this machine.
func resolveIn(root, p string) (string, error) {
	cur := filepath.Clean("/" + p)
	for i := 0; i < 40; i++ {
		// Resolve each component, so a link anywhere in the path (/sbin ->
		// usr/sbin on a merged-/usr system) is followed too.
		parts := strings.Split(strings.TrimPrefix(cur, "/"), "/")
		resolved := "/"
		changed := false
		for j, part := range parts {
			next := filepath.Join(resolved, part)
			st, err := os.Lstat(filepath.Join(root, next))
			if err != nil {
				return "", err
			}
			if st.Mode()&fs.ModeSymlink != 0 {
				target, err := os.Readlink(filepath.Join(root, next))
				if err != nil {
					return "", err
				}
				if !filepath.IsAbs(target) {
					target = filepath.Join(resolved, target)
				}
				rest := strings.Join(parts[j+1:], "/")
				cur = filepath.Clean(filepath.Join(target, rest))
				changed = true
				break
			}
			resolved = next
		}
		if !changed {
			return resolved, nil
		}
	}
	return "", errors.New("too many levels of symbolic links")
}
