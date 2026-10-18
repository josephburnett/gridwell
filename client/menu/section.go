package menu

// What the + menu shows. The primitives are always there. The swatches above
// them are an open set, so that section folds behind a chevron. The fold lasts
// one menu opening and every opening starts folded; State owns the flag.

// Section counts the door swatches the node declares and the primitives the
// grid takes, zero on a read-only grid.
type Section struct {
	Plugins    int
	Primitives int
	Expanded   bool
}

type Chevron int

const (
	// ChevronNone is a popover with no toggle.
	ChevronNone Chevron = iota
	// ChevronUp is collapsed: click to open the section above.
	ChevronUp
	// ChevronDown is expanded: click to fold it back up.
	ChevronDown
)

// String lets a spec pin the chevron without reading pixels.
func (c Chevron) String() string {
	switch c {
	case ChevronUp:
		return "up"
	case ChevronDown:
		return "down"
	}
	return "none"
}

type Shown struct {
	Plugins bool
	Toggle  bool
	Chevron Chevron
}

// Show gives two states no toggle, there being nothing to fold: a node that
// declares no doors has no section, and a read-only grid offers no
// primitives, so folding would leave the popover empty.
func Show(s Section) Shown {
	if s.Plugins <= 0 {
		return Shown{}
	}
	if s.Primitives <= 0 {
		return Shown{Plugins: true}
	}
	if s.Expanded {
		return Shown{Plugins: true, Toggle: true, Chevron: ChevronDown}
	}
	return Shown{Toggle: true, Chevron: ChevronUp}
}
