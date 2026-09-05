package rpc

import (
	"encoding/json"
	"testing"
)

// The beacon bodies must be the exact Connect-unary wire form the ordinary
// client calls send: same converters, same procedures, so the unload flush
// and the settle flush cannot write different shapes.
func TestBeaconBodies(t *testing.T) {
	path, body := SetFramingBeacon(&SetFramingRequest{
		TileID: "u1/5", Framing: Framing{Cx: 1, Cy: 2, Zoom: 0.5},
	})
	if path != "/gridwell.v1.Gridwell/SetFraming" {
		t.Errorf("doorway framing path = %q", path)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if m["tileId"] != "u1/5" {
		t.Errorf("body = %s", body)
	}

	path, body = SetFramingBeacon(&SetFramingRequest{RootGridID: "u1/1", Framing: Framing{Cx: 1, Cy: 2, Zoom: 0.3}})
	// The same procedure for a root: one verb, both rows.
	if path != "/gridwell.v1.Gridwell/SetFraming" {
		t.Errorf("root framing path = %q", path)
	}
	if err := json.Unmarshal(body, &m); err != nil || m["rootGridId"] != "u1/1" {
		t.Errorf("root body = %s err=%v", body, err)
	}

	if path, body = SetTextViewBeacon(&SetTextViewRequest{TileID: "u1/9", TextY: 40}); path == "" || body == nil {
		t.Error("text-view beacon empty")
	}

	// The ephemeral cleanup parks, so it can be drained at unload: a quit
	// mid-ascent must still take the scratch row, and a shell's tmux session,
	// with it.
	path, body = DeleteTileBeacon(&DeleteTileRequest{TileID: "u1/7"})
	if path != "/gridwell.v1.Gridwell/DeleteTile" {
		t.Errorf("delete path = %q", path)
	}
	if err := json.Unmarshal(body, &m); err != nil || m["tileId"] != "u1/7" {
		t.Errorf("delete body = %s err=%v", body, err)
	}
}
