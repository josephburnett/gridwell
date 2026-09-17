// Package tileface owns what a tile's face says about its row from outside:
// whether it draws in the outside-Gridwell treatment, and which hue its banner
// wears. Both are one classification each, read by the grid renderer, the
// child preview, the palette swatch and the drag ghost, so no painter can
// answer differently from another. It is js-free; the shim maps a hue to a
// color and paints.
package tileface

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Outside reports the outside-Gridwell treatment: a grid that declares
// host_content, so every row in it is host state; an exit well, wherever it
// sits; or a shell tile.
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

// BannerHue answers in the outline's priority: a shell's warmth first, then a
// well's blue whichever grid it sits in, then the host treatment, then the
// kind. outside is Outside's answer for the same row.
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
