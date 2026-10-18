// Package menu owns the + menu: what it shows, where it draws, and its live
// state. It is open on at most one pane, the focused one, and every transition
// goes through a State method, so a new gesture-ending path cannot strand it
// open on an unfocused pane. What survives a descent rides the place frame
// instead (pane.Frame.MenuOpen), recorded with OpenOn and restored with Open.
package menu

const noHover = -1

// State's zero value is invalid, because hover would read as item 0;
// construct with New.
type State struct {
	open     bool
	paneID   string
	hover    int
	expanded bool
}

func New() State { return State{hover: noHover} }

func (s *State) IsOpen() bool { return s.open }

// OpenOn is the snapshot a place frame records at a descent.
func (s *State) OpenOn(paneID string) bool { return s.open && s.paneID == paneID }

// PaneID reports "" when closed, so a stale id cannot resolve to a pane.
func (s *State) PaneID() string {
	if !s.open {
		return ""
	}
	return s.paneID
}

// Hover is the hovered menu-item index, or -1.
func (s *State) Hover() int { return s.hover }

// PluginsExpanded is live state rather than a preference, so every opening
// starts folded.
func (s *State) PluginsExpanded() bool { return s.expanded }

// TogglePlugins flips the door section and returns the new state. Hover is
// dropped, because the swatch list under the pointer has changed.
func (s *State) TogglePlugins() bool {
	s.expanded = !s.expanded
	s.hover = noHover
	return s.expanded
}

// Open resets hover and the fold: every opening starts folded.
func (s *State) Open(paneID string) {
	s.open = true
	s.paneID = paneID
	s.hover = noHover
	s.expanded = false
}

// Close clears the remembered pane, so nothing downstream reads a stale id.
func (s *State) Close() {
	s.open = false
	s.paneID = ""
	s.hover = noHover
	s.expanded = false
}

// Toggle is the + click, returning the resulting open state.
func (s *State) Toggle(paneID string) bool {
	if s.OpenOn(paneID) {
		s.Close()
		return false
	}
	s.Open(paneID)
	return true
}

// SetHover reports whether the index changed, so the caller redraws only on a
// real change.
func (s *State) SetHover(i int) (changed bool) {
	if !s.open || s.hover == i {
		return false
	}
	s.hover = i
	return true
}

// SyncFocus closes a menu open on a pane that is no longer focused, so no
// focus path has to close the menu itself.
func (s *State) SyncFocus(focusedPaneID string) {
	if s.open && s.paneID != focusedPaneID {
		s.Close()
	}
}

// TransferFocus reports whether focus changed and syncs the menu when it did.
// Every path that moves wasm focus calls it; calling it on no move is safe.
func (s *State) TransferFocus(prevID, newID string) bool {
	if prevID == newID {
		return false
	}
	s.SyncFocus(newID)
	return true
}
