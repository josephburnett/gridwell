package server

// The node grid: every gridwell node exposes its plugin list as a real,
// read-only grid — one link tile (exit well) per configured plugin, dashed
// like every other cross-plugin reference. The local client anchors its panes
// here on boot (the landing page), and a remote mounter descending into an
// ssh plugin sees the remote node's node grid — the SAME shape, served the
// SAME way, so "the launcher" is one concept everywhere instead of a
// client-side special case.
//
// The provider is a pure adapter over facts that already have owners: the
// tile set comes from the registry (config order), each tile's child and
// framing come from the plugin's Info handshake (root_grid_id, root_view_*),
// and a framing writeback maps onto the plugin's own SetRootView. Nothing is
// stored here except the node grid's own viewport (in-memory).
//
// Identity: the node grid's grid id is "0" in the node's own uuid namespace
// (server.yaml node_id), and each link tile's LOCAL id is the plugin's uuid —
// stable across restarts and meaningful in descent paths, no allocation
// needed.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// nodeGridID is the node grid's local id within the node's uuid namespace.
const nodeGridID = "0"

type nodeGrid struct {
	pb.UnimplementedGridwellServer
	reg *plugin.Registry
	// info fetches a plugin's (cached) Info handshake; invalidate drops the
	// cache entry after a framing writeback. Both provided by the Server so
	// the provider shares the one Info cache instead of growing its own.
	info       func(ctx context.Context, uuid string) (*pb.InfoResponse, error)
	invalidate func(uuid string)

	// The node grid's own viewport — the landing page's framing. Held in
	// memory and, when statePath is set, mirrored to a small JSON file so it
	// survives a server restart (the landing page stays as you left it).
	// The file is the durable copy; memory is the read cache — one writer
	// (SetRootView), loaded once at construction.
	mu        sync.Mutex
	view      nodeView
	statePath string
}

// nodeView is the persisted landing-page viewport.
type nodeView struct {
	Cx   float64 `json:"cx"`
	Cy   float64 `json:"cy"`
	Zoom float64 `json:"zoom"`
}

// loadView restores the persisted viewport, if any. A missing file is a
// fresh node; any other read problem is surfaced to the log, never fatal —
// the landing page still works, it just opens at the default framing.
func (n *nodeGrid) loadView() {
	if n.statePath == "" {
		return
	}
	data, err := os.ReadFile(n.statePath)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "gridwell: node grid: read %s: %v\n", n.statePath, err)
		}
		return
	}
	var v nodeView
	if err := json.Unmarshal(data, &v); err != nil {
		fmt.Fprintf(os.Stderr, "gridwell: node grid: parse %s: %v\n", n.statePath, err)
		return
	}
	n.mu.Lock()
	n.view = v
	n.mu.Unlock()
}

var errNodeGridReadOnly = status.Error(codes.FailedPrecondition,
	"the node grid is read-only: plugins are added with `gridwell init` and linked elsewhere by drag")

// Info: the node presents as a rootful, read-only, event-less plugin. Its
// display name is empty — the LOCAL config name of a mount labels a remote
// node, the same rule as every plugin label.
func (n *nodeGrid) Info(ctx context.Context, _ *pb.InfoRequest) (*pb.InfoResponse, error) {
	n.mu.Lock()
	v := n.view
	n.mu.Unlock()
	return &pb.InfoResponse{
		Kind:         "node",
		Watch:        false,
		Writable:     false,
		RootGridId:   nodeGridID,
		RootViewCx:   v.Cx,
		RootViewCy:   v.Cy,
		RootViewZoom: v.Zoom,
	}, nil
}

// GetGrid serves the plugin-list grid: a centered row of link tiles in config
// order. Each tile's child is the plugin's qualified root ("" while the
// plugin is broken or rootless — still listed, not enterable), its label the
// configured name, and its framing the plugin's persisted root view, so
// descending through the tile restores exactly the viewport the plugin's own
// SetRootView last saved.
func (n *nodeGrid) GetGrid(ctx context.Context, req *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	if req.GridId != nodeGridID {
		return nil, status.Errorf(codes.NotFound, "node grid: no grid %q", req.GridId)
	}
	plugins := n.reg.Ordered()
	tiles := make([]*pb.Tile, 0, len(plugins))
	for i, p := range plugins {
		t := &pb.Tile{
			Id:     p.UUID,
			GridId: nodeGridID,
			Kind:   "well",
			// A centered row with a one-cell gap, in config order.
			X: int64(2*i - len(plugins) + 1), Y: 0, W: 1, H: 1,
			AltText:   n.reg.Label(p.UUID),
			Reference: true,
		}
		if info, err := n.info(ctx, p.UUID); err == nil && info.RootGridId != "" {
			// Both shapes concat correctly: a leaf plugin's local root
			// ("1" → "uuid/1") and a transit plugin's chain
			// ("rnode/0" → "sshuuid/rnode/0").
			t.ChildGridId = p.UUID + "/" + info.RootGridId
			// view_x/view_y are int64 cell coords on the wire; the root view
			// center is a double. Truncation matches how a well's framing is
			// stored (the fractional part rides in the zoom transform).
			t.ViewX = int64(info.RootViewCx)
			t.ViewY = int64(info.RootViewCy)
			t.ViewZoom = info.RootViewZoom
		}
		tiles = append(tiles, t)
	}
	return &pb.GetGridResponse{
		// source_kind "node" tells a renderer "this is a node grid" (a
		// mount's tile shows the generic globe glyph, not a well) — the same
		// ride-on-the-grid channel fs/proc use.
		Grid:  &pb.Grid{Id: nodeGridID, SourceKind: "node", Writable: false},
		Tiles: tiles,
	}, nil
}

// GetTile serves one link tile — the same synthesis as GetGrid, so the two
// can never disagree.
func (n *nodeGrid) GetTile(ctx context.Context, req *pb.GetTileRequest) (*pb.TileResponse, error) {
	g, err := n.GetGrid(ctx, &pb.GetGridRequest{GridId: nodeGridID})
	if err != nil {
		return nil, err
	}
	for _, t := range g.Tiles {
		if t.Id == req.TileId {
			return &pb.TileResponse{Tile: t}, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "node grid: no plugin %q", req.TileId)
}

// Probe: a link tile is present exactly while its plugin is configured. A
// transiently broken plugin still probes present — only removal from the
// config is GONE (the I12 rule: unreachable is not gone).
func (n *nodeGrid) Probe(ctx context.Context, req *pb.ProbeRequest) (*pb.ProbeResponse, error) {
	presence := pb.ProbeResponse_PRESENCE_GONE
	if _, ok := n.reg.Get(req.TileId); ok {
		presence = pb.ProbeResponse_PRESENCE_PRESENT
	}
	return &pb.ProbeResponse{Presence: presence}, nil
}

// SetTile accepts the one mutation the node grid supports: a framing
// writeback on a plugin's link tile (the normal well-ascent viewport save),
// mapped onto that plugin's own SetRootView — the fact already has an owner,
// the plugin's DB; this is routing, not storage. Content writes are refused.
func (n *nodeGrid) SetTile(ctx context.Context, req *pb.SetTileRequest) (*pb.TileResponse, error) {
	c, ok := n.reg.Get(req.TileId)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "node grid: no plugin %q", req.TileId)
	}
	t := req.Tile
	if t == nil || t.Kind != "well" {
		return nil, errNodeGridReadOnly
	}
	info, err := n.info(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	if _, err := c.SetRootView(ctx, &pb.SetRootViewRequest{
		RootGridId: info.RootGridId,
		Cx:         float64(t.ViewX), Cy: float64(t.ViewY), Zoom: t.ViewZoom,
	}); err != nil && !isUnimplemented(err) {
		// fs/proc keep no root view; their ascent must not surface an error.
		return nil, err
	}
	// root_view_* ride the Info handshake — drop the cache so the next
	// GetGrid synthesizes the tile with the framing just written.
	n.invalidate(req.TileId)
	return n.GetTile(ctx, &pb.GetTileRequest{TileId: req.TileId})
}

// SetRootView stores the node grid's OWN viewport — the landing page
// framing — and mirrors it to the state file so it survives a restart. A
// failed write surfaces as an error (the client shows it) rather than
// silently downgrading durability.
func (n *nodeGrid) SetRootView(_ context.Context, req *pb.SetRootViewRequest) (*pb.SetRootViewResponse, error) {
	n.mu.Lock()
	n.view = nodeView{Cx: req.Cx, Cy: req.Cy, Zoom: req.Zoom}
	v := n.view
	path := n.statePath
	n.mu.Unlock()
	if path != "" {
		data, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, fmt.Errorf("node grid: persist viewport: %w", err)
		}
	}
	return &pb.SetRootViewResponse{}, nil
}

// Every content mutation is refused with one clear error.
func (n *nodeGrid) CreateTile(context.Context, *pb.CreateTileRequest) (*pb.TileResponse, error) {
	return nil, errNodeGridReadOnly
}
func (n *nodeGrid) MoveTile(context.Context, *pb.MoveTileRequest) (*pb.TileResponse, error) {
	return nil, errNodeGridReadOnly
}
func (n *nodeGrid) CloneTile(context.Context, *pb.CloneTileRequest) (*pb.TileResponse, error) {
	return nil, errNodeGridReadOnly
}
func (n *nodeGrid) ResizeTile(context.Context, *pb.ResizeTileRequest) (*pb.TileResponse, error) {
	return nil, errNodeGridReadOnly
}
func (n *nodeGrid) UpdateText(context.Context, *pb.UpdateTextRequest) (*pb.TileResponse, error) {
	return nil, errNodeGridReadOnly
}
func (n *nodeGrid) DeleteTile(context.Context, *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error) {
	return nil, errNodeGridReadOnly
}
func (n *nodeGrid) SetTileAlt(context.Context, *pb.SetTileAltRequest) (*pb.TileResponse, error) {
	return nil, errNodeGridReadOnly
}

func (n *nodeGrid) SetContentZoom(context.Context, *pb.SetContentZoomRequest) (*pb.TileResponse, error) {
	return nil, errNodeGridReadOnly
}
