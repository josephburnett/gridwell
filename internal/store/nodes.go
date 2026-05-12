package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// allowedMimes are the file MIME types accepted in v1 (spec §8). The list is
// closed; uploads of other types are rejected.
var allowedMimes = map[string]bool{
	"text/markdown":  true,
	"text/uri-list":  true,
	"image/png":      true,
	"image/jpeg":     true,
	"image/gif":      true,
	"image/webp":     true,
}

// MaxBlobBytes caps a single uploaded file size. The cap exists to bound
// memory and to prevent the SQLite database from growing unboundedly from
// rogue uploads.
const MaxBlobBytes = 16 * 1024 * 1024

// CreateWell creates a new well at (x,y) with footprint (w,h) inside the leaf
// grid of req.Path. The well points at a fresh empty child grid that
// inherits the parent grid's owner/group/mode.
func (s *Store) CreateWell(ctx context.Context, userID int64, req *rpc.CreateWellRequest) (*rpc.Node, error) {
	if req.W <= 0 || req.H <= 0 {
		return nil, fmt.Errorf("%w: w and h must be positive", ErrInvalidArgument)
	}
	if !req.ViewRect.Intersects(req.X, req.Y, req.W, req.H) {
		return nil, ErrLocality
	}
	var out *rpc.Node
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		seq, err := s.buildGridSequence(ctx, tx, userID, req.Path)
		if err != nil {
			return err
		}
		if seq.grids[len(seq.grids)-1] != req.GridID {
			return fmt.Errorf("%w: path leaf grid is %d not %d", ErrInvalidPath, seq.grids[len(seq.grids)-1], req.GridID)
		}
		// Permission check on parent grid.
		_, write, err := s.permForGrid(ctx, tx, userID, req.GridID)
		if err != nil {
			return err
		}
		if !write {
			return ErrPermissionDenied
		}

		pre, err := s.preWrite(ctx, tx, userID, req.Path, 0)
		if err != nil {
			return err
		}
		gridID := pre.GridID
		events = append(events, pre.Events...)

		// Overlap check.
		over, err := overlapsExisting(ctx, tx, gridID, req.X, req.Y, req.W, req.H)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}

		// Create child grid (empty), inherit owner/group/mode from parent.
		parent, err := s.loadGrid(ctx, tx, gridID)
		if err != nil {
			return err
		}
		now := s.now().Unix()
		childObj := s.newID()
		res, err := tx.ExecContext(ctx,
			`INSERT INTO grids (object_id, owner_id, group_id, mode, refcount, created_at)
			 VALUES (?, ?, ?, ?, 1, ?)`,
			childObj, parent.OwnerID, parent.GroupID, parent.Mode, now)
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
			INSERT INTO nodes (object_id, grid_id, type, x, y, w, h, view_x, view_y,
				child_grid_id, capped, owner_id, group_id, mode, created_at, updated_at)
			VALUES (?, ?, 'well', ?, ?, ?, ?, 0, 0, ?, 0, ?, ?, ?, ?, ?)`,
			objID, gridID, req.X, req.Y, req.W, req.H, childGridID,
			parent.OwnerID, parent.GroupID, parent.Mode, now, now)
		if err != nil {
			return fmt.Errorf("insert well: %w", err)
		}
		nodeID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		out, err = s.loadNode(ctx, tx, nodeID)
		if err != nil {
			return err
		}
		events = append(events, rpc.Event{Kind: rpc.EventNodeChanged, NodeChanged: &rpc.NodeChanged{Node: *out}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		s.publish(userID, ev)
	}
	return out, nil
}

// CreateFile uploads bytes as a content-addressed blob and creates a file
// node referencing it. If the blob already exists (same sha256), it is
// reused with refcount bumped.
func (s *Store) CreateFile(ctx context.Context, userID int64, req *rpc.CreateFileRequest) (*rpc.Node, error) {
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

	// Hash outside the transaction — it's the slow step and pure.
	hash := hashBytes(req.Data)

	var out *rpc.Node
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		seq, err := s.buildGridSequence(ctx, tx, userID, req.Path)
		if err != nil {
			return err
		}
		if seq.grids[len(seq.grids)-1] != req.GridID {
			return fmt.Errorf("%w: path leaf grid mismatch", ErrInvalidPath)
		}
		_, write, err := s.permForGrid(ctx, tx, userID, req.GridID)
		if err != nil {
			return err
		}
		if !write {
			return ErrPermissionDenied
		}

		pre, err := s.preWrite(ctx, tx, userID, req.Path, 0)
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

		// Find or insert the blob. Refcount starts at 0; it's bumped after
		// the file row references it.
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

		parent, err := s.loadGrid(ctx, tx, gridID)
		if err != nil {
			return err
		}
		now := s.now().Unix()
		objID := s.newID()
		res, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (object_id, grid_id, type, x, y, w, h, view_x, view_y,
				mime_type, blob_id, owner_id, group_id, mode, created_at, updated_at)
			VALUES (?, ?, 'file', ?, ?, ?, ?, 0, 0, ?, ?, ?, ?, ?, ?, ?)`,
			objID, gridID, req.X, req.Y, req.W, req.H, req.MimeType, blobID,
			parent.OwnerID, parent.GroupID, parent.Mode, now, now)
		if err != nil {
			return fmt.Errorf("insert file: %w", err)
		}
		nodeID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE blobs SET refcount = refcount + 1 WHERE id = ?`, blobID); err != nil {
			return err
		}
		out, err = s.loadNode(ctx, tx, nodeID)
		if err != nil {
			return err
		}
		events = append(events, rpc.Event{Kind: rpc.EventNodeChanged, NodeChanged: &rpc.NodeChanged{Node: *out}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		s.publish(userID, ev)
	}
	return out, nil
}

// ResizeNode changes a node's footprint to (X, Y, W, H). The new
// footprint must not overlap any other node in the grid and must lie
// inside the request's view rect. The full footprint is updated each
// call — corner-drag resize uses this to move any corner.
func (s *Store) ResizeNode(ctx context.Context, userID int64, req *rpc.ResizeNodeRequest) (*rpc.Node, error) {
	if req.W <= 0 || req.H <= 0 {
		return nil, fmt.Errorf("%w: w and h must be positive", ErrInvalidArgument)
	}
	var out *rpc.Node
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		n, err := s.loadNode(ctx, tx, req.NodeID)
		if err != nil {
			return err
		}
		// Locality: existing footprint AND new footprint must each lie
		// inside the framed view. Existing-side prevents action-at-a-
		// distance; new-side prevents flinging the tile out of view.
		if !req.ViewRect.Intersects(n.X, n.Y, n.W, n.H) {
			return ErrLocality
		}
		if !req.ViewRect.Intersects(req.X, req.Y, req.W, req.H) {
			return ErrLocality
		}
		_, write, err := s.permForNode(ctx, tx, userID, req.NodeID)
		if err != nil {
			return err
		}
		if !write {
			return ErrPermissionDenied
		}

		pre, err := s.preWrite(ctx, tx, userID, req.Path, req.NodeID)
		if err != nil {
			return err
		}
		nodeID := pre.TargetNodeID
		events = append(events, pre.Events...)

		// Overlap check against the new footprint, excluding this node.
		over, err := overlapsExisting(ctx, tx, pre.GridID, req.X, req.Y, req.W, req.H, nodeID)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE nodes SET x = ?, y = ?, w = ?, h = ?, updated_at = ? WHERE id = ?`,
			req.X, req.Y, req.W, req.H, s.now().Unix(), nodeID); err != nil {
			return err
		}
		out, err = s.loadNode(ctx, tx, nodeID)
		if err != nil {
			return err
		}
		events = append(events, rpc.Event{Kind: rpc.EventNodeChanged, NodeChanged: &rpc.NodeChanged{Node: *out}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		s.publish(userID, ev)
	}
	return out, nil
}

// SetNodeViewport updates the (view_x, view_y) of a node.
func (s *Store) SetNodeViewport(ctx context.Context, userID int64, req *rpc.SetNodeViewportRequest) (*rpc.Node, error) {
	var out *rpc.Node
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		n, err := s.loadNode(ctx, tx, req.NodeID)
		if err != nil {
			return err
		}
		if !req.ViewRect.Intersects(n.X, n.Y, n.W, n.H) {
			return ErrLocality
		}
		_, write, err := s.permForNode(ctx, tx, userID, req.NodeID)
		if err != nil {
			return err
		}
		if !write {
			return ErrPermissionDenied
		}
		pre, err := s.preWrite(ctx, tx, userID, req.Path, req.NodeID)
		if err != nil {
			return err
		}
		events = append(events, pre.Events...)
		// view_zoom = 0 is the sentinel "no view set yet" — the client
		// uses calibrated descent zoom in that case. Older clients (or
		// callers that don't care about zoom) send 0; we store 0.
		if _, err := tx.ExecContext(ctx,
			`UPDATE nodes SET view_x = ?, view_y = ?, view_zoom = ?, updated_at = ? WHERE id = ?`,
			req.ViewX, req.ViewY, req.ViewZoom, s.now().Unix(), pre.TargetNodeID); err != nil {
			return err
		}
		out, err = s.loadNode(ctx, tx, pre.TargetNodeID)
		if err != nil {
			return err
		}
		events = append(events, rpc.Event{Kind: rpc.EventNodeChanged, NodeChanged: &rpc.NodeChanged{Node: *out}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		s.publish(userID, ev)
	}
	return out, nil
}

// CapWell sets capped=true on a well.
func (s *Store) CapWell(ctx context.Context, userID int64, req *rpc.CapWellRequest) (*rpc.Node, error) {
	return s.flipWell(ctx, userID, req.Path, req.ViewRect, req.NodeID, true)
}

// RedigWell clears capped on a well.
func (s *Store) RedigWell(ctx context.Context, userID int64, req *rpc.RedigWellRequest) (*rpc.Node, error) {
	return s.flipWell(ctx, userID, req.Path, req.ViewRect, req.NodeID, false)
}

func (s *Store) flipWell(ctx context.Context, userID int64, p rpc.Path, vr rpc.ViewRect, nodeID int64, capped bool) (*rpc.Node, error) {
	var out *rpc.Node
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		n, err := s.loadNode(ctx, tx, nodeID)
		if err != nil {
			return err
		}
		if n.Type != "well" {
			return fmt.Errorf("%w: node is not a well", ErrInvalidArgument)
		}
		if !vr.Contains(n.X, n.Y, n.W, n.H) {
			return ErrLocality
		}
		if capped && n.Capped {
			return ErrCapped
		}
		if !capped && !n.Capped {
			return ErrNotCapped
		}
		_, write, err := s.permForNode(ctx, tx, userID, nodeID)
		if err != nil {
			return err
		}
		if !write {
			return ErrPermissionDenied
		}
		pre, err := s.preWrite(ctx, tx, userID, p, nodeID)
		if err != nil {
			return err
		}
		events = append(events, pre.Events...)
		var v int64
		if capped {
			v = 1
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE nodes SET capped = ?, updated_at = ? WHERE id = ?`,
			v, s.now().Unix(), pre.TargetNodeID); err != nil {
			return err
		}
		out, err = s.loadNode(ctx, tx, pre.TargetNodeID)
		if err != nil {
			return err
		}
		events = append(events, rpc.Event{Kind: rpc.EventNodeChanged, NodeChanged: &rpc.NodeChanged{Node: *out}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		s.publish(userID, ev)
	}
	return out, nil
}

// FillWell deletes an empty well and its (empty) child grid. Returns
// ErrNotEmpty if the child grid contains any nodes.
func (s *Store) FillWell(ctx context.Context, userID int64, req *rpc.FillWellRequest) error {
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		n, err := s.loadNode(ctx, tx, req.NodeID)
		if err != nil {
			return err
		}
		if n.Type != "well" {
			return fmt.Errorf("%w: node is not a well", ErrInvalidArgument)
		}
		if !req.ViewRect.Intersects(n.X, n.Y, n.W, n.H) {
			return ErrLocality
		}
		// Permission: w on parent grid.
		_, write, err := s.permForGrid(ctx, tx, userID, n.GridID)
		if err != nil {
			return err
		}
		if !write {
			return ErrPermissionDenied
		}
		var nodeCount int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM nodes WHERE grid_id = ?`, n.ChildGridID).Scan(&nodeCount); err != nil {
			return err
		}
		if nodeCount > 0 {
			return ErrNotEmpty
		}
		pre, err := s.preWrite(ctx, tx, userID, req.Path, req.NodeID)
		if err != nil {
			return err
		}
		events = append(events, pre.Events...)
		// Reload the well after potential fork.
		w, err := s.loadNode(ctx, tx, pre.TargetNodeID)
		if err != nil {
			return err
		}
		// Delete the well row, then dec refcount on its child grid.
		if _, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, pre.TargetNodeID); err != nil {
			return err
		}
		if err := s.decRefcount(ctx, tx, w.ChildGridID); err != nil {
			return err
		}
		events = append(events, rpc.Event{Kind: rpc.EventNodeRemoved, NodeRemoved: &rpc.NodeRemoved{GridID: pre.GridID, NodeID: pre.TargetNodeID}})
		return nil
	})
	if err != nil {
		return err
	}
	for _, ev := range events {
		s.publish(userID, ev)
	}
	return nil
}

// AscendAtRoot creates a new grid with a single well pointing at the user's
// current root grid, and updates the user's root_grid_id to the new grid.
// The new well is at (0,0) with size 1×1.
func (s *Store) AscendAtRoot(ctx context.Context, userID int64) (*rpc.AscendAtRootResponse, error) {
	var resp rpc.AscendAtRootResponse
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		u, err := getUserQR(ctx, tx, userID)
		if err != nil {
			return err
		}
		// Create a new grid owned by this user, inheriting old root's
		// owner/group/mode for consistency.
		old, err := s.loadGrid(ctx, tx, u.RootGridID)
		if err != nil {
			return err
		}
		now := s.now().Unix()
		newObj := s.newID()
		res, err := tx.ExecContext(ctx,
			`INSERT INTO grids (object_id, owner_id, group_id, mode, refcount, created_at) VALUES (?, ?, ?, ?, 1, ?)`,
			newObj, old.OwnerID, old.GroupID, old.Mode, now)
		if err != nil {
			return err
		}
		newGridID, err := res.LastInsertId()
		if err != nil {
			return err
		}

		// Create well in new grid pointing at old root.
		wellObj := s.newID()
		res, err = tx.ExecContext(ctx, `
			INSERT INTO nodes (object_id, grid_id, type, x, y, w, h, view_x, view_y,
				child_grid_id, capped, owner_id, group_id, mode, created_at, updated_at)
			VALUES (?, ?, 'well', 0, 0, 1, 1, 0, 0, ?, 0, ?, ?, ?, ?, ?)`,
			wellObj, newGridID, u.RootGridID, old.OwnerID, old.GroupID, old.Mode, now, now)
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

		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET root_grid_id = ? WHERE id = ?`, newGridID, userID); err != nil {
			return err
		}
		resp.NewRootGridID = newGridID
		resp.WellID = wellID
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.publish(userID, rpc.Event{Kind: rpc.EventGridChanged, GridChanged: &rpc.GridChanged{GridID: resp.NewRootGridID}})
	return &resp, nil
}
