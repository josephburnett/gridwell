package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/josephburnett/gridwell/api/rpc"
)

// SetURLState freezes a live URL tile in one mutation: it writes the
// preview JPEG (with blob refcounting), the address the page ended on, the
// page title, and the navigation history, then publishes a single
// tile_changed event. Empty jpeg/url/title arguments are skipped so a partial
// capture (e.g. a failed final frame) never clobbers good state. This is the
// RPC the Electron shell calls on ascend, replacing the old server-side rod
// freeze.
//
// Every field here is a CAPTURE — what the live surface was observed to be,
// not something the user typed — so it carries NO version claim and makes no
// version bump (docs/simplify-plan.md S5). Last-writer-wins: the capture
// rides the tile event to every client. (A url the user TYPES is a different
// verb — WriteContent's url arm — and that one claims and bumps.)
//
// Freezing is an in-place edit of this URL tile: copy-on-clone keeps tiles
// unshared, so the frozen frame and address write straight to the tile's own
// row.
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
			// The page-title capture defers to a user-set name (alt_user,
			// issue #61) — renaming a url tile must survive every freeze.
			if _, err := tx.ExecContext(ctx,
				`UPDATE tiles SET alt_text = ?, updated_at = ? WHERE id = ? AND alt_user = 0`,
				req.Title, s.now().Unix(), tileID); err != nil {
				return err
			}
		}
		if req.History != "" {
			// The navigation back-stack captured at freeze (issue #113). Empty
			// is skipped like the JPEG — a partial capture must not clobber a
			// good stored history.
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

// SetTileAlt updates a tile's stored alt-text. Two writers, one rule (issue
// #61): user=true is the RENAME gesture — it sets the name and latches
// alt_user so it is owned by the user from then on; user=false is an
// automatic capture (the shell detach path baking in the tmux foreground
// command) — it silently no-ops once the user owns the name. The versioned
// user rename on the wire is RenameTile (content.go), which shares setAltTx
// so the latch arbitration has exactly one implementation.
func (s *Store) SetTileAlt(ctx context.Context, tileID int64, alt string, user bool) error {
	return s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		if _, err := s.loadTile(ctx, tx, tileID); err != nil {
			return err
		}
		return s.setAltTx(ctx, tx, tileID, alt, user, events)
	})
}

// setAltTx is the ONE alt-text write: the alt_user latch rule lives here and
// nowhere else. user=true sets the name and latches ownership; user=false is
// an automatic capture that no-ops (no write, no event) once the user owns
// the name.
//
// The version follows the same fork (docs/simplify-plan.md S5): the USER's
// rename is a content edit and bumps; the automatic capture is an observation
// and rides the tile event unversioned. Before, a shell's foreground command
// changing bumped the row and cost whichever client was mid-edit its claim.
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
		return nil // capture deferred to a user-owned name: no edit happened
	}
	if user {
		_, err = s.finishContentEdit(ctx, tx, tileID, events)
		return err
	}
	_, err = s.emitTileChanged(ctx, tx, tileID, events)
	return err
}

// SetContentZoom persists the per-tile content scale (text font, terminal
// font, page zoom — issue #82). Framing: no claim, no version bump — the
// enforced split (CLAUDE.md face #3). Wells are refused: their view_zoom is
// the grid viewport, a different concept with its own writer.
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

// SetURLFrozen persists the user's standing freeze on a url tile (issue
// #237): frozen=true means descending must not auto-go-live until the
// reconnect gesture clears it. Framing: no claim, no version bump — the
// enforced split (CLAUDE.md face #3). Refused for every other kind: the fact
// only means something for a url tile.
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

// boolToInt maps a bool onto SQLite's 0/1 integer convention.
func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// Navigation no longer has its own RPC: in the Electron model the live
// WebContentsView reports its final address back through SetURLState at
// freeze time, so the old rod-era SetURLString (driven by a server-side
// browser tab) had no caller and was removed.
