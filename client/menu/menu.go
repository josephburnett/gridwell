// Package menu owns the live UI state of the "+" creation menu: whether it is
// open, which pane it is open on, and which palette item is hovered.
//
// This is the single owner. The menu is open on at most one pane at a time —
// "the menu appears on exactly one pane, whichever is focused" (CLAUDE.md: the
// menu is the deliberate focused-pane exception to the guiding rule). Every
// transition goes through a method here and nothing else assigns the fields, so
// a newly-added gesture-ending path cannot forget to clear the menu and leave it
// stranded open on a stale, now-unfocused pane. That stranding is exactly the
// historical "menus disappearing / menu on the wrong pane" bug class, which came
// from the open/closed flag being written from ~14 scattered sites with no owner.
//
// State is the LIVE state only. The menu's persistence ACROSS a portal descent
// (so ascending returns you exactly as you left) rides in pane.Frame.MenuOpen, a
// saved snapshot restored on ascent; record it with OpenOn at descent and reopen
// with Open on ascent.
//
// The package is plain Go (no js/wasm build tag) so the whole state machine is
// unit-tested headlessly — the orchestration, not just a leaf predicate.
package menu

// noHover is the hovered-item index when nothing is hovered.
const noHover = -1

// State is the single owner of the + menu's live state. The zero value is not
// valid (hover would read as item 0); construct with New.
type State struct {
	open   bool
	paneID string
	hover  int
}

// New returns a closed menu with no hovered item.
func New() State { return State{hover: noHover} }

// IsOpen reports whether the menu is open on any pane.
func (s *State) IsOpen() bool { return s.open }

// OpenOn reports whether the menu is open on paneID specifically. This is the
// guard every per-pane site uses (draw the palette, route a click into it), and
// the snapshot recorded into a portal frame at descent.
func (s *State) OpenOn(paneID string) bool { return s.open && s.paneID == paneID }

// PaneID returns the pane the menu is open on, or "" when the menu is closed.
// Closed always reports "" so a stale id can never resolve to a pane.
func (s *State) PaneID() string {
	if !s.open {
		return ""
	}
	return s.paneID
}

// Hover returns the hovered palette-item index, or -1 when nothing is hovered.
func (s *State) Hover() int { return s.hover }

// Open opens the menu on paneID. Idempotent on the target; always resets hover,
// since the pointer has not yet moved over the freshly-shown palette.
func (s *State) Open(paneID string) {
	s.open = true
	s.paneID = paneID
	s.hover = noHover
}

// Close closes the menu and clears the remembered pane and hover, so nothing
// downstream can read a stale pane id off a closed menu.
func (s *State) Close() {
	s.open = false
	s.paneID = ""
	s.hover = noHover
}

// Toggle is the corner "+" click: close if already open on paneID, otherwise
// open on paneID (moving the menu there if it was open elsewhere). Returns the
// resulting open state.
func (s *State) Toggle(paneID string) bool {
	if s.OpenOn(paneID) {
		s.Close()
		return false
	}
	s.Open(paneID)
	return true
}

// SetHover sets the hovered palette-item index (-1 for none) and reports whether
// it changed, so the caller can redraw only on a real change. A no-op while the
// menu is closed.
func (s *State) SetHover(i int) (changed bool) {
	if !s.open || s.hover == i {
		return false
	}
	s.hover = i
	return true
}

// SyncFocus enforces "the menu belongs only to the focused pane": if the menu is
// open on a pane that is no longer the focused one, it closes. Call this whenever
// focus moves. (In practice the menu can only be open on the focused pane, so a
// focus move away from it always closes it — but expressing the rule this way
// means the menu never has to be closed defensively from every focus path.)
func (s *State) SyncFocus(focusedPaneID string) {
	if s.open && s.paneID != focusedPaneID {
		s.Close()
	}
}
