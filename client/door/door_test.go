package door

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

var plugins = []rpc.PluginInfo{
	{UUID: "loc", Label: "home", Glyph: "well", RootGridID: "loc/1",
		MenuEntries: []rpc.MenuEntry{{ID: "trash", Label: "trash", Glyph: "trash", GridID: "loc/9"}}},
	// A connection menu row: a chained uuid whose root is the remote home.
	{UUID: "sshc/ns1", Label: "rtb", RootGridID: "sshc/ns1/rp1/root7"},
}

// The tile actually descended through wins over every declaration — an
// adopted plugin well carries the user's name for the place.
func TestFindPrefersTheParentGridWell(t *testing.T) {
	parent := map[string]rpc.Tile{
		"loc/5": {ID: "loc/5", Kind: rpc.KindWell, AltText: "my rtb",
			ChildGridID: "sshc/ns1/rp1/root7"},
		"loc/6": {ID: "loc/6", Kind: rpc.KindText, ChildGridID: "sshc/ns1/rp1/root7"},
	}
	got, kind := Find("sshc/ns1/rp1/root7", parent, plugins)
	if kind != Well || got.ID != "loc/5" || got.AltText != "my rtb" {
		t.Fatalf("door = %+v (%v), want the parent-grid well loc/5", got, kind)
	}
}

// A menu-row descent has no parent-grid well; the connection row is the
// door — its label, declaration-owned (renaming is a yaml edit).
func TestFindResolvesConnectionRows(t *testing.T) {
	got, kind := Find("sshc/ns1/rp1/root7", nil, plugins)
	if kind != Root || got.AltText != "rtb" {
		t.Fatalf("door = %+v (%v), want the rtb connection row", got, kind)
	}
}

// A menu-swatch descent into a root entry resolves to the entry's pseudo
// swatch: its label and glyph, declaration-owned (not renamable).
func TestFindResolvesRootEntries(t *testing.T) {
	got, kind := Find("loc/9", nil, plugins)
	if kind != Entry || got.AltText != "trash" || got.ChildGridID != "loc/9" {
		t.Fatalf("door = %+v (%v), want the trash entry swatch", got, kind)
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

// The trash grid is an ordinary local grid — only the entry declaration
// knows its face. Anything undeclared answers "".
func TestEntryGlyph(t *testing.T) {
	if g := EntryGlyph("loc/9", plugins); g != "trash" {
		t.Errorf("EntryGlyph(trash grid) = %q, want trash", g)
	}
	if g := EntryGlyph("loc/1", plugins); g != "" {
		t.Errorf("EntryGlyph(plain root) = %q, want empty", g)
	}
}

// The grid's face comes from what its plugin DECLARED, never from a kind the
// client recognizes. fs and proc get the folder and the process faces because
// they declare them; a plugin that declares nothing — the gitlab shape — is
// owned content and takes the well, exactly as it did when the client
// switched on a source kind it had no declaration for. Without this the
// glyph arm is free to grow a kind switch back, which is the leak the
// declared facts replaced.
func TestGlyphForReadsDeclarationsOnly(t *testing.T) {
	plugins := []rpc.PluginInfo{
		{UUID: "ufs", Glyph: rpc.GlyphFolder, RootGridID: "ufs/1"},
		{UUID: "ugl", RootGridID: "ugl/1"},
		{UUID: "n1/conn", Glyph: rpc.GlyphFolder, MenuEntries: []rpc.MenuEntry{
			{GridID: "ufs/9", Glyph: rpc.GlyphTrash},
		}},
	}
	for _, tc := range []struct {
		name   string
		gridID string
		grid   *rpc.Grid
		want   string
	}{
		{"declared folder", "ufs/1", &rpc.Grid{ID: "ufs/1", Glyph: rpc.GlyphFolder, HostContent: true}, rpc.GlyphFolder},
		{"declared process", "up/1", &rpc.Grid{ID: "up/1", Glyph: rpc.GlyphProcess, HostContent: true}, rpc.GlyphProcess},
		{"declares nothing is owned content", "ugl/1", &rpc.Grid{ID: "ugl/1"}, rpc.GlyphWell},
		{"a root entry outranks the grid", "ufs/9", &rpc.Grid{ID: "ufs/9", Glyph: rpc.GlyphFolder}, rpc.GlyphTrash},
		{"mounted content wears the door", "n1/conn/x/1", &rpc.Grid{ID: "n1/conn/x/1", NodeNS: "n1/conn"}, rpc.GlyphFolder},
		{"unknown mount takes the globe", "n1/gone/x/1", &rpc.Grid{ID: "n1/gone/x/1", NodeNS: "n1/gone"}, ""},
		{"uncached falls back to the plugin row", "ufs/4", nil, rpc.GlyphFolder},
		{"uncached unknown namespace", "zzz/4", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := GlyphFor(tc.gridID, tc.grid, plugins); got != tc.want {
				t.Errorf("GlyphFor(%q) = %q, want %q", tc.gridID, got, tc.want)
			}
		})
	}
}
