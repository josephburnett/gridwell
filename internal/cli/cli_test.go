package cli

import (
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

func TestParseServeFlagsDefaults(t *testing.T) {
	f, err := parseServeFlags(nil)
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
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := serveFlags{
		DB:        "/tmp/x.db",
		Bind:      ":9000",
		StaticDir: "/srv/web",
	}
	if f != want {
		t.Errorf("got %+v, want %+v", f, want)
	}
}

func TestParseServeFlagsPositionalsReordered(t *testing.T) {
	// reorderFlagsFirst must shuffle a positional in front of a
	// `--db` so flag.Parse() sees the flag first.
	f, err := parseServeFlags([]string{"extra", "--db", "/tmp/x.db"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.DB != "/tmp/x.db" {
		t.Errorf("DB = %q, want /tmp/x.db (positional should have been reordered)", f.DB)
	}
}

func TestParseServeFlagsRejectsUnknown(t *testing.T) {
	_, err := parseServeFlags([]string{"--no-such-flag"})
	if err == nil {
		t.Error("parse should reject unknown flags")
	}
}
