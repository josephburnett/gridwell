package door

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

var sources = []*gridwellv1.SourceInfo{
	{Uuid: "loc", Label: "home", Glyph: "door", RootGridId: "loc/1",
		SourceGrids: []*gridwellv1.SourceGrid{{Id: "trash", Label: "trash", Glyph: "trash", GridId: "loc/9"}}},
	// A connection menu row: a chained uuid whose root is the remote home.
	{Uuid: "sshc/ns1", Label: "rtb", RootGridId: "sshc/ns1/rp1/root7"},
}

// The tile descended through wins over every declaration, so an adopted
// source door carries the user's name for the place.
func TestFindPrefersTheParentGridDoor(t *testing.T) {
	parent := map[string]*gridwellv1.Tile{
		"loc/5": {Id: "loc/5", Kind: rpc.KindDoor, AltText: "my rtb",
			ChildGridId: "sshc/ns1/rp1/root7"},
		"loc/6": {Id: "loc/6", Kind: rpc.KindText, ChildGridId: "sshc/ns1/rp1/root7"},
	}
	got, kind := Find("sshc/ns1/rp1/root7", parent, sources)
	if kind != Tile || got.Id != "loc/5" || got.AltText != "my rtb" {
		t.Fatalf("door = %+v (%v), want the parent-grid door loc/5", got, kind)
	}
}

// A menu-row descent has no parent-grid door, so the connection row is the
// door and its label is declaration-owned.
func TestFindResolvesConnectionRows(t *testing.T) {
	got, kind := Find("sshc/ns1/rp1/root7", nil, sources)
	if kind != Root || got.AltText != "rtb" {
		t.Fatalf("door = %+v (%v), want the rtb connection row", got, kind)
	}
}

// An entry is a top-level door with no level above it, so its crumb wears
// the provenance.
func TestFindResolvesRootEntries(t *testing.T) {
	got, kind := Find("loc/9", nil, sources)
	if kind != Declared || got.AltText != "trash (home)" || got.ChildGridId != "loc/9" {
		t.Fatalf("door = %+v (%v), want the trash entry swatch", got, kind)
	}
}

func TestEntryName(t *testing.T) {
	cases := []struct{ row, entry, want string }{
		{"hey", "Feed", "Feed (hey)"},
		{"home", "trash", "trash (home)"},
		{"files", "", "files"},
		{"", "Feed", "Feed"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := GridName(c.row, c.entry); got != c.want {
			t.Errorf("GridName(%q, %q) = %q, want %q", c.row, c.entry, got, c.want)
		}
	}
}

func TestFindResolvesSourceRoots(t *testing.T) {
	got, kind := Find("loc/1", nil, sources)
	if kind != Root || got.AltText != "home" {
		t.Fatalf("door = %+v (%v), want the local source swatch", got, kind)
	}
}

func TestFindMissesCleanly(t *testing.T) {
	if _, kind := Find("nowhere/3", nil, sources); kind != None {
		t.Fatalf("unknown anchor must resolve to None, got %v", kind)
	}
	if _, kind := Find("", nil, sources); kind != None {
		t.Fatalf("empty anchor must resolve to None, got %v", kind)
	}
}

// A + menu row wears one glyph in the menu and the bar alike, so an undeclared
// glyph cannot mean one thing at one call site and another at the next.
func TestRowGlyphIsTheSameInTheMenuAndInTheBar(t *testing.T) {
	conn := rpc.ConnectionRow("n2/rtb", "", "n2/rtb/rp/1", "", rpc.View{})
	rows := []*gridwellv1.SourceInfo{
		{Uuid: "ufs", Glyph: rpc.GlyphFolder, RootGridId: "ufs/1"},
		{Uuid: "ugl", RootGridId: "ugl/1"}, // the gitlab shape: declares nothing
		conn,
	}
	for _, tc := range []struct {
		name string
		row  *gridwellv1.SourceInfo
		grid *gridwellv1.Grid
		want string
	}{
		{"declared keeps its glyph", rows[0],
			&gridwellv1.Grid{Id: "ufs/1", Glyph: rpc.GlyphFolder}, rpc.GlyphFolder},
		{"undeclared source takes the grid glyph", rows[1],
			&gridwellv1.Grid{Id: "ugl/1"}, rpc.GlyphDoor},
		// The far node's home grid declares its own door, and the
		// connection's glyph still wins on its own root.
		{"a connection takes the globe", conn,
			&gridwellv1.Grid{Id: "n2/rtb/rp/1", Glyph: rpc.GlyphDoor, NodeNs: "n2/rtb"}, rpc.GlyphGlobe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RowGlyph(tc.row); got != tc.want {
				t.Errorf("RowGlyph (the + menu swatch) = %q, want %q", got, tc.want)
			}
			if got := GlyphFor(tc.row.RootGridId, tc.grid, rows); got != tc.want {
				t.Errorf("GlyphFor (the bar crumb) = %q, want %q", got, tc.want)
			}
		})
	}
}

// The trash grid is an ordinary local grid, so only the entry knows its glyph.
func TestEntryGlyph(t *testing.T) {
	if g := GridGlyph("loc/9", sources); g != "trash" {
		t.Errorf("GridGlyph(trash grid) = %q, want trash", g)
	}
	if g := GridGlyph("loc/1", sources); g != "" {
		t.Errorf("GridGlyph(plain root) = %q, want empty", g)
	}
}

// The grid's glyph comes from what its source declared, never from a kind the
// client recognizes. A source that declares nothing takes the door.
func TestGlyphForReadsDeclarationsOnly(t *testing.T) {
	sources := []*gridwellv1.SourceInfo{
		{Uuid: "ufs", Glyph: rpc.GlyphFolder, RootGridId: "ufs/1"},
		{Uuid: "ugl", RootGridId: "ugl/1"},
		{Uuid: "n1/conn", Glyph: rpc.GlyphFolder, SourceGrids: []*gridwellv1.SourceGrid{
			{GridId: "ufs/9", Glyph: rpc.GlyphTrash},
		}},
	}
	for _, tc := range []struct {
		name   string
		gridID string
		grid   *gridwellv1.Grid
		want   string
	}{
		{"declared folder", "ufs/1", &gridwellv1.Grid{Id: "ufs/1", Glyph: rpc.GlyphFolder}, rpc.GlyphFolder},
		{"declares nothing is owned content", "ugl/1", &gridwellv1.Grid{Id: "ugl/1"}, rpc.GlyphDoor},
		{"a root entry outranks the grid", "ufs/9", &gridwellv1.Grid{Id: "ufs/9", Glyph: rpc.GlyphFolder}, rpc.GlyphTrash},
		{"far content wears the door", "n1/conn/x/1", &gridwellv1.Grid{Id: "n1/conn/x/1", NodeNs: "n1/conn"}, rpc.GlyphFolder},
		{"an unknown connection takes the globe", "n1/gone/x/1", &gridwellv1.Grid{Id: "n1/gone/x/1", NodeNs: "n1/gone"}, rpc.GlyphGlobe},
		{"uncached falls back to the source row", "ufs/4", nil, rpc.GlyphFolder},
		{"uncached unknown namespace", "zzz/4", nil, rpc.GlyphDoor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := GlyphFor(tc.gridID, tc.grid, sources); got != tc.want {
				t.Errorf("GlyphFor(%q) = %q, want %q", tc.gridID, got, tc.want)
			}
		})
	}
}

// A declared grid's pseudo-row takes the entry's view, never the declaring
// row's, so a grid lands where it was left.
func TestGridRowCarriesTheGridsFraming(t *testing.T) {
	row := &gridwellv1.SourceInfo{Uuid: "hey", Label: "hey", RootViewCx: 9, RootViewCy: 9, RootViewZoom: 9}
	e := &gridwellv1.SourceGrid{Id: "feed", Label: "Feed", GridId: "hey/2",
		ViewCx: 3.5, ViewCy: -2.25, ViewZoom: 1.75}
	got := GridRow(row, e)
	if got.RootViewCx != 3.5 || got.RootViewCy != -2.25 || got.RootViewZoom != 1.75 {
		t.Errorf("view = %v/%v/%v, want the entry's", got.RootViewCx, got.RootViewCy, got.RootViewZoom)
	}
	blank := GridRow(row, &gridwellv1.SourceGrid{Id: "imbox", GridId: "hey/1"})
	if blank.RootViewZoom != 0 {
		t.Errorf("an unvisited entry must carry no view, got zoom %v", blank.RootViewZoom)
	}
}

func TestPlacesOf(t *testing.T) {
	cases := []struct {
		name string
		row  *gridwellv1.SourceInfo
		want []string
	}{
		{"a home, then its trashcan", sources[0], []string{"loc/1", "loc/9"}},
		{"a connection's far home", sources[1], []string{"sshc/ns1/rp1/root7"}},
		{"a source is its grids", &gridwellv1.SourceInfo{Uuid: "hey", Label: "hey",
			SourceGrids: []*gridwellv1.SourceGrid{{Id: "imbox", GridId: "hey/1"}, {Id: "feed", GridId: "hey/2"}}},
			[]string{"hey/1", "hey/2"}},
		{"an entry with no grid is not a door", &gridwellv1.SourceInfo{Uuid: "hey", Label: "hey",
			SourceGrids: []*gridwellv1.SourceGrid{{Id: "feed"}}}, nil},
		{"a row that declares nothing is a door onto nothing",
			&gridwellv1.SourceInfo{Uuid: "fs", Label: "files"}, nil},
	}
	for _, c := range cases {
		got := PlacesOf(c.row)
		if len(got) != len(c.want) {
			t.Errorf("%s: %d places, want %d", c.name, len(got), len(c.want))
			continue
		}
		for i := range got {
			if got[i].Row.RootGridId != c.want[i] {
				t.Errorf("%s: place %d = %q, want %q", c.name, i, got[i].Row.RootGridId, c.want[i])
			}
		}
	}
}

// A grid's framing is kept against the door the menu descends
// through, so ByRoot must resolve an entry's grid as well as a row's own.
func TestByRootResolvesADeclaredEntry(t *testing.T) {
	got, ok := ByRoot("loc/9", sources)
	if !ok || got.Label != "trash (home)" || got.RootGridId != "loc/9" {
		t.Fatalf("ByRoot = %+v (%v), want the trash entry's door", got, ok)
	}
	if _, ok := ByRoot("", sources); ok {
		t.Error("an empty grid id must resolve to nothing")
	}
}

// The files row is no place of its own: each root is one swatch, in config
// order, wearing the folder, and a directory not yet cached takes the files
// row's glyph.
func TestFilesRowPlacesAreItsRoots(t *testing.T) {
	a, b := rpc.QualifyID("n1", rpc.KeyTileID("/srv/a")), rpc.QualifyID("n1", rpc.KeyTileID("/srv/b"))
	files := &gridwellv1.SourceInfo{Uuid: rpc.FilesOwner("n1"), Label: "files", Glyph: rpc.GlyphFolder,
		SourceGrids: []*gridwellv1.SourceGrid{{Id: "/srv/a", Label: "A", Glyph: rpc.GlyphFolder, GridId: a},
			{Id: "/srv/b", Label: "B", Glyph: rpc.GlyphFolder, GridId: b}}}
	places := PlacesOf(files)
	if len(places) != 2 || places[0].Row.RootGridId != a || places[1].Row.RootGridId != b ||
		places[0].Row.Label != "A (files)" || RowGlyph(places[0].Row) != rpc.GlyphFolder {
		t.Fatalf("places = %v", places)
	}
	roster := []*gridwellv1.SourceInfo{{Uuid: "n1", Label: "home", RootGridId: "n1/1"}, files}
	sub := rpc.QualifyID("n1", rpc.KeyTileID("/srv/a/sub"))
	if got := GlyphFor(sub, nil, roster); got != rpc.GlyphFolder {
		t.Errorf("an uncached directory's glyph = %q, want the files row's folder", got)
	}
	if got := GlyphFor("n1/9", nil, roster); got != rpc.GlyphDoor {
		t.Errorf("an uncached home grid's glyph = %q, want home's door", got)
	}
}

// An uncached grid's bar and notice name the roster row it falls back to: a
// directory's is the files', never home's, though their ids share the node.
func TestRowLabelOfADirectoryIsTheFiles(t *testing.T) {
	roster := []*gridwellv1.SourceInfo{
		{Uuid: "n1", Label: "home", RootGridId: "n1/1"},
		{Uuid: rpc.FilesOwner("n1"), Label: "files"},
	}
	for id, want := range map[string]string{"n1/" + rpc.KeyTileID("/srv/notes"): "files", "n1/4": "home", "zz/4": ""} {
		if got := RowLabel(id, roster); got != want {
			t.Errorf("RowLabel(%q) = %q, want %q", id, got, want)
		}
	}
}

// A door the files refuse this user asks no preview; a disabled source's
// doors, every row stamped on a grid taking no edits, still do.
func TestPreviewAskedSkipsARefusedDoorOnly(t *testing.T) {
	for _, c := range []struct {
		name     string
		readOnly string
		writable bool
		want     bool
	}{
		{"a door", "", true, true},
		{"a door the OS refuses", "Trash cannot be read: operation not permitted", true, false},
		{"a disabled source's door", "rtb is disabled", false, true},
	} {
		d := &gridwellv1.Tile{Kind: rpc.KindDoor, ChildGridId: "n/~L1RyYXNo", ReadOnly: c.readOnly}
		if got := PreviewAsked(d, c.writable); got != c.want {
			t.Errorf("%s: PreviewAsked = %v, want %v", c.name, got, c.want)
		}
	}
	if PreviewAsked(&gridwellv1.Tile{Kind: rpc.KindText}, true) {
		t.Error("a tile opening no grid asks a preview")
	}
}
