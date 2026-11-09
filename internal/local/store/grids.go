package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
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
		`SELECT `+gridColumns+` FROM grids WHERE id = ? AND key = ''`, gridID,
	).Scan(scanDests(gridsColumns, &g)...)
	if err != nil {
		return nil, notFound(err)
	}
	return &g, nil
}

// tileColumns is the SELECT list for a tile row and scanTile reads it back.
// Both derive from columns.go, so they cannot fall out of step.
var tileColumns = wireColumns(tilesColumns)

// scanTile scans a single row into a Tile. extra takes the destinations of any
// columns a caller selected beyond the descriptor's, in their SELECT order.
func scanTile(scanner interface {
	Scan(dest ...any) error
}, extra ...any) (*gridwellv1.Tile, error) {
	var n gridwellv1.Tile
	if err := scanner.Scan(append(scanDests(tilesColumns, &n), extra...)...); err != nil {
		return nil, err
	}
	return &n, nil
}

// homeTileSelect selects home's rows with the key of the grid each sits on,
// which scanHomeTile spells the row's grid by.
var homeTileSelect = `SELECT ` + tileColumns + `, (SELECT g.key FROM grids g WHERE g.id = tiles.grid_id) FROM tiles`

// scanHomeTile is scanTile for a home row: one on a directory's grid names
// that grid as the directory's id, ~<path>, the id the files answer it by.
func scanHomeTile(scanner interface {
	Scan(dest ...any) error
}) (*gridwellv1.Tile, error) {
	var key sql.NullString
	t, err := scanTile(scanner, &key)
	if err == nil && key.String != "" {
		t.GridId = rpc.KeyTileID(key.String)
	}
	return t, err
}

func (s *Store) loadTile(ctx context.Context, q gridReader, tileID int64) (*gridwellv1.Tile, error) {
	n, err := scanHomeTile(q.QueryRowContext(ctx, homeTileSelect+` WHERE id = ? AND key = ''`, tileID))
	return n, notFound(err)
}

func (s *Store) loadTilesInGrid(ctx context.Context, q gridReader, gridID int64) ([]*gridwellv1.Tile, error) {
	rows, err := q.QueryContext(ctx, homeTileSelect+` WHERE grid_id = ? AND key = '' ORDER BY id`, gridID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(rows *sql.Rows) (*gridwellv1.Tile, error) { return scanHomeTile(rows) })
}

// gridRow reads a wire grid id as its row: a home grid's digits, or a
// directory's ~<path>, the grids row keyed by its path, NotFound when the
// directory was never touched. It is scanHomeTile's inverse.
func gridRow(ctx context.Context, q gridReader, wire string) (int64, error) {
	key, ok := rpc.TileKey(wire)
	if !ok {
		return parseID(wire)
	}
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM grids WHERE key = ? AND key != ''`, key).Scan(&id)
	return id, notFound(err)
}

// destGrid is the grid row a Gridwell write onto the wire grid id lands on:
// a home grid that exists, or a directory's, its row minted on first sight
// in the write's own transaction (mintKeyGrid).
func (s *Store) destGrid(ctx context.Context, tx *sql.Tx, wire string) (int64, error) {
	if key, ok := rpc.TileKey(wire); ok {
		if key == "" {
			return 0, fmt.Errorf("%w: the files' own id is no grid", ErrInvalidArgument)
		}
		return mintKeyGrid(ctx, tx, key, s.now().Unix())
	}
	id, err := parseID(wire)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid grid_id %q", ErrInvalidArgument, wire)
	}
	if _, err := s.loadGrid(ctx, tx, id); err != nil {
		return 0, fmt.Errorf("%w: grid %d: %v", ErrInvalidArgument, id, err)
	}
	return id, nil
}

// mintKeyGrid is the one mint of a directory's grid row, the files' touch and
// a Gridwell tile placed there alike; two first sights at once mint one row.
func mintKeyGrid(ctx context.Context, tx *sql.Tx, key string, now int64) (id int64, err error) {
	if _, err := tx.ExecContext(ctx, `INSERT INTO grids (version, created_at, updated_at, key)
		VALUES (0, ?, ?, ?) ON CONFLICT DO NOTHING`, now, now, key); err != nil {
		return 0, err
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM grids WHERE key = ?`, key).Scan(&id)
	return id, err
}

// gridWire is grid row id as the wire names it (scanHomeTile).
func gridWire(ctx context.Context, q gridReader, id int64) (string, error) {
	var key string
	if err := q.QueryRowContext(ctx, `SELECT key FROM grids WHERE id = ?`, id).Scan(&key); err != nil {
		return "", notFound(err)
	}
	if key != "" {
		return rpc.KeyTileID(key), nil
	}
	return strconv.FormatInt(id, 10), nil
}

// insertGrid mints an empty grid row. The migration chain keeps its own copy
// of this INSERT: a migration step must materialize the shape of the version
// it is building, not the current one.
func insertGrid(ctx context.Context, tx *sql.Tx, now int64) (int64, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO grids (created_at, updated_at) VALUES (?, ?)`, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ancestryCap bounds the door-parent walk, which a cycle would loop forever.
const ancestryCap = 256

// gridInSubtree walks the door-parent chain from gridID up to a root, reporting
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
	return s.tilePreview(ctx, home, id)
}

// Preview is a file tile's last-frozen JPEG, nil when it has none.
func (n *FileRows) Preview(tileID int64) ([]byte, error) {
	return n.s.tilePreview(context.Background(), files, tileID)
}

// tilePreview is the one frozen-preview read.
func (s *Store) tilePreview(ctx context.Context, on side, id int64) (jpeg []byte, err error) {
	err = s.readSnapshot(ctx, func(tx *sql.Tx) error {
		var previewBID sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT preview_blob_id FROM tiles WHERE id = ? AND `+on.of("key"), id).Scan(&previewBID)
		if err != nil || !previewBID.Valid {
			return notFound(err)
		}
		jpeg, _, err = readBlob(ctx, tx, previewBID.Int64)
		return err
	})
	return jpeg, err
}

// TmuxNamers is how many shell rows name one tmux, and how many of
// those carry a frozen preview.
type TmuxNamers struct {
	Rows, Previewed int64
}

// ShellTmuxNamers counts the shell rows naming the tmux key, a trashed
// row included: a tmux with no rows is an orphan, and one whose rows have
// no preview was never started.
func (s *Store) ShellTmuxNamers(ctx context.Context, key string) (TmuxNamers, error) {
	var n TmuxNamers
	keyInt, err := parseID(key)
	if err != nil {
		return n, nil
	}
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(1), COUNT(preview_blob_id) FROM tiles
		WHERE kind = 'shell' AND link_target_id IS NULL
		  AND COALESCE(shell_session, id) = ?`, keyInt).Scan(&n.Rows, &n.Previewed)
	return n, err
}

// bumpTileVersion increments a tile row's version by 1 and stamps the time,
// content_at's one writer.
func (s *Store) bumpTileVersion(ctx context.Context, tx *sql.Tx, tileID int64) error {
	now := s.now().Unix()
	_, err := tx.ExecContext(ctx,
		`UPDATE tiles SET version = version + 1, content_at = ?, updated_at = ? WHERE id = ?`, now, now, tileID)
	return err
}

// contentAtSQL is a tile row's content time; see the content_at column.
const contentAtSQL = "COALESCE(t.content_at, t.created_at)"

// bumpGridVersion increments a grid row's version and stamps updated_at. A
// grid's version moves on a structural change only, which a directory's grid
// tells as GridChanged, since the files compose it from the disk and home's
// rows alike.
func (s *Store) bumpGridVersion(ctx context.Context, tx *sql.Tx, events *[]*gridwellv1.Event, gridID int64) error {
	var key string
	err := tx.QueryRowContext(ctx,
		`UPDATE grids SET version = version + 1, updated_at = ? WHERE id = ? RETURNING key`,
		s.now().Unix(), gridID).Scan(&key)
	if err != nil || key == "" {
		return notFound(err)
	}
	*events = append(*events, &gridwellv1.Event{Payload: &gridwellv1.Event_GridChanged{GridChanged: &gridwellv1.GridChanged{GridId: rpc.KeyTileID(key)}}})
	return nil
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
