package store

import (
	"context"
	"database/sql"
	"fmt"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// The user's standing freeze, one fact over every kind that can go live.

// SetFrozen persists the standing freeze on a url or shell tile: descending
// must not auto-go-live until the reconnect gesture clears it. It is framing,
// so no claim and no bump, and it is refused for a kind with nothing to go
// live to. The column and the wire field are named url_frozen, from before a
// shell could be frozen; neither can be renamed.
func (s *Store) SetFrozen(ctx context.Context, tileIDStr string, frozen bool) (*gridwellv1.Tile, error) {
	tileID, err := parseID(tileIDStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *gridwellv1.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		n, err := s.loadForWrite(ctx, tx, tileID, "", nil)
		if err != nil {
			return err
		}
		if n.Kind != rpc.KindURL && n.Kind != rpc.KindShell {
			return fmt.Errorf("%w: url_frozen only applies to url and shell tiles", ErrInvalidArgument)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET url_frozen = ?, updated_at = ? WHERE id = ?`,
			boolToInt(frozen), s.now().Unix(), tileID); err != nil {
			return err
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}
