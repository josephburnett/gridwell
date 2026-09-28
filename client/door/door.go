// Package door derives a level's identity, its title, rename target and crumb
// glyph, from the doorway the pane descended through. It is the one
// derivation; everything else reads it.
package door

import (
	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// EntrySeparator joins a menu row's name to one of its entries'.
const EntrySeparator = " · "

// EntryName is what a row's menu entry is called everywhere it is shown. An
// entry stands alone with no plugin row above it, so it says whose it is:
// "hey · Feed". Every entry of every row is named this way.
func EntryName(row, entry string) string {
	switch {
	case entry == "":
		return row
	case row == "":
		return entry
	}
	return row + EntrySeparator + entry
}

// EntryPlugin shapes one of a row's MenuEntries as a pseudo-row, so every
// downstream flow takes the ordinary row path. It is a doorway and not a
// declarer, so it carries none of the row's entries, which would show every
// collection twice; its framing is the entry's, so a collection reopens where
// it was left.
func EntryPlugin(pl *gridwellv1.PluginInfo, e *gridwellv1.MenuEntry) *gridwellv1.PluginInfo {
	pseudo := proto.Clone(pl).(*gridwellv1.PluginInfo)
	pseudo.RootGridId = e.GridId
	pseudo.RootViewCx, pseudo.RootViewCy, pseudo.RootViewZoom = e.ViewCx, e.ViewCy, e.ViewZoom
	pseudo.MenuEntries = nil
	pseudo.Label = EntryName(pl.Label, e.Label)
	if e.Glyph != "" {
		pseudo.Glyph = e.Glyph
	}
	return pseudo
}

// Kind says what the resolved door is, which decides renamability: a real
// well row takes the rename gesture, a declaration is config-owned.
type Kind int

const (
	None  Kind = iota
	Well       // a real tile row, renamable where you stand
	Entry      // a declared MenuEntry's pseudo swatch
	Root       // a row's own swatch, for a row that is a place. A plugin has none.
)

// WellInto finds the well tile whose child grid is anchor.
func WellInto(anchor string, tiles map[string]*gridwellv1.Tile) (*gridwellv1.Tile, bool) {
	for _, t := range tiles {
		if t.ChildGridId == anchor && rpc.IsWellKind(t.Kind) {
			return t, true
		}
	}
	return nil, false
}

// Find resolves the door into the level rooted at anchor, most specific
// first: the parent level's well whose child is anchor, a MenuEntry declaring
// it, then the row rooted at it. None leaves callers their fallback.
func Find(anchor string, parentTiles map[string]*gridwellv1.Tile, plugins []*gridwellv1.PluginInfo) (*gridwellv1.Tile, Kind) {
	if anchor == "" {
		return nil, None
	}
	if t, ok := WellInto(anchor, parentTiles); ok {
		return t, Well
	}
	for _, pl := range plugins {
		for _, e := range pl.MenuEntries {
			if e.GridId == anchor {
				return rpc.PluginWellTile(EntryPlugin(pl, e)), Entry
			}
		}
	}
	for _, pl := range plugins {
		if pl.RootGridId == anchor {
			return rpc.PluginWellTile(pl), Root
		}
	}
	return nil, None
}

// EntryGlyph is the glyph a menu entry declares for gridID, or "". It is the
// one override the grid cannot carry: the trash grid is an ordinary local
// grid, and only the declaration knows its face.
func EntryGlyph(gridID string, plugins []*gridwellv1.PluginInfo) string {
	for i := range plugins {
		for _, e := range plugins[i].MenuEntries {
			if e.GridId == gridID && e.Glyph != "" {
				return e.Glyph
			}
		}
	}
	return ""
}

// RowGlyph is the face a + menu row wears in its swatch, its ghost and the
// crumb of the grid it roots, so a glyph cannot mean one thing in the menu and
// another in the bar. A connection declares the globe at rpc.ConnectionRow, so
// nothing here switches on a kind.
func RowGlyph(pl *gridwellv1.PluginInfo) string {
	if pl.Glyph != "" {
		return pl.Glyph
	}
	return rpc.GlyphWell
}

// GlyphFor is a grid's identity glyph, from declarations only, most specific
// first: a MenuEntry naming it, a row rooted exactly here, the cached grid's
// own glyph, the mount door's face, then the plugin row's face by namespace. A
// row rooted here wins over the grid's own glyph because a connection's root
// is the far node's home grid, which declares the far node's face. The mount
// door is looked up by exact uuid, since the node's own id prefixes
// "<id>/<conn>". There is always an answer, a faceless crumb being a blank
// square.
func GlyphFor(gridID string, grid *gridwellv1.Grid, plugins []*gridwellv1.PluginInfo) string {
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
		if grid.NodeNs != "" {
			if pl, ok := byUUID(grid.NodeNs, plugins); ok {
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

// Place is one grid a menu row is a doorway onto: the row that names it, a
// pseudo-row for an entry, and the entry it was declared by, nil for the
// row's own grid.
type Place struct {
	Plugin *gridwellv1.PluginInfo
	Entry  *gridwellv1.MenuEntry
}

// PlacesOf enumerates the doorways one menu row declares, in declaration
// order. It is the one enumeration, read by client/palette, ByRoot and the
// framing restore, so what a doorway is cannot be answered three ways.
func PlacesOf(pl *gridwellv1.PluginInfo) []Place {
	out := make([]Place, 0, 1+len(pl.MenuEntries))
	if pl.RootGridId != "" {
		out = append(out, Place{Plugin: pl})
	}
	for _, e := range pl.MenuEntries {
		if e.GridId == "" {
			continue
		}
		out = append(out, Place{Plugin: EntryPlugin(pl, e), Entry: e})
	}
	return out
}

// Places is PlacesOf over a whole menu, rows in order and each row's places
// directly after it.
func Places(plugins []*gridwellv1.PluginInfo) []Place {
	out := make([]Place, 0, len(plugins))
	for _, pl := range plugins {
		out = append(out, PlacesOf(pl)...)
	}
	return out
}

// ByRoot finds the doorway rooted exactly at gridID. Rooted, not by
// namespace: a connection row's uuid "<id>/<conn>" is not a prefix of its
// root "<id>/<conn>/<remote-home>/<n>".
func ByRoot(gridID string, plugins []*gridwellv1.PluginInfo) (*gridwellv1.PluginInfo, bool) {
	if gridID == "" {
		return nil, false
	}
	for _, p := range Places(plugins) {
		if p.Plugin.RootGridId == gridID {
			return p.Plugin, true
		}
	}
	return nil, false
}

func byUUID(u string, plugins []*gridwellv1.PluginInfo) (*gridwellv1.PluginInfo, bool) {
	for _, pl := range plugins {
		if pl.Uuid == u {
			return pl, true
		}
	}
	return nil, false
}
