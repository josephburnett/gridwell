package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/mcpdoor"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// mcpNode is a real Server behind the real web door, with a home and a
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
	hs := webDoorTest(t, srv.WebHandler())
	return &mcpNode{srv: srv, url: hs.URL + mcpdoor.Path, root: root, feedRoot: "feed/" + feedRoot, reg: reg, connectURL: hs.URL, web: hs.Client()}
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

// agent connects an MCP client to the door, sending hdr on every request.
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

// Every tool crosses the real web door and the router, and every answer
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

	var grid mcpdoor.GridOut
	call(t, s, "read_grid", nil, &grid)
	if grid.Grid.ID != n.root || !grid.Grid.AcceptsTiles || len(grid.Tiles) != 0 {
		t.Fatalf("read_grid() = %+v, want the empty home grid %s", grid, n.root)
	}

	var made mcpdoor.TileOut
	call(t, s, "create_text", map[string]any{"grid": n.root, "body": "# plan\nfirst", "near": map[string]any{"x": 2, "y": 1}}, &made)
	qualified("create_text", made.Tile.ID)
	if made.Tile.Kind != rpc.KindText || made.Tile.X != 2 || made.Tile.Y != 1 || made.Tile.Name != "plan" {
		t.Fatalf("create_text = %+v, want text \"plan\" at (2,1)", made.Tile)
	}
	var second mcpdoor.TileOut
	call(t, s, "create_text", map[string]any{"grid": n.root, "body": "beside", "near": map[string]any{"x": 2, "y": 1}}, &second)
	if second.Tile.X != 2 || second.Tile.Y != 2 {
		t.Fatalf("a create near a taken cell landed at (%d,%d), want (2,2), below it", second.Tile.X, second.Tile.Y)
	}

	var read mcpdoor.TileOut
	call(t, s, "read_tile", map[string]any{"id": made.Tile.ID}, &read)
	if read.Body == nil || *read.Body != "# plan\nfirst" {
		t.Fatalf("read_tile body = %v, want the created body", read.Body)
	}

	var wrote mcpdoor.TileOut
	call(t, s, "write_text", map[string]any{"id": made.Tile.ID, "body": "# plan\nsecond", "version": *read.BodyVersion}, &wrote)
	if wrote.Tile.ID != made.Tile.ID || wrote.Tile.Version != *read.BodyVersion+1 {
		t.Fatalf("write_text = %+v, want %s bumped past %d", wrote.Tile, made.Tile.ID, *read.BodyVersion)
	}
	call(t, s, "write_text", map[string]any{"id": made.Tile.ID, "body": "# plan\nthird"}, &wrote)
	call(t, s, "read_tile", map[string]any{"id": made.Tile.ID}, &read)
	if *read.Body != "# plan\nthird" {
		t.Fatalf("an unclaimed write_text left %q, want it to claim the row as it stands", *read.Body)
	}

	var placed mcpdoor.TileOut
	call(t, s, "place", map[string]any{"id": made.Tile.ID, "x": 6, "y": 4, "w": 2}, &placed)
	if p := placed.Tile; p.X != 6 || p.Y != 4 || p.W != 2 || p.H != 1 || p.Version != wrote.Tile.Version {
		t.Fatalf("place = %+v, want 2x1 at (6,4) with the content version untouched", p)
	}

	var clone mcpdoor.TileOut
	call(t, s, "clone", map[string]any{"id": made.Tile.ID}, &clone)
	qualified("clone", clone.Tile.ID)
	if clone.Tile.ID == made.Tile.ID || clone.Tile.X != 8 || clone.Tile.Y != 4 {
		t.Fatalf("clone = %+v, want a new tile beside the source at (8,4)", clone.Tile)
	}
	var link mcpdoor.TileOut
	call(t, s, "link", map[string]any{"id": made.Tile.ID, "at": map[string]any{"x": 0, "y": 9}}, &link)
	if link.Tile.Link != made.Tile.ID || link.Tile.X != 0 || link.Tile.Y != 9 {
		t.Fatalf("link = %+v, want a link to %s at (0,9)", link.Tile, made.Tile.ID)
	}
	var through mcpdoor.TileOut
	call(t, s, "read_tile", map[string]any{"id": link.Tile.ID}, &through)
	if through.Body == nil || *through.Body != "# plan\nthird" || through.Tile.Dead {
		t.Fatalf("read_tile through the link = %+v, want the target's body", through)
	}

	well, err := newRouter(n.srv).CreateTile(context.Background(), &gridwellv1.CreateTileRequest{GridId: n.root,
		Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	var renamed mcpdoor.TileOut
	call(t, s, "rename", map[string]any{"id": well.GetTile().GetId(), "name": "inbox"}, &renamed)
	if renamed.Tile.Name != "inbox" || renamed.Tile.ChildGrid == "" {
		t.Fatalf("rename = %+v, want the well named inbox", renamed.Tile)
	}
	refusedWith(t, callRaw(t, s, "rename", map[string]any{"id": made.Tile.ID, "name": "x"}), "refused:")

	var found mcpdoor.FindOut
	call(t, s, "find", map[string]any{"query": "third"}, &found)
	if len(found.Hits) == 0 || found.Hits[0].Tile.ID == "" {
		t.Fatalf("find(third) = %+v, want the text", found)
	}

	var gone mcpdoor.DeleteOut
	call(t, s, "delete", map[string]any{"id": clone.Tile.ID}, &gone)
	if gone.Deleted != clone.Tile.ID {
		t.Fatalf("delete = %+v", gone)
	}
	call(t, s, "read_grid", map[string]any{"id": n.root}, &grid)
	for _, tl := range grid.Tiles {
		if tl.ID == clone.Tile.ID {
			t.Fatalf("the deleted clone is still on its grid: %+v", tl)
		}
	}
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
	var made, read, wrote mcpdoor.TileOut
	call(t, s, "create_text", map[string]any{"grid": n.root, "body": "one"}, &made)
	call(t, s, "read_tile", map[string]any{"id": made.Tile.ID}, &read)
	call(t, s, "write_text", map[string]any{"id": made.Tile.ID, "body": "two", "version": *read.BodyVersion}, &wrote)

	refusedWith(t, callRaw(t, s, "write_text", map[string]any{"id": made.Tile.ID, "body": "three", "version": *read.BodyVersion}), "conflict:")
	refusedWith(t, callRaw(t, s, "delete", map[string]any{"id": made.Tile.ID, "version": *read.BodyVersion}), "conflict:")
	call(t, s, "read_tile", map[string]any{"id": made.Tile.ID}, &read)
	if *read.Body != "two" {
		t.Fatalf("on disk %q, want the first writer's", *read.Body)
	}
}

// A source the user switched off is read-only: the door's write is refused
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
	refusedWith(t, callRaw(t, s, "write_text", map[string]any{"id": id, "body": "lost"}), "disabled:")
	refusedWith(t, callRaw(t, s, "create_text", map[string]any{"grid": n.feedRoot, "body": "x"}), "disabled:")
	refusedWith(t, callRaw(t, s, "delete", map[string]any{"id": id}), "disabled:")
	var read mcpdoor.TileOut
	call(t, s, "read_tile", map[string]any{"id": id}, &read)
	if *read.Body != "kept" || read.Tile.ReadOnly == "" {
		t.Fatalf("read_tile = %+v, want the body kept and the row read-only with the reason", read)
	}
}

// The web password admits an agent as a bearer, a wrong one is refused, and
// the browser's cookie still opens the same door.
func TestTheMCPDoorAdmitsTheWebPasswordAsABearer(t *testing.T) {
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
// build, so the gate never refuses it, even from this door's own origin,
// where it does refuse a Connect call naming no build.
func TestTheBuildGateDoesNotApplyToTheMCPDoor(t *testing.T) {
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
		t.Fatalf("the build gate refused the MCP door: %v", err)
	}
	var grid mcpdoor.GridOut
	call(t, s, "read_grid", nil, &grid)
}
