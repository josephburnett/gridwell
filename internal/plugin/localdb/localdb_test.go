package localdb_test

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin/localdb"
	"github.com/josephburnett/gridwell/internal/shellsvc"
	"github.com/josephburnett/gridwell/internal/store"
	"github.com/josephburnett/gridwell/internal/tmux"
)

func openPlugin(t *testing.T) *localdb.Plugin {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return localdb.New(st, nil)
}

// rootGrid returns the plugin's default root grid id (from Info — the whole
// handshake; there is no Attach).
func rootGrid(t *testing.T, p *localdb.Plugin) string {
	t.Helper()
	info, err := p.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.RootGridId == "" {
		t.Fatal("Info.RootGridId empty")
	}
	return info.RootGridId
}

// createText is a CreateTile helper for a text tile.
func createText(t *testing.T, p *localdb.Plugin, gridID string, data []byte) *gridwellv1.Tile {
	t.Helper()
	cr, err := p.CreateTile(context.Background(), &gridwellv1.CreateTileRequest{
		GridId: gridID,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 0, Y: 0, W: 4, H: 4},
		Data:   data,
	})
	if err != nil {
		t.Fatalf("CreateTile(text): %v", err)
	}
	return cr.Tile
}

func TestInfo(t *testing.T) {
	p := openPlugin(t)
	resp, err := p.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Kind != "localdb" {
		t.Errorf("Kind = %q, want localdb", resp.Kind)
	}
	if resp.RootGridId == "" {
		t.Errorf("RootGridId = %q, want non-empty", resp.RootGridId)
	}
	if !resp.HasSession {
		t.Error("expected HasSession=true")
	}
}

func TestGetGrid_ReturnsGrid(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	root := rootGrid(t, p)
	resp, err := p.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Grid.Id != root {
		t.Errorf("grid id = %q, want %q", resp.Grid.Id, root)
	}
}

func TestProbe_Present(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	tile := createText(t, p, rootGrid(t, p), []byte("hello"))
	pr, err := p.Probe(ctx, &gridwellv1.ProbeRequest{TileId: tile.Id})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Presence != gridwellv1.ProbeResponse_PRESENCE_PRESENT {
		t.Errorf("Presence = %v, want PRESENT", pr.Presence)
	}
}

func TestProbe_Gone(t *testing.T) {
	p := openPlugin(t)
	pr, err := p.Probe(context.Background(), &gridwellv1.ProbeRequest{TileId: "999999"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Presence != gridwellv1.ProbeResponse_PRESENCE_GONE {
		t.Errorf("Presence = %v, want GONE", pr.Presence)
	}
}

func TestCreateWell_ThenGetGrid(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	root := rootGrid(t, p)

	cr, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: root,
		Tile:   &gridwellv1.Tile{Kind: "well", X: 0, Y: 0, W: 4, H: 4},
	})
	if err != nil {
		t.Fatalf("CreateTile(well): %v", err)
	}
	if cr.Tile.Kind != "well" {
		t.Errorf("Kind = %q, want well", cr.Tile.Kind)
	}

	gr, err := p.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tile := range gr.Tiles {
		if tile.Id == cr.Tile.Id {
			found = true
		}
	}
	if !found {
		t.Error("created well not found in GetGrid response")
	}
}

func TestDeleteTile_RemovesTile(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	tile := createText(t, p, rootGrid(t, p), []byte("bye"))
	_, err := p.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{
		TileId:  tile.Id,
		Version: tile.Version,
	})
	if err != nil {
		t.Fatalf("DeleteTile: %v", err)
	}
	pr, _ := p.Probe(ctx, &gridwellv1.ProbeRequest{TileId: tile.Id})
	if pr.Presence != gridwellv1.ProbeResponse_PRESENCE_GONE {
		t.Error("tile still PRESENT after DeleteTile")
	}
}

func TestShellSessionAlive_NoShellHost(t *testing.T) {
	p := openPlugin(t) // shell = nil
	resp, err := p.ShellSessionAlive(context.Background(), &gridwellv1.ShellSessionAliveRequest{TileId: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Alive {
		t.Error("expected Alive=false with no shell host")
	}
}

// fakeStreamer is a shellsvc.Streamer stub: it reports a session is alive but
// never opens a real PTY, so the manager can be exercised without tmux.
type fakeStreamer struct{ alive bool }

func (f *fakeStreamer) OpenSession(string, tmux.Mode, uint16, uint16) (shellsvc.Session, error) {
	return nil, nil
}
func (f *fakeStreamer) HasSession(string) (bool, error)    { return f.alive, nil }
func (f *fakeStreamer) Kill(string) error                  { return nil }
func (f *fakeStreamer) ListLiveTileIDs() ([]string, error) { return nil, nil }
func (f *fakeStreamer) PaneCommand(string) (string, error) { return "", nil }

func TestShellSessionAlive_WithShellHost(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	p := localdb.New(st, shellsvc.NewManager(&fakeStreamer{alive: true}))

	resp, err := p.ShellSessionAlive(context.Background(), &gridwellv1.ShellSessionAliveRequest{TileId: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Alive {
		t.Error("expected Alive=true from the fake streamer")
	}
}

// TestCreateWell_InteriorVsExit: a well CreateTile with no child_grid_id
// allocates an interior child grid; with a qualified child_grid_id it stores a
// cross-plugin exit well pointing at that grid (no interior grid, the reference
// verbatim). alt_text is the exit well's label.
func TestCreateWell_InteriorVsExit(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	root := rootGrid(t, p)

	interior, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: root,
		Tile:   &gridwellv1.Tile{Kind: "well", X: 0, Y: 0, W: 1, H: 1},
	})
	if err != nil {
		t.Fatalf("interior CreateTile: %v", err)
	}
	if interior.Tile.ChildGridId == "" || interior.Tile.ChildGridId == "0" {
		t.Errorf("interior well child = %q, want a fresh local grid", interior.Tile.ChildGridId)
	}

	exit, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: root,
		Tile: &gridwellv1.Tile{
			Kind: "well", X: 2, Y: 0, W: 1, H: 1,
			ChildGridId: "other-plugin-uuid/9", AltText: "mounted",
		},
	})
	if err != nil {
		t.Fatalf("exit CreateTile: %v", err)
	}
	if exit.Tile.ChildGridId != "other-plugin-uuid/9" {
		t.Errorf("exit well child = %q, want verbatim cross-plugin ref", exit.Tile.ChildGridId)
	}
	if exit.Tile.AltText != "mounted" {
		t.Errorf("exit well label = %q, want mounted", exit.Tile.AltText)
	}
}

// TestGetTileContent_ReturnsBody: a text tile's body is fetched through the
// plugin (delegating to the store's blob).
func TestGetTileContent_ReturnsBody(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	txt := createText(t, p, rootGrid(t, p), []byte("# hello"))
	resp, err := p.GetTileContent(ctx, &gridwellv1.GetTileContentRequest{TileId: txt.Id})
	if err != nil {
		t.Fatalf("GetTileContent: %v", err)
	}
	if string(resp.Data) != "# hello" {
		t.Errorf("content = %q, want %q", resp.Data, "# hello")
	}
}

// TestGetTileAndSetTileAlt: GetTile reads a tile's metadata; SetTileAlt stamps
// its label and returns the updated tile.
func TestGetTileAndSetTileAlt(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	txt := createText(t, p, rootGrid(t, p), []byte("# hi"))

	got, err := p.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: txt.Id})
	if err != nil {
		t.Fatalf("GetTile: %v", err)
	}
	if got.Tile.Id != txt.Id || got.Tile.Kind != "text" {
		t.Errorf("GetTile = %+v, want the text tile", got.Tile)
	}

	stamped, err := p.SetTileAlt(ctx, &gridwellv1.SetTileAltRequest{TileId: txt.Id, Alt: "claude"})
	if err != nil {
		t.Fatalf("SetTileAlt: %v", err)
	}
	if stamped.Tile.AltText != "claude" {
		t.Errorf("alt = %q, want claude", stamped.Tile.AltText)
	}
}

// TestSetTile_WellFramingNoVersionBump: framing writes (well view) do not bump
// version; a content writeback (url freeze) does. This pins the version rule
// that the merged SetTile inherits from the store operations it dispatches to.
func TestSetTile_WellFramingNoVersionBump(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	root := rootGrid(t, p)

	well, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: root,
		Tile:   &gridwellv1.Tile{Kind: "well", X: 0, Y: 0, W: 1, H: 1},
	})
	if err != nil {
		t.Fatalf("CreateTile(well): %v", err)
	}
	set, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{
		TileId:  well.Tile.Id,
		Version: well.Tile.Version,
		Tile:    &gridwellv1.Tile{Kind: "well", ViewX: 5, ViewY: 6, ViewZoom: 2},
	})
	if err != nil {
		t.Fatalf("SetTile(well framing): %v", err)
	}
	if set.Tile.Version != well.Tile.Version {
		t.Errorf("framing bumped version: %d → %d, want unchanged", well.Tile.Version, set.Tile.Version)
	}
	if set.Tile.ViewX != 5 || set.Tile.ViewY != 6 || set.Tile.ViewZoom != 2 {
		t.Errorf("framing not persisted: %+v", set.Tile)
	}
}
