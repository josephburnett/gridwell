//go:build js && wasm

package main

import (
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// uuidOf and isExitWell forward to the canonical rpc helpers: the
// "<uuid>/<local>" id convention and its exit-well classification live once, in
// internal/rpc (tested there). Kept as local names because the wasm renderer
// reads them at many call sites.
func uuidOf(id string) string     { return rpc.UUIDOf(id) }
func isExitWell(n *rpc.Tile) bool { return rpc.IsExitWell(n) }

// gridWritable reports whether the grid accepts new/edited tiles. The fact
// is PER GRID and travels ON the grid (Grid.Writable, stamped by the serving
// node from the owning plugin's Info) — a uuid lookup against the local
// plugin list cannot answer for a remote plugin reached through an ssh mount,
// whose local first segment is the transit plugin. An uncached grid is
// treated as not writable (nothing renders from it yet anyway).
func (a *App) gridWritable(gridID string) bool {
	if gridID == "" {
		return false
	}
	g, ok := a.c.Grid(gridID)
	if !ok {
		return false
	}
	return g.Meta.Writable
}

// gridKnownReadOnly reports that the grid is cached AND declares itself
// read-only. Distinct from !gridWritable: an UNCACHED grid is unknown, and a
// drop gesture must not be rejected on ignorance — the server is the
// authority for the race where the fetch hasn't landed yet.
func (a *App) gridKnownReadOnly(gridID string) bool {
	g, ok := a.c.Grid(gridID)
	return ok && !g.Meta.Writable
}

// isNodeGridPane reports whether pane p sits at the node grid — the plugin
// list landing page. Drives only the pane's identity border color; the node
// grid otherwise renders and behaves as the ordinary (read-only) grid it is.
func (a *App) isNodeGridPane(p *pane.Pane) bool {
	return a.nodeGrid != "" && p.Anchor == a.nodeGrid && len(p.Path) == 0 && p.TextFocus == ""
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

// pluginKind returns the glyph kind for the plugin owning the given
// qualified grid id: the cached grid's own source_kind when available (the
// fact rides ON the grid — it answers for remote grids a local plugin-list
// lookup cannot), else the local plugin list, else "". A cached localdb grid
// has source_kind "" — that maps to the well glyph, which is correct.
func (a *App) pluginKind(gridID string) string {
	if g, ok := a.c.Grid(gridID); ok {
		switch g.Meta.SourceKind {
		case rpc.GridSourceFS, rpc.GridSourceProc:
			return g.Meta.SourceKind
		case "node":
			return "node" // a node grid (a mount's landing page): generic globe
		default:
			return "localdb"
		}
	}
	if pl, ok := a.pluginByUUID(uuidOf(gridID)); ok {
		return pl.Kind
	}
	return ""
}
