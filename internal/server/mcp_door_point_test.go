package server

import (
	"slices"
	"testing"

	"github.com/josephburnett/gridwell/internal/mcpdoor"
)

// select sets a grid's selection claiming what read_grid gave, and read_grid
// answers it back; a claim another writer has moved past is refused and the
// set on disk stands.
func TestMCPSelectRoundTripsAndRefusesAStaleClaim(t *testing.T) {
	n := newMCPNode(t)
	s, err := n.agent(t, bearer(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	var a, b mcpdoor.TileOut
	call(t, s, "create_text", map[string]any{"grid": n.root, "body": "a"}, &a)
	call(t, s, "create_text", map[string]any{"grid": n.root, "body": "b"}, &b)
	var g mcpdoor.GridOut
	call(t, s, "read_grid", map[string]any{"id": n.root}, &g)
	if g.Grid.Selection == nil || len(g.Grid.Selection) != 0 {
		t.Fatalf("read_grid selection = %v, want an empty set", g.Grid.Selection)
	}
	read := g.Grid.Selection

	call(t, s, "select", map[string]any{"grid": n.root, "tiles": []string{a.Tile.ID, b.Tile.ID}, "claim": read}, &g)
	call(t, s, "read_grid", map[string]any{"id": n.root}, &g)
	if got := slices.Sorted(slices.Values(g.Grid.Selection)); !slices.Equal(got, slices.Sorted(slices.Values([]string{a.Tile.ID, b.Tile.ID}))) {
		t.Fatalf("read_grid selection = %v, want both tiles", got)
	}
	refusedWith(t, callRaw(t, s, "select", map[string]any{"grid": n.root, "tiles": []string{}, "claim": read}), "conflict:")
	call(t, s, "read_grid", map[string]any{"id": n.root}, &g)
	if len(g.Grid.Selection) != 2 {
		t.Fatalf("after a refused select, selection = %v, want the two kept", g.Grid.Selection)
	}
	call(t, s, "select", map[string]any{"grid": n.root, "tiles": []string{}}, &g)
	if len(g.Grid.Selection) != 0 {
		t.Fatalf("an unclaimed clear left %v, want it to claim the set on disk", g.Grid.Selection)
	}
}
