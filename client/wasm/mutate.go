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

// This file is the mutation dispatch layer, and since docs/simplify-plan.md
// S5 it is TWO paths, not a family of six:
//
//   - do / post — every write that carries no version claim: framing, an
//     automatic capture, a layout move, a create, a delete. One policy body.
//   - postWriteContent — the one write that claims a version, because it is
//     the only one that changes the user's content bytes.
//
// The *decision* — what an outcome was and what it permits — is the pure
// clientsync package (Of + the React* tables); the *record* of what the
// server has not yet acknowledged is the pure client/outbox. This layer is
// the glue that applies both against App state.
//
// The rule both paths serve (2026-08-14, the transport-loss class): LOCAL
// STATE IS DROPPED ONLY ON A SERVER VERDICT, never on a transport failure.
// There used to be six dispatchers because there were six version-claim
// stories; there is one claim story now, so the shapes that differed only in
// their retry ceremony collapsed into `write`.

// tileCall is the closure shape a tile-producing mutation takes. Callers wrap
// the matching a.cl method (CreateText, PlaceTile, …) so the dispatcher
// doesn't need to know the request type.
type tileCall func(ctx context.Context) (*rpc.Tile, error)

// write is ONE non-content mutation and everything the dispatcher needs in
// order to react to it. Policy (which reaction table, whether the write
// parks, what the notices say) lives in `do`; `then` and `undo` are the
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
	// id keys the outbox entry: a tile id, or a GRID id for a root framing
	// write (the two are separate sequences, so the key must carry which).
	// EMPTY means this write is not parked — its value is still on screen
	// and the failure notice IS the reconcile (a create; a drag whose ghost
	// snaps back). Nothing user-made may be parked-less unless the user can
	// see that it did not happen.
	id string
	// optimistic marks a caller that patched the cache BEFORE the RPC, so a
	// server verdict must roll the cache back to truth (issue #156).
	optimistic bool
	// refetchOnOK refetches gid (and alsoGID) after the write lands.
	refetchOnOK bool
	// call is the RPC.
	call func(ctx context.Context) error
	// then runs after a successful call, before the refetch is scheduled.
	then func()
	// undo runs after a failure — the visible reconcile for a write that
	// showed the user something optimistic with no cache patch behind it
	// (the drag ghost snapping back to its origin). It is the ALTERNATIVE to
	// parking: a write that reconciles visibly must not also be retried
	// later, so `undo` and `id` are never both set.
	undo func()
	// source/failText name the DOMAIN notice for a write whose failure the
	// user must see in its own words ("shell preview save failed"), on top
	// of the generic rpc: notice. Empty source = the generic notice alone.
	source, failText string
	// beacon is this write's navigator.sendBeacon form — the transport that
	// survives the page (api/rpc's *Beacon builders: one request builder,
	// two transports; contentType picks unary proto-JSON or the WriteContent
	// streaming envelope). Consulted ONLY during beforeunload. A write with
	// no beacon form is fired and hoped for at unload; it can never block the
	// unload handler waiting for a reply that cannot arrive.
	beacon func() (path string, body []byte, contentType string)
}

// isUnimplemented reports a plugin's "I don't serve this" answer — a
// capability property, never a failure to surface. The judgment is
// clientsync's (the one wire-code classifier).
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
	if a.unloading {
		return a.doOnUnload(w)
	}
	err := w.call(context.Background())
	o := clientsync.Of(err)

	if w.id != "" {
		// The closure holds the captured payload — a settled viewport, a
		// freeze's jpeg/url/title/trail, a workspace arrangement, a typed
		// name — which is the ONLY copy once the gesture that made it is
		// over. Abandoning it on a blip was audit bug #2 (2026-08-14).
		a.out.Record(o, outbox.Key{Op: w.label, ID: w.id}, func() { a.post(w) })
	}

	r := clientsync.React(o)
	if w.optimistic {
		r = clientsync.ReactOptimistic(o)
	}
	if r.Refetch {
		a.refetchGridOnConflict(w.gid, w.label)
	}
	// A write with its own words says them; the generic rpc: notice rides
	// along on a VERDICT (the developer half of "why did that fail") but not
	// on a transport blip, where "will retry" is the whole story and a
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
	// The refetch is scheduled BEFORE `then`, the order the plain tile
	// dispatcher always used: a success hook that relocates a pane wants the
	// catch-up fetch already in flight.
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
// RPC's reply can never arrive and there is no outbox left to park into (the
// outbox is session-local — it dies with the page too). The write's BEACON
// form is what survives; navigator.sendBeacon hands the body to the browser,
// which completes it after the page is gone.
//
// A write with no beacon form, or one the browser refused (the queue budget),
// is fired asynchronously and hoped for — it beats guaranteeing the loss —
// but never waited on: blocking the unload handler on a reply that cannot
// arrive is how a quit turns into a hang.
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
// appears or the failure notice says it didn't — and a refetch on success so
// the cache catches up to the server's authoritative row. onSuccess (nil ok)
// fires with the response tile.
func (a *App) postTileMutate(label string, gid string, call tileCall, onSuccess func(rpc.Tile)) {
	var tile *rpc.Tile
	a.post(write{
		label: label, gid: gid, refetchOnOK: true,
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

// postFramingPersist dispatches a settle-persister FRAMING write: the caller
// already patched the cache, so a server verdict rolls that patch back to
// truth, and a transport failure keeps it and parks the write. id is the
// doorway TILE's id, or the ROOT GRID's id when the framing lives on a grid
// row — one dispatcher for both rows framing can live on.
//
// Framing carries no version claim (docs/simplify-plan.md S5), so there is no
// conflict to re-claim through: the one-retry rule this path used to need
// existed only because automatic captures bumped the row out from under a
// settle.
func (a *App) postFramingPersist(label, gid, id string, call func(ctx context.Context) error) {
	a.postFramingPersistBeacon(label, gid, id, call, nil)
}

// postFramingPersistBeacon is postFramingPersist for a write that HAS an
// unload form. Every settle persister passes one: the transport choice is the
// dispatcher's, so a framing write reaches the beacon whether it is being
// posted fresh or drained out of the outbox at unload. (Content zoom has no
// beacon builder and rides the plain form.)
func (a *App) postFramingPersistBeacon(label, gid, id string,
	call func(ctx context.Context) error, beacon func() (string, []byte, string)) {
	a.persistPosts[label]++
	a.post(write{label: label, gid: gid, id: id, optimistic: true, call: call, beacon: beacon})
}

// recordContent syncs the tile's OUTBOX entry to the one owner of its bytes,
// the cache's content entry: still dirty means the server is still owed this
// write, clean means it is not. The outbox therefore knows WHICH tiles owe a
// write and in what order, while the bytes stay where the renderer reads them
// — one fact, one owner, and no second copy of the user's words (charter §1).
//
// Every path that can change dirtiness calls this: the keystroke that makes
// an entry dirty, and each completed save (which leaves it clean, drops it on
// a verdict, or leaves it dirty on a transport failure — the three outcomes
// that decide parked-or-acked without this function having to know which
// happened).
func (a *App) recordContent(cid string) {
	k := outbox.Key{Op: outbox.OpContent, ID: cid}
	if _, dirty := a.c.DirtyContent(cid); dirty {
		a.out.Park(k, func() { a.flushTileContent(cid) })
		return
	}
	a.out.Ack(k)
}

// syncContentOutbox re-derives the outbox's content entries from their one
// owner, the cache. recordContent already runs on every path that changes
// dirtiness, so this is belt and braces before a drain — and it is worth
// having: a drift between the two would cost exactly the words the user typed
// last, silently, at the one moment (a quit) with no next sweep behind it.
func (a *App) syncContentOutbox() {
	for _, cid := range a.c.DirtyTileIDs() {
		a.recordContent(cid)
	}
}

// putEditedContent is the ONE door for an optimistic local edit: store the
// bytes with their owner and record that the server is owed them. Splitting
// the two was how an edit typed during an outage stayed out of the outbox and
// rode a separate dirty-sweep with its own retry rule.
func (a *App) putEditedContent(cid string, data []byte) {
	a.c.PutEditedContent(cid, data)
	a.recordContent(cid)
}

// postWriteContent fires the one content write (WriteContent — id-addressed,
// version-claimed, commit-at-close; 2026-07-26 redesign) and, on success,
// replaces the cached blob so renderers reflect the new content immediately.
// On version-conflict it triggers a grid refetch. Returns the updated
// tile and ok=true on success; ok=false on any failure (callers
// should stop further work).
//
// This is the ONE path that carries a version claim, because content bytes
// are the one thing version means (docs/simplify-plan.md S5). Its outbox
// bookkeeping is recordContent's: the cache entry's dirtiness IS whether the
// write is still owed, so all three outcomes below resolve through one line.
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
			// ReactSave logs on a conflict too, so the notice above always
			// tells the user why their text just reverted.
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
		a.recordContent(tileID)
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
	a.recordContent(tile.ID)
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
