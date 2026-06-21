package gridpath

import "testing"

func TestResolveLeafGrid(t *testing.T) {
	// World: root 1 --well 10--> grid 2 --well 20--> grid 3.
	lookup := func(gid, wellID int64) (int64, bool, bool) {
		switch {
		case gid == 1 && wellID == 10:
			return 2, true, true
		case gid == 2 && wellID == 20:
			return 3, true, true
		case gid == 2 && wellID == 99:
			return 0, true, false // grid cached, tile missing
		case gid == 7:
			return 0, false, false // grid not cached
		}
		return 0, true, false
	}

	cases := []struct {
		name string
		root int64
		path []int64
		want int64
	}{
		{"empty path -> root", 1, nil, 1},
		{"one well", 1, []int64{10}, 2},
		{"two wells -> leaf", 1, []int64{10, 20}, 3},
		{"root 0 -> 0", 0, []int64{10}, 0},
		{"missing tile stops at current grid", 1, []int64{10, 99}, 2},
		{"unknown well id stops at current grid", 1, []int64{10, 12345}, 2},
	}
	for _, c := range cases {
		if got := ResolveLeafGrid(c.root, c.path, lookup); got != c.want {
			t.Errorf("%s: ResolveLeafGrid = %d, want %d", c.name, got, c.want)
		}
	}

	// Uncached grid mid-walk stops there.
	if got := ResolveLeafGrid(7, []int64{1}, lookup); got != 7 {
		t.Errorf("uncached grid: got %d want 7", got)
	}
}

func TestClassifyAscent(t *testing.T) {
	cases := []struct {
		name              string
		resolvedLevel, pathLen int
		want              AscentMode
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
