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
	// Bind and BindDefault stay empty when not passed: unset is what lets
	// resolveBind fall through to the config or built-in default.
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
	// A positional ahead of --bind is shuffled so flag.Parse sees the flag.
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
// --bind (a hard override) > server.yaml bind: (explicitly present) >
// --bind-default (the desktop sidecar's ephemeral loopback fallback) > the
// built-in default. Explicitly present is config.Load's BindSet, a
// non-empty bind: key in the file, so a config bind equal to the built-in
// default still pins the address. TestLoad_bindSet covers the detection.
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

// TestServingBanner pins the sidecar boot contract that lines.ts parses:
// the web address leads, auth= is the derived token, and federation= is
// last, running to the closing paren, because the node door's socket path
// may contain spaces.
func TestServingBanner(t *testing.T) {
	got := servingBanner("127.0.0.1:8080", "/home/j o/.gridwell/federation.sock", "./web", 2, "hunter2")
	want := "gridwell: serving on 127.0.0.1:8080 (static=./web plugins=2 auth=" + server.AuthToken("hunter2") + " federation=/home/j o/.gridwell/federation.sock)"
	if got != want {
		t.Errorf("banner = %q, want %q", got, want)
	}
}

// A missing config is a fresh home: the node mints its id, writes the file,
// and creates the home store. There is no init step.
func TestBuildServeConfigFreshHome(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "server.yaml")
	cfg, err := buildServeConfig(home, path)
	if err != nil {
		t.Fatalf("fresh home: %v", err)
	}
	if cfg.ID == "" {
		t.Fatal("the node's id must be minted")
	}
	back, err := config.Load(path)
	if err != nil || back.ID != cfg.ID {
		t.Fatalf("the minted id must be written back: %v %+v", err, back)
	}
	if _, err := os.Stat(config.DBFile(home)); err != nil {
		t.Fatalf("the home store must exist after the first build: %v", err)
	}
	again, err := buildServeConfig(home, path)
	if err != nil || again.ID != cfg.ID {
		t.Fatalf("a second build must keep the id: %v %+v", err, again)
	}
}

// A plugin listed without an id gets one minted and written back; the
// node's database path is never stored.
func TestBuildServeConfigMintsPluginIDs(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "server.yaml")
	if err := os.WriteFile(path, []byte("plugins:\n  - kind: fs\n    config:\n      root: /tmp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := buildServeConfig(home, path)
	if err != nil {
		t.Fatalf("buildServeConfig: %v", err)
	}
	pid := cfg.Plugins[0].ID
	if pid == "" {
		t.Fatal("plugin id must be minted")
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "db_file") || strings.Contains(string(raw), "gridwell.db") {
		t.Errorf("the database path must never be stored:\n%s", raw)
	}
	if !strings.Contains(string(raw), pid) {
		t.Errorf("the minted plugin id must be written back:\n%s", raw)
	}
}

// TestBuildServeConfigChangedID is the guard against a silently new
// database: a home whose store is missing under its id while another store
// exists — the id was edited — must be a hard error, not a fresh empty
// store beside the real one.
func TestBuildServeConfigChangedID(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "server.yaml")
	if _, err := buildServeConfig(home, path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("id: changed1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildServeConfig(home, path); err == nil || !strings.Contains(err.Error(), "did `id` change") {
		t.Fatalf("a changed id must be refused, got %v", err)
	}
}
