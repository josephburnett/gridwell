//go:build js && wasm

package main

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// This file is the mutation dispatch layer: the handful of helpers every
// tile RPC funnels through so the optimistic-cache / conflict-resync / error
// surfacing policy lives in exactly one place. The *decision* (resync vs
// surface) is the pure clientsync.Classify; this layer detects the
// transport-specific conflict and applies the reaction against App state.

// tileCall is the closure shape every tile-producing mutation takes.
// Callers wrap the matching a.cl method (CreateText, MoveTile, etc.)
// so the dispatcher helpers don't need to know the request type.
type tileCall func(ctx context.Context) (*rpc.Tile, error)

// voidCall is the closure shape for mutations that return no tile
// (DeleteTile, SetRootView).
type voidCall func(ctx context.Context) error

// isVersionConflict reports whether an RPC error came back with the
// FailedPrecondition code Connect uses for both ErrVersionConflict and
// ErrOverlap. Either case warrants a cache-resync via grid refetch.
func isVersionConflict(err error) bool {
	if err == nil {
		return false
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code() == connect.CodeFailedPrecondition
	}
	return false
}

// surfaceRPCError surfaces a non-nil RPC error as an on-canvas notice (the
// errsurface strip — what the user actually sees); reportErr also writes the
// one console/log line. It is only reached for real failures — the
// conflict-vs-surface decision is owned by clientsync.Classify (reactToErr),
// which never routes a conflict here.
func (a *App) surfaceRPCError(label string, err error) {
	if err == nil {
		return
	}
	a.reportErr(errsurface.Error, "rpc:"+label, label+" failed: "+rpcErrText(err))
}

// rpcErrText strips the Connect wire prefix ("unknown: ", "internal: …") down
// to readable failure text for the notice strip and log line.
func rpcErrText(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Message()
	}
	return err.Error()
}

// reactToErr applies the clientsync resync policy to an RPC error: a
// version/overlap conflict refetches the grid (cache resync), any other
// error is surfaced to the console. Conflict detection is transport-specific
// (Connect status), so it is computed here and handed to clientsync.Classify,
// which owns the pure decision. Returns true when err was nil (success), so
// callers can early-out on failure.
func (a *App) reactToErr(label string, gid string, err error) bool {
	r := clientsync.Classify(err, isVersionConflict(err))
	if r.Refetch {
		a.refetchGridOnConflict(gid, label)
	}
	if r.Log {
		a.surfaceRPCError(label, err)
	}
	return err == nil
}

// postCrossGridMutate is the shared body of left-drag (MoveTile) and
// right-drag (CloneTile) ghost commits. Both call an RPC that touches
// two grids (source + destination), and both have to roll the ghost
// back to its origin on failure so the user sees the stone return
// instead of vanishing.
//
// On success it refetches both grids; on any error it triggers a
// refetch of the source grid (if version-conflict) and snaps the
// dragged ghost back. `label` is the breadcrumb name for the
// conflict log.
func (a *App) postCrossGridMutate(label string, srcGridID, dstGridID string, call tileCall, d *dragState) {
	go func() {
		_, err := call(context.Background())
		if err != nil {
			a.reactToErr(label, srcGridID, err)
			a.snapBackToOrigin(d)
			return
		}
		a.fetchGrid(srcGridID)
		a.fetchGrid(dstGridID)
	}()
}

// postPersist runs a "save my local view" RPC: the caller has already
// patched the cache, the server-side write is the durable mirror. Used
// by SetWellView, where the cache has already been updated
// optimistically before the goroutine fires.
func (a *App) postPersist(label string, gid string, call tileCall) {
	go func() {
		_, err := call(context.Background())
		a.reactToErr(label, gid, err)
	}()
}

// postOptimisticPersist is postPersist for a caller that ALREADY patched the
// cache before the RPC (e.g. persistWellView's framing patch). The reaction
// table differs: ANY failure refetches, because a rejected optimistic patch
// left the cache ahead of server truth (clientsync.ClassifyOptimistic,
// issue #156).
func (a *App) postOptimisticPersist(label string, gid string, call tileCall) {
	go func() {
		_, err := call(context.Background())
		r := clientsync.ClassifyOptimistic(err, isVersionConflict(err))
		if r.Refetch {
			a.refetchGridOnConflict(gid, label)
		}
		if r.Log {
			a.surfaceRPCError(label, err)
		}
	}()
}

// doFreezeWrite runs a leaving-gesture freeze writeback (url page / shell
// terminal preview) with the one retry rule: claim `version`; on a version
// conflict re-claim ONCE via GetTile and retry — an automatic writer racing
// the user's leaving gesture (the detach-time title capture, a foreign
// framing write) must not cost the freeze. Any remaining error surfaces as
// `source`/`failText` AND goes through the conflict-reconcile dispatcher so
// the cache resyncs instead of drifting (issue #156 — these paths used to
// bypass reactToErr). Blocking; callers run it from a goroutine.
func (a *App) doFreezeWrite(label, gid, tileID string, version int64, source, failText string, write func(version int64) error) {
	err := write(version)
	if err != nil && isVersionConflict(err) {
		if fresh, gerr := a.cl.GetTile(context.Background(), tileID); gerr == nil {
			err = write(fresh.Version)
		}
	}
	if err != nil {
		a.reportErr(errsurface.Error, source, failText+": "+rpcErrText(err))
		a.reactToErr(label, gid, err)
	}
}

// postVoidPersist is postPersist for RPCs that return no tile — used by
// SetRootView, the plugin-root framing writeback of a + menu portal ascent.
func (a *App) postVoidPersist(label string, gid string, call voidCall) {
	go func() {
		a.reactToErr(label, gid, call(context.Background()))
	}()
}

// postTwoGridMutate is the no-snapback variant of postCrossGridMutate.
// Used by DeleteTile, where the tile is going to vanish either way —
// a failed delete still needs the cache refreshed but there's no
// ghost to roll back to. Skips the dstGrid refetch when it equals
// srcGrid (delete onto a black hole in the same grid).
func (a *App) postTwoGridMutate(label string, srcGridID, dstGridID string, call voidCall) {
	go func() {
		a.reactToErr(label, srcGridID, call(context.Background()))
		a.fetchGrid(srcGridID)
		if dstGridID != "" && dstGridID != srcGridID {
			a.fetchGrid(dstGridID)
		}
	}()
}

// postWriteContent fires the one content write (WriteContent — id-addressed,
// version-claimed, commit-at-close; 2026-07-26 redesign) and, on success,
// replaces the cached blob so renderers reflect the new content immediately.
// On version-conflict it triggers a grid refetch. Returns the updated
// tile and ok=true on success; ok=false on any failure (callers
// should stop further work).
func (a *App) postWriteContent(gid, tileID string, version int64, newContent []byte) (rpc.Tile, bool) {
	tile, err := a.cl.WriteContent(context.Background(), tileID, version, newContent)
	if !a.reactToErr("WriteContent", gid, err) {
		// Reconcile the rejected optimistic edit: callers wrote newContent
		// into the cache before this RPC, so on any rejection the screen is
		// showing bytes the server refused. Drop them so the next render
		// refetches server truth (grid refetch alone never evicts content),
		// and refetch the grid on the non-conflict path (the conflict path
		// already refetched via reactToErr). The reactToErr notice tells the
		// user why their text just reverted; without this the rejected edit
		// lingers looking saved, then vanishes on some later refetch —
		// the silent-disappearance class (charter §6).
		a.c.DropTileContent(tileID)
		if !isVersionConflict(err) {
			a.fetchGrid(gid)
		}
		a.refreshFileOverlay()
		a.scheduleFrame()
		return rpc.Tile{}, false
	}
	// Advance the cached tile AND the save basis to the response row NOW —
	// not when the SSE echo lands. Text saves are serialized per tile and
	// claim their basis at send time (issue #140); that only chains if the
	// previous write's response has already moved the basis forward.
	// The tile is cached under the RESPONSE row's own grid, not the caller's
	// gid — for a save routed through a leaf link the response row lives in
	// the target's (foreign) grid, and writing it under the link's grid
	// would plant a foreign tile row in the wrong grid map.
	a.c.UpdateTile(tile.GridID, *tile)
	a.c.PutSavedContent(tile.ID, newContent, tile.Version)
	return *tile, true
}

// enqueueTextSave posts a content write through the per-tile serial queue.
// The version is claimed AT SEND TIME — after any earlier write for the same
// tile has completed and advanced the save basis — so pipelined saves (the
// raw→rendered toggle's flush and the keystroke typed right after it,
// issue #140) chain versions instead of both claiming the same one and
// losing the second edit to a conflict reconcile.
//
// The claim is the cache's SaveBasis — the version of the BYTES this edit is
// based on — never the grid row version. The row advances when a foreign
// writer's event or a grid refetch lands, without this client ever seeing the
// new content; claiming it would carry current-version + stale-bytes past the
// server's optimistic-concurrency check and silently destroy the foreign edit
// (the remote-stomp bug). Claiming the basis makes that save conflict
// instead, which reconciles visibly. fallbackVersion (the enqueue-time row
// version) is used only if the content entry is gone, so the server still
// sees the write and surfaces the real story.
func (a *App) enqueueTextSave(gid string, tileID string, fallbackVersion int64, data []byte) {
	a.textSaves.Enqueue(tileID, func() {
		version := fallbackVersion
		if base, ok := a.c.SaveBasis(tileID); ok {
			version = base
		}
		a.postWriteContent(gid, tileID, version, data)
	})
}

// doTileMutate is the synchronous core of a single-grid tile RPC.
// On version-conflict it triggers a grid refetch and returns
// ok=false (so callers can stop early). On success it schedules a
// grid refetch so the cache catches up to the server's authoritative
// tile state and returns the response tile. All "Create<X>",
// "ResizeTile", "SetTextView" calls fit this shape.
func (a *App) doTileMutate(label string, gid string, call tileCall) (rpc.Tile, bool) {
	tile, err := call(context.Background())
	if !a.reactToErr(label, gid, err) {
		return rpc.Tile{}, false
	}
	a.fetchGrid(gid)
	return *tile, true
}

// postTileMutate runs doTileMutate in a goroutine; onSuccess (if
// non-nil) fires once the success-path refetch is scheduled.
func (a *App) postTileMutate(label string, gid string, call tileCall, onSuccess func(rpc.Tile)) {
	go func() {
		if tile, ok := a.doTileMutate(label, gid, call); ok && onSuccess != nil {
			onSuccess(tile)
		}
	}()
}
