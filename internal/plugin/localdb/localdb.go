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
		Kind:          "localdb",
		DisplayName:   "local",
		SchemaVersion: int64(p.st.SchemaVersion()),
		RootGridId:    id,
		ScratchGridId: scratch,
		HasSession:    true,
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

// GetTileContent returns a text tile's stored body bytes (the markdown source).
func (p *Plugin) GetTileContent(ctx context.Context, req *gridwellv1.GetTileContentRequest) (*gridwellv1.GetTileContentResponse, error) {
	tile, err := p.st.GetTile(ctx, req.TileId)
	if err != nil {
		return nil, errToStatus(err)
	}
	if tile.BlobID == 0 {
		return &gridwellv1.GetTileContentResponse{}, nil
	}
	data, mediaType, err := p.st.GetBlobWithMedia(ctx, tile.BlobID)
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.GetTileContentResponse{Data: data, MediaType: mediaType}, nil
}

// GetTile reads a single tile's metadata.
func (p *Plugin) GetTile(ctx context.Context, req *gridwellv1.GetTileRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.GetTile(ctx, req.TileId))
}

// SetTileAlt stamps a tile's display label and returns the updated tile. The
// wire RPC is the USER rename gesture (issue #61): it latches alt_user so the
// automatic captures (url title, shell command) defer from then on. Text
// tiles are refused — their alt derives from the first line (UpdateText owns
// it), so a rename would be silently clobbered on the next edit.
func (p *Plugin) SetTileAlt(ctx context.Context, req *gridwellv1.SetTileAltRequest) (*gridwellv1.TileResponse, error) {
	id, err := strconv.ParseInt(req.TileId, 10, 64)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tile_id")
	}
	if t, err := p.st.GetTile(ctx, req.TileId); err == nil && t.Kind == rpc.KindText {
		return nil, status.Error(codes.InvalidArgument,
			"a text tile's name derives from its first line; rename the content instead")
	}
	if err := p.st.SetTileAlt(ctx, id, req.Alt, true); err != nil {
		return nil, errToStatus(err)
	}
	return tileResp(p.st.GetTile(ctx, req.TileId))
}

// SetPaneLayout writes a pane tile's serialized workspace layout — framing,
// never a version bump (the whole layout is arrangement; owner decision
// 2026-07-08). Path-free by id, like SetTileAlt.
func (p *Plugin) SetPaneLayout(ctx context.Context, req *gridwellv1.SetPaneLayoutRequest) (*gridwellv1.TileResponse, error) {
	id, err := strconv.ParseInt(req.TileId, 10, 64)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tile_id")
	}
	return tileResp(p.st.SetPaneLayout(ctx, id, req.Version, req.Data))
}

// SetContentZoom persists the per-tile content scale (issue #82) — framing,
// never a version bump.
func (p *Plugin) SetContentZoom(ctx context.Context, req *gridwellv1.SetContentZoomRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.SetContentZoom(ctx, &rpc.SetContentZoomRequest{
		TileID: req.TileId, Version: req.Version, ContentZoom: req.ContentZoom,
	}))
}

// ── Creates ──────────────────────────────────────────────────────────────────

// CreateTile is the single create: tile.kind selects which typed store create
// to run. The wire carries one create; localdb fans it back out here.
func (p *Plugin) CreateTile(ctx context.Context, req *gridwellv1.CreateTileRequest) (*gridwellv1.TileResponse, error) {
	t := req.Tile
	if t == nil {
		return nil, status.Error(codes.InvalidArgument, "create: nil tile")
	}
	path := rpc.PathFromProto(req.Path)
	switch t.Kind {
	case rpc.KindWell:
		// child_grid_id set → an exit well pointing at a grid owned by another
		// plugin (a mounted DB, an fs/proc grid). No interior child grid is
		// allocated; the cross-plugin reference is stored verbatim. alt_text is
		// the exit well's label. On an interior well, alt_text is the
		// user-given grid name (the + palette's name field); empty = unnamed.
		if t.ChildGridId != "" {
			return tileResp(p.st.CreateExitWell(ctx, path, req.GridId, t.X, t.Y, t.W, t.H,
				t.ChildGridId, t.AltText, t.ViewX, t.ViewY, t.ViewZoom))
		}
		return tileResp(p.st.CreateWell(ctx, &rpc.CreateWellRequest{Path: path, GridID: req.GridId, X: t.X, Y: t.Y, W: t.W, H: t.H, Label: t.AltText}))
	case rpc.KindText:
		return tileResp(p.st.CreateText(ctx, &rpc.CreateTextRequest{Path: path, GridID: req.GridId, X: t.X, Y: t.Y, W: t.W, H: t.H, Data: req.Data}))
	case rpc.KindURL:
		// A url create targeting this plugin's scratch grid is an EPHEMERAL
		// visit ("descend into a url") — route it path-free (the off-grid
		// scratch grid has no descent path). Any other grid is a normal placed
		// url tile.
		if scratch, err := p.st.ScratchGridID(ctx); err == nil && req.GridId == scratch {
			return tileResp(p.st.CreateScratchURL(ctx, t.UrlString))
		}
		return tileResp(p.st.CreateURL(ctx, &rpc.CreateURLRequest{Path: path, GridID: req.GridId, X: t.X, Y: t.Y, W: t.W, H: t.H, URL: t.UrlString}))
	case rpc.KindShell:
		// A shell create targeting the scratch grid is an EPHEMERAL shell
		// (clicked, not dragged, from the + palette): off-grid, path-free,
		// deleted on ascent (issue #85). Mirrors the url routing above.
		if scratch, err := p.st.ScratchGridID(ctx); err == nil && req.GridId == scratch {
			return tileResp(p.st.CreateScratchShell(ctx))
		}
		return tileResp(p.st.CreateShell(ctx, &rpc.CreateShellRequest{Path: path, GridID: req.GridId, X: t.X, Y: t.Y, W: t.W, H: t.H}))
	case rpc.KindPane:
		// A durable workspace. req.Data is the optional initial layout blob
		// (usually empty: NULL blob_id = never arranged); alt_text is the
		// workspace name the bottom bar shows.
		return tileResp(p.st.CreatePane(ctx, path, req.GridId, t.X, t.Y, t.W, t.H, t.AltText, req.Data))
	default:
		return nil, status.Errorf(codes.InvalidArgument, "create: unknown kind %q", t.Kind)
	}
}

// ── Mutations ────────────────────────────────────────────────────────────────

func (p *Plugin) MoveTile(ctx context.Context, req *gridwellv1.MoveTileRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.MoveTile(ctx, rpc.MoveTileFromProto(req)))
}

func (p *Plugin) CloneTile(ctx context.Context, req *gridwellv1.CloneTileRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.CloneTile(ctx, rpc.CloneTileFromProto(req)))
}

func (p *Plugin) ResizeTile(ctx context.Context, req *gridwellv1.ResizeTileRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.ResizeTile(ctx, rpc.ResizeTileFromProto(req)))
}

// SetTile is the single framing/preview writeback: tile.kind selects the one
// store operation that kind supports, and that mapping fixes the version
// semantics — well/text framing never bumps version, url/shell preview does.
func (p *Plugin) SetTile(ctx context.Context, req *gridwellv1.SetTileRequest) (*gridwellv1.TileResponse, error) {
	t := req.Tile
	if t == nil {
		return nil, status.Error(codes.InvalidArgument, "set: nil tile")
	}
	path := rpc.PathFromProto(req.Path)
	switch t.Kind {
	case rpc.KindWell:
		return tileResp(p.st.SetWellView(ctx, &rpc.SetWellViewRequest{Path: path, TileID: req.TileId, Version: req.Version, ViewX: t.ViewX, ViewY: t.ViewY, ViewZoom: t.ViewZoom}))
	case rpc.KindText:
		return tileResp(p.st.SetTextView(ctx, &rpc.SetTextViewRequest{Path: path, TileID: req.TileId, Version: req.Version, TextX: t.TextX, TextY: t.TextY, TextW: t.TextW, TextH: t.TextH, TextMode: t.TextMode}))
	case rpc.KindShell:
		return tileResp(p.st.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{Path: path, TileID: req.TileId, Version: req.Version, JPEG: req.Preview}))
	case rpc.KindURL:
		return tileResp(p.st.SetURLState(ctx, &rpc.SetURLStateRequest{Path: path, TileID: req.TileId, Version: req.Version, JPEG: req.Preview, URL: t.UrlString, Title: t.AltText, History: t.UrlHistory}))
	case rpc.KindPane:
		// Refused so the kind→operation mapping stays total: the layout blob
		// rides its own verb (SetPaneLayout — path-free, framing-class).
		return nil, status.Error(codes.InvalidArgument, "set: pane layout rides SetPaneLayout")
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

// sessionChunkSize bounds each streamed session fragment.
const sessionChunkSize = 64 * 1024

// GetSession streams this DB's Chromium session blob down (cookies + web
// storage). The plugin is the session boundary: one session per DB, shared by
// all its url tiles. Empty when none has been captured yet.
func (p *Plugin) GetSession(_ *gridwellv1.GetSessionRequest, stream grpc.ServerStreamingServer[gridwellv1.BlobChunk]) error {
	data, err := p.st.GetSession(stream.Context())
	if err != nil {
		return errToStatus(err)
	}
	for off := 0; off < len(data); off += sessionChunkSize {
		end := off + sessionChunkSize
		if end > len(data) {
			end = len(data)
		}
		if err := stream.Send(&gridwellv1.BlobChunk{Data: data[off:end]}); err != nil {
			return err
		}
	}
	return nil
}

// PutSession stores a session blob streamed up (checkout/checkin, last-writer-
// wins). The first message's root_grid_id binds the stream; for localdb the
// session is the DB's singleton, so it is advisory.
func (p *Plugin) PutSession(stream grpc.ClientStreamingServer[gridwellv1.PutSessionRequest, gridwellv1.PutSessionResponse]) error {
	var buf []byte
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			if err := p.st.PutSession(stream.Context(), buf); err != nil {
				return errToStatus(err)
			}
			return stream.SendAndClose(&gridwellv1.PutSessionResponse{})
		}
		if err != nil {
			return err
		}
		buf = append(buf, msg.Data...)
	}
}

func (p *Plugin) UpdateText(ctx context.Context, req *gridwellv1.UpdateTextRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.UpdateText(ctx, rpc.UpdateTextFromProto(req)))
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
