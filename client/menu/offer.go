package menu

// Which primitives the + menu offers on a grid. Every fact is the serving
// node's: the PTY rides that node's web API, so the client's own caps would
// be a second, wrong owner of the shells policy.

import (
	"strings"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Offer is what the grid and its node declare.
type Offer struct {
	// AcceptsTiles is Grid.accepts_tiles; unknown is false, so no swatch is
	// shown on a guess a drop would then refuse.
	AcceptsTiles bool
	// ShellsDisabled is the context node's disable_shells.
	ShellsDisabled bool
	// Directory is the grid's id naming a directory (rpc.IsFilesID), and
	// Writable its Grid.writable: the disk side's accepts fact.
	Directory, Writable bool
}

// Primitives reports whether the primitive row is offered at all.
func (o Offer) Primitives() bool { return o.AcceptsTiles }

// Shell reports whether the shell swatch is among them.
func (o Offer) Shell() bool { return o.AcceptsTiles && !o.ShellsDisabled }

// Disk reports whether the file and directory swatches are among them: only
// on a directory's grid, and only while the disk takes a new name there.
func (o Offer) Disk() bool { return o.Directory && o.Writable }

// DiskCreate is the create a file or directory swatch asks for once its name
// is typed: on the disk side of grid (CreateTileRequest.file), a directory for
// a door kind and an empty file for any other, named name trimmed. An empty
// name creates nothing.
func DiskCreate(grid, kind, name string, x, y int64) (*gridwellv1.CreateTileRequest, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	return &gridwellv1.CreateTileRequest{GridId: grid, File: true,
		Tile: &gridwellv1.Tile{Kind: kind, AltText: name, X: x, Y: y, W: 1, H: 1}}, true
}
