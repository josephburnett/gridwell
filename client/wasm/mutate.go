//go:build js && wasm

package main

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pending"
)

// This file is the mutation dispatch layer: the handful of helpers every
// tile RPC funnels through so the optimistic-cache / conflict-resync / error
// surfacing policy lives in exactly one place. The *decision* — what the
// outcome was and what it permits — is the pure clientsync package
// (Of + the React* tables); this layer applies the reaction against App
// state. The one rule (2026-08-14): local state is dropped only on a
// server verdict, never on a transport failure.

// tileCall is the closure shape every tile-producing mutation takes.
// Callers wrap the matching a.cl method (CreateText, MoveTile, etc.)
// so the dispatcher helpers don't need to know the request type.
type tileCall func(ctx context.Context) (*rpc.Tile, error)

// voidCall is the closure shape for mutations that return no tile
// (DeleteTile, SetRootView).
type voidCall func(ctx context.Context) error

// isUnimplemented reports a plugin's "I don't serve this" answer — a
// capability property, never a failure to surface. The judgment is
// clientsync's (the one wire-code classifier).
func isUnimplemented(err error) bool { return clientsync.IsUnimplemented(err) }

// surfaceRPCError surfaces a non-nil RPC error as an on-canvas notice (the
// errsurface strip — what the user actually sees); reportErr also writes the
// one console/log line. It is only reached for real failures — the
// conflict-vs-surface decision is owned by clientsync.Of (reactToErr),
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

// reactToErr applies the plain-mutation policy (clientsync.React) to an
// RPC error: a version/overlap conflict refetches the grid (cache resync);
// a rejection or a transport failure is surfaced. Returns true when err
// was nil (success), so callers can early-out on failure.
func (a *App) reactToErr(label string, gid string, err error) bool {
	r := clientsync.React(clientsync.Of(err))
	if r.Refetch {
		a.refetchGridOnConflict(gid, label)
	}
	if r.Log {
		a.surfaceRPCError(label, err)
	}
	return err == nil
}

// reactFraming applies the optimistic-writer policy
// (clientsync.ReactOptimistic) for a caller that patched the cache BEFORE
// the RPC, and keeps the pending ledger honest: EVERY completed attempt
// (success or server verdict) acks the key, a transport failure parks the
// retry. A verdict rolls the cache back to truth (refetch — issue #156);
// transport KEEPS the patch (it is the user's value, and a refetch
// against a flapping link could succeed and silently revert it), parks
// the write for the retry kick, and surfaces the failure.
func (a *App) reactFraming(label, gid string, k pending.Key, retry func(), err error) {
	o := clientsync.Of(err)
	if o == clientsync.OutcomeTransport {
		a.pend.Put(k, retry)
	} else {
		a.pend.Ack(k)
	}
	r := clientsync.ReactOptimistic(o)
	if r.Refetch {
		a.refetchGridOnConflict(gid, label)
	}
	if r.Log {
		a.surfaceRPCError(label, err)
	}
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

// postFramingPersist dispatches a FRAMING write. Framing carries no version
// claim (2026-08-29, docs/simplify-plan.md S5: version is the user's content
// claim, framing is last-writer-wins), so there is no conflict to re-claim
// through — the one-retry rule the old dispatcher needed existed only
// because automatic captures bumped the row out from under a settle. The
// caller already patched the cache, so a failure follows reactFraming: a
// verdict refetches (rolls the patch back to server truth); a transport
// failure keeps the patch and PARKS the write in the pending ledger for the
// retry kick.
func (a *App) postFramingPersist(label, gid, tileID string, call func(ctx context.Context) error) {
	a.persistPosts[label]++
	go func() {
		err := call(context.Background())
		a.reactFraming(label, gid, pending.Key{Op: label, ID: tileID},
			func() { a.postFramingPersist(label, gid, tileID, call) }, err)
	}()
}

// doFreezeWrite runs a leaving-gesture freeze writeback (url page / shell
// terminal preview). A freeze is an automatic CAPTURE: no version claim, no
// bump (docs/simplify-plan.md S5) — so nothing here can conflict, and the
// re-claim this path used to need is gone with the bump that caused it. Any
// error surfaces as `source`/`failText` AND goes through the reconcile
// dispatcher so the cache resyncs instead of drifting (issue #156).
// Blocking; callers run it from a goroutine.
func (a *App) doFreezeWrite(label, gid, tileID, source, failText string, write func() error) error {
	err := write()
	k := pending.Key{Op: label, ID: tileID}
	if clientsync.Of(err) == clientsync.OutcomeTransport {
		// Park the freeze: the write closure holds the captured payload
		// (jpeg frame, url, title, history) — the ONLY copy once the live
		// surface is gone. Abandoning it here was audit bug #2 (2026-08-14):
		// one blip at ascent cost the tile its page, its trail, and its
		// frozen face until the next live visit.
		a.pend.Put(k, func() {
			go a.doFreezeWrite(label, gid, tileID, source, failText, write)
		})
		a.reportErr(errsurface.Info, source, failText+": server unreachable — will retry")
		return err
	}
	a.pend.Ack(k)
	if err != nil {
		a.reportErr(errsurface.Error, source, failText+": "+rpcErrText(err))
		a.reactToErr(label, gid, err)
	}
	return err
}

// postVoidPersist is postPersist for RPCs that return no tile — used by
// SetRootView, the plugin-root framing writeback of a + menu portal
// ascent. Framing-class, so it rides the pending ledger like every other
// framing write: transport parks a retry, any completion acks.
func (a *App) postVoidPersist(label string, gid string, call voidCall) {
	go func() {
		err := call(context.Background())
		k := pending.Key{Op: label, ID: gid}
		if clientsync.Of(err) == clientsync.OutcomeTransport {
			a.pend.Put(k, func() { a.postVoidPersist(label, gid, call) })
		} else {
			a.pend.Ack(k)
		}
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

// postWriteContent fires the one content write (WriteContent — id-addressed,
// version-claimed, commit-at-close; 2026-07-26 redesign) and, on success,
// replaces the cached blob so renderers reflect the new content immediately.
// On version-conflict it triggers a grid refetch. Returns the updated
// tile and ok=true on success; ok=false on any failure (callers
// should stop further work).
func (a *App) postWriteContent(gid, tileID string, version int64, newContent []byte) (rpc.Tile, bool) {
	tile, err := a.cl.WriteContent(context.Background(), tileID, version, newContent)
	if err != nil {
		o := clientsync.Of(err)
		r := clientsync.ReactSave(o)
		if r.Log {
			a.surfaceRPCError("WriteContent", err)
		}
		if r.DropLocal {
			// The server gave a VERDICT: the screen is showing bytes it
			// refused. Drop them so the next render refetches server truth
			// (grid refetch alone never evicts content) — without this the
			// rejected edit lingers looking saved, then vanishes on some
			// later refetch: the silent-disappearance class (charter §6).
			// The conflict/reject notice tells the user why their text
			// just reverted.
			a.c.DropTileContent(tileID)
			if o == clientsync.OutcomeConflict {
				a.refetchGridOnConflict(gid, "WriteContent")
			} else {
				a.fetchGrid(gid)
			}
			a.refreshFileOverlay()
			a.scheduleFrame()
		} else {
			// Transport: the server never spoke. The content entry stays
			// DIRTY — it is the ONLY copy of the user's unsaved words
			// (the textarea is a view of it, not an owner) — so the flush
			// sweep re-posts it on the next tick and the retry kick drains
			// it on reconnect. Dropping it here was the archetype
			// data-loss bug (2026-08-14): a wifi blip during autosave
			// destroyed the paragraph and repainted the textarea from
			// stale server bytes.
			a.reportErr(errsurface.Info, "textsave",
				"unsaved changes kept — server unreachable, will retry")
		}
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
