package gesture

type WheelAction int

const (
	// WheelZoomPane is zoomtrans.WheelZoom, cursor-anchored.
	WheelZoomPane WheelAction = iota
	// WheelZoomDoor zooms through the hovered door's stored preview framing,
	// not the grid the pane shows.
	WheelZoomDoor
	WheelScrollDoc
	WheelSwallow
	// WheelIgnore leaves the canvas alone; a textarea overlay scrolls itself.
	WheelIgnore
	// WheelZoomFocused zooms the focused pane about its own centre: the wheel
	// is over the bar, which no pane is under, so it is the escape hatch for a
	// grid tiled wall to wall with doors.
	WheelZoomFocused
)

// WheelInput is the state ClassifyWheel decides on; the caller resolves the
// impure facts.
type WheelInput struct {
	// OverBar is a wheel in the bar's band, where there is no pane under the
	// cursor; every other field then describes the FOCUSED pane.
	OverBar bool
	// TextFocused means the pane is descended into a content tile.
	TextFocused      bool
	URLDescent       bool
	LiveURLView      bool
	InContentBox     bool
	TextModeRendered bool
	// OverEnterableDoor is the same predicate a drop's PromoteToDoor uses.
	OverEnterableDoor bool
	// ZoomOut is deltaY > 0, zoomtrans.WheelZoom's convention.
	ZoomOut bool
	// DoorCoverage is RectCoverage of the hovered door over the content box.
	// It means nothing without OverEnterableDoor.
	DoorCoverage float64
}

// DoorZoomOutRedirect is the coverage past which zooming out over a door zooms
// the pane instead: a door filling the view leaves no outer context, so
// wheeling out asks to back out. Zooming in stays with the door always.
const DoorZoomOutRedirect = 0.5

// RectCoverage is the share of the box the rect covers, 0 to 1.
func RectCoverage(rx, ry, rw, rh, bx, by, bw, bh float64) float64 {
	if bw <= 0 || bh <= 0 {
		return 0
	}
	ix := max(rx, bx)
	iy := max(ry, by)
	ix2 := min(rx+rw, bx+bw)
	iy2 := min(ry+rh, by+bh)
	if ix2 <= ix || iy2 <= iy {
		return 0
	}
	return ((ix2 - ix) * (iy2 - iy)) / (bw * bh)
}

// ClassifyWheel routes a wheel event. Inside a descent a live url view over
// the content box swallows strays because the view scrolls itself.
func ClassifyWheel(in WheelInput) WheelAction {
	if in.OverBar {
		if in.TextFocused {
			return WheelIgnore
		}
		return WheelZoomFocused
	}
	if !in.TextFocused {
		if in.OverEnterableDoor {
			if in.ZoomOut && in.DoorCoverage > DoorZoomOutRedirect {
				return WheelZoomPane
			}
			return WheelZoomDoor
		}
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
