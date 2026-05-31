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
		n, err := s.checkTileVersion(ctx, tx, req.TileID, req.Version)
		if err != nil {
			return err
		}

		srcSeq, err := s.buildGridSequence(ctx, tx, req.Path)
		if err != nil {
			return err
		}
		srcGrid := srcSeq.grids[len(srcSeq.grids)-1]
		if n.GridID != srcGrid {
			return fmt.Errorf("%w: tile %d not in source path leaf grid %d", ErrInvalidPath, req.TileID, srcGrid)
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

		crossGrid := dstGrid != srcGrid
		if crossGrid {
			dstPre, err := s.preWrite(ctx, tx, req.DestPath, 0)
			if err != nil {
				return err
			}
			events = append(events, dstPre.Events...)
			dstGrid = dstPre.GridID
		} else {
			dstGrid = srcGrid
		}

		if n.Kind == rpc.KindWell {
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
		if err := bumpTileVersion(ctx, tx, tileID); err != nil {
			return err
		}
		if crossGrid {
			if err := bumpGridVersion(ctx, tx, srcGrid); err != nil {
				return err
			}
			if err := bumpGridVersion(ctx, tx, dstGrid); err != nil {
				return err
			}
		}
		out, err = s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		if crossGrid {
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

// CloneTile duplicates a tile into a destination grid at (x, y). The new row
// shares the source's object_id and starting version. For wells, both rows
// share the same child grid (refcount bumps). For text tiles, both rows
// share the same blob (refcount bumps). For URL tiles, the new row copies
// the URL string and the last-frozen preview JPEG. For blackhole tiles,
// only the kind is carried over.
func (s *Store) CloneTile(ctx context.Context, req *rpc.CloneTileRequest) (*rpc.Tile, error) {
	var out *rpc.Tile
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		n, err := s.checkTileVersion(ctx, tx, req.TileID, req.Version)
		if err != nil {
			return err
		}

		srcSeq, err := s.buildGridSequence(ctx, tx, req.Path)
		if err != nil {
			return err
		}
		if n.GridID != srcSeq.grids[len(srcSeq.grids)-1] {
			return fmt.Errorf("%w: tile %d not in source path leaf grid", ErrInvalidPath, req.TileID)
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
			blob        sql.NullInt64
			urlStr      sql.NullString
			textMode    sql.NullString
			previewJPEG []byte
		)
		switch n.Kind {
		case rpc.KindWell:
			child = sql.NullInt64{Int64: n.ChildGridID, Valid: true}
		case rpc.KindURL:
			urlStr = sql.NullString{String: n.URLString, Valid: true}
			previewJPEG, err = loadPreviewJPEG(ctx, tx, n.ID)
			if err != nil {
				return err
			}
		case rpc.KindText:
			blob = sql.NullInt64{Int64: n.BlobID, Valid: true}
			if n.TextMode != "" {
				textMode = sql.NullString{String: n.TextMode, Valid: true}
			}
		case rpc.KindBlackHole:
			// no kind-specific state
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO tiles (object_id, version, grid_id, kind, x, y, w, h,
				view_x, view_y, view_zoom, child_grid_id,
				text_x, text_y, text_w, text_h, text_mode, blob_id,
				url_string, preview_jpeg,
				created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			n.ObjectID, n.Version, dstGrid, n.Kind, req.X, req.Y, n.W, n.H,
			n.ViewX, n.ViewY, n.ViewZoom, child,
			n.TextX, n.TextY, n.TextW, n.TextH, textMode, blob,
			urlStr, previewJPEG,
			now, now)
		if err != nil {
			return fmt.Errorf("insert clone: %w", err)
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		switch n.Kind {
		case rpc.KindWell:
			if err := s.incRefcount(ctx, tx, n.ChildGridID); err != nil {
				return err
			}
		case rpc.KindText:
			if err := s.incBlobRefcount(ctx, tx, n.BlobID); err != nil {
				return err
			}
		}
		if err := bumpGridVersion(ctx, tx, dstGrid); err != nil {
			return err
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

// UpdateText replaces a text tile's blob with new bytes.
func (s *Store) UpdateText(ctx context.Context, req *rpc.UpdateTextRequest) (*rpc.Tile, error) {
	if int64(len(req.Data)) > MaxBlobBytes {
		return nil, fmt.Errorf("%w: text too large", ErrInvalidArgument)
	}
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

		hash := hashBytes(req.Data)
		newBlobID, err := putBlob(ctx, tx, hash, req.Data)
		if err != nil {
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
			if err := s.incBlobRefcount(ctx, tx, newBlobID); err != nil {
				return err
			}
			if err := s.decBlobRefcount(ctx, tx, oldBlobID); err != nil {
				return err
			}
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
