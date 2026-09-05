//go:build js && wasm

package main

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/deadref"
	"github.com/josephburnett/gridwell/client/door"
)

// uuidOf and isExitWell forward to the canonical rpc helpers: the
// "<uuid>/<local>" id convention and its exit-well classification live once,
// in api/rpc, where they are tested. They keep local names because the wasm
// renderer reads them at many call sites.
func uuidOf(id string) string     { return rpc.UUIDOf(id) }
func isExitWell(n *rpc.Tile) bool { return rpc.IsExitWell(n) }

// nodeID is this node's own id: the segment its home grid and every one of
// its connections hang under. The handshake's home grid is the one place it
// is written down, so it is read from there rather than kept a second time.
func (a *App) nodeID() string { return rpc.UUIDOf(a.home) }

// deadNamespace reports that a qualified id names a namespace this node does
// not declare — a plugin dropped from server.yaml, a connection name retired.
// The rule and the boundary against a merely dark namespace are deadref's;
// this is the impure half, handing it the handshake roster and the node id.
//
// Nothing asks a dead namespace for anything: every fetch door consults this
// first, so a dead link costs no RPC, raises no verdict, and surfaces no
// error. It just sits there, greyed, until the user throws it away.
func (a *App) deadNamespace(id string) bool {
	return deadref.Dead(id, a.plugins, a.nodeID())
}

// deadLink reports that a tile is a link into a namespace this node does not
// declare: the greyed, inert face, and the one thing the renderer, the
// descent guard, and the fetch doors all read.
func (a *App) deadLink(n *rpc.Tile) bool {
	return deadref.DeadTile(n, a.plugins, a.nodeID())
}

// gridWritable reports whether the grid accepts new or edited tiles. The fact
// is per grid and travels on the grid (Grid.Writable, stamped by the serving
// node from the owning plugin's Info): a uuid lookup against the local plugin
// list cannot answer for a remote plugin reached through an ssh mount, whose
// local first segment is the transit plugin. An uncached grid is treated as
// not writable, since nothing renders from it yet.
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

// gridKnownReadOnly reports that the grid is cached and declares itself
// read-only. It differs from !gridWritable: an uncached grid is unknown, and
// a drop gesture must not be rejected on ignorance — the server is the
// authority when the fetch has not landed yet.
func (a *App) gridKnownReadOnly(gridID string) bool {
	g, ok := a.c.Grid(gridID)
	return ok && !g.Meta.Writable
}

// pluginByRoot returns the menu row rooted at gridID — the row whose root
// view a root-grid reframe persists to and restores from, and whose face that
// grid wears. The rule is door.ByRoot's, js-free and unit-tested; this is the
// impure half, resolving the declaration list it reads.
func (a *App) pluginByRoot(gridID string) (rpc.PluginInfo, bool) {
	return door.ByRoot(gridID, a.allPlugins())
}

// pluginByUUID returns the plugin with the given, possibly chain-qualified,
// namespace: the local list first, then every fetched remote menu context. A
// remote plugin's descent guards need its PluginInfo, and its uuid arrives
// chain-qualified, so the two spaces cannot collide.
func (a *App) pluginByUUID(u string) (rpc.PluginInfo, bool) {
	for i := range a.plugins {
		if a.plugins[i].UUID == u {
			return a.plugins[i], true
		}
	}
	for _, ctx := range a.views.menuCtxs {
		for i := range ctx.plugins {
			if ctx.plugins[i].UUID == u {
				return ctx.plugins[i], true
			}
		}
	}
	return rpc.PluginInfo{}, false
}

// pluginGlyph returns the identity glyph for the plugin owning the given
// qualified grid id. The rule is door.GlyphFor's, js-free and unit-tested;
// this is the impure half, resolving the cached grid and the declaration
// list the rule reads. No kind strings anywhere.
func (a *App) pluginGlyph(gridID string) string {
	plugins := a.allPlugins()
	if g, ok := a.c.Grid(gridID); ok {
		return door.GlyphFor(gridID, &g.Meta, plugins)
	}
	return door.GlyphFor(gridID, nil, plugins)
}

// allPlugins is every PluginInfo the client knows — the boot handshake's list
// plus each fetched remote menu context — for declaration scans (door.Find,
// door.EntryGlyph) that must see remote declarations too.
func (a *App) allPlugins() []rpc.PluginInfo {
	out := a.plugins
	for _, ctx := range a.views.menuCtxs {
		out = append(out[:len(out):len(out)], ctx.plugins...)
	}
	return out
}
