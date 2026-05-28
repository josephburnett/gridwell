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

// allowedMimes are the file MIME types accepted in v1.
var allowedMimes = map[string]bool{
	"text/markdown":   true,
	rpc.MimeURIList:   true,
	"image/png":       true,
	"image/jpeg":      true,
	"image/gif":       true,
	"image/webp":      true,
	rpc.MimeBlackHole: true,
}

// MaxBlobBytes caps a single uploaded file size.
const MaxBlobBytes = 16 * 1024 * 1024

// CreateWell creates a new well at (x,y) with footprint (w,h) inside the leaf
// grid of req.Path.
func (s *Store) CreateWell(ctx context.Context, req *rpc.CreateWellRequest) (*rpc.Tile, error) {
	if req.W <= 0 || req.H <= 0 {
		return nil, fmt.Errorf("%w: w and h must be positive", ErrInvalidArgument)
	}
	if !req.ViewRect.Intersects(req.X, req.Y, req.W, req.H) {
		return nil, ErrLocality
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

		// Create child grid (empty).
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

		// Create the well node.
		objID := s.newID()
		res, err = tx.ExecContext(ctx, `
			INSERT INTO tiles (object_id, grid_id, type, x, y, w, h, view_x, view_y,
				child_grid_id, capped, created_at, updated_at)
			VALUES (?, ?, 'well', ?, ?, ?, ?, 0, 0, ?, 0, ?, ?)`,
			objID, gridID, req.X, req.Y, req.W, req.H, childGridID, now, now)
		if err != nil {
			return fmt.Errorf("insert well: %w", err)
		}
		tileID, err := res.LastInsertId()
		if err != nil {
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

// CreateFile creates a file tile.
func (s *Store) CreateFile(ctx context.Context, req *rpc.CreateFileRequest) (*rpc.Tile, error) {
	if req.W <= 0 || req.H <= 0 {
		return nil, fmt.Errorf("%w: w and h must be positive", ErrInvalidArgument)
	}
	if !allowedMimes[req.MimeType] {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedMime, req.MimeType)
	}
	if int64(len(req.Data)) > MaxBlobBytes {
		return nil, fmt.Errorf("%w: file too large", ErrInvalidArgument)
	}
	if !req.ViewRect.Intersects(req.X, req.Y, req.W, req.H) {
		return nil, ErrLocality
	}

	isURL := req.MimeType == rpc.MimeURIList
	var urlString string
	if isURL {
		urlString = strings.TrimSpace(string(req.Data))
		if !urlSchemeAllowed(urlString) {
			return nil, fmt.Errorf("%w: only http/https URLs allowed", ErrInvalidArgument)
		}
	}

	var hash string
	if !isURL {
		hash = hashBytes(req.Data)
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

		var tileID int64
		if isURL {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO tiles (object_id, grid_id, type, x, y, w, h, view_x, view_y,
					mime_type, url_string, created_at, updated_at)
				VALUES (?, ?, 'file', ?, ?, ?, ?, 0, 0, ?, ?, ?, ?)`,
				objID, gridID, req.X, req.Y, req.W, req.H, req.MimeType, urlString, now, now)
			if err != nil {
				return fmt.Errorf("insert url tile: %w", err)
			}
			tileID, err = res.LastInsertId()
			if err != nil {
				return err
			}
		} else {
			var blobID int64
			err = tx.QueryRowContext(ctx, `SELECT id FROM blobs WHERE hash = ?`, hash).Scan(&blobID)
			if errors.Is(err, sql.ErrNoRows) {
				res, err := tx.ExecContext(ctx,
					`INSERT INTO blobs (hash, size, mime_type, data, refcount) VALUES (?, ?, ?, ?, 0)`,
					hash, len(req.Data), req.MimeType, req.Data)
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
				INSERT INTO tiles (object_id, grid_id, type, x, y, w, h, view_x, view_y,
					mime_type, blob_id, created_at, updated_at)
				VALUES (?, ?, 'file', ?, ?, ?, ?, 0, 0, ?, ?, ?, ?)`,
				objID, gridID, req.X, req.Y, req.W, req.H, req.MimeType, blobID, now, now)
			if err != nil {
				return fmt.Errorf("insert file: %w", err)
			}
			tileID, err = res.LastInsertId()
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE blobs SET refcount = refcount + 1 WHERE id = ?`, blobID); err != nil {
				return err
			}
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
		n, err := s.loadTile(ctx, tx, req.TileID)
		if err != nil {
			return err
		}
		if !req.ViewRect.Intersects(n.X, n.Y, n.W, n.H) {
			return ErrLocality
		}
		if !req.ViewRect.Intersects(req.X, req.Y, req.W, req.H) {
			return ErrLocality
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

// SetTileViewport updates a tile's stored view: scroll (view_x/view_y),
// zoom, and — for text-file tiles — the framed window size (view_w/view_h)
// and rendered/raw mode. ViewW/ViewH default to the existing values when
// the request sends 0; FileMode likewise when empty.
func (s *Store) SetTileViewport(ctx context.Context, req *rpc.SetTileViewportRequest) (*rpc.Tile, error) {
	var out *rpc.Tile
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		n, err := s.loadTile(ctx, tx, req.TileID)
		if err != nil {
			return err
		}
		if !req.ViewRect.Intersects(n.X, n.Y, n.W, n.H) {
			return ErrLocality
		}
		pre, err := s.preWrite(ctx, tx, req.Path, req.TileID)
		if err != nil {
			return err
		}
		events = append(events, pre.Events...)
		viewW, viewH := req.ViewW, req.ViewH
		if viewW == 0 {
			viewW = n.ViewW
		}
		if viewH == 0 {
			viewH = n.ViewH
		}
		fileMode := req.FileMode
		if fileMode == "" {
			fileMode = n.FileMode
		}
		var fileModeArg any
		if fileMode != "" {
			fileModeArg = fileMode
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET view_x = ?, view_y = ?, view_zoom = ?, view_w = ?, view_h = ?, file_mode = ?, updated_at = ? WHERE id = ?`,
			req.ViewX, req.ViewY, req.ViewZoom, viewW, viewH, fileModeArg, s.now().Unix(), pre.TargetTileID); err != nil {
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
		n, err := s.loadTile(ctx, tx, req.TileID)
		if err != nil {
			return err
		}
		if !req.ViewRect.Intersects(n.X, n.Y, n.W, n.H) {
			return ErrLocality
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
		if t.Type == "well" && t.ChildGridID != 0 {
			if err := s.decRefcount(ctx, tx, t.ChildGridID); err != nil {
				return err
			}
		}
		if t.Type == "file" && t.BlobID != 0 {
			if err := s.decBlobRefcount(ctx, tx, t.BlobID); err != nil {
				return err
			}
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

// AscendAtRoot creates a new grid with a single well pointing at the current
// root grid, and updates the system root_grid_id to the new grid.
func (s *Store) AscendAtRoot(ctx context.Context) (*rpc.AscendAtRootResponse, error) {
	var resp rpc.AscendAtRootResponse
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		oldRootID, err := rootGridID(ctx, tx)
		if err != nil {
			return err
		}
		now := s.now().Unix()
		newObj := s.newID()
		res, err := tx.ExecContext(ctx,
			`INSERT INTO grids (object_id, refcount, created_at) VALUES (?, 1, ?)`,
			newObj, now)
		if err != nil {
			return err
		}
		newGridID, err := res.LastInsertId()
		if err != nil {
			return err
		}

		wellObj := s.newID()
		res, err = tx.ExecContext(ctx, `
			INSERT INTO tiles (object_id, grid_id, type, x, y, w, h, view_x, view_y,
				child_grid_id, capped, created_at, updated_at)
			VALUES (?, ?, 'well', 0, 0, 1, 1, 0, 0, ?, 0, ?, ?)`,
			wellObj, newGridID, oldRootID, now, now)
		if err != nil {
			return err
		}
		wellID, err := res.LastInsertId()
		if err != nil {
			return err
		}

		// The old root grid had refcount=1 because of its "+1 for being root".
		// Now: it's no longer the root (-1) but it gains a well pointer (+1).
		// Net change is 0; no update needed.

		if err := setRootGridID(ctx, tx, newGridID); err != nil {
			return err
		}
		resp.NewRootGridID = newGridID
		resp.WellID = wellID
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.publish(rpc.Event{Kind: rpc.EventGridChanged, GridChanged: &rpc.GridChanged{GridID: resp.NewRootGridID}})
	return &resp, nil
}
