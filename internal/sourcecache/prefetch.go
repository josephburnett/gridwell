package sourcecache

// Whole-source prefetch: the cache remembers what you touched, and this walker
// warms what you did not, so "everything on this source is readable offline"
// is literally true. It is a per-namespace policy (Options.Prefetch), and the
// data is small by construction, so the walk is a full traversal and the caps
// are emergency valves. It goes through the wrapper's own read methods, so
// every answer lands by the one existing write path. A transport failure
// aborts quietly and the next trigger walks again; a coded refusal skips that
// branch, because the walker must never invent reachability the source denies.

import (
	"context"
	"github.com/josephburnett/gridwell/api/gwerr"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Emergency valves, not tuning knobs: a source that trips one is outside the
// small-data model this cache is built for, and the walk stops warming there.
var (
	// prefetchMaxGrids caps the traversal breadth.
	prefetchMaxGrids = 4096
	// prefetchContentBudget caps the sum of content bodies fetched by one
	// walk; per-entry bodies are already capped by maxCachedContentBytes.
	prefetchContentBudget = 256 << 20
	// prefetchPause is the gap between RPCs, so a background walk never
	// crowds out the user's own reads on a slow link. A test lengthens it and
	// times a walk against it; see prefetchpause_test.go.
	prefetchPause = 2 * time.Millisecond
)

// prefetcher is the walk's single-flight state, one per Client. running keys
// the walks in flight by what each covers: "" is every source.
type prefetcher struct {
	mu      sync.Mutex
	running map[string]bool
	// closed is set before the closer waits, under the same mutex a kick
	// takes, so no walk starts once waiting has begun. A trigger rides every
	// pass-through call, and one landing during Close would otherwise race
	// the WaitGroup it is counted by.
	closed bool
	ctx    context.Context
	cancel context.CancelFunc
	// wg counts the running walk. The closer waits on it after cancelling and
	// before closing the DB, so a walk never writes into a closed cache.
	wg sync.WaitGroup
}

// kickPrefetch is the one owner of when a walk starts, on either trigger: a
// Layer.Subscribe establishment, with source "" for every source the namespace
// declares, or a source coming back (Layer.setDark), with that source's
// segment. It is serialized per source and never queued, which is also the
// flap guard: a connection that leaves and returns twenty times has one walk.
func (c *Layer) kickPrefetch(source string) {
	if !c.opts.Prefetch {
		return
	}
	c.pf.mu.Lock()
	// running[""] covers every source, so a recovery arriving under one is
	// that walk's to warm.
	if c.pf.ctx == nil || c.pf.closed || c.pf.running[""] || c.pf.running[source] {
		c.pf.mu.Unlock()
		return
	}
	c.pf.running[source] = true
	c.pf.wg.Add(1)
	ctx := c.pf.ctx
	c.pf.mu.Unlock()
	go func() {
		defer c.pf.wg.Done()
		defer func() {
			c.pf.mu.Lock()
			delete(c.pf.running, source)
			c.pf.mu.Unlock()
		}()
		c.prefetch(ctx, source)
	}()
}

// stopWalks refuses further kicks and then waits for the walks in flight, so a
// walk never writes into a closed cache. Refusing first is the point: a
// trigger landing between the cancel and the wait would be counted by a
// WaitGroup already being waited on.
func (c *Layer) stopWalks() {
	c.pf.mu.Lock()
	c.pf.closed = true
	c.pf.mu.Unlock()
	c.pf.cancel()
	c.pf.wg.Wait()
}

// prefetch walks one source or all of them, warming grids, tiles, previews,
// plugin lists and content bodies.
func (c *Layer) prefetch(ctx context.Context, source string) {
	w := &walker{c: c, ctx: ctx, seenGrids: map[string]bool{}, seenTiles: map[string]bool{}, seenNs: map[string]bool{}}
	// The roots are what the fronted namespace declares about itself, through
	// the door every namespace answers on: the handshake with no namespace.
	hs, err := c.Handshake(ctx, &pb.HandshakeRequest{})
	if err != nil {
		return // dark, or refused, at the doorstep: nothing to walk
	}
	roots := []string{}
	add := func(id string) {
		// A root belongs to the source its id names, so one source's walk is a
		// filter over the same declaration rather than a second way of
		// asking. A key deeper than a connection segment owns no root here.
		if id == "" || (source != "" && sourceOf(id) != source) {
			return
		}
		roots = append(roots, id)
	}
	for _, conn := range hs.GetConnections() {
		add(conn.GetRootGridId())
	}
	for _, p := range hs.GetPlugins() {
		add(p.GetRootGridId())
	}
	for _, g := range roots {
		if !w.walkGrid(g) {
			return
		}
	}
}

type walker struct {
	c         *Layer
	ctx       context.Context
	seenGrids map[string]bool
	seenTiles map[string]bool
	seenNs    map[string]bool
	spent     int
}

// pause returns false when the walk should stop because the client is
// closing.
func (w *walker) pause() bool {
	select {
	case <-w.ctx.Done():
		return false
	case <-time.After(prefetchPause):
		return true
	}
}

// walkGrid warms one grid and recurses into its children. False means abort:
// the source is dark, the context is done, or a cap tripped. A coded refusal
// skips the branch and returns true.
func (w *walker) walkGrid(gridID string) bool {
	if w.seenGrids[gridID] || len(w.seenGrids) >= prefetchMaxGrids {
		return len(w.seenGrids) < prefetchMaxGrids
	}
	w.seenGrids[gridID] = true
	if !w.pause() {
		return false
	}
	// The live read, never the serve-first door: a remembered answer would
	// warm nothing.
	resp, err := w.c.getGridLive(w.ctx, gridID)
	if err != nil {
		return !gwerr.IsTransport(err) // dark aborts; a refusal skips this branch
	}
	// The + menu context for this grid's node, once per namespace.
	if ns := resp.GetGrid().GetNodeNs(); !w.seenNs[ns] {
		w.seenNs[ns] = true
		if w.pause() {
			if _, err := w.c.Handshake(w.ctx, &pb.HandshakeRequest{Namespace: ns}); err != nil && gwerr.IsTransport(err) {
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

// walkTile warms one tile's preview and, for a content kind, its body,
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
	// A body is what the walk fetches; everything else renders offline from
	// its cached row and preview.
	if rpc.IsBodyKind(t.GetKind()) && w.spent < prefetchContentBudget {
		if !w.pause() {
			return false
		}
		n, ok := drain(func(count func(int)) error {
			return w.c.ReadContent(w.ctx, &pb.ReadContentRequest{TileId: t.GetId()},
				func(ch *pb.ContentChunk) error { count(len(ch.GetData())); return nil })
		})
		if !ok {
			return false
		}
		w.spent += n
	}
	// A serves_page tile's face-value body is its door page at the root
	// subpath, rpc.PageURL's target, bounded like every other body.
	if t.GetServesPage() && w.spent < prefetchContentBudget {
		if !w.pause() {
			return false
		}
		n, ok := drain(func(count func(int)) error {
			return w.c.ServeContent(w.ctx, &pb.ServeContentRequest{TileId: t.GetId(), Subpath: ""},
				func(ch *pb.ServeContentChunk) error { count(len(ch.GetData())); return nil })
		})
		if !ok {
			return false
		}
		w.spent += n
	}
	// A leaf link's target is what the link renders through, so warm it even
	// if its own grid is never walked. walkTile owns the seen-set:
	// pre-marking the target here would make the recursion below return at
	// its seen-check with nothing warmed.
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

// drain runs one content read to warm the cache and reports the bytes seen.
// ok is false when the source went dark before a single chunk, so the walk
// aborts; a failure after bytes have flowed ends only this branch.
func drain(read func(count func(int)) error) (n int, ok bool) {
	first := true
	err := read(func(size int) {
		first = false
		n += size
	})
	if err != nil {
		return n, !(first && gwerr.IsTransport(err))
	}
	return n, true
}
