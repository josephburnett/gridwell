// Package textedit holds the pure decision logic for the text editor's
// debounced auto-save, factored out of the wasm client so the load-bearing
// guards get real `go test` coverage. The save scheduler itself lives in
// client/wasm, build-tag-excluded from host tests.
package textedit

// CanvasHiddenByOverlay reports whether a text pane's canvas render should be
// suppressed because a DOM overlay is actively covering it with content —
// the editing textarea in text mode, the rendered-HTML div in rendered mode.
// Both are overlays on the focused pane, so the rule is mode-agnostic. All
// three conditions must hold:
//
//   - isDescended: the function is called for a DESCENDED pane (not a preview
//     node). An overlay covers only the focused descended pane; a preview
//     node is never covered, so this returns false for all previews.
//   - isFocused: this is the specific pane the singleton overlay sits over.
//   - overlayReady: the overlay currently holds the tile's content. During a
//     pane switch the overlay is cleared before the new tile's blob arrives;
//     the canvas paints until the overlay actually covers content, or the user
//     sees a blank pane while the blob loads.
//
// This is the single owner of the "does the canvas paint, or does the overlay
// cover it?" decision.
//
// The wasm caller invokes CanvasHiddenByOverlay(true, ...) in drawMarkdownInPane
// (the descended path). drawMarkdownNode (the preview path) does not call it at
// all — previews are never covered by the overlay.
func CanvasHiddenByOverlay(isDescended, isFocused, overlayReady bool) bool {
	return isDescended && isFocused && overlayReady
}

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
	// PendingEdit reports that LastTileID's cache entry carries an unsaved
	// edit (typing in flight). The textarea mirrors every keystroke into the
	// cache, so a rebind loses nothing: the entry is tile-scoped and the
	// debounced sweep posts it. PendingEdit only decides whether a same-tile
	// sync may rewrite the DOM value, since rewriting mid-typing would jump
	// the cursor.
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
//     tile's buffer doesn't leak into the new tile. If the blob is already
//     cached, seed with it in the same step so the user doesn't see a flash
//     of empty.
//   - Same tile, pending edit: in-progress typing, so preserve it. The buffer
//     is the one authority for unsaved keystrokes.
//   - Same tile, no pending edit: the buffer is a mere view of the cached
//     body, so follow it. That is what makes a foreign writer's edit (another
//     device, same tile) appear in an open editor instead of lingering as a
//     stale buffer: the arriving event evicts the stale body, the refetch
//     lands the foreign bytes, and this sync repaints the textarea.
//
// LastTileID always advances to FocusedTileID so the blob-fetch
// onComplete sees the "same tile, value-driven" branch rather than
// re-clearing the user's freshly-typed content.
func DecideTextareaSync(in TextareaSyncInput) TextareaSyncDecision {
	if in.LastTileID != in.FocusedTileID {
		// Rebinding away from LastTileID destroys nothing: every keystroke
		// was mirrored into its tile-scoped cache entry, and the dirty sweep
		// posts that entry no matter where focus went.
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
