package store

import (
	"context"
	"reflect"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// The seam over the store's scan list and the proto: a row the store wrote,
// read back through its scan list, encoded by the Connect JSON codec and
// decoded again, must be the identical value. A unit test on either side would
// not catch this, because the scan list and the proto are two spellings of the
// same record and a field dropped from either still passes that side's own
// tests. TestEveryStoredFieldCrossesTheWire asserts the fixtures cover every
// stored on-wire column.
func TestStoreRowSurvivesTheWire(t *testing.T) {
	for name, tile := range wireFixtures(t) {
		t.Run(name, func(t *testing.T) {
			got := tileThroughJSON(t, tile)
			if !proto.Equal(tile, got) {
				t.Errorf("store row changed crossing the wire:\n store = %+v\n back  = %+v", tile, got)
			}
		})
	}
}

// The fixtures above are total over what the store can put on the wire: every
// pb.Tile field is non-zero in at least one, except the four the store never
// sets. Without this a new column could be wired through the store and
// silently left out of the seam fixture.
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
		v := reflect.ValueOf(tile).Elem()
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() && !v.Field(i).IsZero() {
				covered[v.Type().Field(i).Name] = true
			}
		}
	}
	rt := reflect.TypeOf(pb.Tile{})
	for i := 0; i < rt.NumField(); i++ {
		if !rt.Field(i).IsExported() {
			continue // protoimpl bookkeeping, not a record field
		}
		name := rt.Field(i).Name
		if _, ok := derived[name]; ok {
			if covered[name] {
				t.Errorf("pb.Tile.%s is on the derived list but a store row set it", name)
			}
			continue
		}
		if !covered[name] {
			t.Errorf("no store fixture sets pb.Tile.%s — the seam test does not cover it "+
				"(add it to a fixture, or to the derived list with a reason)", name)
		}
	}
}

// wireFixtures builds one store-written tile per stored shape, keyed by name.
func wireFixtures(t *testing.T) map[string]*pb.Tile {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)

	well, err := s.CreateWell(ctx, root, 0, 0, 2, 3, "the well")
	if err != nil {
		t.Fatal(err)
	}
	well, err = s.SetFraming(ctx, &pb.SetFramingRequest{TileId: well.Id,
		Cx: 4.25, Cy: -5.5, Zoom: 0.75})
	if err != nil {
		t.Fatal(err)
	}

	// A user rename: the one writeback that bumps `version`, so the fixture
	// carries a non-zero version.
	well, err = s.RenameTile(ctx, well.Id, well.Version, "a renamed well")
	if err != nil {
		t.Fatal(err)
	}

	url, err := s.CreateURL(ctx, root, 4, 1, 1, 1, "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetURLState(ctx, url.Id, []byte("\xff\xd8\xff not really a jpeg"), "https://example.com/page", "Example", `{"index":1,"entries":[{"url":"https://example.com"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetContentZoom(ctx, url.Id, 1.25); err != nil {
		t.Fatal(err)
	}
	url, err = s.SetFrozen(ctx, url.Id, true)
	if err != nil {
		t.Fatal(err)
	}

	text, err := s.CreateText(ctx, root, 6, 2, 2, 2, []byte("# a doc\n"))
	if err != nil {
		t.Fatal(err)
	}
	text, err = s.SetTextView(ctx, text.Id, 7, 8, 9, 10, rpc.TextModeRendered)
	if err != nil {
		t.Fatal(err)
	}

	link, err := s.CreateLeafLink(ctx, root, 9, 0, 1, 1, rpc.KindText, "otherplugin/42", "a link")
	if err != nil {
		t.Fatal(err)
	}

	return map[string]*pb.Tile{"well": well, "url": url, "text": text, "link": link}
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
