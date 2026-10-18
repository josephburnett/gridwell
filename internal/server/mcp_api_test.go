package server

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/mcpapi"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// mcpNode is a real Server behind the real web API, with a home and a
// second namespace, "feed", the user can switch off.
type mcpNode struct {
	srv        *Server
	url, root  string
	feedRoot   string
	reg        *plugin.Registry
	connectURL string
	web        *http.Client
}

func newMCPNode(t *testing.T) *mcpNode {
	t.Helper()
	ctx := context.Background()
	home, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = home.Close() })
	reg := plugin.NewRegistry()
	_, root := registerPrimaryLocaldb(t, reg, home)
	feed, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = feed.Close() })
	reg.Register("feed", "feed", local.New(feed, nil), nil)
	reg.Switch("feed", "feed", func() {})
	feedRoot, err := feed.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv := mustNew(t, reg, Config{})
	hs := webAPITest(t, srv.WebHandler())
	return &mcpNode{srv: srv, url: hs.URL + mcpapi.Path, root: root, feedRoot: "feed/" + feedRoot, reg: reg, connectURL: hs.URL, web: hs.Client()}
}

// headers adds a fixed set of headers to every request an MCP client makes.
type headers struct {
	h    map[string]string
	base http.RoundTripper
}

func (h headers) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.h {
		r.Header.Set(k, v)
	}
	return h.base.RoundTrip(r)
}

// agent connects an MCP client to the API, sending hdr on every request.
func (n *mcpNode) agent(t *testing.T, hdr map[string]string) (*mcp.ClientSession, error) {
	t.Helper()
	hc := &http.Client{Transport: headers{h: hdr, base: n.web.Transport}}
	cl := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "0"}, nil)
	s, err := cl.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: n.url, HTTPClient: hc, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err == nil {
		t.Cleanup(func() { _ = s.Close() })
	}
	return s, err
}

func bearer(password string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + password}
}

// call runs a tool and decodes its answer into out, failing on a tool error.
func call(t *testing.T, s *mcp.ClientSession, tool string, args map[string]any, out any) {
	t.Helper()
	res := callRaw(t, s, tool, args)
	if res.IsError {
		t.Fatalf("%s %v: refused: %s", tool, args, toolText(res))
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s: %v in %s", tool, err, b)
	}
}

func callRaw(t *testing.T, s *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res
}

func toolText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// refusedWith asserts a tool error whose text leads with the kind of no.
func refusedWith(t *testing.T, res *mcp.CallToolResult, lead string) {
	t.Helper()
	if !res.IsError || !strings.HasPrefix(toolText(res), lead) {
		t.Fatalf("got error=%v %q, want a refusal starting %q", res.IsError, toolText(res), lead)
	}
}

// items is a bulk write's answer, failing on a whole-call refusal.
func items(t *testing.T, s *mcp.ClientSession, tool string, args ...map[string]any) []mcpapi.Verdict {
	t.Helper()
	var out mcpapi.WriteOut
	call(t, s, tool, map[string]any{"items": args}, &out)
	if len(out.Items) != len(args) {
		t.Fatalf("%s answered %d verdicts for %d items: %+v", tool, len(out.Items), len(args), out.Items)
	}
	return out.Items
}

// landed is the tile one bulk item wrote, failing on its refusal.
func landed(t *testing.T, v mcpapi.Verdict) mcpapi.Tile {
	t.Helper()
	if v.Verdict != "ok" || v.Tile == nil {
		t.Fatalf("verdict %+v, want ok with the tile", v)
	}
	return *v.Tile
}

// mkText creates a text tile on grid through write_text.
func mkText(t *testing.T, s *mcp.ClientSession, grid, body string) mcpapi.Tile {
	t.Helper()
	return landed(t, items(t, s, "write_text", map[string]any{"grid": grid, "body": body})[0])
}

// readTiles reads ids through read_tiles, one answer per id in order.
func readTiles(t *testing.T, s *mcp.ClientSession, ids ...string) []mcpapi.TileRead {
	t.Helper()
	var out mcpapi.ReadTilesOut
	call(t, s, "read_tiles", map[string]any{"ids": ids}, &out)
	if len(out.Tiles) != len(ids) {
		t.Fatalf("read_tiles answered %d for %d ids: %+v", len(out.Tiles), len(ids), out.Tiles)
	}
	return out.Tiles
}

// readOne is read_tiles of one id that must answer.
func readOne(t *testing.T, s *mcp.ClientSession, id string) mcpapi.TileRead {
	t.Helper()
	r := readTiles(t, s, id)[0]
	if r.Verdict != "ok" {
		t.Fatalf("read_tiles(%s) = %s", id, r.Verdict)
	}
	return r
}

// The API lists its ten tools and nothing else (docs/mcp.md).
func TestTheMCPAPIListsItsTenTools(t *testing.T) {
	n := newMCPNode(t)
	s, err := n.agent(t, bearer(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tl := range res.Tools {
		got = append(got, tl.Name)
	}
	want := []string{"copy", "delete", "find", "move", "read_grid", "read_tiles", "rename", "select", "sessions", "write_text"}
	if slices.Sort(got); !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

// Every tool crosses the real web API and the router, and every answer
// names the tile by the qualified address the agent names it back with.
func TestEveryMCPToolRoundTripsThroughTheRouter(t *testing.T) {
	n := newMCPNode(t)
	s, err := n.agent(t, bearer(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	ns := rpc.UUIDOf(n.root)
	qualified := func(what, id string) {
		t.Helper()
		if rpc.UUIDOf(id) != ns {
			t.Fatalf("%s answered %q, want an address in %s", what, id, ns)
		}
	}

	var grid mcpapi.GridOut
	call(t, s, "read_grid", nil, &grid)
	if grid.Grid.ID != n.root || !grid.Grid.AcceptsTiles || len(grid.Tiles) != 0 {
		t.Fatalf("read_grid() = %+v, want the empty home grid %s", grid, n.root)
	}

	near := map[string]any{"x": 2, "y": 1}
	made := items(t, s, "write_text",
		map[string]any{"grid": n.root, "body": "# plan\nfirst", "near": near},
		map[string]any{"grid": n.root, "body": "beside", "near": near})
	plan, second := landed(t, made[0]), landed(t, made[1])
	qualified("write_text", plan.ID)
	if plan.Kind != rpc.KindText || plan.X != 2 || plan.Y != 1 || plan.Name != "plan" {
		t.Fatalf("write_text created %+v, want text \"plan\" at (2,1)", plan)
	}
	if second.X != 2 || second.Y != 2 {
		t.Fatalf("a create near a taken cell landed at (%d,%d), want (2,2), below it", second.X, second.Y)
	}

	read := readOne(t, s, plan.ID)
	if read.Body == nil || *read.Body != "# plan\nfirst" {
		t.Fatalf("read_tiles body = %v, want the created body", read.Body)
	}

	wrote := landed(t, items(t, s, "write_text", map[string]any{"id": plan.ID, "body": "# plan\nsecond", "version": *read.BodyVersion})[0])
	if wrote.ID != plan.ID || wrote.Version != *read.BodyVersion+1 {
		t.Fatalf("write_text = %+v, want %s bumped past %d", wrote, plan.ID, *read.BodyVersion)
	}
	wrote = landed(t, items(t, s, "write_text", map[string]any{"id": plan.ID, "body": "# plan\nthird"})[0])
	if read = readOne(t, s, plan.ID); *read.Body != "# plan\nthird" {
		t.Fatalf("an unclaimed write_text left %q, want it to claim the row as it stands", *read.Body)
	}

	moved := landed(t, items(t, s, "move", map[string]any{"id": plan.ID, "x": 6, "y": 4, "w": 2})[0])
	if moved.X != 6 || moved.Y != 4 || moved.W != 2 || moved.H != 1 || moved.Version != wrote.Version {
		t.Fatalf("move = %+v, want 2x1 at (6,4) with the content version untouched", moved)
	}

	copies := items(t, s, "copy",
		map[string]any{"id": plan.ID, "link": false},
		map[string]any{"id": plan.ID, "link": true, "at": map[string]any{"x": 0, "y": 9}})
	cp, link := landed(t, copies[0]), landed(t, copies[1])
	qualified("copy", cp.ID)
	if cp.ID == plan.ID || cp.Link != "" || cp.X != 8 || cp.Y != 4 {
		t.Fatalf("copy = %+v, want a new tile, no link, beside the source at (8,4)", cp)
	}
	if link.Link != plan.ID || link.X != 0 || link.Y != 9 {
		t.Fatalf("copy with link = %+v, want a link to %s at (0,9)", link, plan.ID)
	}
	if through := readOne(t, s, link.ID); through.Body == nil || *through.Body != "# plan\nthird" || through.Tile.Dead {
		t.Fatalf("read_tiles through the link = %+v, want the target's body", through)
	}

	door, err := newRouter(n.srv).CreateTile(context.Background(), &gridwellv1.CreateTileRequest{GridId: n.root,
		Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	renamed := items(t, s, "rename",
		map[string]any{"id": door.GetTile().GetId(), "name": "inbox"},
		map[string]any{"id": plan.ID, "name": "x"})
	if r := landed(t, renamed[0]); r.Name != "inbox" || r.ChildGrid == "" {
		t.Fatalf("rename = %+v, want the door named inbox", r)
	}
	if !strings.HasPrefix(renamed[1].Verdict, "refused:") {
		t.Fatalf("renaming a text tile = %q, want refused", renamed[1].Verdict)
	}

	var found mcpapi.FindOut
	call(t, s, "find", map[string]any{"query": "third"}, &found)
	if len(found.Hits) == 0 || found.Hits[0].Tile.ID == "" {
		t.Fatalf("find(third) = %+v, want the text", found)
	}

	if gone := items(t, s, "delete", map[string]any{"id": cp.ID})[0]; gone.Verdict != "ok" || gone.Deleted != cp.ID {
		t.Fatalf("delete = %+v", gone)
	}
	call(t, s, "read_grid", map[string]any{"id": n.root}, &grid)
	for _, tl := range grid.Tiles {
		if tl.ID == cp.ID {
			t.Fatalf("the deleted copy is still on its grid: %+v", tl)
		}
	}
}

// verdicts asserts a bulk answer item by item: "ok", or the refusal's lead.
func verdicts(t *testing.T, tool string, got []mcpapi.Verdict, want ...string) {
	t.Helper()
	for i, v := range got {
		if want[i] == "ok" && v.Verdict != "ok" || want[i] != "ok" && !strings.HasPrefix(v.Verdict, want[i]) {
			t.Fatalf("%s item %d = %q, want %q (all: %+v)", tool, i, v.Verdict, want[i], got)
		}
	}
}

// Every bulk write applies its items in order, each with its own claim and
// verdict: a refused item in the middle stops nothing, and the items either
// side of it land.
func TestAnMCPBulkWriteRefusesOneItemAndLandsTheRest(t *testing.T) {
	n := newMCPNode(t)
	s, err := n.agent(t, bearer(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	a, b, c := mkText(t, s, n.root, "a"), mkText(t, s, n.root, "b"), mkText(t, s, n.root, "c")
	stale := readOne(t, s, b.ID)
	items(t, s, "write_text", map[string]any{"id": b.ID, "body": "b2"})

	verdicts(t, "write_text", items(t, s, "write_text",
		map[string]any{"id": a.ID, "body": "a3"},
		map[string]any{"id": b.ID, "body": "b3", "version": *stale.BodyVersion},
		map[string]any{"grid": n.root, "body": "new", "near": map[string]any{"x": 0, "y": 7}}),
		"ok", "conflict:", "ok")
	got := readTiles(t, s, a.ID, b.ID)
	if *got[0].Body != "a3" || *got[1].Body != "b2" {
		t.Fatalf("bodies %q %q, want a3 written and b2 kept", *got[0].Body, *got[1].Body)
	}
	verdicts(t, "write_text", items(t, s, "write_text",
		map[string]any{"id": a.ID, "grid": n.root, "body": "both"},
		map[string]any{"body": "neither"}),
		"refused:", "refused:")

	verdicts(t, "rename", items(t, s, "rename",
		map[string]any{"id": a.ID, "name": "x"}, map[string]any{"id": "nowhere/1", "name": "y"}),
		"refused:", "dead link:")

	verdicts(t, "move", items(t, s, "move",
		map[string]any{"id": a.ID, "x": 10, "y": 0},
		map[string]any{"id": b.ID, "x": 10, "y": 0},
		map[string]any{"id": c.ID, "x": 11, "y": 0}),
		"ok", "conflict:", "ok")

	verdicts(t, "copy", items(t, s, "copy",
		map[string]any{"id": a.ID, "grid": n.root, "at": map[string]any{"x": 20, "y": 0}},
		map[string]any{"id": "nowhere/1"},
		map[string]any{"id": c.ID, "link": true, "grid": n.root, "at": map[string]any{"x": 20, "y": 1}}),
		"ok", "dead link:", "ok")

	verdicts(t, "delete", items(t, s, "delete",
		map[string]any{"id": a.ID},
		map[string]any{"id": b.ID, "version": *stale.BodyVersion},
		map[string]any{"id": c.ID}),
		"ok", "conflict:", "ok")
	var grid mcpapi.GridOut
	call(t, s, "read_grid", map[string]any{"id": n.root}, &grid)
	on := map[string]bool{}
	for _, tl := range grid.Tiles {
		on[tl.ID] = true
	}
	if on[a.ID] || !on[b.ID] || on[c.ID] {
		t.Fatalf("after the bulk delete a:%v b:%v c:%v on the grid, want only b", on[a.ID], on[b.ID], on[c.ID])
	}

	refusedWith(t, callRaw(t, s, "delete", map[string]any{"items": []any{}}), "refused:")
}

// read_tiles answers each id in order: a dead link reads dead, and an id
// that names nothing is that item's refusal, never the call's.
func TestMCPReadTilesAnswersEachIdInOrder(t *testing.T) {
	n := newMCPNode(t)
	s, err := n.agent(t, bearer(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, gone := mkText(t, s, n.root, "a"), mkText(t, s, n.root, "gone")
	link := landed(t, items(t, s, "copy", map[string]any{"id": gone.ID, "link": true})[0])
	rt := newRouter(n.srv)
	for range 2 { // to the trash, then out of it
		row, err := rt.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: gone.ID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rt.DeleteTile(ctx, rpc.DeleteOf(row.GetTile())); err != nil {
			t.Fatal(err)
		}
	}

	got := readTiles(t, s, a.ID, "nowhere/1", link.ID, a.ID)
	if got[0].Verdict != "ok" || *got[0].Body != "a" || got[3].Verdict != "ok" || got[3].Tile.ID != a.ID {
		t.Fatalf("the good ids = %+v, %+v, want a's body twice", got[0], got[3])
	}
	if !strings.HasPrefix(got[1].Verdict, "dead link:") || got[1].Body != nil {
		t.Fatalf("an id naming nothing = %+v, want its own refusal", got[1])
	}
	if got[2].Verdict != "ok" || !got[2].Tile.Dead || got[2].Body != nil {
		t.Fatalf("the link to a destroyed tile = %+v, want it read dead", got[2])
	}
}

// find's filters reach the router: a grid with its doors, and the content
// time, keep only the tile whose body was written since.
func TestMCPFindFiltersReachTheStore(t *testing.T) {
	n := newMCPNode(t)
	s, err := n.agent(t, bearer(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	door, err := newRouter(n.srv).CreateTile(context.Background(), &gridwellv1.CreateTileRequest{GridId: n.root,
		Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 9, Y: 9, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	inner := door.GetTile().GetChildGridId()
	kept, still, outside := mkText(t, s, inner, "note kept"), mkText(t, s, inner, "note still"), mkText(t, s, n.root, "note outside")
	made := time.Now().Unix()
	for time.Now().Unix() == made {
		time.Sleep(10 * time.Millisecond)
	}
	since := time.Now().UTC().Format(time.RFC3339)
	items(t, s, "write_text", map[string]any{"id": kept.ID, "body": "note kept, rewritten"},
		map[string]any{"id": outside.ID, "body": "note outside, rewritten"})

	hits := func(args map[string]any) []string {
		t.Helper()
		var found mcpapi.FindOut
		call(t, s, "find", args, &found)
		var ids []string
		for _, h := range found.Hits {
			ids = append(ids, h.Tile.ID)
		}
		slices.Sort(ids)
		return ids
	}
	sorted := func(ids ...string) []string { slices.Sort(ids); return ids }
	if got := hits(map[string]any{"query": "note", "grid": n.root, "recurse": true}); !slices.Equal(got, sorted(kept.ID, still.ID, outside.ID)) {
		t.Fatalf("find in root with its doors = %v, want all three", got)
	}
	if got := hits(map[string]any{"query": "note", "grid": n.root}); !slices.Equal(got, []string{outside.ID}) {
		t.Fatalf("find in root alone = %v, want [%s]", got, outside.ID)
	}
	if got := hits(map[string]any{"query": "note", "grid": n.root, "recurse": true,
		"content_changed": map[string]any{"from": since}}); !slices.Equal(got, sorted(kept.ID, outside.ID)) {
		t.Fatalf("find changed since %s = %v, want the two rewritten", since, got)
	}
	if got := hits(map[string]any{"grid": inner, "content_changed": map[string]any{"from": since}}); !slices.Equal(got, []string{kept.ID}) {
		t.Fatalf("an empty query in the door changed since %s = %v, want [%s]", since, got, kept.ID)
	}
	if got := hits(map[string]any{"query": "note", "created": map[string]any{"to": "2000-01-01T00:00:00Z"}}); len(got) != 0 {
		t.Fatalf("find created before 2000 = %v, want none", got)
	}
	if got := hits(map[string]any{"query": "note", "modified": map[string]any{"from": since}, "grid": inner}); !slices.Equal(got, []string{kept.ID}) {
		t.Fatalf("find modified since %s in the door = %v, want [%s]", since, got, kept.ID)
	}
	var g mcpapi.GridOut
	call(t, s, "select", map[string]any{"grid": inner, "add": []string{still.ID}}, &g)
	if got := hits(map[string]any{"query": "note", "selected": true}); !slices.Equal(got, []string{still.ID}) {
		t.Fatalf("find selected = %v, want [%s]", got, still.ID)
	}
	refusedWith(t, callRaw(t, s, "find", map[string]any{"query": "note", "created": map[string]any{"from": "yesterday"}}), "refused:")

	// kind keeps the tiles of that kind, alone or with a query; one the node
	// does not know is refused by the router.
	pt, err := newRouter(n.srv).CreateTile(context.Background(), &gridwellv1.CreateTileRequest{GridId: inner,
		Tile: &gridwellv1.Tile{Kind: rpc.KindWorkspace, X: 7, Y: 7, W: 1, H: 1, AltText: "work"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := hits(map[string]any{"kind": "workspace"}); !slices.Equal(got, []string{pt.GetTile().GetId()}) {
		t.Fatalf("find kind workspace = %v, want [%s]", got, pt.GetTile().GetId())
	}
	if got := hits(map[string]any{"kind": "workspace", "content_changed": map[string]any{}}); !slices.Equal(got, []string{pt.GetTile().GetId()}) {
		t.Fatalf("find kind workspace by content time = %v, want [%s]", got, pt.GetTile().GetId())
	}
	if got := hits(map[string]any{"query": "note", "kind": "door"}); len(got) != 0 {
		t.Fatalf("find note of kind door = %v, want none", got)
	}
	refusedWith(t, callRaw(t, s, "find", map[string]any{"kind": "panes"}), "refused:")
}

// A write claims what the agent read: once another write has landed, the
// stale claim is refused with the conflict and its reason, and the first
// write stands.
func TestAStaleMCPClaimIsRefusedWithTheReason(t *testing.T) {
	n := newMCPNode(t)
	s, err := n.agent(t, bearer(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	made := mkText(t, s, n.root, "one")
	read := readOne(t, s, made.ID)
	items(t, s, "write_text", map[string]any{"id": made.ID, "body": "two", "version": *read.BodyVersion})

	verdicts(t, "write_text", items(t, s, "write_text", map[string]any{"id": made.ID, "body": "three", "version": *read.BodyVersion}), "conflict:")
	verdicts(t, "delete", items(t, s, "delete", map[string]any{"id": made.ID, "version": *read.BodyVersion}), "conflict:")
	if read = readOne(t, s, made.ID); *read.Body != "two" {
		t.Fatalf("on disk %q, want the first writer's", *read.Body)
	}
}

// A source the user switched off is read-only: the API's write is refused
// with the verdict and its sentence, as the browser's is.
func TestAnMCPWriteToADisabledSourceIsRefused(t *testing.T) {
	n := newMCPNode(t)
	made, err := newRouter(n.srv).CreateTile(context.Background(), &gridwellv1.CreateTileRequest{GridId: n.feedRoot,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}, Content: []byte("kept")})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.reg.Disable("feed"); err != nil {
		t.Fatal(err)
	}
	s, err := n.agent(t, bearer(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	id := made.GetTile().GetId()
	verdicts(t, "write_text", items(t, s, "write_text",
		map[string]any{"id": id, "body": "lost"}, map[string]any{"grid": n.feedRoot, "body": "x"}),
		"disabled:", "disabled:")
	verdicts(t, "delete", items(t, s, "delete", map[string]any{"id": id}), "disabled:")
	if read := readOne(t, s, id); *read.Body != "kept" || read.Tile.ReadOnly == "" {
		t.Fatalf("read_tiles = %+v, want the body kept and the row read-only with the reason", read)
	}
}

// The web password admits an agent as a bearer, a wrong one is refused, and
// the browser's cookie still opens the same API.
func TestTheMCPAPIAdmitsTheWebPasswordAsABearer(t *testing.T) {
	n := newMCPNode(t)
	if _, err := n.agent(t, bearer(testPassword)); err != nil {
		t.Fatalf("the web password as a bearer: %v", err)
	}
	if _, err := n.agent(t, bearer("wrong")); err == nil {
		t.Fatal("a wrong bearer was admitted")
	}
	if _, err := n.agent(t, nil); err == nil {
		t.Fatal("no credential was admitted")
	}
	if _, err := n.agent(t, map[string]string{"Cookie": AuthCookieName + "=" + AuthToken(testPassword)}); err != nil {
		t.Fatalf("the cookie: %v", err)
	}
	// The bearer is the password, never the cookie's derivation of it.
	if _, err := n.agent(t, bearer(AuthToken(testPassword))); err == nil {
		t.Fatal("the cookie value was admitted as a bearer")
	}
}

// The build gate judges a page's calls. An agent is no page of this node's
// build, so the gate never refuses it, even from this API's own origin,
// where it does refuse a Connect call naming no build.
func TestTheBuildGateDoesNotApplyToTheMCPAPI(t *testing.T) {
	n := newMCPNode(t)
	n.srv.build = "node-build"
	page := map[string]string{"Cookie": AuthCookieName + "=" + AuthToken(testPassword), "Origin": n.connectURL}
	cl := rpc.NewClient(&http.Client{Transport: headers{h: page, base: n.web.Transport}}, n.connectURL, connect.WithProtoJSON())
	if _, err := cl.GetGrid(context.Background(), n.root); err == nil {
		t.Fatal("control: the build gate let a page naming no build through")
	}

	hdr := bearer(testPassword)
	hdr["Origin"] = n.connectURL
	s, err := n.agent(t, hdr)
	if err != nil {
		t.Fatalf("the build gate refused the MCP API: %v", err)
	}
	var grid mcpapi.GridOut
	call(t, s, "read_grid", nil, &grid)
}

// A call is traced under the tile or grid it names first, a bulk call's
// first item's.
func TestAnMCPCallIsTracedUnderTheFirstIdItNames(t *testing.T) {
	for raw, want := range map[string]string{
		`{"id":"n/1"}`:                            "n/1",
		`{"grid":"n/2"}`:                          "n/2",
		`{"ids":["n/3","n/4"]}`:                   "n/3",
		`{"items":[{"grid":"n/5"},{"id":"n/6"}]}`: "n/5",
		`{"items":[{"id":"n/7","grid":"n/8"}]}`:   "n/7",
		`{"query":"x"}`:                           "",
	} {
		if got := tracedID(json.RawMessage(raw)); got != want {
			t.Errorf("tracedID(%s) = %q, want %q", raw, got, want)
		}
	}
}
