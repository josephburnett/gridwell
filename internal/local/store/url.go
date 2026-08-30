package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/josephburnett/gridwell/api/rpc"
)

// SetURLState freezes a live url tile in one mutation: it writes the preview
// JPEG, with blob refcounting, the address the page ended on, the page title,
// and the navigation history, then publishes a single tile_changed event.
// Empty jpeg, url, and title arguments are skipped so a partial capture never
// clobbers good state. The desktop shell calls it on ascent.
//
// Every field here is a capture — what the live surface was observed to be,
// not something the user typed — so it carries no version claim and makes no
// version bump. It is last-writer-wins, and the capture rides the tile event
// to every client. A url the user types is a different verb, WriteContent's
// url arm, and that one claims and bumps.
//
// Freezing is an in-place edit of this url tile: tiles are unshared, so the
// frozen frame and address write straight to the tile's own row.
func (s *Store) SetURLState(ctx context.Context, req *rpc.SetURLStateRequest) (*rpc.Tile, error) {
	tileID, err := parseID(req.TileID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		if _, err := s.loadForWrite(ctx, tx, tileID, rpc.KindURL, ErrNotURLTile); err != nil {
			return err
		}

		// An empty JPEG is skipped, so a partial capture cannot clobber a good
		// frozen frame. The blob swap handles dedup and refcounting.
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
			// The page-title capture defers to a user-set name, the alt_user
			// latch, so renaming a url tile survives every freeze.
			if _, err := tx.ExecContext(ctx,
				`UPDATE tiles SET alt_text = ?, updated_at = ? WHERE id = ? AND alt_user = 0`,
				req.Title, s.now().Unix(), tileID); err != nil {
				return err
			}
		}
		if req.History != "" {
			// The navigation back-stack captured at freeze. Empty is skipped
			// like the JPEG, so a partial capture cannot clobber a good
			// stored history.
			if _, err := tx.ExecContext(ctx,
				`UPDATE tiles SET url_history = ?, updated_at = ? WHERE id = ?`,
				req.History, s.now().Unix(), tileID); err != nil {
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

// SetTileAlt updates a tile's stored alt-text. Two writers, one rule:
// user=true is the rename gesture, which sets the name and latches alt_user so
// the name is the user's from then on; user=false is an automatic capture, as
// on the shell detach path, and it no-ops once the user owns the name. The
// versioned user rename on the wire is RenameTile, in content.go, which shares
// setAltTx so the latch arbitration has exactly one implementation.
func (s *Store) SetTileAlt(ctx context.Context, tileID int64, alt string, user bool) error {
	return s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		if _, err := s.loadTile(ctx, tx, tileID); err != nil {
			return err
		}
		return s.setAltTx(ctx, tx, tileID, alt, user, events)
	})
}

// setAltTx is the one alt-text write: the alt_user latch rule lives here and
// nowhere else. user=true sets the name and latches ownership; user=false is
// an automatic capture that no-ops, with no write and no event, once the user
// owns the name.
//
// The version follows the same fork: the user's rename is a content edit and
// bumps, while the automatic capture is an observation and rides the tile
// event unversioned. A bumping capture would cost whichever client was
// mid-edit its claim.
func (s *Store) setAltTx(ctx context.Context, tx *sql.Tx, tileID int64, alt string, user bool, events *[]rpc.Event) error {
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

// SetContentZoom persists the per-tile content scale: the text font, the
// terminal font, the page zoom. It is framing, so no claim and no version
// bump. Wells are refused: their view_zoom is the grid viewport, a different
// concept with its own writer.
func (s *Store) SetContentZoom(ctx context.Context, req *rpc.SetContentZoomRequest) (*rpc.Tile, error) {
	tileID, err := parseID(req.TileID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	if req.ContentZoom < 0 {
		return nil, fmt.Errorf("%w: content_zoom must be >= 0", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.loadForWrite(ctx, tx, tileID, "", nil)
		if err != nil {
			return err
		}
		if isWellKind(n.Kind) {
			return fmt.Errorf("%w: a well has no content zoom", ErrInvalidArgument)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET content_zoom = ?, updated_at = ? WHERE id = ?`,
			req.ContentZoom, s.now().Unix(), tileID); err != nil {
			return err
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// SetURLFrozen persists the user's standing freeze on a url tile: frozen=true
// means descending must not auto-go-live until the reconnect gesture clears
// it. It is framing, so no claim and no version bump. Refused for every other
// kind: the fact only means something for a url tile.
func (s *Store) SetURLFrozen(ctx context.Context, req *rpc.SetURLFrozenRequest) (*rpc.Tile, error) {
	tileID, err := parseID(req.TileID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.loadForWrite(ctx, tx, tileID, "", nil)
		if err != nil {
			return err
		}
		if n.Kind != rpc.KindURL {
			return fmt.Errorf("%w: url_frozen only applies to url tiles", ErrInvalidArgument)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET url_frozen = ?, updated_at = ? WHERE id = ?`,
			boolToInt(req.Frozen), s.now().Unix(), tileID); err != nil {
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
