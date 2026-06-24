package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_missing(t *testing.T) {
	cfg, err := Load("/nonexistent/path/server.yaml")
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if cfg.Bind != Defaults.Bind {
		t.Errorf("bind: got %q, want %q", cfg.Bind, Defaults.Bind)
	}
}

func TestLoad_full(t *testing.T) {
	dir := t.TempDir()
	yml := `
bind: "127.0.0.1:9090"
root: "abc123"
static: "/var/www"
plugins:
  - id: "abc123"
    name: "home"
    kind: "localdb"
    config:
      db_file: "/tmp/home.db"
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
	if cfg.Bind != "127.0.0.1:9090" {
		t.Errorf("bind: got %q", cfg.Bind)
	}
	if cfg.StaticDir != "/var/www" {
		t.Errorf("static: got %q", cfg.StaticDir)
	}
	if len(cfg.Plugins) != 2 {
		t.Fatalf("plugins: got %d, want 2", len(cfg.Plugins))
	}
	p := cfg.Plugins[0]
	if p.ID != "abc123" || p.Kind != "localdb" || p.Config["db_file"] != "/tmp/home.db" {
		t.Errorf("plugin[0]: %+v", p)
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
	if cfg.Bind != "0.0.0.0:7070" {
		t.Errorf("bind: got %q", cfg.Bind)
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
