package markdown

// WrapRawLine wraps one raw source line into the visual rows the editing
// <textarea> produces (issue #216: the canvas painter must agree with the
// textarea, or the text visibly reflows when pane focus moves). Chromium's
// UA stylesheet gives a textarea `white-space: pre-wrap; overflow-wrap:
// break-word`, which means, for a monospace face where every rune is one
// column:
//   - soft breaks happen before a word whose end would pass `cols`;
//   - spaces at a soft break HANG past the edge (they stay on the earlier
//     row and are simply invisible off the end);
//   - a word wider than a whole row is char-broken at the column limit —
//     but only once it has a row to itself (break-word, not break-all).
//
// cols <= 0 disables wrapping (the caller had no measurable width).
func WrapRawLine(line string, cols int) []string {
	if cols <= 0 {
		return []string{line}
	}
	r := []rune(line)
	wordStart := func(j int) bool { return r[j] != ' ' && r[j-1] == ' ' }
	var out []string
	pos := 0
	for len(r)-pos > cols {
		// window is the first column that no longer fits; classify what the
		// boundary landed on.
		window := pos + cols
		cut := -1
		switch {
		case wordStart(window):
			// Exactly at a word start: break before the word.
			cut = window
		case r[window] == ' ':
			// Inside (or entering) a space run: the spaces HANG — the row
			// extends to the next word start, or swallows the rest.
			for j := window + 1; j < len(r); j++ {
				if wordStart(j) {
					cut = j
					break
				}
			}
		default:
			// Inside a word: move the whole word down when it started after
			// pos; a word owning the row from its start is char-broken.
			cut = window
			for j := window - 1; j > pos; j-- {
				if wordStart(j) {
					cut = j
					break
				}
			}
		}
		if cut == -1 {
			break // only spaces remain past the window: one hanging row
		}
		out = append(out, string(r[pos:cut]))
		pos = cut
	}
	return append(out, string(r[pos:]))
}

// WrapRawText applies WrapRawLine to every newline-delimited source line,
// returning the flattened visual rows — what the canvas paints, one slot
// per row, exactly as many rows as the textarea shows.
func WrapRawText(src string, cols int) []string {
	var out []string
	start := 0
	for i := 0; i <= len(src); i++ {
		if i == len(src) || src[i] == '\n' {
			out = append(out, WrapRawLine(src[start:i], cols)...)
			start = i + 1
		}
	}
	return out
}
