package fs_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/compose"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/plugins/fs"
)

// The #258 worked example, tested at the SEAM it lives on: the search tool
// exists only through the wire declarations (Info menu_entries, CreateTile
// carrying Tile.menu_entry, WriteContent committing the params, GetGrid
// serving the results) — so the test drives a real gRPC client end to end.

// searchTree builds root/{alpha-notes.txt, sub/{alpha-deep.txt, beta.txt}}.
func searchTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha-notes.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "alpha-deep.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "beta.txt"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func serveSearchPlugin(t *testing.T) (gridwellv1.GridwellClient, string, string) {
	t.Helper()
	dir := searchTree(t)
	p, err := fs.Open(":memory:", nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	p.SetRoot(dir)
	client, closer, err := compose.ServeInProcess(p)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	t.Cleanup(closer)
	info, err := client.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	return client, info.RootGridId, dir
}

func createSearchWell(t *testing.T, c gridwellv1.GridwellClient, gridID string) *gridwellv1.Tile {
	t.Helper()
	resp, err := c.CreateTile(context.Background(), &gridwellv1.CreateTileRequest{
		GridId: gridID,
		Tile:   &gridwellv1.Tile{Kind: "well", MenuEntry: fs.MenuEntrySearch, X: 9, Y: 9, W: 1, H: 1},
	})
	if err != nil {
		t.Fatalf("CreateTile(search): %v", err)
	}
	return resp.Tile
}

func commitQuery(t *testing.T, c gridwellv1.GridwellClient, tile *gridwellv1.Tile, query string) *gridwellv1.Tile {
	t.Helper()
	w, err := c.WriteContent(context.Background())
	if err != nil {
		t.Fatalf("WriteContent open: %v", err)
	}
	if err := w.Send(&gridwellv1.WriteContentRequest{TileId: tile.Id, Version: tile.Version, Data: []byte(`{"query":` + strconv(query) + `}`)}); err != nil {
		t.Fatalf("send: %v", err)
	}
	resp, err := w.CloseAndRecv()
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return resp.Tile
}

func strconv(s string) string { return `"` + s + `"` }

func TestSearch_InfoDeclaresEntry(t *testing.T) {
	c, _, _ := serveSearchPlugin(t)
	info, err := c.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(info.MenuEntries) != 1 || info.MenuEntries[0].Id != fs.MenuEntrySearch {
		t.Fatalf("Info.MenuEntries = %v, want the one search entry", info.MenuEntries)
	}
	if info.MenuEntries[0].ParamSchema == "" || info.MenuEntries[0].Kind != "well" {
		t.Errorf("search entry must carry a schema and kind=well: %v", info.MenuEntries[0])
	}
}

func TestSearch_CreateRefusesUndeclared(t *testing.T) {
	c, root, _ := serveSearchPlugin(t)
	if _, err := c.CreateTile(context.Background(), &gridwellv1.CreateTileRequest{
		GridId: root, Tile: &gridwellv1.Tile{Kind: "text", X: 0, Y: 0},
	}); err == nil {
		t.Fatal("fs must refuse tiles that are not declared menu entries")
	}
}

func TestSearch_CommitFillsChildGridWithLinks(t *testing.T) {
	c, root, dir := serveSearchPlugin(t)
	ctx := context.Background()
	well := createSearchWell(t, c, root)
	if well.MenuEntry != fs.MenuEntrySearch {
		t.Fatalf("created tile must carry menu_entry, got %q", well.MenuEntry)
	}
	if well.ChildGridId != "" {
		t.Fatalf("a search well has no child before the query commits")
	}

	well = commitQuery(t, c, well, "alpha")
	if well.ChildGridId == "" {
		t.Fatal("committed search must have a child grid")
	}
	if !strings.HasPrefix(well.AltText, "search: alpha") {
		t.Errorf("committed well face = %q, want the query visible", well.AltText)
	}

	grid, err := c.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: well.ChildGridId})
	if err != nil {
		t.Fatalf("results grid: %v", err)
	}
	byName := map[string]*gridwellv1.Tile{}
	for _, tl := range grid.Tiles {
		byName[tl.AltText] = tl
	}
	// alpha matches the root file and the deep file, never beta.
	if len(grid.Tiles) != 2 {
		t.Fatalf("results = %v, want alpha-notes.txt and sub/alpha-deep.txt", byName)
	}
	if _, hit := byName["beta.txt"]; hit {
		t.Error("beta must not match alpha")
	}
	for name, tl := range byName {
		if tl.LinkTargetId == "" {
			t.Errorf("file hit %q must be a leaf link to the real tile", name)
			continue
		}
		// The link resolves: the target is the projected tile for the path.
		target, err := c.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: tl.LinkTargetId})
		if err != nil {
			t.Errorf("link target for %q: %v", name, err)
			continue
		}
		if target.Tile.Kind != "text" {
			t.Errorf("target of %q = kind %q, want text", name, target.Tile.Kind)
		}
	}

	// A directory hit shares the REAL directory grid.
	well2 := createSearchWell(t, c, root)
	well2 = commitQuery(t, c, well2, "sub")
	g2, err := c.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: well2.ChildGridId})
	if err != nil {
		t.Fatal(err)
	}
	if len(g2.Tiles) != 1 || g2.Tiles[0].Kind != "well" || g2.Tiles[0].ChildGridId == "" {
		t.Fatalf("dir hit = %v, want one well with the shared child grid", g2.Tiles)
	}
	sub, err := c.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: g2.Tiles[0].ChildGridId})
	if err != nil {
		t.Fatal(err)
	}
	if sub.Grid.SourceId != filepath.Join(dir, "sub") {
		t.Errorf("dir hit's child = %q, want the real directory %q", sub.Grid.SourceId, filepath.Join(dir, "sub"))
	}

	// Re-committing replaces the snapshot wholesale.
	well = commitQuery(t, c, well, "beta")
	g3, _ := c.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: well.ChildGridId})
	if len(g3.Tiles) != 1 || g3.Tiles[0].AltText != "sub/beta.txt" {
		t.Fatalf("re-commit results = %v, want just sub/beta.txt", g3.Tiles)
	}
}

func TestSearch_ToolRowSurvivesSweepAndDeletes(t *testing.T) {
	c, root, _ := serveSearchPlugin(t)
	ctx := context.Background()
	well := createSearchWell(t, c, root)

	// The reconcile sweep must never eat the tool row (it has no backing
	// path); it must come back stamped with its entry.
	grid, err := c.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	var found *gridwellv1.Tile
	for _, tl := range grid.Tiles {
		if tl.Id == well.Id {
			found = tl
		}
	}
	if found == nil {
		t.Fatalf("the sweep ate the search well: %v", grid.Tiles)
	}
	if found.MenuEntry != fs.MenuEntrySearch || found.AltText != "search" {
		t.Errorf("tool row face = (entry %q, alt %q), want (search, search)", found.MenuEntry, found.AltText)
	}

	// Content round-trip: the well's content is its params document.
	well = commitQuery(t, c, well, "alpha")
	stream, err := c.ReadContent(ctx, &gridwellv1.ReadContentRequest{TileId: well.Id})
	if err != nil {
		t.Fatal(err)
	}
	var body []byte
	for {
		ch, rerr := stream.Recv()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			t.Fatalf("recv: %v", rerr)
		}
		body = append(body, ch.Data...)
	}
	if !strings.Contains(string(body), `"alpha"`) {
		t.Errorf("well content = %q, want the committed params", body)
	}

	// Deleting the well removes the row AND its snapshot grid, nothing else.
	if _, err := c.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: well.Id}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	grid, _ = c.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: root})
	for _, tl := range grid.Tiles {
		if tl.Id == well.Id {
			t.Fatal("deleted search well still listed")
		}
	}
	if len(grid.Tiles) == 0 {
		t.Fatal("deleting the tool must not touch the projected entries")
	}
}
