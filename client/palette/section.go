package palette

// WHAT THE + MENU SHOWS. The primitives are the pane's own tiles and are
// always there. The plugin and connection swatches above them are an open set
// — a node declares as many as it likes, and one plugin can declare several
// menu entries — so that section is folded away behind a chevron and opened
// on demand. The fold is per menu opening: the menu is transient, and every
// opening starts collapsed (client/menu owns the flag and clears it on Open).
//
// This is the composition decision, kept out of the shim so the states are
// table-tested: what a given set of entries shows in a given fold state, and
// which way the chevron points. The geometry of the rows it names is Layout;
// the drawing is the renderer.

// Section is the input: how many plugin and connection swatches the pane's
// node declares, how many primitives the pane's grid takes (zero on a
// read-only grid), and whether the user has opened the section on this menu
// opening.
type Section struct {
	Plugins    int
	Primitives int
	Expanded   bool
}

// Chevron is the disclosure control's face, and ChevronNone means there is no
// control at all.
type Chevron int

const (
	// ChevronNone: no toggle in the popover.
	ChevronNone Chevron = iota
	// ChevronUp: collapsed — click to open the section above.
	ChevronUp
	// ChevronDown: expanded — click to fold it back up.
	ChevronDown
)

// String names the chevron for the test hook, so a spec can pin the face
// without reading pixels.
func (c Chevron) String() string {
	switch c {
	case ChevronUp:
		return "up"
	case ChevronDown:
		return "down"
	}
	return "none"
}

// Shown is the decision: whether the plugin section's swatches are in the
// popover, whether the toggle strip is, and which way it points.
type Shown struct {
	Plugins bool
	Toggle  bool
	Chevron Chevron
}

// Show decides what one menu opening displays.
//
// Two states have no toggle, and both are "there is nothing to fold": a node
// with no plugins or connections declares no section, and a read-only grid
// offers no primitives, so folding the section would leave an empty popover —
// there the section is simply the menu, always shown.
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
