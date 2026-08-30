package server

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// The OFFLINE deep-copy seam (offline-plan owner decision, 2026-08-14):
// cloning a partially-reachable remote grid never refuses and never leaves
// a silent hole — a tile whose bytes the source cannot serve degrades to a
// LINK to the original; a nested room whose grid is unreachable degrades
// to a well link; a url whose preview is unreachable copies faceless (its
// fact — the address — is present). The degrade keys on TRANSPORT failures
// only: a source that ANSWERS "gone" still aborts the walk (gone is never
// a link). darkSource simulates the future mountcache's offline shape —
// metadata served, selected reads Unavailable.

// darkSource wraps a real plugin client, failing selected calls the way a
// dark mount does (codes.Unavailable) — or with an injected verdict, for
// the gone-is-not-a-link pin.
type darkSource struct {
	pb.GridwellClient
	darkContent  map[string]bool // local tile id → ReadContent unavailable
	darkGrids    map[string]bool // local grid id → GetGrid unavailable
	darkPreviews map[string]bool // local tile id → GetTilePreview unavailable
	verdict      map[string]error
	// darkFramingFrom fails the Nth (1-indexed) and every later SetFraming
	// — the dest-goes-dark-MID-copy shape (a framing writeback into a
	// mount whose tunnel dropped partway through the walk). 0 = never.
	darkFramingFrom int
	framingCalls    int
}

func (d *darkSource) SetFraming(ctx context.Context, in *pb.SetFramingRequest, opts ...grpc.CallOption) (*pb.SetFramingResponse, error) {
	d.framingCalls++
	if d.darkFramingFrom > 0 && d.framingCalls >= d.darkFramingFrom {
		return nil, status.Error(codes.Unavailable, "mount dark: tunnel dropped")
	}
	return d.GridwellClient.SetFraming(ctx, in, opts...)
}

func (d *darkSource) ReadContent(ctx context.Context, in *pb.ReadContentRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[pb.ContentChunk], error) {
	if err, ok := d.verdict[in.TileId]; ok {
		return nil, err
	}
	if d.darkContent[in.TileId] {
		return nil, status.Error(codes.Unavailable, "mount dark: content not cached")
	}
	return d.GridwellClient.ReadContent(ctx, in, opts...)
}

func (d *darkSource) GetGrid(ctx context.Context, in *pb.GetGridRequest, opts ...grpc.CallOption) (*pb.GetGridResponse, error) {
	if d.darkGrids[in.GridId] {
		return nil, status.Error(codes.Unavailable, "mount dark: grid not cached")
	}
	return d.GridwellClient.GetGrid(ctx, in, opts...)
}

func (d *darkSource) GetTilePreview(ctx context.Context, in *pb.GetTilePreviewRequest, opts ...grpc.CallOption) (*pb.GetTilePreviewResponse, error) {
	if d.darkPreviews[in.TileId] {
		return nil, status.Error(codes.Unavailable, "mount dark: preview not cached")
	}
	return d.GridwellClient.GetTilePreview(ctx, in, opts...)
}

// darkTwoPluginServer is twoPluginServer with plugin A's client wrapped in
// a darkSource the test controls.
func darkTwoPluginServer(t *testing.T) (cl *rpc.Client, dark *darkSource, uuidA, rootA, uuidB, rootB string) {
	t.Helper()
	ctx := context.Background()
	reg := plugin.NewRegistry()

	stA, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stA.Close() })
	uuidA, err = stA.PluginUUID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clientA, closerA, err := plugin.ServeInProcess(local.New(stA, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closerA)
	dark = &darkSource{GridwellClient: clientA,
		darkContent: map[string]bool{}, darkGrids: map[string]bool{},
		darkPreviews: map[string]bool{}, verdict: map[string]error{}}
	reg.Register(uuidA, "home", dark, nil)
	bareRootA, err := stA.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootA = uuidA + "/" + bareRootA

	stB, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stB.Close() })
	uuidB, err = stB.PluginUUID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clientB, closerB, err := plugin.ServeInProcess(local.New(stB, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closerB)
	reg.Register(uuidB, "home", clientB, nil)
	bareRootB, err := stB.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootB = uuidB + "/" + bareRootB

	srv := mustNew(t, reg, Config{})
	hs := serveWeb(t, srv)
	return rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON()), dark, uuidA, rootA, uuidB, rootB
}

func localID(t *testing.T, qualified string) string {
	t.Helper()
	_, local, ok := rpc.SplitID(qualified)
	if !ok {
		t.Fatalf("not a qualified id: %q", qualified)
	}
	return local
}

func TestDeepCopyDegradesToLinksWhenSourceDark(t *testing.T) {
	cl, dark, _, rootA, _, rootB := darkTwoPluginServer(t)
	ctx := context.Background()

	well, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: rootA, X: 0, Y: 0, W: 1, H: 1, Label: "trip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: well.ChildGridID, X: 0, Y: 0, W: 1, H: 1, Data: []byte("cached notes"),
	}); err != nil {
		t.Fatal(err)
	}
	uncached, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: well.ChildGridID, X: 2, Y: 0, W: 1, H: 1, Data: []byte("never opened"),
	})
	if err != nil {
		t.Fatal(err)
	}
	nested, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: well.ChildGridID, X: 4, Y: 0, W: 1, H: 1, Label: "unvisited",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: nested.ChildGridID, X: 0, Y: 0, W: 1, H: 1, Data: []byte("deep"),
	}); err != nil {
		t.Fatal(err)
	}
	urlTile, err := cl.CreateURL(ctx, &rpc.CreateURLRequest{GridID: well.ChildGridID, X: 6, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	urlTile, err = cl.WriteContent(ctx, urlTile.ID, urlTile.Version, []byte("https://example.com/album"))
	if err != nil {
		t.Fatal(err)
	}
	urlTile, err = cl.SetURLState(ctx, &rpc.SetURLStateRequest{
		TileID: urlTile.ID,
		JPEG:   []byte("\xff\xd8fakejpeg"), URL: "https://example.com/album", Title: "album",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The offline shape: metadata reachable (cached), these reads dark.
	dark.darkContent[localID(t, uncached.ID)] = true
	dark.darkGrids[localID(t, nested.ChildGridID)] = true
	dark.darkPreviews[localID(t, urlTile.ID)] = true

	copyTop, err := cl.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: well.ID, DestGridID: rootB, X: 0, Y: 0,
	})
	if err != nil {
		t.Fatalf("offline deep copy must not refuse: %v", err)
	}
	if copyTop.Reference {
		t.Fatal("the top copy must be SOLID (its own grid was reachable)")
	}

	cg, err := cl.GetGrid(ctx, copyTop.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	byX := map[int64]*rpc.Tile{}
	for i := range cg.Tiles {
		byX[cg.Tiles[i].X] = &cg.Tiles[i]
	}

	// Cached text → a real copy with the bytes.
	if c := byX[0]; c == nil || c.Reference || c.LinkTargetID != "" {
		t.Fatalf("cached text should be a solid copy: %+v", byX[0])
	} else if body, _, _, err := cl.ReadContent(ctx, c.ID); err != nil || string(body) != "cached notes" {
		t.Fatalf("cached copy body = %q (%v)", body, err)
	}

	// Dark-content text → a LINK to the original (dashed says "elsewhere").
	if l := byX[2]; l == nil || l.LinkTargetID != uncached.ID {
		t.Fatalf("dark text should degrade to a link to %s: %+v", uncached.ID, byX[2])
	} else if !l.Reference {
		t.Error("the degraded link must derive Reference=true")
	}

	// Dark-grid nested well → a well LINK sharing the original child.
	if w := byX[4]; w == nil || !w.Reference || w.ChildGridID != nested.ChildGridID {
		t.Fatalf("dark nested well should degrade to a link to %s: %+v", nested.ChildGridID, byX[4])
	}

	// Dark-preview url → a solid copy of the ADDRESS, faceless.
	if u := byX[6]; u == nil || u.Reference || u.URLString != "https://example.com/album" {
		t.Fatalf("dark-preview url should copy its address: %+v", byX[6])
	} else if u.PreviewBlobID != 0 {
		t.Error("the faceless copy must carry no preview blob")
	}
}

func TestTopLevelCloneDegradesWhenSourceDark(t *testing.T) {
	cl, dark, _, rootA, _, rootB := darkTwoPluginServer(t)
	ctx := context.Background()

	// A text whose bytes are dark: the single-tile right-drag degrades to a
	// leaf link.
	txt, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: rootA, X: 0, Y: 0, W: 1, H: 1, Data: []byte("body"),
	})
	if err != nil {
		t.Fatal(err)
	}
	dark.darkContent[localID(t, txt.ID)] = true
	got, err := cl.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: txt.ID, DestGridID: rootB, X: 0, Y: 0,
	})
	if err != nil {
		t.Fatalf("dark leaf clone must degrade, not fail: %v", err)
	}
	if got.LinkTargetID != txt.ID || !got.Reference {
		t.Fatalf("dark leaf clone should be a link to %s: %+v", txt.ID, got)
	}

	// A well whose OWN child grid is dark: the whole room degrades to an
	// exit-well link, framing preserved.
	well, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: rootA, X: 3, Y: 0, W: 1, H: 1, Label: "dark room",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.SetFraming(ctx, &rpc.SetFramingRequest{
		TileID: well.ID, Framing: rpc.Framing{Cx: 5, Cy: 6, Zoom: 1.5},
	}); err != nil {
		t.Fatal(err)
	}
	dark.darkGrids[localID(t, well.ChildGridID)] = true
	gotWell, err := cl.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: well.ID, DestGridID: rootB, X: 3, Y: 0,
	})
	if err != nil {
		t.Fatalf("dark well clone must degrade, not fail: %v", err)
	}
	if !gotWell.Reference || gotWell.ChildGridID != well.ChildGridID {
		t.Fatalf("dark well clone should be a link sharing %s: %+v", well.ChildGridID, gotWell)
	}
	if gotWell.ViewCx != 5 || gotWell.ViewCy != 6 || gotWell.ViewZoom != 1.5 {
		t.Errorf("degraded well link lost the framing: %+v", gotWell)
	}
}

// TestGoneIsNeverALink pins the gate: a source that ANSWERS — NotFound, any
// verdict — still aborts the walk with the visible-partial contract. Only
// the-server-never-spoke degrades. Turning a verdict into a link would
// resurrect deleted content as a dangling reference that looks deliberate.
func TestGoneIsNeverALink(t *testing.T) {
	cl, dark, _, rootA, _, rootB := darkTwoPluginServer(t)
	ctx := context.Background()

	well, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: rootA, X: 0, Y: 0, W: 1, H: 1, Label: "trip",
	})
	if err != nil {
		t.Fatal(err)
	}
	txt, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: well.ChildGridID, X: 0, Y: 0, W: 1, H: 1, Data: []byte("body"),
	})
	if err != nil {
		t.Fatal(err)
	}
	dark.verdict[localID(t, txt.ID)] = status.Error(codes.NotFound, "no such tile")

	_, err = cl.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: well.ID, DestGridID: rootB, X: 0, Y: 0,
	})
	if err == nil || !strings.Contains(err.Error(), "deep copy incomplete") {
		t.Fatalf("a verdict mid-walk must abort with the partial contract, got: %v", err)
	}
}

// TestMidCopyFailureNeverDoublesTheWell pins the deepCopyTile well arm's
// degrade guard: a failure AFTER the copy well exists (here: the DEST going
// dark on the framing writeback) must surface as that failure — never fire
// the dark-SOURCE degrade, which would stack a link well on the very cell
// the partial copy already occupies (the user then saw an "overlap" refusal
// pointing at a grid they never touched). Same guard the top-level clone
// already has (out != nil before sourceUnreachable); the child arm lacked it.
func TestMidCopyFailureNeverDoublesTheWell(t *testing.T) {
	cl, dark, _, rootA, _, rootB := darkTwoPluginServer(t)
	ctx := context.Background()

	// Source lives in HEALTHY plugin B; the wrapped plugin A is the dest.
	well, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: rootB, X: 0, Y: 0, W: 1, H: 1, Label: "outer",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A NESTED well, so the failing copy runs through the child arm (the
	// top-level clone has its own, correct, guard).
	if _, err := cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: well.ChildGridID, X: 0, Y: 0, W: 1, H: 1, Label: "inner",
	}); err != nil {
		t.Fatal(err)
	}

	// The dest tunnel drops MID-copy: the outer copy's framing (1st
	// SetFraming) lands, the inner's (2nd) fails — so the failure travels
	// up through the child arm with the inner copy already created.
	dark.darkFramingFrom = 2

	_, cerr := cl.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID: well.ID, DestGridID: rootA, X: 0, Y: 0,
	})
	if cerr == nil {
		t.Fatal("a dest-dark mid-copy must surface, not pretend success")
	}
	if strings.Contains(cerr.Error(), "overlap") {
		t.Fatalf("the degrade stacked a link on the partial copy (overlap refusal): %v", cerr)
	}
	if !strings.Contains(cerr.Error(), "mount dark") {
		t.Fatalf("the REAL failure (dest unavailable) must surface, got: %v", cerr)
	}
}
