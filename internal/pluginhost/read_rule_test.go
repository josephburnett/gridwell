package pluginhost_test

// Listing a plugin grid mints nothing, through the real fs binary. The node's
// rows are the user's arrangement of the plugin's entries and nothing else, so
// a grid nobody has touched has no rows at all however often it is read, and
// once a touch has minted rows, a listing that finds the source unchanged
// leaves every stored byte alone.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/local/store/storetest"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// readEveryVerb runs every read the adapter serves over one plugin grid: the
// listing, the single-tile reads that share its synthesis, a preview, and the
// content stream. A read that writes writes during one of them.
func readEveryVerb(t *testing.T, cl *rpc.Client, grid string) {
	t.Helper()
	ctx := context.Background()
	g, err := cl.GetGrid(ctx, grid)
	if err != nil {
		t.Fatalf("GetGrid: %v", err)
	}
	for _, tile := range g.Tiles {
		if _, err := cl.GetTile(ctx, tile.Id); err != nil {
			t.Fatalf("GetTile %s: %v", tile.AltText, err)
		}
		if _, err := cl.GetTilePreview(ctx, tile.Id); err != nil {
			t.Fatalf("GetTilePreview %s: %v", tile.AltText, err)
		}
		if tile.Kind == rpc.KindWell {
			if _, err := cl.GetGrid(ctx, tile.ChildGridId); err != nil {
				t.Fatalf("GetGrid %s: %v", tile.AltText, err)
			}
			continue
		}
		if _, _, _, err := cl.ReadContent(ctx, tile.Id); err != nil {
			t.Fatalf("ReadContent %s: %v", tile.AltText, err)
		}
	}
	if _, err := cl.GetGrid(ctx, grid); err != nil {
		t.Fatalf("GetGrid again: %v", err)
	}
}

// nsRows is what the node has written down about this plugin: its grid rows and
// its tile rows, whole.
func nsRows(t *testing.T, st *store.Store) string {
	t.Helper()
	return storetest.Table(t, st.SQL(), "grids") + "\n--\n" + storetest.Table(t, st.SQL(), "tiles")
}

func TestListingAPluginGridMintsNothing(t *testing.T) {
	root := seedTree(t)
	cl, st := pluginNodeAt(t, root, filepath.Join(t.TempDir(), "mem.db"))
	ctx := context.Background()

	pl, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	landing := plugintest.LandingOf(t, pl.Plugins[0])

	// Untouched. The rows the node holds about this plugin are none, and every
	// read leaves them none: a row here would be a durable fact about an entry
	// the user has never so much as moved, and it would survive the file being
	// deleted.
	before := storetest.Snapshot(t, st.SQL())
	readEveryVerb(t, cl, landing)
	if _, err := cl.Handshake(ctx); err != nil {
		t.Fatal(err)
	}
	if after := storetest.Snapshot(t, st.SQL()); after != before {
		t.Errorf("reading an untouched plugin grid wrote to the store:\n%s", storetest.Diff(before, after))
	}
	if rows := nsRows(t, st); strings.Contains(rows, "ns=p1") {
		t.Errorf("an untouched plugin grid has rows:\n%s", rows)
	}

	// Touched. One placement is a durable fact, so it mints the entry's row and
	// the grid it belongs to. Every read afterwards finds the same source and
	// must leave those rows exactly as the touch left them — Refresh writes to a
	// row only where the source changed something, and Sweep retires only what
	// an authoritative listing stopped naming.
	g, err := cl.GetGrid(ctx, landing)
	if err != nil {
		t.Fatal(err)
	}
	notes := tileNamed(g.Tiles, "notes.md")
	if _, err := cl.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: notes.Id, X: 7, Y: 3, W: 2, H: 2}); err != nil {
		t.Fatal(err)
	}
	sub := tileNamed(g.Tiles, "sub")
	if _, err := cl.SetFraming(ctx, &gridwellv1.SetFramingRequest{TileId: sub.Id, Cx: 2, Cy: 3, Zoom: 0.75}); err != nil {
		t.Fatal(err)
	}

	touched := storetest.Snapshot(t, st.SQL())
	if touched == before {
		t.Fatal("the touch minted nothing, so the case below proves nothing")
	}
	readEveryVerb(t, cl, landing)
	if _, err := cl.Handshake(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.GetTile(ctx, notes.Id); err != nil {
		t.Fatal(err)
	}
	if after := storetest.Snapshot(t, st.SQL()); after != touched {
		t.Errorf("reading a touched plugin grid rewrote the user's arrangement:\n%s",
			storetest.Diff(touched, after))
	}
}
