package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// leafLinkKinds is the set of kinds with a link variant through
// link_target_id. A door's link variant is the door to another namespace, a qualified
// child_grid_id. The CHECK's link branch mirrors this set.
var leafLinkKinds = map[string]bool{
	rpc.KindText:  true,
	rpc.KindURL:   true,
	rpc.KindShell: true,
	rpc.KindPane:  true,
}

// CreateLeafLink creates a leaf tile that is a link to another tile.
// link_target_id holds the qualified "<uuid>/<tile-id>" link verbatim, on
// the same contract as the child_grid_id of a door to another namespace: the
// qualification is what
// makes the link unambiguous, not the crossing, so a same-namespace
// target is an ordinary link. The row carries no content, readers resolving
// bytes and tmux through the target, so deleting it only unlinks.
func (s *Store) CreateLeafLink(ctx context.Context, gridID string, x, y, w, h int64, kind, linkTargetID, alt string) (*gridwellv1.Tile, error) {
	if !leafLinkKinds[kind] {
		return nil, fmt.Errorf("%w: kind %q has no leaf-link variant", ErrInvalidArgument, kind)
	}
	if !strings.Contains(linkTargetID, "/") {
		// The target must be a qualified tile id: a bare integer is ambiguous
		// to a client that does not know which namespace allocated it. Every
		// id a client holds is already qualified, home's included, which is
		// why a same-namespace link needs no special case.
		return nil, fmt.Errorf("%w: link_target_id %q is not a qualified <uuid>/<tile-id> link", ErrInvalidArgument, linkTargetID)
	}
	return s.createTile(ctx, gridID, x, y, w, h,
		func(tx *sql.Tx, gid, now int64) (int64, error) {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO tiles (grid_id, kind, x, y, w, h,
					link_target_id, alt_text, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				gid, kind, x, y, w, h, linkTargetID, alt, now, now)
			if err != nil {
				return 0, fmt.Errorf("insert leaf link: %w", err)
			}
			return res.LastInsertId()
		})
}
