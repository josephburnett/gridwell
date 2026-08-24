package cli

// resolvePluginBinaries: a bundled binary's in-process PLUGIN factory
// must not swallow a PROVIDER entry's binary lookup — the two serve
// different services (found 2026-08-23: gridwell-all + a provider home
// refused to boot).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/plugin"
)

func TestProviderEntriesResolvePastPluginFactories(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"gridwell-provider-fs", "gridwell-plugin-fs"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GRIDWELL_PLUGIN_DIR", dir)

	cfg := &config.ServerConfig{Plugins: []config.PluginConfig{
		{ID: "p1", Kind: "fs", Provider: true},
		{ID: "p2", Kind: "fs"},
	}}
	factories := map[string]plugin.ServerFactory{"fs": nil} // the bundled shape
	if err := resolvePluginBinaries(cfg, factories); err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(cfg.Plugins[0].Binary); got != "gridwell-provider-fs" {
		t.Fatalf("provider entry resolved %q — the plugin factory swallowed it", cfg.Plugins[0].Binary)
	}
	if cfg.Plugins[1].Binary != "" {
		t.Fatalf("plugin entry with a factory must stay in-process, resolved %q", cfg.Plugins[1].Binary)
	}
}
