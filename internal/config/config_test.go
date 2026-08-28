package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoad_missing pins the mandatory-config contract: a missing server.yaml is
// an error (wrapping fs.ErrNotExist), not a silent defaults fallback. This is
// what forces a node to declare its plugins via `gridwell init` before it runs.
func TestLoad_missing(t *testing.T) {
	_, err := Load("/nonexistent/path/server.yaml")
	if err == nil {
		t.Fatal("missing file must error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error should wrap fs.ErrNotExist; got: %v", err)
	}
}

func TestHome(t *testing.T) {
	t.Setenv("GRIDWELL_HOME", "/tmp/gw-home")
	h, err := Home()
	if err != nil {
		t.Fatalf("Home: %v", err)
	}
	if h != "/tmp/gw-home" {
		t.Errorf("GRIDWELL_HOME not honored: got %q", h)
	}

	os.Unsetenv("GRIDWELL_HOME")
	h, err = Home()
	if err != nil {
		t.Fatalf("Home fallback: %v", err)
	}
	if !strings.HasSuffix(h, "/.gridwell") {
		t.Errorf("fallback should be ~/.gridwell; got %q", h)
	}
}

func TestDBPaths(t *testing.T) {
	if got, want := DBDir("/home/x/.gridwell", "abc"), "/home/x/.gridwell/db/abc"; got != want {
		t.Errorf("DBDir: got %q, want %q", got, want)
	}
	if got, want := DBFile("/home/x/.gridwell", "abc"), "/home/x/.gridwell/db/abc/store.db"; got != want {
		t.Errorf("DBFile: got %q, want %q", got, want)
	}
}

func TestAppendPlugin(t *testing.T) {
	home := t.TempDir()
	a := PluginConfig{ID: "id-a", Name: "home", Kind: "home"}
	b := PluginConfig{ID: "id-b", Name: "files", Kind: "fs", Config: map[string]string{"root": "/srv"}}

	// First plugin bootstraps the file; second appends.
	if err := AppendPlugin(home, a); err != nil {
		t.Fatalf("append a: %v", err)
	}
	if err := AppendPlugin(home, b); err != nil {
		t.Fatalf("append b: %v", err)
	}

	cfg, err := Load(filepath.Join(home, "server.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Plugins) != 2 {
		t.Fatalf("plugins: got %d, want 2", len(cfg.Plugins))
	}
	// StaticDir must NOT be forced empty by the round-trip (would mean headless).
	if cfg.StaticDir != Defaults.StaticDir {
		t.Errorf("static dir clobbered: got %q, want %q", cfg.StaticDir, Defaults.StaticDir)
	}
	if cfg.Plugins[1].Config["root"] != "/srv" {
		t.Errorf("config map not persisted: %+v", cfg.Plugins[1])
	}

	// Duplicate id and duplicate name are both rejected.
	if err := AppendPlugin(home, PluginConfig{ID: "id-a", Name: "other", Kind: "home"}); !errors.Is(err, ErrDuplicatePlugin) {
		t.Errorf("dup id should be rejected: %v", err)
	}
	if err := AppendPlugin(home, PluginConfig{ID: "id-c", Name: "home", Kind: "home"}); !errors.Is(err, ErrDuplicatePlugin) {
		t.Errorf("dup name should be rejected: %v", err)
	}
}

// TestPasswordRoundTrip pins that a legacy `password:` line is PRESERVED
// across a config rewrite (never silently dropped) even though it is
// ignored since 2026-08-26 — the password is the web-password file.
func TestPasswordRoundTrip(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "server.yaml")
	if err := os.WriteFile(path, []byte("password: \"hunter2\"\nplugins:\n  - id: id-a\n    name: home\n    kind: localdb\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LegacyPassword != "hunter2" {
		t.Fatalf("password: got %q, want hunter2", cfg.LegacyPassword)
	}
	if err := AppendPlugin(home, PluginConfig{ID: "id-b", Name: "files", Kind: "fs"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := EnsureNodeID(home, func() string { return "n0deidx" }); err != nil {
		t.Fatalf("ensure node id: %v", err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.LegacyPassword != "hunter2" {
		t.Fatalf("password dropped by a config rewrite: got %q", cfg.LegacyPassword)
	}
}

func TestLoad_full(t *testing.T) {
	dir := t.TempDir()
	yml := `
bind: "127.0.0.1:9090"
static: "/var/www"
plugins:
  - id: "abc123"
    name: "home"
    kind: "home"
  - id: "def456"
    name: "files"
    kind: "fs"
    binary: "/usr/local/bin/gridwell-fs"
    config:
      root: "/home/joe"
`
	f := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(f, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Web.Bind != "127.0.0.1:9090" {
		t.Errorf("bind: got %q", cfg.Web.Bind)
	}
	if cfg.StaticDir != "/var/www" {
		t.Errorf("static: got %q", cfg.StaticDir)
	}
	if len(cfg.Plugins) != 2 {
		t.Fatalf("plugins: got %d, want 2", len(cfg.Plugins))
	}
	p := cfg.Plugins[0]
	if p.ID != "abc123" || p.Name != "home" || p.Kind != "home" {
		t.Errorf("plugin[0]: %+v", p)
	}
}

// TestLoad_rejectsBadIDs pins the identity-shape contract at the one config
// door (2026-07-25): a purely-numeric or slash-carrying plugin/node id would
// be indistinguishable from a tile id in URL paths and embed hrefs (or break
// the qualified-id codec), and once stored into cross-plugin references it
// can never be removed. gridwell init mints conforming ids; this catches the
// hand-edited file.
func TestLoad_rejectsBadIDs(t *testing.T) {
	write := func(t *testing.T, yml string) string {
		t.Helper()
		f := filepath.Join(t.TempDir(), "server.yaml")
		if err := os.WriteFile(f, []byte(yml), 0o600); err != nil {
			t.Fatal(err)
		}
		return f
	}
	bad := map[string]string{
		"numeric plugin id":  "plugins:\n  - id: \"12345\"\n    name: a\n    kind: localdb\n",
		"slash in plugin id": "plugins:\n  - id: \"abc/def\"\n    name: a\n    kind: localdb\n",
		"empty plugin id":    "plugins:\n  - name: a\n    kind: localdb\n",
		"numeric node id":    "node_id: \"777\"\nplugins:\n  - id: \"abc123\"\n    name: a\n    kind: localdb\n",
	}
	for name, yml := range bad {
		if _, err := Load(write(t, yml)); err == nil {
			t.Errorf("%s: Load accepted it", name)
		}
	}
	good := map[string]string{
		"short id":       "plugins:\n  - id: \"k3x9m2q\"\n    name: a\n    kind: localdb\n",
		"legacy hex id":  "plugins:\n  - id: \"0123456789abcdef0123456789abcdef\"\n    name: a\n    kind: localdb\n",
		"absent node id": "plugins:\n  - id: \"abc123\"\n    name: a\n    kind: localdb\n",
	}
	for name, yml := range good {
		if _, err := Load(write(t, yml)); err != nil {
			t.Errorf("%s: Load rejected it: %v", name, err)
		}
	}
}

func TestLoad_defaults_for_missing_fields(t *testing.T) {
	dir := t.TempDir()
	yml := `bind: "0.0.0.0:7070"`
	f := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(f, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Web.Bind != "0.0.0.0:7070" {
		t.Errorf("bind: got %q", cfg.Web.Bind)
	}
}

func TestLoad_tilde_expansion(t *testing.T) {
	dir := t.TempDir()
	yml := `
plugins:
  - id: "xyz"
    name: "files"
    kind: "fs"
    binary: "~/bin/gridwell-fs"
    config:
      root: "~/docs"
`
	f := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(f, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	home, _ := os.UserHomeDir()
	if !strings.HasPrefix(cfg.Plugins[0].Binary, home) {
		t.Errorf("binary tilde not expanded: %q", cfg.Plugins[0].Binary)
	}
	if !strings.HasPrefix(cfg.Plugins[0].Config["root"], home) {
		t.Errorf("config root tilde not expanded: %q", cfg.Plugins[0].Config["root"])
	}
}

func TestLoad_invalid_yaml(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(f, []byte("bind: [not a string]"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(f)
	if err == nil {
		t.Error("expected error for invalid yaml")
	}
}

// TestLoad_bindSet pins how "the user pinned the listen address" is detected:
// BindSet is true only when server.yaml actually contains a non-empty `bind:`
// key. This is what lets `serve --bind-default` (the desktop sidecar's
// ephemeral loopback port) fill in only when the config is silent — an
// explicit bind: equal to the built-in default must still count as set.
func TestLoad_bindSet(t *testing.T) {
	cases := []struct {
		name     string
		yml      string
		wantBind string
		wantSet  bool
	}{
		{"key absent", `static: "/var/www"`, Defaults.Web.Bind, false},
		{"key present", `bind: "100.64.0.7:8080"`, "100.64.0.7:8080", true},
		{"key present, equals built-in default", `bind: "127.0.0.1:8080"`, Defaults.Web.Bind, true},
		{"key present but empty", `bind: ""`, Defaults.Web.Bind, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "server.yaml")
			if err := os.WriteFile(f, []byte(c.yml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(f)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Web.Bind != c.wantBind {
				t.Errorf("Bind = %q, want %q", cfg.Web.Bind, c.wantBind)
			}
			if cfg.Web.BindSet != c.wantSet {
				t.Errorf("BindSet = %v, want %v", cfg.Web.BindSet, c.wantSet)
			}
		})
	}
}

// TestEnsureNodeID: mints once, persists, and never re-mints — the node id is
// durable identity, exactly like a plugin id.
func TestEnsureNodeID(t *testing.T) {
	home := t.TempDir()
	if err := AppendPlugin(home, PluginConfig{ID: "p1", Name: "e2e", Kind: "home"}); err != nil {
		t.Fatal(err)
	}
	n := 0
	mint := func() string { n++; return "node-minted" }
	id1, err := EnsureNodeID(home, mint)
	if err != nil {
		t.Fatalf("EnsureNodeID: %v", err)
	}
	if id1 != "node-minted" || n != 1 {
		t.Fatalf("first call = %q (mints %d), want node-minted (1)", id1, n)
	}
	// Persisted: a reload sees it, and a second Ensure never re-mints.
	cfg, err := Load(home + "/server.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeID != "node-minted" {
		t.Errorf("Load NodeID = %q, want node-minted", cfg.NodeID)
	}
	id2, err := EnsureNodeID(home, mint)
	if err != nil {
		t.Fatal(err)
	}
	if id2 != "node-minted" || n != 1 {
		t.Errorf("second call = %q (mints %d) — the id must never change", id2, n)
	}
	// The plugin list survived the rewrite.
	if len(cfg.Plugins) != 1 || cfg.Plugins[0].ID != "p1" {
		t.Errorf("plugins after node-id write = %+v", cfg.Plugins)
	}
}

// TestLoad_doors pins the grouped-by-door shape (owner decision
// 2026-08-26): web.bind / web.password and federation.socket load; the
// legacy flat bind: / password: still load, folded into the web door
// with a deprecation each; and a config REWRITE keeps a legacy file's
// keys (they are struct fields, not probe-only).
func TestLoad_doors(t *testing.T) {
	write := func(t *testing.T, yml string) string {
		t.Helper()
		f := filepath.Join(t.TempDir(), "server.yaml")
		if err := os.WriteFile(f, []byte(yml), 0o600); err != nil {
			t.Fatal(err)
		}
		return f
	}
	nested, err := Load(write(t, "web:\n  bind: \"100.64.0.7:8080\"\nfederation:\n  socket: /run/gw.sock\nplugins: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if nested.Web.Bind != "100.64.0.7:8080" || !nested.Web.BindSet ||
		nested.Federation.Socket != "/run/gw.sock" || len(nested.Deprecations) != 0 {
		t.Errorf("nested = %+v", nested)
	}
	// A yaml password (flat or nested) is IGNORED, with one notice: the
	// password is the web-password file.
	legacy, err := Load(write(t, "bind: \"100.64.0.7:8080\"\npassword: hunter2\nplugins: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Web.Bind != "100.64.0.7:8080" || !legacy.Web.BindSet || legacy.WebPassword != "" || len(legacy.Deprecations) != 2 {
		t.Errorf("legacy fold = %+v deprecations %v", legacy.Web, legacy.Deprecations)
	}
	tilde, err := Load(write(t, "federation:\n  socket: ~/gw.sock\nplugins: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if userHome, _ := os.UserHomeDir(); tilde.Federation.Socket != filepath.Join(userHome, "gw.sock") {
		t.Errorf("~ not expanded in federation.socket: %q", tilde.Federation.Socket)
	}
	silent, err := Load(write(t, "plugins: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if silent.Web.Bind != Defaults.Web.Bind || silent.Web.BindSet || silent.Federation.Socket != "" {
		t.Errorf("silent = web %+v federation %+v", silent.Web, silent.Federation)
	}
	// A rewrite (AppendPlugin re-marshals the struct) keeps a legacy
	// file's lines rather than silently dropping them.
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "server.yaml"), []byte("password: hunter2\nplugins: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendPlugin(home, PluginConfig{ID: "abc1234", Name: "a", Kind: "home"}); err != nil {
		t.Fatal(err)
	}
	after, err := Load(filepath.Join(home, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if after.LegacyPassword != "hunter2" {
		t.Errorf("legacy line lost across a rewrite: %+v", after)
	}
}

// TestEnsurePasswordFile: the web password is the web-password file
// (2026-08-26) — minted 0600 on first ask, stable while the file exists,
// rotated by deleting it.
func TestEnsurePasswordFile(t *testing.T) {
	home := t.TempDir()
	minted, err := EnsurePasswordFile(home)
	if err != nil || len(minted) != 32 {
		t.Fatalf("mint = %q %v", minted, err)
	}
	st, err := os.Stat(PasswordFile(home))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("password file mode = %v (%v), want 0600", st.Mode(), err)
	}
	if again, _ := EnsurePasswordFile(home); again != minted {
		t.Fatalf("re-minted while the file exists: %q vs %q", again, minted)
	}
	if err := os.Remove(PasswordFile(home)); err != nil {
		t.Fatal(err)
	}
	if rotated, _ := EnsurePasswordFile(home); rotated == minted {
		t.Fatal("deleting the file must rotate the password")
	}
	if err := os.WriteFile(PasswordFile(home), []byte("chosen\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := EnsurePasswordFile(home); got != "chosen" {
		t.Fatalf("a hand-written file is the password: %q", got)
	}
}

// TestConnectionsPresenceSurvivesRewrite: `connections: []` (present and
// empty = the transport is in config mode with no connections) must
// survive AppendPlugin / EnsureNodeID, which re-marshal the struct — a
// plain omitempty slice dropped the key (2026-08-27), silently flipping
// a home out of config mode on its next `gridwell init`.
func TestConnectionsPresenceSurvivesRewrite(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "server.yaml"), []byte("connections: []\nplugins: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendPlugin(home, PluginConfig{ID: "abc1234", Name: "a", Kind: "home"}); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureNodeID(home, func() string { return "nodeid1" }); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(filepath.Join(home, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if conns, set := cfg.ConnectionList(); !set || len(conns) != 0 {
		t.Fatalf("connections presence lost across rewrites: set=%v conns=%v", set, conns)
	}
	absent, err := Load(write(t, "plugins: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, set := absent.ConnectionList(); set {
		t.Fatal("an absent key must not read as present")
	}
}

func write(t *testing.T, yml string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "server.yaml")
	if err := os.WriteFile(f, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}
