package gridpath

import "testing"

func TestResolveLeafGrid(t *testing.T) {
	// World: root "1" --well "10"--> grid "2" --well "20"--> grid "3".
	lookup := func(gid, wellID string) (string, bool, bool) {
		switch {
		case gid == "1" && wellID == "10":
			return "2", true, true
		case gid == "2" && wellID == "20":
			return "3", true, true
		case gid == "2" && wellID == "99":
			return "", true, false // grid cached, tile missing
		case gid == "7":
			return "", false, false // grid not cached
		}
		return "", true, false
	}

	cases := []struct {
		name string
		root string
		path []string
		want string
	}{
		{"empty path -> root", "1", nil, "1"},
		{"one well", "1", []string{"10"}, "2"},
		{"two wells -> leaf", "1", []string{"10", "20"}, "3"},
		{"root empty -> empty", "", []string{"10"}, ""},
		{"missing tile stops at current grid", "1", []string{"10", "99"}, "2"},
		{"unknown well id stops at current grid", "1", []string{"10", "12345"}, "2"},
	}
	for _, c := range cases {
		if got := ResolveLeafGrid(c.root, c.path, lookup); got != c.want {
			t.Errorf("%s: ResolveLeafGrid = %q, want %q", c.name, got, c.want)
		}
	}

	// Uncached grid mid-walk stops there.
	if got := ResolveLeafGrid("7", []string{"1"}, lookup); got != "7" {
		t.Errorf("uncached grid: got %q want 7", got)
	}
}

func TestClassifyAscent(t *testing.T) {
	cases := []struct {
		name                   string
		resolvedLevel, pathLen int
		want                   AscentMode
	}{
		{"nothing resolved -> root", -1, 3, AscentToRoot},
		{"leaf resolved -> animate", 2, 3, AscentAnimate},
		{"skipped levels -> snap", 0, 3, AscentSnapToLevel},
		{"skipped one level -> snap", 1, 3, AscentSnapToLevel},
		{"single-level path, leaf resolved -> animate", 0, 1, AscentAnimate},
		{"single-level path, none -> root", -1, 1, AscentToRoot},
	}
	for _, c := range cases {
		if got := ClassifyAscent(c.resolvedLevel, c.pathLen); got != c.want {
			t.Errorf("%s: ClassifyAscent(%d,%d) = %v, want %v", c.name, c.resolvedLevel, c.pathLen, got, c.want)
		}
	}
}
