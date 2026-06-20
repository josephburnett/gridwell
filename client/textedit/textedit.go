// Package textedit holds the pure decision logic for the file-text
// editor's debounced auto-save, factored out of the wasm client so the
// load-bearing guards get real `go test` coverage (the save scheduler
// itself lives in client/wasm, build-tag-excluded from host tests).
package textedit

// ShouldDebouncedSaveFire reports whether a debounced text-save timer,
// when it fires, should actually persist the textarea's contents.
//
// The save scheduler queues a timer on every keystroke; by the time it
// fires the world may have moved on, so three things must still hold:
//
//   - hasFocusedPane — there is a focused pane to read from.
//   - textFocusTileID != 0 && isTextMode — that pane is editing a text
//     tile in raw-text mode (rendered mode is read-only; a non-text or
//     unfocused pane has nothing to save).
//   - lastTextareaTileID == textFocusTileID — the shared textarea
//     singleton is still bound to the SAME tile the timer was scheduled
//     for. A save scheduled while editing tile A must NOT fire after the
//     user has descended into tile B: it would read A's stale buffer out
//     of the singleton and persist it as B's content (the "new tile
//     contains the last edited tile's text" regression).
func ShouldDebouncedSaveFire(hasFocusedPane bool, textFocusTileID int64, isTextMode bool, lastTextareaTileID int64) bool {
	if !hasFocusedPane || textFocusTileID == 0 || !isTextMode {
		return false
	}
	return lastTextareaTileID == textFocusTileID
}
