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

// gridsPlugin declares its grids the way a plugin does: one + menu
// row each, and no landing grid of its own.
type gridsPlugin struct {
	pluginv1.UnimplementedPluginServer
}

func (gridsPlugin) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{
		Kind: "mail", DisplayName: "mail",
		Grids: []*pluginv1.PluginGrid{
			{Id: "imbox", Label: "Imbox", Grid: "imbox"},
			{Id: "feed", Label: "Feed", Grid: "feed"},
		},
	}, nil
}

func (gridsPlugin) List(context.Context, *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	return &pluginv1.ListResponse{Authoritative: true}, nil
}

// A grid is a door, and a door remembers the view it was left at.
// The framing write and the handshake read are two sides of one fact, and
// nothing between them knows which door a grid was declared by, so this
// crosses the adapter/store seam: frame one grid, hand back a
// fresh handshake, and read it off that entry — and only that entry.
func TestAPluginGridRemembersItsFraming(t *testing.T) {
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp, closer, err := plugintest.Loopback(gridsPlugin{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	a := pluginhost.New(cp, memStore.Namespace("p1"), nil)
	ctx := context.Background()

	before, err := a.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.PluginGrids) != 2 {
		t.Fatalf("entries = %+v, want the two grids", before.PluginGrids)
	}
	if before.RootGridId != "" {
		t.Fatalf("a plugin that declares no landing must name no grid of its own, got %q", before.RootGridId)
	}
	for _, e := range before.PluginGrids {
		if e.GridId == "" {
			t.Fatalf("entry %q resolved no grid", e.Id)
		}
		if e.ViewZoom != 0 {
			t.Fatalf("entry %q was never visited but carries a view: %+v", e.Id, e)
		}
	}

	feed := before.PluginGrids[1]
	if _, err := a.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		RootGridId: feed.GridId, Cx: 3.5, Cy: -2.25, Zoom: 1.75,
	}); err != nil {
		t.Fatal(err)
	}

	after, err := a.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got := after.PluginGrids[1]
	if got.ViewCx != 3.5 || got.ViewCy != -2.25 || got.ViewZoom != 1.75 {
		t.Errorf("Feed's entry = %+v, want the framing it was left at", got)
	}
	if other := after.PluginGrids[0]; other.ViewZoom != 0 {
		t.Errorf("framing one grid moved another: %+v", other)
	}
	if got.GridId != feed.GridId {
		t.Errorf("grid id = %q, want the same grid the framing was written to (%q)", got.GridId, feed.GridId)
	}
}

// A store the handshake cannot read is an error on the handshake, not a
// grid reopening at zero framing with nothing said.
func TestAnUnreadableStoreFailsTheHandshake(t *testing.T) {
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	cp, closer, err := plugintest.Loopback(gridsPlugin{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	a := pluginhost.New(cp, memStore.Namespace("p1"), nil)
	ctx := context.Background()
	if _, err := a.Info(ctx, &gridwellv1.InfoRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := memStore.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Info(ctx, &gridwellv1.InfoRequest{}); err == nil {
		t.Fatal("Info over a closed store answered; the store failure must surface")
	}
}
