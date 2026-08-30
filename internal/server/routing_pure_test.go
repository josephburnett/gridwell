package server

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
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
	// A same-namespace mount: the child arrived qualified with the same uuid
	// as the well's grid. It is still a reference, which a bare uuid
	// comparison would miss.
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

// TestQualifyTilesTransit: a TRANSIT plugin (the ssh node mount) speaks ids
// that are already qualified from the REMOTE node's perspective — chains.
// Local qualification must prepend the transit plugin's uuid to every id
// (id, grid, child — even an already-qualified child, which for a leaf plugin
// would be a sibling reference but through a hop is a reference within the
// remote's namespace, reachable only through this connection), and must trust
// the wire Reference bit verbatim: a remote plugin's interior well is OWNED
// content and renders solid, even though its child id contains "/".
func TestQualifyTilesTransit(t *testing.T) {
	// A remote home's interior well, as the remote front door emits it.
	interior := qualifyTilesTransit("ssh1", []*pb.Tile{{
		Id: "rp/7", GridId: "rp/1", ChildGridId: "rp/5", Reference: false,
	}})[0]
	if interior.Id != "ssh1/rp/7" || interior.GridId != "ssh1/rp/1" || interior.ChildGridId != "ssh1/rp/5" {
		t.Errorf("transit interior = %+v, want every id prefixed with ssh1/", interior)
	}
	if interior.Reference {
		t.Error("a remote plugin's interior well is owned content, not a link — Reference must stay false through the hop")
	}

	// A remote link (exit well / mount) keeps Reference true and its target
	// is prefixed too — the target lives on the remote node.
	link := qualifyTilesTransit("ssh1", []*pb.Tile{{
		Id: "rp/9", GridId: "rp/1", ChildGridId: "otherrp/3", Reference: true,
	}})[0]
	if link.ChildGridId != "ssh1/otherrp/3" {
		t.Errorf("transit link child = %q, want ssh1/otherrp/3", link.ChildGridId)
	}
	if !link.Reference {
		t.Error("a remote link must stay a link through the hop")
	}

	// No child → no child, and a leaf tile is untouched beyond id/grid.
	leaf := qualifyTilesTransit("ssh1", []*pb.Tile{{Id: "rp/2", GridId: "rp/1"}})[0]
	if leaf.ChildGridId != "" {
		t.Errorf("empty child became %q", leaf.ChildGridId)
	}
}

// TestQualifyTilesLeafLink: a leaf link (text/url/shell/pane carrying a
// link_target_id) is the leaf twin of the exit well at the qualify seam — the
// ONE derived Reference bit covers both shapes. The stored target is always
// qualified (the store enforces it), so a leaf plugin's qualification never
// prefixes it; a transit hop prepends exactly one segment, the same chain rule
// as a qualified child_grid_id.
func TestQualifyTilesLeafLink(t *testing.T) {
	// Leaf plugin: target stays verbatim, Reference derived true.
	ln := qualifyTiles("uuidA", []*pb.Tile{{Id: "5", GridId: "1", Kind: "text", LinkTargetId: "uuidB/42"}})[0]
	if !ln.Reference {
		t.Error("a leaf link must derive Reference=true (dashed = link)")
	}
	if ln.LinkTargetId != "uuidB/42" {
		t.Errorf("leaf link target rewritten to %q, want uuidB/42 verbatim", ln.LinkTargetId)
	}
	// An owned leaf (no target) is not a reference.
	owned := qualifyTiles("uuidA", []*pb.Tile{{Id: "5", GridId: "1", Kind: "text"}})[0]
	if owned.Reference {
		t.Error("an owned leaf must not be a reference")
	}
	// Transit hop: the target chains — one prepended segment — and the wire
	// Reference bit rides verbatim.
	hop := qualifyTilesTransit("ssh1", []*pb.Tile{{
		Id: "rp/9", GridId: "rp/1", Kind: "url", LinkTargetId: "otherrp/42", Reference: true,
	}})[0]
	if hop.LinkTargetId != "ssh1/otherrp/42" {
		t.Errorf("transit leaf link target = %q, want ssh1/otherrp/42", hop.LinkTargetId)
	}
	if !hop.Reference {
		t.Error("a remote leaf link must stay a link through the hop")
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
	grid := qualifyEvent("u", false, &pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{GridId: "1"}}})
	if grid.GetGridChanged().GridId != "u/1" {
		t.Errorf("GridChanged id = %q", grid.GetGridChanged().GridId)
	}
	tile := qualifyEvent("u", false, &pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{Tile: &pb.Tile{Id: "5", GridId: "1"}}}})
	if tile.GetTileChanged().Tile.Id != "u/5" || tile.GetTileChanged().Tile.GridId != "u/1" {
		t.Errorf("TileChanged tile = %+v", tile.GetTileChanged().Tile)
	}
	rem := qualifyEvent("u", false, &pb.Event{Payload: &pb.Event_TileRemoved{TileRemoved: &pb.TileRemoved{GridId: "1", TileId: "5"}}})
	if rem.GetTileRemoved().GridId != "u/1" || rem.GetTileRemoved().TileId != "u/5" {
		t.Errorf("TileRemoved = %+v", rem.GetTileRemoved())
	}

	// PluginHealth carries a plugin uuid, which is an id like any other: each
	// hop prepends one segment. A mounted node's fan-in re-serves its own
	// plugins' health transitions; without the prepend they arrive addressed
	// by a bare remote uuid that names nothing on this side of the mount, so
	// the "live updates stopped" notice attaches to nothing (or, worse, could
	// collide with a local source key). Regression: qualifyEvent's switch
	// omitted this variant and passed it through verbatim.
	health := qualifyEvent("ssh1", true, &pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: &pb.EventPluginHealth{
		PluginUuid: "rp", Healthy: false, Detail: "stream ended",
	}}})
	if got := health.GetPluginHealth().GetPluginUuid(); got != "ssh1/rp" {
		t.Errorf("transit PluginHealth uuid = %q, want ssh1/rp (chain rule: one segment per hop)", got)
	}
	if health.GetPluginHealth().Healthy || health.GetPluginHealth().Detail != "stream ended" {
		t.Errorf("PluginHealth payload mutated beyond the uuid: %+v", health.GetPluginHealth())
	}
	// Leaf plugins don't emit PluginHealth today, but the chain rule is
	// uniform: every id in a response gets this hop prepended.
	leafHealth := qualifyEvent("u", false, &pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: &pb.EventPluginHealth{
		PluginUuid: "x", Healthy: true,
	}}})
	if got := leafHealth.GetPluginHealth().GetPluginUuid(); got != "u/x" {
		t.Errorf("leaf PluginHealth uuid = %q, want u/x", got)
	}
}

// TestSplitPluginID covers the parse both ways.
func TestSplitPluginID(t *testing.T) {
	if u, l, ok := rpc.SplitID("abc/12"); !ok || u != "abc" || l != "12" {
		t.Errorf("split(abc/12) = %q,%q,%v", u, l, ok)
	}
	if _, _, ok := rpc.SplitID("bare"); ok {
		t.Error("a bare id should report ok=false")
	}
	// A leading slash is not a plugin prefix (IndexByte > 0 required).
	if _, _, ok := rpc.SplitID("/x"); ok {
		t.Error("leading-slash id should report ok=false")
	}
}

// The sentinel→class mapping itself is pinned in internal/store
// (TestClassifyError / TestEverySentinelIsClassified); here we only assert
// the server consumes it (TestAsConnectError below drives the store-sentinel
// fall-through path).

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
		// Unavailable (e.g. a plugin's dial to its configured target failed)
		// must stay Unavailable, not fall through to Internal: clientsync.Of
		// classifies only CodeUnavailable/DeadlineExceeded/Canceled as
		// OutcomeTransport ("the server never spoke, keep local state and
		// retry"). Remapping it to Internal made a retryable connection
		// failure (e.g. a bad ssh key path) look like a hard rejection.
		{status.Error(gcodes.Unavailable, "x"), connect.CodeUnavailable},
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
