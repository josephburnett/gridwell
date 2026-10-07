package contentrow

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

func TestOf(t *testing.T) {
	// A hey box's link to a thread in everything: the link row carries the
	// kind and the name, the target the page.
	link := &gridwellv1.Tile{Id: "uh1/~box", Kind: rpc.KindURL, AltText: "Wren: Kite day",
		Reference: true, LinkTargetId: "uh1/~thread"}
	target := &gridwellv1.Tile{Id: "uh1/~thread", Kind: rpc.KindURL, ServesPage: true}
	home := &gridwellv1.Tile{Id: "5", Kind: rpc.KindURL, UrlString: "https://example.com"}
	none := func(string) *gridwellv1.Tile { return nil }
	holds := func(id string) *gridwellv1.Tile {
		if id == target.Id {
			return target
		}
		return nil
	}

	for _, c := range []struct {
		name   string
		t      *gridwellv1.Tile
		cached func(string) *gridwellv1.Tile
		dead   bool
		row    *gridwellv1.Tile
		state  State
		ask    string
	}{
		{"a content row is its own", home, none, false, home, Ready, ""},
		{"a link with its target cached reads the target", link, holds, false, target, Ready, ""},
		{"a link with its target uncached asks for it", link, none, false, nil, Pending, "uh1/~thread"},
		{"a dead link has nothing to read and asks nothing", link, holds, true, nil, Dead, ""},
		{"a row that has not landed is pending, with nothing to ask", nil, holds, false, nil, Pending, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			row, s := Of(c.t, c.cached, c.dead)
			if row != c.row || s != c.state {
				t.Errorf("Of = %v, %v; want %v, %v", row, s, c.row, c.state)
			}
			if got := Ask(c.t, s); got != c.ask {
				t.Errorf("Ask = %q, want %q", got, c.ask)
			}
		})
	}
}
