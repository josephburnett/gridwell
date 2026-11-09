package pane

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/theme"
)

// The one tile classifier: every drawer of a tile, its swatch and its crumb
// reads it, so a door, the pane it opens and the crumb that names it agree.
func TestTileFamily(t *testing.T) {
	dir := "n1/" + rpc.KeyTileID("/srv")
	door := func(grid, child string) *gridwellv1.Tile {
		return &gridwellv1.Tile{Kind: rpc.KindDoor, GridId: grid, ChildGridId: child}
	}
	cases := []struct {
		name        string
		tile        *gridwellv1.Tile
		inDirectory bool
		want        theme.Family
	}{
		{"an interior door", door("n1/1", "n1/2"), false, theme.FamilyGrid},
		{"a door with no grid yet", &gridwellv1.Tile{Kind: rpc.KindDoor}, false, theme.FamilyGrid},
		{"a home door onto a directory", door("n1/1", dir), false, theme.FamilyDirectory},
		{"a root's swatch", rpc.SourceDoorTile(&gridwellv1.SourceInfo{RootGridId: dir}), false, theme.FamilyDirectory},
		{"a directory's door into its subdirectory", door(dir, "n1/"+rpc.KeyTileID("/srv/a")), true, theme.FamilyDirectory},
		{"a door to a far home in a directory", door(dir, "n1/geneva/n2/5"), true, theme.FamilyGrid},
		{"a door onto a far home's grid", door("n1/1", "n1/geneva/n2/5"), false, theme.FamilyGrid},
		{"a text tile", &gridwellv1.Tile{Kind: rpc.KindText}, false, theme.FamilyText},
		{"a text in a directory", &gridwellv1.Tile{Kind: rpc.KindText}, true, theme.FamilyDirectory},
		{"a url tile", &gridwellv1.Tile{Kind: rpc.KindURL}, false, theme.FamilyURL},
		{"a page file", &gridwellv1.Tile{Kind: rpc.KindURL}, true, theme.FamilyDirectory},
		{"a shell", &gridwellv1.Tile{Kind: rpc.KindShell}, false, theme.FamilyShell},
		{"a shell is a shell in a directory", &gridwellv1.Tile{Kind: rpc.KindShell}, true, theme.FamilyShell},
		{"a workspace tile", &gridwellv1.Tile{Kind: rpc.KindWorkspace}, false, theme.FamilyWorkspace},
		{"an unknown kind", &gridwellv1.Tile{Kind: "fax"}, false, theme.FamilyMuted},
	}
	for _, c := range cases {
		if got := TileFamily(c.tile, c.inDirectory); got != c.want {
			t.Errorf("%s: TileFamily = %v, want %v", c.name, got, c.want)
		}
	}
}

// The pane's family, which its border and the bar read.
func TestFamilyOf(t *testing.T) {
	cases := []struct {
		name string
		in   BorderInput
		want theme.Family
	}{
		{"grid", BorderInput{}, theme.FamilyGrid},
		{"a descent with no content frame", BorderInput{DescentDepth: 1}, theme.FamilyGrid},
		{"text", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "text"}, theme.FamilyText},
		{"a file's text", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "text", InDirectory: true}, theme.FamilyDirectory},
		{"a directory", BorderInput{InDirectory: true}, theme.FamilyDirectory},
		{"a page file is a file, live or not", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "url", InDirectory: true, URLLive: true}, theme.FamilyDirectory},
		{"url frozen", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "url"}, theme.FamilyURL},
		{"url live", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "url", URLLive: true}, theme.FamilyURLLive},
		{"shell", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "shell"}, theme.FamilyShell},
		{"ephemeral beats kind and live", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "url", URLLive: true, Ephemeral: true}, theme.FamilyEphemeral},
		{"an unknown tile is the grid's", BorderInput{HasTextFocus: true, TileKind: "text", URLLive: true}, theme.FamilyGrid},
		{"an unknown tile in a directory is the directory's", BorderInput{HasTextFocus: true, InDirectory: true}, theme.FamilyDirectory},
	}
	for _, c := range cases {
		if got := FamilyOf(c.in); got != c.want {
			t.Errorf("%s: FamilyOf = %v, want %v", c.name, got, c.want)
		}
	}
}

// A pane on a directory and a door onto it are one family, so the door, the
// pane it opens and its crumb cannot disagree.
func TestADoorAndThePaneItOpensAgree(t *testing.T) {
	for _, child := range []string{"n1/2", "n1/" + rpc.KeyTileID("/srv"), "n1/geneva/n2/5"} {
		d := TileFamily(&gridwellv1.Tile{Kind: rpc.KindDoor, ChildGridId: child}, false)
		p := FamilyOf(BorderInput{DescentDepth: 1, InDirectory: rpc.IsFilesID(child)})
		if d != p || d != GridFamily(child) {
			t.Errorf("%s: door %v, pane %v, crumb %v", child, d, p, GridFamily(child))
		}
	}
}
