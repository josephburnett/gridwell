package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/josephburnett/ascent/internal/rpc"
)

// gridSequence is the sequence of grid ids from the user's root down to the
// leaf grid the editing pane is in. grids[0] is always the user's root grid;
// grids[len-1] is the leaf. wells[i] is the well in grids[i] that points at
// grids[i+1], so len(wells) == len(grids)-1.
type gridSequence struct {
	grids []int64
	wells []int64
}

// buildGridSequence validates the path and returns the sequence of grids and
// path wells for it.
func (s *Store) buildGridSequence(ctx context.Context, q gridReader, userID int64, p rpc.Path) (gridSequence, error) {
	u, err := getUserQR(ctx, q, userID)
	if err != nil {
		return gridSequence{}, err
	}
	seq := gridSequence{grids: []int64{u.RootGridID}}
	for _, wellID := range p.WellIDs {
		w, err := s.loadNode(ctx, q, wellID)
		if err != nil {
			return gridSequence{}, fmt.Errorf("%w: well %d: %v", ErrInvalidPath, wellID, err)
		}
		if w.Type != "well" {
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
	// GridID is the (possibly new, post-fork) leaf grid id where mutations
	// must now apply.
	GridID int64
	// TargetNodeID is the (possibly new) id of the input target node after
	// any fork. Zero if no target was supplied.
	TargetNodeID int64
	// Events is a list of GridForked events to publish to subscribers
	// after the transaction commits.
	Events []rpc.Event
}

// preWrite ensures that the leaf grid in the descent path is unshared,
// forking grids up the path as needed so the editing pane has a private
// chain. If a target node id is supplied (non-zero), it must lie in the
// leaf grid at call time; the returned TargetNodeID is its post-fork id.
//
// preWrite must be called inside a transaction; it does not commit.
func (s *Store) preWrite(ctx context.Context, tx *sql.Tx, userID int64, path rpc.Path, targetNodeID int64) (*preWriteResult, error) {
	seq, err := s.buildGridSequence(ctx, tx, userID, path)
	if err != nil {
		return nil, err
	}

	// Find the topmost grid index that needs forking by walking from the
	// leaf upward and stopping at the first refcount==1.
	numForks := 0
	for i := len(seq.grids) - 1; i >= 0; i-- {
		var rc int64
		err := tx.QueryRowContext(ctx, `SELECT refcount FROM grids WHERE id = ?`, seq.grids[i]).Scan(&rc)
		if err != nil {
			return nil, err
		}
		// Root has +1 in its refcount baseline; refcount==1 means no other
		// well points at it. For non-root grids, refcount==1 means exactly
		// one well points at it (the parent's well on this path).
		if rc <= 1 {
			break
		}
		numForks++
		// Don't try to fork the root grid; the path's well above it is
		// the user's root_grid_id anchor, which has no parent well.
		if i == 0 {
			return nil, fmt.Errorf("internal: root grid is shared (refcount=%d)", rc)
		}
	}

	if numForks == 0 {
		return &preWriteResult{GridID: seq.grids[len(seq.grids)-1], TargetNodeID: targetNodeID}, nil
	}

	// We will fork grids at indices [topForkIdx .. len-1].
	topForkIdx := len(seq.grids) - numForks

	// Prep the per-grid object_id of the path well (so we can find its
	// copy after a fork by object_id rather than row id).
	wellObjects := make([]string, len(seq.wells))
	for i, wid := range seq.wells {
		var obj string
		if err := tx.QueryRowContext(ctx, `SELECT object_id FROM nodes WHERE id = ?`, wid).Scan(&obj); err != nil {
			return nil, err
		}
		wellObjects[i] = obj
	}
	// Object id of the target node, if any.
	var targetObjectID string
	if targetNodeID != 0 {
		err := tx.QueryRowContext(ctx, `SELECT object_id FROM nodes WHERE id = ?`, targetNodeID).Scan(&targetObjectID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: target node %d", ErrNotFound, targetNodeID)
		}
		if err != nil {
			return nil, err
		}
	}

	events := []rpc.Event{}

	// Walk shallowest-first so we can update the well in the grid above
	// each fork, which has just been forked itself (or, for the topmost
	// fork, is the unchanged parent grid).
	parentWellID := int64(0) // the well above the current fork that we must redirect
	if topForkIdx > 0 {
		// Parent grid is unchanged. Its well to redirect is seq.wells[topForkIdx-1].
		parentWellID = seq.wells[topForkIdx-1]
	}

	prevForkedGridID := int64(0) // tracks the new grid id from the previous (shallower) fork
	_ = prevForkedGridID

	for i := topForkIdx; i < len(seq.grids); i++ {
		oldGridID := seq.grids[i]
		newGridID, wellRemap, err := s.forkGrid(ctx, tx, oldGridID)
		if err != nil {
			return nil, fmt.Errorf("fork grid %d: %w", oldGridID, err)
		}

		// Redirect the parent well (which lives in the previous-iteration's
		// new grid, or in the unchanged parent grid for the topmost fork)
		// to point at newGridID.
		if parentWellID != 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE nodes SET child_grid_id = ?, updated_at = ? WHERE id = ?`,
				newGridID, s.now().Unix(), parentWellID); err != nil {
				return nil, err
			}
			// The well used to point at oldGridID. Decrement its refcount.
			if err := s.decRefcount(ctx, tx, oldGridID); err != nil {
				return nil, err
			}
			// And bump the refcount on newGridID for the new pointer.
			if err := s.incRefcount(ctx, tx, newGridID); err != nil {
				return nil, err
			}
			events = append(events, rpc.Event{
				Kind:       rpc.EventGridForked,
				GridForked: &rpc.GridForked{WellID: parentWellID, OldGridID: oldGridID, NewGridID: newGridID},
			})
		}

		// Set up parentWellID for the next iteration: the path well that
		// points from this newly-forked grid down. It was copied during
		// forkGrid; its old id was seq.wells[i] and its new id is in
		// wellRemap. After this loop iteration, when we fork the next
		// (deeper) grid, we will redirect this well to point at the
		// newer grid id.
		if i < len(seq.wells) {
			oldWell := seq.wells[i]
			newWell, ok := wellRemap[oldWell]
			if !ok {
				return nil, fmt.Errorf("internal: well %d (object %s) not remapped", oldWell, wellObjects[i])
			}
			parentWellID = newWell
		}

		// Update grid sequence in-place so that subsequent iterations see
		// the new id.
		seq.grids[i] = newGridID
	}

	// Translate target node id if it was in a forked grid.
	newTargetID := targetNodeID
	if targetNodeID != 0 {
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM nodes WHERE grid_id = ? AND object_id = ?`,
			seq.grids[len(seq.grids)-1], targetObjectID).Scan(&newTargetID)
		if err != nil {
			return nil, fmt.Errorf("relocate target after fork: %w", err)
		}
	}

	return &preWriteResult{
		GridID:       seq.grids[len(seq.grids)-1],
		TargetNodeID: newTargetID,
		Events:       events,
	}, nil
}

// forkGrid creates a new grid that is a copy of oldGridID. Returns the new
// grid id and a mapping from old node id to new node id in the copy.
// The new grid starts with refcount=0 (the caller must increment it as it
// installs the well that points to it).
//
// For each well copied into the new grid, the child grid's refcount is
// incremented (the new well shares the same child grid). The blob refcount
// is similarly incremented for each file copied.
func (s *Store) forkGrid(ctx context.Context, tx *sql.Tx, oldGridID int64) (int64, map[int64]int64, error) {
	// Load the old grid's metadata.
	old, err := s.loadGrid(ctx, tx, oldGridID)
	if err != nil {
		return 0, nil, err
	}

	// Insert the new grid row. Same object_id (clone-distinct rows share
	// lineage), refcount=0 to start (caller bumps it on install).
	now := s.now().Unix()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO grids (object_id, owner_id, group_id, mode, refcount, default_view_cx, default_view_cy, default_zoom, created_at)
		 VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		old.ObjectID, old.OwnerID, old.GroupID, old.Mode,
		old.DefaultViewCx, old.DefaultViewCy, old.DefaultZoom, now)
	if err != nil {
		return 0, nil, fmt.Errorf("insert grid: %w", err)
	}
	newGridID, err := res.LastInsertId()
	if err != nil {
		return 0, nil, err
	}

	// Copy each node row, bumping child grid / blob refcounts as needed.
	rows, err := tx.QueryContext(ctx, `
		SELECT id, object_id, type, x, y, w, h, view_x, view_y,
		       child_grid_id, capped, mime_type, blob_id, owner_id, group_id, mode,
		       created_at, updated_at
		FROM nodes WHERE grid_id = ?`, oldGridID)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()

	type nodeCopy struct {
		oldID     int64
		objectID  string
		typ       string
		x, y, w, h int64
		viewX, viewY int64
		childGrid sql.NullInt64
		cappedInt int64
		mime      sql.NullString
		blob      sql.NullInt64
		ownerID, groupID int64
		mode      int32
		createdAt, updatedAt int64
	}
	var copies []nodeCopy
	for rows.Next() {
		var nc nodeCopy
		if err := rows.Scan(&nc.oldID, &nc.objectID, &nc.typ, &nc.x, &nc.y, &nc.w, &nc.h,
			&nc.viewX, &nc.viewY, &nc.childGrid, &nc.cappedInt, &nc.mime, &nc.blob,
			&nc.ownerID, &nc.groupID, &nc.mode, &nc.createdAt, &nc.updatedAt); err != nil {
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
			INSERT INTO nodes (object_id, grid_id, type, x, y, w, h, view_x, view_y,
				child_grid_id, capped, mime_type, blob_id, owner_id, group_id, mode,
				created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nc.objectID, newGridID, nc.typ, nc.x, nc.y, nc.w, nc.h, nc.viewX, nc.viewY,
			nc.childGrid, nc.cappedInt, nc.mime, nc.blob, nc.ownerID, nc.groupID, nc.mode,
			nc.createdAt, now)
		if err != nil {
			return 0, nil, fmt.Errorf("copy node: %w", err)
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return 0, nil, err
		}
		remap[nc.oldID] = newID
		if nc.typ == "well" && nc.childGrid.Valid {
			if err := s.incRefcount(ctx, tx, nc.childGrid.Int64); err != nil {
				return 0, nil, err
			}
		}
		if nc.typ == "file" && nc.blob.Valid {
			if _, err := tx.ExecContext(ctx,
				`UPDATE blobs SET refcount = refcount + 1 WHERE id = ?`, nc.blob.Int64); err != nil {
				return 0, nil, err
			}
		}
	}
	return newGridID, remap, nil
}

// incRefcount increments a grid's refcount. Used when a well starts pointing
// at it.
func (s *Store) incRefcount(ctx context.Context, tx *sql.Tx, gridID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE grids SET refcount = refcount + 1 WHERE id = ?`, gridID)
	return err
}

// decRefcount decrements a grid's refcount. If it reaches 0, the grid (and
// everything inside it) is garbage collected.
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

// deleteGrid removes a grid row and all of its nodes, decrementing refcounts
// on referenced child grids and blobs (recursively, possibly deleting them).
func (s *Store) deleteGrid(ctx context.Context, tx *sql.Tx, gridID int64) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, type, child_grid_id, blob_id FROM nodes WHERE grid_id = ?`, gridID)
	if err != nil {
		return err
	}
	type ref struct {
		id       int64
		typ      string
		child    sql.NullInt64
		blob     sql.NullInt64
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.typ, &r.child, &r.blob); err != nil {
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
		if _, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, r.id); err != nil {
			return err
		}
		if r.typ == "well" && r.child.Valid {
			if err := s.decRefcount(ctx, tx, r.child.Int64); err != nil {
				return err
			}
		}
		if r.typ == "file" && r.blob.Valid {
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

// decBlobRefcount decrements a blob's refcount; deletes if it reaches 0.
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
