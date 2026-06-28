package server

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/store"
)

// TestQualifyTilesQualifiesIDs: a plugin returns bare ids; the server prefixes
// each with the plugin uuid (id and grid_id), and qualifies an interior
// child_grid_id too.
func TestQualifyTilesQualifiesIDs(t *testing.T) {
	in := []*pb.Tile{{Id: "5", GridId: "1", ChildGridId: "7"}}
	out := qualifyTiles("uuidA", in)
	if out[0].Id != "uuidA/5" || out[0].GridId != "uuidA/1" || out[0].ChildGridId != "uuidA/7" {
		t.Errorf("qualifyTiles = %+v", out[0])
	}
	// The source slice is not mutated (qualifyTiles copies each tile).
	if in[0].Id != "5" {
		t.Errorf("qualifyTiles mutated its input: %+v", in[0])
	}
}

// TestQualifyTilesLeavesAlreadyQualifiedChild: an exit well's child_grid_id is
// already a cross-plugin reference (<uuid>/<local>) and must NOT be re-prefixed
// — re-qualifying would produce uuidA/uuidB/9 and break routing. This is the
// branch the integration tests never hit.
func TestQualifyTilesLeavesAlreadyQualifiedChild(t *testing.T) {
	in := []*pb.Tile{{Id: "5", GridId: "1", ChildGridId: "uuidB/9"}}
	out := qualifyTiles("uuidA", in)
	if out[0].ChildGridId != "uuidB/9" {
		t.Errorf("already-qualified child rewritten to %q, want uuidB/9", out[0].ChildGridId)
	}
}

// TestQualifyTilesReference: the authoritative link signal. A child that
// arrived ALREADY qualified is a reference (a mount / exit well / cross-plugin
// clone) — Reference must be set; render draws it dashed. A bare interior child
// is owned content — Reference stays false (solid). This is the exact bug the
// fix closes: a same-plugin mount (child uuid == grid uuid) is still a
// reference, which a bare uuid comparison would call owned and render solid.
func TestQualifyTilesReference(t *testing.T) {
	// Cross-plugin reference: child uuid differs from the well's own.
	cross := qualifyTiles("uuidA", []*pb.Tile{{Id: "5", GridId: "1", ChildGridId: "uuidB/9"}})
	if !cross[0].Reference {
		t.Error("cross-plugin exit well should be a reference")
	}
	// Same-plugin mount: child arrived qualified with the SAME uuid as the
	// well's grid. Still a reference (the reported bug — it used to render solid).
	same := qualifyTiles("uuidA", []*pb.Tile{{Id: "5", GridId: "1", ChildGridId: "uuidA/9"}})
	if !same[0].Reference {
		t.Error("same-plugin mount (qualified child) should be a reference, not owned")
	}
	// Interior well: bare numeric child, owned — not a reference.
	interior := qualifyTiles("uuidA", []*pb.Tile{{Id: "5", GridId: "1", ChildGridId: "7"}})
	if interior[0].Reference {
		t.Error("interior well (bare child) must not be a reference")
	}
	// A childless tile is never a reference.
	leaf := qualifyTiles("uuidA", []*pb.Tile{{Id: "5", GridId: "1"}})
	if leaf[0].Reference {
		t.Error("a tile with no child grid must not be a reference")
	}
}

// TestQualifyTilesEmptyChildStaysEmpty: an interior tile with no child grid
// keeps an empty child_grid_id (not "uuidA/").
func TestQualifyTilesEmptyChildStaysEmpty(t *testing.T) {
	out := qualifyTiles("uuidA", []*pb.Tile{{Id: "5", GridId: "1"}})
	if out[0].ChildGridId != "" {
		t.Errorf("empty child became %q", out[0].ChildGridId)
	}
}

func TestQualifyGrid(t *testing.T) {
	if got := qualifyGrid("u", &pb.Grid{Id: "3"}); got.Id != "u/3" {
		t.Errorf("qualifyGrid id = %q, want u/3", got.Id)
	}
	if qualifyGrid("u", nil) != nil {
		t.Error("qualifyGrid(nil) should be nil")
	}
}

// TestQualifyEvent qualifies the ids carried by each of the three event kinds.
func TestQualifyEvent(t *testing.T) {
	grid := qualifyEvent("u", &pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{GridId: "1"}}})
	if grid.GetGridChanged().GridId != "u/1" {
		t.Errorf("GridChanged id = %q", grid.GetGridChanged().GridId)
	}
	tile := qualifyEvent("u", &pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{Tile: &pb.Tile{Id: "5", GridId: "1"}}}})
	if tile.GetTileChanged().Tile.Id != "u/5" || tile.GetTileChanged().Tile.GridId != "u/1" {
		t.Errorf("TileChanged tile = %+v", tile.GetTileChanged().Tile)
	}
	rem := qualifyEvent("u", &pb.Event{Payload: &pb.Event_TileRemoved{TileRemoved: &pb.TileRemoved{GridId: "1", TileId: "5"}}})
	if rem.GetTileRemoved().GridId != "u/1" || rem.GetTileRemoved().TileId != "u/5" {
		t.Errorf("TileRemoved = %+v", rem.GetTileRemoved())
	}
}

// TestSplitPluginID covers the parse both ways.
func TestSplitPluginID(t *testing.T) {
	if u, l, ok := splitPluginID("abc/12"); !ok || u != "abc" || l != "12" {
		t.Errorf("split(abc/12) = %q,%q,%v", u, l, ok)
	}
	if _, _, ok := splitPluginID("bare"); ok {
		t.Error("a bare id should report ok=false")
	}
	// A leading slash is not a plugin prefix (IndexByte > 0 required).
	if _, _, ok := splitPluginID("/x"); ok {
		t.Error("leading-slash id should report ok=false")
	}
}

// TestClassifyStoreError maps each store sentinel to its transport-agnostic
// class — the single place the wire error code is decided.
func TestClassifyStoreError(t *testing.T) {
	cases := []struct {
		err  error
		want storeErrorClass
	}{
		{store.ErrNotFound, classNotFound},
		{store.ErrInvalidArgument, classInvalidArgument},
		{store.ErrInvalidPath, classInvalidArgument},
		{store.ErrNotURLTile, classInvalidArgument},
		{store.ErrNotTextTile, classInvalidArgument},
		{store.ErrNotWellTile, classInvalidArgument},
		{store.ErrNotShellTile, classInvalidArgument},
		{store.ErrOverlap, classConflict},
		{store.ErrVersionConflict, classConflict},
		{errors.New("boom"), classInternal},
	}
	for _, c := range cases {
		if got := classifyStoreError(c.err); got != c.want {
			t.Errorf("classifyStoreError(%v) = %d, want %d", c.err, got, c.want)
		}
	}
	// Wrapped sentinels still classify (errors.Is unwraps).
	if classifyStoreError(errors.Join(errors.New("ctx"), store.ErrVersionConflict)) != classConflict {
		t.Error("a wrapped ErrVersionConflict should classify as conflict")
	}
}

// TestAsConnectError maps both gRPC status codes (from plugin subprocesses) and
// local store sentinels onto Connect codes.
func TestAsConnectError(t *testing.T) {
	if asConnectError(nil) != nil {
		t.Error("nil error should stay nil")
	}
	// gRPC status path (errors that crossed the plugin boundary).
	grpcCases := []struct {
		in   error
		want connect.Code
	}{
		{status.Error(gcodes.NotFound, "x"), connect.CodeNotFound},
		{status.Error(gcodes.InvalidArgument, "x"), connect.CodeInvalidArgument},
		{status.Error(gcodes.FailedPrecondition, "x"), connect.CodeFailedPrecondition},
		{status.Error(gcodes.Unavailable, "x"), connect.CodeInternal},
	}
	for _, c := range grpcCases {
		if got := connect.CodeOf(asConnectError(c.in)); got != c.want {
			t.Errorf("asConnectError(%v) code = %v, want %v", c.in, got, c.want)
		}
	}
	// Local store-sentinel path.
	storeCases := []struct {
		in   error
		want connect.Code
	}{
		{store.ErrNotFound, connect.CodeNotFound},
		{store.ErrInvalidArgument, connect.CodeInvalidArgument},
		{store.ErrVersionConflict, connect.CodeFailedPrecondition},
		{errors.New("boom"), connect.CodeInternal},
	}
	for _, c := range storeCases {
		if got := connect.CodeOf(asConnectError(c.in)); got != c.want {
			t.Errorf("asConnectError(%v) code = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestParseShellSize: defaults when absent/empty/garbage, raises below the
// minimum, and caps at the uint16 ceiling.
func TestParseShellSize(t *testing.T) {
	if c, r := parseShellSize(map[string][]string{}); c != defaultShellCols || r != defaultShellRows {
		t.Errorf("absent = (%d,%d), want defaults", c, r)
	}
	if c, r := parseShellSize(map[string][]string{"cols": {"abc"}, "rows": {"0"}}); c != defaultShellCols || r != defaultShellRows {
		t.Errorf("garbage/zero = (%d,%d), want defaults", c, r)
	}
	if c, _ := parseShellSize(map[string][]string{"cols": {"3"}}); c != minShellCols {
		t.Errorf("below min cols = %d, want %d", c, minShellCols)
	}
	if c, _ := parseShellSize(map[string][]string{"cols": {"999999"}}); c != 65535 {
		t.Errorf("overflow cols = %d, want 65535", c)
	}
	if c, r := parseShellSize(map[string][]string{"cols": {"100"}, "rows": {"40"}}); c != 100 || r != 40 {
		t.Errorf("valid = (%d,%d), want (100,40)", c, r)
	}
}

func TestClampShell(t *testing.T) {
	if c, r := clampShell(1, 1); c != minShellCols || r != minShellRows {
		t.Errorf("clampShell(1,1) = (%d,%d), want (%d,%d)", c, r, minShellCols, minShellRows)
	}
	if c, r := clampShell(100, 40); c != 100 || r != 40 {
		t.Errorf("clampShell(100,40) = (%d,%d), want unchanged", c, r)
	}
}
