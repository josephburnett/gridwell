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

// ShouldDebouncedSaveFire reports whether a debounced text-save timer,
// when it fires, should actually persist the textarea's contents.
//
// The save scheduler queues a timer on every keystroke; by the time it
// fires the world may have moved on, so three things must still hold:
//
//   - hasFocusedPane — there is a focused pane to read from.
//   - textFocusTileID != "" && isTextMode — that pane is editing a text
//     tile in raw-text mode. (Rendered-mode edits persist on their own path,
//     saveFileFromCache, gated on the dirty mark; a non-text or unfocused
//     pane has nothing to save.)
//   - lastTextareaTileID == textFocusTileID — the shared textarea
//     singleton is still bound to the SAME tile the timer was scheduled
//     for. A save scheduled while editing tile A must NOT fire after the
//     user has descended into tile B: it would read A's stale buffer out
//     of the singleton and persist it as B's content (the "new tile
//     contains the last edited tile's text" regression).
func ShouldDebouncedSaveFire(hasFocusedPane bool, textFocusTileID string, isTextMode bool, lastTextareaTileID string) bool {
	if !hasFocusedPane || textFocusTileID == "" || !isTextMode {
		return false
	}
	return lastTextareaTileID == textFocusTileID
}
