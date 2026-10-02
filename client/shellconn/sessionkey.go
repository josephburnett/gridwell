package shellconn

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// SessionKey is the session a pane opening row attaches, the key every
// client-side shell fact uses: rpc.ShellSession of the content row it resolves
// to, so a link keys on its target's session as a clone does. target is that
// row for a link and unread for anything else; ok is false while a link's
// target is not known, because the link's own spelling names a tile, not a
// session.
func SessionKey(row, target *gridwellv1.Tile) (key string, ok bool) {
	if row.LinkTargetId == "" {
		return rpc.ShellSession(row), true
	}
	if target == nil {
		return "", false
	}
	return rpc.ShellSession(target), true
}
