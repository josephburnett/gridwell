package dragdrop

import (
	"slices"
	"testing"
)

func lookup(ms ...Member) func(string) (Member, bool) {
	return func(id string) (Member, bool) {
		for _, m := range ms {
			if m.ID == id {
				return m, true
			}
		}
		return Member{}, false
	}
}

func TestCarryOfSelectedTileCarriesTheSetGrabbedFirst(t *testing.T) {
	a := Member{ID: "a", X: 0, Y: 0, W: 1, H: 1}
	b := Member{ID: "b", X: 3, Y: 1, W: 2, H: 1}
	c := Member{ID: "c", X: 5, Y: 5, W: 1, H: 1}
	got := CarryOf(b, []string{"a", "b", "c"}, lookup(a, b, c))
	if !slices.Equal(got.IDs(), []string{"b", "a", "c"}) {
		t.Fatalf("carried %v, want the grabbed tile first then the rest of the set", got.IDs())
	}
}

func TestCarryOfUnselectedTileCarriesItAlone(t *testing.T) {
	a := Member{ID: "a", W: 1, H: 1}
	z := Member{ID: "z", X: 9, W: 1, H: 1}
	got := CarryOf(z, []string{"a"}, lookup(a, z))
	if !slices.Equal(got.IDs(), []string{"z"}) {
		t.Fatalf("carried %v, want the unselected tile alone", got.IDs())
	}
	if got := CarryOf(z, nil, lookup(a, z)); len(got) != 1 {
		t.Fatalf("no selection carried %v", got.IDs())
	}
}

func TestCarryOfSkipsASelectedIDTheGridDoesNotHold(t *testing.T) {
	a := Member{ID: "a", W: 1, H: 1}
	got := CarryOf(a, []string{"a", "gone"}, lookup(a))
	if !slices.Equal(got.IDs(), []string{"a"}) {
		t.Fatalf("carried %v", got.IDs())
	}
}

func TestCarryAtKeepsTheLayout(t *testing.T) {
	c := Carry{{ID: "g", X: 2, Y: 2, W: 1, H: 1}, {ID: "o", X: 4, Y: 1, W: 2, H: 3}}
	got := c.At(10, 20)
	want := Carry{{ID: "g", X: 10, Y: 20, W: 1, H: 1}, {ID: "o", X: 12, Y: 19, W: 2, H: 3}}
	if !slices.Equal(got, want) {
		t.Fatalf("At = %v, want %v", got, want)
	}
	if dx, dy := c.Offset(1); dx != 2 || dy != -1 {
		t.Fatalf("Offset = %d,%d", dx, dy)
	}
}

func TestCarryOccupied(t *testing.T) {
	// g at (0,0), o at (2,0); a stranger s at (5,0).
	g := Member{ID: "g", X: 0, Y: 0, W: 1, H: 1}
	o := Member{ID: "o", X: 2, Y: 0, W: 1, H: 1}
	s := Member{ID: "s", X: 5, Y: 0, W: 1, H: 1}
	c := Carry{g, o}
	grid := []Member{g, o, s}
	cases := []struct {
		name    string
		x, y    int64
		vacates bool
		want    bool
	}{
		{"a move one right lands on cells the set vacates", 1, 0, true, false},
		{"a move lands its follower on a stranger", 3, 0, true, true},
		{"a move lands its grabbed tile on a stranger", 5, 0, true, true},
		{"a move clear of everything", 0, 4, true, false},
		{"a move two right lands on a cell the set vacates", 2, 0, true, false},
		{"a copy two right lands on the set it copies", 2, 0, false, true},
		{"a copy clear of everything", 0, 4, false, false},
	}
	for _, tc := range cases {
		if got := c.Occupied(tc.x, tc.y, grid, tc.vacates); got != tc.want {
			t.Errorf("%s: Occupied = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCarryOccupiedOfOneIsTheSingleTileRule(t *testing.T) {
	g := Member{ID: "g", X: 0, Y: 0, W: 2, H: 2}
	n := Member{ID: "n", X: 3, Y: 0, W: 1, H: 1}
	c := Carry{g}
	if c.Occupied(1, 0, []Member{g, n}, true) {
		t.Fatal("a move overlapping only its own old cells is refused")
	}
	if !c.Occupied(2, 0, []Member{g, n}, true) {
		t.Fatal("a move onto a neighbor is allowed")
	}
	if !c.Occupied(1, 0, []Member{g, n}, false) {
		t.Fatal("a copy onto its source is allowed")
	}
}

// landsClear replays the placements in order against the grid as it stands
// after each, the way the node checks them.
func landsClear(c Carry, x, y int64, order []int) bool {
	at := slices.Clone(c)
	to := c.At(x, y)
	for _, i := range order {
		for j := range at {
			if j != i && RectsOverlap(to[i].X, to[i].Y, to[i].W, to[i].H, at[j].X, at[j].Y, at[j].W, at[j].H) {
				return false
			}
		}
		at[i] = to[i]
	}
	return true
}

func TestMoveOrderNeverLandsOnAMemberNotYetMoved(t *testing.T) {
	row := Carry{{ID: "a", X: 0, Y: 0, W: 1, H: 1}, {ID: "b", X: 1, Y: 0, W: 1, H: 1}, {ID: "c", X: 2, Y: 0, W: 1, H: 1}}
	block := Carry{{ID: "p", X: 0, Y: 0, W: 2, H: 1}, {ID: "q", X: 2, Y: 0, W: 1, H: 2},
		{ID: "r", X: 1, Y: 2, W: 2, H: 1}, {ID: "s", X: 0, Y: 1, W: 1, H: 2}}
	for _, set := range []Carry{row, block, slices.Concat(Carry{row[2]}, row[:2])} {
		for _, d := range [][2]int64{{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {-1, 1}, {2, -1}} {
			x, y := set[0].X+d[0], set[0].Y+d[1]
			order := set.MoveOrder(x, y)
			if len(order) != len(set) {
				t.Fatalf("%v by %v: order %v drops a member", set.IDs(), d, order)
			}
			if !landsClear(set, x, y, order) {
				t.Errorf("%v by %v: order %v lands on a member not yet moved", set.IDs(), d, order)
			}
		}
	}
}
