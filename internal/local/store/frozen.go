package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// The user's standing freeze, one fact over every kind that can go live and
// every namespace that holds such a row.

// SetFrozen persists the standing freeze on a url or shell tile: descending
// must not auto-go-live until the reconnect gesture clears it. It claims the
// value the writer saw (setFrozenTx) and bumps no version. The column and the
// wire field are named url_frozen, from before a shell could be frozen;
// neither can be renamed.
func (s *Store) SetFrozen(ctx context.Context, tileIDStr string, frozen bool, claim *bool) (*gridwellv1.Tile, error) {
	tileID, err := parseID(tileIDStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *gridwellv1.Tile
	err = s.withTileWrite(ctx, "SetFrozen", tileID, func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		if err := s.setFrozenTx(ctx, tx, "", tileID, frozen, claim); err != nil {
			return err
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// SetFrozen is the standing freeze on a plugin's tile, the write home's rows
// take.
func (n *Namespace) SetFrozen(tileID int64, frozen bool, claim *bool) (changed bool, err error) {
	return n.rowWrite("SetFrozen", "tiles", tileID, tileRow(tileID), func(ctx context.Context, tx *sql.Tx) error {
		return n.s.setFrozenTx(ctx, tx, n.ns, tileID, frozen, claim)
	})
}

// setFrozenTx is the one standing-freeze write. It is refused for a kind with
// nothing to go live to. claim is the value the writer saw, nil for the node's
// own write; a write landing where the row already is loses nothing, so only
// a freeze that moved under a writer asking to move it is refused.
func (s *Store) setFrozenTx(ctx context.Context, tx *sql.Tx, ns string, tileID int64, frozen bool, claim *bool) error {
	kind, err := liveKind(ctx, tx, ns, tileID)
	if err != nil {
		return err
	}
	if !rpc.KindOf(kind).Freezes() {
		return fmt.Errorf("%w: url_frozen only applies to url and shell tiles", ErrInvalidArgument)
	}
	var cur bool
	if err := tx.QueryRowContext(ctx, `SELECT url_frozen FROM tiles WHERE id = ?`, tileID).Scan(&cur); err != nil {
		return err
	}
	if cur == frozen {
		return nil
	}
	if claim != nil && *claim != cur {
		return fmt.Errorf("%w: tile %d's freeze moved since the writer saw it", ErrVersionConflict, tileID)
	}
	_, err = tx.ExecContext(ctx, `UPDATE tiles SET url_frozen = ?, updated_at = ? WHERE id = ?`,
		boolToInt(frozen), s.now().Unix(), tileID)
	return err
}

// liveKind reads a live row's kind within one namespace: ErrNotFound for a row
// that is retired or another namespace's.
func liveKind(ctx context.Context, q gridReader, ns string, tileID int64) (string, error) {
	var kind string
	err := q.QueryRowContext(ctx, `SELECT kind FROM tiles WHERE id = ? AND ns = ? AND tombstoned = 0`, tileID, ns).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return kind, err
}
