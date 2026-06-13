package store

import (
	"context"
	"database/sql"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// SetURLPreview overwrites a URL tile's preview JPEG. Called by the
// server's URL stream handler on WS close. The JPEG is hash-deduped
// through the blobs table — two tiles whose previews happen to be
// identical bytes share one row. Bumps the tile's version.
func (s *Store) SetURLPreview(ctx context.Context, tileID int64, jpeg []byte) error {
	return s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		t, err := s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		if t.Kind != rpc.KindURL {
			return ErrNotURLTile
		}
		oldBlobID := t.PreviewBlobID
		var newBlobID int64
		if len(jpeg) > 0 {
			hash := hashBytes(jpeg)
			newBlobID, err = putBlob(ctx, tx, hash, jpeg)
			if err != nil {
				return err
			}
		}
		var newArg any
		if newBlobID != 0 {
			newArg = newBlobID
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET preview_blob_id = ?, updated_at = ? WHERE id = ?`,
			newArg, s.now().Unix(), tileID); err != nil {
			return err
		}
		if oldBlobID != newBlobID {
			if newBlobID != 0 {
				if err := s.incBlobRefcount(ctx, tx, newBlobID); err != nil {
					return err
				}
			}
			if oldBlobID != 0 {
				if err := s.decBlobRefcount(ctx, tx, oldBlobID); err != nil {
					return err
				}
			}
		}
		if err := bumpTileVersion(ctx, tx, tileID); err != nil {
			return err
		}
		out, err := s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
		return nil
	})
}

// SetURLState freezes a live URL tile in one mutation: it writes the
// preview JPEG (with blob refcounting), the address, and the page title,
// then bumps the version once and publishes a single tile_changed event.
// Empty jpeg/url/title arguments are skipped so a partial capture (e.g. a
// failed final frame) never clobbers good state. This is the RPC the
// Electron shell calls on ascend, replacing the old server-side rod freeze.
//
// Freezing goes through Path + version + preWrite, exactly like a text or
// well edit: a URL tile in a shared (cloned) grid forks the spine so the
// frozen frame and address land in this clone's row only — they used to
// write the shared row and leak into every clone.
func (s *Store) SetURLState(ctx context.Context, req *rpc.SetURLStateRequest) (*rpc.Tile, error) {
	var out *rpc.Tile
	err := s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.checkTileVersion(ctx, tx, req.TileID, req.Version)
		if err != nil {
			return err
		}
		if n.Kind != rpc.KindURL {
			return ErrNotURLTile
		}
		pre, err := s.preWrite(ctx, tx, req.Path, req.TileID)
		if err != nil {
			return err
		}
		*events = append(*events, pre.Events...)
		tileID := pre.TargetTileID

		current, err := s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		if len(req.JPEG) > 0 {
			oldBlobID := current.PreviewBlobID
			hash := hashBytes(req.JPEG)
			newBlobID, err := putBlob(ctx, tx, hash, req.JPEG)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE tiles SET preview_blob_id = ?, updated_at = ? WHERE id = ?`,
				newBlobID, s.now().Unix(), tileID); err != nil {
				return err
			}
			if oldBlobID != newBlobID {
				if err := s.incBlobRefcount(ctx, tx, newBlobID); err != nil {
					return err
				}
				if oldBlobID != 0 {
					if err := s.decBlobRefcount(ctx, tx, oldBlobID); err != nil {
						return err
					}
				}
			}
		}
		if req.URL != "" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE tiles SET url_string = ?, updated_at = ? WHERE id = ?`,
				req.URL, s.now().Unix(), tileID); err != nil {
				return err
			}
		}
		if req.Title != "" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE tiles SET alt_text = ?, updated_at = ? WHERE id = ?`,
				req.Title, s.now().Unix(), tileID); err != nil {
				return err
			}
		}

		if err := bumpTileVersion(ctx, tx, tileID); err != nil {
			return err
		}
		out, err = s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetTileAlt updates a tile's stored alt-text. Used by the shell stream
// handler to bake the tmux foreground command into the tile as its label.
// Bumps the tile's version.
func (s *Store) SetTileAlt(ctx context.Context, tileID int64, alt string) error {
	return s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		if _, err := s.loadTile(ctx, tx, tileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET alt_text = ?, updated_at = ? WHERE id = ?`,
			alt, s.now().Unix(), tileID); err != nil {
			return err
		}
		if err := bumpTileVersion(ctx, tx, tileID); err != nil {
			return err
		}
		out, err := s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
		return nil
	})
}

// Navigation no longer has its own RPC: in the Electron model the live
// WebContentsView reports its final address back through SetURLState at
// freeze time, so the old rod-era SetURLString (driven by a server-side
// browser tab) had no caller and was removed.
