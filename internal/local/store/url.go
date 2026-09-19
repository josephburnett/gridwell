package store

import (
	"context"
	"database/sql"
	"fmt"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// SetURLState freezes a live url tile in one mutation: the preview JPEG, the
// address the page ended on, the title and the navigation history, then one
// tile_changed event. Empty arguments are skipped so a partial capture never
// clobbers good state. Every field is a capture, not something the user typed,
// so no claim and no version bump; a url the user types is WriteContent's url
// arm, which claims and bumps.
func (s *Store) SetURLState(ctx context.Context, tileIDStr string, jpeg []byte, url, title, history string) (*gridwellv1.Tile, error) {
	tileID, err := parseID(tileIDStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *gridwellv1.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		if _, err := s.loadForWrite(ctx, tx, tileID, rpc.KindURL, ErrNotURLTile); err != nil {
			return err
		}

		// An empty JPEG is skipped, so a partial capture cannot clobber a good
		// frozen frame.
		if len(jpeg) > 0 {
			if _, _, err := s.swapTileBlob(ctx, tx, tileID, "preview_blob_id", jpeg, mediaJPEG); err != nil {
				return err
			}
		}
		if url != "" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE tiles SET url_string = ?, updated_at = ? WHERE id = ?`,
				url, s.now().Unix(), tileID); err != nil {
				return err
			}
		}
		if title != "" {
			// The page-title capture defers to a user-set name, the alt_user
			// latch, so renaming a url tile survives every freeze.
			if _, err := tx.ExecContext(ctx,
				`UPDATE tiles SET alt_text = ?, updated_at = ? WHERE id = ? AND alt_user = 0`,
				title, s.now().Unix(), tileID); err != nil {
				return err
			}
		}
		if history != "" {
			// Empty is skipped like the JPEG, so a partial capture cannot
			// clobber a good stored history.
			if _, err := tx.ExecContext(ctx,
				`UPDATE tiles SET url_history = ?, updated_at = ? WHERE id = ?`,
				history, s.now().Unix(), tileID); err != nil {
				return err
			}
		}

		var err error
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetTileAlt updates a tile's stored alt-text. user=true is the rename
// gesture, which latches alt_user; user=false is an automatic capture, which
// no-ops once the user owns the name. RenameTile is the versioned wire verb
// and shares setAltTx, so the latch arbitration has one implementation.
func (s *Store) SetTileAlt(ctx context.Context, tileIDStr, alt string, user bool) error {
	tileID, err := parseID(tileIDStr)
	if err != nil {
		return fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	return s.withMutation(ctx, func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		if _, err := s.loadTile(ctx, tx, tileID); err != nil {
			return err
		}
		return s.setAltTx(ctx, tx, tileID, alt, user, events)
	})
}

// setAltTx is the one alt-text write, and the alt_user latch rule lives here.
// user=true sets the name and latches ownership; user=false no-ops, with no
// write and no event, once the user owns the name. The version follows the
// same fork, because a bumping capture would cost a mid-edit client its claim.
func (s *Store) setAltTx(ctx context.Context, tx *sql.Tx, tileID int64, alt string, user bool, events *[]*gridwellv1.Event) error {
	q := `UPDATE tiles SET alt_text = ?, alt_user = 1, updated_at = ? WHERE id = ?`
	if !user {
		q = `UPDATE tiles SET alt_text = ?, updated_at = ? WHERE id = ? AND alt_user = 0`
	}
	res, err := tx.ExecContext(ctx, q, alt, s.now().Unix(), tileID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return nil // the capture deferred to a user-owned name: no edit
	}
	if user {
		_, err = s.finishContentEdit(ctx, tx, tileID, events)
		return err
	}
	_, err = s.emitTileChanged(ctx, tx, tileID, events)
	return err
}

// SetContentZoom persists the per-tile content scale. It is framing, so no
// claim and no bump. Wells are refused: their view_zoom is the grid viewport,
// a different fact with its own writer.
func (s *Store) SetContentZoom(ctx context.Context, tileIDStr string, contentZoom float64) (*gridwellv1.Tile, error) {
	tileID, err := parseID(tileIDStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	if contentZoom < 0 {
		return nil, fmt.Errorf("%w: content_zoom must be >= 0", ErrInvalidArgument)
	}
	var out *gridwellv1.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		n, err := s.loadForWrite(ctx, tx, tileID, "", nil)
		if err != nil {
			return err
		}
		if isWellKind(n.Kind) {
			return fmt.Errorf("%w: a well has no content zoom", ErrInvalidArgument)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET content_zoom = ?, updated_at = ? WHERE id = ?`,
			contentZoom, s.now().Unix(), tileID); err != nil {
			return err
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// boolToInt maps a bool onto SQLite's 0 and 1 integer convention.
func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
