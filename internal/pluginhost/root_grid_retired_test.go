package pluginhost_test

import (
	"context"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// handshakePlugin answers whatever handshake the test hands it, so a test can
// present the old shape and the new one over the same source.
type handshakePlugin struct {
	pluginv1.UnimplementedPluginServer
	info *pluginv1.InfoResponse
}

func (p handshakePlugin) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return p.info, nil
}

func (handshakePlugin) List(_ context.Context, req *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	return &pluginv1.ListResponse{Authoritative: true, Entries: []*pluginv1.Entry{
		{Key: req.Grid + "/one", Kind: "text", Label: "one"},
	}}, nil
}

// adapterOver stands the adapter up over one handshake and a store path, so
// two handshakes can be served over the SAME store.
func adapterOver(t *testing.T, memPath string, info *pluginv1.InfoResponse) *pluginhost.Adapter {
	t.Helper()
	memStore, err := store.Open(memPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp, closer, err := plugintest.Loopback(handshakePlugin{info: info})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	return pluginhost.New(cp, memStore.Namespace("p1"), nil)
}

// The one compat derivation. A plugin built before grids were declared
// answers a root_grid and no declared grids; the node turns that into its
// single grid, so the plugin still presents without being rebuilt. The
// derived entry declares no label and no glyph, so the swatch reads as the
// configured instance — which is the whole identity of a plugin with one
// grid.
func TestALegacyRootGridBecomesTheOneGrid(t *testing.T) {
	a := adapterOver(t, filepath.Join(t.TempDir(), "mem.db"), &pluginv1.InfoResponse{
		Kind: "fs", DisplayName: "files", RootGrid: ".",
	})
	info, err := a.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if info.RootGridId != "" {
		t.Errorf("the node declared a landing of its own: %q", info.RootGridId)
	}
	if len(info.PluginGrids) != 1 {
		t.Fatalf("declared grids = %+v, want the root gridKey derived into one", info.PluginGrids)
	}
	e := info.PluginGrids[0]
	if e.Id != "." || e.GridId == "" {
		t.Errorf("derived entry = %+v, want the root gridKey, resolved", e)
	}
	if e.Label != "" || e.Glyph != "" {
		t.Errorf("derived entry = %+v, want no glyph of its own so it wears the plugin's", e)
	}
	if _, err := a.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: e.GridId}); err != nil {
		t.Errorf("the derived grid does not serve: %v", err)
	}
}

// A plugin that declares both has said what its grids are, and the
// root gets no privilege among them: there is no privileged grid. A
// root_grid that names a grid nobody declared simply goes nowhere.
func TestDeclaredEntriesWinOverALingeringRootGrid(t *testing.T) {
	a := adapterOver(t, filepath.Join(t.TempDir(), "mem.db"), &pluginv1.InfoResponse{
		Kind: "mail", DisplayName: "mail", RootGrid: "imbox",
		Grids: []*pluginv1.PluginGrid{
			{Id: "feed", Label: "Feed", Grid: "feed"},
			{Id: "aside", Label: "Set Aside", Grid: "aside"},
		},
	})
	info, err := a.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if info.RootGridId != "" {
		t.Errorf("the node declared a landing of its own: %q", info.RootGridId)
	}
	if len(info.PluginGrids) != 2 ||
		info.PluginGrids[0].Id != "feed" || info.PluginGrids[1].Id != "aside" {
		t.Fatalf("declared grids = %+v, want exactly the two declared", info.PluginGrids)
	}
}

// Things stay as the user left them. A tile the user placed in a plugin's
// former root grid keeps resolving after the plugin stops declaring a root
// and declares that same grid in grids: the grid key did not
// change, so the minted grid id and the minted row do not either. The old
// shape and the new one are served over one store, which is the only way to
// see it — either handshake alone looks fine.
func TestALandingMintedUnderRootGridStillResolvesAsAGrid(t *testing.T) {
	ctx := context.Background()
	memPath := filepath.Join(t.TempDir(), "mem.db")

	// The old shape: the plugin declares a landing, and the user frames it,
	// which is a durable fact the node mints a row for.
	old := adapterOver(t, memPath, &pluginv1.InfoResponse{
		Kind: "fs", DisplayName: "files", RootGrid: ".",
	})
	before, err := old.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	landing := plugintest.Landing(t, before)
	if _, err := old.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		RootGridId: landing, Cx: 2, Cy: 3, Zoom: 1.25,
	}); err != nil {
		t.Fatal(err)
	}

	// The new shape over the same store: the same grid, now declared as a
	// grid.
	migrated := adapterOver(t, memPath, &pluginv1.InfoResponse{
		Kind: "fs", DisplayName: "files",
		Grids: []*pluginv1.PluginGrid{{Id: ".", Grid: "."}},
	})
	after, err := migrated.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	e := after.PluginGrids[0]
	if e.GridId != landing {
		t.Fatalf("the declared grid's id moved: %q, was %q — every link to it is now dead", e.GridId, landing)
	}
	if e.ViewCx != 2 || e.ViewCy != 3 || e.ViewZoom != 1.25 {
		t.Errorf("the framing the user left is gone: %+v", e)
	}
	if _, err := migrated.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: landing}); err != nil {
		t.Errorf("a tile pointing at the former landing no longer descends: %v", err)
	}
}
