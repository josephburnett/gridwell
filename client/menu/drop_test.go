package menu

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/dragdrop"
)

var (
	opensRow   = &gridwellv1.SourceInfo{Uuid: "h", Label: "home", RootGridId: "h/1"}
	brokenRow  = rpc.ConnectionRow("c1", "rtb", "", "dial tcp: connection refused", rpc.View{})
	waitingRow = rpc.ConnectionRow("c2", "mac", "", "", rpc.View{})
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
			Release{Target: true, Door: opensRow, AcceptsTiles: true, TilesKnown: true}, DropLink, ""},
		{"a broken door says what its source answered",
			Release{Target: true, Door: brokenRow, AcceptsTiles: true, TilesKnown: true}, DropSnapBack,
			"rtb: dial tcp: connection refused"},
		{"a waiting door says it is still loading, over a grid not yet read too",
			Release{Target: true, Door: waitingRow}, DropSnapBack,
			"loading mac — it will open once the connection answers"},
		{"a door over a grid that takes no new tiles is refused",
			Release{Target: true, Door: opensRow, TilesKnown: true}, DropSnapBack, dragdrop.TakesNoTiles},
		{"a door over an uncached grid snaps back",
			Release{Target: true, Door: opensRow}, DropSnapBack, ""},
		{"a primitive over another node's grid is refused",
			Release{Target: true, AcceptsTiles: true, TilesKnown: true}, DropSnapBack, OtherNode},
		{"a primitive over a grid that takes no new tiles is refused",
			Release{Target: true, SameNode: true, TilesKnown: true}, DropSnapBack, dragdrop.TakesNoTiles},
		{"a primitive over this node's grid creates",
			Release{Target: true, SameNode: true, AcceptsTiles: true, TilesKnown: true}, DropCreate, ""},
		{"a file or directory over a writable directory creates",
			Release{Target: true, SameNode: true, AcceptsTiles: true, TilesKnown: true, Disk: true, Directory: true, DiskWritable: true}, DropCreate, ""},
		{"a file or directory over a directory that cannot be written is refused",
			Release{Target: true, SameNode: true, AcceptsTiles: true, TilesKnown: true, Disk: true, Directory: true}, DropSnapBack, DiskReadOnly},
		{"a file or directory over a Gridwell grid is refused",
			Release{Target: true, SameNode: true, AcceptsTiles: true, TilesKnown: true, Disk: true}, DropSnapBack, DiskNeedsDirectory},
		{"a file or directory over a taken cell of a Gridwell grid says where a file is made",
			Release{Target: true, Occupied: true, SameNode: true, AcceptsTiles: true, TilesKnown: true, Disk: true}, DropSnapBack, DiskNeedsDirectory},
		{"a file or directory over no grid says nothing",
			Release{Disk: true}, DropSnapBack, ""},
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
	in := Release{Target: true, Door: opensRow, AcceptsTiles: true, TilesKnown: true}
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
