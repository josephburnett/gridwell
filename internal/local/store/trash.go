package store

// Home's trashcan, built entirely from facts the store already owns. DeleteTile
// on an ordinary grid moves the tile into a per-month subgrid of the trash
// grid, so ids and versions continue and links keep resolving; on a tile
// already inside the trash tree it destroys for real, through the ordinary
// cascade. The trash grid is a system-keyed singleton like the scratch grid,
// surfaced as a declared root menu entry, so nothing special-cases it. Scratch
// tiles bypass the trash: they are system-made ephemerals, while the trash is
// a net for what the user placed in space and asked to remove.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

const systemKeyTrashGridID = "trash_grid_id"

// trashAncestryCap bounds the ancestor walk, which a cycle would loop forever.
const trashAncestryCap = 256

// TrashGridID returns the trash grid, creating it on first use by the same
// system-key pattern as ScratchGridID. Info declares it as a root menu entry.
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

// trashGridIDTx reads or mints the trash grid inside an existing transaction.
// The single writer connection serializes them, so the re-check inside the
// transaction is the whole idempotence story.
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

// deleteBypassesTrash reports a real delete: the tile is in the scratch grid,
// or already inside the trash tree. It reads the system keys without minting,
// because an absent trash grid means nothing can be inside it yet.
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

// gridInSubtree walks the well-parent chain from gridID up to a root, reporting
// whether rootID is on the way.
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

// moveTileToTrash files t under the current month's subgrid, minting the month
// well on first use. It is PlaceTile's cross-grid shape exactly: same row and
// id, tile version untouched, both grid versions bumped, and TileRemoved plus
// TileChanged, so every client reconciles it as the move it is.
func (s *Store) moveTileToTrash(ctx context.Context, tx *sql.Tx, events *[]*gridwellv1.Event, t *gridwellv1.Tile) error {
	tileID, err := parseID(t.Id)
	if err != nil {
		return fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	srcGrid, err := parseID(t.GridId)
	if err != nil {
		return fmt.Errorf("tile %s: bad grid_id %q: %w", t.Id, t.GridId, err)
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
	x, y, err := s.firstFreeCell(ctx, tx, monthGrid, t.W, t.H)
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
	*events = append(*events, &gridwellv1.Event{Payload: &gridwellv1.Event_TileRemoved{TileRemoved: &gridwellv1.TileRemoved{
		GridId: strconv.FormatInt(srcGrid, 10),
		TileId: t.Id,
	}}})
	_, err = s.emitTileChanged(ctx, tx, tileID, events)
	return err
}

// monthGridTx finds the trash grid's well for month, whose alt_text is the
// month, minting it on first use. minted reports a fresh well so the caller
// bumps the trash grid's version exactly once.
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
	x, y, err := s.firstFreeCell(ctx, tx, trashID, 1, 1)
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

// firstFreeCell is the one auto-place rule (autoplace.go) over the grid's
// rows: the first slot from the origin that fits (w, h).
func (s *Store) firstFreeCell(ctx context.Context, tx *sql.Tx, gridID, w, h int64) (int64, int64, error) {
	tiles, err := s.loadTilesInGrid(ctx, tx, gridID)
	if err != nil {
		return 0, 0, err
	}
	occupied := map[[2]int64]bool{}
	for _, t := range tiles {
		occupyRect(occupied, t.X, t.Y, t.W, t.H)
	}
	var cur cursor
	x, y := nextFreeRect(occupied, &cur, w, h)
	return x, y, nil
}
