//go:build js && wasm

package main

import (
	"context"
	"errors"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/inflight"
	"github.com/josephburnett/gridwell/client/outbox"
	"github.com/josephburnett/gridwell/client/textedit"
)

// Mutation dispatch. do and post carry no version claim; postWriteContent is
// the one write that claims one, because content bytes are what version
// means. Local state is dropped only on a server verdict, never on a
// transport failure.

// tileCall is the closure shape a tile-producing mutation takes, so the
// dispatcher does not need to know the request type.
type tileCall func(ctx context.Context) (*gridwellv1.Tile, error)

// write is one non-content mutation. Policy lives in `do`; `then` and `undo`
// are the caller's.
type write struct {
	// label names the op in the notice strip and the outbox key.
	label string
	// gid is the grid whose cache reconciles on a server verdict.
	gid string
	// alsoGID is a second grid the write touched. Empty, or equal to gid, is
	// one grid.
	alsoGID string
	// id keys the outbox entry: a tile id, or a grid id for a root framing
	// write. Empty parks nothing, allowed only when the failure notice is the
	// reconcile.
	id string
	// optimistic marks a caller that patched the cache before the RPC, so a
	// verdict rolls it back.
	optimistic  bool
	refetchOnOK bool
	call        func(ctx context.Context) error
	// then runs after a successful call, once the refetch is scheduled.
	then func()
	// done runs once the write has finished, landed, failed or parked. It is
	// the release half of an in-flight count.
	done func()
	// undo is the visible reconcile for a failure, such as a drag ghost
	// snapping back. It is the alternative to parking, so `undo` and `id` are
	// never both set.
	undo func()
	// source and failText name the domain notice for a failure the user must
	// see in its own words. Empty means the generic rpc: notice alone.
	source, failText string
	// beacon is this write's sendBeacon form, consulted only during
	// beforeunload. A write with none is fired and hoped for, never waited
	// on.
	beacon func() (path string, body []byte, contentType string)
}

// isUnimplemented reports a plugin's "I don't serve this" answer: a
// capability property, never a failure to surface.
func isUnimplemented(err error) bool { return clientsync.IsUnimplemented(err) }

// surfaceRPCError puts a failed RPC on the errsurface strip. clientsync's
// tables never route a conflict here.
func (a *App) surfaceRPCError(label string, err error) {
	if err == nil {
		return
	}
	a.reportErr(errsurface.Error, "rpc:"+label, label+" failed: "+rpcErrText(err))
}

// rpcErrText strips the Connect wire prefix down to readable failure text.
func rpcErrText(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Message()
	}
	return err.Error()
}

// do runs one non-content mutation: park it in the outbox, react per
// clientsync's table, surface what failed, run then/undo. Blocking; `post` is
// the goroutine form. Bounded, so a request the network swallows cannot leave
// a write parked with no verdict.
func (a *App) do(w write) error {
	if w.done != nil {
		defer w.done()
	}
	if a.unloading {
		return a.doOnUnload(w)
	}
	var err error
	// Once the gesture is over this closure holds the only copy of the
	// payload.
	var retry func()
	if w.id != "" {
		retry = func() { a.post(w) }
	}
	o := a.persist.out.Send(outbox.Key{Op: w.label, ID: w.id}, retry, func() clientsync.Outcome {
		ctx, cancel := inflight.Bounded()
		defer cancel()
		err = w.call(ctx)
		return clientsync.Of(err)
	})

	r := clientsync.React(o)
	if w.optimistic {
		r = clientsync.ReactOptimistic(o)
	}
	// clientsync.NoticesFor says which notices this outcome posts.
	n := clientsync.NoticesFor(r, o, w.source != "")
	if n.Reloaded {
		a.refetchGridOnConflict(w.gid, w.label)
	} else if r.Refetch {
		a.fetchGrid(w.gid)
	}
	if n.Generic {
		a.surfaceRPCError(w.label, err)
	}
	switch n.Own {
	case clientsync.OwnRetry:
		a.reportErr(errsurface.Info, w.source, w.failText+": server unreachable — will retry")
	case clientsync.OwnFailed:
		a.reportErr(errsurface.Error, w.source, w.failText+": "+rpcErrText(err))
	}
	if err != nil {
		if w.undo != nil {
			w.undo()
		}
		return err
	}
	// Before `then`: a hook that relocates a pane wants the fetch already in
	// flight.
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

// post runs do in a goroutine, except during beforeunload, where a goroutine
// would never be scheduled before the page dies.
func (a *App) post(w write) {
	if a.unloading {
		a.do(w)
		return
	}
	go a.do(w)
}

// doOnUnload is `do` during beforeunload. The reply can never arrive and the
// outbox dies with the page, so the beacon form is what survives. A write
// without one is fired and never waited on: blocking the unload handler turns
// a quit into a hang.
func (a *App) doOnUnload(w write) error {
	if w.beacon != nil {
		if path, body, ct := w.beacon(); body != nil && a.sendBeacon(path, body, ct) {
			if w.then != nil {
				w.then()
			}
			return nil
		}
	}
	go func() {
		ctx, cancel := inflight.Bounded()
		defer cancel()
		w.call(ctx)
	}()
	return nil
}

// postTileMutate is the adapter for the plain single-grid tile mutations: no
// claim, no parked value, a refetch on success.
func (a *App) postTileMutate(label string, gid string, call tileCall, onSuccess func(*gridwellv1.Tile)) {
	var tile *gridwellv1.Tile
	// The gesture is not over while the row is being made, since the descent
	// happens in onSuccess. The e2e idle signal reads this count.
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
				onSuccess(tile)
			}
		},
	})
}

// postFramingPersist dispatches a settle-persister framing write. id is the
// doorway tile's id, or the root grid's id when the framing lives on a grid
// row. beacon is nil only for content zoom, which has no *Beacon builder.
func (a *App) postFramingPersist(label, gid, id string,
	call func(ctx context.Context) error, beacon func() (string, []byte, string)) {
	a.persist.persistPosts[label]++
	a.post(write{label: label, gid: gid, id: id, optimistic: true, call: call, beacon: beacon})
}

// recordContent and syncContentOutbox read dirtiness from the cache and hand
// client/outbox the retry thunk. Every path that can change dirtiness calls
// recordContent, so none knows which outcome happened.
func (a *App) recordContent(cid string) {
	_, dirty := a.c.DirtyContent(cid)
	a.persist.out.RecordContent(cid, dirty, a.flushContent(cid))
}

func (a *App) syncContentOutbox() {
	a.persist.out.SyncContent(a.c.DirtyTileIDs(), a.flushContent)
}

// flushContent is one tile's retry thunk. The outbox holds order and retry,
// never a copy of the user's words.
func (a *App) flushContent(cid string) func() {
	return func() { a.flushTileContent(cid) }
}

// putEditedContent is the one door for an optimistic local edit. Splitting
// the store from the outbox record would give an edit typed during an outage
// its own retry rule.
func (a *App) putEditedContent(cid string, data []byte) {
	a.c.PutEditedContent(cid, data)
	a.recordContent(cid)
}

// postWriteContent fires the one version-claimed write and on success
// replaces the cached blob. Its outbox bookkeeping is recordContent's: the
// entry's dirtiness is whether the write is still owed. Bounded, doubly so
// because saves for one document run on a serial queue.
func (a *App) postWriteContent(gid, tileID string, version int64, newContent []byte) (*gridwellv1.Tile, bool) {
	ctx, cancel := inflight.Bounded()
	defer cancel()
	tile, err := a.cl.WriteContent(ctx, tileID, version, newContent)
	if err != nil {
		o := clientsync.Of(err)
		r := clientsync.ReactSave(o)
		if r.Log {
			a.surfaceRPCError("WriteContent", err)
		}
		if r.DropLocal {
			// The server refused these bytes, and a grid refetch alone
			// never evicts content, so the rejected edit would linger
			// looking saved.
			a.c.DropTileContent(tileID)
			if clientsync.NoticesFor(r, o, false).Reloaded {
				a.refetchGridOnConflict(gid, "WriteContent")
			} else {
				a.fetchGrid(gid)
			}
			a.refreshFileOverlay()
			a.scheduleFrame()
		} else {
			// The server never spoke, so the entry stays dirty and the
			// flush sweep re-posts it. It is the only copy of the user's
			// unsaved words.
			a.reportErr(errsurface.Info, "textsave",
				"unsaved changes kept — server unreachable, will retry")
		}
		a.recordContent(tileID)
		return nil, false
	}
	// Advance the save basis now, not on the event echo, because pipelined
	// saves claim it at send time. The tile is cached under the response
	// row's own grid, so a save through a leaf link cannot plant a foreign
	// row in the wrong grid map.
	a.c.UpdateTile(tile.GridId, tile)
	a.c.PutSavedContent(tile.Id, newContent, tile.Version)
	a.recordContent(tile.Id)
	return tile, true
}

// enqueueTextSave posts a content write through the document's serial queue,
// named by textedit.SaveQueueKey. The claim is read at send time, so
// pipelined saves chain versions instead of both claiming the same one.
// rowVersion is the fallback for an entry gone by send time.
func (a *App) enqueueTextSave(gid, tileID, cid string, rowVersion int64, data []byte) {
	a.persist.textSaves.Enqueue(textedit.SaveQueueKey(tileID, cid), func() {
		a.saveClaimedContent(gid, cid, tileID == cid, rowVersion, data)
	})
}

// saveClaimedContent claims a version and posts one text content write.
// Every text write reaches it, so no path can spell the claim differently;
// textedit.SaveClaim owns the rule.
func (a *App) saveClaimedContent(gid, cid string, rowOwnsContent bool, rowVersion int64, data []byte) (*gridwellv1.Tile, bool) {
	basis, haveBasis := a.c.SaveBasis(cid)
	return a.postWriteContent(gid, cid, textedit.SaveClaim(rowOwnsContent, rowVersion, basis, haveBasis), data)
}
