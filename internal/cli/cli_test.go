package cli

import (
	"runtime"
	"slices"
	"sort"
	"testing"

	"github.com/josephburnett/gridwell/internal/urldriver"
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

func TestSortedBrands(t *testing.T) {
	got := sortedBrands()
	if !sort.StringsAreSorted(got) {
		t.Errorf("sortedBrands not sorted: %v", got)
	}
	// Must include every registered brand.
	if len(got) != len(urldriver.BrandNames()) {
		t.Errorf("sortedBrands length = %d, want %d", len(got), len(urldriver.BrandNames()))
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
	if f.BrowserName != "chromium" {
		t.Errorf("BrowserName = %q, want default chromium", f.BrowserName)
	}
	if f.XvfbResolution != "2560x1600" {
		t.Errorf("XvfbResolution = %q, want default 2560x1600", f.XvfbResolution)
	}
	// Xvfb is Linux-only; off Linux, --no-xvfb and --headless auto-default
	// to true so `gridwell serve` runs without flags on macOS / *BSD.
	wantNoXvfb := runtime.GOOS != "linux"
	if f.NoXvfb != wantNoXvfb {
		t.Errorf("NoXvfb default = %v, want %v on %s", f.NoXvfb, wantNoXvfb, runtime.GOOS)
	}
	if f.Headless != wantNoXvfb {
		t.Errorf("Headless default = %v, want %v on %s", f.Headless, wantNoXvfb, runtime.GOOS)
	}
}

func TestParseServeFlagsOverrides(t *testing.T) {
	f, err := parseServeFlags([]string{
		"--db", "/tmp/x.db",
		"--bind", ":9000",
		"--static", "/srv/web",
		"--browser", "brave",
		"--browser-bin", "/usr/bin/brave",
		"--xvfb-resolution", "1280x720",
		"--no-xvfb",
		"--headless",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := serveFlags{
		DB:             "/tmp/x.db",
		Bind:           ":9000",
		StaticDir:      "/srv/web",
		BrowserName:    "brave",
		BrowserBin:     "/usr/bin/brave",
		XvfbResolution: "1280x720",
		NoXvfb:         true,
		Headless:       true,
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

func TestParseResolution(t *testing.T) {
	cases := []struct {
		in      string
		w, h    int
		wantErr bool
	}{
		{"1280x720", 1280, 720, false},
		{"800x600", 800, 600, false},
		{"1x1", 1, 1, false},
		{"", 0, 0, true},
		{"abc", 0, 0, true},
		{"1280", 0, 0, true},
		{"x720", 0, 0, true},
		{"1280x", 0, 0, true},
		{"-1x720", 0, 0, true},
		{"1280x0", 0, 0, true},
		{"1280xabc", 0, 0, true},
	}
	for _, c := range cases {
		w, h, err := parseResolution(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseResolution(%q) err = %v, wantErr = %v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && (w != c.w || h != c.h) {
			t.Errorf("parseResolution(%q) = (%d, %d), want (%d, %d)", c.in, w, h, c.w, c.h)
		}
	}
}


