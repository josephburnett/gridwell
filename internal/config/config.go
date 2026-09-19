// Package config loads ~/.gridwell/server.yaml, the one config of the one
// node. Plugin config keys are pass-through, handed to the plugin binary at
// spawn uninterpreted. The node writes the file only to mint an absent id.
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

// ServerConfig is ~/.gridwell/server.yaml.
type ServerConfig struct {
	// ID is the node's identity and its home's: the segment on every local
	// reference ("<id>/12") and every connection. Never changed once minted.
	ID string `yaml:"id,omitempty"`
	// Web is the browser door. Its password is not yaml; see WebPassword.
	Web WebConfig `yaml:"web,omitempty"`
	// Federation is the connection door other nodes mount this one through.
	Federation FederationConfig `yaml:"federation,omitempty"`
	// StaticDir serves the web client from disk, not the embedded copy.
	StaticDir string `yaml:"static,omitempty"`
	// Shell is the login shell for shell tiles; "" means the host default.
	Shell string `yaml:"shell,omitempty"`
	// DisableShells removes shell tiles entirely: no shell primitive in the
	// + palette, CreateTile(kind=shell) and OpenShell refused in every
	// namespace, existing tiles left with their preview and placement.
	DisableShells bool `yaml:"disable_shells,omitempty"`
	// Connections declares the remote nodes. Dropping a stanza does not
	// retire the name: the row and its landing stay and the stanza brings
	// them back. Only RetiredNames retires.
	Connections []ConnectionConfig `yaml:"connections,omitempty"`
	// RetiredNames is the one owner of retirement: a name here is never
	// reused and its references stay dangling rather than re-routed. Boot
	// mirrors it onto the rows and retires no other way.
	RetiredNames []string `yaml:"retired_names,omitempty"`
	// Plugins are the content plugins: separate binaries speaking plugin.v1.
	Plugins []PluginConfig `yaml:"plugins,omitempty"`

	// WebPassword is not a yaml fact: BuildConfig reads it from the file.
	WebPassword string `yaml:"-"`
	// CacheDir is derived by serve, <home>/cache; empty disables caching.
	CacheDir string `yaml:"-"`

	// doc is the file as loaded, comments and all. Save edits the minted ids
	// into it and writes it back, so a default Parse filled in or a path it
	// expanded can never reach the file: the struct is what the node reads,
	// the document is what the user wrote. nil is a home with no file yet.
	doc *yaml.Node
}

type WebConfig struct {
	Bind string `yaml:"bind,omitempty"`
	// BindSet lets serve --bind-default fill in only when the file was silent.
	BindSet bool `yaml:"-"`
}

// FederationConfig is the connection door: a unix socket path, never a TCP
// address. The socket is 0600, so the kernel keeps every other uid and every
// sandboxed app off the ungated gRPC export. "" closes the door entirely.
type FederationConfig struct {
	Socket string `yaml:"socket,omitempty"`
}

// FederationSocket is the default connection-door socket path for a home.
func FederationSocket(home string) string {
	return filepath.Join(home, "federation.sock")
}

// ConnectionConfig is one remote node. Name is an immutable id, the segment
// inside every stored reference through this connection, so renaming it
// dangles them; change Label instead. Host set means the ssh bridge, Host
// empty a direct dial; Addr is the far socket path either way.
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

// PluginConfig describes one content plugin. ID is the segment on every
// reference into it, minted once and never changed; Label is display only.
// Binary defaults to gridwell-plugin-<kind> beside the gridwell binary.
// Config lands in the plugin's environment, so a secret is a path, not a value.
type PluginConfig struct {
	ID     string            `yaml:"id,omitempty"`
	Kind   string            `yaml:"kind"`
	Label  string            `yaml:"label,omitempty"`
	Binary string            `yaml:"binary,omitempty"`
	Config map[string]string `yaml:"config,omitempty"`
}

// Defaults fills fields the config file leaves absent.
var Defaults = ServerConfig{
	Web: WebConfig{Bind: "127.0.0.1:8080"},
}

// Home returns GRIDWELL_HOME if set, else ~/.gridwell.
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

// DBFile is the node's database, <home>/gridwell.db.
func DBFile(home string) string {
	return filepath.Join(home, "gridwell.db")
}

// CacheFile is the node's source cache, <home>/cache.db. Disposable.
func CacheFile(home string) string {
	return filepath.Join(home, "cache.db")
}

// PluginStateDir is one plugin's private directory, <home>/plugins/<id>,
// handed over at spawn as `state_dir`. What a plugin keeps there is its own
// memory of its source, never a node fact, and disposable like cache.db.
func PluginStateDir(home, id string) string {
	return filepath.Join(home, "plugins", id)
}

// DefaultPath is <home>/server.yaml.
func DefaultPath() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "server.yaml"), nil
}

// retiredKeys makes a stale file fail with the fix, not a decoder message.
var retiredKeys = map[string]string{
	"bind":     "is `web: {bind: …}`",
	"password": "is the web-password file beside this config (delete it to rotate)",
	"node_id":  "is the layout Gridwell used before one database per node; v0.1.0 is the last release that converts a home written that way",
}

// Load reads path and fills in defaults. A missing file wraps fs.ErrNotExist.
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

// Parse decodes server.yaml bytes, for callers that already hold them.
func Parse(data []byte) (*ServerConfig, error) {
	var cfg ServerConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, retiredKeyHint(err)
	}
	// The same bytes a second time, as a document: the decode above already
	// refused every unknown key, so this cannot fail on shape.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	cfg.doc = &doc
	// Presence first: the default below can no longer tell it apart.
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
			return nil, fmt.Errorf("plugins[%d]: kind %q is the node itself, not a plugin: the node's id is `id:` and its connections are `connections:` — a home that still lists them here is the shape v0.1.0 was the last release to convert", i, cfg.Plugins[i].Kind)
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

// retiredKeyHint rewrites "field X not found" for a retired key into the fix.
func retiredKeyHint(err error) error {
	msg := err.Error()
	for key, hint := range retiredKeys {
		if strings.Contains(msg, "field "+key+" not found") {
			return fmt.Errorf("%s: `%s` %s", msg, key, hint)
		}
	}
	return err
}

// Mint fills every absent id and reports whether it changed anything. It is
// the one place config ids are minted.
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

// Save writes the minted ids into the loaded document and writes it back
// 0600, through a temp file and a rename so a crash never loses the only copy
// of the node's ids. It is the one config write, and it writes only what Mint
// minted: a field the file left absent stays absent, and the document's
// comments and spelling survive.
func Save(path string, cfg *ServerConfig) error {
	root, err := mintedDocument(cfg)
	if err != nil {
		return err
	}
	out, err := yaml.Marshal(root)
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

// mintedDocument is the loaded document with cfg's ids set on it. A home with
// no file gets a document holding the node id alone, which is all a fresh
// Defaults has to mint; anything else on a nil document has no user spelling
// to keep and is refused rather than invented.
func mintedDocument(cfg *ServerConfig) (*yaml.Node, error) {
	root := cfg.doc
	if root == nil || len(root.Content) == 0 {
		if len(cfg.Plugins) > 0 {
			return nil, errors.New("config: cannot mint plugin ids into a document that was never loaded")
		}
		root = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	top := root.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil, errors.New("config: server.yaml is not a mapping")
	}
	setScalar(top, "id", cfg.ID)
	plugins := mappingValue(top, "plugins")
	if plugins == nil {
		if len(cfg.Plugins) > 0 {
			return nil, errors.New("config: plugins declared but the document has no plugins list")
		}
		return root, nil
	}
	if plugins.Kind != yaml.SequenceNode || len(plugins.Content) != len(cfg.Plugins) {
		return nil, fmt.Errorf("config: the document lists %d plugins, the config %d", len(plugins.Content), len(cfg.Plugins))
	}
	for i, item := range plugins.Content {
		if item.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("config: plugins[%d] is not a mapping", i)
		}
		setScalar(item, "id", cfg.Plugins[i].ID)
	}
	return root, nil
}

// mappingValue is the value node under key in a mapping, nil when absent.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setScalar sets key to value in a mapping, in place where the key exists and
// first otherwise, so a minted id lands where a hand-written one would.
func setScalar(m *yaml.Node, key, value string) {
	if v := mappingValue(m, key); v != nil {
		v.Kind, v.Tag, v.Value = yaml.ScalarNode, "!!str", value
		return
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	m.Content = append([]*yaml.Node{k, v}, m.Content...)
}

// validateIDs is the one door that catches a hand-edited id before it is
// stored into references it can never be removed from. Two properties are
// load-bearing: no '/', rpc.SplitID's delimiter, and never purely numeric,
// which is how a URL tells a segment from a tile id.
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
	for _, r := range cfg.RetiredNames {
		if err := check("retired connection name", r); err != nil {
			return err
		}
		if names[r] {
			return fmt.Errorf("connection %q is declared and also in retired_names — a retired name never returns; mint a new one", r)
		}
	}
	return nil
}

// expandPaths applies ExpandHome to every path server.yaml can spell with a
// tilde. The home directory is asked for once, and only a path that needs it
// fails when there is none.
func expandPaths(cfg *ServerConfig) error {
	home, herr := os.UserHomeDir()
	expand := func(p string) (string, error) {
		if !needsHome(p) {
			return p, nil
		}
		if herr != nil {
			return "", fmt.Errorf("config: home dir: %w", herr)
		}
		return ExpandHome(p, home), nil
	}
	var err error
	if cfg.Federation.Socket, err = expand(cfg.Federation.Socket); err != nil {
		return err
	}
	if cfg.StaticDir, err = expand(cfg.StaticDir); err != nil {
		return err
	}
	for i := range cfg.Plugins {
		if cfg.Plugins[i].Binary, err = expand(cfg.Plugins[i].Binary); err != nil {
			return err
		}
		for k, v := range cfg.Plugins[i].Config {
			if cfg.Plugins[i].Config[k], err = expand(v); err != nil {
				return err
			}
		}
	}
	return nil
}

// PasswordFile is <home>/web-password.
func PasswordFile(home string) string { return filepath.Join(home, "web-password") }

// DurableFiles are the loose files a home is made of besides its databases:
// what a backup must carry, the password included so every browser stays
// logged in. It is the one list.
func DurableFiles(home string) []string {
	return []string{filepath.Join(home, "server.yaml"), PasswordFile(home)}
}

// EnsurePasswordFile returns the home's web password, minting 128 random
// bits as hex written 0600 when absent, so the door is never open and never
// needs a human to choose a secret. Deleting the file rotates.
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

// ExpandHome is the one tilde grammar every host-local path in server.yaml
// reads, the `connections:` key paths included: "~" is home, "~/x" is under
// it, and anything else is verbatim, so "~user" is never guessed at. An empty
// home expands nothing, for a caller with no home to default from.
func ExpandHome(p, home string) string {
	if home == "" || !needsHome(p) {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

func needsHome(p string) bool { return p == "~" || strings.HasPrefix(p, "~/") }
