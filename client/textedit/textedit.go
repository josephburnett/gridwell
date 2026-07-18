// Package textedit holds the pure decision logic for the file-text
// editor's debounced auto-save, factored out of the wasm client so the
// load-bearing guards get real `go test` coverage (the save scheduler
// itself lives in client/wasm, build-tag-excluded from host tests).
package textedit

import "unicode/utf8"

// InsertAt inserts ins into src at byte offset off and returns the new source
// plus the caret offset just past the insertion. off is clamped to [0, len].
// Used by the rendered-mode editor: a typed character (or "\n" for Enter) is
// spliced at the caret. Pure so the splice math is test-covered.
func InsertAt(src, ins string, off int) (string, int) {
	off = clamp(off, len(src))
	return src[:off] + ins + src[off:], off + len(ins)
}

// InsertParagraphBreak inserts a markdown paragraph break at byte offset off
// and returns the new source plus the caret just past the break. The break is
// normalized, not blindly spliced: the maximal run of '\n' touching the
// insertion point collapses to exactly "\n\n". Markdown renders three blank
// lines identically to one, so any extra newlines would be invisible source
// the caret still walks — repeated Enter would sink the caret ever lower with
// no rendered change. Normalizing makes Enter idempotent at a paragraph
// boundary: it moves the caret past the break instead of accumulating.
func InsertParagraphBreak(src string, off int) (string, int) {
	off = clamp(off, len(src))
	l := off
	for l > 0 && src[l-1] == '\n' {
		l--
	}
	r := off
	for r < len(src) && src[r] == '\n' {
		r++
	}
	return src[:l] + "\n\n" + src[r:], l + 2
}

// DeleteBefore removes the rune ending at byte offset off (the Backspace
// action) and returns the new source plus the caret at the deleted rune's
// start. A no-op (returns src, off) when off <= 0. off is clamped to [0, len].
func DeleteBefore(src string, off int) (string, int) {
	off = clamp(off, len(src))
	if off == 0 {
		return src, 0
	}
	_, sz := utf8.DecodeLastRuneInString(src[:off])
	return src[:off-sz] + src[off:], off - sz
}

// DeleteAt removes the rune starting at byte offset off (the Delete action) and
// returns the new source; the caret stays at off. A no-op when off is at/after
// the end. off is clamped to [0, len].
func DeleteAt(src string, off int) string {
	off = clamp(off, len(src))
	if off >= len(src) {
		return src
	}
	_, sz := utf8.DecodeRuneInString(src[off:])
	return src[:off] + src[off+sz:]
}

// MoveLeft returns the byte offset one rune before off (clamped at 0).
func MoveLeft(src string, off int) int {
	off = clamp(off, len(src))
	if off == 0 {
		return 0
	}
	_, sz := utf8.DecodeLastRuneInString(src[:off])
	return off - sz
}

// MoveRight returns the byte offset one rune after off (clamped at len).
func MoveRight(src string, off int) int {
	off = clamp(off, len(src))
	if off >= len(src) {
		return len(src)
	}
	_, sz := utf8.DecodeRuneInString(src[off:])
	return off + sz
}

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
// suppressed because the textarea overlay is actively covering it with content.
// All four conditions must hold:
//
//   - isDescended: the function is called for a DESCENDED pane (not a preview
//     node). The textarea overlay covers only the focused descended pane; a
//     preview node is never covered, so this returns false for all previews.
//   - isFocused: this is the specific pane the single textarea sits over. The
//     textarea covers exactly one pane at a time (the tree's focused pane).
//   - isTextMode: the pane is in raw-text mode. The textarea is only shown in
//     text mode; in rendered mode the canvas paints the markdown directly.
//   - textareaReady: the textarea currently holds the tile's content. During a
//     pane switch the textarea is cleared before the new tile's blob arrives;
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
func CanvasHiddenByOverlay(isDescended, isFocused, isTextMode, textareaReady bool) bool {
	return isDescended && isFocused && isTextMode && textareaReady
}

// ShouldDebouncedSaveFire is GONE. It guarded the debounced save's read of
// the singleton textarea — proving the DOM still belonged to the tile being
// saved. Saves no longer read the DOM at all: keystrokes mirror into the
// tile-scoped content store, and the debounce sweeps DIRTY ENTRIES by tile id
// (App.flushDirtyText), which needs no focus/binding proof and cannot strand
// an edit whose pane moved on. A guard at one call site was also the trap
// that caused the 2026-07-18 stomp: the ascent/collapse flushes never got it.
