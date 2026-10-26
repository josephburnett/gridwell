package store

// The selection: the set of a grid's tiles the user has in hand, one fact on
// the grid row (grids.selection) for home's grids and a plugin's alike. A
// select clears, adds and removes, claims nothing and bumps no version.

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
	if err != nil {
		return nil, notFound(err)
	}
	return parseSelection(text)
}

// SelectOp is one select: Clear empties the grid's set, then Add puts tiles
// in and Remove takes them out, by rpc.ApplySelect. Recurse, only with Clear
// (rpc.SelectRefusal), also empties every grid nested under the grid's doors
// within its namespace.
type SelectOp struct {
	Add, Remove    []int64
	Clear, Recurse bool
}

// setSelectionTx is the one selection write. It answers each grid whose set
// it changed, the named grid first when it did. An added tile not live on the
// grid is ignored.
func setSelectionTx(ctx context.Context, tx *sql.Tx, ns string, gridID int64, op SelectOp) ([]int64, error) {
	cur, err := readSelection(ctx, tx, ns, gridID)
	if err != nil {
		return nil, err
	}
	on := make([]int64, 0, len(op.Add))
	for _, id := range op.Add {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tiles
			WHERE id = ? AND grid_id = ? AND ns = ? AND tombstoned = 0`, id, gridID, ns).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			on = append(on, id)
		}
	}
	from := idStrings(cur)
	if op.Clear {
		from = nil
	}
	next, err := parseIDs(rpc.ApplySelect(from, idStrings(on), idStrings(op.Remove)))
	if err != nil {
		return nil, err
	}
	var changed []int64
	if selectionText(next) != selectionText(cur) {
		if _, err := tx.ExecContext(ctx, `UPDATE grids SET selection = ? WHERE id = ?`, selectionText(next), gridID); err != nil {
			return nil, err
		}
		changed = append(changed, gridID)
	}
	if !op.Recurse {
		return changed, nil
	}
	nested, err := clearNestedTx(ctx, tx, ns, gridID)
	return append(changed, nested...), err
}

// clearNestedTx empties the set of every grid under gridID's doors, to any
// depth. A door to another namespace, whose child_grid_id is a
// qualified string, is not followed.
func clearNestedTx(ctx context.Context, tx *sql.Tx, ns string, gridID int64) ([]int64, error) {
	var changed []int64
	seen := map[int64]bool{gridID: true}
	queue := []int64{gridID}
	for len(queue) > 0 {
		g := queue[0]
		queue = queue[1:]
		rows, err := tx.QueryContext(ctx, `SELECT child_grid_id FROM tiles
			WHERE grid_id = ? AND ns = ? AND kind = 'door' AND tombstoned = 0
			AND typeof(child_grid_id) = 'integer'`, g, ns)
		if err != nil {
			return nil, err
		}
		kids, err := collect(rows, scanID)
		if err != nil {
			return nil, err
		}
		for _, k := range kids {
			if seen[k] {
				continue
			}
			seen[k] = true
			res, err := tx.ExecContext(ctx, `UPDATE grids SET selection = '' WHERE id = ? AND ns = ? AND selection != ''`, k, ns)
			if err != nil {
				return nil, err
			}
			if n, err := res.RowsAffected(); err != nil {
				return nil, err
			} else if n > 0 {
				changed = append(changed, k)
			}
			queue = append(queue, k)
		}
	}
	return changed, nil
}

// leaveSelection takes a tile that left gridID out of its selection, and is
// how the set names only tiles on its grid: setSelectionTx adds nothing
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

// SetSelection is a select of a home grid, or with All of every home grid,
// telling each grid whose set it changed. Ids are home's decimal row ids.
func (s *Store) SetSelection(ctx context.Context, req *gridwellv1.SetSelectionRequest) error {
	if why := rpc.SelectRefusal(req); why != "" {
		return fmt.Errorf("%w: %s", ErrInvalidArgument, why)
	}
	if req.All {
		return s.withMutation(ctx, "SetSelection", func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
			changed, err := clearAllTx(ctx, tx, "")
			if err != nil {
				return err
			}
			if len(changed) == 0 {
				return errUnchanged
			}
			for _, g := range changed {
				*events = append(*events, rpc.SelectionEvent(strconv.FormatInt(g, 10), nil))
			}
			return nil
		})
	}
	gid, err := parseID(req.GridId)
	if err != nil {
		return fmt.Errorf("%w: invalid grid_id", ErrInvalidArgument)
	}
	op := SelectOp{Clear: req.Clear, Recurse: req.Recurse}
	if op.Add, err = parseIDs(req.Add); err != nil {
		return err
	}
	if op.Remove, err = parseIDs(req.Remove); err != nil {
		return err
	}
	return s.withMutation(ctx, "SetSelection", func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		changed, err := setSelectionTx(ctx, tx, "", gid, op)
		if err != nil {
			return err
		}
		if len(changed) == 0 {
			return errUnchanged
		}
		for _, g := range changed {
			stored, err := readSelection(ctx, tx, "", g)
			if err != nil {
				return err
			}
			*events = append(*events, rpc.SelectionEvent(strconv.FormatInt(g, 10), idStrings(stored)))
		}
		return nil
	})
}

// SetSelection is a select of a plugin's grid row, the write home's grids
// take. It answers each grid whose set changed, for the caller to announce.
func (n *Namespace) SetSelection(gridID int64, op SelectOp) (changed []int64, err error) {
	err = n.s.withMutation(context.Background(), "SetSelection", func(tx *sql.Tx, _ *[]*gridwellv1.Event) error {
		var err error
		if changed, err = setSelectionTx(context.Background(), tx, n.ns, gridID, op); err != nil {
			return err
		}
		if len(changed) == 0 {
			return errUnchanged
		}
		return nil
	}, n.ns+":s/"+strconv.FormatInt(gridID, 10))
	if err != nil {
		return nil, err
	}
	return changed, nil
}

// ClearAllSelections empties every grid's set in the namespace, answering
// each grid whose set changed, for the caller to announce.
func (n *Namespace) ClearAllSelections() (changed []int64, err error) {
	err = n.s.withMutation(context.Background(), "SetSelection", func(tx *sql.Tx, _ *[]*gridwellv1.Event) error {
		var err error
		if changed, err = clearAllTx(context.Background(), tx, n.ns); err != nil {
			return err
		}
		if len(changed) == 0 {
			return errUnchanged
		}
		return nil
	}, n.ns+":s/*")
	if err != nil {
		return nil, err
	}
	return changed, nil
}

// clearAllTx empties every grid's set in namespace ns.
func clearAllTx(ctx context.Context, tx *sql.Tx, ns string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM grids WHERE ns = ? AND selection != '' ORDER BY id`, ns)
	if err != nil {
		return nil, err
	}
	changed, err := collect(rows, scanID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE grids SET selection = '' WHERE ns = ? AND selection != ''`, ns); err != nil {
		return nil, err
	}
	return changed, nil
}

// Selection reads a plugin grid row's selection.
func (n *Namespace) Selection(gridID int64) ([]int64, error) {
	return readSelection(context.Background(), n.s.db, n.ns, gridID)
}
