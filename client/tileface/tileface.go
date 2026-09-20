// Package tileface owns what a tile's face says about its row from outside:
// whether it draws in the outside-Gridwell treatment, and which hue its banner
// wears. One classification each, read by every painter of a row, so no two
// can answer differently. It is js-free; the shim maps a hue to a color.
package tileface

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Outside reports the outside-Gridwell treatment. A grid that declares
// host_content makes every row in it host state, whatever the row is.
func Outside(t *gridwellv1.Tile, parentHostContent bool) bool {
	return parentHostContent || rpc.IsExitWell(t) || t.Kind == rpc.KindShell
}

// Hue is the family a banner label echoes: the tile's own outline, so label
// and border read as one.
type Hue int

const (
	// HueMuted is an unknown kind, which has no outline of its own.
	HueMuted Hue = iota
	HueShell
	// HueWell is every well, exit wells included: a cross-plugin well is
	// dashed, never recolored.
	HueWell
	// HueHost is host content in the text family: the plugin border.
	HueHost
	HueURL
	HueText
)

// BannerHue answers in the outline's priority, so a well keeps its blue
// whichever grid it sits in. outside is Outside's answer for the same row.
func BannerHue(t *gridwellv1.Tile, outside bool) Hue {
	switch {
	case t.Kind == rpc.KindShell:
		return HueShell
	case rpc.IsExitWell(t):
		return HueWell
	case outside:
		return HueHost
	}
	switch t.Kind {
	case rpc.KindWell:
		return HueWell
	case rpc.KindURL:
		return HueURL
	case rpc.KindText:
		return HueText
	}
	return HueMuted
}
