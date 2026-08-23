package providerhost_test

// The migration gate in miniature (docs/v2-design.md §8): a legacy fs DB
// a user has lived in — materialized grids, dragged tiles, framed wells,
// a panned root — converts into a v2 memory DB, and the two stacks crawl
// to zero differences over the SAME directory. Identity is the heart of
// it: converted ids are the legacy ids, and the pinned AUTOINCREMENT
// sequences mean even entries discovered AFTER the conversion mint the
// same fresh ids on both sides.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/convert"
	"github.com/josephburnett/gridwell/internal/parity"
	_ "modernc.org/sqlite"
)

func TestConvertedFSDBMatchesLegacyOnTheWire(t *testing.T) {
	root := seedTree(t)
	legacyDB := filepath.Join(t.TempDir(), "fs.db")
	legacy := legacyNodeAt(t, root, legacyDB)
	ctx := context.Background()

	// Live in the legacy world: materialize everything, drag a tile,
	// frame a well, pan the root.
	if _, err := parity.Crawl(ctx, legacy, parity.Options{}); err != nil {
		t.Fatal(err)
	}
	pl, err := legacy.ListPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := pl.Plugins[0].RootGridID
	g, err := legacy.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	for _, tile := range g.Tiles {
		switch tile.AltText {
		case "notes.md":
			if _, err := legacy.PlaceTile(ctx, &rpc.PlaceTileRequest{
				TileID: tile.ID, Version: tile.Version, GridID: rootGrid, X: 5, Y: 3, W: 2, H: 2,
			}); err != nil {
				t.Fatal(err)
			}
		case "sub":
			if _, err := legacy.SetWellView(ctx, &rpc.SetWellViewRequest{
				TileID: tile.ID, Version: tile.Version, ViewX: 4, ViewY: -2, ViewZoom: 0.8,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := legacy.SetRootView(ctx, &rpc.SetRootViewRequest{RootGridID: rootGrid, Cx: 1.5, Cy: -0.5, Zoom: 1.2}); err != nil {
		t.Fatal(err)
	}

	// Convert. The legacy DB stays in service (the old binary on the old
	// home) — conversion reads it read-only.
	memPath := filepath.Join(t.TempDir(), "mem.db")
	res, err := convert.FS(legacyDB, memPath, fsUUID, "fs", root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Grids == 0 || res.Tiles == 0 {
		t.Fatalf("empty conversion: %+v", res)
	}

	// The v2 stack over the converted memory, same directory.
	v2, _ := providerNodeAt(t, root, memPath)

	// The real gate's shape: crawl scoped to the grids the legacy DB had
	// materialized (on a live source, an unscoped crawl would go on
	// materializing the world on both sides).
	allow := map[string]bool{}
	for _, gid := range res.GridIDs {
		allow[fsUUID+"/"+strconv.FormatInt(gid, 10)] = true
	}
	opts := parity.Options{GridAllow: allow}
	sa, sb, err := parity.CrawlPair(ctx, legacy, v2, opts)
	if err != nil {
		t.Fatal(err)
	}
	if diffs := parity.Diff(sa, sb, parity.Policy{}); len(diffs) != 0 {
		t.Fatalf("converted home differs from legacy (%d):\n%s", len(diffs), strings.Join(diffs, "\n"))
	}

	// Post-conversion life: a NEW file appears. Pinned sequences mean
	// both stacks mint the SAME fresh id for it.
	if err := os.WriteFile(filepath.Join(root, "after.md"), []byte("post-cutover"), 0o644); err != nil {
		t.Fatal(err)
	}
	sa, sb, err = parity.CrawlPair(ctx, legacy, v2, opts)
	if err != nil {
		t.Fatal(err)
	}
	if diffs := parity.Diff(sa, sb, parity.Policy{}); len(diffs) != 0 {
		t.Fatalf("post-conversion mint diverged (%d):\n%s", len(diffs), strings.Join(diffs, "\n"))
	}
}

func TestConvertRefusesUnknownColumns(t *testing.T) {
	root := seedTree(t)
	legacyDB := filepath.Join(t.TempDir(), "fs.db")
	legacy := legacyNodeAt(t, root, legacyDB)
	if _, err := parity.Crawl(context.Background(), legacy, parity.Options{}); err != nil {
		t.Fatal(err)
	}
	// A future column this converter has never heard of.
	mutateDB(t, legacyDB, `ALTER TABLE tiles ADD COLUMN mystery TEXT NOT NULL DEFAULT ''`)
	_, err := convert.FS(legacyDB, filepath.Join(t.TempDir(), "mem.db"), fsUUID, "fs", root)
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("unknown column not refused: %v", err)
	}
}

func TestConvertRefusesWrongIdentity(t *testing.T) {
	root := seedTree(t)
	legacyDB := filepath.Join(t.TempDir(), "fs.db")
	legacy := legacyNodeAt(t, root, legacyDB)
	if _, err := parity.Crawl(context.Background(), legacy, parity.Options{}); err != nil {
		t.Fatal(err)
	}
	_, err := convert.FS(legacyDB, filepath.Join(t.TempDir(), "mem.db"), "stranger", "fs", root)
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("wrong identity not refused: %v", err)
	}
}

// mutateDB applies one DDL statement to a fixture DB.
func mutateDB(t *testing.T, path, ddl string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
}
