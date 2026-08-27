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

// TestBindWarning pins the exposure warning: a non-loopback web bind with
// no password must produce a prominent notice (every byte is open on
// that interface); WITH a password there is nothing left to warn about
// — since 2026-08-26 the gRPC node export is its own loopback-only
// listener, so the old "the export on the same port is ungated" warning
// would be a false fact. Loopback never warns.
func TestBindWarning(t *testing.T) {
	loopback := []string{"127.0.0.1:8080", "127.1.2.3:9000", "[::1]:8080", "localhost:8080"}
	for _, addr := range loopback {
		for _, hasPW := range []bool{false, true} {
			if w := bindWarning(addr, hasPW); w != "" {
				t.Errorf("bindWarning(%q, %v) = %q, want none (loopback)", addr, hasPW, w)
			}
		}
	}
	exposed := []string{"0.0.0.0:8080", "[::]:8080", ":8080", "100.64.0.7:8080", "192.168.1.5:8080"}
	for _, addr := range exposed {
		w := bindWarning(addr, false)
		if w == "" {
			t.Errorf("bindWarning(%q, false) = none, want a warning (non-loopback)", addr)
			continue
		}
		if !strings.Contains(w, addr) || !strings.Contains(strings.ToLower(w), "unauthenticated") || !strings.Contains(w, "web.password") {
			t.Errorf("bindWarning(%q, false) should name the address, say the UI is unauthenticated, and name web.password; got %q", addr, w)
		}
		if wp := bindWarning(addr, true); wp != "" {
			t.Errorf("bindWarning(%q, true) = %q, want none: the web door is gated and the export is loopback-only", addr, wp)
		}
	}
}

// TestResolveFederationPort pins the node door's precedence — the same
// resolveSetting as the web bind, with -1 as unset so that 0 (an
// ephemeral port, what the sidecar asks for) is a real value.
func TestResolveFederationPort(t *testing.T) {
	def := config.Defaults.Federation.Port
	cases := []struct {
		name              string
		flag, cfg         int
		cfgSet            bool
		flagDefault, want int
	}{
		{"flag beats all", 9100, 9000, true, 0, 9100},
		{"flag zero is a real value", 0, 9000, true, 9200, 0},
		{"config beats the default twin", -1, 9000, true, 0, 9000},
		{"config equal to built-in still wins", -1, def, true, 0, def},
		{"default twin fills in when config is silent", -1, def, false, 0, 0},
		{"built-in when nothing is set", -1, def, false, -1, def},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveFederationPort(c.flag, c.cfg, c.cfgSet, c.flagDefault); got != c.want {
				t.Errorf("resolveFederationPort(%d, %d, %v, %d) = %d, want %d", c.flag, c.cfg, c.cfgSet, c.flagDefault, got, c.want)
			}
		})
	}
}

// TestServingBanner pins the sidecar boot contract (lines.ts parses this):
// the web address leads, federation= carries the node door's loopback
// address (the shell relay's dial target), and a configured password
// rides along as the derived auth token so the desktop window can
// authenticate without prompting.
func TestServingBanner(t *testing.T) {
	plain := servingBanner("127.0.0.1:8080", "127.0.0.1:8081", "./web", 2, "")
	if plain != "gridwell: serving on 127.0.0.1:8080 (static=./web plugins=2 federation=127.0.0.1:8081)" {
		t.Errorf("bare banner drifted: %q", plain)
	}
	withPW := servingBanner("127.0.0.1:8080", "127.0.0.1:8081", "./web", 2, "hunter2")
	want := "gridwell: serving on 127.0.0.1:8080 (static=./web plugins=2 federation=127.0.0.1:8081 auth=" + server.AuthToken("hunter2") + ")"
	if withPW != want {
		t.Errorf("auth banner = %q, want %q", withPW, want)
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
