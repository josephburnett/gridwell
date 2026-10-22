// Package tilepreview owns what a tile's preview says about its row from outside:
// whether it draws in the outside-Gridwell treatment, what its banner reads,
// and which hue that banner wears. One classification each, read by every
// painter of a row, so no two can answer differently. It is js-free; the shim
// maps a hue to a color.
package tilepreview

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Outside reports the outside-Gridwell treatment. A grid that declares
// host_content makes every row in it host state, whatever the row is.
func Outside(t *gridwellv1.Tile, parentHostContent bool) bool {
	return parentHostContent || rpc.IsDoorToOtherNamespace(t) || rpc.KindOf(t.Kind).Tmux
}

// Hue is the family a banner label echoes: the tile's own outline, so label
// and border read as one.
type Hue int

const (
	// HueMuted is an unknown kind, which has no outline of its own.
	HueMuted Hue = iota
	HueShell
	// HueDoor is every door, a door to another namespace included: a
	// cross-plugin door is dashed, never recolored.
	HueDoor
	// HueHost is host content in the text family: the plugin border.
	HueHost
	HueURL
	HueText
)

// BannerHue answers in the outline's priority, so a door keeps its blue
// whichever grid it sits in. outside is Outside's answer for the same row.
func BannerHue(t *gridwellv1.Tile, outside bool) Hue {
	switch k := rpc.KindOf(t.Kind); {
	case k.Live == rpc.DescentShell:
		return HueShell
	case rpc.IsDoorToOtherNamespace(t):
		return HueDoor
	case outside:
		return HueHost
	case k.Descends == rpc.DescendGrid:
		return HueDoor
	case k.Live == rpc.DescentURL:
		return HueURL
	case k.Content == rpc.ContentBody:
		return HueText
	}
	return HueMuted
}
