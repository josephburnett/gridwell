package pluginhost_test

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/gesture"
)

// A page row its source does not list right now is still the page it was:
// served at the plugin's address, so a click descends into it and never asks
// the user for an address the plugin would refuse. Only the source's word that
// the key is gone changes that, and then the row is dead. The test crosses
// from the plugin's listing through the router to the client's click verdict,
// because each side alone holds: the listing said serves_page, and the click
// rule reads it.
func TestAnUnlistedPageRowStaysAPage(t *testing.T) {
	for _, tc := range []struct {
		name          string
		authoritative bool
		dark          bool
		presence      pluginv1.ProbeResponse_Presence
		dead          bool
	}{
		{"the source is dark", true, true, 0, false},
		{"a partial listing omits it and the probe cannot say", false, false, pluginv1.ProbeResponse_PRESENCE_UNSPECIFIED, false},
		{"a partial listing omits it and the probe says present", false, false, pluginv1.ProbeResponse_PRESENCE_PRESENT, false},
		{"an authoritative listing omits it", true, false, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &goneSource{page: true, listed: true, authoritative: true}
			cl, landing := goneStack(t, src)
			ctx := context.Background()
			page := tileLabelled(ctx, t, cl, landing, "doc")
			if _, err := cl.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: page, X: 4, Y: 4, W: 1, H: 1}); err != nil {
				t.Fatal(err)
			}

			src.set(func(p *goneSource) {
				p.listed, p.authoritative, p.dark, p.presence = false, tc.authoritative, tc.dark, tc.presence
			})
			g, err := cl.GetGrid(ctx, landing)
			if err != nil {
				t.Fatal(err)
			}
			var row *gridwellv1.Tile
			for _, tile := range g.Tiles {
				if tile.Id == page {
					row = tile
				}
			}
			got, terr := cl.GetTile(ctx, page)
			if tc.dead {
				if row != nil {
					t.Errorf("a gone key still sits in its grid: %+v", row)
				}
				if !gwerr.IsDeadRef(terr) {
					t.Errorf("GetTile on a gone page = %v, want the dead verdict", terr)
				}
				return
			}
			if terr != nil {
				t.Fatalf("GetTile = %v, want the remembered page", terr)
			}
			if row == nil {
				t.Fatalf("the touched page row is missing from its grid: %v", g.Tiles)
			}
			for verb, tile := range map[string]*gridwellv1.Tile{"GetGrid": row, "GetTile": got} {
				if !rpc.PageContent(tile) {
					t.Errorf("%s answers %+v, want a page the plugin serves", verb, tile)
				}
				if v := clickOn(tile, g.Grid.GetAcceptsTiles()); v != gesture.ClickDescend {
					t.Errorf("%s: a click on the page answers verdict %v, want a descent into it", verb, v)
				}
			}
		})
	}
}

// clickOn is the client's left-click verdict on tile in a grid that does or
// does not accept tiles, from the facts the shim resolves
// (client/wasm/input.go attemptDescentOrAscent).
func clickOn(tile *gridwellv1.Tile, acceptsTiles bool) gesture.ClickVerdict {
	return gesture.DecideTileClick(gesture.ClickInput{
		Well:           rpc.IsWellKind(tile.Kind),
		ContentDescent: rpc.IsContentDescentKind(tile.Kind),
		Workspace:      rpc.IsWorkspaceKind(tile.Kind),
		URL:            tile.Kind == rpc.KindURL,
		URLEmpty:       tile.UrlString == "",
		Page:           rpc.PageContent(tile),
		AcceptsTiles:   acceptsTiles,
		LeafLink:       rpc.LeafLink(tile),
	})
}
