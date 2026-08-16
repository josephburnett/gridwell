package store

import (
	"context"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// The id: selector subsumes LocateTile (issue #244 over #234): an exact
// lookup whose one result carries the containing-well chain outermost
// first, tracking moves; a missing id is empty results, never an error.
func TestSearchByID(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	outer, err := s.CreateWell(ctx, &rpc.CreateWellRequest{GridID: root, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateWell(ctx, &rpc.CreateWellRequest{GridID: outer.ChildGridID, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	text, err := s.CreateText(ctx, &rpc.CreateTextRequest{GridID: root, X: 3, Y: 0, W: 1, H: 1, Data: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}

	// At the root: one result, empty chain.
	res, err := s.Search(ctx, "id:"+text.ID, 0)
	if err != nil {
		t.Fatalf("search at root: %v", err)
	}
	if len(res) != 1 || len(res[0].Path) != 0 || res[0].Tile.ID != text.ID {
		t.Fatalf("root result = %+v, want the tile with an empty chain", res)
	}

	// Move it two levels deep: the chain is [outer, inner].
	if _, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: text.ID, Version: text.Version,
		GridID: inner.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatalf("move into inner: %v", err)
	}
	res, err = s.Search(ctx, "id:"+text.ID, 0)
	if err != nil {
		t.Fatalf("search after move: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("results = %d, want 1", len(res))
	}
	path := res[0].Path
	if len(path) != 2 || path[0].ID != outer.ID || path[1].ID != inner.ID {
		t.Fatalf("chain = %+v, want [outer inner]", path)
	}
	if path[0].GridID != root || path[1].GridID != outer.ChildGridID {
		t.Fatalf("chain rows carry wrong grids: %+v", path)
	}

	// A missing id is EMPTY, not an error — "no results" is an answer.
	if res, err := s.Search(ctx, "id:999999", 0); err != nil || len(res) != 0 {
		t.Fatalf("missing tile: res=%v err=%v, want empty and nil", res, err)
	}
}

// Free text matches names and text bodies, names ranked first, each hit a
// PLACE with its chain; ephemeral (scratch) tiles never surface.
func TestSearchText(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	well, err := s.CreateWell(ctx, &rpc.CreateWellRequest{GridID: root, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: well.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
		Data: []byte("# Plans\n\nthe gopher meeting is on tuesday\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	named, err := s.CreateURL(ctx, &rpc.CreateURLRequest{
		GridID: root, X: 3, Y: 0, W: 1, H: 1, URL: "https://example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTileAlt(ctx, mustTileID(t, named.ID), "Gopher Conference", true); err != nil {
		t.Fatal(err)
	}

	res, err := s.Search(ctx, "gopher", 0)
	if err != nil {
		t.Fatalf("text search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("results = %d (%+v), want 2 (name + body)", len(res), res)
	}
	// Name hit first (score 1), body hit second (0.5) with its chain + snippet.
	if res[0].Tile.ID != named.ID || res[0].Score != 1 {
		t.Errorf("first = %+v, want the NAME hit", res[0])
	}
	if res[1].Tile.ID != doc.ID || res[1].Score != 0.5 {
		t.Errorf("second = %+v, want the BODY hit", res[1])
	}
	if !strings.Contains(res[1].Snippet, "gopher meeting") {
		t.Errorf("snippet = %q, want the matched context", res[1].Snippet)
	}
	if len(res[1].Path) != 1 || res[1].Path[0].ID != well.ID {
		t.Errorf("body hit path = %+v, want [well]", res[1].Path)
	}

	// Case-insensitive; no match = empty.
	if res, _ := s.Search(ctx, "GOPHER", 0); len(res) != 2 {
		t.Errorf("case-insensitive: %d results, want 2", len(res))
	}
	if res, _ := s.Search(ctx, "zebra", 0); len(res) != 0 {
		t.Errorf("no-match: %+v, want empty", res)
	}

	// An ephemeral (scratch-grid) tile never surfaces.
	scratch, err := s.ScratchGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: scratch, X: 0, Y: 0, W: 1, H: 1, Data: []byte("gopher scratchpad"),
	}); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.Search(ctx, "gopher", 0); len(res) != 2 {
		t.Errorf("scratch tile surfaced: %d results, want still 2", len(res))
	}
}

// mustTileID parses a store-local tile id for direct store helpers.
func mustTileID(t *testing.T, id string) int64 {
	t.Helper()
	n, err := parseID(id)
	if err != nil {
		t.Fatalf("parse id %q: %v", id, err)
	}
	return n
}
