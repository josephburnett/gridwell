//go:build js && wasm

package main

import (
	"github.com/josephburnett/gridwell/internal/rpc"
)

// uuidOf and isExitWell forward to the canonical rpc helpers: the
// "<uuid>/<local>" id convention and its exit-well classification live once, in
// internal/rpc (tested there). Kept as local names because the wasm renderer
// reads them at many call sites.
func uuidOf(id string) string     { return rpc.UUIDOf(id) }
func isExitWell(n *rpc.Tile) bool { return rpc.IsExitWell(n) }

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

// pluginKind returns the configured kind ("fs" / "proc" / "localdb" / …) of
// the plugin owning the given qualified grid id, or "" if unknown. Drives the
// identity glyph on a cross-plugin well that has no preview yet.
func (a *App) pluginKind(gridID string) string {
	if pl, ok := a.pluginByUUID(uuidOf(gridID)); ok {
		return pl.Kind
	}
	return ""
}
