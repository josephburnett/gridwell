package gesture

import (
	"testing"

	"github.com/josephburnett/gridwell/client/wsbar"
)

func TestClassifyWheel(t *testing.T) {
	cases := []struct {
		name string
		in   WheelInput
		want WheelAction
	}{
		// On a grid pane the plain wheel zooms the grid the pane shows wherever
		// the cursor is, and alt + wheel zooms a door's inside or nothing: a
		// tile that is no enterable door reads as the background. Neither
		// reads the wheel's direction.
		{"grid background zooms the pane", WheelInput{}, WheelZoomPane},
		{"over a door zooms the pane", WheelInput{OverEnterableDoor: true}, WheelZoomPane},
		{"alt over a door zooms the door", WheelInput{Alt: true, OverEnterableDoor: true}, WheelZoomDoor},
		{"alt over the background does nothing", WheelInput{Alt: true}, WheelIgnore},
		{"grid view zooms even with live view flags", WheelInput{LiveURLView: true, InContentBox: true}, WheelZoomPane},
		// The popover floats over panes and the bar alike, and takes the
		// wheel wherever it is, alt or not.
		{"over the menu scrolls its strip", WheelInput{OverMenu: true, OverEnterableDoor: true, Zone: wsbar.ZoneBar}, WheelScrollMenu},
		{"alt over the menu scrolls its strip", WheelInput{OverMenu: true, Alt: true}, WheelScrollMenu},
		// The bar is under no pane, so a wheel there zooms the focused pane
		// about its centre, or nothing when that pane is inside a document or
		// alt is held, since alt zooms no grid.
		{"over the bar zooms the focused grid pane", WheelInput{Zone: wsbar.ZoneBar}, WheelZoomFocused},
		{"alt over the bar does nothing", WheelInput{Zone: wsbar.ZoneBar, Alt: true}, WheelIgnore},
		{"over the bar, a focused content pane is left alone", WheelInput{Zone: wsbar.ZoneBar, TextFocused: true}, WheelIgnore},
		{"over the bar, a door under nothing claims nothing", WheelInput{Zone: wsbar.ZoneBar, OverEnterableDoor: true}, WheelZoomFocused},
		{"alt over the bar, a door under nothing claims nothing", WheelInput{Zone: wsbar.ZoneBar, OverEnterableDoor: true, Alt: true}, WheelIgnore},
		// Beside the bar there is no pane under the wheel, and the band is
		// no pane's.
		{"beside the bar does nothing", WheelInput{Zone: wsbar.ZoneBand}, WheelIgnore},
		{"alt beside the bar does nothing", WheelInput{Zone: wsbar.ZoneBand, Alt: true}, WheelIgnore},
		{"a door under a content descent never claims the wheel", WheelInput{TextFocused: true, OverEnterableDoor: true, Alt: true}, WheelIgnore},
		{"live url over content box swallows", WheelInput{TextFocused: true, URLDescent: true, LiveURLView: true, InContentBox: true}, WheelSwallow},
		{"alt over a live url's content box swallows", WheelInput{TextFocused: true, URLDescent: true, LiveURLView: true, InContentBox: true, Alt: true}, WheelSwallow},
		{"live url outside content box ignored", WheelInput{TextFocused: true, URLDescent: true, LiveURLView: true}, WheelIgnore},
		{"frozen url descent ignored", WheelInput{TextFocused: true, URLDescent: true, InContentBox: true}, WheelIgnore},
		{"rendered doc scrolls", WheelInput{TextFocused: true, TextModeRendered: true}, WheelScrollDoc},
		{"alt over a rendered doc scrolls it", WheelInput{TextFocused: true, TextModeRendered: true, Alt: true}, WheelScrollDoc},
		{"rendered doc scrolls in content box", WheelInput{TextFocused: true, TextModeRendered: true, InContentBox: true}, WheelScrollDoc},
		{"text mode ignored (textarea owns scrolling)", WheelInput{TextFocused: true}, WheelIgnore},
		{"alt in text mode ignored", WheelInput{TextFocused: true, Alt: true}, WheelIgnore},
		// A live url view over the box outranks the document scroll.
		{"live view outranks rendered scroll", WheelInput{TextFocused: true, URLDescent: true, LiveURLView: true, InContentBox: true, TextModeRendered: true}, WheelSwallow},
	}
	for _, c := range cases {
		if got := ClassifyWheel(c.in); got != c.want {
			t.Errorf("%s: ClassifyWheel = %v, want %v", c.name, got, c.want)
		}
	}
}
