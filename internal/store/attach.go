package store

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// rebindExitWells re-resolves every durable file/process well's child_grid_id.
// Source grids are shared by identity (fs_path / pid), so a stale or missing
// child_grid_id is re-created by getOrCreateSourceGrid and the tile is updated
// in place. Runs once at Open so the canvas opens correctly even when the
// database was opened on a machine that never had its source grids populated.
func (s *Store) rebindExitWells(ctx context.Context) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id, kind, COALESCE(fs_path,''), COALESCE(pid,0), COALESCE(child_grid_id,0)
			   FROM tiles WHERE kind IN ('file-well','process-well')`)
		if err != nil {
			return err
		}
		type exitWell struct {
			id         int64
			kind       string
			fsPath     string
			pid, child int64
		}
		var wells []exitWell
		for rows.Next() {
			var w exitWell
			if err := rows.Scan(&w.id, &w.kind, &w.fsPath, &w.pid, &w.child); err != nil {
				rows.Close()
				return err
			}
			wells = append(wells, w)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		now := s.now().Unix()
		for _, w := range wells {
			var sourceKind, sourceID string
			switch w.kind {
			case rpc.KindFileWell:
				sourceKind, sourceID = rpc.GridSourceFS, w.fsPath
			case rpc.KindProcessWell:
				sourceKind, sourceID = rpc.GridSourceProc, strconv.FormatInt(w.pid, 10)
			}
			if sourceID == "" {
				continue
			}
			gid, err := s.getOrCreateSourceGrid(ctx, tx, sourceKind, sourceID, now)
			if err != nil {
				return err
			}
			if gid != w.child {
				if _, err := tx.ExecContext(ctx,
					`UPDATE tiles SET child_grid_id = ? WHERE id = ?`, gid, w.id); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
