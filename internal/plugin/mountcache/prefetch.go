package mountcache

// Whole-mount prefetch (issue #254, the phase-1 open decision resolved
// YES): the cache remembers what you TOUCHED; this walker warms what you
// DIDN'T, so "everything on the mount is readable offline" is literally
// true rather than everything-visited. The data is small by construction
// (text, previews, metadata — megabytes, not gigabytes; 2026-08-14), so
// the walk is a full traversal with caps as emergency valves, not a
// sampling strategy.
//
// Trigger: every successful Subscribe establishment — that is both the
// initial connect and each health-up reconnect (the server's fan-in
// re-subscribes through this wrapper), so the walk doubles as the
// deletes-while-away resync for grids the user never re-opened. The walk
// runs through the wrapper's OWN read methods, so every answer lands in
// the cache by the one existing write path (no second writer).
//
// A transport failure mid-walk aborts quietly — the mount went dark; the
// next successful Subscribe walks again. Coded refusals on individual
// reads (a tombstoned segment, a permission wall) skip that branch and
// keep walking: the walker must never invent reachability the mount
// denies. A serves_page tile's door body (its root subpath) is walked
// under the same byte budget, so photos and plugin pages are offline too
// for the common case; the explicit pin gesture stays deferred until
// budget-bounded prefetch proves too cold in practice (issue #255).

import (
	"context"
	"github.com/josephburnett/gridwell/api/gwerr"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Emergency valves, not tuning knobs: a mount that trips one is far
// outside the small-data model this cache is built for, and the walk
// simply stops warming there (everything already walked stays cached).
var (
	// prefetchMaxGrids caps the traversal breadth.
	prefetchMaxGrids = 4096
	// prefetchContentBudget caps the SUM of content bodies fetched by one
	// walk (per-entry bodies are already capped by maxCachedContentBytes).
	prefetchContentBudget = 256 << 20
	// prefetchPause is the politeness gap between RPCs so a background
	// walk never crowds out the user's own reads on a slow link.
	prefetchPause = 2 * time.Millisecond
)

// contentKinds are the tile kinds whose bodies the walk fetches: the ones
// whose CONTENT is what the user came for offline (a note's text, a
// workspace's layout). Everything else offline-renders from its cached
// row + preview.
var contentKinds = map[string]bool{"text": true, "pane": true}

// prefetcher is the walk's single-flight state, one per Client.
type prefetcher struct {
	mu      sync.Mutex
	running bool
	ctx     context.Context
	cancel  context.CancelFunc
}

// kick starts one walk if none is running. Serialized, never queued: a
// trigger during a walk is satisfied by that walk's own freshness (each
// Subscribe re-trigger means the mount is up NOW, which the running walk
// is already exploiting).
func (c *Client) kickPrefetch() {
	c.pf.mu.Lock()
	if c.pf.running || c.pf.ctx == nil {
		c.pf.mu.Unlock()
		return
	}
	c.pf.running = true
	ctx := c.pf.ctx
	c.pf.mu.Unlock()
	go func() {
		defer func() {
			c.pf.mu.Lock()
			c.pf.running = false
			c.pf.mu.Unlock()
		}()
		c.Prefetch(ctx)
	}()
}

// Prefetch walks the whole mount through the wrapper's own read methods,
// warming grids, tiles, previews, plugin lists, and content bodies.
// Exported so a deliberate warm (a future pin gesture, a test) can run it
// synchronously; the Subscribe trigger runs it in the background.
func (c *Client) Prefetch(ctx context.Context) {
	w := &walker{c: c, ctx: ctx, seenGrids: map[string]bool{}, seenTiles: map[string]bool{}, seenNs: map[string]bool{}}
	info, err := c.Info(ctx, &pb.InfoRequest{})
	if err != nil {
		return // dark (or refused) at the doorstep: nothing to walk
	}
	roots := []string{}
	if info.GetRootGridId() != "" {
		roots = append(roots, info.GetRootGridId())
	}
	for _, e := range info.GetMenuEntries() {
		if e.GetGridId() != "" {
			roots = append(roots, e.GetGridId())
		}
	}
	for _, g := range roots {
		if !w.walkGrid(g) {
			return
		}
	}
}

type walker struct {
	c         *Client
	ctx       context.Context
	seenGrids map[string]bool
	seenTiles map[string]bool
	seenNs    map[string]bool
	spent     int
}

// pause is the politeness gap; false means the walk should stop (context
// done — the client is closing).
func (w *walker) pause() bool {
	select {
	case <-w.ctx.Done():
		return false
	case <-time.After(prefetchPause):
		return true
	}
}

// walkGrid warms one grid and recurses into its children. Returns false
// only when the walk should ABORT (transport-dark, context done, cap
// tripped) — a coded refusal skips the branch and returns true.
func (w *walker) walkGrid(gridID string) bool {
	if w.seenGrids[gridID] || len(w.seenGrids) >= prefetchMaxGrids {
		return len(w.seenGrids) < prefetchMaxGrids
	}
	w.seenGrids[gridID] = true
	if !w.pause() {
		return false
	}
	resp, err := w.c.GetGrid(w.ctx, &pb.GetGridRequest{GridId: gridID})
	if err != nil {
		return !gwerr.IsTransport(err) // dark → abort; refused → skip this branch
	}
	// The + menu context for this grid's node (remote-menu): warm the
	// routed plugin list once per namespace.
	if ns := resp.GetGrid().GetNodeNs(); !w.seenNs[ns] {
		w.seenNs[ns] = true
		if w.pause() {
			if _, err := w.c.ListPlugins(w.ctx, &pb.ListPluginsRequest{Namespace: ns}); err != nil && gwerr.IsTransport(err) {
				return false
			}
		} else {
			return false
		}
	}
	for _, t := range resp.GetTiles() {
		if !w.walkTile(t) {
			return false
		}
	}
	for _, t := range resp.GetTiles() {
		if child := t.GetChildGridId(); child != "" {
			if !w.walkGrid(child) {
				return false
			}
		}
	}
	return true
}

// walkTile warms one tile's preview and (for content kinds) its body,
// following a leaf link to its target row once.
func (w *walker) walkTile(t *pb.Tile) bool {
	if w.seenTiles[t.GetId()] {
		return true
	}
	w.seenTiles[t.GetId()] = true
	if !w.pause() {
		return false
	}
	if _, err := w.c.GetTilePreview(w.ctx, &pb.GetTilePreviewRequest{TileId: t.GetId()}); err != nil && gwerr.IsTransport(err) {
		return false
	}
	if contentKinds[t.GetKind()] && w.spent < prefetchContentBudget {
		if !w.pause() {
			return false
		}
		s, err := w.c.ReadContent(w.ctx, &pb.ReadContentRequest{TileId: t.GetId()})
		if err != nil {
			return !gwerr.IsTransport(err)
		}
		if n, ok := drainStream(s); !ok {
			return false
		} else {
			w.spent += n
		}
	}
	// A serves_page tile's face-value body is its door page at the root
	// subpath (rpc.PageURL's target). Bounded like every body: the entry
	// cap skips oversized pages, the budget stops the class.
	if t.GetServesPage() && w.spent < prefetchContentBudget {
		if !w.pause() {
			return false
		}
		s, err := w.c.ServeContent(w.ctx, &pb.ServeContentRequest{TileId: t.GetId(), Subpath: ""})
		if err != nil {
			return !gwerr.IsTransport(err)
		}
		if n, ok := drainServeStream(s); !ok {
			return false
		} else {
			w.spent += n
		}
	}
	// A leaf link's target is what the link renders and resolves through:
	// warm the target row (and its face/body) even if its own grid is
	// never walked.
	// walkTile owns the seen-set: pre-marking the target here would make
	// the recursion below return at its seen-check with nothing warmed —
	// and poison the target's later natural visit too.
	if target := t.GetLinkTargetId(); target != "" && !w.seenTiles[target] {
		if !w.pause() {
			return false
		}
		tr, err := w.c.GetTile(w.ctx, &pb.GetTileRequest{TileId: target})
		if err != nil {
			return !gwerr.IsTransport(err)
		}
		if tt := tr.GetTile(); tt != nil {
			tt2 := proto.Clone(tt).(*pb.Tile)
			tt2.LinkTargetId = "" // never chase a chain twice
			return w.walkTile(tt2)
		}
	}
	return true
}

// drainServeStream is drainStream for the door's chunk shape.
func drainServeStream(s grpc.ServerStreamingClient[pb.ServeContentChunk]) (n int, ok bool) {
	first := true
	for {
		ch, err := s.Recv()
		if err == io.EOF {
			return n, true
		}
		if err != nil {
			return n, !(first && gwerr.IsTransport(err))
		}
		first = false
		n += len(ch.GetData())
	}
}

// drainStream consumes a ReadContent stream so the tee stores it at EOF,
// reporting the byte count; ok=false means transport-dark before any
// frame (abort the walk).
func drainStream(s grpc.ServerStreamingClient[pb.ContentChunk]) (n int, ok bool) {
	first := true
	for {
		ch, err := s.Recv()
		if err == io.EOF {
			return n, true
		}
		if err != nil {
			return n, !(first && gwerr.IsTransport(err))
		}
		first = false
		n += len(ch.GetData())
	}
}
