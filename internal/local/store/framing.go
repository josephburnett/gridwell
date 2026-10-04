package store

// Framing is how a grid looked when the user left it through a doorway: a
// float center in the grid's own coordinates plus a pane-size-independent
// zoom, so a window resize never moves a saved view. It lives on the row that
// owns the doorway, a tile row (view_cx, view_cy, view_zoom) for a grid
// entered through a well, or a grid row (root_cx, root_cy, root_zoom) for a
// root, home's included at ns = ''. A zero or NULL zoom is the one "never
// visited" convention: cx and cy carry no meaning and the reader falls back to
// the preview calibration. This file is the single SQL writer, so the shape
// cannot drift.

import (
	"context"
	"database/sql"

	"github.com/josephburnett/gridwell/api/rpc"
)

// execer is the write half of a *sql.DB or *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// updateFraming writes f onto the row that owns it and reports how many rows
// it touched; 0 means no such live row. Exactly one of tileID and gridID is
// non-zero, and a tombstoned tile refuses the write because a retired key
// stays retired. A grid row takes no timestamp: updated_at on grids follows
// content.
func updateFraming(ctx context.Context, x execer, ns string, tileID, gridID int64, f rpc.Framing, now int64) (int64, error) {
	var (
		res sql.Result
		err error
	)
	if tileID != 0 {
		res, err = x.ExecContext(ctx,
			`UPDATE tiles SET view_cx = ?, view_cy = ?, view_zoom = ?, updated_at = ?
			 WHERE id = ? AND ns = ? AND tombstoned = 0`,
			f.Cx(), f.Cy(), f.Zoom(), now, tileID, ns)
	} else {
		res, err = x.ExecContext(ctx,
			`UPDATE grids SET root_cx = ?, root_cy = ?, root_zoom = ? WHERE id = ? AND ns = ?`,
			f.Cx(), f.Cy(), f.Zoom(), gridID, ns)
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
