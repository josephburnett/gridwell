package shellconn

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Every row a pane opens keys on the session of the content row it resolves
// to, so a link to a clone shares the clone's source's session.
func TestSessionKeyTable(t *testing.T) {
	tile := &gridwellv1.Tile{Id: "a1", Kind: rpc.KindShell}
	clone := &gridwellv1.Tile{Id: "b1", Kind: rpc.KindShell, ShellSession: "a1"}
	far := &gridwellv1.Tile{Id: "far/c1", Kind: rpc.KindShell}
	farClone := &gridwellv1.Tile{Id: "far/d1", Kind: rpc.KindShell, ShellSession: "far/c1"}
	link := func(to string) *gridwellv1.Tile {
		return &gridwellv1.Tile{Id: "l1", Kind: rpc.KindShell, LinkTargetId: to}
	}
	cases := []struct {
		name        string
		row, target *gridwellv1.Tile
		want        string
		ok          bool
	}{
		{"a tile keys on itself", tile, nil, "a1", true},
		{"a clone keys on its source's session", clone, nil, "a1", true},
		{"a link to a tile keys on the tile", link("a1"), tile, "a1", true},
		{"a link to a clone keys on the source's session", link("b1"), clone, "a1", true},
		{"a link to a far tile keys on the far tile", link("far/c1"), far, "far/c1", true},
		{"a link to a far clone keys on the far source's session", link("far/d1"), farClone, "far/c1", true},
		{"a link whose target is not known keys on nothing", link("b1"), nil, "", false},
	}
	for _, c := range cases {
		got, ok := SessionKey(c.row, c.target)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: = (%q, %v), want (%q, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}
