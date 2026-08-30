// Package door answers ONE question the bar keeps asking about a namespace
// level: what did the pane descend THROUGH? A place frame records the
// doorway's id, so the level's identity — its title, its rename target, its
// crumb glyph — must be DERIVED from the row, and before
// this package each consumer derived it differently (the plugin's config
// label, a grid-shape glyph guess), which is how a connection named "rtb"
// presented as "connections" and the trash crumb as a generic grid. One
// derivation, everything else reads it (charter §1); js-free and
// unit-tested per charter §5.
package door

import "github.com/josephburnett/gridwell/api/rpc"

// Kind says what the resolved door IS — which decides renamability: a
// real well row takes the rename gesture; a declaration (a plugin root,
// a root menu entry) is config-owned and read-only.
type Kind int

const (
	None Kind = iota
	// Well: a real tile row (the well actually descended through, or the
	// instance well naming the same place) — renamable where you stand.
	Well
	// Entry: a plugin ROOT MenuEntry's pseudo swatch (the trashcan).
	Entry
	// Root: the plugin's own root swatch.
	Root
)

// WellInto finds the well tile whose child grid is anchor — the door a
// descent into anchor went through. The same scan the portal-ascent
// animation trusts (portalWellForFrame).
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
//  2. a plugin ROOT MenuEntry declaring anchor — a menu-swatch descent
//     (the trashcan);
//  3. the plugin whose RootGridID is anchor — its own swatch (a
//     connection menu row lands here too: its label is the row's, its
//     framing the row's view — v2 #269).
//
// (The old case 4 — an instance well matched through the picker's cached
// instance grid — died with the picker, 2026-08-23.)
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

// EntryGlyph is the glyph a plugin ROOT entry declares for gridID, or ""
// when no entry names it — the one override the grid itself cannot carry
// (the trash grid is an ordinary local grid; only the declaration knows
// its face).
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
