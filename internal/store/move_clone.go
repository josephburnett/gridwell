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

// MoveTile moves a tile either within its grid or across grids. When the
// source grid and dest grid differ, both must be reachable via valid paths
// (req.Path for source, req.DestPath for dest), and the user must have write
// permission on both grids. CoW fork is performed on each path independently
// when needed.
//
// Cross-grid move is the "teleport" gesture from §4.2.
func (s *Store) MoveTile(ctx context.Context, userID int64, req *rpc.MoveTileRequest) (*rpc.Tile, error) {
	var out *rpc.Tile
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// Load the source node up front so we can validate its current
		// position against the source view rect.
		n, err := s.loadTile(ctx, tx, req.TileID)
		if err != nil {
			return err
		}
		// Source-side locality (the existing footprint must lie inside
		// the source view rect).
		if !req.ViewRect.Intersects(n.X, n.Y, n.W, n.H) {
			return ErrLocality
		}
		// Dest-side locality (the new footprint must lie inside the dest
		// view rect, with the existing w/h since move doesn't resize).
		if !req.DestViewRect.Intersects(req.X, req.Y, n.W, n.H) {
			return ErrLocality
		}

		// Validate paths and confirm the source node is in the source
		// path's leaf grid.
		srcSeq, err := s.buildGridSequence(ctx, tx, userID, req.Path)
		if err != nil {
			return err
		}
		srcGrid := srcSeq.grids[len(srcSeq.grids)-1]
		if n.GridID != srcGrid {
			return fmt.Errorf("%w: node %d not in source path leaf grid %d", ErrInvalidPath, req.TileID, srcGrid)
		}

		// Permissions: w on source grid (for removal) and w on dest grid (for insertion).
		_, srcWrite, err := s.permForGrid(ctx, tx, userID, srcGrid)
		if err != nil {
			return err
		}
		if !srcWrite {
			return ErrPermissionDenied
		}
		_, dstWrite, err := s.permForGrid(ctx, tx, userID, req.DestGridID)
		if err != nil {
			return err
		}
		if !dstWrite {
			return ErrPermissionDenied
		}

		// Source-side fork: ensure the source path leaf is private, since
		// we're about to remove a node from it.
		srcPre, err := s.preWrite(ctx, tx, userID, req.Path, req.TileID)
		if err != nil {
			return err
		}
		events = append(events, srcPre.Events...)
		tileID := srcPre.TargetTileID
		srcGrid = srcPre.GridID

		// Dest-side: validate dest path and fork if needed. If dest is the
		// same grid as src (after fork), skip the dest fork — they're
		// already coherent.
		dstSeq, err := s.buildGridSequence(ctx, tx, userID, req.DestPath)
		if err != nil {
			return err
		}
		dstGrid := dstSeq.grids[len(dstSeq.grids)-1]
		if dstGrid != req.DestGridID {
			return fmt.Errorf("%w: dest path leaf is %d not %d", ErrInvalidPath, dstGrid, req.DestGridID)
		}

		var dstPre *preWriteResult
		if dstGrid != srcGrid {
			dstPre, err = s.preWrite(ctx, tx, userID, req.DestPath, 0)
			if err != nil {
				return err
			}
			events = append(events, dstPre.Events...)
			dstGrid = dstPre.GridID
		} else {
			dstGrid = srcGrid
		}

		// Refuse to move a well into itself or one of its descendants.
		// The dest path is the chain of wells the user descended through
		// to reach the destination grid; if our well is on that chain,
		// dropping there would either point the well at its own child
		// (immediate cycle, well becomes unreachable) or at a descendant
		// (deeper cycle). Both orphan the well from any path that walks
		// down from root.
		if n.Type == "well" {
			for _, wid := range req.DestPath.WellIDs {
				if wid == tileID {
					return fmt.Errorf("%w: cannot move a well into itself or a descendant", ErrInvalidArgument)
				}
			}
		}

		// Overlap check on the destination, excluding the moving node iff
		// dest is the same grid as src.
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

		// Apply: update grid_id (if moving across) and (x, y).
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET grid_id = ?, x = ?, y = ?, updated_at = ? WHERE id = ?`,
			dstGrid, req.X, req.Y, s.now().Unix(), tileID); err != nil {
			return err
		}
		out, err = s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		// Cross-grid move: fire NodeRemoved on src, NodeChanged on dst.
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
		s.publish(userID, ev)
	}
	return out, nil
}

// CloneTile duplicates a tile into a destination grid at (x, y). Inherits the
// source's owner/group/mode (spec §7.6). For wells, the clone shares the
// child grid (CoW deferred to the first write through either clone).
func (s *Store) CloneTile(ctx context.Context, userID int64, req *rpc.CloneTileRequest) (*rpc.Tile, error) {
	var out *rpc.Tile
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		n, err := s.loadTile(ctx, tx, req.TileID)
		if err != nil {
			return err
		}
		// Source locality: existing footprint inside source view rect.
		if !req.ViewRect.Intersects(n.X, n.Y, n.W, n.H) {
			return ErrLocality
		}
		// Dest locality: clone target footprint inside dest view rect.
		if !req.DestViewRect.Intersects(req.X, req.Y, n.W, n.H) {
			return ErrLocality
		}

		// Validate paths.
		srcSeq, err := s.buildGridSequence(ctx, tx, userID, req.Path)
		if err != nil {
			return err
		}
		if n.GridID != srcSeq.grids[len(srcSeq.grids)-1] {
			return fmt.Errorf("%w: node %d not in source path leaf grid", ErrInvalidPath, req.TileID)
		}
		dstSeq, err := s.buildGridSequence(ctx, tx, userID, req.DestPath)
		if err != nil {
			return err
		}
		if req.DestGridID != dstSeq.grids[len(dstSeq.grids)-1] {
			return fmt.Errorf("%w: dest path leaf mismatch", ErrInvalidPath)
		}

		// Permissions: write on the SOURCE NODE (clone privilege), and
		// write on the DEST GRID. Spec §7.3.
		_, srcWrite, err := s.permForTile(ctx, tx, userID, req.TileID)
		if err != nil {
			return err
		}
		if !srcWrite {
			return ErrPermissionDenied
		}
		_, dstWrite, err := s.permForGrid(ctx, tx, userID, req.DestGridID)
		if err != nil {
			return err
		}
		if !dstWrite {
			return ErrPermissionDenied
		}

		// Fork the dest path if it's shared. Source isn't being mutated,
		// so no source-side fork is needed; we just read the row.
		dstPre, err := s.preWrite(ctx, tx, userID, req.DestPath, 0)
		if err != nil {
			return err
		}
		events = append(events, dstPre.Events...)
		dstGrid := dstPre.GridID

		// Overlap check on dest.
		over, err := overlapsExisting(ctx, tx, dstGrid, req.X, req.Y, n.W, n.H)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}

		// Insert the clone. Same object_id, new row id, same owner/group/
		// mode, same content (child_grid_id, blob_id, or url_string+preview).
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
		res, err := tx.ExecContext(ctx, `
			INSERT INTO tiles (object_id, grid_id, type, x, y, w, h, view_x, view_y, view_zoom,
				child_grid_id, capped, mime_type, blob_id, url_string, preview_jpeg,
				owner_id, group_id, mode, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			n.ObjectID, dstGrid, n.Type, req.X, req.Y, n.W, n.H, n.ViewX, n.ViewY, n.ViewZoom,
			child, cappedInt, mime, blob, urlStr, previewJPEG,
			n.OwnerID, n.GroupID, n.Mode, now, now)
		if err != nil {
			return fmt.Errorf("insert clone: %w", err)
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		// Bump child grid or blob refcount for the new pointer. URL tiles
		// don't reference shared resources, so nothing to bump.
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
		s.publish(userID, ev)
	}
	return out, nil
}

// UpdateFileContent replaces a file tile's blob with new bytes.
func (s *Store) UpdateFileContent(ctx context.Context, userID int64, req *rpc.UpdateFileContentRequest) (*rpc.Tile, error) {
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
		// Image and uri-list are read-only per spec §13.
		if n.MimeType != "text/markdown" {
			return fmt.Errorf("%w: %s is read-only", ErrInvalidArgument, n.MimeType)
		}
		if !req.ViewRect.Intersects(n.X, n.Y, n.W, n.H) {
			return ErrLocality
		}
		_, write, err := s.permForTile(ctx, tx, userID, req.TileID)
		if err != nil {
			return err
		}
		if !write {
			return ErrPermissionDenied
		}
		pre, err := s.preWrite(ctx, tx, userID, req.Path, req.TileID)
		if err != nil {
			return err
		}
		events = append(events, pre.Events...)

		// Compute new blob hash, find or insert.
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

		// Reload the (post-fork) node to get its current blob_id.
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
		// Adjust blob refcounts. If old == new (no-op write), skip.
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
		s.publish(userID, ev)
	}
	return out, nil
}

// sha256Hex hashes the data and returns the hex-encoded sha256.
func sha256Hex(data []byte) string {
	// Defined here to avoid an import cycle from cow.go test scenarios; the
	// actual hash work is delegated to crypto/sha256 + encoding/hex.
	return hashBytes(data)
}
