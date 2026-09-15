package store

import (
	"context"
	"errors"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// createURLTileForTest creates a URL tile and returns it.
func createURLTileForTest(t *testing.T, s *Store, root string, x int64, url string) *gridwellv1.Tile {
	t.Helper()
	tile, err := s.CreateURL(context.Background(), root, x, 0, 1, 1, url)
	if err != nil {
		t.Fatalf("create URL tile: %v", err)
	}
	return tile
}

// TestCloneURLTile verifies that CloneTile of a URL tile carries the URL
// and preview JPEG onto a fresh row.
func TestCloneURLTile(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	src := createURLTileForTest(t, s, root, 0, "https://example.com/a")
	// Seed a preview (via the freeze RPC) so we can verify it carries over.
	src, err := s.SetURLState(ctx, src.Id, []byte("jpegbytes"), "", "", "")
	if err != nil {
		t.Fatalf("seed preview: %v", err)
	}

	clone, err := s.CloneTile(ctx, &gridwellv1.CloneTileRequest{
		TileId:     src.Id,
		DestGridId: root, X: 2, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if clone.Kind != rpc.KindURL {
		t.Errorf("clone kind = %q, want %q", clone.Kind, rpc.KindURL)
	}
	if clone.UrlString != src.UrlString {
		t.Errorf("clone URLString = %q, want %q", clone.UrlString, src.UrlString)
	}
	if clone.Id == src.Id {
		t.Error("clone has same row id as source")
	}
	if clone.Version != src.Version {
		t.Errorf("clone version = %d, want %d (shared until divergence)", clone.Version, src.Version)
	}
	// Preview bytes should have copied.
	jpeg, err := s.GetTilePreview(ctx, clone.Id)
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
	tileIDInt, _ := parseID(tile.Id)

	if err := s.SetTileAlt(ctx, tile.Id, "Example Title", false); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.loadTile(ctx, s.db, tileIDInt)
	if err != nil {
		t.Fatal(err)
	}
	if got.AltText != "Example Title" {
		t.Errorf("AltText = %q, want %q", got.AltText, "Example Title")
	}
	// An AUTOMATIC capture (user=false) is an observation, not an edit: it
	// writes the name and fans the event, but leaves the version alone so it
	// can never cost a concurrent editor their claim (version_rule_test.go).
	if got.Version != tile.Version {
		t.Errorf("automatic capture moved the version %d -> %d", tile.Version, got.Version)
	}
	// Setting back to empty clears the column.
	if err := s.SetTileAlt(ctx, tile.Id, "", false); err != nil {
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

	out, err := s.SetURLState(ctx, tile.Id, []byte("frozenjpeg"), "https://example.com/b", "Example B", "")
	if err != nil {
		t.Fatalf("SetURLState: %v", err)
	}
	// Returned tile reflects all three writes. The freeze is a capture, so
	// the version stays put (version_rule_test.go).
	if out.UrlString != "https://example.com/b" {
		t.Errorf("URLString = %q, want https://example.com/b", out.UrlString)
	}
	if out.AltText != "Example B" {
		t.Errorf("AltText = %q, want %q", out.AltText, "Example B")
	}
	if out.Version != tile.Version {
		t.Errorf("capture moved the version %d -> %d", tile.Version, out.Version)
	}
	jpeg, err := s.GetTilePreview(ctx, tile.Id)
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
	tileIDInt, _ := parseID(tile.Id)
	// Seed preview + title we expect to survive an empty-field update.
	if _, err := s.SetURLState(ctx, tile.Id, []byte("keepjpeg"), "", "Keep Title", ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A capture that failed (empty jpeg) and reported no url/title must not
	// clobber the good state.
	if _, err := s.SetURLState(ctx, tile.Id, nil, "", "", ""); err != nil {
		t.Fatalf("empty update: %v", err)
	}
	got, err := s.loadTile(ctx, s.db, tileIDInt)
	if err != nil {
		t.Fatal(err)
	}
	if got.UrlString != "https://example.com/keep" {
		t.Errorf("URLString = %q, want preserved", got.UrlString)
	}
	if got.AltText != "Keep Title" {
		t.Errorf("AltText = %q, want preserved", got.AltText)
	}
	jpeg, err := s.GetTilePreview(ctx, tile.Id)
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
	w, err := s.CreateWell(context.Background(), root, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SetURLState(context.Background(), w.Id, []byte("x"), "", "", "")
	if !errors.Is(err, ErrNotURLTile) {
		t.Errorf("got %v, want ErrNotURLTile", err)
	}
}

// Freezing a url tile in a cloned grid lands the new address and preview in
// this clone's row only, never leaking navigation into every clone.
func TestSetURLStateForksSharedGrid(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	// A well whose child grid holds a single URL tile.
	wellA, err := s.CreateWell(ctx, root, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateURL(ctx, wellA.ChildGridId, 0, 0, 1, 1, "https://a.example"); err != nil {
		t.Fatal(err)
	}

	// Clone the well: copy-on-clone deep-copies the child grid, so wellB gets
	// its own independent URL tile (a re-rowed copy of wellA's).
	wellB, err := s.CloneTile(ctx, &gridwellv1.CloneTileRequest{
		TileId:     wellA.Id,
		DestGridId: root, X: 50, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Freeze through wellB's OWN copy of the URL tile. It must touch only
	// wellB; wellA is a separate row and stays as it was.
	bGrid, err := s.GetGrid(ctx, wellB.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	var bURL *gridwellv1.Tile
	for _, tile := range bGrid.Tiles {
		if tile.Kind == rpc.KindURL {
			bURL = tile
		}
	}
	if bURL == nil {
		t.Fatalf("no URL tile in wellB's child grid %s", wellB.ChildGridId)
	}
	if _, err := s.SetURLState(ctx, bURL.Id, []byte("frozen-b"), "https://b.example", "B", ""); err != nil {
		t.Fatal(err)
	}

	// Reload both wells to get their (possibly forked) child grids, then read
	// the URL tile in each.
	urlIn := func(well *gridwellv1.Tile) *gridwellv1.Tile {
		t.Helper()
		wellIDInt, _ := parseID(well.Id)
		reloaded, err := s.loadTile(ctx, s.db, wellIDInt)
		if err != nil {
			t.Fatal(err)
		}
		g, err := s.GetGrid(ctx, reloaded.ChildGridId)
		if err != nil {
			t.Fatal(err)
		}
		for _, tile := range g.Tiles {
			if tile.Kind == rpc.KindURL {
				return tile
			}
		}
		t.Fatalf("no URL tile in grid %s", reloaded.ChildGridId)
		return nil
	}

	a := urlIn(wellA)
	b := urlIn(wellB)
	if a.UrlString != "https://a.example" {
		t.Errorf("wellA URL = %q, want https://a.example (must NOT see the clone's nav)", a.UrlString)
	}
	if a.PreviewBlobId != 0 {
		t.Errorf("wellA preview = %d, want 0 (the freeze leaked into the original)", a.PreviewBlobId)
	}
	if b.UrlString != "https://b.example" {
		t.Errorf("wellB URL = %q, want https://b.example", b.UrlString)
	}
	if b.PreviewBlobId == 0 {
		t.Error("wellB has no preview after freeze")
	}
	verifyRefcounts(t, s)
}

// TestURLHistoryRoundTrip: the freeze writeback persists the
// navigation back-stack; an empty capture leaves the stored one untouched
// (the JPEG rule); the tile reads it back for revive.
func TestURLHistoryRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	tile, err := s.CreateURL(ctx, root, 0, 0, 1, 1, "https://a")
	if err != nil {
		t.Fatalf("CreateURL: %v", err)
	}
	hist := `{"index":1,"entries":[{"url":"https://a","title":"A"},{"url":"https://b","title":"B"}]}`
	out, err := s.SetURLState(ctx, tile.Id, nil, "https://b", "", hist)
	if err != nil {
		t.Fatalf("SetURLState: %v", err)
	}
	if out.UrlHistory != hist {
		t.Errorf("url_history = %q, want the captured stack", out.UrlHistory)
	}
	// A later freeze with NO history (partial capture) keeps the stored one.
	out2, err := s.SetURLState(ctx, tile.Id, nil, "https://b", "", "")
	if err != nil {
		t.Fatalf("second SetURLState: %v", err)
	}
	if out2.UrlHistory != hist {
		t.Errorf("empty capture clobbered the stored history: %q", out2.UrlHistory)
	}
}
