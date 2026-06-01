package cli

import (
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


