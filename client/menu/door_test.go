package menu

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

// One swatch per declared door: a row that names a grid of its own, plus
// every declared grid it declares. A plugin names no grid of its own, so only
// its grids appear, and a plugin that declares nothing shows nothing.
func TestDoorsTable(t *testing.T) {
	entry := func(id, label, grid string) *gridwellv1.PluginGrid {
		return &gridwellv1.PluginGrid{Id: id, Label: label, GridId: grid}
	}
	cases := []struct {
		name string
		rows []*gridwellv1.PluginInfo
		want []string
	}{{
		name: "a node's home is a place: its own swatch, then its entries",
		rows: []*gridwellv1.PluginInfo{{Uuid: "n1", Label: "home", RootGridId: "n1/1",
			PluginGrids: []*gridwellv1.PluginGrid{entry("trash", "trash", "n1/9")}}},
		want: []string{"home", "trash (home)"},
	}, {
		name: "a plugin with three grids is three swatches and no row",
		rows: []*gridwellv1.PluginInfo{{Uuid: "hey", Kind: "mail", Label: "hey", PluginGrids: []*gridwellv1.PluginGrid{
			entry("imbox", "Imbox", "hey/1"),
			entry("feed", "Feed", "hey/2"),
			entry("paper_trail", "Paper Trail", "hey/3"),
		}}},
		want: []string{"Imbox (hey)", "Feed (hey)", "Paper Trail (hey)"},
	}, {
		name: "a single-grid plugin declares no entry label and reads as itself",
		rows: []*gridwellv1.PluginInfo{{Uuid: "fs", Kind: "fs", Label: "files",
			PluginGrids: []*gridwellv1.PluginGrid{entry(".", "", "fs/1")}}},
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
			rpc.ConnectionRow("n1/rtb", "rtb", "n1/rtb/1", "", rpc.View{}),
			rpc.ConnectionRow("n1/far", "far", "", "", rpc.View{}),
		},
		want: []string{"rtb", "far"},
	}, {
		name: "an entry with no grid is not a door",
		rows: []*gridwellv1.PluginInfo{{Uuid: "hey", Label: "hey", PluginGrids: []*gridwellv1.PluginGrid{
			entry("imbox", "Imbox", "hey/1"), entry("feed", "Feed", ""),
		}}},
		want: []string{"Imbox (hey)"},
	}, {
		name: "rows keep handshake order, each row's entries directly after it",
		rows: []*gridwellv1.PluginInfo{
			{Uuid: "n1", Label: "home", RootGridId: "n1/1",
				PluginGrids: []*gridwellv1.PluginGrid{entry("trash", "trash", "n1/9")}},
			{Uuid: "hey", Label: "hey", PluginGrids: []*gridwellv1.PluginGrid{entry("feed", "Feed", "hey/2")}},
			rpc.ConnectionRow("n1/rtb", "rtb", "n1/rtb/1", "", rpc.View{}),
		},
		want: []string{"home", "trash (home)", "Feed (hey)", "rtb"},
	}}
	for _, c := range cases {
		got := names(Doors(c.rows))
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

// Every swatch names a door, so a click has somewhere to go. The one
// exception is a row the menu shows in order to report that it is unhealthy.
func TestEverySwatchNamesADoorOrAFailure(t *testing.T) {
	rows := []*gridwellv1.PluginInfo{
		{Uuid: "n1", Label: "home", RootGridId: "n1/1",
			PluginGrids: []*gridwellv1.PluginGrid{{Id: "trash", Label: "trash", GridId: "n1/9"}}},
		{Uuid: "hey", Label: "hey", PluginGrids: []*gridwellv1.PluginGrid{{Id: "feed", Label: "Feed", GridId: "hey/2"}}},
	}
	for _, s := range Doors(rows) {
		if s.Plugin.RootGridId == "" {
			t.Errorf("swatch %q names no grid to descend into", s.Plugin.Label)
		}
	}
}

// An entry's pseudo-row carries no entries of its own, so a second pass over
// them cannot show every grid twice.
func TestEntrySwatchDoesNotCarryTheRowsEntries(t *testing.T) {
	rows := []*gridwellv1.PluginInfo{{Uuid: "hey", Label: "hey", PluginGrids: []*gridwellv1.PluginGrid{
		{Id: "imbox", Label: "Imbox", GridId: "hey/1"},
		{Id: "feed", Label: "Feed", GridId: "hey/2"},
	}}}
	sw := Doors(rows)
	if len(sw) != 2 {
		t.Fatalf("swatches = %q, want the two grids", names(sw))
	}
	for _, s := range sw {
		if len(s.Plugin.PluginGrids) != 0 {
			t.Errorf("%q carries %d entries of its own", s.Plugin.Label, len(s.Plugin.PluginGrids))
		}
		if s.Grid == nil {
			t.Errorf("%q must name the entry it came from", s.Plugin.Label)
		}
	}
}
