// Package panestate holds the per-pane, session-local client state that is
// not the pane's place: today, the selection. None of it is persisted; the
// server owns durable state.
//
// It is its own package so this data lives somewhere unit-tested as plain
// Go, instead of as parallel maps on the wasm App struct. The wasm side
// embeds State in a per-pane struct that adds the native live URL/shell
// handles; this package stays js-free.
package panestate

// State is the plain-data per-pane client state.
type State struct {
	// Selected is the selected tile id in this pane ("" = nothing selected).
	Selected string
	// There is deliberately no per-pane "unsaved edit" mark. That fact is
	// tile-scoped and lives on the cache entry (cache.DirtyContent); a
	// pane-scoped copy is reset by the pane's next descent and strands the
	// edit it describes.
}

// New returns an empty pane state.
func New() State { return State{} }
