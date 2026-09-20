package pane

// SessionState is one pane's session-local state that is not its place, today
// the selection. It is js-free so the data is unit tested as plain Go, not as
// parallel maps on the wasm App struct.
type SessionState struct {
	Selected string
	// There is deliberately no per-pane unsaved-edit mark: that fact is
	// tile-scoped, see cache.DirtyContent, and a pane-scoped copy is reset by
	// the pane's next descent, stranding the edit.
}

func NewSessionState() SessionState { return SessionState{} }
