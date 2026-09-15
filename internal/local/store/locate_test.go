package store

import (
	"context"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The id: selector is an exact lookup whose one result carries the
// containing-well chain, outermost first, tracking moves. A missing id gives
// empty results, never an error.
func TestSearchByID(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	outer, err := s.CreateWell(ctx, root, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateWell(ctx, outer.ChildGridId, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	text, err := s.CreateText(ctx, root, 3, 0, 1, 1, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}

	// At the root: one result, empty chain.
	res, err := s.Search(ctx, "id:"+text.Id, 0)
	if err != nil {
		t.Fatalf("search at root: %v", err)
	}
	if len(res) != 1 || len(res[0].Path) != 0 || res[0].Tile.Id != text.Id {
		t.Fatalf("root result = %+v, want the tile with an empty chain", res)
	}

	// Move it two levels deep: the chain is [outer, inner].
	if _, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: text.Id,
		GridId: inner.ChildGridId, X: 0, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatalf("move into inner: %v", err)
	}
	res, err = s.Search(ctx, "id:"+text.Id, 0)
	if err != nil {
		t.Fatalf("search after move: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("results = %d, want 1", len(res))
	}
	path := res[0].Path
	if len(path) != 2 || path[0].Id != outer.Id || path[1].Id != inner.Id {
		t.Fatalf("chain = %+v, want [outer inner]", path)
	}
	if path[0].GridId != root || path[1].GridId != outer.ChildGridId {
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

	well, err := s.CreateWell(ctx, root, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.CreateText(ctx, well.ChildGridId, 0, 0, 1, 1, []byte("# Plans\n\nthe gopher meeting is on tuesday\n"))
	if err != nil {
		t.Fatal(err)
	}
	named, err := s.CreateURL(ctx, root, 3, 0, 1, 1, "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTileAlt(ctx, named.Id, "Gopher Conference", true); err != nil {
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
	if res[0].Tile.Id != named.Id || res[0].Score != 1 {
		t.Errorf("first = %+v, want the NAME hit", res[0])
	}
	if res[1].Tile.Id != doc.Id || res[1].Score != 0.5 {
		t.Errorf("second = %+v, want the BODY hit", res[1])
	}
	if !strings.Contains(res[1].Snippet, "gopher meeting") {
		t.Errorf("snippet = %q, want the matched context", res[1].Snippet)
	}
	if len(res[1].Path) != 1 || res[1].Path[0].Id != well.Id {
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
	if _, err := s.CreateText(ctx, scratch, 0, 0, 1, 1, []byte("gopher scratchpad")); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.Search(ctx, "gopher", 0); len(res) != 2 {
		t.Errorf("scratch tile surfaced: %d results, want still 2", len(res))
	}
}
