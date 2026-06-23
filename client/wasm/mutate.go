//go:build js && wasm

package main

import (
	"context"
	"errors"
	"syscall/js"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/client/clientsync"
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

// logRPCError surfaces a non-nil RPC error to the JS console so the user
// sees something concrete when a tile silently fails to land. It is only
// reached for real failures — the conflict-vs-log decision is owned by
// clientsync.Classify (reactToErr), which never routes a conflict here.
func logRPCError(label string, err error) {
	if err == nil {
		return
	}
	js.Global().Get("console").Call("error", "gridwell: "+label+" failed: "+err.Error())
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
		logRPCError(label, err)
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

// postUpdateText fires an UpdateText RPC and, on success, replaces the
// cached blob so renderers reflect the new content immediately. On
// version-conflict it triggers a grid refetch. Returns the updated
// tile and ok=true on success; ok=false on any failure (callers
// should stop further work).
func (a *App) postUpdateText(gid string, req *rpc.UpdateTextRequest, newContent []byte) (rpc.Tile, bool) {
	tile, err := a.cl.UpdateText(context.Background(), req)
	if !a.reactToErr("UpdateText", gid, err) {
		return rpc.Tile{}, false
	}
	a.c.PutBlob(tile.BlobID, newContent)
	return *tile, true
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
