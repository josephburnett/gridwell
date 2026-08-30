// Package textcursor converts between a character offset into a text
// buffer and a (row, col) coordinate, and back. It is the pure core of
// the textarea cursor math used by URL save/restore — extracted so the
// off-by-one, EOF, empty-line, and CRLF hazards get real test coverage; the
// wasm callers are build-tag-excluded from `go test`.
//
// Coordinates are 0-indexed. A '\n' ends a line; '\r' is an ordinary
// character (CRLF therefore leaves the '\r' as the last column of the
// line, matching the browser textarea's own counting).
package textcursor

// OffsetFromRowCol walks src counting newlines until row, then adds col
// (clamped to that line's length). Negative row/col clamp to 0. A row
// past the end of the buffer returns len(src).
func OffsetFromRowCol(src string, row, col int) int {
	if row < 0 {
		row = 0
	}
	if col < 0 {
		col = 0
	}
	r := 0
	lineStart := 0
	for i := 0; i < len(src); i++ {
		if r == row {
			break
		}
		if src[i] == '\n' {
			r++
			lineStart = i + 1
		}
	}
	if r != row {
		// Past end of file — return end.
		return len(src)
	}
	// Find the end of this line.
	lineEnd := lineStart
	for lineEnd < len(src) && src[lineEnd] != '\n' {
		lineEnd++
	}
	if lineStart+col > lineEnd {
		return lineEnd
	}
	return lineStart + col
}

// RowColFromOffset returns the (row, col) of a character offset into
// src. The offset is clamped to [0, len(src)]. It is the inverse of
// OffsetFromRowCol on in-bounds, line-length-respecting inputs.
func RowColFromOffset(src string, off int) (row, col int) {
	if off > len(src) {
		off = len(src)
	}
	if off < 0 {
		off = 0
	}
	for i := 0; i < off; i++ {
		if src[i] == '\n' {
			row++
			col = 0
		} else {
			col++
		}
	}
	return row, col
}
