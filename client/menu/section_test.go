package menu

import "testing"

// TestShow pins every state of the + menu's top section, including that it
// starts collapsed and that the two no-toggle states are the ones where
// folding would leave nothing.
func TestShow(t *testing.T) {
	tests := []struct {
		name string
		in   Section
		want Shown
	}{{
		name: "collapsed by default: primitives only, chevron points up",
		in:   Section{Swatches: 4, Primitives: 5, Expanded: false},
		want: Shown{Swatches: false, Toggle: true, Chevron: ChevronUp},
	}, {
		name: "expanded: the section shows, chevron folds it back",
		in:   Section{Swatches: 4, Primitives: 5, Expanded: true},
		want: Shown{Swatches: true, Toggle: true, Chevron: ChevronDown},
	}, {
		name: "one entry still folds: the section is not sized-based",
		in:   Section{Swatches: 1, Primitives: 5},
		want: Shown{Swatches: false, Toggle: true, Chevron: ChevronUp},
	}, {
		name: "no sources declared: no section and no control",
		in:   Section{Swatches: 0, Primitives: 5},
		want: Shown{},
	}, {
		name: "no sources, and expanded anyway: still nothing to show",
		in:   Section{Swatches: 0, Primitives: 5, Expanded: true},
		want: Shown{},
	}, {
		name: "read-only grid: the section IS the menu, always shown",
		in:   Section{Swatches: 3, Primitives: 0},
		want: Shown{Swatches: true},
	}, {
		name: "read-only grid, expanded: the same, no control appears",
		in:   Section{Swatches: 3, Primitives: 0, Expanded: true},
		want: Shown{Swatches: true},
	}, {
		name: "nothing at all",
		in:   Section{},
		want: Shown{},
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Show(tc.in); got != tc.want {
				t.Errorf("Show(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestChevronNames pins the strings a spec asserts instead of an integer.
func TestChevronNames(t *testing.T) {
	for c, want := range map[Chevron]string{
		ChevronNone: "none",
		ChevronUp:   "up",
		ChevronDown: "down",
	} {
		if got := c.String(); got != want {
			t.Errorf("Chevron(%d).String() = %q, want %q", c, got, want)
		}
	}
}
