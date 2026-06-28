package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/internal/config"
)

func TestReorderFlagsFirst(t *testing.T) {
	takesValue := func(name string) bool {
		return name == "db" || name == "addr"
	}
	cases := []struct {
		in   []string
		want []string
	}{
		// Already in flag-first order: unchanged.
		{[]string{"--db", "/tmp/x.db", "alice"}, []string{"--db", "/tmp/x.db", "alice"}},
		// Positional first, then flag: reordered.
		{[]string{"alice", "--db", "/tmp/x.db"}, []string{"--db", "/tmp/x.db", "alice"}},
		// Mixed: each flag and its value stay together.
		{[]string{"alice", "--db", "/tmp/x.db", "--addr", ":9000"}, []string{"--db", "/tmp/x.db", "--addr", ":9000", "alice"}},
		// "--name=value" form: no separate value follows.
		{[]string{"alice", "--db=/tmp/x.db"}, []string{"--db=/tmp/x.db", "alice"}},
		// Bool flag (not in takesValue map): no value after it.
		{[]string{"--insecure", "alice"}, []string{"--insecure", "alice"}},
		// Two positional args, one flag in the middle.
		{[]string{"a", "--db", "/x", "b"}, []string{"--db", "/x", "a", "b"}},
	}
	for _, c := range cases {
		got := reorderFlagsFirst(c.in, takesValue)
		if !slices.Equal(got, c.want) {
			t.Errorf("reorderFlagsFirst(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseServeFlagsDefaults(t *testing.T) {
	f, err := parseServeFlags(nil, "127.0.0.1:8080", "./web")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Bind != "127.0.0.1:8080" {
		t.Errorf("Bind = %q, want default", f.Bind)
	}
	if f.StaticDir != "./web" {
		t.Errorf("StaticDir = %q, want default", f.StaticDir)
	}
}

func TestParseServeFlagsOverrides(t *testing.T) {
	f, err := parseServeFlags([]string{"--bind", ":9000", "--static", "/srv/web"}, "127.0.0.1:8080", "./web")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Bind != ":9000" || f.StaticDir != "/srv/web" {
		t.Errorf("got %+v, want bind=:9000 static=/srv/web", f)
	}
}

func TestParseServeFlagsPositionalsReordered(t *testing.T) {
	// A positional ahead of --bind must be shuffled so flag.Parse sees the flag.
	f, err := parseServeFlags([]string{"extra", "--bind", ":9000"}, "127.0.0.1:8080", "./web")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Bind != ":9000" {
		t.Errorf("Bind = %q, want :9000 (positional should have been reordered)", f.Bind)
	}
}

func TestParseServeFlagsRejectsUnknown(t *testing.T) {
	if _, err := parseServeFlags([]string{"--no-such-flag"}, "127.0.0.1:8080", "./web"); err == nil {
		t.Error("parse should reject unknown flags")
	}
}

func TestBuildServeConfigMissingFile(t *testing.T) {
	home := t.TempDir()
	_, err := buildServeConfig(home, filepath.Join(home, "server.yaml"))
	if err == nil {
		t.Fatal("a missing config must be an error (no synthesized fallback)")
	}
	if !strings.Contains(err.Error(), "gridwell init") {
		t.Errorf("error should guide the user to `gridwell init`; got: %v", err)
	}
}

func TestBuildServeConfigNoPlugins(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "server.yaml")
	if err := os.WriteFile(path, []byte("bind: \"127.0.0.1:9090\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildServeConfig(home, path); err == nil {
		t.Fatal("a config with no plugins must be an error")
	}
}

func TestBuildServeConfigInjectsDBFile(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "server.yaml")
	yml := "plugins:\n  - id: \"abc\"\n    name: \"home\"\n    kind: \"localdb\"\n"
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	// The DB must already exist (created by `gridwell init`); fake one.
	want := config.DBFile(home, "abc")
	if err := os.MkdirAll(config.DBDir(home, "abc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := buildServeConfig(home, path)
	if err != nil {
		t.Fatalf("buildServeConfig: %v", err)
	}
	if got := cfg.Plugins[0].Config["db_file"]; got != want {
		t.Errorf("db_file = %q, want derived %q", got, want)
	}
}

// TestBuildServeConfigMissingDB is the regression guard for the silent-new-DB
// hole: a config entry whose DB does not exist (e.g. its id was changed) must
// be a hard error, not a fresh empty store.
func TestBuildServeConfigMissingDB(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "server.yaml")
	yml := "plugins:\n  - id: \"abc\"\n    name: \"home\"\n    kind: \"localdb\"\n"
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildServeConfig(home, path); err == nil {
		t.Fatal("a plugin whose DB does not exist must be rejected")
	}
}
