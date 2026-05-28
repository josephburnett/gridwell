package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// loadPreviewJPEG reads a tile's preview_jpeg bytes inside the given
// transaction. Returns nil for tiles with no preview.
func loadPreviewJPEG(ctx context.Context, tx *sql.Tx, tileID int64) ([]byte, error) {
	var jpeg []byte
	err := tx.QueryRowContext(ctx, `SELECT preview_jpeg FROM tiles WHERE id = ?`, tileID).Scan(&jpeg)
	if err != nil {
		return nil, err
	}
	return jpeg, nil
}

// MoveTile moves a tile either within its grid or across grids.
func (s *Store) MoveTile(ctx context.Context, req *rpc.MoveTileRequest) (*rpc.Tile, error) {
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
		if !req.DestViewRect.Intersects(req.X, req.Y, n.W, n.H) {
			return ErrLocality
		}

		srcSeq, err := s.buildGridSequence(ctx, tx, req.Path)
		if err != nil {
			return err
		}
		srcGrid := srcSeq.grids[len(srcSeq.grids)-1]
		if n.GridID != srcGrid {
			return fmt.Errorf("%w: node %d not in source path leaf grid %d", ErrInvalidPath, req.TileID, srcGrid)
		}

		srcPre, err := s.preWrite(ctx, tx, req.Path, req.TileID)
		if err != nil {
			return err
		}
		events = append(events, srcPre.Events...)
		tileID := srcPre.TargetTileID
		srcGrid = srcPre.GridID

		dstSeq, err := s.buildGridSequence(ctx, tx, req.DestPath)
		if err != nil {
			return err
		}
		dstGrid := dstSeq.grids[len(dstSeq.grids)-1]
		if dstGrid != req.DestGridID {
			return fmt.Errorf("%w: dest path leaf is %d not %d", ErrInvalidPath, dstGrid, req.DestGridID)
		}

		if dstGrid != srcGrid {
			dstPre, err := s.preWrite(ctx, tx, req.DestPath, 0)
			if err != nil {
				return err
			}
			events = append(events, dstPre.Events...)
			dstGrid = dstPre.GridID
		} else {
			dstGrid = srcGrid
		}

		if n.Type == "well" {
			for _, wid := range req.DestPath.WellIDs {
				if wid == tileID {
					return fmt.Errorf("%w: cannot move a well into itself or a descendant", ErrInvalidArgument)
				}
			}
		}

		var excludes []int64
		if dstGrid == srcGrid {
			excludes = []int64{tileID}
		}
		over, err := overlapsExisting(ctx, tx, dstGrid, req.X, req.Y, n.W, n.H, excludes...)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET grid_id = ?, x = ?, y = ?, updated_at = ? WHERE id = ?`,
			dstGrid, req.X, req.Y, s.now().Unix(), tileID); err != nil {
			return err
		}
		out, err = s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		if dstGrid != srcGrid {
			events = append(events, rpc.Event{Kind: rpc.EventTileRemoved, TileRemoved: &rpc.TileRemoved{GridID: srcGrid, TileID: tileID}})
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

// CloneTile duplicates a tile into a destination grid at (x, y).
func (s *Store) CloneTile(ctx context.Context, req *rpc.CloneTileRequest) (*rpc.Tile, error) {
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
		if !req.DestViewRect.Intersects(req.X, req.Y, n.W, n.H) {
			return ErrLocality
		}

		srcSeq, err := s.buildGridSequence(ctx, tx, req.Path)
		if err != nil {
			return err
		}
		if n.GridID != srcSeq.grids[len(srcSeq.grids)-1] {
			return fmt.Errorf("%w: node %d not in source path leaf grid", ErrInvalidPath, req.TileID)
		}
		dstSeq, err := s.buildGridSequence(ctx, tx, req.DestPath)
		if err != nil {
			return err
		}
		if req.DestGridID != dstSeq.grids[len(dstSeq.grids)-1] {
			return fmt.Errorf("%w: dest path leaf mismatch", ErrInvalidPath)
		}

		dstPre, err := s.preWrite(ctx, tx, req.DestPath, 0)
		if err != nil {
			return err
		}
		events = append(events, dstPre.Events...)
		dstGrid := dstPre.GridID

		over, err := overlapsExisting(ctx, tx, dstGrid, req.X, req.Y, n.W, n.H)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}

		now := s.now().Unix()
		var (
			child       sql.NullInt64
			mime        sql.NullString
			blob        sql.NullInt64
			urlStr      sql.NullString
			previewJPEG []byte
		)
		switch {
		case n.Type == "well":
			child = sql.NullInt64{Int64: n.ChildGridID, Valid: true}
		case n.IsURL():
			mime = sql.NullString{String: n.MimeType, Valid: true}
			urlStr = sql.NullString{String: n.URLString, Valid: true}
			previewJPEG, err = loadPreviewJPEG(ctx, tx, n.ID)
			if err != nil {
				return err
			}
		default:
			mime = sql.NullString{String: n.MimeType, Valid: true}
			blob = sql.NullInt64{Int64: n.BlobID, Valid: true}
		}
		var cappedInt int64
		if n.Capped {
			cappedInt = 1
		}
		var fileModeArg any
		if n.FileMode != "" {
			fileModeArg = n.FileMode
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO tiles (object_id, grid_id, type, x, y, w, h, view_x, view_y, view_zoom, view_w, view_h, file_mode,
				child_grid_id, capped, mime_type, blob_id, url_string, preview_jpeg,
				created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			n.ObjectID, dstGrid, n.Type, req.X, req.Y, n.W, n.H, n.ViewX, n.ViewY, n.ViewZoom, n.ViewW, n.ViewH, fileModeArg,
			child, cappedInt, mime, blob, urlStr, previewJPEG, now, now)
		if err != nil {
			return fmt.Errorf("insert clone: %w", err)
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		switch {
		case n.Type == "well":
			if err := s.incRefcount(ctx, tx, n.ChildGridID); err != nil {
				return err
			}
		case !n.IsURL():
			if _, err := tx.ExecContext(ctx,
				`UPDATE blobs SET refcount = refcount + 1 WHERE id = ?`, n.BlobID); err != nil {
				return err
			}
		}
		out, err = s.loadTile(ctx, tx, newID)
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

// UpdateFileContent replaces a file tile's blob with new bytes.
func (s *Store) UpdateFileContent(ctx context.Context, req *rpc.UpdateFileContentRequest) (*rpc.Tile, error) {
	if int64(len(req.Data)) > MaxBlobBytes {
		return nil, fmt.Errorf("%w: file too large", ErrInvalidArgument)
	}
	var out *rpc.Tile
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		n, err := s.loadTile(ctx, tx, req.TileID)
		if err != nil {
			return err
		}
		if n.Type != "file" {
			return fmt.Errorf("%w: node is not a file", ErrInvalidArgument)
		}
		if n.MimeType != "text/markdown" {
			return fmt.Errorf("%w: %s is read-only", ErrInvalidArgument, n.MimeType)
		}
		if !req.ViewRect.Intersects(n.X, n.Y, n.W, n.H) {
			return ErrLocality
		}
		pre, err := s.preWrite(ctx, tx, req.Path, req.TileID)
		if err != nil {
			return err
		}
		events = append(events, pre.Events...)

		hash := sha256Hex(req.Data)
		var newBlobID int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM blobs WHERE hash = ?`, hash).Scan(&newBlobID)
		if err == sql.ErrNoRows {
			res, err := tx.ExecContext(ctx,
				`INSERT INTO blobs (hash, size, mime_type, data, refcount) VALUES (?, ?, ?, ?, 0)`,
				hash, len(req.Data), n.MimeType, req.Data)
			if err != nil {
				return fmt.Errorf("insert blob: %w", err)
			}
			newBlobID, err = res.LastInsertId()
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}

		current, err := s.loadTile(ctx, tx, pre.TargetTileID)
		if err != nil {
			return err
		}
		oldBlobID := current.BlobID

		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET blob_id = ?, updated_at = ? WHERE id = ?`,
			newBlobID, s.now().Unix(), pre.TargetTileID); err != nil {
			return err
		}
		if oldBlobID != newBlobID {
			if _, err := tx.ExecContext(ctx,
				`UPDATE blobs SET refcount = refcount + 1 WHERE id = ?`, newBlobID); err != nil {
				return err
			}
			if err := s.decBlobRefcount(ctx, tx, oldBlobID); err != nil {
				return err
			}
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

func sha256Hex(data []byte) string {
	return hashBytes(data)
}
