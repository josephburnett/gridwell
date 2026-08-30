package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/josephburnett/gridwell/api/panelayout"
	"github.com/josephburnett/gridwell/api/rpc"
)

// This file is the store side of the 'pane' tile kind: a tile whose content
// blob is a serialized split-pane layout, in the api/panelayout format under
// panelayout.LayoutMediaType. The store treats the layout as opaque bytes —
// the codec, the id-relativity rule, and the restore semantics live in
// client/pane — so here it is one more content-addressed blob.

// CreatePane creates a pane tile. data is the optional initial layout blob;
// empty leaves blob_id NULL, meaning never arranged, and descent installs the
// default single pane. alt is the user-given name, which the bar's crumb
// reads.
func (s *Store) CreatePane(ctx context.Context, gridID string, x, y, w, h int64, alt string, data []byte) (*rpc.Tile, error) {
	if int64(len(data)) > MaxBlobBytes {
		return nil, fmt.Errorf("%w: layout too large", ErrInvalidArgument)
	}
	return s.createTile(ctx, gridID, x, y, w, h,
		func(tx *sql.Tx, gid, now int64) (int64, error) {
			var blob sql.NullInt64
			if len(data) > 0 {
				blobID, err := s.putBlob(ctx, tx, hashBytes(data), data, panelayout.LayoutMediaType)
				if err != nil {
					return 0, err
				}
				blob = sql.NullInt64{Int64: blobID, Valid: true}
			}
			res, err := tx.ExecContext(ctx, `
				INSERT INTO tiles (grid_id, kind, x, y, w, h,
					blob_id, alt_text, created_at, updated_at)
				VALUES (?, 'pane', ?, ?, ?, ?, ?, ?, ?, ?)`,
				gid, x, y, w, h, blob, alt, now, now)
			if err != nil {
				return 0, fmt.Errorf("insert pane tile: %w", err)
			}
			tileID, err := res.LastInsertId()
			if err != nil {
				return 0, err
			}
			if blob.Valid {
				if err := s.incBlobRefcount(ctx, tx, blob.Int64); err != nil {
					return 0, err
				}
			}
			return tileID, nil
		})
}

// SetPaneLayout writes a pane tile's layout blob. It is framing-class: the
// whole layout is an arrangement of references to other content, so it goes
// through emitTileChanged and never bumps version. There is no layout history;
// the layout is edited in place. It carries no claim either, because framing
// is last-writer-wins and version is the content claim only. The version
// parameter survives as WriteContent's kind-dispatched signature and this arm
// ignores it. Identical bytes are a pure no-op, since swapTileBlob dedups, so
// the client's hash-diff persister and a pure re-save cannot churn the DB.
func (s *Store) SetPaneLayout(ctx context.Context, tileID, version int64, data []byte) (*rpc.Tile, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty layout", ErrInvalidArgument)
	}
	if int64(len(data)) > MaxBlobBytes {
		return nil, fmt.Errorf("%w: layout too large", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err := s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.loadForWrite(ctx, tx, tileID, "", nil)
		if err != nil {
			return err
		}
		if n.Kind != rpc.KindPane {
			return ErrNotPaneTile
		}
		if _, _, err := s.swapTileBlob(ctx, tx, tileID, "blob_id", data, panelayout.LayoutMediaType); err != nil {
			return err
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// WorkspaceEphemeralRefs returns the set of local tile ids referenced as a
// content descent by any pane tile's layout blob in this store. The boot
// scratch sweep reads it to spare pane-tile-owned ephemerals. The blob is the
// one record of that ownership, with no second bookkeeping table, so a
// reference dies exactly when its pane tile — or the arrangement that named it
// — does, and the next sweep reclaims the tile. unreadable is true when any
// pane blob failed to decode, whether corrupt or written by a newer Gridwell;
// the caller must then reap nothing, because a wrongly-swept shell is a killed
// process and unrecoverable, while a delayed sweep is not.
func (s *Store) WorkspaceEphemeralRefs(ctx context.Context) (refs map[string]bool, unreadable bool, err error) {
	uuid, err := s.PluginUUID(ctx)
	if err != nil {
		return nil, false, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT blob_id FROM tiles WHERE ns = '' AND kind = 'pane' AND blob_id IS NOT NULL`)
	if err != nil {
		return nil, false, fmt.Errorf("workspace refs: %w", err)
	}
	defer rows.Close()
	var blobIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, false, err
		}
		blobIDs = append(blobIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	refs = map[string]bool{}
	for _, blobID := range blobIDs {
		data, _, err := s.GetBlobWithMedia(ctx, blobID)
		if err != nil {
			unreadable = true
			continue
		}
		ids, err := panelayout.TextFocusIDs(data)
		if err != nil {
			unreadable = true
			continue
		}
		for _, id := range ids {
			// Blob ids are qualified in the owning node's frame; only
			// same-namespace references resolve to rows in this store.
			if u, local, ok := rpc.SplitID(id); ok && u == uuid {
				refs[local] = true
			}
		}
	}
	return refs, unreadable, nil
}
