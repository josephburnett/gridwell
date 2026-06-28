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
		ID:            "id-1",
		ObjectID:      "obj-2",
		Version:       3,
		GridID:        "grid-4",
		Kind:          KindURL,
		X:             5, Y: 6, W: 7, H: 8,
		ViewX:         9, ViewY: 10, ViewZoom: 11,
		ChildGridID:   "child-12",
		TextX:         13, TextY: 14, TextW: 15, TextH: 16,
		TextMode:      "rendered",
		BlobID:        18,
		URLString:     "https://19",
		PreviewBlobID: 20,
		AltText:       "alt-21",
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
	in := &Grid{ID: "g1", ObjectID: "o2", Version: 3, SourceKind: "fs", SourceID: "/tmp"}
	got := GridFromProto(GridToProto(in))
	if !reflect.DeepEqual(in, got) {
		t.Errorf("grid round-trip diverged:\n in = %+v\nout = %+v", in, got)
	}
	if GridToProto(nil) != nil || GridFromProto(nil) != nil {
		t.Error("nil grid must map to nil both ways")
	}
}

// TestPathProtoRoundTrip: a populated path round-trips, and a nil wire path
// decodes to the empty (root) Path the server treats as the root pane.
func TestPathProtoRoundTrip(t *testing.T) {
	in := Path{WellIDs: []string{"a", "b", "c"}}
	if got := PathFromProto(PathToProto(in)); !reflect.DeepEqual(in, got) {
		t.Errorf("path round-trip: in=%+v out=%+v", in, got)
	}
	if got := PathFromProto(nil); len(got.WellIDs) != 0 {
		t.Errorf("nil wire path should be empty, got %+v", got)
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

// TestEventProtoRoundTrip covers all three event kinds through the proto oneof
// and back — the discriminator + the one populated payload must survive.
func TestEventProtoRoundTrip(t *testing.T) {
	cases := []Event{
		{Kind: EventGridChanged, GridChanged: &GridChanged{GridID: "g-1"}},
		{Kind: EventTileChanged, TileChanged: &TileChanged{Tile: *fullTile()}},
		{Kind: EventTileRemoved, TileRemoved: &TileRemoved{GridID: "g-2", TileID: "t-3"}},
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
	move := &MoveTileRequest{Path: Path{WellIDs: []string{"p"}}, TileID: "t", Version: 2, DestGridID: "dg", DestPath: Path{WellIDs: []string{"d"}}, X: 3, Y: 4}
	if got := MoveTileFromProto(MoveTileToProto(move)); !reflect.DeepEqual(move, got) {
		t.Errorf("move round-trip: in=%+v out=%+v", move, got)
	}

	clone := &CloneTileRequest{Path: Path{WellIDs: []string{"p"}}, TileID: "t", Version: 5, DestGridID: "dg", DestPath: Path{WellIDs: []string{"d"}}, X: 6, Y: 7}
	if got := CloneTileFromProto(CloneTileToProto(clone)); !reflect.DeepEqual(clone, got) {
		t.Errorf("clone round-trip: in=%+v out=%+v", clone, got)
	}

	resize := &ResizeTileRequest{Path: Path{WellIDs: []string{"p"}}, TileID: "t", Version: 8, X: 9, Y: 10, W: 11, H: 12}
	if got := ResizeTileFromProto(ResizeTileToProto(resize)); !reflect.DeepEqual(resize, got) {
		t.Errorf("resize round-trip: in=%+v out=%+v", resize, got)
	}

	upd := &UpdateTextRequest{Path: Path{WellIDs: []string{"p"}}, TileID: "t", Version: 13, Data: []byte("body")}
	if got := UpdateTextFromProto(UpdateTextToProto(upd)); !reflect.DeepEqual(upd, got) {
		t.Errorf("updatetext round-trip: in=%+v out=%+v", upd, got)
	}

	del := &DeleteTileRequest{Path: Path{WellIDs: []string{"p"}}, TileID: "t", Version: 14}
	if got := DeleteTileFromProto(DeleteTileToProto(del)); !reflect.DeepEqual(del, got) {
		t.Errorf("delete round-trip: in=%+v out=%+v", del, got)
	}

	alive := &ShellSessionAliveRequest{TileID: "t-9"}
	if got := ShellSessionAliveFromProto(ShellSessionAliveToProto(alive)); !reflect.DeepEqual(alive, got) {
		t.Errorf("shell-alive req round-trip: in=%+v out=%+v", alive, got)
	}
	aliveResp := &ShellSessionAliveResponse{Alive: true}
	if got := ShellSessionAliveResponseFromProto(ShellSessionAliveResponseToProto(aliveResp)); !reflect.DeepEqual(aliveResp, got) {
		t.Errorf("shell-alive resp round-trip: in=%+v out=%+v", aliveResp, got)
	}
}

// TestCreateConvertersSelectKindAndFields: each typed create maps onto the
// unified CreateTile shape with the right Kind and the kind-specific fields in
// the right place (the one spot per primitive where this mapping lives).
func TestCreateConvertersSelectKindAndFields(t *testing.T) {
	well := CreateWellToProto(&CreateWellRequest{GridID: "g", X: 1, Y: 2, W: 3, H: 4, ChildGridID: "cg", Label: "home"})
	if well.Tile.Kind != KindWell || well.Tile.ChildGridId != "cg" || well.Tile.AltText != "home" || well.GridId != "g" {
		t.Errorf("CreateWell mapping wrong: %+v", well.Tile)
	}
	text := CreateTextToProto(&CreateTextRequest{GridID: "g", W: 2, H: 2, Data: []byte("hi")})
	if text.Tile.Kind != KindText || string(text.Data) != "hi" {
		t.Errorf("CreateText mapping wrong: tile=%+v data=%q", text.Tile, text.Data)
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
	well := SetWellViewToProto(&SetWellViewRequest{TileID: "t", Version: 1, ViewX: 4, ViewY: 5, ViewZoom: 6})
	if well.Tile.Kind != KindWell || well.Tile.ViewX != 4 || well.Tile.ViewZoom != 6 {
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
