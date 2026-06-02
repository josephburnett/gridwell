package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// CreateFileWell creates a file-well at the given footprint. The well
// points at the singleton fs-grid for FSPath (path identity), so two
// file-wells at the same path share one backing grid. The path is
// normalized — converted to an absolute clean path — so "/foo/", "/foo",
// and "/foo/." all resolve to the same fs-grid.
func (s *Store) CreateFileWell(ctx context.Context, req *rpc.CreateFileWellRequest) (*rpc.Tile, error) {
	fsPath, err := canonicalFSPath(req.FSPath)
	if err != nil {
		return nil, err
	}
	return s.createTile(ctx, req.Path, req.GridID, req.X, req.Y, req.W, req.H,
		func(tx *sql.Tx, gridID, now int64, objID string) (int64, error) {
			childGridID, err := s.getOrCreateSourceGrid(ctx, tx, rpc.GridSourceFS, fsPath, now)
			if err != nil {
				return 0, err
			}
			res, err := tx.ExecContext(ctx, `
				INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h,
					view_x, view_y, view_zoom, child_grid_id, fs_path,
					created_at, updated_at)
				VALUES (?, ?, 'file-well', ?, ?, ?, ?, 0, 0, 0, ?, ?, ?, ?)`,
				objID, gridID, req.X, req.Y, req.W, req.H, childGridID, fsPath, now, now)
			if err != nil {
				return 0, fmt.Errorf("insert file-well: %w", err)
			}
			return res.LastInsertId()
		})
}

// CreateProcessWell creates a process-well at the given PID. The well
// points at the singleton proc-grid for PID, mirroring the fs-grid
// shared-by-identity contract.
func (s *Store) CreateProcessWell(ctx context.Context, req *rpc.CreateProcessWellRequest) (*rpc.Tile, error) {
	if req.PID <= 0 {
		return nil, fmt.Errorf("%w: pid must be positive", ErrInvalidArgument)
	}
	pidStr := strconv.FormatInt(req.PID, 10)
	return s.createTile(ctx, req.Path, req.GridID, req.X, req.Y, req.W, req.H,
		func(tx *sql.Tx, gridID, now int64, objID string) (int64, error) {
			childGridID, err := s.getOrCreateSourceGrid(ctx, tx, rpc.GridSourceProc, pidStr, now)
			if err != nil {
				return 0, err
			}
			res, err := tx.ExecContext(ctx, `
				INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h,
					view_x, view_y, view_zoom, child_grid_id, pid,
					created_at, updated_at)
				VALUES (?, ?, 'process-well', ?, ?, ?, ?, 0, 0, 0, ?, ?, ?, ?)`,
				objID, gridID, req.X, req.Y, req.W, req.H, childGridID, req.PID, now, now)
			if err != nil {
				return 0, fmt.Errorf("insert process-well: %w", err)
			}
			return res.LastInsertId()
		})
}

// getOrCreateSourceGrid returns the grid_id of the singleton grid for
// (sourceKind, sourceID), creating one if necessary. The UNIQUE INDEX
// on (source_kind, source_id) WHERE source_kind IS NOT NULL guards
// against duplicate inserts under concurrent callers — the second one
// races and falls through to the SELECT.
func (s *Store) getOrCreateSourceGrid(ctx context.Context, tx *sql.Tx, sourceKind, sourceID string, now int64) (int64, error) {
	var existing int64
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM grids WHERE source_kind = ? AND source_id = ?`,
		sourceKind, sourceID,
	).Scan(&existing)
	if err == nil {
		// Bump refcount so this file-well/process-well counts as a holder.
		if _, err := tx.ExecContext(ctx,
			`UPDATE grids SET refcount = refcount + 1 WHERE id = ?`, existing); err != nil {
			return 0, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	objID := s.newID()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO grids (object_id, refcount, source_kind, source_id, created_at)
		 VALUES (?, 1, ?, ?, ?)`,
		objID, sourceKind, sourceID, now)
	if err != nil {
		return 0, fmt.Errorf("insert source grid: %w", err)
	}
	return res.LastInsertId()
}

// canonicalFSPath converts a user-supplied filesystem path to its
// canonical absolute form: trimmed, made absolute against /, and Clean'd
// so "/foo/", "/foo/.", and "/foo" all collapse to "/foo". Returns
// ErrInvalidArgument for empty paths.
func canonicalFSPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("%w: fs_path empty", ErrInvalidArgument)
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%w: fs_path must be absolute", ErrInvalidArgument)
	}
	return filepath.Clean(p), nil
}
