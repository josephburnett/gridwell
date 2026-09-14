// Package cadence owns the client's fixed waits: how long the shim sits on a
// change before writing it out, how often it samples a live surface, how long a
// hint stays on screen. A duration written into a setTimeout is a guarantee no
// test can hold, so they live here, away from syscall/js. client/retry owns the
// other half, how long the client waits before asking again.
package cadence

// Milliseconds, the unit the JS timers take, and untyped, so one name serves a
// setTimeout's int and an interpolation's float64.
const (
	// TextSaveMs is the delay from the first keystroke since the last save to
	// the next save fire, so continuous typing saves at most once per interval.
	TextSaveMs = 600

	// URLUpdateMs coalesces wheel and keystroke bursts into one
	// history.replaceState, while staying short enough for a quick bookmark.
	URLUpdateMs = 150

	// FramingSaveMs is longer than URLUpdateMs, so a continuous pan or zoom
	// persists only its resting state.
	FramingSaveMs = 600

	// WorkspaceSaveMs is the layout persister's coalescing window: a reload
	// inside it loses at most that much arrangement.
	WorkspaceSaveMs = 500

	// ShellMirrorMs is how often a live shell is snapshotted into the shared
	// preview cache. Nothing arms it; it runs from boot for the app's life.
	ShellMirrorMs = 250

	// TraceFadeMs is how long the ascent-trace outline takes to fade out.
	TraceFadeMs = 2000
)
