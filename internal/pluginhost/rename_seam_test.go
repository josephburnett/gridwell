package pluginhost

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// retitlingPlugin lists a url and a door whose labels the test changes, as a
// source renames a thing behind the node's back.
type retitlingPlugin struct {
	oneEntryPlugin
	mu    sync.Mutex
	label string
}

func (p *retitlingPlugin) List(_ context.Context, req *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	if req.Grid != "all" {
		return &pluginv1.ListResponse{Authoritative: true}, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return &pluginv1.ListResponse{Authoritative: true, Entries: []*pluginv1.Entry{
		{Key: "u", Kind: rpc.KindURL, Label: "u " + p.label, UrlString: "https://example.com/"},
		{Key: "d", Kind: rpc.KindDoor, Label: "d " + p.label, ChildGrid: "inside"},
		{Key: "t", Kind: rpc.KindText, Label: "t " + p.label},
	}}, nil
}

func (p *retitlingPlugin) retitle(label string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.label = label
}

// A plugin tile is renamed on the node's row, as home's is, and the plugin is
// never told: the typed name stands when the source retitles the thing, and
// an entry the user never renamed follows the source.
func TestARenamedPluginTileKeepsTheTypedName(t *testing.T) {
	ctx := context.Background()
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	p := &retitlingPlugin{label: "old"}
	cp, closer, err := plugintest.Loopback(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	a := New(cp, memStore.Namespace("p1"), nil)
	info, err := a.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	grid := info.PluginGrids[0].GridId
	byKind := func() map[string]*gridwellv1.Tile {
		t.Helper()
		g, err := a.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: grid})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]*gridwellv1.Tile{}
		for _, tile := range g.Tiles {
			out[tile.Kind] = tile
		}
		return out
	}
	rows := byKind()

	for _, kind := range []string{rpc.KindURL, rpc.KindDoor} {
		row := rows[kind]
		resp, err := a.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: row.Id, Version: row.Version, Rename: "mine " + kind})
		if err != nil {
			t.Fatalf("rename the %s: %v", kind, err)
		}
		if resp.Tile.AltText != "mine "+kind || resp.Tile.Version != row.Version+1 || resp.Tile.Id != row.Id {
			t.Fatalf("the %s's rename answered %q at version %d as %q, want the typed name at %d as %q",
				kind, resp.Tile.AltText, resp.Tile.Version, resp.Tile.Id, row.Version+1, row.Id)
		}
		_, err = a.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: row.Id, Version: row.Version, Rename: "stale"})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("a rename claiming the version before the first: got %v, want the conflict", err)
		}
	}
	_, err = a.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: rows[rpc.KindText].Id, Rename: "nope"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("a text rename: got %v, want it refused, its name being its source's", err)
	}

	p.retitle("new")
	after := byKind()
	for _, kind := range []string{rpc.KindURL, rpc.KindDoor} {
		if got := after[kind].AltText; got != "mine "+kind {
			t.Errorf("the source retitled the %s and its name became %q, want the typed %q", kind, got, "mine "+kind)
		}
	}
	if got := after[rpc.KindText].AltText; got != "t new" {
		t.Errorf("the unrenamed text is named %q, want the source's new label", got)
	}
}
