package palette

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/door"
)

// names is the section as the user reads it: one label per swatch, in order.
func names(sw []door.Place) []string {
	out := make([]string, 0, len(sw))
	for _, s := range sw {
		out = append(out, s.Plugin.Label)
	}
	return out
}

// One swatch per declared doorway: a row that names a grid of its own, plus
// every menu entry it declares. A plugin names no grid of its own, so only
// its collections appear, and a plugin that declares nothing shows nothing.
func TestDoorwaysTable(t *testing.T) {
	entry := func(id, label, grid string) *gridwellv1.MenuEntry {
		return &gridwellv1.MenuEntry{Id: id, Label: label, GridId: grid}
	}
	cases := []struct {
		name string
		rows []*gridwellv1.PluginInfo
		want []string
	}{{
		name: "a node's home is a place: its own swatch, then its entries",
		rows: []*gridwellv1.PluginInfo{{Uuid: "n1", Label: "home", RootGridId: "n1/1",
			MenuEntries: []*gridwellv1.MenuEntry{entry("trash", "trash", "n1/9")}}},
		want: []string{"home", "home · trash"},
	}, {
		name: "a plugin with three collections is three swatches and no row",
		rows: []*gridwellv1.PluginInfo{{Uuid: "hey", Kind: "mail", Label: "hey", MenuEntries: []*gridwellv1.MenuEntry{
			entry("imbox", "Imbox", "hey/1"),
			entry("feed", "Feed", "hey/2"),
			entry("paper_trail", "Paper Trail", "hey/3"),
		}}},
		want: []string{"hey · Imbox", "hey · Feed", "hey · Paper Trail"},
	}, {
		name: "a single-collection plugin declares no entry label and reads as itself",
		rows: []*gridwellv1.PluginInfo{{Uuid: "fs", Kind: "fs", Label: "files",
			MenuEntries: []*gridwellv1.MenuEntry{entry(".", "", "fs/1")}}},
		want: []string{"files"},
	}, {
		name: "a plugin that declares nothing contributes nothing",
		rows: []*gridwellv1.PluginInfo{{Uuid: "fs", Kind: "fs", Label: "files"}},
		want: []string{},
	}, {
		name: "a failure is still a swatch, so it can be seen and asked about",
		rows: []*gridwellv1.PluginInfo{{Uuid: "fs", Kind: "fs", Label: "files",
			InfoError: "plugin not responding: boom"}},
		want: []string{"files"},
	}, {
		name: "a connection is a place, answered or not",
		rows: []*gridwellv1.PluginInfo{
			rpc.ConnectionRow("n1/rtb", "rtb", "n1/rtb/1", "", rpc.Framing{}),
			rpc.ConnectionRow("n1/far", "far", "", "", rpc.Framing{}),
		},
		want: []string{"rtb", "far"},
	}, {
		name: "an entry with no grid is not a doorway",
		rows: []*gridwellv1.PluginInfo{{Uuid: "hey", Label: "hey", MenuEntries: []*gridwellv1.MenuEntry{
			entry("imbox", "Imbox", "hey/1"), entry("feed", "Feed", ""),
		}}},
		want: []string{"hey · Imbox"},
	}, {
		name: "rows keep handshake order, each row's entries directly after it",
		rows: []*gridwellv1.PluginInfo{
			{Uuid: "n1", Label: "home", RootGridId: "n1/1",
				MenuEntries: []*gridwellv1.MenuEntry{entry("trash", "trash", "n1/9")}},
			{Uuid: "hey", Label: "hey", MenuEntries: []*gridwellv1.MenuEntry{entry("feed", "Feed", "hey/2")}},
			rpc.ConnectionRow("n1/rtb", "rtb", "n1/rtb/1", "", rpc.Framing{}),
		},
		want: []string{"home", "home · trash", "hey · Feed", "rtb"},
	}}
	for _, c := range cases {
		got := names(Doorways(c.rows))
		if len(got) != len(c.want) {
			t.Errorf("%s: swatches = %q, want %q", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: swatches = %q, want %q", c.name, got, c.want)
				break
			}
		}
	}
}

// Every swatch names a doorway, so a click has somewhere to go. The one
// exception is a row the menu shows in order to report that it is unhealthy.
func TestEverySwatchNamesADoorwayOrAFailure(t *testing.T) {
	rows := []*gridwellv1.PluginInfo{
		{Uuid: "n1", Label: "home", RootGridId: "n1/1",
			MenuEntries: []*gridwellv1.MenuEntry{{Id: "trash", Label: "trash", GridId: "n1/9"}}},
		{Uuid: "hey", Label: "hey", MenuEntries: []*gridwellv1.MenuEntry{{Id: "feed", Label: "Feed", GridId: "hey/2"}}},
	}
	for _, s := range Doorways(rows) {
		if s.Plugin.RootGridId == "" {
			t.Errorf("swatch %q names no grid to descend into", s.Plugin.Label)
		}
	}
}

// An entry's pseudo-row carries no entries of its own, so a second pass over
// them cannot show every collection twice.
func TestEntrySwatchDoesNotCarryTheRowsEntries(t *testing.T) {
	rows := []*gridwellv1.PluginInfo{{Uuid: "hey", Label: "hey", MenuEntries: []*gridwellv1.MenuEntry{
		{Id: "imbox", Label: "Imbox", GridId: "hey/1"},
		{Id: "feed", Label: "Feed", GridId: "hey/2"},
	}}}
	sw := Doorways(rows)
	if len(sw) != 2 {
		t.Fatalf("swatches = %q, want the two collections", names(sw))
	}
	for _, s := range sw {
		if len(s.Plugin.MenuEntries) != 0 {
			t.Errorf("%q carries %d entries of its own", s.Plugin.Label, len(s.Plugin.MenuEntries))
		}
		if s.Entry == nil {
			t.Errorf("%q must name the entry it came from", s.Plugin.Label)
		}
	}
}
