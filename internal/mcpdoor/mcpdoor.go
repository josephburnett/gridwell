// Package mcpdoor is the MCP door's tool set: each tool a translation of one
// gesture into the router calls the client makes for it, every write reading
// first and claiming what it read, and every refusal the router answers
// handed back as a tool error with its reason. It decides nothing the router
// does not; docs/mcp.md is the contract.
package mcpdoor

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

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
back exactly as a tool answered it. Every write takes a list of items and
answers a verdict per item, in order: "ok", or the refusal. Each item claims
what you read: pass the version read_tiles or read_grid gave you and a write
made since is refused as a conflict, or omit it to claim the row as it stands
now; select claims nothing. Nothing here destroys a tile: delete moves it to
the trash. Shells are not reachable here. When the user says "this", "here"
or "these", call pointed: it answers the grid or the selection in front of
them, and names every other screen.`

// New is the MCP server over rt, the node's router, which every tool calls as
// the browser codec does. node is the node's own id, the frame dead links are
// judged in, and screens is what the node knows of the clients showing it.
func New(rt namespace.Namespace, node, version string, screens Screens) *mcp.Server {
	d := &door{rt: rt, node: node, screens: screens}
	s := mcp.NewServer(&mcp.Implementation{Name: "gridwell", Version: version},
		&mcp.ServerOptions{Instructions: instructions})
	mcp.AddTool(s, &mcp.Tool{Name: "pointed", Description: "What the user means by \"here\" and \"these\": in one session's focused pane, its grid's selection when it has one, else the grid. The session is the one named, else the one whose pane holds this agent's shell live, else the one whose view changed last. It answers that session's panes and every other screen showing the node, with theirs."}, d.pointed)
	mcp.AddTool(s, &mcp.Tool{Name: "read_grid", Description: "Read a grid: its selection, the tiles the user has in hand, and its rows with id, kind, name, placement, version, and whether each is a link and dead. No id reads the node's home grid."}, d.readGrid)
	mcp.AddTool(s, &mcp.Tool{Name: "read_tiles", Description: "Read tiles by id, one answer per id in order: its row, and the body of a text tile (with body_version, the claim write_text takes), the address of a url, the session a shell names, or the grid a well holds. A link answers its target's content, or dead. An id that cannot be read is that answer's refusal, not the call's."}, d.readTiles)
	mcp.AddTool(s, &mcp.Tool{Name: "find", Description: "Find tiles by name, body or address, everywhere or within one namespace; each hit carries the wells it sits inside. Filters narrow it and combine: grid (with recurse, the grids inside its wells too), created, modified (any write: a move, a framing, content) and content_changed, each a {from, to} range of RFC 3339 times, either end left open, and selected (only tiles in their grid's selection). With a filter the query may be empty: it then lists what passes, newest first."}, d.find)
	mcp.AddTool(s, &mcp.Tool{Name: "write_text", Description: "Write text tiles, a list of items applied in order, a verdict per item. An item with id replaces that text tile's whole body (through a link, its target's), claiming the version you read or, with none, the one on disk now. An item with grid and no id creates a 1x1 text tile there, its name the body's first line, at the first free cell or at or below the cell named in near."}, d.writeText)
	mcp.AddTool(s, &mcp.Tool{Name: "rename", Description: "Rename url, shell, well or pane tiles, a list of items applied in order, a verdict per item. A text tile's name is its first line, so write_text renames it."}, d.rename)
	mcp.AddTool(s, &mcp.Tool{Name: "place", Description: "Move or resize tiles, within a grid or to another grid of the same namespace, a list of items applied in order, a verdict per item; each claims the placement its tile has now."}, d.place)
	mcp.AddTool(s, &mcp.Tool{Name: "copy", Description: "Copy tiles into a grid (the tile's own when none is named), at the cell named or beside the tile, a list of items applied in order, a verdict per item. link false is a deep copy; link true is a link: a dashed tile showing the same content, in this namespace or across one."}, d.copy)
	mcp.AddTool(s, &mcp.Tool{Name: "delete", Description: "Delete tiles, a list of items applied in order, a verdict per item: each moves to the trash, claiming the version and placement it has now (or the version you read). Deleting a link unlinks."}, d.delete)
	mcp.AddTool(s, &mcp.Tool{Name: "select", Description: "Clear, add tiles to and take tiles out of a grid's selection, the tiles the user has in hand; clear with recurse also clears every grid inside its wells, and clear with all (no grid) clears every selection the node and its far nodes hold. It claims nothing: the user's own selects meanwhile stand, and the last select of a tile wins. Every pane showing the grid shows it, inside a pane tile too."}, d.selectTiles)
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

type readTilesIn struct {
	IDs []string `json:"ids" jsonschema:"the tiles to read"`
}

func (d *door) readTiles(ctx context.Context, _ *mcp.CallToolRequest, in readTilesIn) (*mcp.CallToolResult, ReadTilesOut, error) {
	if len(in.IDs) == 0 {
		return nil, ReadTilesOut{}, errNoItems
	}
	out := ReadTilesOut{Tiles: make([]TileRead, 0, len(in.IDs))}
	for _, id := range in.IDs {
		r, err := d.readTile(ctx, id)
		out.Tiles = append(out.Tiles, TileRead{Verdict: verdictOf(err), TileOut: r})
	}
	return nil, out, nil
}

// readTile is one read_tiles answer.
func (d *door) readTile(ctx context.Context, id string) (TileOut, error) {
	row, err := d.tile(ctx, id)
	if err != nil {
		return TileOut{}, err
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
				return out, nil
			}
			return TileOut{}, refusal(err)
		}
		content = tr.GetTile()
	}
	switch content.GetKind() {
	case rpc.KindText:
		body, basis, err := d.readBody(ctx, row.GetId())
		if err != nil {
			return TileOut{}, refusal(err)
		}
		out.Body, out.BodyVersion, out.BodyStamp = &body, &basis.Version, basis.Stamp
	case rpc.KindURL:
		out.URL = content.GetUrlString()
	case rpc.KindShell:
		out.ShellSession = rpc.ShellSession(content)
	}
	return out, nil
}

type findIn struct {
	Query          string `json:"query,omitempty" jsonschema:"free text matched against names, bodies and addresses, or id:<tile-id>; may be empty when a filter is given"`
	Scope          string `json:"scope,omitempty" jsonschema:"a namespace or id to search within; empty searches everything this node reaches"`
	Limit          int32  `json:"limit,omitempty" jsonschema:"the most hits each namespace answers; 0 is its default"`
	Grid           string `json:"grid,omitempty" jsonschema:"keep only the tiles on this grid"`
	Recurse        bool   `json:"recurse,omitempty" jsonschema:"with grid, also the tiles on the grids inside its wells, to any depth; a link well is not followed"`
	Created        *Span  `json:"created,omitempty" jsonschema:"keep only the tiles made in this range"`
	Modified       *Span  `json:"modified,omitempty" jsonschema:"keep only the tiles last written by anything (a move, a framing, content) in this range"`
	ContentChanged *Span  `json:"content_changed,omitempty" jsonschema:"keep only the tiles whose content (a body, an address, a name) last changed in this range"`
	Selected       bool   `json:"selected,omitempty" jsonschema:"keep only the tiles in their grid's selection"`
}

// Span is a time range, each end an RFC 3339 time; an empty end is open.
type Span struct {
	From string `json:"from,omitempty" jsonschema:"the earliest time, RFC 3339, such as 2026-10-17T09:00:00Z; empty is open"`
	To   string `json:"to,omitempty" jsonschema:"the latest time, RFC 3339, inclusive; empty is open"`
}

// rangeOf is s as the wire's unix-second range.
func (s *Span) rangeOf(name string) (*pb.TimeRange, error) {
	if s == nil {
		return nil, nil
	}
	var r pb.TimeRange
	for _, end := range []struct {
		name, at string
		to       *int64
	}{{"from", s.From, &r.From}, {"to", s.To, &r.To}} {
		if end.at == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, end.at)
		if err != nil {
			return nil, fmt.Errorf("refused: %s.%s %q is not an RFC 3339 time such as 2026-10-17T09:00:00Z", name, end.name, end.at)
		}
		*end.to = t.Unix()
	}
	return &r, nil
}

func (d *door) find(ctx context.Context, _ *mcp.CallToolRequest, in findIn) (*mcp.CallToolResult, FindOut, error) {
	req := &pb.SearchRequest{Query: in.Query, Scope: in.Scope, Limit: in.Limit,
		GridId: in.Grid, Recurse: in.Recurse, Selected: in.Selected}
	var err error
	if req.Created, err = in.Created.rangeOf("created"); err != nil {
		return nil, FindOut{}, err
	}
	if req.Modified, err = in.Modified.rangeOf("modified"); err != nil {
		return nil, FindOut{}, err
	}
	if req.ContentChanged, err = in.ContentChanged.rangeOf("content_changed"); err != nil {
		return nil, FindOut{}, err
	}
	resp, err := d.rt.Search(ctx, req)
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

// errNoItems refuses a call that names nothing to do.
var errNoItems = errors.New("refused: the list is empty; name at least one")

// each applies do to every item in order, one verdict per item: a refused
// item is its verdict and stops nothing after it.
func each[T any](items []T, do func(T) (Verdict, error)) (*mcp.CallToolResult, WriteOut, error) {
	if len(items) == 0 {
		return nil, WriteOut{}, errNoItems
	}
	out := WriteOut{Items: make([]Verdict, 0, len(items))}
	for _, it := range items {
		v, err := do(it)
		if err != nil {
			v = Verdict{}
		}
		v.Verdict = verdictOf(err)
		out.Items = append(out.Items, v)
	}
	return nil, out, nil
}

type writeTextItem struct {
	ID      string `json:"id,omitempty" jsonschema:"the text tile to replace the body of, or a link to one; empty creates a tile in grid"`
	Grid    string `json:"grid,omitempty" jsonschema:"with no id, the grid to create the tile in"`
	Body    string `json:"body" jsonschema:"the whole body, markdown; its first line is the tile's name"`
	Version *int64 `json:"version,omitempty" jsonschema:"with id, the body_version read_tiles gave; omitted claims the version on disk now"`
	Stamp   string `json:"stamp,omitempty" jsonschema:"with id, for a plugin's text, the body_stamp read_tiles gave, in place of a version"`
	Near    *Cell  `json:"near,omitempty" jsonschema:"with no id, a cell to land at, or below it in its column when taken"`
}

type writeTextIn struct {
	Items []writeTextItem `json:"items" jsonschema:"the bodies to write, applied in order"`
}

func (d *door) writeText(ctx context.Context, _ *mcp.CallToolRequest, in writeTextIn) (*mcp.CallToolResult, WriteOut, error) {
	return each(in.Items, func(it writeTextItem) (Verdict, error) {
		if it.ID == "" {
			return d.createText(ctx, it)
		}
		if it.Grid != "" || it.Near != nil {
			return Verdict{}, errors.New("refused: an item names either id, to replace a body, or grid and near, to create a tile, not both")
		}
		row, err := d.tile(ctx, it.ID)
		if err != nil {
			return Verdict{}, err
		}
		// A link owns no bytes; the write goes to the content it names, as the
		// editor's does.
		target := rpc.ContentID(row)
		claim := rpc.ContentBasis{Stamp: it.Stamp}
		if it.Version != nil {
			claim.Version = *it.Version
		} else if it.Stamp == "" {
			if _, claim, err = d.readBody(ctx, target); err != nil {
				return Verdict{}, refusal(err)
			}
		}
		return d.landed(d.rt.WriteContent(ctx, chunks(target, claim, []byte(it.Body))))
	})
}

func (d *door) createText(ctx context.Context, it writeTextItem) (Verdict, error) {
	if it.Grid == "" {
		return Verdict{}, errors.New("refused: an item names id, to replace a body, or grid, to create a tile")
	}
	if it.Version != nil || it.Stamp != "" {
		return Verdict{}, errors.New("refused: a new tile has nothing to claim; version and stamp go with id")
	}
	x, y, err := d.slot(ctx, it.Grid, it.Near.near(), 1, 1)
	if err != nil {
		return Verdict{}, err
	}
	return d.landed(d.rt.CreateTile(ctx, &pb.CreateTileRequest{GridId: it.Grid,
		Tile: &pb.Tile{Kind: rpc.KindText, X: x, Y: y, W: 1, H: 1}, Content: []byte(it.Body)}))
}

type renameItem struct {
	ID      string `json:"id" jsonschema:"the tile"`
	Name    string `json:"name" jsonschema:"the new name"`
	Version *int64 `json:"version,omitempty" jsonschema:"the version you read; omitted claims the version on disk now"`
}

type renameIn struct {
	Items []renameItem `json:"items" jsonschema:"the renames, applied in order"`
}

func (d *door) rename(ctx context.Context, _ *mcp.CallToolRequest, in renameIn) (*mcp.CallToolResult, WriteOut, error) {
	return each(in.Items, func(it renameItem) (Verdict, error) {
		row, err := d.tile(ctx, it.ID)
		if err != nil {
			return Verdict{}, err
		}
		version := row.GetVersion()
		if it.Version != nil {
			version = *it.Version
		}
		return d.landed(d.rt.SetTile(ctx, &pb.SetTileRequest{TileId: row.GetId(), Version: version, Rename: it.Name}))
	})
}

type placeItem struct {
	ID   string `json:"id" jsonschema:"the tile"`
	X    int64  `json:"x" jsonschema:"the column of its top-left cell"`
	Y    int64  `json:"y" jsonschema:"the row of its top-left cell"`
	W    int64  `json:"w,omitempty" jsonschema:"its width in cells; 0 keeps it"`
	H    int64  `json:"h,omitempty" jsonschema:"its height in cells; 0 keeps it"`
	Grid string `json:"grid,omitempty" jsonschema:"the grid to move it to, in the same namespace; empty keeps its grid"`
}

type placeIn struct {
	Items []placeItem `json:"items" jsonschema:"the moves, applied in order"`
}

func (d *door) place(ctx context.Context, _ *mcp.CallToolRequest, in placeIn) (*mcp.CallToolResult, WriteOut, error) {
	return each(in.Items, func(it placeItem) (Verdict, error) {
		row, err := d.tile(ctx, it.ID)
		if err != nil {
			return Verdict{}, err
		}
		return d.landed(d.rt.PlaceTile(ctx, &pb.PlaceTileRequest{TileId: row.GetId(),
			GridId: cmp.Or(it.Grid, row.GetGridId()), X: it.X, Y: it.Y,
			W: cmp.Or(it.W, row.GetW()), H: cmp.Or(it.H, row.GetH()), Claim: rpc.PlacementOf(row)}))
	})
}

type copyItem struct {
	ID   string `json:"id" jsonschema:"the tile"`
	Grid string `json:"grid,omitempty" jsonschema:"the grid it lands in; empty is the tile's own grid"`
	At   *Cell  `json:"at,omitempty" jsonschema:"a cell to land at, or below it in its column when taken; empty is beside the tile in its own grid, the first free cell of another"`
	Link bool   `json:"link,omitempty" jsonschema:"false makes a deep copy; true makes a link, a dashed tile showing the same content"`
}

type copyIn struct {
	Items []copyItem `json:"items" jsonschema:"the copies, applied in order"`
}

func (d *door) copy(ctx context.Context, _ *mcp.CallToolRequest, in copyIn) (*mcp.CallToolResult, WriteOut, error) {
	return each(in.Items, func(it copyItem) (Verdict, error) {
		src, grid, x, y, err := d.landing(ctx, it)
		if err != nil {
			return Verdict{}, err
		}
		if it.Link {
			return d.landed(d.rt.CreateTile(ctx, &pb.CreateTileRequest{GridId: grid, Tile: rpc.LinkOf(src, x, y)}))
		}
		return d.landed(d.rt.CloneTile(ctx, &pb.CloneTileRequest{TileId: src.GetId(), DestGridId: grid, X: x, Y: y}))
	})
}

type deleteItem struct {
	ID      string `json:"id" jsonschema:"the tile"`
	Version *int64 `json:"version,omitempty" jsonschema:"the version you read; omitted claims the version on disk now"`
}

type deleteIn struct {
	Items []deleteItem `json:"items" jsonschema:"the tiles to delete, in order"`
}

func (d *door) delete(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, WriteOut, error) {
	return each(in.Items, func(it deleteItem) (Verdict, error) {
		row, err := d.tile(ctx, it.ID)
		if err != nil {
			return Verdict{}, err
		}
		req := rpc.DeleteOf(row)
		if it.Version != nil {
			req.ClaimVersion = *it.Version
		}
		resp, err := d.rt.DeleteTile(ctx, req)
		if err != nil {
			return Verdict{}, refusal(err)
		}
		return Verdict{Deleted: row.GetId(), SessionLeft: resp.GetSessionLeft()}, nil
	})
}

type selectIn struct {
	Grid    string   `json:"grid,omitempty" jsonschema:"the grid; none with all"`
	Add     []string `json:"add,omitempty" jsonschema:"tiles of this grid to put in the selection; one not on the grid is ignored"`
	Remove  []string `json:"remove,omitempty" jsonschema:"tiles to take out of the selection"`
	Clear   bool     `json:"clear,omitempty" jsonschema:"empty the grid's selection first, without naming it"`
	Recurse bool     `json:"recurse,omitempty" jsonschema:"with clear, also empty the selection of every grid inside this grid's wells, to any depth, within its namespace"`
	All     bool     `json:"all,omitempty" jsonschema:"with clear and no grid, empty every selection the node holds, and each connected far node's"`
}

func (d *door) selectTiles(ctx context.Context, _ *mcp.CallToolRequest, in selectIn) (*mcp.CallToolResult, GridOut, error) {
	if _, err := d.rt.SetSelection(ctx, &pb.SetSelectionRequest{GridId: in.Grid, Add: in.Add, Remove: in.Remove,
		Clear: in.Clear, Recurse: in.Recurse, All: in.All}); err != nil {
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
func (d *door) landing(ctx context.Context, in copyItem) (src *pb.Tile, grid string, x, y int64, err error) {
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

// landed is one write's verdict: the row it wrote, or its refusal.
func (d *door) landed(resp *pb.TileResponse, err error) (Verdict, error) {
	if err != nil {
		return Verdict{}, refusal(err)
	}
	t := tileOf(resp.GetTile(), nil, d.node)
	return Verdict{Tile: &t}, nil
}
