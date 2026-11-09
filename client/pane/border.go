package pane

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/theme"
)

// BorderInput is what the classifier needs about a pane. It is a struct rather
// than a *Pane so this package depends on no cache or tile types: the caller
// resolves the descended tile and its kind first.
type BorderInput struct {
	HasTextFocus bool // the pane's place is a content frame
	DescentDepth int  // greater than 0 means inside at least one door
	TileKnown    bool // the descended row is cached, so TileKind is meaningful
	TileKind     string
	TileID       string // the descended row's id, which says whether it is a file
	Focused      bool   // the keyboard-focused pane in the split tree
	URLLive      bool   // a live native view renders into this pane
	// InDirectory is whether the viewed grid is a directory
	// (cache.Grid.Directory).
	InDirectory bool
	// Ephemeral is the tile the pane stands in, descended into or entered as
	// a door, living in the scratch grid, so it is deleted on ascent.
	Ephemeral bool
}

// TileFamily is the one classifier of a tile's colour: its frame, its banner,
// its swatch, its crumb, its ghost and a pane descended into it all read it.
// A file or a directory is one by its id (rpc.OnDisk): a Gridwell tile on a
// directory's grid wears its kind's family, as in home. A gone door is
// muted; the grid it opens is still a directory's.
func TileFamily(t *gridwellv1.Tile) theme.Family {
	disk := rpc.OnDisk(t.GetId())
	switch k := rpc.KindOf(t.GetKind()); {
	case t.GetGone():
		return theme.FamilyMuted
	case k.Live == rpc.DescentShell:
		return theme.FamilyShell
	case k.Descends == rpc.DescendGrid && disk:
		return theme.FamilyDirectory
	case k.Descends == rpc.DescendGrid:
		// A door wears the family of the grid it opens, wherever it sits.
		return GridFamily(t.GetChildGridId())
	case disk && (k.Live == rpc.DescentURL || k.Content == rpc.ContentBody):
		// A file is outside, whatever it holds.
		return theme.FamilyDirectory
	case k.Live == rpc.DescentURL:
		return theme.FamilyURL
	case k.Content == rpc.ContentBody:
		return theme.FamilyText
	case k.Content == rpc.ContentLayout:
		return theme.FamilyWorkspace
	}
	return theme.FamilyMuted
}

// GridFamily is a grid's family: a directory is outside the system.
func GridFamily(gridID string) theme.Family {
	if rpc.IsFilesID(gridID) {
		return theme.FamilyDirectory
	}
	return theme.FamilyGrid
}

// FamilyOf is the pane's family, which its border and the bar's band and
// buttons read: the descended tile's, or the grid's.
func FamilyOf(s BorderInput) theme.Family {
	if s.Ephemeral {
		return theme.FamilyEphemeral
	}
	if !s.HasTextFocus || !s.TileKnown {
		if s.InDirectory {
			return theme.FamilyDirectory
		}
		return theme.FamilyGrid
	}
	f := TileFamily(&gridwellv1.Tile{Kind: s.TileKind, Id: s.TileID})
	if f == theme.FamilyURL && s.URLLive {
		return theme.FamilyURLLive
	}
	return f
}
