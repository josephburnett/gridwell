package store

import (
	"context"
	"database/sql"
	"fmt"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/panelayout"
	"github.com/josephburnett/gridwell/api/rpc"
)

// The store side of the 'pane' tile kind: a tile whose content blob is an
// api/panelayout split-pane layout. The store treats it as opaque bytes; the
// codec, the id-relativity rule and the restore semantics live in client/pane.

// CreatePane creates a pane tile. Empty data leaves blob_id NULL, meaning
// never arranged, and descent installs the default single pane.
func (s *Store) CreatePane(ctx context.Context, gridID string, x, y, w, h int64, alt string, data []byte) (*gridwellv1.Tile, error) {
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
// layout is an arrangement of references to other content, so it never bumps
// version and carries no claim, and the version parameter, which
// WriteContent's signature forces, is ignored. Identical bytes are a pure
// no-op, so a re-save cannot churn the DB.
func (s *Store) SetPaneLayout(ctx context.Context, tileID, version int64, data []byte) (*gridwellv1.Tile, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty layout", ErrInvalidArgument)
	}
	if int64(len(data)) > MaxBlobBytes {
		return nil, fmt.Errorf("%w: layout too large", ErrInvalidArgument)
	}
	var out *gridwellv1.Tile
	err := s.withMutation(ctx, func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
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

// WorkspaceEphemeralRefs returns the local tile ids any pane tile's layout
// blob references as a content descent. The boot scratch sweep reads it to
// spare pane-owned ephemerals. The blob is the one record of that ownership,
// so a reference dies exactly when its pane tile does. unreadable is true when
// any pane blob failed to decode, and the caller must then reap nothing: a
// wrongly-swept shell is a killed process, while a delayed sweep is not.
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
	blobIDs, err := collect(rows, func(rows *sql.Rows) (int64, error) {
		var id int64
		err := rows.Scan(&id)
		return id, err
	})
	if err != nil {
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
