// Package textedit holds the text editor's decisions, separated from the
// scheduler in client/wasm so `go test` executes them.
package textedit

// CanvasHiddenByOverlay is the one owner of whether the canvas paints or the
// DOM overlay, a singleton over the focused descended pane, covers it.
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
	// PendingEdit reports typing in flight on LastTileID, which a same-tile
	// sync must not rewrite.
	PendingEdit bool
}

// TextareaSyncDecision keeps the textarea coherent with the focused tile. The
// caller stores NewLastTileID always, even when SetValue is false. A write
// never moves the view: KeepView restores a same-tile rewrite's caret and
// scroll, and any other write puts the caret at the top.
type TextareaSyncDecision struct {
	SetValue      bool
	Value         string
	KeepView      bool
	NewLastTileID string
}

// DecideTextareaSync drives the textarea singleton across focus shifts and
// async blob fetches. On the same tile a pending edit is the authority;
// without one the buffer follows the cached body, which is how a foreign
// writer's edit reaches an open editor.
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
