// Package config loads ~/.gridwell/server.yaml and exposes the server and
// plugin configuration. Plugin config keys are pass-through: the server hands
// the map to the plugin binary at spawn (GRIDWELL_PLUGIN_CONFIG) without
// interpreting the keys.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ServerConfig is the top-level ~/.gridwell/server.yaml structure. There is no
// root: every plugin is equal; the client enters one from the launcher.
type ServerConfig struct {
	// NodeID is this node's own durable identity — the uuid that qualifies the
	// node grid (the launcher: one link tile per plugin). Minted once by
	// EnsureNodeID and never changed; a stored deep link or a remote mount's
	// reference through this node depends on it staying put, exactly like a
	// plugin id.
	NodeID string `yaml:"node_id,omitempty"`
	Bind   string `yaml:"bind,omitempty"`
	// BindSet is derived by Load, never stored: true when the file contains a
	// non-empty `bind:` key. It distinguishes "the user pinned the listen
	// address in server.yaml" from "Bind holds the built-in default", which is
	// what lets `serve --bind-default` (the desktop sidecar's ephemeral
	// loopback port) fill in only when the config is silent.
	BindSet bool `yaml:"-"`
	// CacheDir is derived by serve (never stored): <home>/cache, where the
	// loader keeps each MOUNT's read-through cache DB (mountcache — the
	// offline-plan phase-1 layer). Empty disables caching (tests, headless
	// probes). Cache files are disposable and excluded from backup.
	CacheDir  string `yaml:"-"`
	StaticDir string `yaml:"static,omitempty"` // "" → the embedded web client; a path → dev override from disk
	// Password, when non-empty, gates the browser-served web UI: every HTTP
	// request must carry the auth cookie (obtained by entering this password
	// on the login page), and the cookie is checked against the CURRENT
	// password — change it and every browser must log in again. Plaintext by
	// design (single-tenant; the file is the secret). The desktop app
	// authenticates itself from the serve banner and never prompts, and the
	// gRPC node export (federation) is not gated — see server/auth.go.
	Password string `yaml:"password,omitempty"`
	// DisableShells, when true, removes shell tiles from this node entirely:
	// the + palette offers no shell primitive, and the server refuses
	// CreateTile(kind=shell) and every OpenShell — whichever plugin (local or
	// mounted) would serve it. Existing shell tiles keep their frozen
	// previews (placement is sacred); they just can never attach a PTY here.
	DisableShells bool           `yaml:"disable_shells,omitempty"`
	Plugins       []PluginConfig `yaml:"plugins"`
}

// PluginConfig describes one plugin instance. ID is the UUID assigned once
// and stored permanently in the plugin's own DB — it must never change after
// the first run. Name is a display alias only; renaming it never invalidates
// stored links. Binary is the path to the plugin executable; "" means
// built-in. Config is forwarded verbatim to the plugin at spawn.
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
	StaticDir: "", // embedded web client (web.FS); a path serves a dev checkout from disk
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
	// BindSet ("the file explicitly names a bind address") is derived here,
	// once. cfg starts as Defaults, so after the unmarshal above Bind==default
	// is ambiguous between "key absent" and "key present with the default
	// value" — a pointer probe is the only way to see presence. An explicitly
	// empty `bind: ""` counts as unset (and is default-filled below).
	var probe struct {
		Bind *string `yaml:"bind"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	cfg.BindSet = probe.Bind != nil && *probe.Bind != ""
	if cfg.Bind == "" {
		cfg.Bind = Defaults.Bind
	}
	if err := expandPaths(&cfg); err != nil {
		return nil, err
	}
	if err := validateIDs(&cfg); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	return &cfg, nil
}

// validateIDs enforces the identity-shape contract on every id the file
// declares. Ids are otherwise opaque strings, but two properties are
// load-bearing everywhere they travel: no '/' (the qualified-id codec's
// delimiter — rpc.SplitID) and never purely numeric (URL paths tell a
// namespace segment from a tile id by exactly this).
// `gridwell init` mints conforming ids (store.NewShortID); this is the one
// door that catches a hand-edited server.yaml before a bad id gets stored
// into cross-plugin references it can never be removed from.
func validateIDs(cfg *ServerConfig) error {
	check := func(what, id string) error {
		if id == "" {
			return nil // node_id may be absent pre-EnsureNodeID
		}
		if strings.Contains(id, "/") {
			return fmt.Errorf("%s %q must not contain '/'", what, id)
		}
		if _, err := strconv.ParseInt(id, 10, 64); err == nil {
			return fmt.Errorf("%s %q must not be purely numeric (indistinguishable from a tile id)", what, id)
		}
		return nil
	}
	if err := check("node_id", cfg.NodeID); err != nil {
		return err
	}
	for _, p := range cfg.Plugins {
		if p.ID == "" {
			return fmt.Errorf("plugin %q has no id", p.Name)
		}
		if err := check("plugin id", p.ID); err != nil {
			return err
		}
	}
	return nil
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

// EnsureNodeID returns the node's durable identity from <home>/server.yaml,
// minting and persisting one (via newID) if the file predates node identity.
// A node that existed before this field keeps every plugin id untouched — the
// node id is purely additive. The file must already exist (a node never runs
// with an undeclared identity; `gridwell init` creates it).
func EnsureNodeID(home string, newID func() string) (string, error) {
	path := filepath.Join(home, "server.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("config: read %s: %w", path, err)
	}
	cfg := ServerConfig{}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("config: parse %s: %w", path, err)
	}
	if cfg.NodeID != "" {
		return cfg.NodeID, nil
	}
	cfg.NodeID = newID()
	out, err := yaml.Marshal(&cfg)
	if err != nil {
		return "", fmt.Errorf("config: marshal: %w", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return "", fmt.Errorf("config: write %s: %w", path, err)
	}
	return cfg.NodeID, nil
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
