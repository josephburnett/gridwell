package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"connectrpc.com/connect"
	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/store"
)

// connectHandler implements gridwellv1connect.GridwellHandler as a thin router
// over the plugin registry. It holds no Gridwell state: every request is
// forwarded to the plugin that owns the id it carries (the primary localdb,
// a mounted localdb, fs, proc, …), with the plugin-uuid prefix stripped on the
// way in and re-applied to the response on the way out. Subscribe fans in every
// watching plugin's event stream (Info.watch); ShellSessionAlive routes to the
// owning plugin.
type connectHandler struct {
	gridwellv1connect.UnimplementedGridwellHandler
	srv *Server
}

func newConnectHandler(srv *Server) *connectHandler {
	return &connectHandler{srv: srv}
}

// ── routing ────────────────────────────────────────────────────────────────────

// route resolves the plugin that owns id, returning its client, the local
// (unprefixed) id, and the owning plugin uuid. Ids are always qualified in the
// rootless model (there is no privileged plugin a bare id could fall back to).
func (h *connectHandler) route(id string) (client pb.GridwellClient, local, uuid string, err error) {
	uuid, local, ok := rpc.SplitID(id)
	if !ok {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unqualified id %q", id))
	}
	c, found := h.srv.routeClient(uuid)
	if !found {
		return nil, "", "", connect.NewError(connect.CodeNotFound, fmt.Errorf("no plugin %q", uuid))
	}
	return c, local, uuid, nil
}

// stripUUID removes a specific plugin uuid prefix from an id, leaving bare and
// foreign-prefixed ids untouched (so a cross-plugin reference stays qualified
// and the target plugin rejects it rather than misresolving it locally).
func stripUUID(id, uuid string) string {
	if u, l, ok := rpc.SplitID(id); ok && u == uuid {
		return l
	}
	return id
}

// localPathFor returns the descent path local to the target plugin. A path can
// cross plugin boundaries (`…/<p1>/<t>/<p2>/<u>`); only the trailing run of
// segments owned by the target plugin is meaningful to it, so segments above
// the last boundary (owned by other plugins) are dropped and the rest are
// stripped of the uuid prefix.
func localPathFor(p *pb.Path, uuid string) *pb.Path {
	if p == nil {
		return nil
	}
	start := len(p.WellIds)
	for start > 0 {
		if u, _, ok := rpc.SplitID(p.WellIds[start-1]); !ok || u != uuid {
			break // a bare or foreign-plugin segment marks the boundary
		}
		start--
	}
	out := make([]string, 0, len(p.WellIds)-start)
	for _, id := range p.WellIds[start:] {
		out = append(out, stripUUID(id, uuid))
	}
	return &pb.Path{WellIds: out}
}

// tileResp qualifies a plugin's TileResponse for the client, applying the
// transit rule when the owning plugin is a node mount.
func (h *connectHandler) tileResp(uuid string, resp *pb.TileResponse, err error) (*connect.Response[pb.TileResponse], error) {
	if err != nil {
		return nil, asConnectError(err)
	}
	t := resp.GetTile()
	if t != nil {
		t = qualifyTilesFor(h.srv.pluginReg.Transit(uuid), uuid, []*pb.Tile{t})[0]
	}
	return connect.NewResponse(&pb.TileResponse{Tile: t}), nil
}

// qualifyEvent re-applies a plugin's uuid to the ids in a change event.
// transit selects the node-mount tile rule (chain prepend, Reference trusted).
// GridId/TileId are plain prepends either way — chains compose by concat.
func qualifyEvent(uuid string, transit bool, ev *pb.Event) *pb.Event {
	switch p := ev.Payload.(type) {
	case *pb.Event_GridChanged:
		return &pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{
			GridId: rpc.QualifyID(uuid, p.GridChanged.GridId),
		}}}
	case *pb.Event_TileChanged:
		return &pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{
			Tile: qualifyTilesFor(transit, uuid, []*pb.Tile{p.TileChanged.Tile})[0],
		}}}
	case *pb.Event_TileRemoved:
		return &pb.Event{Payload: &pb.Event_TileRemoved{TileRemoved: &pb.TileRemoved{
			GridId: rpc.QualifyID(uuid, p.TileRemoved.GridId),
			TileId: rpc.QualifyID(uuid, p.TileRemoved.TileId),
		}}}
	case *pb.Event_PluginHealth:
		// The plugin uuid is an id like any other: one segment prepended per
		// hop. A mounted node's fan-in re-serves its own plugins' health
		// transitions; unqualified, they arrive addressed by a bare remote
		// uuid that names nothing on this side of the mount.
		return &pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: &pb.EventPluginHealth{
			PluginUuid: rpc.QualifyID(uuid, p.PluginHealth.PluginUuid),
			Healthy:    p.PluginHealth.Healthy,
			Detail:     p.PluginHealth.Detail,
		}}}
	}
	return ev
}

// ── reads ──────────────────────────────────────────────────────────────────────

func (h *connectHandler) GetGrid(ctx context.Context, req *connect.Request[pb.GetGridRequest]) (*connect.Response[pb.GetGridResponse], error) {
	c, local, uuid, err := h.route(req.Msg.GridId)
	if err != nil {
		return nil, err
	}
	resp, err := c.GetGrid(ctx, &pb.GetGridRequest{GridId: local})
	if err != nil {
		return nil, asConnectError(err)
	}
	transit := h.srv.pluginReg.Transit(uuid)
	g := qualifyGrid(uuid, resp.Grid)
	// Grid.writable / scratch_grid_id are per-grid facts of the OWNING
	// plugin. A leaf plugin doesn't stamp them (its Info declares them once);
	// a transit plugin's response carries the remote node's stamp — writable
	// verbatim, the scratch id prepended one segment like every other id.
	if g != nil {
		if transit {
			if g.ScratchGridId != "" {
				g.ScratchGridId = rpc.QualifyID(uuid, g.ScratchGridId)
			}
			// The transit hop's own tunnel proxy REPLACES whatever the remote
			// stamped: page traffic must enter the tunnel where the USER is,
			// so the outermost hop wins. (Multi-hop exits at the first remote
			// — a documented limit; SOCKS-over-SOCKS chaining is out of scope.)
			if info, ierr := h.srv.pluginInfo(ctx, uuid); ierr == nil {
				g.ProxyEndpoint = proxyEndpointOf(info)
			}
		} else if info, ierr := h.srv.pluginInfo(ctx, uuid); ierr == nil {
			g.Writable = info.Writable
			if info.ScratchGridId != "" {
				g.ScratchGridId = rpc.QualifyID(uuid, info.ScratchGridId)
			}
			g.ProxyEndpoint = proxyEndpointOf(info)
		}
	}
	return connect.NewResponse(&pb.GetGridResponse{
		Grid:  g,
		Tiles: qualifyTilesFor(transit, uuid, resp.Tiles),
	}), nil
}

func (h *connectHandler) GetTilePreview(ctx context.Context, req *connect.Request[pb.GetTilePreviewRequest]) (*connect.Response[pb.GetTilePreviewResponse], error) {
	// contentRoute: a leaf link's preview is its TARGET's preview, resolved at
	// the serving node (owner decision 8, 2026-07-26).
	c, local, err := h.srv.contentRoute(ctx, req.Msg.TileId)
	if err != nil {
		return nil, asConnectError(err)
	}
	resp, err := c.GetTilePreview(ctx, &pb.GetTilePreviewRequest{TileId: local})
	if err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(resp), nil
}

func (h *connectHandler) GetTileContent(ctx context.Context, req *connect.Request[pb.GetTileContentRequest]) (*connect.Response[pb.GetTileContentResponse], error) {
	c, local, _, err := h.route(req.Msg.TileId)
	if err != nil {
		return nil, err
	}
	resp, err := c.GetTileContent(ctx, &pb.GetTileContentRequest{TileId: local})
	if err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(resp), nil
}

// pluginInfoTimeout bounds each plugin's Info handshake during ListPlugins so
// one slow or hung plugin can't stall (or block) the whole launcher. On timeout
// the plugin is still listed from its config (kind + configured label), just
// without a clickable root — graceful degradation beats a frozen launcher.
const pluginInfoTimeout = 3 * time.Second

// ListPlugins enumerates the configured plugins in config order so the client
// can build the launcher / + menu. label comes from each plugin's Info, and so
// does writable (accepts new tiles) — a capability the handshake declares,
// never derived from the kind string.
func (h *connectHandler) ListPlugins(ctx context.Context, _ *connect.Request[pb.ListPluginsRequest]) (*connect.Response[pb.ListPluginsResponse], error) {
	var out []*pb.PluginInfo
	for _, p := range h.srv.pluginReg.Ordered() {
		// The server.yaml display name is authoritative (the menu and a mounted
		// well must agree); buildPluginInfo falls back to Info's name then kind.
		label := h.srv.pluginReg.Label(p.UUID)
		// Info is the whole handshake (default root grid + fallback label +
		// capabilities), served from the per-uuid cache after the first
		// success. Bounded, so a hung plugin degrades to a config-only entry
		// instead of hanging the launcher; a failed Info → info stays nil, and
		// the error rides along so buildPluginInfo can distinguish "broken"
		// from "healthy but rootless" — previously dropped on the floor here,
		// which made both cases identical on the wire (issue #47).
		info, err := h.srv.pluginInfo(ctx, p.UUID)
		out = append(out, buildPluginInfo(p.UUID, p.Kind, label, info, err))
	}
	resp := &pb.ListPluginsResponse{Plugins: out}
	if h.srv.cfg.NodeID != "" {
		resp.NodeUuid = h.srv.cfg.NodeID
		resp.NodeRootGridId = h.srv.cfg.NodeID + "/" + nodeGridID
	}
	return connect.NewResponse(resp), nil
}

// buildPluginInfo assembles a launcher PluginInfo from the config (uuid, kind,
// configured label) and the plugin's Info handshake. info is nil when Info
// failed or timed out (infoErr is the reason): the plugin is still listed (so
// the launcher never blanks a configured plugin), but with no clickable
// root/scratch grid, no writable bit, and a label that falls back to the
// configured name, then the kind. Pure, so the fallback rules are
// unit-tested without standing up a plugin.
//
// info == nil (Info failed/timed out — "broken") and info != nil but
// info.RootGridId == "" (Info succeeded, the plugin just has no root
// configured — "rootless") both leave RootGridId == "" on the result; without
// InfoError they are the SAME PluginInfo and the launcher tile (and a click on
// it) cannot tell them apart. InfoError is what makes them distinguishable: set
// only in the broken case, always empty in the rootless one.
//
// writable comes from the Info handshake, NEVER from the kind string: a remote
// localdb reached through the ssh proxy has local kind "ssh" but is every bit
// as writable — the proxy forwards its Info verbatim, so the capability
// travels while a kind check would strand it read-only.
func buildPluginInfo(uuid, kind, configLabel string, info *pb.InfoResponse, infoErr error) *pb.PluginInfo {
	label := configLabel
	var rootGridID, scratchGridID, infoError string
	var writable bool
	var rootViewCx, rootViewCy, rootViewZoom float64
	if info != nil {
		if info.RootGridId != "" {
			rootGridID = rpc.QualifyID(uuid, info.RootGridId)
		}
		// Qualified scratch grid id (ephemeral-url target); empty for plugins
		// that don't support ephemeral visits.
		if info.ScratchGridId != "" {
			scratchGridID = rpc.QualifyID(uuid, info.ScratchGridId)
		}
		if label == "" {
			label = info.DisplayName
		}
		writable = info.Writable
		// Root-view viewport forwarded verbatim from Info (localdb fills it;
		// fs/proc return zero). The client seeds enterPlugin framing from this.
		rootViewCx = info.RootViewCx
		rootViewCy = info.RootViewCy
		rootViewZoom = info.RootViewZoom
	} else if infoErr != nil {
		infoError = "plugin not responding: " + infoErr.Error()
	}
	if label == "" {
		label = kind
	}
	return &pb.PluginInfo{
		Uuid:          uuid,
		Kind:          kind,
		Label:         label,
		Writable:      writable,
		RootGridId:    rootGridID,
		ScratchGridId: scratchGridID,
		RootViewCx:    rootViewCx,
		RootViewCy:    rootViewCy,
		RootViewZoom:  rootViewZoom,
		InfoError:     infoError,
	}
}

func (h *connectHandler) GetTile(ctx context.Context, req *connect.Request[pb.GetTileRequest]) (*connect.Response[pb.TileResponse], error) {
	c, local, uuid, err := h.route(req.Msg.TileId)
	if err != nil {
		return nil, err
	}
	resp, err := c.GetTile(ctx, &pb.GetTileRequest{TileId: local})
	return h.tileResp(uuid, resp, err)
}
func (h *connectHandler) SetTileAlt(ctx context.Context, req *connect.Request[pb.SetTileAltRequest]) (*connect.Response[pb.TileResponse], error) {
	c, local, uuid, err := h.route(req.Msg.TileId)
	if err != nil {
		return nil, err
	}
	resp, err := c.SetTileAlt(ctx, &pb.SetTileAltRequest{TileId: local, Alt: req.Msg.Alt})
	return h.tileResp(uuid, resp, err)
}

// SetPaneLayout routes by tile id (path-free, the SetTileAlt shape) — the
// layout blob itself is opaque here; its ids are owner-frame-relative by the
// codec's rule, so no rewriting happens at this hop or any transit hop.
func (h *connectHandler) SetPaneLayout(ctx context.Context, req *connect.Request[pb.SetPaneLayoutRequest]) (*connect.Response[pb.TileResponse], error) {
	c, local, uuid, err := h.route(req.Msg.TileId)
	if err != nil {
		return nil, err
	}
	resp, err := c.SetPaneLayout(ctx, &pb.SetPaneLayoutRequest{
		TileId: local, Version: req.Msg.Version, Data: req.Msg.Data,
	})
	return h.tileResp(uuid, resp, err)
}

func (h *connectHandler) SetContentZoom(ctx context.Context, req *connect.Request[pb.SetContentZoomRequest]) (*connect.Response[pb.TileResponse], error) {
	c, local, uuid, err := h.route(req.Msg.TileId)
	if err != nil {
		return nil, err
	}
	resp, err := c.SetContentZoom(ctx, &pb.SetContentZoomRequest{
		TileId: local, Version: req.Msg.Version, ContentZoom: req.Msg.ContentZoom,
	})
	return h.tileResp(uuid, resp, err)
}

// ── creates ──────────────────────────────────────────────────────────────────

// CreateTile is the single create router: resolve the owning plugin by
// destination grid, localize grid_id + path, forward. tile.kind selects the
// primitive; tile.child_grid_id (an exit well's cross-plugin reference) stays
// qualified.
func (h *connectHandler) CreateTile(ctx context.Context, req *connect.Request[pb.CreateTileRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.GridId)
	if err != nil {
		return nil, err
	}
	m.GridId = local
	m.Path = localPathFor(m.Path, uuid)
	resp, err := c.CreateTile(ctx, m)
	return h.tileResp(uuid, resp, err)
}

// ── mutations ──────────────────────────────────────────────────────────────────

func (h *connectHandler) MoveTile(ctx context.Context, req *connect.Request[pb.MoveTileRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	m.DestGridId = stripUUID(m.DestGridId, uuid)
	m.Path = localPathFor(m.Path, uuid)
	m.DestPath = localPathFor(m.DestPath, uuid)
	resp, err := c.MoveTile(ctx, m)
	return h.tileResp(uuid, resp, err)
}

// CloneTile clones within a plugin, or — when the destination grid belongs to
// a DIFFERENT plugin — applies the cross-plugin clone contract (owner decision
// 2026-07-19: right-drag = COPY everywhere, left-drag across a boundary =
// LINK): a leaf copies its bytes into the destination plugin (provenance
// object_id carried); a well's deep cross-plugin copy is not yet implemented
// and is refused loudly (the LINK gesture is the left-drag, which arrives
// here as a plain CreateTile carrying a qualified child_grid_id or
// link_target_id — never as a clone). The source plugin is never asked to
// write into a grid it doesn't own.
func (h *connectHandler) CloneTile(ctx context.Context, req *connect.Request[pb.CloneTileRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	if dstUUID, _, ok := rpc.SplitID(m.DestGridId); ok && dstUUID != uuid {
		return h.cloneAcrossPlugins(ctx, m, c, local, uuid)
	}
	m.TileId = local
	m.DestGridId = stripUUID(m.DestGridId, uuid)
	m.Path = localPathFor(m.Path, uuid)
	m.DestPath = localPathFor(m.DestPath, uuid)
	resp, err := c.CloneTile(ctx, m)
	return h.tileResp(uuid, resp, err)
}

// cloneAcrossPlugins materializes a cross-plugin clone: read the source tile
// from its plugin, then create the link (well) or byte copy (leaf) in the
// destination plugin. src is the source plugin's client; srcLocal/srcUUID the
// routed source tile id.
func (h *connectHandler) cloneAcrossPlugins(ctx context.Context, m *pb.CloneTileRequest, src pb.GridwellClient, srcLocal, srcUUID string) (*connect.Response[pb.TileResponse], error) {
	resp, err := src.GetTile(ctx, &pb.GetTileRequest{TileId: srcLocal})
	if err != nil {
		return nil, asConnectError(err)
	}
	// Qualify to the server-global view: an interior well's child becomes
	// "<srcUUID>/<grid>", an exit well's already-qualified target stays put —
	// either way the link target below is exactly what the client would see.
	st := qualifyTilesFor(h.srv.pluginReg.Transit(srcUUID), srcUUID, []*pb.Tile{resp.GetTile()})[0]
	if m.Version != st.Version {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("version conflict: tile %s is at %d, request has %d", m.TileId, st.Version, m.Version))
	}

	// Provenance rides every cross-plugin copy: object_id is a random 128-bit
	// hex, globally unique with no per-plugin qualification, so the copy and
	// the source share a lineage exactly as a within-plugin clone's rows do
	// (insertTileCopy preserves it).
	create := &pb.CreateTileRequest{
		Tile: &pb.Tile{Kind: st.Kind, X: m.X, Y: m.Y, W: st.W, H: st.H,
			AltText: st.AltText, ObjectId: st.ObjectId},
	}
	switch {
	case st.Kind == "well" && st.Reference:
		// The source is itself a LINK (an exit well — a mount, a node-grid
		// plugin tile, an earlier cross-plugin link). Cloning a link copies
		// the LINK: same shared child grid, same framing — exactly what a
		// within-plugin clone of an exit well does. This is also the mount
		// gesture's path (right-drag a node-grid plugin tile).
		create.Tile.ChildGridId = st.ChildGridId
		create.Tile.ViewX = st.ViewX
		create.Tile.ViewY = st.ViewY
		create.Tile.ViewZoom = st.ViewZoom
	case st.Kind == "well":
		// A SOLID well's cross-plugin CLONE is a deep copy of its subtree
		// into the destination plugin — bulk grid/tile/blob transfer the
		// plugin surface has no primitive for yet. Refuse loudly rather than
		// silently substituting a link: the link is the LEFT-drag's meaning,
		// and a gesture that returns something other than what it names is
		// the silent-divergence bug class. (Owner decision 2026-07-19; the
		// old behavior — right-drag well = link — moved to left-drag.)
		return nil, connect.NewError(connect.CodeUnimplemented,
			fmt.Errorf("cross-plugin clone of a well is not implemented yet; left-drag creates a link"))
	case st.LinkTargetId != "":
		// A leaf LINK clones as another link to the same target (the tile
		// being copied is a reference; copying it copies the reference).
		create.Tile.LinkTargetId = st.LinkTargetId
	case st.Kind == "text":
		body, err := src.GetTileContent(ctx, &pb.GetTileContentRequest{TileId: srcLocal})
		if err != nil {
			return nil, asConnectError(err)
		}
		create.Data = body.Data
	case st.Kind == "url":
		create.Tile.UrlString = st.UrlString
	case st.Kind == "shell":
		// A shell's PTY session is plugin-local; the copy is a fresh shell
		// tile carrying the same label.
	case st.Kind == "pane":
		// A workspace clones as a byte copy of its layout blob (like text).
		// The layout's ids are owner-frame-relative — the copy's panes keep
		// naming the ORIGINAL places (a pane tile is arrangement of
		// references, not content), which is exactly the cross-plugin link
		// semantics, carried in bytes instead of a child_grid_id. A
		// never-arranged pane tile (no blob) copies with no Data.
		if st.BlobId != 0 {
			body, err := src.GetTileContent(ctx, &pb.GetTileContentRequest{TileId: srcLocal})
			if err != nil {
				return nil, asConnectError(err)
			}
			create.Data = body.Data
		}
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("cross-plugin clone: unsupported tile kind %q", st.Kind))
	}

	dst, dstLocal, dstUUID, err := h.route(m.DestGridId)
	if err != nil {
		return nil, err
	}
	create.GridId = dstLocal
	create.Path = localPathFor(m.DestPath, dstUUID)
	out, err := dst.CreateTile(ctx, create)
	return h.tileResp(dstUUID, out, err)
}
func (h *connectHandler) ResizeTile(ctx context.Context, req *connect.Request[pb.ResizeTileRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	m.Path = localPathFor(m.Path, uuid)
	resp, err := c.ResizeTile(ctx, m)
	return h.tileResp(uuid, resp, err)
}

// SetTile is the single framing/preview writeback router. The owning plugin
// dispatches on the target tile's kind to the one operation that kind supports
// (well/text framing → no version bump; url/shell preview → bump).
func (h *connectHandler) SetTile(ctx context.Context, req *connect.Request[pb.SetTileRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	m.Path = localPathFor(m.Path, uuid)
	resp, err := c.SetTile(ctx, m)
	return h.tileResp(uuid, resp, err)
}
func (h *connectHandler) UpdateText(ctx context.Context, req *connect.Request[pb.UpdateTextRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	m.Path = localPathFor(m.Path, uuid)
	resp, err := c.UpdateText(ctx, m)
	return h.tileResp(uuid, resp, err)
}

func (h *connectHandler) DeleteTile(ctx context.Context, req *connect.Request[pb.DeleteTileRequest]) (*connect.Response[pb.DeleteTileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	// A pane tile's layout blob is the ONLY record of the workspace's
	// ephemeral leaves (scratch-grid tiles). Deleting the workspace deletes
	// only the arrangement — but must terminate what the arrangement OWNS,
	// exactly like closing a pane does (issue #174): reap referenced
	// scratch tiles BEFORE the pane tile row (and with it the blob) goes
	// away. Transit tiles skip this hop — the forwarded delete reaches the
	// owning node's router, whose blob ids are in that node's frame.
	if !h.srv.pluginReg.Transit(uuid) {
		h.reapWorkspaceEphemerals(ctx, c, local, m.TileId)
	}
	m.TileId = local
	m.Path = localPathFor(m.Path, uuid)
	// The owning plugin reaps the tile's shell session (if any) as part of
	// DeleteTile — the PTY lives behind the interface now.
	if _, err := c.DeleteTile(ctx, m); err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(&pb.DeleteTileResponse{}), nil
}

// reapWorkspaceEphemerals deletes the SCRATCH-grid tiles a pane tile's layout
// blob references (issue #174) — the workspace's ephemeral shells/urls, whose
// tmux sessions die through the existing DeleteTile chain. Referenced
// non-scratch tiles are content the workspace merely views and are never
// touched; an unreadable blob (corrupt, or written by a newer Gridwell) reaps
// NOTHING — never guess — and the boot sweep reclaims the leftovers once the
// pane tile (and so the blob that shielded them) is gone. Best-effort: a
// failure here must not block the user's delete, so errors go to the log.
func (h *connectHandler) reapWorkspaceEphemerals(ctx context.Context, owner pb.GridwellClient, localID, qualifiedID string) {
	tr, err := owner.GetTile(ctx, &pb.GetTileRequest{TileId: localID})
	if err != nil || tr.GetTile() == nil || tr.GetTile().Kind != rpc.KindPane || tr.GetTile().BlobId == 0 {
		return
	}
	body, err := owner.GetTileContent(ctx, &pb.GetTileContentRequest{TileId: localID})
	if err != nil || len(body.GetData()) == 0 {
		return
	}
	// Blob ids are in THIS node's frame (the encoder strips the reader's
	// transit prefix, which is empty for a locally-owned pane tile).
	tree, err := pane.DecodeLayout(body.GetData(), func(id string) string { return id })
	if err != nil {
		log.Printf("gridwell: delete %s: layout blob unreadable, reaping nothing: %v", qualifiedID, err)
		return
	}
	for _, id := range pane.LeafTextFocusIDs(tree) {
		ec, elocal, euuid, err := h.route(id)
		if err != nil {
			continue
		}
		if h.srv.pluginReg.Transit(euuid) {
			// A remote node's ephemeral: this node can't see the remote's
			// scratch-grid fact through the raw transit client. v1 limit —
			// the remote's boot sweep reclaims it.
			log.Printf("gridwell: delete %s: not reaping remote ephemeral candidate %s (transit)", qualifiedID, id)
			continue
		}
		// "Is this tile ephemeral" = its grid IS the owning plugin's scratch
		// grid — the same fact the server's GetGrid stamps from Info (the raw
		// plugin response doesn't carry it).
		info, err := h.srv.pluginInfo(ctx, euuid)
		if err != nil || info.ScratchGridId == "" {
			continue
		}
		et, err := ec.GetTile(ctx, &pb.GetTileRequest{TileId: elocal})
		if err != nil || et.GetTile() == nil || et.GetTile().GridId != info.ScratchGridId {
			continue // not an ephemeral — viewed content, never touched
		}
		if _, err := ec.DeleteTile(ctx, &pb.DeleteTileRequest{
			TileId: elocal, Version: et.GetTile().Version,
		}); err != nil {
			log.Printf("gridwell: delete %s: reaping ephemeral %s failed: %v", qualifiedID, id, err)
		}
	}
}

// SetRootView persists the plugin root-grid framing (the same fact as SetTile
// for a well, but for the synthetic plugin root which has no tile row). Routes
// on root_grid_id; localdb stores to the system KV table; fs/proc are no-ops
// (the UnimplementedGridwellServer returns Unimplemented — ignored here so a
// read-only plugin's ascent doesn't surface an error to the user).
// After a successful write, the per-plugin Info cache is invalidated so the
// next ListPlugins (e.g. after a page refresh) returns fresh root-view fields.
func (h *connectHandler) SetRootView(ctx context.Context, req *connect.Request[pb.SetRootViewRequest]) (*connect.Response[pb.SetRootViewResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.RootGridId)
	if err != nil {
		return nil, err
	}
	_, err = c.SetRootView(ctx, &pb.SetRootViewRequest{
		RootGridId: local,
		Cx:         m.Cx,
		Cy:         m.Cy,
		Zoom:       m.Zoom,
	})
	if err != nil {
		// Unimplemented is not an error — fs/proc don't persist a root view.
		if isUnimplemented(err) {
			return connect.NewResponse(&pb.SetRootViewResponse{}), nil
		}
		return nil, asConnectError(err)
	}
	// Invalidate the Info cache: root_view_* travel in the Info handshake
	// and now differ from the cached values. The next ListPlugins (page
	// refresh) must re-fetch Info to see the updated viewport.
	h.srv.invalidateInfoCache(uuid)
	return connect.NewResponse(&pb.SetRootViewResponse{}), nil
}

// ShellSessionAlive routes the wasm's per-descent probe to the owning plugin,
// which holds the PTY and answers whether the tile's session is alive. An infra
// error is reported as not-alive rather than a Connect error — the wasm only
// cares whether the refresh button should hide.
func (h *connectHandler) ShellSessionAlive(ctx context.Context, req *connect.Request[pb.ShellSessionAliveRequest]) (*connect.Response[pb.ShellSessionAliveResponse], error) {
	c, local, _, err := h.route(req.Msg.TileId)
	if err != nil {
		return connect.NewResponse(&pb.ShellSessionAliveResponse{Alive: false}), nil
	}
	resp, err := c.ShellSessionAlive(ctx, &pb.ShellSessionAliveRequest{TileId: local})
	if err != nil {
		return connect.NewResponse(&pb.ShellSessionAliveResponse{Alive: false}), nil
	}
	return connect.NewResponse(resp), nil
}

// Subscribe fans every watching plugin's change-event stream into the client's
// stream, re-qualifying each event's ids with the emitting plugin's uuid. A
// plugin declares that it emits events via Info.watch — a capability from the
// handshake, never the kind string, so a remote node's events (an ssh-proxied
// localdb) flow exactly like a local plugin's. (fs/proc report watch=false and
// are polled via GetGrid.) With no single root, a client subscribes to the
// whole federation at once.
//
// Failures surface and heal rather than silently ending a plugin's events for
// the life of the client stream: an Info or Subscribe failure is logged with
// the plugin uuid, and watchPlugin re-dials (Info) / fanInEvents re-dials
// (the event stream) with backoff while the client stream lives — so a plugin
// that crashes and restarts resumes delivering events without the client
// reconnecting, AND the client is told about the outage and the recovery via
// an EventPluginHealth (issue #47) instead of tiles just quietly going stale.
func (h *connectHandler) Subscribe(ctx context.Context, _ *connect.Request[pb.SubscribeRequest], stream *connect.ServerStream[pb.Event]) error {
	return h.subscribe(ctx, stream.Send)
}

// subscribe is the transport-free fan-in body, shared by the Connect stream
// (browsers/Electron) and the node export's gRPC stream (a remote mounter) so
// there is exactly one implementation of "every plugin's events, qualified".
func (h *connectHandler) subscribe(ctx context.Context, send func(*pb.Event) error) error {
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	events := make(chan *pb.Event, 64)
	for _, p := range h.srv.pluginReg.Ordered() {
		c, ok := h.srv.pluginReg.Get(p.UUID)
		if !ok {
			continue
		}
		go watchPlugin(subCtx, p.UUID, h.srv.pluginReg.Transit(p.UUID), c, h.srv, events)
	}

	for {
		select {
		case ev := <-events:
			if err := send(ev); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// watchPlugin resolves whether plugin uuid supports live events (Info.watch)
// and, if so, hands off to fanInEvents for the life of ctx. The Info fetch is
// retried with the same backoff fanInEvents itself uses for a dead stream —
// previously a single failed Info at Subscribe time did `continue`, which
// PERMANENTLY excluded the plugin's fan-in for the life of the client stream
// (a plugin that was merely slow to start on server boot, or briefly
// unreachable, silently never got its events fanned in even after it came
// up). Emits the down/recovery EventPluginHealth transition itself when the
// failure is at this Info stage; once Info succeeds and Watch is true,
// fanInEvents owns the health state for the rest of ctx's life (a plugin
// capability doesn't flip false again — only its stream can go down).
func watchPlugin(ctx context.Context, uuid string, transit bool, client pb.GridwellClient, srv *Server, events chan<- *pb.Event) {
	backoff := time.Second
	healthy := true // assume healthy until the first failure
	for {
		info, err := srv.pluginInfo(ctx, uuid)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("gridwell: subscribe: info %s: %v — retrying fan-in in %v", uuid, err, backoff)
			if healthy {
				healthy = false
				reportHealth(ctx, events, uuid, false, err.Error())
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		// Report recovery BEFORE the Watch check: a plugin that went down
		// during a transient Info failure and comes back as Watch:false
		// (fs/proc — no event stream at all) must still clear its down
		// notice, or the client shows "live updates stopped" forever for a
		// plugin that never had live updates to begin with.
		if !healthy {
			healthy = true
			reportHealth(ctx, events, uuid, true, "")
		}
		if !info.Watch {
			return // this plugin kind doesn't emit events (fs/proc) — nothing to fan in, not a failure
		}
		fanInEvents(ctx, uuid, transit, client, events) // owns health from here; returns only when ctx ends
		return
	}
}

// fanInEvents relays one plugin's Subscribe stream into events until ctx ends,
// re-dialing with exponential backoff (1s..30s) after any stream failure so a
// plugin restart resumes its events. Failures are logged, never swallowed —
// a plugin whose events silently stop presents to the user as "tiles stopped
// updating" with no evidence (the silent-disappearance class) — and reported
// as an EventPluginHealth transition (down on first failure, up on recovery;
// not once per retry attempt, so a flapping plugin doesn't spam the client).
func fanInEvents(ctx context.Context, uuid string, transit bool, client pb.GridwellClient, events chan<- *pb.Event) {
	backoff := time.Second
	healthy := true // caller (watchPlugin) already reported recovery if it was ever down
	for {
		ps, err := client.Subscribe(ctx, &pb.SubscribeRequest{})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("gridwell: subscribe: plugin %s stream open failed: %v (retrying in %v)", uuid, err, backoff)
			if healthy {
				healthy = false
				reportHealth(ctx, events, uuid, false, err.Error())
			}
		} else {
			if !healthy {
				healthy = true
				reportHealth(ctx, events, uuid, true, "")
			}
			backoff = time.Second // healthy stream: reset for the next outage
			for {
				ev, rerr := ps.Recv()
				if rerr != nil {
					if ctx.Err() != nil {
						return
					}
					log.Printf("gridwell: subscribe: plugin %s stream ended: %v (retrying in %v)", uuid, rerr, backoff)
					if healthy {
						healthy = false
						reportHealth(ctx, events, uuid, false, rerr.Error())
					}
					break
				}
				select {
				case events <- qualifyEvent(uuid, transit, ev):
				case <-ctx.Done():
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// reportHealth pushes an EventPluginHealth into the fan-in channel, the same
// path a plugin's own re-qualified events take. Best-effort against ctx
// ending mid-send (the client stream is closing anyway in that case).
func reportHealth(ctx context.Context, events chan<- *pb.Event, uuid string, healthy bool, detail string) {
	ev := &pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: &pb.EventPluginHealth{
		PluginUuid: uuid,
		Healthy:    healthy,
		Detail:     detail,
	}}}
	select {
	case events <- ev:
	case <-ctx.Done():
	}
}

// proxyEndpointOf flattens an Info's network context to the one wire string
// the Electron layer feeds ses.setProxy ("socks5://host:port"); "" for
// direct/unset (the host's own network).
func proxyEndpointOf(info *pb.InfoResponse) string {
	if info == nil || info.Network == nil {
		return ""
	}
	if pe := info.Network.GetProxy(); pe != nil && pe.Address != "" {
		return pe.Scheme + "://" + pe.Address
	}
	return ""
}

// isUnimplemented reports whether a gRPC/Connect error carries an Unimplemented
// code. Used to treat a plugin's "method not supported" as a silent no-op
// (e.g. SetRootView on fs/proc which have no persistent root view).
func isUnimplemented(err error) bool {
	if err == nil {
		return false
	}
	if st, ok := status.FromError(err); ok {
		return st.Code() == gcodes.Unimplemented
	}
	return false
}

// asConnectError maps an error returned from a plugin (or, on the borrowed
// store paths, a raw store sentinel) to a Connect status code. Plugin errors
// arrive as gRPC status errors — the plugins translate store sentinels into
// codes (see localdb.errToStatus) so NotFound / InvalidArgument / overlap and
// version conflicts survive the routing hop; a non-gRPC error falls through to
// the same store.ClassifyError categorization used by the raw-HTTP endpoints.
func asConnectError(err error) error {
	if err == nil {
		return nil
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case gcodes.NotFound:
			return connect.NewError(connect.CodeNotFound, errors.New(st.Message()))
		case gcodes.InvalidArgument:
			return connect.NewError(connect.CodeInvalidArgument, errors.New(st.Message()))
		case gcodes.FailedPrecondition:
			return connect.NewError(connect.CodeFailedPrecondition, errors.New(st.Message()))
		default:
			return connect.NewError(connect.CodeInternal, errors.New(st.Message()))
		}
	}
	switch store.ClassifyError(err) {
	case store.ClassNotFound:
		return connect.NewError(connect.CodeNotFound, err)
	case store.ClassInvalidArgument:
		return connect.NewError(connect.CodeInvalidArgument, err)
	case store.ClassConflict:
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
