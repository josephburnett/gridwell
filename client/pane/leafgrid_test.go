package pane

import "testing"

func TestResolveLeafGrid(t *testing.T) {
	// Root grid "1" holds well "10" over grid "2", which holds well "20" over
	// grid "3".
	lookup := func(gid, wellID string) (string, bool, bool) {
		switch {
		case gid == "1" && wellID == "10":
			return "2", true, true
		case gid == "2" && wellID == "20":
			return "3", true, true
		case gid == "2" && wellID == "99":
			return "", true, false // cached, tile missing
		case gid == "7":
			return "", false, false // not cached
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

	if got := ResolveLeafGrid("7", []string{"1"}, lookup); got != "7" {
		t.Errorf("uncached grid: got %q want 7", got)
	}
}
