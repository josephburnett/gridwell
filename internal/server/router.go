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

// router is THE routing implementation, and it is itself a
// namespace.Namespace: a space of QUALIFIED ids whose verbs resolve one
// segment and forward to the namespace that owns the rest. It holds no
// Gridwell state — every request goes to the namespace that owns the id it
// carries (home, a content plugin, the transport), with the uuid prefix
// stripped on the way in and re-applied to the response on the way out.
//
// Two codecs stand over this ONE router and neither routes anything of its
// own: the browser's Connect door (connect_codec.go) and the federation
// socket's gRPC export (namespace.Server, nodeexport.go). Before
// docs/simplify-plan.md S2 the export delegated method-by-method to the
// Connect handler to keep them from drifting; now they are the same value.
//
// Subscribe fans in every watching namespace's event stream (Info.watch);
// ShellSessionAlive routes to the owner.
type router struct {
	srv *Server
}

// The router is a namespace of qualified ids; the compiler is what says so.
var _ namespace.Namespace = (*router)(nil)

func newRouter(srv *Server) *router { return &router{srv: srv} }

// ── routing ────────────────────────────────────────────────────────────────────

// route resolves the namespace that owns id (Server.resolve), returning
// it, the local id, the uuid to re-qualify with, and whether the namespace
// is transit. Ids are always qualified (there is no privileged namespace a
// bare id could fall back to).
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
// transit selects the node-mount tile rule (chain prepend, Reference trusted).
// The walk and the plain prepends live in internal/rpc (QualifyEventIDs) —
// shared with the ssh plugin's per-connection prepend — and only the tile
// rule is chosen here.
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
	// Grid.writable / scratch_grid_id are per-grid facts of the OWNING
	// plugin. A leaf plugin doesn't stamp them (its Info declares them once);
	// a transit plugin's response carries the remote node's stamp, ids
	// prepended one segment (rpc.TransitQualifyGrid — the one grid rule,
	// shared with the builtin remote transport's hop).
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
				}
				// The plugin's declared (+) menu additions (#258).
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
	// contentRoute: a leaf link's preview is its TARGET's preview, resolved at
	// the serving node (owner decision 8, 2026-07-26).
	c, local, err := rt.srv.contentRoute(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	return c.GetTilePreview(ctx, &pb.GetTilePreviewRequest{TileId: local})
}

// pluginInfoTimeout bounds each plugin's Info handshake during Handshake so
// one slow or hung plugin can't stall (or block) the whole launcher. On timeout
// the plugin is still listed from its config (kind + configured label), just
// without a clickable root — graceful degradation beats a frozen launcher.
const pluginInfoTimeout = 3 * time.Second

// Handshake enumerates the configured plugins in config order so the client
// can build the launcher / + menu. label comes from each plugin's Info, and so
// does writable (accepts new tiles) — a capability the handshake declares,
// never derived from the kind string.
func (rt *router) Handshake(ctx context.Context, req *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	// A namespaced request ROUTES (remote-menu, 2026-08-16): peel one
	// segment, forward the rest, re-qualify the answer with this hop —
	// the same shape as every routed read. "" stays the local handshake.
	if ns := req.GetNamespace(); ns != "" {
		hop, rest, ok := rpc.SplitID(ns)
		if !ok {
			hop, rest = ns, ""
		}
		c, found := rt.srv.routeClient(hop)
		if hop == rt.srv.cfg.ID && rest != "" {
			// "<ID>/<conn>/…": the transport answers for the connection.
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
		// The server.yaml display name is authoritative (the menu and a mounted
		// well must agree); buildPluginInfo falls back to Info's name then kind.
		label := rt.srv.pluginReg.Label(p.UUID)
		// Info is the whole handshake (default root grid + fallback label +
		// capabilities), served from the per-uuid cache after the first
		// success. Bounded, so a hung plugin degrades to a config-only entry
		// instead of hanging the launcher; a failed Info → info stays nil, and
		// the error rides along so buildPluginInfo can distinguish "broken"
		// from "healthy but rootless" — previously dropped on the floor here,
		// which made both cases identical on the wire (issue #47).
		info, err := rt.srv.pluginInfo(ctx, p.UUID)
		out = append(out, buildPluginInfo(p.UUID, p.Kind, label, info, err))
	}
	// HOME is a field: the node's own store, where "/" lands (its row is
	// first in out — the registry registers it first — but a client never
	// has to know that).
	resp := &pb.HandshakeResponse{
		Plugins:        out,
		ShellsDisabled: rt.srv.cfg.DisableShells,
		// The /content/ door's path capability, handed out only here — on
		// the cookie-authenticated mux — so only a logged-in client learns
		// it (content_door.go).
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
		// ("<ID>/<conn>"), from the transport's declared set.
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
// localdb reached through the ssh proxy has local kind "remote" but is every bit
// as writable — the proxy forwards its Info verbatim, so the capability
// travels while a kind check would strand it read-only.
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
		// Qualified scratch grid id (ephemeral-url target); empty for plugins
		// that don't support ephemeral visits.
		if info.ScratchGridId != "" {
			scratchGridID = rpc.QualifyID(uuid, info.ScratchGridId)
		}
		if label == "" {
			label = info.DisplayName
		}
		writable = info.Writable
		glyph = info.Glyph
		menuEntries = rpc.QualifyMenuEntries(uuid, info.MenuEntries)
		// Root-view viewport forwarded verbatim from Info (localdb fills it;
		// fs/proc return zero). The client seeds enterPlugin framing from this.
		rootViewCx = info.RootViewCx
		rootViewCy = info.RootViewCy
		rootViewZoom = info.RootViewZoom
	} else if infoErr != nil {
		infoError = "plugin not responding: " + infoErr.Error()
	}
	// An error alongside a live Info (the instance-grid read failed after a
	// healthy handshake) still rides the row: without it "store down" and
	// "healthy but rootless" are indistinguishable on the wire (issue #47's
	// class, one read further in).
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

// Search is the one generic find verb (issue #244). scope (any qualified
// id) routes to the namespace owning it — localized like every routed
// call; an empty scope fans out to every configured plugin in config
// order, each bounded by rpc.SearchHopTimeout, Unimplemented and errors
// skipped (a search answers with what answered). Results come back
// qualified like every other id — tile and path rows alike — so a hit is
// immediately addressable.
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
			continue // Unimplemented, timeout, a dead plugin: not this one's answer
		}
		out.Results = append(out.Results, qualifySearch(false, p.UUID, resp).Results...)
	}
	// The connections: the transport fans out to every remote, answering
	// in chains this node re-qualifies under its own id.
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

// localizeSearchQuery strips this plugin's uuid off an `id:` selector the
// way every routed id is localized; other queries pass through verbatim.
// One place, via the one grammar parser — a plugin must never see a
// foreign-qualified id it would fail to parse.
func localizeSearchQuery(query, uuid string) string {
	q := rpc.ParseSearchQuery(query)
	if q.ID == "" {
		return query
	}
	return "id:" + stripUUID(q.ID, uuid)
}

// qualifySearch re-applies the owning namespace to every id in a search
// response, with the same leaf/transit rule as every other read. The
// walk is rpc.QualifySearchResponse — shared with the builtin
// transport's per-connection prepend.
func qualifySearch(transit bool, uuid string, resp *pb.SearchResponse) *pb.SearchResponse {
	return rpc.QualifySearchResponse(resp, func(ts []*pb.Tile) []*pb.Tile {
		return qualifyTilesFor(transit, uuid, ts)
	})
}

// ── creates ──────────────────────────────────────────────────────────────────

// CreateTile is the single create router: resolve the owning plugin by
// destination grid, localize grid_id + path, forward. tile.kind selects the
// primitive; tile.child_grid_id (an exit well's cross-plugin reference) stays
// qualified.
func (rt *router) CreateTile(ctx context.Context, req *pb.CreateTileRequest) (*pb.TileResponse, error) {
	m := req
	// The node-wide shell refusal (server.yaml disable_shells) lives at the
	// router, BEFORE namespace resolution, so nothing — home, a plugin, a
	// connection — can serve one. The palette hides the swatch; this is
	// the authority.
	if rt.srv.cfg.DisableShells && m.Tile.GetKind() == rpc.KindShell {
		return nil, status.Error(gcodes.PermissionDenied,
			"shell tiles are disabled on this node (server.yaml disable_shells)")
	}
	c, local, uuid, transit, err := rt.route(m.GridId)
	if err != nil {
		return nil, err
	}
	m.GridId = local
	resp, err := c.CreateTile(ctx, m)
	return rt.tileResp(uuid, transit, resp, err)
}

// ── mutations ──────────────────────────────────────────────────────────────────

// CloneTile clones within a plugin, or — when the destination grid belongs to
// a DIFFERENT plugin — applies the cross-plugin clone contract (owner decision
// 2026-07-19: right-drag = COPY everywhere, left-drag across a boundary =
// LINK): a leaf copies its bytes into the destination plugin; a solid
// well deep-copies (deepcopy.go, issue #200),
// degrading to a LINK when the source is unreachable (the offline-plan
// decision, 2026-08-14). The LINK gesture is the left-drag, which arrives
// here as a plain CreateTile carrying a qualified child_grid_id or
// link_target_id — never as a clone. The source plugin is never asked to
// write into a grid it doesn't own.
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
	// "<srcUUID>/<grid>", an exit well's already-qualified target stays put —
	// either way the link target below is exactly what the client would see.
	st := qualifyTilesFor(srcTransit, srcUUID, []*pb.Tile{resp.GetTile()})[0]
	// No version claim: a clone is LAYOUT (the source row is untouched), and
	// version means the user's content bytes and nothing else
	// (docs/simplify-plan.md S5). The old hand-rolled check here re-derived
	// the local store's claim across the seam; both are gone.

	create := &pb.CreateTileRequest{
		Tile: &pb.Tile{Kind: st.Kind, X: m.X, Y: m.Y, W: st.W, H: st.H,
			AltText: st.AltText},
	}
	var copyBody []byte
	switch {
	case st.Kind == "well" && st.Reference:
		// The source is itself a LINK (an exit well — a mount, a node-grid
		// plugin tile, an earlier cross-plugin link). Cloning a link copies
		// the LINK: same shared child grid, same framing — exactly what a
		// within-plugin clone of an exit well does. This is also the mount
		// gesture's path (right-drag a node-grid plugin tile).
		create.Tile.ChildGridId = st.ChildGridId
		create.Tile.ViewCx = st.ViewCx
		create.Tile.ViewCy = st.ViewCy
		create.Tile.ViewZoom = st.ViewZoom
	case st.Kind == "well":
		// A SOLID well's cross-plugin CLONE is the deep copy (issue #200,
		// unblocked by the content streams): walk the source subtree and
		// materialize it in the destination — top-down by necessity (a
		// child grid is allocated by its well's create), so the copy
		// appears and fills in; a mid-walk failure leaves a visible,
		// deletable partial with the error surfaced. Right-drag = COPY
		// everywhere, at last (owner decision 2026-07-19, completed).
		dst, dstLocal, dstUUID, dstTransit, err := rt.route(m.DestGridId)
		if err != nil {
			return nil, err
		}
		srcLocalTile := resp.GetTile()
		// A SOURCE-BACKED subtree (fs directory, proc table) is refused
		// BEFORE anything is created: its "content" is host metadata stubs
		// — the copy would be a forest of summaries, not the files, and a
		// gesture that returns something other than what it names is the
		// silent-divergence class. Copying host files is its own feature.
		if sg, gerr := src.GetGrid(ctx, &pb.GetGridRequest{GridId: srcLocalTile.ChildGridId}); gerr == nil && sg.GetGrid().GetSourceKind() != "" {
			return nil, status.Errorf(gcodes.Unimplemented,
				"deep copy of a %s-backed well is not implemented (the copy would be metadata stubs, not the %s content); left-drag creates a link",
				sg.GetGrid().GetSourceKind(), sg.GetGrid().GetSourceKind())
		}
		out, err := rt.deepCopyWell(ctx, src, srcTransit, srcUUID,
			srcLocalTile, dst, dstLocal, m.X, m.Y)
		if err != nil {
			if out != nil {
				// The partial is real and visible; say what stopped the walk.
				return nil, status.Errorf(gcodes.Aborted,
					"deep copy incomplete (the partial copy remains, delete it if unwanted): %v", err)
			}
			if gwerr.IsTransport(err) {
				// The whole room is dark: degrade the TOP-LEVEL well to a
				// link (offline-plan decision 2026-08-14) — st.ChildGridId
				// is already the qualified target, so this is exactly the
				// exit well a left-drag would have made, framing included.
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
		// A leaf LINK clones as another link to the same target (the tile
		// being copied is a reference; copying it copies the reference).
		create.Tile.LinkTargetId = st.LinkTargetId
	case st.Kind == "text":
		// The body bytes follow the create as a WriteContent (below) — one
		// way to write bytes, even inside the router. An UNREACHABLE source
		// degrades the copy to a link to the original (offline-plan
		// decision, 2026-08-14) — same rule as inside a deep walk.
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
		// A shell's PTY session is plugin-local; the copy is a fresh shell
		// tile carrying the same label.
	case st.Kind == "pane":
		// A workspace clones as a byte copy of its layout blob (like text).
		// The layout's ids are owner-frame-relative — the copy's panes keep
		// naming the ORIGINAL places (a pane tile is arrangement of
		// references, not content), which is exactly the cross-plugin link
		// semantics, carried in bytes instead of a child_grid_id. A
		// never-arranged pane tile (no blob) copies with no body.
		if st.BlobId != 0 {
			if copyBody, err = readAllContent(ctx, src, srcLocal); err != nil {
				if gwerr.IsTransport(err) {
					// Degrade to a link, like text above.
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
	out, err := dst.CreateTile(ctx, create)
	if err != nil {
		return rt.tileResp(dstUUID, dstTransit, out, err)
	}
	if copyBody != nil {
		// The copied bytes follow the create through the one content door.
		// Not atomic with the create: a failure here leaves an empty copy
		// (visible, deletable) and surfaces — never a silent half-state.
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
// stream (commit-at-close, like every content write).
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

// SetTile is the single framing/preview writeback router. The owning plugin
// dispatches on the target tile's kind to the one operation that kind supports
// (well/text framing → no version bump; url/shell preview → bump).
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
	// A pane tile's layout blob is the ONLY record of the workspace's
	// ephemeral leaves (scratch-grid tiles). Deleting the workspace deletes
	// only the arrangement — but must terminate what the arrangement OWNS,
	// exactly like closing a pane does (issue #174). The plugin alone
	// decides whether this delete DESTROYS or merely parks the tile in its
	// trash (#262) — so capture the candidates BEFORE the delete (the blob
	// dies with the row) and reap AFTER, only if the row is actually gone;
	// a trashed workspace keeps its ephemerals so a restore comes back
	// whole. Transit tiles skip this hop — the forwarded delete reaches
	// the owning node's router, whose blob ids are in that node's frame.
	var candidates []string
	if !transit {
		candidates = rt.workspaceEphemeralCandidates(ctx, c, local, qualifiedID)
	}
	m.TileId = local
	// The owning namespace reaps the tile's shell session (if any) as part
	// of DeleteTile — the PTY lives behind the interface now.
	if _, err := c.DeleteTile(ctx, m); err != nil {
		return nil, err
	}
	if len(candidates) > 0 {
		// Reap only on an EXPLICIT NotFound: "unreadable right now"
		// (Unavailable, a deadline) is not "destroyed", and the reap is
		// unrecoverable while the trash may be keeping the workspace
		// for a restore. A missed reap is reclaimed by the boot sweep;
		// a wrong reap is not reclaimed by anything.
		if _, err := c.GetTile(ctx, &pb.GetTileRequest{TileId: local}); status.Code(err) == gcodes.NotFound {
			rt.reapWorkspaceEphemerals(ctx, candidates, qualifiedID)
		}
	}
	return &pb.DeleteTileResponse{}, nil
}

// workspaceEphemeralCandidates reads a pane tile's layout blob and returns
// the leaf ids that MIGHT be workspace-owned ephemerals — captured before
// the delete because the blob dies with the row. Nil for anything that is
// not a readable pane layout; an unreadable blob (corrupt, or written by a
// newer Gridwell) yields NOTHING — never guess — and the boot sweep
// reclaims the leftovers once the pane tile is gone.
func (rt *router) workspaceEphemeralCandidates(ctx context.Context, owner namespace.Namespace, localID, qualifiedID string) []string {
	tr, err := owner.GetTile(ctx, &pb.GetTileRequest{TileId: localID})
	if err != nil || tr.GetTile() == nil || tr.GetTile().Kind != rpc.KindPane || tr.GetTile().BlobId == 0 {
		return nil
	}
	body, err := readAllContent(ctx, owner, localID)
	if err != nil || len(body) == 0 {
		return nil
	}
	// Blob ids are in THIS node's frame (the encoder strips the reader's
	// transit prefix, which is empty for a locally-owned pane tile).
	tree, err := pane.DecodeLayout(body, func(id string) string { return id }, "")
	if err != nil {
		log.Printf("gridwell: delete %s: layout blob unreadable, reaping nothing: %v", qualifiedID, err)
		return nil
	}
	return pane.LeafTextFocusIDs(tree)
}

// reapWorkspaceEphemerals deletes the SCRATCH-grid tiles among a destroyed
// pane tile's captured layout leaves (issue #174) — the workspace's
// ephemeral shells/urls, whose tmux sessions die through the existing
// DeleteTile chain. Referenced non-scratch tiles are content the workspace
// merely viewed and are never touched. Best-effort: a failure here must
// not block the user's delete, so errors go to the log.
func (rt *router) reapWorkspaceEphemerals(ctx context.Context, candidates []string, qualifiedID string) {
	for _, id := range candidates {
		ec, elocal, euuid, transit, err := rt.route(id)
		if err != nil {
			continue
		}
		if transit {
			// A remote node's ephemeral: this node can't see the remote's
			// scratch-grid fact through the raw transit client. v1 limit —
			// the remote's boot sweep reclaims it.
			log.Printf("gridwell: delete %s: not reaping remote ephemeral candidate %s (transit)", qualifiedID, id)
			continue
		}
		// "Is this tile ephemeral" = its grid IS the owning plugin's scratch
		// grid — the same fact the server's GetGrid stamps from Info (the raw
		// plugin response doesn't carry it).
		info, err := rt.srv.pluginInfo(ctx, euuid)
		if err != nil || info.ScratchGridId == "" {
			continue
		}
		et, err := ec.GetTile(ctx, &pb.GetTileRequest{TileId: elocal})
		if err != nil || et.GetTile() == nil || et.GetTile().GridId != info.ScratchGridId {
			continue // not an ephemeral — viewed content, never touched
		}
		if _, err := ec.DeleteTile(ctx, &pb.DeleteTileRequest{TileId: elocal}); err != nil {
			log.Printf("gridwell: delete %s: reaping ephemeral %s failed: %v", qualifiedID, id, err)
		}
	}
}

// SetFraming persists a grid's framing — the ONE framing write, routed on
// whichever target the request names: the DOORWAY tile a grid was entered
// through, or the ROOT grid itself when there is no doorway. A plugin that
// keeps no framing answers Unimplemented, which is not an error here (a
// read-only plugin's ascent must not surface one to the user).
//
// After a ROOT write the per-plugin Info cache is invalidated: root_view_*
// travel in the Info handshake and now differ from the cached values, so
// the next Handshake (a page refresh) must re-fetch Info to see the
// viewport the user just left.
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

// ShellSessionAlive routes the wasm's per-descent probe to the owning plugin,
// which holds the PTY and answers whether the tile's session is alive. An infra
// error is reported as not-alive rather than a Connect error — the wasm only
// cares whether the refresh button should hide.
func (rt *router) ShellSessionAlive(ctx context.Context, req *pb.ShellSessionAliveRequest) (*pb.ShellSessionAliveResponse, error) {
	// disable_shells: every session is unreachable by design, so answer
	// "gone" — the wasm hides the refresh/reconnect affordance exactly as it
	// does for a dead tmux session.
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
		// The transport always watches (it fans in every connection's
		// events); there is no handshake to ask.
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
		fanInEvents(ctx, uuid, transit, ns, events) // owns health from here; returns only when ctx ends
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
func fanInEvents(ctx context.Context, uuid string, transit bool, ns namespace.Namespace, events chan<- *pb.Event) {
	backoff := time.Second
	healthy := true // caller (watchPlugin) already reported recovery if it was ever down
	for {
		// namespace.Follow supplies the moment a callback stream has no
		// open to report: ESTABLISHED (a first event, or a settle without
		// failure) is what "this namespace is back" means now.
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
		// A stream that ENDS — with an error or cleanly — is an outage: the
		// namespace's events stopped arriving. Never silent; the user
		// otherwise sees "tiles stopped updating" with no evidence.
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

// isUnimplemented reports whether a gRPC/Connect error carries an Unimplemented
// code. Used to treat a plugin's "method not supported" as a silent no-op
// (e.g. SetFraming on a plugin that keeps no framing).
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

// Info describes THIS NODE to a mounter: its identity, where a descent
// lands, and its capabilities. Watch is true because Subscribe fans in
// every namespace's events. Served on the federation codec only — the
// browser learns all of this from Handshake, on its own door.
func (rt *router) Info(ctx context.Context, _ *pb.InfoRequest) (*pb.InfoResponse, error) {
	// A mount lands where a direct client lands: HOME — the first
	// configured entry with a root (the same derivation as rpc.HomeGrid,
	// over the same handshake). A node has no grid of its own.
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

// Probe routes by tile id to the owning namespace — presence is that
// namespace's verdict, never inferred from reachability.
func (rt *router) Probe(ctx context.Context, req *pb.ProbeRequest) (*pb.ProbeResponse, error) {
	c, local, ok := rt.srv.clientForID(req.TileId)
	if !ok {
		return nil, status.Errorf(gcodes.NotFound, "no plugin for %q", req.TileId)
	}
	return c.Probe(ctx, &pb.ProbeRequest{TileId: local})
}

// OpenShell attaches a tile's PTY through the ONE shell route — the same
// one the browser's /shell WebSocket enters by (shell_door.go).
func (rt *router) OpenShell(ctx context.Context, recv func() (*pb.OpenShellRequest, error), send func(*pb.OpenShellResponse) error) error {
	first, err := recv()
	if err != nil {
		return err
	}
	return rt.srv.openShellRoute(ctx, first, recv, send)
}
