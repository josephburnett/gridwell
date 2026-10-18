package pluginhost

import (
	"context"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// A write to a plugin's row that lands where the row already is announces
// nothing, as home's does (store.TestARepeatedWriteWritesNothing): a tile
// behaves the same wherever its content comes from.
func TestARepeatedPluginWriteAnnouncesNothing(t *testing.T) {
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

	setTile := func(req *gridwellv1.SetTileRequest) func() error {
		return func() error { _, err := a.SetTile(ctx, req); return err }
	}
	zoom, frozen := 1.5, true
	for _, tc := range []struct {
		name  string
		write func() error
	}{
		{"content zoom", setTile(&gridwellv1.SetTileRequest{TileId: ids[rpc.KindText], ContentZoom: &zoom})},
		{"text scroll", setTile(&gridwellv1.SetTileRequest{TileId: ids[rpc.KindText],
			Tile: &gridwellv1.Tile{Kind: rpc.KindText, TextX: 1, TextY: 2, TextW: 30, TextH: 40, TextMode: rpc.TextModeRendered}})},
		{"url frozen", setTile(&gridwellv1.SetTileRequest{TileId: ids[rpc.KindURL], UrlFrozen: &frozen})},
		{"url screenshot", setTile(&gridwellv1.SetTileRequest{TileId: ids[rpc.KindURL],
			Tile: &gridwellv1.Tile{Kind: rpc.KindURL}, Preview: []byte("\xff\xd8screenshot")})},
		{"placement", func() error {
			_, err := a.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: ids[rpc.KindText], X: 7, Y: 7, W: 1, H: 1})
			return err
		}},
		{"root framing", func() error {
			_, err := a.SetFraming(ctx, &gridwellv1.SetFramingRequest{RootGridId: grid, Cx: 3.5, Cy: -2.25, Zoom: 1.75})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.write(); err != nil {
				t.Fatalf("first: %v", err)
			}
			if evs := collect(seen); len(evs) != 1 {
				t.Fatalf("the first write announced %v, want one event", evs)
			}
			if err := tc.write(); err != nil {
				t.Fatalf("repeat: %v", err)
			}
			if evs := collect(seen); len(evs) != 0 {
				t.Errorf("the repeat announced %v, want nothing", evs)
			}
		})
	}
}
