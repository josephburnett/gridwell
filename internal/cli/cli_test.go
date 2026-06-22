package cli

import (
	"os"
	"slices"
	"testing"
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

// noConfig is a sentinel that disables config file loading in tests.
const noConfig = ""

func TestParseServeFlagsDefaults(t *testing.T) {
	f, err := parseServeFlags(nil, noConfig)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.DB != "./gridwell.db" {
		t.Errorf("DB = %q, want default ./gridwell.db", f.DB)
	}
	if f.Bind != "127.0.0.1:8080" {
		t.Errorf("Bind = %q, want default 127.0.0.1:8080", f.Bind)
	}
	if f.StaticDir != "./web" {
		t.Errorf("StaticDir = %q, want default ./web", f.StaticDir)
	}
}

func TestParseServeFlagsOverrides(t *testing.T) {
	f, err := parseServeFlags([]string{
		"--db", "/tmp/x.db",
		"--bind", ":9000",
		"--static", "/srv/web",
	}, noConfig)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := serveFlags{
		DB:        "/tmp/x.db",
		Bind:      ":9000",
		StaticDir: "/srv/web",
		cfgPath:   noConfig,
	}
	if f != want {
		t.Errorf("got %+v, want %+v", f, want)
	}
}

func TestParseServeFlagsPositionalsReordered(t *testing.T) {
	// reorderFlagsFirst must shuffle a positional in front of a
	// `--db` so flag.Parse() sees the flag first.
	f, err := parseServeFlags([]string{"extra", "--db", "/tmp/x.db"}, noConfig)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.DB != "/tmp/x.db" {
		t.Errorf("DB = %q, want /tmp/x.db (positional should have been reordered)", f.DB)
	}
}

func TestParseServeFlagsRejectsUnknown(t *testing.T) {
	_, err := parseServeFlags([]string{"--no-such-flag"}, noConfig)
	if err == nil {
		t.Error("parse should reject unknown flags")
	}
}

func TestParseServeFlagsFromConfigFile(t *testing.T) {
	// Write a temp server.yaml and verify its values become defaults.
	dir := t.TempDir()
	cfgFile := dir + "/server.yaml"
	if err := os.WriteFile(cfgFile, []byte("bind: \"0.0.0.0:9090\"\ndb: \"/tmp/test.db\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := parseServeFlags(nil, cfgFile)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Bind != "0.0.0.0:9090" {
		t.Errorf("Bind = %q, want 0.0.0.0:9090 from config", f.Bind)
	}
	if f.DB != "/tmp/test.db" {
		t.Errorf("DB = %q, want /tmp/test.db from config", f.DB)
	}
}

func TestParseServeFlagsCliOverridesConfig(t *testing.T) {
	// CLI flags should override config file values.
	dir := t.TempDir()
	cfgFile := dir + "/server.yaml"
	if err := os.WriteFile(cfgFile, []byte("bind: \"0.0.0.0:9090\"\ndb: \"/tmp/test.db\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := parseServeFlags([]string{"--bind", ":8888"}, cfgFile)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Bind != ":8888" {
		t.Errorf("Bind = %q, want :8888 (CLI should override config)", f.Bind)
	}
	// DB not overridden — comes from config.
	if f.DB != "/tmp/test.db" {
		t.Errorf("DB = %q, want /tmp/test.db from config", f.DB)
	}
}
