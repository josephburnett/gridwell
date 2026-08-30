package store

// Framing — "how this grid looked when I left it through this doorway" — is
// one fact with one shape everywhere in this store: a float center in the
// grid's own coordinates plus a pane-size-independent zoom, the intrinsic
// ratio live over overtake, so a window resize never moves a saved view. It
// lives on the row that owns the doorway:
//
//   - a tile row (view_cx, view_cy, view_zoom) for a grid entered through a
//     well, interior or exit or link, since each doorway keeps its own;
//   - a grid row (root_cx, root_cy, root_zoom) for a root grid, which has no
//     doorway. Home's root is that row at ns = '', so there is no second
//     shape for home.
//
// A view_zoom or root_zoom of 0, or NULL, is the one "never visited"
// convention: cx and cy carry no meaning then and the reader falls back to the
// preview calibration.
//
// This file holds the single SQL writer. Everything that persists framing —
// Store.SetFraming, Namespace.SetFraming, a migration — goes through it, so
// there is one place the shape is written and none where it can drift.

import (
	"context"
	"database/sql"

	"github.com/josephburnett/gridwell/api/rpc"
)

// execer is the write half of a *sql.DB or *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// updateFraming writes f onto the one row that owns it and reports how many
// rows it touched; 0 means no such live row. Exactly one of tileID and gridID
// is non-zero: tileID names a doorway tile in namespace ns, where a tombstoned
// row refuses the write because a retired key stays retired, and gridID names
// a root grid in that namespace. now stamps the tile row's updated_at; a grid
// row has no framing timestamp, since framing never bumps a version and
// updated_at on grids follows content.
func updateFraming(ctx context.Context, x execer, ns string, tileID, gridID int64, f rpc.Framing, now int64) (int64, error) {
	var (
		res sql.Result
		err error
	)
	if tileID != 0 {
		res, err = x.ExecContext(ctx,
			`UPDATE tiles SET view_cx = ?, view_cy = ?, view_zoom = ?, updated_at = ?
			 WHERE id = ? AND ns = ? AND tombstoned = 0`,
			f.Cx, f.Cy, f.Zoom, now, tileID, ns)
	} else {
		res, err = x.ExecContext(ctx,
			`UPDATE grids SET root_cx = ?, root_cy = ?, root_zoom = ? WHERE id = ? AND ns = ?`,
			f.Cx, f.Cy, f.Zoom, gridID, ns)
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
