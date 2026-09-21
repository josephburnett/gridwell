package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// GetGrid returns the grid plus all of its tiles. It is a pure read: home holds
// only Gridwell-owned grids, so there is no host-state reconciliation here.
func (s *Store) GetGrid(ctx context.Context, gridID string) (*gridwellv1.GetGridResponse, error) {
	id, err := parseID(gridID)
	if err != nil {
		return nil, ErrNotFound
	}
	g, err := s.loadGrid(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	tiles, err := s.loadTilesInGrid(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	return &gridwellv1.GetGridResponse{Grid: g, Tiles: tiles}, nil
}

// gridReader is what reading grid and tile rows needs; *sql.DB and *sql.Tx
// both satisfy it.
type gridReader interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// gridColumns is the SELECT list for a grid row. Everything else on
// gridwellv1.Grid is derived by the serving node and never read from a row.
var gridColumns = wireColumns(gridsColumns)

func (s *Store) loadGrid(ctx context.Context, q gridReader, gridID int64) (*gridwellv1.Grid, error) {
	var g gridwellv1.Grid
	err := q.QueryRowContext(ctx,
		`SELECT `+gridColumns+` FROM grids WHERE id = ? AND ns = ''`, gridID,
	).Scan(scanDests(gridsColumns, &g)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// tileColumns is the SELECT list for a tile row and scanTile reads it back.
// Both derive from columns.go, so they cannot fall out of step.
var tileColumns = wireColumns(tilesColumns)

// scanTile scans a single row into a Tile.
func scanTile(scanner interface {
	Scan(dest ...any) error
}) (*gridwellv1.Tile, error) {
	var n gridwellv1.Tile
	if err := scanner.Scan(scanDests(tilesColumns, &n)...); err != nil {
		return nil, err
	}
	return &n, nil
}

func (s *Store) loadTile(ctx context.Context, q gridReader, tileID int64) (*gridwellv1.Tile, error) {
	row := q.QueryRowContext(ctx, `SELECT `+tileColumns+` FROM tiles WHERE id = ? AND ns = ''`, tileID)
	n, err := scanTile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return n, nil
}

func (s *Store) loadTilesInGrid(ctx context.Context, q gridReader, gridID int64) ([]*gridwellv1.Tile, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+tileColumns+` FROM tiles WHERE grid_id = ? AND ns = '' ORDER BY id`, gridID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*gridwellv1.Tile
	for rows.Next() {
		n, err := scanTile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ancestryCap bounds the well-parent walk, which a cycle would loop forever.
const ancestryCap = 256

// gridInSubtree walks the well-parent chain from gridID up to a root, reporting
// whether rootID is on the way. It is the one ancestor walk: the trash's
// bypass check and placement's own-subtree refusal are both this question.
func gridInSubtree(ctx context.Context, tx *sql.Tx, gridID, rootID int64) (bool, error) {
	g := gridID
	for i := 0; i < ancestryCap; i++ {
		if g == rootID {
			return true, nil
		}
		var parent int64
		err := tx.QueryRowContext(ctx,
			`SELECT grid_id FROM tiles WHERE child_grid_id = ?`, g).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		g = parent
	}
	return false, fmt.Errorf("grid %d: ancestry deeper than %d (cycle?)", gridID, ancestryCap)
}

// GetTile returns a single tile by ID.
func (s *Store) GetTile(ctx context.Context, tileID string) (*gridwellv1.Tile, error) {
	id, err := parseID(tileID)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.loadTile(ctx, s.db, id)
}

// GetTilePreview returns a tile's last-frozen JPEG, or nil when it has none.
func (s *Store) GetTilePreview(ctx context.Context, tileID string) ([]byte, error) {
	id, err := parseID(tileID)
	if err != nil {
		return nil, ErrNotFound
	}
	var previewBID sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT preview_blob_id FROM tiles WHERE id = ?`, id).Scan(&previewBID)
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

// ShellTileExists reports whether a shell tile with that row id is still
// present, which is how DeleteTile decides a tmux session is orphaned: the
// session dies only when this exact id is gone. A cloned shell has its own id
// and no session, so deleting it never affects the original.
func (s *Store) ShellTileExists(ctx context.Context, id string) (bool, error) {
	idInt, err := parseID(id)
	if err != nil {
		return false, nil
	}
	var n int64
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM tiles WHERE id = ? AND kind = 'shell'`, idInt).Scan(&n)
	return n > 0, err
}

// bumpTileVersion increments a tile row's version by 1.
func bumpTileVersion(ctx context.Context, tx *sql.Tx, tileID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE tiles SET version = version + 1 WHERE id = ?`, tileID)
	return err
}

// bumpGridVersion increments a grid row's version and stamps updated_at. A
// grid's version moves on a structural change only.
func (s *Store) bumpGridVersion(ctx context.Context, tx *sql.Tx, gridID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE grids SET version = version + 1, updated_at = ? WHERE id = ?`,
		s.now().Unix(), gridID)
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
