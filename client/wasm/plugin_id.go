//go:build js && wasm

package main

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/door"
	"github.com/josephburnett/gridwell/client/pane"
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

// pluginByUUID returns the plugin with the given (possibly chain-
// qualified) namespace: the local list first, then every fetched remote
// menu context (remote-menu, 2026-08-16 — a remote plugin's descent
// guards need its PluginInfo, and its uuid arrives chain-qualified so
// the spaces can never collide).
func (a *App) pluginByUUID(u string) (rpc.PluginInfo, bool) {
	for i := range a.plugins {
		if a.plugins[i].UUID == u {
			return a.plugins[i], true
		}
	}
	for _, ctx := range a.menuCtxs {
		for i := range ctx.plugins {
			if ctx.plugins[i].UUID == u {
				return ctx.plugins[i], true
			}
		}
	}
	return rpc.PluginInfo{}, false
}

// pluginGlyph returns the identity glyph for the plugin owning the given
// qualified grid id, from DECLARATIONS only: a root MenuEntry naming the
// grid wins (the trash grid is an ordinary local grid — only its entry
// knows its face, #264), then the cached grid's own source_kind (a wire
// enum riding ON the grid — it answers for remote grids a local
// plugin-list lookup cannot), then, for content served by ANOTHER node
// (node_ns set), the mount door's declared glyph — the same face the
// tile you descended through wore. Else the plugin's declared glyph from
// the handshake, else "" (the globe). A cached LOCAL grid with no
// source_kind is owned content — the well glyph. No kind strings
// anywhere (charter, 2026-08-15).
func (a *App) pluginGlyph(gridID string) string {
	if g := door.EntryGlyph(gridID, a.allPlugins()); g != "" {
		return g
	}
	if g, ok := a.c.Grid(gridID); ok {
		switch g.Meta.SourceKind {
		case rpc.GridSourceFS:
			return rpc.GlyphFolder
		case rpc.GridSourceProc:
			return rpc.GlyphProcess
		case rpc.GridSourceNode:
			return "" // a node grid (a mount's landing page): generic globe
		default:
			if g.Meta.NodeNS != "" {
				if pl, ok := a.pluginByUUID(uuidOf(g.Meta.NodeNS)); ok {
					return pl.Glyph
				}
				return "" // an unknown mount: the globe, like its swatch
			}
			return rpc.GlyphWell
		}
	}
	if pl, ok := a.pluginByUUID(uuidOf(gridID)); ok {
		return pl.Glyph
	}
	return ""
}

// allPlugins is every PluginInfo the client knows — the boot handshake's
// list plus each fetched remote menu context — for declaration scans
// (door.Find, door.EntryGlyph) that must see remote declarations too.
func (a *App) allPlugins() []rpc.PluginInfo {
	out := a.plugins
	for _, ctx := range a.menuCtxs {
		out = append(out[:len(out):len(out)], ctx.plugins...)
	}
	return out
}
