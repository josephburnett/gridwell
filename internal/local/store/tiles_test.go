package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

func TestCreateWellHappyPath(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 1, Y: 2, W: 3, H: 4,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if w.Kind != rpc.KindWell {
		t.Errorf("kind=%q", w.Kind)
	}
	if w.X != 1 || w.Y != 2 || w.W != 3 || w.H != 4 {
		t.Errorf("dims wrong: %+v", w)
	}
	if w.ChildGridID == "" {
		t.Error("no child grid")
	}
	if w.Version != 0 {
		t.Errorf("new tile version = %d, want 0", w.Version)
	}
	// Read the parent grid back.
	g, err := s.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Tiles) != 1 {
		t.Errorf("expected 1 tile, got %d", len(g.Tiles))
	}
}

func TestCreateWellLabelBecomesAltText(t *testing.T) {
	// The + palette's name field: a user-provided label on an interior well
	// create is stored as the tile's alt_text (the grid's name). Wells have
	// no content to derive an alt from, so the label is the only writer.
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 1, Y: 2, W: 1, H: 1,
		Label: "recipes",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if w.AltText != "recipes" {
		t.Errorf("AltText = %q, want %q", w.AltText, "recipes")
	}
	// The label is a durable server fact: read it back off the grid.
	g, err := s.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Tiles) != 1 || g.Tiles[0].AltText != "recipes" {
		t.Errorf("grid readback tiles = %+v, want one well named recipes", g.Tiles)
	}

	// No label → no alt, exactly as before.
	plain, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 5, Y: 5, W: 1, H: 1,
	})
	if err != nil {
		t.Fatalf("create unlabeled: %v", err)
	}
	if plain.AltText != "" {
		t.Errorf("unlabeled AltText = %q, want empty", plain.AltText)
	}
}

func TestCreateWellOverlapRefused(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	if _, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 5, H: 5,
	}); err != nil {
		t.Fatal(err)
	}
	// Overlap.
	_, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 4, Y: 4, W: 2, H: 2,
	})
	if !errors.Is(err, ErrOverlap) {
		t.Errorf("got %v, want ErrOverlap", err)
	}
	// Adjacent (touching) is OK.
	if _, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 5, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Errorf("adjacent placement refused: %v", err)
	}
}

func TestCreateWellInvalidArgs(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	_, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 0, H: 1,
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("zero w: got %v", err)
	}
	_, err = s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: -1,
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("neg h: got %v", err)
	}
}

func TestCreateIntoNestedAndMissingGrids(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Descend into well; create a sub-well inside it.
	sub, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: w.ChildGridID, X: 0, Y: 0, W: 2, H: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sub.GridID != w.ChildGridID {
		t.Errorf("sub.GridID = %s, want %s", sub.GridID, w.ChildGridID)
	}

	// A create into a grid that doesn't exist is refused (grid_id is the
	// authoritative location; there is no descent path to validate).
	_, err = s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: "999999", X: 1, Y: 1, W: 1, H: 1,
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("got %v, want ErrInvalidArgument", err)
	}
}

func TestCreateTextSize(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	huge := make([]byte, MaxBlobBytes+1)
	_, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root,
		X:      0, Y: 0, W: 1, H: 1, Data: huge,
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("oversized: got %v", err)
	}
}

func TestCreateURLTile(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	tile, err := s.CreateURL(ctx, &rpc.CreateURLRequest{
		GridID: root,
		X:      0, Y: 0, W: 1, H: 1,
		URL: "https://example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tile.Kind != rpc.KindURL {
		t.Errorf("kind = %q, want %q", tile.Kind, rpc.KindURL)
	}
	if tile.URLString != "https://example.com" {
		t.Errorf("URLString = %q, want https://example.com", tile.URLString)
	}
	if tile.BlobID != 0 {
		t.Errorf("URL tile got BlobID = %d, want 0", tile.BlobID)
	}
	// Whitespace around the URL should be trimmed.
	tile2, err := s.CreateURL(ctx, &rpc.CreateURLRequest{
		GridID: root,
		X:      1, Y: 0, W: 1, H: 1,
		URL: "  https://example.org/path\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tile2.URLString != "https://example.org/path" {
		t.Errorf("URLString = %q (whitespace not trimmed)", tile2.URLString)
	}
	// Disallowed scheme → ErrInvalidArgument.
	_, err = s.CreateURL(ctx, &rpc.CreateURLRequest{
		GridID: root,
		X:      2, Y: 0, W: 1, H: 1,
		URL: "javascript:alert(1)",
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("javascript: scheme: got %v, want ErrInvalidArgument", err)
	}
	// GetTilePreview on a brand-new URL tile returns nil.
	jpeg, err := s.GetTilePreview(ctx, tile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(jpeg) != 0 {
		t.Errorf("new URL tile preview = %d bytes, want 0", len(jpeg))
	}
}

func TestCreateTextBlobReuse(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	data := []byte("hello world")
	a, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root,
		X:      0, Y: 0, W: 1, H: 1, Data: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root,
		X:      5, Y: 0, W: 1, H: 1, Data: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.BlobID != b.BlobID {
		t.Errorf("blob ids = %d, %d (want same)", a.BlobID, b.BlobID)
	}
	// Refcount should be 2.
	if rc := refcount(t, s, "blobs", a.BlobID); rc != 2 {
		t.Errorf("blob refcount = %d, want 2", rc)
	}
}

func TestResizeNode(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: w.ID, Version: w.Version,
		GridID: w.GridID, X: 0, Y: 0, W: 3, H: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.W != 3 || r.H != 4 {
		t.Errorf("after resize %+v", r)
	}
	// A resize is layout: the version stays put (version_rule_test.go).
	if r.Version != w.Version {
		t.Errorf("resize moved the version %d -> %d; layout does not bump", w.Version, r.Version)
	}
	// Resize to overlap another tile should fail.
	_, err = s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 4, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: r.ID, Version: r.Version,
		GridID: r.GridID, X: 0, Y: 0, W: 5, H: 4,
	})
	if !errors.Is(err, ErrOverlap) {
		t.Errorf("expected ErrOverlap, got %v", err)
	}
}

// TestResizeIgnoresStaleClaim: resize rides PlaceTile, which carries no
// version claim (docs/simplify-plan.md S5) — see TestPlaceTileIgnoresStaleClaim
// for the rule and version_rule_test.go for the whole table.
func TestResizeIgnoresStaleClaim(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: w.ID, Version: w.Version + 99,
		GridID: w.GridID, X: 0, Y: 0, W: 2, H: 2,
	})
	if err != nil {
		t.Fatalf("stale claim must be accepted: %v", err)
	}
	if r.W != 2 || r.H != 2 {
		t.Errorf("resized to %dx%d, want 2x2", r.W, r.H)
	}
}

func TestSetFraming(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SetFraming(ctx, &rpc.SetFramingRequest{
		TileID: w.ID, Version: w.Version,
		Framing: rpc.Framing{Cx: 5.25, Cy: 7.5, Zoom: 1.5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ViewCx != 5.25 || got.ViewCy != 7.5 || got.ViewZoom != 1.5 {
		t.Errorf("view %+v", got)
	}
	// Framing is not a content edit — the version must NOT move.
	if got.Version != w.Version {
		t.Errorf("version after framing = %d, want %d (framing must not bump)", got.Version, w.Version)
	}
}

// TestFramingKeepsClonesAtSharedVersion: re-framing one clone of a well
// (descend, pan/zoom, ascend) must not bump the version, so the clones
// still satisfy "share a version until one is edited" even though their
// stored framing has diverged. A real content edit is what bumps it.
func TestFramingKeepsClonesAtSharedVersion(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: w.ID, Version: w.Version,
		DestGridID: root, X: 10, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if clone.Version != w.Version {
		t.Fatalf("clone starts at version %d, want %d", clone.Version, w.Version)
	}
	// Frame only the clone.
	framed, err := s.SetFraming(ctx, &rpc.SetFramingRequest{
		TileID: clone.ID, Version: clone.Version,
		Framing: rpc.Framing{Cx: 3, Cy: 4, Zoom: 2.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if framed.Version != w.Version {
		t.Errorf("framed clone version = %d, want %d (still shared)", framed.Version, w.Version)
	}
	// The original is untouched and at the same version.
	wIDInt, _ := parseID(w.ID)
	orig, err := s.loadTile(ctx, s.db, wIDInt)
	if err != nil {
		t.Fatal(err)
	}
	if orig.Version != w.Version {
		t.Errorf("original drifted: version=%d, want %d", orig.Version, w.Version)
	}
	if orig.ViewCx == framed.ViewCx && orig.ViewCy == framed.ViewCy {
		t.Error("framing did not diverge between clones")
	}
}

// TestSetTextViewPersistsWindowAndMode pins the text-tile preview-frame
// contract: SetTextView persists text_x/y/w/h and text_mode, and GetGrid
// reads them back.
func TestSetTextViewPersistsWindowAndMode(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	f, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root,
		X:      0, Y: 0, W: 2, H: 2, Data: []byte("hello"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SetTextView(ctx, &rpc.SetTextViewRequest{
		TileID: f.ID, Version: f.Version,
		TextX: 10, TextY: 20, TextW: 640, TextH: 480, TextMode: rpc.TextModeRendered,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.TextX != 10 || got.TextY != 20 || got.TextW != 640 || got.TextH != 480 || got.TextMode != rpc.TextModeRendered {
		t.Errorf("after write: %+v", got)
	}
	// Readback through GetGrid must see the same values.
	gg, err := s.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	var found *rpc.Tile
	for i := range gg.Tiles {
		if gg.Tiles[i].ID == f.ID {
			found = &gg.Tiles[i]
			break
		}
	}
	if found == nil {
		t.Fatal("tile missing from GetGrid")
	}
	if found.TextW != 640 || found.TextH != 480 || found.TextMode != rpc.TextModeRendered {
		t.Errorf("GetGrid round-trip: %+v", found)
	}
}

func TestSetFramingRejectsNonWell(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	f, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root,
		X:      0, Y: 0, W: 1, H: 1, Data: []byte("x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SetFraming(ctx, &rpc.SetFramingRequest{
		TileID: f.ID, Version: f.Version, Framing: rpc.Framing{Cx: 1, Cy: 1, Zoom: 1},
	})
	if !errors.Is(err, ErrNotWellTile) {
		t.Errorf("got %v, want ErrNotWellTile", err)
	}
}

func TestSetTextViewRejectsNonText(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SetTextView(ctx, &rpc.SetTextViewRequest{
		TileID: w.ID, Version: w.Version,
	})
	if !errors.Is(err, ErrNotTextTile) {
		t.Errorf("got %v, want ErrNotTextTile", err)
	}
}

func TestDeleteTile(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	childGridIDStr := w.ChildGridID
	childGridID, _ := parseID(childGridIDStr)
	wIDInt, _ := parseID(w.ID)
	// Two-stage (#262): the first delete parks it in the trash; the second
	// destroys. This test is about destruction's reference release.
	hardDelete(t, s, w.ID)
	if _, err := s.loadTile(ctx, s.db, wIDInt); !errors.Is(err, ErrNotFound) {
		t.Errorf("well still exists: %v", err)
	}
	if _, err := s.loadGrid(ctx, s.db, childGridID); !errors.Is(err, ErrNotFound) {
		t.Errorf("child grid still exists: %v", err)
	}
}

func TestDeleteTileCascadesNonEmptyWell(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: w.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// w may have been bumped by the creation of inner in its child grid?
	// No: creating inner bumps the inner-grid version, not the outer well's.
	// Reload w for current version.
	hardDelete(t, s, w.ID)
	innerIDInt, _ := parseID(inner.ID)
	if _, err := s.loadTile(ctx, s.db, innerIDInt); !errors.Is(err, ErrNotFound) {
		t.Errorf("inner well still exists after cascading delete")
	}
}

// TestDeleteTileIgnoresStaleClaim: the delete gesture is the user's, on a
// tile they can see, and it is recoverable (the row moves to the trash). A
// version that moved under it — a page title capture on the very tile being
// discarded — must not turn the gesture into an error the user has to
// re-issue. No claim (docs/simplify-plan.md S5).
func TestDeleteTileIgnoresStaleClaim(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: w.ID, Version: w.Version + 1}); err != nil {
		t.Errorf("stale claim must be accepted: %v", err)
	}
}
