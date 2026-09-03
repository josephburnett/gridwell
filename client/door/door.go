// Package door answers one question the bar keeps asking about a namespace
// level: what did the pane descend through? A place frame records the
// doorway's id, so the level's identity — its title, its rename target, its
// crumb glyph — is derived from the row. This is the one derivation;
// everything else reads it. Js-free and unit-tested.
package door

import "github.com/josephburnett/gridwell/api/rpc"

// Kind says what the resolved door is, which decides renamability: a real
// well row takes the rename gesture; a declaration (a plugin root, a root
// menu entry) is config-owned and read-only.
type Kind int

const (
	None Kind = iota
	// Well: a real tile row (the well actually descended through, or the
	// instance well naming the same place) — renamable where you stand.
	Well
	// Entry: a plugin root MenuEntry's pseudo swatch (the trashcan).
	Entry
	// Root: the plugin's own root swatch.
	Root
)

// WellInto finds the well tile whose child grid is anchor — the door a
// descent into anchor went through. The portal-ascent animation runs the
// same scan.
func WellInto(anchor string, tiles map[string]rpc.Tile) (rpc.Tile, bool) {
	for _, t := range tiles {
		if t.ChildGridID == anchor && rpc.IsWellKind(t.Kind) {
			return t, true
		}
	}
	return rpc.Tile{}, false
}

// Find resolves the door into the level rooted at anchor, most-specific
// first:
//  1. the parent level's well whose child is anchor — the tile actually
//     descended through (grid-tile descents, adopted plugin wells);
//  2. a plugin root MenuEntry declaring anchor — a menu-swatch descent
//     (the trashcan);
//  3. the plugin whose RootGridID is anchor — its own swatch (a connection
//     menu row lands here too: its label is the row's, its framing the
//     row's view).
//
// A None result means the level has no derivable door (a workspace root,
// an uncached world) — callers keep their fallback.
func Find(anchor string, parentTiles map[string]rpc.Tile, plugins []rpc.PluginInfo) (rpc.Tile, Kind) {
	if anchor == "" {
		return rpc.Tile{}, None
	}
	if t, ok := WellInto(anchor, parentTiles); ok {
		return t, Well
	}
	for i := range plugins {
		pl := &plugins[i]
		for j := range pl.MenuEntries {
			e := &pl.MenuEntries[j]
			if e.GridID == anchor {
				return rpc.PluginWellTile(rpc.EntryPlugin(*pl, *e)), Entry
			}
		}
	}
	for i := range plugins {
		if plugins[i].RootGridID == anchor {
			return rpc.PluginWellTile(plugins[i]), Root
		}
	}
	return rpc.Tile{}, None
}

// EntryGlyph is the glyph a plugin root entry declares for gridID, or ""
// when no entry names it — the one override the grid itself cannot carry
// (the trash grid is an ordinary local grid; only the declaration knows its
// face).
func EntryGlyph(gridID string, plugins []rpc.PluginInfo) string {
	for i := range plugins {
		for _, e := range plugins[i].MenuEntries {
			if e.GridID == gridID && e.Glyph != "" {
				return e.Glyph
			}
		}
	}
	return ""
}

// RowGlyph is the face a + menu row wears — the one answer for its swatch,
// its drag ghost, and the crumb of the grid it roots, so an undeclared glyph
// cannot mean one thing in the menu and another in the bar. A row that
// declares a glyph keeps it everywhere; a row that declares none takes the
// grid face, because a plugin serves grids. A connection declares the globe
// where connection rows are minted (rpc.ConnectionRow), so nothing here
// switches on a kind.
func RowGlyph(pl rpc.PluginInfo) string {
	if pl.Glyph != "" {
		return pl.Glyph
	}
	return rpc.GlyphWell
}

// GlyphFor is the identity glyph for the plugin owning gridID, from
// DECLARATIONS only — no reader here knows a plugin's kind. Most specific
// first:
//
//  1. a root MenuEntry naming the grid: the trash grid is an ordinary local
//     grid, so only its entry knows its face;
//  2. a + menu row rooted exactly here: this grid IS that row, so it wears
//     the row's face (RowGlyph), the same square the menu draws. A
//     connection's root is the far node's home grid, which declares the far
//     node's own face; from here it is the connection, so the row wins;
//  3. the cached grid's own declared glyph (Grid.glyph, stamped by the
//     serving node from the owning plugin's Info), which answers for remote
//     grids a local plugin-list lookup cannot;
//  4. for content served by another node (node_ns set), the mount door's
//     declared glyph — the same face the tile you descended through wore.
//     The mount door is the connection row, uuid "<id>/<conn>"; the node's
//     own id prefixes it, so a prefix lookup would answer for home, not the
//     door. An unknown mount takes the globe, like every connection;
//  5. the plugin row's face from the handshake, looked up by the grid's
//     namespace, for a grid not cached yet.
//
// A cached grid that declares no glyph and came from this node is owned
// content: the well glyph. grid is nil when the client has not cached it.
// There is always an answer — a crumb with no face is a blank square.
func GlyphFor(gridID string, grid *rpc.Grid, plugins []rpc.PluginInfo) string {
	if g := EntryGlyph(gridID, plugins); g != "" {
		return g
	}
	if pl, ok := ByRoot(gridID, plugins); ok {
		return RowGlyph(pl)
	}
	if grid != nil {
		if grid.Glyph != "" {
			return grid.Glyph
		}
		if grid.NodeNS != "" {
			if pl, ok := byUUID(grid.NodeNS, plugins); ok {
				return RowGlyph(pl)
			}
			return rpc.GlyphGlobe
		}
		return rpc.GlyphWell
	}
	if pl, ok := byUUID(rpc.UUIDOf(gridID), plugins); ok {
		return RowGlyph(pl)
	}
	return rpc.GlyphWell
}

// ByRoot finds the menu row rooted exactly at gridID — the row whose swatch
// this grid IS. Rooted, not by namespace: a connection row's uuid
// ("<id>/<conn>") is not a prefix of its root
// ("<id>/<conn>/<remote-home>/<n>").
func ByRoot(gridID string, plugins []rpc.PluginInfo) (rpc.PluginInfo, bool) {
	if gridID == "" {
		return rpc.PluginInfo{}, false
	}
	for i := range plugins {
		if plugins[i].RootGridID == gridID {
			return plugins[i], true
		}
	}
	return rpc.PluginInfo{}, false
}

// byUUID finds the plugin row with the given, possibly chain-qualified,
// namespace.
func byUUID(u string, plugins []rpc.PluginInfo) (rpc.PluginInfo, bool) {
	for i := range plugins {
		if plugins[i].UUID == u {
			return plugins[i], true
		}
	}
	return rpc.PluginInfo{}, false
}
