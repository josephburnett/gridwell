package store

// Framing is how a grid looked when the user left it through a doorway: a
// float center in the grid's own coordinates plus a pane-size-independent
// zoom, so a window resize never moves a saved view. It lives on the row that
// owns the doorway, a tile row (view_cx, view_cy, view_zoom) for a grid
// entered through a well, or a grid row (root_cx, root_cy, root_zoom) for a
// root, home's included at ns = ''. Never visited is three NULLs, written only
// by a row's creation; updateFraming writes only an rpc.Framing. This file is
// the single SQL writer, so the shape cannot drift.

import (
	"context"
	"database/sql"

	"github.com/josephburnett/gridwell/api/rpc"
)

// viewArgs binds v as the three framing columns: NULL when never visited.
func viewArgs(v rpc.View) (cx, cy, zoom any) {
	f, ok := v.Framing()
	if !ok {
		return nil, nil, nil
	}
	return f.Cx(), f.Cy(), f.Zoom()
}

// updateFraming writes f onto the row that owns it and reports how many rows
// it touched; 0 means no such live row. Exactly one of tileID and gridID is
// non-zero, and a tombstoned tile refuses the write because a retired key
// stays retired. A grid row takes no timestamp: updated_at on grids follows
// content.
func updateFraming(ctx context.Context, tx *sql.Tx, ns string, tileID, gridID int64, f rpc.Framing, now int64) (int64, error) {
	var (
		res sql.Result
		err error
	)
	if tileID != 0 {
		res, err = tx.ExecContext(ctx,
			`UPDATE tiles SET view_cx = ?, view_cy = ?, view_zoom = ?, updated_at = ?
			 WHERE id = ? AND ns = ? AND tombstoned = 0`,
			f.Cx(), f.Cy(), f.Zoom(), now, tileID, ns)
	} else {
		res, err = tx.ExecContext(ctx,
			`UPDATE grids SET root_cx = ?, root_cy = ?, root_zoom = ? WHERE id = ? AND ns = ?`,
			f.Cx(), f.Cy(), f.Zoom(), gridID, ns)
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
