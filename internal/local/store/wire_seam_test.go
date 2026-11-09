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
// pb.Tile field is non-zero in at least one, except the wire-only ones the store never
// sets. Without this a new column could be wired through the store and
// silently left out of the seam fixture.
func TestEveryStoredFieldCrossesTheWire(t *testing.T) {
	// Wire-only fields: derived by the server or the owning source, never a
	// stored column, so no store row can exercise them here.
	derived := map[string]string{
		"Link":             "derived by the router from child_grid_id's shape",
		"TextPresentation": "stated by the owning source from its content",
		"ContentStamp":     "the owning source's name for a claimless row's bytes",
		"ReadOnly":         "the owning source's reason a text body takes no edits",
		"Gone":             "the files' word that a directory door is gone from the disk",
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

	door, err := s.CreateDoor(ctx, root, 0, 0, 2, 3, "the door", false)
	if err != nil {
		t.Fatal(err)
	}
	door, err = s.SetFraming(ctx, &pb.SetFramingRequest{TileId: door.Id,
		Cx: 4.25, Cy: -5.5, Zoom: 0.75})
	if err != nil {
		t.Fatal(err)
	}

	// A user rename: the one writeback that bumps `version`, so the fixture
	// carries a non-zero version.
	door, err = s.RenameTile(ctx, door.Id, door.Version, "a renamed door")
	if err != nil {
		t.Fatal(err)
	}

	url, err := s.CreateURL(ctx, root, 4, 1, 1, 1, "https://example.com", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetURLState(ctx, url.Id, []byte("\xff\xd8\xff not really a jpeg"), "Example", `{"index":1,"entries":[{"url":"https://example.com"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetContentZoom(ctx, url.Id, zoomOf(1.25)); err != nil {
		t.Fatal(err)
	}
	url, err = s.SetFrozen(ctx, url.Id, true, nil)
	if err != nil {
		t.Fatal(err)
	}

	text, err := s.CreateText(ctx, root, 6, 2, 2, 2, []byte("# a doc\n"))
	if err != nil {
		t.Fatal(err)
	}
	text, err = s.SetTextScroll(ctx, text.Id, 7, 8, 9, 10, rpc.TextModeRendered)
	if err != nil {
		t.Fatal(err)
	}

	link, err := s.CreateLeafLink(ctx, root, 9, 0, 1, 1, rpc.KindText, "otherplugin/42", "a link", false)
	if err != nil {
		t.Fatal(err)
	}

	// serves_page is stored only on a file's row.
	ns := s.Files()
	pgid, err := ns.GridOf("root")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ns.Mint(pgid, pageEntry("a.html"), 0, 0, 0, 1, 1); err != nil {
		t.Fatal(err)
	}
	pageRows, err := ns.tiles(pgid)
	if err != nil {
		t.Fatal(err)
	}

	shell, err := s.CreateShell(ctx, root, 12, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	shellCopy, err := s.CopyTile(ctx, &pb.CopyTileRequest{TileId: shell.Id, DestGridId: root, X: 14})
	if err != nil {
		t.Fatal(err)
	}

	return map[string]*pb.Tile{"door": door, "url": url, "text": text, "link": link, "page": pageRows[0].Tile,
		"shell copy": shellCopy}
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
