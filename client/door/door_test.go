package door

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

var plugins = []*gridwellv1.PluginInfo{
	{Uuid: "loc", Label: "home", Glyph: "well", RootGridId: "loc/1",
		MenuEntries: []*gridwellv1.MenuEntry{{Id: "trash", Label: "trash", Glyph: "trash", GridId: "loc/9"}}},
	// A connection menu row: a chained uuid whose root is the remote home.
	{Uuid: "sshc/ns1", Label: "rtb", RootGridId: "sshc/ns1/rp1/root7"},
}

// The tile descended through wins over every declaration, so an adopted
// plugin well carries the user's name for the place.
func TestFindPrefersTheParentGridWell(t *testing.T) {
	parent := map[string]*gridwellv1.Tile{
		"loc/5": {Id: "loc/5", Kind: rpc.KindWell, AltText: "my rtb",
			ChildGridId: "sshc/ns1/rp1/root7"},
		"loc/6": {Id: "loc/6", Kind: rpc.KindText, ChildGridId: "sshc/ns1/rp1/root7"},
	}
	got, kind := Find("sshc/ns1/rp1/root7", parent, plugins)
	if kind != Well || got.Id != "loc/5" || got.AltText != "my rtb" {
		t.Fatalf("door = %+v (%v), want the parent-grid well loc/5", got, kind)
	}
}

// A menu-row descent has no parent-grid well, so the connection row is the
// door and its label is declaration-owned.
func TestFindResolvesConnectionRows(t *testing.T) {
	got, kind := Find("sshc/ns1/rp1/root7", nil, plugins)
	if kind != Root || got.AltText != "rtb" {
		t.Fatalf("door = %+v (%v), want the rtb connection row", got, kind)
	}
}

// An entry is a top-level doorway with no level above it, so its crumb wears
// the provenance.
func TestFindResolvesRootEntries(t *testing.T) {
	got, kind := Find("loc/9", nil, plugins)
	if kind != Entry || got.AltText != "home · trash" || got.ChildGridId != "loc/9" {
		t.Fatalf("door = %+v (%v), want the trash entry swatch", got, kind)
	}
}

func TestEntryName(t *testing.T) {
	cases := []struct{ row, entry, want string }{
		{"hey", "Feed", "hey · Feed"},
		{"home", "trash", "home · trash"},
		{"files", "", "files"},
		{"", "Feed", "Feed"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := EntryName(c.row, c.entry); got != c.want {
			t.Errorf("EntryName(%q, %q) = %q, want %q", c.row, c.entry, got, c.want)
		}
	}
}

func TestFindResolvesPluginRoots(t *testing.T) {
	got, kind := Find("loc/1", nil, plugins)
	if kind != Root || got.AltText != "home" {
		t.Fatalf("door = %+v (%v), want the local plugin swatch", got, kind)
	}
}

func TestFindMissesCleanly(t *testing.T) {
	if _, kind := Find("nowhere/3", nil, plugins); kind != None {
		t.Fatalf("unknown anchor must resolve to None, got %v", kind)
	}
	if _, kind := Find("", nil, plugins); kind != None {
		t.Fatalf("empty anchor must resolve to None, got %v", kind)
	}
}

// A + menu row wears one face in the menu and the bar alike, so an undeclared
// glyph cannot mean one thing at one call site and another at the next.
func TestRowFaceIsTheSameInTheMenuAndInTheBar(t *testing.T) {
	conn := rpc.ConnectionRow("n2/rtb", "", "n2/rtb/rp/1", "", rpc.Framing{})
	rows := []*gridwellv1.PluginInfo{
		{Uuid: "ufs", Glyph: rpc.GlyphFolder, RootGridId: "ufs/1"},
		{Uuid: "ugl", RootGridId: "ugl/1"}, // the gitlab shape: declares nothing
		conn,
	}
	for _, tc := range []struct {
		name string
		row  *gridwellv1.PluginInfo
		grid *gridwellv1.Grid
		want string
	}{
		{"declared keeps its glyph", rows[0],
			&gridwellv1.Grid{Id: "ufs/1", Glyph: rpc.GlyphFolder, HostContent: true}, rpc.GlyphFolder},
		{"undeclared plugin takes the grid face", rows[1],
			&gridwellv1.Grid{Id: "ugl/1"}, rpc.GlyphWell},
		// The far node's home grid declares its own well, and the
		// connection's face still wins on its own root.
		{"a connection takes the globe", conn,
			&gridwellv1.Grid{Id: "n2/rtb/rp/1", Glyph: rpc.GlyphWell, NodeNs: "n2/rtb"}, rpc.GlyphGlobe},
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

// The trash grid is an ordinary local grid, so only the entry knows its face.
func TestEntryGlyph(t *testing.T) {
	if g := EntryGlyph("loc/9", plugins); g != "trash" {
		t.Errorf("EntryGlyph(trash grid) = %q, want trash", g)
	}
	if g := EntryGlyph("loc/1", plugins); g != "" {
		t.Errorf("EntryGlyph(plain root) = %q, want empty", g)
	}
}

// The grid's face comes from what its plugin declared, never from a kind the
// client recognizes. A plugin that declares nothing takes the well.
func TestGlyphForReadsDeclarationsOnly(t *testing.T) {
	plugins := []*gridwellv1.PluginInfo{
		{Uuid: "ufs", Glyph: rpc.GlyphFolder, RootGridId: "ufs/1"},
		{Uuid: "ugl", RootGridId: "ugl/1"},
		{Uuid: "n1/conn", Glyph: rpc.GlyphFolder, MenuEntries: []*gridwellv1.MenuEntry{
			{GridId: "ufs/9", Glyph: rpc.GlyphTrash},
		}},
	}
	for _, tc := range []struct {
		name   string
		gridID string
		grid   *gridwellv1.Grid
		want   string
	}{
		{"declared folder", "ufs/1", &gridwellv1.Grid{Id: "ufs/1", Glyph: rpc.GlyphFolder, HostContent: true}, rpc.GlyphFolder},
		{"declared process", "up/1", &gridwellv1.Grid{Id: "up/1", Glyph: rpc.GlyphProcess, HostContent: true}, rpc.GlyphProcess},
		{"declares nothing is owned content", "ugl/1", &gridwellv1.Grid{Id: "ugl/1"}, rpc.GlyphWell},
		{"a root entry outranks the grid", "ufs/9", &gridwellv1.Grid{Id: "ufs/9", Glyph: rpc.GlyphFolder}, rpc.GlyphTrash},
		{"mounted content wears the door", "n1/conn/x/1", &gridwellv1.Grid{Id: "n1/conn/x/1", NodeNs: "n1/conn"}, rpc.GlyphFolder},
		{"unknown mount takes the globe", "n1/gone/x/1", &gridwellv1.Grid{Id: "n1/gone/x/1", NodeNs: "n1/gone"}, rpc.GlyphGlobe},
		{"uncached falls back to the plugin row", "ufs/4", nil, rpc.GlyphFolder},
		{"uncached unknown namespace", "zzz/4", nil, rpc.GlyphWell},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := GlyphFor(tc.gridID, tc.grid, plugins); got != tc.want {
				t.Errorf("GlyphFor(%q) = %q, want %q", tc.gridID, got, tc.want)
			}
		})
	}
}

// A menu entry's pseudo-row takes the entry's view, never the declaring
// row's, so a collection lands where it was left.
func TestEntryPluginCarriesTheEntrysFraming(t *testing.T) {
	row := &gridwellv1.PluginInfo{Uuid: "hey", Label: "hey", RootViewCx: 9, RootViewCy: 9, RootViewZoom: 9}
	e := &gridwellv1.MenuEntry{Id: "feed", Label: "Feed", GridId: "hey/2",
		ViewCx: 3.5, ViewCy: -2.25, ViewZoom: 1.75}
	got := EntryPlugin(row, e)
	if got.RootViewCx != 3.5 || got.RootViewCy != -2.25 || got.RootViewZoom != 1.75 {
		t.Errorf("view = %v/%v/%v, want the entry's", got.RootViewCx, got.RootViewCy, got.RootViewZoom)
	}
	blank := EntryPlugin(row, &gridwellv1.MenuEntry{Id: "imbox", GridId: "hey/1"})
	if blank.RootViewZoom != 0 {
		t.Errorf("an unvisited entry must carry no view, got zoom %v", blank.RootViewZoom)
	}
}

func TestPlacesOf(t *testing.T) {
	cases := []struct {
		name string
		row  *gridwellv1.PluginInfo
		want []string
	}{
		{"a home, then its trashcan", plugins[0], []string{"loc/1", "loc/9"}},
		{"a connection's far home", plugins[1], []string{"sshc/ns1/rp1/root7"}},
		{"a plugin is its collections", &gridwellv1.PluginInfo{Uuid: "hey", Label: "hey",
			MenuEntries: []*gridwellv1.MenuEntry{{Id: "imbox", GridId: "hey/1"}, {Id: "feed", GridId: "hey/2"}}},
			[]string{"hey/1", "hey/2"}},
		{"an entry with no grid is not a doorway", &gridwellv1.PluginInfo{Uuid: "hey", Label: "hey",
			MenuEntries: []*gridwellv1.MenuEntry{{Id: "feed"}}}, nil},
		{"a row that declares nothing is a doorway onto nothing",
			&gridwellv1.PluginInfo{Uuid: "fs", Label: "files"}, nil},
	}
	for _, c := range cases {
		got := PlacesOf(c.row)
		if len(got) != len(c.want) {
			t.Errorf("%s: %d places, want %d", c.name, len(got), len(c.want))
			continue
		}
		for i := range got {
			if got[i].Plugin.RootGridId != c.want[i] {
				t.Errorf("%s: place %d = %q, want %q", c.name, i, got[i].Plugin.RootGridId, c.want[i])
			}
		}
	}
}

// A collection's framing is remembered against the doorway the menu descends
// through, so ByRoot must resolve an entry's grid as well as a row's own.
func TestByRootResolvesADeclaredEntry(t *testing.T) {
	got, ok := ByRoot("loc/9", plugins)
	if !ok || got.Label != "home · trash" || got.RootGridId != "loc/9" {
		t.Fatalf("ByRoot = %+v (%v), want the trash entry's doorway", got, ok)
	}
	if _, ok := ByRoot("", plugins); ok {
		t.Error("an empty grid id must resolve to nothing")
	}
}
