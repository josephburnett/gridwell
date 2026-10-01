package urlview

import "testing"

func TestGoneEndsOnlyTheViewItNames(t *testing.T) {
	var g Gens
	old := g.Next()
	placed := g.Next()
	if old == 0 || placed == old {
		t.Fatalf("minted %d then %d, want two distinct non-zero gens", old, placed)
	}
	cases := []struct {
		name       string
		held, gone Gen
		want       bool
	}{
		{"the view the event names", placed, placed, true},
		{"gone for an older view of the same tile and pane", placed, old, false},
		{"gone for a later view", old, placed, false},
		{"an event that names no view", placed, 0, false},
	}
	for _, c := range cases {
		if got := GoneEnds(c.held, c.gone); got != c.want {
			t.Errorf("%s: GoneEnds(%d, %d) = %v, want %v", c.name, c.held, c.gone, got, c.want)
		}
	}
}
