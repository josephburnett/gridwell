// Package panestate holds the per-pane, session-local client state that is
// NOT the pane's place: today, the selection. None of it is persisted (the
// server owns durable state); it is the plain-data half of a pane's
// client-only state. (The rendered-mode caret died with the custom markdown
// engine, issue #218.)
//
// The saved-ascent stack that used to live here is GONE (S8, one place): the
// viewport a pane will ascend onto is the frame it left, held by the pane's
// own place stack (client/pane, place.go) — one owner, no second copy to
// pop out of sync with the pane. The framing-writer rule moved to
// pane.FramingWriters for the same reason: it is a projection of where the
// panes are.
//
// It exists as its own package so this data lives somewhere unit-tested as
// plain Go, instead of as a sprawl of parallel maps on the wasm App
// god-object. The wasm side embeds State in a per-pane struct that adds the
// native (js-coupled) live URL/shell handles; this package stays js-free.
package panestate

// State is the plain-data per-pane client state.
type State struct {
	// Selected is the selected tile id in this pane ("" = nothing selected).
	Selected string
	// NOTE: there is deliberately no per-pane "unsaved edit" mark. That fact
	// is tile-scoped and lives on the content-store entry (cache.DirtyContent)
	// — a pane-scoped copy was reset by the pane's next descent, stranding the
	// edit it described (the 2026-07-18 incident's sibling bug).
}

// New returns an empty pane state.
func New() State { return State{} }
