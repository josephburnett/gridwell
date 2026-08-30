package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/josephburnett/gridwell/api/rpc"
)

// CreateShell creates a new shell tile. The session is not started here: the
// tile begins frozen with an empty preview, and an explicit refresh from the
// client starts the PTY. Once started, the session lives in a
// gridwell-private tmux session keyed by the tile id and survives ascents
// until the tile is deleted, or the machine reboots and takes the whole tmux
// server with it.
func (s *Store) CreateShell(ctx context.Context, req *rpc.CreateShellRequest) (*rpc.Tile, error) {
	return s.createTile(ctx, req.GridID, req.X, req.Y, req.W, req.H,
		func(tx *sql.Tx, gridID, now int64) (int64, error) {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO tiles (grid_id, kind, x, y, w, h,
					alt_text, created_at, updated_at)
				VALUES (?, 'shell', ?, ?, ?, ?, ?, ?, ?)`,
				gridID, req.X, req.Y, req.W, req.H, "shell", now, now)
			if err != nil {
				return 0, fmt.Errorf("insert shell tile: %w", err)
			}
			return res.LastInsertId()
		})
}

// SetShellPreview overwrites the frozen-state JPEG. Bytes are hash-deduped
// through the blobs table the same way url preview bytes are. An empty JPEG
// clears the preview, which is the reset after a failed refresh.
//
// The frozen frame is a capture — what the terminal was observed to look like
// when the user left — so it carries no version claim and makes no version
// bump. It rides the tile event to every client as last-writer-wins state,
// which is the right answer for a tile whose real concurrency primitive is the
// live PTY session, one per tile at a time.
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
			// An empty capture, from a failed refresh, clears the frozen frame
			// to NULL. A url tile skips empties instead, to preserve the last
			// good frame. swapTileBlob cannot express a NULL set, so the clear
			// stays explicit. Drop the reference to NULL before releasing the
			// blob, or the foreign key trips when decBlobRefcount collects
			// it.
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
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}
