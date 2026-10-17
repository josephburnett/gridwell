// Package mcpdoor is the MCP door's tool set: each tool a translation of one
// gesture into the router calls the client makes for it, every write reading
// first and claiming what it read, and every refusal the router answers
// handed back as a tool error with its reason. It decides nothing the router
// does not; docs/mcp.md is the contract.
package mcpdoor

import (
	"cmp"
	"context"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// Path is where the web door serves the MCP door.
const Path = "/mcp"

const instructions = `Gridwell is a personal space of tiles on infinite grids. Every id is a
qualified address ("<node>/12", "<node>/<connection>/<far-node>/7"); name it
back exactly as a tool answered it. A write claims what you read: pass the
version read_tile or read_grid gave you and a write made since is refused as a
version conflict, or omit it to claim the row as it stands now; select claims
nothing. Nothing here
destroys a tile: delete moves it to the trash. Shells are not reachable here.
When the user says "this", "here" or "these", call pointed: it answers the
grid or the selection in front of them.`

// New is the MCP server over rt, the node's router, which every tool calls as
// the browser codec does. node is the node's own id, the frame dead links are
// judged in, and screens is what the node knows of the clients showing it.
func New(rt namespace.Namespace, node, version string, screens Screens) *mcp.Server {
	d := &door{rt: rt, node: node, screens: screens}
	s := mcp.NewServer(&mcp.Implementation{Name: "gridwell", Version: version},
		&mcp.ServerOptions{Instructions: instructions})
	mcp.AddTool(s, &mcp.Tool{Name: "read_grid", Description: "Read a grid: its selection, the tiles the user has in hand, and its rows with id, kind, name, placement, version, and whether each is a link and dead. No id reads the node's home grid."}, d.readGrid)
	mcp.AddTool(s, &mcp.Tool{Name: "read_tile", Description: "Read one tile: its row, and the body of a text tile, the address of a url, the session a shell names, or the grid a well holds."}, d.readTile)
	mcp.AddTool(s, &mcp.Tool{Name: "find", Description: "Find tiles by name, body or address, everywhere or within one namespace; each hit carries the wells it sits inside."}, d.find)
	mcp.AddTool(s, &mcp.Tool{Name: "create_text", Description: "Create a text tile with a body in a grid. Its name is the body's first line. It lands at the first free cell, or at or below the cell named in near."}, d.createText)
	mcp.AddTool(s, &mcp.Tool{Name: "write_text", Description: "Replace a text tile's body, claiming the version you read (or the one on disk now when none is given)."}, d.writeText)
	mcp.AddTool(s, &mcp.Tool{Name: "rename", Description: "Rename a url, shell, well or pane tile. A text tile's name is its first line, so write_text renames it."}, d.rename)
	mcp.AddTool(s, &mcp.Tool{Name: "place", Description: "Move or resize a tile, within its grid or to another grid of the same namespace, claiming the placement it has now."}, d.place)
	mcp.AddTool(s, &mcp.Tool{Name: "clone", Description: "Deep-copy a tile into a grid (its own when none is named), at the first free cell near it or at the cell named."}, d.clone)
	mcp.AddTool(s, &mcp.Tool{Name: "link", Description: "Create a link to a tile in a grid: a dashed tile showing the same content, in this namespace or across one."}, d.link)
	mcp.AddTool(s, &mcp.Tool{Name: "delete", Description: "Delete a tile: it moves to the trash, claiming the version and placement it has now (or the version you read). Deleting a link unlinks."}, d.delete)
	mcp.AddTool(s, &mcp.Tool{Name: "select", Description: "Clear, add tiles to and take tiles out of a grid's selection, the tiles the user has in hand; clear with recurse also clears every grid inside its wells. It claims nothing: the user's own selects meanwhile stand, and the last select of a tile wins. A pane inside a pane tile shows its own set, which this does not write."}, d.selectTiles)
	mcp.AddTool(s, &mcp.Tool{Name: "sessions", Description: "List every screen showing this node now: each client session's label, when its view last changed, and its panes, each with its place, the grid it shows, the pane tile it is inside, and whether it has focus."}, d.sessions)
	mcp.AddTool(s, &mcp.Tool{Name: "pointed", Description: "What the user means by \"here\" and \"these\": in one session's focused pane, the selection that pane shows when it has one (the grid's, or inside a pane tile the pane's own), else the grid. The session is the one named, else the one whose pane holds this agent's shell live, else the one whose view changed last."}, d.pointed)
	return s
}

type door struct {
	rt      namespace.Namespace
	node    string
	screens Screens
}

// Cell is a grid cell.
type Cell struct {
	X int64 `json:"x"`
	Y int64 `json:"y"`
}

func (c *Cell) near() *[2]int64 {
	if c == nil {
		return nil
	}
	return &[2]int64{c.X, c.Y}
}

type gridIn struct {
	ID string `json:"id,omitempty" jsonschema:"the grid's id; empty is the node's home grid"`
}

func (d *door) readGrid(ctx context.Context, _ *mcp.CallToolRequest, in gridIn) (*mcp.CallToolResult, GridOut, error) {
	out, err := d.grid(ctx, in.ID)
	return nil, out, err
}

// grid is read_grid's answer for id, the home grid when id is empty.
func (d *door) grid(ctx context.Context, id string) (GridOut, error) {
	hs, err := d.rt.Handshake(ctx, &pb.HandshakeRequest{})
	if err != nil {
		return GridOut{}, refusal(err)
	}
	g, err := d.rt.GetGrid(ctx, &pb.GetGridRequest{GridId: cmp.Or(id, hs.GetHomeGridId())})
	if err != nil {
		return GridOut{}, refusal(err)
	}
	out := GridOut{Grid: gridOf(g.GetGrid()), Tiles: []Tile{}}
	for _, t := range g.GetTiles() {
		out.Tiles = append(out.Tiles, tileOf(t, hs.GetPlugins(), d.node))
	}
	return out, nil
}

type tileIn struct {
	ID string `json:"id" jsonschema:"the tile's id"`
}

func (d *door) readTile(ctx context.Context, _ *mcp.CallToolRequest, in tileIn) (*mcp.CallToolResult, TileOut, error) {
	row, err := d.tile(ctx, in.ID)
	if err != nil {
		return nil, TileOut{}, err
	}
	out := TileOut{Tile: tileOf(row, nil, d.node)}
	content := row
	if rpc.LeafLink(row) {
		// A link shows its target's content, and a target that is gone is
		// the link's dead state, not a failed read.
		tr, err := d.rt.GetTile(ctx, &pb.GetTileRequest{TileId: rpc.ContentID(row)})
		if err != nil {
			if dead(err) {
				out.Tile.Dead = true
				return nil, out, nil
			}
			return nil, TileOut{}, refusal(err)
		}
		content = tr.GetTile()
	}
	switch content.GetKind() {
	case rpc.KindText:
		body, basis, err := d.readBody(ctx, row.GetId())
		if err != nil {
			return nil, TileOut{}, refusal(err)
		}
		out.Body, out.BodyVersion, out.BodyStamp = &body, &basis.Version, basis.Stamp
	case rpc.KindURL:
		out.URL = content.GetUrlString()
	case rpc.KindShell:
		out.ShellSession = rpc.ShellSession(content)
	}
	return nil, out, nil
}

type findIn struct {
	Query string `json:"query" jsonschema:"free text matched against names, bodies and addresses, or id:<tile-id>"`
	Scope string `json:"scope,omitempty" jsonschema:"a namespace or id to search within; empty searches everything this node reaches"`
	Limit int32  `json:"limit,omitempty" jsonschema:"the most hits each namespace answers; 0 is its default"`
}

func (d *door) find(ctx context.Context, _ *mcp.CallToolRequest, in findIn) (*mcp.CallToolResult, FindOut, error) {
	resp, err := d.rt.Search(ctx, &pb.SearchRequest{Query: in.Query, Scope: in.Scope, Limit: in.Limit})
	if err != nil {
		return nil, FindOut{}, refusal(err)
	}
	out := FindOut{Hits: []Hit{}}
	for _, r := range resp.GetResults() {
		h := Hit{Tile: tileOf(r.GetTile(), nil, d.node), Snippet: r.GetSnippet()}
		for _, p := range r.GetPath() {
			h.Inside = append(h.Inside, Crumb{ID: p.GetId(), Name: p.GetAltText()})
		}
		out.Hits = append(out.Hits, h)
	}
	for _, s := range resp.GetSkipped() {
		out.Skipped = append(out.Skipped, Skip{Namespace: s.GetNamespace(), Reason: s.GetReason()})
	}
	return nil, out, nil
}

type createTextIn struct {
	Grid string `json:"grid" jsonschema:"the grid to create it in"`
	Body string `json:"body" jsonschema:"the text, markdown; its first line is the tile's name"`
	Near *Cell  `json:"near,omitempty" jsonschema:"a cell to land at, or below it in its column when taken"`
}

func (d *door) createText(ctx context.Context, _ *mcp.CallToolRequest, in createTextIn) (*mcp.CallToolResult, TileOut, error) {
	x, y, err := d.slot(ctx, in.Grid, in.Near.near(), 1, 1)
	if err != nil {
		return nil, TileOut{}, err
	}
	resp, err := d.rt.CreateTile(ctx, &pb.CreateTileRequest{GridId: in.Grid,
		Tile: &pb.Tile{Kind: rpc.KindText, X: x, Y: y, W: 1, H: 1}, Content: []byte(in.Body)})
	return d.answer(resp, err)
}

type writeTextIn struct {
	ID      string `json:"id" jsonschema:"the text tile, or a link to one"`
	Body    string `json:"body" jsonschema:"the whole new body"`
	Version *int64 `json:"version,omitempty" jsonschema:"the body_version read_tile gave; omitted claims the version on disk now"`
	Stamp   string `json:"stamp,omitempty" jsonschema:"for a plugin's text, the body_stamp read_tile gave, in place of a version"`
}

func (d *door) writeText(ctx context.Context, _ *mcp.CallToolRequest, in writeTextIn) (*mcp.CallToolResult, TileOut, error) {
	row, err := d.tile(ctx, in.ID)
	if err != nil {
		return nil, TileOut{}, err
	}
	// A link owns no bytes; the write goes to the content it names, as the
	// editor's does.
	target := rpc.ContentID(row)
	claim := rpc.ContentBasis{Stamp: in.Stamp}
	if in.Version != nil {
		claim.Version = *in.Version
	} else if in.Stamp == "" {
		if _, claim, err = d.readBody(ctx, target); err != nil {
			return nil, TileOut{}, refusal(err)
		}
	}
	resp, err := d.rt.WriteContent(ctx, chunks(target, claim, []byte(in.Body)))
	return d.answer(resp, err)
}

type renameIn struct {
	ID      string `json:"id" jsonschema:"the tile"`
	Name    string `json:"name" jsonschema:"the new name"`
	Version *int64 `json:"version,omitempty" jsonschema:"the version you read; omitted claims the version on disk now"`
}

func (d *door) rename(ctx context.Context, _ *mcp.CallToolRequest, in renameIn) (*mcp.CallToolResult, TileOut, error) {
	row, err := d.tile(ctx, in.ID)
	if err != nil {
		return nil, TileOut{}, err
	}
	version := row.GetVersion()
	if in.Version != nil {
		version = *in.Version
	}
	resp, err := d.rt.SetTile(ctx, &pb.SetTileRequest{TileId: row.GetId(), Version: version, Rename: in.Name})
	return d.answer(resp, err)
}

type placeIn struct {
	ID   string `json:"id" jsonschema:"the tile"`
	X    int64  `json:"x" jsonschema:"the column of its top-left cell"`
	Y    int64  `json:"y" jsonschema:"the row of its top-left cell"`
	W    int64  `json:"w,omitempty" jsonschema:"its width in cells; 0 keeps it"`
	H    int64  `json:"h,omitempty" jsonschema:"its height in cells; 0 keeps it"`
	Grid string `json:"grid,omitempty" jsonschema:"the grid to move it to, in the same namespace; empty keeps its grid"`
}

func (d *door) place(ctx context.Context, _ *mcp.CallToolRequest, in placeIn) (*mcp.CallToolResult, TileOut, error) {
	row, err := d.tile(ctx, in.ID)
	if err != nil {
		return nil, TileOut{}, err
	}
	resp, err := d.rt.PlaceTile(ctx, &pb.PlaceTileRequest{TileId: row.GetId(),
		GridId: cmp.Or(in.Grid, row.GetGridId()), X: in.X, Y: in.Y,
		W: cmp.Or(in.W, row.GetW()), H: cmp.Or(in.H, row.GetH()), Claim: rpc.PlacementOf(row)})
	return d.answer(resp, err)
}

type copyIn struct {
	ID   string `json:"id" jsonschema:"the tile"`
	Grid string `json:"grid,omitempty" jsonschema:"the grid it lands in; empty is the tile's own grid"`
	At   *Cell  `json:"at,omitempty" jsonschema:"a cell to land at, or below it in its column when taken"`
}

func (d *door) clone(ctx context.Context, _ *mcp.CallToolRequest, in copyIn) (*mcp.CallToolResult, TileOut, error) {
	src, grid, x, y, err := d.landing(ctx, in)
	if err != nil {
		return nil, TileOut{}, err
	}
	resp, err := d.rt.CloneTile(ctx, &pb.CloneTileRequest{TileId: src.GetId(), DestGridId: grid, X: x, Y: y})
	return d.answer(resp, err)
}

func (d *door) link(ctx context.Context, _ *mcp.CallToolRequest, in copyIn) (*mcp.CallToolResult, TileOut, error) {
	src, grid, x, y, err := d.landing(ctx, in)
	if err != nil {
		return nil, TileOut{}, err
	}
	resp, err := d.rt.CreateTile(ctx, &pb.CreateTileRequest{GridId: grid, Tile: rpc.LinkOf(src, x, y)})
	return d.answer(resp, err)
}

type deleteIn struct {
	ID      string `json:"id" jsonschema:"the tile"`
	Version *int64 `json:"version,omitempty" jsonschema:"the version you read; omitted claims the version on disk now"`
}

func (d *door) delete(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, DeleteOut, error) {
	row, err := d.tile(ctx, in.ID)
	if err != nil {
		return nil, DeleteOut{}, err
	}
	req := rpc.DeleteOf(row)
	if in.Version != nil {
		req.ClaimVersion = *in.Version
	}
	resp, err := d.rt.DeleteTile(ctx, req)
	if err != nil {
		return nil, DeleteOut{}, refusal(err)
	}
	return nil, DeleteOut{Deleted: row.GetId(), SessionLeft: resp.GetSessionLeft()}, nil
}

type selectIn struct {
	Grid    string   `json:"grid" jsonschema:"the grid"`
	Add     []string `json:"add,omitempty" jsonschema:"tiles of this grid to put in the selection; one not on the grid is ignored"`
	Remove  []string `json:"remove,omitempty" jsonschema:"tiles to take out of the selection"`
	Clear   bool     `json:"clear,omitempty" jsonschema:"empty the grid's selection first, without naming it"`
	Recurse bool     `json:"recurse,omitempty" jsonschema:"with clear, also empty the selection of every grid inside this grid's wells, to any depth, within its namespace"`
}

func (d *door) selectTiles(ctx context.Context, _ *mcp.CallToolRequest, in selectIn) (*mcp.CallToolResult, GridOut, error) {
	if _, err := d.rt.SetSelection(ctx, &pb.SetSelectionRequest{GridId: in.Grid, Add: in.Add, Remove: in.Remove,
		Clear: in.Clear, Recurse: in.Recurse}); err != nil {
		return nil, GridOut{}, refusal(err)
	}
	out, err := d.grid(ctx, in.Grid)
	return nil, out, err
}

// tile is the row a write reads first, the claim's source.
func (d *door) tile(ctx context.Context, id string) (*pb.Tile, error) {
	resp, err := d.rt.GetTile(ctx, &pb.GetTileRequest{TileId: id})
	if err != nil {
		return nil, refusal(err)
	}
	return resp.GetTile(), nil
}

// landing is where a copy or link of in.ID lands: the named grid or the
// tile's own, at the named cell or, unnamed, beside the tile in its own grid
// and at the first free cell of another.
func (d *door) landing(ctx context.Context, in copyIn) (src *pb.Tile, grid string, x, y int64, err error) {
	if src, err = d.tile(ctx, in.ID); err != nil {
		return nil, "", 0, 0, err
	}
	grid = cmp.Or(in.Grid, src.GetGridId())
	near := in.At.near()
	if near == nil && grid == src.GetGridId() {
		near = &[2]int64{src.GetX() + src.GetW(), src.GetY()}
	}
	x, y, err = d.slot(ctx, grid, near, src.GetW(), src.GetH())
	return src, grid, x, y, err
}

// slot reads the grid and asks the one auto-place rule (store.FreeSlot)
// where a w×h tile lands in it.
func (d *door) slot(ctx context.Context, grid string, near *[2]int64, w, h int64) (x, y int64, err error) {
	g, err := d.rt.GetGrid(ctx, &pb.GetGridRequest{GridId: grid})
	if err != nil {
		return 0, 0, refusal(err)
	}
	x, y = store.FreeSlot(g.GetTiles(), near, w, h)
	return x, y, nil
}

// readBody drains a body and the basis it was read under.
func (d *door) readBody(ctx context.Context, id string) (string, rpc.ContentBasis, error) {
	var data []byte
	var basis rpc.ContentBasis
	first := true
	err := d.rt.ReadContent(ctx, &pb.ReadContentRequest{TileId: id}, func(c *pb.ContentChunk) error {
		if first {
			basis, first = rpc.ContentBasis{Version: c.GetVersion(), Stamp: c.GetContentStamp()}, false
		}
		data = append(data, c.GetData()...)
		return nil
	})
	return string(data), basis, err
}

// chunks is a WriteContent stream of data claiming claim, cut where the
// client cuts it.
func chunks(id string, claim rpc.ContentBasis, data []byte) func() (*pb.WriteContentRequest, error) {
	off, sent := 0, false
	return func() (*pb.WriteContentRequest, error) {
		if sent && off >= len(data) {
			return nil, io.EOF
		}
		end := min(off+rpc.ContentChunkBytes, len(data))
		m := &pb.WriteContentRequest{Data: data[off:end]}
		if !sent {
			m.TileId, m.Version, m.ContentStamp = id, claim.Version, claim.Stamp
		}
		off, sent = end, true
		return m, nil
	}
}

func (d *door) answer(resp *pb.TileResponse, err error) (*mcp.CallToolResult, TileOut, error) {
	if err != nil {
		return nil, TileOut{}, refusal(err)
	}
	return nil, TileOut{Tile: tileOf(resp.GetTile(), nil, d.node)}, nil
}
