package gesture

import "testing"

func TestClassifyWheel(t *testing.T) {
	cases := []struct {
		name string
		in   WheelInput
		want WheelAction
	}{
		{"grid view zooms", WheelInput{}, WheelZoomPane},
		{"grid view zooms even with live view flags", WheelInput{LiveURLView: true, InContentBox: true}, WheelZoomPane},
		{"live url over content box swallows", WheelInput{TextFocused: true, URLDescent: true, LiveURLView: true, InContentBox: true}, WheelSwallow},
		{"live url outside content box ignored", WheelInput{TextFocused: true, URLDescent: true, LiveURLView: true}, WheelIgnore},
		{"frozen url descent ignored", WheelInput{TextFocused: true, URLDescent: true, InContentBox: true}, WheelIgnore},
		{"rendered doc scrolls", WheelInput{TextFocused: true, TextModeRendered: true}, WheelScrollDoc},
		{"rendered doc scrolls in content box", WheelInput{TextFocused: true, TextModeRendered: true, InContentBox: true}, WheelScrollDoc},
		{"text mode ignored (textarea owns scrolling)", WheelInput{TextFocused: true}, WheelIgnore},
		// A live url descent that somehow reports rendered mode still swallows
		// over the box — the native view outranks the doc scroll.
		{"live view outranks rendered scroll", WheelInput{TextFocused: true, URLDescent: true, LiveURLView: true, InContentBox: true, TextModeRendered: true}, WheelSwallow},
	}
	for _, c := range cases {
		if got := ClassifyWheel(c.in); got != c.want {
			t.Errorf("%s: ClassifyWheel = %v, want %v", c.name, got, c.want)
		}
	}
}
