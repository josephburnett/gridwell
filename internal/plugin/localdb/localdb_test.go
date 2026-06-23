package localdb_test

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin/localdb"
	"github.com/josephburnett/gridwell/internal/store"
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

func TestInfo(t *testing.T) {
	p := openPlugin(t)
	resp, err := p.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Kind != "localdb" {
		t.Errorf("Kind = %q, want localdb", resp.Kind)
	}
}

func TestAttach_ReturnsRootGridID(t *testing.T) {
	p := openPlugin(t)
	resp, err := p.Attach(context.Background(), &gridwellv1.AttachRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.RootGridId == "" {
		t.Errorf("RootGridId = %q, want non-empty", resp.RootGridId)
	}
	if !resp.Caps.Write {
		t.Error("expected Write cap")
	}
}

func TestBootstrap_ReturnsRootGridID(t *testing.T) {
	p := openPlugin(t)
	resp, err := p.Bootstrap(context.Background(), &gridwellv1.BootstrapRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.RootGridId == "" {
		t.Errorf("RootGridId = %q, want non-empty", resp.RootGridId)
	}
}

func TestAttach_MatchesBootstrap(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	a, err := p.Attach(ctx, &gridwellv1.AttachRequest{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Bootstrap(ctx, &gridwellv1.BootstrapRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if a.RootGridId != b.RootGridId {
		t.Errorf("Attach.RootGridId=%q != Bootstrap.RootGridId=%q", a.RootGridId, b.RootGridId)
	}
}

func TestGetGrid_ReturnsGrid(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	a, _ := p.Attach(ctx, &gridwellv1.AttachRequest{})
	resp, err := p.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: a.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Grid.Id != a.RootGridId {
		t.Errorf("grid id = %q, want %q", resp.Grid.Id, a.RootGridId)
	}
}

func TestProbe_Present(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	a, _ := p.Attach(ctx, &gridwellv1.AttachRequest{})

	// Create a tile so we have something to probe.
	cr, err := p.CreateText(ctx, &gridwellv1.CreateTextRequest{
		GridId: a.RootGridId,
		X: 0, Y: 0, W: 4, H: 4,
		Data:  []byte("hello"),
	})
	if err != nil {
		t.Fatalf("CreateText: %v", err)
	}
	pr, err := p.Probe(ctx, &gridwellv1.ProbeRequest{TileId: cr.Tile.Id})
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
	a, _ := p.Attach(ctx, &gridwellv1.AttachRequest{})

	cr, err := p.CreateWell(ctx, &gridwellv1.CreateWellRequest{
		GridId: a.RootGridId,
		X: 0, Y: 0, W: 4, H: 4,
	})
	if err != nil {
		t.Fatalf("CreateWell: %v", err)
	}
	if cr.Tile.Kind != "well" {
		t.Errorf("Kind = %q, want well", cr.Tile.Kind)
	}

	gr, err := p.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: a.RootGridId})
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
	a, _ := p.Attach(ctx, &gridwellv1.AttachRequest{})

	cr, _ := p.CreateText(ctx, &gridwellv1.CreateTextRequest{
		GridId: a.RootGridId,
		X: 0, Y: 0, W: 4, H: 4,
		Data:  []byte("bye"),
	})
	_, err := p.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{
		TileId:  cr.Tile.Id,
		Version: cr.Tile.Version,
	})
	if err != nil {
		t.Fatalf("DeleteTile: %v", err)
	}
	pr, _ := p.Probe(ctx, &gridwellv1.ProbeRequest{TileId: cr.Tile.Id})
	if pr.Presence != gridwellv1.ProbeResponse_PRESENCE_GONE {
		t.Error("tile still PRESENT after DeleteTile")
	}
}

func TestShellSessionAlive_NilSess(t *testing.T) {
	p := openPlugin(t) // sess = nil
	resp, err := p.ShellSessionAlive(context.Background(), &gridwellv1.ShellSessionAliveRequest{TileId: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Alive {
		t.Error("expected Alive=false with nil sess")
	}
}

// recordSession records Kill calls for verification.
type recordSession struct {
	killed []int64
}

func (r *recordSession) HasSession(tileID int64) (bool, error) { return true, nil }
func (r *recordSession) Kill(tileID int64) error {
	r.killed = append(r.killed, tileID)
	return nil
}

func TestShellSessionAlive_WithSess(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	sess := &recordSession{}
	p := localdb.New(st, sess)

	resp, err := p.ShellSessionAlive(context.Background(), &gridwellv1.ShellSessionAliveRequest{TileId: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Alive {
		t.Error("expected Alive=true from recordSession.HasSession")
	}
}

func TestSetRootView_RoundTrip(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()

	_, err := p.SetRootView(ctx, &gridwellv1.SetRootViewRequest{Cx: 100, Cy: 200, Zoom: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	boot, err := p.Bootstrap(ctx, &gridwellv1.BootstrapRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if boot.RootViewCx != 100 || boot.RootViewCy != 200 || boot.RootZoom != 1.5 {
		t.Errorf("got cx=%v cy=%v zoom=%v, want 100 200 1.5", boot.RootViewCx, boot.RootViewCy, boot.RootZoom)
	}
}

// TestCreateWell_InteriorVsExit: CreateWell with no child_grid_id allocates an
// interior child grid; with a qualified child_grid_id it stores a cross-plugin
// exit well pointing at that grid (no interior grid, the reference verbatim).
func TestCreateWell_InteriorVsExit(t *testing.T) {
	p := openPlugin(t)
	ctx := context.Background()
	root, err := p.Attach(ctx, &gridwellv1.AttachRequest{})
	if err != nil {
		t.Fatal(err)
	}

	interior, err := p.CreateWell(ctx, &gridwellv1.CreateWellRequest{
		GridId: root.RootGridId, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatalf("interior CreateWell: %v", err)
	}
	if interior.Tile.ChildGridId == "" || interior.Tile.ChildGridId == "0" {
		t.Errorf("interior well child = %q, want a fresh local grid", interior.Tile.ChildGridId)
	}

	exit, err := p.CreateWell(ctx, &gridwellv1.CreateWellRequest{
		GridId: root.RootGridId, X: 2, Y: 0, W: 1, H: 1,
		ChildGridId: "other-plugin-uuid/9", Label: "mounted",
	})
	if err != nil {
		t.Fatalf("exit CreateWell: %v", err)
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
	root, err := p.Attach(ctx, &gridwellv1.AttachRequest{})
	if err != nil {
		t.Fatal(err)
	}
	txt, err := p.CreateText(ctx, &gridwellv1.CreateTextRequest{
		GridId: root.RootGridId, X: 0, Y: 0, W: 1, H: 1, Data: []byte("# hello"),
	})
	if err != nil {
		t.Fatalf("CreateText: %v", err)
	}
	resp, err := p.GetTileContent(ctx, &gridwellv1.GetTileContentRequest{TileId: txt.Tile.Id})
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
	root, err := p.Attach(ctx, &gridwellv1.AttachRequest{})
	if err != nil {
		t.Fatal(err)
	}
	txt, err := p.CreateText(ctx, &gridwellv1.CreateTextRequest{
		GridId: root.RootGridId, X: 0, Y: 0, W: 1, H: 1, Data: []byte("# hi"),
	})
	if err != nil {
		t.Fatalf("CreateText: %v", err)
	}

	got, err := p.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: txt.Tile.Id})
	if err != nil {
		t.Fatalf("GetTile: %v", err)
	}
	if got.Tile.Id != txt.Tile.Id || got.Tile.Kind != "text" {
		t.Errorf("GetTile = %+v, want the text tile", got.Tile)
	}

	stamped, err := p.SetTileAlt(ctx, &gridwellv1.SetTileAltRequest{TileId: txt.Tile.Id, Alt: "claude"})
	if err != nil {
		t.Fatalf("SetTileAlt: %v", err)
	}
	if stamped.Tile.AltText != "claude" {
		t.Errorf("alt = %q, want claude", stamped.Tile.AltText)
	}
}
