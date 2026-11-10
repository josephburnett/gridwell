package menu

import "testing"

// A swatch with nothing to do on a click answers ClickNothing, so the click
// stops at the menu instead of reaching the canvas behind the popover.
func TestClickOn(t *testing.T) {
	cases := []struct {
		name string
		in   Swatch
		want ClickTarget
	}{
		{"a source row enters it", Swatch{IsSwatch: true}, ClickEnter},
		{"a primitive that declares a visit opens it", Swatch{Visits: true}, ClickVisit},
		{"a primitive that declares none does nothing", Swatch{}, ClickNothing},
		// A source row carries the zero primitive kind, so identity beats kind.
		{"a source row beats its zero-value primitive",
			Swatch{IsSwatch: true, Visits: true}, ClickEnter},
		{"a template visits a copy of itself", Swatch{Template: true}, ClickCopy},
		{"the copy swatch is only dragged", Swatch{CopySwatch: true}, ClickNothing},
	}
	for _, c := range cases {
		if got := ClickOn(c.in); got != c.want {
			t.Errorf("%s: ClickOn(%+v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}
