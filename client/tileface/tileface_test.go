package tileface

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

func well(child string) *gridwellv1.Tile {
	return &gridwellv1.Tile{Kind: rpc.KindWell, GridId: "n1/1", ChildGridId: child}
}

func TestOutside(t *testing.T) {
	cases := []struct {
		name   string
		tile   *gridwellv1.Tile
		inHost bool
		want   bool
	}{
		{"a text tile in a Gridwell grid", &gridwellv1.Tile{Kind: rpc.KindText}, false, false},
		{"a text tile in a host-content grid", &gridwellv1.Tile{Kind: rpc.KindText}, true, true},
		{"a shell anywhere", &gridwellv1.Tile{Kind: rpc.KindShell}, false, true},
		{"an interior well", well("n1/2"), false, false},
		{"an exit well into another namespace", well("p9/2"), false, true},
		{"a url tile", &gridwellv1.Tile{Kind: rpc.KindURL}, false, false},
		{"a pane tile", &gridwellv1.Tile{Kind: rpc.KindPane}, false, false},
	}
	for _, c := range cases {
		if got := Outside(c.tile, c.inHost); got != c.want {
			t.Errorf("%s: Outside = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBannerHue(t *testing.T) {
	cases := []struct {
		name    string
		tile    *gridwellv1.Tile
		outside bool
		want    Hue
	}{
		{"shell wins over outside", &gridwellv1.Tile{Kind: rpc.KindShell}, true, HueShell},
		{"exit well stays a well", well("p9/2"), true, HueWell},
		{"interior well", well("n1/2"), false, HueWell},
		{"host text is the plugin hue", &gridwellv1.Tile{Kind: rpc.KindText}, true, HueHost},
		{"host url is the plugin hue too", &gridwellv1.Tile{Kind: rpc.KindURL}, true, HueHost},
		{"text", &gridwellv1.Tile{Kind: rpc.KindText}, false, HueText},
		{"url", &gridwellv1.Tile{Kind: rpc.KindURL}, false, HueURL},
		{"pane has no hue of its own", &gridwellv1.Tile{Kind: rpc.KindPane}, false, HueMuted},
	}
	for _, c := range cases {
		if got := BannerHue(c.tile, c.outside); got != c.want {
			t.Errorf("%s: BannerHue = %v, want %v", c.name, got, c.want)
		}
	}
}

// The hue a banner wears is the same question Outside answers, asked of the
// same row: a shell is outside and warm, a Gridwell text tile neither.
func TestBannerHueAgreesWithOutside(t *testing.T) {
	for _, tile := range []*gridwellv1.Tile{
		{Kind: rpc.KindShell}, {Kind: rpc.KindText}, {Kind: rpc.KindURL}, well("n1/2"), well("p9/2"),
	} {
		hue := BannerHue(tile, Outside(tile, false))
		if hue == HueHost {
			t.Errorf("%v: a Gridwell-owned row wears the host hue", tile)
		}
	}
}
