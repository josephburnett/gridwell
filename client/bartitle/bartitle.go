// Package bartitle owns the bottom bar's centered title: what it says, whether
// the user may retype it, and whether it is muted. One verdict names the
// rename target and the label together, so the bar cannot offer an edit on a
// name it did not draw. It is js-free; the shim gathers the facts, resolves
// the row Rename names, and paints.
package bartitle

import "github.com/josephburnett/gridwell/client/door"

// Rename names which candidate row the title edits.
type Rename int

const (
	// RenameNone is a read-only title: a text tile's name derives from its
	// first line, an ephemeral row dies on ascent, and a declared doorway is
	// config-owned.
	RenameNone Rename = iota
	// RenameDescent is the tile the pane is descended into.
	RenameDescent
	// RenameDoor is the well the level was entered through: renaming the room
	// names its door.
	RenameDoor
	// RenameParent is the well row at the tail of the pane's path.
	RenameParent
)

// Input is the world state the title reads. The caller resolves every field;
// nothing here is re-derived.
type Input struct {
	// Descent is pane.ContentID() != "".
	Descent bool
	// Descended* are the row the pane is descended into, as cached.
	Descended     bool
	DescendedText bool
	DescendedName string
	// DescendedReadOnly is the descended row's Tile.read_only: why its source
	// takes no edits to its body.
	DescendedReadOnly string
	// PossiblyEphemeral gates the rename and CertainlyEphemeral the label: a
	// name written about a visit that may be about to die is a mark the user
	// never asked for, while saying "ephemeral" needs a known yes.
	PossiblyEphemeral  bool
	CertainlyEphemeral bool
	// AtAnchor is len(pane.Path()) == 0: the pane stands on the level's own
	// grid, so its door is the row it was entered through.
	AtAnchor bool
	// Door is door.Find's verdict for that level. The caller resolves it on
	// the AtAnchor arm alone, because resolving it kicks a grid fetch; Decide
	// reads it on that arm alone, so the guard cannot change the verdict.
	Door     door.Kind
	DoorName string
	// Parent is whether the path's tail row is a cached well.
	Parent     bool
	ParentName string
	// ConfigLabel is the declared label of an uncached parent, resolved on the
	// no-descent arm alone for the same reason as Door.
	ConfigLabel string
	// SourceLabel is the pane's grid's Grid.source_label, resolved on the
	// no-descent arm alone.
	SourceLabel string
}

// Verdict is what the bar draws and what a right-click edits. Label is the
// room's own name, the one Rename edits; Source is shown after it, never
// edited.
type Verdict struct {
	Rename   Rename
	Label    string
	Source   string
	Editable bool
	Muted    bool
}

// Text is what the bar draws. The name comes first because the bar clips a
// long title from the right (wsbar.TitleSpan), so a narrow bar loses the
// source before the name. The dash keeps it apart from the "·" plugins use
// inside their labels.
func (v Verdict) Text() string {
	if v.Source == "" {
		return v.Label
	}
	return v.Label + " — " + v.Source
}

const unnamed = "unnamed"

// Decide answers the descent before the level, because a pane inside a tile
// names that tile and not the room around it. Anything with no name of its own
// falls through to a muted "unnamed" rather than to blank chrome. A pane on a
// grid also says what its source calls it, unless that only repeats the name,
// and a pane inside a body its source takes no edits to says why.
func Decide(in Input) Verdict {
	v := decideName(in)
	switch {
	case !in.Descent && in.SourceLabel != v.Label:
		v.Source = in.SourceLabel
	case in.Descent && in.Descended && in.DescendedReadOnly != "":
		v.Source = "read-only: " + in.DescendedReadOnly
	}
	return v
}

func decideName(in Input) Verdict {
	if r := renameOf(in); r != RenameNone {
		switch name := renameName(in, r); name {
		case "":
			return Verdict{Rename: r, Label: unnamed, Editable: true, Muted: true}
		default:
			return Verdict{Rename: r, Label: name, Editable: true}
		}
	}
	switch {
	case in.Descent:
		if in.Descended {
			if in.CertainlyEphemeral {
				return Verdict{Label: "ephemeral", Muted: true}
			}
			if in.DescendedName != "" {
				return Verdict{Label: in.DescendedName} // derived, read-only
			}
		}
	case in.AtAnchor && in.Door != door.None:
		if in.DoorName != "" {
			return Verdict{Label: in.DoorName, Muted: true}
		}
	case in.ConfigLabel != "":
		return Verdict{Label: in.ConfigLabel, Muted: true}
	}
	return Verdict{Label: unnamed, Muted: true}
}

func renameOf(in Input) Rename {
	switch {
	case in.Descent:
		if in.Descended && !in.DescendedText && !in.PossiblyEphemeral {
			return RenameDescent
		}
	case in.AtAnchor:
		if in.Door == door.Well {
			return RenameDoor
		}
	case in.Parent:
		return RenameParent
	}
	return RenameNone
}

func renameName(in Input, r Rename) string {
	switch r {
	case RenameDescent:
		return in.DescendedName
	case RenameDoor:
		return in.DoorName
	case RenameParent:
		return in.ParentName
	}
	return ""
}
