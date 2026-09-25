// Package cadence owns the client's fixed waits. A duration written into a
// setTimeout is a guarantee no test can hold, so they live here, away from
// syscall/js. client/retry owns the other half, how long the client waits
// before asking again.
//
// A wait that arms a debounce declares its mode here too, beside the sentence
// that asks for it: how a burst lands on the window is half of what the wait
// promises, and the shim has no mode of its own to pass.
package cadence

import "github.com/josephburnett/gridwell/client/debounce"

// Milliseconds, the unit the JS timers take, and untyped, so one name serves a
// setTimeout's int and an interpolation's float64.
const (
	// TextSaveMs coalesces typing: continuous keystrokes save at most once
	// per interval.
	TextSaveMs = 600

	// TextSaveMode is Throttle because that interval is a rate and not a
	// silence: a paragraph reaches the server while it is still being typed.
	TextSaveMode = debounce.Throttle

	// URLUpdateMs coalesces wheel and keystroke bursts into one
	// history.replaceState, while staying short enough for a quick bookmark.
	URLUpdateMs = 150

	// URLUpdateMode is Settle, because one is the whole promise: a burst has
	// no resting place to describe until it stops.
	URLUpdateMode = debounce.Settle

	// FramingSaveMs is longer than URLUpdateMs, so a continuous pan or zoom
	// persists only its resting state.
	FramingSaveMs = 600

	// FramingSaveMode is Settle, which is what makes that sentence true: a
	// window fixed at the first arm writes the middle of the gesture, and
	// each of those is a store write, an event back to this client, and a
	// repaint.
	FramingSaveMode = debounce.Settle

	// WorkspaceSaveMs is the layout persister's window.
	WorkspaceSaveMs = 500

	// WorkspaceSaveMode is Settle: an arrangement is written once it stops
	// changing, so a reload taken while a divider is still moving loses the
	// drag in flight and nothing else.
	WorkspaceSaveMode = debounce.Settle

	// ShellMirrorMs is how often a live surface is snapshotted into the shared
	// preview cache; nothing arms it, it runs from boot for the app's life. It
	// owns the mirror cadence: apps/desktop/src/main/capture.ts keeps a
	// drift-linted copy.
	ShellMirrorMs = 250

	// TraceFadeMs is how long the ascent-trace outline takes to fade out.
	TraceFadeMs = 2000

	// TraceFlushMs is how long a trace record waits for company before the
	// client posts it. client/trace.NeedFlush reads it beside the other half
	// of that decision, the batch a flush will not grow past.
	TraceFlushMs = 1000

	// TraceFlushMode is Throttle: the records worth having are made while
	// something is going wrong continuously, and a settle would hold them on
	// the client for as long as that lasted.
	TraceFlushMode = debounce.Throttle
)
