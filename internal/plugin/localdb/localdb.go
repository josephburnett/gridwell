// Package localdb implements a Gridwell plugin that wraps the local SQLite
// store. It satisfies gridwellv1.GridwellServer by delegating every RPC to
// store.Store and translating between the proto wire types and the internal
// rpc.* types via the existing rpc.ConvXxx functions.
//
// This is the "main" plugin: it owns the full Gridwell space — wells, text,
// URL, and shell tiles. The fs and proc plugins project external state; this
// plugin owns everything the user creates inside Gridwell.
package localdb

import (
	"context"
	"errors"
	"io"
	"log"
	"strconv"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/shellsvc"
	"github.com/josephburnett/gridwell/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Plugin wraps store.Store as a gridwellv1.GridwellServer. It also owns the
// shell PTY lifecycle (shellsvc.Manager) so OpenShell — the live shell bytes —
// crosses the Gridwell gRPC interface like every other call.
type Plugin struct {
	gridwellv1.UnimplementedGridwellServer
	st    *store.Store
	shell *shellsvc.Manager // nil → this instance hosts no live shells
}

// New wraps an open store. shell may be nil (no live-shell host: ShellSessionAlive
// returns false, OpenShell is unimplemented, and DeleteTile skips reaping).
func New(st *store.Store, shell *shellsvc.Manager) *Plugin {
	return &Plugin{st: st, shell: shell}
}

// CleanupOrphanedShells kills tmux sessions whose tile rows no longer exist —
// the bounded leak from a delete that raced a crash. Called once at plugin
// startup. No-op if this instance hosts no shells.
func (p *Plugin) CleanupOrphanedShells(ctx context.Context) (int, error) {
	if p.shell == nil {
		return 0, nil
	}
	return p.shell.CleanupOrphans(ctx, func(tileID string) (bool, error) {
		return p.st.ShellTileExists(ctx, tileID)
	})
}

// CleanupScratch deletes every UNOWNED tile in the scratch grid at startup.
// Scratch tiles are ephemeral by definition — gray means gone-on-ascent
// (issue #85); the client deletes them when the user ascends, and this sweep
// is the crash net (an ascent that never ran). The exception (issue #174):
// a scratch tile referenced by a pane tile's layout blob belongs to a
// WORKSPACE — a durable arrangement whose ephemerals live across app
// restarts on purpose (their tmux sessions survive them) — and is spared;
// the reference dies with the pane tile, so a later sweep reclaims it. If
// any pane blob is unreadable (corrupt / newer format) the sweep reaps
// NOTHING: a wrongly-killed workspace shell is unrecoverable, a delayed
// sweep is not. Runs before CleanupOrphanedShells so a swept shell's row is
// gone by the time the orphan sweep looks for session owners.
func (p *Plugin) CleanupScratch(ctx context.Context) (int, error) {
	scratch, err := p.st.ScratchGridID(ctx)
	if err != nil {
		return 0, err
	}
	refs, unreadable, err := p.st.WorkspaceEphemeralRefs(ctx)
	if err != nil {
		return 0, err
	}
	if unreadable {
		log.Printf("localdb: scratch sweep skipped: a pane layout blob is unreadable (never guess at workspace ownership)")
		return 0, nil
	}
	g, err := p.st.GetGrid(ctx, scratch)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range g.Tiles {
		if refs[t.ID] {
			continue // a workspace's ephemeral — owned, not leaked
		}
		if err := p.st.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: t.ID, Version: t.Version}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Store returns the underlying store (used by the server to wire shell
// session cleanup and orphan detection at startup).
func (p *Plugin) Store() *store.Store { return p.st }

// Close closes the underlying store.
func (p *Plugin) Close() error { return p.st.Close() }

// ── Lifecycle ────────────────────────────────────────────────────────────────

// Info is the whole handshake: identity plus the default root grid (localdb's
// singleton root) and the root viewport. No Attach/Detach — the gRPC
// connection is the lifecycle.
func (p *Plugin) Info(ctx context.Context, _ *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	id, err := p.st.RootGridID(ctx)
	if err != nil {
		return nil, errToStatus(err)
	}
	scratch, err := p.st.ScratchGridID(ctx)
	if err != nil {
		return nil, errToStatus(err)
	}
	// Root viewport: seed the client's enterPlugin framing so re-entry
	// restores the left-off view.  Zero on a fresh DB (never visited).
	cx, cy, zoom, err := p.st.RootView(ctx)
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.InfoResponse{
		Glyph:         "well",
		Kind:          "localdb",
		DisplayName:   "local",
		SchemaVersion: int64(p.st.SchemaVersion()),
		RootGridId:    id,
		ScratchGridId: scratch,
		// Capabilities the server reads from this handshake (never from the
		// kind string): localdb emits change events and accepts creates.
		Watch:        true,
		Writable:     true,
		RootViewCx:   cx,
		RootViewCy:   cy,
		RootViewZoom: zoom,
	}, nil
}

// SetRootView persists the plugin root-grid framing. Framing only — never
// bumps a content version; mirrors SetTile for a well but for the plugin root
// which has no tile row. Routed by the server via root_grid_id.
func (p *Plugin) SetRootView(ctx context.Context, req *gridwellv1.SetRootViewRequest) (*gridwellv1.SetRootViewResponse, error) {
	if err := p.st.SetRootView(ctx, &rpc.SetRootViewRequest{
		Cx:   req.Cx,
		Cy:   req.Cy,
		Zoom: req.Zoom,
	}); err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.SetRootViewResponse{}, nil
}

func (p *Plugin) Probe(ctx context.Context, req *gridwellv1.ProbeRequest) (*gridwellv1.ProbeResponse, error) {
	_, err := p.st.GetTile(ctx, req.TileId)
	if errors.Is(err, store.ErrNotFound) {
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	}
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_PRESENT}, nil
}

// ── Reads ────────────────────────────────────────────────────────────────────

func (p *Plugin) GetGrid(ctx context.Context, req *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	r, err := p.st.GetGrid(ctx, req.GridId)
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.GetGridResponse{
		Grid:  rpc.GridToProto(&r.Grid),
		Tiles: rpc.TilesToProto(r.Tiles),
	}, nil
}

func (p *Plugin) GetTilePreview(ctx context.Context, req *gridwellv1.GetTilePreviewRequest) (*gridwellv1.GetTilePreviewResponse, error) {
	jpeg, err := p.st.GetTilePreview(ctx, req.TileId)
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.GetTilePreviewResponse{Jpeg: jpeg}, nil
}

// GetTile reads a single tile's metadata.
func (p *Plugin) GetTile(ctx context.Context, req *gridwellv1.GetTileRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.GetTile(ctx, req.TileId))
}

// Search is the one generic find verb (issue #244): `id:` locates a tile
// by its immutable id (path included — the old LocateTile), free text
// matches names and text bodies. The store owns the semantics.
func (p *Plugin) Search(ctx context.Context, req *gridwellv1.SearchRequest) (*gridwellv1.SearchResponse, error) {
	res, err := p.st.Search(ctx, req.Query, int(req.Limit))
	if err != nil {
		return nil, errToStatus(err)
	}
	out := &gridwellv1.SearchResponse{}
	for i := range res {
		out.Results = append(out.Results, rpc.SearchResultToProto(&res[i]))
	}
	return out, nil
}

// contentChunkBytes is the ReadContent chunk size. Small enough to stream a
// large body without one giant message, large enough that a typical text tile
// is one chunk.
const contentChunkBytes = 256 * 1024

// ReadContent streams a tile's content bytes (2026-07-26 redesign). Chunk 1
// carries media_type and the row version the bytes belong to — the caller's
// save basis, paired with the bytes at the owner; later chunks carry data
// only. Empty content still sends the one meta chunk so the version always
// arrives.
func (p *Plugin) ReadContent(req *gridwellv1.ReadContentRequest, stream grpc.ServerStreamingServer[gridwellv1.ContentChunk]) error {
	data, mediaType, version, err := p.st.ReadContent(stream.Context(), req.TileId)
	if err != nil {
		return errToStatus(err)
	}
	first := &gridwellv1.ContentChunk{MediaType: mediaType, Version: version}
	if len(data) <= contentChunkBytes {
		first.Data = data
		return stream.Send(first)
	}
	first.Data = data[:contentChunkBytes]
	if err := stream.Send(first); err != nil {
		return err
	}
	for off := contentChunkBytes; off < len(data); off += contentChunkBytes {
		end := min(off+contentChunkBytes, len(data))
		if err := stream.Send(&gridwellv1.ContentChunk{Data: data[off:end]}); err != nil {
			return err
		}
	}
	return nil
}

// WriteContent assembles the client stream and commits ONCE, at clean close —
// nothing is written until SendAndClose time, so a broken stream leaves the
// old value byte-for-byte intact (commit-at-close; the store's WriteContent
// is the one transactional door and owns the kind-dispatched version
// semantics). The first message binds tile_id and claims the version;
// accumulation is capped at the store's blob limit so an oversized stream
// fails fast instead of buffering without bound.
func (p *Plugin) WriteContent(stream grpc.ClientStreamingServer[gridwellv1.WriteContentRequest, gridwellv1.TileResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		return status.Error(codes.InvalidArgument, "write: empty stream")
	}
	tileID, version := first.TileId, first.Version
	if tileID == "" {
		return status.Error(codes.InvalidArgument, "write: first message must bind tile_id")
	}
	data := append([]byte(nil), first.Data...)
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err // broken stream: no commit, old value intact
		}
		data = append(data, msg.Data...)
		if int64(len(data)) > store.MaxBlobBytes {
			return status.Error(codes.InvalidArgument, "write: content too large")
		}
	}
	tile, err := p.st.WriteContent(stream.Context(), tileID, version, data)
	if err != nil {
		return errToStatus(err)
	}
	return stream.SendAndClose(&gridwellv1.TileResponse{Tile: rpc.TileToProto(tile)})
}

// ── Creates ──────────────────────────────────────────────────────────────────

// CreateTile is the single create: tile.kind selects which typed store create
// to run. The wire carries one create; localdb fans it back out here.
func (p *Plugin) CreateTile(ctx context.Context, req *gridwellv1.CreateTileRequest) (*gridwellv1.TileResponse, error) {
	t := req.Tile
	if t == nil {
		return nil, status.Error(codes.InvalidArgument, "create: nil tile")
	}
	if t.LinkTargetId != "" {
		// A LEAF LINK: any leaf kind whose content lives in another plugin's
		// tile (the cross-plugin left-drag). One create for all four kinds;
		// the store validates the kind set and the qualified-target shape.
		// t.ObjectId carries provenance (the link names the same origin).
		return tileResp(p.st.CreateLeafLink(ctx, req.GridId, t.X, t.Y, t.W, t.H,
			t.Kind, t.LinkTargetId, t.AltText, t.ObjectId))
	}
	switch t.Kind {
	case rpc.KindWell:
		// child_grid_id set → an exit well pointing at a grid owned by another
		// plugin (a mounted DB, an fs/proc grid). No interior child grid is
		// allocated; the cross-plugin reference is stored verbatim. alt_text is
		// the exit well's label. On an interior well, alt_text is the
		// user-given grid name (the + palette's name field); empty = unnamed.
		// configure_plugin_id (childless) → an unconfigured plugin well
		// (issue #251), waiting for its instance picker.
		if t.ChildGridId != "" && t.ConfigurePluginId != "" {
			return nil, status.Error(codes.InvalidArgument,
				"create: a well is born configured (child_grid_id) or configurable (configure_plugin_id), not both")
		}
		if t.ChildGridId != "" {
			return tileResp(p.st.CreateExitWell(ctx, req.GridId, t.X, t.Y, t.W, t.H,
				t.ChildGridId, t.AltText, t.ViewX, t.ViewY, t.ViewZoom, t.ObjectId))
		}
		if t.ConfigurePluginId != "" {
			return tileResp(p.st.CreatePluginWell(ctx, req.GridId, t.X, t.Y, t.W, t.H, t.ConfigurePluginId))
		}
		return tileResp(p.st.CreateWell(ctx, &rpc.CreateWellRequest{GridID: req.GridId, X: t.X, Y: t.Y, W: t.W, H: t.H, Label: t.AltText, ObjectID: t.ObjectId}))
	case rpc.KindText:
		return tileResp(p.st.CreateText(ctx, &rpc.CreateTextRequest{GridID: req.GridId, X: t.X, Y: t.Y, W: t.W, H: t.H, ObjectID: t.ObjectId}))
	case rpc.KindURL:
		// A url create targeting this plugin's scratch grid is an EPHEMERAL
		// visit ("descend into a url") — route it path-free (the off-grid
		// scratch grid has no descent path). Any other grid is a normal placed
		// url tile.
		if scratch, err := p.st.ScratchGridID(ctx); err == nil && req.GridId == scratch {
			return tileResp(p.st.CreateScratchURL(ctx, t.UrlString))
		}
		return tileResp(p.st.CreateURL(ctx, &rpc.CreateURLRequest{GridID: req.GridId, X: t.X, Y: t.Y, W: t.W, H: t.H, URL: t.UrlString}))
	case rpc.KindShell:
		// A shell create targeting the scratch grid is an EPHEMERAL shell
		// (clicked, not dragged, from the + palette): off-grid, path-free,
		// deleted on ascent (issue #85). Mirrors the url routing above.
		if scratch, err := p.st.ScratchGridID(ctx); err == nil && req.GridId == scratch {
			return tileResp(p.st.CreateScratchShell(ctx))
		}
		return tileResp(p.st.CreateShell(ctx, &rpc.CreateShellRequest{GridID: req.GridId, X: t.X, Y: t.Y, W: t.W, H: t.H}))
	case rpc.KindPane:
		// A durable workspace, created with no layout blob (NULL blob_id =
		// never arranged; the first arrangement rides WriteContent); alt_text
		// is the workspace name the bottom bar shows.
		return tileResp(p.st.CreatePane(ctx, req.GridId, t.X, t.Y, t.W, t.H, t.AltText, nil, t.ObjectId))
	default:
		return nil, status.Errorf(codes.InvalidArgument, "create: unknown kind %q", t.Kind)
	}
}

// ── Mutations ────────────────────────────────────────────────────────────────

func (p *Plugin) CloneTile(ctx context.Context, req *gridwellv1.CloneTileRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.CloneTile(ctx, rpc.CloneTileFromProto(req)))
}

// PlaceTile is the single placement writeback (2026-07-26 redesign): one verb
// owns (grid, x, y, w, h); the store derives the well-into-own-subtree
// refusal itself.
func (p *Plugin) PlaceTile(ctx context.Context, req *gridwellv1.PlaceTileRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.PlaceTile(ctx, rpc.PlaceTileFromProto(req)))
}

// SetTile is the single framing/preview writeback: tile.kind selects the one
// store operation that kind supports, and that mapping fixes the version
// semantics — well/text framing never bumps version, url/shell preview does.
// 2026-07-26: it also carries the absorbed scalar operations — rename (the
// versioned user rename; latches alt_user), content_zoom (framing), and
// url_frozen (framing, issue #237) — exactly ONE operation per call,
// refused otherwise, so the empty-fields-skip rule never turns ambiguous.
func (p *Plugin) SetTile(ctx context.Context, req *gridwellv1.SetTileRequest) (*gridwellv1.TileResponse, error) {
	ops := 0
	if req.Rename != "" {
		ops++
	}
	if req.ContentZoom != nil {
		ops++
	}
	if req.UrlFrozen != nil {
		ops++
	}
	if req.AdoptChildGrid != nil {
		ops++
	}
	if req.Tile != nil {
		ops++
	}
	if ops > 1 {
		return nil, status.Error(codes.InvalidArgument,
			"set: one operation per call (rename, content_zoom, url_frozen, adopt_child_grid, or tile writeback)")
	}
	if req.Rename != "" {
		return tileResp(p.st.RenameTile(ctx, req.TileId, req.Version, req.Rename))
	}
	if a := req.AdoptChildGrid; a != nil {
		return tileResp(p.st.AdoptChildGrid(ctx, &rpc.AdoptChildGridRequest{
			TileID: req.TileId, Version: req.Version,
			ChildGridID: a.ChildGridId, Label: a.Label,
			ViewX: a.ViewX, ViewY: a.ViewY, ViewZoom: a.ViewZoom,
		}))
	}
	if req.ContentZoom != nil {
		return tileResp(p.st.SetContentZoom(ctx, &rpc.SetContentZoomRequest{
			TileID: req.TileId, Version: req.Version, ContentZoom: *req.ContentZoom,
		}))
	}
	if req.UrlFrozen != nil {
		return tileResp(p.st.SetURLFrozen(ctx, &rpc.SetURLFrozenRequest{
			TileID: req.TileId, Version: req.Version, Frozen: *req.UrlFrozen,
		}))
	}
	t := req.Tile
	if t == nil {
		return nil, status.Error(codes.InvalidArgument, "set: nil tile")
	}
	switch t.Kind {
	case rpc.KindWell:
		return tileResp(p.st.SetWellView(ctx, &rpc.SetWellViewRequest{TileID: req.TileId, Version: req.Version, ViewX: t.ViewX, ViewY: t.ViewY, ViewZoom: t.ViewZoom}))
	case rpc.KindText:
		return tileResp(p.st.SetTextView(ctx, &rpc.SetTextViewRequest{TileID: req.TileId, Version: req.Version, TextX: t.TextX, TextY: t.TextY, TextW: t.TextW, TextH: t.TextH, TextMode: t.TextMode}))
	case rpc.KindShell:
		return tileResp(p.st.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{TileID: req.TileId, Version: req.Version, JPEG: req.Preview}))
	case rpc.KindURL:
		return tileResp(p.st.SetURLState(ctx, &rpc.SetURLStateRequest{TileID: req.TileId, Version: req.Version, JPEG: req.Preview, URL: t.UrlString, Title: t.AltText, History: t.UrlHistory}))
	case rpc.KindPane:
		// Refused so the kind→operation mapping stays total: the layout blob
		// rides the content door (WriteContent — framing-class for layouts).
		return nil, status.Error(codes.InvalidArgument, "set: pane layout rides WriteContent")
	default:
		return nil, status.Errorf(codes.InvalidArgument, "set: unknown kind %q", t.Kind)
	}
}

func (p *Plugin) ShellSessionAlive(_ context.Context, req *gridwellv1.ShellSessionAliveRequest) (*gridwellv1.ShellSessionAliveResponse, error) {
	if p.shell == nil {
		return &gridwellv1.ShellSessionAliveResponse{Alive: false}, nil
	}
	alive, _ := p.shell.HasSession(req.TileId)
	return &gridwellv1.ShellSessionAliveResponse{Alive: alive}, nil
}

// OpenShell streams a tile's live PTY both ways: the first request binds the
// tile id (data empty), then keystrokes/resizes flow up and terminal output
// flows down. The plugin owns the tmux/PTY, so these bytes cross the Gridwell
// interface like everything else; the server only bridges a WebSocket to it.
func (p *Plugin) OpenShell(stream grpc.BidiStreamingServer[gridwellv1.OpenShellRequest, gridwellv1.OpenShellResponse]) error {
	if p.shell == nil {
		return status.Error(codes.Unimplemented, "this plugin hosts no live shells")
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	tileID := first.TileId
	if tileID == "" {
		return status.Error(codes.InvalidArgument, "OpenShell: first message must bind tile_id")
	}
	var cols, rows uint16
	if r := first.Resize; r != nil {
		cols, rows = uint16(r.Cols), uint16(r.Rows)
	}
	cols, rows = shellsvc.ClampSize(cols, rows)

	// A fresh tile (no frozen snapshot) may spawn a new bash; a snapshotted tile
	// must not — we won't fabricate state behind the JPEG.
	allowCreate := true
	if tile, gerr := p.st.GetTile(stream.Context(), tileID); gerr == nil {
		allowCreate = tile.PreviewBlobID == 0
	}

	session, stopOld, err := p.shell.Acquire(tileID, allowCreate, cols, rows)
	if err != nil {
		if errors.Is(err, shellsvc.ErrSessionGone) {
			return status.Error(codes.FailedPrecondition, err.Error())
		}
		return status.Error(codes.Internal, err.Error())
	}
	defer p.shell.Release(tileID, session, stopOld, func() { p.captureShellTitle(tileID) })

	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	// Reader: keystrokes / resizes up. Exits on stream EOF/error or a dead PTY,
	// cancelling the writer.
	go func() {
		defer cancel()
		for {
			msg, rerr := stream.Recv()
			if rerr != nil {
				return
			}
			if len(msg.Data) > 0 {
				if _, werr := session.Write(msg.Data); errors.Is(werr, io.ErrClosedPipe) {
					return
				}
			}
			if r := msg.Resize; r != nil {
				c, rw := shellsvc.ClampSize(uint16(r.Cols), uint16(r.Rows))
				_ = session.Resize(c, rw)
			}
		}
	}()

	// Writer: PTY output down. Exits on cancel (reader done / our ctx), session
	// death, or a takeover (stopOld), or a send error.
	out := session.Output()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-session.Done():
			return nil
		case <-stopOld:
			return nil
		case chunk, ok := <-out:
			if !ok {
				return nil
			}
			if serr := stream.Send(&gridwellv1.OpenShellResponse{Data: chunk}); serr != nil {
				return serr
			}
		}
	}
}

// captureShellTitle stamps the tile's label with its tmux session's foreground
// command on detach — the way URL tiles capture the page title. Best-effort.
func (p *Plugin) captureShellTitle(tileID string) {
	cmd, err := p.shell.PaneCommand(tileID)
	if err != nil || cmd == "" {
		return
	}
	id, err := strconv.ParseInt(tileID, 10, 64)
	if err != nil {
		return
	}
	_ = p.st.SetTileAlt(context.Background(), id, cmd, false)
}

func (p *Plugin) DeleteTile(ctx context.Context, req *gridwellv1.DeleteTileRequest) (*gridwellv1.DeleteTileResponse, error) {
	tileID := req.TileId
	if err := p.st.DeleteTile(ctx, rpc.DeleteTileFromProto(req)); err != nil {
		return nil, errToStatus(err)
	}
	// Reap the tile's shell session once its row is gone (a cloned shell is an
	// independent copy with its own id, so deleting a copy never touches the
	// original's PTY). Fire-and-forget; the startup orphan sweep is the net.
	if p.shell != nil {
		if exists, err := p.st.ShellTileExists(ctx, tileID); err == nil && !exists {
			_ = p.shell.Kill(tileID)
		}
	}
	return &gridwellv1.DeleteTileResponse{}, nil
}

// ── Subscribe (server-streaming) ─────────────────────────────────────────────

func (p *Plugin) Subscribe(_ *gridwellv1.SubscribeRequest, stream grpc.ServerStreamingServer[gridwellv1.Event]) error {
	ch, cancel := p.st.SubscribeEvents()
	defer cancel()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(rpc.EventToProto(ev)); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func tileResp(t *rpc.Tile, err error) (*gridwellv1.TileResponse, error) {
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.TileResponse{Tile: rpc.TileToProto(t)}, nil
}

// errToStatus maps a store sentinel error to a gRPC status code so the
// classification survives the routing hop to the server (which maps the code
// to a Connect status). The sentinel→class table is store.ClassifyError —
// the one owner, next to the sentinels — so this cannot drift from the
// server's mapping. An unclassified error passes through (grpc wraps it as
// codes.Unknown → CodeInternal).
func errToStatus(err error) error {
	if err == nil {
		return nil
	}
	switch store.ClassifyError(err) {
	case store.ClassNotFound:
		return status.Error(codes.NotFound, err.Error())
	case store.ClassInvalidArgument:
		return status.Error(codes.InvalidArgument, err.Error())
	case store.ClassConflict:
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return err
	}
}
