//go:build js && wasm

package main

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/outbox"
)

// This file is the mutation dispatch layer. It is two paths:
//
//   - do / post — every write that carries no version claim: framing, an
//     automatic capture, a layout move, a create, a delete. One policy body.
//   - postWriteContent — the one write that claims a version, because it is
//     the only one that changes the user's content bytes.
//
// The decision — what an outcome was and what it permits — is the pure
// clientsync package (Of plus the React* tables). The record of what the
// server has not yet acknowledged is the pure client/outbox. This layer is
// the glue that applies both against App state.
//
// The rule both paths serve: local state is dropped only on a server verdict,
// never on a transport failure.

// tileCall is the closure shape a tile-producing mutation takes. Callers wrap
// the matching a.cl method (CreateText, PlaceTile, …) so the dispatcher
// doesn't need to know the request type.
type tileCall func(ctx context.Context) (*rpc.Tile, error)

// write is one non-content mutation and everything the dispatcher needs in
// order to react to it. Policy — which reaction table, whether the write
// parks, what the notices say — lives in `do`; `then` and `undo` are the
// caller's own business.
type write struct {
	// label names the op in the notice strip, the persist counters, and the
	// outbox key.
	label string
	// gid is the grid whose cache reconciles on a server verdict.
	gid string
	// alsoGID is a second grid the write touched (a cross-grid drag),
	// refetched alongside gid on success. Empty, or equal to gid, is one
	// grid.
	alsoGID string
	// id keys the outbox entry: a tile id, or a grid id for a root framing
	// write; the two are separate sequences, so the key carries which. Empty
	// means this write is not parked — its value is still on screen and the
	// failure notice is the reconcile (a create, a drag whose ghost snaps
	// back). Nothing user-made goes unparked unless the user can see that it
	// did not happen.
	id string
	// optimistic marks a caller that patched the cache before the RPC, so a
	// server verdict rolls the cache back to truth.
	optimistic bool
	// refetchOnOK refetches gid (and alsoGID) after the write lands.
	refetchOnOK bool
	// call is the RPC.
	call func(ctx context.Context) error
	// then runs after a successful call, before the refetch is scheduled.
	then func()
	// done, when set, runs once this write has finished — landed, failed, or
	// parked. It is the release half of an in-flight count, so no path out of
	// the dispatcher can leave a write counted as still going.
	done func()
	// undo runs after a failure: the visible reconcile for a write that
	// showed the user something optimistic with no cache patch behind it,
	// such as the drag ghost snapping back to its origin. It is the
	// alternative to parking — a write that reconciles visibly must not also
	// be retried later — so `undo` and `id` are never both set.
	undo func()
	// source and failText name the domain notice for a write whose failure
	// the user must see in its own words ("shell preview save failed"), on
	// top of the generic rpc: notice. An empty source means the generic
	// notice alone.
	source, failText string
	// beacon is this write's navigator.sendBeacon form, the transport that
	// survives the page (api/rpc's *Beacon builders: one request builder,
	// two transports, with contentType picking unary proto-JSON or the
	// WriteContent streaming envelope). Consulted only during beforeunload. A
	// write with no beacon form is fired and hoped for at unload; it never
	// blocks the unload handler waiting for a reply that cannot arrive.
	beacon func() (path string, body []byte, contentType string)
}

// isUnimplemented reports a plugin's "I don't serve this" answer: a
// capability property, never a failure to surface. The judgment is
// clientsync's, the one wire-code classifier.
func isUnimplemented(err error) bool { return clientsync.IsUnimplemented(err) }

// surfaceRPCError surfaces a non-nil RPC error as an on-canvas notice (the
// errsurface strip — what the user actually sees); reportErr also writes the
// one console/log line. It is only reached for real failures — the
// conflict-vs-surface decision is owned by clientsync.Of, whose tables never
// route a conflict here.
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

// do runs one non-content mutation and applies the whole policy: record the
// outcome in the outbox (transport parks a retry, any verdict acks), react
// per clientsync's table for this caller's shape, surface what failed, and
// run the caller's own then/undo. Blocking; `post` is the goroutine form.
//
// The retry thunk re-enters here with the same `write`, so a retry that fails
// on transport again re-parks itself and the outbox converges rather than
// losing the value.
func (a *App) do(w write) error {
	if w.done != nil {
		defer w.done()
	}
	if a.unloading {
		return a.doOnUnload(w)
	}
	err := w.call(context.Background())
	o := clientsync.Of(err)

	if w.id != "" {
		// The closure holds the captured payload — a settled viewport, a
		// freeze's jpeg, url, title, and trail, a pane arrangement, a typed
		// name — which is the only copy once the gesture that made it is
		// over. Abandoning it on a blip loses it.
		a.out.Record(o, outbox.Key{Op: w.label, ID: w.id}, func() { a.post(w) })
	}

	r := clientsync.React(o)
	if w.optimistic {
		r = clientsync.ReactOptimistic(o)
	}
	if r.Refetch {
		a.refetchGridOnConflict(w.gid, w.label)
	}
	// A write with its own words says them. The generic rpc: notice rides
	// along on a verdict — the developer half of "why did that fail" — but
	// not on a transport blip, where "will retry" is the whole story and a
	// second red line beside it is noise.
	if r.Log && !(w.source != "" && o == clientsync.OutcomeTransport) {
		a.surfaceRPCError(w.label, err)
	}
	if w.source != "" {
		switch {
		case o == clientsync.OutcomeTransport:
			a.reportErr(errsurface.Info, w.source, w.failText+": server unreachable — will retry")
		case err != nil:
			a.reportErr(errsurface.Error, w.source, w.failText+": "+rpcErrText(err))
		}
	}
	if err != nil {
		if w.undo != nil {
			w.undo()
		}
		return err
	}
	// The refetch is scheduled before `then`: a success hook that relocates
	// a pane wants the catch-up fetch already in flight.
	if w.refetchOnOK {
		a.fetchGrid(w.gid)
		if w.alsoGID != "" && w.alsoGID != w.gid {
			a.fetchGrid(w.alsoGID)
		}
	}
	if w.then != nil {
		w.then()
	}
	return nil
}

// post runs do in a goroutine — except during beforeunload, where a
// goroutine started here would never be scheduled before the page dies, so
// the write runs inline through the beacon transport instead.
func (a *App) post(w write) {
	if a.unloading {
		a.do(w)
		return
	}
	go a.do(w)
}

// doOnUnload is `do` during beforeunload: the page is dying, so the ordinary
// RPC's reply can never arrive and there is no outbox left to park into,
// since the outbox is session-local and dies with the page too. The write's
// beacon form is what survives; navigator.sendBeacon hands the body to the
// browser, which completes it after the page is gone.
//
// A write with no beacon form, or one the browser refused on its queue
// budget, is fired asynchronously and hoped for — which beats guaranteeing
// the loss — but never waited on: blocking the unload handler on a reply that
// cannot arrive turns a quit into a hang.
func (a *App) doOnUnload(w write) error {
	if w.beacon != nil {
		if path, body, ct := w.beacon(); body != nil && a.sendBeacon(path, body, ct) {
			if w.then != nil {
				w.then()
			}
			return nil
		}
	}
	go w.call(context.Background())
	return nil
}

// postTileMutate is the adapter for the plain single-grid tile mutations
// (every Create*, a resize): no claim, no parked value — the tile either
// appears or the failure notice says it did not — and a refetch on success so
// the cache catches up to the server's authoritative row. onSuccess, which
// may be nil, fires with the response tile.
func (a *App) postTileMutate(label string, gid string, call tileCall, onSuccess func(rpc.Tile)) {
	var tile *rpc.Tile
	// The gesture is not over while the row is still being made: the descent,
	// the visit, or the placement it leads to happens in onSuccess. Counting
	// it in flight is what lets a caller — the e2e's idle signal — tell "the
	// gesture finished" from "the gesture is still waiting on the server".
	// The release is the dispatcher's own, on every path out, and these
	// writes never park, so no retry can double-count one.
	a.tileMutates++
	a.post(write{
		label: label, gid: gid, refetchOnOK: true,
		done: func() { a.tileMutates-- },
		call: func(ctx context.Context) error {
			var err error
			tile, err = call(ctx)
			return err
		},
		then: func() {
			if onSuccess != nil && tile != nil {
				onSuccess(*tile)
			}
		},
	})
}

// postFramingPersist dispatches a settle-persister framing write: the caller
// already patched the cache, so a server verdict rolls that patch back to
// truth, and a transport failure keeps it and parks the write. id is the
// doorway tile's id, or the root grid's id when the framing lives on a grid
// row — one dispatcher for both rows framing can live on.
//
// Framing carries no version claim, so there is no conflict to re-claim
// through.
//
// beacon is the write's unload form, or nil when it has none: the transport
// choice is the dispatcher's, so a framing write reaches the beacon whether
// it is posted fresh or drained out of the outbox at unload. Every settle
// persister passes one except content zoom, which has no *Beacon builder.
func (a *App) postFramingPersist(label, gid, id string,
	call func(ctx context.Context) error, beacon func() (string, []byte, string)) {
	a.persistPosts[label]++
	a.post(write{label: label, gid: gid, id: id, optimistic: true, call: call, beacon: beacon})
}

// recordContent and syncContentOutbox are the shim halves of the two rules in
// client/outbox: read the dirtiness from the cache, hand the outbox the retry
// thunk, and let it decide. Every path that can change dirtiness calls
// recordContent — the keystroke that makes an entry dirty, and each completed
// save, which leaves it clean, drops it on a verdict, or leaves it dirty on a
// transport failure — and none of them has to know which happened.
func (a *App) recordContent(cid string) {
	_, dirty := a.c.DirtyContent(cid)
	a.out.RecordContent(cid, dirty, a.flushContent(cid))
}

func (a *App) syncContentOutbox() {
	a.out.SyncContent(a.c.DirtyTileIDs(), a.flushContent)
}

// flushContent is one tile's retry thunk: re-read the bytes from the cache and
// send them. The outbox holds order and retry, never a copy of the user's
// words.
func (a *App) flushContent(cid string) func() {
	return func() { a.flushTileContent(cid) }
}

// putEditedContent is the one door for an optimistic local edit: store the
// bytes with their owner and record that the server is owed them. Splitting
// the two would let an edit typed during an outage stay out of the outbox and
// ride a separate dirty sweep with its own retry rule.
func (a *App) putEditedContent(cid string, data []byte) {
	a.c.PutEditedContent(cid, data)
	a.recordContent(cid)
}

// postWriteContent fires the one content write — WriteContent: id-addressed,
// version-claimed, commit-at-close — and, on success, replaces the cached
// blob so renderers reflect the new content immediately. On a version
// conflict it triggers a grid refetch. Returns the updated tile and ok=true
// on success; ok=false on any failure, and callers stop further work.
//
// This is the one path that carries a version claim, because content bytes
// are the one thing version means. Its outbox bookkeeping is recordContent's:
// the cache entry's dirtiness is whether the write is still owed, so all
// three outcomes below resolve through one line.
func (a *App) postWriteContent(gid, tileID string, version int64, newContent []byte) (rpc.Tile, bool) {
	tile, err := a.cl.WriteContent(context.Background(), tileID, version, newContent)
	if err != nil {
		o := clientsync.Of(err)
		r := clientsync.ReactSave(o)
		if r.Log {
			a.surfaceRPCError("WriteContent", err)
		}
		if r.DropLocal {
			// The server gave a verdict: the screen is showing bytes it
			// refused. Drop them so the next render refetches server truth,
			// since a grid refetch alone never evicts content. Otherwise
			// the rejected edit lingers looking saved and vanishes on some
			// later refetch. ReactSave logs on a conflict too, so the
			// notice above always tells the user why their text reverted.
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
			// dirty — it is the only copy of the user's unsaved words, and
			// the textarea is a view of it, not an owner — so the flush
			// sweep re-posts it on the next tick and the retry kick drains
			// it on reconnect. Dropping it here would let a wifi blip
			// during autosave destroy the paragraph and repaint the
			// textarea from stale server bytes.
			a.reportErr(errsurface.Info, "textsave",
				"unsaved changes kept — server unreachable, will retry")
		}
		a.recordContent(tileID)
		return rpc.Tile{}, false
	}
	// Advance the cached tile and the save basis to the response row now,
	// not when the event echo lands. Text saves are serialized per tile and
	// claim their basis at send time, which only chains if the previous
	// write's response has already moved the basis forward. The tile is
	// cached under the response row's own grid, not the caller's gid: for a
	// save routed through a leaf link the response row lives in the target's
	// foreign grid, and writing it under the link's grid would plant a
	// foreign tile row in the wrong grid map.
	a.c.UpdateTile(tile.GridID, *tile)
	a.c.PutSavedContent(tile.ID, newContent, tile.Version)
	a.recordContent(tile.ID)
	return *tile, true
}

// enqueueTextSave posts a content write through the per-tile serial queue.
// The version is claimed at send time — after any earlier write for the same
// tile has completed and advanced the save basis — so pipelined saves chain
// versions instead of both claiming the same one and losing the second edit
// to a conflict reconcile.
//
// The claim is the cache's SaveBasis, the version of the bytes this edit is
// based on, never the grid row version. The row advances when a foreign
// writer's event or a grid refetch lands, without this client ever seeing the
// new content, so claiming it would carry the current version with stale
// bytes past the server's concurrency check and destroy the foreign edit.
// Claiming the basis makes that save conflict instead, which reconciles
// visibly. fallbackVersion, the enqueue-time row version, is used only if the
// content entry is gone, so the server still sees the write and surfaces the
// real story.
func (a *App) enqueueTextSave(gid string, tileID string, fallbackVersion int64, data []byte) {
	a.textSaves.Enqueue(tileID, func() {
		version := fallbackVersion
		if base, ok := a.c.SaveBasis(tileID); ok {
			version = base
		}
		a.postWriteContent(gid, tileID, version, data)
	})
}
