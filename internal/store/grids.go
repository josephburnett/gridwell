package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// GetGrid returns the grid plus all of its tiles.
func (s *Store) GetGrid(ctx context.Context, gridID int64) (*rpc.GetGridResponse, error) {
	g, err := s.loadGrid(ctx, s.db, gridID)
	if err != nil {
		return nil, err
	}
	tiles, err := s.loadTilesInGrid(ctx, s.db, gridID)
	if err != nil {
		return nil, err
	}
	return &rpc.GetGridResponse{Grid: *g, Tiles: tiles}, nil
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
		`SELECT id, object_id, version FROM grids WHERE id = ?`, gridID,
	).Scan(&g.ID, &g.ObjectID, &g.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// tileColumns is the column list for reading a tile row. Keep in sync with
// scanTile.
const tileColumns = `id, object_id, version, grid_id, kind, x, y, w, h,
	view_x, view_y, view_zoom, child_grid_id,
	text_x, text_y, text_w, text_h, text_mode, blob_id,
	url_string`

// scanTile scans a single row into an rpc.Tile. It expects the columns to
// match tileColumns in order.
func scanTile(scanner interface {
	Scan(dest ...any) error
}) (*rpc.Tile, error) {
	var (
		n         rpc.Tile
		childGrid sql.NullInt64
		blob      sql.NullInt64
		urlStr    sql.NullString
		textMode  sql.NullString
	)
	if err := scanner.Scan(
		&n.ID, &n.ObjectID, &n.Version, &n.GridID, &n.Kind,
		&n.X, &n.Y, &n.W, &n.H,
		&n.ViewX, &n.ViewY, &n.ViewZoom, &childGrid,
		&n.TextX, &n.TextY, &n.TextW, &n.TextH, &textMode, &blob,
		&urlStr,
	); err != nil {
		return nil, err
	}
	if childGrid.Valid {
		n.ChildGridID = childGrid.Int64
	}
	if blob.Valid {
		n.BlobID = blob.Int64
	}
	if urlStr.Valid {
		n.URLString = urlStr.String
	}
	if textMode.Valid {
		n.TextMode = textMode.String
	}
	return &n, nil
}

func (s *Store) loadTile(ctx context.Context, q gridReader, tileID int64) (*rpc.Tile, error) {
	row := q.QueryRowContext(ctx, `SELECT `+tileColumns+` FROM tiles WHERE id = ?`, tileID)
	n, err := scanTile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return n, nil
}

func (s *Store) loadTilesInGrid(ctx context.Context, q gridReader, gridID int64) ([]rpc.Tile, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+tileColumns+` FROM tiles WHERE grid_id = ? ORDER BY id`, gridID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rpc.Tile
	for rows.Next() {
		n, err := scanTile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// GetTile returns a single tile by ID.
func (s *Store) GetTile(ctx context.Context, tileID int64) (*rpc.Tile, error) {
	return s.loadTile(ctx, s.db, tileID)
}

// GetTilePreview returns the JPEG bytes for a URL tile's current preview.
// Returns nil for tiles that don't have a preview yet.
func (s *Store) GetTilePreview(ctx context.Context, tileID int64) ([]byte, error) {
	var jpeg []byte
	err := s.db.QueryRowContext(ctx, `SELECT preview_jpeg FROM tiles WHERE id = ?`, tileID).Scan(&jpeg)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return jpeg, nil
}

// bumpTileVersion increments a tile row's version by 1.
func bumpTileVersion(ctx context.Context, tx *sql.Tx, tileID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE tiles SET version = version + 1 WHERE id = ?`, tileID)
	return err
}

// bumpGridVersion increments a grid row's version by 1.
func bumpGridVersion(ctx context.Context, tx *sql.Tx, gridID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE grids SET version = version + 1 WHERE id = ?`, gridID)
	return err
}

// overlapsExisting reports whether the rectangle (x,y,w,h) overlaps any tile
// in the grid except those whose id is in the excludeIDs set.
func overlapsExisting(ctx context.Context, q gridReader, gridID, x, y, w, h int64, excludeIDs ...int64) (bool, error) {
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
