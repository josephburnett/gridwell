// Package textedit holds the text editor's decisions, separated from the
// scheduler in client/wasm so `go test` executes them.
package textedit

// CanvasHiddenByOverlay is the one owner of whether the canvas paints or the
// DOM overlay covers it. The overlay is a singleton over the focused
// descended pane, so a preview is never covered, and it is cleared during a
// pane switch, so the canvas paints until the new blob lands.
func CanvasHiddenByOverlay(isDescended, isFocused, overlayReady bool) bool {
	return isDescended && isFocused && overlayReady
}

// TextareaSyncInput is the snapshot DecideTextareaSync reads.
type TextareaSyncInput struct {
	FocusedTileID string
	LastTileID    string
	CurrentValue  string
	BlobCached    bool
	BlobContent   string
	// PendingEdit reports typing in flight on LastTileID. It only decides
	// whether a same-tile sync may rewrite the DOM value, since rewriting
	// mid-typing would jump the cursor.
	PendingEdit bool
}

// TextareaSyncDecision keeps the textarea coherent with the focused tile. The
// caller writes Value when SetValue, and stores NewLastTileID always, even
// when SetValue is false, so a delayed blob fetch's second pass sees the same
// tile.
//
// A browser leaves the caret of written bytes at their end and scrolls to it,
// a scroll nobody made. So a write never moves the view: KeepView puts back the
// caret and scroll a same-tile rewrite found, and any other write puts the
// caret at the top, where the pane's own scroll applies.
type TextareaSyncDecision struct {
	SetValue      bool
	Value         string
	KeepView      bool
	NewLastTileID string
}

// DecideTextareaSync drives the textarea singleton across focus shifts and
// async blob fetches. A different tile clears, seeded from the cache, so the
// previous buffer cannot leak. On the same tile a pending edit is the
// authority for unsaved keystrokes; without one the buffer follows the cached
// body, which is how a foreign writer's edit reaches an open editor.
func DecideTextareaSync(in TextareaSyncInput) TextareaSyncDecision {
	if in.LastTileID != in.FocusedTileID {
		// Rebinding destroys nothing: every keystroke was mirrored into
		// LastTileID's cache entry, and the dirty sweep posts it.
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
			KeepView:      true,
			NewLastTileID: in.FocusedTileID,
		}
	}
	return TextareaSyncDecision{
		SetValue:      false,
		NewLastTileID: in.FocusedTileID,
	}
}
