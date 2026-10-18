package markdown

// WrapRawLine reproduces the editing <textarea>'s wrapping, which the canvas
// painter must match or the text reflows when pane focus moves. Chromium's UA
// stylesheet gives a textarea `white-space: pre-wrap; overflow-wrap:
// break-word`, so for a monospace font:
//   - a soft break happens before a word whose end would pass cols;
//   - spaces at a soft break hang past the edge, staying on the earlier row;
//   - a word wider than a whole row is char-broken at the column limit, but
//     only once it has a row to itself.
//
// cols <= 0 disables wrapping, for a caller with no measurable width.
func WrapRawLine(line string, cols int) []string {
	if cols <= 0 {
		return []string{line}
	}
	r := []rune(line)
	wordStart := func(j int) bool { return r[j] != ' ' && r[j-1] == ' ' }
	var out []string
	pos := 0
	for len(r)-pos > cols {
		window := pos + cols
		cut := -1
		switch {
		case wordStart(window):
			cut = window
		case r[window] == ' ':
			// The spaces hang, so the row extends to the next word start or
			// swallows the rest.
			for j := window + 1; j < len(r); j++ {
				if wordStart(j) {
					cut = j
					break
				}
			}
		default:
			// Move the whole word down when it started after pos; a word
			// owning the row from its start is char-broken.
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

// WrapRawText flattens WrapRawLine over every source line, one row per slot
// the canvas paints.
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
