// Package config loads ~/.gridwell/server.yaml and exposes the server and
// plugin configuration. Plugin config keys are pass-through: the server hands
// the map to the plugin binary at spawn (GRIDWELL_PLUGIN_CONFIG) without
// interpreting the keys.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/josephburnett/gridwell/api/idshape"
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
	// Web is the BROWSER door: the address browsers and the desktop window
	// load from, and the password that gates it. Federation is the NODE
	// door: the loopback port other nodes mount this one through (ssh
	// tunnels terminate there). Grouped by door (owner decision
	// 2026-08-26) so the file cannot be misread about which listener the
	// password protects or which port a remote entry must name.
	Web        WebConfig        `yaml:"web,omitempty"`
	Federation FederationConfig `yaml:"federation,omitempty"`
	// WebPassword gates the web door. It is NOT a yaml fact: BuildConfig
	// reads it from <home>/web-password (EnsurePasswordFile), which serve
	// mints — a random token, 0600 — when absent and prints at startup, so
	// whoever runs the process carries it to a browser once; the cookie
	// then lasts. Delete the file to rotate: the next serve mints a new
	// one and every browser must log in again (owner decision 2026-08-26).
	WebPassword string `yaml:"-"`
	// LegacyBind is the pre-2026-08-26 flat bind key. Load folds it into
	// Web (and records a deprecation) so an existing home keeps working;
	// nothing reads it after Load. LegacyPassword (and Web.LegacyPassword)
	// are IGNORED — the password is the file — and parsed only so a rewrite
	// keeps the user's line. They stay in the struct, not in a parse-only
	// probe, because every config REWRITE (AppendPlugin, EnsureNodeID)
	// re-marshals this struct, and a key the struct cannot hold would be
	// silently dropped by the next `gridwell init`.
	LegacyBind     string `yaml:"bind,omitempty"`
	LegacyPassword string `yaml:"password,omitempty"`
	// Deprecations is derived by Load, never stored: one line per legacy
	// key the file still uses, for serve to print.
	Deprecations []string `yaml:"-"`
	// CacheDir is derived by serve (never stored): <home>/cache, where the
	// loader keeps each MOUNT's read-through cache DB (mountcache — the
	// offline-plan phase-1 layer). Empty disables caching (tests, headless
	// probes). Cache files are disposable and excluded from backup.
	CacheDir  string `yaml:"-"`
	StaticDir string `yaml:"static,omitempty"` // "" → the embedded web client; a path → dev override from disk
	// DisableShells, when true, removes shell tiles from this node entirely:
	// the + palette offers no shell primitive, and the server refuses
	// CreateTile(kind=shell) and every OpenShell — whichever plugin (local or
	// mounted) would serve it. Existing shell tiles keep their frozen
	// previews (placement is sacred); they just can never attach a PTY here.
	DisableShells bool           `yaml:"disable_shells,omitempty"`
	Plugins       []PluginConfig `yaml:"plugins"`
	// Connections declares the node's remote-node connections (v2, #269 —
	// reversing #199 by owner decision 2026-08-22: connections are server
	// CONFIG, reconciled into the builtin transport at boot; the picker
	// no longer creates them). A nil slice (no `connections:` key) leaves
	// a legacy transport DB alone; a PRESENT key — even an empty list —
	// makes this file authoritative: rows absent from it tombstone.
	// A POINTER so presence survives a rewrite: nil = no key; a non-nil
	// empty slice = `connections: []`, the authoritative-empty marker —
	// which a plain `omitempty` slice silently dropped on the next
	// `gridwell init` (2026-08-27), flipping the transport out of config
	// mode. Set reports presence.
	Connections *[]ConnectionConfig `yaml:"connections,omitempty"`
	// RetiredNames reserves connection names FOREVER: a deleted
	// connection's name goes here so it can never be reused (stored
	// references through its namespace stay dangling, never re-routed).
	RetiredNames []string `yaml:"retired_names,omitempty"`
}

// WebConfig is the browser door's configuration.
type WebConfig struct {
	Bind string `yaml:"bind,omitempty"`
	// BindSet is derived by Load, never stored: true when the file names
	// a non-empty bind (web.bind, or the legacy flat bind:). It
	// distinguishes "the user pinned the listen address" from "Bind holds
	// the built-in default", which is what lets `serve --bind-default`
	// (the desktop sidecar's ephemeral loopback port) fill in only when
	// the config is silent.
	BindSet bool `yaml:"-"`
	// LegacyPassword is the pre-2026-08-26 `web.password` key. It is
	// IGNORED (the password lives in the web-password file — see
	// ServerConfig.WebPassword) and only parsed so a rewrite keeps the
	// user's line and Load can say so once.
	LegacyPassword string `yaml:"password,omitempty"`
}

// FederationConfig is the node door's configuration: a UNIX SOCKET path,
// never a TCP address (owner decision 2026-08-26: "federation exposed
// only by socket, no fallback"). The kernel enforces who may connect —
// the socket is created 0600, so only the owning uid reaches the
// ungated gRPC export; other users and sandboxed apps on the machine
// cannot, which loopback TCP could never promise. ssh tunnels terminate
// on the socket (direct-streamlocal); a remote entry's addr names it.
// "" = the door is closed (no listener) — the mobile node, which nobody
// mounts. BuildConfig fills the default (FederationSocket) for serve.
type FederationConfig struct {
	Socket string `yaml:"socket,omitempty"`
}

// FederationSocket is the default federation socket path for a home.
func FederationSocket(home string) string {
	return filepath.Join(home, "federation.sock")
}

// ConnectionList answers the declared connections ([] when the key is
// absent) and whether the key was PRESENT — present means this file is
// authoritative for the connection set, empty list included (v2 #269).
func (c *ServerConfig) ConnectionList() (conns []ConnectionConfig, set bool) {
	if c.Connections == nil {
		return nil, false
	}
	return *c.Connections, true
}

// ConnectionConfig is one remote-node connection. Name is an IMMUTABLE
// ID — it is the namespace segment inside every stored reference through
// this connection (idshape.ValidateSegment shape). RENAMING IT DANGLES
// THOSE REFERENCES; change Label instead, retire the old name into
// retired_names if the connection itself dies. Field meanings mirror the
// old picker form: Host set = the ssh bridge; Host empty = a DIRECT dial
// of Addr.
type ConnectionConfig struct {
	Name       string `yaml:"name"`
	Label      string `yaml:"label,omitempty"`
	Host       string `yaml:"host,omitempty"`
	User       string `yaml:"user,omitempty"`
	Port       int64  `yaml:"port,omitempty"`
	Addr       string `yaml:"addr,omitempty"`
	Key        string `yaml:"key,omitempty"`
	KnownHosts string `yaml:"known_hosts,omitempty"`
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
	// LegacyProvider is the retired `provider: true` marker (2026-08-27):
	// every non-native kind IS a content provider now, so the flag says
	// nothing. Parsed only so a rewrite keeps the line; Load notes it.
	LegacyProvider bool `yaml:"provider,omitempty"`
}

// Defaults holds the built-in values used when a field is absent from the
// config file or no config file exists.
var Defaults = ServerConfig{
	Web:       WebConfig{Bind: "127.0.0.1:8080"},
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
	// Presence is derived here, once. cfg starts as Defaults, so after the
	// unmarshal above a field equal to its default is ambiguous between
	// "key absent" and "key present with the default value" — a pointer
	// probe is the only way to see presence. An explicitly empty value
	// counts as unset (and is default-filled below).
	var probe struct {
		Bind *string `yaml:"bind"`
		Web  *struct {
			Bind *string `yaml:"bind"`
		} `yaml:"web"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	// The legacy flat keys fold into the web door — a file written before
	// the doors were grouped keeps working, and says so once per serve.
	// Presence comes from the probe, never from the value: cfg started as
	// Defaults, so Web.Bind is non-empty even when the file never said so.
	webBindSet := probe.Web != nil && probe.Web.Bind != nil && *probe.Web.Bind != ""
	legacyBindSet := probe.Bind != nil && *probe.Bind != ""
	if !webBindSet && legacyBindSet {
		cfg.Web.Bind = cfg.LegacyBind
		cfg.Deprecations = append(cfg.Deprecations, "bind: is now web.bind (the flat key still loads)")
	}
	if cfg.LegacyPassword != "" || cfg.Web.LegacyPassword != "" {
		cfg.Deprecations = append(cfg.Deprecations, "password: / web.password are IGNORED — the web password is the web-password file beside this config (delete it to rotate)")
	}
	cfg.Web.BindSet = webBindSet || legacyBindSet
	for _, pc := range cfg.Plugins {
		if pc.LegacyProvider {
			cfg.Deprecations = append(cfg.Deprecations, fmt.Sprintf("plugin %q: provider: true is implied for every non-native kind; drop the line", pc.Name))
		}
	}
	if cfg.Web.Bind == "" {
		cfg.Web.Bind = Defaults.Web.Bind
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
// `gridwell init` mints conforming ids (idshape.NewShortID); this is the one
// door that catches a hand-edited server.yaml before a bad id gets stored
// into cross-plugin references it can never be removed from.
func validateIDs(cfg *ServerConfig) error {
	// The rules live with the mint (api/idshape) — one id-shape owner.
	check := idshape.ValidateSegment
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
	if cfg.Federation.Socket, err = expandHome(cfg.Federation.Socket); err != nil {
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

// PasswordFile is where a home's web password lives: beside server.yaml
// and the federation socket, 0600.
func PasswordFile(home string) string { return filepath.Join(home, "web-password") }

// NodeViewFile is the landing page's persisted viewport.
func NodeViewFile(home string) string { return filepath.Join(home, "node-view.json") }

// DurableFiles are the loose files a home is made of besides its DBs —
// what a backup must carry for a restored home to be "as you left it"
// (the password: every browser stays logged in; the landing viewport).
// The ONE list; backup reads it, so a new durable file joins here and
// nowhere else. Absent files are simply absent (a home that has never
// served has no password yet).
func DurableFiles(home string) []string {
	return []string{filepath.Join(home, "server.yaml"), PasswordFile(home), NodeViewFile(home)}
}

// EnsurePasswordFile returns the home's web password, minting one (128
// random bits as hex, written 0600) when the file is absent — the door
// is never open and never needs a human to choose a secret. The file IS
// the password: delete it and the next serve rotates.
func EnsurePasswordFile(home string) (string, error) {
	path := PasswordFile(home)
	data, err := os.ReadFile(path)
	if err == nil {
		if pw := strings.TrimSpace(string(data)); pw != "" {
			return pw, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("config: read %s: %w", path, err)
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("config: mint web password: %w", err)
	}
	pw := hex.EncodeToString(b[:])
	if err := os.WriteFile(path, []byte(pw+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("config: write %s: %w", path, err)
	}
	return pw, nil
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
