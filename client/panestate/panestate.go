// Package panestate holds the per-pane, session-local client state for one
// pane: the current selection and the saved-ascent stack. None of it is
// persisted (the server owns durable state); it is the plain-data half of a
// pane's client-only state. (The rendered-mode caret died with the custom
// markdown engine, issue #218.)
//
// It exists as its own package so this data and its small amount of logic
// (the ascent stack) are unit-tested as plain Go, instead of living as a
// sprawl of parallel maps on the wasm App god-object where nothing about
// them can be tested. The wasm side embeds State in a per-pane struct that
// adds the native (js-coupled) live URL/shell handles; this package stays
// js-free.
package panestate

// Saved is one entry on the ascent stack: the parent viewport (Cx/Cy/Zoom) saved
// just before a descent, plus — when the descent originated inside a text tile —
// the text-descent context to reinstall on the matching ascent so a single
// ascent lands back in the doc. Anchor/Path are set only for an embed descent
// that re-anchored the pane onto another grid. Since #218 the one writer of
// the text-descent fields is descendEphemeral's stack-a-visit-over-a-shell
// stash (the #208 residual class); readers restore via restoreStashedDescent.
type Saved struct {
	Cx          float64  `json:"cx"`
	Cy          float64  `json:"cy"`
	Zoom        float64  `json:"zoom"`
	TextFocus   string   `json:"text_focus,omitempty"`
	TextMode    string   `json:"text_mode,omitempty"`
	TextScrollX float64  `json:"text_scroll_x,omitempty"`
	TextScrollY float64  `json:"text_scroll_y,omitempty"`
	Anchor      string   `json:"anchor,omitempty"`
	Path        []string `json:"path,omitempty"`
}

// State is the plain-data per-pane client state.
type State struct {
	// Selected is the selected tile id in this pane ("" = nothing selected).
	Selected string
	// NOTE: there is deliberately no per-pane "unsaved edit" mark. That fact
	// is tile-scoped and lives on the content-store entry (cache.DirtyContent)
	// — a pane-scoped copy was reset by the pane's next descent, stranding the
	// edit it described (the 2026-07-18 incident's sibling bug).
	ascent []Saved
}

// New returns an empty pane state.
func New() State { return State{} }

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

// PaneGrid names one pane and the grid it currently shows — the input to
// the framing-ownership rule.
type PaneGrid struct {
	PaneID string
	GridID string
}

// FramingWriters applies the ONE-ACTIVE-SURFACE rule to grid framing
// (owner decision 2026-08-13, extending #249 from url/shell tiles to
// grids): when several panes show the SAME grid, exactly one — the
// focused pane — owns the framing writeback; the others are passive
// viewers. A pane that shares its grid with nobody always writes (it is
// trivially the active surface). Focusing a shared grid's sibling IS the
// takeover, exactly as opening a live tile elsewhere takes the stream.
// Before this rule every sibling wrote its own rect-derived values each
// settle tick, so the persisted framing thrashed nondeterministically.
func FramingWriters(panes []PaneGrid, focusedID string) map[string]bool {
	byGrid := map[string]int{}
	for _, p := range panes {
		byGrid[p.GridID]++
	}
	out := map[string]bool{}
	for _, p := range panes {
		out[p.PaneID] = byGrid[p.GridID] == 1 || p.PaneID == focusedID
	}
	return out
}
