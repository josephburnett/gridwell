package shellconn

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// TmuxKey is the tmux a pane opening row attaches, the key every
// client-side shell fact uses: rpc.ShellTmux of the content row it resolves
// to, so a link keys on its target's tmux as a copy does. target is that
// row for a link and unread for anything else; ok is false while a link's
// target is not known, because the link's own spelling names a tile, not a
// tmux.
func TmuxKey(row, target *gridwellv1.Tile) (key string, ok bool) {
	if row.LinkTargetId == "" {
		return rpc.ShellTmux(row), true
	}
	if target == nil {
		return "", false
	}
	return rpc.ShellTmux(target), true
}
