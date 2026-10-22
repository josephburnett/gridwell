package server_test

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
)

// A copy across a hop, home onto a far node and back, carries every fact
// rpc.CopyFacts names, as a copy within home does: a text's scroll and
// content zoom, a url's history, standing freeze and preview.
func TestACopyAcrossAHopCarriesWhatACopyWithinHomeCarries(t *testing.T) {
	ctx := context.Background()
	h := newTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil)
	cl := h.localCl
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home, far := lp.HomeGridId, connectionRows(lp)[0].RootGridId

	text, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: home,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("Body"))
	if err != nil {
		t.Fatal(err)
	}
	url, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: home,
		Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 1, Y: 0, W: 1, H: 1, UrlString: "https://example.com/u"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []*gridwellv1.SetTileRequest{
		{TileId: text.Id, Tile: &gridwellv1.Tile{Kind: rpc.KindText, TextY: 40, TextMode: rpc.TextModeText}},
		{TileId: url.Id, Preview: []byte("\xff\xd8 picture"),
			Tile: &gridwellv1.Tile{Kind: rpc.KindURL, UrlHistory: `{"index":0,"entries":[{"url":"https://example.com/u"}]}`}},
	} {
		if _, err := cl.SetTile(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{text.Id, url.Id} {
		if _, err := cl.SetContentZoom(ctx, id, 1.5); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cl.SetFrozen(ctx, url.Id, true, false); err != nil {
		t.Fatal(err)
	}

	for _, src := range []*gridwellv1.Tile{text, url} {
		within, err := cl.CopyTile(ctx, &gridwellv1.CopyTileRequest{TileId: src.Id, DestGridId: home, X: src.X, Y: 2})
		if err != nil {
			t.Fatal(err)
		}
		there, err := cl.CopyTile(ctx, &gridwellv1.CopyTileRequest{TileId: src.Id, DestGridId: far, X: src.X, Y: 3})
		if err != nil {
			t.Fatalf("copy %s onto the far node: %v", src.Kind, err)
		}
		back, err := cl.CopyTile(ctx, &gridwellv1.CopyTileRequest{TileId: there.Id, DestGridId: home, X: src.X, Y: 4})
		if err != nil {
			t.Fatalf("copy %s back from the far node: %v", src.Kind, err)
		}
		want, err := cl.GetTile(ctx, within.Id)
		if err != nil {
			t.Fatal(err)
		}
		for hop, c := range map[string]*gridwellv1.Tile{"onto the far node": there, "back home": back} {
			got, err := cl.GetTile(ctx, c.Id)
			if err != nil {
				t.Fatal(err)
			}
			if g, w := rpc.CopyFacts(got), rpc.CopyFacts(want); g.String() != w.String() {
				t.Errorf("a %s copied %s carries %v, a copy within home %v", src.Kind, hop, g, w)
			}
			if (got.PreviewBlobId != 0) != (want.PreviewBlobId != 0) {
				t.Errorf("a %s copied %s has a preview %v, a copy within home %v", src.Kind, hop, got.PreviewBlobId != 0, want.PreviewBlobId != 0)
			}
		}
	}
}
