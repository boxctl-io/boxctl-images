package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLocalRefs(t *testing.T) {
	df := []byte(`# syntax=docker/dockerfile:1
FROM debian:trixie-slim@sha256:abc AS deps
FROM --platform=linux/amd64 desktop
RUN --mount=from=common,target=/boxctl sh /boxctl/install.sh
RUN --mount=type=bind,from=extras,source=/x,target=/y true
COPY --from=deps /usr/bin/x /usr/bin/x
COPY --from=tools /bin/t /bin/t
from ubuntu:26.04
`)
	got := localRefs(df)
	want := []string{"desktop", "tools", "common", "extras"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v (registry images and the file's own stages excluded)", got, want)
	}
}

// writeTree creates files (path -> content) under a temp dir.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, c := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestBakeFile(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"common/install.sh":  "",
		"desktop/Dockerfile": "FROM ubuntu:24.04\nRUN --mount=from=common,target=/boxctl sh /boxctl/install.sh\n",
		"vscode/Dockerfile":  "FROM desktop\nRUN true\n",
	})
	data, err := bakeFile(dir, "vscode")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Target map[string]bakeTarget `json:"target"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Target) != 2 {
		t.Fatalf("targets %v, want vscode and desktop", got.Target)
	}
	if c := got.Target["vscode"].Contexts; c["desktop"] != "target:desktop" {
		t.Errorf("vscode contexts %v: FROM desktop must build the desktop target", c)
	}
	if c := got.Target["desktop"].Contexts; c["common"] != filepath.Join(dir, "common") {
		t.Errorf("desktop contexts %v: common must be its directory", c)
	}
	for name, tg := range got.Target {
		if !filepath.IsAbs(tg.Context) || tg.Platforms[0] != buildPlatform {
			t.Errorf("%s: %+v", name, tg)
		}
	}

	deps, err := imageDeps(dir, "vscode")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(deps, map[string]bool{"desktop": true, "common": false}) {
		t.Errorf("deps %v", deps)
	}
}

func TestBakeFileTargetNames(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"ubuntu-26.04/Dockerfile": "FROM ubuntu:26.04\n",
		"dev/Dockerfile":          "FROM ubuntu-26.04\n",
	})
	data, err := bakeFile(dir, "dev")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Target map[string]bakeTarget `json:"target"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Target["ubuntu-26_04"]; !ok {
		t.Fatalf("targets %v: a dotted image name must become a valid target name", got.Target)
	}
	if c := got.Target["dev"].Contexts["ubuntu-26.04"]; c != "target:ubuntu-26_04" {
		t.Fatalf("dev's FROM ubuntu-26.04 context is %q", c)
	}

	clash := writeTree(t, map[string]string{
		"a.b/Dockerfile": "FROM x:1\n",
		"a_b/Dockerfile": "FROM x:1\n",
		"c/Dockerfile":   "FROM a.b\nCOPY --from=a_b / /\n",
	})
	if _, err := bakeFile(clash, "c"); err == nil {
		t.Fatal("two images with the same target name were accepted")
	}
}

func TestImageDepsCycle(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"a/Dockerfile": "FROM b\n",
		"b/Dockerfile": "FROM a\n",
	})
	if _, err := imageDeps(dir, "a"); err == nil || !strings.Contains(err.Error(), "builds from itself") {
		t.Fatalf("got %v", err)
	}
}

// exportedRootfs is roughly what a Docker build of a box image exports.
func exportedRootfs(t *testing.T) string {
	t.Helper()
	root := writeTree(t, map[string]string{
		".dockerenv":                            "",
		"etc/hosts":                             "",
		"etc/machine-id":                        "0123456789abcdef\n",
		"etc/apt/apt.conf.d/docker-clean":       "",
		"etc/apt/apt.conf.d/70debconf":          "",
		"usr/sbin/policy-rc.d":                  "exit 101\n",
		"usr/lib/systemd/systemd":               "",
		"root/.bashrc":                          "",
		"etc/systemd/system/boxctl-ssh.service": "",
	})
	link := func(target, at string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, at)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, at)); err != nil {
			t.Fatal(err)
		}
	}
	link("usr/sbin", "sbin")                          // merged /usr, relative
	link("/usr/lib/systemd/systemd", "usr/sbin/init") // absolute: inside the box, not here
	link("/lib/systemd/system/ssh.service", "etc/systemd/system/multi-user.target.wants/ssh.service")
	link("../run/systemd/resolve/stub-resolv.conf", "etc/resolv.conf")
	return root
}

func TestFixupAndCheckRootfs(t *testing.T) {
	root := exportedRootfs(t)
	if err := fixupRootfs(root, "debian-slim"); err != nil {
		t.Fatal(err)
	}
	if err := checkRootfs(root); err != nil {
		t.Fatalf("a fixed-up rootfs failed its check: %v", err)
	}

	read := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if read("etc/hostname") != "boxctl-debian-slim\n" || !strings.Contains(read("etc/hosts"), "127.0.1.1\tboxctl-debian-slim") {
		t.Error("hostname placeholders not written")
	}
	if st, _ := os.Lstat(filepath.Join(root, "etc/resolv.conf")); !st.Mode().IsRegular() || !strings.Contains(read("etc/resolv.conf"), "nameserver") {
		t.Error("resolv.conf is still the build's link")
	}
	if read("etc/machine-id") != "" {
		t.Error("machine-id not emptied")
	}
	for _, gone := range []string{".dockerenv", "usr/sbin/policy-rc.d", "etc/apt/apt.conf.d/docker-clean"} {
		if _, err := os.Lstat(filepath.Join(root, gone)); err == nil {
			t.Errorf("%s is still there", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "etc/apt/apt.conf.d/70debconf")); err != nil {
		t.Error("removed an apt config that isn't Docker's")
	}
	if _, err := os.Stat(filepath.Join(root, "opt/.baked")); err != nil {
		t.Error("no /opt/.baked marker")
	}
}

func TestCheckRootfsRejects(t *testing.T) {
	cases := map[string]func(root string) error{
		"baked host keys": func(root string) error {
			if err := os.MkdirAll(filepath.Join(root, "etc/ssh"), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(root, "etc/ssh/ssh_host_ed25519_key"), nil, 0o600)
		},
		"sshd not enabled": func(root string) error {
			return os.Remove(filepath.Join(root, "etc/systemd/system/multi-user.target.wants/ssh.service"))
		},
		"init isn't systemd": func(root string) error {
			p := filepath.Join(root, "usr/sbin/init")
			if err := os.Remove(p); err != nil {
				return err
			}
			return os.Symlink("/bin/busybox", p)
		},
		"hostname is a link": func(root string) error {
			p := filepath.Join(root, "etc/hostname")
			if err := os.Remove(p); err != nil {
				return err
			}
			return os.Symlink("/proc/sys/kernel/hostname", p)
		},
		"no /root": func(root string) error { return os.RemoveAll(filepath.Join(root, "root")) },
	}
	for name, breakIt := range cases {
		root := exportedRootfs(t)
		if err := fixupRootfs(root, "x"); err != nil {
			t.Fatal(err)
		}
		if err := breakIt(root); err != nil {
			t.Fatal(err)
		}
		if err := checkRootfs(root); err == nil {
			t.Errorf("%s: passed the check", name)
		}
	}
}

func TestResolveInStaysInsideRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "usr/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "usr/bin/real"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	// An absolute link means the box's /usr/bin/real, never this
	// machine's.
	if err := os.Symlink("/usr/bin/real", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveIn(root, "/link"); err != nil || got != "/usr/bin/real" {
		t.Fatalf("got %q, %v", got, err)
	}
	if err := os.Symlink("loop", filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveIn(root, "/loop"); err == nil {
		t.Fatal("a symlink loop resolved")
	}
}

func TestBuildRunsTheBakeScriptForImagesWithoutADockerfile(t *testing.T) {
	var ran [][]string
	b := &builder{run: func(name string, args ...string) error {
		ran = append(ran, append([]string{name}, args...))
		return nil
	}}
	err := b.build(buildRequest{
		def: find(t, "desktop"), imagesDir: "../../images", scriptsDir: "../../scripts/images", out: filepath.Join(t.TempDir(), "x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0][0] != "bash" || ran[0][1] != "../../scripts/images/bake-desktop-image.sh" {
		t.Fatalf("ran %v", ran)
	}
}

func TestBuildExportsTheDockerBuildAsATar(t *testing.T) {
	var ran [][]string
	b := &builder{run: func(name string, args ...string) error {
		ran = append(ran, append([]string{name}, args...))
		return nil
	}}
	tar := filepath.Join(t.TempDir(), "rootfs.tar")
	err := b.build(buildRequest{
		def: find(t, "debian-slim"), imagesDir: "../../images", scriptsDir: "../../scripts/images",
		out: filepath.Join(t.TempDir(), "debian-slim.ext4"), tarOnly: tar,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 {
		t.Fatalf("ran %v, want only the docker build", ran)
	}
	got := strings.Join(ran[0], " ")
	if !strings.HasPrefix(got, "docker buildx bake -f ") || !strings.HasSuffix(got, "debian-slim --set debian-slim.output=type=tar,dest="+tar) {
		t.Fatalf("ran %q", got)
	}
}

func TestBuildWritesOnlyTheBakeFile(t *testing.T) {
	var ran [][]string
	b := &builder{run: func(name string, args ...string) error {
		ran = append(ran, append([]string{name}, args...))
		return nil
	}}
	bake := filepath.Join(t.TempDir(), "bake.json")
	err := b.build(buildRequest{
		def: find(t, "claude-agent"), imagesDir: "../../images", scriptsDir: "../../scripts/images",
		out: filepath.Join(t.TempDir(), "claude-agent.ext4"), bakeOnly: bake,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 {
		t.Fatalf("ran %v, want nothing: the bake is the caller's", ran)
	}
	data, err := os.ReadFile(bake)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Target map[string]bakeTarget `json:"target"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	// The whole graph, so the caller's bake builds debian-slim and mounts
	// common the way build does.
	if c := got.Target["claude-agent"].Contexts; c["debian-slim"] != "target:debian-slim" {
		t.Errorf("claude-agent contexts %v", c)
	}
	if c := got.Target["debian-slim"].Contexts; !strings.HasSuffix(c["common"], filepath.Join("images", "common")) {
		t.Errorf("debian-slim contexts %v", c)
	}

	if err := b.build(buildRequest{def: find(t, "desktop"), imagesDir: "../../images", bakeOnly: bake}); err == nil {
		t.Fatal("an image without a Dockerfile has no bake file to write")
	}
}

func TestValidateRejects(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			"images/boxctl-kernel/image.json": `{"name":"boxctl-kernel","kind":"kernel","provider":"fc","source":"baked","status":"available"}`,
			"scripts/bake-kernel-image.sh":    "",
			"images/common/install.sh":        "",
			"images/slim/image.json":          `{"name":"slim","kind":"rootfs","provider":"fc","source":"baked","label":"S","description":"s","os":"debian","size_mib":4096,"status":"available","default":true}`,
			"images/slim/Dockerfile":          "FROM debian:trixie-slim\nRUN --mount=from=common,target=/boxctl true\n",
		}
	}
	run := func(files map[string]string) error {
		dir := writeTree(t, files)
		_, err := validate(filepath.Join(dir, "images"), filepath.Join(dir, "scripts"))
		return err
	}
	if err := run(base()); err != nil {
		t.Fatalf("valid tree rejected: %v", err)
	}

	both := base()
	both["scripts/bake-slim-image.sh"] = ""
	neither := base()
	delete(neither, "images/slim/Dockerfile")
	noSize := base()
	noSize["images/slim/image.json"] = strings.Replace(noSize["images/slim/image.json"], `"size_mib":4096,`, "", 1)
	tooBig := base()
	tooBig["images/slim/image.json"] = strings.Replace(tooBig["images/slim/image.json"], "4096", "999999", 1)
	cycle := base()
	cycle["images/slim/Dockerfile"] = "FROM slim\n"

	for name, files := range map[string]map[string]string{
		"both a Dockerfile and a bake script": both,
		"neither":                             neither,
		"no size_mib":                         noSize,
		"bigger than the smallest disk":       tooBig,
		"builds from itself":                  cycle,
	} {
		if err := run(files); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
