package server_test

import (
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// A SetTile carrying two operations is refused with the reason by every
// owner, and neither lands (rpc.SetTileOp).
func TestSetTileOneOperationEveryOwner(t *testing.T) {
	f := newParityFix(t)
	for _, o := range []parityOwner{onHome, onPlugin, onFar} {
		before, err := f.cl.GetTile(f.ctx, f.tile(o, kURL).Id)
		if err != nil {
			t.Fatal(err)
		}
		zoom := 1.5
		_, err = f.cl.SetTile(f.ctx, &gridwellv1.SetTileRequest{TileId: before.Id,
			Version: before.Version, Rename: "Renamed", ContentZoom: &zoom})
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: rename and content_zoom in one SetTile = %v, want InvalidArgument", o, err)
		}
		after, err := f.cl.GetTile(f.ctx, before.Id)
		if err != nil {
			t.Fatal(err)
		}
		if after.AltText != before.AltText || after.ContentZoom != before.ContentZoom || after.Version != before.Version {
			t.Errorf("%s: a refused SetTile wrote: name %q -> %q, zoom %v -> %v, version %d -> %d", o,
				before.AltText, after.AltText, before.ContentZoom, after.ContentZoom, before.Version, after.Version)
		}
	}
}
