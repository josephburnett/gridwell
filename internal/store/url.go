package store

import (
	"context"
	"database/sql"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// SetURLState freezes a live URL tile in one mutation: it writes the
// preview JPEG (with blob refcounting), the address, and the page title,
// then bumps the version once and publishes a single tile_changed event.
// Empty jpeg/url/title arguments are skipped so a partial capture (e.g. a
// failed final frame) never clobbers good state. This is the RPC the
// Electron shell calls on ascend, replacing the old server-side rod freeze.
//
// Freezing is an in-place, versioned edit of this URL tile: copy-on-clone
// keeps tiles unshared, so the frozen frame and address write straight to the
// tile's own row.
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
		if _, err := s.checkPathLeaf(ctx, tx, req.Path, n); err != nil {
			return err
		}
		tileID := req.TileID

		// Empty JPEG is skipped (a partial capture must not clobber a good
		// frozen frame); the blob-swap kernel handles dedup + refcounting.
		if len(req.JPEG) > 0 {
			if _, _, err := s.swapTileBlob(ctx, tx, tileID, "preview_blob_id", req.JPEG, mediaJPEG); err != nil {
				return err
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
