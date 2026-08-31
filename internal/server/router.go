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
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// router is the routing implementation, and it is itself a
// namespace.Namespace: a space of qualified ids whose verbs resolve one
// segment and forward to the namespace that owns the rest. It holds no
// Gridwell state — every request goes to the namespace that owns the id it
// carries, whether home, a content plugin, or the transport — with the uuid
// prefix stripped on the way in and re-applied to the response on the way
// out.
//
// Two codecs stand over this one router and neither routes anything of its
// own: the browser's Connect door (connect_codec.go) and the federation
// socket's gRPC export (namespace.Server, nodeexport.go). They cannot drift,
// because they are the same value.
//
// Subscribe fans in every watching namespace's event stream, per Info.watch;
// ShellSessionAlive routes to the owner.
type router struct {
	srv *Server
}

// The router is a namespace of qualified ids; the compiler is what says so.
var _ namespace.Namespace = (*router)(nil)

func newRouter(srv *Server) *router { return &router{srv: srv} }

// ── routing ────────────────────────────────────────────────────────────────────

// route resolves the namespace that owns id, through Server.resolve,
// returning it, the local id, the uuid to re-qualify with, and whether the
// namespace is transit. Ids are always qualified; there is no privileged
// namespace a bare id could fall back to.
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

// stripUUID removes a specific plugin uuid prefix from an id, leaving bare and
// foreign-prefixed ids untouched (so a cross-plugin reference stays qualified
// and the target plugin rejects it rather than misresolving it locally).
func stripUUID(id, uuid string) string {
	if u, l, ok := rpc.SplitID(id); ok && u == uuid {
		return l
	}
	return id
}

// tileResp qualifies a plugin's TileResponse for the client, applying the
// transit rule when the owning plugin is a node mount.
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

// qualifyEvent re-applies a plugin's uuid to the ids in a change event.
// transit selects the transit tile rule: a chain prepend with the Reference
// bit trusted. The walk and the plain prepends live in api/rpc, in
// QualifyEventIDs, shared with the transport's per-connection prepend; only
// the tile rule is chosen here.
func qualifyEvent(uuid string, transit bool, ev *pb.Event) *pb.Event {
	return rpc.QualifyEventIDs(uuid, ev, func(t *pb.Tile) *pb.Tile {
		return qualifyTilesFor(transit, uuid, []*pb.Tile{t})[0]
	})
}

// ── reads ──────────────────────────────────────────────────────────────────────

func (rt *router) GetGrid(ctx context.Context, req *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	c, local, uuid, transit, err := rt.route(req.GridId)
	if err != nil {
		return nil, err
	}
	resp, err := c.GetGrid(ctx, &pb.GetGridRequest{GridId: local})
	if err != nil {
		return nil, err
	}
	// Grid.writable and scratch_grid_id are per-grid facts of the owning
	// plugin. A leaf plugin does not stamp them, since its Info declares them
	// once; a transit namespace's response carries the remote node's stamp
	// with ids prepended one segment, through rpc.TransitQualifyGrid, the one
	// grid rule, shared with the transport's hop.
	var g *pb.Grid
	if transit {
		g = rpc.TransitQualifyGrid(uuid, resp.Grid)
	} else {
		g = qualifyGrid(uuid, resp.Grid)
		if g != nil {
			if info, ierr := rt.srv.pluginInfo(ctx, uuid); ierr == nil {
				g.Writable = info.Writable
				if info.ScratchGridId != "" {
					g.ScratchGridId = rpc.QualifyID(uuid, info.ScratchGridId)
				} else if hu := rt.srv.homeUUID(); hu != "" && hu != uuid {
					// A plugin that declares no scratch grid — fs, proc,
					// gitlab — still serves grids whose links open as
					// ephemeral visits, and those land in the node's home
					// scratch grid. Stamped here because the grid is the
					// carrier that chains through mounts: a client behind a
					// transit hop has no other way to learn it.
					if hinfo, herr := rt.srv.pluginInfo(ctx, hu); herr == nil && hinfo.ScratchGridId != "" {
						g.ScratchGridId = rpc.QualifyID(hu, hinfo.ScratchGridId)
					}
				}
				// The plugin's declared (+) menu additions.
				g.MenuEntries = rpc.QualifyMenuEntries(uuid, info.MenuEntries)
			}
		}
	}
	return &pb.GetGridResponse{
		Grid:  g,
		Tiles: qualifyTilesFor(transit, uuid, resp.Tiles),
	}, nil
}

func (rt *router) GetTilePreview(ctx context.Context, req *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	// contentRoute: a leaf link's preview is its target's preview, resolved
	// at the serving node.
	c, local, err := rt.srv.contentRoute(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	return c.GetTilePreview(ctx, &pb.GetTilePreviewRequest{TileId: local})
}

// pluginInfoTimeout bounds each plugin's Info handshake during Handshake so
// one slow or hung plugin cannot stall the whole menu. On timeout the plugin
// is still listed from its config, by kind and configured label, just without
// a clickable root.
const pluginInfoTimeout = 3 * time.Second

// Handshake enumerates the configured plugins in config order so the client
// can build the + menu. label comes from each plugin's Info, and so does
// writable, whether it accepts new tiles: a capability the handshake
// declares, never derived from the kind string.
func (rt *router) Handshake(ctx context.Context, req *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	// A namespaced request routes: peel one segment, forward the rest, and
	// re-qualify the answer with this hop, the same shape as every routed
	// read. "" stays the local handshake.
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
		// mounted well must agree. buildPluginInfo falls back to Info's name,
		// then the kind.
		label := rt.srv.pluginReg.Label(p.UUID)
		// Info is the whole handshake — the default root grid, the fallback
		// label, and the capabilities — served from the per-uuid cache after
		// the first success. It is bounded, so a hung plugin degrades to a
		// config-only entry instead of hanging the menu. A failed Info leaves
		// info nil and the error rides along, so buildPluginInfo can
		// distinguish broken from healthy but rootless; dropping the error
		// here would make both cases identical on the wire.
		info, err := rt.srv.pluginInfo(ctx, p.UUID)
		out = append(out, buildPluginInfo(p.UUID, p.Kind, label, info, err))
	}
	// Home is a field: the node's own store, where "/" lands. Its row is
	// first in out, because the registry registers it first, but a client
	// never has to know that.
	resp := &pb.HandshakeResponse{
		Plugins:        out,
		ShellsDisabled: rt.srv.cfg.DisableShells,
		// The /content/ door's path capability, handed out only here, on the
		// cookie-authenticated mux, so only a logged-in client learns it. See
		// content_door.go.
		ContentToken: ContentToken(rt.srv.cfg.Password),
	}
	if rt.srv.cfg.ID != "" {
		for _, p := range out {
			if p.Uuid == rt.srv.cfg.ID {
				resp.HomeGridId = p.RootGridId
				resp.HomeViewCx, resp.HomeViewCy, resp.HomeViewZoom = p.RootViewCx, p.RootViewCy, p.RootViewZoom
			}
		}
		// The connections: one row each, under the node's own id
		// ("<id>/<conn>"), from the transport's declared set.
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

// buildPluginInfo assembles a menu PluginInfo from the config — uuid, kind,
// configured label — and the plugin's Info handshake. info is nil when Info
// failed or timed out, with infoErr the reason: the plugin is still listed, so
// the menu never blanks a configured plugin, but with no clickable root or
// scratch grid, no writable bit, and a label that falls back to the configured
// name and then the kind. It is pure, so the fallback rules are unit-tested
// without standing up a plugin.
//
// A nil info, meaning broken, and a non-nil info whose RootGridId is "",
// meaning healthy but rootless, both leave RootGridId == "" on the result.
// Without InfoError they would be the same PluginInfo and the menu row could
// not tell them apart. InfoError is what distinguishes them: set only in the
// broken case, always empty in the rootless one.
//
// writable comes from the Info handshake, never from the kind string: a remote
// home reached through a connection is every bit as writable, and the
// capability travels in Info while a kind check would strand it read-only.
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
		// The qualified scratch grid id, the ephemeral-url target. It is
		// empty for a plugin that does not support ephemeral visits.
		if info.ScratchGridId != "" {
			scratchGridID = rpc.QualifyID(uuid, info.ScratchGridId)
		}
		if label == "" {
			label = info.DisplayName
		}
		writable = info.Writable
		glyph = info.Glyph
		menuEntries = rpc.QualifyMenuEntries(uuid, info.MenuEntries)
		// The root viewport, forwarded verbatim from Info; home fills it and
		// fs and proc return zero. The client seeds its doorway framing from
		// it.
		rootViewCx = info.RootViewCx
		rootViewCy = info.RootViewCy
		rootViewZoom = info.RootViewZoom
	} else if infoErr != nil {
		infoError = "plugin not responding: " + infoErr.Error()
	}
	// An error alongside a live Info still rides the row: without it, "store
	// down" and "healthy but rootless" are indistinguishable on the wire.
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

// Search is the one generic find verb. scope, any qualified id, routes to the
// namespace owning it, localized like every routed call. An empty scope fans
// out to every configured plugin in config order, each bounded by
// rpc.SearchHopTimeout, with Unimplemented and errors skipped, because a
// search answers with what answered. Results come back qualified like every
// other id, tile and path rows alike, so a hit is immediately addressable.
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
	// The connections: the transport fans out to every remote, answering in
	// chains this node re-qualifies under its own id.
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

// localizeSearchQuery strips this plugin's uuid off an id: selector the way
// every routed id is localized; other queries pass through verbatim. It is one
// place, through the one grammar parser, so a plugin never sees a
// foreign-qualified id it would fail to parse.
func localizeSearchQuery(query, uuid string) string {
	q := rpc.ParseSearchQuery(query)
	if q.ID == "" {
		return query
	}
	return "id:" + stripUUID(q.ID, uuid)
}

// qualifySearch re-applies the owning namespace to every id in a search
// response, with the same leaf and transit rule as every other read. The walk
// is rpc.QualifySearchResponse, shared with the transport's per-connection
// prepend.
func qualifySearch(transit bool, uuid string, resp *pb.SearchResponse) *pb.SearchResponse {
	return rpc.QualifySearchResponse(resp, func(ts []*pb.Tile) []*pb.Tile {
		return qualifyTilesFor(transit, uuid, ts)
	})
}

// ── creates ──────────────────────────────────────────────────────────────────

// CreateTile is the single create router: resolve the owning plugin by
// destination grid, localize the grid_id, and forward. tile.kind selects the
// primitive, and tile.child_grid_id, an exit well's cross-plugin reference,
// stays qualified.
func (rt *router) CreateTile(ctx context.Context, req *pb.CreateTileRequest) (*pb.TileResponse, error) {
	m := req
	// The node-wide shell refusal, from server.yaml's disable_shells, lives
	// at the router, before namespace resolution, so nothing — home, a
	// plugin, a connection — can serve one. The palette hides the swatch;
	// this is the authority.
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

// mintReferences canonicalizes the ids a tile is about to STORE — an exit
// well's child grid, a leaf link's target — before the create that stores
// them. A namespace may answer an untouched thing by what it is rather than
// by a row (pluginhost's derived addresses); a reference at rest must name a
// row, so this is where a link or a mount pays for the row its target has
// been getting for free. It is the one call, made by every create that can
// carry a reference: the client's link drop and the cross-plugin clone.
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
// An id that names nothing here, or a namespace that does not derive ids —
// home, a mount of another node, which mints its own rows and has no wire verb
// to be asked — answers itself.
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

// ── mutations ──────────────────────────────────────────────────────────────────

// CloneTile clones within a plugin, or, when the destination grid belongs to a
// different plugin, applies the cross-plugin clone contract: a right-drag
// copies everywhere, and a left-drag across a boundary links. A leaf copies
// its bytes into the destination plugin; a solid well deep-copies, in
// deepcopy.go, degrading to a link when the source is unreachable. The link
// gesture arrives here as a plain CreateTile carrying a qualified
// child_grid_id or link_target_id, never as a clone. The source plugin is
// never asked to write into a grid it does not own.
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

// cloneAcrossPlugins materializes a cross-plugin clone: read the source tile
// from its plugin, then create the link (well) or byte copy (leaf) in the
// destination plugin. src is the source plugin's client; srcLocal/srcUUID the
// routed source tile id.
func (rt *router) cloneAcrossPlugins(ctx context.Context, m *pb.CloneTileRequest, src namespace.Namespace, srcLocal, srcUUID string, srcTransit bool) (*pb.TileResponse, error) {
	resp, err := src.GetTile(ctx, &pb.GetTileRequest{TileId: srcLocal})
	if err != nil {
		return nil, err
	}
	// Qualify to the server-global view: an interior well's child becomes
	// "<srcUUID>/<grid>" and an exit well's already-qualified target stays
	// put, so either way the link target below is exactly what the client
	// would see.
	st := qualifyTilesFor(srcTransit, srcUUID, []*pb.Tile{resp.GetTile()})[0]
	// No version claim: a clone is layout, the source row is untouched, and
	// version means the user's content bytes and nothing else.

	create := &pb.CreateTileRequest{
		Tile: &pb.Tile{Kind: st.Kind, X: m.X, Y: m.Y, W: st.W, H: st.H,
			AltText: st.AltText},
	}
	var copyBody []byte
	switch {
	case st.Kind == "well" && st.Reference:
		// The source is itself a link: an exit well, a menu swatch, or an
		// earlier cross-plugin link. Cloning a link copies the link — the
		// same shared child grid and the same framing — exactly as a
		// within-plugin clone of an exit well does. This is also how a mount
		// is made, by right-dragging a connection row.
		create.Tile.ChildGridId = st.ChildGridId
		create.Tile.ViewCx = st.ViewCx
		create.Tile.ViewCy = st.ViewCy
		create.Tile.ViewZoom = st.ViewZoom
	case st.Kind == "well":
		// A solid well's cross-plugin clone is a deep copy: walk the source
		// subtree and materialize it in the destination. It is top-down by
		// necessity, because a child grid is allocated by its well's create,
		// so the copy appears and fills in, and a mid-walk failure leaves a
		// visible, deletable partial with the error surfaced.
		dst, dstLocal, dstUUID, dstTransit, err := rt.route(m.DestGridId)
		if err != nil {
			return nil, err
		}
		srcLocalTile := resp.GetTile()
		// A subtree the owning plugin declares as HOST CONTENT — an fs
		// directory, the proc table — is refused before anything is created:
		// its rows are host metadata stubs, so the copy would be a forest of
		// summaries rather than the files, and a gesture that returns
		// something other than what it names diverges silently. Copying host
		// files is its own feature. The declaration is read off the grid, so
		// this rule never learns a plugin's kind; a plugin whose rows carry
		// their own content copies like any other.
		if sg, gerr := src.GetGrid(ctx, &pb.GetGridRequest{GridId: srcLocalTile.ChildGridId}); gerr == nil && sg.GetGrid().GetHostContent() {
			return nil, status.Error(gcodes.Unimplemented,
				"deep copy of a host-content well is not implemented (the copy would be metadata stubs, not the host content); left-drag creates a link")
		}
		out, err := rt.deepCopyWell(ctx, src, srcTransit, srcUUID,
			srcLocalTile, dst, dstLocal, m.X, m.Y)
		if err != nil {
			if out != nil {
				// The partial is real and visible, so say what stopped the
				// walk.
				return nil, status.Errorf(gcodes.Aborted,
					"deep copy incomplete (the partial copy remains, delete it if unwanted): %v", err)
			}
			if gwerr.IsTransport(err) {
				// The whole room is dark, so degrade the top-level well to a
				// link. st.ChildGridId is already the qualified target, so
				// this is exactly the exit well a left-drag would have made,
				// framing included.
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
		// A leaf link clones as another link to the same target: the tile
		// being copied is a reference, so copying it copies the reference.
		create.Tile.LinkTargetId = st.LinkTargetId
	case st.Kind == "text":
		// The body bytes follow the create as a WriteContent below: one way
		// to write bytes, even inside the router. An unreachable source
		// degrades the copy to a link to the original, the same rule as
		// inside a deep walk.
		if copyBody, err = readAllContent(ctx, src, srcLocal); err != nil {
			if gwerr.IsTransport(err) {
				create.Tile.LinkTargetId = st.Id
				copyBody = nil
				break
			}
			return nil, err
		}
	case st.Kind == "url":
		create.Tile.UrlString = st.UrlString
	case st.Kind == "shell":
		// A shell's PTY session is namespace-local, so the copy is a fresh
		// shell tile carrying the same label.
	case st.Kind == "pane":
		// A pane tile clones as a byte copy of its layout blob, like text.
		// The layout's ids are owner-frame-relative, so the copy's panes
		// keep naming the original places — a pane tile is an arrangement of
		// references, not content — which is the cross-plugin link semantics
		// carried in bytes instead of a child_grid_id. A never-arranged pane
		// tile, with no blob, copies with no body.
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
		// The copied bytes follow the create through the one content door. It
		// is not atomic with the create: a failure here leaves a visible,
		// deletable empty copy and surfaces, never a silent half-state.
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
// stream, committing at close like every content write.
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

// SetTile is the single framing and preview writeback router. The owning
// namespace dispatches on the target tile's kind to the one operation that
// kind supports.
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
	// A pane tile's layout blob is the only record of its ephemeral leaves,
	// the scratch-grid tiles. Deleting the pane tile deletes only the
	// arrangement, but must terminate what that arrangement owns, exactly as
	// closing a pane does. The owning namespace alone decides whether this
	// delete destroys the row or merely parks it in the trash, so capture the
	// candidates before the delete, because the blob dies with the row, and
	// reap after, only if the row is actually gone: a trashed pane tile keeps
	// its ephemerals so a restore comes back whole. A transit tile skips this
	// hop, because the forwarded delete reaches the owning node's router,
	// whose blob ids are in that node's frame.
	var candidates []string
	if !transit {
		candidates = rt.workspaceEphemeralCandidates(ctx, c, local, qualifiedID)
	}
	m.TileId = local
	// The owning namespace reaps the tile's shell session, if any, as part of
	// DeleteTile: the PTY lives behind the interface.
	if _, err := c.DeleteTile(ctx, m); err != nil {
		return nil, err
	}
	if len(candidates) > 0 {
		// Reap only on an explicit NotFound. "Unreadable right now" —
		// Unavailable, a deadline — is not "destroyed", and the reap is
		// unrecoverable while the trash may be keeping the pane tile for a
		// restore. A missed reap is reclaimed by the boot sweep; a wrong reap
		// is reclaimed by nothing.
		if _, err := c.GetTile(ctx, &pb.GetTileRequest{TileId: local}); status.Code(err) == gcodes.NotFound {
			rt.reapWorkspaceEphemerals(ctx, candidates, qualifiedID)
		}
	}
	return &pb.DeleteTileResponse{}, nil
}

// workspaceEphemeralCandidates reads a pane tile's layout blob and returns the
// leaf ids that might be its own ephemerals, captured before the delete
// because the blob dies with the row. It returns nil for anything that is not
// a readable pane layout, and an unreadable blob — corrupt, or written by a
// newer Gridwell — yields nothing rather than a guess; the boot sweep reclaims
// the leftovers once the pane tile is gone.
func (rt *router) workspaceEphemeralCandidates(ctx context.Context, owner namespace.Namespace, localID, qualifiedID string) []string {
	tr, err := owner.GetTile(ctx, &pb.GetTileRequest{TileId: localID})
	if err != nil || tr.GetTile() == nil || tr.GetTile().Kind != rpc.KindPane || tr.GetTile().BlobId == 0 {
		return nil
	}
	body, err := readAllContent(ctx, owner, localID)
	if err != nil || len(body) == 0 {
		return nil
	}
	// Blob ids are in this node's frame: the encoder strips the reader's
	// transit prefix, which is empty for a locally-owned pane tile.
	tree, err := pane.DecodeLayout(body, func(id string) string { return id }, "")
	if err != nil {
		log.Printf("gridwell: delete %s: layout blob unreadable, reaping nothing: %v", qualifiedID, err)
		return nil
	}
	return pane.LeafTextFocusIDs(tree)
}

// reapWorkspaceEphemerals deletes the scratch-grid tiles among a destroyed
// pane tile's captured layout leaves: its ephemeral shells and urls, whose
// tmux sessions die through the ordinary DeleteTile chain. A referenced
// non-scratch tile is content the arrangement merely viewed and is never
// touched. It is best-effort: a failure here must not block the user's delete,
// so errors go to the log.
func (rt *router) reapWorkspaceEphemerals(ctx context.Context, candidates []string, qualifiedID string) {
	for _, id := range candidates {
		ec, elocal, euuid, transit, err := rt.route(id)
		if err != nil {
			continue
		}
		if transit {
			// A remote node's ephemeral: this node cannot see the remote's
			// scratch-grid fact through the raw transit client, so the
			// remote's boot sweep reclaims it.
			log.Printf("gridwell: delete %s: not reaping remote ephemeral candidate %s (transit)", qualifiedID, id)
			continue
		}
		// A tile is ephemeral exactly when its grid is the owning namespace's
		// scratch grid: the same fact the server's GetGrid stamps from Info,
		// since the raw namespace response does not carry it.
		info, err := rt.srv.pluginInfo(ctx, euuid)
		if err != nil || info.ScratchGridId == "" {
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

// SetFraming persists a grid's framing: the one framing write, routed on
// whichever target the request names — the doorway tile a grid was entered
// through, or the root grid itself when there is no doorway. A plugin that
// keeps no framing answers Unimplemented, which is not an error here, since a
// read-only plugin's ascent must not surface one to the user.
//
// After a root write the per-plugin Info cache is invalidated: the root_view_*
// fields travel in the Info handshake and now differ from the cached values,
// so the next Handshake must re-fetch Info to see the viewport the user just
// left.
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

// ShellSessionAlive routes the client's per-descent probe to the owning
// namespace, which holds the PTY and answers whether the tile's session is
// alive. An infrastructure error is reported as not-alive rather than as a
// Connect error: the client only cares whether the refresh button should
// hide.
func (rt *router) ShellSessionAlive(ctx context.Context, req *pb.ShellSessionAliveRequest) (*pb.ShellSessionAliveResponse, error) {
	// With disable_shells, every session is unreachable by design, so answer
	// gone: the client hides the refresh and reconnect affordance exactly as
	// it does for a dead tmux session.
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
// client's stream, re-qualifying each event's ids with the emitting
// namespace's uuid. A namespace declares that it emits events through
// Info.watch, a capability from the handshake and never the kind string, so a
// remote node's events flow exactly like a local plugin's. fs and proc report
// watch=false and are polled through GetGrid. With no single root, a client
// subscribes to the whole federation at once.
//
// Failures surface and heal rather than silently ending a namespace's events
// for the life of the client stream: an Info or Subscribe failure is logged
// with the uuid, and watchPlugin re-dials Info while fanInEvents re-dials the
// event stream, with backoff, while the client stream lives. A plugin that
// crashes and restarts resumes delivering events without the client
// reconnecting, and the client is told about the outage and the recovery
// through an EventPluginHealth instead of tiles quietly going stale.
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
		// The transport always watches, since it fans in every connection's
		// events, and there is no handshake to ask.
		go watchPlugin(subCtx, rt.srv.cfg.ID, true, t,
			func(context.Context) (*pb.InfoResponse, error) { return &pb.InfoResponse{Watch: true}, nil }, events)
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

// watchPlugin resolves whether plugin uuid supports live events, per
// Info.watch, and if so hands off to fanInEvents for the life of ctx. The Info
// fetch is retried with the same backoff fanInEvents uses for a dead stream:
// giving up after one failed Info would permanently exclude a plugin that was
// merely slow to start, for the life of the client stream. It emits the down
// and recovery EventPluginHealth transitions itself when the failure is at
// this Info stage; once Info succeeds and Watch is true, fanInEvents owns the
// health state for the rest of ctx's life, since a capability does not flip
// false again and only its stream can go down.
func watchPlugin(ctx context.Context, uuid string, transit bool, ns namespace.Namespace, infoOf func(context.Context) (*pb.InfoResponse, error), events chan<- *pb.Event) {
	backoff := time.Second
	healthy := true // assume healthy until the first failure
	for {
		info, err := infoOf(ctx)
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
		// Report recovery before the Watch check: a plugin that went down
		// during a transient Info failure and comes back as Watch:false, as
		// fs and proc do, having no event stream at all, must still clear its
		// down notice, or the client shows "live updates stopped" forever for
		// a plugin that never had live updates to begin with.
		if !healthy {
			healthy = true
			reportHealth(ctx, events, uuid, true, "")
		}
		if !info.Watch {
			return // this plugin emits no events: nothing to fan in, not a failure
		}
		fanInEvents(ctx, uuid, transit, ns, events) // owns health from here; returns only when ctx ends
		return
	}
}

// fanInEvents relays one namespace's Subscribe stream into events until ctx
// ends, re-dialing with exponential backoff after any stream failure so a
// plugin restart resumes its events. Failures are logged rather than
// swallowed, because events that silently stop present to the user as "tiles
// stopped updating" with no evidence, and are reported as an EventPluginHealth
// transition: down on the first failure, up on recovery, and not once per
// retry attempt, so a flapping plugin does not spam the client.
func fanInEvents(ctx context.Context, uuid string, transit bool, ns namespace.Namespace, events chan<- *pb.Event) {
	backoff := time.Second
	healthy := true // caller (watchPlugin) already reported recovery if it was ever down
	for {
		// namespace.Follow supplies the moment a callback stream has no open
		// to report: established — a first event, or a settle without failure
		// — is what "this namespace is back" means.
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
		// A stream that ends, with an error or cleanly, is an outage: the
		// namespace's events stopped arriving. It is never silent, or the
		// user sees "tiles stopped updating" with no evidence.
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

// reportHealth pushes an EventPluginHealth into the fan-in channel, the same
// path a namespace's own re-qualified events take. It is best-effort against
// ctx ending mid-send, since the client stream is closing in that case.
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

// isUnimplemented reports whether an error carries an Unimplemented code. It
// is how a namespace's "method not supported" becomes a silent no-op, as with
// SetFraming on a plugin that keeps no framing.
func isUnimplemented(err error) bool {
	if err == nil {
		return false
	}
	if st, ok := status.FromError(err); ok {
		return st.Code() == gcodes.Unimplemented
	}
	return false
}

// ── the node's own identity ──────────────────────────────────────────────────

// Info describes this node to a mounter: its identity, where a descent lands,
// and its capabilities. Watch is true because Subscribe fans in every
// namespace's events. It is served on the federation codec only; the browser
// learns all of this from Handshake, on its own door.
func (rt *router) Info(ctx context.Context, _ *pb.InfoRequest) (*pb.InfoResponse, error) {
	// A mount lands where a direct client lands: home, the first configured
	// entry with a root, by the same derivation as rpc.HomeGrid over the same
	// handshake. A node has no grid of its own.
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
		Kind:       "node",
		Watch:      true,
		Writable:   false,
		RootGridId: root,
	}, nil
}

// Probe routes by tile id to the owning namespace: presence is that
// namespace's verdict, never inferred from reachability.
func (rt *router) Probe(ctx context.Context, req *pb.ProbeRequest) (*pb.ProbeResponse, error) {
	c, local, ok := rt.srv.clientForID(req.TileId)
	if !ok {
		return nil, status.Errorf(gcodes.NotFound, "no plugin for %q", req.TileId)
	}
	return c.Probe(ctx, &pb.ProbeRequest{TileId: local})
}

// OpenShell attaches a tile's PTY through the one shell route, the same one
// the browser's /shell WebSocket enters by. See shell_door.go.
func (rt *router) OpenShell(ctx context.Context, recv func() (*pb.OpenShellRequest, error), send func(*pb.OpenShellResponse) error) error {
	first, err := recv()
	if err != nil {
		return err
	}
	return rt.srv.openShellRoute(ctx, first, recv, send)
}
