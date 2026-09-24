package store

import (
	"context"
	"database/sql"
	"fmt"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// CreateShell creates a shell tile frozen with an empty preview. An explicit
// refresh from the client starts the PTY; from then on the session lives in a
// gridwell-private tmux session keyed by the tile id and survives ascents
// until the tile is deleted or the machine reboots.
func (s *Store) CreateShell(ctx context.Context, gridID string, x, y, w, h int64) (*gridwellv1.Tile, error) {
	return s.createTile(ctx, gridID, x, y, w, h,
		func(tx *sql.Tx, gid, now int64) (int64, error) {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO tiles (grid_id, kind, x, y, w, h,
					alt_text, created_at, updated_at)
				VALUES (?, 'shell', ?, ?, ?, ?, ?, ?, ?)`,
				gid, x, y, w, h, "shell", now, now)
			if err != nil {
				return 0, fmt.Errorf("insert shell tile: %w", err)
			}
			return res.LastInsertId()
		})
}

// SetShellPreview overwrites the frozen-state JPEG, hash-deduped through the
// blobs table. An empty JPEG clears the preview, the reset after a failed
// refresh. The frame is a capture, not something the user typed, so it carries
// no version claim and makes no bump; a shell's real concurrency primitive is
// the live PTY session, one per tile at a time.
func (s *Store) SetShellPreview(ctx context.Context, tileIDStr string, jpeg []byte) (*gridwellv1.Tile, error) {
	tileID, err := parseID(tileIDStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *gridwellv1.Tile
	err = s.withMutation(ctx, "SetShellPreview", func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		n, err := s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		if n.Kind != rpc.KindShell {
			return ErrNotShellTile
		}

		if len(jpeg) > 0 {
			if _, _, err := s.swapTileBlob(ctx, tx, tileID, "preview_blob_id", jpeg, mediaJPEG); err != nil {
				return err
			}
		} else {
			// An empty capture clears the frozen frame to NULL; a url tile
			// skips empties instead, to preserve the last good frame.
			// swapTileBlob cannot express a NULL set. Drop the reference
			// before releasing the blob, or the foreign key trips when
			// decBlobRefcount collects it.
			if _, err := tx.ExecContext(ctx,
				`UPDATE tiles SET preview_blob_id = NULL, updated_at = ? WHERE id = ?`,
				s.now().Unix(), tileID); err != nil {
				return err
			}
			if n.PreviewBlobId != 0 {
				if err := s.decBlobRefcount(ctx, tx, n.PreviewBlobId); err != nil {
					return err
				}
			}
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}
