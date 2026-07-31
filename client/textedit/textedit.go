// Package textedit holds the pure decision logic for the file-text
// editor's debounced auto-save, factored out of the wasm client so the
// load-bearing guards get real `go test` coverage (the save scheduler
// itself lives in client/wasm, build-tag-excluded from host tests).
package textedit

func clamp(off, n int) int {
	if off < 0 {
		return 0
	}
	if off > n {
		return n
	}
	return off
}

// CanvasHiddenByOverlay reports whether a text pane's canvas render should be
// suppressed because a DOM overlay is actively covering it with content —
// the editing textarea in text mode, the rendered-HTML div in rendered mode
// (issue #218; mode-agnostic since both are overlays on the focused pane).
// All three conditions must hold:
//
//   - isDescended: the function is called for a DESCENDED pane (not a preview
//     node). An overlay covers only the focused descended pane; a preview
//     node is never covered, so this returns false for all previews.
//   - isFocused: this is the specific pane the singleton overlay sits over.
//   - overlayReady: the overlay currently holds the tile's content. During a
//     pane switch the overlay is cleared before the new tile's blob arrives;
//     the canvas must paint until the overlay actually covers content — otherwise
//     the user sees a blank pane while the blob loads (the loading-race blank).
//
// This is the single owner of the "does canvas paint or overlay covers?"
// decision, preventing two bugs:
//
// A (wrong-size preview): removing this predicate from drawMarkdownNode forces
// the preview path to always paint, regardless of sibling-pane state.
// B (blank pane): textareaReady=false keeps the canvas painting during the
// pane-switch loading race instead of suppressing it.
//
// The wasm caller invokes CanvasHiddenByOverlay(true, ...) in drawMarkdownInPane
// (the descended path). drawMarkdownNode (the preview path) does not call it at
// all — previews are never covered by the overlay.
func CanvasHiddenByOverlay(isDescended, isFocused, overlayReady bool) bool {
	return isDescended && isFocused && overlayReady
}

// ShouldDebouncedSaveFire is GONE. It guarded the debounced save's read of
// the singleton textarea — proving the DOM still belonged to the tile being
// saved. Saves no longer read the DOM at all: keystrokes mirror into the
// tile-scoped content store, and the debounce sweeps DIRTY ENTRIES by tile id
// (App.flushDirtyText), which needs no focus/binding proof and cannot strand
// an edit whose pane moved on. A guard at one call site was also the trap
// that caused the 2026-07-18 stomp: the ascent/collapse flushes never got it.

// TextareaSyncInput is the snapshot a wasm caller hands to
// DecideTextareaSync: which tile the focus is on, which tile the
// singleton textarea is currently bound to (from the App's tracking
// field), what the textarea is showing right now, and whether the
// focused tile's blob is cached and what its content is.
type TextareaSyncInput struct {
	FocusedTileID string
	LastTileID    string
	CurrentValue  string
	BlobCached    bool
	BlobContent   string
	// PendingEdit reports that LastTileID's content-store entry carries an
	// unsaved edit (typing in flight). The textarea mirrors every keystroke
	// into the content store, so on a rebind nothing is lost — the entry is
	// tile-scoped and the debounced sweep posts it. PendingEdit only decides
	// whether a SAME-tile sync may rewrite the DOM value (rewriting mid-typing
	// would jump the cursor).
	PendingEdit bool
}

// TextareaSyncDecision is what to do to keep the textarea coherent with
// the current focused tile. Apply by, in order:
//
//  1. If SetValue is true, write Value into the textarea.
//  2. Always store NewLastTileID into the App's tracking field — even
//     when SetValue is false, so a delayed blob fetch's second pass
//     sees the correct "same tile" state.
type TextareaSyncDecision struct {
	SetValue      bool
	Value         string
	NewLastTileID string
}

// DecideTextareaSync drives the textarea singleton's value across focus
// shifts and async blob fetches. The rules:
//
//   - Different tile than last bound: clear immediately so the previous
//     tile's buffer doesn't leak (this is the bug where "new text tile
//     has the last edited tile's content by default"). If the blob is
//     already cached, seed with it in the same step so the user doesn't
//     see a flash of empty.
//   - Same tile, PENDING edit: in-progress typing — preserve. The buffer is
//     the one authority for unsaved keystrokes.
//   - Same tile, NO pending edit: the buffer is a mere view of the cached
//     body — follow it. This is what makes a foreign writer's edit (another
//     device, same tile) appear in an open editor instead of lingering as a
//     stale buffer: the arriving event evicts the stale body, the refetch
//     lands the foreign bytes, and this sync repaints the textarea. Before
//     this rule the clean buffer was preserved merely for being non-empty —
//     a fourth un-owned copy of the content, and the ascent flush then saved
//     those stale bytes back (the foreign-edit stomp).
//
// LastTileID always advances to FocusedTileID so the blob-fetch
// onComplete sees the "same tile, value-driven" branch rather than
// re-clearing the user's freshly-typed content.
func DecideTextareaSync(in TextareaSyncInput) TextareaSyncDecision {
	if in.LastTileID != in.FocusedTileID {
		// Rebinding away from LastTileID destroys nothing: every keystroke
		// was mirrored into its tile-scoped content-store entry, and the
		// dirty sweep posts that entry no matter where focus went.
		val := ""
		if in.BlobCached {
			val = in.BlobContent
		}
		return TextareaSyncDecision{
			SetValue:      true,
			Value:         val,
			NewLastTileID: in.FocusedTileID,
		}
	}
	if !in.PendingEdit && in.BlobCached && in.CurrentValue != in.BlobContent {
		return TextareaSyncDecision{
			SetValue:      true,
			Value:         in.BlobContent,
			NewLastTileID: in.FocusedTileID,
		}
	}
	return TextareaSyncDecision{
		SetValue:      false,
		NewLastTileID: in.FocusedTileID,
	}
}
