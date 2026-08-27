package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/server"
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
	f, err := parseServeFlags(nil, "./web")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Bind and BindDefault stay empty when not passed — "unset" is what lets
	// resolveBind fall through to the config / built-in default.
	if f.Bind != "" || f.BindDefault != "" {
		t.Errorf("Bind = %q, BindDefault = %q, want both empty", f.Bind, f.BindDefault)
	}
	if f.StaticDir != "./web" {
		t.Errorf("StaticDir = %q, want default", f.StaticDir)
	}
}

func TestParseServeFlagsOverrides(t *testing.T) {
	f, err := parseServeFlags([]string{"--bind", ":9000", "--bind-default", "127.0.0.1:41000", "--static", "/srv/web"}, "./web")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Bind != ":9000" || f.BindDefault != "127.0.0.1:41000" || f.StaticDir != "/srv/web" {
		t.Errorf("got %+v, want bind=:9000 bind-default=127.0.0.1:41000 static=/srv/web", f)
	}
}

func TestParseServeFlagsPositionalsReordered(t *testing.T) {
	// A positional ahead of --bind must be shuffled so flag.Parse sees the flag.
	f, err := parseServeFlags([]string{"extra", "--bind", ":9000"}, "./web")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Bind != ":9000" {
		t.Errorf("Bind = %q, want :9000 (positional should have been reordered)", f.Bind)
	}
}

func TestParseServeFlagsRejectsUnknown(t *testing.T) {
	if _, err := parseServeFlags([]string{"--no-such-flag"}, "./web"); err == nil {
		t.Error("parse should reject unknown flags")
	}
}

// TestResolveBind pins the one owner of the listen-address decision:
// --bind (hard override) > server.yaml bind: (explicitly present) >
// --bind-default (the desktop sidecar's ephemeral loopback fallback) >
// the built-in default. "Explicitly present" is config.Load's BindSet — a
// non-empty bind: key in the file — so a config bind equal to the built-in
// default still pins the address (TestLoad_bindSet covers the detection).
func TestResolveBind(t *testing.T) {
	const def = "127.0.0.1:8080" // config.Defaults.Web.Bind
	cases := []struct {
		name          string
		flagBind      string
		configBind    string
		configBindSet bool
		bindDefault   string
		want          string
	}{
		{"flag beats config and bind-default", ":9000", "100.64.0.7:8080", true, "127.0.0.1:41000", ":9000"},
		{"config bind beats bind-default", "", "100.64.0.7:8080", true, "127.0.0.1:41000", "100.64.0.7:8080"},
		{"config bind equal to built-in default still wins", "", def, true, "127.0.0.1:41000", def},
		{"bind-default fills in when config is silent", "", def, false, "127.0.0.1:41000", "127.0.0.1:41000"},
		{"built-in default when nothing is set", "", def, false, "", def},
		{"flag alone", ":9000", def, false, "", ":9000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveBind(c.flagBind, c.configBind, c.configBindSet, c.bindDefault)
			if got != c.want {
				t.Errorf("resolveBind(%q, %q, %v, %q) = %q, want %q",
					c.flagBind, c.configBind, c.configBindSet, c.bindDefault, got, c.want)
			}
		})
	}
}

// TestServingBanner pins the sidecar boot contract (lines.ts parses this):
// the web address leads, auth= is the derived token (a password is always
// configured), and federation= is LAST, running to the closing paren —
// the node door's socket path, which may contain spaces.
func TestServingBanner(t *testing.T) {
	got := servingBanner("127.0.0.1:8080", "/home/j o/.gridwell/federation.sock", "./web", 2, "hunter2")
	want := "gridwell: serving on 127.0.0.1:8080 (static=./web plugins=2 auth=" + server.AuthToken("hunter2") + " federation=/home/j o/.gridwell/federation.sock)"
	if got != want {
		t.Errorf("banner = %q, want %q", got, want)
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
	yml := "web:\n  password: \"pw\"\nplugins:\n  - id: \"abc\"\n    name: \"home\"\n    kind: \"localdb\"\n"
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
	yml := "web:\n  password: \"pw\"\nplugins:\n  - id: \"abc\"\n    name: \"home\"\n    kind: \"localdb\"\n"
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildServeConfig(home, path); err == nil {
		t.Fatal("a plugin whose DB does not exist must be rejected")
	}
}
