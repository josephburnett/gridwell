package parity_test

// The parity oracle's own proof (docs/v2-design.md §8.4). These tests
// cross the real seam the migration gate depends on: a full server
// (registry + localdb plugin + Connect handler) crawled over HTTP by
// the same client every caller uses. Two servers seeded by identical
// operations must diff empty; a single divergent write must surface as
// exactly that difference; and crawling one server twice must diff
// empty (reading never mutates — the guiding rule as a test).

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/parity"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

// node is one in-process gridwell: a localdb plugin behind a real
// Connect handler, plus the typed client the crawler uses.
type node struct {
	cl   *rpc.Client
	root string
}

func newNode(t *testing.T) *node {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	uuid, err := st.PluginUUID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	client, closer, err := plugin.ServeInProcess(local.New(st, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register(uuid, "home", client, nil)
	srv := servertest.New(t, reg, server.Config{})
	hs := servertest.Serve(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	bareRoot, err := st.RootGridID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return &node{cl: cl, root: uuid + "/" + bareRoot}
}

// seed applies the same user actions to a node: a text tile with
// content, a well with framing, and a nested text inside the well.
func seed(t *testing.T, n *node) (textID string) {
	t.Helper()
	ctx := context.Background()
	txt, err := n.cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: n.root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("# hello\n\nworld"),
	})
	if err != nil {
		t.Fatal(err)
	}
	well, err := n.cl.CreateWell(ctx, &rpc.CreateWellRequest{GridID: n.root, X: 2, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.cl.SetWellView(ctx, &rpc.SetWellViewRequest{
		TileID: well.ID, Version: well.Version, ViewX: 3, ViewY: -1, ViewZoom: 1.5,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := n.cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: well.ChildGridID, X: 1, Y: 1, W: 2, H: 1, Data: []byte("nested"),
	}); err != nil {
		t.Fatal(err)
	}
	return txt.ID
}

// crawlBoth crawls two INDEPENDENT fixture nodes separately. (CrawlPair
// is for two servers over the same logical data — same ids — where the
// union frontier is the point; two independent stores would just probe
// each other's foreign roots.)
func crawlBoth(t *testing.T, a, b *node) (*parity.Snapshot, *parity.Snapshot) {
	t.Helper()
	ctx := context.Background()
	sa, err := parity.Crawl(ctx, a.cl, parity.Options{SkipPreviews: true})
	if err != nil {
		t.Fatal(err)
	}
	sb, err := parity.Crawl(ctx, b.cl, parity.Options{SkipPreviews: true})
	if err != nil {
		t.Fatal(err)
	}
	return sa, sb
}

func TestCrawlPairSameData(t *testing.T) {
	// The pair crawl's contract on genuinely shared data: both sides
	// visit the union frontier and come back identical.
	n := newNode(t)
	seed(t, n)
	sa, sb, err := parity.CrawlPair(context.Background(), n.cl, n.cl, parity.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if diffs := parity.Diff(sa, sb, parity.Policy{}); len(diffs) != 0 {
		t.Fatalf("pair crawl of one node differs with itself:\n%s", strings.Join(diffs, "\n"))
	}
}

func TestIdenticalNodesDiffEmpty(t *testing.T) {
	a, b := newNode(t), newNode(t)
	seed(t, a)
	seed(t, b)
	sa, sb := crawlBoth(t, a, b)
	// The two stores mint different plugin uuids and object ids — the
	// ONLY legitimate differences between two independently-seeded
	// fixtures. The real migration copies identity verbatim, so the
	// real gate runs with none of these ignores; the fixture names its
	// blind spots explicitly instead.
	diffs := parity.Diff(remapNS(sa, "NS"), remapNS(sb, "NS"), parity.Policy{
		IgnoreFields: map[string]bool{"object_id": true},
	})
	if len(diffs) != 0 {
		t.Fatalf("identically-seeded nodes differ:\n%s", strings.Join(diffs, "\n"))
	}
}

func TestCrawlTwiceDiffEmpty(t *testing.T) {
	// Reading never mutates: two crawls of the SAME node are
	// byte-identical, with no policy at all.
	n := newNode(t)
	seed(t, n)
	ctx := context.Background()
	s1, err := parity.Crawl(ctx, n.cl, parity.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := parity.Crawl(ctx, n.cl, parity.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if diffs := parity.Diff(s1, s2, parity.Policy{}); len(diffs) != 0 {
		t.Fatalf("a second crawl differs (a read mutated something):\n%s", strings.Join(diffs, "\n"))
	}
}

func TestPlacementDivergenceSurfaces(t *testing.T) {
	a, b := newNode(t), newNode(t)
	textA := seed(t, a)
	seed(t, b)
	ctx := context.Background()
	tile, err := a.cl.GetTile(ctx, textA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.cl.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: textA, Version: tile.Version, GridID: tile.GridID, X: 7, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	sa, sb := crawlBoth(t, a, b)
	diffs := parity.Diff(remapNS(sa, "NS"), remapNS(sb, "NS"), parity.Policy{
		IgnoreFields: map[string]bool{"object_id": true},
	})
	found := false
	for _, d := range diffs {
		if strings.Contains(d, "x: 7") {
			found = true
		}
	}
	if !found {
		t.Fatalf("moved tile did not surface as an x difference; diffs:\n%s", strings.Join(diffs, "\n"))
	}
}

func TestContentDivergenceSurfaces(t *testing.T) {
	a, b := newNode(t), newNode(t)
	textA := seed(t, a)
	seed(t, b)
	ctx := context.Background()
	tile, err := a.cl.GetTile(ctx, textA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.cl.WriteContent(ctx, textA, tile.Version, []byte("# hello\n\nCHANGED")); err != nil {
		t.Fatal(err)
	}
	sa, sb := crawlBoth(t, a, b)
	diffs := parity.Diff(remapNS(sa, "NS"), remapNS(sb, "NS"), parity.Policy{
		// version legitimately differs after the edit; the content hash
		// must still surface on its own.
		IgnoreFields: map[string]bool{"object_id": true, "version": true},
	})
	found := false
	for _, d := range diffs {
		if strings.HasPrefix(d, "content ") {
			found = true
		}
	}
	if !found {
		t.Fatalf("edited content did not surface as a content difference; diffs:\n%s", strings.Join(diffs, "\n"))
	}
}

func TestTransitBoundary(t *testing.T) {
	if parity.TooDeep("uuid/5") {
		t.Fatal("a leaf grid id must not read as transit")
	}
	if !parity.TooDeep("ssh/conn/plug/0") {
		t.Fatal("a chained grid id must read as transit")
	}
}

func TestVolatileNamespaceSuppressesPresenceOnly(t *testing.T) {
	// Hand-built snapshots: proc-like churn (a tile only in A, and
	// changed content) is suppressed; a placement change on a common
	// tile still surfaces.
	a := &parity.Snapshot{
		Grids: map[string]parity.GridRecord{"proc/1": {TileIDs: []string{"proc/2", "proc/3"}}},
		Tiles: map[string]map[string]any{
			"proc/2": {"x": float64(1)},
			"proc/3": {"x": float64(9)},
		},
		Contents: map[string]parity.ContentRecord{"proc/2": {SHA256: "aaa"}},
		Plugins:  map[string]map[string]any{},
	}
	b := &parity.Snapshot{
		Grids: map[string]parity.GridRecord{"proc/1": {TileIDs: []string{"proc/2"}}},
		Tiles: map[string]map[string]any{
			"proc/2": {"x": float64(2)},
		},
		Contents: map[string]parity.ContentRecord{"proc/2": {SHA256: "bbb"}},
		Plugins:  map[string]map[string]any{},
	}
	diffs := parity.Diff(a, b, parity.Policy{VolatileNS: map[string]bool{"proc": true}})
	if len(diffs) != 1 || !strings.Contains(diffs[0], "x: 1 != 2") {
		t.Fatalf("want exactly the placement difference, got:\n%s", strings.Join(diffs, "\n"))
	}
}

func TestAllowIDsScopesTheDiff(t *testing.T) {
	a := &parity.Snapshot{
		Grids:   map[string]parity.GridRecord{"ns/1": {TileIDs: []string{"ns/2", "ns/9"}}},
		Tiles:   map[string]map[string]any{"ns/2": {"x": float64(1)}, "ns/9": {"x": float64(5)}},
		Plugins: map[string]map[string]any{},
	}
	b := &parity.Snapshot{
		Grids:   map[string]parity.GridRecord{"ns/1": {TileIDs: []string{"ns/2", "ns/9"}}},
		Tiles:   map[string]map[string]any{"ns/2": {"x": float64(1)}, "ns/9": {"x": float64(6)}},
		Plugins: map[string]map[string]any{},
	}
	diffs := parity.Diff(a, b, parity.Policy{AllowIDs: map[string]bool{"ns/1": true, "ns/2": true}})
	if len(diffs) != 0 {
		t.Fatalf("difference outside the allow set surfaced:\n%s", strings.Join(diffs, "\n"))
	}
}

// remapNS rewrites every namespace-dependent key and value to a fixed
// namespace so two INDEPENDENT fixture stores (different plugin uuids)
// become comparable. The real migration gate never needs this — uuids
// are copied verbatim — but fixtures can't share a uuid across two
// stores without defeating identity verification.
func remapNS(s *parity.Snapshot, ns string) *parity.Snapshot {
	var oldNS string
	for uuid := range s.Plugins {
		oldNS = uuid
	}
	swap := func(v string) string { return strings.ReplaceAll(v, oldNS, ns) }
	var deep func(v any) any
	deep = func(v any) any {
		switch t := v.(type) {
		case string:
			return swap(t)
		case map[string]any:
			o := map[string]any{}
			for k, vv := range t {
				o[k] = deep(vv)
			}
			return o
		case []any:
			o := make([]any, len(t))
			for i, vv := range t {
				o[i] = deep(vv)
			}
			return o
		default:
			return v
		}
	}
	out := &parity.Snapshot{
		NodeUUID: "node",
		Plugins:  map[string]map[string]any{},
		Grids:    map[string]parity.GridRecord{},
		Tiles:    map[string]map[string]any{},
		Contents: map[string]parity.ContentRecord{},
		Previews: map[string]string{},
	}
	swapMap := func(m map[string]any) map[string]any {
		if m == nil {
			return nil
		}
		return deep(m).(map[string]any)
	}
	for uuid, m := range s.Plugins {
		out.Plugins[swap(uuid)] = swapMap(m)
	}
	for gid, g := range s.Grids {
		ids := make([]string, len(g.TileIDs))
		for i, id := range g.TileIDs {
			ids[i] = swap(id)
		}
		out.Grids[swap(gid)] = parity.GridRecord{Fields: swapMap(g.Fields), TileIDs: ids, Err: g.Err}
	}
	for tid, m := range s.Tiles {
		out.Tiles[swap(tid)] = swapMap(m)
	}
	for tid, c := range s.Contents {
		out.Contents[swap(tid)] = c
	}
	for tid, pv := range s.Previews {
		out.Previews[swap(tid)] = pv
	}
	for _, sk := range s.Skipped {
		out.Skipped = append(out.Skipped, swap(sk))
	}
	return out
}

func TestMaxGridsAbortsLoudly(t *testing.T) {
	// A bounded crawl must ABORT, never silently cap: a truncated crawl
	// that returns normally would read as full coverage to the migration
	// gate.
	n := newNode(t)
	seed(t, n)
	_, err := parity.Crawl(context.Background(), n.cl, parity.Options{MaxGrids: 1})
	if err == nil || !strings.Contains(err.Error(), "MaxGrids") {
		t.Fatalf("bounded crawl did not abort loudly: %v", err)
	}
}
