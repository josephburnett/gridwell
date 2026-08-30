package store

import (
	"context"
	"reflect"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// TestStoreRowSurvivesTheWire is the SEAM test for finding 4 (three copies of
// Tile): a row the store wrote, read back through its scan list, converted to
// the proto message, encoded the way the Connect JSON codec encodes it, and
// decoded back into the client's Go struct must be the identical value.
//
// A unit test on either side would not catch the bug this guards: the store's
// scan list, the rpc↔pb conversion and the proto are three spellings of the
// same record, and a field dropped from any ONE of them still passes that
// side's own tests. Here a dropped field changes the value that comes back.
//
// The kinds together cover every stored on-wire column: a well carries the
// framing and the child grid, a url carries the address/preview/history/
// freeze/zoom, a text carries the body blob and the doc window, and a leaf
// link carries link_target_id. TestEveryStoredFieldCrossesTheWire below
// asserts that coverage rather than trusting this comment.
func TestStoreRowSurvivesTheWire(t *testing.T) {
	for name, tile := range wireFixtures(t) {
		t.Run(name, func(t *testing.T) {
			got := rpc.TileFromProto(tileThroughJSON(t, rpc.TileToProto(tile)))
			if !reflect.DeepEqual(tile, got) {
				t.Errorf("store row changed crossing the wire:\n store = %+v\n back  = %+v", tile, got)
			}
		})
	}
}

// TestEveryStoredFieldCrossesTheWire proves the fixtures above are TOTAL over
// what the store can put on the wire: every rpc.Tile field is non-zero in at
// least one fixture, except the four the store never sets (derived wire-only
// fields — see the derived-field inventory in ARCHITECTURE.md §7). Without
// this, a new column could be added, wired through the store, and silently
// left out of the seam fixture.
func TestEveryStoredFieldCrossesTheWire(t *testing.T) {
	// Wire-only fields: derived by the server or the owning plugin, never a
	// stored column, so no store row can exercise them here.
	derived := map[string]string{
		"Reference":        "derived by the router from child_grid_id's shape",
		"ServesPage":       "declared by the owning plugin from its content",
		"TextPresentation": "declared by the owning plugin from its content",
		"StatusDetail":     "the owning plugin's current trouble with the tile",
	}
	covered := map[string]bool{}
	for _, tile := range wireFixtures(t) {
		v := reflect.ValueOf(*tile)
		for i := 0; i < v.NumField(); i++ {
			if !v.Field(i).IsZero() {
				covered[v.Type().Field(i).Name] = true
			}
		}
	}
	rt := reflect.TypeOf(rpc.Tile{})
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		if _, ok := derived[name]; ok {
			if covered[name] {
				t.Errorf("rpc.Tile.%s is on the derived list but a store row set it", name)
			}
			continue
		}
		if !covered[name] {
			t.Errorf("no store fixture sets rpc.Tile.%s — the seam test does not cover it "+
				"(add it to a fixture, or to the derived list with a reason)", name)
		}
	}
}

// wireFixtures builds one store-written tile per stored shape, keyed by name.
func wireFixtures(t *testing.T) map[string]*rpc.Tile {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)

	well, err := s.CreateWell(ctx, &rpc.CreateWellRequest{GridID: root, X: 0, Y: 0, W: 2, H: 3, Label: "the well"})
	if err != nil {
		t.Fatal(err)
	}
	well, err = s.SetFraming(ctx, &rpc.SetFramingRequest{TileID: well.ID,
		Framing: rpc.Framing{Cx: 4.25, Cy: -5.5, Zoom: 0.75}})
	if err != nil {
		t.Fatal(err)
	}

	// A user rename: the one writeback that bumps `version`
	// (docs/simplify-plan.md S5), so the fixture carries a non-zero version.
	well, err = s.RenameTile(ctx, well.ID, well.Version, "a renamed well")
	if err != nil {
		t.Fatal(err)
	}

	url, err := s.CreateURL(ctx, &rpc.CreateURLRequest{GridID: root, X: 4, Y: 1, W: 1, H: 1, URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetURLState(ctx, &rpc.SetURLStateRequest{TileID: url.ID,
		JPEG:  []byte("\xff\xd8\xff not really a jpeg"),
		URL:   "https://example.com/page",
		Title: "Example",
		// A back-stack captured at freeze (issue #113).
		History: `{"index":1,"entries":[{"url":"https://example.com"}]}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetContentZoom(ctx, &rpc.SetContentZoomRequest{TileID: url.ID, ContentZoom: 1.25}); err != nil {
		t.Fatal(err)
	}
	url, err = s.SetURLFrozen(ctx, &rpc.SetURLFrozenRequest{TileID: url.ID, Frozen: true})
	if err != nil {
		t.Fatal(err)
	}

	text, err := s.CreateText(ctx, &rpc.CreateTextRequest{GridID: root, X: 6, Y: 2, W: 2, H: 2, Data: []byte("# a doc\n")})
	if err != nil {
		t.Fatal(err)
	}
	text, err = s.SetTextView(ctx, &rpc.SetTextViewRequest{TileID: text.ID,
		TextX: 7, TextY: 8, TextW: 9, TextH: 10, TextMode: rpc.TextModeRendered})
	if err != nil {
		t.Fatal(err)
	}

	link, err := s.CreateLeafLink(ctx, root, 9, 0, 1, 1, rpc.KindText, "otherplugin/42", "a link")
	if err != nil {
		t.Fatal(err)
	}

	return map[string]*rpc.Tile{"well": well, "url": url, "text": text, "link": link}
}

// tileThroughJSON encodes a tile the way the Connect JSON codec does
// (connect.WithProtoJSON — api/rpc.NewDefaultClient) and reads it back.
func tileThroughJSON(t *testing.T, m *pb.Tile) *pb.Tile {
	t.Helper()
	b, err := protojson.Marshal(m)
	if err != nil {
		t.Fatalf("protojson marshal: %v", err)
	}
	var out pb.Tile
	if err := protojson.Unmarshal(b, &out); err != nil {
		t.Fatalf("protojson unmarshal: %v", err)
	}
	return &out
}
