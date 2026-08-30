package store

// trash.go — the local plugin's trashcan (issue #262). A safety net built
// entirely from facts the store already owns: DeleteTile on an ordinary
// grid MOVES the tile into a per-month subgrid of the trash grid (ids and
// versions continue; links keep resolving — it moved, it didn't die);
// DeleteTile on a tile already inside the trash tree destroys it for real
// (the pre-existing cascade). The trash grid itself is a system-keyed
// singleton like the scratch grid, surfaced to the client as a declared
// ROOT menu entry (#258) — no host or client special-casing anywhere.
//
// Decided here and pinned by test: SCRATCH tiles bypass the trash. The
// scratch grid holds system-made ephemerals (visited urls, gone-on-ascent
// shells, the reap's targets); the trash is a net for things the USER
// placed in space and asked to remove.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/josephburnett/gridwell/api/rpc"
)

const systemKeyTrashGridID = "trash_grid_id"

// trashCols is the fixed fill width for trash placement (month wells in
// the trash root, discarded tiles in a month grid). Layout only — the
// user can rearrange afterwards and it stays as left (guiding rule).
const trashCols = 8

// trashAncestryCap bounds the ancestor walk (matches the tree depth any
// real grid could have; a cycle would otherwise loop forever).
const trashAncestryCap = 256

// TrashGridID returns the id of this store's trash grid, creating it on
// first use — the same idempotent system-key pattern as ScratchGridID.
// The plugin's Info declares it as a root menu entry.
func (s *Store) TrashGridID(ctx context.Context) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM system WHERE key = ?`, systemKeyTrashGridID).Scan(&v)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err := s.withTx(ctx, func(tx *sql.Tx) error {
		id, e := s.trashGridIDTx(ctx, tx)
		if e != nil {
			return e
		}
		v = strconv.FormatInt(id, 10)
		return nil
	}); err != nil {
		return "", err
	}
	return v, nil
}

// trashGridIDTx reads or mints the trash grid inside an existing tx (the
// single writer connection serializes transactions, so re-checking inside
// the tx is the whole idempotence story).
func (s *Store) trashGridIDTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	var v string
	err := tx.QueryRowContext(ctx, `SELECT value FROM system WHERE key = ?`, systemKeyTrashGridID).Scan(&v)
	if err == nil {
		return strconv.ParseInt(v, 10, 64)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	now := s.now().Unix()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO grids (created_at, updated_at) VALUES (?, ?)`,
		now, now)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO system (key, value) VALUES (?, ?)`,
		systemKeyTrashGridID, strconv.FormatInt(id, 10)); err != nil {
		return 0, err
	}
	return id, nil
}

// deleteBypassesTrash reports a REAL delete: the tile sits in the scratch
// grid (system ephemerals) or already inside the trash tree (second
// delete). Reads the system keys without minting — an absent trash grid
// means nothing can be inside it yet.
func (s *Store) deleteBypassesTrash(ctx context.Context, tx *sql.Tx, srcGrid int64) (bool, error) {
	for _, key := range []string{systemKeyScratchGridID, systemKeyTrashGridID} {
		var v string
		err := tx.QueryRowContext(ctx, `SELECT value FROM system WHERE key = ?`, key).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return false, err
		}
		if key == systemKeyScratchGridID {
			if srcGrid == id {
				return true, nil
			}
			continue
		}
		in, err := gridInSubtree(ctx, tx, srcGrid, id)
		if err != nil {
			return false, err
		}
		if in {
			return true, nil
		}
	}
	return false, nil
}

// gridInSubtree walks the well-parent chain from gridID up to a root,
// reporting whether rootID is on the way (the same walk
// wellWouldContainItself does, in the other direction).
func gridInSubtree(ctx context.Context, tx *sql.Tx, gridID, rootID int64) (bool, error) {
	g := gridID
	for i := 0; i < trashAncestryCap; i++ {
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
	return false, fmt.Errorf("grid %d: ancestry deeper than %d (cycle?)", gridID, trashAncestryCap)
}

// moveTileToTrash files t under the current month's subgrid of the trash
// grid, minting the month well on first use. The move is PlaceTile's
// cross-grid shape exactly: same row, same id, tile version UNTOUCHED (a
// move is layout, not content — docs/simplify-plan.md S5), both grid
// versions bumped, TileRemoved(src) + TileChanged(dest) — so every
// client reconciles it as the move it is.
func (s *Store) moveTileToTrash(ctx context.Context, tx *sql.Tx, events *[]rpc.Event, t *rpc.Tile) error {
	tileID, err := parseID(t.ID)
	if err != nil {
		return fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	srcGrid, err := parseID(t.GridID)
	if err != nil {
		return fmt.Errorf("tile %s: bad grid_id %q: %w", t.ID, t.GridID, err)
	}
	trashID, err := s.trashGridIDTx(ctx, tx)
	if err != nil {
		return err
	}
	month := s.now().UTC().Format("2006-01")
	monthGrid, minted, err := s.monthGridTx(ctx, tx, trashID, month)
	if err != nil {
		return err
	}
	x, y, err := firstFreeCell(ctx, tx, monthGrid, t.W, t.H)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tiles SET grid_id = ?, x = ?, y = ?, updated_at = ? WHERE id = ?`,
		monthGrid, x, y, s.now().Unix(), tileID); err != nil {
		return err
	}
	if err := s.bumpGridVersion(ctx, tx, srcGrid); err != nil {
		return err
	}
	if err := s.bumpGridVersion(ctx, tx, monthGrid); err != nil {
		return err
	}
	if minted {
		if err := s.bumpGridVersion(ctx, tx, trashID); err != nil {
			return err
		}
	}
	*events = append(*events, rpc.Event{Kind: rpc.EventTileRemoved, TileRemoved: &rpc.TileRemoved{
		GridID: strconv.FormatInt(srcGrid, 10),
		TileID: t.ID,
	}})
	_, err = s.emitTileChanged(ctx, tx, tileID, events)
	return err
}

// monthGridTx finds the trash grid's well for month (alt_text "2026-08"),
// minting well + child grid on first use. minted reports a fresh well so
// the caller bumps the trash grid's version exactly once.
func (s *Store) monthGridTx(ctx context.Context, tx *sql.Tx, trashID int64, month string) (gridID int64, minted bool, err error) {
	var childStr sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT child_grid_id FROM tiles
		 WHERE grid_id = ? AND kind = 'well' AND alt_text = ? AND child_grid_id IS NOT NULL
		 ORDER BY id LIMIT 1`, trashID, month).Scan(&childStr)
	if err == nil {
		id, perr := strconv.ParseInt(childStr.String, 10, 64)
		return id, false, perr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	now := s.now().Unix()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO grids (created_at, updated_at) VALUES (?, ?)`,
		now, now)
	if err != nil {
		return 0, false, err
	}
	child, err := res.LastInsertId()
	if err != nil {
		return 0, false, err
	}
	x, y, err := firstFreeCell(ctx, tx, trashID, 1, 1)
	if err != nil {
		return 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tiles (grid_id, kind, x, y, w, h,
			view_cx, view_cy, view_zoom, child_grid_id, alt_text,
			created_at, updated_at)
		VALUES (?, 'well', ?, ?, 1, 1, 0, 0, 0, ?, ?, ?, ?)`,
		trashID, x, y, child, month, now, now); err != nil {
		return 0, false, err
	}
	return child, true, nil
}

// firstFreeCell scans a trashCols-wide fill for the first slot that fits
// (w, h) without overlap — row-major, unbounded downward.
func firstFreeCell(ctx context.Context, tx *sql.Tx, gridID, w, h int64) (int64, int64, error) {
	for y := int64(0); ; y++ {
		for x := int64(0); x < trashCols; x++ {
			over, err := overlapsExisting(ctx, tx, gridID, x, y, w, h)
			if err != nil {
				return 0, 0, err
			}
			if !over {
				return x, y, nil
			}
		}
	}
}
