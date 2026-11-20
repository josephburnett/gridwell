package menu

import (
	"testing"

	"github.com/josephburnett/gridwell/client/dragdrop"
)

// Every arm ends either in a create the user asked for or in a snap-back that
// leaves the menu open, so a drop never half-lands; a snap-back that refuses
// what the drop asked says why, in the words a tile's drag uses (Joe,
// 2026-11-19), and one with no reason the client holds says nothing.
func TestDropOn(t *testing.T) {
	cases := []struct {
		name string
		in   Release
		want Drop
		why  string
	}{
		{"a release over no grid snaps back", Release{}, DropSnapBack, ""},
		{"a release over taken cells is refused",
			Release{Target: true, Occupied: true, SameNode: true}, DropSnapBack, dragdrop.CellTaken},
		{"a healthy door over a writable grid links",
			Release{Target: true, Door: true, Enterable: true, AcceptsTiles: true, TilesKnown: true}, DropLink, ""},
		{"a door that cannot be entered snaps back",
			Release{Target: true, Door: true, AcceptsTiles: true, TilesKnown: true}, DropSnapBack, ""},
		{"a door over a grid that takes no new tiles is refused",
			Release{Target: true, Door: true, Enterable: true, TilesKnown: true}, DropSnapBack, dragdrop.TakesNoTiles},
		{"a door over an uncached grid snaps back",
			Release{Target: true, Door: true, Enterable: true}, DropSnapBack, ""},
		{"a primitive over another node's grid is refused",
			Release{Target: true, AcceptsTiles: true, TilesKnown: true}, DropSnapBack, OtherNode},
		{"a primitive over a grid that takes no new tiles is refused",
			Release{Target: true, SameNode: true, TilesKnown: true}, DropSnapBack, dragdrop.TakesNoTiles},
		{"a primitive over this node's grid creates",
			Release{Target: true, SameNode: true, AcceptsTiles: true, TilesKnown: true}, DropCreate, ""},
		{"a file or directory over a writable directory creates",
			Release{Target: true, SameNode: true, AcceptsTiles: true, TilesKnown: true, Disk: true, DiskWritable: true}, DropCreate, ""},
		{"a file or directory over a home grid or a read-only directory snaps back",
			Release{Target: true, SameNode: true, AcceptsTiles: true, TilesKnown: true, Disk: true}, DropSnapBack, ""},
	}
	for _, c := range cases {
		if got, why := DropOn(c.in); got != c.want || why != c.why {
			t.Errorf("%s: DropOn(%+v) = %v %q, want %v %q", c.name, c.in, got, why, c.want, c.why)
		}
	}
}

// A link is the one thing that may cross nodes, since a link out of a source
// is how the far grid is reached at all.
func TestDropOnDoorCrossesNodes(t *testing.T) {
	in := Release{Target: true, Door: true, Enterable: true, AcceptsTiles: true, TilesKnown: true}
	if got, _ := DropOn(in); got != DropLink {
		t.Fatalf("DropOn(door across nodes) = %v, want %v", got, DropLink)
	}
}

// A primitive carries no health of its own, and a grid not yet read is not
// refused: the swatch was already gated by the menu's node, and the create
// RPC is the authority.
func TestDropOnPrimitiveIgnoresDoorFacts(t *testing.T) {
	in := Release{Target: true, SameNode: true}
	if got, _ := DropOn(in); got != DropCreate {
		t.Fatalf("DropOn(primitive, no door facts) = %v, want %v", got, DropCreate)
	}
}
