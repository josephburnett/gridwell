package menu

import "testing"

// Every arm ends either in a create the user asked for or in a snap-back that
// leaves the menu open, so a drop never half-lands.
func TestDropOn(t *testing.T) {
	cases := []struct {
		name string
		in   Release
		want Drop
	}{
		{"a release over no grid snaps back", Release{}, DropSnapBack},
		{"a release over occupied cells snaps back",
			Release{Target: true, Occupied: true, SameNode: true}, DropSnapBack},
		{"a healthy door over a writable grid links",
			Release{Target: true, Door: true, Enterable: true, AcceptsTiles: true}, DropLink},
		{"a door that cannot be entered snaps back",
			Release{Target: true, Door: true, AcceptsTiles: true}, DropSnapBack},
		{"a door over a read-only or uncached grid snaps back",
			Release{Target: true, Door: true, Enterable: true}, DropSnapBack},
		{"a primitive over another node's grid refuses",
			Release{Target: true, AcceptsTiles: true}, DropRefuse},
		{"a primitive over this node's grid creates",
			Release{Target: true, SameNode: true, AcceptsTiles: true}, DropCreate},
		{"the promote crumb over this node's grid promotes",
			Release{Target: true, SameNode: true, AcceptsTiles: true, Promote: true}, DropPromote},
		{"the promote crumb over another node's grid refuses",
			Release{Target: true, Promote: true}, DropRefuse},
	}
	for _, c := range cases {
		if got := DropOn(c.in); got != c.want {
			t.Errorf("%s: DropOn(%+v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

// A link is the one thing that may cross nodes, since a link out of a plugin
// is how the far grid is reached at all.
func TestDropOnDoorCrossesNodes(t *testing.T) {
	in := Release{Target: true, Door: true, Enterable: true, AcceptsTiles: true}
	if got := DropOn(in); got != DropLink {
		t.Fatalf("DropOn(door across nodes) = %v, want %v", got, DropLink)
	}
}

// A primitive carries no health and no writable check of its own: the swatch
// was already gated by the menu's node, and the create RPC is the authority.
func TestDropOnPrimitiveIgnoresDoorFacts(t *testing.T) {
	in := Release{Target: true, SameNode: true}
	if got := DropOn(in); got != DropCreate {
		t.Fatalf("DropOn(primitive, no door facts) = %v, want %v", got, DropCreate)
	}
}
