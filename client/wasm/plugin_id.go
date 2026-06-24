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

// gridWritable reports whether the plugin owning gridID accepts new/edited
// tiles (only localdb plugins do). Looked up by the grid's uuid against the
// plugin list. An unknown uuid (not yet loaded) is treated as not writable.
func (a *App) gridWritable(gridID string) bool {
	u := uuidOf(gridID)
	if u == "" {
		return false
	}
	for i := range a.plugins {
		if a.plugins[i].UUID == u {
			return a.plugins[i].Writable
		}
	}
	return false
}

// pluginByUUID returns the configured plugin with the given uuid, or false.
func (a *App) pluginByUUID(u string) (rpc.PluginInfo, bool) {
	for i := range a.plugins {
		if a.plugins[i].UUID == u {
			return a.plugins[i], true
		}
	}
	return rpc.PluginInfo{}, false
}
