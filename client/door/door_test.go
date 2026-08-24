package door

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

var plugins = []rpc.PluginInfo{
	{UUID: "loc", Label: "local", Glyph: "well", RootGridID: "loc/1",
		MenuEntries: []rpc.MenuEntry{{ID: "trash", Label: "trash", Glyph: "trash", GridID: "loc/9"}}},
	// A connection MENU ROW (v2 #269): a chained uuid whose root is the
	// remote home — the replacement for the retired instance-picker arm.
	{UUID: "sshc/ns1", Label: "rtb", RootGridID: "sshc/ns1/rp1/root7"},
}

// The tile actually descended through wins over every declaration — an
// adopted plugin well carries the USER's name for the place.
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

// A menu-row descent has no parent-grid well; the connection ROW is the
// door — its label, declaration-owned (renaming is a yaml edit).
func TestFindResolvesConnectionRows(t *testing.T) {
	got, kind := Find("sshc/ns1/rp1/root7", nil, plugins)
	if kind != Root || got.AltText != "rtb" {
		t.Fatalf("door = %+v (%v), want the rtb connection row", got, kind)
	}
}

// A menu-swatch descent into a ROOT entry resolves to the entry's pseudo
// swatch: its label and glyph, declaration-owned (not renamable).
func TestFindResolvesRootEntries(t *testing.T) {
	got, kind := Find("loc/9", nil, plugins)
	if kind != Entry || got.AltText != "trash" || got.ChildGridID != "loc/9" {
		t.Fatalf("door = %+v (%v), want the trash entry swatch", got, kind)
	}
}

func TestFindResolvesPluginRoots(t *testing.T) {
	got, kind := Find("loc/1", nil, plugins)
	if kind != Root || got.AltText != "local" {
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
