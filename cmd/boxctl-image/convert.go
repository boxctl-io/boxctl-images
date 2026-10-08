package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Converting a container image into a box image: `boxctl-image convert
// <ref>` takes any image Docker can pull -- Docker Hub, GHCR, ECR,
// Artifactory, a private registry `docker login` already knows -- and
// makes the ext4 a box boots from.
//
// It's a Dockerfile build like any other (build.go, and
// docs/plans/dockerfile-rootfs.md), of a Dockerfile written here:
//
//	FROM <ref>
//	RUN sh /boxctl/install.sh          systemd, sshd, chrony
//	COPY boxctl-container.service ...  what the container ran
//
// A container image is a filesystem plus a config -- ENTRYPOINT, CMD,
// ENV, USER, WORKDIR -- that says what to run in it. A box boots systemd
// and never reads that config, so a converted image would come up with
// the container's files and nothing running. convert reads the config
// from the registry and writes it as a systemd unit,
// boxctl-container.service, so the box runs what the container did.
//
// Nothing here is shared with build() beyond the functions it already
// had: cmd/boxctl-image/build.go is a build-fc-images.yml trigger that
// rebuilds and republishes every Dockerfile image, so a change there is
// never just a refactor.

const (
	// containerUnit runs the container's ENTRYPOINT and CMD in the box.
	containerUnit = "boxctl-container.service"
	// containerEnvFile gives login shells the container's ENV.
	containerEnvFile = "boxctl-container-env.sh"
	// commonContext is images/common as a named build context. Not
	// "common", as in images/*/Dockerfile: a named context shadows a
	// registry image of the same name, and here FROM is someone else's.
	commonContext = "boxctl-common"
	// defaultConvertSizeMiB is debian-slim's size_mib.
	defaultConvertSizeMiB = 4096
)

var (
	// imageRef is what convert accepts as a reference. Narrower than
	// Docker's own grammar only in what it leaves to Docker to reject; the
	// point is that nothing reaching the generated FROM line can end it.
	imageRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`)
	envName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// containerConfig is the part of an OCI image config a box needs
// translated: what the container runtime would have run, and how.
type containerConfig struct {
	User         string              `json:"User"`
	Env          []string            `json:"Env"`
	Entrypoint   []string            `json:"Entrypoint"`
	Cmd          []string            `json:"Cmd"`
	WorkingDir   string              `json:"WorkingDir"`
	StopSignal   string              `json:"StopSignal"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts"`
}

// parseImageConfig reads `docker buildx imagetools inspect --format
// '{{json .Image}}'`: one image config, or for a multi-platform image a
// map of them by platform, from which it takes buildPlatform's.
func parseImageConfig(data []byte) (containerConfig, error) {
	type image struct {
		Architecture string           `json:"architecture"`
		Config       *containerConfig `json:"config"`
	}
	var single image
	if err := json.Unmarshal(data, &single); err != nil {
		return containerConfig{}, fmt.Errorf("reading the image config: %w", err)
	}
	if single.Architecture != "" {
		if got := "linux/" + single.Architecture; got != buildPlatform {
			return containerConfig{}, fmt.Errorf("the image is %s only; boxes run %s", got, buildPlatform)
		}
		if single.Config == nil {
			return containerConfig{}, nil
		}
		return *single.Config, nil
	}
	var byPlatform map[string]image
	if err := json.Unmarshal(data, &byPlatform); err != nil {
		return containerConfig{}, fmt.Errorf("reading the image config: %w", err)
	}
	img, ok := byPlatform[buildPlatform]
	if !ok {
		var have []string
		for p := range byPlatform {
			have = append(have, p)
		}
		sort.Strings(have)
		return containerConfig{}, fmt.Errorf("the image has no %s build (it has %s)", buildPlatform, strings.Join(have, ", "))
	}
	if img.Config == nil {
		return containerConfig{}, nil
	}
	return *img.Config, nil
}

// command is what the container runs: ENTRYPOINT then CMD. Empty when
// there's nothing worth a unit -- no command at all, or only a shell,
// which is a base image's "run something in me" and would exit at once
// with no terminal.
func (c containerConfig) command() []string {
	argv := append(append([]string{}, c.Entrypoint...), c.Cmd...)
	if len(argv) == 0 {
		return nil
	}
	if len(argv) == 1 {
		switch filepath.Base(argv[0]) {
		case "sh", "bash", "ash", "dash", "zsh":
			return nil
		// An image made to boot systemd: the box does that itself.
		case "init", "systemd":
			return nil
		}
	}
	return argv
}

// containerLauncher starts the container's command ("$@") the way a
// container runtime would, in the two respects systemd differs:
//
//   - A bare command name is found on the image's PATH (bash's exec uses
//     the Environment= above; systemd looks only in its compiled-in list).
//   - stdout and stderr are a pipe. A unit's are journal sockets, which
//     can't be reopened by path, and images log by doing exactly that:
//     nginx's links /var/log/nginx/error.log to /dev/stderr, and under a
//     plain unit dies opening it (ENXIO). The pipe's other end is
//     systemd-cat, so the output still lands in the unit's journal.
//
// exec, so the unit's main process -- what gets the stop signal, and
// whose exit status the unit reports -- is the command, not bash.
const containerLauncher = `exec "$@" > >(exec systemd-cat -t boxctl-container) 2>&1`

// systemdQuote quotes s as one word of a unit file setting: C-style
// escapes inside double quotes, and %% so a literal percent isn't read
// as a specifier. execLine also escapes $, which ExecStart= expands and
// Environment= doesn't.
func systemdQuote(s string, execLine bool) string {
	r := []string{`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`, "\r", `\r`, "%", "%%"}
	if execLine {
		r = append(r, "$", "$$")
	}
	return `"` + strings.NewReplacer(r...).Replace(s) + `"`
}

// containerService is the unit that runs ref's container command in a
// box, or "" when the image has none (see command).
func containerService(ref string, c containerConfig) string {
	argv := c.command()
	if argv == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Written by `boxctl-image convert %s`: the image's ENTRYPOINT, CMD, ENV,\n", ref)
	b.WriteString("# USER and WORKDIR, which a box -- booting systemd, not a container\n")
	b.WriteString("# runtime -- wouldn't otherwise act on. Logs: journalctl -u boxctl-container\n")
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=Container command of %s\n", strings.ReplaceAll(ref, "%", "%%"))
	b.WriteString("After=network.target\n\n[Service]\n")

	user, group, _ := strings.Cut(c.User, ":")
	root := user == "" || user == "root" || user == "0"
	if !root {
		fmt.Fprintf(&b, "User=%s\n", user)
	}
	if group != "" {
		fmt.Fprintf(&b, "Group=%s\n", group)
	}
	if c.WorkingDir != "" {
		fmt.Fprintf(&b, "WorkingDirectory=%s\n", strings.ReplaceAll(c.WorkingDir, "%", "%%"))
	}
	hasHome := false
	for _, kv := range c.Env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || !envName.MatchString(name) {
			continue
		}
		hasHome = hasHome || name == "HOME"
		fmt.Fprintf(&b, "Environment=%s\n", systemdQuote(kv, false))
	}
	// A container runtime sets HOME from the passwd entry. systemd does
	// the same once User= is set, but not for a root system service.
	if root && !hasHome {
		b.WriteString("Environment=HOME=/root\n")
	}

	words := []string{"/bin/bash", "-c", systemdQuote(containerLauncher, true), "boxctl-container"}
	for _, a := range argv {
		words = append(words, systemdQuote(a, true))
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(words, " "))
	// Stop it the way `docker stop` does: signal the command alone, and
	// leave the logger to drain what it prints on its way out.
	b.WriteString("KillMode=mixed\n")
	if c.StopSignal != "" {
		fmt.Fprintf(&b, "KillSignal=%s\n", c.StopSignal)
	}
	b.WriteString("Restart=on-failure\n\n[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}

// containerEnv is the image's ENV as a /etc/profile.d script, so someone
// who SSHes in finds the same PATH and variables `docker exec` would have
// given them. "" when the image sets none.
func containerEnv(ref string, c containerConfig) string {
	var b strings.Builder
	for _, kv := range c.Env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !envName.MatchString(name) {
			continue
		}
		fmt.Fprintf(&b, "export %s='%s'\n", name, strings.ReplaceAll(value, `'`, `'\''`))
	}
	if b.Len() == 0 {
		return ""
	}
	return fmt.Sprintf("# Written by `boxctl-image convert %s`: the image's ENV.\n", ref) + b.String()
}

// convertDockerfile is the Dockerfile that turns ref into a box rootfs.
// service and env say which of the two generated files the context has.
func convertDockerfile(ref string, service, env bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", ref)
	// The image's own USER and SHELL would otherwise apply to the steps
	// below, which need root and a POSIX shell.
	b.WriteString("USER root\n")
	b.WriteString("SHELL [\"/bin/sh\", \"-c\"]\n")
	// install.sh is apt-only; say so instead of failing on its first line.
	fmt.Fprintf(&b, "RUN command -v apt-get >/dev/null || { echo 'boxctl-image convert: %s has no apt-get -- only Debian- and Ubuntu-based images can be converted' >&2; exit 1; }\n", ref)
	if service {
		// Packages enable their units as they're installed, systemd or no
		// systemd, and in a container none of them ever ran: the image's
		// command was the only thing started. In a box they all would --
		// nginx's own nginx.service next to the image's `nginx -g ...`,
		// both after port 80. So what the image came with enabled is
		// dropped, before install.sh enables what a box needs. Not with
		// -no-service: then those units are all the image has to run.
		b.WriteString("RUN find /etc/systemd/system -path '*.wants/*' -delete 2>/dev/null || true\n")
	}
	fmt.Fprintf(&b, "RUN --mount=from=%s,target=/boxctl sh /boxctl/install.sh\n", commonContext)
	if env {
		fmt.Fprintf(&b, "COPY %s /etc/profile.d/%s\n", containerEnvFile, containerEnvFile)
	}
	if service {
		fmt.Fprintf(&b, "COPY %s /etc/systemd/system/%s\n", containerUnit, containerUnit)
		fmt.Fprintf(&b, "RUN systemctl enable %s\n", containerUnit)
	}
	return b.String()
}

var notNameChar = regexp.MustCompile(`[^a-z0-9.-]+`)

// refName is a name for the image converted from ref, when none is
// given: its repository's last component ("ghcr.io/acme/api:1.2" ->
// "api").
func refName(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	ref, _, _ = strings.Cut(ref, ":")
	name := strings.Trim(notNameChar.ReplaceAllString(strings.ToLower(ref), "-"), "-.")
	if name == "" {
		return "container"
	}
	return name
}

// convertRequest is one `boxctl-image convert`.
type convertRequest struct {
	ref       string
	name      string // hostname placeholder and scratch names; refName(ref) if empty
	sizeMiB   int
	imagesDir string // for images/common
	out       string
	noService bool
	// fromTar and tarOnly split the run in two, as for build: the Docker
	// half anywhere Docker runs, the ext4 half as root on Linux.
	fromTar string
	tarOnly string
}

type converter struct {
	// run runs a command with its output passed through.
	run func(name string, args ...string) error
	// output runs a command and returns what it printed.
	output func(name string, args ...string) ([]byte, error)
}

func newConverter() *converter {
	return &converter{
		run: newBuilder().run,
		output: func(name string, args ...string) ([]byte, error) {
			cmd := exec.Command(name, args...)
			cmd.Stderr = os.Stderr
			out, err := cmd.Output()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			return out, nil
		},
	}
}

func (c *converter) convert(req convertRequest) error {
	if req.fromTar == "" {
		if !imageRef.MatchString(req.ref) {
			return fmt.Errorf("%q isn't an image reference", req.ref)
		}
		if req.ref == commonContext {
			return fmt.Errorf("%q is this build's own context name; give the image's full reference", req.ref)
		}
	}
	if req.name == "" {
		req.name = refName(req.ref)
	}
	if !contextName.MatchString(req.name) {
		return fmt.Errorf("%q isn't a usable image name (lowercase letters, digits, '.', '_', '-')", req.name)
	}
	if req.tarOnly == "" {
		if min := smallestDiskMiB(); req.sizeMiB <= 0 || req.sizeMiB > min {
			return fmt.Errorf("size %d MiB: must be between 1 and the smallest box disk (%d MiB)", req.sizeMiB, min)
		}
	}

	// Scratch space as in build: next to the output, except for
	// -tar-only, which needs room for a small context only.
	workParent := filepath.Dir(req.out)
	if req.tarOnly != "" {
		workParent = ""
	} else if err := os.MkdirAll(workParent, 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp(workParent, "."+req.name+".convert-")
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
		if err := c.export(req, work, tarPath); err != nil {
			return err
		}
		if req.tarOnly != "" {
			log.Printf("filesystem of %s written to %s", req.ref, tarPath)
			return nil
		}
	}

	root := filepath.Join(work, "rootfs")
	if err := os.Mkdir(root, 0o755); err != nil {
		return err
	}
	log.Printf("unpacking %s", tarPath)
	if err := c.run("tar", "-xpf", tarPath, "--numeric-owner", "-C", root); err != nil {
		return err
	}
	if err := fixupRootfs(root, req.name); err != nil {
		return err
	}
	if err := checkRootfs(root); err != nil {
		return fmt.Errorf("%s doesn't look bootable: %w", req.name, err)
	}

	img := filepath.Join(work, req.name+".ext4")
	log.Printf("making a %d MiB ext4 from it", req.sizeMiB)
	f, err := os.Create(img)
	if err != nil {
		return err
	}
	if err := f.Truncate(int64(req.sizeMiB) << 20); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := c.run("mkfs.ext4", "-q", "-F", "-d", root, img); err != nil {
		return fmt.Errorf("%w (does everything fit in %d MiB? try a bigger -size-mib)", err, req.sizeMiB)
	}
	if err := c.run("e2fsck", "-fn", img); err != nil {
		return err
	}
	return install(img, req.out)
}

// export runs the Docker half: read ref's config, write the Dockerfile
// and what it copies into a context under work, and build that to a
// filesystem tar at tarPath.
func (c *converter) export(req convertRequest, work, tarPath string) error {
	common, err := filepath.Abs(filepath.Join(req.imagesDir, "common"))
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(common, "install.sh")); err != nil {
		return fmt.Errorf("no install.sh in %s (run from the repository, or pass -images): %w", common, err)
	}

	raw, err := c.output("docker", "buildx", "imagetools", "inspect", req.ref, "--format", "{{json .Image}}")
	if err != nil {
		return fmt.Errorf("reading %s from its registry: %w", req.ref, err)
	}
	cfg, err := parseImageConfig(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", req.ref, err)
	}

	ctxDir := filepath.Join(work, "context")
	if err := os.Mkdir(ctxDir, 0o755); err != nil {
		return err
	}
	env := containerEnv(req.ref, cfg)
	service := ""
	if !req.noService {
		service = containerService(req.ref, cfg)
	}
	files := map[string]string{
		dockerfileName: convertDockerfile(req.ref, service != "", env != ""),
	}
	if env != "" {
		files[containerEnvFile] = env
	}
	if service != "" {
		files[containerUnit] = service
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(ctxDir, name), []byte(content), 0o644); err != nil {
			return err
		}
	}

	switch argv := cfg.command(); {
	case req.noService:
		log.Printf("not installing a unit for the container's command (-no-service)")
	case service == "":
		log.Printf("%s has no command to run (no ENTRYPOINT or CMD beyond a shell); the box will just boot", req.ref)
	default:
		log.Printf("the box will run %q as %s", argv, containerUnit)
	}
	if len(cfg.ExposedPorts) > 0 {
		ports := make([]string, 0, len(cfg.ExposedPorts))
		for p := range cfg.ExposedPorts {
			ports = append(ports, p)
		}
		sort.Strings(ports)
		log.Printf("the image exposes %s; a box publishes nothing by itself", strings.Join(ports, ", "))
	}

	const target = "convert"
	bake, err := json.MarshalIndent(map[string]any{"target": map[string]bakeTarget{target: {
		Context:    ctxDir,
		Dockerfile: dockerfileName,
		Platforms:  []string{buildPlatform},
		Contexts:   map[string]string{commonContext: common},
	}}}, "", "  ")
	if err != nil {
		return err
	}
	bakePath := filepath.Join(work, "bake.json")
	if err := os.WriteFile(bakePath, bake, 0o644); err != nil {
		return err
	}
	log.Printf("building a box rootfs from %s", req.ref)
	return c.run("docker", "buildx", "bake", "-f", bakePath, target,
		"--set", target+".output=type=tar,dest="+tarPath)
}

func runConvert(args []string) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("an image reference is required\n%s", usage)
	}
	ref := args[0]
	fs := flag.NewFlagSet("convert", flag.ExitOnError)
	imagesDir := fs.String("images", "images", "directory of image definitions (for images/common)")
	name := fs.String("name", "", "the image's name (default: from the reference)")
	out := fs.String("out", "", "where the image lands (default: ./<name>.ext4)")
	sizeMiB := fs.Int("size-mib", defaultConvertSizeMiB, "size of the ext4")
	noService := fs.Bool("no-service", false, "don't run the container's ENTRYPOINT/CMD as a systemd unit")
	fromTar := fs.String("from-tar", "", "skip the Docker build: make the image from this filesystem tar")
	tarOnly := fs.String("tar-only", "", "stop after the Docker build, leaving the filesystem tar here")
	_ = fs.Parse(args[1:])

	if *fromTar != "" && *tarOnly != "" {
		return errors.New("-from-tar and -tar-only are the two halves of a run; pass one")
	}
	if *name == "" {
		*name = refName(ref)
	}
	if *out == "" {
		*out = *name + ".ext4"
	}
	return newConverter().convert(convertRequest{
		ref: ref, name: *name, sizeMiB: *sizeMiB, imagesDir: *imagesDir, out: *out,
		noService: *noService, fromTar: *fromTar, tarOnly: *tarOnly,
	})
}
