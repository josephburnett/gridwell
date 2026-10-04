// Package local is the node's home: the namespace over the local SQLite store,
// owning everything the user creates inside Gridwell.
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Plugin wraps store.Store as a namespace.Namespace and owns the shell PTY
// lifecycle.
type Plugin struct {
	namespace.Unimplemented
	st    *store.Store
	shell *shellsvc.Manager // nil means this instance hosts no live shells
}

var _ namespace.Namespace = (*Plugin)(nil)

// New wraps an open store. A nil shell hosts no live shells: OpenShell is
// unimplemented and DeleteTile skips reaping.
func New(st *store.Store, shell *shellsvc.Manager) *Plugin {
	return &Plugin{st: st, shell: shell}
}

// CleanupOrphanedShells kills tmux sessions no shell row names any more: the
// bounded leak from a delete that raced a crash. Called once at startup.
func (p *Plugin) CleanupOrphanedShells(ctx context.Context) (int, error) {
	if p.shell == nil {
		return 0, nil
	}
	return p.shell.CleanupOrphans(ctx, func(key string) (bool, error) {
		n, err := p.st.ShellSessionNamers(ctx, key)
		return n.Rows > 0, err
	})
}

// CleanupScratch deletes every unowned tile in the scratch grid at startup,
// sparing any a pane tile's layout blob references. If any pane blob is
// unreadable it reaps nothing: a wrongly-killed shell is unrecoverable and a
// delayed sweep is not. Runs before CleanupOrphanedShells.
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
		log.Printf("gridwell: home: scratch sweep skipped: a pane layout blob is unreadable (never guess at workspace ownership)")
		return 0, nil
	}
	g, err := p.st.GetGrid(ctx, scratch)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range g.Tiles {
		if refs[t.Id] {
			continue // a pane tile's ephemeral: owned, not leaked
		}
		if err := p.st.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: t.Id}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Close closes the underlying store.
func (p *Plugin) Close() error { return p.st.Close() }

// Info is the whole handshake: identity, the singleton root grid, and its
// viewport.
func (p *Plugin) Info(ctx context.Context, _ *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	id, err := p.st.RootGridID(ctx)
	if err != nil {
		return nil, errToStatus(err)
	}
	scratch, err := p.st.ScratchGridID(ctx)
	if err != nil {
		return nil, errToStatus(err)
	}
	trash, err := p.st.TrashGridID(ctx)
	if err != nil {
		return nil, errToStatus(err)
	}
	// Every declared doorway carries its grid's framing, root and trashcan
	// alike. A fresh DB's zero zoom reads as the calibrated default.
	view, _, err := p.st.RootFraming(ctx)
	if err != nil {
		return nil, errToStatus(err)
	}
	trashView, _, err := p.st.GridFraming(trash)
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.InfoResponse{
		Glyph:         rpc.GlyphWell,
		DisplayName:   "home",
		RootGridId:    id,
		ScratchGridId: scratch,
		MenuEntries: []*gridwellv1.MenuEntry{{
			Id:     "trash",
			Label:  "trash",
			Glyph:  rpc.GlyphTrash,
			GridId: trash,
			ViewCx: trashView.Cx, ViewCy: trashView.Cy, ViewZoom: trashView.Zoom,
		}},
		Writable:     true,
		RootViewCx:   view.Cx,
		RootViewCy:   view.Cy,
		RootViewZoom: view.Zoom,
	}, nil
}

// SetFraming persists a grid's framing on a doorway tile or, for a root, the
// grid row. It never bumps a content version.
func (p *Plugin) SetFraming(ctx context.Context, req *gridwellv1.SetFramingRequest) (*gridwellv1.SetFramingResponse, error) {
	t, err := p.st.SetFraming(ctx, req)
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.SetFramingResponse{Tile: t}, nil
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

func (p *Plugin) GetGrid(ctx context.Context, req *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	r, err := p.st.GetGrid(ctx, req.GridId)
	if err != nil {
		return nil, errToStatus(err)
	}
	return r, nil
}

func (p *Plugin) GetTilePreview(ctx context.Context, req *gridwellv1.GetTilePreviewRequest) (*gridwellv1.GetTilePreviewResponse, error) {
	jpeg, err := p.st.GetTilePreview(ctx, req.TileId)
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.GetTilePreviewResponse{Jpeg: jpeg}, nil
}

// GetTile reads a single tile's metadata. An id with no row is dead: home
// never reassigns an id.
func (p *Plugin) GetTile(ctx context.Context, req *gridwellv1.GetTileRequest) (*gridwellv1.TileResponse, error) {
	t, err := p.st.GetTile(ctx, req.TileId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, gwerr.DeadRef("", "home: no tile %q", req.TileId)
	}
	return tileResp(t, err)
}

// Search is the one generic find verb; the store owns the semantics.
func (p *Plugin) Search(ctx context.Context, req *gridwellv1.SearchRequest) (*gridwellv1.SearchResponse, error) {
	res, err := p.st.Search(ctx, req.Query, int(req.Limit))
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.SearchResponse{Results: res}, nil
}

// ReadContent streams a tile's content bytes. Chunk 1 carries media_type and
// the row version, the caller's save basis, even for empty content.
func (p *Plugin) ReadContent(ctx context.Context, req *gridwellv1.ReadContentRequest, send func(*gridwellv1.ContentChunk) error) error {
	data, mediaType, version, err := p.st.ReadContent(ctx, req.TileId)
	if err != nil {
		return errToStatus(err)
	}
	first := &gridwellv1.ContentChunk{MediaType: mediaType, Version: version}
	if len(data) <= rpc.ContentChunkBytes {
		first.Data = data
		return send(first)
	}
	first.Data = data[:rpc.ContentChunkBytes]
	if err := send(first); err != nil {
		return err
	}
	for off := rpc.ContentChunkBytes; off < len(data); off += rpc.ContentChunkBytes {
		end := min(off+rpc.ContentChunkBytes, len(data))
		if err := send(&gridwellv1.ContentChunk{Data: data[off:end]}); err != nil {
			return err
		}
	}
	return nil
}

// WriteContent assembles the client stream and commits once, at clean close,
// so a broken stream leaves the old value intact. The first message binds
// tile_id and claims the version; accumulation is capped at the blob limit.
func (p *Plugin) WriteContent(ctx context.Context, recv func() (*gridwellv1.WriteContentRequest, error)) (*gridwellv1.TileResponse, error) {
	first, err := recv()
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "write: empty stream")
	}
	tileID, version := first.TileId, first.Version
	if tileID == "" {
		return nil, status.Error(codes.InvalidArgument, "write: first message must bind tile_id")
	}
	data := append([]byte(nil), first.Data...)
	for {
		msg, err := recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err // broken stream: no commit, old value intact
		}
		data = append(data, msg.Data...)
		if int64(len(data)) > store.MaxBlobBytes {
			return nil, status.Error(codes.InvalidArgument, "write: content too large")
		}
	}
	tile, err := p.st.WriteContent(ctx, tileID, version, data)
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.TileResponse{Tile: tile}, nil
}

// CreateTile is the single create: tile.kind selects the typed store create.
func (p *Plugin) CreateTile(ctx context.Context, req *gridwellv1.CreateTileRequest) (*gridwellv1.TileResponse, error) {
	t := req.Tile
	if t == nil {
		return nil, status.Error(codes.InvalidArgument, "create: nil tile")
	}
	if t.LinkTargetId != "" {
		// A leaf link of any leaf kind; the store validates kind and target.
		return tileResp(p.st.CreateLeafLink(ctx, req.GridId, t.X, t.Y, t.W, t.H,
			t.Kind, t.LinkTargetId, t.AltText))
	}
	switch t.Kind {
	case rpc.KindWell:
		// child_grid_id set makes an exit well: the qualified reference is
		// stored verbatim, with no interior grid.
		if t.ChildGridId != "" {
			return tileResp(p.st.CreateExitWell(ctx, req.GridId, t.X, t.Y, t.W, t.H,
				t.ChildGridId, t.AltText,
				rpc.Framing{Cx: t.ViewCx, Cy: t.ViewCy, Zoom: t.ViewZoom}))
		}
		return tileResp(p.st.CreateWell(ctx, req.GridId, t.X, t.Y, t.W, t.H, t.AltText))
	case rpc.KindText:
		return tileResp(p.st.CreateText(ctx, req.GridId, t.X, t.Y, t.W, t.H, nil))
	case rpc.KindURL:
		// A url create on the scratch grid is an ephemeral visit, routed
		// path-free because the scratch grid has no descent path.
		if scratch, err := p.st.ScratchGridID(ctx); err == nil && req.GridId == scratch {
			return tileResp(p.st.CreateScratchURL(ctx, t.UrlString))
		}
		return tileResp(p.st.CreateURL(ctx, req.GridId, t.X, t.Y, t.W, t.H, t.UrlString))
	case rpc.KindShell:
		// Likewise an ephemeral shell, deleted on ascent.
		if scratch, err := p.st.ScratchGridID(ctx); err == nil && req.GridId == scratch {
			return tileResp(p.st.CreateScratchShell(ctx))
		}
		return tileResp(p.st.CreateShell(ctx, req.GridId, t.X, t.Y, t.W, t.H))
	case rpc.KindPane:
		// A NULL blob_id means never arranged; the first arrangement rides
		// WriteContent.
		return tileResp(p.st.CreatePane(ctx, req.GridId, t.X, t.Y, t.W, t.H, t.AltText, nil))
	default:
		return nil, status.Errorf(codes.InvalidArgument, "create: unknown kind %q", t.Kind)
	}
}

func (p *Plugin) CloneTile(ctx context.Context, req *gridwellv1.CloneTileRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.CloneTile(ctx, req))
}

// PlaceTile is the single placement writeback; the store derives the
// well-into-own-subtree refusal itself.
func (p *Plugin) PlaceTile(ctx context.Context, req *gridwellv1.PlaceTileRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.PlaceTile(ctx, req))
}

// SetTile is the single capture writeback: tile.kind selects the one store
// operation that kind supports. The scalar operations (rename, content_zoom,
// url_frozen) ride here exactly one per call, refused otherwise, so the
// empty-fields-skip rule never turns ambiguous.
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
	if req.Tile != nil {
		ops++
	}
	if ops > 1 {
		return nil, status.Error(codes.InvalidArgument,
			"set: one operation per call (rename, content_zoom, url_frozen, or tile writeback)")
	}
	if req.Rename != "" {
		return tileResp(p.st.RenameTile(ctx, req.TileId, req.Version, req.Rename))
	}
	if req.ContentZoom != nil {
		z, err := rpc.NewContentZoom(*req.ContentZoom)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		return tileResp(p.st.SetContentZoom(ctx, req.TileId, z))
	}
	if req.UrlFrozen != nil {
		return tileResp(p.st.SetFrozen(ctx, req.TileId, *req.UrlFrozen))
	}
	t := req.Tile
	if t == nil {
		return nil, status.Error(codes.InvalidArgument, "set: nil tile")
	}
	switch t.Kind {
	case rpc.KindWell:
		return nil, status.Error(codes.InvalidArgument, "set: well framing rides SetFraming")
	case rpc.KindText:
		return tileResp(p.st.SetTextView(ctx, req.TileId, t.TextX, t.TextY, t.TextW, t.TextH, t.TextMode))
	case rpc.KindShell:
		return tileResp(p.st.SetShellPreview(ctx, req.TileId, req.Preview))
	case rpc.KindURL:
		// The address is content, claimed and bumped, so it rides WriteContent.
		if t.UrlString != "" {
			return nil, status.Error(codes.InvalidArgument, "set: a url tile's address rides WriteContent")
		}
		return tileResp(p.st.SetURLState(ctx, req.TileId, req.Preview, t.AltText, t.UrlHistory))
	case rpc.KindPane:
		return nil, status.Error(codes.InvalidArgument, "set: pane layout rides WriteContent")
	default:
		return nil, status.Errorf(codes.InvalidArgument, "set: unknown kind %q", t.Kind)
	}
}

// ShellSessionAlive is the per-descent liveness probe of the session the tile
// names. A host with no shells, or a row that is gone, answers dead, which is a
// verdict; a tmux that cannot be asked is not, and answering dead for it would
// hide the refresh affordance with nothing said.
func (p *Plugin) ShellSessionAlive(ctx context.Context, req *gridwellv1.ShellSessionAliveRequest) (*gridwellv1.ShellSessionAliveResponse, error) {
	if p.shell == nil {
		return &gridwellv1.ShellSessionAliveResponse{Alive: false}, nil
	}
	key, err := p.sessionOf(ctx, req.TileId)
	if status.Code(err) == codes.NotFound {
		return &gridwellv1.ShellSessionAliveResponse{Alive: false}, nil
	}
	if err != nil {
		return nil, err
	}
	alive, err := p.shell.HasSession(key)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &gridwellv1.ShellSessionAliveResponse{Alive: alive}, nil
}

// sessionOf is the session a shell tile names (rpc.ShellSession), refusing a
// row that is not there or is not an owned shell.
func (p *Plugin) sessionOf(ctx context.Context, tileID string) (string, error) {
	t, err := p.st.GetTile(ctx, tileID)
	if err != nil {
		return "", errToStatus(err)
	}
	if t.Kind != rpc.KindShell || t.LinkTargetId != "" {
		return "", status.Errorf(codes.InvalidArgument, "tile %s is not a shell of this namespace", tileID)
	}
	return rpc.ShellSession(t), nil
}

// OpenShell streams a tile's live PTY both ways: the first request binds the
// tile id, then keystrokes and resizes flow up and output flows down.
func (p *Plugin) OpenShell(sctx context.Context, recv func() (*gridwellv1.OpenShellRequest, error), send func(*gridwellv1.OpenShellResponse) error) error {
	if p.shell == nil {
		return status.Error(codes.Unimplemented, "this namespace hosts no live shells")
	}
	first, err := recv()
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

	key, err := p.sessionOf(sctx, tileID)
	if err != nil {
		return err
	}
	// A session no tile has a face of was never started, so whichever tile
	// opens it first creates it; a started one that is gone must not be
	// fabricated behind the faces that show what was running.
	namers, err := p.st.ShellSessionNamers(sctx, key)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	session, stopOld, err := p.shell.Acquire(key, namers.Faced == 0, cols, rows)
	if err != nil {
		if errors.Is(err, shellsvc.ErrSessionGone) {
			return status.Error(codes.FailedPrecondition, err.Error())
		}
		return status.Error(codes.Internal, err.Error())
	}
	// Detach fires after the stream is ending, so the log is the surface; a
	// capture that does not land leaves the tile under its current name.
	defer p.shell.Release(key, session, stopOld, func() {
		if err := p.captureShellTitle(key, tileID); err != nil {
			log.Printf("gridwell: home: shell title capture for tile %s: %v", tileID, err)
		}
	})

	ctx, cancel := context.WithCancel(sctx)
	defer cancel()

	go func() {
		defer cancel()
		for {
			msg, rerr := recv()
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

	// Writer: PTY output down.
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
			if serr := send(&gridwellv1.OpenShellResponse{Data: chunk}); serr != nil {
				return serr
			}
		}
	}
}

// captureShellTitle stamps the label of the tile the session was opened from
// with the session's foreground command on detach, as a url tile captures the
// page title.
func (p *Plugin) captureShellTitle(key, tileID string) error {
	cmd, err := p.shell.PaneCommand(key)
	if err != nil {
		return fmt.Errorf("read the foreground command: %w", err)
	}
	if cmd == "" {
		return nil // tmux answers "" for a session that is gone: nothing to stamp
	}
	return p.st.SetTileAlt(context.Background(), tileID, cmd, false)
}

func (p *Plugin) DeleteTile(ctx context.Context, req *gridwellv1.DeleteTileRequest) (*gridwellv1.DeleteTileResponse, error) {
	// The key is read first: a destroy takes the row that names it.
	key := ""
	if p.shell != nil {
		key, _ = p.sessionOf(ctx, req.TileId)
	}
	if err := p.st.DeleteTile(ctx, req); err != nil {
		return nil, errToStatus(err)
	}
	// The session dies with the last row naming it, a trashed row included.
	// The startup orphan sweep is the net.
	if key != "" {
		if n, err := p.st.ShellSessionNamers(ctx, key); err == nil && n.Rows == 0 {
			_ = p.shell.Kill(key)
		}
	}
	return &gridwellv1.DeleteTileResponse{}, nil
}

func (p *Plugin) Subscribe(ctx context.Context, _ *gridwellv1.SubscribeRequest, send func(*gridwellv1.Event) error) error {
	ch, cancel := p.st.SubscribeEvents()
	defer cancel()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			// Nothing downstream writes into the shared event:
			// server.qualifyTiles clones.
			if err := send(ev); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func tileResp(t *gridwellv1.Tile, err error) (*gridwellv1.TileResponse, error) {
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.TileResponse{Tile: t}, nil
}

// errToStatus is gwerr.ToStatus, the one class-to-code table.
func errToStatus(err error) error { return gwerr.ToStatus(err) }
