package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/panelayout"
	"github.com/josephburnett/gridwell/api/rpc"
)

// The store side of the workspace tile kind: a tile whose content blob is an
// api/panelayout split-pane layout. The store treats it as opaque bytes; the
// codec, the id-relativity rule and the restore semantics live in client/pane.

// CreateWorkspace creates a workspace tile. Empty data leaves blob_id NULL, meaning
// never arranged, and descent installs the default single pane.
func (s *Store) CreateWorkspace(ctx context.Context, gridID string, x, y, w, h int64, alt string, named bool, data []byte) (*gridwellv1.Tile, error) {
	if int64(len(data)) > MaxBlobBytes {
		return nil, fmt.Errorf("%w: layout too large", ErrInvalidArgument)
	}
	return s.createTile(ctx, gridID, x, y, w, h, named,
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
				VALUES (?, 'workspace', ?, ?, ?, ?, ?, ?, ?, ?)`,
				gid, x, y, w, h, blob, alt, now, now)
			if err != nil {
				return 0, fmt.Errorf("insert workspace tile: %w", err)
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

// SetWorkspaceLayout writes a workspace tile's layout blob. A layout is the user's
// bytes, so it claims the version it was read at and bumps it, as a text save
// does. Identical bytes are a pure no-op, so a re-save cannot churn the DB.
func (s *Store) SetWorkspaceLayout(ctx context.Context, tileID, version int64, data []byte) (*gridwellv1.Tile, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty layout", ErrInvalidArgument)
	}
	if int64(len(data)) > MaxBlobBytes {
		return nil, fmt.Errorf("%w: layout too large", ErrInvalidArgument)
	}
	return s.claimContentVersion(ctx, "SetWorkspaceLayout", tileID, version, func(tx *sql.Tx, n *gridwellv1.Tile, events *[]*gridwellv1.Event) (*gridwellv1.Tile, error) {
		if rpc.KindOf(n.Kind).Content != rpc.ContentLayout {
			return nil, ErrNotWorkspace
		}
		_, changed, err := s.swapTileBlob(ctx, tx, tileID, "blob_id", data, panelayout.LayoutMediaType)
		if err != nil {
			return nil, err
		}
		if !changed {
			return n, nil
		}
		return s.finishContentEdit(ctx, tx, tileID, events)
	})
}

// WorkspaceEphemeralRefs returns the local tile ids any workspace tile's layout
// blob holds (panelayout.HeldIDs). The boot scratch sweep reads it to
// spare pane-owned ephemerals. The blob is the one record of that ownership,
// so a reference dies exactly when its workspace tile does. unreadable is true when
// any pane blob failed to decode, and the caller must then reap nothing: a
// wrongly-swept shell is a killed process, while a delayed sweep is not.
func (s *Store) WorkspaceEphemeralRefs(ctx context.Context) (refs map[string]bool, unreadable bool, err error) {
	uuid, err := s.NodeUUID(ctx)
	if err != nil {
		return nil, false, err
	}
	layouts, err := workspaceLayouts(ctx, s.db)
	if err != nil {
		return nil, false, err
	}
	refs = map[string]bool{}
	for _, data := range layouts {
		ids, ok := layoutRefs(uuid, data)
		unreadable = unreadable || !ok
		for _, id := range ids {
			refs[id] = true
		}
	}
	return refs, unreadable, nil
}

// workspaceLayouts is every workspace tile's layout by its row, in one
// statement, so each is the blob its row names at one instant (see
// readSnapshot); nil is a row whose blob is missing.
func workspaceLayouts(ctx context.Context, q gridReader) (map[int64][]byte, error) {
	rows, err := q.QueryContext(ctx, `SELECT t.id, b.data FROM tiles t LEFT JOIN blobs b ON b.id = t.blob_id
		WHERE t.key = '' AND t.kind = 'workspace' AND t.blob_id IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("workspace layouts: %w", err)
	}
	out := map[int64][]byte{}
	for rows.Next() {
		var id int64
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			rows.Close()
			return nil, err
		}
		out[id] = data
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// layoutRefs is the home rows of node a layout holds, by their local ids; ok
// is false for a layout that will not read, nil being one whose blob is
// missing. Blob ids are qualified in the owning node's frame, so only node's
// own references are rows of this store.
func layoutRefs(node string, data []byte) (ids []string, ok bool) {
	if data == nil {
		return nil, false
	}
	named, err := panelayout.HeldIDs(data)
	if err != nil {
		return nil, false
	}
	for _, id := range named {
		if u, local, ok := rpc.SplitID(id); ok && u == node {
			ids = append(ids, local)
		}
	}
	return ids, true
}
