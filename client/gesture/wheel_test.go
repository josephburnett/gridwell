package gesture

import "testing"

func TestClassifyWheel(t *testing.T) {
	cases := []struct {
		name string
		in   WheelInput
		want WheelAction
	}{
		{"grid view zooms", WheelInput{}, WheelZoomPane},
		// The bar is under no pane, so a wheel there zooms the focused pane
		// about its centre, or nothing when that pane is inside a document.
		{"over the bar zooms the focused grid pane", WheelInput{OverBar: true}, WheelZoomFocused},
		{"over the bar, a focused content pane is left alone", WheelInput{OverBar: true, TextFocused: true}, WheelIgnore},
		{"over the bar, a well under nothing claims nothing", WheelInput{OverBar: true, OverEnterableWell: true}, WheelZoomFocused},
		{"grid view zooms even with live view flags", WheelInput{LiveURLView: true, InContentBox: true}, WheelZoomPane},
		// An enterable well under the cursor zooms its own preview, and
		// empty space zooms the pane.
		{"over an enterable well zooms the well", WheelInput{OverEnterableWell: true}, WheelZoomWell},
		// A well filling most of the view hides the outer context, so
		// zooming out goes to the pane while zooming in stays the
		// well.
		{"zoom OUT over a dominant well redirects to the pane",
			WheelInput{OverEnterableWell: true, ZoomOut: true, WellCoverage: 0.8}, WheelZoomPane},
		{"zoom OUT over a small well stays on the well",
			WheelInput{OverEnterableWell: true, ZoomOut: true, WellCoverage: 0.3}, WheelZoomWell},
		{"zoom OUT at exactly the threshold stays on the well",
			WheelInput{OverEnterableWell: true, ZoomOut: true, WellCoverage: WellZoomOutRedirect}, WheelZoomWell},
		{"zoom IN over a dominant well still zooms the well",
			WheelInput{OverEnterableWell: true, ZoomOut: false, WellCoverage: 1.0}, WheelZoomWell},
		{"a well under a content descent never claims the wheel", WheelInput{TextFocused: true, OverEnterableWell: true}, WheelIgnore},
		{"live url over content box swallows", WheelInput{TextFocused: true, URLDescent: true, LiveURLView: true, InContentBox: true}, WheelSwallow},
		{"live url outside content box ignored", WheelInput{TextFocused: true, URLDescent: true, LiveURLView: true}, WheelIgnore},
		{"frozen url descent ignored", WheelInput{TextFocused: true, URLDescent: true, InContentBox: true}, WheelIgnore},
		{"rendered doc scrolls", WheelInput{TextFocused: true, TextModeRendered: true}, WheelScrollDoc},
		{"rendered doc scrolls in content box", WheelInput{TextFocused: true, TextModeRendered: true, InContentBox: true}, WheelScrollDoc},
		{"text mode ignored (textarea owns scrolling)", WheelInput{TextFocused: true}, WheelIgnore},
		// A live url view over the box outranks the document scroll.
		{"live view outranks rendered scroll", WheelInput{TextFocused: true, URLDescent: true, LiveURLView: true, InContentBox: true, TextModeRendered: true}, WheelSwallow},
	}
	for _, c := range cases {
		if got := ClassifyWheel(c.in); got != c.want {
			t.Errorf("%s: ClassifyWheel = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRectCoverage(t *testing.T) {
	cases := []struct {
		name           string
		rx, ry, rw, rh float64
		bx, by, bw, bh float64
		want           float64
	}{
		{"rect fills the box exactly", 0, 0, 100, 100, 0, 0, 100, 100, 1},
		{"rect overflows the box on every side", -50, -50, 200, 200, 0, 0, 100, 100, 1},
		{"quarter coverage", 0, 0, 50, 50, 0, 0, 100, 100, 0.25},
		{"disjoint", 200, 200, 50, 50, 0, 0, 100, 100, 0},
		{"degenerate box", 0, 0, 50, 50, 0, 0, 0, 100, 0},
	}
	for _, c := range cases {
		if got := RectCoverage(c.rx, c.ry, c.rw, c.rh, c.bx, c.by, c.bw, c.bh); got != c.want {
			t.Errorf("%s: RectCoverage = %v, want %v", c.name, got, c.want)
		}
	}
}
