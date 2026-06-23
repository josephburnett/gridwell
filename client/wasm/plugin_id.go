//go:build js && wasm

package main

import (
	"strings"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// uuidOf returns the plugin-uuid segment of a qualified id ("<uuid>/<local>"),
// or "" when the id has no "/" (a bare/unqualified id).
func uuidOf(id string) string {
	if i := strings.IndexByte(id, '/'); i >= 0 {
		return id[:i]
	}
	return ""
}

// isExitWell reports whether a well tile's child grid lives in a different
// plugin than the well itself — i.e. descending leaves the current plugin's
// id space (a file or process well). Derived purely from the qualified ids:
// the well's own grid uuid versus its child grid uuid. A synthetic tile with
// empty grid/child ids is not an exit well.
func isExitWell(n *rpc.Tile) bool {
	return n.Kind == rpc.KindWell && n.ChildGridID != "" &&
		uuidOf(n.ChildGridID) != uuidOf(n.GridID)
}

// isPluginTile reports whether a tile is owned by a non-local plugin (its id
// is qualified with a uuid other than the local store's). Such tiles are
// read-only and fetch their body via GetTileContent rather than a blob id.
func (a *App) isPluginTile(n *rpc.Tile) bool {
	u := uuidOf(n.ID)
	return u != "" && u != a.localdbUUID
}
