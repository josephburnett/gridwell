// Package panestate holds the per-pane session-local client state that is not
// the pane's place, today the selection. It is js-free so the data is unit
// tested as plain Go, not as parallel maps on the wasm App struct.
package panestate

type State struct {
	Selected string
	// There is deliberately no per-pane unsaved-edit mark: that fact is
	// tile-scoped, see cache.DirtyContent, and a pane-scoped copy is reset by
	// the pane's next descent, stranding the edit.
}

func New() State { return State{} }
