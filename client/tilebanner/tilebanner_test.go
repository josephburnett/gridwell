package tilebanner

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// The status is the plugin's word about the tile, never a second name: it can
// only follow a name, so a plugin cannot rename a tile through it.
func TestRuns(t *testing.T) {
	for _, tc := range []struct {
		name         string
		tile         *gridwellv1.Tile
		label, statu string
	}{
		{"a named tile with no status",
			&gridwellv1.Tile{Kind: rpc.KindText, AltText: "notes.md"}, "notes.md", ""},
		{"the plugin's state rides after the name",
			&gridwellv1.Tile{Kind: rpc.KindText, AltText: "Ada: lunch?", StatusDetail: "unread"},
			"Ada: lunch?", "unread"},
		{"an unnamed tile has no banner to hang a status on",
			&gridwellv1.Tile{Kind: rpc.KindWell, StatusDetail: "done"}, "", ""},
		{"a plain well has neither",
			&gridwellv1.Tile{Kind: rpc.KindWell}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			label, status := Runs(tc.tile)
			if label != tc.label || status != tc.statu {
				t.Errorf("Runs = (%q, %q), want (%q, %q)", label, status, tc.label, tc.statu)
			}
		})
	}
}
