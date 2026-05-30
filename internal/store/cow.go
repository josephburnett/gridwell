package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// gridSequence is the sequence of grid ids from the root down to the leaf
// grid the editing pane is in. grids[0] is always the current root grid;
// grids[len-1] is the leaf. wells[i] is the well in grids[i] that points at
// grids[i+1], so len(wells) == len(grids)-1.
type gridSequence struct {
	grids []int64
	wells []int64
}

// buildGridSequence validates the path and returns the sequence of grids and
// path wells for it.
func (s *Store) buildGridSequence(ctx context.Context, q gridReader, p rpc.Path) (gridSequence, error) {
	root, err := rootGridID(ctx, q)
	if err != nil {
		return gridSequence{}, err
	}
	seq := gridSequence{grids: []int64{root}}
	for _, wellID := range p.WellIDs {
		w, err := s.loadTile(ctx, q, wellID)
		if err != nil {
			return gridSequence{}, fmt.Errorf("%w: well %d: %v", ErrInvalidPath, wellID, err)
		}
		if w.Kind != rpc.KindWell {
			return gridSequence{}, fmt.Errorf("%w: node %d is not a well", ErrInvalidPath, wellID)
		}
		if w.GridID != seq.grids[len(seq.grids)-1] {
			return gridSequence{}, fmt.Errorf("%w: well %d not in grid %d", ErrInvalidPath, wellID, seq.grids[len(seq.grids)-1])
		}
		seq.wells = append(seq.wells, wellID)
		seq.grids = append(seq.grids, w.ChildGridID)
	}
	return seq, nil
}

// preWriteResult describes the result of preWrite.
type preWriteResult struct {
	GridID       int64
	TargetTileID int64
	Events       []rpc.Event
}

// preWrite ensures that the leaf grid in the descent path is unshared,
// forking grids up the path as needed.
func (s *Store) preWrite(ctx context.Context, tx *sql.Tx, path rpc.Path, targetTileID int64) (*preWriteResult, error) {
	seq, err := s.buildGridSequence(ctx, tx, path)
	if err != nil {
		return nil, err
	}

	// Find the TOPMOST grid in the path whose refcount > 1. Every grid from
	// that index down to the leaf must be forked, because forking a shared
	// ancestor bumps the refcount of every well-child it contains — so any
	// rc=1 descendant becomes rc=2 as soon as its parent forks, and a
	// subsequent write through this path would leak into the other clones
	// of the ancestor.
	topForkIdx := -1
	for i := 0; i < len(seq.grids); i++ {
		var rc int64
		err := tx.QueryRowContext(ctx, `SELECT refcount FROM grids WHERE id = ?`, seq.grids[i]).Scan(&rc)
		if err != nil {
			return nil, err
		}
		if rc > 1 {
			if i == 0 {
				return nil, fmt.Errorf("internal: root grid is shared (refcount=%d)", rc)
			}
			topForkIdx = i
			break
		}
	}

	if topForkIdx == -1 {
		return &preWriteResult{GridID: seq.grids[len(seq.grids)-1], TargetTileID: targetTileID}, nil
	}

	wellObjects := make([]string, len(seq.wells))
	for i, wid := range seq.wells {
		var obj string
		if err := tx.QueryRowContext(ctx, `SELECT object_id FROM tiles WHERE id = ?`, wid).Scan(&obj); err != nil {
			return nil, err
		}
		wellObjects[i] = obj
	}
	var targetObjectID string
	if targetTileID != 0 {
		err := tx.QueryRowContext(ctx, `SELECT object_id FROM tiles WHERE id = ?`, targetTileID).Scan(&targetObjectID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: target node %d", ErrNotFound, targetTileID)
		}
		if err != nil {
			return nil, err
		}
	}

	events := []rpc.Event{}

	parentWellID := int64(0)
	if topForkIdx > 0 {
		parentWellID = seq.wells[topForkIdx-1]
	}

	for i := topForkIdx; i < len(seq.grids); i++ {
		oldGridID := seq.grids[i]
		newGridID, wellRemap, err := s.forkGrid(ctx, tx, oldGridID)
		if err != nil {
			return nil, fmt.Errorf("fork grid %d: %w", oldGridID, err)
		}

		if parentWellID != 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE tiles SET child_grid_id = ?, updated_at = ? WHERE id = ?`,
				newGridID, s.now().Unix(), parentWellID); err != nil {
				return nil, err
			}
			if err := s.decRefcount(ctx, tx, oldGridID); err != nil {
				return nil, err
			}
			if err := s.incRefcount(ctx, tx, newGridID); err != nil {
				return nil, err
			}
			events = append(events, rpc.Event{
				Kind:       rpc.EventGridForked,
				GridForked: &rpc.GridForked{WellID: parentWellID, OldGridID: oldGridID, NewGridID: newGridID},
			})
		}

		if i < len(seq.wells) {
			oldWell := seq.wells[i]
			newWell, ok := wellRemap[oldWell]
			if !ok {
				return nil, fmt.Errorf("internal: well %d (object %s) not remapped", oldWell, wellObjects[i])
			}
			parentWellID = newWell
		}

		seq.grids[i] = newGridID
	}

	newTargetID := targetTileID
	if targetTileID != 0 {
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM tiles WHERE grid_id = ? AND object_id = ?`,
			seq.grids[len(seq.grids)-1], targetObjectID).Scan(&newTargetID)
		if err != nil {
			return nil, fmt.Errorf("relocate target after fork: %w", err)
		}
	}

	return &preWriteResult{
		GridID:       seq.grids[len(seq.grids)-1],
		TargetTileID: newTargetID,
		Events:       events,
	}, nil
}

// forkGrid creates a new grid that is a copy of oldGridID.
func (s *Store) forkGrid(ctx context.Context, tx *sql.Tx, oldGridID int64) (int64, map[int64]int64, error) {
	old, err := s.loadGrid(ctx, tx, oldGridID)
	if err != nil {
		return 0, nil, err
	}

	now := s.now().Unix()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO grids (object_id, version, refcount, created_at)
		 VALUES (?, ?, 0, ?)`,
		old.ObjectID, old.Version, now)
	if err != nil {
		return 0, nil, fmt.Errorf("insert grid: %w", err)
	}
	newGridID, err := res.LastInsertId()
	if err != nil {
		return 0, nil, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, object_id, version, kind, x, y, w, h,
		       view_x, view_y, view_zoom, child_grid_id,
		       text_x, text_y, text_w, text_h, text_mode, blob_id,
		       url_string, preview_jpeg,
		       created_at, updated_at
		FROM tiles WHERE grid_id = ?`, oldGridID)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()

	type tileCopy struct {
		oldID                int64
		objectID             string
		version              int64
		kind                 string
		x, y, w, h           int64
		viewX, viewY         int64
		viewZoom             float64
		childGrid            sql.NullInt64
		textX, textY         int64
		textW, textH         int64
		textMode             sql.NullString
		blob                 sql.NullInt64
		urlString            sql.NullString
		previewJPEG          []byte
		createdAt, updatedAt int64
	}
	var copies []tileCopy
	for rows.Next() {
		var nc tileCopy
		if err := rows.Scan(&nc.oldID, &nc.objectID, &nc.version, &nc.kind,
			&nc.x, &nc.y, &nc.w, &nc.h,
			&nc.viewX, &nc.viewY, &nc.viewZoom, &nc.childGrid,
			&nc.textX, &nc.textY, &nc.textW, &nc.textH, &nc.textMode, &nc.blob,
			&nc.urlString, &nc.previewJPEG,
			&nc.createdAt, &nc.updatedAt); err != nil {
			return 0, nil, err
		}
		copies = append(copies, nc)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}

	remap := make(map[int64]int64, len(copies))
	for _, nc := range copies {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO tiles (object_id, version, grid_id, kind, x, y, w, h,
				view_x, view_y, view_zoom, child_grid_id,
				text_x, text_y, text_w, text_h, text_mode, blob_id,
				url_string, preview_jpeg,
				created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nc.objectID, nc.version, newGridID, nc.kind, nc.x, nc.y, nc.w, nc.h,
			nc.viewX, nc.viewY, nc.viewZoom, nc.childGrid,
			nc.textX, nc.textY, nc.textW, nc.textH, nc.textMode, nc.blob,
			nc.urlString, nc.previewJPEG,
			nc.createdAt, now)
		if err != nil {
			return 0, nil, fmt.Errorf("copy tile: %w", err)
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return 0, nil, err
		}
		remap[nc.oldID] = newID
		if nc.kind == rpc.KindWell && nc.childGrid.Valid {
			if err := s.incRefcount(ctx, tx, nc.childGrid.Int64); err != nil {
				return 0, nil, err
			}
		}
		if nc.kind == rpc.KindText && nc.blob.Valid {
			if _, err := tx.ExecContext(ctx,
				`UPDATE blobs SET refcount = refcount + 1 WHERE id = ?`, nc.blob.Int64); err != nil {
				return 0, nil, err
			}
		}
	}
	return newGridID, remap, nil
}

func (s *Store) incRefcount(ctx context.Context, tx *sql.Tx, gridID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE grids SET refcount = refcount + 1 WHERE id = ?`, gridID)
	return err
}

func (s *Store) decRefcount(ctx context.Context, tx *sql.Tx, gridID int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE grids SET refcount = refcount - 1 WHERE id = ?`, gridID); err != nil {
		return err
	}
	var rc int64
	if err := tx.QueryRowContext(ctx, `SELECT refcount FROM grids WHERE id = ?`, gridID).Scan(&rc); err != nil {
		return err
	}
	if rc <= 0 {
		return s.deleteGrid(ctx, tx, gridID)
	}
	return nil
}

func (s *Store) deleteGrid(ctx context.Context, tx *sql.Tx, gridID int64) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, kind, child_grid_id, blob_id FROM tiles WHERE grid_id = ?`, gridID)
	if err != nil {
		return err
	}
	type ref struct {
		id    int64
		kind  string
		child sql.NullInt64
		blob  sql.NullInt64
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.kind, &r.child, &r.blob); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, r := range refs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM tiles WHERE id = ?`, r.id); err != nil {
			return err
		}
		if r.kind == rpc.KindWell && r.child.Valid {
			if err := s.decRefcount(ctx, tx, r.child.Int64); err != nil {
				return err
			}
		}
		if r.kind == rpc.KindText && r.blob.Valid {
			if err := s.decBlobRefcount(ctx, tx, r.blob.Int64); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM grids WHERE id = ?`, gridID); err != nil {
		return err
	}
	return nil
}

func (s *Store) decBlobRefcount(ctx context.Context, tx *sql.Tx, blobID int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE blobs SET refcount = refcount - 1 WHERE id = ?`, blobID); err != nil {
		return err
	}
	var rc int64
	if err := tx.QueryRowContext(ctx, `SELECT refcount FROM blobs WHERE id = ?`, blobID).Scan(&rc); err != nil {
		return err
	}
	if rc <= 0 {
		_, err := tx.ExecContext(ctx, `DELETE FROM blobs WHERE id = ?`, blobID)
		return err
	}
	return nil
}
