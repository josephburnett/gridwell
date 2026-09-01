package plugin

// The config map a plugin is spawned with, pinned from inside the package:
// what the loader hands the guest, without a subprocess. The spawn itself is
// crossed in plugin_e2e_test.go, over the real binary.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/internal/config"
)

// TestSpawnConfig_StateDir pins the private directory: minted 0700 at
// <home>/plugins/<id> and named to the plugin as state_dir, beside uuid and
// kind, with the plugin's own keys untouched.
func TestSpawnConfig_StateDir(t *testing.T) {
	home := t.TempDir()
	pc := &config.PluginConfig{ID: "p1abcde", Kind: "fs", Config: map[string]string{"root": "/srv"}}

	cfg, err := spawnConfig(pc, home)
	if err != nil {
		t.Fatalf("spawnConfig: %v", err)
	}
	want := filepath.Join(home, "plugins", "p1abcde")
	if cfg["state_dir"] != want {
		t.Errorf("state_dir = %q, want %q", cfg["state_dir"], want)
	}
	if cfg["uuid"] != "p1abcde" || cfg["kind"] != "fs" || cfg["root"] != "/srv" {
		t.Errorf("config map lost a key: %v", cfg)
	}
	fi, err := os.Stat(want)
	if err != nil {
		t.Fatalf("state dir not minted: %v", err)
	}
	if !fi.IsDir() {
		t.Fatalf("%s is not a directory", want)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("state dir mode = %04o, want 0700", perm)
	}
	// The plugin's config map is its own: nothing was written back into the
	// server.yaml entry.
	if _, ok := pc.Config["state_dir"]; ok {
		t.Error("state_dir leaked into the config entry")
	}
}

// TestSpawnConfig_KeepsWhatIsThere pins that a second load leaves an existing
// state directory and its contents alone. Nothing auto-deletes one.
func TestSpawnConfig_KeepsWhatIsThere(t *testing.T) {
	home := t.TempDir()
	pc := &config.PluginConfig{ID: "p1abcde", Kind: "fs"}

	first, err := spawnConfig(pc, home)
	if err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(first["state_dir"], "todos.json")
	if err := os.WriteFile(kept, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := spawnConfig(pc, home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("a reload lost the plugin's own file: %v", err)
	}
}

// TestSpawnConfig_NoHome pins that an empty home is an error the launch
// carries, never a relative directory written wherever the node started.
func TestSpawnConfig_NoHome(t *testing.T) {
	if _, err := spawnConfig(&config.PluginConfig{ID: "p1abcde", Kind: "fs"}, ""); err == nil {
		t.Fatal("spawnConfig with no home = nil error, want a refusal")
	}
	if _, err := os.Stat(filepath.Join("plugins", "p1abcde")); err == nil {
		t.Fatal("a state dir was minted relative to the working directory")
	}
}
