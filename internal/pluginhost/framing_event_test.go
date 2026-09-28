package pluginhost

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// oneEntryPlugin declares one collection and lists one text entry in it.
type oneEntryPlugin struct {
	pluginv1.UnimplementedPluginServer
}

func (oneEntryPlugin) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "notes", DisplayName: "notes",
		MenuEntries: []*pluginv1.MenuEntry{{Id: "all", Label: "All", Context: "all"}}}, nil
}

func (oneEntryPlugin) List(context.Context, *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	return &pluginv1.ListResponse{Authoritative: true,
		Entries: []*pluginv1.Entry{{Key: "a", Kind: "text", Label: "a"}}}, nil
}

// wellPlugin lists one well in its collection, opening onto a second context.
type wellPlugin struct{ oneEntryPlugin }

func (wellPlugin) List(_ context.Context, req *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	if req.Context != "all" {
		return &pluginv1.ListResponse{Authoritative: true}, nil
	}
	return &pluginv1.ListResponse{Authoritative: true,
		Entries: []*pluginv1.Entry{{Key: "w", Kind: rpc.KindWell, Label: "w", ChildContext: "inner"}}}, nil
}

// A collection's framing changes no listing, so framing its grid announces the
// three numbers and never GridChanged, which costs every client a refetch per
// pan. A layout write on the same grid still announces GridChanged. Both run
// over the adapter's own stream, the one the router relays.
func TestRootFramingAnnouncesItsFramingNotAGridChange(t *testing.T) {
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp, closer, err := plugintest.Loopback(oneEntryPlugin{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	a := New(cp, memStore.Namespace("p1"), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	info, err := a.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	grid := info.MenuEntries[0].GridId
	listing, err := a.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: grid})
	if err != nil || len(listing.Tiles) != 1 {
		t.Fatalf("listing = %v, %v; want the one entry", listing, err)
	}

	seen := make(chan *gridwellv1.Event, 64)
	go func() {
		_ = a.Subscribe(ctx, &gridwellv1.SubscribeRequest{}, func(ev *gridwellv1.Event) error {
			seen <- ev
			return nil
		})
	}()
	awaitSubscriber(t, a, seen)

	if _, err := a.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		RootGridId: grid, Cx: 3.5, Cy: -2.25, Zoom: 1.75,
	}); err != nil {
		t.Fatal(err)
	}
	evs := collect(seen)
	want := &gridwellv1.GridFramingChanged{GridId: grid, ViewCx: 3.5, ViewCy: -2.25, ViewZoom: 1.75}
	if len(evs) != 1 || !proto.Equal(evs[0].GetGridFramingChanged(), want) {
		t.Errorf("framing a collection announced %v, want exactly %v", evs, want)
	}

	if _, err := a.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: listing.Tiles[0].Id, X: 4, Y: 4, W: 1, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	evs = collect(seen)
	if len(evs) != 1 || evs[0].GetGridChanged().GetGridId() != grid {
		t.Errorf("a layout write announced %v, want one GridChanged(%s)", evs, grid)
	}
}

// A well's framing is a fact on its tile row, so framing inside a plugin well
// announces that one tile, as home does, and never GridChanged on the grid
// holding it, which would refetch the parent per pan.
func TestWellFramingAnnouncesTheTileNotAGridChange(t *testing.T) {
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp, closer, err := plugintest.Loopback(wellPlugin{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	a := New(cp, memStore.Namespace("p1"), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	info, err := a.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	grid := info.MenuEntries[0].GridId
	listing, err := a.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: grid})
	if err != nil || len(listing.Tiles) != 1 {
		t.Fatalf("listing = %v, %v; want the one well", listing, err)
	}
	well := listing.Tiles[0]

	seen := make(chan *gridwellv1.Event, 64)
	go func() {
		_ = a.Subscribe(ctx, &gridwellv1.SubscribeRequest{}, func(ev *gridwellv1.Event) error {
			seen <- ev
			return nil
		})
	}()
	awaitSubscriber(t, a, seen)

	resp, err := a.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		TileId: well.Id, Cx: 1.5, Cy: 2.5, Zoom: 0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(seen)
	if len(evs) != 1 || evs[0].GetTileChanged() == nil {
		t.Fatalf("framing a well announced %v, want exactly one TileChanged", evs)
	}
	got := evs[0].GetTileChanged().GetTile()
	if got.GetId() != well.Id || got.GetGridId() != grid {
		t.Errorf("TileChanged names tile %q in grid %q, want %q in %q", got.GetId(), got.GetGridId(), well.Id, grid)
	}
	if got.GetViewCx() != 1.5 || got.GetViewCy() != 2.5 || got.GetViewZoom() != 0.5 {
		t.Errorf("TileChanged carries framing (%v, %v, %v), want the write's (1.5, 2.5, 0.5)",
			got.GetViewCx(), got.GetViewCy(), got.GetViewZoom())
	}
	if !proto.Equal(got, resp.GetTile()) {
		t.Errorf("the event's tile %v differs from the write's answer %v", got, resp.GetTile())
	}

	if _, err := a.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: well.Id, X: 4, Y: 4, W: 1, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	evs = collect(seen)
	if len(evs) != 1 || evs[0].GetGridChanged().GetGridId() != grid {
		t.Errorf("a layout write announced %v, want one GridChanged(%s)", evs, grid)
	}
}

// collect drains what the stream delivers in the next short window.
func collect(seen <-chan *gridwellv1.Event) []*gridwellv1.Event {
	var out []*gridwellv1.Event
	for {
		select {
		case ev := <-seen:
			out = append(out, ev)
		case <-time.After(100 * time.Millisecond):
			return out
		}
	}
}
