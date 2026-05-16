package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// GetGrid returns the grid plus all of its tiles, or ErrPermissionDenied if
// the user lacks read on the grid.
//
// Tiles are returned in their stored order. The caller (RPC layer) substitutes
// "locked" placeholders for tiles the user cannot read; the store returns the
// raw rows so callers can decide.
func (s *Store) GetGrid(ctx context.Context, userID, gridID int64) (*rpc.GetGridResponse, error) {
	read, write, err := s.permForGrid(ctx, s.db, userID, gridID)
	if err != nil {
		return nil, err
	}
	if !read {
		return nil, ErrPermissionDenied
	}
	g, err := s.loadGrid(ctx, s.db, gridID)
	if err != nil {
		return nil, err
	}
	tiles, err := s.loadTilesInGrid(ctx, s.db, gridID)
	if err != nil {
		return nil, err
	}
	return &rpc.GetGridResponse{Grid: *g, Tiles: tiles, Readable: true, Writable: write}, nil
}

// gridReader is the interface needed to read grid/tile rows. Both *sql.DB and
// *sql.Tx satisfy it.
type gridReader interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (s *Store) loadGrid(ctx context.Context, q gridReader, gridID int64) (*rpc.Grid, error) {
	var g rpc.Grid
	err := q.QueryRowContext(ctx,
		`SELECT id, object_id, owner_id, group_id, mode, default_view_cx, default_view_cy, default_zoom
		 FROM grids WHERE id = ?`, gridID,
	).Scan(&g.ID, &g.ObjectID, &g.OwnerID, &g.GroupID, &g.Mode,
		&g.DefaultViewCx, &g.DefaultViewCy, &g.DefaultZoom)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func (s *Store) loadTile(ctx context.Context, q gridReader, tileID int64) (*rpc.Tile, error) {
	var (
		n         rpc.Tile
		childGrid sql.NullInt64
		mime      sql.NullString
		blob      sql.NullInt64
		urlStr    sql.NullString
		cappedInt int64
	)
	err := q.QueryRowContext(ctx, `
		SELECT id, object_id, grid_id, type, x, y, w, h, view_x, view_y, view_zoom,
		       child_grid_id, capped, mime_type, blob_id, url_string, owner_id, group_id, mode
		FROM tiles WHERE id = ?`, tileID,
	).Scan(&n.ID, &n.ObjectID, &n.GridID, &n.Type, &n.X, &n.Y, &n.W, &n.H,
		&n.ViewX, &n.ViewY, &n.ViewZoom, &childGrid, &cappedInt, &mime, &blob, &urlStr,
		&n.OwnerID, &n.GroupID, &n.Mode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if childGrid.Valid {
		n.ChildGridID = childGrid.Int64
	}
	if mime.Valid {
		n.MimeType = mime.String
	}
	if blob.Valid {
		n.BlobID = blob.Int64
	}
	if urlStr.Valid {
		n.URLString = urlStr.String
	}
	n.Capped = cappedInt != 0
	return &n, nil
}

func (s *Store) loadTilesInGrid(ctx context.Context, q gridReader, gridID int64) ([]rpc.Tile, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, object_id, grid_id, type, x, y, w, h, view_x, view_y, view_zoom,
		       child_grid_id, capped, mime_type, blob_id, url_string, owner_id, group_id, mode
		FROM tiles WHERE grid_id = ? ORDER BY id`, gridID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rpc.Tile
	for rows.Next() {
		var (
			n         rpc.Tile
			childGrid sql.NullInt64
			mime      sql.NullString
			blob      sql.NullInt64
			urlStr    sql.NullString
			cappedInt int64
		)
		if err := rows.Scan(&n.ID, &n.ObjectID, &n.GridID, &n.Type, &n.X, &n.Y, &n.W, &n.H,
			&n.ViewX, &n.ViewY, &n.ViewZoom, &childGrid, &cappedInt, &mime, &blob, &urlStr,
			&n.OwnerID, &n.GroupID, &n.Mode); err != nil {
			return nil, err
		}
		if childGrid.Valid {
			n.ChildGridID = childGrid.Int64
		}
		if mime.Valid {
			n.MimeType = mime.String
		}
		if blob.Valid {
			n.BlobID = blob.Int64
		}
		if urlStr.Valid {
			n.URLString = urlStr.String
		}
		n.Capped = cappedInt != 0
		out = append(out, n)
	}
	return out, rows.Err()
}

// GetTile returns a single tile by ID, after checking that the user
// has read permission on it.
func (s *Store) GetTile(ctx context.Context, userID, tileID int64) (*rpc.Tile, error) {
	read, _, err := s.permForTile(ctx, s.db, userID, tileID)
	if err != nil {
		return nil, err
	}
	if !read {
		return nil, ErrPermissionDenied
	}
	return s.loadTile(ctx, s.db, tileID)
}

// GetTilePreview returns the JPEG bytes for a URL tile's current preview.
// Returns nil for tiles that don't have a preview yet (brand-new URL tile,
// or non-URL tile). Requires read permission on the tile.
func (s *Store) GetTilePreview(ctx context.Context, userID, tileID int64) ([]byte, error) {
	read, _, err := s.permForTile(ctx, s.db, userID, tileID)
	if err != nil {
		return nil, err
	}
	if !read {
		return nil, ErrPermissionDenied
	}
	var jpeg []byte
	err = s.db.QueryRowContext(ctx, `SELECT preview_jpeg FROM tiles WHERE id = ?`, tileID).Scan(&jpeg)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return jpeg, nil
}

// SetGridDefaultView updates the grid's stored default viewport.
// Requires write permission on the grid. Emits a GridChanged event so
// any subscribed clients refetch.
func (s *Store) SetGridDefaultView(ctx context.Context, userID int64, req *rpc.SetGridDefaultViewRequest) (*rpc.Grid, error) {
	var out *rpc.Grid
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		_, write, err := s.permForGrid(ctx, tx, userID, req.GridID)
		if err != nil {
			return err
		}
		if !write {
			return ErrPermissionDenied
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE grids SET default_view_cx = ?, default_view_cy = ?, default_zoom = ?
			 WHERE id = ?`,
			req.Cx, req.Cy, req.Zoom, req.GridID); err != nil {
			return err
		}
		out, err = s.loadGrid(ctx, tx, req.GridID)
		if err != nil {
			return err
		}
		events = append(events, rpc.Event{Kind: rpc.EventGridChanged, GridChanged: &rpc.GridChanged{GridID: req.GridID}})
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

// overlapsExisting reports whether the rectangle (x,y,w,h) overlaps any tile
// in the grid except those whose id is in the excludeIDs set.
//
// Two AABBs do not overlap iff one is entirely to the left, right, above, or
// below the other. We translate that into "lo strictly less than hi on every
// axis" for the overlap region.
func overlapsExisting(ctx context.Context, q gridReader, gridID, x, y, w, h int64, excludeIDs ...int64) (bool, error) {
	// Build a NOT IN clause if needed.
	args := []any{gridID, x + w, x, y + h, y}
	excl := ""
	if len(excludeIDs) > 0 {
		excl = " AND id NOT IN ("
		for i, id := range excludeIDs {
			if i > 0 {
				excl += ","
			}
			excl += "?"
			args = append(args, id)
		}
		excl += ")"
	}
	q1 := fmt.Sprintf(`
		SELECT 1 FROM tiles
		WHERE grid_id = ?
		  AND x < ? AND (x + w) > ?
		  AND y < ? AND (y + h) > ?
		%s LIMIT 1`, excl)
	var n int
	err := q.QueryRowContext(ctx, q1, args...).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

