// Package config loads ~/.gridwell/server.yaml and exposes the server and
// plugin configuration. Plugin config keys are pass-through: the server
// routes Attach(config) to the plugin binary without interpreting the keys.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ServerConfig is the top-level ~/.gridwell/server.yaml structure. There is no
// root: every plugin is equal; the client enters one from the launcher.
type ServerConfig struct {
	Bind      string         `yaml:"bind,omitempty"`
	StaticDir string         `yaml:"static,omitempty"` // "" → headless (no static files served)
	Plugins   []PluginConfig `yaml:"plugins"`
}

// PluginConfig describes one plugin instance. ID is the UUID assigned once
// and stored permanently in the plugin's own DB — it must never change after
// the first run. Name is a display alias only; renaming it never invalidates
// stored links. Binary is the path to the plugin executable; "" means
// built-in. Config is forwarded verbatim to the plugin's Attach call.
type PluginConfig struct {
	ID     string            `yaml:"id"`
	Name   string            `yaml:"name"`
	Kind   string            `yaml:"kind"`
	Binary string            `yaml:"binary,omitempty"`
	Config map[string]string `yaml:"config,omitempty"`
}

// Defaults holds the built-in values used when a field is absent from the
// config file or no config file exists.
var Defaults = ServerConfig{
	Bind:      "127.0.0.1:8080",
	StaticDir: "./web",
}

// Home returns the Gridwell home directory: GRIDWELL_HOME if set, else
// ~/.gridwell. It anchors the mandatory server.yaml and every plugin's derived
// DB path, and is overridable so tests and the desktop app can point at a
// throwaway home without touching the real one.
func Home() (string, error) {
	if h := os.Getenv("GRIDWELL_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: home dir: %w", err)
	}
	return filepath.Join(home, ".gridwell"), nil
}

// DBDir is the per-plugin database directory: <home>/db/<id>. A directory (not
// a bare file) so the plugin's SQLite store and its -wal/-shm siblings live
// together under the id that routes to it.
func DBDir(home, id string) string {
	return filepath.Join(home, "db", id)
}

// DBFile is the per-plugin database file: <home>/db/<id>/store.db. This is the
// single source of truth both `init` and `serve` derive — the path is never
// stored in server.yaml.
func DBFile(home, id string) string {
	return filepath.Join(DBDir(home, id), "store.db")
}

// DefaultPath is the canonical location of the server config file:
// <home>/server.yaml.
func DefaultPath() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "server.yaml"), nil
}

// Load reads path and returns a ServerConfig with defaults filled in for
// missing fields. The config file is mandatory: a missing file returns an
// error (wrapping fs.ErrNotExist) — there is no synthesized fallback, so a
// node never runs with an undeclared identity. Tilde paths ("~/...") in
// StaticDir and plugin Binary/Config are expanded.
func Load(path string) (*ServerConfig, error) {
	cfg := Defaults
	data, err := os.ReadFile(path)
	if err != nil {
		// Surface the missing-file case verbatim (errors.Is(err, fs.ErrNotExist)
		// stays true) so callers can print the "run `gridwell init`" guidance.
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if cfg.Bind == "" {
		cfg.Bind = Defaults.Bind
	}
	if err := expandPaths(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// expandPaths expands "~/" prefixes in every path field of cfg.
func expandPaths(cfg *ServerConfig) error {
	var err error
	if cfg.StaticDir != "" {
		if cfg.StaticDir, err = expandHome(cfg.StaticDir); err != nil {
			return err
		}
	}
	for i := range cfg.Plugins {
		if cfg.Plugins[i].Binary != "" {
			if cfg.Plugins[i].Binary, err = expandHome(cfg.Plugins[i].Binary); err != nil {
				return err
			}
		}
		for k, v := range cfg.Plugins[i].Config {
			if cfg.Plugins[i].Config[k], err = expandHome(v); err != nil {
				return err
			}
		}
	}
	return nil
}

// ErrDuplicatePlugin is returned by AppendPlugin when an entry with the same
// id or name already exists — both must stay unique across the config.
var ErrDuplicatePlugin = errors.New("config: a plugin with this id or name already exists")

// AppendPlugin adds entry to <home>/server.yaml, creating the file (and its
// parent dir) on the first plugin. It tolerates a missing file (this is how the
// very first `gridwell init` bootstraps the config) but rejects a duplicate id
// or name, so the file stays the authoritative, conflict-free plugin list.
//
// Only id/name/kind/config are persisted; the DB path is derived, never stored.
func AppendPlugin(home string, entry PluginConfig) error {
	path := filepath.Join(home, "server.yaml")
	cfg := ServerConfig{}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// First plugin: start from an empty config.
	case err != nil:
		return fmt.Errorf("config: read %s: %w", path, err)
	default:
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("config: parse %s: %w", path, err)
		}
	}

	for _, p := range cfg.Plugins {
		if p.ID == entry.ID {
			return fmt.Errorf("%w: id %q", ErrDuplicatePlugin, entry.ID)
		}
		if p.Name == entry.Name {
			return fmt.Errorf("%w: name %q", ErrDuplicatePlugin, entry.Name)
		}
	}
	cfg.Plugins = append(cfg.Plugins, entry)

	out, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", home, err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

func expandHome(p string) (string, error) {
	if !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: home dir: %w", err)
	}
	return filepath.Join(home, p[2:]), nil
}
