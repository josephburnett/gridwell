package gesture

import "github.com/josephburnett/gridwell/client/wsbar"

type WheelAction int

const (
	// WheelZoomPane is zoomtrans.WheelZoom, cursor-anchored.
	WheelZoomPane WheelAction = iota
	// WheelZoomDoor zooms the grid inside the hovered door, its framing on the
	// door's row, not the grid the pane shows.
	WheelZoomDoor
	WheelScrollDoc
	WheelSwallow
	// WheelIgnore leaves the canvas alone; a textarea overlay scrolls itself.
	WheelIgnore
	// WheelZoomFocused zooms the focused pane about its own centre: the wheel
	// is over the bar, which no pane is under, and alt is not held.
	WheelZoomFocused
	// WheelScrollMenu scrolls the open + menu's strip: the popover floats
	// over the canvas, so the wheel there is the menu's.
	WheelScrollMenu
)

// WheelInput is the state ClassifyWheel decides on; the caller resolves the
// impure facts.
type WheelInput struct {
	// OverMenu is the wheel over the open + menu's popover.
	OverMenu bool
	// Zone is where the wheel is in the bar's band, which no pane is under;
	// on the bar every other field describes the FOCUSED pane.
	Zone wsbar.Zone
	// TextFocused means the pane is descended into a content tile.
	TextFocused      bool
	URLDescent       bool
	LiveURLView      bool
	InContentBox     bool
	TextModeRendered bool
	// OverEnterableDoor is the same predicate a drop's DropsIntoDoor uses.
	OverEnterableDoor bool
	// Alt is the alt key (option on macOS) held, which makes the wheel the
	// door's zoom and only that: it zooms no grid, over the bar or a pane.
	Alt bool
}

// ClassifyWheel routes a wheel event. On a grid pane the wheel zooms the grid
// the pane shows, and alt + wheel zooms the door under the cursor or nothing,
// never a grid; over the bar too it zooms nothing. Inside a descent a live
// url view over the content box swallows strays because the view scrolls itself.
func ClassifyWheel(in WheelInput) WheelAction {
	switch {
	case in.OverMenu:
		return WheelScrollMenu
	case in.Zone == wsbar.ZoneBand || in.Zone == wsbar.ZoneBar && (in.TextFocused || in.Alt):
		return WheelIgnore
	case in.Zone == wsbar.ZoneBar:
		return WheelZoomFocused
	case in.TextFocused:
	case in.Alt && in.OverEnterableDoor:
		return WheelZoomDoor
	case in.Alt:
		return WheelIgnore
	default:
		return WheelZoomPane
	}
	if in.URLDescent && in.LiveURLView && in.InContentBox {
		return WheelSwallow
	}
	if in.TextModeRendered {
		return WheelScrollDoc
	}
	return WheelIgnore
}
