package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/josephburnett/gridwell/api/rpc"
)

// leafLinkKinds is the set of tile kinds that have a LINK variant via
// link_target_id. The well kind's link variant is the exit well (a qualified
// child_grid_id) and never uses link_target_id; the CHECK's link branch
// mirrors this set.
var leafLinkKinds = map[string]bool{
	rpc.KindText:  true,
	rpc.KindURL:   true,
	rpc.KindShell: true,
	rpc.KindPane:  true,
}

// CreateLeafLink creates a leaf tile (text/url/shell/pane) that is a LINK to a
// tile owned by another plugin: link_target_id holds the qualified
// "<uuid>/<tile-id>" reference, stored verbatim (the same contract as an exit
// well's qualified child_grid_id). The row carries no content of its own —
// readers resolve bytes/preview/session through the target id — so deleting
// it later only unlinks (tileRefs: a link owns nothing). alt is the link's
// local label.
func (s *Store) CreateLeafLink(ctx context.Context, gridID string, x, y, w, h int64, kind, linkTargetID, alt string) (*rpc.Tile, error) {
	if !leafLinkKinds[kind] {
		return nil, fmt.Errorf("%w: kind %q has no leaf-link variant", ErrInvalidArgument, kind)
	}
	if !strings.Contains(linkTargetID, "/") {
		// The target must be a QUALIFIED tile id: a bare integer would be
		// ambiguous the moment this row is read by a client that doesn't know
		// which plugin allocated it. Same rule as an exit well's child.
		return nil, fmt.Errorf("%w: link_target_id %q is not a qualified <uuid>/<tile-id> reference", ErrInvalidArgument, linkTargetID)
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
