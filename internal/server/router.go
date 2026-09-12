package server

import (
	"context"
	"io"
	"log"
	"time"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/panelayout"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// router is a namespace.Namespace of qualified ids: every verb resolves one
// segment and forwards to the namespace that owns the rest, with the uuid
// stripped on the way in and re-applied on the way out. It holds no Gridwell
// state. Two codecs stand over it, the browser's Connect door and the
// connection socket's gRPC export, and neither routes anything of its own.
type router struct {
	srv *Server
}

// The router is a namespace of qualified ids; the compiler is what says so.
var _ namespace.Namespace = (*router)(nil)

func newRouter(srv *Server) *router { return &router{srv: srv} }

// route resolves the namespace that owns id. Ids are always qualified; there
// is no privileged namespace a bare id falls back to.
func (rt *router) route(id string) (ns namespace.Namespace, local, uuid string, transit bool, err error) {
	if _, _, ok := rpc.SplitID(id); !ok {
		return nil, "", "", false, status.Errorf(gcodes.InvalidArgument, "unqualified id %q", id)
	}
	c, local, uuid, transit, found := rt.srv.resolve(id)
	if !found {
		return nil, "", "", false, status.Errorf(gcodes.NotFound, "no plugin %q", uuid)
	}
	return c, local, uuid, transit, nil
}

// stripUUID leaves bare and foreign-prefixed ids untouched, so a cross-plugin
// reference stays qualified and the target plugin rejects it rather than
// misresolving it locally.
func stripUUID(id, uuid string) string {
	if u, l, ok := rpc.SplitID(id); ok && u == uuid {
		return l
	}
	return id
}

// tileResp applies the transit rule when the owning plugin is a node mount.
func (rt *router) tileResp(uuid string, transit bool, resp *pb.TileResponse, err error) (*pb.TileResponse, error) {
	if err != nil {
		return nil, err
	}
	t := resp.GetTile()
	if t != nil {
		t = qualifyTilesFor(transit, uuid, []*pb.Tile{t})[0]
	}
	return &pb.TileResponse{Tile: t}, nil
}

// qualifyEvent re-applies a plugin's uuid to a change event's ids. The walk is
// rpc.QualifyEventIDs, shared with the transport's prepend; only the tile rule
// is chosen here.
func qualifyEvent(uuid string, transit bool, ev *pb.Event) *pb.Event {
	return rpc.QualifyEventIDs(uuid, ev, func(t *pb.Tile) *pb.Tile {
		return qualifyTilesFor(transit, uuid, []*pb.Tile{t})[0]
	})
}

func (rt *router) GetGrid(ctx context.Context, req *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	c, local, uuid, transit, err := rt.route(req.GridId)
	if err != nil {
		return nil, err
	}
	resp, err := c.GetGrid(ctx, &pb.GetGridRequest{GridId: local})
	if err != nil {
		return nil, err
	}
	// Grid.writable and scratch_grid_id are the owning plugin's facts: a leaf
	// plugin's Info declares them once, a transit namespace carries the remote
	// node's stamp through rpc.TransitQualifyGrid.
	var g *pb.Grid
	if transit {
		g = rpc.TransitQualifyGrid(uuid, resp.Grid)
	} else {
		g = qualifyGrid(uuid, resp.Grid)
		if g != nil {
			// The declared face has no other source, so a handshake that does
			// not answer fails the read rather than presenting a read-only
			// room with no primitives and no ephemeral visits — one that
			// flips back on the next read, since Info is not negatively
			// cached. pluginhost.Adapter.synthesize applies the same rule.
			info, ierr := rt.srv.pluginInfo(ctx, uuid)
			if ierr != nil {
				return nil, infoFaceError(req.GridId, uuid, ierr)
			}
			g.Writable = info.Writable
			if info.ScratchGridId != "" {
				g.ScratchGridId = rpc.QualifyID(uuid, info.ScratchGridId)
			} else if hu := rt.srv.homeUUID(); hu != "" && hu != uuid {
				// A plugin with no scratch grid still serves grids whose
				// links open as ephemeral visits, and those land in the
				// node's home scratch grid. Stamped on the grid, which is
				// what chains through mounts.
				hinfo, herr := rt.srv.pluginInfo(ctx, hu)
				if herr != nil {
					return nil, infoFaceError(req.GridId, hu, herr)
				}
				if hinfo.ScratchGridId != "" {
					g.ScratchGridId = rpc.QualifyID(hu, hinfo.ScratchGridId)
				}
			}
			// The plugin's declared (+) menu additions.
			g.MenuEntries = rpc.QualifyMenuEntries(uuid, info.MenuEntries)
		}
	}
	return &pb.GetGridResponse{
		Grid:  g,
		Tiles: qualifyTilesFor(transit, uuid, resp.Tiles),
	}, nil
}

// infoFaceError says which grid could not be answered and why. A plugin that
// never answered its handshake is an outage, so the code is transport-class
// unless the plugin gave one of its own, and the client re-asks rather than
// latching the grid as a verdict.
func infoFaceError(gridID, uuid string, err error) error {
	code := status.Code(err)
	if code == gcodes.Unknown {
		code = gcodes.Unavailable
	}
	return status.Errorf(code, "grid %s: plugin %s handshake failed, so the grid's declared face is unknown: %v", gridID, uuid, err)
}

func (rt *router) GetTilePreview(ctx context.Context, req *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	// A leaf link's preview is its target's, resolved at the serving node.
	c, local, err := rt.srv.contentRoute(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	return c.GetTilePreview(ctx, &pb.GetTilePreviewRequest{TileId: local})
}

// pluginInfoTimeout bounds each plugin's Info handshake so one hung plugin
// cannot stall the menu; on timeout it is still listed from its config,
// without a clickable root.
const pluginInfoTimeout = 3 * time.Second

// Handshake enumerates the configured plugins in config order for the + menu.
func (rt *router) Handshake(ctx context.Context, req *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	// A namespaced request routes like every other read; "" stays local.
	if ns := req.GetNamespace(); ns != "" {
		hop, rest, ok := rpc.SplitID(ns)
		if !ok {
			hop, rest = ns, ""
		}
		c, found := rt.srv.routeClient(hop)
		if hop == rt.srv.cfg.ID && rest != "" {
			// "<id>/<conn>/…": the transport answers for the connection.
			c, found = rt.srv.pluginReg.Transport()
		}
		if !found {
			return nil, status.Errorf(gcodes.NotFound, "no plugin %q", hop)
		}
		resp, err := c.Handshake(ctx, &pb.HandshakeRequest{Namespace: rest})
		if err != nil {
			return nil, err
		}
		return rpc.TransitQualifyPluginList(hop, resp), nil
	}
	var out []*pb.PluginInfo
	for _, p := range rt.srv.pluginReg.Ordered() {
		// The server.yaml display name is authoritative, since the menu and a
		// mounted well must agree; buildPluginInfo owns the fallbacks.
		label := rt.srv.pluginReg.Label(p.UUID)
		// Bounded and cached per uuid, so a hung plugin degrades to a
		// config-only entry. A failed Info leaves info nil and the error rides
		// along, or broken and healthy-but-rootless would be identical on the
		// wire.
		info, err := rt.srv.pluginInfo(ctx, p.UUID)
		out = append(out, buildPluginInfo(p.UUID, p.Kind, label, info, err))
	}
	// Home is the node's own store, where "/" lands. Its row is first because
	// the registry registers it first, which no client has to know.
	resp := &pb.HandshakeResponse{
		Plugins:        out,
		ShellsDisabled: rt.srv.cfg.DisableShells,
		// The /content/ door's capability, handed out only here, on the
		// cookie-authenticated mux.
		ContentToken: ContentToken(rt.srv.cfg.Password),
	}
	if rt.srv.cfg.ID != "" {
		for _, p := range out {
			if p.Uuid == rt.srv.cfg.ID {
				resp.HomeGridId = p.RootGridId
				resp.HomeViewCx, resp.HomeViewCy, resp.HomeViewZoom = p.RootViewCx, p.RootViewCy, p.RootViewZoom
			}
		}
		// One row per connection, under the node's own id.
		for _, c := range rt.srv.pluginReg.Connections(ctx) {
			row := &pb.ConnectionInfo{
				Uuid: rpc.QualifyID(rt.srv.cfg.ID, c.Name), Label: c.Label,
				RootViewCx: c.ViewCx, RootViewCy: c.ViewCy, RootViewZoom: c.ViewZoom, StatusDetail: c.StatusDetail,
			}
			if c.RootGridID != "" {
				row.RootGridId = rpc.QualifyID(rt.srv.cfg.ID, c.RootGridID)
			}
			resp.Connections = append(resp.Connections, row)
		}
	}
	return resp, nil
}

// buildPluginInfo assembles a menu PluginInfo from the config and the plugin's
// Info handshake. info is nil when Info failed, with infoErr the reason: the
// plugin is still listed, so the menu never blanks a configured plugin, but
// without a clickable root. Broken and healthy-but-rootless both leave
// RootGridId == "", so InfoError is what distinguishes them. writable comes
// from Info, never the kind string. It is pure, so the fallbacks are
// unit-tested without standing up a plugin.
func buildPluginInfo(uuid, kind, configLabel string, info *pb.InfoResponse, infoErr error) *pb.PluginInfo {
	label := configLabel
	var rootGridID, scratchGridID, infoError string
	var writable bool
	var glyph string
	var menuEntries []*pb.MenuEntry
	var rootViewCx, rootViewCy, rootViewZoom float64
	if info != nil {
		if info.RootGridId != "" {
			rootGridID = rpc.QualifyID(uuid, info.RootGridId)
		}
		// Empty for a plugin that supports no ephemeral visits.
		if info.ScratchGridId != "" {
			scratchGridID = rpc.QualifyID(uuid, info.ScratchGridId)
		}
		if label == "" {
			label = info.DisplayName
		}
		writable = info.Writable
		glyph = info.Glyph
		menuEntries = rpc.QualifyMenuEntries(uuid, info.MenuEntries)
		// Forwarded verbatim from Info; the client seeds its doorway framing
		// from it.
		rootViewCx = info.RootViewCx
		rootViewCy = info.RootViewCy
		rootViewZoom = info.RootViewZoom
	} else if infoErr != nil {
		infoError = "plugin not responding: " + infoErr.Error()
	}
	// An error alongside a live Info still rides the row.
	if infoError == "" && infoErr != nil {
		infoError = infoErr.Error()
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
		Glyph:         glyph,
		MenuEntries:   menuEntries,
	}
}

func (rt *router) GetTile(ctx context.Context, req *pb.GetTileRequest) (*pb.TileResponse, error) {
	c, local, uuid, transit, err := rt.route(req.TileId)
	if err != nil {
		return nil, err
	}
	resp, err := c.GetTile(ctx, &pb.GetTileRequest{TileId: local})
	return rt.tileResp(uuid, transit, resp, err)
}

// Search is the one generic find verb. scope routes to the namespace owning
// that id; an empty scope fans out to every plugin, each bounded by
// rpc.SearchHopTimeout and errors skipped, because a search answers with what
// answered. Results come back qualified, so a hit is addressable.
func (rt *router) Search(ctx context.Context, req *pb.SearchRequest) (*pb.SearchResponse, error) {
	m := req
	if m.Scope != "" {
		c, _, uuid, transit, err := rt.route(m.Scope)
		if err != nil {
			return nil, err
		}
		resp, err := c.Search(ctx, &pb.SearchRequest{Query: localizeSearchQuery(m.Query, uuid), Limit: m.Limit})
		if err != nil {
			return nil, err
		}
		return qualifySearch(transit, uuid, resp), nil
	}
	out := &pb.SearchResponse{}
	for _, p := range rt.srv.pluginReg.Ordered() {
		c, ok := rt.srv.routeClient(p.UUID)
		if !ok {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, rpc.SearchHopTimeout)
		resp, err := c.Search(pctx, &pb.SearchRequest{Query: localizeSearchQuery(m.Query, p.UUID), Limit: m.Limit})
		cancel()
		if err != nil {
			continue // Unimplemented, a timeout, a dead plugin: no answer here
		}
		out.Results = append(out.Results, qualifySearch(false, p.UUID, resp).Results...)
	}
	// The transport fans out to every connection, in chains this node
	// re-qualifies under its own id.
	if t, ok := rt.srv.pluginReg.Transport(); ok && rt.srv.cfg.ID != "" {
		pctx, cancel := context.WithTimeout(ctx, rpc.SearchHopTimeout)
		resp, err := t.Search(pctx, &pb.SearchRequest{Query: localizeSearchQuery(m.Query, rt.srv.cfg.ID), Limit: m.Limit})
		cancel()
		if err == nil {
			out.Results = append(out.Results, qualifySearch(true, rt.srv.cfg.ID, resp).Results...)
		}
	}
	return out, nil
}

// localizeSearchQuery strips this plugin's uuid off an id: selector, through
// the one grammar parser, so a plugin never sees a foreign-qualified id.
func localizeSearchQuery(query, uuid string) string {
	q := rpc.ParseSearchQuery(query)
	if q.ID == "" {
		return query
	}
	return "id:" + stripUUID(q.ID, uuid)
}

// qualifySearch re-applies the owning namespace to every id in a search
// response, by the same leaf and transit rule as every other read.
func qualifySearch(transit bool, uuid string, resp *pb.SearchResponse) *pb.SearchResponse {
	return rpc.QualifySearchResponse(resp, func(ts []*pb.Tile) []*pb.Tile {
		return qualifyTilesFor(transit, uuid, ts)
	})
}

// CreateTile resolves the owning plugin by destination grid and forwards; an
// exit well's child_grid_id stays qualified.
func (rt *router) CreateTile(ctx context.Context, req *pb.CreateTileRequest) (*pb.TileResponse, error) {
	m := req
	// The node-wide shell refusal lives at the router, before namespace
	// resolution, so nothing can serve one. The palette hides the swatch; this
	// is the authority.
	if rt.srv.cfg.DisableShells && m.Tile.GetKind() == rpc.KindShell {
		return nil, status.Error(gcodes.PermissionDenied,
			"shell tiles are disabled on this node (server.yaml disable_shells)")
	}
	c, local, uuid, transit, err := rt.route(m.GridId)
	if err != nil {
		return nil, err
	}
	if err := rt.mintReferences(ctx, m.Tile); err != nil {
		return nil, err
	}
	m.GridId = local
	resp, err := c.CreateTile(ctx, m)
	return rt.tileResp(uuid, transit, resp, err)
}

// mintReferences canonicalizes the ids a tile is about to store. A namespace
// may accept more than one shape for the same thing, and a reference at rest
// must hold the shape that namespace answers under, or the document reached
// through the link would wear a second name.
func (rt *router) mintReferences(ctx context.Context, t *pb.Tile) error {
	if t == nil {
		return nil
	}
	child, err := rt.mintRef(ctx, t.ChildGridId)
	if err != nil {
		return err
	}
	target, err := rt.mintRef(ctx, t.LinkTargetId)
	if err != nil {
		return err
	}
	t.ChildGridId, t.LinkTargetId = child, target
	return nil
}

// mintRef canonicalizes one qualified reference through its owning namespace.
// An id that names nothing here, or a namespace that does not derive ids,
// answers itself.
func (rt *router) mintRef(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	c, local, uuid, _, ok := rt.srv.resolve(id)
	if !ok {
		return id, nil
	}
	minted, err := namespace.MintRef(ctx, c, local)
	if err != nil {
		return "", err
	}
	return rpc.QualifyID(uuid, minted), nil
}

// CloneTile clones within a plugin, or across one: a leaf copies its bytes and
// a solid well deep-copies (deepcopy.go), degrading to a link when the source
// is unreachable. The link gesture arrives as a plain CreateTile carrying a
// qualified reference, never as a clone, and the source plugin is never asked
// to write into a grid it does not own.
func (rt *router) CloneTile(ctx context.Context, req *pb.CloneTileRequest) (*pb.TileResponse, error) {
	m := req
	c, local, uuid, transit, err := rt.route(m.TileId)
	if err != nil {
		return nil, err
	}
	if dst, _, _, _, ok := rt.srv.resolve(m.DestGridId); ok && dst != c {
		return rt.cloneAcrossPlugins(ctx, m, c, local, uuid, transit)
	}
	m.TileId = local
	m.DestGridId = stripUUID(m.DestGridId, uuid)
	resp, err := c.CloneTile(ctx, m)
	return rt.tileResp(uuid, transit, resp, err)
}

// cloneAcrossPlugins reads the source tile, then creates the link or byte copy
// in the destination plugin.
func (rt *router) cloneAcrossPlugins(ctx context.Context, m *pb.CloneTileRequest, src namespace.Namespace, srcLocal, srcUUID string, srcTransit bool) (*pb.TileResponse, error) {
	resp, err := src.GetTile(ctx, &pb.GetTileRequest{TileId: srcLocal})
	if err != nil {
		return nil, err
	}
	// Qualify to the server-global view, so the link target below is what the
	// client would see.
	st := qualifyTilesFor(srcTransit, srcUUID, []*pb.Tile{resp.GetTile()})[0]
	// No version claim: a clone is layout and the source row is untouched.

	create := &pb.CreateTileRequest{
		Tile: &pb.Tile{Kind: st.Kind, X: m.X, Y: m.Y, W: st.W, H: st.H,
			AltText: st.AltText},
	}
	var copyBody []byte
	switch {
	case rpc.IsWellKind(st.Kind) && st.Reference:
		// Cloning a link copies the link: the same shared child grid and
		// framing, as a within-plugin clone of an exit well does. This is
		// also how a mount is made.
		create.Tile.ChildGridId = st.ChildGridId
		create.Tile.ViewCx = st.ViewCx
		create.Tile.ViewCy = st.ViewCy
		create.Tile.ViewZoom = st.ViewZoom
	case rpc.IsWellKind(st.Kind):
		// A deep copy (deepcopy.go) is top-down by necessity, so a mid-walk
		// failure leaves a visible, deletable partial with the error
		// surfaced.
		dst, dstLocal, dstUUID, dstTransit, err := rt.route(m.DestGridId)
		if err != nil {
			return nil, err
		}
		srcLocalTile := resp.GetTile()
		// Host content is refused before anything is created: its rows are
		// metadata stubs, so the copy would be a forest of summaries rather
		// than the files. Read off the grid, so this never learns a kind.
		if sg, gerr := src.GetGrid(ctx, &pb.GetGridRequest{GridId: srcLocalTile.ChildGridId}); gerr == nil && sg.GetGrid().GetHostContent() {
			return nil, status.Error(gcodes.Unimplemented,
				"deep copy of a host-content well is not implemented (the copy would be metadata stubs, not the host content); left-drag creates a link")
		}
		out, err := rt.deepCopyWell(ctx, src, srcTransit, srcUUID,
			srcLocalTile, dst, dstLocal, m.X, m.Y)
		if err != nil {
			if out != nil {
				// The partial is visible, so say what stopped the walk.
				return nil, status.Errorf(gcodes.Aborted,
					"deep copy incomplete (the partial copy remains, delete it if unwanted): %v", err)
			}
			if gwerr.IsTransport(err) {
				// The whole room is dark, so degrade the top-level well to
				// exactly the exit well a left-drag would have made.
				create.Tile.ChildGridId = st.ChildGridId
				create.Tile.ViewCx = st.ViewCx
				create.Tile.ViewCy = st.ViewCy
				create.Tile.ViewZoom = st.ViewZoom
				break
			}
			return nil, err
		}
		return rt.tileResp(dstUUID, dstTransit, out, nil)
	case st.LinkTargetId != "":
		// The tile being copied is a reference, so the copy is one too.
		create.Tile.LinkTargetId = st.LinkTargetId
	case st.Kind == rpc.KindText:
		// The bytes follow the create as a WriteContent below; an unreachable
		// source degrades the copy to a link, the deep walk's rule.
		if copyBody, err = readAllContent(ctx, src, srcLocal); err != nil {
			if gwerr.IsTransport(err) {
				create.Tile.LinkTargetId = st.Id
				copyBody = nil
				break
			}
			return nil, err
		}
	case st.Kind == rpc.KindURL:
		create.Tile.UrlString = st.UrlString
	case st.Kind == rpc.KindShell:
		// A PTY session is namespace-local, so the copy is a fresh shell.
	case st.Kind == rpc.KindPane:
		// A pane tile clones as a byte copy of its blob. Its ids are
		// owner-frame-relative, so the copy's panes keep naming the original
		// places: link semantics carried in bytes, not a child_grid_id.
		if st.BlobId != 0 {
			if copyBody, err = readAllContent(ctx, src, srcLocal); err != nil {
				if gwerr.IsTransport(err) {
					// Degrade to a link, as text does above.
					create.Tile.LinkTargetId = st.Id
					copyBody = nil
					break
				}
				return nil, err
			}
		}
	default:
		return nil, status.Errorf(gcodes.InvalidArgument,
			"cross-plugin clone: unsupported tile kind %q", st.Kind)
	}

	dst, dstLocal, dstUUID, dstTransit, err := rt.route(m.DestGridId)
	if err != nil {
		return nil, err
	}
	create.GridId = dstLocal
	if err := rt.mintReferences(ctx, create.Tile); err != nil {
		return nil, err
	}
	out, err := dst.CreateTile(ctx, create)
	if err != nil {
		return rt.tileResp(dstUUID, dstTransit, out, err)
	}
	if copyBody != nil {
		// Not atomic with the create: a failure leaves a visible, deletable
		// empty copy and surfaces, never a silent half-state.
		if _, werr := writeAllContent(ctx, dst, out.GetTile().GetId(), out.GetTile().GetVersion(), copyBody); werr != nil {
			return nil, werr
		}
		// Re-read so the response row carries the post-write version.
		fresh, gerr := dst.GetTile(ctx, &pb.GetTileRequest{TileId: out.GetTile().GetId()})
		if gerr == nil {
			out = fresh
		}
	}
	return rt.tileResp(dstUUID, dstTransit, out, err)
}

// readAllContent drains a namespace's ReadContent stream into one value.
func readAllContent(ctx context.Context, c namespace.Namespace, tileID string) ([]byte, error) {
	var data []byte
	err := c.ReadContent(ctx, &pb.ReadContentRequest{TileId: tileID}, func(chunk *pb.ContentChunk) error {
		data = append(data, chunk.Data...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

// writeAllContent sends one complete value up a namespace's WriteContent
// stream, committing at close.
func writeAllContent(ctx context.Context, c namespace.Namespace, tileID string, version int64, data []byte) (*pb.TileResponse, error) {
	sent := false
	return c.WriteContent(ctx, func() (*pb.WriteContentRequest, error) {
		if sent {
			return nil, io.EOF
		}
		sent = true
		return &pb.WriteContentRequest{TileId: tileID, Version: version, Data: data}, nil
	})
}

// SetTile is the single framing and preview writeback router; the owning
// namespace dispatches on the target tile's kind.
func (rt *router) SetTile(ctx context.Context, req *pb.SetTileRequest) (*pb.TileResponse, error) {
	m := req
	c, local, uuid, transit, err := rt.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	resp, err := c.SetTile(ctx, m)
	return rt.tileResp(uuid, transit, resp, err)
}

func (rt *router) DeleteTile(ctx context.Context, req *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error) {
	m := req
	qualifiedID := m.TileId
	c, local, _, transit, err := rt.route(m.TileId)
	if err != nil {
		return nil, err
	}
	// The layout blob is the only record of a pane tile's ephemeral leaves, so
	// the delete must terminate what the arrangement owns. Capture before,
	// because the blob dies with the row, and reap after only if the row is
	// really gone, since a trashed pane tile keeps its ephemerals for a
	// restore. A transit tile skips this hop: the forwarded delete reaches the
	// owning node's router.
	var candidates []string
	if !transit {
		candidates = rt.workspaceEphemeralCandidates(ctx, c, local, qualifiedID)
	}
	m.TileId = local
	// The owning namespace reaps the tile's shell session as part of
	// DeleteTile: the PTY lives behind the interface.
	if _, err := c.DeleteTile(ctx, m); err != nil {
		return nil, err
	}
	if len(candidates) > 0 {
		// Reap only on an explicit NotFound: unreadable now is not destroyed,
		// and a missed reap is reclaimed by the boot sweep, a wrong one by
		// nothing.
		if _, err := c.GetTile(ctx, &pb.GetTileRequest{TileId: local}); status.Code(err) == gcodes.NotFound {
			rt.reapWorkspaceEphemerals(ctx, candidates, qualifiedID)
		}
	}
	return &pb.DeleteTileResponse{}, nil
}

// workspaceEphemeralCandidates reads a pane tile's layout blob for the leaf ids
// that might be its own ephemerals; an unreadable blob yields nothing rather
// than a guess. "Which content tiles does this blob reference" has one owner,
// panelayout.TextFocusIDs, which the boot sweep reads for its protection set,
// and this reap is the other side of that question, so it must be the same
// derivation or an ephemeral is reaped or kept wrongly.
func (rt *router) workspaceEphemeralCandidates(ctx context.Context, owner namespace.Namespace, localID, qualifiedID string) []string {
	tr, err := owner.GetTile(ctx, &pb.GetTileRequest{TileId: localID})
	if err != nil || tr.GetTile() == nil || tr.GetTile().Kind != rpc.KindPane || tr.GetTile().BlobId == 0 {
		return nil
	}
	body, err := readAllContent(ctx, owner, localID)
	if err != nil || len(body) == 0 {
		return nil
	}
	// Blob ids are already in this node's frame: the encoder strips the
	// reader's transit prefix, empty for a locally-owned pane tile.
	ids, err := panelayout.TextFocusIDs(body)
	if err != nil {
		log.Printf("gridwell: delete %s: layout blob unreadable, reaping nothing: %v", qualifiedID, err)
		return nil
	}
	return ids
}

// reapWorkspaceEphemerals deletes the scratch-grid tiles among a destroyed pane
// tile's captured leaves. A non-scratch tile is content the arrangement merely
// viewed. Best-effort: a failure must not block the user's delete.
func (rt *router) reapWorkspaceEphemerals(ctx context.Context, candidates []string, qualifiedID string) {
	for _, id := range candidates {
		ec, elocal, euuid, transit, err := rt.route(id)
		if err != nil {
			continue
		}
		if transit {
			// A remote's scratch-grid fact is invisible through the raw
			// transit client, so its own boot sweep reclaims this.
			log.Printf("gridwell: delete %s: not reaping remote ephemeral candidate %s (transit)", qualifiedID, id)
			continue
		}
		// Ephemeral means the tile's grid is the owning namespace's scratch
		// grid, the fact GetGrid stamps from Info.
		info, err := rt.srv.pluginInfo(ctx, euuid)
		if err != nil {
			log.Printf("gridwell: delete %s: not reaping candidate %s: plugin %s handshake failed: %v", qualifiedID, id, euuid, err)
			continue
		}
		if info.ScratchGridId == "" {
			continue
		}
		et, err := ec.GetTile(ctx, &pb.GetTileRequest{TileId: elocal})
		if err != nil || et.GetTile() == nil || et.GetTile().GridId != info.ScratchGridId {
			continue // not an ephemeral: viewed content, never touched
		}
		if _, err := ec.DeleteTile(ctx, &pb.DeleteTileRequest{TileId: elocal}); err != nil {
			log.Printf("gridwell: delete %s: reaping ephemeral %s failed: %v", qualifiedID, id, err)
		}
	}
}

// SetFraming is the one framing write, routed on whichever target the request
// names. A plugin that keeps no framing answers Unimplemented, which is not an
// error here, since a read-only plugin's ascent must not surface one. After a
// root write the per-plugin Info cache is invalidated, because the root_view_*
// fields travel in Info.
func (rt *router) SetFraming(ctx context.Context, req *pb.SetFramingRequest) (*pb.SetFramingResponse, error) {
	m := req
	root := m.RootGridId != ""
	ref := m.TileId
	if root {
		ref = m.RootGridId
	}
	c, local, uuid, transit, err := rt.route(ref)
	if err != nil {
		return nil, err
	}
	out := &pb.SetFramingRequest{Cx: m.Cx, Cy: m.Cy, Zoom: m.Zoom}
	if root {
		out.RootGridId = local
	} else {
		out.TileId = local
	}
	resp, err := c.SetFraming(ctx, out)
	if err != nil {
		if isUnimplemented(err) {
			return &pb.SetFramingResponse{}, nil
		}
		return nil, err
	}
	if root {
		rt.srv.invalidateInfoCache(uuid)
		return &pb.SetFramingResponse{}, nil
	}
	// A doorway tile comes back qualified like every other tile response.
	t := resp.GetTile()
	if t != nil {
		t = qualifyTilesFor(transit, uuid, []*pb.Tile{t})[0]
	}
	return &pb.SetFramingResponse{Tile: t}, nil
}

// ShellSessionAlive routes the per-descent probe to the namespace holding the
// PTY. An infrastructure error reports not-alive rather than a Connect error:
// the client only cares whether the refresh button hides.
func (rt *router) ShellSessionAlive(ctx context.Context, req *pb.ShellSessionAliveRequest) (*pb.ShellSessionAliveResponse, error) {
	// With disable_shells every session is unreachable by design.
	if rt.srv.cfg.DisableShells {
		return &pb.ShellSessionAliveResponse{Alive: false}, nil
	}
	c, local, _, _, err := rt.route(req.TileId)
	if err != nil {
		return &pb.ShellSessionAliveResponse{Alive: false}, nil
	}
	resp, err := c.ShellSessionAlive(ctx, &pb.ShellSessionAliveRequest{TileId: local})
	if err != nil {
		return &pb.ShellSessionAliveResponse{Alive: false}, nil
	}
	return resp, nil
}

// Subscribe fans every watching namespace's change-event stream into the
// client's, re-qualifying each event's ids. A namespace declares that it emits
// events through Info.watch, a capability and never the kind string.
//
// Failures heal rather than silently ending a namespace's events for the life
// of the client stream: watchPlugin re-dials Info and fanInEvents re-dials the
// stream, and the client hears about the outage and the recovery through an
// EventPluginHealth instead of tiles quietly going stale.
func (rt *router) Subscribe(ctx context.Context, _ *pb.SubscribeRequest, send func(*pb.Event) error) error {
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	events := make(chan *pb.Event, 64)
	for _, p := range rt.srv.pluginReg.Ordered() {
		c, ok := rt.srv.pluginReg.Get(p.UUID)
		if !ok {
			continue
		}
		uuid := p.UUID
		go watchPlugin(subCtx, uuid, false, c,
			func(ctx context.Context) (*pb.InfoResponse, error) { return rt.srv.pluginInfo(ctx, uuid) }, events)
	}
	if t, ok := rt.srv.pluginReg.Transport(); ok && rt.srv.cfg.ID != "" {
		// The transport is ready as soon as it exists: it fans in every
		// connection's events, and there is no handshake to ask.
		go watchPlugin(subCtx, rt.srv.cfg.ID, true, t,
			func(context.Context) (*pb.InfoResponse, error) { return &pb.InfoResponse{}, nil }, events)
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

// watchPlugin waits for plugin uuid to answer Info and hands off to
// fanInEvents. The Info fetch is retried with fanInEvents' backoff, because
// giving up after one failure would permanently exclude a plugin that was
// merely slow to start. It owns the health transitions until Info succeeds;
// after that fanInEvents does.
func watchPlugin(ctx context.Context, uuid string, transit bool, ns namespace.Namespace, infoOf func(context.Context) (*pb.InfoResponse, error), events chan<- *pb.Event) {
	backoff := time.Second
	healthy := true // assume healthy until the first failure
	for {
		_, err := infoOf(ctx)
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
		// A plugin that went down on a transient Info failure and came back
		// must have its notice cleared here, before the fan-in takes over, or
		// the client shows "live updates stopped" for a plugin that is up.
		if !healthy {
			healthy = true
			reportHealth(ctx, events, uuid, true, "")
		}
		fanInEvents(ctx, uuid, transit, ns, events) // owns health from here; returns only when ctx ends
		return
	}
}

// fanInEvents relays one namespace's Subscribe stream until ctx ends, re-dialing
// with backoff so a plugin restart resumes its events. Failures are reported as
// an EventPluginHealth transition, not once per retry, because events that
// silently stop present as "tiles stopped updating" with no evidence.
func fanInEvents(ctx context.Context, uuid string, transit bool, ns namespace.Namespace, events chan<- *pb.Event) {
	backoff := time.Second
	healthy := true // caller (watchPlugin) already reported recovery if it was ever down
	for {
		// namespace.Follow supplies the moment a callback stream has no open to
		// report; established is what "this namespace is back" means.
		err := namespace.Follow(ctx, ns, &pb.SubscribeRequest{},
			func(ev *pb.Event) error {
				select {
				case events <- qualifyEvent(uuid, transit, ev):
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
			func() {
				if !healthy {
					healthy = true
					reportHealth(ctx, events, uuid, true, "")
				}
				backoff = time.Second // a live stream: reset for the next outage
			})
		if ctx.Err() != nil {
			return
		}
		// A stream that ends, error or clean, is an outage, and never silent.
		detail := "the event stream ended"
		if err != nil {
			detail = err.Error()
		}
		log.Printf("gridwell: subscribe: plugin %s stream ended: %v (retrying in %v)", uuid, detail, backoff)
		if healthy {
			healthy = false
			reportHealth(ctx, events, uuid, false, detail)
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

// reportHealth pushes an EventPluginHealth down the path a namespace's own
// events take. Best-effort against ctx ending mid-send.
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

// isUnimplemented is how a namespace's "method not supported" becomes a silent
// no-op, as with SetFraming on a plugin that keeps no framing.
func isUnimplemented(err error) bool {
	if err == nil {
		return false
	}
	if st, ok := status.FromError(err); ok {
		return st.Code() == gcodes.Unimplemented
	}
	return false
}

// Info describes this node to a mounter, on the connection door only; the
// browser learns the same from Handshake. Watch is true because Subscribe fans
// in every namespace's events.
func (rt *router) Info(ctx context.Context, _ *pb.InfoRequest) (*pb.InfoResponse, error) {
	// A mount lands where a direct client lands, by rpc.HomeGrid's derivation
	// over the same handshake. A node has no grid of its own.
	lp, err := rt.Handshake(ctx, &pb.HandshakeRequest{})
	if err != nil {
		return nil, err
	}
	root := ""
	for _, p := range lp.Plugins {
		if p.RootGridId != "" {
			root = p.RootGridId
			break
		}
	}
	return &pb.InfoResponse{
		Writable:   false,
		RootGridId: root,
	}, nil
}

// Probe routes by tile id: presence is the owning namespace's verdict, never
// inferred from reachability.
func (rt *router) Probe(ctx context.Context, req *pb.ProbeRequest) (*pb.ProbeResponse, error) {
	c, local, ok := rt.srv.clientForID(req.TileId)
	if !ok {
		return nil, status.Errorf(gcodes.NotFound, "no plugin for %q", req.TileId)
	}
	return c.Probe(ctx, &pb.ProbeRequest{TileId: local})
}

// OpenShell attaches a tile's PTY through the one shell route, the same one
// the browser's /shell WebSocket enters by (shell_door.go).
func (rt *router) OpenShell(ctx context.Context, recv func() (*pb.OpenShellRequest, error), send func(*pb.OpenShellResponse) error) error {
	first, err := recv()
	if err != nil {
		return err
	}
	return rt.srv.openShellRoute(ctx, first, recv, send)
}
