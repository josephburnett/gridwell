package server

import (
	"context"
	"path/filepath"
	"sync"
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

// retitledSource lists a door whose label the test changes, as a source
// renames a thing behind the node's back.
type retitledSource struct {
	pluginv1.UnimplementedPluginServer
	mu    sync.Mutex
	label string
}

func (s *retitledSource) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "retitled", DisplayName: "retitled",
		Grids: []*pluginv1.PluginGrid{{Id: "all", Label: "All", Grid: "all"}}}, nil
}

func (s *retitledSource) List(_ context.Context, req *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	if req.Grid != "all" {
		return &pluginv1.ListResponse{Authoritative: true}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return &pluginv1.ListResponse{Authoritative: true, Entries: []*pluginv1.Entry{
		{Key: "d", Kind: rpc.KindDoor, Label: "door " + s.label, ChildGrid: "inside"},
	}}, nil
}

// A plugin tile renamed through the web API keeps the typed name when its
// source retitles it, and a copy onto home carries that name, as a copy of
// one of home's tiles does.
func TestARenamedPluginDoorKeepsItsNameAndCopiesWithIt(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	_, home := registerPrimaryLocaldb(t, reg, st)
	src := &retitledSource{label: "old"}
	cp, closer, err := plugintest.Loopback(src)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	const uuid = "retitled-uuid"
	a, stop := pluginhost.Start(cp, st.Namespace(uuid), nil, "retitled watch")
	t.Cleanup(stop)
	reg.Register(uuid, "retitled", a, nil)
	h := serveWeb(t, mustNew(t, reg, Config{}))
	cl := rpc.NewClient(h.Client(), h.URL, connect.WithProtoJSON())

	grid := rpc.QualifyID(uuid, rpc.EntryGridID("all"))
	door := func() *gridwellv1.Tile {
		t.Helper()
		g, err := cl.GetGrid(ctx, grid)
		if err != nil {
			t.Fatal(err)
		}
		if len(g.Tiles) != 1 {
			t.Fatalf("grid = %+v, want the one door", g.Tiles)
		}
		return g.Tiles[0]
	}
	d := door()
	renamed, err := cl.RenameTile(ctx, d.Id, d.Version, "mine")
	if err != nil {
		t.Fatalf("rename the plugin's door: %v", err)
	}
	if renamed.AltText != "mine" || renamed.Id != d.Id {
		t.Fatalf("rename answered %q as %q, want the typed name as %q", renamed.AltText, renamed.Id, d.Id)
	}

	src.mu.Lock()
	src.label = "new"
	src.mu.Unlock()
	d = door()
	if d.AltText != "mine" {
		t.Fatalf("the source retitled the door and it reads %q, want the typed name", d.AltText)
	}

	copied, err := cl.CopyTile(ctx, &gridwellv1.CopyTileRequest{TileId: d.Id, DestGridId: home, X: 4, Y: 4})
	if err != nil {
		t.Fatalf("copy the renamed door onto home: %v", err)
	}
	if copied.AltText != "mine" {
		t.Fatalf("the copy is named %q, want the typed name it was copied with", copied.AltText)
	}
}
