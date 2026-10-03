package tileface

import (
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// The status is the plugin's mark on the tile, never a second name: it can
// only ride a name, and it leads, so the right-edge clip takes the name's tail
// first.
func TestBannerText(t *testing.T) {
	long := strings.Repeat("a very long subject line ", 8)
	for _, tc := range []struct {
		name string
		tile *gridwellv1.Tile
		want string
	}{
		{"an empty status draws the name only",
			&gridwellv1.Tile{Kind: rpc.KindText, AltText: "notes.md"}, "notes.md"},
		{"the plugin's emoji leads the name",
			&gridwellv1.Tile{Kind: rpc.KindText, AltText: "Ada: lunch?", StatusDetail: "●"}, "● Ada: lunch?"},
		{"a long name keeps the emoji where no clip reaches",
			&gridwellv1.Tile{Kind: rpc.KindText, AltText: long, StatusDetail: "✅"}, "✅ " + long},
		{"an unnamed tile has no banner to hang a status on",
			&gridwellv1.Tile{Kind: rpc.KindWell, StatusDetail: "✅"}, ""},
		{"a plain well has neither",
			&gridwellv1.Tile{Kind: rpc.KindWell}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := BannerText(tc.tile); got != tc.want {
				t.Errorf("BannerText = %q, want %q", got, tc.want)
			}
		})
	}
}
