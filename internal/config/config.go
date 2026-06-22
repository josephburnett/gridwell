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

// ServerConfig is the top-level ~/.gridwell/server.yaml structure.
type ServerConfig struct {
	Bind      string         `yaml:"bind"`
	DB        string         `yaml:"db"`
	StaticDir string         `yaml:"static"` // "" → headless (no static files served)
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
	Binary string            `yaml:"binary"`
	Config map[string]string `yaml:"config"`
}

// Defaults holds the built-in values used when a field is absent from the
// config file or no config file exists.
var Defaults = ServerConfig{
	Bind:      "127.0.0.1:8080",
	DB:        "~/.gridwell/gridwell.db",
	StaticDir: "./web",
}

// DefaultPath is the canonical location of the server config file.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: home dir: %w", err)
	}
	return filepath.Join(home, ".gridwell", "server.yaml"), nil
}

// Load reads path and returns a ServerConfig with defaults filled in for
// missing fields. If path does not exist, defaults are returned without error.
// Tilde paths ("~/...") in DB, StaticDir, and plugin Binary are expanded.
func Load(path string) (*ServerConfig, error) {
	cfg := Defaults
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		expanded := cfg
		if err := expandPaths(&expanded); err != nil {
			return nil, err
		}
		return &expanded, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if cfg.Bind == "" {
		cfg.Bind = Defaults.Bind
	}
	if cfg.DB == "" {
		cfg.DB = Defaults.DB
	}
	if err := expandPaths(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// expandPaths expands "~/" prefixes in every path field of cfg.
func expandPaths(cfg *ServerConfig) error {
	var err error
	if cfg.DB, err = expandHome(cfg.DB); err != nil {
		return err
	}
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
