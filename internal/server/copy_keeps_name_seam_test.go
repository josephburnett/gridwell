package server

import (
	"context"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// pagesSource lists two url entries, each labelled by the source.
type pagesSource struct {
	pluginv1.UnimplementedPluginServer
}

func (pagesSource) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "pages", DisplayName: "pages",
		Grids: []*pluginv1.PluginGrid{{Id: "all", Label: "All", Grid: "all"}}}, nil
}

func (pagesSource) List(_ context.Context, req *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	if req.Grid != "all" {
		return &pluginv1.ListResponse{Authoritative: true}, nil
	}
	return &pluginv1.ListResponse{Authoritative: true, Entries: []*pluginv1.Entry{
		{Key: "a", Kind: rpc.KindURL, Label: "Source A", UrlString: "https://example.com/a"},
		{Key: "b", Kind: rpc.KindURL, Label: "Source B", UrlString: "https://example.com/b"},
	}}, nil
}

// A plugin's url copied onto home keeps the name the copier saw, the source's
// or a typed one, and a later page-title capture on the copy does not replace
// it (CLAUDE.md, 2026-10-20).
func TestAPluginURLCopiedOntoHomeKeepsItsName(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	_, home := registerPrimaryLocaldb(t, reg, st)
	cp, closer, err := plugintest.Loopback(pagesSource{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	const uuid = "pages-uuid"
	a, stop := pluginhost.Start(cp, st.Namespace(uuid), nil, "pages watch")
	t.Cleanup(stop)
	reg.Register(uuid, "pages", a, nil)
	h := serveWeb(t, mustNew(t, reg, Config{}))
	cl := rpc.NewClient(h.Client(), h.URL, connect.WithProtoJSON())

	g, err := cl.GetGrid(ctx, rpc.QualifyID(uuid, rpc.EntryGridID("all")))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*gridwellv1.Tile{}
	for _, tl := range g.Tiles {
		byName[tl.AltText] = tl
	}
	src, plain := byName["Source A"], byName["Source B"]
	if src == nil || plain == nil {
		t.Fatalf("grid = %+v, want the two url entries", g.Tiles)
	}
	renamed, err := cl.RenameTile(ctx, src.Id, src.Version, "mine")
	if err != nil {
		t.Fatalf("rename the plugin's url: %v", err)
	}

	for i, c := range []struct {
		from *gridwellv1.Tile
		want string
	}{
		{renamed, "mine"},
		{plain, "Source B"},
	} {
		copied, err := cl.CopyTile(ctx, &gridwellv1.CopyTileRequest{TileId: c.from.Id, DestGridId: home, X: int64(4 + i), Y: 4})
		if err != nil {
			t.Fatalf("copy %q onto home: %v", c.want, err)
		}
		if copied.AltText != c.want {
			t.Fatalf("the copy is named %q, want %q, the name it was copied with", copied.AltText, c.want)
		}
		if _, err := cl.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: copied.Id,
			Tile: &gridwellv1.Tile{Kind: rpc.KindURL, AltText: "Page title"}}); err != nil {
			t.Fatalf("capture the copy's page title: %v", err)
		}
		got, err := cl.GetTile(ctx, copied.Id)
		if err != nil {
			t.Fatal(err)
		}
		if got.AltText != c.want {
			t.Fatalf("a page-title capture renamed the copy to %q, want %q", got.AltText, c.want)
		}
	}
}

// Every kind a copy carries into another namespace keeps the name its source
// had: a url and a door as copies, a shell and a link as links.
func TestEveryKindCopiedAcrossNamespacesKeepsItsName(t *testing.T) {
	cl, _, rootA, _, rootB := twoPluginServer(t)
	ctx := context.Background()
	create := func(tl *gridwellv1.Tile) *gridwellv1.Tile {
		t.Helper()
		created, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rootA, Tile: tl})
		if err != nil {
			t.Fatalf("create %s: %v", tl.Kind, err)
		}
		return created
	}
	named := func(tl *gridwellv1.Tile, name string) *gridwellv1.Tile {
		t.Helper()
		renamed, err := cl.RenameTile(ctx, tl.Id, tl.Version, name)
		if err != nil {
			t.Fatalf("rename %s: %v", tl.Kind, err)
		}
		return renamed
	}
	target := create(&gridwellv1.Tile{Kind: rpc.KindText, X: 9, Y: 9, W: 1, H: 1})
	for i, src := range []*gridwellv1.Tile{
		named(create(&gridwellv1.Tile{Kind: rpc.KindURL, X: 0, Y: 0, W: 1, H: 1, UrlString: "https://example.com/"}), "url name"),
		named(create(&gridwellv1.Tile{Kind: rpc.KindShell, X: 1, Y: 0, W: 1, H: 1}), "shell name"),
		create(&gridwellv1.Tile{Kind: rpc.KindDoor, X: 2, Y: 0, W: 1, H: 1, AltText: "door name"}),
		create(&gridwellv1.Tile{Kind: rpc.KindText, X: 3, Y: 0, W: 1, H: 1, LinkTargetId: target.Id, AltText: "link name"}),
	} {
		copied, err := cl.CopyTile(ctx, &gridwellv1.CopyTileRequest{TileId: src.Id, DestGridId: rootB, X: int64(i), Y: 5})
		if err != nil {
			t.Fatalf("copy %s %q: %v", src.Kind, src.AltText, err)
		}
		if copied.AltText != src.AltText {
			t.Errorf("a %s named %q copied across namespaces is named %q", src.Kind, src.AltText, copied.AltText)
		}
	}
}
