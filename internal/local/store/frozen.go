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
// must not auto-go-live until the reconnect gesture clears it. It is framing,
// so no claim and no bump. The column and the wire field are named url_frozen,
// from before a shell could be frozen; neither can be renamed.
func (s *Store) SetFrozen(ctx context.Context, tileIDStr string, frozen bool) (*gridwellv1.Tile, error) {
	tileID, err := parseID(tileIDStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *gridwellv1.Tile
	err = s.withMutation(ctx, "SetFrozen", func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		if err := s.setFrozenTx(ctx, tx, "", tileID, frozen); err != nil {
			return err
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// SetFrozen is the standing freeze on a plugin's tile, the write home's rows
// take.
func (n *Namespace) SetFrozen(tileID int64, frozen bool) error {
	ctx := context.Background()
	return n.s.withMutation(ctx, "SetFrozen", func(tx *sql.Tx, _ *[]*gridwellv1.Event) error {
		return n.s.setFrozenTx(ctx, tx, n.ns, tileID, frozen)
	})
}

// setFrozenTx is the one standing-freeze write. It is refused for a kind with
// nothing to go live to.
func (s *Store) setFrozenTx(ctx context.Context, tx *sql.Tx, ns string, tileID int64, frozen bool) error {
	kind, err := liveKind(ctx, tx, ns, tileID)
	if err != nil {
		return err
	}
	if kind != rpc.KindURL && kind != rpc.KindShell {
		return fmt.Errorf("%w: url_frozen only applies to url and shell tiles", ErrInvalidArgument)
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
