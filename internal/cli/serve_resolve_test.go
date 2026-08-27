package cli

// resolvePluginBinaries: a bundled binary's in-process PLUGIN factory
// must not swallow a PROVIDER entry's binary lookup — the two serve
// different services (found 2026-08-23: gridwell-all + a provider home
// refused to boot).

import (
	"os"
	"path/filepath"
	"strings"
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
	if err := resolvePluginBinaries(cfg, factories, nil); err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(cfg.Plugins[0].Binary); got != "gridwell-provider-fs" {
		t.Fatalf("provider entry resolved %q — the plugin factory swallowed it", cfg.Plugins[0].Binary)
	}
	if cfg.Plugins[1].Binary != "" {
		t.Fatalf("plugin entry with a factory must stay in-process, resolved %q", cfg.Plugins[1].Binary)
	}
}

// The provider twin: a bundled PROVIDER factory keeps its entry
// in-process (no binary resolved), and never satisfies a PLUGIN entry.
func TestProviderFactoriesKeepProviderEntriesInProcess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gridwell-plugin-fs"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GRIDWELL_PLUGIN_DIR", dir)

	cfg := &config.ServerConfig{Plugins: []config.PluginConfig{
		{ID: "p1", Kind: "fs", Provider: true},
		{ID: "p2", Kind: "fs"},
	}}
	providers := map[string]plugin.ProviderFactory{"fs": nil} // the bundled shape
	if err := resolvePluginBinaries(cfg, nil, providers); err != nil {
		t.Fatal(err)
	}
	if cfg.Plugins[0].Binary != "" {
		t.Fatalf("provider entry with a provider factory must stay in-process, resolved %q", cfg.Plugins[0].Binary)
	}
	if got := filepath.Base(cfg.Plugins[1].Binary); got != "gridwell-plugin-fs" {
		t.Fatalf("plugin entry resolved %q — the provider factory must not satisfy it", cfg.Plugins[1].Binary)
	}
}

// A provider kind declared WITHOUT `provider: true` (Joe, 2026-08-27:
// the gitlab entry) fails on the plugin name — the error must point at
// the flag when the provider binary is right there.
func TestMissingProviderFlagIsNamedInTheError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gridwell-provider-gitlab"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GRIDWELL_PLUGIN_DIR", dir)
	cfg := &config.ServerConfig{Plugins: []config.PluginConfig{{ID: "g1", Name: "todos", Kind: "gitlab"}}}
	err := resolvePluginBinaries(cfg, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "provider: true") {
		t.Fatalf("err = %v, want the provider-flag hint", err)
	}
	cfg = &config.ServerConfig{Plugins: []config.PluginConfig{{ID: "g1", Name: "x", Kind: "nosuch"}}}
	if err := resolvePluginBinaries(cfg, nil, nil); err == nil || strings.Contains(err.Error(), "provider: true") {
		t.Fatalf("no provider binary → no hint: %v", err)
	}
}
