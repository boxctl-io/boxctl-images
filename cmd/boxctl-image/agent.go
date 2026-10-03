package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"time"

	"boxctl-vms/internal/agentrelease"
	"boxctl-vms/internal/objectstore"
)

// runPublishAgent packs dir (scripts/agent, with boxctl-tunnel-agent
// already built into it) and publishes it as the current agent release
// (internal/agentrelease), unless the same bundle is already current --
// then nothing changes and no host reinstalls.
func runPublishAgent(args []string) error {
	fs := flag.NewFlagSet("publish-agent", flag.ExitOnError)
	dir := fs.String("dir", "scripts/agent", "agent directory to bundle")
	gitSHA := fs.String("git-sha", "", "commit the bundle was built from")
	_ = fs.Parse(args)

	bundle, sha, err := agentrelease.Bundle(*dir)
	if err != nil {
		return err
	}
	store, err := storeFromEnv()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if data, err := store.Get(ctx, agentrelease.ManifestKey); err == nil {
		if cur, err := agentrelease.ParseManifest(data); err == nil && cur.SHA256 == sha {
			fmt.Printf("agent bundle unchanged (version %s), nothing to publish\n", cur.Version)
			return nil
		}
	} else if !errors.Is(err, objectstore.ErrNotFound) {
		return err
	}

	m := agentrelease.Manifest{
		Version:     sha[:12],
		SHA256:      sha,
		Key:         agentrelease.BundleKey(sha),
		SizeBytes:   int64(len(bundle)),
		GitSHA:      *gitSHA,
		PublishedAt: time.Now().UTC(),
	}
	if err := store.Put(ctx, m.Key, bytes.NewReader(bundle), m.SizeBytes, "application/gzip", nil); err != nil {
		return err
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := store.Put(ctx, agentrelease.ManifestKey, bytes.NewReader(manifest), int64(len(manifest)), "application/json", nil); err != nil {
		return err
	}
	fmt.Printf("published agent version %s (%d bytes) as %s\n", m.Version, m.SizeBytes, m.Key)
	return nil
}
