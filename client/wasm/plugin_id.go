//go:build js && wasm

package main

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/deadref"
	"github.com/josephburnett/gridwell/client/door"
)

// uuidOf and isExitWell forward to api/rpc, where the "<uuid>/<local>" id
// convention lives and is tested. The local names are for the renderer's many
// call sites.
func uuidOf(id string) string            { return rpc.UUIDOf(id) }
func isExitWell(n *gridwellv1.Tile) bool { return rpc.IsExitWell(n) }

// nodeID is the segment this node's home grid and connections hang under,
// read off the handshake's home grid, which is where it is written down.
func (a *App) nodeID() string { return rpc.UUIDOf(a.home) }

// deadNamespace reports that a qualified id names a namespace this node does
// not declare. The rule, and the boundary against a merely dark namespace, is
// deadref's. Every fetch door consults it first, so a dead link costs no RPC,
// raises no verdict, and surfaces no error.
func (a *App) deadNamespace(id string) bool {
	return deadref.Dead(id, a.plugins, a.nodeID())
}

// deadLink reports that a tile links into a namespace this node does not
// declare: the one thing the renderer, the descent guard and the fetch doors
// all read.
func (a *App) deadLink(n *gridwellv1.Tile) bool {
	return deadref.DeadTile(n, a.plugins, a.nodeID())
}

// gridWritable reports whether the grid accepts new or edited tiles, and
// whether that is known. The fact travels on the grid, because a uuid lookup
// against the local plugin list cannot answer for a remote plugin reached
// through an ssh mount. An uncached grid answers (false, false), never a
// guess, and each caller picks its own default where the reason is visible.
func (a *App) gridWritable(gridID string) (writable, known bool) {
	if gridID == "" {
		return false, false
	}
	g, ok := a.c.Grid(gridID)
	if !ok {
		return false, false
	}
	return g.Meta.Writable, true
}

// pluginByRoot returns the doorway rooted at gridID, a menu row's own grid
// or one of its declared entries'. The rule is door.ByRoot's; this resolves
// the declaration list it reads.
func (a *App) pluginByRoot(gridID string) (*gridwellv1.PluginInfo, bool) {
	return door.ByRoot(gridID, a.allPlugins())
}

// cacheDoorwayFraming reconciles the handshake's copy of a doorway's framing,
// after this client's own root-grid reframe commits and when another's
// arrives as an event; rpc.Reframe owns which rows that is.
func (a *App) cacheDoorwayFraming(gridID string, f rpc.Framing) bool {
	return rpc.Reframe(gridID, f, a.allPlugins())
}

// pluginByUUID returns the plugin with the given, possibly chain-qualified,
// namespace, searching the local list then every fetched remote menu context.
// A chain-qualified uuid cannot collide with a local one.
func (a *App) pluginByUUID(u string) (*gridwellv1.PluginInfo, bool) {
	for i := range a.plugins {
		if a.plugins[i].Uuid == u {
			return a.plugins[i], true
		}
	}
	for _, ctx := range a.views.menuCtxs {
		for i := range ctx.plugins {
			if ctx.plugins[i].Uuid == u {
				return ctx.plugins[i], true
			}
		}
	}
	return nil, false
}

// pluginGlyph returns the identity glyph for the plugin owning a qualified
// grid id. The rule is door.GlyphFor's; this resolves the cached grid and the
// declaration list it reads.
func (a *App) pluginGlyph(gridID string) string {
	plugins := a.allPlugins()
	if g, ok := a.c.Grid(gridID); ok {
		return door.GlyphFor(gridID, g.Meta, plugins)
	}
	return door.GlyphFor(gridID, nil, plugins)
}

// allPlugins is the boot handshake's list plus each fetched remote menu
// context, for declaration scans that must see remote declarations too.
func (a *App) allPlugins() []*gridwellv1.PluginInfo {
	out := a.plugins
	for _, ctx := range a.views.menuCtxs {
		out = append(out[:len(out):len(out)], ctx.plugins...)
	}
	return out
}
