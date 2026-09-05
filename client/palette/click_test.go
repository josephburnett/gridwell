package palette

import "testing"

// The table's whole job is the default row: a swatch with nothing to do on a
// click answers ClickNothing, so the click stops at the palette instead of
// reaching the canvas behind the popover, where it would descend into or
// select whatever tile sits at the swatch's coordinates.
func TestClickOn(t *testing.T) {
	cases := []struct {
		name string
		in   Swatch
		want ClickTarget
	}{
		{"a plugin row enters it", Swatch{IsPlugin: true}, ClickEnter},
		{"the promote crumb is where you already are", Swatch{Promote: true}, ClickHere},
		{"a primitive that declares a visit opens it", Swatch{Visits: true}, ClickVisit},
		{"a primitive that declares none does nothing", Swatch{}, ClickNothing},
		// Precedence: a plugin row carries the zero primitive kind, and the
		// promote crumb is spelled as a url template, so identity beats kind.
		{"a plugin row beats its zero-value primitive",
			Swatch{IsPlugin: true, Visits: true}, ClickEnter},
		{"the promote crumb beats the url primitive it is spelled as",
			Swatch{Promote: true, Visits: true}, ClickHere},
	}
	for _, c := range cases {
		if got := ClickOn(c.in); got != c.want {
			t.Errorf("%s: ClickOn(%+v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}
