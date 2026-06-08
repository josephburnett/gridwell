package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// GetGrid returns the grid plus all of its tiles. For source-backed
// (fs / proc) grids, GetGrid first reconciles the tile rows against the
// current host state — this is a read RPC that may mutate, but only for
// grids whose contents come from outside Gridwell.
func (s *Store) GetGrid(ctx context.Context, gridID int64) (*rpc.GetGridResponse, error) {
	g, err := s.loadGrid(ctx, s.db, gridID)
	if err != nil {
		return nil, err
	}
	if g.SourceKind != "" {
		if err := s.reconcileSourceGrid(ctx, g); err != nil {
			return nil, err
		}
		// Reload — reconcile may have bumped the version.
		g, err = s.loadGrid(ctx, s.db, gridID)
		if err != nil {
			return nil, err
		}
	}
	tiles, err := s.loadTilesInGrid(ctx, s.db, gridID)
	if err != nil {
		return nil, err
	}
	return &rpc.GetGridResponse{Grid: *g, Tiles: tiles}, nil
}

// gridReader is the interface needed to read grid/tile rows. Both *sql.DB and
// *sql.Tx satisfy it. It is also used by helpers that only need QueryRowContext
// (e.g. rootGridID, readFloatKey) since *sql.DB and *sql.Tx satisfy the
// superset anyway.
type gridReader interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (s *Store) loadGrid(ctx context.Context, q gridReader, gridID int64) (*rpc.Grid, error) {
	var (
		g          rpc.Grid
		sourceKind sql.NullString
		sourceID   sql.NullString
	)
	err := q.QueryRowContext(ctx,
		`SELECT id, object_id, version, source_kind, source_id FROM grids WHERE id = ?`, gridID,
	).Scan(&g.ID, &g.ObjectID, &g.Version, &sourceKind, &sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if sourceKind.Valid {
		g.SourceKind = sourceKind.String
	}
	if sourceID.Valid {
		g.SourceID = sourceID.String
	}
	return &g, nil
}

// tileColumns is the column list for reading a tile row. Keep in sync with
// scanTile.
const tileColumns = `id, object_id, version, grid_id, kind, x, y, w, h,
	view_x, view_y, view_zoom, child_grid_id,
	text_x, text_y, text_w, text_h, text_mode, blob_id,
	url_string, preview_blob_id, fs_path, pid, source_key, alt_text`

// scanTile scans a single row into an rpc.Tile. It expects the columns to
// match tileColumns in order.
func scanTile(scanner interface {
	Scan(dest ...any) error
}) (*rpc.Tile, error) {
	var (
		n          rpc.Tile
		childGrid  sql.NullInt64
		blob       sql.NullInt64
		urlStr     sql.NullString
		previewBID sql.NullInt64
		textMode   sql.NullString
		fsPath     sql.NullString
		pidNS      sql.NullInt64
		sourceKey  sql.NullString
	)
	if err := scanner.Scan(
		&n.ID, &n.ObjectID, &n.Version, &n.GridID, &n.Kind,
		&n.X, &n.Y, &n.W, &n.H,
		&n.ViewX, &n.ViewY, &n.ViewZoom, &childGrid,
		&n.TextX, &n.TextY, &n.TextW, &n.TextH, &textMode, &blob,
		&urlStr, &previewBID, &fsPath, &pidNS, &sourceKey, &n.AltText,
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
	if previewBID.Valid {
		n.PreviewBlobID = previewBID.Int64
	}
	if textMode.Valid {
		n.TextMode = textMode.String
	}
	if fsPath.Valid {
		n.FSPath = fsPath.String
	}
	if pidNS.Valid {
		n.PID = pidNS.Int64
	}
	if sourceKey.Valid {
		n.SourceKey = sourceKey.String
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

// GetTilePreview returns the JPEG bytes for a tile's current preview —
// URL tiles store the last-frozen page render; shell tiles store the
// last-frozen terminal frame. Returns nil for tiles that don't have a
// preview yet (fresh palette drop, never refreshed).
func (s *Store) GetTilePreview(ctx context.Context, tileID int64) ([]byte, error) {
	var previewBID sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT preview_blob_id FROM tiles WHERE id = ?`, tileID).Scan(&previewBID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !previewBID.Valid {
		return nil, nil
	}
	return s.GetBlob(ctx, previewBID.Int64)
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
