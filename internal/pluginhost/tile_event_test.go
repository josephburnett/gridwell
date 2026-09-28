package pluginhost

import (
	"context"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// textAndURLPlugin lists one text entry and one url entry in its collection.
type textAndURLPlugin struct{ oneEntryPlugin }

func (textAndURLPlugin) List(context.Context, *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	return &pluginv1.ListResponse{Authoritative: true, Entries: []*pluginv1.Entry{
		{Key: "a", Kind: rpc.KindText, Label: "a"},
		{Key: "u", Kind: rpc.KindURL, Label: "u", UrlString: "https://example.com/"},
	}}, nil
}

// Every SetTile arm is a fact on one tile row that changes no listing, so each
// announces exactly that tile, as home does, never GridChanged on its grid,
// which would refetch the whole listing on every zoom, view or capture.
func TestTileWritesAnnounceTheTileNotAGridChange(t *testing.T) {
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp, closer, err := plugintest.Loopback(textAndURLPlugin{})
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
	if err != nil || len(listing.Tiles) != 2 {
		t.Fatalf("listing = %v, %v; want the two entries", listing, err)
	}
	ids := map[string]string{}
	for _, tile := range listing.Tiles {
		ids[tile.Kind] = tile.Id
	}

	seen := make(chan *gridwellv1.Event, 64)
	go func() {
		_ = a.Subscribe(ctx, &gridwellv1.SubscribeRequest{}, func(ev *gridwellv1.Event) error {
			seen <- ev
			return nil
		})
	}()
	awaitSubscriber(t, a, seen)

	zoom, frozen := 1.5, true
	for _, tc := range []struct {
		name string
		req  *gridwellv1.SetTileRequest
	}{
		{"content zoom", &gridwellv1.SetTileRequest{TileId: ids[rpc.KindText], ContentZoom: &zoom}},
		{"text view", &gridwellv1.SetTileRequest{TileId: ids[rpc.KindText],
			Tile: &gridwellv1.Tile{Kind: rpc.KindText, TextX: 1, TextY: 2, TextW: 30, TextH: 40, TextMode: rpc.TextModeRendered}}},
		{"url frozen", &gridwellv1.SetTileRequest{TileId: ids[rpc.KindURL], UrlFrozen: &frozen}},
		{"url screenshot", &gridwellv1.SetTileRequest{TileId: ids[rpc.KindURL],
			Tile: &gridwellv1.Tile{Kind: rpc.KindURL}, Preview: []byte("\xff\xd8screenshot")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := a.SetTile(ctx, tc.req)
			if err != nil {
				t.Fatal(err)
			}
			evs := collect(seen)
			if len(evs) != 1 || evs[0].GetTileChanged() == nil {
				t.Fatalf("announced %v, want exactly one TileChanged", evs)
			}
			got := evs[0].GetTileChanged().GetTile()
			if got.GetId() != tc.req.TileId || got.GetGridId() != grid {
				t.Errorf("TileChanged names tile %q in grid %q, want %q in %q", got.GetId(), got.GetGridId(), tc.req.TileId, grid)
			}
			if !proto.Equal(got, resp.GetTile()) {
				t.Errorf("the event's tile %v differs from the write's answer %v", got, resp.GetTile())
			}
		})
	}
}
