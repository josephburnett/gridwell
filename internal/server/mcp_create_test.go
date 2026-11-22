package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/mcpapi"
)

// create makes the text, url, door and workspace tiles the + menu makes, each
// as its swatch's create does, at the first free cell or at or below near, and a door answers the
// grid it holds, which the next write can name.
func TestMCPCreateMakesTextUrlDoorAndWorkspaceTiles(t *testing.T) {
	n := newMCPNode(t)
	s, err := n.agent(t, bearer(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	made := items(t, s, "create",
		map[string]any{"kind": "door", "grid": n.root},
		map[string]any{"kind": "door", "grid": n.root, "name": "inbox", "near": map[string]any{"x": 4, "y": 2}},
		map[string]any{"kind": "url", "grid": n.root, "address": "example.com/a"},
		map[string]any{"kind": "url", "grid": n.root, "address": "http://example.org", "name": "docs"},
		map[string]any{"kind": "text", "grid": n.root, "body": "# plan\nfirst", "near": map[string]any{"x": 4, "y": 2}},
		map[string]any{"kind": "workspace", "grid": n.root, "name": "work"})
	door, inbox, bare, docs := landed(t, made[0]), landed(t, made[1]), landed(t, made[2]), landed(t, made[3])
	text, work := landed(t, made[4]), landed(t, made[5])

	if door.Kind != rpc.KindDoor || door.Name != "" || door.ChildGrid == "" || door.X != 0 || door.Y != 0 || door.W != 1 || door.H != 1 {
		t.Fatalf("a bare door = %+v, want an unnamed 1x1 door at the first free cell holding its grid", door)
	}
	if inbox.Name != "inbox" || inbox.X != 4 || inbox.Y != 2 || inbox.ChildGrid == door.ChildGrid {
		t.Fatalf("a named door near (4,2) = %+v, want inbox at (4,2) holding a grid of its own", inbox)
	}
	if text.Kind != rpc.KindText || text.Name != "plan" || text.X != 4 || text.Y != 3 {
		t.Fatalf("a text near a taken cell = %+v, want plan below it at (4,3)", text)
	}
	if bare.Kind != rpc.KindURL || bare.Name != "" || readOne(t, s, bare.ID).URL != "https://example.com/a" {
		t.Fatalf("a url with no name = %+v at %q, want unnamed at https://example.com/a", bare, readOne(t, s, bare.ID).URL)
	}
	if docs.Name != "docs" || readOne(t, s, docs.ID).URL != "http://example.org" {
		t.Fatalf("a named url = %+v, want docs at http://example.org", docs)
	}
	if work.Kind != rpc.KindWorkspace || work.Name != "work" {
		t.Fatalf("the workspace = %+v, want one named work", work)
	}
	if r := readOne(t, s, work.ID); r.Layout == nil || len(r.Layout.Panes) != 0 {
		t.Fatalf("the workspace = %+v, want it never arranged", r.Layout)
	}

	inside := mkText(t, s, door.ChildGrid, "inside")
	if inside.Grid != door.ChildGrid {
		t.Fatalf("a text written into the door's grid landed on %s, want %s", inside.Grid, door.ChildGrid)
	}
	var g mcpapi.GridOut
	call(t, s, "read_grid", map[string]any{"id": door.ChildGrid}, &g)
	if len(g.Tiles) != 1 || g.Tiles[0].ID != inside.ID {
		t.Fatalf("the door's grid holds %+v, want the one text", g.Tiles)
	}
}

// Each item is its own create: a refused one says why and stops nothing
// after it.
func TestAnMCPCreateRefusesAnItemWithTheReasonAndLandsTheRest(t *testing.T) {
	n := newMCPNode(t)
	s, err := n.agent(t, bearer(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	got := items(t, s, "create",
		map[string]any{"kind": "panes", "grid": n.root},
		map[string]any{"kind": "door", "grid": n.root},
		map[string]any{"kind": "url", "grid": n.root},
		map[string]any{"kind": "url", "grid": n.root, "address": "ftp://example.com"},
		map[string]any{"kind": "door", "grid": n.root, "address": "https://example.com"},
		map[string]any{"kind": "text", "grid": n.root, "name": "x"},
		map[string]any{"kind": "url", "grid": n.root, "body": "x", "address": "https://example.com"},
		map[string]any{"kind": "door", "grid": ""},
		map[string]any{"kind": "shell", "grid": n.root},
		map[string]any{"kind": "file", "grid": n.root, "name": "a.md"},
		map[string]any{"kind": "directory", "grid": n.root, "name": "d"})
	verdicts(t, "create", got, "refused:", "ok", "refused:", "refused:", "refused:", "refused:", "refused:", "refused:", "refused:", "refused:", "refused:")
	for i, want := range map[int]string{
		0: `not "panes"`, 2: "please enter a URL", 3: "only http and https", 4: "only a url holds an address",
		5: "first line", 6: "only a text is born with a body", 7: "names the grid",
		8: "shells are not reachable through the MCP API", 9: "writes nothing to disk", 10: "writes nothing to disk",
	} {
		if !strings.Contains(got[i].Verdict, want) {
			t.Errorf("item %d = %q, want it saying %q", i, got[i].Verdict, want)
		}
	}
	var g mcpapi.GridOut
	call(t, s, "read_grid", map[string]any{"id": n.root}, &g)
	if len(g.Tiles) != 1 || g.Tiles[0].ID != got[1].Tile.ID {
		t.Fatalf("home holds %+v, want the one door", g.Tiles)
	}
	refusedWith(t, callRaw(t, s, "create", map[string]any{"items": []any{}}), "refused:")
}

// A far node's grid takes a create through the router, its door answering
// the far grid it holds; switched off, it takes none and says so.
func TestMCPCreatesOnAFarGridAndADisabledOneRefuses(t *testing.T) {
	n := newMCPNode(t)
	s, err := n.agent(t, bearer(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	door := landed(t, items(t, s, "create", map[string]any{"kind": "door", "grid": n.feedRoot, "name": "far"})[0])
	if !strings.HasPrefix(door.ID, n.feedNS+"/") || !strings.HasPrefix(door.ChildGrid, n.feedNS+"/") {
		t.Fatalf("a far door = %+v, want it and its grid under %s", door, n.feedNS)
	}
	if inside := mkText(t, s, door.ChildGrid, "far note"); inside.Grid != door.ChildGrid {
		t.Fatalf("a text into the far door's grid landed on %s", inside.Grid)
	}
	if err := n.reg.Switches.Disable("feed"); err != nil {
		t.Fatal(err)
	}
	verdicts(t, "create", items(t, s, "create",
		map[string]any{"kind": "url", "grid": n.feedRoot, "address": "https://example.com"},
		map[string]any{"kind": "door", "grid": n.root}),
		"disabled:", "ok")
}

// at places a tile at an exact cell and w and h shape it; with near or no
// cell store.FreeSlot places a tile of that size, free of every tile.
func TestMCPCreatePlacesAndShapesATile(t *testing.T) {
	n := newMCPNode(t)
	s, err := n.agent(t, bearer(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	made := items(t, s, "create",
		map[string]any{"kind": "door", "grid": n.root, "at": map[string]any{"x": 5, "y": 1}, "w": 3, "h": 2},
		map[string]any{"kind": "text", "grid": n.root, "body": "near", "near": map[string]any{"x": 5, "y": 1}, "w": 2, "h": 2},
		map[string]any{"kind": "url", "grid": n.root, "address": "example.com", "at": map[string]any{"x": 0, "y": 0}},
		map[string]any{"kind": "workspace", "grid": n.root, "w": 2, "h": 3})
	want := [][4]int64{{5, 1, 3, 2}, {5, 3, 2, 2}, {0, 0, 1, 1}, {1, 0, 2, 3}}
	var g mcpapi.GridOut
	call(t, s, "read_grid", map[string]any{"id": n.root}, &g)
	if len(g.Tiles) != len(want) {
		t.Fatalf("home holds %+v, want the %d tiles made", g.Tiles, len(want))
	}
	for i, v := range made {
		got := landed(t, v)
		for _, r := range g.Tiles {
			if r.ID == got.ID {
				got = r
			}
		}
		if p := [4]int64{got.X, got.Y, got.W, got.H}; p != want[i] {
			t.Errorf("item %d reads back at %v, want %v", i, p, want[i])
		}
	}
}

// The node judges a placement: one over a tile or of a size it does not
// allow is refused with its reason, at with near is refused, and each item
// after a refused one still lands.
func TestAnMCPCreateRefusesAPlacementTheNodeRefuses(t *testing.T) {
	n := newMCPNode(t)
	s, err := n.agent(t, bearer(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	at := func(x, y int) map[string]any { return map[string]any{"x": x, "y": y} }
	got := items(t, s, "create",
		map[string]any{"kind": "door", "grid": n.root, "at": at(0, 0), "w": 2, "h": 2},
		map[string]any{"kind": "text", "grid": n.root, "body": "over", "at": at(1, 1)},
		map[string]any{"kind": "door", "grid": n.root, "at": at(4, 0), "near": at(4, 0)},
		map[string]any{"kind": "door", "grid": n.root, "at": at(4, 0), "w": -1},
		map[string]any{"kind": "door", "grid": n.root, "h": -2},
		map[string]any{"kind": "text", "grid": n.root, "body": "free", "at": at(2, 1)})
	verdicts(t, "create", got, "ok", "conflict:", "refused:", "refused:", "refused:", "ok")
	for i, want := range map[int]string{1: "overlaps an existing tile", 2: "not both", 3: "must be positive", 4: "must be positive"} {
		if !strings.Contains(got[i].Verdict, want) {
			t.Errorf("item %d = %q, want it saying %q", i, got[i].Verdict, want)
		}
	}
}

// A directory's grid and a far node's grid each take a placed, shaped
// Gridwell tile and refuse one over a tile, a file's included.
func TestMCPCreatePlacesOnADirectoryAndAFarGrid(t *testing.T) {
	n := newMCPNode(t)
	s, err := n.agent(t, bearer(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(n.dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	var g mcpapi.GridOut
	call(t, s, "read_grid", map[string]any{"id": n.dirGrid}, &g)
	if len(g.Tiles) != 1 {
		t.Fatalf("the directory holds %+v, want its one file", g.Tiles)
	}
	file := g.Tiles[0]
	at := func(x, y int64) map[string]any { return map[string]any{"x": x, "y": y} }
	for _, grid := range []string{n.dirGrid, n.feedRoot} {
		got := items(t, s, "create",
			map[string]any{"kind": "door", "grid": grid, "at": at(3, 3), "w": 2, "h": 3},
			map[string]any{"kind": "text", "grid": grid, "body": "over", "at": at(4, 5)})
		verdicts(t, "create on "+grid, got, "ok", "conflict:")
		if d := landed(t, got[0]); d.X != 3 || d.Y != 3 || d.W != 2 || d.H != 3 {
			t.Errorf("a door on %s = %+v, want 2x3 at (3,3)", grid, d)
		}
	}
	verdicts(t, "create over a file", items(t, s, "create",
		map[string]any{"kind": "text", "grid": n.dirGrid, "body": "over", "at": at(file.X, file.Y)}), "conflict:")
}
