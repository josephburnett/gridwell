package rpc

import (
	"reflect"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// fullTile is a Tile with a DISTINCT value in every field, so a round-trip that
// swaps or drops any field is caught (a same-valued fixture would hide it).
func fullTile() *Tile {
	return &Tile{
		ID:      "id-1",
		Version: 3,
		GridID:  "grid-4",
		Kind:    KindURL,
		X:       5, Y: 6, W: 7, H: 8,
		ViewCx: 9, ViewCy: 10, ViewZoom: 11,
		ChildGridID: "child-12",
		TextX:       13, TextY: 14, TextW: 15, TextH: 16,
		TextMode:         "rendered",
		BlobID:           18,
		URLString:        "https://19",
		PreviewBlobID:    20,
		AltText:          "alt-21",
		Reference:        true,
		ContentZoom:      22,
		URLHistory:       "hist-23",
		LinkTargetID:     "target-24",
		URLFrozen:        true,
		ServesPage:       true,
		TextPresentation: "both",
	}
}

// TestTileProtoRoundTrip: every field survives Tile → proto → Tile unchanged.
func TestTileProtoRoundTrip(t *testing.T) {
	in := fullTile()
	got := TileFromProto(TileToProto(in))
	if !reflect.DeepEqual(in, got) {
		t.Errorf("tile round-trip diverged:\n in = %+v\nout = %+v", in, got)
	}
}

// TestTileProtoNil: nil maps to nil in both directions (the server relies on
// this to pass "no tile" through without allocating an empty one).
func TestTileProtoNil(t *testing.T) {
	if TileToProto(nil) != nil {
		t.Error("TileToProto(nil) should be nil")
	}
	if TileFromProto(nil) != nil {
		t.Error("TileFromProto(nil) should be nil")
	}
}

func TestGridProtoRoundTrip(t *testing.T) {
	in := &Grid{ID: "g1", Version: 3, SourceKind: "fs", SourceID: "/tmp"}
	got := GridFromProto(GridToProto(in))
	if !reflect.DeepEqual(in, got) {
		t.Errorf("grid round-trip diverged:\n in = %+v\nout = %+v", in, got)
	}
	if GridToProto(nil) != nil || GridFromProto(nil) != nil {
		t.Error("nil grid must map to nil both ways")
	}
}

// TestTilesSliceProto: nil slices stay nil (not []), and a populated slice
// round-trips element-for-element.
func TestTilesSliceProto(t *testing.T) {
	if TilesToProto(nil) != nil {
		t.Error("TilesToProto(nil) should be nil")
	}
	if TilesFromProto(nil) != nil {
		t.Error("TilesFromProto(nil) should be nil")
	}
	in := []Tile{*fullTile(), {ID: "x", Kind: KindText, X: 1}}
	got := TilesFromProto(TilesToProto(in))
	if !reflect.DeepEqual(in, got) {
		t.Errorf("tiles slice round-trip diverged:\n in = %+v\nout = %+v", in, got)
	}
}

// TestEventProtoRoundTrip covers all four event kinds through the proto oneof
// and back — the discriminator + the one populated payload must survive.
func TestEventProtoRoundTrip(t *testing.T) {
	cases := []Event{
		{Kind: EventGridChanged, GridChanged: &GridChanged{GridID: "g-1"}},
		{Kind: EventTileChanged, TileChanged: &TileChanged{Tile: *fullTile()}},
		{Kind: EventTileRemoved, TileRemoved: &TileRemoved{GridID: "g-2", TileID: "t-3"}},
		{Kind: EventPluginHealth, PluginHealth: &PluginHealth{PluginUUID: "u-1", Healthy: false, Detail: "dial tcp: connection refused"}},
		{Kind: EventPluginHealth, PluginHealth: &PluginHealth{PluginUUID: "u-1", Healthy: true}},
	}
	for _, in := range cases {
		got := EventFromProto(EventToProto(in))
		if !reflect.DeepEqual(in, got) {
			t.Errorf("event round-trip (%v) diverged:\n in = %+v\nout = %+v", in.Kind, in, got)
		}
	}
}

// TestEventFromProtoNil: a nil/empty wire event decodes to the zero Event
// rather than panicking on a nil payload.
func TestEventFromProtoNil(t *testing.T) {
	if got := EventFromProto(nil); got.Kind != "" {
		t.Errorf("EventFromProto(nil) = %+v, want zero", got)
	}
	if got := EventFromProto(&pb.Event{}); got.Kind != "" {
		t.Errorf("EventFromProto(empty) = %+v, want zero", got)
	}
}

// TestMutationRequestRoundTrips: the symmetric *FromProto/*ToProto request
// converters preserve every field both ways. These are the wire boundary for
// the placement mutations and the optimistic-concurrency Version key.
func TestMutationRequestRoundTrips(t *testing.T) {
	place := &PlaceTileRequest{TileID: "t", Version: 2, GridID: "dg", X: 3, Y: 4, W: 5, H: 6}
	if got := PlaceTileFromProto(PlaceTileToProto(place)); !reflect.DeepEqual(place, got) {
		t.Errorf("place round-trip: in=%+v out=%+v", place, got)
	}

	clone := &CloneTileRequest{TileID: "t", Version: 5, DestGridID: "dg", X: 6, Y: 7}
	if got := CloneTileFromProto(CloneTileToProto(clone)); !reflect.DeepEqual(clone, got) {
		t.Errorf("clone round-trip: in=%+v out=%+v", clone, got)
	}

	del := &DeleteTileRequest{TileID: "t", Version: 14}
	if got := DeleteTileFromProto(DeleteTileToProto(del)); !reflect.DeepEqual(del, got) {
		t.Errorf("delete round-trip: in=%+v out=%+v", del, got)
	}

	if got := ShellSessionAliveToProto(&ShellSessionAliveRequest{TileID: "t-9"}); got.TileId != "t-9" {
		t.Errorf("shell-alive req: got %+v", got)
	}
	if got := ShellSessionAliveResponseFromProto(&pb.ShellSessionAliveResponse{Alive: true}); !got.Alive {
		t.Errorf("shell-alive resp: got %+v", got)
	}
}

// TestCreateConvertersSelectKindAndFields: each typed create maps onto the
// unified CreateTile shape with the right Kind and the kind-specific fields in
// the right place (the one spot per primitive where this mapping lives).
func TestCreateConvertersSelectKindAndFields(t *testing.T) {
	well := CreateWellToProto(&CreateWellRequest{GridID: "g", X: 1, Y: 2, W: 3, H: 4, ChildGridID: "cg", Label: "home",
		Framing: Framing{Cx: 7, Cy: 8, Zoom: 0.5}})
	if well.Tile.Kind != KindWell || well.Tile.ChildGridId != "cg" || well.Tile.AltText != "home" || well.GridId != "g" {
		t.Errorf("CreateWell mapping wrong: %+v", well.Tile)
	}
	// The framing seed rides the create so an exit well (a plugin link dropped
	// from the + menu) starts at the target's persisted root view.
	if well.Tile.ViewCx != 7 || well.Tile.ViewCy != 8 || well.Tile.ViewZoom != 0.5 {
		t.Errorf("CreateWell dropped the view framing seed: %+v", well.Tile)
	}
	text := CreateTextToProto(&CreateTextRequest{GridID: "g", W: 2, H: 2})
	if text.Tile.Kind != KindText || text.Tile.W != 2 {
		t.Errorf("CreateText mapping wrong: tile=%+v", text.Tile)
	}
	u := CreateURLToProto(&CreateURLRequest{GridID: "g", URL: "https://x"})
	if u.Tile.Kind != KindURL || u.Tile.UrlString != "https://x" {
		t.Errorf("CreateURL mapping wrong: %+v", u.Tile)
	}
	sh := CreateShellToProto(&CreateShellRequest{GridID: "g", W: 2, H: 2})
	if sh.Tile.Kind != KindShell {
		t.Errorf("CreateShell mapping wrong: %+v", sh.Tile)
	}
}

// TestSetConvertersAreFramingByKind: the SetTile converters carry framing/
// preview fields under the right Kind, and route the JPEG preview via the
// Preview field (not the tile body). This is the framing-vs-content boundary.
func TestSetConvertersAreFramingByKind(t *testing.T) {
	well := SetWellViewToProto(&SetFramingRequest{TileID: "t", Version: 1, Framing: Framing{Cx: 4, Cy: 5, Zoom: 6}})
	if well.Tile.Kind != KindWell || well.Tile.ViewCx != 4 || well.Tile.ViewZoom != 6 {
		t.Errorf("SetWellView mapping wrong: %+v", well.Tile)
	}
	txt := SetTextViewToProto(&SetTextViewRequest{TileID: "t", TextX: 1, TextY: 2, TextW: 3, TextH: 4, TextMode: "rendered"})
	if txt.Tile.Kind != KindText || txt.Tile.TextMode != "rendered" || txt.Tile.TextW != 3 {
		t.Errorf("SetTextView mapping wrong: %+v", txt.Tile)
	}
	urlState := SetURLStateToProto(&SetURLStateRequest{TileID: "t", URL: "https://x", Title: "T", JPEG: []byte("j")})
	if urlState.Tile.Kind != KindURL || urlState.Tile.UrlString != "https://x" || urlState.Tile.AltText != "T" || string(urlState.Preview) != "j" {
		t.Errorf("SetURLState mapping wrong: tile=%+v preview=%q", urlState.Tile, urlState.Preview)
	}
	shellPrev := SetShellPreviewToProto(&SetShellPreviewRequest{TileID: "t", JPEG: []byte("j")})
	if shellPrev.Tile.Kind != KindShell || string(shellPrev.Preview) != "j" {
		t.Errorf("SetShellPreview mapping wrong: tile=%+v preview=%q", shellPrev.Tile, shellPrev.Preview)
	}
}
