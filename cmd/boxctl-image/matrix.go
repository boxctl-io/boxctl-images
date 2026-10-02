package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"boxctl-vms/internal/imagecatalog"
)

// matrixEntry is one build-fc-images.yml job. Rebuild false means only
// the image's definition changed: the job republishes its manifest
// without baking anything.
type matrixEntry struct {
	Image   string `json:"image"`
	Rebuild bool   `json:"rebuild"`
}

// workflowFile rebuilds every image when it changes, as it always has: a
// change to how images are baked may change every image.
const workflowFile = ".github/workflows/build-fc-images.yml"

// built reports whether build-fc-images.yml builds d. The kernel has its
// own workflow.
func built(d imagecatalog.Definition) bool {
	return d.Kind == imagecatalog.KindRootfs
}

// planMatrix decides what a push builds from the files it changed; nil
// changed means everything (a manual run, or a push whose base couldn't
// be diffed). A retired image is never rebuilt, but a change to its
// definition is still published -- that's how it gets retired.
func planMatrix(defs []imagecatalog.Definition, changed []string) []matrixEntry {
	touched := map[string]bool{}
	for _, f := range changed {
		touched[f] = true
	}
	all := changed == nil || touched[workflowFile]

	entries := []matrixEntry{}
	for _, d := range defs {
		if !built(d) {
			continue
		}
		retired := d.Status == imagecatalog.StatusRetired
		rebuild := !retired && (all || touched["scripts/images/"+bakeScript(d)])
		metadata := touched["images/"+d.Name+"/"+imagecatalog.DefinitionFile]
		if rebuild || metadata {
			entries = append(entries, matrixEntry{Image: d.Name, Rebuild: rebuild})
		}
	}
	return entries
}

// matrixFor rebuilds exactly the named images (a manual run's input).
func matrixFor(defs []imagecatalog.Definition, names []string) ([]matrixEntry, error) {
	entries := []matrixEntry{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		d, ok := imagecatalog.Find(defs, n)
		if !ok || !built(d) {
			return nil, fmt.Errorf("%q isn't an image build-fc-images.yml builds", n)
		}
		if d.Status == imagecatalog.StatusRetired {
			return nil, fmt.Errorf("%q is retired", n)
		}
		entries = append(entries, matrixEntry{Image: n, Rebuild: true})
	}
	return entries, nil
}

// changedFiles lists what changed between since and HEAD, or nil --
// "everything" -- when there's nothing to diff against: no ref, a new
// branch's all-zero before-sha, or history the checkout doesn't have.
func changedFiles(since string) []string {
	if since == "" || strings.Trim(since, "0") == "" {
		return nil
	}
	out, err := exec.Command("git", "diff", "--name-only", since, "HEAD").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "can't diff %s..HEAD (%v); building everything\n", since, err)
		return nil
	}
	files := []string{}
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}

// githubOutput renders entries as $GITHUB_OUTPUT lines: the matrix, and
// its size so the workflow can skip the job when there's nothing to do.
func githubOutput(entries []matrixEntry) (string, error) {
	data, err := json.Marshal(map[string][]matrixEntry{"include": entries})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("matrix=%s\ncount=%d\n", data, len(entries)), nil
}
