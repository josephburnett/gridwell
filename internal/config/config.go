// Package config loads ~/.gridwell/server.yaml: the ONE config of the ONE
// node (docs/one-node.md). The node is its home — one id qualifies every
// local reference and every connection through this node; `plugins:` lists
// content plugins only; `connections:` lists the remote nodes. Plugin
// config keys are pass-through: the node hands the map to the plugin
// binary at spawn (GRIDWELL_PLUGIN_CONFIG) without interpreting the keys.
//
// The file is hand-edited. The node writes it in exactly one case: to
// mint an id that is absent (the node's own, or a plugin's) — nothing else
// ever rewrites it, so a comment the user leaves in it survives every
// serve after the first.
package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/josephburnett/gridwell/api/idshape"
)

// ServerConfig is the top-level ~/.gridwell/server.yaml structure.
type ServerConfig struct {
	// ID is the node's identity AND its home's: the namespace segment on
	// every local reference ("<id>/12") and on every connection through
	// this node ("<id>/<conn>/…"). Minted once by serve when absent, never
	// changed — every stored reference depends on it staying put.
	ID string `yaml:"id,omitempty"`
	// Web is the BROWSER door: the address browsers and the desktop window
	// load from. Its password is NOT a yaml fact — see WebPassword.
	Web WebConfig `yaml:"web,omitempty"`
	// Federation is the NODE door: the unix socket other nodes mount this
	// one through (ssh tunnels terminate there).
	Federation FederationConfig `yaml:"federation,omitempty"`
	// StaticDir: "" → the embedded web client; a path → dev override from
	// disk.
	StaticDir string `yaml:"static,omitempty"`
	// Shell is the login shell for shell tiles ("" → the host default).
	Shell string `yaml:"shell,omitempty"`
	// DisableShells, when true, removes shell tiles from this node entirely:
	// the + palette offers no shell primitive, and the server refuses
	// CreateTile(kind=shell) and every OpenShell — whichever namespace
	// (home or mounted) would serve it. Existing shell tiles keep their
	// frozen previews (placement is sacred); they just can never attach a
	// PTY here.
	DisableShells bool `yaml:"disable_shells,omitempty"`
	// Connections declares the remote nodes. The list is AUTHORITATIVE:
	// the transport's connection rows are reconciled against it at every
	// boot, and a name absent from it (and not retired) is an error.
	Connections []ConnectionConfig `yaml:"connections,omitempty"`
	// RetiredNames reserves connection names FOREVER: a deleted
	// connection's name goes here so it can never be reused (stored
	// references through its namespace stay dangling, never re-routed).
	RetiredNames []string `yaml:"retired_names,omitempty"`
	// Plugins are the CONTENT plugins — separate binaries speaking
	// plugin.v1. The node's own store and transport are not listed: they
	// are the node.
	Plugins []PluginConfig `yaml:"plugins,omitempty"`

	// WebPassword gates the web door. It is NOT a yaml fact: BuildConfig
	// reads it from <home>/web-password (EnsurePasswordFile), which serve
	// mints — a random token, 0600 — when absent and prints at startup, so
	// whoever runs the process carries it to a browser once; the cookie
	// then lasts. Delete the file to rotate: the next serve mints a new
	// one and every browser must log in again (owner decision 2026-08-26).
	WebPassword string `yaml:"-"`
	// CacheDir is derived by serve (never stored): <home>/cache, where the
	// node keeps the mount cache. Empty disables caching (tests, headless
	// probes). Cache files are disposable and excluded from backup.
	CacheDir string `yaml:"-"`
}

// WebConfig is the browser door's configuration.
type WebConfig struct {
	Bind string `yaml:"bind,omitempty"`
	// BindSet is derived by Load, never stored: true when the file names
	// a non-empty web.bind. It distinguishes "the user pinned the listen
	// address" from "Bind holds the built-in default", which is what lets
	// `serve --bind-default` (the desktop sidecar's ephemeral loopback
	// port) fill in only when the config is silent.
	BindSet bool `yaml:"-"`
}

// FederationConfig is the node door's configuration: a UNIX SOCKET path,
// never a TCP address (owner decision 2026-08-26: "federation exposed
// only by socket, no fallback"). The kernel enforces who may connect —
// the socket is created 0600, so only the owning uid reaches the
// ungated gRPC export; other users and sandboxed apps on the machine
// cannot, which loopback TCP could never promise. ssh tunnels terminate
// on the socket (direct-streamlocal); a connection's addr names it.
// "" = the door is closed (no listener) — the mobile node, which nobody
// mounts. BuildConfig fills the default (FederationSocket) for serve.
type FederationConfig struct {
	Socket string `yaml:"socket,omitempty"`
}

// FederationSocket is the default federation socket path for a home.
func FederationSocket(home string) string {
	return filepath.Join(home, "federation.sock")
}

// ConnectionConfig is one remote node. Name is an IMMUTABLE ID — it is
// the namespace segment inside every stored reference through this
// connection ("<id>/<name>/…", idshape.ValidateSegment shape). RENAMING IT
// DANGLES THOSE REFERENCES; change Label instead, retire the old name
// into retired_names if the connection itself dies. Host set = the ssh
// bridge; Host empty = a DIRECT dial of Addr. Addr is the REMOTE's
// federation socket path and is required either way.
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

// PluginConfig describes one content plugin. ID is the namespace segment
// on every reference into it, minted once by serve when absent and never
// changed. Label is display only; renaming it never invalidates stored
// links. Binary is the path to the plugin executable ("" → the
// gridwell-plugin-<kind> beside the gridwell binary). Config is forwarded
// verbatim to the plugin at spawn — its values are visible in the
// process environment, so a secret is a FILE PATH, never a value.
type PluginConfig struct {
	ID     string            `yaml:"id,omitempty"`
	Kind   string            `yaml:"kind"`
	Label  string            `yaml:"label,omitempty"`
	Binary string            `yaml:"binary,omitempty"`
	Config map[string]string `yaml:"config,omitempty"`
}

// Defaults holds the built-in values used when a field is absent from the
// config file or no config file exists.
var Defaults = ServerConfig{
	Web: WebConfig{Bind: "127.0.0.1:8080"},
}

// Home returns the Gridwell home directory: GRIDWELL_HOME if set, else
// ~/.gridwell. It anchors server.yaml and every derived path, and is
// overridable so tests and the desktop app can point at a throwaway home
// without touching the real one.
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

// DBDir is a namespace's database directory: <home>/db/<id>. A directory
// (not a bare file) so a SQLite store and its -wal/-shm siblings live
// together under the id that routes to it.
func DBDir(home, id string) string {
	return filepath.Join(home, "db", id)
}

// DBFile is a namespace's database file: <home>/db/<id>/store.db — the
// home store under the node's id, a plugin's memory DB under the
// plugin's. Derived, never stored in server.yaml.
func DBFile(home, id string) string {
	return filepath.Join(DBDir(home, id), "store.db")
}

// RemoteDBFile is the transport's connection store, beside the home
// store under the node's own id: <home>/db/<id>/remote.db.
func RemoteDBFile(home, id string) string {
	return filepath.Join(DBDir(home, id), "remote.db")
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

// retiredKeys names the keys of the pre-one-node file shapes so a stale
// file fails with the fix, not a decoder message.
var retiredKeys = map[string]string{
	"node_id":  "is `id` (the node IS its home; docs/one-node.md)",
	"bind":     "is `web: {bind: …}`",
	"password": "is the web-password file beside this config (delete it to rotate)",
	"provider": "is gone (the old `provider: true` flag) — every entry is a content plugin",
	"name":     "is `label`",
}

// Load reads path and returns a ServerConfig with defaults filled in for
// missing fields. A missing file returns an error wrapping fs.ErrNotExist
// (serve creates the file; BuildConfig). The decode is STRICT: an unknown
// key is an error, so a retired key (node_id, flat bind, a `kind: home`
// plugin entry) fails loudly instead of being silently ignored. Tilde
// paths ("~/...") are expanded.
func Load(path string) (*ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	return cfg, nil
}

// Parse decodes server.yaml bytes (Load's body, for callers that hold the
// bytes — tests, the desktop's seeded homes).
func Parse(data []byte) (*ServerConfig, error) {
	var cfg ServerConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, retiredKeyHint(err)
	}
	// Presence before defaults: BindSet is "the file names a bind", which
	// the default-filled value below can no longer tell.
	cfg.Web.BindSet = cfg.Web.Bind != ""
	if cfg.Web.Bind == "" {
		cfg.Web.Bind = Defaults.Web.Bind
	}
	for i := range cfg.Plugins {
		if cfg.Plugins[i].Kind == "" {
			return nil, fmt.Errorf("plugins[%d]: kind is required", i)
		}
		switch cfg.Plugins[i].Kind {
		case "home", "remote", "local", "localdb", "ssh":
			return nil, fmt.Errorf("plugins[%d]: kind %q is the node itself, not a plugin — delete the entry (the node's id is `id:`, its connections are `connections:`; docs/one-node.md)", i, cfg.Plugins[i].Kind)
		}
	}
	if err := expandPaths(&cfg); err != nil {
		return nil, err
	}
	if err := validateIDs(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// retiredKeyHint rewrites the decoder's "field X not found" for a key the
// one-node shape retired into the fix.
func retiredKeyHint(err error) error {
	msg := err.Error()
	for key, hint := range retiredKeys {
		if strings.Contains(msg, "field "+key+" not found") {
			return fmt.Errorf("%s: `%s` %s", msg, key, hint)
		}
	}
	return err
}

// Mint fills every absent id (the node's own, each plugin's) with a fresh
// short id and reports whether anything was minted — the caller saves the
// file then. The ONE place ids are minted.
func Mint(cfg *ServerConfig) bool {
	changed := false
	if cfg.ID == "" {
		cfg.ID = idshape.NewShortID()
		changed = true
	}
	for i := range cfg.Plugins {
		if cfg.Plugins[i].ID == "" {
			cfg.Plugins[i].ID = idshape.NewShortID()
			changed = true
		}
	}
	return changed
}

// Save writes cfg to path (0600, atomically: temp + rename, so a crash
// mid-write never loses the only copy of the node's ids). Derived fields
// (WebPassword, CacheDir, BindSet) never reach the file.
func Save(path string, cfg *ServerConfig) error {
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: mkdir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("config: rename %s: %w", path, err)
	}
	return nil
}

// validateIDs enforces the identity-shape contract on every id the file
// declares. Ids are otherwise opaque strings, but two properties are
// load-bearing everywhere they travel: no '/' (the qualified-id codec's
// delimiter — rpc.SplitID) and never purely numeric (URL paths tell a
// namespace segment from a tile id by exactly this). Mint produces
// conforming ids; this is the one door that catches a hand-edited id
// before it gets stored into references it can never be removed from. An
// absent id is legal here (Mint fills it).
func validateIDs(cfg *ServerConfig) error {
	check := idshape.ValidateSegment
	if cfg.ID != "" {
		if err := check("id", cfg.ID); err != nil {
			return err
		}
	}
	seen := map[string]bool{cfg.ID: cfg.ID != ""}
	for _, p := range cfg.Plugins {
		if p.ID == "" {
			continue
		}
		if err := check("plugin id", p.ID); err != nil {
			return err
		}
		if seen[p.ID] {
			return fmt.Errorf("plugin id %q is declared twice", p.ID)
		}
		seen[p.ID] = true
	}
	names := map[string]bool{}
	for _, c := range cfg.Connections {
		if err := check("connection name", c.Name); err != nil {
			return err
		}
		if names[c.Name] {
			return fmt.Errorf("connection %q is declared twice", c.Name)
		}
		names[c.Name] = true
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

// PasswordFile is where a home's web password lives: beside server.yaml
// and the federation socket, 0600.
func PasswordFile(home string) string { return filepath.Join(home, "web-password") }

// DurableFiles are the loose files a home is made of besides its DBs —
// what a backup must carry for a restored home to be "as you left it"
// (the password: every browser stays logged in). The ONE list; backup
// reads it, so a new durable file joins here and nowhere else. Absent
// files are simply absent (a home that has never served has no password
// yet).
func DurableFiles(home string) []string {
	return []string{filepath.Join(home, "server.yaml"), PasswordFile(home)}
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
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", fmt.Errorf("config: mkdir %s: %w", home, err)
	}
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
