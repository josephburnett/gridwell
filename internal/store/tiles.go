package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// urlSchemeAllowed reports whether u is one of the schemes accepted by
// URL tiles (spec §8.3 hard boundary). Only http and https.
func urlSchemeAllowed(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// MaxBlobBytes caps a single uploaded text-tile blob size.
const MaxBlobBytes = 16 * 1024 * 1024

// checkTileVersion loads a tile and verifies its claimed version matches.
// Returns the loaded tile.
func (s *Store) checkTileVersion(ctx context.Context, q gridReader, tileID, claimed int64) (*rpc.Tile, error) {
	t, err := s.loadTile(ctx, q, tileID)
	if err != nil {
		return nil, err
	}
	if t.Version != claimed {
		return nil, fmt.Errorf("%w: tile %d at version %d, claimed %d",
			ErrVersionConflict, tileID, t.Version, claimed)
	}
	return t, nil
}

// CreateWell creates a new well at (x,y) with footprint (w,h) inside the leaf
// grid of req.Path. The child grid is created empty with no framing on the
// well (view_x/y/zoom all zero).
func (s *Store) CreateWell(ctx context.Context, req *rpc.CreateWellRequest) (*rpc.Tile, error) {
	if req.W <= 0 || req.H <= 0 {
		return nil, fmt.Errorf("%w: w and h must be positive", ErrInvalidArgument)
	}
	var out *rpc.Tile
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		seq, err := s.buildGridSequence(ctx, tx, req.Path)
		if err != nil {
			return err
		}
		if seq.grids[len(seq.grids)-1] != req.GridID {
			return fmt.Errorf("%w: path leaf grid is %d not %d", ErrInvalidPath, seq.grids[len(seq.grids)-1], req.GridID)
		}

		pre, err := s.preWrite(ctx, tx, req.Path, 0)
		if err != nil {
			return err
		}
		gridID := pre.GridID
		events = append(events, pre.Events...)

		over, err := overlapsExisting(ctx, tx, gridID, req.X, req.Y, req.W, req.H)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}

		now := s.now().Unix()
		childObj := s.newID()
		res, err := tx.ExecContext(ctx,
			`INSERT INTO grids (object_id, refcount, created_at) VALUES (?, 1, ?)`,
			childObj, now)
		if err != nil {
			return fmt.Errorf("insert child grid: %w", err)
		}
		childGridID, err := res.LastInsertId()
		if err != nil {
			return err
		}

		objID := s.newID()
		res, err = tx.ExecContext(ctx, `
			INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h,
				view_x, view_y, view_zoom, child_grid_id,
				created_at, updated_at)
			VALUES (?, ?, 'well', ?, ?, ?, ?, 0, 0, 0, ?, ?, ?)`,
			objID, gridID, req.X, req.Y, req.W, req.H, childGridID, now, now)
		if err != nil {
			return fmt.Errorf("insert well: %w", err)
		}
		tileID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if err := bumpGridVersion(ctx, tx, gridID); err != nil {
			return err
		}
		out, err = s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		events = append(events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		s.publish(ev)
	}
	return out, nil
}

// CreateText creates a markdown text tile.
func (s *Store) CreateText(ctx context.Context, req *rpc.CreateTextRequest) (*rpc.Tile, error) {
	if req.W <= 0 || req.H <= 0 {
		return nil, fmt.Errorf("%w: w and h must be positive", ErrInvalidArgument)
	}
	if int64(len(req.Data)) > MaxBlobBytes {
		return nil, fmt.Errorf("%w: text too large", ErrInvalidArgument)
	}
	hash := hashBytes(req.Data)

	var out *rpc.Tile
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		seq, err := s.buildGridSequence(ctx, tx, req.Path)
		if err != nil {
			return err
		}
		if seq.grids[len(seq.grids)-1] != req.GridID {
			return fmt.Errorf("%w: path leaf grid mismatch", ErrInvalidPath)
		}

		pre, err := s.preWrite(ctx, tx, req.Path, 0)
		if err != nil {
			return err
		}
		gridID := pre.GridID
		events = append(events, pre.Events...)

		over, err := overlapsExisting(ctx, tx, gridID, req.X, req.Y, req.W, req.H)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}

		now := s.now().Unix()
		objID := s.newID()

		var blobID int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM blobs WHERE hash = ?`, hash).Scan(&blobID)
		if errors.Is(err, sql.ErrNoRows) {
			res, err := tx.ExecContext(ctx,
				`INSERT INTO blobs (hash, size, data, refcount) VALUES (?, ?, ?, 0)`,
				hash, len(req.Data), req.Data)
			if err != nil {
				return fmt.Errorf("insert blob: %w", err)
			}
			blobID, err = res.LastInsertId()
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}

		res, err := tx.ExecContext(ctx, `
			INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h,
				blob_id, created_at, updated_at)
			VALUES (?, ?, 'text', ?, ?, ?, ?, ?, ?, ?)`,
			objID, gridID, req.X, req.Y, req.W, req.H, blobID, now, now)
		if err != nil {
			return fmt.Errorf("insert text tile: %w", err)
		}
		tileID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE blobs SET refcount = refcount + 1 WHERE id = ?`, blobID); err != nil {
			return err
		}
		if err := bumpGridVersion(ctx, tx, gridID); err != nil {
			return err
		}
		out, err = s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		events = append(events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		s.publish(ev)
	}
	return out, nil
}

// CreateURL creates a URL tile pointing at the given URL.
func (s *Store) CreateURL(ctx context.Context, req *rpc.CreateURLRequest) (*rpc.Tile, error) {
	if req.W <= 0 || req.H <= 0 {
		return nil, fmt.Errorf("%w: w and h must be positive", ErrInvalidArgument)
	}
	urlString := strings.TrimSpace(req.URL)
	if !urlSchemeAllowed(urlString) {
		return nil, fmt.Errorf("%w: only http/https URLs allowed", ErrInvalidArgument)
	}
	var out *rpc.Tile
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		seq, err := s.buildGridSequence(ctx, tx, req.Path)
		if err != nil {
			return err
		}
		if seq.grids[len(seq.grids)-1] != req.GridID {
			return fmt.Errorf("%w: path leaf grid mismatch", ErrInvalidPath)
		}
		pre, err := s.preWrite(ctx, tx, req.Path, 0)
		if err != nil {
			return err
		}
		gridID := pre.GridID
		events = append(events, pre.Events...)

		over, err := overlapsExisting(ctx, tx, gridID, req.X, req.Y, req.W, req.H)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}

		now := s.now().Unix()
		objID := s.newID()
		res, err := tx.ExecContext(ctx, `
			INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h,
				url_string, created_at, updated_at)
			VALUES (?, ?, 'url', ?, ?, ?, ?, ?, ?, ?)`,
			objID, gridID, req.X, req.Y, req.W, req.H, urlString, now, now)
		if err != nil {
			return fmt.Errorf("insert url tile: %w", err)
		}
		tileID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if err := bumpGridVersion(ctx, tx, gridID); err != nil {
			return err
		}
		out, err = s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		events = append(events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		s.publish(ev)
	}
	return out, nil
}

// CreateBlackHole creates a blackhole tile — a deletion sink.
func (s *Store) CreateBlackHole(ctx context.Context, req *rpc.CreateBlackHoleRequest) (*rpc.Tile, error) {
	if req.W <= 0 || req.H <= 0 {
		return nil, fmt.Errorf("%w: w and h must be positive", ErrInvalidArgument)
	}
	var out *rpc.Tile
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		seq, err := s.buildGridSequence(ctx, tx, req.Path)
		if err != nil {
			return err
		}
		if seq.grids[len(seq.grids)-1] != req.GridID {
			return fmt.Errorf("%w: path leaf grid mismatch", ErrInvalidPath)
		}
		pre, err := s.preWrite(ctx, tx, req.Path, 0)
		if err != nil {
			return err
		}
		gridID := pre.GridID
		events = append(events, pre.Events...)

		over, err := overlapsExisting(ctx, tx, gridID, req.X, req.Y, req.W, req.H)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}

		now := s.now().Unix()
		objID := s.newID()
		res, err := tx.ExecContext(ctx, `
			INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h,
				created_at, updated_at)
			VALUES (?, ?, 'blackhole', ?, ?, ?, ?, ?, ?)`,
			objID, gridID, req.X, req.Y, req.W, req.H, now, now)
		if err != nil {
			return fmt.Errorf("insert blackhole tile: %w", err)
		}
		tileID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if err := bumpGridVersion(ctx, tx, gridID); err != nil {
			return err
		}
		out, err = s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		events = append(events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		s.publish(ev)
	}
	return out, nil
}

// ResizeTile changes a tile's footprint to (X, Y, W, H).
func (s *Store) ResizeTile(ctx context.Context, req *rpc.ResizeTileRequest) (*rpc.Tile, error) {
	if req.W <= 0 || req.H <= 0 {
		return nil, fmt.Errorf("%w: w and h must be positive", ErrInvalidArgument)
	}
	var out *rpc.Tile
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.checkTileVersion(ctx, tx, req.TileID, req.Version); err != nil {
			return err
		}

		pre, err := s.preWrite(ctx, tx, req.Path, req.TileID)
		if err != nil {
			return err
		}
		tileID := pre.TargetTileID
		events = append(events, pre.Events...)

		over, err := overlapsExisting(ctx, tx, pre.GridID, req.X, req.Y, req.W, req.H, tileID)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET x = ?, y = ?, w = ?, h = ?, updated_at = ? WHERE id = ?`,
			req.X, req.Y, req.W, req.H, s.now().Unix(), tileID); err != nil {
			return err
		}
		if err := bumpTileVersion(ctx, tx, tileID); err != nil {
			return err
		}
		out, err = s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		events = append(events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		s.publish(ev)
	}
	return out, nil
}

// SetWellView updates a well tile's framing (view_x/view_y/view_zoom).
func (s *Store) SetWellView(ctx context.Context, req *rpc.SetWellViewRequest) (*rpc.Tile, error) {
	var out *rpc.Tile
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		n, err := s.checkTileVersion(ctx, tx, req.TileID, req.Version)
		if err != nil {
			return err
		}
		if n.Kind != rpc.KindWell {
			return ErrNotWellTile
		}
		pre, err := s.preWrite(ctx, tx, req.Path, req.TileID)
		if err != nil {
			return err
		}
		events = append(events, pre.Events...)
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET view_x = ?, view_y = ?, view_zoom = ?, updated_at = ? WHERE id = ?`,
			req.ViewX, req.ViewY, req.ViewZoom, s.now().Unix(), pre.TargetTileID); err != nil {
			return err
		}
		if err := bumpTileVersion(ctx, tx, pre.TargetTileID); err != nil {
			return err
		}
		out, err = s.loadTile(ctx, tx, pre.TargetTileID)
		if err != nil {
			return err
		}
		events = append(events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		s.publish(ev)
	}
	return out, nil
}

// SetTextView updates a text tile's framed-document window and rendered/text
// mode.
func (s *Store) SetTextView(ctx context.Context, req *rpc.SetTextViewRequest) (*rpc.Tile, error) {
	var out *rpc.Tile
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		n, err := s.checkTileVersion(ctx, tx, req.TileID, req.Version)
		if err != nil {
			return err
		}
		if n.Kind != rpc.KindText {
			return ErrNotTextTile
		}
		pre, err := s.preWrite(ctx, tx, req.Path, req.TileID)
		if err != nil {
			return err
		}
		events = append(events, pre.Events...)
		var textModeArg any
		if req.TextMode != "" {
			textModeArg = req.TextMode
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET text_x = ?, text_y = ?, text_w = ?, text_h = ?, text_mode = ?, updated_at = ? WHERE id = ?`,
			req.TextX, req.TextY, req.TextW, req.TextH, textModeArg, s.now().Unix(), pre.TargetTileID); err != nil {
			return err
		}
		if err := bumpTileVersion(ctx, tx, pre.TargetTileID); err != nil {
			return err
		}
		out, err = s.loadTile(ctx, tx, pre.TargetTileID)
		if err != nil {
			return err
		}
		events = append(events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		s.publish(ev)
	}
	return out, nil
}

// DeleteTile removes a single tile by ID.
func (s *Store) DeleteTile(ctx context.Context, req *rpc.DeleteTileRequest) error {
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.checkTileVersion(ctx, tx, req.TileID, req.Version); err != nil {
			return err
		}
		pre, err := s.preWrite(ctx, tx, req.Path, req.TileID)
		if err != nil {
			return err
		}
		events = append(events, pre.Events...)
		t, err := s.loadTile(ctx, tx, pre.TargetTileID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tiles WHERE id = ?`, pre.TargetTileID); err != nil {
			return err
		}
		if t.Kind == rpc.KindWell && t.ChildGridID != 0 {
			if err := s.decRefcount(ctx, tx, t.ChildGridID); err != nil {
				return err
			}
		}
		if t.Kind == rpc.KindText && t.BlobID != 0 {
			if err := s.decBlobRefcount(ctx, tx, t.BlobID); err != nil {
				return err
			}
		}
		if err := bumpGridVersion(ctx, tx, pre.GridID); err != nil {
			return err
		}
		events = append(events, rpc.Event{Kind: rpc.EventTileRemoved, TileRemoved: &rpc.TileRemoved{GridID: pre.GridID, TileID: pre.TargetTileID}})
		return nil
	})
	if err != nil {
		return err
	}
	for _, ev := range events {
		s.publish(ev)
	}
	return nil
}
