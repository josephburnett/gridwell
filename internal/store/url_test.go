package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// createURLTileForTest creates a URL tile and returns it.
func createURLTileForTest(t *testing.T, s *Store, root string, x int64, url string) *rpc.Tile {
	t.Helper()
	tile, err := s.CreateURL(context.Background(), &rpc.CreateURLRequest{
		GridID: root,
		X:      x, Y: 0, W: 1, H: 1, URL: url,
	})
	if err != nil {
		t.Fatalf("create URL tile: %v", err)
	}
	return tile
}

// TestCloneURLTile verifies that CloneTile of a URL tile carries the URL
// and preview JPEG, and that the clone shares the source's object_id.
func TestCloneURLTile(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	src := createURLTileForTest(t, s, root, 0, "https://example.com/a")
	// Seed a preview (via the freeze RPC) so we can verify it carries over.
	src, err := s.SetURLState(ctx, &rpc.SetURLStateRequest{
		TileID: src.ID, Version: src.Version,
		JPEG: []byte("jpegbytes"),
	})
	if err != nil {
		t.Fatalf("seed preview: %v", err)
	}

	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: src.ID, Version: src.Version,
		DestGridID: root, X: 2, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if clone.Kind != rpc.KindURL {
		t.Errorf("clone kind = %q, want %q", clone.Kind, rpc.KindURL)
	}
	if clone.URLString != src.URLString {
		t.Errorf("clone URLString = %q, want %q", clone.URLString, src.URLString)
	}
	if clone.ID == src.ID {
		t.Error("clone has same row id as source")
	}
	if clone.ObjectID != src.ObjectID {
		t.Errorf("clone object_id = %q, want %q (shared identity)", clone.ObjectID, src.ObjectID)
	}
	if clone.Version != src.Version {
		t.Errorf("clone version = %d, want %d (shared until divergence)", clone.Version, src.Version)
	}
	// Preview bytes should have copied.
	jpeg, err := s.GetTilePreview(ctx, clone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(jpeg) != "jpegbytes" {
		t.Errorf("clone preview = %q, want \"jpegbytes\"", string(jpeg))
	}
}

func TestSetTileAlt(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	tile := createURLTileForTest(t, s, root, 0, "https://example.com")
	tileIDInt, _ := parseID(tile.ID)

	if err := s.SetTileAlt(ctx, tileIDInt, "Example Title", false); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.loadTile(ctx, s.db, tileIDInt)
	if err != nil {
		t.Fatal(err)
	}
	if got.AltText != "Example Title" {
		t.Errorf("AltText = %q, want %q", got.AltText, "Example Title")
	}
	if got.Version != tile.Version+1 {
		t.Errorf("version after SetTileAlt = %d, want %d", got.Version, tile.Version+1)
	}
	// Setting back to empty clears the column.
	if err := s.SetTileAlt(ctx, tileIDInt, "", false); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err = s.loadTile(ctx, s.db, tileIDInt)
	if err != nil {
		t.Fatal(err)
	}
	if got.AltText != "" {
		t.Errorf("AltText after clear = %q, want empty", got.AltText)
	}
}

func TestSetURLState(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	tile := createURLTileForTest(t, s, root, 0, "https://example.com/a")

	out, err := s.SetURLState(ctx, &rpc.SetURLStateRequest{
		TileID: tile.ID, Version: tile.Version,
		JPEG: []byte("frozenjpeg"), URL: "https://example.com/b", Title: "Example B",
	})
	if err != nil {
		t.Fatalf("SetURLState: %v", err)
	}
	// Returned tile reflects all three writes and a single version bump.
	if out.URLString != "https://example.com/b" {
		t.Errorf("URLString = %q, want https://example.com/b", out.URLString)
	}
	if out.AltText != "Example B" {
		t.Errorf("AltText = %q, want %q", out.AltText, "Example B")
	}
	if out.Version != tile.Version+1 {
		t.Errorf("version after SetURLState = %d, want %d (single bump)", out.Version, tile.Version+1)
	}
	jpeg, err := s.GetTilePreview(ctx, tile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(jpeg) != "frozenjpeg" {
		t.Errorf("preview = %q, want frozenjpeg", string(jpeg))
	}
}

func TestSetURLStateSkipsEmptyFields(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	tile := createURLTileForTest(t, s, root, 0, "https://example.com/keep")
	tileIDInt, _ := parseID(tile.ID)
	// Seed preview + title we expect to survive an empty-field update.
	seed, err := s.SetURLState(ctx, &rpc.SetURLStateRequest{
		TileID: tile.ID, Version: tile.Version,
		JPEG: []byte("keepjpeg"), Title: "Keep Title",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A capture that failed (empty jpeg) and reported no url/title must not
	// clobber the good state.
	if _, err := s.SetURLState(ctx, &rpc.SetURLStateRequest{
		TileID: tile.ID, Version: seed.Version,
	}); err != nil {
		t.Fatalf("empty update: %v", err)
	}
	got, err := s.loadTile(ctx, s.db, tileIDInt)
	if err != nil {
		t.Fatal(err)
	}
	if got.URLString != "https://example.com/keep" {
		t.Errorf("URLString = %q, want preserved", got.URLString)
	}
	if got.AltText != "Keep Title" {
		t.Errorf("AltText = %q, want preserved", got.AltText)
	}
	jpeg, err := s.GetTilePreview(ctx, tile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(jpeg) != "keepjpeg" {
		t.Errorf("preview = %q, want preserved keepjpeg", string(jpeg))
	}
}

func TestSetURLStateRefusesNonURLTile(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	w, err := s.CreateWell(context.Background(), &rpc.CreateWellRequest{
		GridID: root,
		X:      0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SetURLState(context.Background(), &rpc.SetURLStateRequest{
		TileID: w.ID, Version: w.Version, JPEG: []byte("x"),
	})
	if !errors.Is(err, ErrNotURLTile) {
		t.Errorf("got %v, want ErrNotURLTile", err)
	}
}

// TestSetURLStateForksSharedGrid: freezing a URL tile that lives in a
// shared (cloned) grid must fork the spine so the new address + preview
// land in this clone's row only. Regression: SetURLState wrote the raw
// shared row, leaking navigation into every clone.
func TestSetURLStateForksSharedGrid(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	// A well whose child grid holds a single URL tile.
	wellA, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateURL(ctx, &rpc.CreateURLRequest{
		GridID: wellA.ChildGridID,
		X:      0, Y: 0, W: 1, H: 1, URL: "https://a.example",
	}); err != nil {
		t.Fatal(err)
	}

	// Clone the well: copy-on-clone deep-copies the child grid, so wellB gets
	// its own independent URL tile (a re-rowed copy of wellA's).
	wellB, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: wellA.ID, Version: wellA.Version,
		DestGridID: root, X: 50, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Freeze through wellB's OWN copy of the URL tile. It must touch only
	// wellB; wellA is a separate row and stays as it was.
	bGrid, err := s.GetGrid(ctx, wellB.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	var bURL rpc.Tile
	for _, tile := range bGrid.Tiles {
		if tile.Kind == rpc.KindURL {
			bURL = tile
		}
	}
	if bURL.ID == "" {
		t.Fatalf("no URL tile in wellB's child grid %s", wellB.ChildGridID)
	}
	if _, err := s.SetURLState(ctx, &rpc.SetURLStateRequest{
		TileID: bURL.ID, Version: bURL.Version,
		JPEG: []byte("frozen-b"), URL: "https://b.example", Title: "B",
	}); err != nil {
		t.Fatal(err)
	}

	// Reload both wells to get their (possibly forked) child grids, then read
	// the URL tile in each.
	urlIn := func(well *rpc.Tile) rpc.Tile {
		t.Helper()
		wellIDInt, _ := parseID(well.ID)
		reloaded, err := s.loadTile(ctx, s.db, wellIDInt)
		if err != nil {
			t.Fatal(err)
		}
		g, err := s.GetGrid(ctx, reloaded.ChildGridID)
		if err != nil {
			t.Fatal(err)
		}
		for _, tile := range g.Tiles {
			if tile.Kind == rpc.KindURL {
				return tile
			}
		}
		t.Fatalf("no URL tile in grid %s", reloaded.ChildGridID)
		return rpc.Tile{}
	}

	a := urlIn(wellA)
	b := urlIn(wellB)
	if a.URLString != "https://a.example" {
		t.Errorf("wellA URL = %q, want https://a.example (must NOT see the clone's nav)", a.URLString)
	}
	if a.PreviewBlobID != 0 {
		t.Errorf("wellA preview = %d, want 0 (the freeze leaked into the original)", a.PreviewBlobID)
	}
	if b.URLString != "https://b.example" {
		t.Errorf("wellB URL = %q, want https://b.example", b.URLString)
	}
	if b.PreviewBlobID == 0 {
		t.Error("wellB has no preview after freeze")
	}
	verifyRefcounts(t, s)
}

// TestURLHistoryRoundTrip (issue #113): the freeze writeback persists the
// navigation back-stack; an empty capture leaves the stored one untouched
// (the JPEG rule); the tile reads it back for revive.
func TestURLHistoryRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	tile, err := s.CreateURL(ctx, &rpc.CreateURLRequest{GridID: root, X: 0, Y: 0, W: 1, H: 1, URL: "https://a"})
	if err != nil {
		t.Fatalf("CreateURL: %v", err)
	}
	hist := `{"index":1,"entries":[{"url":"https://a","title":"A"},{"url":"https://b","title":"B"}]}`
	out, err := s.SetURLState(ctx, &rpc.SetURLStateRequest{
		TileID: tile.ID, Version: tile.Version, URL: "https://b", History: hist,
	})
	if err != nil {
		t.Fatalf("SetURLState: %v", err)
	}
	if out.URLHistory != hist {
		t.Errorf("url_history = %q, want the captured stack", out.URLHistory)
	}
	// A later freeze with NO history (partial capture) keeps the stored one.
	out2, err := s.SetURLState(ctx, &rpc.SetURLStateRequest{
		TileID: tile.ID, Version: out.Version, URL: "https://b",
	})
	if err != nil {
		t.Fatalf("second SetURLState: %v", err)
	}
	if out2.URLHistory != hist {
		t.Errorf("empty capture clobbered the stored history: %q", out2.URLHistory)
	}
}
