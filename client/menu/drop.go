package menu

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/sourcehealth"
)

// What a menu drag's release does. Every swatch rides the same drag, so one
// verdict answers for every one of them, and a release that refuses what it
// asked says why, in the words a tile's drag uses (dragdrop.Told).

type Drop int

const (
	// DropSnapBack returns the ghost to its swatch and leaves the menu open.
	DropSnapBack Drop = iota
	// DropLink places a door to another namespace onto the door's root grid.
	DropLink
	// DropCreate creates the primitive the swatch names.
	DropCreate
)

// OtherNode is why a primitive over another node's grid is refused: the
// swatch was gated by the menu's node.
const OtherNode = "this menu belongs to another node — drop into a grid on that node, or open the menu here"

// Why a file or directory swatch is refused: a name is made on disk only in
// a directory, and only in one the disk lets this user write.
const (
	DiskNeedsDirectory = "a file is made in a directory"
	DiskReadOnly       = "this directory cannot be written"
)

// Release is the world state a drop reads. The caller resolves every field
// against the destination the drag-preview already settled on, so the create
// lands exactly where the preview said it may.
type Release struct {
	// Target is whether the release landed on a grid at all.
	Target bool
	// Occupied is whether the snapped cells already hold a tile.
	Occupied bool
	// Door is the source a source, connection or declared-root swatch opens,
	// nil for a primitive.
	Door *gridwellv1.SourceInfo
	// AcceptsTiles is the destination's Grid.accepts_tiles, TilesKnown
	// whether its grid is read. A door needs it known: minting into a grid
	// that may refuse the link would show a tile the next read takes back.
	AcceptsTiles, TilesKnown bool
	// SameNode is whether the destination belongs to the node whose menu
	// offered the swatch.
	SameNode bool
	// Disk marks the file or directory swatch; Directory is the
	// destination's Offer.Directory and DiskWritable its Offer.Disk.
	Disk, Directory, DiskWritable bool
}

// DropOn asks the destination before the swatch, because a drop with nowhere
// to land is a snap-back whatever was dragged; a file or directory swatch's
// own refusals stand above a taken cell, since moving to another cell would
// not help. why is the refusal told, "" when it says nothing.
func DropOn(r Release) (d Drop, why string) {
	refuses := r.TilesKnown && !r.AcceptsTiles
	switch {
	case !r.Target:
		return DropSnapBack, ""
	case r.Disk && !r.Directory:
		return DropSnapBack, DiskNeedsDirectory
	case r.Disk && !r.DiskWritable:
		return DropSnapBack, DiskReadOnly
	case r.Occupied:
		return DropSnapBack, dragdrop.CellTaken
	case r.Door != nil && refuses:
		return DropSnapBack, dragdrop.TakesNoTiles
	case r.Door != nil && !opens(r.Door):
		// A row sourcehealth does not classify is never offered (Doors).
		_, _, why, _ := sourcehealth.ClickNotice(r.Door)
		return DropSnapBack, why
	case r.Door != nil && r.AcceptsTiles:
		return DropLink, ""
	case r.Door != nil:
		return DropSnapBack, ""
	case !r.SameNode:
		return DropSnapBack, OtherNode
	case refuses:
		return DropSnapBack, dragdrop.TakesNoTiles
	}
	return DropCreate, ""
}

func opens(pl *gridwellv1.SourceInfo) bool {
	st, known := sourcehealth.Classify(pl)
	return known && st == sourcehealth.Enterable
}
