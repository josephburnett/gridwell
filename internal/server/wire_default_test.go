package server

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
)

// Every Create* RPC at the proto3-default-value input. proto3 omits
// default-valued fields on the wire, so a client's Data=[]byte{} reaches the
// server as Data=nil, and each case asserts the exact outcome the user-facing
// path needs: success for empty-content tiles, InvalidArgument for
// semantically required fields.

func TestCreateTextEmptyData(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	// Empty Data is what client/wasm/input.go's menu drop sends.
	// After proto3 default-value omission round-trips through the wire
	// the server sees req.Data == nil.
	tile, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte{})
	if err != nil {
		t.Fatalf("CreateText with empty data: %v", err)
	}
	if tile.Kind != rpc.KindText {
		t.Errorf("kind = %q, want text", tile.Kind)
	}

	// Confirm the tile actually landed in the grid — a successful
	// response with a missing row in the table would be the same
	// silent-disappear symptom the user reported.
	resp, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Tiles) != 1 || resp.Tiles[0].Id != tile.Id {
		t.Errorf("after CreateText empty: %d tiles, want 1 matching id=%s",
			len(resp.Tiles), tile.Id)
	}
}

// TestCreateTextNilData explicitly hands the request nil (rather than
// an empty non-nil slice). Same wire shape after proto3 marshaling —
// this guards the case where any future caller passes nil directly.
func TestCreateTextNilData(t *testing.T) {
	_, cl, root := newTestServer(t)
	if _, err := cl.CreateWithContent(context.Background(), &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, nil); err != nil {
		t.Fatalf("CreateText with nil data: %v", err)
	}
}

// TestCreateURLEmptyString: an EMPTY url is the legal unconfigured state
// (drop first, prompt on first descent); a garbage scheme
// still fails loudly with InvalidArgument, not silently with Internal.
func TestCreateURLEmptyString(t *testing.T) {
	_, cl, root := newTestServer(t)
	tile, err := cl.CreateTile(context.Background(), &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 0, Y: 0, W: 1, H: 1, UrlString: ""}})
	if err != nil {
		t.Fatalf("empty URL is the unconfigured state, must create: %v", err)
	}
	if tile.UrlString != "" {
		t.Errorf("unconfigured tile URLString = %q, want empty", tile.UrlString)
	}
	_, err = cl.CreateTile(context.Background(), &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 2, Y: 0, W: 1, H: 1, UrlString: "javascript:alert(1)"}})
	if got := errCode(err); got != connect.CodeInvalidArgument {
		t.Errorf("garbage scheme: code %v, want InvalidArgument", got)
	}
}

// TestMountUnknownPlugin asserts that mounting an unregistered plugin uuid is
// rejected at the boundary with NotFound, not somewhere deeper. (Mounting is
// a copy of a node-grid tile; an unknown plugin has no tile, so the source
// id routes nowhere.)
func TestMountUnknownPlugin(t *testing.T) {
	_, cl, root := newTestServer(t)
	_, err := cl.CopyTile(context.Background(), &gridwellv1.CopyTileRequest{
		TileId: "no-such-plugin/1", DestGridId: root, X: 0, Y: 0,
	})
	if got := errCode(err); got != connect.CodeNotFound {
		t.Errorf("unknown plugin: code %v, want NotFound", got)
	}
}
