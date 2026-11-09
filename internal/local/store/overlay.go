package store

// The Gridwell layer over a directory as a whole: every home row on a grid
// keyed at or under a path. A directory moved within Gridwell carries it
// (Rekey), and a gone directory's tiles where it would land refuse it
// (HeldAt).

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// below is the prefix every path under dir starts with.
func below(dir string) string {
	if strings.HasSuffix(dir, "/") {
		return dir
	}
	return dir + "/"
}

// atOrUnder is the condition that col, a key column, is path or a path under
// it, and its arguments.
func atOrUnder(col, path string) (string, []any) {
	p := below(path)
	return "(" + col + " = ? OR substr(" + col + ", 1, length(?)) = ?)", []any{path, p, p}
}

// heldSQL is the condition that grids row g holds a Gridwell tile.
const heldSQL = `EXISTS (SELECT 1 FROM tiles t WHERE t.grid_id = g.id AND t.key = '')`

// HeldAt reports whether a grid keyed at or under path holds a Gridwell
// tile: the overlay a directory at path has, or had before it went.
func (n *FileRows) HeldAt(path string) (bool, error) {
	return heldAt(context.Background(), n.s.db, path)
}

func heldAt(ctx context.Context, q gridReader, path string) (bool, error) {
	cond, args := atOrUnder("g.key", path)
	var one int
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM grids g WHERE g.key != '' AND `+cond+` AND `+heldSQL+`)`,
		args...).Scan(&one)
	return one == 1, err
}

// Rekey carries the directory moved from from to to: every grids row keyed
// at or under from, its framing and selection with it, and the files' live
// rows under from, follow to the same place under to, so the Gridwell tiles
// on those grids keep their ids and cells. A grid row a vanished directory
// left at or under to holds no Gridwell tile, or the move is refused, and is
// vacated, its key spelled as no path ('#' and its id), so it reads dead.
// Each moved grid tells GridChanged under both spellings and each Gridwell
// tile on one TileChanged under its new grid.
func (n *FileRows) Rekey(from, to string) error {
	ctx := context.Background()
	return n.s.withMutation(ctx, "Rekey", func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		held, err := heldAt(ctx, tx, to)
		if err != nil {
			return err
		}
		if held {
			return fmt.Errorf("%w: %s holds a gone directory's Gridwell tiles; move them out first", ErrInvalidArgument, to)
		}
		cond, args := atOrUnder("key", to)
		if _, err := tx.ExecContext(ctx, `UPDATE grids SET key = '#' || id WHERE key != '' AND `+cond, args...); err != nil {
			return err
		}
		cond, args = atOrUnder("key", from)
		rows, err := tx.QueryContext(ctx, `SELECT id, key FROM grids WHERE key != '' AND `+cond, args...)
		if err != nil {
			return err
		}
		type moved struct {
			id       int64
			old, new string
		}
		grids, err := collect(rows, func(rows *sql.Rows) (moved, error) {
			var m moved
			err := rows.Scan(&m.id, &m.old)
			m.new = to + m.old[len(from):]
			return m, err
		})
		if err != nil {
			return err
		}
		now := n.s.now().Unix()
		for _, g := range grids {
			if _, err := tx.ExecContext(ctx, `UPDATE grids SET key = ?, updated_at = ? WHERE id = ?`, g.new, now, g.id); err != nil {
				return err
			}
		}
		p := below(from)
		if _, err := tx.ExecContext(ctx, `UPDATE tiles SET key = ? || substr(key, length(?) + 1), updated_at = ?
			WHERE key != '' AND tombstoned = 0 AND substr(key, 1, length(?)) = ?`, below(to), p, now, p, p); err != nil {
			return err
		}
		for _, g := range grids {
			for _, key := range []string{g.old, g.new} {
				*events = append(*events, &gridwellv1.Event{Payload: &gridwellv1.Event_GridChanged{
					GridChanged: &gridwellv1.GridChanged{GridId: rpc.KeyTileID(key)}}})
			}
			ids, err := homeRowsOn(ctx, tx, g.id)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if _, err := n.s.emitTileChanged(ctx, tx, id, events); err != nil {
					return err
				}
			}
		}
		return nil
	}, "~:rekey/"+from)
}

// homeRowsOn is the Gridwell tiles on grid gridID, by id.
func homeRowsOn(ctx context.Context, q gridReader, gridID int64) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM tiles WHERE grid_id = ? AND key = '' ORDER BY id`, gridID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(rows *sql.Rows) (int64, error) {
		var id int64
		return id, rows.Scan(&id)
	})
}
