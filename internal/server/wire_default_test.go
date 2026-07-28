package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// Wire-boundary tests: every Create* RPC at the proto3-default-value
// input. This is the codec edge where silent bugs hide — proto3 omits
// default-valued fields on the wire, so a request with Data=[]byte{}
// from the client reaches the server as Data=nil. The store layer has
// to handle that without surprising the user.
//
// Each case asserts the *exact* outcome the user-facing path needs:
// success for empty-content tiles (a fresh palette drop of markdown
// arrives with empty Data and must persist), InvalidArgument for
// semantically required fields (empty URL, empty FSPath, PID=0).

func TestCreateTextEmptyData(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	// Empty Data is what client/wasm/input.go's palette drop sends.
	// After proto3 default-value omission round-trips through the wire
	// the server sees req.Data == nil.
	tile, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte{},
	})
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
	if len(resp.Tiles) != 1 || resp.Tiles[0].ID != tile.ID {
		t.Errorf("after CreateText empty: %d tiles, want 1 matching id=%s",
			len(resp.Tiles), tile.ID)
	}
}

// TestCreateTextNilData explicitly hands the request nil (rather than
// an empty non-nil slice). Same wire shape after proto3 marshaling —
// this guards the case where any future caller passes nil directly.
func TestCreateTextNilData(t *testing.T) {
	_, cl, root := newTestServer(t)
	if _, err := cl.CreateText(context.Background(), &rpc.CreateTextRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: nil,
	}); err != nil {
		t.Fatalf("CreateText with nil data: %v", err)
	}
}

// TestCreateURLEmptyString: an EMPTY url is the legal unconfigured state
// (issue #209 — drop first, prompt on first descent); a GARBAGE scheme
// still fails loudly with InvalidArgument, not silently with Internal.
func TestCreateURLEmptyString(t *testing.T) {
	_, cl, root := newTestServer(t)
	tile, err := cl.CreateURL(context.Background(), &rpc.CreateURLRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, URL: "",
	})
	if err != nil {
		t.Fatalf("empty URL is the unconfigured state, must create: %v", err)
	}
	if tile.URLString != "" {
		t.Errorf("unconfigured tile URLString = %q, want empty", tile.URLString)
	}
	_, err = cl.CreateURL(context.Background(), &rpc.CreateURLRequest{
		GridID: root, X: 2, Y: 0, W: 1, H: 1, URL: "javascript:alert(1)",
	})
	if got := errCode(err); got != connect.CodeInvalidArgument {
		t.Errorf("garbage scheme: code %v, want InvalidArgument", got)
	}
}

// TestMountUnknownPlugin asserts that mounting an unregistered plugin uuid is
// rejected at the boundary with NotFound, not somewhere deeper. (Mounting is
// a clone of a node-grid tile; an unknown plugin has no tile, so the source
// id routes nowhere.)
func TestMountUnknownPlugin(t *testing.T) {
	_, cl, root := newTestServer(t)
	_, err := cl.CloneTile(context.Background(), &rpc.CloneTileRequest{
		TileID: "no-such-plugin/1", Version: 0, DestGridID: root, X: 0, Y: 0,
	})
	if got := errCode(err); got != connect.CodeNotFound {
		t.Errorf("unknown plugin: code %v, want NotFound", got)
	}
}
