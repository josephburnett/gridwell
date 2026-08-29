package cli

// resolvePluginBinaries: a bundled binary's in-process PLUGIN factory
// must not swallow a PLUGIN entry's binary lookup — the two serve
// different services (found 2026-08-23: gridwell-all + a plugin home
// refused to boot).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// Every kind is a plugin: a bundled plugin factory keeps its entry
// in-process, and everything else resolves gridwell-plugin-<kind> — there
// is no plugin binary and no flag to get wrong.
func TestKindsResolvePluginBinaries(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gridwell-plugin-fs"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GRIDWELL_PLUGIN_DIR", dir)

	cfg := &config.ServerConfig{Plugins: []config.PluginConfig{
		{ID: "p1", Kind: "fs"},
		{ID: "p3", Kind: "proc"},
	}}
	bundled := map[string]plugin.Factory{"proc": nil}
	if err := resolvePluginBinaries(cfg, bundled); err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(cfg.Plugins[0].Binary); got != "gridwell-plugin-fs" {
		t.Fatalf("fs resolved %q", cfg.Plugins[0].Binary)
	}
	if cfg.Plugins[1].Binary != "" {
		t.Fatalf("a bundled kind must stay in-process: %q", cfg.Plugins[1].Binary)
	}
	missing := &config.ServerConfig{Plugins: []config.PluginConfig{{ID: "g1", Label: "todos", Kind: "gitlab"}}}
	if err := resolvePluginBinaries(missing, nil); err == nil || !strings.Contains(err.Error(), "gridwell-plugin-gitlab") {
		t.Fatalf("a missing plugin binary must be named: %v", err)
	}
}
