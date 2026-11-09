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
	file := func(kind, path string) *gridwellv1.Tile {
		return &gridwellv1.Tile{Kind: kind, Id: "n1/" + rpc.KeyTileID(path), GridId: dir}
	}
	door := func(grid, child string) *gridwellv1.Tile {
		return &gridwellv1.Tile{Kind: rpc.KindDoor, GridId: grid, ChildGridId: child}
	}
	cases := []struct {
		name string
		tile *gridwellv1.Tile
		want theme.Family
	}{
		{"an interior door", door("n1/1", "n1/2"), theme.FamilyGrid},
		{"a door with no grid yet", &gridwellv1.Tile{Kind: rpc.KindDoor}, theme.FamilyGrid},
		{"a home door onto a directory", door("n1/1", dir), theme.FamilyDirectory},
		{"a root's swatch", rpc.SourceDoorTile(&gridwellv1.SourceInfo{RootGridId: dir}), theme.FamilyDirectory},
		{"a directory's door into its subdirectory", door(dir, "n1/"+rpc.KeyTileID("/srv/a")), theme.FamilyDirectory},
		{"a directory's own door", file(rpc.KindDoor, "/srv/a"), theme.FamilyDirectory},
		{"a gone directory's door", &gridwellv1.Tile{Kind: rpc.KindDoor, Id: "n1/" + rpc.KeyTileID("/srv/a"), GridId: dir, ChildGridId: "n1/" + rpc.KeyTileID("/srv/a"), Gone: true}, theme.FamilyMuted},
		{"a door to a far home in a directory", door(dir, "n1/geneva/n2/5"), theme.FamilyGrid},
		{"a door onto a far home's grid", door("n1/1", "n1/geneva/n2/5"), theme.FamilyGrid},
		{"a text tile", &gridwellv1.Tile{Kind: rpc.KindText}, theme.FamilyText},
		{"a text file", file(rpc.KindText, "/srv/a.md"), theme.FamilyDirectory},
		// A Gridwell tile on a directory's grid is its kind's (2026-11-08).
		{"a Gridwell text on a directory", &gridwellv1.Tile{Kind: rpc.KindText, Id: "n1/7", GridId: dir}, theme.FamilyText},
		{"a Gridwell url on a directory", &gridwellv1.Tile{Kind: rpc.KindURL, Id: "n1/8", GridId: dir}, theme.FamilyURL},
		{"a url tile", &gridwellv1.Tile{Kind: rpc.KindURL}, theme.FamilyURL},
		{"a page file", file(rpc.KindURL, "/srv/p.png"), theme.FamilyDirectory},
		{"a shell", &gridwellv1.Tile{Kind: rpc.KindShell}, theme.FamilyShell},
		{"a shell is a shell in a directory", &gridwellv1.Tile{Kind: rpc.KindShell, Id: "n1/9", GridId: dir}, theme.FamilyShell},
		{"a workspace tile", &gridwellv1.Tile{Kind: rpc.KindWorkspace}, theme.FamilyWorkspace},
		{"an unknown kind", &gridwellv1.Tile{Kind: "fax"}, theme.FamilyMuted},
	}
	for _, c := range cases {
		if got := TileFamily(c.tile); got != c.want {
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
		{"a file's text", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "text", TileID: "n1/" + rpc.KeyTileID("/srv/a.md"), InDirectory: true}, theme.FamilyDirectory},
		{"a Gridwell text on a directory", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "text", TileID: "n1/7", InDirectory: true}, theme.FamilyText},
		{"a directory", BorderInput{InDirectory: true}, theme.FamilyDirectory},
		// The gone door is muted; the place it opens is still a directory.
		{"inside a gone directory", BorderInput{DescentDepth: 1, InDirectory: true}, theme.FamilyDirectory},
		{"a page file is a file, live or not", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "url", TileID: "n1/" + rpc.KeyTileID("/srv/p.png"), InDirectory: true, URLLive: true}, theme.FamilyDirectory},
		{"url frozen", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "url"}, theme.FamilyURL},
		{"url live", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "url", URLLive: true}, theme.FamilyURLLive},
		{"shell", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "shell"}, theme.FamilyShell},
		{"ephemeral beats kind and live", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "url", URLLive: true, Ephemeral: true}, theme.FamilyEphemeral},
		{"a grid entered through an ephemeral door is ephemeral", BorderInput{DescentDepth: 1, Ephemeral: true}, theme.FamilyEphemeral},
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
		d := TileFamily(&gridwellv1.Tile{Kind: rpc.KindDoor, ChildGridId: child})
		p := FamilyOf(BorderInput{DescentDepth: 1, InDirectory: rpc.IsFilesID(child)})
		if d != p || d != GridFamily(child) {
			t.Errorf("%s: door %v, pane %v, crumb %v", child, d, p, GridFamily(child))
		}
	}
}
