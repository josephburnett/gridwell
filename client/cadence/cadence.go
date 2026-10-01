// Package cadence owns the client's fixed waits, away from syscall/js so a
// test can hold them, and each debounced wait's mode beside it. client/retry
// owns how long the client waits before asking again.
package cadence

import "github.com/josephburnett/gridwell/client/debounce"

// Milliseconds, untyped so one name serves an int and a float64.
const (
	// TextSaveMs coalesces typing.
	TextSaveMs = 600

	// TextSaveMode is Throttle: a paragraph reaches the server while it is
	// still being typed.
	TextSaveMode = debounce.Throttle

	// URLAddressMs is how long a live page's address rests before it is
	// written to its row.
	URLAddressMs = 600

	// URLAddressMode is Settle: an address in the middle of a redirect chain
	// is not where the user went.
	URLAddressMode = debounce.Settle

	// URLUpdateMs coalesces wheel and keystroke bursts into one
	// history.replaceState.
	URLUpdateMs = 150

	// URLUpdateMode is Settle: a burst has no resting place to describe until it stops.
	URLUpdateMode = debounce.Settle

	// FramingSaveMs is longer than URLUpdateMs, so a continuous pan or zoom
	// persists only its resting state.
	FramingSaveMs = 600

	// FramingSaveMode is Settle: a fixed window would write the middle of a gesture.
	FramingSaveMode = debounce.Settle

	// WorkspaceSaveMs is the layout persister's window.
	WorkspaceSaveMs = 500

	// WorkspaceSaveMode is Settle: an arrangement is written once it stops changing.
	WorkspaceSaveMode = debounce.Settle

	// ShellMirrorMs is how often a live surface another pane shows is
	// snapshotted into the shared preview cache (pane.Mirrored). It owns the
	// mirror cadence: apps/desktop/src/main/capture.ts keeps a drift-linted
	// copy for the url pump.
	ShellMirrorMs = 250

	// ShellMirrorMode is Throttle: a terminal that repaints without pause is
	// still mirrored once per window.
	ShellMirrorMode = debounce.Throttle

	// TraceFadeMs is how long the ascent-trace outline takes to fade out.
	TraceFadeMs = 2000

	// TraceFlushMs is how long a trace record waits for company before the
	// client posts it; see client/trace.NeedFlush.
	TraceFlushMs = 1000

	// TraceFlushMode is Throttle: a settle would hold records on the client
	// for as long as something kept going wrong.
	TraceFlushMode = debounce.Throttle
)
