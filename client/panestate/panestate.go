// Package panestate holds the per-pane, session-local client state for one pane:
// the current selection, the saved-ascent stack, the rendered-mode caret + dirty
// flag, and the frozen-URL pan offset. None of it is persisted (the server owns
// durable state); it is the plain-data half of a pane's client-only state.
//
// It exists as its own package so this data and its small amount of logic (the
// ascent stack and the caret's "no caret" sentinel) are unit-tested as plain Go,
// instead of living as a sprawl of parallel maps on the wasm App god-object where
// nothing about them can be tested. The wasm side embeds State in a per-pane
// struct that adds the native (js-coupled) live URL/shell handles; this package
// stays js-free.
package panestate

// Saved is one entry on the ascent stack: the parent viewport (Cx/Cy/Zoom) saved
// just before a descent, plus — when the descent originated inside a text tile —
// the text-descent context to reinstall on the matching ascent so a single
// ascent lands back in the doc. Anchor/Path are set only for an embed descent
// that re-anchored the pane onto another grid (a cross-grid/plugin follow).
type Saved struct {
	Cx          float64 `json:"cx"`
	Cy          float64 `json:"cy"`
	Zoom        float64 `json:"zoom"`
	TextFocus   string  `json:"text_focus,omitempty"`
	TextMode    string  `json:"text_mode,omitempty"`
	TextScrollX float64 `json:"text_scroll_x,omitempty"`
	TextScrollY float64 `json:"text_scroll_y,omitempty"`
	Anchor      string  `json:"anchor,omitempty"`
	Path        []string `json:"path,omitempty"`
}

// noCaret is the caret value meaning "this pane has no rendered-mode caret"
// (never clicked into editable rendered mode). It replaces the old "absent from
// the map" sentinel, which made the state easy to leave dangling.
const noCaret = -1

// State is the plain-data per-pane client state. Construct with New so the caret
// sentinel is set; the zero value would read as a caret at offset 0.
type State struct {
	// Selected is the selected tile id in this pane ("" = nothing selected).
	Selected string
	// Dirty marks a rendered-mode body with unsaved edits, so a quick ascent
	// (within the save debounce) still flushes it.
	Dirty bool
	// PanX/PanY are the pan offset for a frozen-URL descent in this pane.
	PanX, PanY float64

	ascent []Saved
	caret  int
}

// New returns an empty pane state with no caret.
func New() State { return State{caret: noCaret} }

// PushAscent saves a parent viewport on this pane's ascent stack.
func (s *State) PushAscent(v Saved) { s.ascent = append(s.ascent, v) }

// PopAscent removes and returns the most recent saved viewport, or nil when the
// stack is empty. The returned pointer is a copy — the entry is already off the
// stack.
func (s *State) PopAscent() *Saved {
	if len(s.ascent) == 0 {
		return nil
	}
	top := s.ascent[len(s.ascent)-1]
	s.ascent = s.ascent[:len(s.ascent)-1]
	return &top
}

// PeekAscent returns a mutable pointer to the top of the ascent stack (so a
// descent can re-anchor the saved state in place), or nil when the stack is
// empty. Valid only until the next push/pop.
func (s *State) PeekAscent() *Saved {
	if len(s.ascent) == 0 {
		return nil
	}
	return &s.ascent[len(s.ascent)-1]
}

// AscentDepth is the number of saved levels (matches the pane's descent depth).
func (s *State) AscentDepth() int { return len(s.ascent) }

// Caret returns the rendered-mode caret byte offset and whether one is set.
func (s *State) Caret() (int, bool) {
	if s.caret == noCaret {
		return 0, false
	}
	return s.caret, true
}

// SetCaret sets the rendered-mode caret to a source byte offset.
func (s *State) SetCaret(off int) { s.caret = off }

// ClearCaret removes the caret ("no caret in this pane").
func (s *State) ClearCaret() { s.caret = noCaret }
