package rpc

import (
	"encoding/json"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The beacon bodies must be the exact Connect-unary wire form the ordinary
// client calls send: same requests, same procedures, so the unload flush and
// the settle flush cannot write different shapes.
func TestBeaconBodies(t *testing.T) {
	path, body := SetFramingBeacon(&pb.SetFramingRequest{
		TileId: "u1/5", Cx: 1, Cy: 2, Zoom: 0.5,
	})
	if path != "/gridwell.v1.Gridwell/SetFraming" {
		t.Errorf("door framing path = %q", path)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if m["tileId"] != "u1/5" {
		t.Errorf("body = %s", body)
	}

	path, body = SetFramingBeacon(&pb.SetFramingRequest{RootGridId: "u1/1", Cx: 1, Cy: 2, Zoom: 0.3})
	// The same procedure for a root: one verb, both rows.
	if path != "/gridwell.v1.Gridwell/SetFraming" {
		t.Errorf("root framing path = %q", path)
	}
	if err := json.Unmarshal(body, &m); err != nil || m["rootGridId"] != "u1/1" {
		t.Errorf("root body = %s err=%v", body, err)
	}

	// The preview jpeg never rides a beacon: the queue budget is about
	// 64 KB, and the store skips an empty preview.
	path, body = SetTileBeacon(&pb.SetTileRequest{TileId: "u1/9",
		Tile: &pb.Tile{Kind: KindURL, UrlString: "https://example.com"}, Preview: []byte("jpeg")})
	if path != "/gridwell.v1.Gridwell/SetTile" {
		t.Errorf("set-tile path = %q", path)
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("set-tile body not JSON: %v", err)
	}
	if _, ok := m["preview"]; ok {
		t.Errorf("beacon carries the preview jpeg: %s", body)
	}

	// The ephemeral cleanup parks, so it can be drained at unload: a quit
	// mid-ascent must still take the scratch row, and a shell's tmux,
	// with it.
	path, body = DeleteTileBeacon(DeleteOf(&pb.Tile{Id: "u1/7", GridId: "u1/2", W: 1, H: 1}))
	if path != "/gridwell.v1.Gridwell/DeleteTile" {
		t.Errorf("delete path = %q", path)
	}
	if err := json.Unmarshal(body, &m); err != nil || m["tileId"] != "u1/7" {
		t.Errorf("delete body = %s err=%v", body, err)
	}
	// One the API would refuse is posted instead, so the refusal surfaces.
	if _, body = DeleteTileBeacon(&pb.DeleteTileRequest{TileId: "u1/7"}); body != nil {
		t.Errorf("an unclaimed delete got a beacon body: %s", body)
	}
}
