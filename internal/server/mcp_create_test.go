package server

import (
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
