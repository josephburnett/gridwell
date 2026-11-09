// Package door derives a level's identity, its title, rename target and crumb
// glyph, from the door the pane descended through. It is the one
// derivation; everything else reads it.
package door

import (
	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// GridName is what a row's grid is called everywhere it is shown. A grid's
// swatch stands alone with no row above it, so it says whose it is, most
// specific first so a clipped name still tells two apart: "Docs (files)".
// Every grid of every row is named this way.
func GridName(row, grid string) string {
	switch {
	case grid == "":
		return row
	case row == "":
		return grid
	}
	return grid + " (" + row + ")"
}

// GridRow shapes one of a row's SourceGrids as a pseudo-row, so every
// downstream flow takes the ordinary row path. It carries none of the row's
// grids, which would show every grid twice; its framing is the grid's, so a
// grid reopens where it was left.
func GridRow(pl *gridwellv1.SourceInfo, e *gridwellv1.SourceGrid) *gridwellv1.SourceInfo {
	pseudo := proto.Clone(pl).(*gridwellv1.SourceInfo)
	pseudo.RootGridId = e.GridId
	pseudo.RootViewCx, pseudo.RootViewCy, pseudo.RootViewZoom = e.ViewCx, e.ViewCy, e.ViewZoom
	pseudo.SourceGrids = nil
	pseudo.Label = GridName(pl.Label, e.Label)
	if e.Glyph != "" {
		pseudo.Glyph = e.Glyph
	}
	return pseudo
}

// Kind says what the resolved door is, which decides renamability: a real
// door row takes the rename gesture, a declaration is config-owned.
type Kind int

const (
	None     Kind = iota
	Tile          // a real tile row, renamable where you stand
	Declared      // a declared SourceGrid's pseudo swatch
	Root          // a row's own swatch, for a row that is a place. The files have none.
)

// DoorInto finds the door tile whose child grid is anchor.
func DoorInto(anchor string, tiles map[string]*gridwellv1.Tile) (*gridwellv1.Tile, bool) {
	for _, t := range tiles {
		if t.ChildGridId == anchor && rpc.IsDoorKind(t.Kind) {
			return t, true
		}
	}
	return nil, false
}

// Find resolves the door into the level rooted at anchor, most specific
// first: the parent level's door whose child is anchor, a SourceGrid declaring
// it, then the row rooted at it. None leaves callers their fallback.
func Find(anchor string, parentTiles map[string]*gridwellv1.Tile, sources []*gridwellv1.SourceInfo) (*gridwellv1.Tile, Kind) {
	if anchor == "" {
		return nil, None
	}
	if t, ok := DoorInto(anchor, parentTiles); ok {
		return t, Tile
	}
	for _, pl := range sources {
		for _, e := range pl.SourceGrids {
			if e.GridId == anchor {
				return rpc.SourceDoorTile(GridRow(pl, e)), Declared
			}
		}
	}
	for _, pl := range sources {
		if pl.RootGridId == anchor {
			return rpc.SourceDoorTile(pl), Root
		}
	}
	return nil, None
}

// GridGlyph is the glyph a declared grid declares for gridID, or "". It is the
// one override the grid cannot carry: the trash grid is an ordinary local
// grid, and only the declaration knows its glyph.
func GridGlyph(gridID string, sources []*gridwellv1.SourceInfo) string {
	for i := range sources {
		for _, e := range sources[i].SourceGrids {
			if e.GridId == gridID && e.Glyph != "" {
				return e.Glyph
			}
		}
	}
	return ""
}

// RowGlyph is the glyph a + menu row wears in its swatch, its ghost and the
// crumb of the grid it roots, so a glyph cannot mean one thing in the menu and
// another in the bar. A connection declares the globe at rpc.ConnectionRow, so
// nothing here switches on a kind.
func RowGlyph(pl *gridwellv1.SourceInfo) string {
	if pl.Glyph != "" {
		return pl.Glyph
	}
	return rpc.GlyphDoor
}

// GlyphFor is a grid's identity glyph, from declarations only, most specific
// first: a SourceGrid naming it, a row rooted exactly here, the cached grid's
// own glyph, the glyph of the door to another namespace, then the source row's
// glyph by namespace. A
// row rooted here wins over the grid's own glyph because a connection's root
// is the far node's home grid, which declares the far node's glyph. The door to
// another namespace is looked up by exact uuid, since the node's own id prefixes
// "<id>/<conn>". There is always an answer, a crumb with no glyph being a blank
// square.
func GlyphFor(gridID string, grid *gridwellv1.Grid, sources []*gridwellv1.SourceInfo) string {
	if g := GridGlyph(gridID, sources); g != "" {
		return g
	}
	if pl, ok := ByRoot(gridID, sources); ok {
		return RowGlyph(pl)
	}
	if grid != nil {
		if grid.Glyph != "" {
			return grid.Glyph
		}
		if grid.NodeNs != "" {
			if pl, ok := byUUID(grid.NodeNs, sources); ok {
				return RowGlyph(pl)
			}
			return rpc.GlyphGlobe
		}
		return rpc.GlyphDoor
	}
	if pl, ok := byUUID(rowOf(gridID), sources); ok {
		return RowGlyph(pl)
	}
	return rpc.GlyphDoor
}

// RowLabel is the label of the roster row an uncached grid falls back to,
// "" when none names it.
func RowLabel(gridID string, sources []*gridwellv1.SourceInfo) string {
	pl, _ := byUUID(rowOf(gridID), sources)
	return pl.GetLabel()
}

// rowOf is the roster row an uncached grid falls back to: the files' for a
// directory (rpc.SourceOf), else its first segment's.
func rowOf(gridID string) string {
	if rpc.IsFilesID(gridID) {
		return rpc.SourceOf(gridID)
	}
	return rpc.UUIDOf(gridID)
}

// Place is one grid a menu row is a door onto: the row that names it, or a
// pseudo-row for one of its SourceGrids, and that grid, nil for the row's
// own grid.
type Place struct {
	Row  *gridwellv1.SourceInfo
	Grid *gridwellv1.SourceGrid
}

// PlacesOf enumerates the doors one menu row declares, in declaration
// order. It is the one enumeration, read by client/menu, ByRoot and the
// framing restore, so what a door is cannot be answered three ways.
func PlacesOf(pl *gridwellv1.SourceInfo) []Place {
	out := make([]Place, 0, 1+len(pl.SourceGrids))
	if pl.RootGridId != "" {
		out = append(out, Place{Row: pl})
	}
	for _, e := range pl.SourceGrids {
		if e.GridId == "" {
			continue
		}
		out = append(out, Place{Row: GridRow(pl, e), Grid: e})
	}
	return out
}

// Places is PlacesOf over a whole menu, rows in order and each row's places
// directly after it.
func Places(sources []*gridwellv1.SourceInfo) []Place {
	out := make([]Place, 0, len(sources))
	for _, pl := range sources {
		out = append(out, PlacesOf(pl)...)
	}
	return out
}

// ByRoot finds the door rooted exactly at gridID. Rooted, not by
// namespace: a connection row's uuid "<id>/<conn>" is not a prefix of its
// root "<id>/<conn>/<remote-home>/<n>".
func ByRoot(gridID string, sources []*gridwellv1.SourceInfo) (*gridwellv1.SourceInfo, bool) {
	if gridID == "" {
		return nil, false
	}
	for _, p := range Places(sources) {
		if p.Row.RootGridId == gridID {
			return p.Row, true
		}
	}
	return nil, false
}

func byUUID(u string, sources []*gridwellv1.SourceInfo) (*gridwellv1.SourceInfo, bool) {
	for _, pl := range sources {
		if pl.Uuid == u {
			return pl, true
		}
	}
	return nil, false
}

// PreviewAsked is whether a door's preview asks for the grid it opens. A
// door whose row says why on a grid that takes edits opens a grid its source
// refuses this user, so nothing asks until the user descends; a switched-off
// source stamps every row and its grid takes no edits, so its doors still
// preview what the node holds.
func PreviewAsked(t *gridwellv1.Tile, gridWritable bool) bool {
	return t.GetChildGridId() != "" && (t.GetReadOnly() == "" || !gridWritable)
}
