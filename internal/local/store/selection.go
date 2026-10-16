package store

// The selection: the set of a grid's tiles the user has in hand, one fact on
// the grid row (grids.selection) for home's grids and a plugin's alike. A
// write claims the set it saw and bumps no version.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// selectionText is a set's one spelling in the column.
func selectionText(ids []int64) string {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

func parseSelection(text string) ([]int64, error) {
	if text == "" {
		return nil, nil
	}
	parts := strings.Split(text, ",")
	out := make([]int64, len(parts))
	for i, p := range parts {
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("grids.selection %q: %w", text, err)
		}
		out[i] = id
	}
	return out, nil
}

func idStrings(ids []int64) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = strconv.FormatInt(id, 10)
	}
	return out
}

// parseIDs is never nil, so an empty claim stays a claim.
func parseIDs(ids []string) ([]int64, error) {
	out := make([]int64, len(ids))
	for i, s := range ids {
		id, err := parseID(s)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid tile id %q", ErrInvalidArgument, s)
		}
		out[i] = id
	}
	return out, nil
}

// selectionScan reads the column onto the wire as decimal ids.
type selectionScan struct{ p *[]string }

func (s selectionScan) Scan(v any) error {
	var text sql.NullString
	if err := text.Scan(v); err != nil {
		return err
	}
	ids, err := parseSelection(text.String)
	if err != nil {
		return err
	}
	*s.p = idStrings(ids)
	return nil
}

func readSelection(ctx context.Context, q gridReader, ns string, gridID int64) ([]int64, error) {
	var text string
	err := q.QueryRowContext(ctx, `SELECT selection FROM grids WHERE id = ? AND ns = ?`, gridID, ns).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return parseSelection(text)
}

// setSelectionTx is the one selection write. claim is the set the writer saw,
// nil for the node's own write; a write landing where the row already is
// loses nothing, so only a set that moved under a writer asking to move it is
// refused.
func setSelectionTx(ctx context.Context, tx *sql.Tx, ns string, gridID int64, claim, set []int64) error {
	cur, err := readSelection(ctx, tx, ns, gridID)
	if err != nil {
		return err
	}
	have, want := selectionText(cur), selectionText(set)
	if have == want {
		return nil
	}
	if claim != nil && selectionText(claim) != have {
		return fmt.Errorf("%w: grid %d's selection moved since the writer saw it", ErrVersionConflict, gridID)
	}
	for _, id := range set {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tiles
			WHERE id = ? AND grid_id = ? AND ns = ? AND tombstoned = 0`, id, gridID, ns).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: tile %d is not on grid %d", ErrInvalidArgument, id, gridID)
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE grids SET selection = ? WHERE id = ?`, want, gridID)
	return err
}

// leaveSelection takes a tile that left gridID out of its selection, and is
// how the set names only tiles on its grid: setSelectionTx admits nothing
// else, and every way a tile leaves a grid calls this in the same transaction
// (tileLeft for home's, Retire for a plugin's). changed is false when the tile
// was not selected.
func leaveSelection(ctx context.Context, tx *sql.Tx, gridID, tileID int64) (set []int64, changed bool, err error) {
	var text string
	err = tx.QueryRowContext(ctx, `SELECT selection FROM grids WHERE id = ?`, gridID).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	cur, err := parseSelection(text)
	if err != nil {
		return nil, false, err
	}
	i := slices.Index(cur, tileID)
	if i < 0 {
		return cur, false, nil
	}
	set = slices.Delete(cur, i, i+1)
	_, err = tx.ExecContext(ctx, `UPDATE grids SET selection = ? WHERE id = ?`, selectionText(set), gridID)
	return set, err == nil, err
}

// tileLeft announces that tileID left home's grid gridID, and takes it out of
// the grid's selection, telling the new set when that changed it.
func (s *Store) tileLeft(ctx context.Context, tx *sql.Tx, events *[]*gridwellv1.Event, gridID, tileID int64) error {
	grid := strconv.FormatInt(gridID, 10)
	*events = append(*events, &gridwellv1.Event{Payload: &gridwellv1.Event_TileRemoved{TileRemoved: &gridwellv1.TileRemoved{
		GridId: grid,
		TileId: strconv.FormatInt(tileID, 10),
	}}})
	set, changed, err := leaveSelection(ctx, tx, gridID, tileID)
	if err != nil || !changed {
		return err
	}
	*events = append(*events, rpc.SelectionEvent(grid, idStrings(set)))
	return nil
}

// SetSelection replaces home grid gridID's selection with set, claiming the
// set the writer saw; a nil claim is the node's own write. Ids are home's
// decimal row ids.
func (s *Store) SetSelection(ctx context.Context, gridID string, claim *gridwellv1.SelectionClaim, set []string) error {
	gid, err := parseID(gridID)
	if err != nil {
		return fmt.Errorf("%w: invalid grid_id", ErrInvalidArgument)
	}
	want, err := parseIDs(set)
	if err != nil {
		return err
	}
	var saw []int64
	if claim != nil {
		if saw, err = parseIDs(claim.GetTileIds()); err != nil {
			return err
		}
	}
	_, err = s.withRowWrite(ctx, "SetSelection", "grids", gid, func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		if err := setSelectionTx(ctx, tx, "", gid, saw, want); err != nil {
			return err
		}
		stored, err := readSelection(ctx, tx, "", gid)
		if err != nil {
			return err
		}
		*events = append(*events, rpc.SelectionEvent(gridID, idStrings(stored)))
		return nil
	})
	return err
}

// SetSelection is the selection on a plugin's grid row, the write home's
// grids take. A nil claim is the node's own write, so a made claim is non-nil
// even when empty. The caller announces the write when changed.
func (n *Namespace) SetSelection(gridID int64, claim, set []int64) (changed bool, err error) {
	return n.rowWrite("SetSelection", "grids", gridID, "s/"+strconv.FormatInt(gridID, 10),
		func(ctx context.Context, tx *sql.Tx) error {
			return setSelectionTx(ctx, tx, n.ns, gridID, claim, set)
		})
}

// Selection reads a plugin grid row's selection.
func (n *Namespace) Selection(gridID int64) ([]int64, error) {
	return readSelection(context.Background(), n.s.db, n.ns, gridID)
}
