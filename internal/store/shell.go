package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// CreateShell creates a new shell tile. The bash session is NOT
// started here — matching the URL tile model, the tile begins frozen
// with an empty preview, and an explicit refresh from the client
// kicks off the PTY. Once kicked off, the bash session lives in a
// gridwell-private tmux session keyed by the tile id and survives
// ascents until the tile is deleted (or the machine reboots, which
// takes the whole tmux server with it).
func (s *Store) CreateShell(ctx context.Context, req *rpc.CreateShellRequest) (*rpc.Tile, error) {
	return s.createTile(ctx, req.GridID, req.X, req.Y, req.W, req.H, req.ObjectID,
		func(tx *sql.Tx, gridID, now int64, objID string) (int64, error) {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h,
					alt_text, created_at, updated_at)
				VALUES (?, ?, 'shell', ?, ?, ?, ?, ?, ?, ?)`,
				objID, gridID, req.X, req.Y, req.W, req.H, "shell", now, now)
			if err != nil {
				return 0, fmt.Errorf("insert shell tile: %w", err)
			}
			return res.LastInsertId()
		})
}

// SetShellPreview overwrites the frozen-state JPEG. Bytes are hash-
// deduped through the blobs table the same way URL preview bytes are.
// Empty JPEG (length 0) clears the preview — useful as a reset after a
// failed refresh.
//
// Unlike most mutations this one intentionally does NOT consult
// req.Version. The preview is a side-channel content blob; the
// authoritative concurrency primitive for shell tiles is the
// WebSocket session (one live PTY per tile at a time). The field
// stays on the wire so clients can still observe versions through
// TileChanged.
func (s *Store) SetShellPreview(ctx context.Context, req *rpc.SetShellPreviewRequest) (*rpc.Tile, error) {
	tileID, err := parseID(req.TileID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		if n.Kind != rpc.KindShell {
			return ErrNotShellTile
		}

		if len(req.JPEG) > 0 {
			if _, _, err := s.swapTileBlob(ctx, tx, tileID, "preview_blob_id", req.JPEG, mediaJPEG); err != nil {
				return err
			}
		} else {
			// An empty capture (failed refresh) clears the frozen frame to
			// NULL — unlike URL, which skips empties to preserve the last
			// good frame. swapTileBlob can't express a NULL set, so the
			// clear stays explicit. Drop the reference (NULL) BEFORE releasing
			// the blob, or the FK trips when decBlobRefcount GCs it.
			if _, err := tx.ExecContext(ctx,
				`UPDATE tiles SET preview_blob_id = NULL, updated_at = ? WHERE id = ?`,
				s.now().Unix(), tileID); err != nil {
				return err
			}
			if n.PreviewBlobID != 0 {
				if err := s.decBlobRefcount(ctx, tx, n.PreviewBlobID); err != nil {
					return err
				}
			}
		}
		out, err = s.finishContentEdit(ctx, tx, tileID, events)
		return err
	})
	return out, err
}
